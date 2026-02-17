package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
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
