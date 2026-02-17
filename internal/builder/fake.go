package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
			interval = 2 * time.Second
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

	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "console.log"), []byte("fake build\n"), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "vivado.log"), []byte("vivado fake\n"), 0o644); err != nil {
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

	if b.FailProjects != nil {
		if err, ok := b.FailProjects[job.Manifest.Project]; ok {
			report("failed", "fake build failed")
			return BuildResult{ExitCode: 2, Message: "fake build failed"}, err
		}
	}

	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "design.bit"), []byte("fake-bitstream"), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	report("bitstream", "fake bitstream written")
	return BuildResult{ExitCode: 0, Message: fmt.Sprintf("fake build succeeded for %s", job.ID)}, nil
}
