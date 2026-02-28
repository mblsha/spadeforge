package queue

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestManagerPrunesTerminalJobsByHistoryLimit(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second
	cfg.HistoryLimit = 2

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{}, hs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	var submitted []string
	for i := 0; i < 4; i++ {
		rec, err := mgr.Submit(context.Background(), SubmitRequest{
			Board:         "alchitry_au",
			DesignName:    "design-" + string(rune('A'+i)),
			BitstreamName: "design.bit",
			Bitstream:     bytes.NewBufferString("bitstream-" + string(rune('A'+i))),
		})
		if err != nil {
			t.Fatalf("Submit(%d) error: %v", i, err)
		}
		submitted = append(submitted, rec.ID)
		waitForTerminal(t, mgr, rec.ID, 3*time.Second)
		time.Sleep(5 * time.Millisecond)
	}

	items := mgr.ListJobs(10)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].ID != submitted[3] {
		t.Fatalf("items[0].ID = %q, want %q", items[0].ID, submitted[3])
	}
	if items[1].ID != submitted[2] {
		t.Fatalf("items[1].ID = %q, want %q", items[1].ID, submitted[2])
	}

	prunedIDs := []string{submitted[0], submitted[1]}
	for _, id := range prunedIDs {
		if _, ok := mgr.Get(id); ok {
			t.Fatalf("expected pruned job %s to be absent from manager", id)
		}
		if _, err := os.Stat(st.JobDir(id)); !os.IsNotExist(err) {
			t.Fatalf("expected removed job dir for %s, err=%v", id, err)
		}
		if _, err := os.Stat(st.ArtifactsJobDir(id)); !os.IsNotExist(err) {
			t.Fatalf("expected removed artifacts dir for %s, err=%v", id, err)
		}
		mgr.mu.RLock()
		_, hasEvents := mgr.events[id]
		_, hasSeq := mgr.nextEventSeq[id]
		_, hasJob := mgr.jobs[id]
		mgr.mu.RUnlock()
		if hasEvents || hasSeq || hasJob {
			t.Fatalf("expected pruned job %s to be dropped from memory maps", id)
		}
	}
}

func TestManagerPrunesTerminalJobsOnStartupKeepsNonTerminal(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.HistoryLimit = 1

	st := store.New(cfg)
	if err := st.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs() error: %v", err)
	}

	now := time.Now().UTC()
	oldTerminal := mustPersistRecord(t, st, newTerminalRecord("old-terminal", job.StateSucceeded, now.Add(-3*time.Minute)))
	newTerminal := mustPersistRecord(t, st, newTerminalRecord("new-terminal", job.StateFailed, now.Add(-2*time.Minute)))
	queued := mustPersistRecord(t, st, job.New("queued", job.NewRecordInput{
		Board:              "alchitry_au",
		DesignName:         "Queued",
		BitstreamName:      "queued.bit",
		BitstreamSHA256:    "sha-queued",
		BitstreamSizeBytes: 12,
	}, now.Add(-time.Minute)))

	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := New(cfg, st, &flasher.FakeFlasher{}, hs)

	startCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.Start(startCtx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if _, ok := mgr.Get(oldTerminal.ID); ok {
		t.Fatalf("expected old terminal job to be pruned on startup")
	}
	if _, ok := mgr.Get(newTerminal.ID); !ok {
		t.Fatalf("expected newest terminal job to be retained")
	}
	if rec, ok := mgr.Get(queued.ID); !ok || rec.State != job.StateQueued {
		t.Fatalf("expected queued job to be retained, got ok=%v state=%v", ok, rec.State)
	}

	if _, err := os.Stat(st.JobDir(oldTerminal.ID)); !os.IsNotExist(err) {
		t.Fatalf("expected old terminal job dir removed, err=%v", err)
	}
	if _, err := os.Stat(st.JobDir(newTerminal.ID)); err != nil {
		t.Fatalf("expected newest terminal job dir retained, err=%v", err)
	}
	if _, err := os.Stat(st.JobDir(queued.ID)); err != nil {
		t.Fatalf("expected queued job dir retained, err=%v", err)
	}
}

func mustPersistRecord(t *testing.T, st *store.Store, rec *job.Record) *job.Record {
	t.Helper()
	if err := st.CreateJobLayout(rec.ID); err != nil {
		t.Fatalf("CreateJobLayout(%s) error: %v", rec.ID, err)
	}
	if err := os.WriteFile(st.RequestBitstreamPath(rec.ID), []byte("bitstream"), 0o644); err != nil {
		t.Fatalf("write request bitstream for %s: %v", rec.ID, err)
	}
	if err := os.MkdirAll(filepath.Dir(st.ConsoleLogPath(rec.ID)), 0o755); err != nil {
		t.Fatalf("ensure console log dir for %s: %v", rec.ID, err)
	}
	if err := os.WriteFile(st.ConsoleLogPath(rec.ID), []byte("console log"), 0o644); err != nil {
		t.Fatalf("write console log for %s: %v", rec.ID, err)
	}
	if err := st.Save(rec); err != nil {
		t.Fatalf("Save(%s) error: %v", rec.ID, err)
	}
	return rec
}

func newTerminalRecord(id string, state job.State, created time.Time) *job.Record {
	createdUTC := created.UTC()
	rec := job.New(id, job.NewRecordInput{
		Board:              "alchitry_au",
		DesignName:         strings.TrimSpace(id),
		BitstreamName:      id + ".bit",
		BitstreamSHA256:    "sha-" + id,
		BitstreamSizeBytes: 16,
	}, createdUTC)
	started := createdUTC.Add(1 * time.Second)
	finished := createdUTC.Add(2 * time.Second)
	exitCode := 0
	if state == job.StateFailed {
		exitCode = 1
		rec.Error = "failed"
	}
	rec.State = state
	rec.Message = "terminal"
	rec.CurrentStep = "done"
	rec.StartedAt = &started
	rec.FinishedAt = &finished
	rec.HeartbeatAt = &finished
	rec.UpdatedAt = finished
	rec.ExitCode = &exitCode
	return rec
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
