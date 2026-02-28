package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
)

const (
	DefaultServiceName = "_spadeforge._tcp"
	DefaultDomain      = "local."
)

var ErrNoServiceFound = errors.New("no discovery service found")

type ServiceEntry struct {
	Instance string
	HostName string
	Port     int
	IPv4     []net.IP
	IPv6     []net.IP
}

type Endpoint struct {
	URL      string
	Instance string
	HostName string
	Port     int
}

type Browser interface {
	Browse(ctx context.Context, service, domain string, entries chan<- ServiceEntry) error
}

func Discover(ctx context.Context, service, domain string) (Endpoint, error) {
	// On macOS, prefer dns-sd (Bonjour daemon) because pure-Go mDNS browsing
	// can miss announcements on hosts with complex interface topologies.
	if runtime.GOOS == "darwin" {
		if endpoint, err := discoverWithDNSSD(ctx, service, domain); err == nil {
			return endpoint, nil
		}
	}

	browser, err := NewMDBrowser()
	if err != nil {
		return Endpoint{}, err
	}
	return DiscoverWithBrowser(ctx, browser, service, domain)
}

func DiscoverWithBrowser(ctx context.Context, browser Browser, service, domain string) (Endpoint, error) {
	if browser == nil {
		return Endpoint{}, errors.New("browser is required")
	}
	service = strings.TrimSpace(service)
	domain = strings.TrimSpace(domain)
	if service == "" {
		service = DefaultServiceName
	}
	if domain == "" {
		domain = DefaultDomain
	}

	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	entries := make(chan ServiceEntry, 32)
	errCh := make(chan error, 1)
	go func() {
		errCh <- browser.Browse(scanCtx, service, domain, entries)
	}()
	browseFinished := false

	for {
		select {
		case <-scanCtx.Done():
			if errors.Is(scanCtx.Err(), context.DeadlineExceeded) || errors.Is(scanCtx.Err(), context.Canceled) || browseFinished {
				return Endpoint{}, fmt.Errorf("discover %s failed: %w", service, ErrNoServiceFound)
			}
			return Endpoint{}, scanCtx.Err()
		case err := <-errCh:
			if err != nil {
				return Endpoint{}, fmt.Errorf("browse discovery service %s: %w", service, err)
			}
			browseFinished = true
			errCh = nil
		case entry := <-entries:
			endpoint, ok := EndpointFromEntry(entry)
			if !ok {
				continue
			}
			return endpoint, nil
		}
	}
}

func EndpointFromEntry(entry ServiceEntry) (Endpoint, bool) {
	if entry.Port <= 0 {
		return Endpoint{}, false
	}
	ip := pickIP(entry.IPv4, entry.IPv6)
	if ip == nil {
		return Endpoint{}, false
	}
	host := ip.String()
	if ip.To4() == nil {
		host = "[" + host + "]"
	}
	return Endpoint{
		URL:      "http://" + host + ":" + strconv.Itoa(entry.Port),
		Instance: entry.Instance,
		HostName: entry.HostName,
		Port:     entry.Port,
	}, true
}

func ParseListenPort(listenAddr string) (int, error) {
	trimmed := strings.TrimSpace(listenAddr)
	if trimmed == "" {
		return 0, errors.New("listen address is required")
	}
	_, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return 0, fmt.Errorf("parse listen address %q: %w", listenAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse listen port %q: %w", portStr, err)
	}
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("listen port out of range: %d", port)
	}
	return port, nil
}

func PrimaryAdvertiseAddrForListenHost(listenHost string, port int) (string, error) {
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("invalid advertise port: %d", port)
	}

	ip := primaryAdvertiseIPForListenHost(listenHost)
	if ip == nil {
		return "", errors.New("no usable advertise ip found")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

func primaryAdvertiseIPForListenHost(listenHost string) net.IP {
	trimmed := strings.TrimSpace(listenHost)
	if !isWildcardListenHost(trimmed) {
		ips, err := resolveListenHostIPs(trimmed)
		if err != nil {
			return nil
		}
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

	var ipv4 []net.IP
	var ipv6 []net.IP
	for _, iface := range pickInterfaces() {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := addrIP(addr)
			if !validAdvertisedIP(ip) || ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				ipv4 = append(ipv4, ip4)
				continue
			}
			ipv6 = append(ipv6, ip)
		}
	}
	return pickIP(ipv4, ipv6)
}

func pickIP(ipv4 []net.IP, ipv6 []net.IP) net.IP {
	for _, ip := range ipv4 {
		if validAdvertisedIP(ip) && !ip.IsLoopback() {
			return ip
		}
	}
	for _, ip := range ipv6 {
		if validAdvertisedIP(ip) && !ip.IsLoopback() {
			return ip
		}
	}
	for _, ip := range ipv4 {
		if validAdvertisedIP(ip) {
			return ip
		}
	}
	for _, ip := range ipv6 {
		if validAdvertisedIP(ip) {
			return ip
		}
	}
	return nil
}

func validAdvertisedIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified()
}
