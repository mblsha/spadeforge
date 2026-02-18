package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/libp2p/zeroconf/v2"
)

type MDBrowser struct {
	ifaces []net.Interface
}

func NewMDBrowser() (*MDBrowser, error) {
	return &MDBrowser{ifaces: pickInterfaces()}, nil
}

func (b *MDBrowser) Browse(ctx context.Context, service, domain string, entries chan<- ServiceEntry) error {
	if b == nil {
		return fmt.Errorf("browser is required")
	}
	service = strings.TrimSpace(service)
	domain = strings.TrimSpace(domain)
	if service == "" {
		service = DefaultServiceName
	}
	if domain == "" {
		domain = DefaultDomain
	}

	rawEntries := make(chan *zeroconf.ServiceEntry)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-rawEntries:
				if !ok || entry == nil {
					return
				}
				converted := ServiceEntry{
					Instance: entry.Instance,
					HostName: entry.HostName,
					Port:     entry.Port,
					IPv4:     copyIPs(entry.AddrIPv4),
					IPv6:     copyIPs(entry.AddrIPv6),
				}
				select {
				case <-ctx.Done():
					return
				case entries <- converted:
				}
			}
		}
	}()

	if len(b.ifaces) > 0 {
		return zeroconf.Browse(ctx, service, domain, rawEntries, zeroconf.SelectIfaces(b.ifaces))
	}
	return zeroconf.Browse(ctx, service, domain, rawEntries)
}

type Advertiser struct {
	server *zeroconf.Server
}

func StartAdvertiser(instance, service, domain string, port int, txt []string) (*Advertiser, error) {
	if strings.TrimSpace(service) == "" {
		service = DefaultServiceName
	}
	if strings.TrimSpace(domain) == "" {
		domain = DefaultDomain
	}
	if strings.TrimSpace(instance) == "" {
		instance = "spadeforge"
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid advertise port: %d", port)
	}

	server, err := zeroconf.Register(instance, service, domain, port, txt, pickInterfaces())
	if err != nil {
		return nil, fmt.Errorf("start mdns advertiser: %w", err)
	}
	return &Advertiser{server: server}, nil
}

func (a *Advertiser) Close() error {
	if a == nil || a.server == nil {
		return nil
	}
	a.server.Shutdown()
	return nil
}

func copyIPs(in []net.IP) []net.IP {
	if len(in) == 0 {
		return nil
	}
	out := make([]net.IP, 0, len(in))
	for _, ip := range in {
		if ip == nil {
			continue
		}
		dup := make(net.IP, len(ip))
		copy(dup, ip)
		out = append(out, dup)
	}
	return out
}

// tailscaleCGNAT is the 100.64.0.0/10 range used by Tailscale.
var tailscaleCGNAT = net.IPNet{
	IP:   net.IP{100, 64, 0, 0},
	Mask: net.CIDRMask(10, 32),
}

func pickInterfaces() []net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]net.Interface, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isTailscale(iface) {
			continue
		}
		out = append(out, iface)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isTailscale returns true if every IPv4 unicast address on the interface is
// in the Tailscale CGNAT range (100.64.0.0/10). Interfaces without IPv4
// addresses are not considered Tailscale.
func isTailscale(iface net.Interface) bool {
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	return onlyTailscaleIPv4(addrs)
}

func onlyTailscaleIPv4(addrs []net.Addr) bool {
	if len(addrs) == 0 {
		return false
	}
	sawIPv4 := false
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue // skip IPv6, check only IPv4
		}
		sawIPv4 = true
		if !tailscaleCGNAT.Contains(ip) {
			return false
		}
	}
	return sawIPv4
}
