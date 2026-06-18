package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"emby-migrator/internal/config"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
)

func TestAPISmokeExportImportWithSameSourceTargetProfile(t *testing.T) {
	state := &mockEmbyState{}
	embyServer := newProfileSmokeEmbyServer(t, state, profileSmokeServerOptions{
		ExpectedToken: "same-secret-token",
		ServerName:    "Same Profile Emby",
		ServerID:      "same-profile-server",
		Version:       "4.8.11",
	})
	defer embyServer.Close()

	dataDir := t.TempDir()
	app := newProfileSmokeApp(t, dataDir)
	defer app.Close()
	client := authenticatedClient(t, app)

	var saved appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "same-profile",
		"name":    "Same Profile",
		"baseUrl": embyServer.URL,
		"apiKey":  "same-secret-token",
		"role":    "source-target",
	}, &saved, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/profiles/select", map[string]string{
		"currentSource": "same-profile",
		"currentTarget": "same-profile",
	}, &saved, http.StatusOK)
	if saved.CurrentSource != "same-profile" || saved.CurrentTarget != "same-profile" {
		t.Fatalf("same profile should be selected for source and target: %#v", saved)
	}

	exportPath := startProfileExport(t, client, app.URL, "same-profile")
	startProfilePrecheck(t, client, app.URL, "same-profile", exportPath)
	if got := state.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("same-server precheck should not write to Emby: %s", got)
	}

	report := startProfileImport(t, client, app.URL, "same-profile", exportPath)
	target := objectField(t, report, "target")
	if got := stringField(t, target, "serverId"); got != "same-profile-server" {
		t.Fatalf("same-server import target id = %q, want same-profile-server", got)
	}
	if got := state.writeCounts(); got != "updates=1 itemImages=1 peopleImages=1" {
		t.Fatalf("same-server import did not use the selected profile connection: %s", got)
	}
}

func TestAPISmokeExportImportWithDistinctSavedProfiles(t *testing.T) {
	sourceState := &mockEmbyState{}
	sourceEmby := newProfileSmokeEmbyServer(t, sourceState, profileSmokeServerOptions{
		ExpectedToken: "source-secret-token",
		ServerName:    "Source Profile Emby",
		ServerID:      "source-profile-server",
		Version:       "4.8.11",
	})
	defer sourceEmby.Close()
	targetState := &mockEmbyState{}
	targetEmby := newProfileSmokeEmbyServer(t, targetState, profileSmokeServerOptions{
		ExpectedToken: "target-secret-token",
		ServerName:    "Target Profile Emby",
		ServerID:      "target-profile-server",
		Version:       "4.9.5",
	})
	defer targetEmby.Close()

	dataDir := t.TempDir()
	app := newProfileSmokeApp(t, dataDir)
	defer app.Close()
	client := authenticatedClient(t, app)

	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "source-profile",
		"name":    "Source Profile",
		"baseUrl": sourceEmby.URL,
		"apiKey":  "source-secret-token",
		"role":    "source",
	}, nil, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "target-profile",
		"name":    "Target Profile",
		"baseUrl": targetEmby.URL,
		"apiKey":  "target-secret-token",
		"role":    "target",
	}, nil, http.StatusOK)

	var selected appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/profiles/select", map[string]string{
		"currentSource": "source-profile",
		"currentTarget": "target-profile",
	}, &selected, http.StatusOK)
	if selected.CurrentSource != "source-profile" || selected.CurrentTarget != "target-profile" {
		t.Fatalf("profile selection = %q/%q, want source-profile/target-profile", selected.CurrentSource, selected.CurrentTarget)
	}

	var sourceConnection map[string]any
	postJSON(t, client, app.URL+"/api/connection/test", map[string]string{
		"profileId": "source-profile",
	}, &sourceConnection, http.StatusOK)
	if objectField(t, sourceConnection, "server")["Id"] != "source-profile-server" {
		t.Fatalf("source profile connection used wrong server: %#v", sourceConnection)
	}

	exportPath := startProfileExport(t, client, app.URL, "source-profile")
	if got := sourceState.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("export should not write to source profile: %s", got)
	}
	if got := targetState.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("export should not touch target profile: %s", got)
	}

	startProfilePrecheck(t, client, app.URL, "target-profile", exportPath)
	if got := targetState.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("target precheck should not write: %s", got)
	}

	report := startProfileImport(t, client, app.URL, "target-profile", exportPath)
	target := objectField(t, report, "target")
	if got := stringField(t, target, "serverId"); got != "target-profile-server" {
		t.Fatalf("cross-server import target id = %q, want target-profile-server", got)
	}
	if got := sourceState.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("cross-server import should not write to source profile: %s", got)
	}
	if got := targetState.writeCounts(); got != "updates=1 itemImages=1 peopleImages=1" {
		t.Fatalf("cross-server import did not write through target profile: %s", got)
	}
}

func newProfileSmokeApp(t *testing.T, dataDir string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(NewServer(
		config.Config{
			DataDir:       dataDir,
			ConfigDir:     t.TempDir(),
			Version:       "profile-smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(dataDir),
	).Routes())
}

func startProfileExport(t *testing.T, client *http.Client, appURL, sourceProfileID string) string {
	t.Helper()
	var exportCreate map[string]any
	postJSON(t, client, appURL+"/api/jobs/export", map[string]any{
		"sourceProfileId":     sourceProfileID,
		"libraryIds":          []string{"lib-movies"},
		"includePeopleImages": true,
		"imageTypes":          []string{"Primary", "Logo"},
	}, &exportCreate, http.StatusAccepted)
	exportJob := waitForJob(t, client, appURL, stringField(t, exportCreate, "id"))
	return stringField(t, objectField(t, exportJob, "result"), "path")
}

func startProfilePrecheck(t *testing.T, client *http.Client, appURL, targetProfileID, exportPath string) {
	t.Helper()
	var precheckCreate map[string]any
	postJSON(t, client, appURL+"/api/jobs/import/precheck", map[string]any{
		"targetProfileId":     targetProfileID,
		"exportPath":          filepath.Base(exportPath),
		"includePeopleImages": true,
		"imageTypes":          []string{"Logo"},
	}, &precheckCreate, http.StatusAccepted)
	precheckJob := waitForJob(t, client, appURL, stringField(t, precheckCreate, "id"))
	if got := stringField(t, precheckJob, "type"); got != "import-precheck" {
		t.Fatalf("precheck job type = %q, want import-precheck", got)
	}
	report := objectField(t, objectField(t, precheckJob, "result"), "report")
	if got := boolField(t, report, "dryRun"); !got {
		t.Fatalf("precheck report dryRun = false, want true")
	}
}

func startProfileImport(t *testing.T, client *http.Client, appURL, targetProfileID, exportPath string) map[string]any {
	t.Helper()
	var importCreate map[string]any
	postJSON(t, client, appURL+"/api/jobs/import", map[string]any{
		"targetProfileId":     targetProfileID,
		"exportPath":          filepath.Base(exportPath),
		"includePeopleImages": true,
		"imageTypes":          []string{"Logo"},
	}, &importCreate, http.StatusAccepted)
	importJob := waitForJob(t, client, appURL, stringField(t, importCreate, "id"))
	report := objectField(t, objectField(t, importJob, "result"), "report")
	summary := objectField(t, report, "summary")
	if intField(t, summary, "metadataUpdated") != 1 {
		t.Fatalf("metadataUpdated summary = %#v, want 1", summary)
	}
	if intField(t, summary, "itemImagesPushed") != 1 {
		t.Fatalf("itemImagesPushed summary = %#v, want 1", summary)
	}
	if intField(t, summary, "peopleImages") != 1 {
		t.Fatalf("peopleImages summary = %#v, want 1", summary)
	}
	return report
}

type profileSmokeServerOptions struct {
	ExpectedToken string
	ServerName    string
	ServerID      string
	Version       string
}

func newProfileSmokeEmbyServer(t *testing.T, state *mockEmbyState, options profileSmokeServerOptions) *httptest.Server {
	t.Helper()
	if strings.TrimSpace(options.ExpectedToken) == "" {
		options.ExpectedToken = "test-key"
	}
	if strings.TrimSpace(options.ServerName) == "" {
		options.ServerName = "Mock Emby"
	}
	if strings.TrimSpace(options.ServerID) == "" {
		options.ServerID = "mock"
	}
	if strings.TrimSpace(options.Version) == "" {
		options.Version = "4.8.11"
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != options.ExpectedToken {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeSmokeJSON(w, map[string]any{
				"ServerName": options.ServerName,
				"Version":    options.Version,
				"Id":         options.ServerID,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			handleSmokeItems(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/Persons":
			writeSmokeJSON(w, map[string]any{
				"Items":            []map[string]any{targetPerson()},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/old-movie-1/Images":
			writeSmokeJSON(w, []map[string]any{
				{"ImageType": "Primary"},
				{"ImageType": "Logo"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/old-movie-1/Images/Primary":
			writeSmokeImage(w, "primary-image")
		case r.Method == http.MethodGet && r.URL.Path == "/Items/old-movie-1/Images/Logo":
			writeSmokeImage(w, "logo-image")
		case r.Method == http.MethodGet && r.URL.Path == "/Persons/Keanu Reeves":
			writeSmokeJSON(w, targetPerson())
		case r.Method == http.MethodGet && r.URL.Path == "/Persons/Keanu Reeves/Images/Primary":
			writeSmokeImage(w, "person-image")
		case r.Method == http.MethodPost && r.URL.Path == "/Items/new-movie-1":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.updateBodies = append(state.updateBodies, body)
			state.mu.Unlock()
			writeSmokeJSON(w, map[string]any{"ok": true})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/Items/new-movie-1/Images/"):
			if body, _ := io.ReadAll(r.Body); len(bytes.TrimSpace(body)) == 0 {
				http.Error(w, "empty image body", http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.itemImageUploads = append(state.itemImageUploads, r.URL.Path)
			state.mu.Unlock()
			writeSmokeJSON(w, map[string]any{"ok": true})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/6384/Images/Primary":
			if body, _ := io.ReadAll(r.Body); len(bytes.TrimSpace(body)) == 0 {
				http.Error(w, "empty person image body", http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.personImageUploads = append(state.personImageUploads, r.URL.Path)
			state.mu.Unlock()
			writeSmokeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
}
