package web

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-migrator/internal/config"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
	"emby-migrator/internal/storage"
)

func TestImportPackageListAndManualPathValidation(t *testing.T) {
	dataDir := t.TempDir()
	configDir := t.TempDir()
	importRoot := t.TempDir()
	localPackage := filepath.Join(dataDir, "exports", "local-package")
	externalPackage := filepath.Join(importRoot, "copied-package")
	for index, packagePath := range []string{localPackage, externalPackage} {
		if err := os.MkdirAll(packagePath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := storage.WriteJSON(filepath.Join(packagePath, "manifest.json"), storage.Manifest{
			ExportedAt:  time.Date(2026, 7, 14, 10+index, 0, 0, 0, time.UTC),
			EmbyVersion: "4.9.5.0",
			Items:       make([]storage.ItemEntry, index+1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	app := httptest.NewServer(NewServer(
		config.Config{DataDir: dataDir, ConfigDir: configDir, AdminPassword: "pw", SessionSecret: "test-secret"},
		job.NewManager(),
		exporter.NewService(dataDir, importRoot),
	).Routes())
	defer app.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	postJSON(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, nil, http.StatusOK)

	var listed struct {
		Exports []exporter.ImportPackageInfo `json:"exports"`
	}
	getJSON(t, client, app.URL+"/api/exports", &listed, http.StatusOK)
	if len(listed.Exports) != 2 {
		t.Fatalf("exports = %#v", listed.Exports)
	}
	byName := make(map[string]exporter.ImportPackageInfo, len(listed.Exports))
	for _, packageInfo := range listed.Exports {
		byName[packageInfo.Name] = packageInfo
	}
	if byName["local-package"].Path != "local-package" || byName["local-package"].Source != "本机导出" {
		t.Fatalf("local package = %#v", byName["local-package"])
	}
	if byName["copied-package"].Path != filepath.ToSlash(externalPackage) || byName["copied-package"].Source != "外部迁移包" {
		t.Fatalf("external package = %#v", byName["copied-package"])
	}

}
