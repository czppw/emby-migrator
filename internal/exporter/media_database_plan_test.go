package exporter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestBuildMediaDatabaseBindingRequiresServerIDAndBindsAllItems(t *testing.T) {
	items := []MediaDatabasePlanItem{
		{TargetItemID: "200", TargetName: "First Movie", SourceName: "First Movie"},
		{TargetItemID: "201", TargetName: "Second Movie", SourceName: "Second Movie"},
	}
	if _, err := buildMediaDatabaseBinding("", items); err == nil {
		t.Fatal("schema 2 binding should reject a missing target server id")
	}
	binding, err := buildMediaDatabaseBinding("target-server", items)
	if err != nil {
		t.Fatal(err)
	}
	if binding.TargetServerID != "target-server" || binding.SchemaIdentity == "" || binding.AnchorCount != 2 || len(binding.AnchorDigest) != 64 {
		t.Fatalf("unexpected database binding: %#v", binding)
	}
	changed := append([]MediaDatabasePlanItem(nil), items...)
	changed[1].TargetName = "Different Movie"
	changedBinding, err := buildMediaDatabaseBinding("target-server", changed)
	if err != nil {
		t.Fatal(err)
	}
	if binding.AnchorDigest == changedBinding.AnchorDigest {
		t.Fatal("changing a target anchor must change the database binding")
	}
}

func TestPreflightMediaDatabaseTargetVerifiesSystemInfo(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	writeMediaDatabasePreflightPlan(t, service, "fixture", "target-server", "4.9.5.0")

	tests := []struct {
		name          string
		actualID      string
		actualVersion string
		wantError     string
	}{
		{name: "matching target", actualID: "target-server", actualVersion: "4.9.5.7"},
		{name: "different server", actualID: "other-server", actualVersion: "4.9.5.0", wantError: "ServerID mismatch"},
		{name: "different version series", actualID: "target-server", actualVersion: "4.8.11.0", wantError: "version series mismatch"},
		{name: "unsupported target version", actualID: "target-server", actualVersion: "4.10.0.0", wantError: "version series mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/System/Info" {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{
					"Id": tt.actualID, "Version": tt.actualVersion, "ServerName": "Actual Target",
				})
			}))
			defer mock.Close()

			result, err := service.PreflightMediaDatabaseTarget(context.Background(), "fixture", emby.Connection{BaseURL: mock.URL, APIKey: "test"})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("PreflightMediaDatabaseTarget error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.PlannedTarget.ServerID != "target-server" || result.ActualTarget.ServerID != "target-server" || result.ActualTarget.Version != tt.actualVersion {
				t.Fatalf("unexpected preflight result: %#v", result)
			}
		})
	}
}

func TestPreflightMediaDatabaseTargetRejectsPlanWithoutServerID(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "legacy")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest.json"), storage.Manifest{}); err != nil {
		t.Fatal(err)
	}
	plan := MediaDatabasePlan{
		SchemaVersion: 1, TargetEmbyVersion: "4.9.5.0",
		Items: []MediaDatabasePlanItem{{TargetItemID: "200", TargetName: "Movie"}},
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "media-db-plan-legacy.json"), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PreflightMediaDatabaseTarget(context.Background(), "legacy", emby.Connection{BaseURL: "http://127.0.0.1:1", APIKey: "test"}); err == nil || !strings.Contains(err.Error(), "does not contain a target Emby ServerID") {
		t.Fatalf("missing planned ServerID should fail before HTTP: %v", err)
	}
}

func writeMediaDatabasePreflightPlan(t *testing.T, service *Service, exportName, serverID, version string) {
	t.Helper()
	exportDir := filepath.Join(service.ExportsDir(), exportName)
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest.json"), storage.Manifest{}); err != nil {
		t.Fatal(err)
	}
	items := []MediaDatabasePlanItem{{TargetItemID: "200", TargetName: "Movie", SourceName: "Movie"}}
	binding, err := buildMediaDatabaseBinding(serverID, items)
	if err != nil {
		t.Fatal(err)
	}
	plan := MediaDatabasePlan{
		SchemaVersion: mediaDatabasePlanSchemaVersion, TargetEmbyVersion: version,
		Target:          ImportTarget{ServerID: serverID, ServerName: "Planned Target", Version: version},
		DatabaseBinding: binding, Items: items,
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "media-db-plan-"+serverID+".json"), plan); err != nil {
		t.Fatal(err)
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
