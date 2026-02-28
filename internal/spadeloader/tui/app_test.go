package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/spadeloader/client"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
)

func TestApplyJobsSortsDescAndSelectsNewest(t *testing.T) {
	t.Parallel()

	m, err := newModel(Options{Client: &client.HTTPClient{}})
	if err != nil {
		t.Fatalf("newModel() error: %v", err)
	}
	now := time.Now().UTC()

	m.applyJobs([]job.Record{
		{ID: "older", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "newer", CreatedAt: now},
		{ID: "middle", CreatedAt: now.Add(-time.Minute)},
	})

	if len(m.items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(m.items))
	}
	if m.items[0].ID != "newer" {
		t.Fatalf("items[0].ID = %q, want newer", m.items[0].ID)
	}
	if m.selectedIdx != 0 {
		t.Fatalf("selectedIdx = %d, want 0", m.selectedIdx)
	}
	if m.selectedID != "newer" {
		t.Fatalf("selectedID = %q, want newer", m.selectedID)
	}
}

func TestApplyJobsKeepsSelectionByID(t *testing.T) {
	t.Parallel()

	m, err := newModel(Options{Client: &client.HTTPClient{}})
	if err != nil {
		t.Fatalf("newModel() error: %v", err)
	}
	now := time.Now().UTC()
	m.selectedID = "middle"

	m.applyJobs([]job.Record{
		{ID: "older", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "newer", CreatedAt: now},
		{ID: "middle", CreatedAt: now.Add(-time.Minute)},
	})

	if m.selectedID != "middle" {
		t.Fatalf("selectedID = %q, want middle", m.selectedID)
	}
	if m.selectedIdx != 1 {
		t.Fatalf("selectedIdx = %d, want 1", m.selectedIdx)
	}
}

func TestApplyJobsPendingIDWins(t *testing.T) {
	t.Parallel()

	m, err := newModel(Options{Client: &client.HTTPClient{}})
	if err != nil {
		t.Fatalf("newModel() error: %v", err)
	}
	now := time.Now().UTC()
	m.selectedID = "middle"
	m.pendingID = "reflash"

	m.applyJobs([]job.Record{
		{ID: "reflash", CreatedAt: now},
		{ID: "middle", CreatedAt: now.Add(-time.Minute)},
		{ID: "older", CreatedAt: now.Add(-2 * time.Minute)},
	})

	if m.selectedID != "reflash" {
		t.Fatalf("selectedID = %q, want reflash", m.selectedID)
	}
	if m.selectedIdx != 0 {
		t.Fatalf("selectedIdx = %d, want 0", m.selectedIdx)
	}
	if m.pendingID != "" {
		t.Fatalf("pendingID = %q, want empty", m.pendingID)
	}
}

func TestApplyJobsDedupesByBitstreamIdentity(t *testing.T) {
	t.Parallel()

	m, err := newModel(Options{Client: &client.HTTPClient{}})
	if err != nil {
		t.Fatalf("newModel() error: %v", err)
	}
	now := time.Now().UTC()

	m.applyJobs([]job.Record{
		{
			ID:              "new",
			Board:           "alchitry_au",
			DesignName:      "Blink",
			BitstreamName:   "design.bit",
			BitstreamSHA256: "abc",
			CreatedAt:       now,
		},
		{
			ID:              "old",
			Board:           "alchitry_au",
			DesignName:      "Blink",
			BitstreamName:   "design.bit",
			BitstreamSHA256: "abc",
			CreatedAt:       now.Add(-time.Minute),
		},
		{
			ID:              "other",
			Board:           "alchitry_cu",
			DesignName:      "UART",
			BitstreamName:   "uart.bit",
			BitstreamSHA256: "def",
			CreatedAt:       now.Add(-2 * time.Minute),
		},
	})

	if len(m.items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(m.items))
	}
	if m.items[0].ID != "new" {
		t.Fatalf("items[0].ID = %q, want new", m.items[0].ID)
	}
	if m.items[1].ID != "other" {
		t.Fatalf("items[1].ID = %q, want other", m.items[1].ID)
	}
}

func TestAddEventTrimsToMaxLines(t *testing.T) {
	t.Parallel()

	m, err := newModel(Options{Client: &client.HTTPClient{}})
	if err != nil {
		t.Fatalf("newModel() error: %v", err)
	}

	for i := 0; i < maxEventLines+5; i++ {
		m.addEvent("event")
	}
	if len(m.eventLines) != maxEventLines {
		t.Fatalf("len(eventLines) = %d, want %d", len(m.eventLines), maxEventLines)
	}
}

func TestObserveJobEventsDetectsStateChange(t *testing.T) {
	t.Parallel()

	m, err := newModel(Options{Client: &client.HTTPClient{}})
	if err != nil {
		t.Fatalf("newModel() error: %v", err)
	}
	now := time.Now().UTC()

	m.observeJobEvents([]job.Record{
		{ID: "j1", State: job.StateQueued, CreatedAt: now, Board: "alchitry_au", DesignName: "Blink"},
	})
	baseEvents := len(m.eventLines)
	if baseEvents == 0 {
		t.Fatalf("expected baseline events after initial observe")
	}

	m.observeJobEvents([]job.Record{
		{ID: "j1", State: job.StateRunning, CreatedAt: now, Board: "alchitry_au", DesignName: "Blink"},
	})
	if len(m.eventLines) <= baseEvents {
		t.Fatalf("expected event count to grow on state change")
	}
	last := m.eventLines[len(m.eventLines)-1]
	if !strings.Contains(last, "QUEUED -> RUNNING") {
		t.Fatalf("last event %q missing state transition", last)
	}
}
