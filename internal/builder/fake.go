package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FakeBuilder is intended for tests and local dry-runs.
type FakeBuilder struct {
	mu sync.Mutex

	Calls []BuildJob

	FailProjects map[string]error
	BlockCh      <-chan struct{}
}

func (b *FakeBuilder) Build(ctx context.Context, job BuildJob) (BuildResult, error) {
	if err := os.MkdirAll(job.ArtifactsDir, 0o755); err != nil {
		return BuildResult{ExitCode: 1}, err
	}

	b.mu.Lock()
	b.Calls = append(b.Calls, job)
	b.mu.Unlock()

	if b.BlockCh != nil {
		select {
		case <-ctx.Done():
			return BuildResult{ExitCode: -1}, ctx.Err()
		case <-b.BlockCh:
		}
	}

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
			return BuildResult{ExitCode: 2, Message: "fake build failed"}, err
		}
	}

	if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "design.bit"), []byte("fake-bitstream"), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, err
	}
	return BuildResult{ExitCode: 0, Message: fmt.Sprintf("fake build succeeded for %s", job.ID)}, nil
}
