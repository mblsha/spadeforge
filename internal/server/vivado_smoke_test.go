//go:build vivado

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/client"
	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/job"
	"github.com/mblsha/spadeforge/internal/queue"
	"github.com/mblsha/spadeforge/internal/store"
)

func TestVivadoSmoke_ServerPipeline(t *testing.T) {
	vivadoBin := os.Getenv("VIVADO_BIN")
	part := os.Getenv("VIVADO_PART")
	if vivadoBin == "" || part == "" {
		t.Skip("set VIVADO_BIN and VIVADO_PART to run Vivado smoke test")
	}

	sourcePath := filepath.Join("..", "..", "testdata", "smoke", "hdl", "top.sv")
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("missing smoke source file: %v", err)
	}

	bundle, err := client.BuildBundle(client.BundleSpec{
		Project: "smoke",
		Top:     "top",
		Part:    part,
		Sources: []string{sourcePath},
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}

	cfg := config.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 90 * time.Minute
	cfg.VivadoBin = vivadoBin

	st := store.New(cfg)
	mgr := queue.New(cfg, st, builder.NewVivadoBuilder(cfg.VivadoBin, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(New(cfg, mgr).Handler())
	defer ts.Close()

	jobID := submitSmokeBundle(t, ts.URL, bundle)
	rec := waitForSmokeJob(t, ts.URL, jobID, 90*time.Minute)
	if rec.State != job.StateSucceeded {
		art := downloadSmokeArtifacts(t, ts.URL, jobID)
		files := unzipMap(t, art)
		t.Fatalf("smoke build failed: state=%s error=%s files=%v", rec.State, rec.Error, files)
	}

	art := downloadSmokeArtifacts(t, ts.URL, jobID)
	files := unzipMap(t, art)
	if len(files["design.bit"]) == 0 {
		t.Fatalf("missing or empty design.bit in smoke artifacts")
	}
	if _, ok := files["vivado.log"]; !ok {
		t.Fatalf("missing vivado.log in smoke artifacts")
	}
	if _, ok := files["console.log"]; !ok {
		t.Fatalf("missing console.log in smoke artifacts")
	}
}

func submitSmokeBundle(t *testing.T, baseURL string, bundle []byte) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, err := mw.CreateFormFile("bundle", "smoke.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bundle); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/jobs", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit failed: %d %s", resp.StatusCode, string(raw))
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.JobID
}

func waitForSmokeJob(t *testing.T, baseURL, jobID string, timeout time.Duration) *job.Record {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/v1/jobs/" + jobID)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("status failed: %d %s", resp.StatusCode, string(raw))
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
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for vivado smoke job")
	return nil
}

func downloadSmokeArtifacts(t *testing.T, baseURL, jobID string) []byte {
	t.Helper()
	resp, err := http.Get(baseURL + "/v1/jobs/" + jobID + "/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("artifact download failed: %d %s", resp.StatusCode, string(raw))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func unzipMap(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = b
	}
	return files
}
