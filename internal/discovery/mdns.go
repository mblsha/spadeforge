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
	return StartAdvertiserForListenHost(instance, service, domain, port, txt, "")
}

func StartAdvertiserForListenHost(instance, service, domain string, port int, txt []string, listenHost string) (*Advertiser, error) {
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

	ifaces, err := advertiseInterfacesForListenHost(listenHost)
	if err != nil {
		return nil, fmt.Errorf("select advertise interfaces: %w", err)
	}

	server, err := zeroconf.Register(instance, service, domain, port, txt, ifaces)
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
		if !isEligibleDiscoveryInterface(iface) {
			continue
		}
		out = append(out, iface)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func advertiseInterfacesForListenHost(listenHost string) ([]net.Interface, error) {
	ifaces := pickInterfaces()
	if len(ifaces) == 0 {
		return nil, nil
	}
	if isWildcardListenHost(listenHost) {
		return ifaces, nil
	}

	targets, err := resolveListenHostIPs(listenHost)
	if err != nil {
		return nil, err
	}
	targetSet := make(map[string]struct{}, len(targets))
	for _, ip := range targets {
		if ip == nil {
			continue
		}
		targetSet[ipKey(ip)] = struct{}{}
	}
	if len(targetSet) == 0 {
		return nil, fmt.Errorf("listen host %q resolved to no usable IPs", listenHost)
	}

	out := make([]net.Interface, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		if addrsContainAnyIP(addrs, targetSet) {
			out = append(out, iface)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no eligible interface matches listen host %q", listenHost)
	}
	return out, nil
}

func isEligibleDiscoveryInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 {
		return false
	}
	if iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	if isTailscale(iface) {
		return false
	}
	return true
}

func isWildcardListenHost(host string) bool {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return true
	}
	if i := strings.IndexByte(trimmed, '%'); i >= 0 {
		trimmed = trimmed[:i]
	}
	ip := net.ParseIP(trimmed)
	return ip != nil && ip.IsUnspecified()
}

func resolveListenHostIPs(host string) ([]net.IP, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, nil
	}
	noZone := trimmed
	if i := strings.IndexByte(noZone, '%'); i >= 0 {
		noZone = noZone[:i]
	}
	if ip := net.ParseIP(noZone); ip != nil {
		return []net.IP{ip}, nil
	}
	ips, err := net.LookupIP(trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolve listen host %q: %w", host, err)
	}
	return ips, nil
}

func addrsContainAnyIP(addrs []net.Addr, targets map[string]struct{}) bool {
	for _, addr := range addrs {
		ip := addrIP(addr)
		if ip == nil {
			continue
		}
		if _, ok := targets[ipKey(ip)]; ok {
			return true
		}
	}
	return false
}

func addrIP(addr net.Addr) net.IP {
	switch v := addr.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		return nil
	}
}

func ipKey(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.String()
	}
	return ip.String()
}

// isTailscale returns true for known Tailscale interface identities, with a
// conservative fallback for renamed tunnel adapters.
func isTailscale(iface net.Interface) bool {
	if isTailscaleName(iface.Name) {
		return true
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	// Fallback for renamed adapters: require both a tunnel-like interface
	// fingerprint and Tailscale CGNAT addresses.
	return isLikelyUserspaceTunnel(iface) && onlyTailscaleIPv4(addrs)
}

func isTailscaleName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "tailscale" || strings.HasPrefix(normalized, "tailscale")
}

func isLikelyUserspaceTunnel(iface net.Interface) bool {
	return iface.MTU == 1280 &&
		len(iface.HardwareAddr) == 0 &&
		iface.Flags&net.FlagRunning != 0 &&
		iface.Flags&net.FlagLoopback == 0 &&
		iface.Flags&net.FlagBroadcast == 0
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
