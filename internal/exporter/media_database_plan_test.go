package exporter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-migrator/internal/emby"
	"emby-migrator/internal/storage"
)

func TestLatestMediaDatabasePlanPathUsesModificationTime(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "media-db-plan-z-server.json")
	newer := filepath.Join(dir, "media-db-plan-a-server.json")
	for _, path := range []string{older, newer} {
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.Local)
	if err := os.Chtimes(older, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, err := latestMediaDatabasePlanPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != newer {
		t.Fatalf("latestMediaDatabasePlanPath = %q, want %q", got, newer)
	}
}

func TestVerifyMediaDatabasePlanReadsBackStreamsAndChapters(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "fixture")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest.json"), storage.Manifest{}); err != nil {
		t.Fatal(err)
	}
	plan := MediaDatabasePlan{
		SchemaVersion: 1,
		Items: []MediaDatabasePlanItem{{
			TargetItemID: "200",
			TargetName:   "Big Buck Bunny",
			MediaStreams: []map[string]any{{"Index": 0, "Type": "Video", "Codec": "h264"}},
			Chapters:     []map[string]any{{"ChapterIndex": 0, "StartPositionTicks": int64(0), "Name": "Opening"}},
		}},
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "media-db-plan-fixture.json"), plan); err != nil {
		t.Fatal(err)
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items" || r.URL.Query().Get("Ids") != "200" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Items": []map[string]any{{
				"Id":           "200",
				"Name":         "Big Buck Bunny",
				"Type":         "Movie",
				"MediaStreams": []map[string]any{{"Index": 0, "Type": "Video", "Codec": "h264", "Width": 640}},
				"Chapters":     []map[string]any{{"ChapterIndex": 0, "StartPositionTicks": 0, "Name": "Opening"}},
			}},
			"TotalRecordCount": 1,
		})
	}))
	defer mock.Close()

	result, err := service.VerifyMediaDatabasePlan(context.Background(), "fixture", emby.Connection{BaseURL: mock.URL, APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items != 1 || result.Streams != 1 || result.Chapters != 1 {
		t.Fatalf("verify result = %#v", result)
	}
}
