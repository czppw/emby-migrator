package job

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
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

	ctx    context.Context
	cancel context.CancelFunc
	logs   []LogEntry
	subs   map[chan LogEntry]struct{}
	mu     sync.RWMutex
}

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type Manager struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewManager() *Manager {
	return &Manager{jobs: map[string]*Job{}}
}

func (m *Manager) Create(jobType string) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	j := &Job{
		ID:        fmt.Sprintf("%d", now.UnixNano()),
		Type:      jobType,
		Status:    StatusQueued,
		CreatedAt: now,
		UpdatedAt: now,
		ctx:       ctx,
		cancel:    cancel,
		subs:      map[chan LogEntry]struct{}{},
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()
	return j
}

func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

func (j *Job) Context() context.Context {
	return j.ctx
}

func (j *Job) Start() {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	j.Status = StatusRunning
	j.StartedAt = now
	j.UpdatedAt = now
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
	j.publishLocked(LogEntry{Time: now, Level: "info", Message: "任务完成"})
	j.closeSubsLocked()
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
	j.publishLocked(LogEntry{Time: now, Level: "error", Message: err.Error()})
	j.closeSubsLocked()
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
	j.publishLocked(LogEntry{Time: now, Level: "error", Message: err.Error()})
	j.closeSubsLocked()
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
	j.publishLocked(LogEntry{Time: now, Level: "warn", Message: "任务已停止"})
	j.closeSubsLocked()
	return true
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

func (j *Job) Subscribe() (<-chan LogEntry, func()) {
	ch := make(chan LogEntry, 100)
	j.mu.Lock()
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

func isTerminalStatus(status Status) bool {
	return status == StatusDone || status == StatusFailed || status == StatusStopped
}

func (j *Job) publishLocked(entry LogEntry) {
	j.logs = append(j.logs, entry)
	for ch := range j.subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

func (j *Job) closeSubsLocked() {
	for ch := range j.subs {
		close(ch)
		delete(j.subs, ch)
	}
}
