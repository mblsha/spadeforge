package discovery

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

const dnssdRegisterActiveTimeout = 3 * time.Second

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
	cancel context.CancelFunc
	waitCh <-chan error
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

	if runtime.GOOS == "darwin" {
		advertiser, err := startDNSSDAdvertiser(instance, service, domain, port, txt)
		if err == nil {
			return advertiser, nil
		}
		if _, lookErr := exec.LookPath("dns-sd"); lookErr == nil {
			return nil, err
		}
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
	if a == nil {
		return nil
	}
	if a.server != nil {
		a.server.Shutdown()
	}
	if a.cancel != nil {
		stopDNSSDCommand(a.cancel, a.waitCh)
	}
	return nil
}

func startDNSSDAdvertiser(instance, service, domain string, port int, txt []string) (*Advertiser, error) {
	if _, err := exec.LookPath("dns-sd"); err != nil {
		return nil, fmt.Errorf("dns-sd is unavailable: %w", err)
	}

	domainArg := trimTrailingDot(strings.TrimSpace(domain))
	if domainArg == "" {
		domainArg = trimTrailingDot(DefaultDomain)
	}

	args := []string{
		"-R",
		strings.TrimSpace(instance),
		strings.TrimSpace(service),
		domainArg,
		strconv.Itoa(port),
	}
	for _, record := range txt {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		args = append(args, record)
	}

	cmdCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cmdCtx, "dns-sd", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dns-sd stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start dns-sd -R: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	activeCh := make(chan struct{}, 1)
	scanErrCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		activeSeen := false
		for scanner.Scan() {
			if !activeSeen && isDNSSDRegistrationActiveLine(scanner.Text()) {
				activeSeen = true
				activeCh <- struct{}{}
			}
		}
		scanErrCh <- scanner.Err()
	}()

	timer := time.NewTimer(dnssdRegisterActiveTimeout)
	defer timer.Stop()

	select {
	case <-activeCh:
		return &Advertiser{cancel: cancel, waitCh: waitCh}, nil
	case scanErr := <-scanErrCh:
		stopDNSSDCommand(cancel, waitCh)
		if scanErr != nil {
			return nil, fmt.Errorf("scan dns-sd -R output: %w", scanErr)
		}
		return nil, fmt.Errorf("dns-sd -R exited before registration became active")
	case waitErr := <-waitCh:
		cancel()
		if waitErr != nil {
			return nil, fmt.Errorf("dns-sd -R exited: %w", waitErr)
		}
		return nil, fmt.Errorf("dns-sd -R exited before registration became active")
	case <-timer.C:
		stopDNSSDCommand(cancel, waitCh)
		return nil, fmt.Errorf("dns-sd -R did not report an active registration within %s", dnssdRegisterActiveTimeout)
	}
}

func isDNSSDRegistrationActiveLine(line string) bool {
	return strings.Contains(strings.TrimSpace(line), "Name now registered and active")
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
		out = append(out, net.IP(bytes.Clone(ip)))
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
