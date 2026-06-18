package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-migrator/internal/config"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
)

func TestTelegramSettingsSaveLoadAndTest(t *testing.T) {
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

	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123456:secret-token/sendMessage" {
			t.Fatalf("telegram path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("chat_id"); got != "100200300" {
			t.Fatalf("chat_id = %q", got)
		}
		if got := r.Form.Get("text"); !strings.Contains(got, "Emby Migrator") {
			t.Fatalf("text = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer telegram.Close()

	oldBaseURL := telegramAPIBaseURL
	telegramAPIBaseURL = telegram.URL
	defer func() { telegramAPIBaseURL = oldBaseURL }()

	var saved telegramSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/telegram", map[string]string{
		"botToken": "123456:secret-token",
		"chatId":   "100200300",
		"proxyUrl": "http://127.0.0.1:7890",
	}, &saved, http.StatusOK)
	if !saved.HasBotToken || strings.Contains(saved.BotTokenMasked, "secret-token") {
		t.Fatalf("unexpected save response: %#v", saved)
	}

	var loaded telegramSettingsResponse
	getJSON(t, client, app.URL+"/api/settings/telegram", &loaded, http.StatusOK)
	if !loaded.HasBotToken || loaded.ChatID != "100200300" || loaded.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected load response: %#v", loaded)
	}
	if strings.Contains(loaded.BotTokenMasked, "secret-token") {
		t.Fatalf("load response leaked token: %#v", loaded)
	}

	var onDisk telegramSettings
	readJSONFile(filepath.Join(configDir, telegramSettingsFileName), &onDisk)
	if onDisk.BotToken != "123456:secret-token" {
		t.Fatalf("settings file did not store token")
	}

	var testResp map[string]any
	postJSON(t, client, app.URL+"/api/settings/telegram/test", map[string]string{
		"chatId": "100200300",
	}, &testResp, http.StatusOK)
	if testResp["ok"] != true {
		t.Fatalf("unexpected test response: %#v", testResp)
	}
}

func TestTelegramSenderUsesConfiguredProxy(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer target.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(r.Method, r.URL.String(), strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	oldBaseURL := telegramAPIBaseURL
	telegramAPIBaseURL = target.URL
	defer func() { telegramAPIBaseURL = oldBaseURL }()

	if err := sendTelegramTestMessage(
		contextForTest(t),
		telegramSettings{BotToken: "123456:secret-token", ChatID: "1", ProxyURL: proxy.URL},
	); err != nil {
		t.Fatal(err)
	}
	if proxyHits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("proxy hits = %d, target hits = %d, want 1/1", proxyHits.Load(), targetHits.Load())
	}
}

func TestTelegramProxyDoesNotAffectDefaultHTTPTransport(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer target.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(r.Method, r.URL.String(), strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected default transport type %T", http.DefaultTransport)
	}
	originalProxyIsNil := transport.Proxy == nil
	oldBaseURL := telegramAPIBaseURL
	telegramAPIBaseURL = target.URL
	defer func() { telegramAPIBaseURL = oldBaseURL }()

	if err := sendTelegramTestMessage(
		contextForTest(t),
		telegramSettings{BotToken: "123456:secret-token", ChatID: "1", ProxyURL: proxy.URL},
	); err != nil {
		t.Fatal(err)
	}
	if http.DefaultTransport != transport || (transport.Proxy == nil) != originalProxyIsNil {
		t.Fatal("telegram proxy mutated http.DefaultTransport")
	}

	defaultClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := defaultClient.Get(target.URL + "/after-telegram")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default client status = %d, want 200", resp.StatusCode)
	}
	if proxyHits.Load() != 1 || targetHits.Load() != 2 {
		t.Fatalf("proxy hits = %d, target hits = %d, want 1/2", proxyHits.Load(), targetHits.Load())
	}
}

func TestTelegramSettingsProxyDoesNotAffectEmbyRequests(t *testing.T) {
	configDir := t.TempDir()
	embyHits := atomic.Int32{}
	embyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embyHits.Add(1)
		if r.URL.Path != "/System/Info" {
			t.Fatalf("emby path = %q", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ServerName": "Mock Emby",
			"Version":    "4.8.11",
			"Id":         "mock",
		})
	}))
	defer embyServer.Close()

	telegramHits := atomic.Int32{}
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telegramHits.Add(1)
		if r.URL.Path != "/bot123456:secret-token/sendMessage" {
			t.Fatalf("telegram path = %q", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer telegram.Close()

	proxyHits := atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.URL.Host != strings.TrimPrefix(telegram.URL, "http://") {
			t.Fatalf("proxy received non-Telegram request for host %q", r.URL.Host)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(r.Method, r.URL.String(), strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	oldBaseURL := telegramAPIBaseURL
	telegramAPIBaseURL = telegram.URL
	defer func() { telegramAPIBaseURL = oldBaseURL }()

	app := httptest.NewServer(NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "proxy-scope-test",
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
	var saved telegramSettingsResponse
	postJSON(t, client, app.URL+"/api/settings/telegram", map[string]string{
		"botToken": "123456:secret-token",
		"chatId":   "100200300",
		"proxyUrl": proxy.URL,
	}, &saved, http.StatusOK)

	var testResp map[string]any
	postJSON(t, client, app.URL+"/api/settings/telegram/test", map[string]string{
		"chatId":   "100200300",
		"proxyUrl": proxy.URL,
	}, &testResp, http.StatusOK)

	var connection map[string]any
	postJSON(t, client, app.URL+"/api/connection/test", map[string]string{
		"baseUrl": embyServer.URL,
		"apiKey":  "test-key",
	}, &connection, http.StatusOK)
	if proxyHits.Load() != 1 || telegramHits.Load() != 1 || embyHits.Load() != 1 {
		t.Fatalf("hits proxy/telegram/emby = %d/%d/%d, want 1/1/1", proxyHits.Load(), telegramHits.Load(), embyHits.Load())
	}
}

func TestTelegramJobTerminalNotificationUsesFakeAPI(t *testing.T) {
	configDir := t.TempDir()
	token := "123456:secret-token"
	chatID := "100200300"
	settings := telegramSettings{BotToken: token, ChatID: chatID}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, telegramSettingsFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	textCh := make(chan string, 1)
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123456:secret-token/sendMessage" {
			t.Fatalf("telegram path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("chat_id"); got != chatID {
			t.Fatalf("chat_id = %q", got)
		}
		textCh <- r.Form.Get("text")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer telegram.Close()

	oldBaseURL := telegramAPIBaseURL
	telegramAPIBaseURL = telegram.URL
	defer func() { telegramAPIBaseURL = oldBaseURL }()

	app := NewServer(
		config.Config{
			DataDir:       t.TempDir(),
			ConfigDir:     configDir,
			Version:       "notify-test",
			AdminPassword: "pw",
			SessionSecret: "test-session-secret",
		},
		job.NewManager(),
		exporter.NewService(t.TempDir()),
	)
	start := time.Now().Add(-90 * time.Second)
	snapshot := job.Job{
		ID:        "job-1",
		Type:      "export",
		Status:    job.StatusDone,
		StartedAt: start,
		EndedAt:   start.Add(90 * time.Second),
		Result: map[string]any{
			"manifest": map[string]any{
				"summary": map[string]any{
					"libraries": 1,
					"items":     2,
					"errors":    0,
				},
			},
		},
	}
	if err := app.sendTelegramJobTerminalNotification(contextForTest(t), snapshot, nil); err != nil {
		t.Fatal(err)
	}

	select {
	case text := <-textCh:
		for _, want := range []string{
			"Emby Migrator 任务通知",
			"任务类型：导出",
			"任务状态：完成",
			"用时：1m30s",
			"摘要：媒体库=1 项目=2 错误=0",
			"版本：notify-test",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("notification text missing %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, token) || strings.Contains(text, chatID) {
			t.Fatalf("notification text leaked Telegram credentials: %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("telegram notification was not sent")
	}
}

func TestTelegramJobNotificationUsesChineseLabelsForTerminalStates(t *testing.T) {
	tests := []struct {
		name     string
		jobType  string
		status   job.Status
		wantType string
		wantStat string
	}{
		{name: "precheck failed", jobType: "import-precheck", status: job.StatusFailed, wantType: "任务类型：导入预检", wantStat: "任务状态：失败"},
		{name: "import stopped", jobType: "import", status: job.StatusStopped, wantType: "任务类型：导入", wantStat: "任务状态：已中止"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := formatTelegramJobNotification(job.Job{
				Type:   tt.jobType,
				Status: tt.status,
				Error:  "sample error",
			}, nil, "test-version")
			for _, want := range []string{tt.wantType, tt.wantStat, "错误：sample error", "版本：test-version"} {
				if !strings.Contains(text, want) {
					t.Fatalf("notification text missing %q:\n%s", want, text)
				}
			}
			for _, forbidden := range []string{"import-precheck", "failed", "stopped", "浠", "鐘", "煎"} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("notification text contains non-Chinese or garbled marker %q:\n%s", forbidden, text)
				}
			}
		})
	}
}

func TestTelegramJobNotificationFriendlyExportPackageError(t *testing.T) {
	text := formatTelegramJobNotification(job.Job{
		Type:   "import-precheck",
		Status: job.StatusFailed,
		Error:  "export package not found: definitely-missing-export-package",
	}, nil, "test-version")
	for _, want := range []string{
		"任务类型：导入预检",
		"任务状态：失败",
		"错误：导出包不存在或已失效，请刷新导出包列表后重试：definitely-missing-export-package",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("notification text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "export package not found") {
		t.Fatalf("notification text leaked raw export package error:\n%s", text)
	}
}

func TestTelegramJobNotificationIncludesServerAddresses(t *testing.T) {
	exportText := formatTelegramJobNotification(job.Job{
		Type:   "export",
		Status: job.StatusDone,
		Result: map[string]any{
			"manifest": map[string]any{
				"source":  "http://source.example:8096/",
				"summary": map[string]any{"items": 2},
			},
		},
	}, nil, "test-version")
	if !strings.Contains(exportText, "源服务器：http://source.example:8096") {
		t.Fatalf("export notification missing source server:\n%s", exportText)
	}
	if strings.Contains(exportText, "目标服务器：") {
		t.Fatalf("export notification should not include target server:\n%s", exportText)
	}

	importText := formatTelegramJobNotification(job.Job{
		Type:   "import",
		Status: job.StatusDone,
		Result: map[string]any{
			"manifest": map[string]any{
				"source": "http://source.example:8096/",
			},
			"report": map[string]any{
				"target":  map[string]any{"baseUrl": "http://target.example:8097/"},
				"summary": map[string]any{"matched": 2},
			},
		},
	}, nil, "test-version")
	for _, want := range []string{
		"源服务器：http://source.example:8096",
		"目标服务器：http://target.example:8097",
	} {
		if !strings.Contains(importText, want) {
			t.Fatalf("import notification missing %q:\n%s", want, importText)
		}
	}

	precheckText := formatTelegramJobNotification(job.Job{
		Type:   "import-precheck",
		Status: job.StatusDone,
		Result: map[string]any{
			"manifest": map[string]any{
				"source": "http://source.example:8096/",
			},
			"report": map[string]any{
				"target":  map[string]any{"baseUrl": "http://target.example:8097/"},
				"summary": map[string]any{"matched": 2},
			},
		},
	}, nil, "test-version")
	for _, want := range []string{
		"任务类型：导入预检",
		"源服务器：http://source.example:8096",
		"目标服务器：http://target.example:8097",
	} {
		if !strings.Contains(precheckText, want) {
			t.Fatalf("import precheck notification missing %q:\n%s", want, precheckText)
		}
	}
}

func TestTelegramErrorSanitizesTokenAndChatID(t *testing.T) {
	token := "123456:secret-token"
	chatID := "100200300"
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"description": "bad token 123456:secret-token for chat 100200300",
		})
	}))
	defer telegram.Close()

	oldBaseURL := telegramAPIBaseURL
	telegramAPIBaseURL = telegram.URL
	defer func() { telegramAPIBaseURL = oldBaseURL }()

	err := sendTelegramMessage(contextForTest(t), telegramSettings{BotToken: token, ChatID: chatID}, "hello")
	if err == nil {
		t.Fatal("expected Telegram error")
	}
	message := err.Error()
	if strings.Contains(message, token) || strings.Contains(message, chatID) {
		t.Fatalf("Telegram error leaked credentials: %s", message)
	}
	if !strings.Contains(message, "123456****oken") || !strings.Contains(message, "****0300") {
		t.Fatalf("Telegram error did not include masked credentials: %s", message)
	}
}

func TestTelegramSettingsRejectsInvalidProxy(t *testing.T) {
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
	postJSON(t, client, app.URL+"/api/settings/telegram", map[string]string{
		"botToken": "123456:secret-token",
		"chatId":   "1",
		"proxyUrl": "ftp://127.0.0.1:7890",
	}, nil, http.StatusBadRequest)
	if _, err := os.Stat(filepath.Join(configDir, telegramSettingsFileName)); !os.IsNotExist(err) {
		t.Fatalf("settings file should not be written, stat err=%v", err)
	}
}

func contextForTest(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}
