package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FakeBuilder is intended for tests and local dry-runs.
type FakeBuilder struct {
	mu sync.Mutex

	Calls []BuildJob

	FailProjects      map[string]error
	BlockCh           <-chan struct{}
	HeartbeatInterval time.Duration
	ConsoleLog        string
	VivadoLog         string
}

func (b *FakeBuilder) Build(ctx context.Context, job BuildJob) (BuildResult, error) {
	report := func(step, message string) {
		if job.Progress != nil {
			job.Progress(ProgressUpdate{
				Step:        step,
				Message:     message,
				HeartbeatAt: time.Now().UTC(),
			})
		}
	}

	report("prepare", "fake build preparing workspace")

	if err := os.MkdirAll(job.ArtifactsDir, 0o755); err != nil {
		return BuildResult{ExitCode: 1}, err
	}

	b.mu.Lock()
	b.Calls = append(b.Calls, job)
	b.mu.Unlock()

	if b.BlockCh != nil {
		report("synth", "fake synth step running")
		interval := b.HeartbeatInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return BuildResult{ExitCode: -1}, ctx.Err()
			case <-ticker.C:
				report("synth", "fake heartbeat")
			case <-b.BlockCh:
				goto unblocked
			}
		}
	}
unblocked:
	report("route", "fake route step running")

	failErr, shouldFail := shouldFailBuild(b.FailProjects, job.Manifest.Project)
	consoleLog := b.ConsoleLog
	if consoleLog == "" {
		consoleLog = "fake build\n"
	}
	vivadoLog := b.VivadoLog
	if vivadoLog == "" {
		vivadoLog = "vivado fake\n"
	}
	if shouldFail {
		if !containsVivadoError(consoleLog) {
			consoleLog += "ERROR: [Synth 8-2716] syntax error near 'fake' [hdl/spade.sv:1]\n"
		}
		if !containsVivadoError(vivadoLog) {
			vivadoLog += "ERROR: [Common 17-69] Command failed: Synthesis failed\n"
		}
	}

	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "console.log"), []byte(consoleLog), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "vivado.log"), []byte(vivadoLog), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "vivado.jou"), []byte("journal fake\n"), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "timing.rpt"), []byte("timing fake\n"), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "utilization.rpt"), []byte("util fake\n"), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}

	if shouldFail {
		report("failed", "fake build failed")
		return BuildResult{ExitCode: 2, Message: "fake build failed"}, failErr
	}

	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "design.bit"), []byte("fake-bitstream"), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	report("bitstream", "fake bitstream written")
	return BuildResult{ExitCode: 0, Message: fmt.Sprintf("fake build succeeded for %s", job.ID)}, nil
}

func shouldFailBuild(failProjects map[string]error, project string) (error, bool) {
	if failProjects == nil {
		return nil, false
	}
	err, ok := failProjects[project]
	return err, ok
}

func containsVivadoError(log string) bool {
	return strings.Contains(log, "ERROR:")
}
