package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/job"
	"github.com/mblsha/spadeforge/internal/manifest"
	"github.com/mblsha/spadeforge/internal/queue"
	"github.com/mblsha/spadeforge/internal/store"
)

func TestHealthz(t *testing.T) {
	ts, _, _, cancel := newTestServer(t, &builder.FakeBuilder{})
	defer cancel()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSubmitJob_SucceedsAndArtifactsDownload(t *testing.T) {
	ts, cfg, mgr, cancel := newTestServer(t, &builder.FakeBuilder{})
	defer cancel()

	jobID := submitBundle(t, ts.URL, cfg, validBundleBytes(t, "ok"))
	rec := waitForJobTerminalHTTP(t, ts.URL, cfg, jobID)
	if rec.State != job.StateSucceeded {
		t.Fatalf("expected success, got %s err=%s", rec.State, rec.Error)
	}

	artifactZip := downloadArtifacts(t, ts.URL, cfg, jobID)
	files := listZipEntries(t, artifactZip)
	if !files["design.bit"] {
		t.Fatalf("expected design.bit in artifacts: %v", files)
	}
	if !files["console.log"] {
		t.Fatalf("expected console.log in artifacts: %v", files)
	}

	// Ensure status endpoint matches manager state too.
	inMem, ok := mgr.Get(jobID)
	if !ok || inMem.State != job.StateSucceeded {
		t.Fatalf("manager state mismatch: %#v", inMem)
	}
}

func TestSubmitJob_FailureIncludesLogsOnly(t *testing.T) {
	fb := &builder.FakeBuilder{FailProjects: map[string]error{"fail": errors.New("forced")}}
	ts, cfg, _, cancel := newTestServer(t, fb)
	defer cancel()

	jobID := submitBundle(t, ts.URL, cfg, validBundleBytes(t, "fail"))
	rec := waitForJobTerminalHTTP(t, ts.URL, cfg, jobID)
	if rec.State != job.StateFailed {
		t.Fatalf("expected failed, got %s", rec.State)
	}

	artifactZip := downloadArtifacts(t, ts.URL, cfg, jobID)
	files := listZipEntries(t, artifactZip)
	if files["design.bit"] {
		t.Fatalf("did not expect bitstream on failure")
	}
	if !files["console.log"] {
		t.Fatalf("expected console.log on failure")
	}
}

func TestSubmitJob_RejectsMissingToken(t *testing.T) {
	ts, _, _, cancel := newTestServer(t, &builder.FakeBuilder{})
	defer cancel()

	body, contentType := multipartBody(t, validBundleBytes(t, "ok"))
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/jobs", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestJobStatus_ExposesStepAndHeartbeat(t *testing.T) {
	block := make(chan struct{})
	fb := &builder.FakeBuilder{
		BlockCh:           block,
		HeartbeatInterval: 25 * time.Millisecond,
	}
	ts, cfg, _, cancel := newTestServer(t, fb)
	defer cancel()

	jobID := submitBundle(t, ts.URL, cfg, validBundleBytes(t, "running"))

	var running job.Record
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/jobs/"+jobID, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(cfg.AuthHeader, cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("status failed: %d body=%s", resp.StatusCode, string(raw))
		}
		if err := json.NewDecoder(resp.Body).Decode(&running); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if running.State == job.StateRunning && running.CurrentStep != "" && running.HeartbeatAt != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if running.State != job.StateRunning || running.CurrentStep == "" || running.HeartbeatAt == nil {
		t.Fatalf("expected running job with progress fields, got %+v", running)
	}

	close(block)
	waitForJobTerminalHTTP(t, ts.URL, cfg, jobID)
}

func newTestServer(t *testing.T, b builder.Builder) (*httptest.Server, config.Config, *queue.Manager, context.CancelFunc) {
	t.Helper()
	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	cfg.Token = "secret"
	cfg.WorkerTimeout = 5 * time.Second

	st := store.New(cfg)
	mgr := queue.New(cfg, st, b)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	api := New(cfg, mgr)
	ts := httptest.NewServer(api.Handler())
	return ts, cfg, mgr, func() {
		cancel()
		ts.Close()
	}
}

func submitBundle(t *testing.T, baseURL string, cfg config.Config, bundle []byte) string {
	t.Helper()
	body, contentType := multipartBody(t, bundle)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/jobs", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(cfg.AuthHeader, cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit failed: %d body=%s", resp.StatusCode, string(raw))
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.JobID == "" {
		t.Fatalf("missing job_id")
	}
	return payload.JobID
}

func waitForJobTerminalHTTP(t *testing.T, baseURL string, cfg config.Config, jobID string) *job.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/jobs/"+jobID, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(cfg.AuthHeader, cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("status failed: %d body=%s", resp.StatusCode, string(raw))
		}
		var rec job.Record
		if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
			resp.Body.Close()
			t.Fatal(err)
		}
		resp.Body.Close()
		if rec.Terminal() {
			return &rec
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for terminal state")
	return nil
}

func downloadArtifacts(t *testing.T, baseURL string, cfg config.Config, jobID string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/jobs/"+jobID+"/artifacts", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(cfg.AuthHeader, cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("artifacts failed: %d body=%s", resp.StatusCode, string(raw))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func multipartBody(t *testing.T, bundle []byte) (bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bundle); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body, mw.FormDataContentType()
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

func listZipEntries(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range zr.File {
		out[f.Name] = true
	}
	return out
}
