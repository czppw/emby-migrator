package web

import (
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
)

func TestEmbyDatabaseDiscoveryEndpointSelectsOnlyDatabaseWithoutDocker(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "emby", "data", "library.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	server := NewServer(config.Config{
		DataDir:          dataDir,
		ConfigDir:        t.TempDir(),
		EmbyDatabaseRoot: root,
		AdminPassword:    "pw",
		SessionSecret:    "test-session-secret",
	}, job.NewManager(), exporter.NewService(dataDir))
	server.docker = unavailableDockerController{err: errors.New("socket unavailable")}
	app := httptest.NewServer(server.Routes())
	defer app.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client()
	client.Jar = jar
	postJSON(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, nil, http.StatusOK)

	var response embyDatabaseDiscoveryResponse
	getJSON(t, client, app.URL+"/api/emby-databases", &response, http.StatusOK)
	if response.DockerAvailable || response.SelectedPath != "emby/data/library.db" || len(response.Databases) != 1 {
		t.Fatalf("unexpected discovery response: %#v", response)
	}
}

func TestDiscoverEmbyDatabasesFindsRegularLibraryFiles(t *testing.T) {
	root := t.TempDir()
	wanted := filepath.Join(root, "emby", "data", "library.db")
	if err := os.MkdirAll(filepath.Dir(wanted), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wanted, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-library.db"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	candidates, err := discoverEmbyDatabases(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Path != "emby/data/library.db" {
		t.Fatalf("discovered databases = %#v", candidates)
	}
}

func TestMatchDatabaseMountsSelectsSameHostSource(t *testing.T) {
	root := t.TempDir()
	candidates := []embyDatabaseCandidate{
		{Path: "first/data/library.db", absolutePath: filepath.Join(root, "first", "data", "library.db")},
		{Path: "second/data/library.db", absolutePath: filepath.Join(root, "second", "data", "library.db")},
	}
	selfMounts := []dockerengine.Mount{
		{Source: "/host/emby-first", Destination: filepath.Join(root, "first")},
		{Source: "/host/emby-second", Destination: filepath.Join(root, "second")},
	}
	targetMounts := []dockerengine.Mount{{Source: "/host/emby-second", Destination: "/config"}}

	matchDatabaseMounts(candidates, targetMounts, selfMounts, "emby-second")
	if candidates[0].Matched {
		t.Fatalf("unrelated database was matched: %#v", candidates[0])
	}
	if !candidates[1].Matched || candidates[1].ContainerName != "emby-second" {
		t.Fatalf("target database was not matched: %#v", candidates[1])
	}
}

func TestCandidateExistsUsesPortableSeparators(t *testing.T) {
	candidates := []embyDatabaseCandidate{{Path: "emby/data/library.db"}}
	requested := filepath.Join("emby", "data", "library.db")
	if !candidateExists(candidates, requested) {
		t.Fatalf("candidate %q should exist", requested)
	}
}
