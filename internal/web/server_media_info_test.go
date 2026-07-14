package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"emby-migrator/internal/config"
	"emby-migrator/internal/dockerengine"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
	"emby-migrator/internal/storage"
)

func TestAutomaticContainerManagementRestartsAfterApplyFailure(t *testing.T) {
	dataDir := t.TempDir()
	manager := job.NewManager()
	server := NewServer(config.Config{DataDir: dataDir}, manager, exporter.NewService(dataDir))
	docker := &recordingDockerController{running: true}
	server.docker = docker
	j := manager.Create("media-db-apply")

	_, err := server.applyMediaDatabaseJob(j, "missing-export", filepath.Join(t.TempDir(), "library.db"), false, appServerProfileSettings{
		BaseURL:             "http://127.0.0.1:8096",
		APIKey:              "test-key",
		ContainerName:       "emby",
		AutoManageContainer: true,
	})
	if err == nil {
		t.Fatal("missing export should make the apply fail")
	}
	if docker.stopCalls != 1 || docker.startCalls != 1 {
		t.Fatalf("container calls after apply failure: stop=%d start=%d", docker.stopCalls, docker.startCalls)
	}
	if !docker.running {
		t.Fatal("container should be running after failure recovery")
	}
}

func TestAutomaticContainerManagementRestartsWhenStopReturnsError(t *testing.T) {
	dataDir := t.TempDir()
	manager := job.NewManager()
	server := NewServer(config.Config{DataDir: dataDir}, manager, exporter.NewService(dataDir))
	docker := &recordingDockerController{running: true, stopErr: errors.New("connection interrupted after stop")}
	server.docker = docker
	j := manager.Create("media-db-apply")

	_, err := server.applyMediaDatabaseJob(j, "unused", filepath.Join(t.TempDir(), "library.db"), false, appServerProfileSettings{
		BaseURL:             "http://127.0.0.1:8096",
		APIKey:              "test-key",
		ContainerName:       "emby",
		AutoManageContainer: true,
	})
	if err == nil {
		t.Fatal("stop error should fail the task")
	}
	if docker.stopCalls != 1 || docker.startCalls != 1 || !docker.running {
		t.Fatalf("container was not recovered after ambiguous stop: %#v", docker)
	}
}

type recordingDockerController struct {
	running    bool
	stopCalls  int
	startCalls int
	stopErr    error
}

func (c *recordingDockerController) Ping(context.Context) error { return nil }

func (c *recordingDockerController) Inspect(context.Context, string) (dockerengine.Container, error) {
	return dockerengine.Container{Name: "emby", Running: c.running}, nil
}

func (c *recordingDockerController) Stop(context.Context, string, int) error {
	c.stopCalls++
	c.running = false
	return c.stopErr
}

func (c *recordingDockerController) Start(context.Context, string) error {
	c.startCalls++
	c.running = true
	return nil
}

func (c *recordingDockerController) WaitRunning(context.Context, string) error { return nil }
func (c *recordingDockerController) WaitStopped(context.Context, string) error { return nil }

func TestMediaDatabaseApplyEndpointCompletesAndRejectsUnsafePaths(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	databaseRoot := filepath.Join(t.TempDir(), "emby-dbs")
	service := exporter.NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "fixture")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest.json"), storage.Manifest{}); err != nil {
		t.Fatal(err)
	}
	plan := exporter.MediaDatabasePlan{
		SchemaVersion:     1,
		SourceEmbyVersion: "4.9.5.0",
		TargetEmbyVersion: "4.9.5.0",
		Items: []exporter.MediaDatabasePlanItem{
			{
				StableKey:    "fixture-movie",
				SourceName:   "Fixture Movie",
				TargetItemID: "200",
				TargetName:   "Fixture Movie",
				MediaSource: map[string]any{
					"RunTimeTicks": int64(120000000), "Bitrate": 1000000, "Size": 1234, "Container": "mkv",
				},
				MediaStreams: []map[string]any{{"Index": 0, "Type": "Video", "Codec": "h264", "Width": 640, "Height": 360}},
				Chapters:     []map[string]any{{"ChapterIndex": 0, "StartPositionTicks": int64(0), "Name": "Opening"}},
			},
		},
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "media-db-plan-fixture.json"), plan); err != nil {
		t.Fatal(err)
	}

	databaseDir := filepath.Join(databaseRoot, "target", "data")
	if err := os.MkdirAll(databaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(databaseDir, "library.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE MediaItems (Id INTEGER PRIMARY KEY, Name TEXT, RunTimeTicks BIGINT, TotalBitrate INT, Width INT, Height INT, Size INT, Container TEXT)`,
		`CREATE TABLE MediaStreams2 (ItemId INT, StreamIndex INT, StreamType INT, Codec TEXT, Language TEXT, ChannelLayout TEXT, Profile TEXT, AspectRatio TEXT, Path TEXT, IsInterlaced BIT, BitRate INT, Channels INT, SampleRate INT, IsDefault BIT, IsForced BIT, IsHearingImpaired BIT, IsExternal BIT, Height INT, Width INT, AverageFrameRate FLOAT, RealFrameRate FLOAT, Level FLOAT, PixelFormat TEXT, BitDepth INT, IsAnamorphic BIT, RefFrames INT, Rotation INT, CodecTag TEXT, Comment TEXT, NalLengthSize TEXT, Title TEXT, TimeBase TEXT, ColorPrimaries TEXT, ColorSpace TEXT, ColorTransfer TEXT, Extradata TEXT, AttachmentSize INT, MimeType TEXT, ExtendedVideoType INT, ExtendedVideoSubtype INT, PRIMARY KEY(ItemId, StreamIndex))`,
		`CREATE TABLE Chapters3 (ItemId INT, ChapterIndex INT, StartPositionTicks BIGINT, Name TEXT, ImagePath TEXT, ImageDateModified INT, MarkerType INT, PRIMARY KEY(ItemId, ChapterIndex))`,
		`INSERT INTO MediaItems (Id, Name) VALUES (200, 'Fixture Movie')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	manager := job.NewManager()
	app := httptest.NewServer(NewServer(config.Config{
		DataDir:          dataDir,
		ConfigDir:        t.TempDir(),
		EmbyDatabaseRoot: databaseRoot,
		AdminPassword:    "pw",
		SessionSecret:    "test-session-secret",
	}, manager, service).Routes())
	defer app.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client()
	client.Jar = jar
	postJSON(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, nil, http.StatusOK)

	var created map[string]any
	postJSON(t, client, app.URL+"/api/jobs/media-info/apply", map[string]any{
		"exportPath": "fixture", "databasePath": "target/data/library.db", "overwrite": true,
	}, &created, http.StatusAccepted)
	if got := stringField(t, created, "type"); got != "media-db-apply" {
		t.Fatalf("created job type = %q, want media-db-apply", got)
	}
	completed := waitForJob(t, client, app.URL, stringField(t, created, "id"))
	result, ok := completed["result"].(map[string]any)
	if !ok {
		t.Fatalf("completed job result = %#v", completed["result"])
	}
	applyResult, ok := result["result"].(map[string]any)
	if !ok || applyResult["itemsApplied"] != float64(1) {
		t.Fatalf("apply result = %#v", result)
	}
	backupPath, _ := applyResult["backupPath"].(string)
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("endpoint did not create backup: %v", err)
	}
	appliedDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer appliedDB.Close()
	var streams, chapters int
	if err := appliedDB.QueryRow("SELECT COUNT(*) FROM MediaStreams2 WHERE ItemId=200").Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if err := appliedDB.QueryRow("SELECT COUNT(*) FROM Chapters3 WHERE ItemId=200").Scan(&chapters); err != nil {
		t.Fatal(err)
	}
	if streams != 1 || chapters != 1 {
		t.Fatalf("persisted media rows: streams=%d chapters=%d, want 1/1", streams, chapters)
	}

	jobsBeforeRejectedRequests := len(manager.List())
	rejections := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "database path escapes configured root",
			payload: map[string]any{"exportPath": "fixture", "databasePath": "../library.db"},
		},
		{
			name:    "missing export path",
			payload: map[string]any{"databasePath": "target/data/library.db"},
		},
		{
			name:    "missing database path",
			payload: map[string]any{"exportPath": "fixture"},
		},
		{
			name:    "absolute database path",
			payload: map[string]any{"exportPath": "fixture", "databasePath": databasePath},
		},
		{
			name:    "database filename is not library db",
			payload: map[string]any{"exportPath": "fixture", "databasePath": "target/data/other.db"},
		},
		{
			name:    "database path does not exist",
			payload: map[string]any{"exportPath": "fixture", "databasePath": "missing/data/library.db"},
		},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			postJSON(t, client, app.URL+"/api/jobs/media-info/apply", tt.payload, nil, http.StatusBadRequest)
		})
	}
	if got := len(manager.List()); got != jobsBeforeRejectedRequests {
		t.Fatalf("rejected requests queued jobs: got %d jobs, want %d", got, jobsBeforeRejectedRequests)
	}
}

func TestExportMediaInfoRequestDefaultAndExplicitValues(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "missing defaults false", payload: `{}`, want: false},
		{name: "explicit true", payload: `{"includeMediaInfo":true}`, want: true},
		{name: "explicit false", payload: `{"includeMediaInfo":false}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req exportRequest
			if err := json.Unmarshal([]byte(tt.payload), &req); err != nil {
				t.Fatal(err)
			}
			if got := req.mediaInfoEnabled(); got != tt.want {
				t.Fatalf("mediaInfoEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveEmbyDatabasePathStaysWithinConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	databaseDir := filepath.Join(root, "emby49", "data")
	if err := os.MkdirAll(databaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(databaseDir, "library.db")
	if err := os.WriteFile(databasePath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveEmbyDatabasePath(root, filepath.Join("emby49", "data", "library.db"))
	if err != nil {
		t.Fatalf("valid database path returned error: %v", err)
	}
	if resolved != databasePath {
		t.Fatalf("resolved path = %q, want %q", resolved, databasePath)
	}

	for _, invalid := range []string{"../library.db", databasePath, "emby49/data/other.db"} {
		if _, err := resolveEmbyDatabasePath(root, invalid); err == nil {
			t.Fatalf("invalid path %q should be rejected", invalid)
		}
	}
	if _, err := resolveEmbyDatabasePath("", "emby49/data/library.db"); err == nil {
		t.Fatalf("empty database root should disable the feature")
	}
}

func TestImportMediaInfoRequestDefaultAndExplicitValues(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "missing defaults false", payload: `{}`, want: false},
		{name: "explicit true", payload: `{"importMediaInfo":true}`, want: true},
		{name: "explicit false", payload: `{"importMediaInfo":false}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req importRequest
			if err := json.Unmarshal([]byte(tt.payload), &req); err != nil {
				t.Fatal(err)
			}
			if got := req.mediaInfoEnabled(); got != tt.want {
				t.Fatalf("mediaInfoEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
