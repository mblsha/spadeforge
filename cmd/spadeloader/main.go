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
	loaderconfig "github.com/mblsha/spadeforge/internal/spadeloader/config"
	"github.com/mblsha/spadeforge/internal/spadeloader/flasher"
	"github.com/mblsha/spadeforge/internal/spadeloader/history"
	"github.com/mblsha/spadeforge/internal/spadeloader/queue"
	"github.com/mblsha/spadeforge/internal/spadeloader/server"
	"github.com/mblsha/spadeforge/internal/spadeloader/store"
)

func main() {
	mode, args, err := parseMode(os.Args[1:])
	if err != nil {
		usage()
		os.Exit(2)
	}
	switch mode {
	case "server":
		if len(args) > 0 {
			usage()
			os.Exit(2)
		}
		if err := runServer(); err != nil {
			log.Fatalf("server failed: %v", err)
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

func runServer() error {
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		return err
	}

	api := server.New(cfg, mgr)
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: api.Handler()}

	var advertiser *discovery.Advertiser
	if cfg.DiscoveryEnabled {
		host, port, err := parseListenHostPort(cfg.ListenAddr)
		if err != nil {
			log.Printf("discovery advertisement disabled: %v", err)
		} else if isLoopbackListenHost(host) {
			log.Printf("discovery advertisement disabled: listen address %q is loopback-only", cfg.ListenAddr)
		} else {
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
				log.Printf("discovery advertisement enabled service=%s domain=%s instance=%s port=%d", cfg.DiscoveryService, cfg.DiscoveryDomain, instance, port)
			}
		}
	}
	if advertiser != nil {
		defer advertiser.Close()
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("spadeloader server listening on %s", cfg.ListenAddr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
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
