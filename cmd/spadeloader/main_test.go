package main

import "testing"

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
