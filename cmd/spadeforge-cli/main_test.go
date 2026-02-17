package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/discovery"
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
