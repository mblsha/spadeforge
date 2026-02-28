package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mblsha/spadeforge/internal/discovery"
	"github.com/mblsha/spadeforge/internal/spadeloader/client"
	loaderui "github.com/mblsha/spadeforge/internal/spadeloader/tui"
)

func runTUI(args []string) error {
	fs := flag.NewFlagSet("spadeloader tui", flag.ContinueOnError)
	fs.Usage = usage

	serverURL := fs.String("server", strings.TrimSpace(os.Getenv("SPADELOADER_SERVER")), "spadeloader server base url (if empty, auto-discover)")
	discoverEnabled := fs.Bool("discover", true, "auto-discover server when --server is not provided")
	discoverTimeout := fs.Duration("discover-timeout", 2*time.Second, "mDNS auto-discovery timeout")
	discoverService := fs.String("discover-service", "_spadeloader._tcp", "mDNS service name used for discovery")
	discoverDomain := fs.String("discover-domain", discovery.DefaultDomain, "mDNS discovery domain")
	token := fs.String("token", strings.TrimSpace(os.Getenv("SPADELOADER_TOKEN")), "auth token")
	authHeader := fs.String("auth-header", envWithFallback("SPADELOADER_AUTH_HEADER", "X-Build-Token"), "auth header")
	limit := fs.Int("limit", 100, "max number of jobs to show")
	refresh := fs.Duration("refresh", 1500*time.Millisecond, "job list refresh interval")
	reflashTimeout := fs.Duration("reflash-timeout", 30*time.Second, "timeout for creating a reflash job")

	if err := fs.Parse(args); err != nil {
		return err
	}

	resolvedServerURL, err := resolveTUIServerURL(*serverURL, *discoverEnabled, *discoverTimeout, *discoverService, *discoverDomain)
	if err != nil {
		return err
	}

	c := &client.HTTPClient{
		BaseURL:    resolvedServerURL,
		Token:      strings.TrimSpace(*token),
		AuthHeader: strings.TrimSpace(*authHeader),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return loaderui.Run(ctx, loaderui.Options{
		Client:          c,
		Limit:           *limit,
		RefreshInterval: *refresh,
		ReflashTimeout:  *reflashTimeout,
	})
}

func resolveTUIServerURL(explicit string, discover bool, timeout time.Duration, service, domain string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return explicit, nil
	}
	if !discover {
		return "", errors.New("server is required when discovery is disabled; pass --server")
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	endpoint, err := discovery.Discover(ctx, service, domain)
	if err != nil {
		return "", fmt.Errorf("discover server via mDNS: %w", err)
	}
	return endpoint.URL, nil
}

func envWithFallback(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
