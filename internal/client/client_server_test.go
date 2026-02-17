package client

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/job"
	"github.com/mblsha/spadeforge/internal/queue"
	"github.com/mblsha/spadeforge/internal/server"
	"github.com/mblsha/spadeforge/internal/store"
)

func TestClientServer_ProgressAndArtifacts(t *testing.T) {
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	cfg.Token = "secret"
	cfg.WorkerTimeout = 5 * time.Second

	block := make(chan struct{})
	fb := &builder.FakeBuilder{
		BlockCh:           block,
		HeartbeatInterval: 20 * time.Millisecond,
	}

	st := store.New(cfg)
	mgr := queue.New(cfg, st, fb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(cfg, mgr).Handler())
	defer ts.Close()

	source := filepath.Join(t.TempDir(), "spade.sv")
	if err := os.WriteFile(source, []byte("module top; endmodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildBundle(BundleSpec{
		Project: "demo",
		Top:     "top",
		Part:    "xc7a35tcsg324-1",
		Sources: []string{source},
	})
	if err != nil {
		t.Fatal(err)
	}

	cli := &HTTPClient{BaseURL: ts.URL, Token: cfg.Token, AuthHeader: cfg.AuthHeader}
	jobID, err := cli.SubmitBundle(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(120 * time.Millisecond)
		close(block)
	}()

	sawRunning := false
	sawHeartbeat := false
	record, err := cli.WaitForTerminalWithProgress(context.Background(), jobID, 25*time.Millisecond, func(rec *job.Record) {
		if rec.State == job.StateRunning {
			sawRunning = true
			if rec.HeartbeatAt != nil {
				sawHeartbeat = true
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.State != job.StateSucceeded {
		t.Fatalf("expected success, got %s", record.State)
	}
	if !sawRunning || !sawHeartbeat {
		t.Fatalf("expected running/heartbeat updates, running=%v heartbeat=%v", sawRunning, sawHeartbeat)
	}

	var rawZip bytes.Buffer
	if err := cli.DownloadArtifacts(context.Background(), jobID, &rawZip); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(t.TempDir(), "artifacts")
	if err := ExtractArtifactZip(rawZip.Bytes(), outDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "design.bit")); err != nil {
		t.Fatalf("expected design.bit in extracted output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "console.log")); err != nil {
		t.Fatalf("expected console.log in extracted output: %v", err)
	}

	report, err := cli.GetDiagnostics(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil {
		t.Fatalf("expected diagnostics report")
	}

	tail, err := cli.GetLogTail(context.Background(), jobID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if tail == "" {
		t.Fatalf("expected non-empty log tail")
	}
}
