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

	"emby-migrator/internal/emby"
	"emby-migrator/internal/job"
	"emby-migrator/internal/storage"
)

func TestExportKnownItemImageFailureIsRecordedWithoutDroppingItem(t *testing.T) {
	result := runImageFailureReviewExport(t, http.StatusInternalServerError, 0, false)

	if len(result.Manifest.Items) != 1 {
		t.Fatalf("manifest items = %d, want 1", len(result.Manifest.Items))
	}
	if len(result.Manifest.Items[0].Images) != 0 {
		t.Fatalf("failed image unexpectedly entered package: %#v", result.Manifest.Items[0].Images)
	}
	errorEntry := requireExportImageError(t, result.Manifest.Errors, "item-image")
	if errorEntry.ID != "item-1" || errorEntry.Name != "Review Movie" {
		t.Fatalf("item image error identity = %#v", errorEntry)
	}
	if !strings.Contains(errorEntry.Message, "HTTP 500") {
		t.Fatalf("item image error message = %q, want HTTP 500", errorEntry.Message)
	}
	if result.Manifest.Summary.Errors != 1 {
		t.Fatalf("summary errors = %d, want 1", result.Manifest.Summary.Errors)
	}

	entry := result.Manifest.Items[0]
	for _, relPath := range []string{entry.InfoPath, entry.RawPath, "manifest.json"} {
		if _, err := os.Stat(filepath.Join(result.Path, filepath.FromSlash(relPath))); err != nil {
			t.Fatalf("usable item package file %q is missing: %v", relPath, err)
		}
	}
}

func TestExportPersonImageFailureIsRecorded(t *testing.T) {
	result := runImageFailureReviewExport(t, http.StatusOK, http.StatusInternalServerError, true)

	errorEntry := requireExportImageError(t, result.Manifest.Errors, "person-image")
	if errorEntry.Name != "Actor One" {
		t.Fatalf("person image error identity = %#v", errorEntry)
	}
	if !strings.Contains(errorEntry.Message, "HTTP 500") {
		t.Fatalf("person image error message = %q, want HTTP 500", errorEntry.Message)
	}
	if result.Manifest.Summary.Errors != 1 || result.Manifest.Summary.PeopleImages != 0 {
		t.Fatalf("unexpected summary after avatar failure: %#v", result.Manifest.Summary)
	}
}

func TestExportMissingPersonImage404IsNotFailure(t *testing.T) {
	result := runImageFailureReviewExport(t, http.StatusOK, http.StatusNotFound, true)

	if len(result.Manifest.Errors) != 0 {
		t.Fatalf("missing avatar should be skipped, got errors: %#v", result.Manifest.Errors)
	}
	if result.Manifest.Summary.Errors != 0 || result.Manifest.Summary.PeopleImages != 0 {
		t.Fatalf("unexpected summary for missing avatar: %#v", result.Manifest.Summary)
	}
}

func runImageFailureReviewExport(t *testing.T, itemImageStatus, personImageStatus int, includePerson bool) ExportResult {
	t.Helper()

	item := map[string]any{
		"Id":        "item-1",
		"Name":      "Review Movie",
		"Type":      "Movie",
		"Path":      "/media/review-movie.mkv",
		"ImageTags": map[string]string{"Primary": "poster-tag"},
	}
	if includePerson {
		item["People"] = []map[string]any{{
			"Name":            "Actor One",
			"Type":            "Actor",
			"PrimaryImageTag": "avatar-tag",
		}}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeImageFailureReviewJSON(t, w, map[string]any{
				"ServerName": "Review Emby",
				"Version":    "4.9.5.0",
				"Id":         "review-server",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("ParentId") == "lib-movies":
			writeImageFailureReviewJSON(t, w, map[string]any{"Items": []any{item}, "TotalRecordCount": 1})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "item-1":
			writeImageFailureReviewJSON(t, w, map[string]any{"Items": []any{item}, "TotalRecordCount": 1})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-1/Images":
			writeImageFailureReviewJSON(t, w, []map[string]any{{"ImageType": "Primary", "ImageTag": "poster-tag"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-1/Images/Primary":
			writeImageFailureReviewResponse(w, itemImageStatus)
		case r.Method == http.MethodGet && r.URL.Path == "/Persons/Actor One":
			writeImageFailureReviewJSON(t, w, map[string]any{
				"Name":            "Actor One",
				"Id":              "person-1",
				"PrimaryImageTag": "avatar-tag",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Persons/Actor One/Images/Primary":
			writeImageFailureReviewResponse(w, personImageStatus)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	service := NewService(t.TempDir())
	manager := job.NewManager()
	j := manager.Create("export-image-failure-review")
	j.Start()
	result, err := service.Export(context.Background(), j, ExportRequest{
		Connection:          emby.Connection{BaseURL: server.URL, APIKey: "test-key"},
		Libraries:           []emby.Library{{ID: "lib-movies", Name: "Movies"}},
		ImageTypes:          []string{"Primary"},
		IncludePeopleImages: includePerson,
		Concurrency:         1,
	})
	if err != nil {
		t.Fatalf("Export returned error: %v", err)
	}
	return result
}

func requireExportImageError(t *testing.T, entries []storage.ErrorEntry, scope string) storage.ErrorEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Scope == scope {
			return entry
		}
	}
	t.Fatalf("manifest errors do not contain scope %q: %#v", scope, entries)
	return storage.ErrorEntry{}
}

func writeImageFailureReviewJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write JSON response: %v", err)
	}
}

func writeImageFailureReviewResponse(w http.ResponseWriter, status int) {
	if status == http.StatusOK {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("review-image"))
		return
	}
	http.Error(w, http.StatusText(status), status)
}
