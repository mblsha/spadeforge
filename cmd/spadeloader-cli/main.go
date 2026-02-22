package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mblsha/spadeforge/internal/discovery"
	"github.com/mblsha/spadeforge/internal/spadeloader/client"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
)

var discoverFn = discovery.Discover

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "flash" {
		args = args[1:]
	}
	if err := runFlash(args); err != nil {
		log.Fatalf("flash failed: %v", err)
	}
}

func runFlash(args []string) error {
	fs := flag.NewFlagSet("spadeloader-cli", flag.ContinueOnError)
	fs.Usage = usage

	serverURL := fs.String("server", defaultString(os.Getenv("SPADELOADER_SERVER"), ""), "spadeloader server base url (if empty, auto-discover)")
	discoverEnabled := fs.Bool("discover", true, "auto-discover server when --server is not provided")
	discoverTimeout := fs.Duration("discover-timeout", 2*time.Second, "mDNS auto-discovery timeout")
	discoverService := fs.String("discover-service", "_spadeloader._tcp", "mDNS service name used for discovery")
	discoverDomain := fs.String("discover-domain", discovery.DefaultDomain, "mDNS discovery domain")

	token := fs.String("token", strings.TrimSpace(os.Getenv("SPADELOADER_TOKEN")), "auth token")
	authHeader := fs.String("auth-header", defaultString(os.Getenv("SPADELOADER_AUTH_HEADER"), "X-Build-Token"), "auth header")

	board := fs.String("board", "", "fpga board name (example: alchitry_au)")
	designName := fs.String("name", "", "human-readable design name")
	bitstream := fs.String("bitstream", "", "bitstream file path (.bit)")

	wait := fs.Bool("wait", true, "poll until flash reaches terminal state")
	poll := fs.Duration("poll", 2*time.Second, "status polling interval")
	streamEvents := fs.Bool("stream-events", false, "stream server events (SSE) instead of polling")
	showLogOnFail := fs.Bool("show-log-on-fail", true, "print full remote console log on failure")
	tailLines := fs.Int("tail-lines", 60, "print this many console tail lines on failure")

	if err := fs.Parse(args); err != nil {
		return err
	}

	interactive := isInteractiveStdin()
	if err := promptForMissing(interactive, board, designName, bitstream); err != nil {
		return err
	}

	if strings.TrimSpace(*board) == "" || strings.TrimSpace(*designName) == "" || strings.TrimSpace(*bitstream) == "" {
		return fmt.Errorf("--board, --name, and --bitstream are required")
	}
	if strings.ToLower(filepath.Ext(strings.TrimSpace(*bitstream))) != ".bit" {
		return fmt.Errorf("--bitstream must point to a .bit file")
	}

	resolvedServerURL, err := resolveServerURL(*serverURL, *discoverEnabled, *discoverTimeout, *discoverService, *discoverDomain)
	if err != nil {
		return err
	}

	c := &client.HTTPClient{BaseURL: resolvedServerURL, Token: *token, AuthHeader: *authHeader}
	ctx := context.Background()
	jobID, err := c.SubmitFlash(ctx, client.SubmitRequest{
		Board:         strings.TrimSpace(*board),
		DesignName:    strings.TrimSpace(*designName),
		BitstreamPath: strings.TrimSpace(*bitstream),
	})
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
		if *tailLines > 0 {
			if tailText, err := c.GetLogTail(ctx, jobID, *tailLines); err == nil {
				trimmed := strings.TrimSpace(tailText)
				if trimmed != "" {
					fmt.Printf("server tail (%d lines):\n%s\n", *tailLines, trimmed)
				}
			}
		}
		if *showLogOnFail {
			logText, err := c.GetLog(ctx, jobID)
			if err == nil {
				trimmed := strings.TrimSpace(logText)
				if trimmed != "" {
					fmt.Printf("server log:\n%s\n", trimmed)
				}
			}
		}
	}
	if record.State != job.StateSucceeded {
		return fmt.Errorf("flash failed: %s", defaultString(record.Error, record.Message))
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

func usage() {
	_, _ = os.Stderr.WriteString("spadeloader-cli usage:\n")
	_, _ = os.Stderr.WriteString("  spadeloader-cli --board <board> --name <design-name> --bitstream design.bit [--server http://host:8080]\n")
	_, _ = os.Stderr.WriteString("  spadeloader-cli flash --board <board> --name <design-name> --bitstream design.bit [--server http://host:8080]\n")
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

func isInteractiveStdin() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func promptForMissing(interactive bool, board *string, designName *string, bitstream *string) error {
	if !interactive {
		return nil
	}
	reader := bufio.NewReader(os.Stdin)

	if strings.TrimSpace(*board) == "" {
		v, err := promptLine(reader, "FPGA board name")
		if err != nil {
			return err
		}
		*board = v
	}
	if strings.TrimSpace(*designName) == "" {
		v, err := promptLine(reader, "Design name")
		if err != nil {
			return err
		}
		*designName = v
	}
	if strings.TrimSpace(*bitstream) == "" {
		v, err := promptLine(reader, "Bitstream path")
		if err != nil {
			return err
		}
		*bitstream = v
	}
	return nil
}

func promptLine(reader *bufio.Reader, label string) (string, error) {
	fmt.Printf("%s: ", label)
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}
