package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	loaderconfig "github.com/mblsha/spadeforge/internal/spadeloader/config"
	"github.com/mblsha/spadeforge/internal/spadeloader/flasher"
	"github.com/mblsha/spadeforge/internal/spadeloader/history"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
	"github.com/mblsha/spadeforge/internal/spadeloader/queue"
	"github.com/mblsha/spadeforge/internal/spadeloader/store"
)

func TestSubmitAndRecentDesigns(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := queue.New(cfg, st, &flasher.FakeFlasher{}, hs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	api := New(cfg, mgr)
	ts := httptest.NewServer(api.Handler())
	defer ts.Close()

	status, body := submitJob(t, ts.URL, "alchitry_au", "Blink", "design.bit", []byte("bitstream"), "", "")
	if status != http.StatusAccepted {
		t.Fatalf("submit status = %d, body=%s", status, body)
	}

	var submitResp map[string]string
	if err := json.Unmarshal([]byte(body), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	jobID := submitResp["job_id"]
	if strings.TrimSpace(jobID) == "" {
		t.Fatalf("job_id missing")
	}

	final := waitForTerminalHTTP(t, ts.URL, jobID, "", "")
	if final.State != job.StateSucceeded {
		t.Fatalf("state = %s, want %s", final.State, job.StateSucceeded)
	}

	recentResp, err := http.Get(ts.URL + "/v1/designs/recent?limit=5")
	if err != nil {
		t.Fatalf("GET recent error: %v", err)
	}
	defer recentResp.Body.Close()
	if recentResp.StatusCode != http.StatusOK {
		t.Fatalf("recent status = %d", recentResp.StatusCode)
	}
	var payload struct {
		Items []struct {
			DesignName string `json:"design_name"`
			Board      string `json:"board"`
		}
	}
	if err := json.NewDecoder(recentResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode recent payload: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(payload.Items))
	}
	if payload.Items[0].DesignName != "Blink" || payload.Items[0].Board != "alchitry_au" {
		t.Fatalf("unexpected recent item: %+v", payload.Items[0])
	}

	logResp, err := http.Get(ts.URL + "/v1/jobs/" + jobID + "/log")
	if err != nil {
		t.Fatalf("GET log error: %v", err)
	}
	defer logResp.Body.Close()
	if logResp.StatusCode != http.StatusOK {
		t.Fatalf("log status = %d", logResp.StatusCode)
	}

	tailResp, err := http.Get(ts.URL + "/v1/jobs/" + jobID + "/tail?lines=1")
	if err != nil {
		t.Fatalf("GET tail error: %v", err)
	}
	defer tailResp.Body.Close()
	if tailResp.StatusCode != http.StatusOK {
		t.Fatalf("tail status = %d", tailResp.StatusCode)
	}
	tailRaw, err := io.ReadAll(tailResp.Body)
	if err != nil {
		t.Fatalf("read tail body: %v", err)
	}
	if strings.TrimSpace(string(tailRaw)) == "" {
		t.Fatalf("expected non-empty tail body")
	}

	eventsResp, err := http.Get(ts.URL + "/v1/jobs/" + jobID + "/events")
	if err != nil {
		t.Fatalf("GET events error: %v", err)
	}
	defer eventsResp.Body.Close()
	if eventsResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", eventsResp.StatusCode)
	}
	eventsRaw, err := io.ReadAll(eventsResp.Body)
	if err != nil {
		t.Fatalf("read events body: %v", err)
	}
	eventsText := string(eventsRaw)
	if !strings.Contains(eventsText, "event: succeeded") {
		t.Fatalf("expected succeeded event in SSE payload, got: %s", eventsText)
	}
	if !strings.Contains(eventsText, "\"state\":\"SUCCEEDED\"") {
		t.Fatalf("expected SUCCEEDED state in SSE payload, got: %s", eventsText)
	}
}

func TestSubmitValidation(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := queue.New(cfg, st, &flasher.FakeFlasher{}, hs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	api := New(cfg, mgr)
	ts := httptest.NewServer(api.Handler())
	defer ts.Close()

	status, _ := submitJob(t, ts.URL, "bad board !", "Blink", "design.bit", []byte("bitstream"), "", "")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}

	status, _ = submitJob(t, ts.URL, "alchitry_au", "Blink", "design.txt", []byte("bitstream"), "", "")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestTokenGuard(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.Token = "secret"

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := queue.New(cfg, st, &flasher.FakeFlasher{}, hs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	api := New(cfg, mgr)
	ts := httptest.NewServer(api.Handler())
	defer ts.Close()

	status, _ := submitJob(t, ts.URL, "alchitry_au", "Blink", "design.bit", []byte("bitstream"), "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}

	status, body := submitJob(t, ts.URL, "alchitry_au", "Blink", "design.bit", []byte("bitstream"), cfg.AuthHeader, "secret")
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", status, http.StatusAccepted)
	}
	var submitResp map[string]string
	if err := json.Unmarshal([]byte(body), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if jobID := strings.TrimSpace(submitResp["job_id"]); jobID != "" {
		_ = waitForTerminalHTTP(t, ts.URL, jobID, cfg.AuthHeader, cfg.Token)
	}
}

func TestBoardAllowlistGuard(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.AllowedBoards = []string{"alchitry_au"}

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := queue.New(cfg, st, &flasher.FakeFlasher{}, hs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	api := New(cfg, mgr)
	ts := httptest.NewServer(api.Handler())
	defer ts.Close()

	status, _ := submitJob(t, ts.URL, "other_board", "Blink", "design.bit", []byte("bitstream"), "", "")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}

	status, body := submitJob(t, ts.URL, "alchitry_au", "Blink", "design.bit", []byte("bitstream"), "", "")
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", status, http.StatusAccepted)
	}
	var submitResp map[string]string
	if err := json.Unmarshal([]byte(body), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if jobID := strings.TrimSpace(submitResp["job_id"]); jobID != "" {
		_ = waitForTerminalHTTP(t, ts.URL, jobID, "", "")
	}
}

func TestListJobsAndReflash(t *testing.T) {
	t.Parallel()

	cfg := loaderconfig.Default()
	cfg.BaseDir = t.TempDir()
	cfg.WorkerTimeout = 2 * time.Second

	st := store.New(cfg)
	hs := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := queue.New(cfg, st, &flasher.FakeFlasher{}, hs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	api := New(cfg, mgr)
	ts := httptest.NewServer(api.Handler())
	defer ts.Close()

	status, body := submitJob(t, ts.URL, "alchitry_au", "Blink", "design.bit", []byte("bitstream"), "", "")
	if status != http.StatusAccepted {
		t.Fatalf("submit status = %d, body=%s", status, body)
	}
	var submitResp map[string]string
	if err := json.Unmarshal([]byte(body), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	sourceJobID := strings.TrimSpace(submitResp["job_id"])
	if sourceJobID == "" {
		t.Fatalf("job_id missing")
	}
	_ = waitForTerminalHTTP(t, ts.URL, sourceJobID, "", "")

	resp, err := http.Get(ts.URL + "/v1/jobs?limit=5")
	if err != nil {
		t.Fatalf("GET jobs error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("jobs status = %d", resp.StatusCode)
	}
	var listPayload struct {
		Items []job.Record `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode jobs payload: %v", err)
	}
	if len(listPayload.Items) == 0 {
		t.Fatalf("expected at least one job")
	}
	if listPayload.Items[0].ID != sourceJobID {
		t.Fatalf("items[0].ID = %q, want %q", listPayload.Items[0].ID, sourceJobID)
	}

	reflashResp, err := http.Post(ts.URL+"/v1/jobs/"+sourceJobID+"/reflash", "application/json", nil)
	if err != nil {
		t.Fatalf("POST reflash error: %v", err)
	}
	defer reflashResp.Body.Close()
	if reflashResp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(reflashResp.Body)
		t.Fatalf("reflash status = %d body=%s", reflashResp.StatusCode, string(raw))
	}
	var reflashPayload map[string]string
	if err := json.NewDecoder(reflashResp.Body).Decode(&reflashPayload); err != nil {
		t.Fatalf("decode reflash payload: %v", err)
	}
	reflashedJobID := strings.TrimSpace(reflashPayload["job_id"])
	if reflashedJobID == "" {
		t.Fatalf("missing reflashed job_id")
	}
	if reflashedJobID == sourceJobID {
		t.Fatalf("expected new job id")
	}
	_ = waitForTerminalHTTP(t, ts.URL, reflashedJobID, "", "")

	resp2, err := http.Get(ts.URL + "/v1/jobs?limit=2")
	if err != nil {
		t.Fatalf("GET jobs (after reflash) error: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("jobs status after reflash = %d", resp2.StatusCode)
	}
	var listPayload2 struct {
		Items []job.Record `json:"items"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&listPayload2); err != nil {
		t.Fatalf("decode jobs payload (after reflash): %v", err)
	}
	if len(listPayload2.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(listPayload2.Items))
	}
	if listPayload2.Items[0].ID != reflashedJobID {
		t.Fatalf("items[0].ID = %q, want %q", listPayload2.Items[0].ID, reflashedJobID)
	}
	if listPayload2.Items[1].ID != sourceJobID {
		t.Fatalf("items[1].ID = %q, want %q", listPayload2.Items[1].ID, sourceJobID)
	}
}

func submitJob(t *testing.T, baseURL, board, designName, filename string, bitstream []byte, authHeader, token string) (int, string) {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("board", board); err != nil {
		t.Fatalf("WriteField(board) error: %v", err)
	}
	if err := mw.WriteField("design_name", designName); err != nil {
		t.Fatalf("WriteField(design_name) error: %v", err)
	}
	fw, err := mw.CreateFormFile("bitstream", filepath.Base(filename))
	if err != nil {
		t.Fatalf("CreateFormFile error: %v", err)
	}
	if _, err := fw.Write(bitstream); err != nil {
		t.Fatalf("Write bitstream error: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("Close multipart writer error: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/jobs", &body)
	if err != nil {
		t.Fatalf("NewRequest error: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if authHeader != "" && token != "" {
		req.Header.Set(authHeader, token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request error: %v", err)
	}
	defer resp.Body.Close()

	raw := new(bytes.Buffer)
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		t.Fatalf("Read response body error: %v", err)
	}
	return resp.StatusCode, raw.String()
}

func waitForTerminalHTTP(t *testing.T, baseURL, jobID, authHeader, token string) *job.Record {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/jobs/"+jobID, nil)
		if err != nil {
			t.Fatalf("NewRequest error: %v", err)
		}
		if authHeader != "" && token != "" {
			req.Header.Set(authHeader, token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET job error: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("GET job status = %d", resp.StatusCode)
		}
		var rec job.Record
		if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
			resp.Body.Close()
			t.Fatalf("decode job response: %v", err)
		}
		resp.Body.Close()
		if rec.Terminal() {
			return &rec
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for terminal job state")
	return nil
}
