package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusPaused  Status = "paused"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusStopped Status = "stopped"
)

type Job struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	Message   string    `json:"message,omitempty"`
	Result    any       `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`

	ctx                 context.Context
	cancel              context.CancelFunc
	logs                []LogEntry
	logFile             *os.File
	logPath             string
	subs                map[chan LogEntry]struct{}
	paused              bool
	pause               chan struct{}
	maxMemoryLogEntries int
	mu                  sync.RWMutex
}

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type Manager struct {
	mu      sync.RWMutex
	jobs    map[string]*Job
	work    chan queuedWork
	options ManagerOptions
}

type ManagerOptions struct {
	LogDir                string
	MaxMemoryLogEntries   int
	MaxCompletedJobs      int
	CompletedJobRetention time.Duration
	ReleaseMemoryOnFinish bool
}

const (
	DefaultMaxMemoryLogEntries   = 2000
	DefaultMaxCompletedJobs      = 20
	DefaultCompletedJobRetention = 24 * time.Hour
)

type queuedWork struct {
	job *Job
	run func(*Job)
}

func NewManager() *Manager {
	return NewManagerWithOptions(ManagerOptions{})
}

func NewManagerWithOptions(options ManagerOptions) *Manager {
	options = normalizeManagerOptions(options)
	m := &Manager{
		jobs:    map[string]*Job{},
		work:    make(chan queuedWork, 128),
		options: options,
	}
	go m.runQueue()
	return m
}

func (m *Manager) Create(jobType string) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	m.mu.Lock()
	id := fmt.Sprintf("%d", now.UnixNano())
	for {
		if _, exists := m.jobs[id]; !exists {
			break
		}
		now = now.Add(time.Nanosecond)
		id = fmt.Sprintf("%d", now.UnixNano())
	}
	j := &Job{
		ID:                  id,
		Type:                jobType,
		Status:              StatusQueued,
		CreatedAt:           now,
		UpdatedAt:           now,
		ctx:                 ctx,
		cancel:              cancel,
		subs:                map[chan LogEntry]struct{}{},
		maxMemoryLogEntries: m.options.MaxMemoryLogEntries,
	}
	m.jobs[j.ID] = j
	m.mu.Unlock()
	if m.options.LogDir != "" {
		j.initLogFile(m.options.LogDir)
	}
	return j
}

func (m *Manager) Enqueue(jobType string, run func(*Job)) *Job {
	j := m.Create(jobType)
	m.work <- queuedWork{job: j, run: run}
	return j
}

func (m *Manager) Get(id string) (*Job, bool) {
	m.pruneCompleted()
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

func (m *Manager) List() []Job {
	m.pruneCompleted()
	m.mu.RLock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.RUnlock()

	out := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (m *Manager) runQueue() {
	for work := range m.work {
		if work.run == nil || !work.job.Start() {
			continue
		}
		work.run(work.job)
		m.afterWork()
	}
}

func (j *Job) Context() context.Context {
	return j.ctx
}

func (j *Job) Start() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if isTerminalStatus(j.Status) {
		return false
	}
	now := time.Now()
	j.Status = StatusRunning
	j.StartedAt = now
	j.UpdatedAt = now
	return true
}

func (j *Job) Complete(result any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if isTerminalStatus(j.Status) {
		return
	}
	now := time.Now()
	j.Status = StatusDone
	j.Result = result
	j.EndedAt = now
	j.UpdatedAt = now
	j.resumeLocked()
	j.publishLocked(LogEntry{Time: now, Level: "info", Message: "任务完成"})
	j.trimMemoryLogsLocked()
	j.closeSubsLocked()
	j.closeLogLocked()
}

func (j *Job) Fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if isTerminalStatus(j.Status) {
		return
	}
	now := time.Now()
	j.Status = StatusFailed
	j.Error = err.Error()
	j.EndedAt = now
	j.UpdatedAt = now
	j.resumeLocked()
	j.publishLocked(LogEntry{Time: now, Level: "error", Message: err.Error()})
	j.trimMemoryLogsLocked()
	j.closeSubsLocked()
	j.closeLogLocked()
}

func (j *Job) FailWithResult(err error, result any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if isTerminalStatus(j.Status) {
		return
	}
	now := time.Now()
	j.Status = StatusFailed
	j.Result = result
	j.Error = err.Error()
	j.EndedAt = now
	j.UpdatedAt = now
	j.resumeLocked()
	j.publishLocked(LogEntry{Time: now, Level: "error", Message: err.Error()})
	j.trimMemoryLogsLocked()
	j.closeSubsLocked()
	j.closeLogLocked()
}

func (j *Job) Stop() bool {
	j.cancel()
	j.mu.Lock()
	defer j.mu.Unlock()
	if isTerminalStatus(j.Status) {
		return false
	}
	now := time.Now()
	j.Status = StatusStopped
	j.EndedAt = now
	j.UpdatedAt = now
	j.resumeLocked()
	j.publishLocked(LogEntry{Time: now, Level: "warn", Message: "任务已停止"})
	j.trimMemoryLogsLocked()
	j.closeSubsLocked()
	j.closeLogLocked()
	return true
}

func (j *Job) Pause() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Status != StatusRunning {
		return false
	}
	now := time.Now()
	j.Status = StatusPaused
	j.paused = true
	if j.pause == nil {
		j.pause = make(chan struct{})
	}
	j.UpdatedAt = now
	j.publishLocked(LogEntry{Time: now, Level: "warn", Message: "任务已暂停，当前请求完成后停止派发新项目"})
	return true
}

func (j *Job) Resume() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Status != StatusPaused {
		return false
	}
	now := time.Now()
	j.Status = StatusRunning
	j.UpdatedAt = now
	j.resumeLocked()
	j.publishLocked(LogEntry{Time: now, Level: "info", Message: "任务已继续"})
	return true
}

func (j *Job) WaitIfPaused(ctx context.Context) error {
	for {
		j.mu.RLock()
		paused := j.paused && j.Status == StatusPaused
		pause := j.pause
		j.mu.RUnlock()
		if !paused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pause:
		}
	}
}

func (j *Job) Log(level, message string, args ...any) {
	if len(args) > 0 {
		message = fmt.Sprintf(message, args...)
	}
	entry := LogEntry{Time: time.Now(), Level: level, Message: message}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.publishLocked(entry)
	j.UpdatedAt = entry.Time
	j.Message = message
}

func (j *Job) Snapshot() Job {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return Job{
		ID:        j.ID,
		Type:      j.Type,
		Status:    j.Status,
		CreatedAt: j.CreatedAt,
		UpdatedAt: j.UpdatedAt,
		StartedAt: j.StartedAt,
		EndedAt:   j.EndedAt,
		Message:   j.Message,
		Result:    j.Result,
		Error:     j.Error,
	}
}

func (j *Job) Logs() []LogEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]LogEntry, len(j.logs))
	copy(out, j.logs)
	return out
}

func (j *Job) LogPath() (string, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.logPath == "" {
		return "", false
	}
	if _, err := os.Stat(j.logPath); err != nil {
		return "", false
	}
	return j.logPath, true
}

func (j *Job) Subscribe() (<-chan LogEntry, func()) {
	j.mu.Lock()
	ch := make(chan LogEntry, len(j.logs)+100)
	for _, entry := range j.logs {
		ch <- entry
	}
	if isTerminalStatus(j.Status) {
		close(ch)
		j.mu.Unlock()
		return ch, func() {}
	}
	j.subs[ch] = struct{}{}
	j.mu.Unlock()
	return ch, func() {
		j.mu.Lock()
		if _, ok := j.subs[ch]; ok {
			delete(j.subs, ch)
			close(ch)
		}
		j.mu.Unlock()
	}
}

func IsTerminalStatus(status Status) bool {
	return status == StatusDone || status == StatusFailed || status == StatusStopped
}

func isTerminalStatus(status Status) bool {
	return IsTerminalStatus(status)
}

func (j *Job) resumeLocked() {
	if j.pause != nil {
		close(j.pause)
		j.pause = nil
	}
	j.paused = false
}

func (j *Job) publishLocked(entry LogEntry) {
	j.logs = append(j.logs, entry)
	if j.shouldTrimMemoryLogsLocked() {
		j.trimMemoryLogsLocked()
	}
	if j.logFile != nil {
		_, _ = j.logFile.WriteString(formatLogEntry(entry))
	}
	for ch := range j.subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

func (j *Job) shouldTrimMemoryLogsLocked() bool {
	if j.maxMemoryLogEntries <= 0 || len(j.logs) <= j.maxMemoryLogEntries {
		return false
	}
	return len(j.logs) >= j.maxMemoryLogEntries*2 || len(j.logs)-j.maxMemoryLogEntries >= 100
}

func (j *Job) trimMemoryLogsLocked() {
	if j.maxMemoryLogEntries <= 0 || len(j.logs) <= j.maxMemoryLogEntries {
		return
	}
	trimmed := make([]LogEntry, j.maxMemoryLogEntries)
	copy(trimmed, j.logs[len(j.logs)-j.maxMemoryLogEntries:])
	j.logs = trimmed
}

func (j *Job) closeSubsLocked() {
	for ch := range j.subs {
		close(ch)
		delete(j.subs, ch)
	}
}

func (j *Job) initLogFile(logDir string) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(logDir, fmt.Sprintf("emby-migrator-job-%s.log", j.ID))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	j.mu.Lock()
	j.logPath = path
	j.logFile = file
	j.mu.Unlock()
}

func (j *Job) closeLogLocked() {
	if j.logFile == nil {
		return
	}
	_ = j.logFile.Close()
	j.logFile = nil
}

func formatLogEntry(entry LogEntry) string {
	return fmt.Sprintf("%s 北京时间 [%s] %s\n", beijingTime(entry.Time).Format("2006-01-02 15:04:05"), entry.Level, entry.Message)
}

func beijingTime(value time.Time) time.Time {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return value.Local()
	}
	return value.In(location)
}

func normalizeManagerOptions(options ManagerOptions) ManagerOptions {
	if options.MaxMemoryLogEntries <= 0 {
		options.MaxMemoryLogEntries = DefaultMaxMemoryLogEntries
	}
	if options.MaxCompletedJobs < 0 {
		options.MaxCompletedJobs = 0
	}
	if options.MaxCompletedJobs == 0 {
		options.MaxCompletedJobs = DefaultMaxCompletedJobs
	}
	if options.CompletedJobRetention < 0 {
		options.CompletedJobRetention = 0
	}
	if options.CompletedJobRetention == 0 {
		options.CompletedJobRetention = DefaultCompletedJobRetention
	}
	return options
}

func (m *Manager) afterWork() {
	m.pruneCompleted()
	if !m.options.ReleaseMemoryOnFinish {
		return
	}
	runtime.GC()
	debug.FreeOSMemory()
}

func (m *Manager) pruneCompleted() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneCompletedLocked(time.Now())
}

func (m *Manager) pruneCompletedLocked(now time.Time) {
	if len(m.jobs) == 0 {
		return
	}
	completed := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		snapshot := j.Snapshot()
		if !isTerminalStatus(snapshot.Status) {
			continue
		}
		if m.options.CompletedJobRetention > 0 && !snapshot.EndedAt.IsZero() && now.Sub(snapshot.EndedAt) > m.options.CompletedJobRetention {
			delete(m.jobs, snapshot.ID)
			continue
		}
		completed = append(completed, j)
	}
	if m.options.MaxCompletedJobs <= 0 || len(completed) <= m.options.MaxCompletedJobs {
		return
	}
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].Snapshot().EndedAt.After(completed[j].Snapshot().EndedAt)
	})
	for _, j := range completed[m.options.MaxCompletedJobs:] {
		delete(m.jobs, j.ID)
	}
}
