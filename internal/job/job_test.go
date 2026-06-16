package job

import (
	"context"
	"testing"
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
