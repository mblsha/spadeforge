package discovery

import (
	"net"
	"testing"
)

func TestIsWildcardListenHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		host string
		want bool
	}{
		{host: "", want: true},
		{host: "0.0.0.0", want: true},
		{host: "::", want: true},
		{host: "::%eth0", want: true},
		{host: "192.168.1.10", want: false},
		{host: "fd00::10", want: false},
		{host: "builder.local", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.host, func(t *testing.T) {
			t.Parallel()
			if got := isWildcardListenHost(tt.host); got != tt.want {
				t.Fatalf("isWildcardListenHost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestResolveListenHostIPs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "ipv4 literal", host: "192.168.1.20", want: "192.168.1.20"},
		{name: "ipv6 literal", host: "fd00::42", want: "fd00::42"},
		{name: "ipv6 literal with zone", host: "fe80::1%eth0", want: "fe80::1"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ips, err := resolveListenHostIPs(tt.host)
			if err != nil {
				t.Fatalf("resolveListenHostIPs(%q) error: %v", tt.host, err)
			}
			if len(ips) != 1 {
				t.Fatalf("resolveListenHostIPs(%q) len = %d, want 1", tt.host, len(ips))
			}
			if got := ipKey(ips[0]); got != tt.want {
				t.Fatalf("resolveListenHostIPs(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestAddrsContainAnyIP(t *testing.T) {
	t.Parallel()

	targets := map[string]struct{}{
		"192.168.1.20": {},
		"fd00::42":     {},
	}

	tests := []struct {
		name  string
		addrs []net.Addr
		want  bool
	}{
		{
			name: "match ipv4",
			addrs: []net.Addr{
				mustIPNetAddr(t, "192.168.1.20", 24),
			},
			want: true,
		},
		{
			name: "match ipv6",
			addrs: []net.Addr{
				mustIPNetAddr(t, "fd00::42", 64),
			},
			want: true,
		},
		{
			name: "no match",
			addrs: []net.Addr{
				mustIPNetAddr(t, "10.0.0.8", 24),
				mustIPNetAddr(t, "fd00::99", 64),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := addrsContainAnyIP(tt.addrs, targets); got != tt.want {
				t.Fatalf("addrsContainAnyIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTailscaleName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "exact", in: "Tailscale", want: true},
		{name: "prefix", in: "tailscale0", want: true},
		{name: "trimmed", in: "  tailscale  ", want: true},
		{name: "not tailscale", in: "Ethernet", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isTailscaleName(tt.in); got != tt.want {
				t.Fatalf("isTailscaleName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsLikelyUserspaceTunnel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		iface net.Interface
		want  bool
	}{
		{
			name: "wintun-like interface",
			iface: net.Interface{
				MTU:   1280,
				Flags: net.FlagUp | net.FlagRunning,
			},
			want: true,
		},
		{
			name: "broadcast-capable interface",
			iface: net.Interface{
				MTU:          1280,
				Flags:        net.FlagUp | net.FlagRunning | net.FlagBroadcast,
				HardwareAddr: []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55},
			},
			want: false,
		},
		{
			name: "not running",
			iface: net.Interface{
				MTU:   1280,
				Flags: net.FlagUp,
			},
			want: false,
		},
		{
			name: "different mtu",
			iface: net.Interface{
				MTU:   1500,
				Flags: net.FlagUp | net.FlagRunning,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isLikelyUserspaceTunnel(tt.iface); got != tt.want {
				t.Fatalf("isLikelyUserspaceTunnel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOnlyTailscaleIPv4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		addrs []net.Addr
		want  bool
	}{
		{
			name: "no addresses",
			want: false,
		},
		{
			name: "tailscale ipv4 only",
			addrs: []net.Addr{
				mustCIDRAddr(t, "100.100.2.5/32"),
			},
			want: true,
		},
		{
			name: "tailscale and non tailscale ipv4",
			addrs: []net.Addr{
				mustCIDRAddr(t, "100.100.2.5/32"),
				mustCIDRAddr(t, "192.168.1.10/24"),
			},
			want: false,
		},
		{
			name: "ipv6 only",
			addrs: []net.Addr{
				mustCIDRAddr(t, "fd00::10/64"),
			},
			want: false,
		},
		{
			name: "ipv6 plus tailscale ipv4",
			addrs: []net.Addr{
				mustCIDRAddr(t, "fd00::10/64"),
				mustCIDRAddr(t, "100.127.1.1/32"),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := onlyTailscaleIPv4(tt.addrs); got != tt.want {
				t.Fatalf("onlyTailscaleIPv4() = %v, want %v", got, tt.want)
			}
		})
	}
}

func mustCIDRAddr(t *testing.T, cidr string) net.Addr {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse cidr %q: %v", cidr, err)
	}
	return n
}

func mustIPNetAddr(t *testing.T, ipStr string, prefixLen int) net.Addr {
	t.Helper()
	ip := net.ParseIP(ipStr)
	if ip == nil {
		t.Fatalf("parse ip %q: invalid", ipStr)
	}
	bits := 128
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
		bits = 32
	}
	return &net.IPNet{
		IP:   ip,
		Mask: net.CIDRMask(prefixLen, bits),
	}
}
