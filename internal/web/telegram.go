package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"emby-migrator/internal/job"
)

const telegramSettingsFileName = "telegram.json"

var telegramAPIBaseURL = "https://api.telegram.org"

type telegramSettings struct {
	BotToken  string `json:"botToken,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	ProxyURL  string `json:"proxyUrl,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type telegramSettingsRequest struct {
	BotToken string `json:"botToken"`
	ChatID   string `json:"chatId"`
	ProxyURL string `json:"proxyUrl"`
}

type telegramSettingsResponse struct {
	OK             bool   `json:"ok"`
	HasBotToken    bool   `json:"hasBotToken"`
	BotTokenMasked string `json:"botTokenMasked,omitempty"`
	ChatID         string `json:"chatId,omitempty"`
	ProxyURL       string `json:"proxyUrl,omitempty"`
}

func (s *Server) handleTelegramSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.loadTelegramSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, telegramResponse(settings))
}

func (s *Server) handleTelegramSettingsSave(w http.ResponseWriter, r *http.Request) {
	settings, err := s.telegramSettingsFromRequest(w, r, true)
	if err != nil || settings == nil {
		return
	}
	settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.saveTelegramSettings(*settings); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, telegramResponse(*settings))
}

func (s *Server) handleTelegramSettingsTest(w http.ResponseWriter, r *http.Request) {
	settings, err := s.telegramSettingsFromRequest(w, r, false)
	if err != nil || settings == nil {
		return
	}
	if strings.TrimSpace(settings.BotToken) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("please fill Telegram Bot Token"))
		return
	}
	if strings.TrimSpace(settings.ChatID) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("please fill Telegram Chat ID"))
		return
	}
	if err := sendTelegramTestMessage(r.Context(), *settings); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) telegramSettingsFromRequest(w http.ResponseWriter, r *http.Request, preserveSavedToken bool) (*telegramSettings, error) {
	var req telegramSettingsRequest
	if !decodeJSON(w, r, &req) {
		return nil, fmt.Errorf("invalid json")
	}
	proxyURL := strings.TrimSpace(req.ProxyURL)
	if proxyURL != "" {
		if err := validateProxyURL(proxyURL); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return nil, err
		}
	}
	settings := telegramSettings{
		BotToken: strings.TrimSpace(req.BotToken),
		ChatID:   strings.TrimSpace(req.ChatID),
		ProxyURL: proxyURL,
	}
	if settings.BotToken == "" && preserveSavedToken {
		saved, err := s.loadTelegramSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return nil, err
		}
		settings.BotToken = saved.BotToken
	}
	if settings.BotToken == "" && !preserveSavedToken {
		saved, err := s.loadTelegramSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return nil, err
		}
		settings.BotToken = saved.BotToken
	}
	return &settings, nil
}

func (s *Server) loadTelegramSettings() (telegramSettings, error) {
	path := s.telegramSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return telegramSettings{}, nil
		}
		return telegramSettings{}, fmt.Errorf("read Telegram settings failed: %w", err)
	}
	var settings telegramSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return telegramSettings{}, fmt.Errorf("parse Telegram settings failed: %w", err)
	}
	settings.BotToken = strings.TrimSpace(settings.BotToken)
	settings.ChatID = strings.TrimSpace(settings.ChatID)
	settings.ProxyURL = strings.TrimSpace(settings.ProxyURL)
	return settings, nil
}

func (s *Server) saveTelegramSettings(settings telegramSettings) error {
	configDir := s.configDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create config directory failed: %w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Telegram settings failed: %w", err)
	}
	if err := os.WriteFile(s.telegramSettingsPath(), data, 0o600); err != nil {
		return fmt.Errorf("save Telegram settings failed: %w", err)
	}
	return nil
}

func (s *Server) configDir() string {
	if strings.TrimSpace(s.cfg.ConfigDir) != "" {
		return s.cfg.ConfigDir
	}
	if strings.TrimSpace(s.cfg.DataDir) != "" {
		return filepath.Join(s.cfg.DataDir, "config")
	}
	return "config"
}

func (s *Server) telegramSettingsPath() string {
	return filepath.Join(s.configDir(), telegramSettingsFileName)
}

func telegramResponse(settings telegramSettings) telegramSettingsResponse {
	token := strings.TrimSpace(settings.BotToken)
	return telegramSettingsResponse{
		OK:             true,
		HasBotToken:    token != "",
		BotTokenMasked: maskTelegramToken(token),
		ChatID:         strings.TrimSpace(settings.ChatID),
		ProxyURL:       strings.TrimSpace(settings.ProxyURL),
	}
}

func maskTelegramToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 10 {
		return "****"
	}
	return token[:6] + "****" + token[len(token)-4:]
}

func validateProxyURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
		return nil
	default:
		return fmt.Errorf("proxy URL only supports http, https, or socks5")
	}
}

func sendTelegramTestMessage(ctx context.Context, settings telegramSettings) error {
	return sendTelegramMessage(ctx, settings, "Emby Migrator 测试消息")
}

func sendTelegramMessage(ctx context.Context, settings telegramSettings, text string) error {
	token := strings.TrimSpace(settings.BotToken)
	chatID := strings.TrimSpace(settings.ChatID)
	if token == "" {
		return fmt.Errorf("Telegram Bot Token is required")
	}
	if chatID == "" {
		return fmt.Errorf("Telegram Chat ID is required")
	}
	client, err := telegramHTTPClient(settings)
	if err != nil {
		return err
	}

	endpoint := strings.TrimRight(telegramAPIBaseURL, "/") + "/bot" + url.PathEscape(token) + "/sendMessage"
	body := url.Values{}
	body.Set("chat_id", chatID)
	body.Set("text", text)
	body.Set("disable_web_page_preview", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return fmt.Errorf("create Telegram request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram request failed: %s", sanitizeTelegramError(err.Error(), token, chatID))
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := telegramErrorDescription(data)
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("Telegram message send failed: %s", sanitizeTelegramError(message, token, chatID))
	}
	return nil
}

func telegramHTTPClient(settings telegramSettings) (*http.Client, error) {
	proxyURL := strings.TrimSpace(settings.ProxyURL)
	client := &http.Client{Timeout: 15 * time.Second}
	if proxyURL == "" {
		return client, nil
	}
	if err := validateProxyURL(proxyURL); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
		transport.Proxy = http.ProxyURL(parsed)
	}
	client.Transport = transport
	return client, nil
}

func (s *Server) notifyTelegramJobTerminal(j *job.Job) {
	snapshot := j.Snapshot()
	if !isTelegramTerminalStatus(snapshot.Status) {
		return
	}
	if _, loaded := s.telegramNotifications.LoadOrStore(snapshot.ID, struct{}{}); loaded {
		return
	}
	logs := j.Logs()
	go func() {
		_ = s.sendTelegramJobTerminalNotification(context.Background(), &snapshot, logs)
	}()
}

func (s *Server) sendTelegramJobTerminalNotification(ctx context.Context, snapshot *job.Job, logs []job.LogEntry) error {
	settings, err := s.loadTelegramSettings()
	if err != nil {
		return err
	}
	if strings.TrimSpace(settings.BotToken) == "" || strings.TrimSpace(settings.ChatID) == "" {
		return nil
	}
	text := formatTelegramJobNotification(snapshot, logs, s.cfg.Version)
	return sendTelegramMessage(ctx, settings, text)
}

func isTelegramTerminalStatus(status job.Status) bool {
	return status == job.StatusDone || status == job.StatusFailed || status == job.StatusStopped
}

func formatTelegramJobNotification(snapshot *job.Job, logs []job.LogEntry, version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "unknown"
	}
	lines := []string{
		"Emby Migrator 任务通知",
		"任务类型：" + telegramJobTypeLabel(snapshot.Type),
		"任务状态：" + telegramStatusLabel(snapshot.Status),
	}
	if duration := formatTelegramJobDuration(snapshot.StartedAt, snapshot.EndedAt); duration != "" {
		lines = append(lines, "用时："+duration)
	}
	if source, target := telegramJobServers(snapshot.Result); source != "" || target != "" {
		if source != "" {
			lines = append(lines, "源服务器："+source)
		}
		if target != "" {
			lines = append(lines, "目标服务器："+target)
		}
	}
	if summary := telegramJobSummary(snapshot, logs); summary != "" {
		lines = append(lines, "摘要："+summary)
	}
	if snapshot.Error != "" {
		lines = append(lines, "错误："+truncateTelegramText(telegramFriendlyError(snapshot.Error), 500))
	}
	lines = append(lines, "版本："+version)
	return truncateTelegramText(strings.Join(lines, "\n"), 3900)
}

func telegramFriendlyError(message string) string {
	message = strings.TrimSpace(message)
	if strings.Contains(message, "export package not found") {
		parts := strings.SplitN(message, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			return "导出包不存在或已失效，请刷新导出包列表后重试：" + strings.TrimSpace(parts[1])
		}
		return "导出包不存在或已失效，请刷新导出包列表后重试。"
	}
	return message
}

func telegramJobServers(result any) (string, string) {
	if result == nil {
		return "", ""
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return "", ""
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return "", ""
	}
	source := telegramServerValue(nestedTelegramMap(root, "manifest"), "source")
	if source == "" {
		source = telegramServerValue(root, "source")
	}
	target := telegramServerValue(nestedTelegramMap(nestedTelegramMap(root, "report"), "target"), "baseUrl")
	if target == "" {
		target = telegramServerValue(nestedTelegramMap(root, "target"), "baseUrl")
	}
	return source, target
}

func telegramServerValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func telegramJobTypeLabel(jobType string) string {
	switch strings.ToLower(strings.TrimSpace(jobType)) {
	case "export":
		return "导出"
	case "import":
		return "导入"
	case "import-precheck":
		return "导入预检"
	default:
		return emptyTelegramField(jobType)
	}
}

func telegramStatusLabel(status job.Status) string {
	switch status {
	case job.StatusQueued:
		return "排队中"
	case job.StatusRunning:
		return "运行中"
	case job.StatusPaused:
		return "已暂停"
	case job.StatusDone:
		return "完成"
	case job.StatusFailed:
		return "失败"
	case job.StatusStopped:
		return "已中止"
	default:
		return emptyTelegramField(string(status))
	}
}

func formatTelegramJobDuration(start, end time.Time) string {
	if start.IsZero() {
		return ""
	}
	if end.IsZero() {
		end = time.Now()
	}
	duration := end.Sub(start)
	if duration < 0 {
		return ""
	}
	if duration < time.Second {
		return duration.Round(time.Millisecond).String()
	}
	return duration.Round(time.Second).String()
}

func telegramJobSummary(snapshot *job.Job, logs []job.LogEntry) string {
	if summary := telegramResultSummary(snapshot.Result); summary != "" {
		return summary
	}
	for i := len(logs) - 1; i >= 0; i-- {
		message := strings.TrimSpace(logs[i].Message)
		if message == "" || message == strings.TrimSpace(snapshot.Error) {
			continue
		}
		return truncateTelegramText(message, 500)
	}
	message := strings.TrimSpace(snapshot.Message)
	if message != "" && message != strings.TrimSpace(snapshot.Error) {
		return truncateTelegramText(message, 500)
	}
	return ""
}

func telegramResultSummary(result any) string {
	if result == nil {
		return ""
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return ""
	}
	for _, summary := range []map[string]any{
		nestedTelegramMap(root, "summary"),
		nestedTelegramMap(nestedTelegramMap(root, "manifest"), "summary"),
		nestedTelegramMap(nestedTelegramMap(root, "report"), "summary"),
	} {
		if formatted := formatTelegramSummary(summary); formatted != "" {
			return formatted
		}
	}
	return ""
}

func nestedTelegramMap(values map[string]any, key string) map[string]any {
	if values == nil {
		return nil
	}
	nested, _ := values[key].(map[string]any)
	return nested
}

func formatTelegramSummary(summary map[string]any) string {
	if len(summary) == 0 {
		return ""
	}
	fields := []string{
		"libraries",
		"items",
		"people",
		"itemImages",
		"peopleImages",
		"errors",
		"matched",
		"unmatched",
		"ambiguous",
		"metadataUpdated",
		"itemImagesPushed",
		"itemImagesFailed",
		"peopleImagesFailed",
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		value, ok := summary[field]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", telegramSummaryFieldLabel(field), telegramSummaryValue(value)))
	}
	return strings.Join(parts, " ")
}

func telegramSummaryFieldLabel(field string) string {
	switch field {
	case "libraries":
		return "媒体库"
	case "items":
		return "项目"
	case "people":
		return "人物"
	case "itemImages":
		return "媒体图片"
	case "peopleImages":
		return "人物头像"
	case "errors":
		return "错误"
	case "matched":
		return "已匹配"
	case "unmatched":
		return "未匹配"
	case "ambiguous":
		return "歧义"
	case "metadataUpdated":
		return "元数据成功"
	case "itemImagesPushed":
		return "媒体图片成功"
	case "itemImagesFailed":
		return "媒体图片失败"
	case "peopleImagesFailed":
		return "人物头像失败"
	default:
		return field
	}
}

func telegramSummaryValue(value any) string {
	switch typed := value.(type) {
	case float64:
		return fmt.Sprintf("%.0f", typed)
	case float32:
		return fmt.Sprintf("%.0f", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case int32:
		return fmt.Sprintf("%d", typed)
	case json.Number:
		return typed.String()
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func emptyTelegramField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func truncateTelegramText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func telegramErrorDescription(data []byte) string {
	var payload struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Description)
}

func sanitizeTelegramError(message, token, chatID string) string {
	message = strings.TrimSpace(message)
	token = strings.TrimSpace(token)
	if token != "" {
		message = strings.ReplaceAll(message, token, maskTelegramToken(token))
	}
	chatID = strings.TrimSpace(chatID)
	if chatID != "" {
		message = strings.ReplaceAll(message, chatID, maskTelegramChatID(chatID))
	}
	return message
}

func maskTelegramChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	if len(chatID) <= 4 {
		return "****"
	}
	return "****" + chatID[len(chatID)-4:]
}
