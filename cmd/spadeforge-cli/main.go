package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mblsha/spadeforge/internal/client"
	"github.com/mblsha/spadeforge/internal/job"
)

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

	serverURL := fs.String("server", "http://127.0.0.1:8080", "builder server base url")
	token := fs.String("token", strings.TrimSpace(os.Getenv("SPADEFORGE_TOKEN")), "auth token")
	authHeader := fs.String("auth-header", defaultString(os.Getenv("SPADEFORGE_AUTH_HEADER"), "X-Build-Token"), "auth header")
	project := fs.String("project", "spade", "project name")
	top := fs.String("top", "", "top module name")
	part := fs.String("part", "", "target FPGA part")
	outputDir := fs.String("output-dir", "output", "directory where artifacts are extracted (under <output-dir>/<job_id>/)")
	outZip := fs.String("out-zip", "", "optional path to save raw downloaded artifacts zip")
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

	var lastState string
	var lastStep string
	var lastHeartbeat string
	record, err := c.WaitForTerminalWithProgress(ctx, jobID, *poll, func(rec *job.Record) {
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
	if err != nil {
		return err
	}
	fmt.Printf("job finished: %s (%s)\n", record.State, record.Message)

	var artifactZip bytes.Buffer
	if err := c.DownloadArtifacts(ctx, jobID, &artifactZip); err != nil {
		return err
	}

	if strings.TrimSpace(*outZip) != "" {
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
	_, _ = os.Stderr.WriteString("  spadeforge-cli --top <top> --part <part> --source build/spade.sv [--xdc top.xdc] [--output-dir output]\n")
	_, _ = os.Stderr.WriteString("  spadeforge-cli submit --top <top> --part <part> --source build/spade.sv [--xdc top.xdc] [--output-dir output]\n")
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}
