package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/client"
	"github.com/mblsha/spadeforge/internal/discovery"
	"github.com/mblsha/spadeforge/internal/job"
)

func TestResolveServerURL_ExplicitWins(t *testing.T) {
	url, err := resolveServerURL("http://example:8080", true, time.Second, discovery.DefaultServiceName, discovery.DefaultDomain)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if url != "http://example:8080" {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestResolveServerURL_DiscoverDisabledWithoutServer(t *testing.T) {
	_, err := resolveServerURL("", false, time.Second, discovery.DefaultServiceName, discovery.DefaultDomain)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestResolveServerURL_DiscoverSuccess(t *testing.T) {
	orig := discoverFn
	t.Cleanup(func() {
		discoverFn = orig
	})
	discoverFn = func(ctx context.Context, service, domain string) (discovery.Endpoint, error) {
		return discovery.Endpoint{URL: "http://10.0.0.9:8080", Instance: "spadeforge", HostName: "builder.local."}, nil
	}

	url, err := resolveServerURL("", true, 200*time.Millisecond, discovery.DefaultServiceName, discovery.DefaultDomain)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if url != "http://10.0.0.9:8080" {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestResolveServerURL_DiscoverError(t *testing.T) {
	orig := discoverFn
	t.Cleanup(func() {
		discoverFn = orig
	})
	discoverFn = func(ctx context.Context, service, domain string) (discovery.Endpoint, error) {
		return discovery.Endpoint{}, errors.New("no service")
	}

	_, err := resolveServerURL("", true, 200*time.Millisecond, discovery.DefaultServiceName, discovery.DefaultDomain)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestWaitForTerminalViaEvents_StreamEndsEarlyFallsBackToPolling(t *testing.T) {
	t.Parallel()

	var getCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/j1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"job_id\":\"j1\",\"state\":\"RUNNING\",\"step\":\"synth\",\"message\":\"building\"}\n\n"))
			return
		case "/v1/jobs/j1":
			call := getCalls.Add(1)
			state := job.StateRunning
			message := "running"
			if call >= 2 {
				state = job.StateSucceeded
				message = "done"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&job.Record{
				ID:      "j1",
				State:   state,
				Message: message,
			})
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c := &client.HTTPClient{BaseURL: ts.URL, Client: ts.Client()}
	rec, err := waitForTerminalViaEvents(context.Background(), c, "j1", 5*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForTerminalViaEvents() error: %v", err)
	}
	if rec.State != job.StateSucceeded {
		t.Fatalf("state = %s, want %s", rec.State, job.StateSucceeded)
	}
	if getCalls.Load() < 2 {
		t.Fatalf("expected fallback polling after stream close, getCalls=%d", getCalls.Load())
	}
}

func TestWaitForTerminalViaEvents_StopsWhenTerminalAfterStream(t *testing.T) {
	t.Parallel()

	var getCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/j1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			return
		case "/v1/jobs/j1":
			getCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&job.Record{
				ID:      "j1",
				State:   job.StateFailed,
				Message: "failed",
			})
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c := &client.HTTPClient{BaseURL: ts.URL, Client: ts.Client()}
	rec, err := waitForTerminalViaEvents(context.Background(), c, "j1", 5*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForTerminalViaEvents() error: %v", err)
	}
	if rec.State != job.StateFailed {
		t.Fatalf("state = %s, want %s", rec.State, job.StateFailed)
	}
	if getCalls.Load() != 1 {
		t.Fatalf("expected single terminal fetch, getCalls=%d", getCalls.Load())
	}
}
