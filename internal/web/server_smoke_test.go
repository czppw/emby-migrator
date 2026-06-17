package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"emby-migrator/internal/config"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
)

func TestAPISmokeExportImportWithMockEmby(t *testing.T) {
	mockState := &mockEmbyState{}
	mockEmby := newMockEmbyServer(mockState)
	defer mockEmby.Close()

	dataDir := t.TempDir()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       dataDir,
			Version:       "smoke-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(dataDir),
	).Routes())
	defer app.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client()
	client.Jar = jar

	postJSON(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, nil, http.StatusOK)

	var connection map[string]any
	postJSON(t, client, app.URL+"/api/connection/test", map[string]any{
		"baseUrl": mockEmby.URL,
		"apiKey":  "test-key",
	}, &connection, http.StatusOK)
	if connection["ok"] != true {
		t.Fatalf("connection test did not return ok: %#v", connection)
	}

	var libraries map[string][]map[string]any
	postJSON(t, client, app.URL+"/api/libraries", map[string]any{
		"baseUrl": mockEmby.URL,
		"apiKey":  "test-key",
	}, &libraries, http.StatusOK)
	if len(libraries["libraries"]) != 1 || libraries["libraries"][0]["id"] != "lib-movies" {
		t.Fatalf("unexpected libraries response: %#v", libraries)
	}

	var exportCreate map[string]any
	postJSON(t, client, app.URL+"/api/jobs/export", map[string]any{
		"baseUrl":             mockEmby.URL,
		"apiKey":              "test-key",
		"libraryIds":          []string{"lib-movies"},
		"includePeopleImages": true,
		"imageTypes":          []string{"Primary", "Logo"},
	}, &exportCreate, http.StatusAccepted)
	exportJob := waitForJob(t, client, app.URL, stringField(t, exportCreate, "id"))
	exportPath := stringField(t, objectField(t, exportJob, "result"), "path")
	if _, err := os.Stat(filepath.Join(exportPath, "manifest.json")); err != nil {
		t.Fatalf("export manifest was not written: %v", err)
	}

	var dryRunCreate map[string]any
	postJSON(t, client, app.URL+"/api/jobs/import/precheck", map[string]any{
		"baseUrl":             mockEmby.URL,
		"apiKey":              "test-key",
		"exportPath":          filepath.Base(exportPath),
		"dryRun":              false,
		"includePeopleImages": true,
		"imageTypes":          []string{"Logo"},
	}, &dryRunCreate, http.StatusAccepted)
	dryRunJob := waitForJob(t, client, app.URL, stringField(t, dryRunCreate, "id"))
	if got := stringField(t, dryRunJob, "type"); got != "import-precheck" {
		t.Fatalf("precheck job type = %q, want import-precheck", got)
	}
	dryRunReport := objectField(t, objectField(t, dryRunJob, "result"), "report")
	if got := boolField(t, dryRunReport, "dryRun"); !got {
		t.Fatalf("precheck report dryRun = false, want true")
	}
	if got := mockState.writeCounts(); got != "updates=0 itemImages=0 peopleImages=0" {
		t.Fatalf("precheck wrote to mock Emby: %s", got)
	}

	var reportsResponse map[string][]map[string]any
	getJSON(t, client, app.URL+"/api/import-reports?exportPath="+filepath.Base(exportPath), &reportsResponse, http.StatusOK)
	if len(reportsResponse["reports"]) != 1 {
		t.Fatalf("reports response = %#v, want one report", reportsResponse)
	}
	reportName := stringField(t, reportsResponse["reports"][0], "name")
	if !strings.HasPrefix(reportName, "import-report-") || !strings.HasSuffix(reportName, ".json") {
		t.Fatalf("unexpected report name %q", reportName)
	}
	if got := boolField(t, reportsResponse["reports"][0], "dryRun"); !got {
		t.Fatalf("precheck report listing dryRun = false, want true")
	}
	downloadURL := app.URL + "/api/import-reports/download?exportPath=" + filepath.Base(exportPath) + "&name=" + reportName
	resp, err := client.Get(downloadURL)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download report status = %d, body=%s", resp.StatusCode, downloaded)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, reportName) {
		t.Fatalf("download disposition = %q, want report filename", got)
	}
	var downloadedReport map[string]any
	if err := json.Unmarshal(downloaded, &downloadedReport); err != nil {
		t.Fatalf("downloaded report is not JSON: %v\n%s", err, downloaded)
	}
	if got := boolField(t, downloadedReport, "dryRun"); !got {
		t.Fatalf("downloaded report dryRun = false, want true")
	}
	getJSON(t, client, app.URL+"/api/import-reports?exportPath=..%2f..", nil, http.StatusBadRequest)
	getJSON(t, client, app.URL+"/api/import-reports/download?exportPath="+filepath.Base(exportPath)+"&name=manifest.json", nil, http.StatusBadRequest)

	var importCreate map[string]any
	postJSON(t, client, app.URL+"/api/jobs/import", map[string]any{
		"baseUrl":             mockEmby.URL,
		"apiKey":              "test-key",
		"exportPath":          filepath.Base(exportPath),
		"dryRun":              false,
		"includePeopleImages": true,
		"imageTypes":          []string{"Logo"},
	}, &importCreate, http.StatusAccepted)
	importJob := waitForJob(t, client, app.URL, stringField(t, importCreate, "id"))
	importSummary := objectField(t, objectField(t, objectField(t, importJob, "result"), "report"), "summary")
	if intField(t, importSummary, "metadataUpdated") != 1 {
		t.Fatalf("metadataUpdated summary = %#v, want 1", importSummary)
	}
	if intField(t, importSummary, "itemImagesPushed") != 1 {
		t.Fatalf("itemImagesPushed summary = %#v, want 1", importSummary)
	}
	if intField(t, importSummary, "peopleImages") != 1 {
		t.Fatalf("peopleImages summary = %#v, want 1", importSummary)
	}

	mockState.mu.Lock()
	defer mockState.mu.Unlock()
	if len(mockState.updateBodies) != 1 {
		t.Fatalf("metadata update count = %d, want 1", len(mockState.updateBodies))
	}
	if got := fmt.Sprint(mockState.updateBodies[0]["Overview"]); got != "Source overview" {
		t.Fatalf("metadata update did not carry source overview: %#v", mockState.updateBodies[0])
	}
	if got, want := mockState.itemImageUploads, []string{"/Items/new-movie-1/Images/Logo"}; !slices.Equal(got, want) {
		t.Fatalf("item image uploads = %#v, want %#v", got, want)
	}
	if got, want := mockState.personImageUploads, []string{"/Items/6384/Images/Primary"}; !slices.Equal(got, want) {
		t.Fatalf("person image uploads = %#v, want %#v", got, want)
	}
}

type mockEmbyState struct {
	mu                 sync.Mutex
	updateBodies       []map[string]any
	itemImageUploads   []string
	personImageUploads []string
}

func (s *mockEmbyState) writeCounts() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("updates=%d itemImages=%d peopleImages=%d", len(s.updateBodies), len(s.itemImageUploads), len(s.personImageUploads))
}

func newMockEmbyServer(state *mockEmbyState) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info" && r.Header.Get("X-Emby-Token") != "test-key" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeSmokeJSON(w, map[string]any{"ServerName": "Mock Emby", "Version": "4.8.11", "Id": "mock"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			handleSmokeItems(w, r)
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
			writeSmokeJSON(w, map[string]any{
				"Name":            "Keanu Reeves",
				"Id":              6384,
				"ProviderIds":     map[string]string{"Tmdb": "6384"},
				"PrimaryImageTag": "person-primary",
			})
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

func handleSmokeItems(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	switch {
	case query.Get("ParentId") == "lib-movies" && query.Get("Limit") == "0":
		writeSmokeJSON(w, map[string]any{"Items": []any{}, "TotalRecordCount": 1})
	case query.Get("ParentId") == "lib-movies":
		writeSmokeJSON(w, map[string]any{"Items": []map[string]any{sourceMovie()}, "TotalRecordCount": 1})
	case query.Get("SearchTerm") != "":
		writeSmokeJSON(w, map[string]any{"Items": []map[string]any{targetMovie()}, "TotalRecordCount": 1})
	case query.Get("AnyProviderIdEquals") == "Tmdb.12345":
		writeSmokeJSON(w, map[string]any{"Items": []map[string]any{targetMovie()}, "TotalRecordCount": 1})
	default:
		writeSmokeJSON(w, map[string]any{
			"Items": []map[string]any{
				{
					"Id":             "lib-movies",
					"Name":           "Movies",
					"Type":           "CollectionFolder",
					"CollectionType": "movies",
					"ChildCount":     1,
				},
			},
			"TotalRecordCount": 1,
		})
	}
}

func sourceMovie() map[string]any {
	return map[string]any{
		"Id":             "old-movie-1",
		"Name":           "Mock Movie",
		"Type":           "Movie",
		"Path":           `D:\Movies\Mock Movie.mkv`,
		"Overview":       "Source overview",
		"ProductionYear": 2024,
		"ProviderIds":    map[string]string{"Tmdb": "12345"},
		"Genres":         []string{"Action"},
		"ImageTags":      map[string]string{"Primary": "primary-tag", "Logo": "logo-tag"},
		"People": []map[string]any{
			{
				"Name":            "Keanu Reeves",
				"Id":              1,
				"Type":            "Actor",
				"Role":            "Neo",
				"ProviderIds":     map[string]string{"Tmdb": "6384"},
				"PrimaryImageTag": "person-primary",
			},
		},
	}
}

func targetMovie() map[string]any {
	return map[string]any{
		"Id":             "new-movie-1",
		"Name":           "Mock Movie",
		"Type":           "Movie",
		"Path":           `/mnt/media/Mock Movie.mkv`,
		"ProductionYear": 2024,
		"ProviderIds":    map[string]string{"Tmdb": "99999"},
		"ImageTags":      map[string]string{},
	}
}

func postJSON(t *testing.T, client *http.Client, url string, payload any, out any, wantStatus int) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s status = %d, want %d, body=%s", url, resp.StatusCode, wantStatus, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode response from %s: %v\n%s", url, err, data)
		}
	}
}

func getJSON(t *testing.T, client *http.Client, url string, out any, wantStatus int) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s status = %d, want %d, body=%s", url, resp.StatusCode, wantStatus, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode response from %s: %v\n%s", url, err, data)
		}
	}
}

func waitForJob(t *testing.T, client *http.Client, appURL, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(appURL + "/api/jobs/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		status := stringField(t, snapshot, "status")
		switch status {
		case "done":
			return snapshot
		case "failed", "stopped":
			t.Fatalf("job %s ended with %s: %#v", id, status, snapshot)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return nil
}

func objectField(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := object[key].(map[string]any)
	if !ok {
		t.Fatalf("field %s is not an object in %#v", key, object)
	}
	return value
}

func stringField(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok || value == "" {
		t.Fatalf("field %s is not a non-empty string in %#v", key, object)
	}
	return value
}

func intField(t *testing.T, object map[string]any, key string) int {
	t.Helper()
	value, ok := object[key].(float64)
	if !ok {
		t.Fatalf("field %s is not a number in %#v", key, object)
	}
	return int(value)
}

func boolField(t *testing.T, object map[string]any, key string) bool {
	t.Helper()
	value, ok := object[key].(bool)
	if !ok {
		t.Fatalf("field %s is not a bool in %#v", key, object)
	}
	return value
}

func writeSmokeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func writeSmokeImage(w http.ResponseWriter, payload string) {
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write([]byte(payload))
}
