package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"emby-migrator/internal/config"
	"emby-migrator/internal/emby"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
)

type Server struct {
	cfg                   config.Config
	jobs                  *job.Manager
	exporter              *exporter.Service
	sessionSecret         []byte
	usersCache            cachedUsersConfig
	telegramNotifications sync.Map
}

type connectionRequest struct {
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"`
	ProfileID string `json:"profileId"`
}

type connectionResponse struct {
	OK          bool            `json:"ok"`
	Server      emby.SystemInfo `json:"server"`
	MaskedKey   string          `json:"maskedKey"`
	ToolVersion string          `json:"toolVersion"`
}

type librariesRequest struct {
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"`
	ProfileID string `json:"profileId"`
}

type exportRequest struct {
	BaseURL             string         `json:"baseUrl"`
	APIKey              string         `json:"apiKey"`
	ProfileID           string         `json:"profileId"`
	SourceProfileID     string         `json:"sourceProfileId"`
	Libraries           []emby.Library `json:"libraries"`
	LibraryIDs          []string       `json:"libraryIds"`
	Concurrency         int            `json:"concurrency"`
	SkipImages          bool           `json:"skipImages"`
	IncludePeopleImages bool           `json:"includePeopleImages"`
	Incremental         bool           `json:"incremental"`
	Overwrite           bool           `json:"overwrite"`
	ImageTypes          []string       `json:"imageTypes"`
}

type importRequest struct {
	BaseURL             string   `json:"baseUrl"`
	APIKey              string   `json:"apiKey"`
	ProfileID           string   `json:"profileId"`
	TargetProfileID     string   `json:"targetProfileId"`
	ExportPath          string   `json:"exportPath"`
	TargetLibraryIDs    []string `json:"targetLibraryIds"`
	LibraryIDs          []string `json:"libraryIds"`
	Concurrency         int      `json:"concurrency"`
	DryRun              bool     `json:"dryRun"`
	SkipImages          bool     `json:"skipImages"`
	IncludePeopleImages bool     `json:"includePeopleImages"`
	Overwrite           bool     `json:"overwrite"`
	Resume              bool     `json:"resume"`
	ImageTypes          []string `json:"imageTypes"`
}

func NewServer(cfg config.Config, jobs *job.Manager, exporter *exporter.Service) *Server {
	return &Server{cfg: cfg, jobs: jobs, exporter: exporter, sessionSecret: makeSessionSecret(cfg.SessionSecret)}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	mux.Handle("POST /api/auth/password", s.requireAuth(http.HandlerFunc(s.handleAuthPasswordChange)))
	mux.Handle("POST /api/connection/test", s.requireRole(roleOperator, http.HandlerFunc(s.handleConnectionTest)))
	mux.Handle("POST /api/libraries", s.requireRole(roleOperator, http.HandlerFunc(s.handleLibraries)))
	mux.Handle("GET /api/settings/app", s.requireRole(roleViewer, http.HandlerFunc(s.handleAppSettingsGet)))
	mux.Handle("POST /api/settings/app", s.requireRole(roleAdmin, http.HandlerFunc(s.handleAppSettingsSave)))
	mux.Handle("GET /api/settings/profiles", s.requireRole(roleViewer, http.HandlerFunc(s.handleAppProfilesList)))
	mux.Handle("POST /api/settings/profiles", s.requireRole(roleAdmin, http.HandlerFunc(s.handleAppProfileSave)))
	mux.Handle("DELETE /api/settings/profiles/{id}", s.requireRole(roleAdmin, http.HandlerFunc(s.handleAppProfileDelete)))
	mux.Handle("POST /api/settings/profiles/select", s.requireRole(roleAdmin, http.HandlerFunc(s.handleAppProfileSelect)))
	mux.Handle("GET /api/exports", s.requireRole(roleViewer, http.HandlerFunc(s.handleExports)))
	mux.Handle("GET /api/import-reports", s.requireRole(roleViewer, http.HandlerFunc(s.handleImportReports)))
	mux.Handle("GET /api/import-reports/download", s.requireRole(roleViewer, http.HandlerFunc(s.handleImportReportDownload)))
	mux.Handle("GET /api/settings/telegram", s.requireRole(roleViewer, http.HandlerFunc(s.handleTelegramSettingsGet)))
	mux.Handle("POST /api/settings/telegram", s.requireRole(roleAdmin, http.HandlerFunc(s.handleTelegramSettingsSave)))
	mux.Handle("POST /api/settings/telegram/test", s.requireRole(roleAdmin, http.HandlerFunc(s.handleTelegramSettingsTest)))
	mux.HandleFunc("GET /api/users", s.handleUsersNotFound)
	mux.HandleFunc("POST /api/users", s.handleUsersNotFound)
	mux.Handle("POST /api/jobs/export", s.requireRole(roleOperator, http.HandlerFunc(s.handleExportJob)))
	mux.Handle("POST /api/jobs/import", s.requireRole(roleOperator, http.HandlerFunc(s.handleImportJob)))
	mux.Handle("POST /api/jobs/import/precheck", s.requireRole(roleOperator, http.HandlerFunc(s.handleImportPrecheckJob)))
	mux.Handle("GET /api/jobs", s.requireRole(roleViewer, http.HandlerFunc(s.handleJobs)))
	mux.Handle("GET /api/jobs/{id}", s.requireRole(roleViewer, http.HandlerFunc(s.handleJob)))
	mux.Handle("POST /api/jobs/{id}/pause", s.requireRole(roleOperator, http.HandlerFunc(s.handlePauseJob)))
	mux.Handle("POST /api/jobs/{id}/resume", s.requireRole(roleOperator, http.HandlerFunc(s.handleResumeJob)))
	mux.Handle("POST /api/jobs/{id}/stop", s.requireRole(roleOperator, http.HandlerFunc(s.handleStopJob)))
	mux.Handle("GET /api/jobs/{id}/logs", s.requireRole(roleViewer, http.HandlerFunc(s.handleJobLogs)))
	mux.Handle("GET /api/jobs/{id}/logs.txt", s.requireRole(roleViewer, http.HandlerFunc(s.handleJobLogDownload)))
	mux.Handle("/", http.FileServer(http.Dir("web")))
	return recoverJSON(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"toolVersion": s.cfg.Version,
		"time":        beijingTime(time.Now()).Format(time.RFC3339),
	})
}

func (s *Server) handleConnectionTest(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	connection, err := s.resolveEmbyConnection(req.BaseURL, req.APIKey, req.ProfileID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	client, err := emby.NewClient(connection.BaseURL, connection.APIKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	info, err := client.SystemInfo(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionResponse{OK: true, Server: info, MaskedKey: emby.MaskAPIKey(connection.APIKey), ToolVersion: s.cfg.Version})
}

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	var req librariesRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	connection, err := s.resolveEmbyConnection(req.BaseURL, req.APIKey, req.ProfileID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	client, err := emby.NewClient(connection.BaseURL, connection.APIKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	libraries, err := client.Libraries(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": libraries})
}

func (s *Server) handleExports(w http.ResponseWriter, r *http.Request) {
	exports, err := s.exporter.ListExports()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exports": exports})
}

func (s *Server) handleImportReports(w http.ResponseWriter, r *http.Request) {
	exportPath := strings.TrimSpace(r.URL.Query().Get("exportPath"))
	if exportPath == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("exportPath is required"))
		return
	}
	reports, err := s.exporter.ListImportReports(exportPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
}

func (s *Server) handleImportReportDownload(w http.ResponseWriter, r *http.Request) {
	exportPath := strings.TrimSpace(r.URL.Query().Get("exportPath"))
	reportName := strings.TrimSpace(r.URL.Query().Get("name"))
	if exportPath == "" || reportName == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("exportPath and name are required"))
		return
	}
	reportPath, err := s.exporter.ImportReportPath(exportPath, reportName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(reportPath)))
	http.ServeFile(w, r, reportPath)
}

func (s *Server) handleExportJob(w http.ResponseWriter, r *http.Request) {
	var req exportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	connection, err := s.resolveEmbyConnection(req.BaseURL, req.APIKey, coalesceProfileID(req.ProfileID, req.SourceProfileID))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	j := s.jobs.Enqueue("export", func(j *job.Job) {
		defer s.notifyTelegramJobTerminal(j)
		j.Log("info", "开始导出任务")
		result, err := s.exporter.Export(j.Context(), j, exporter.ExportRequest{
			Connection:          connection,
			Libraries:           req.Libraries,
			LibraryIDs:          req.LibraryIDs,
			Concurrency:         req.Concurrency,
			SkipImages:          req.SkipImages,
			IncludePeopleImages: req.IncludePeopleImages,
			Incremental:         req.Incremental,
			Overwrite:           req.Overwrite,
			ImageTypes:          req.ImageTypes,
			ToolVersion:         s.cfg.Version,
		})
		if err != nil {
			j.Fail(err)
			return
		}
		if len(result.Manifest.Errors) > 0 {
			j.FailWithResult(
				fmt.Errorf("导出未完全成功：%d 个错误，导出包已保留：%s", len(result.Manifest.Errors), result.Path),
				result,
			)
			return
		}
		j.Complete(result)
	})
	writeJSON(w, http.StatusAccepted, j.Snapshot())
}

func (s *Server) handleImportJob(w http.ResponseWriter, r *http.Request) {
	s.startImportJob(w, r, false)
}

func (s *Server) handleImportPrecheckJob(w http.ResponseWriter, r *http.Request) {
	s.startImportJob(w, r, true)
}

func (s *Server) startImportJob(w http.ResponseWriter, r *http.Request, forceDryRun bool) {
	var req importRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ExportPath) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("exportPath is required"))
		return
	}
	if _, _, err := s.exporter.ResolveExportPath(req.ExportPath); err != nil {
		writeError(w, http.StatusBadRequest, friendlyExportPathError(req.ExportPath, err))
		return
	}
	if forceDryRun {
		req.DryRun = true
	}
	connection, err := s.resolveEmbyConnection(req.BaseURL, req.APIKey, coalesceProfileID(req.ProfileID, req.TargetProfileID))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	jobType := "import"
	startMessage := "开始导入任务"
	if req.DryRun {
		jobType = "import-precheck"
		startMessage = "开始导入预检任务"
	}
	j := s.jobs.Enqueue(jobType, func(j *job.Job) {
		defer s.notifyTelegramJobTerminal(j)
		j.Log("info", startMessage)
		result, err := s.exporter.Import(j.Context(), j, exporter.ImportRequest{
			Connection:          connection,
			ExportPath:          filepath.Clean(req.ExportPath),
			TargetLibraryIDs:    req.TargetLibraryIDs,
			LibraryIDs:          req.LibraryIDs,
			Concurrency:         req.Concurrency,
			DryRun:              req.DryRun,
			SkipImages:          req.SkipImages,
			IncludePeopleImages: req.IncludePeopleImages,
			Overwrite:           req.Overwrite,
			Resume:              req.Resume,
			ImageTypes:          req.ImageTypes,
			ToolVersion:         s.cfg.Version,
		})
		if err != nil {
			j.Fail(err)
			return
		}
		j.Complete(result)
	})
	writeJSON(w, http.StatusAccepted, j.Snapshot())
}

func friendlyExportPathError(exportPath string, err error) error {
	message := strings.TrimSpace(err.Error())
	if strings.Contains(message, "export package not found") {
		return fmt.Errorf("导出包不存在或已失效，请刷新导出包列表后重试：%s", strings.TrimSpace(exportPath))
	}
	return fmt.Errorf("导出包不可用：%s", message)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.List()})
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
		return
	}
	writeJSON(w, http.StatusOK, j.Snapshot())
}

func (s *Server) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
		return
	}
	paused := j.Pause()
	writeJSON(w, http.StatusOK, map[string]any{
		"paused": paused,
		"job":    j.Snapshot(),
	})
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
		return
	}
	resumed := j.Resume()
	writeJSON(w, http.StatusOK, map[string]any{
		"resumed": resumed,
		"job":     j.Snapshot(),
	})
}

func (s *Server) handleStopJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
		return
	}
	stopped := j.Stop()
	if stopped {
		s.notifyTelegramJobTerminal(j)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stopped": stopped,
		"job":     j.Snapshot(),
	})
}

func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming is not supported"))
		return
	}
	ch, unsubscribe := j.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-r.Context().Done():
			return
		case entry, ok := <-ch:
			if !ok {
				data, _ := json.Marshal(j.Snapshot())
				fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
				fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(entry)
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleJobLogDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("job not found"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"emby-migrator-job-%s.log\"", id))
	for _, entry := range j.Logs() {
		fmt.Fprintf(w, "%s 北京时间 [%s] %s\n", beijingTime(entry.Time).Format("2006-01-02 15:04:05"), entry.Level, entry.Message)
	}
}

func beijingTime(value time.Time) time.Time {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return value.Local()
	}
	return value.In(location)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func recoverJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("%v", recovered))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
