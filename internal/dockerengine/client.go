// Package dockerengine provides the small subset of the Docker Engine API
// needed to control a local container through the Docker Unix socket.
package dockerengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSocketPath   = "/var/run/docker.sock"
	DefaultPollInterval = 250 * time.Millisecond

	dockerHost       = "http://docker"
	maxErrorBodySize = 32 << 10
)

// ClientOptions customizes a Client. HTTPClient is primarily useful for
// tests; when nil, the client connects to SocketPath over a Unix socket.
type ClientOptions struct {
	SocketPath   string
	HTTPClient   *http.Client
	PollInterval time.Duration
}

// Client calls the Docker Engine HTTP API.
type Client struct {
	httpClient   *http.Client
	pollInterval time.Duration
}

// Mount is the host-to-container path mapping returned by container inspect.
type Mount struct {
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

// Container is the subset of container inspect data used by the migrator.
type Container struct {
	ID      string
	Name    string
	Status  string
	Running bool
	Mounts  []Mount
}

// ContainerInfo is an alias retained for callers that prefer an explicit
// inspect-result name.
type ContainerInfo = Container

// APIError reports a non-success response from Docker Engine.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("docker engine %s %s failed: HTTP %d %s", e.Method, e.Path, e.StatusCode, message)
}

// NewClient creates a client for socketPath. An empty path uses
// DefaultSocketPath.
func NewClient(socketPath string) *Client {
	return NewClientWithOptions(ClientOptions{SocketPath: socketPath})
}

// New is shorthand for NewClient.
func New(socketPath string) *Client {
	return NewClient(socketPath)
}

// NewClientWithOptions creates a client with optional transport and polling
// overrides.
func NewClientWithOptions(options ClientOptions) *Client {
	pollInterval := options.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		socketPath := strings.TrimSpace(options.SocketPath)
		if socketPath == "" {
			socketPath = DefaultSocketPath
		}
		dialer := &net.Dialer{}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		}
		httpClient = &http.Client{Transport: transport}
	}

	return &Client{
		httpClient:   httpClient,
		pollInterval: pollInterval,
	}
}

// Ping verifies that Docker Engine is reachable.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/_ping", false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// Inspect returns the name, state and mounts for a container. Docker prefixes
// inspected names with '/', which is removed from the returned Name.
func (c *Client) Inspect(ctx context.Context, container string) (Container, error) {
	endpoint, err := containerEndpoint(container, "/json")
	if err != nil {
		return Container{}, err
	}
	resp, err := c.do(ctx, http.MethodGet, endpoint, false)
	if err != nil {
		return Container{}, err
	}
	defer resp.Body.Close()

	var payload struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
		} `json:"State"`
		Mounts []Mount `json:"Mounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Container{}, fmt.Errorf("decode docker container inspect response: %w", err)
	}
	return Container{
		ID:      strings.TrimSpace(payload.ID),
		Name:    strings.TrimPrefix(payload.Name, "/"),
		Status:  payload.State.Status,
		Running: payload.State.Running,
		Mounts:  payload.Mounts,
	}, nil
}

// Stop asks Docker to stop a container, waiting timeoutSeconds before Docker
// forcefully terminates it. HTTP 304 means the container is already stopped
// and is treated as success.
func (c *Client) Stop(ctx context.Context, container string, timeoutSeconds int) error {
	endpoint, err := containerEndpoint(container, "/stop")
	if err != nil {
		return err
	}
	endpoint += "?" + url.Values{"t": {strconv.Itoa(timeoutSeconds)}}.Encode()
	return c.action(ctx, endpoint)
}

// Start starts a container. HTTP 304 means the container is already running
// and is treated as success.
func (c *Client) Start(ctx context.Context, container string) error {
	endpoint, err := containerEndpoint(container, "/start")
	if err != nil {
		return err
	}
	return c.action(ctx, endpoint)
}

// WaitRunning polls Inspect until the container is running or ctx ends.
func (c *Client) WaitRunning(ctx context.Context, container string) error {
	return c.wait(ctx, container, true)
}

// WaitStopped polls Inspect until the container is no longer running or ctx
// ends.
func (c *Client) WaitStopped(ctx context.Context, container string) error {
	return c.wait(ctx, container, false)
}

func (c *Client) action(ctx context.Context, endpoint string) error {
	resp, err := c.do(ctx, http.MethodPost, endpoint, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func (c *Client) wait(ctx context.Context, container string, running bool) error {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := c.Inspect(ctx, container)
		if err != nil {
			return err
		}
		if info.Running == running {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) do(ctx context.Context, method, endpoint string, allowNotModified bool) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, dockerHost+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create docker engine request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker engine %s %s request: %w", method, req.URL.Path, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 || allowNotModified && resp.StatusCode == http.StatusNotModified {
		return resp, nil
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	if readErr != nil {
		return nil, fmt.Errorf("read docker engine %s %s error response: %w", method, req.URL.Path, readErr)
	}
	return nil, &APIError{
		Method:     method,
		Path:       req.URL.Path,
		StatusCode: resp.StatusCode,
		Message:    string(body),
	}
}

func containerEndpoint(container, suffix string) (string, error) {
	container = strings.TrimSpace(container)
	container = strings.TrimPrefix(container, "/")
	if container == "" {
		return "", fmt.Errorf("docker container name or ID is required")
	}
	if strings.Contains(container, "/") {
		return "", fmt.Errorf("invalid docker container name or ID %q", container)
	}
	return "/containers/" + url.PathEscape(container) + suffix, nil
}
