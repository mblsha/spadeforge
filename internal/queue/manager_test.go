package queue

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/job"
	"github.com/mblsha/spadeforge/internal/manifest"
	"github.com/mblsha/spadeforge/internal/store"
)

func TestSubmitJob_SpoolsAndQueues(t *testing.T) {
	cfg := testConfig(t)
	st := store.New(cfg)
	if err := st.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mgr := New(cfg, st, &builder.FakeBuilder{})

	rec, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "ok")))
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if rec.State != job.StateQueued {
		t.Fatalf("expected queued, got %s", rec.State)
	}

	if _, err := os.Stat(st.RequestZipPath(rec.ID)); err != nil {
		t.Fatalf("request zip missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.SourceDir(rec.ID), "hdl", "spade.sv")); err != nil {
		t.Fatalf("source not extracted: %v", err)
	}

	loaded, err := st.Load(rec.ID)
	if err != nil {
		t.Fatalf("load state failed: %v", err)
	}
	if loaded.State != job.StateQueued {
		t.Fatalf("expected persisted queued state, got %s", loaded.State)
	}
}

func TestLoadJobs_RecoversQueuedJobs(t *testing.T) {
	cfg := testConfig(t)
	st := store.New(cfg)
	if err := st.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Add(-time.Hour)
	rec1 := job.New("job1", manifest.Manifest{Top: "top", Part: "part", Sources: []string{"hdl/spade.sv"}}, now)
	rec2 := job.New("job2", manifest.Manifest{Top: "top", Part: "part", Sources: []string{"hdl/spade.sv"}}, now.Add(time.Second))
	if err := rec2.Transition(job.StateRunning, now.Add(2*time.Second), "running"); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"job1", "job2"} {
		if err := st.CreateJobLayout(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Save(rec1); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(rec2); err != nil {
		t.Fatal(err)
	}

	mgr := New(cfg, st, &builder.FakeBuilder{BlockCh: make(chan struct{})})
	if err := mgr.recoverJobs(); err != nil {
		t.Fatalf("recover failed: %v", err)
	}

	out1, ok := mgr.Get("job1")
	if !ok {
		t.Fatalf("missing job1 after recovery")
	}
	if out1.State != job.StateQueued {
		t.Fatalf("expected job1 queued, got %s", out1.State)
	}

	out2, ok := mgr.Get("job2")
	if !ok {
		t.Fatalf("missing job2 after recovery")
	}
	if out2.State != job.StateQueued {
		t.Fatalf("expected recovered running job to be queued, got %s", out2.State)
	}
}

func TestWorker_UpdatesStatesCorrectly_OnSuccess(t *testing.T) {
	cfg := testConfig(t)
	st := store.New(cfg)
	fb := &builder.FakeBuilder{}
	mgr := New(cfg, st, fb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	rec, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "ok")))
	if err != nil {
		t.Fatal(err)
	}

	final := waitForTerminalState(t, mgr, rec.ID)
	if final.State != job.StateSucceeded {
		t.Fatalf("expected success, got %s error=%s", final.State, final.Error)
	}
	if _, err := os.Stat(filepath.Join(st.ArtifactsJobDir(rec.ID), "design.bit")); err != nil {
		t.Fatalf("expected bitstream: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.ArtifactsJobDir(rec.ID), "diagnostics.json")); err != nil {
		t.Fatalf("expected diagnostics.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.ArtifactsJobDir(rec.ID), "artifact_manifest.json")); err != nil {
		t.Fatalf("expected artifact_manifest.json: %v", err)
	}
}

func TestWorker_UpdatesStatesCorrectly_OnFailure(t *testing.T) {
	cfg := testConfig(t)
	st := store.New(cfg)
	fb := &builder.FakeBuilder{FailProjects: map[string]error{"fail": errors.New("forced failure")}}
	mgr := New(cfg, st, fb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	rec, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "fail")))
	if err != nil {
		t.Fatal(err)
	}
	final := waitForTerminalState(t, mgr, rec.ID)
	if final.State != job.StateFailed {
		t.Fatalf("expected failed, got %s", final.State)
	}
	if _, err := os.Stat(filepath.Join(st.ArtifactsJobDir(rec.ID), "design.bit")); !os.IsNotExist(err) {
		t.Fatalf("did not expect bitstream on failure")
	}
	if _, err := os.Stat(filepath.Join(st.ArtifactsJobDir(rec.ID), "console.log")); err != nil {
		t.Fatalf("expected logs on failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.ArtifactsJobDir(rec.ID), "diagnostics.json")); err != nil {
		t.Fatalf("expected diagnostics on failure: %v", err)
	}
	if final.FailureKind == "" || final.FailureSummary == "" {
		t.Fatalf("expected failure metadata, got %+v", final)
	}
}

func TestQueue_IsSequential(t *testing.T) {
	cfg := testConfig(t)
	st := store.New(cfg)
	block := make(chan struct{})
	fb := &builder.FakeBuilder{BlockCh: block}
	mgr := New(cfg, st, fb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	rec1, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "first")))
	if err != nil {
		t.Fatal(err)
	}
	rec2, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "second")))
	if err != nil {
		t.Fatal(err)
	}

	waitForState(t, mgr, rec1.ID, job.StateRunning)
	rec2State, ok := mgr.Get(rec2.ID)
	if !ok {
		t.Fatalf("job2 missing")
	}
	if rec2State.State != job.StateQueued {
		t.Fatalf("expected job2 queued while job1 blocked, got %s", rec2State.State)
	}

	close(block)
	waitForTerminalState(t, mgr, rec1.ID)
	waitForTerminalState(t, mgr, rec2.ID)

	if len(fb.Calls) != 2 {
		t.Fatalf("expected 2 builder calls, got %d", len(fb.Calls))
	}
	if fb.Calls[0].ID != rec1.ID || fb.Calls[1].ID != rec2.ID {
		t.Fatalf("unexpected call order: %#v", fb.Calls)
	}
}

func TestWorker_ProgressStepAndHeartbeat(t *testing.T) {
	cfg := testConfig(t)
	st := store.New(cfg)
	block := make(chan struct{})
	fb := &builder.FakeBuilder{
		BlockCh:           block,
		HeartbeatInterval: 25 * time.Millisecond,
	}
	mgr := New(cfg, st, fb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	rec, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "progress")))
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, mgr, rec.ID, job.StateRunning)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cur, ok := mgr.Get(rec.ID)
		if ok && cur.CurrentStep == "synth" && cur.HeartbeatAt != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	cur, _ := mgr.Get(rec.ID)
	if cur.CurrentStep == "" || cur.HeartbeatAt == nil {
		t.Fatalf("expected progress fields while running, got %+v", cur)
	}

	close(block)
	final := waitForTerminalState(t, mgr, rec.ID)
	if final.CurrentStep != "done" {
		t.Fatalf("expected final step done, got %q", final.CurrentStep)
	}
}

func TestWorker_PreserveWorkDirOption(t *testing.T) {
	cfg := testConfig(t)
	cfg.PreserveWorkDir = true
	st := store.New(cfg)
	mgr := New(cfg, st, &builder.FakeBuilder{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	rec, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "keep-work")))
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminalState(t, mgr, rec.ID)
	if _, err := os.Stat(st.WorkJobDir(rec.ID)); err != nil {
		t.Fatalf("expected preserved work dir, got %v", err)
	}
}

func TestEvents_SubscribeProvidesBacklog(t *testing.T) {
	cfg := testConfig(t)
	st := store.New(cfg)
	mgr := New(cfg, st, &builder.FakeBuilder{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	rec, err := mgr.Submit(context.Background(), bytes.NewReader(validBundleBytes(t, "ok")))
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminalState(t, mgr, rec.ID)

	events, ch, release, ok := mgr.SubscribeEvents(rec.ID, 0)
	if !ok {
		t.Fatalf("expected subscribe ok")
	}
	defer release()
	if ch != nil {
		t.Fatalf("expected nil live channel for terminal job")
	}
	if len(events) < 2 {
		t.Fatalf("expected backlog events, got %d", len(events))
	}
	if events[0].Type != "queued" {
		t.Fatalf("expected first event queued, got %q", events[0].Type)
	}
	if !events[len(events)-1].Terminal() {
		t.Fatalf("expected last event terminal, got %+v", events[len(events)-1])
	}
}

func waitForTerminalState(t *testing.T, mgr *Manager, id string) *job.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok := mgr.Get(id)
		if ok && rec.Terminal() {
			return rec
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal state: %s", id)
	return nil
}

func waitForState(t *testing.T, mgr *Manager, id string, want job.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok := mgr.Get(id)
		if ok && rec.State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %s: %s", want, id)
}

func validBundleBytes(t *testing.T, project string) []byte {
	t.Helper()

	mf := manifest.Manifest{
		Schema:  1,
		Project: project,
		Top:     "top",
		Part:    "xc7a35tcsg324-1",
		Sources: []string{"hdl/spade.sv"},
		Build: manifest.Build{
			Steps: []string{"synth", "impl", "bitstream"},
		},
	}
	rawManifest, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addZipFile(t, zw, "manifest.json", rawManifest)
	addZipFile(t, zw, "hdl/spade.sv", []byte("module top; endmodule\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func addZipFile(t *testing.T, zw *zip.Writer, name string, content []byte) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 5 * time.Second
	cfg.MaxUploadBytes = 10 << 20
	cfg.MaxExtractedFiles = 100
	cfg.MaxExtractedTotalBytes = 10 << 20
	cfg.MaxExtractedFileBytes = 5 << 20
	return cfg
}
