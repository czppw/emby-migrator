package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"emby-migrator/internal/config"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
)

func TestVersionCheckReportsNewStableReleaseAndCaches(t *testing.T) {
	requests := 0
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeJSON(w, http.StatusOK, map[string]any{
			"tag_name":   "v1.2.0",
			"html_url":   "https://example.invalid/releases/v1.2.0",
			"draft":      false,
			"prerelease": false,
		})
	}))
	defer releases.Close()
	oldURL := latestReleaseAPIURL
	latestReleaseAPIURL = releases.URL
	defer func() { latestReleaseAPIURL = oldURL }()

	server := NewServer(config.Config{Version: "1.1.1"}, job.NewManager(), exporter.NewService(t.TempDir()))
	first := server.checkLatestVersion()
	second := server.checkLatestVersion()
	if !first.Checked || !first.UpdateAvailable || first.LatestVersion != "1.2.0" || first.ReleaseURL == "" {
		t.Fatalf("version check = %#v", first)
	}
	if second != first || requests != 1 {
		t.Fatalf("cached version check = %#v, requests = %d", second, requests)
	}
}

func TestVersionCheckFailureIsSilent(t *testing.T) {
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer releases.Close()
	oldURL := latestReleaseAPIURL
	oldWebURL := latestReleaseWebURL
	latestReleaseAPIURL = releases.URL
	latestReleaseWebURL = releases.URL
	defer func() {
		latestReleaseAPIURL = oldURL
		latestReleaseWebURL = oldWebURL
	}()

	server := NewServer(config.Config{Version: "1.1.1"}, job.NewManager(), exporter.NewService(t.TempDir()))
	result := server.checkLatestVersion()
	if result.Checked || result.UpdateAvailable || result.CurrentVersion != "1.1.1" {
		t.Fatalf("failure result = %#v", result)
	}
}

func TestVersionCheckFallsBackToLatestReleaseRedirect(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer api.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/czppw/emby-migrator/releases/tag/v1.3.0", http.StatusFound)
	}))
	defer web.Close()
	oldAPIURL := latestReleaseAPIURL
	oldWebURL := latestReleaseWebURL
	latestReleaseAPIURL = api.URL
	latestReleaseWebURL = web.URL + "/czppw/emby-migrator/releases/latest"
	defer func() {
		latestReleaseAPIURL = oldAPIURL
		latestReleaseWebURL = oldWebURL
	}()

	server := NewServer(config.Config{Version: "1.1.1"}, job.NewManager(), exporter.NewService(t.TempDir()))
	result := server.checkLatestVersion()
	if !result.Checked || !result.UpdateAvailable || result.LatestVersion != "1.3.0" {
		t.Fatalf("fallback result = %#v", result)
	}
}

func TestCompareVersionNumbers(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "v1.2.0", right: "1.1.9", want: 1},
		{left: "1.1.1", right: "v1.1.1", want: 0},
		{left: "1.1.0", right: "1.1.1", want: -1},
		{left: "1.2", right: "1.2.0", want: 0},
	}
	for _, test := range tests {
		if got := compareVersionNumbers(test.left, test.right); got != test.want {
			t.Fatalf("compareVersionNumbers(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}
