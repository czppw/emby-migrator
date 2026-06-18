package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"emby-migrator/internal/emby"
)

const appSettingsFileName = "settings.json"

type appSettings struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Connection    appConnectionSettings      `json:"connection,omitempty"`
	Defaults      appTaskDefaultSettings     `json:"defaults,omitempty"`
	Profiles      []appServerProfileSettings `json:"profiles,omitempty"`
	CurrentSource string                     `json:"currentSource,omitempty"`
	CurrentTarget string                     `json:"currentTarget,omitempty"`
	UpdatedAt     string                     `json:"updatedAt,omitempty"`
}

type appConnectionSettings struct {
	BaseURL string `json:"baseUrl,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
}

type appServerProfileSettings struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	BaseURL   string `json:"baseUrl,omitempty"`
	APIKey    string `json:"apiKey,omitempty"`
	Role      string `json:"role,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type appTaskDefaultSettings struct {
	Export appTaskOptions `json:"export,omitempty"`
	Import appTaskOptions `json:"import,omitempty"`
}

type appTaskOptions struct {
	Concurrency         int      `json:"concurrency,omitempty"`
	SkipImages          bool     `json:"skipImages"`
	IncludePeopleImages bool     `json:"includePeopleImages"`
	Incremental         bool     `json:"incremental,omitempty"`
	Overwrite           bool     `json:"overwrite"`
	ImageTypes          []string `json:"imageTypes,omitempty"`
}

type appSettingsRequest struct {
	Connection    appConnectionSettings    `json:"connection"`
	Defaults      appTaskDefaultSettings   `json:"defaults"`
	Profile       appServerProfileSettings `json:"profile"`
	CurrentSource string                   `json:"currentSource"`
	CurrentTarget string                   `json:"currentTarget"`
}

type appSettingsResponse struct {
	OK            bool                        `json:"ok"`
	Configured    bool                        `json:"configured"`
	Connection    appConnectionSettingsMasked `json:"connection"`
	Defaults      appTaskDefaultSettings      `json:"defaults"`
	Profiles      []appServerProfileResponse  `json:"profiles,omitempty"`
	CurrentSource string                      `json:"currentSource,omitempty"`
	CurrentTarget string                      `json:"currentTarget,omitempty"`
}

type appConnectionSettingsMasked struct {
	BaseURL      string `json:"baseUrl,omitempty"`
	HasAPIKey    bool   `json:"hasApiKey"`
	APIKeyMasked string `json:"apiKeyMasked,omitempty"`
}

type appServerProfileResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	BaseURL      string `json:"baseUrl,omitempty"`
	Role         string `json:"role,omitempty"`
	HasAPIKey    bool   `json:"hasApiKey"`
	APIKeyMasked string `json:"apiKeyMasked,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type appProfileSaveRequest struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Role    string `json:"role"`
}

type appProfileSelectRequest struct {
	CurrentSource   string `json:"currentSource"`
	CurrentTarget   string `json:"currentTarget"`
	SourceProfileID string `json:"sourceProfileId"`
	TargetProfileID string `json:"targetProfileId"`
}

func (s *Server) handleAppSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.loadAppSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, appSettingsToResponse(settings))
}

func (s *Server) handleAppSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req appSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	settings, err := s.settingsFromRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.saveAppSettings(settings); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, appSettingsToResponse(settings))
}

func (s *Server) handleAppProfilesList(w http.ResponseWriter, r *http.Request) {
	settings, err := s.loadAppSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, appSettingsToResponse(settings))
}

func (s *Server) handleAppProfileSave(w http.ResponseWriter, r *http.Request) {
	var req appProfileSaveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	settings, err := s.saveProfileFromRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, appSettingsToResponse(settings))
}

func (s *Server) handleAppProfileDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("profile id is required"))
		return
	}
	settings, err := s.deleteProfile(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, appSettingsToResponse(settings))
}

func (s *Server) handleAppProfileSelect(w http.ResponseWriter, r *http.Request) {
	var req appProfileSelectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	settings, err := s.selectProfiles(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, appSettingsToResponse(settings))
}

func (s *Server) resolveEmbyConnection(baseURL, apiKey, profileID string) (emby.Connection, error) {
	baseURL = strings.TrimSpace(baseURL)
	apiKey = strings.TrimSpace(apiKey)
	profileID = strings.TrimSpace(profileID)
	if profileID != "" || apiKey == "" || baseURL == "" {
		saved, err := s.loadAppSettings()
		if err != nil {
			return emby.Connection{}, err
		}
		if profileID != "" {
			profile, ok := findProfileByID(saved.Profiles, profileID)
			if !ok {
				return emby.Connection{}, fmt.Errorf("server profile not found")
			}
			baseURL = profile.BaseURL
			apiKey = profile.APIKey
		} else {
			if baseURL == "" {
				baseURL = saved.Connection.BaseURL
			}
			if apiKey == "" {
				normalizedRequest := ""
				if baseURL != "" {
					if normalized, err := emby.NormalizeBaseURL(baseURL); err == nil {
						normalizedRequest = normalized
					}
				}
				if normalizedRequest == "" || normalizedRequest == saved.Connection.BaseURL {
					apiKey = saved.Connection.APIKey
				}
				if apiKey == "" && normalizedRequest != "" {
					apiKey = savedAPIKeyForBaseURL(saved, normalizedRequest)
				}
			}
		}
	}
	if strings.TrimSpace(baseURL) == "" {
		return emby.Connection{}, fmt.Errorf("请填写 Emby 地址，或先保存连接配置")
	}
	if strings.TrimSpace(apiKey) == "" {
		return emby.Connection{}, fmt.Errorf("请填写 Emby API Key，或先保存连接配置")
	}
	normalized, err := emby.NormalizeBaseURL(baseURL)
	if err != nil {
		return emby.Connection{}, err
	}
	return emby.Connection{BaseURL: normalized, APIKey: apiKey}, nil
}

func (s *Server) settingsFromRequest(req appSettingsRequest) (appSettings, error) {
	current, err := s.loadAppSettings()
	if err != nil {
		return appSettings{}, err
	}
	baseURL := strings.TrimSpace(req.Connection.BaseURL)
	if baseURL == "" {
		return appSettings{}, fmt.Errorf("请填写 Emby 地址")
	}
	normalizedBaseURL, err := emby.NormalizeBaseURL(baseURL)
	if err != nil {
		return appSettings{}, err
	}
	apiKey := strings.TrimSpace(req.Connection.APIKey)
	if apiKey == "" {
		apiKey = savedAPIKeyForProfileSave(current, normalizedBaseURL)
	}
	if apiKey == "" {
		return appSettings{}, fmt.Errorf("请填写 Emby API Key")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	current.SchemaVersion = 2
	current.Connection = appConnectionSettings{
		BaseURL: normalizedBaseURL,
		APIKey:  apiKey,
	}
	current.Defaults = normalizeAppDefaults(req.Defaults)
	current.UpdatedAt = now
	current.CurrentSource = coalesceProfileID(req.CurrentSource, current.CurrentSource)
	current.CurrentTarget = coalesceProfileID(req.CurrentTarget, current.CurrentTarget)
	current.Profiles = normalizeProfiles(current.Profiles)
	if !appSettingsProfilePresent(req.Profile) {
		req.Profile = appServerProfileSettings{
			ID:      profileIDFromBaseURL(normalizedBaseURL),
			Name:    defaultProfileName(normalizedBaseURL),
			BaseURL: normalizedBaseURL,
			Role:    "source-target",
		}
	}
	if appSettingsProfilePresent(req.Profile) {
		profileReq := appProfileSaveRequest{
			ID:      req.Profile.ID,
			Name:    req.Profile.Name,
			BaseURL: req.Profile.BaseURL,
			APIKey:  req.Profile.APIKey,
			Role:    req.Profile.Role,
		}
		if strings.TrimSpace(profileReq.BaseURL) == "" {
			profileReq.BaseURL = normalizedBaseURL
		}
		if strings.TrimSpace(profileReq.APIKey) == "" {
			if profileBaseURL, err := emby.NormalizeBaseURL(profileReq.BaseURL); err == nil && profileBaseURL == normalizedBaseURL {
				profileReq.APIKey = apiKey
			}
		}
		profile, err := normalizedProfileFromSave(profileReq, current.Profiles, now)
		if err != nil {
			return appSettings{}, err
		}
		current.Profiles = upsertProfile(current.Profiles, profile)
		current.CurrentSource = preferCurrentProfile(current.CurrentSource, profile.ID)
		current.CurrentTarget = preferCurrentProfile(current.CurrentTarget, profile.ID)
	}
	current.ensureProfileSelections()
	return current, nil
}

func normalizeAppDefaults(defaults appTaskDefaultSettings) appTaskDefaultSettings {
	defaults.Export = normalizeAppTaskOptions(defaults.Export)
	defaults.Import = normalizeAppTaskOptions(defaults.Import)
	return defaults
}

func normalizeAppTaskOptions(options appTaskOptions) appTaskOptions {
	if options.Concurrency < 1 {
		options.Concurrency = 4
	}
	options.ImageTypes = normalizeImageTypes(options.ImageTypes)
	return options
}

func normalizeImageTypes(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	if len(out) == 0 {
		return append([]string(nil), emby.DefaultImageTypes...)
	}
	return out
}

func (s *Server) loadAppSettings() (appSettings, error) {
	data, err := os.ReadFile(s.appSettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return appSettings{}, nil
		}
		return appSettings{}, fmt.Errorf("读取应用配置失败：%w", err)
	}
	var settings appSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return appSettings{}, fmt.Errorf("解析应用配置失败：%w", err)
	}
	settings.Connection.BaseURL = strings.TrimSpace(settings.Connection.BaseURL)
	settings.Connection.APIKey = strings.TrimSpace(settings.Connection.APIKey)
	settings.Defaults = normalizeAppDefaults(settings.Defaults)
	settings.Profiles = normalizeProfiles(settings.Profiles)
	if settings.SchemaVersion < 2 {
		settings.migrateLegacyProfile()
	}
	settings.ensureProfileSelections()
	return settings, nil
}

func (s *Server) saveAppSettings(settings appSettings) error {
	configDir := s.configDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败：%w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("编码应用配置失败：%w", err)
	}
	if err := os.WriteFile(s.appSettingsPath(), data, 0o600); err != nil {
		return fmt.Errorf("保存应用配置失败：%w", err)
	}
	return nil
}

func (s *Server) appSettingsPath() string {
	return filepath.Join(s.configDir(), appSettingsFileName)
}

func appSettingsToResponse(settings appSettings) appSettingsResponse {
	apiKey := strings.TrimSpace(settings.Connection.APIKey)
	profiles := profilesToResponse(settings.Profiles)
	configured := settings.SchemaVersion > 0 || strings.TrimSpace(settings.Connection.BaseURL) != "" || apiKey != "" || len(profiles) > 0
	defaults := appTaskDefaultSettings{}
	if configured {
		defaults = normalizeAppDefaults(settings.Defaults)
	}
	return appSettingsResponse{
		OK:         true,
		Configured: configured,
		Connection: appConnectionSettingsMasked{
			BaseURL:      strings.TrimSpace(settings.Connection.BaseURL),
			HasAPIKey:    apiKey != "",
			APIKeyMasked: emby.MaskAPIKey(apiKey),
		},
		Defaults:      defaults,
		Profiles:      profiles,
		CurrentSource: settings.CurrentSource,
		CurrentTarget: settings.CurrentTarget,
	}
}

func (s *Server) saveProfileFromRequest(req appProfileSaveRequest) (appSettings, error) {
	settings, err := s.loadAppSettings()
	if err != nil {
		return appSettings{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.APIKey == "" {
		if normalizedBaseURL, err := emby.NormalizeBaseURL(req.BaseURL); err == nil {
			req.APIKey = savedAPIKeyForProfileSave(settings, normalizedBaseURL)
		}
	}
	profile, err := normalizedProfileFromSave(req, settings.Profiles, now)
	if err != nil {
		return appSettings{}, err
	}
	settings.SchemaVersion = 2
	settings.Profiles = upsertProfile(settings.Profiles, profile)
	if settings.Connection.BaseURL == "" || settings.Connection.APIKey == "" {
		settings.Connection = appConnectionSettings{BaseURL: profile.BaseURL, APIKey: profile.APIKey}
	}
	if settings.CurrentSource == "" {
		settings.CurrentSource = profile.ID
	}
	if settings.CurrentTarget == "" {
		settings.CurrentTarget = profile.ID
	}
	settings.UpdatedAt = now
	settings.Profiles = normalizeProfiles(settings.Profiles)
	settings.ensureProfileSelections()
	if err := s.saveAppSettings(settings); err != nil {
		return appSettings{}, err
	}
	return settings, nil
}

func (s *Server) deleteProfile(id string) (appSettings, error) {
	settings, err := s.loadAppSettings()
	if err != nil {
		return appSettings{}, err
	}
	index := -1
	for i, profile := range settings.Profiles {
		if profile.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return appSettings{}, fmt.Errorf("server profile not found")
	}
	settings.Profiles = append(settings.Profiles[:index], settings.Profiles[index+1:]...)
	if settings.CurrentSource == id {
		settings.CurrentSource = ""
	}
	if settings.CurrentTarget == id {
		settings.CurrentTarget = ""
	}
	settings.SchemaVersion = 2
	settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	settings.ensureProfileSelections()
	if err := s.saveAppSettings(settings); err != nil {
		return appSettings{}, err
	}
	return settings, nil
}

func (s *Server) selectProfiles(req appProfileSelectRequest) (appSettings, error) {
	settings, err := s.loadAppSettings()
	if err != nil {
		return appSettings{}, err
	}
	sourceID := coalesceProfileID(req.CurrentSource, req.SourceProfileID)
	targetID := coalesceProfileID(req.CurrentTarget, req.TargetProfileID)
	if sourceID != "" {
		if _, ok := findProfileByID(settings.Profiles, sourceID); !ok {
			return appSettings{}, fmt.Errorf("source server profile not found")
		}
		settings.CurrentSource = sourceID
	}
	if targetID != "" {
		if _, ok := findProfileByID(settings.Profiles, targetID); !ok {
			return appSettings{}, fmt.Errorf("target server profile not found")
		}
		settings.CurrentTarget = targetID
	}
	settings.SchemaVersion = 2
	settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	settings.ensureProfileSelections()
	if err := s.saveAppSettings(settings); err != nil {
		return appSettings{}, err
	}
	return settings, nil
}

func normalizedProfileFromSave(req appProfileSaveRequest, existing []appServerProfileSettings, now string) (appServerProfileSettings, error) {
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		return appServerProfileSettings{}, fmt.Errorf("请填写 Emby 地址")
	}
	normalizedBaseURL, err := emby.NormalizeBaseURL(baseURL)
	if err != nil {
		return appServerProfileSettings{}, err
	}
	id := strings.TrimSpace(req.ID)
	var current appServerProfileSettings
	if id != "" {
		if profile, ok := findProfileByID(existing, id); ok {
			current = profile
		}
	}
	if profile, ok := findProfileByBaseURL(existing, normalizedBaseURL); ok {
		if current.ID == "" || current.BaseURL != normalizedBaseURL {
			current = profile
		}
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && current.BaseURL == normalizedBaseURL {
		apiKey = current.APIKey
	}
	if apiKey == "" {
		return appServerProfileSettings{}, fmt.Errorf("请填写 Emby API Key")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultProfileName(normalizedBaseURL)
	}
	if current.ID != "" && current.BaseURL == normalizedBaseURL {
		id = current.ID
	}
	if id == "" {
		id = uniqueProfileID(name, normalizedBaseURL, existing)
	}
	createdAt := current.CreatedAt
	if createdAt == "" {
		createdAt = now
	}
	return appServerProfileSettings{
		ID:        id,
		Name:      name,
		BaseURL:   normalizedBaseURL,
		APIKey:    apiKey,
		Role:      normalizeProfileRole(req.Role),
		CreatedAt: createdAt,
		UpdatedAt: now,
	}, nil
}

func normalizeProfiles(profiles []appServerProfileSettings) []appServerProfileSettings {
	out := make([]appServerProfileSettings, 0, len(profiles))
	seen := map[string]bool{}
	for _, profile := range profiles {
		profile.ID = strings.TrimSpace(profile.ID)
		profile.Name = strings.TrimSpace(profile.Name)
		profile.BaseURL = strings.TrimSpace(profile.BaseURL)
		profile.APIKey = strings.TrimSpace(profile.APIKey)
		profile.Role = normalizeProfileRole(profile.Role)
		if profile.ID == "" || profile.BaseURL == "" || profile.APIKey == "" || seen[profile.ID] {
			continue
		}
		if profile.Name == "" {
			profile.Name = defaultProfileName(profile.BaseURL)
		}
		seen[profile.ID] = true
		out = append(out, profile)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].Name < out[j].Name
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

func upsertProfile(profiles []appServerProfileSettings, profile appServerProfileSettings) []appServerProfileSettings {
	out := append([]appServerProfileSettings(nil), profiles...)
	for i := range out {
		if out[i].ID == profile.ID {
			out[i] = profile
			return out
		}
	}
	return append(out, profile)
}

func appSettingsProfilePresent(profile appServerProfileSettings) bool {
	return strings.TrimSpace(profile.Name) != "" ||
		strings.TrimSpace(profile.BaseURL) != "" ||
		strings.TrimSpace(profile.APIKey) != "" ||
		strings.TrimSpace(profile.Role) != ""
}

func profilesToResponse(profiles []appServerProfileSettings) []appServerProfileResponse {
	out := make([]appServerProfileResponse, 0, len(profiles))
	for _, profile := range profiles {
		apiKey := strings.TrimSpace(profile.APIKey)
		out = append(out, appServerProfileResponse{
			ID:           profile.ID,
			Name:         profile.Name,
			BaseURL:      profile.BaseURL,
			Role:         profile.Role,
			HasAPIKey:    apiKey != "",
			APIKeyMasked: emby.MaskAPIKey(apiKey),
			CreatedAt:    profile.CreatedAt,
			UpdatedAt:    profile.UpdatedAt,
		})
	}
	return out
}

func (settings *appSettings) migrateLegacyProfile() {
	if len(settings.Profiles) > 0 {
		return
	}
	baseURL := strings.TrimSpace(settings.Connection.BaseURL)
	apiKey := strings.TrimSpace(settings.Connection.APIKey)
	if baseURL == "" || apiKey == "" {
		return
	}
	now := settings.UpdatedAt
	if now == "" {
		now = time.Now().UTC().Format(time.RFC3339)
	}
	name := defaultProfileName(baseURL)
	settings.Profiles = []appServerProfileSettings{{
		ID:        uniqueProfileID(name, baseURL, nil),
		Name:      name,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Role:      "source-target",
		CreatedAt: now,
		UpdatedAt: now,
	}}
}

func (settings *appSettings) ensureProfileSelections() {
	if len(settings.Profiles) == 0 {
		settings.CurrentSource = ""
		settings.CurrentTarget = ""
		return
	}
	if !profileIDExists(settings.Profiles, settings.CurrentSource) {
		settings.CurrentSource = firstProfileID(settings.Profiles)
	}
	if !profileIDExists(settings.Profiles, settings.CurrentTarget) {
		settings.CurrentTarget = firstProfileID(settings.Profiles)
	}
}

func findProfileByID(profiles []appServerProfileSettings, id string) (appServerProfileSettings, bool) {
	id = strings.TrimSpace(id)
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return appServerProfileSettings{}, false
}

func findProfileByBaseURL(profiles []appServerProfileSettings, baseURL string) (appServerProfileSettings, bool) {
	baseURL = strings.TrimSpace(baseURL)
	for _, profile := range profiles {
		if profile.BaseURL == baseURL {
			return profile, true
		}
	}
	return appServerProfileSettings{}, false
}

func savedAPIKeyForBaseURL(settings appSettings, normalizedBaseURL string) string {
	normalizedBaseURL = strings.TrimSpace(normalizedBaseURL)
	if normalizedBaseURL == "" {
		return ""
	}
	if strings.TrimSpace(settings.Connection.BaseURL) == normalizedBaseURL {
		return strings.TrimSpace(settings.Connection.APIKey)
	}
	if profile, ok := findProfileByBaseURL(settings.Profiles, normalizedBaseURL); ok {
		return strings.TrimSpace(profile.APIKey)
	}
	return ""
}

func savedAPIKeyForProfileSave(settings appSettings, normalizedBaseURL string) string {
	return savedAPIKeyForBaseURL(settings, normalizedBaseURL)
}

func profileIDFromBaseURL(baseURL string) string {
	return slugifyProfileID(defaultProfileName(baseURL))
}

func preferCurrentProfile(currentID, profileID string) string {
	if strings.TrimSpace(currentID) != "" {
		return currentID
	}
	return strings.TrimSpace(profileID)
}

func profileIDExists(profiles []appServerProfileSettings, id string) bool {
	_, ok := findProfileByID(profiles, id)
	return ok
}

func firstProfileID(profiles []appServerProfileSettings) string {
	for _, profile := range profiles {
		return profile.ID
	}
	return ""
}

func normalizeProfileRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "source", "src":
		return "source"
	case "target", "dst", "dest", "destination":
		return "target"
	default:
		return "source-target"
	}
}

func coalesceProfileID(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func uniqueProfileID(name, baseURL string, existing []appServerProfileSettings) string {
	slug := slugifyProfileID(name)
	if slug == "" {
		slug = slugifyProfileID(defaultProfileName(baseURL))
	}
	if slug == "" {
		slug = "server"
	}
	used := map[string]bool{}
	for _, profile := range existing {
		used[profile.ID] = true
	}
	id := slug
	for i := 2; used[id]; i++ {
		id = fmt.Sprintf("%s-%d", slug, i)
	}
	return id
}

func slugifyProfileID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func defaultProfileName(baseURL string) string {
	if parsed, err := url.Parse(baseURL); err == nil && strings.TrimSpace(parsed.Host) != "" {
		return parsed.Host
	}
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "Emby Server"
	}
	return trimmed
}
