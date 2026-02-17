package builder

import (
	"context"
	"time"

	"github.com/mblsha/spadeforge/internal/manifest"
)

type ProgressUpdate struct {
	Step        string
	Message     string
	HeartbeatAt time.Time
}

type ProgressFunc func(update ProgressUpdate)

type BuildJob struct {
	ID           string
	WorkDir      string
	SourceDir    string
	ArtifactsDir string
	Manifest     manifest.Manifest
	Progress     ProgressFunc
}

type BuildResult struct {
	ExitCode int
	Message  string
}

type Builder interface {
	Build(ctx context.Context, job BuildJob) (BuildResult, error)
}
