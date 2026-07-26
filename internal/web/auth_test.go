package web

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDefaultSingleUserLoginRejectsDifferentUsername(t *testing.T) {
	app, client := newAuthTestServer(t, t.TempDir(), "pw")
	defer app.Close()

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "someone-else",
		"password": "pw",
	}, http.StatusUnauthorized)

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "admin",
		"password": "pw",
	}, http.StatusOK)
}

func TestSingleUserCanChangeAccountAndInvalidateOldSessions(t *testing.T) {
	configDir := t.TempDir()
	app, client := newAuthTestServer(t, configDir, "pw")
	defer app.Close()

	otherJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherClient := &http.Client{Transport: app.Client().Transport, Jar: otherJar}
	for _, loginClient := range []*http.Client{client, otherClient} {
		postJSONRaw(t, loginClient, app.URL+"/api/auth/login", map[string]string{
			"username": "admin",
			"password": "pw",
		}, http.StatusOK)
	}

	body := postJSONRaw(t, client, app.URL+"/api/auth/account", map[string]string{
		"currentPassword": "pw",
		"newUsername":     "迁移管理员_01",
		"newPassword":     "next-pw",
	}, http.StatusOK)
	assertNoSecretInBody(t, body, "pw")
	var changed usernameChangeResponse
	if err := json.Unmarshal(body, &changed); err != nil {
		t.Fatal(err)
	}
	if !changed.OK || changed.Username != "迁移管理员_01" {
		t.Fatalf("unexpected username change response: %#v", changed)
	}

	for _, oldSession := range []*http.Client{client, otherClient} {
		var status authStatusResponse
		if err := json.Unmarshal(getRaw(t, oldSession, app.URL+"/api/auth/status", http.StatusOK), &status); err != nil {
			t.Fatal(err)
		}
		if status.Authenticated {
			t.Fatalf("old session remained authenticated: %#v", status)
		}
	}

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "admin",
		"password": "pw",
	}, http.StatusUnauthorized)
	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "迁移管理员_01",
		"password": "pw",
	}, http.StatusUnauthorized)
	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "迁移管理员_01",
		"password": "next-pw",
	}, http.StatusOK)

	var disk usersConfig
	if err := readJSONFile(filepath.Join(configDir, usersFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Users) != 1 || disk.Users[0].Username != "迁移管理员_01" || !disk.Users[0].verifyPassword("next-pw") {
		t.Fatalf("unexpected persisted account: %#v", disk.Users)
	}
}

func TestUsernameChangeValidatesPasswordAndFormat(t *testing.T) {
	app, client := newAuthTestServer(t, t.TempDir(), "pw")
	defer app.Close()
	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{"password": "pw"}, http.StatusOK)

	postJSONRaw(t, client, app.URL+"/api/auth/account", map[string]string{
		"currentPassword": "wrong",
		"newUsername":     "new-admin",
	}, http.StatusUnauthorized)
	postJSONRaw(t, client, app.URL+"/api/auth/account", map[string]string{
		"currentPassword": "pw",
		"newUsername":     "x",
	}, http.StatusBadRequest)
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

func TestLegacySHA256PasswordHashMigratesToBcryptAfterSuccessfulLogin(t *testing.T) {
	configDir := t.TempDir()
	salt := []byte("legacy-test-salt")
	sum := saltedPasswordSum([]byte("legacy-password"), salt)
	legacyHash := legacyPasswordHashPrefix + "v1:" + base64.RawURLEncoding.EncodeToString(salt) + ":" + hex.EncodeToString(sum[:])
	writeUsersFile(t, configDir, usersConfig{Users: []configUser{{
		Username:     "admin",
		PasswordHash: legacyHash,
		Role:         "admin",
	}}})
	app, client := newAuthTestServer(t, configDir, "unused")
	defer app.Close()

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "admin",
		"password": "wrong-password",
	}, http.StatusUnauthorized)
	var before usersConfig
	if err := readJSONFile(filepath.Join(configDir, usersFileName), &before); err != nil {
		t.Fatal(err)
	}
	if before.Users[0].PasswordHash != legacyHash {
		t.Fatalf("legacy hash changed after failed login: %q", before.Users[0].PasswordHash)
	}

	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "admin",
		"password": "legacy-password",
	}, http.StatusOK)
	var after usersConfig
	if err := readJSONFile(filepath.Join(configDir, usersFileName), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Users) != 1 || !strings.HasPrefix(after.Users[0].PasswordHash, passwordHashPrefix) {
		t.Fatalf("legacy hash was not migrated to bcrypt: %#v", after.Users)
	}
	if !after.Users[0].verifyPassword("legacy-password") {
		t.Fatal("migrated bcrypt hash does not verify")
	}
	if after.Users[0].SessionVersion != defaultUserSessionVersion {
		t.Fatalf("sessionVersion = %d, want %d", after.Users[0].SessionVersion, defaultUserSessionVersion)
	}
}

func TestPasswordOnlyChangeInvalidatesAllExistingSessions(t *testing.T) {
	configDir := t.TempDir()
	writeUsersFile(t, configDir, usersConfig{Users: []configUser{{
		Username: "admin", Password: "old-password", Role: "admin",
	}}})
	app, client := newAuthTestServer(t, configDir, "unused")
	defer app.Close()
	otherJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherClient := &http.Client{Transport: app.Client().Transport, Jar: otherJar}
	for _, loginClient := range []*http.Client{client, otherClient} {
		postJSONRaw(t, loginClient, app.URL+"/api/auth/login", map[string]string{
			"username": "admin", "password": "old-password",
		}, http.StatusOK)
	}

	postJSONRaw(t, client, app.URL+"/api/auth/password", map[string]string{
		"oldPassword": "old-password", "newPassword": "new-password",
	}, http.StatusOK)
	for _, oldClient := range []*http.Client{client, otherClient} {
		var status authStatusResponse
		if err := json.Unmarshal(getRaw(t, oldClient, app.URL+"/api/auth/status", http.StatusOK), &status); err != nil {
			t.Fatal(err)
		}
		if status.Authenticated {
			t.Fatalf("session remained authenticated after password change: %#v", status)
		}
	}
	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "admin", "password": "new-password",
	}, http.StatusOK)

	var disk usersConfig
	if err := readJSONFile(filepath.Join(configDir, usersFileName), &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Users[0].SessionVersion != defaultUserSessionVersion+1 {
		t.Fatalf("sessionVersion = %d, want %d", disk.Users[0].SessionVersion, defaultUserSessionVersion+1)
	}
}

func TestLoginRateLimitAndSuccessfulLoginReset(t *testing.T) {
	app, client := newAuthTestServer(t, t.TempDir(), "correct-password")
	defer app.Close()

	for i := 1; i < loginMaxFailures; i++ {
		postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
			"username": "admin", "password": "wrong-password",
		}, http.StatusUnauthorized)
	}
	resp := postJSONResponse(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "ADMIN", "password": "wrong-password",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("fifth failed login status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("rate-limited response is missing Retry-After")
	}
	postJSONRaw(t, client, app.URL+"/api/auth/login", map[string]string{
		"username": "admin", "password": "correct-password",
	}, http.StatusTooManyRequests)

	resetApp, resetClient := newAuthTestServer(t, t.TempDir(), "correct-password")
	defer resetApp.Close()
	for i := 0; i < loginMaxFailures-1; i++ {
		postJSONRaw(t, resetClient, resetApp.URL+"/api/auth/login", map[string]string{
			"username": "admin", "password": "wrong-password",
		}, http.StatusUnauthorized)
	}
	postJSONRaw(t, resetClient, resetApp.URL+"/api/auth/login", map[string]string{
		"username": "admin", "password": "correct-password",
	}, http.StatusOK)
	for i := 0; i < loginMaxFailures-1; i++ {
		postJSONRaw(t, resetClient, resetApp.URL+"/api/auth/login", map[string]string{
			"username": "admin", "password": "wrong-password",
		}, http.StatusUnauthorized)
	}
}

func TestExpiredLoginRateLimitIsCleanedUp(t *testing.T) {
	s := &Server{loginAttempts: make(map[string]loginAttemptState)}
	now := time.Now()
	for i := 0; i < loginMaxFailures; i++ {
		s.recordLoginFailure("client:admin", now)
	}
	if _, blocked := s.loginBlocked("client:admin", now); !blocked {
		t.Fatal("expected login to be blocked")
	}
	if _, blocked := s.loginBlocked("client:admin", now.Add(loginRateLimitWindow+time.Second)); blocked {
		t.Fatal("expired login block was not removed")
	}
}

func TestForwardedHTTPSProducesSecureSessionCookies(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/login", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if cookie := s.sessionCookie(r, "token"); !cookie.Secure {
		t.Fatal("session cookie is not Secure behind HTTPS proxy")
	}
	w := httptest.NewRecorder()
	s.clearSessionCookie(w, r)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("cleared session cookie is not Secure: %#v", cookies)
	}
}

func TestSessionRandomFailureDoesNotUsePredictableFallback(t *testing.T) {
	original := readSecureRandom
	readSecureRandom = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	defer func() { readSecureRandom = original }()

	s := &Server{sessionSecret: []byte("test-secret")}
	if token, err := s.newSessionToken(defaultPrincipal()); err == nil || token != "" {
		t.Fatalf("newSessionToken() = %q, %v; want empty token and error", token, err)
	}
}

func TestSessionSecretRandomFailurePanics(t *testing.T) {
	original := readSecureRandom
	readSecureRandom = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	defer func() { readSecureRandom = original }()
	defer func() {
		if recover() == nil {
			t.Fatal("makeSessionSecret did not fail closed")
		}
	}()
	makeSessionSecret("")
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

func postJSONResponse(t *testing.T, client *http.Client, url string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
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
