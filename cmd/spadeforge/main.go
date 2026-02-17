package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/client"
	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/queue"
	"github.com/mblsha/spadeforge/internal/server"
	"github.com/mblsha/spadeforge/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "server":
		if err := runServer(); err != nil {
			log.Fatalf("server failed: %v", err)
		}
	case "submit":
		if err := runSubmit(os.Args[2:]); err != nil {
			log.Fatalf("submit failed: %v", err)
		}
	default:
		usage()
		os.Exit(2)
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

func runSubmit(args []string) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	var sources stringListFlag
	var constraints stringListFlag

	serverURL := fs.String("server", "http://127.0.0.1:8080", "builder server base url")
	token := fs.String("token", strings.TrimSpace(os.Getenv("SPADEFORGE_TOKEN")), "auth token")
	authHeader := fs.String("auth-header", defaultString(os.Getenv("SPADEFORGE_AUTH_HEADER"), "X-Build-Token"), "auth header")
	project := fs.String("project", "spade", "project name")
	top := fs.String("top", "", "top module name")
	part := fs.String("part", "", "target FPGA part")
	outZip := fs.String("out", "artifacts.zip", "where to write downloaded artifacts zip")
	wait := fs.Bool("wait", true, "poll until job reaches terminal state")
	poll := fs.Duration("poll", 2*time.Second, "status polling interval")
	runSwim := fs.Bool("run-swim", false, "run `swim build` before bundling")
	swimBin := fs.String("swim-bin", "swim", "swim executable")

	fs.Var(&sources, "source", "source file (repeatable)")
	fs.Var(&constraints, "xdc", "constraint file (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *runSwim {
		cmd := exec.Command(*swimBin, "build")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("run swim build: %w", err)
		}
		if len(sources) == 0 {
			sources = append(sources, "build/spade.sv")
		}
	}

	if *top == "" || *part == "" {
		return fmt.Errorf("both --top and --part are required")
	}
	if len(sources) == 0 {
		return fmt.Errorf("at least one --source is required")
	}

	bundle, err := client.BuildBundle(client.BundleSpec{
		Project:     *project,
		Top:         *top,
		Part:        *part,
		Sources:     sources,
		Constraints: constraints,
	})
	if err != nil {
		return err
	}

	c := &client.HTTPClient{BaseURL: *serverURL, Token: *token, AuthHeader: *authHeader}
	ctx := context.Background()
	jobID, err := c.SubmitBundle(ctx, bundle)
	if err != nil {
		return err
	}
	fmt.Printf("job submitted: %s\n", jobID)
	if !*wait {
		return nil
	}

	record, err := c.WaitForTerminal(ctx, jobID, *poll)
	if err != nil {
		return err
	}
	fmt.Printf("job finished: %s (%s)\n", record.State, record.Message)

	f, err := os.OpenFile(*outZip, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := c.DownloadArtifacts(ctx, jobID, f); err != nil {
		return err
	}
	fmt.Printf("artifacts written to %s\n", *outZip)

	if record.State != "SUCCEEDED" {
		return fmt.Errorf("job failed: %s", record.Error)
	}
	return nil
}

type stringListFlag []string

func (s *stringListFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringListFlag) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*s = append(*s, v)
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "spadeforge usage:\n")
	fmt.Fprintf(os.Stderr, "  spadeforge server\n")
	fmt.Fprintf(os.Stderr, "  spadeforge submit --top <top> --part <part> --source build/spade.sv [--xdc top.xdc]\n")
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}
