package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mblsha/spadeforge/internal/job"
)

const defaultAuthHeader = "X-Build-Token"

type HTTPClient struct {
	BaseURL    string
	Token      string
	AuthHeader string
	Client     *http.Client
}

func (c *HTTPClient) SubmitBundle(ctx context.Context, bundle []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(bundle); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.buildURL("/v1/jobs"), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c.setAuth(req)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("submit failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.JobID == "" {
		return "", fmt.Errorf("submit response missing job_id")
	}
	return payload.JobID, nil
}

func (c *HTTPClient) GetJob(ctx context.Context, jobID string) (*job.Record, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL(path.Join("/v1/jobs", jobID)), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get job failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var record job.Record
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *HTTPClient) WaitForTerminal(ctx context.Context, jobID string, pollInterval time.Duration) (*job.Record, error) {
	return c.WaitForTerminalWithProgress(ctx, jobID, pollInterval, nil)
}

func (c *HTTPClient) WaitForTerminalWithProgress(
	ctx context.Context,
	jobID string,
	pollInterval time.Duration,
	onUpdate func(record *job.Record),
) (*job.Record, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		record, err := c.GetJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if onUpdate != nil {
			onUpdate(record)
		}
		if record.Terminal() {
			return record, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *HTTPClient) DownloadArtifacts(ctx context.Context, jobID string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL(path.Join("/v1/jobs", jobID, "artifacts")), nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download artifacts failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	_, err = io.Copy(out, resp.Body)
	return err
}

func (c *HTTPClient) GetDiagnostics(ctx context.Context, jobID string) (*job.DiagnosticsReport, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL(path.Join("/v1/jobs", jobID, "diagnostics")), nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get diagnostics failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var report job.DiagnosticsReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, err
	}
	return &report, nil
}

func (c *HTTPClient) GetLogTail(ctx context.Context, jobID string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	reqURL := c.buildURL(path.Join("/v1/jobs", jobID, "tail"))
	parsed, err := url.Parse(reqURL)
	if err != nil {
		return "", err
	}
	q := parsed.Query()
	q.Set("lines", strconv.Itoa(lines))
	parsed.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	c.setAuth(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get log tail failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return string(raw), nil
}

func (c *HTTPClient) StreamEvents(ctx context.Context, jobID string, since int64, onEvent func(*job.Event)) error {
	reqURL := c.buildURL(path.Join("/v1/jobs", jobID, "events"))
	parsed, err := url.Parse(reqURL)
	if err != nil {
		return err
	}
	if since > 0 {
		q := parsed.Query()
		q.Set("since", strconv.FormatInt(since, 10))
		parsed.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	c.setAuth(req)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stream events failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	dataLines := make([]string, 0, 4)
	dispatch := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		var ev job.Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return fmt.Errorf("decode sse event: %w", err)
		}
		if onEvent != nil {
			onEvent(&ev)
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			dataLines = append(dataLines, data)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

func (c *HTTPClient) buildURL(pathPart string) string {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	u, err := url.Parse(base)
	if err != nil {
		return base + pathPart
	}
	u.Path = path.Join(u.Path, pathPart)
	return u.String()
}

func (c *HTTPClient) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func (c *HTTPClient) setAuth(req *http.Request) {
	header := c.AuthHeader
	if header == "" {
		header = defaultAuthHeader
	}
	if strings.TrimSpace(c.Token) != "" {
		req.Header.Set(header, c.Token)
	}
}
