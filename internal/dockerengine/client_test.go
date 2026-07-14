package dockerengine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPingAccepts2xx(t *testing.T) {
	client := testClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/_ping" {
			t.Fatalf("request = %s %s, want GET /_ping", req.Method, req.URL.Path)
		}
		return response(http.StatusOK, "OK"), nil
	})

	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
}

func TestInspectReturnsContainerFields(t *testing.T) {
	client := testClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.EscapedPath() != "/containers/emby%20server/json" {
			t.Fatalf("request = %s %s, want escaped inspect endpoint", req.Method, req.URL.EscapedPath())
		}
		return response(http.StatusOK, `{
			"Name":"/emby-server",
			"State":{"Status":"running","Running":true},
			"Mounts":[
				{"Type":"bind","Source":"/srv/emby","Destination":"/config","Mode":"rw"},
				{"Type":"volume","Source":"media","Destination":"/media"}
			]
		}`), nil
	})

	info, err := client.Inspect(context.Background(), "emby server")
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	if info.Name != "emby-server" || info.Status != "running" || !info.Running {
		t.Fatalf("unexpected container state: %#v", info)
	}
	if len(info.Mounts) != 2 || info.Mounts[0].Source != "/srv/emby" || info.Mounts[0].Destination != "/config" || info.Mounts[1].Destination != "/media" {
		t.Fatalf("unexpected mounts: %#v", info.Mounts)
	}
}

func TestStartAndStopAcceptNotModified(t *testing.T) {
	var requests atomic.Int32
	client := testClient(t, func(req *http.Request) (*http.Response, error) {
		switch requests.Add(1) {
		case 1:
			if req.Method != http.MethodPost || req.URL.Path != "/containers/emby/stop" || req.URL.Query().Get("t") != "17" {
				t.Fatalf("stop request = %s %s?%s", req.Method, req.URL.Path, req.URL.RawQuery)
			}
		case 2:
			if req.Method != http.MethodPost || req.URL.Path != "/containers/emby/start" {
				t.Fatalf("start request = %s %s", req.Method, req.URL.Path)
			}
		default:
			t.Fatalf("unexpected extra request")
		}
		return response(http.StatusNotModified, ""), nil
	})

	if err := client.Stop(context.Background(), "emby", 17); err != nil {
		t.Fatalf("Stop returned error for 304: %v", err)
	}
	if err := client.Start(context.Background(), "emby"); err != nil {
		t.Fatalf("Start returned error for 304: %v", err)
	}
}

func TestInspectReturns404APIError(t *testing.T) {
	client := testClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusNotFound, `{"message":"No such container: missing"}`), nil
	})

	_, err := client.Inspect(context.Background(), "missing")
	assertAPIError(t, err, http.StatusNotFound, "No such container")
}

func TestPingReturns500APIError(t *testing.T) {
	client := testClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusInternalServerError, "daemon failure"), nil
	})

	err := client.Ping(context.Background())
	assertAPIError(t, err, http.StatusInternalServerError, "daemon failure")
}

func TestWaitRunningAndWaitStoppedPoll(t *testing.T) {
	var runningCalls atomic.Int32
	runningClient := testClient(t, func(*http.Request) (*http.Response, error) {
		running := runningCalls.Add(1) >= 2
		if running {
			return inspectResponse(true, "running"), nil
		}
		return inspectResponse(false, "created"), nil
	})
	if err := runningClient.WaitRunning(context.Background(), "emby"); err != nil {
		t.Fatalf("WaitRunning returned error: %v", err)
	}
	if runningCalls.Load() != 2 {
		t.Fatalf("WaitRunning inspect calls = %d, want 2", runningCalls.Load())
	}

	var stoppedCalls atomic.Int32
	stoppedClient := testClient(t, func(*http.Request) (*http.Response, error) {
		running := stoppedCalls.Add(1) < 2
		if running {
			return inspectResponse(true, "running"), nil
		}
		return inspectResponse(false, "exited"), nil
	})
	if err := stoppedClient.WaitStopped(context.Background(), "emby"); err != nil {
		t.Fatalf("WaitStopped returned error: %v", err)
	}
	if stoppedCalls.Load() != 2 {
		t.Fatalf("WaitStopped inspect calls = %d, want 2", stoppedCalls.Load())
	}
}

func TestWaitRunningReturnsContextDeadlineExceeded(t *testing.T) {
	client := testClient(t, func(*http.Request) (*http.Response, error) {
		return inspectResponse(false, "created"), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()

	err := client.WaitRunning(ctx, "emby")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitRunning error = %v, want context deadline exceeded", err)
	}
}

func testClient(t *testing.T, fn roundTripFunc) *Client {
	t.Helper()
	return NewClientWithOptions(ClientOptions{
		HTTPClient:   &http.Client{Transport: fn},
		PollInterval: time.Millisecond,
	})
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func inspectResponse(running bool, status string) *http.Response {
	body := `{"Name":"/emby","State":{"Status":"` + status + `","Running":` + strconvBool(running) + `},"Mounts":[]}`
	return response(http.StatusOK, body)
}

func strconvBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func assertAPIError(t *testing.T, err error, status int, message string) {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != status || !strings.Contains(apiErr.Error(), message) {
		t.Fatalf("APIError = %#v (%v), want status %d containing %q", apiErr, apiErr, status, message)
	}
}
