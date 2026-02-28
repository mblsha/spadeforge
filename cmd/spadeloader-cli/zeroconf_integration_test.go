package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/discovery"
)

func TestRunFlash_DiscoversServerViaZeroconf(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("dns-sd"); err != nil {
			t.Skipf("dns-sd is required on darwin for discovery integration: %v", err)
		}
	}

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen on ephemeral port: %v", err)
	}

	var submitCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		board := strings.TrimSpace(r.FormValue("board"))
		name := strings.TrimSpace(r.FormValue("design_name"))
		if board == "" || name == "" {
			http.Error(w, "missing board or design_name", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("bitstream")
		if err != nil {
			http.Error(w, "missing bitstream", http.StatusBadRequest)
			return
		}
		_ = file.Close()

		submitCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"job_id": "job-discovery-it",
			"state":  "QUEUED",
		})
	})

	httpServer := &http.Server{Handler: mux}
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- httpServer.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		select {
		case err := <-serverErrCh:
			if err != nil && err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "use of closed network connection") {
				t.Fatalf("http server error: %v", err)
			}
		default:
		}
	})

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address type: %T", listener.Addr())
	}
	port := tcpAddr.Port

	service := fmt.Sprintf("_sdlit%d._tcp", time.Now().UnixNano()%100_000)
	instance := fmt.Sprintf("spadeloader-it-%d", time.Now().UnixNano()%1_000_000_000)
	advertiser, err := discovery.StartAdvertiser(instance, service, discovery.DefaultDomain, port, []string{"proto=http", "path=/healthz"})
	if err != nil {
		t.Skipf("mDNS advertiser unavailable in this environment: %v", err)
	}
	t.Cleanup(func() {
		closeWithTimeout(t, 1500*time.Millisecond, advertiser.Close)
	})
	time.Sleep(400 * time.Millisecond)

	bitstreamPath := filepath.Join(t.TempDir(), "design.bit")
	if err := os.WriteFile(bitstreamPath, []byte("fake-bitstream"), 0o600); err != nil {
		t.Fatalf("write bitstream: %v", err)
	}

	err = runFlash([]string{
		"--board", "alchitry_au",
		"--name", "mdns-integration",
		"--bitstream", bitstreamPath,
		"--discover-service", service,
		"--discover-domain", discovery.DefaultDomain,
		"--discover-timeout", "20s",
		"--wait=false",
	})
	if err != nil {
		t.Fatalf("runFlash with discovery failed: %v", err)
	}
	if submitCount.Load() == 0 {
		t.Fatalf("expected at least one submit request")
	}
}

func closeWithTimeout(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()

	done := make(chan error, 1)
	go func() {
		done <- fn()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("close returned error: %v", err)
		}
	case <-time.After(timeout):
		// zeroconf shutdown can block on some interface topologies; don't fail tests on cleanup.
	}
}
