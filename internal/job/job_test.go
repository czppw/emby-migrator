package job

import (
	"context"
	"testing"
	"time"
)

func TestStopKeepsStoppedWhenFailArrivesLater(t *testing.T) {
	j := NewManager().Create("export")
	j.Start()

	if !j.Stop() {
		t.Fatalf("Stop returned false for running job")
	}
	j.Fail(context.Canceled)

	snapshot := j.Snapshot()
	if snapshot.Status != StatusStopped {
		t.Fatalf("status = %q, want %q", snapshot.Status, StatusStopped)
	}
}

func TestStopKeepsStoppedWhenCompleteArrivesLater(t *testing.T) {
	j := NewManager().Create("export")
	j.Start()

	if !j.Stop() {
		t.Fatalf("Stop returned false for running job")
	}
	j.Complete(map[string]string{"path": "partial"})

	snapshot := j.Snapshot()
	if snapshot.Status != StatusStopped {
		t.Fatalf("status = %q, want %q", snapshot.Status, StatusStopped)
	}
}

func TestManagerRunsQueuedJobsSerially(t *testing.T) {
	m := NewManager()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})

	first := m.Enqueue("export", func(j *Job) {
		close(firstStarted)
		<-firstRelease
		j.Complete("first")
	})
	second := m.Enqueue("import", func(j *Job) {
		close(secondStarted)
		j.Complete("second")
	})

	waitForSignal(t, firstStarted, "first job did not start")
	if got := first.Snapshot().Status; got != StatusRunning {
		t.Fatalf("first status = %q, want %q", got, StatusRunning)
	}
	if got := second.Snapshot().Status; got != StatusQueued {
		t.Fatalf("second status = %q, want %q", got, StatusQueued)
	}
	select {
	case <-secondStarted:
		t.Fatalf("second job started before first finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(firstRelease)
	waitForSignal(t, secondStarted, "second job did not start after first completed")
	waitForStatus(t, second, StatusDone)

	list := m.List()
	if len(list) != 2 {
		t.Fatalf("job list length = %d, want 2", len(list))
	}
}

func TestQueuedStoppedJobDoesNotRun(t *testing.T) {
	m := NewManager()
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	queuedRan := make(chan struct{})

	m.Enqueue("export", func(j *Job) {
		close(firstStarted)
		<-firstRelease
		j.Complete(nil)
	})
	queued := m.Enqueue("import", func(j *Job) {
		close(queuedRan)
		j.Complete(nil)
	})

	waitForSignal(t, firstStarted, "first job did not start")
	if !queued.Stop() {
		t.Fatalf("Stop returned false for queued job")
	}
	close(firstRelease)
	waitForStatus(t, queued, StatusStopped)
	select {
	case <-queuedRan:
		t.Fatalf("stopped queued job should not run")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPauseResumeUnblocksWaiters(t *testing.T) {
	j := NewManager().Create("import")
	j.Start()
	if !j.Pause() {
		t.Fatalf("Pause returned false for running job")
	}

	done := make(chan error, 1)
	go func() {
		done <- j.WaitIfPaused(context.Background())
	}()
	select {
	case err := <-done:
		t.Fatalf("WaitIfPaused returned before resume: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if !j.Resume() {
		t.Fatalf("Resume returned false for paused job")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitIfPaused returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitIfPaused did not return after resume")
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func waitForStatus(t *testing.T, j *Job, want Status) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := j.Snapshot().Status; got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("status = %q, want %q", j.Snapshot().Status, want)
		case <-ticker.C:
		}
	}
}
