package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/discovery"
	"github.com/mblsha/spadeforge/internal/queue"
	"github.com/mblsha/spadeforge/internal/server"
	"github.com/mblsha/spadeforge/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] != "server" {
		usage()
		os.Exit(2)
	}
	if err := runServer(); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func runServer() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	var b builder.Builder
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SPADEFORGE_USE_FAKE_BUILDER")), "1") {
		b = &builder.FakeBuilder{}
		log.Printf("using fake builder")
	} else {
		b = builder.NewVivadoBuilder(cfg.VivadoBin, nil)
	}

	st := store.New(cfg)
	mgr := queue.New(cfg, st, b)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		return err
	}

	api := server.New(cfg, mgr)
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: api.Handler()}

	var advertiser *discovery.Advertiser
	if cfg.DiscoveryEnabled {
		port, err := discovery.ParseListenPort(cfg.ListenAddr)
		if err != nil {
			log.Printf("discovery advertisement disabled: %v", err)
		} else {
			instance := cfg.DiscoveryInstance
			if instance == "" {
				instance = hostFallback()
			}
			advertiser, err = discovery.StartAdvertiser(
				instance,
				cfg.DiscoveryService,
				cfg.DiscoveryDomain,
				port,
				[]string{"proto=http", "path=/healthz"},
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
		log.Printf("spadeforge server listening on %s", cfg.ListenAddr)
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
	_, _ = os.Stderr.WriteString("spadeforge usage:\n")
	_, _ = os.Stderr.WriteString("  spadeforge\n")
	_, _ = os.Stderr.WriteString("  spadeforge server\n")
}

func hostFallback() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return "spadeforge"
	}
	return strings.TrimSpace(hostname)
}
