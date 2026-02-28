package queue

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	loaderconfig "github.com/mblsha/spadeforge/internal/spadeloader/config"
	"github.com/mblsha/spadeforge/internal/spadeloader/flasher"
	"github.com/mblsha/spadeforge/internal/spadeloader/history"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
	"github.com/mblsha/spadeforge/internal/spadeloader/store"
)

func TestManagerSubmitAndProcessSuccess(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{}, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	rec, err := mgr.Submit(context.Background(), SubmitRequest{
		Board:         "alchitry_au",
		DesignName:    "Blink",
		BitstreamName: "design.bit",
		Bitstream:     bytes.NewBufferString("bitstream"),
	})
	if err != nil {
		t.Fatalf("Submit() error: %v", err)
	}
	if rec.State != job.StateQueued {
		t.Fatalf("Submit() state = %s, want %s", rec.State, job.StateQueued)
	}

	finished := waitForTerminal(t, mgr, rec.ID, 3*time.Second)
	if finished.State != job.StateSucceeded {
		t.Fatalf("State = %s, want %s", finished.State, job.StateSucceeded)
	}

	historyItems, err := mgr.ListRecentDesigns(10)
	if err != nil {
		t.Fatalf("ListRecentDesigns() error: %v", err)
	}
	if len(historyItems) != 1 {
		t.Fatalf("len(historyItems) = %d, want 1", len(historyItems))
	}
	if historyItems[0].DesignName != "Blink" {
		t.Fatalf("DesignName = %q, want Blink", historyItems[0].DesignName)
	}

	logRaw, err := mgr.ReadConsoleLog(rec.ID)
	if err != nil {
		t.Fatalf("ReadConsoleLog() error: %v", err)
	}
	if len(logRaw) == 0 {
		t.Fatalf("expected non-empty console log")
	}
}

func TestManagerRecoverRunningRequeues(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	if err := st.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error: %v", err)
	}
	jobID := "recover-job"
	if err := st.CreateJobLayout(jobID); err != nil {
		t.Fatalf("CreateJobLayout() error: %v", err)
	}
	if err := os.WriteFile(st.RequestBitstreamPath(jobID), []byte("bitstream"), 0o644); err != nil {
		t.Fatalf("write request bitstream: %v", err)
	}

	rec := job.New(jobID, job.NewRecordInput{
		Board:              "alchitry_au",
		DesignName:         "Recovered",
		BitstreamName:      "design.bit",
		BitstreamSHA256:    "sha",
		BitstreamSizeBytes: 8,
	}, time.Now())
	if err := rec.Transition(job.StateRunning, time.Now(), "running before restart"); err != nil {
		t.Fatalf("Transition() error: %v", err)
	}
	if err := st.Save(rec); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{}, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	finished := waitForTerminal(t, mgr, jobID, 3*time.Second)
	if finished.State != job.StateSucceeded {
		t.Fatalf("State = %s, want %s", finished.State, job.StateSucceeded)
	}
}

func TestManagerEventsAndTail(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{Message: "done"}, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	rec, err := mgr.Submit(context.Background(), SubmitRequest{
		Board:         "alchitry_au",
		DesignName:    "Events",
		BitstreamName: "design.bit",
		Bitstream:     bytes.NewBufferString("bitstream"),
	})
	if err != nil {
		t.Fatalf("Submit() error: %v", err)
	}

	waitForTerminal(t, mgr, rec.ID, 3*time.Second)

	backlog, ch, cancelSub, ok := mgr.SubscribeEvents(rec.ID, 0)
	if !ok {
		t.Fatalf("SubscribeEvents() expected job to exist")
	}
	defer cancelSub()
	if ch != nil {
		t.Fatalf("expected nil live channel for terminal job")
	}
	if len(backlog) == 0 {
		t.Fatalf("expected non-empty backlog")
	}
	if backlog[len(backlog)-1].State != job.StateSucceeded {
		t.Fatalf("last event state = %s, want %s", backlog[len(backlog)-1].State, job.StateSucceeded)
	}

	tail, err := mgr.ReadConsoleTail(rec.ID, 1)
	if err != nil {
		t.Fatalf("ReadConsoleTail() error: %v", err)
	}
	if len(tail) == 0 {
		t.Fatalf("expected non-empty console tail")
	}
}

func TestManagerListJobsSortedDesc(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{}, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	first, err := mgr.Submit(context.Background(), SubmitRequest{
		Board:         "alchitry_au",
		DesignName:    "First",
		BitstreamName: "first.bit",
		Bitstream:     bytes.NewBufferString("first"),
	})
	if err != nil {
		t.Fatalf("Submit(first) error: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := mgr.Submit(context.Background(), SubmitRequest{
		Board:         "alchitry_au",
		DesignName:    "Second",
		BitstreamName: "second.bit",
		Bitstream:     bytes.NewBufferString("second"),
	})
	if err != nil {
		t.Fatalf("Submit(second) error: %v", err)
	}

	waitForTerminal(t, mgr, first.ID, 3*time.Second)
	waitForTerminal(t, mgr, second.ID, 3*time.Second)

	items := mgr.ListJobs(10)
	if len(items) < 2 {
		t.Fatalf("len(items) = %d, want at least 2", len(items))
	}
	if items[0].ID != second.ID {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, second.ID)
	}
	if items[1].ID != first.ID {
		t.Fatalf("items[1].ID = %q, want %q", items[1].ID, first.ID)
	}

	limited := mgr.ListJobs(1)
	if len(limited) != 1 {
		t.Fatalf("len(limited) = %d, want 1", len(limited))
	}
	if limited[0].ID != second.ID {
		t.Fatalf("limited[0].ID = %q, want %q", limited[0].ID, second.ID)
	}
}

func TestManagerReflash(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{}, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	original, err := mgr.Submit(context.Background(), SubmitRequest{
		Board:         "alchitry_au",
		DesignName:    "Blink",
		BitstreamName: "design.bit",
		Bitstream:     bytes.NewBufferString("bitstream"),
	})
	if err != nil {
		t.Fatalf("Submit() error: %v", err)
	}
	waitForTerminal(t, mgr, original.ID, 3*time.Second)

	reflashed, err := mgr.Reflash(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("Reflash() error: %v", err)
	}
	if reflashed.ID == original.ID {
		t.Fatalf("expected different job ids")
	}
	if reflashed.Board != original.Board || reflashed.DesignName != original.DesignName {
		t.Fatalf("unexpected reflash metadata: board=%q design=%q", reflashed.Board, reflashed.DesignName)
	}
	waitForTerminal(t, mgr, reflashed.ID, 3*time.Second)

	items := mgr.ListJobs(2)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].ID != reflashed.ID {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, reflashed.ID)
	}
}

func TestManagerReflashMissingSourceJob(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{}, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	_, err := mgr.Reflash(context.Background(), "missing")
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Reflash() error = %v, want ErrJobNotFound", err)
	}
}

func waitForTerminal(t *testing.T, mgr *Manager, jobID string, timeout time.Duration) *job.Record {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec, ok := mgr.Get(jobID)
		if !ok {
			t.Fatalf("job %s not found", jobID)
		}
		if rec.Terminal() {
			return rec
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for terminal state for job %s", jobID)
	return nil
}
