package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mblsha/spadeforge/internal/discovery"
	"github.com/mblsha/spadeforge/internal/spadeloader/client"
	loaderconfig "github.com/mblsha/spadeforge/internal/spadeloader/config"
	"github.com/mblsha/spadeforge/internal/spadeloader/flasher"
	"github.com/mblsha/spadeforge/internal/spadeloader/history"
	"github.com/mblsha/spadeforge/internal/spadeloader/queue"
	"github.com/mblsha/spadeforge/internal/spadeloader/server"
	"github.com/mblsha/spadeforge/internal/spadeloader/store"
	loaderui "github.com/mblsha/spadeforge/internal/spadeloader/tui"
)

func main() {
	mode, args, err := parseMode(os.Args[1:])
	if err != nil {
		usage()
		os.Exit(2)
	}
	switch mode {
	case "server":
		if err := runServerTUI(args); err != nil {
			log.Fatalf("server+tui failed: %v", err)
		}
	case "tui":
		if err := runTUI(args); err != nil {
			log.Fatalf("tui failed: %v", err)
		}
	}
}

func parseMode(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "server", nil, nil
	}
	switch strings.TrimSpace(args[0]) {
	case "server":
		return "server", args[1:], nil
	case "tui":
		return "tui", args[1:], nil
	default:
		return "", nil, fmt.Errorf("unknown mode %q", args[0])
	}
}

func runServerTUI(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("server mode does not accept flags; use environment variables for server config")
	}

	cfg, err := loaderconfig.FromEnv()
	if err != nil {
		return err
	}

	var f flasher.Flasher
	if cfg.UseFakeFlasher {
		f = &flasher.FakeFlasher{}
		log.Printf("using fake flasher")
	} else {
		resolvedBin, err := resolveOpenFPGALoaderBin(cfg.OpenFPGALoaderBin)
		if err != nil {
			return err
		}
		log.Printf("using openFPGALoader binary: %s", resolvedBin)
		f = flasher.NewOpenFPGALoaderFlasher(resolvedBin)
	}

	st := store.New(cfg)
	historyStore := history.New(cfg.HistoryPath(), cfg.HistoryLimit)
	mgr := queue.New(cfg, st, f, historyStore)

	signalCtx, stopSignalNotify := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignalNotify()
	rootCtx, cancelRoot := context.WithCancel(signalCtx)
	defer cancelRoot()

	if err := mgr.Start(rootCtx); err != nil {
		return err
	}

	api := server.New(cfg, mgr)
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: api.Handler()}

	var advertiser *discovery.Advertiser
	advertisePrimaryAddr := ""
	if cfg.DiscoveryEnabled {
		host, port, err := parseListenHostPort(cfg.ListenAddr)
		if err != nil {
			log.Printf("discovery advertisement disabled: %v", err)
		} else if isLoopbackListenHost(host) {
			log.Printf("discovery advertisement disabled: listen address %q is loopback-only", cfg.ListenAddr)
		} else {
			primaryAddr, primaryErr := discovery.PrimaryAdvertiseAddrForListenHost(host, port)
			if primaryErr != nil {
				log.Printf("discovery advertisement primary address unavailable: %v", primaryErr)
			}

			instance := cfg.DiscoveryInstance
			if instance == "" {
				instance = hostFallback()
			}
			advertiser, err = discovery.StartAdvertiserForListenHost(
				instance,
				cfg.DiscoveryService,
				cfg.DiscoveryDomain,
				port,
				[]string{"proto=http", "path=/healthz"},
				host,
			)
			if err != nil {
				log.Printf("failed to start discovery advertisement: %v", err)
			} else {
				advertisePrimaryAddr = primaryAddr
				if strings.TrimSpace(advertisePrimaryAddr) == "" {
					log.Printf("discovery advertisement enabled service=%s domain=%s instance=%s port=%d", cfg.DiscoveryService, cfg.DiscoveryDomain, instance, port)
				} else {
					log.Printf("discovery advertisement enabled service=%s domain=%s instance=%s primary=%s", cfg.DiscoveryService, cfg.DiscoveryDomain, instance, advertisePrimaryAddr)
				}
			}
		}
	}
	if advertiser != nil {
		defer closeAdvertiserWithTimeout(advertiser, 1500*time.Millisecond)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("spadeloader server listening on %s", cfg.ListenAddr)
		errCh <- httpServer.ListenAndServe()
	}()

	localServerURL, err := localServerURLForClient(cfg.ListenAddr)
	if err != nil {
		_ = httpServer.Close()
		return err
	}

	uiCtx, cancelUI := context.WithCancel(rootCtx)
	defer cancelUI()
	serverErrCh := make(chan error, 1)
	go func() {
		err := <-errCh
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		select {
		case serverErrCh <- err:
		default:
		}
		cancelUI()
	}()

	c := &client.HTTPClient{
		BaseURL:    localServerURL,
		Token:      cfg.Token,
		AuthHeader: cfg.AuthHeader,
	}
	uiErr := loaderui.Run(uiCtx, loaderui.Options{
		Client:               c,
		Limit:                cfg.HistoryLimit,
		AdvertisePrimaryAddr: advertisePrimaryAddr,
	})

	// Ensure worker contexts and in-flight operations are canceled when the UI exits.
	cancelRoot()
	_ = httpServer.Close()

	select {
	case err := <-serverErrCh:
		return err
	default:
	}

	if uiErr != nil && !errors.Is(uiErr, context.Canceled) {
		return uiErr
	}
	return nil
}

func usage() {
	_, _ = os.Stderr.WriteString("spadeloader usage:\n")
	_, _ = os.Stderr.WriteString("  spadeloader\n")
	_, _ = os.Stderr.WriteString("  spadeloader server\n")
	_, _ = os.Stderr.WriteString("  spadeloader tui [--server <url>]\n")
}

func hostFallback() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "spadeloader"
	}
	return strings.TrimSpace(hostname)
}

func parseListenHostPort(listenAddr string) (string, int, error) {
	port, err := discovery.ParseListenPort(listenAddr)
	if err != nil {
		return "", 0, err
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(host), port, nil
}

func localServerURLForClient(listenAddr string) (string, error) {
	host, port, err := parseListenHostPort(listenAddr)
	if err != nil {
		return "", err
	}

	trimmedHost := strings.TrimSpace(host)
	if trimmedHost == "" {
		trimmedHost = "127.0.0.1"
	}
	if idx := strings.IndexByte(trimmedHost, '%'); idx >= 0 {
		trimmedHost = trimmedHost[:idx]
	}
	if ip := net.ParseIP(trimmedHost); ip != nil && ip.IsUnspecified() {
		trimmedHost = "127.0.0.1"
	}
	if strings.EqualFold(trimmedHost, "localhost") {
		trimmedHost = "127.0.0.1"
	}

	return "http://" + net.JoinHostPort(trimmedHost, fmt.Sprintf("%d", port)), nil
}

func isLoopbackListenHost(host string) bool {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return false
	}
	lowered := strings.ToLower(trimmed)
	lowered = strings.TrimSuffix(lowered, ".")
	if lowered == "localhost" {
		return true
	}
	if idx := strings.IndexByte(lowered, '%'); idx >= 0 {
		lowered = lowered[:idx]
	}
	ip := net.ParseIP(lowered)
	return ip != nil && ip.IsLoopback()
}

func resolveOpenFPGALoaderBin(bin string) (string, error) {
	trimmed := strings.TrimSpace(bin)
	if trimmed == "" {
		return "", fmt.Errorf("openFPGALoader bin is required")
	}
	path, err := exec.LookPath(trimmed)
	if err != nil {
		return "", fmt.Errorf("find openFPGALoader binary %q: %w", trimmed, err)
	}
	return path, nil
}

func closeAdvertiserWithTimeout(advertiser *discovery.Advertiser, timeout time.Duration) {
	if advertiser == nil {
		return
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		advertiser.Close()
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("discovery advertiser close timed out after %s", timeout)
	}
}
