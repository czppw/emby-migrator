package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	versionCheckSuccessTTL = time.Hour
	versionCheckFailureTTL = 15 * time.Minute
)

var latestReleaseAPIURL = "https://api.github.com/repos/czppw/emby-migrator/releases/latest"
var latestReleaseWebURL = "https://github.com/czppw/emby-migrator/releases/latest"

type versionCheckCache struct {
	mu        sync.Mutex
	expiresAt time.Time
	result    versionCheckResponse
}

type versionCheckResponse struct {
	Checked         bool      `json:"checked"`
	CurrentVersion  string    `json:"currentVersion"`
	LatestVersion   string    `json:"latestVersion,omitempty"`
	UpdateAvailable bool      `json:"updateAvailable"`
	ReleaseURL      string    `json:"releaseUrl,omitempty"`
	CheckedAt       time.Time `json:"checkedAt,omitempty"`
}

type githubLatestRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (s *Server) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.checkLatestVersion())
}

func (s *Server) checkLatestVersion() versionCheckResponse {
	now := time.Now()
	s.versionCheck.mu.Lock()
	defer s.versionCheck.mu.Unlock()
	if now.Before(s.versionCheck.expiresAt) {
		return s.versionCheck.result
	}

	result := versionCheckResponse{CurrentVersion: strings.TrimSpace(s.cfg.Version), CheckedAt: now}
	release, err := fetchLatestReleaseAPI()
	if err != nil {
		release, err = fetchLatestReleaseRedirect()
	}
	if err == nil {
		result.Checked = true
		result.LatestVersion = normalizeVersionLabel(release.TagName)
		result.ReleaseURL = strings.TrimSpace(release.HTMLURL)
		result.UpdateAvailable = compareVersionNumbers(result.LatestVersion, result.CurrentVersion) > 0
	}
	if err != nil {
		result.CheckedAt = time.Time{}
		s.versionCheck.expiresAt = now.Add(versionCheckFailureTTL)
	} else {
		s.versionCheck.expiresAt = now.Add(versionCheckSuccessTTL)
	}
	s.versionCheck.result = result
	return result
}

func fetchLatestReleaseAPI() (githubLatestRelease, error) {
	request, err := http.NewRequest(http.MethodGet, latestReleaseAPIURL, nil)
	if err != nil {
		return githubLatestRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "emby-migrator-version-check")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return githubLatestRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return githubLatestRelease{}, fmt.Errorf("GitHub release API returned HTTP %d", response.StatusCode)
	}
	var release githubLatestRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return githubLatestRelease{}, err
	}
	if release.Draft || release.Prerelease || strings.TrimSpace(release.TagName) == "" {
		return githubLatestRelease{}, fmt.Errorf("GitHub release response did not contain a stable version")
	}
	return release, nil
}

func fetchLatestReleaseRedirect() (githubLatestRelease, error) {
	request, err := http.NewRequest(http.MethodHead, latestReleaseWebURL, nil)
	if err != nil {
		return githubLatestRelease{}, err
	}
	request.Header.Set("User-Agent", "emby-migrator-version-check")
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return githubLatestRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 300 || response.StatusCode >= 400 {
		return githubLatestRelease{}, fmt.Errorf("GitHub latest release redirect returned HTTP %d", response.StatusCode)
	}
	location := strings.TrimSpace(response.Header.Get("Location"))
	resolved, err := request.URL.Parse(location)
	if err != nil || resolved == nil {
		return githubLatestRelease{}, fmt.Errorf("GitHub latest release redirect is invalid")
	}
	const tagMarker = "/releases/tag/"
	index := strings.LastIndex(resolved.Path, tagMarker)
	if index < 0 {
		return githubLatestRelease{}, fmt.Errorf("GitHub latest release redirect is missing a tag")
	}
	tag := strings.Trim(strings.TrimSpace(resolved.Path[index+len(tagMarker):]), "/")
	if tag == "" {
		return githubLatestRelease{}, fmt.Errorf("GitHub latest release redirect tag is empty")
	}
	return githubLatestRelease{TagName: tag, HTMLURL: resolved.String()}, nil
}

func normalizeVersionLabel(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "v"), "V")
	return value
}

func compareVersionNumbers(left, right string) int {
	leftParts := numericVersionParts(left)
	rightParts := numericVersionParts(right)
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for index := 0; index < length; index++ {
		var leftValue, rightValue int
		if index < len(leftParts) {
			leftValue = leftParts[index]
		}
		if index < len(rightParts) {
			rightValue = rightParts[index]
		}
		if leftValue > rightValue {
			return 1
		}
		if leftValue < rightValue {
			return -1
		}
	}
	return 0
}

func numericVersionParts(value string) []int {
	value = normalizeVersionLabel(value)
	if index := strings.IndexAny(value, "-+"); index >= 0 {
		value = value[:index]
	}
	parts := strings.Split(value, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return nil
		}
		out = append(out, parsed)
	}
	return out
}
