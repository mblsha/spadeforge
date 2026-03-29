package discovery

import (
	"context"
	"net"
	"testing"
)

func TestParseDNSSDBrowseLine(t *testing.T) {
	t.Parallel()

	line := "13:18:43.084  Add        3  25 local.               _spadeloader._tcp.   spadeloader"
	instance, ok := parseDNSSDBrowseLine(line, "_spadeloader._tcp", "local")
	if !ok {
		t.Fatalf("expected parse success")
	}
	if instance != "spadeloader" {
		t.Fatalf("instance = %q, want %q", instance, "spadeloader")
	}
}

func TestDefaultDNSSDInstanceCandidates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		service string
		want    []string
	}{
		{service: "_spadeloader._tcp", want: []string{"spadeloader"}},
		{service: "_spadeloader._tcp.", want: []string{"spadeloader"}},
		{service: DefaultServiceName, want: []string{"spadeforge"}},
		{service: "_custom._tcp", want: nil},
		{service: "", want: nil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.service, func(t *testing.T) {
			t.Parallel()

			got := defaultDNSSDInstanceCandidates(tt.service)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("candidate[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseDNSSDLookupLine(t *testing.T) {
	t.Parallel()

	line := "13:18:45.109  spadeloader._spadeloader._tcp.local. can be reached at koubou.local.local.:8080 (interface 24) Flags: 1"
	value, ok := parseDNSSDLookupLine(line)
	if !ok {
		t.Fatalf("expected parse success")
	}
	if value.Host != "koubou.local.local." {
		t.Fatalf("host = %q, want %q", value.Host, "koubou.local.local.")
	}
	if value.Port != 8080 {
		t.Fatalf("port = %d, want %d", value.Port, 8080)
	}
	if value.InterfaceIndex != 24 {
		t.Fatalf("interface = %d, want %d", value.InterfaceIndex, 24)
	}
}

func TestParseDNSSDAddressLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want net.IP
		ok   bool
	}{
		{
			name: "ipv4",
			line: "13:20:17.187  Add  40000003      25  koubou.local.                          192.0.2.10                                   4500",
			want: net.ParseIP("192.0.2.10"),
			ok:   true,
		},
		{
			name: "ipv6 with zone",
			line: "13:20:17.187  Add  40000003      24  koubou.local.                          FE80:0000:0000:0000:0000:0000:0000:0010%en1  4500",
			want: net.ParseIP("fe80::10"),
			ok:   true,
		},
		{
			name: "no such record placeholder",
			line: "13:20:17.187  Add  40000003      42  koubou.local.                          0.0.0.0                                      1   No Such Record",
			ok:   false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseDNSSDAddressLine(tt.line)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got.InterfaceIndex <= 0 {
				t.Fatalf("interface = %d, want > 0", got.InterfaceIndex)
			}
			if !got.IP.Equal(tt.want) {
				t.Fatalf("ip = %v, want %v", got.IP, tt.want)
			}
		})
	}
}

func TestResolveDNSSDEndpointHosts_PrefersInterfaceScopedIPs(t *testing.T) {
	originalLookup := lookupBonjourHostIPs
	originalInterfaceLookup := lookupBonjourHostIPsForInterface
	lookupBonjourHostIPsForInterface = func(_ context.Context, host string, interfaceIndex int) ([]net.IP, error) {
		if host != "koubou.local" {
			t.Fatalf("lookup host = %q, want %q", host, "koubou.local")
		}
		if interfaceIndex != 25 {
			t.Fatalf("interface index = %d, want %d", interfaceIndex, 25)
		}
		return []net.IP{
			net.ParseIP("fe80::10"),
			net.ParseIP("192.0.2.10"),
		}, nil
	}
	lookupBonjourHostIPs = func(host string) ([]net.IP, error) {
		if host != "koubou.local" {
			t.Fatalf("lookup host = %q, want %q", host, "koubou.local")
		}
		return []net.IP{
			net.ParseIP("fe80::10"),
			net.ParseIP("169.254.10.20"),
			net.ParseIP("192.0.2.10"),
			net.ParseIP("2001:db8::10"),
			net.ParseIP("198.51.100.10"),
		}, nil
	}
	t.Cleanup(func() {
		lookupBonjourHostIPsForInterface = originalInterfaceLookup
		lookupBonjourHostIPs = originalLookup
	})

	hostName, urlHost, err := resolveDNSSDEndpointHosts(context.Background(), "koubou.local.local.", 25)
	if err != nil {
		t.Fatalf("resolveDNSSDEndpointHosts() error: %v", err)
	}
	if hostName != "koubou.local" {
		t.Fatalf("hostName = %q, want %q", hostName, "koubou.local")
	}
	if urlHost != "192.0.2.10" {
		t.Fatalf("urlHost = %q, want %q", urlHost, "192.0.2.10")
	}
}

func TestResolveDNSSDEndpointHosts_FallsBackToGlobalLookup(t *testing.T) {
	originalLookup := lookupBonjourHostIPs
	originalInterfaceLookup := lookupBonjourHostIPsForInterface
	lookupBonjourHostIPsForInterface = func(_ context.Context, _ string, _ int) ([]net.IP, error) {
		return nil, ErrNoServiceFound
	}
	lookupBonjourHostIPs = func(host string) ([]net.IP, error) {
		if host != "koubou.local" {
			t.Fatalf("lookup host = %q, want %q", host, "koubou.local")
		}
		return []net.IP{
			net.ParseIP("fe80::10"),
			net.ParseIP("198.51.100.10"),
			net.ParseIP("2001:db8::10"),
		}, nil
	}
	t.Cleanup(func() {
		lookupBonjourHostIPsForInterface = originalInterfaceLookup
		lookupBonjourHostIPs = originalLookup
	})

	hostName, urlHost, err := resolveDNSSDEndpointHosts(context.Background(), "koubou.local.local.", 25)
	if err != nil {
		t.Fatalf("resolveDNSSDEndpointHosts() error: %v", err)
	}
	if hostName != "koubou.local" {
		t.Fatalf("hostName = %q, want %q", hostName, "koubou.local")
	}
	if urlHost != "198.51.100.10" {
		t.Fatalf("urlHost = %q, want %q", urlHost, "198.51.100.10")
	}
}

func TestResolveDNSSDEndpointHosts_IPv6Literal(t *testing.T) {
	hostName, urlHost, err := resolveDNSSDEndpointHosts(context.Background(), "2001:db8::42", 0)
	if err != nil {
		t.Fatalf("resolveDNSSDEndpointHosts() error: %v", err)
	}
	if hostName != "2001:db8::42" {
		t.Fatalf("hostName = %q, want %q", hostName, "2001:db8::42")
	}
	if urlHost != "[2001:db8::42]" {
		t.Fatalf("urlHost = %q, want %q", urlHost, "[2001:db8::42]")
	}
}

func TestNormalizeBonjourHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{in: "koubou.local.local.", want: "koubou.local"},
		{in: "spadeloader.local.", want: "spadeloader.local"},
		{in: " plain-host ", want: "plain-host"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := normalizeBonjourHost(tt.in); got != tt.want {
				t.Fatalf("normalizeBonjourHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
