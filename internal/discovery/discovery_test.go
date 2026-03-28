package discovery

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestEndpointFromEntry_PrefersNonLoopbackIPv4(t *testing.T) {
	entry := ServiceEntry{
		Instance: "spadeforge",
		HostName: "host.local.",
		Port:     8080,
		IPv4:     []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("169.254.9.10"), net.ParseIP("198.51.100.10")},
		IPv6:     []net.IP{net.ParseIP("::1")},
	}
	ep, ok := EndpointFromEntry(entry)
	if !ok {
		t.Fatalf("expected endpoint")
	}
	if ep.URL != "http://198.51.100.10:8080" {
		t.Fatalf("unexpected url: %s", ep.URL)
	}
}

func TestEndpointFromEntry_UsesBracketedIPv6(t *testing.T) {
	entry := ServiceEntry{
		Instance: "spadeforge",
		HostName: "host.local.",
		Port:     8080,
		IPv6:     []net.IP{net.ParseIP("2001:db8::10")},
	}
	ep, ok := EndpointFromEntry(entry)
	if !ok {
		t.Fatalf("expected endpoint")
	}
	if ep.URL != "http://[2001:db8::10]:8080" {
		t.Fatalf("unexpected url: %s", ep.URL)
	}
}

func TestEndpointFromEntry_PrefersRoutableIPv6OverLinkLocalIPv4(t *testing.T) {
	entry := ServiceEntry{
		Instance: "spadeforge",
		HostName: "host.local.",
		Port:     8080,
		IPv4:     []net.IP{net.ParseIP("169.254.9.10")},
		IPv6:     []net.IP{net.ParseIP("fe80::10"), net.ParseIP("2001:db8::10")},
	}
	ep, ok := EndpointFromEntry(entry)
	if !ok {
		t.Fatalf("expected endpoint")
	}
	if ep.URL != "http://[2001:db8::10]:8080" {
		t.Fatalf("unexpected url: %s", ep.URL)
	}
}

func TestEndpointFromEntry_InvalidEntry(t *testing.T) {
	if _, ok := EndpointFromEntry(ServiceEntry{Port: 0}); ok {
		t.Fatalf("expected invalid endpoint")
	}
	if _, ok := EndpointFromEntry(ServiceEntry{Port: 8080}); ok {
		t.Fatalf("expected invalid endpoint without IP")
	}
}

func TestParseListenPort(t *testing.T) {
	port, err := ParseListenPort(":8080")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if port != 8080 {
		t.Fatalf("expected 8080, got %d", port)
	}

	if _, err := ParseListenPort("8080"); err == nil {
		t.Fatalf("expected error for invalid listen address")
	}
}

func TestPrimaryAdvertiseAddrForListenHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		listenHost string
		port       int
		want       string
		expectErr  bool
	}{
		{
			name:       "ipv4 literal",
			listenHost: "198.51.100.20",
			port:       8080,
			want:       "198.51.100.20:8080",
		},
		{
			name:       "ipv6 literal",
			listenHost: "2001:db8::42",
			port:       8081,
			want:       "[2001:db8::42]:8081",
		},
		{
			name:       "loopback host rejected",
			listenHost: "127.0.0.1",
			port:       8082,
			expectErr:  true,
		},
		{
			name:       "invalid port",
			listenHost: "198.51.100.20",
			port:       0,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := PrimaryAdvertiseAddrForListenHost(tt.listenHost, tt.port)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("PrimaryAdvertiseAddrForListenHost() error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("addr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiscoverWithBrowser_FindsEndpoint(t *testing.T) {
	fb := &fakeBrowser{entries: []ServiceEntry{{
		Instance: "spadeforge",
		HostName: "host.local.",
		Port:     8080,
		IPv4:     []net.IP{net.ParseIP("198.51.100.5")},
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ep, err := DiscoverWithBrowser(ctx, fb, DefaultServiceName, DefaultDomain)
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if ep.URL != "http://198.51.100.5:8080" {
		t.Fatalf("unexpected url: %s", ep.URL)
	}
}

func TestDiscoverWithBrowser_NoResult(t *testing.T) {
	fb := &fakeBrowser{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := DiscoverWithBrowser(ctx, fb, DefaultServiceName, DefaultDomain)
	if err == nil {
		t.Fatalf("expected no service error")
	}
	if !errors.Is(err, ErrNoServiceFound) {
		t.Fatalf("expected ErrNoServiceFound, got %v", err)
	}
}

func TestDiscoverWithBrowser_BrowseError(t *testing.T) {
	fb := &fakeBrowser{err: errors.New("boom")}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := DiscoverWithBrowser(ctx, fb, DefaultServiceName, DefaultDomain)
	if err == nil {
		t.Fatalf("expected browse error")
	}
}

func TestDiscoverWithBrowser_BrowseReturnsImmediatelyStillFindsEntry(t *testing.T) {
	fb := &fakeBrowser{
		asyncEntries: []ServiceEntry{{
			Instance: "spadeforge",
			HostName: "host.local.",
			Port:     8080,
			IPv4:     []net.IP{net.ParseIP("198.51.100.11")},
		}},
		asyncDelay: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	endpoint, err := DiscoverWithBrowser(ctx, fb, DefaultServiceName, DefaultDomain)
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if endpoint.URL != "http://198.51.100.11:8080" {
		t.Fatalf("unexpected url: %s", endpoint.URL)
	}
}

type fakeBrowser struct {
	entries      []ServiceEntry
	asyncEntries []ServiceEntry
	asyncDelay   time.Duration
	err          error
}

func (f *fakeBrowser) Browse(ctx context.Context, service, domain string, entries chan<- ServiceEntry) error {
	if f.err != nil {
		return f.err
	}
	for _, entry := range f.entries {
		select {
		case <-ctx.Done():
			return nil
		case entries <- entry:
		}
	}
	if len(f.asyncEntries) > 0 {
		go func() {
			timer := time.NewTimer(f.asyncDelay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			for _, entry := range f.asyncEntries {
				select {
				case <-ctx.Done():
					return
				case entries <- entry:
				}
			}
		}()
		return nil
	}
	<-ctx.Done()
	return nil
}
