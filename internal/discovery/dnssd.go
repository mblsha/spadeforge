package discovery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	dnssdAddressSettleDelay   = 150 * time.Millisecond
	dnssdBrowseAttemptTimeout = 1 * time.Second
	dnssdShutdownGracePeriod  = 200 * time.Millisecond
)

type dnssdLookupResult struct {
	Host           string
	Port           int
	InterfaceIndex int
}

type dnssdAddressRecord struct {
	InterfaceIndex int
	IP             net.IP
}

var lookupBonjourHostIPs = net.LookupIP
var lookupBonjourHostIPsForInterface = lookupBonjourHostIPsWithDNSSD

func discoverWithDNSSD(ctx context.Context, service, domain string) (Endpoint, error) {
	service = strings.TrimSpace(service)
	domain = strings.TrimSpace(domain)
	if service == "" {
		service = DefaultServiceName
	}
	if domain == "" {
		domain = DefaultDomain
	}
	domainArg := trimTrailingDot(domain)

	if _, err := exec.LookPath("dns-sd"); err != nil {
		return Endpoint{}, fmt.Errorf("dns-sd is unavailable: %w", err)
	}

	for _, instance := range defaultDNSSDInstanceCandidates(service) {
		endpoint, err := resolveDNSSDEndpointForInstance(ctx, instance, service, domainArg)
		if err == nil {
			return endpoint, nil
		}
		if ctx.Err() != nil {
			return Endpoint{}, fmt.Errorf("discover %s failed: %w", service, ErrNoServiceFound)
		}
	}

	browseCtx, stopBrowse := withDNSSDAttemptTimeout(ctx, dnssdBrowseAttemptTimeout)
	defer stopBrowse()

	cmdCtx, cancel := context.WithCancel(browseCtx)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "dns-sd", "-B", service, domainArg)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Endpoint{}, fmt.Errorf("dns-sd stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return Endpoint{}, fmt.Errorf("start dns-sd -B: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	seenInstances := map[string]struct{}{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		instance, ok := parseDNSSDBrowseLine(scanner.Text(), service, domainArg)
		if !ok {
			continue
		}
		if _, seen := seenInstances[instance]; seen {
			continue
		}

		endpoint, err := resolveDNSSDEndpointForInstance(ctx, instance, service, domainArg)
		if err != nil {
			continue
		}
		seenInstances[instance] = struct{}{}
		stopDNSSDCommand(cancel, waitCh)
		return endpoint, nil
	}

	if scanErr := scanner.Err(); scanErr != nil {
		stopDNSSDCommand(cancel, waitCh)
		return Endpoint{}, fmt.Errorf("scan dns-sd browse output: %w", scanErr)
	}

	stopDNSSDCommand(cancel, waitCh)
	if ctx.Err() != nil {
		return Endpoint{}, fmt.Errorf("discover %s failed: %w", service, ErrNoServiceFound)
	}
	return Endpoint{}, fmt.Errorf("discover %s failed: %w", service, ErrNoServiceFound)
}

func lookupInstanceWithDNSSD(ctx context.Context, instance, service, domain string) (dnssdLookupResult, error) {
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "dns-sd", "-L", instance, service, domain)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return dnssdLookupResult{}, fmt.Errorf("dns-sd stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return dnssdLookupResult{}, fmt.Errorf("start dns-sd -L: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	resultCh := make(chan dnssdLookupResult, 1)
	scanErrCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if result, ok := parseDNSSDLookupLine(scanner.Text()); ok {
				resultCh <- result
				return
			}
		}
		scanErrCh <- scanner.Err()
	}()

	select {
	case <-ctx.Done():
		stopDNSSDCommand(cancel, waitCh)
		return dnssdLookupResult{}, ctx.Err()
	case result := <-resultCh:
		stopDNSSDCommand(cancel, waitCh)
		return result, nil
	case scanErr := <-scanErrCh:
		stopDNSSDCommand(cancel, waitCh)
		if scanErr != nil {
			return dnssdLookupResult{}, fmt.Errorf("scan dns-sd output: %w", scanErr)
		}
		return dnssdLookupResult{}, ErrNoServiceFound
	case waitErr := <-waitCh:
		if waitErr != nil && ctx.Err() == nil {
			return dnssdLookupResult{}, fmt.Errorf("dns-sd exited: %w", waitErr)
		}
		return dnssdLookupResult{}, ErrNoServiceFound
	}
}

func resolveDNSSDEndpointForInstance(ctx context.Context, instance, service, domain string) (Endpoint, error) {
	result, err := lookupInstanceWithDNSSD(ctx, instance, service, domain)
	if err != nil {
		return Endpoint{}, err
	}
	normalizedHost, urlHost, err := resolveDNSSDEndpointHosts(ctx, result.Host, result.InterfaceIndex)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{
		URL:      fmt.Sprintf("http://%s:%d", urlHost, result.Port),
		Instance: instance,
		HostName: normalizedHost,
		Port:     result.Port,
	}, nil
}

func resolveDNSSDEndpointHosts(ctx context.Context, host string, interfaceIndex int) (string, string, error) {
	normalizedHost := normalizeBonjourHost(host)
	if normalizedHost == "" {
		return "", "", fmt.Errorf("dns-sd resolved no usable host")
	}

	if ip := net.ParseIP(normalizedHost); ip != nil {
		return normalizedHost, formatURLHost(ip), nil
	}

	if interfaceIndex > 0 {
		ips, err := lookupBonjourHostIPsForInterface(ctx, normalizedHost, interfaceIndex)
		if err == nil {
			if ip := pickResolvedEndpointIP(ips); ip != nil {
				return normalizedHost, formatURLHost(ip), nil
			}
		}
	}

	ips, err := lookupBonjourHostIPs(normalizedHost)
	if err != nil {
		return "", "", fmt.Errorf("resolve bonjour host %q: %w", normalizedHost, err)
	}

	ip := pickResolvedEndpointIP(ips)
	if ip == nil {
		return "", "", fmt.Errorf("bonjour host %q resolved to no usable ips", normalizedHost)
	}
	return normalizedHost, formatURLHost(ip), nil
}

func pickResolvedEndpointIP(ips []net.IP) net.IP {
	var ipv4 []net.IP
	var ipv6 []net.IP

	for _, ip := range ips {
		if !validAdvertisedIP(ip) || ip.IsLoopback() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			ipv4 = append(ipv4, ip4)
			continue
		}
		ipv6 = append(ipv6, ip)
	}

	return pickIP(ipv4, ipv6)
}

func lookupBonjourHostIPsWithDNSSD(ctx context.Context, host string, interfaceIndex int) ([]net.IP, error) {
	if interfaceIndex <= 0 {
		return nil, ErrNoServiceFound
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "dns-sd", "-G", "v4v6", host)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dns-sd stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start dns-sd -G: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	recordCh := make(chan dnssdAddressRecord, 32)
	scanErrCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			record, ok := parseDNSSDAddressLine(scanner.Text())
			if !ok {
				continue
			}
			select {
			case <-cmdCtx.Done():
				return
			case recordCh <- record:
			}
		}
		scanErrCh <- scanner.Err()
	}()

	var settleTimer *time.Timer
	var settleCh <-chan time.Time
	ips := make([]net.IP, 0, 4)
	seen := make(map[string]struct{})
	stopTimer := func() {
		if settleTimer == nil {
			return
		}
		if !settleTimer.Stop() {
			select {
			case <-settleTimer.C:
			default:
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopDNSSDCommand(cancel, waitCh)
			if len(ips) > 0 {
				return ips, nil
			}
			return nil, ctx.Err()
		case record := <-recordCh:
			if record.InterfaceIndex != interfaceIndex {
				continue
			}
			key := ipKey(record.IP)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			ips = append(ips, record.IP)

			if settleTimer == nil {
				settleTimer = time.NewTimer(dnssdAddressSettleDelay)
				settleCh = settleTimer.C
			} else {
				stopTimer()
				settleTimer.Reset(dnssdAddressSettleDelay)
			}
		case <-settleCh:
			stopDNSSDCommand(cancel, waitCh)
			return ips, nil
		case scanErr := <-scanErrCh:
			stopDNSSDCommand(cancel, waitCh)
			if len(ips) > 0 {
				return ips, nil
			}
			if scanErr != nil {
				return nil, fmt.Errorf("scan dns-sd output: %w", scanErr)
			}
			return nil, ErrNoServiceFound
		case waitErr := <-waitCh:
			if len(ips) > 0 {
				stopTimer()
				return ips, nil
			}
			if waitErr != nil && ctx.Err() == nil {
				return nil, fmt.Errorf("dns-sd exited: %w", waitErr)
			}
			return nil, ErrNoServiceFound
		}
	}
}

func withDNSSDAttemptTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func stopDNSSDCommand(cancel context.CancelFunc, waitCh <-chan error) {
	if cancel != nil {
		cancel()
	}
	if waitCh == nil {
		return
	}
	timer := time.NewTimer(dnssdShutdownGracePeriod)
	defer timer.Stop()
	select {
	case <-waitCh:
	case <-timer.C:
	}
}

func parseDNSSDBrowseLine(line, service, domain string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 7 {
		return "", false
	}
	if !strings.EqualFold(fields[1], "Add") {
		return "", false
	}
	lineDomain := trimTrailingDot(fields[4])
	lineService := trimTrailingDot(fields[5])
	if !strings.EqualFold(lineDomain, trimTrailingDot(domain)) {
		return "", false
	}
	if !strings.EqualFold(lineService, trimTrailingDot(service)) {
		return "", false
	}
	instance := strings.TrimSpace(strings.Join(fields[6:], " "))
	return instance, instance != ""
}

func defaultDNSSDInstanceCandidates(service string) []string {
	switch strings.ToLower(trimTrailingDot(strings.TrimSpace(service))) {
	case "_spadeloader._tcp":
		return []string{"spadeloader"}
	case trimTrailingDot(DefaultServiceName):
		return []string{"spadeforge"}
	default:
		return nil
	}
}

func parseDNSSDLookupLine(line string) (dnssdLookupResult, bool) {
	marker := " can be reached at "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return dnssdLookupResult{}, false
	}
	rest := strings.TrimSpace(line[idx+len(marker):])
	interfaceIndex := 0
	if ifaceStart := strings.Index(rest, "(interface "); ifaceStart >= 0 {
		ifacePart := rest[ifaceStart+len("(interface "):]
		if ifaceEnd := strings.IndexByte(ifacePart, ')'); ifaceEnd >= 0 {
			if parsed, err := strconv.Atoi(strings.TrimSpace(ifacePart[:ifaceEnd])); err == nil && parsed > 0 {
				interfaceIndex = parsed
			}
		}
	}
	if cut := strings.Index(rest, " ("); cut >= 0 {
		rest = strings.TrimSpace(rest[:cut])
	}
	sep := strings.LastIndex(rest, ":")
	if sep < 0 {
		return dnssdLookupResult{}, false
	}
	host := strings.TrimSpace(rest[:sep])
	portStr := strings.TrimSpace(rest[sep+1:])
	if host == "" || portStr == "" {
		return dnssdLookupResult{}, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return dnssdLookupResult{}, false
	}
	return dnssdLookupResult{
		Host:           host,
		Port:           port,
		InterfaceIndex: interfaceIndex,
	}, true
}

func parseDNSSDAddressLine(line string) (dnssdAddressRecord, bool) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return dnssdAddressRecord{}, false
	}
	if !strings.EqualFold(fields[1], "Add") {
		return dnssdAddressRecord{}, false
	}
	interfaceIndex, err := strconv.Atoi(strings.TrimSpace(fields[3]))
	if err != nil || interfaceIndex <= 0 {
		return dnssdAddressRecord{}, false
	}
	addr := strings.TrimSpace(fields[5])
	if addr == "" {
		return dnssdAddressRecord{}, false
	}
	if zoneIdx := strings.Index(addr, "%"); zoneIdx >= 0 {
		addr = addr[:zoneIdx]
	}
	if strings.EqualFold(addr, "0.0.0.0") {
		return dnssdAddressRecord{}, false
	}
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsUnspecified() {
		return dnssdAddressRecord{}, false
	}
	return dnssdAddressRecord{InterfaceIndex: interfaceIndex, IP: ip}, true
}

func normalizeBonjourHost(host string) string {
	trimmed := strings.TrimSpace(host)
	trimmed = trimTrailingDot(trimmed)
	lower := strings.ToLower(trimmed)
	if strings.HasSuffix(lower, ".local.local") {
		trimmed = trimmed[:len(trimmed)-len(".local")]
	}
	return trimmed
}

func trimTrailingDot(s string) string {
	trimmed := strings.TrimSpace(s)
	return strings.TrimSuffix(trimmed, ".")
}
