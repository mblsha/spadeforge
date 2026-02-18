package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mblsha/spadeforge/internal/client"
	"github.com/mblsha/spadeforge/internal/discovery"
	"github.com/mblsha/spadeforge/internal/job"
)

var discoverFn = discovery.Discover

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "submit" {
		args = args[1:]
	}
	if err := runSubmit(args); err != nil {
		log.Fatalf("submit failed: %v", err)
	}
}

func runSubmit(args []string) error {
	fs := flag.NewFlagSet("spadeforge-cli", flag.ContinueOnError)
	fs.Usage = usage

	var sources stringListFlag
	var constraints stringListFlag

	serverURL := fs.String("server", defaultString(os.Getenv("SPADEFORGE_SERVER"), ""), "builder server base url (if empty, auto-discover)")
	discoverEnabled := fs.Bool("discover", true, "auto-discover server when --server is not provided")
	discoverTimeout := fs.Duration("discover-timeout", 2*time.Second, "mDNS auto-discovery timeout")
	discoverService := fs.String("discover-service", discovery.DefaultServiceName, "mDNS service name used for discovery")
	discoverDomain := fs.String("discover-domain", discovery.DefaultDomain, "mDNS discovery domain")
	token := fs.String("token", strings.TrimSpace(os.Getenv("SPADEFORGE_TOKEN")), "auth token")
	authHeader := fs.String("auth-header", defaultString(os.Getenv("SPADEFORGE_AUTH_HEADER"), "X-Build-Token"), "auth header")
	project := fs.String("project", "spade", "project name")
	top := fs.String("top", "", "top module name")
	part := fs.String("part", "", "target FPGA part")
	outputDir := fs.String("output-dir", "output", "directory where artifacts are extracted (under <output-dir>/<job_id>/)")
	outZip := fs.String("out-zip", "", "optional path to save raw downloaded artifacts zip")
	wait := fs.Bool("wait", true, "poll until job reaches terminal state")
	poll := fs.Duration("poll", 2*time.Second, "status polling interval")
	streamEvents := fs.Bool("stream-events", false, "stream server events (SSE) instead of polling")
	showDiagnostics := fs.Bool("show-diagnostics", true, "print parsed diagnostics on failures when available")
	diagnosticLimit := fs.Int("diagnostic-limit", 5, "max diagnostics to print on failure")
	tailLines := fs.Int("tail-lines", 60, "print this many console tail lines on failure")
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

	resolvedServerURL, err := resolveServerURL(*serverURL, *discoverEnabled, *discoverTimeout, *discoverService, *discoverDomain)
	if err != nil {
		return err
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

	c := &client.HTTPClient{BaseURL: resolvedServerURL, Token: *token, AuthHeader: *authHeader}
	ctx := context.Background()
	jobID, err := c.SubmitBundle(ctx, bundle)
	if err != nil {
		return err
	}
	fmt.Printf("job submitted: %s\n", jobID)
	if !*wait {
		return nil
	}

	record, err := waitForTerminal(ctx, c, jobID, *poll, *streamEvents)
	if err != nil {
		return err
	}
	fmt.Printf("job finished: %s (%s)\n", record.State, record.Message)
	if record.State == job.StateFailed {
		if record.FailureKind != "" || record.FailureSummary != "" {
			fmt.Printf("failure: kind=%s summary=%s\n", record.FailureKind, record.FailureSummary)
		}
		if *showDiagnostics {
			if report, err := c.GetDiagnostics(ctx, jobID); err == nil {
				printDiagnostics(report, *diagnosticLimit)
			}
		}
		if *tailLines > 0 {
			if tail, err := c.GetLogTail(ctx, jobID, *tailLines); err == nil {
				if strings.TrimSpace(tail) != "" {
					fmt.Printf("console tail (%d lines):\n%s", *tailLines, tail)
				}
			}
		}
	}

	var artifactZip bytes.Buffer
	if err := c.DownloadArtifacts(ctx, jobID, &artifactZip); err != nil {
		return err
	}

	if strings.TrimSpace(*outZip) != "" {
		dir := filepath.Dir(*outZip)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		f, err := os.OpenFile(*outZip, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.Write(artifactZip.Bytes()); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Printf("artifact zip written to %s\n", *outZip)
	}

	finalOutputDir := filepath.Join(*outputDir, jobID)
	if err := client.ExtractArtifactZip(artifactZip.Bytes(), finalOutputDir); err != nil {
		return err
	}
	fmt.Printf("artifacts extracted to %s\n", finalOutputDir)

	if record.State != "SUCCEEDED" {
		return fmt.Errorf("job failed: %s", record.Error)
	}
	return nil
}

func waitForTerminal(ctx context.Context, c *client.HTTPClient, jobID string, poll time.Duration, stream bool) (*job.Record, error) {
	if stream {
		return waitForTerminalViaEvents(ctx, c, jobID, poll)
	}

	var lastState string
	var lastStep string
	var lastHeartbeat string
	return c.WaitForTerminalWithProgress(ctx, jobID, poll, func(rec *job.Record) {
		heartbeat := "-"
		if rec.HeartbeatAt != nil {
			heartbeat = rec.HeartbeatAt.UTC().Format(time.RFC3339)
		}
		step := rec.CurrentStep
		if step == "" {
			step = "-"
		}

		shouldPrint := rec.State != job.StateSucceeded && rec.State != job.StateFailed
		changed := string(rec.State) != lastState || step != lastStep || heartbeat != lastHeartbeat
		if shouldPrint && changed {
			fmt.Printf("state=%s step=%s heartbeat=%s message=%s\n", rec.State, step, heartbeat, rec.Message)
			lastState = string(rec.State)
			lastStep = step
			lastHeartbeat = heartbeat
		}
	})
}

func waitForTerminalViaEvents(ctx context.Context, c *client.HTTPClient, jobID string, poll time.Duration) (*job.Record, error) {
	var lastState string
	var lastStep string
	var lastHeartbeat string

	printProgress := func(state job.State, step string, heartbeatAt *time.Time, message string) {
		heartbeat := "-"
		if heartbeatAt != nil {
			heartbeat = heartbeatAt.UTC().Format(time.RFC3339)
		}
		if step == "" {
			step = "-"
		}
		shouldPrint := state != job.StateSucceeded && state != job.StateFailed
		changed := string(state) != lastState || step != lastStep || heartbeat != lastHeartbeat
		if shouldPrint && changed {
			fmt.Printf("state=%s step=%s heartbeat=%s message=%s\n", state, step, heartbeat, message)
			lastState = string(state)
			lastStep = step
			lastHeartbeat = heartbeat
		}
	}

	if err := c.StreamEvents(ctx, jobID, 0, func(ev *job.Event) {
		printProgress(ev.State, ev.Step, ev.HeartbeatAt, ev.Message)
	}); err != nil {
		return nil, err
	}

	rec, err := c.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if rec.Terminal() {
		return rec, nil
	}

	return c.WaitForTerminalWithProgress(ctx, jobID, poll, func(update *job.Record) {
		printProgress(update.State, update.CurrentStep, update.HeartbeatAt, update.Message)
	})
}

func printDiagnostics(report *job.DiagnosticsReport, limit int) {
	if report == nil {
		return
	}
	if limit <= 0 {
		limit = 5
	}
	if len(report.Diagnostics) == 0 {
		fmt.Println("diagnostics: none")
		return
	}

	printed := 0
	for _, d := range report.Diagnostics {
		if d.Severity != job.SeverityError {
			continue
		}
		fmt.Printf("diagnostic[%d]: %s [%s] %s", printed+1, d.Severity, d.Code, d.Message)
		if d.File != "" && d.Line > 0 {
			fmt.Printf(" (%s:%d)", d.File, d.Line)
		} else if d.File != "" {
			fmt.Printf(" (%s)", d.File)
		}
		fmt.Println()
		printed++
		if printed >= limit {
			break
		}
	}
	if printed == 0 {
		fmt.Printf("diagnostics: %d entries (no errors)\n", len(report.Diagnostics))
	}
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
	_, _ = os.Stderr.WriteString("spadeforge-cli usage:\n")
	_, _ = os.Stderr.WriteString("  spadeforge-cli --top <top> --part <part> --source build/spade.sv [--xdc top.xdc] [--output-dir output] [--server http://host:8080]\n")
	_, _ = os.Stderr.WriteString("  spadeforge-cli submit --top <top> --part <part> --source build/spade.sv [--xdc top.xdc] [--output-dir output] [--server http://host:8080]\n")
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func resolveServerURL(
	explicit string,
	discover bool,
	timeout time.Duration,
	service string,
	domain string,
) (string, error) {
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
	endpoint, err := discoverFn(ctx, service, domain)
	if err != nil {
		return "", fmt.Errorf("discover server via mDNS: %w", err)
	}
	fmt.Printf("discovered server: %s (instance=%s host=%s)\n", endpoint.URL, endpoint.Instance, endpoint.HostName)
	return endpoint.URL, nil
}
