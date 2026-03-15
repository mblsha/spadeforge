package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		wantMode  string
		wantRest  []string
		expectErr bool
	}{
		{name: "default", args: nil, wantMode: "server"},
		{name: "explicit server", args: []string{"server"}, wantMode: "server"},
		{name: "tui", args: []string{"tui"}, wantMode: "tui"},
		{name: "tui with args", args: []string{"tui", "--server", "http://127.0.0.1:8080"}, wantMode: "tui", wantRest: []string{"--server", "http://127.0.0.1:8080"}},
		{name: "invalid", args: []string{"bad"}, expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mode, rest, err := parseMode(tt.args)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMode() error: %v", err)
			}
			if mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantMode)
			}
			if len(rest) != len(tt.wantRest) {
				t.Fatalf("len(rest) = %d, want %d", len(rest), len(tt.wantRest))
			}
			for i := range rest {
				if rest[i] != tt.wantRest[i] {
					t.Fatalf("rest[%d] = %q, want %q", i, rest[i], tt.wantRest[i])
				}
			}
		})
	}
}

func TestParseListenHostPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		listen    string
		wantHost  string
		wantPort  int
		expectErr bool
	}{
		{name: "all interfaces", listen: ":8080", wantHost: "", wantPort: 8080},
		{name: "ipv4 loopback", listen: "127.0.0.1:8080", wantHost: "127.0.0.1", wantPort: 8080},
		{name: "ipv6 loopback", listen: "[::1]:8080", wantHost: "::1", wantPort: 8080},
		{name: "invalid", listen: "8080", expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, port, err := parseListenHostPort(tt.listen)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.listen)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseListenHostPort(%q) error: %v", tt.listen, err)
			}
			if host != tt.wantHost {
				t.Fatalf("host = %q, want %q", host, tt.wantHost)
			}
			if port != tt.wantPort {
				t.Fatalf("port = %d, want %d", port, tt.wantPort)
			}
		})
	}
}

func TestLocalServerURLForClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		listen    string
		wantURL   string
		expectErr bool
	}{
		{name: "all interfaces", listen: ":8080", wantURL: "http://127.0.0.1:8080"},
		{name: "unspecified ipv4", listen: "0.0.0.0:8081", wantURL: "http://127.0.0.1:8081"},
		{name: "unspecified ipv6", listen: "[::]:8082", wantURL: "http://127.0.0.1:8082"},
		{name: "localhost", listen: "localhost:8083", wantURL: "http://127.0.0.1:8083"},
		{name: "ipv4", listen: "192.168.1.10:8084", wantURL: "http://192.168.1.10:8084"},
		{name: "ipv6", listen: "[fd00::10]:8085", wantURL: "http://[fd00::10]:8085"},
		{name: "invalid", listen: "8080", expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := localServerURLForClient(tt.listen)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("localServerURLForClient(%q) error: %v", tt.listen, err)
			}
			if got != tt.wantURL {
				t.Fatalf("url = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestIsLoopbackListenHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host string
		want bool
	}{
		{host: "", want: false},
		{host: "127.0.0.1", want: true},
		{host: "::1", want: true},
		{host: "localhost", want: true},
		{host: "localhost.", want: true},
		{host: "127.0.0.1%lo0", want: true},
		{host: "0.0.0.0", want: false},
		{host: "::", want: false},
		{host: "192.168.1.5", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if got := isLoopbackListenHost(tt.host); got != tt.want {
				t.Fatalf("isLoopbackListenHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestResolveOpenFPGALoaderBin(t *testing.T) {
	t.Parallel()

	if _, err := resolveOpenFPGALoaderBin("sh"); err != nil {
		t.Fatalf("resolveOpenFPGALoaderBin(sh) error: %v", err)
	}
	if _, err := resolveOpenFPGALoaderBin("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatalf("expected error for missing binary")
	}
}

func TestWaitForServerHealthy(t *testing.T) {
	t.Parallel()

	t.Run("succeeds after retries", func(t *testing.T) {
		t.Parallel()

		attempts := 0
		probe := func(_ context.Context, _ string) error {
			attempts++
			if attempts < 3 {
				return errors.New("connection refused")
			}
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		if err := waitForServerHealthy(ctx, "http://127.0.0.1:8080", make(chan error), probe, time.Millisecond); err != nil {
			t.Fatalf("waitForServerHealthy() error: %v", err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})

	t.Run("returns embedded server error", func(t *testing.T) {
		t.Parallel()

		serverErrCh := make(chan error, 1)
		serverErrCh <- errors.New("listen tcp :8080: bind: address already in use")

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		err := waitForServerHealthy(
			ctx,
			"http://127.0.0.1:8080",
			serverErrCh,
			func(_ context.Context, _ string) error { return errors.New("connection refused") },
			time.Millisecond,
		)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(err.Error(), "failed before becoming healthy") {
			t.Fatalf("error = %q, want startup failure context", err)
		}
		if !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("error = %q, want listen failure detail", err)
		}
	})
}

func TestMonitorServerHealth(t *testing.T) {
	t.Parallel()

	t.Run("fails after consecutive probe errors", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch := monitorServerHealth(
			ctx,
			"http://127.0.0.1:8080",
			func(_ context.Context, _ string) error { return errors.New("connection refused") },
			time.Millisecond,
			2,
		)

		select {
		case err := <-ch:
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), "became unreachable") {
				t.Fatalf("error = %q, want unreachable context", err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("timed out waiting for health monitor failure")
		}
	})

	t.Run("resets after transient failure", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		attempts := 0
		ch := monitorServerHealth(
			ctx,
			"http://127.0.0.1:8080",
			func(_ context.Context, _ string) error {
				attempts++
				switch attempts {
				case 1:
					return errors.New("connection refused")
				case 2:
					return nil
				default:
					cancel()
					return nil
				}
			},
			time.Millisecond,
			2,
		)

		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("timed out waiting for monitor shutdown")
		}
	})
}
