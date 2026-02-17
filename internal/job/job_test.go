package job

import (
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/manifest"
)

func TestJobStateTransitions_Valid(t *testing.T) {
	now := time.Now()
	rec := New("id", manifest.Manifest{Top: "top", Part: "part", Sources: []string{"hdl/spade.sv"}}, now)
	if rec.State != StateQueued {
		t.Fatalf("expected queued, got %s", rec.State)
	}
	if err := rec.Transition(StateRunning, now.Add(time.Second), "start"); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}
	if err := rec.MarkSucceeded(now.Add(2*time.Second), "done", 0); err != nil {
		t.Fatalf("mark succeeded failed: %v", err)
	}
	if rec.State != StateSucceeded {
		t.Fatalf("expected succeeded, got %s", rec.State)
	}
}

func TestJobStateTransitions_Invalid(t *testing.T) {
	now := time.Now()
	rec := New("id", manifest.Manifest{Top: "top", Part: "part", Sources: []string{"hdl/spade.sv"}}, now)
	if err := rec.Transition(StateSucceeded, now, ""); err == nil {
		t.Fatalf("expected invalid transition error")
	}
}
