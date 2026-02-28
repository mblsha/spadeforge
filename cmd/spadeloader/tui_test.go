package main

import (
	"testing"
	"time"
)

func TestResolveTUIServerURLExplicit(t *testing.T) {
	t.Parallel()

	got, err := resolveTUIServerURL("http://127.0.0.1:8080", true, time.Second, "_spadeloader._tcp", "local.")
	if err != nil {
		t.Fatalf("resolveTUIServerURL() error: %v", err)
	}
	if got != "http://127.0.0.1:8080" {
		t.Fatalf("url = %q, want %q", got, "http://127.0.0.1:8080")
	}
}

func TestResolveTUIServerURLDiscoverDisabled(t *testing.T) {
	t.Parallel()

	_, err := resolveTUIServerURL("", false, time.Second, "_spadeloader._tcp", "local.")
	if err == nil {
		t.Fatalf("expected error")
	}
}
