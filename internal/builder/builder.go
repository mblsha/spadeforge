package builder

import (
	"context"

	"github.com/mblsha/spadeforge/internal/manifest"
)

type BuildJob struct {
	ID           string
	WorkDir      string
	SourceDir    string
	ArtifactsDir string
	Manifest     manifest.Manifest
}

type BuildResult struct {
	ExitCode int
	Message  string
}

type Builder interface {
	Build(ctx context.Context, job BuildJob) (BuildResult, error)
}
