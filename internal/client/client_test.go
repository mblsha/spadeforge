package client

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mblsha/spadeforge/internal/job"
)

func TestBundleBuilder_IncludesSpadeSVAndXDCAndManifest(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "spade.sv")
	xdc := filepath.Join(tmp, "top.xdc")
	if err := os.WriteFile(src, []byte("module top;endmodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdc, []byte("set_property PACKAGE_PIN W5 [get_ports clk]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := BuildBundle(BundleSpec{
		Project:     "demo",
		Top:         "top",
		Part:        "xc7a35tcsg324-1",
		Sources:     []string{src},
		Constraints: []string{xdc},
	})
	if err != nil {
		t.Fatalf("build bundle failed: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		seen[f.Name] = true
	}
	if !seen["manifest.json"] || !seen["hdl/spade.sv"] || !seen["constraints/top.xdc"] {
		t.Fatalf("unexpected bundle contents: %v", seen)
	}
}

func TestClient_HandlesServerErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/jobs" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	c := &HTTPClient{BaseURL: ts.URL}
	_, err := c.SubmitBundle(context.Background(), []byte("not-a-zip"))
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestClient_SubmitAndDownloadAgainstTestServer(t *testing.T) {
	artifact := []byte("zip-bytes")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			f, _, err := r.FormFile("bundle")
			if err != nil {
				t.Fatalf("missing bundle: %v", err)
			}
			defer f.Close()
			if _, err := io.ReadAll(f); err != nil {
				t.Fatalf("read bundle: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"j1","state":"QUEUED"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/j1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"j1","state":"SUCCEEDED","manifest":{"top":"top","part":"part","sources":["hdl/spade.sv"]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/j1/artifacts":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(artifact)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/j1/diagnostics":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema":1,"error_count":1,"warning_count":0,"info_count":0,"diagnostics":[{"severity":"ERROR","code":"Synth 8-2716","message":"syntax error","file":"hdl/spade.sv","line":1}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/j1/tail":
			if r.URL.Query().Get("lines") != "3" {
				t.Fatalf("expected lines=3, got %q", r.URL.Query().Get("lines"))
			}
			_, _ = w.Write([]byte("lineA\nlineB\nlineC\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/j1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(strings.Join([]string{
				"id: 1",
				"event: queued",
				`data: {"seq":1,"job_id":"j1","type":"queued","state":"QUEUED","at":"2026-01-01T00:00:00Z"}`,
				"",
				"id: 2",
				"event: succeeded",
				`data: {"seq":2,"job_id":"j1","type":"succeeded","state":"SUCCEEDED","at":"2026-01-01T00:00:01Z"}`,
				"",
			}, "\n")))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c := &HTTPClient{BaseURL: ts.URL}
	jobID, err := c.SubmitBundle(context.Background(), []byte("bundle"))
	if err != nil {
		t.Fatal(err)
	}
	if jobID != "j1" {
		t.Fatalf("expected j1, got %s", jobID)
	}
	updateCount := 0
	rec, err := c.WaitForTerminalWithProgress(context.Background(), "j1", 10, func(record *job.Record) {
		updateCount++
	})
	if err != nil {
		t.Fatal(err)
	}
	if updateCount == 0 {
		t.Fatalf("expected progress updates during polling")
	}
	if !rec.Terminal() {
		t.Fatalf("expected terminal state")
	}
	var out bytes.Buffer
	if err := c.DownloadArtifacts(context.Background(), "j1", &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), artifact) {
		t.Fatalf("unexpected artifact payload")
	}

	report, err := c.GetDiagnostics(context.Background(), "j1")
	if err != nil {
		t.Fatal(err)
	}
	if report.ErrorCount != 1 || len(report.Diagnostics) != 1 {
		t.Fatalf("unexpected diagnostics report: %+v", report)
	}

	tail, err := c.GetLogTail(context.Background(), "j1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "lineA\nlineB\nlineC\n" {
		t.Fatalf("unexpected tail: %q", tail)
	}

	eventCount := 0
	if err := c.StreamEvents(context.Background(), "j1", 0, func(ev *job.Event) {
		eventCount++
	}); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 {
		t.Fatalf("expected 2 streamed events, got %d", eventCount)
	}
}
