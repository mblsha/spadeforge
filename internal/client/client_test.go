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
	"testing"
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
	rec, err := c.WaitForTerminal(context.Background(), "j1", 10)
	if err != nil {
		t.Fatal(err)
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
}
