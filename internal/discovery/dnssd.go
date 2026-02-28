package discovery

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

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

	cmdCtx, cancel := context.WithCancel(ctx)
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
		seenInstances[instance] = struct{}{}

		endpoint, err := resolveDNSSDEndpointForInstance(ctx, instance, service, domainArg)
		if err != nil {
			continue
		}
		if endpointHealthy(ctx, endpoint.URL) {
			cancel()
			_ = <-waitCh
			return endpoint, nil
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		cancel()
		_ = <-waitCh
		return Endpoint{}, fmt.Errorf("scan dns-sd browse output: %w", scanErr)
	}

	cancel()
	_ = <-waitCh
	if ctx.Err() != nil {
		return Endpoint{}, fmt.Errorf("discover %s failed: %w", service, ErrNoServiceFound)
	}
	return Endpoint{}, fmt.Errorf("discover %s failed: %w", service, ErrNoServiceFound)
}

func lookupInstanceWithDNSSD(ctx context.Context, instance, service, domain string) (string, int, error) {
	value, err := runDNSSDForFirstValue(ctx, []string{"-L", instance, service, domain}, parseDNSSDLookupLine)
	if err != nil {
		return "", 0, err
	}
	host, portStr, ok := strings.Cut(value, "\x00")
	if !ok {
		return "", 0, fmt.Errorf("invalid dns-sd lookup result")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid dns-sd lookup port %q", portStr)
	}
	return host, port, nil
}

func resolveDNSSDEndpointForInstance(ctx context.Context, instance, service, domain string) (Endpoint, error) {
	host, port, err := lookupInstanceWithDNSSD(ctx, instance, service, domain)
	if err != nil {
		return Endpoint{}, err
	}
	normalizedHost := normalizeBonjourHost(host)
	if normalizedHost == "" {
		return Endpoint{}, fmt.Errorf("dns-sd resolved no usable host")
	}

	urlHost := normalizedHost
	if ip := net.ParseIP(normalizedHost); ip != nil {
		if ip.To4() != nil {
			urlHost = ip.String()
		} else {
			urlHost = "[" + ip.String() + "]"
		}
	}
	return Endpoint{
		URL:      fmt.Sprintf("http://%s:%d", urlHost, port),
		Instance: instance,
		HostName: normalizedHost,
		Port:     port,
	}, nil
}

func resolveHostWithDNSSD(ctx context.Context, host string) ([]net.IP, error) {
	value, err := runDNSSDForFirstValue(ctx, []string{"-G", "v4v6", trimTrailingDot(host)}, func(line string) (string, bool) {
		ip, ok := parseDNSSDAddressLine(line)
		if !ok {
			return "", false
		}
		return ip.String(), true
	})
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return nil, fmt.Errorf("invalid dns-sd address %q", value)
	}
	return []net.IP{ip}, nil
}

func endpointHealthy(ctx context.Context, baseURL string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func runDNSSDForFirstValue(
	ctx context.Context,
	args []string,
	parseLine func(line string) (string, bool),
) (string, error) {
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "dns-sd", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("dns-sd stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start dns-sd %s: %w", strings.Join(args, " "), err)
	}

	resultCh := make(chan string, 1)
	scanErrCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if value, ok := parseLine(scanner.Text()); ok {
				resultCh <- value
				return
			}
		}
		scanErrCh <- scanner.Err()
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		cancel()
		<-waitCh
		return "", ctx.Err()
	case value := <-resultCh:
		cancel()
		<-waitCh
		return value, nil
	case scanErr := <-scanErrCh:
		cancel()
		<-waitCh
		if scanErr != nil {
			return "", fmt.Errorf("scan dns-sd output: %w", scanErr)
		}
		return "", ErrNoServiceFound
	case waitErr := <-waitCh:
		if waitErr != nil && ctx.Err() == nil {
			return "", fmt.Errorf("dns-sd exited: %w", waitErr)
		}
		return "", ErrNoServiceFound
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

func parseDNSSDLookupLine(line string) (string, bool) {
	marker := " can be reached at "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(line[idx+len(marker):])
	if cut := strings.Index(rest, " ("); cut >= 0 {
		rest = strings.TrimSpace(rest[:cut])
	}
	sep := strings.LastIndex(rest, ":")
	if sep < 0 {
		return "", false
	}
	host := strings.TrimSpace(rest[:sep])
	port := strings.TrimSpace(rest[sep+1:])
	if host == "" || port == "" {
		return "", false
	}
	return host + "\x00" + port, true
}

func parseDNSSDAddressLine(line string) (net.IP, bool) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return nil, false
	}
	if !strings.EqualFold(fields[1], "Add") {
		return nil, false
	}
	addr := strings.TrimSpace(fields[5])
	if addr == "" {
		return nil, false
	}
	if zoneIdx := strings.Index(addr, "%"); zoneIdx >= 0 {
		addr = addr[:zoneIdx]
	}
	if strings.EqualFold(addr, "0.0.0.0") {
		return nil, false
	}
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsUnspecified() {
		return nil, false
	}
	return ip, true
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
