package discovery

import (
	"net"
	"testing"
)

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
