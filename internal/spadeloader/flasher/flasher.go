package flasher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultBin = "openFPGALoader"

type ProgressUpdate struct {
	Step        string
	Message     string
	HeartbeatAt time.Time
}

type ProgressFunc func(ProgressUpdate)

type FlashJob struct {
	ID            string
	Board         string
	BitstreamPath string
	ArtifactsDir  string
	Progress      ProgressFunc
}

type Result struct {
	Message  string
	ExitCode int
}

type Flasher interface {
	Flash(ctx context.Context, job FlashJob) (Result, error)
}

type OpenFPGALoaderFlasher struct {
	Bin string
}

func NewOpenFPGALoaderFlasher(bin string) *OpenFPGALoaderFlasher {
	if strings.TrimSpace(bin) == "" {
		bin = defaultBin
	}
	return &OpenFPGALoaderFlasher{Bin: strings.TrimSpace(bin)}
}

func (f *OpenFPGALoaderFlasher) Flash(ctx context.Context, job FlashJob) (Result, error) {
	if err := os.MkdirAll(job.ArtifactsDir, 0o755); err != nil {
		return Result{Message: "failed to prepare artifacts directory", ExitCode: -1}, err
	}
	consolePath := filepath.Join(job.ArtifactsDir, "console.log")
	logFile, err := os.OpenFile(consolePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Result{Message: "failed to create console log", ExitCode: -1}, err
	}
	defer logFile.Close()

	if job.Progress != nil {
		job.Progress(ProgressUpdate{Step: "flash", Message: "running openFPGALoader", HeartbeatAt: time.Now().UTC()})
	}

	_, _ = fmt.Fprintf(logFile, "running: %s -b %s %s\n", f.Bin, job.Board, job.BitstreamPath)

	cmd := exec.CommandContext(ctx, f.Bin, "-b", job.Board, job.BitstreamPath)
	cmd.Stdout = io.MultiWriter(logFile)
	cmd.Stderr = io.MultiWriter(logFile)

	err = cmd.Run()
	if err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if exitCode == -1 {
				exitCode = 124
			}
			return Result{Message: "flash timed out", ExitCode: exitCode}, ctx.Err()
		}
		return Result{Message: "openFPGALoader failed", ExitCode: exitCode}, err
	}

	return Result{Message: "flash succeeded", ExitCode: 0}, nil
}

type FakeFlasher struct {
	Delay    time.Duration
	Fail     bool
	ExitCode int
	Message  string
}

func (f *FakeFlasher) Flash(ctx context.Context, job FlashJob) (Result, error) {
	if err := os.MkdirAll(job.ArtifactsDir, 0o755); err != nil {
		return Result{Message: "failed to prepare artifacts directory", ExitCode: -1}, err
	}
	consolePath := filepath.Join(job.ArtifactsDir, "console.log")
	logFile, err := os.OpenFile(consolePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Result{Message: "failed to create console log", ExitCode: -1}, err
	}
	defer logFile.Close()

	if job.Progress != nil {
		job.Progress(ProgressUpdate{Step: "flash", Message: "running fake flasher", HeartbeatAt: time.Now().UTC()})
	}

	_, _ = fmt.Fprintf(logFile, "fake flashing board=%s bitstream=%s\n", job.Board, job.BitstreamPath)

	if f.Delay > 0 {
		timer := time.NewTimer(f.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return Result{Message: "flash timed out", ExitCode: 124}, ctx.Err()
		case <-timer.C:
		}
	}

	if f.Fail {
		exitCode := f.ExitCode
		if exitCode == 0 {
			exitCode = 1
		}
		message := f.Message
		if strings.TrimSpace(message) == "" {
			message = "fake flash failed"
		}
		_, _ = fmt.Fprintln(logFile, message)
		return Result{Message: message, ExitCode: exitCode}, errors.New(message)
	}

	message := f.Message
	if strings.TrimSpace(message) == "" {
		message = "flash succeeded"
	}
	_, _ = fmt.Fprintln(logFile, message)
	return Result{Message: message, ExitCode: 0}, nil
}
