package discovery

import (
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

func TestParseDNSSDLookupLine(t *testing.T) {
	t.Parallel()

	line := "13:18:45.109  spadeloader._spadeloader._tcp.local. can be reached at koubou.local.local.:8080 (interface 24) Flags: 1"
	value, ok := parseDNSSDLookupLine(line)
	if !ok {
		t.Fatalf("expected parse success")
	}
	if value != "koubou.local.local.\x008080" {
		t.Fatalf("value = %q", value)
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
			line: "13:20:17.187  Add  40000003      25  koubou.local.                          192.168.50.86                                4500",
			want: net.ParseIP("192.168.50.86"),
			ok:   true,
		},
		{
			name: "ipv6 with zone",
			line: "13:20:17.187  Add  40000003      24  koubou.local.                          FE80:0000:0000:0000:008C:BFC8:9632:C3B2%en1  4500",
			want: net.ParseIP("fe80::8c:bfc8:9632:c3b2"),
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
			if !got.Equal(tt.want) {
				t.Fatalf("ip = %v, want %v", got, tt.want)
			}
		})
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
