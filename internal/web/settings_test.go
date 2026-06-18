package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-migrator/internal/config"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
)

func TestAppSettingsSaveMasksAPIKeyAndFeedsConnection(t *testing.T) {
	mockState := &mockEmbyState{}
	mockEmby := newMockEmbyServer(mockState)
	defer mockEmby.Close()

	configDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	defer app.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client()
	client.Jar = jar
	postJSON(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, nil, http.StatusOK)

	var saved appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": mockEmby.URL,
			"apiKey":  "test-key",
		},
		"defaults": map[string]any{
			"export": map[string]any{
				"concurrency":         8,
				"skipImages":          false,
				"includePeopleImages": true,
				"incremental":         true,
				"overwrite":           false,
				"imageTypes":          []string{"Primary", "Logo"},
			},
			"import": map[string]any{
				"concurrency":         6,
				"skipImages":          false,
				"includePeopleImages": true,
				"overwrite":           true,
				"imageTypes":          []string{"Primary"},
			},
		},
	}, &saved, http.StatusOK)
	if !saved.Configured || !saved.Connection.HasAPIKey || saved.Connection.BaseURL == "" {
		t.Fatalf("unexpected saved settings response: %#v", saved)
	}
	if strings.Contains(saved.Connection.APIKeyMasked, "test-key") {
		t.Fatalf("settings response leaked API key: %#v", saved)
	}
	if saved.Defaults.Export.Concurrency != 8 || saved.Defaults.Import.Concurrency != 6 {
		t.Fatalf("settings defaults not returned: %#v", saved.Defaults)
	}

	var disk appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Connection.APIKey != "test-key" {
		t.Fatalf("settings file did not persist API key")
	}
	if len(disk.Profiles) != 1 {
		t.Fatalf("saving default connection should create one server profile: %#v", disk)
	}
	if disk.CurrentSource == "" || disk.CurrentTarget == "" || disk.CurrentSource != disk.CurrentTarget {
		t.Fatalf("single saved server should be selected for source and target: %#v", disk)
	}
	profile, ok := findProfileByID(disk.Profiles, disk.CurrentSource)
	if !ok || profile.BaseURL != mockEmby.URL || profile.APIKey != "test-key" {
		t.Fatalf("saved server profile is incorrect: %#v", disk.Profiles)
	}

	var loaded appSettingsResponse
	getJSON(t, client, app.URL+"/api/settings/app", &loaded, http.StatusOK)
	if !loaded.Connection.HasAPIKey || strings.Contains(loaded.Connection.APIKeyMasked, "test-key") {
		t.Fatalf("loaded settings leaked or lost API key: %#v", loaded)
	}

	var connection map[string]any
	postJSON(t, client, app.URL+"/api/connection/test", map[string]any{
		"baseUrl": mockEmby.URL,
	}, &connection, http.StatusOK)
	if connection["ok"] != true {
		t.Fatalf("connection test did not use saved API key: %#v", connection)
	}

	var libraries map[string][]map[string]any
	postJSON(t, client, app.URL+"/api/libraries", map[string]any{
		"baseUrl": mockEmby.URL,
	}, &libraries, http.StatusOK)
	if len(libraries["libraries"]) != 1 {
		t.Fatalf("libraries did not use saved API key: %#v", libraries)
	}
}

func TestAppSettingsRejectsBlankAPIKeyForNewServer(t *testing.T) {
	configDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	defer app.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client()
	client.Jar = jar
	postJSON(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, nil, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": "http://127.0.0.1:8096",
		},
	}, nil, http.StatusBadRequest)
}

func TestAppProfilesRejectSecondServerWithoutAddressAPIKey(t *testing.T) {
	configDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	defer app.Close()

	client := authenticatedClient(t, app)
	var saved appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "source-one",
		"name":    "Source One",
		"baseUrl": "http://source.example:8096",
		"apiKey":  "shared-secret-token",
		"role":    "source-target",
	}, &saved, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "target-one",
		"name":    "Target One",
		"baseUrl": "http://target.example:8096",
		"role":    "source-target",
	}, &saved, http.StatusBadRequest)
	assertSettingsResponseDoesNotLeak(t, saved, "shared-secret-token")

	var disk appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Profiles) != 1 {
		t.Fatalf("blank key for a new address should not create target profile: %#v", disk.Profiles)
	}
}

func TestAppSettingsManualAPIKeyUpdatesSavedServer(t *testing.T) {
	configDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	defer app.Close()

	client := authenticatedClient(t, app)
	saveConnection := func(apiKey string) {
		t.Helper()
		postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
			"connection": map[string]string{
				"baseUrl": "http://source.example:8096",
				"apiKey":  apiKey,
			},
		}, nil, http.StatusOK)
	}

	saveConnection("old-secret-token")
	saveConnection("")
	var reused appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Connection.APIKey != "old-secret-token" {
		t.Fatalf("blank API key for the same address should reuse the saved key: %#v", reused.Connection)
	}
	saveConnection("new-secret-token")

	var disk appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Connection.APIKey != "new-secret-token" {
		t.Fatalf("default connection API key was not updated: %#v", disk.Connection)
	}
	profile, ok := findProfileByID(disk.Profiles, profileIDFromBaseURL("http://source.example:8096"))
	if !ok || profile.APIKey != "new-secret-token" {
		t.Fatalf("saved server profile API key was not updated: %#v", disk.Profiles)
	}
}

func TestAppSettingsSaveUpdatesExistingServerByAddress(t *testing.T) {
	configDir := t.TempDir()
	appServer := NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	)
	if err := appServer.saveAppSettings(appSettings{
		SchemaVersion: 2,
		Connection: appConnectionSettings{
			BaseURL: "http://source.example:8096",
			APIKey:  "old-secret-token",
		},
		Profiles: []appServerProfileSettings{
			{
				ID:      "source-one",
				Name:    "Source One",
				BaseURL: "http://source.example:8096",
				APIKey:  "old-secret-token",
				Role:    "source-target",
			},
		},
		CurrentSource: "source-one",
		CurrentTarget: "source-one",
	}); err != nil {
		t.Fatal(err)
	}

	app := httptest.NewServer(appServer.Routes())
	defer app.Close()

	client := authenticatedClient(t, app)
	var saved appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": "http://source.example:8096",
			"apiKey":  "new-secret-token",
		},
		"profile": map[string]string{
			"id":      profileIDFromBaseURL("http://source.example:8096"),
			"name":    "source.example:8096",
			"baseUrl": "http://source.example:8096",
			"role":    "source-target",
		},
	}, &saved, http.StatusOK)
	if len(saved.Profiles) != 1 || saved.Profiles[0].ID != "source-one" {
		t.Fatalf("same server address should update the existing address-book row: %#v", saved.Profiles)
	}

	var disk appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &disk); err != nil {
		t.Fatal(err)
	}
	profile, ok := findProfileByID(disk.Profiles, "source-one")
	if !ok || profile.APIKey != "new-secret-token" {
		t.Fatalf("existing same-address profile was not updated: %#v", disk.Profiles)
	}
}

func TestResolveProfileConnectionIgnoresStaleInputBaseURL(t *testing.T) {
	app := NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     t.TempDir(),
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	)
	settings := appSettings{
		SchemaVersion: 2,
		Profiles: []appServerProfileSettings{
			{ID: "source-one", Name: "Source", BaseURL: "http://source.example:8096", APIKey: "source-secret", Role: "source-target"},
			{ID: "target-one", Name: "Target", BaseURL: "http://target.example:8096", APIKey: "target-secret", Role: "source-target"},
		},
	}
	if err := app.saveAppSettings(settings); err != nil {
		t.Fatal(err)
	}

	connection, err := app.resolveEmbyConnection("http://source.example:8096", "", "target-one")
	if err != nil {
		t.Fatal(err)
	}
	if connection.BaseURL != "http://target.example:8096" || connection.APIKey != "target-secret" {
		t.Fatalf("profile connection mixed stale input and profile data: %#v", connection)
	}
}

func TestAppSettingsCanUpsertMultipleProfilesFromProfilePayload(t *testing.T) {
	configDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	defer app.Close()

	client := authenticatedClient(t, app)

	var saved appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": "http://source.example:8096",
			"apiKey":  "source-secret-token",
		},
		"profile": map[string]string{
			"id":      "source-one",
			"name":    "Source One",
			"baseUrl": "http://source.example:8096",
			"role":    "source",
		},
	}, &saved, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": "http://target.example:8096",
			"apiKey":  "target-secret-token",
		},
		"profile": map[string]string{
			"id":      "target-one",
			"name":    "Target One",
			"baseUrl": "http://target.example:8096",
			"role":    "target",
		},
	}, &saved, http.StatusOK)
	assertSettingsResponseDoesNotLeak(t, saved, "source-secret-token", "target-secret-token")
	if len(saved.Profiles) != 2 {
		t.Fatalf("saved profiles count = %d, want 2: %#v", len(saved.Profiles), saved.Profiles)
	}

	var listed appSettingsResponse
	getJSON(t, client, app.URL+"/api/settings/profiles", &listed, http.StatusOK)
	if len(listed.Profiles) != 2 {
		t.Fatalf("listed profiles count = %d, want 2: %#v", len(listed.Profiles), listed.Profiles)
	}

	var loaded appSettingsResponse
	getJSON(t, client, app.URL+"/api/settings/app", &loaded, http.StatusOK)
	if len(loaded.Profiles) != 2 {
		t.Fatalf("app settings profiles count = %d, want 2: %#v", len(loaded.Profiles), loaded.Profiles)
	}

	var disk appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if _, ok := findProfileByID(disk.Profiles, "source-one"); !ok {
		t.Fatalf("source profile missing on disk: %#v", disk.Profiles)
	}
	if _, ok := findProfileByID(disk.Profiles, "target-one"); !ok {
		t.Fatalf("target profile missing on disk: %#v", disk.Profiles)
	}
}

func TestAppSettingsProfilePayloadRequiresOwnAPIKeyForDifferentNewServer(t *testing.T) {
	configDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	defer app.Close()

	client := authenticatedClient(t, app)
	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": "http://source.example:8096",
			"apiKey":  "source-secret-token",
		},
		"profile": map[string]string{
			"id":      "source-one",
			"name":    "Source One",
			"baseUrl": "http://source.example:8096",
			"role":    "source",
		},
	}, nil, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": "http://target.example:8096",
		},
		"profile": map[string]string{
			"id":      "target-one",
			"name":    "Target One",
			"baseUrl": "http://target.example:8096",
			"role":    "target",
		},
	}, nil, http.StatusBadRequest)

	var listed appSettingsResponse
	getJSON(t, client, app.URL+"/api/settings/profiles", &listed, http.StatusOK)
	if len(listed.Profiles) != 1 {
		t.Fatalf("blank key for a new address should not create a second server profile: %#v", listed.Profiles)
	}

	postJSON(t, client, app.URL+"/api/settings/app", map[string]any{
		"connection": map[string]string{
			"baseUrl": "http://target.example:8096",
			"apiKey":  "target-secret-token",
		},
		"profile": map[string]string{
			"id":      "target-one",
			"name":    "Target One",
			"baseUrl": "http://target.example:8096",
			"role":    "target",
		},
	}, &listed, http.StatusOK)
	if len(listed.Profiles) != 2 {
		t.Fatalf("new address with its own key should create a second server profile: %#v", listed.Profiles)
	}

	var disk appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &disk); err != nil {
		t.Fatal(err)
	}
	target, ok := findProfileByID(disk.Profiles, "target-one")
	if !ok || target.APIKey != "target-secret-token" {
		t.Fatalf("target profile did not save its own key: %#v", disk.Profiles)
	}
}

func TestAppProfilesMaskAPIKeysAndDelete(t *testing.T) {
	configDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	defer app.Close()

	client := authenticatedClient(t, app)

	var saved appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "source-one",
		"name":    "Source One",
		"baseUrl": "http://source.example:8096",
		"apiKey":  "source-secret-token",
		"role":    "source",
	}, &saved, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "target-one",
		"name":    "Target One",
		"baseUrl": "http://target.example:8096",
		"apiKey":  "target-secret-token",
		"role":    "target",
	}, &saved, http.StatusOK)

	var listed appSettingsResponse
	getJSON(t, client, app.URL+"/api/settings/profiles", &listed, http.StatusOK)
	assertSettingsResponseDoesNotLeak(t, listed, "source-secret-token", "target-secret-token")
	if len(listed.Profiles) != 2 {
		t.Fatalf("profiles count = %d, want 2: %#v", len(listed.Profiles), listed.Profiles)
	}
	if listed.CurrentSource != "source-one" || listed.CurrentTarget != "source-one" {
		t.Fatalf("new server save should not change selected source/target automatically: %q/%q", listed.CurrentSource, listed.CurrentTarget)
	}
	for _, profile := range listed.Profiles {
		if profile.ID == "source-one" && (!profile.HasAPIKey || profile.APIKeyMasked == "") {
			t.Fatalf("source profile did not report masked key: %#v", profile)
		}
		if strings.Contains(profile.APIKeyMasked, "secret-token") {
			t.Fatalf("profile masked key leaked secret: %#v", profile)
		}
	}

	postJSON(t, client, app.URL+"/api/settings/profiles/select", map[string]string{
		"currentSource": "source-one",
		"currentTarget": "target-one",
	}, &listed, http.StatusOK)
	if listed.CurrentSource != "source-one" || listed.CurrentTarget != "target-one" {
		t.Fatalf("profile selection did not set source/target = %q/%q", listed.CurrentSource, listed.CurrentTarget)
	}

	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "source-one",
		"name":    "Source Updated",
		"baseUrl": "http://source.example:8096",
		"role":    "source",
	}, &saved, http.StatusOK)
	assertSettingsResponseDoesNotLeak(t, saved, "source-secret-token", "target-secret-token")

	var disk appSettings
	if err := readJSONFile(filepath.Join(configDir, appSettingsFileName), &disk); err != nil {
		t.Fatal(err)
	}
	source, ok := findProfileByID(disk.Profiles, "source-one")
	if !ok || source.APIKey != "source-secret-token" || source.Name != "Source Updated" {
		t.Fatalf("profile update did not preserve api key/name: %#v", disk.Profiles)
	}

	deleteJSON(t, client, app.URL+"/api/settings/profiles/source-one", &listed, http.StatusOK)
	assertSettingsResponseDoesNotLeak(t, listed, "source-secret-token", "target-secret-token")
	if _, ok := findProfileByID(responseProfilesToSettings(listed.Profiles), "source-one"); ok {
		t.Fatalf("deleted profile still returned: %#v", listed.Profiles)
	}
	if _, err := os.Stat(filepath.Join(configDir, appSettingsFileName)); err != nil {
		t.Fatalf("settings file missing after delete: %v", err)
	}
}

func TestAppProfilesFeedExportAndImportConnections(t *testing.T) {
	sourceState := &mockEmbyState{}
	sourceEmby := newMockEmbyServerWithToken(sourceState, "source-secret-token")
	defer sourceEmby.Close()
	targetState := &mockEmbyState{}
	targetEmby := newMockEmbyServerWithToken(targetState, "target-secret-token")
	defer targetEmby.Close()

	dataDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       dataDir,
			ConfigDir:     t.TempDir(),
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(dataDir),
	).Routes())
	defer app.Close()

	client := authenticatedClient(t, app)
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "source-profile",
		"name":    "Source",
		"baseUrl": sourceEmby.URL,
		"apiKey":  "source-secret-token",
		"role":    "source",
	}, nil, http.StatusOK)
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "target-profile",
		"name":    "Target",
		"baseUrl": targetEmby.URL,
		"apiKey":  "target-secret-token",
		"role":    "target",
	}, nil, http.StatusOK)
	var saved appSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/profiles", map[string]string{
		"id":      "source-profile",
		"name":    "Source Updated",
		"baseUrl": sourceEmby.URL,
		"role":    "source",
	}, &saved, http.StatusOK)
	assertSettingsResponseDoesNotLeak(t, saved, "source-secret-token", "target-secret-token")
	postJSON(t, client, app.URL+"/api/settings/profiles/select", map[string]string{
		"currentSource": "source-profile",
		"currentTarget": "target-profile",
	}, nil, http.StatusOK)

	var connection map[string]any
	postJSON(t, client, app.URL+"/api/connection/test", map[string]any{
		"profileId": "source-profile",
	}, &connection, http.StatusOK)
	if connection["ok"] != true {
		t.Fatalf("connection test did not resolve source profile: %#v", connection)
	}

	var libraries map[string][]map[string]any
	postJSON(t, client, app.URL+"/api/libraries", map[string]string{
		"profileId": "source-profile",
	}, &libraries, http.StatusOK)
	if len(libraries["libraries"]) != 1 || libraries["libraries"][0]["id"] != "lib-movies" {
		t.Fatalf("libraries did not resolve source profile: %#v", libraries)
	}

	var exportCreate map[string]any
	postJSON(t, client, app.URL+"/api/jobs/export", map[string]any{
		"sourceProfileId":     "source-profile",
		"libraryIds":          []string{"lib-movies"},
		"includePeopleImages": true,
		"imageTypes":          []string{"Primary", "Logo"},
	}, &exportCreate, http.StatusAccepted)
	exportJob := waitForJob(t, client, app.URL, stringField(t, exportCreate, "id"))
	exportPath := stringField(t, objectField(t, exportJob, "result"), "path")

	var precheckCreate map[string]any
	postJSON(t, client, app.URL+"/api/jobs/import/precheck", map[string]any{
		"targetProfileId":     "target-profile",
		"exportPath":          filepath.Base(exportPath),
		"includePeopleImages": true,
		"imageTypes":          []string{"Logo"},
	}, &precheckCreate, http.StatusAccepted)
	precheckJob := waitForJob(t, client, app.URL, stringField(t, precheckCreate, "id"))
	if got := stringField(t, precheckJob, "type"); got != "import-precheck" {
		t.Fatalf("precheck type = %q, want import-precheck", got)
	}
	if got := targetState.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("precheck should not write to target: %s", got)
	}

	var importCreate map[string]any
	postJSON(t, client, app.URL+"/api/jobs/import", map[string]any{
		"targetProfileId":     "target-profile",
		"exportPath":          filepath.Base(exportPath),
		"includePeopleImages": true,
		"imageTypes":          []string{"Logo"},
	}, &importCreate, http.StatusAccepted)
	waitForJob(t, client, app.URL, stringField(t, importCreate, "id"))
	if got := sourceState.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("import should not write to source profile: %s", got)
	}
	if got := targetState.writeCounts(); got != "updates=1 itemImages=1 peopleImages=1" {
		t.Fatalf("import did not use target profile connection: %s", got)
	}
}

func newMockEmbyServerWithToken(state *mockEmbyState, expectedToken string) *httptest.Server {
	if strings.TrimSpace(expectedToken) == "" {
		expectedToken = "test-key"
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != expectedToken {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeSmokeJSON(w, map[string]any{"ServerName": "Mock Emby", "Version": "4.8.11", "Id": "mock"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			handleSmokeItems(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/Persons":
			writeSmokeJSON(w, map[string]any{
				"Items": []map[string]any{
					targetPerson(),
				},
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

func authenticatedClient(t *testing.T, app *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client()
	client.Jar = jar
	postJSON(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, nil, http.StatusOK)
	return client
}

func assertSettingsResponseDoesNotLeak(t *testing.T, response appSettingsResponse, secrets ...string) {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("settings response leaked API key %q: %s", secret, data)
		}
	}
}

func responseProfilesToSettings(profiles []appServerProfileResponse) []appServerProfileSettings {
	out := make([]appServerProfileSettings, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, appServerProfileSettings{ID: profile.ID, Name: profile.Name, BaseURL: profile.BaseURL, Role: profile.Role})
	}
	return out
}

func deleteJSON(t *testing.T, client *http.Client, url string, out any, wantStatus int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("DELETE %s status = %d, want %d, body=%s", url, resp.StatusCode, wantStatus, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode response from %s: %v\n%s", url, err, data)
		}
	}
}
