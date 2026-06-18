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

func TestDefaultSinglePasswordLoginReturnsAdminPrincipal(t *testing.T) {
	app, client := newAuthTestServer(t, t.TempDir(), "pw")
	defer app.Close()

	body := postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, http.StatusOK)
	assertNoSecretInBody(t, body, "pw")
	var login authStatusResponse
	if err := json.Unmarshal(body, &login); err != nil {
		t.Fatal(err)
	}
	if !login.Authenticated || login.User != "admin" || login.Role != "admin" {
		t.Fatalf("unexpected login response: %#v", login)
	}

	body = getRaw(t, client, app.URL+"/api/auth/status", http.StatusOK)
	assertNoSecretInBody(t, body, "pw")
	var status authStatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.User != "admin" || status.Role != "admin" {
		t.Fatalf("unexpected auth status: %#v", status)
	}
}

func TestUsersJSONLoginMigratesPlaintextPassword(t *testing.T) {
	configDir := t.TempDir()
	writeUsersFile(t, configDir, usersConfig{
		Users: []configUser{
			{Username: "viewer1", Password: "viewer-pass", Role: "viewer"},
		},
	})
	app, client := newAuthTestServer(t, configDir, "legacy-pw")
	defer app.Close()

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "viewer1",
		"password": "viewer-pass",
	}, http.StatusOK)

	body := getRaw(t, client, app.URL+"/api/auth/status", http.StatusOK)
	assertNoSecretInBody(t, body, "viewer-pass")
	var status authStatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.User != "viewer1" || status.Role != "viewer" {
		t.Fatalf("unexpected users.json auth status: %#v", status)
	}

	var disk usersConfig
	if err := readJSONFile(filepath.Join(configDir, usersFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Users) != 1 || disk.Users[0].Password != "" || !strings.HasPrefix(disk.Users[0].PasswordHash, passwordHashPrefix) {
		t.Fatalf("users.json was not migrated to passwordHash: %#v", disk.Users)
	}

	otherApp, otherClient := newAuthTestServer(t, configDir, "legacy-pw")
	defer otherApp.Close()
	postJSONRaw(t, otherClient, otherApp.URL+"/api/auth/login", map[string]string{
		"password": "legacy-pw",
	}, http.StatusUnauthorized)
}

func TestViewerCannotUseWriteEndpoints(t *testing.T) {
	configDir := t.TempDir()
	writeUsersFile(t, configDir, usersConfig{
		Users: []configUser{
			{Username: "viewer1", Password: "viewer-pass", Role: "viewer"},
		},
	})
	app, client := newAuthTestServer(t, configDir, "pw")
	defer app.Close()

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "viewer1",
		"password": "viewer-pass",
	}, http.StatusOK)

	postJSONRaw(t, client, app.URL+"/api/settings/app", map[string]any{}, http.StatusForbidden)
	postJSONRaw(t, client, app.URL+"/api/jobs/export", map[string]any{}, http.StatusForbidden)
}

func TestUsersAPIIsNotExposed(t *testing.T) {
	configDir := t.TempDir()
	writeUsersFile(t, configDir, usersConfig{
		Users: []configUser{
			{Username: "admin", Password: "admin-secret", Role: "admin"},
			{Username: "viewer1", Password: "viewer-secret", Role: "viewer"},
		},
	})
	app, client := newAuthTestServer(t, configDir, "pw")
	defer app.Close()

	loginBody := postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "admin",
		"password": "admin-secret",
	}, http.StatusOK)
	assertNoSecretInBody(t, loginBody, "admin-secret")

	body := getRaw(t, client, app.URL+"/api/users", http.StatusNotFound)
	assertNoSecretInBody(t, body, "admin-secret")
	assertNoSecretInBody(t, body, "viewer-secret")
	if !bytes.Contains(body, []byte("not found")) {
		t.Fatalf("unexpected users endpoint response: %s", body)
	}
	postJSONRaw(t, client, app.URL+"/api/users", map[string]any{
		"users": []map[string]string{
			{"username": "admin", "role": "admin"},
		},
	}, http.StatusNotFound)
}

func TestLoggedInUserCanChangeOwnPassword(t *testing.T) {
	configDir := t.TempDir()
	writeUsersFile(t, configDir, usersConfig{
		Users: []configUser{
			{Username: "admin", Password: "admin-secret", Role: "admin"},
			{Username: "viewer1", Password: "viewer-secret", Role: "viewer"},
		},
	})
	app, client := newAuthTestServer(t, configDir, "pw")
	defer app.Close()

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "viewer1",
		"password": "viewer-secret",
	}, http.StatusOK)

	body := postJSONRaw(t, client, app.URL+"/api/auth/password", map[string]string{
		"oldPassword": "viewer-secret",
		"newPassword": "viewer-next",
	}, http.StatusOK)
	assertNoSecretInBody(t, body, "viewer-secret")
	assertNoSecretInBody(t, body, "viewer-next")

	nextApp, nextClient := newAuthTestServer(t, configDir, "pw")
	defer nextApp.Close()
	postJSONRaw(t, nextClient, nextApp.URL+"/api/auth/login", map[string]string{
		"username": "viewer1",
		"password": "viewer-next",
	}, http.StatusOK)

	var disk usersConfig
	if err := readJSONFile(filepath.Join(configDir, usersFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Users) != 2 || disk.Users[1].Password != "" || disk.Users[1].PasswordHash == "" {
		t.Fatalf("password change was not persisted as passwordHash: %#v", disk.Users)
	}
}

func TestPasswordChangeRejectsWrongOldPassword(t *testing.T) {
	configDir := t.TempDir()
	writeUsersFile(t, configDir, usersConfig{
		Users: []configUser{
			{Username: "viewer1", Password: "viewer-secret", Role: "viewer"},
		},
	})
	app, client := newAuthTestServer(t, configDir, "pw")
	defer app.Close()

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "viewer1",
		"password": "viewer-secret",
	}, http.StatusOK)

	postJSONRaw(t, client, app.URL+"/api/auth/password", map[string]string{
		"oldPassword": "wrong",
		"newPassword": "viewer-next",
	}, http.StatusUnauthorized)

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "viewer1",
		"password": "viewer-secret",
	}, http.StatusOK)
}

func TestDefaultAdminPasswordCanBePromotedToUsersConfig(t *testing.T) {
	configDir := t.TempDir()
	app, client := newAuthTestServer(t, configDir, "pw")
	defer app.Close()

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"password": "pw",
	}, http.StatusOK)

	body := postJSONRaw(t, client, app.URL+"/api/auth/password", map[string]string{
		"oldPassword": "pw",
		"newPassword": "admin-next",
	}, http.StatusOK)
	assertNoSecretInBody(t, body, "pw")
	assertNoSecretInBody(t, body, "admin-next")

	var disk usersConfig
	if err := readJSONFile(filepath.Join(configDir, usersFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Users) != 1 || !sameLoginName("admin", disk.Users[0].Username) || !disk.Users[0].verifyPassword("admin-next") {
		t.Fatalf("default admin password was not migrated: %#v", disk.Users)
	}

	nextApp, nextClient := newAuthTestServer(t, configDir, "pw")
	defer nextApp.Close()
	postJSONRaw(t, nextClient, nextApp.URL+"/api/auth/login", map[string]string{
		"username": "admin",
		"password": "admin-next",
	}, http.StatusOK)
}

func newAuthTestServer(t *testing.T, configDir, adminPassword string) (*httptest.Server, *http.Client) {
	t.Helper()
	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "auth-test",
			AdminPassword: adminPassword,
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	).Routes())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := app.Client()
	client.Jar = jar
	return app, client
}

func writeUsersFile(t *testing.T, configDir string, cfg usersConfig) {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, usersFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func postJSONRaw(t *testing.T, client *http.Client, url string, payload any, wantStatus int) []byte {
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
	return data
}

func getRaw(t *testing.T, client *http.Client, url string, wantStatus int) []byte {
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
	return data
}

func assertNoSecretInBody(t *testing.T, body []byte, secret string) {
	t.Helper()
	if secret != "" && bytes.Contains(body, []byte(secret)) {
		t.Fatalf("response leaked secret %q: %s", secret, body)
	}
}
