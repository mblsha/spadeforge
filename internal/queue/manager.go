package queue

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	spadearchive "github.com/mblsha/spadeforge/internal/archive"
	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/job"
	"github.com/mblsha/spadeforge/internal/manifest"
	"github.com/mblsha/spadeforge/internal/store"
)

type Manager struct {
	cfg     config.Config
	store   *store.Store
	builder builder.Builder

	mu    sync.RWMutex
	jobs  map[string]*job.Record
	queue chan string

	once sync.Once
}

func New(cfg config.Config, st *store.Store, b builder.Builder) *Manager {
	return &Manager{
		cfg:     cfg,
		store:   st,
		builder: b,
		jobs:    map[string]*job.Record{},
		queue:   make(chan string, 4096),
	}
}

func (m *Manager) Start(ctx context.Context) error {
	if err := m.store.EnsureDirs(); err != nil {
		return err
	}
	if err := m.recoverJobs(); err != nil {
		return err
	}

	m.once.Do(func() {
		go m.worker(ctx)
	})
	return nil
}

func (m *Manager) Submit(ctx context.Context, bundle io.Reader) (*job.Record, error) {
	_ = ctx
	id, err := newJobID()
	if err != nil {
		return nil, fmt.Errorf("generate job id: %w", err)
	}
	if err := m.store.CreateJobLayout(id); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, bundle); err != nil {
		return nil, fmt.Errorf("read uploaded bundle: %w", err)
	}

	if err := m.store.WriteRequestZip(id, bytes.NewReader(buf.Bytes())); err != nil {
		return nil, err
	}

	if _, err := spadearchive.ExtractZipSecure(
		m.store.RequestZipPath(id),
		m.store.SourceDir(id),
		spadearchive.Limits{
			MaxFiles:      m.cfg.MaxExtractedFiles,
			MaxTotalBytes: m.cfg.MaxExtractedTotalBytes,
			MaxFileBytes:  m.cfg.MaxExtractedFileBytes,
		},
	); err != nil {
		return nil, fmt.Errorf("extract bundle: %w", err)
	}

	manifestPath := filepath.Join(m.store.SourceDir(id), "manifest.json")
	rawManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest.json: %w", err)
	}
	mf, err := manifest.Parse(rawManifest)
	if err != nil {
		return nil, err
	}
	if err := mf.Validate(m.store.SourceDir(id)); err != nil {
		return nil, fmt.Errorf("validate manifest: %w", err)
	}

	rec := job.New(id, mf, time.Now())
	if err := m.store.Save(rec); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.jobs[id] = rec
	m.mu.Unlock()

	m.enqueue(id)
	return rec, nil
}

func (m *Manager) Get(jobID string) (*job.Record, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.jobs[jobID]
	if !ok {
		return nil, false
	}
	copyRec := *rec
	return &copyRec, true
}

func (m *Manager) DownloadArtifacts(jobID string, w io.Writer) error {
	rec, ok := m.Get(jobID)
	if !ok {
		return os.ErrNotExist
	}
	if !rec.Terminal() {
		return fmt.Errorf("job is not complete")
	}
	artifactsDir := m.store.ArtifactsJobDir(jobID)
	if _, err := os.Stat(artifactsDir); err != nil {
		return err
	}
	return spadearchive.WriteZipFromDir(artifactsDir, w)
}

func (m *Manager) ReadConsoleLog(jobID string) ([]byte, error) {
	path := filepath.Join(m.store.ArtifactsJobDir(jobID), "console.log")
	return os.ReadFile(path)
}

func (m *Manager) recoverJobs() error {
	recs, err := m.store.LoadAll()
	if err != nil {
		return err
	}
	for _, rec := range recs {
		m.jobs[rec.ID] = rec
		switch rec.State {
		case job.StateQueued:
			m.enqueue(rec.ID)
		case job.StateRunning:
			now := time.Now().UTC()
			rec.State = job.StateQueued
			rec.UpdatedAt = now
			rec.Message = "requeued after restart"
			rec.Error = ""
			rec.StartedAt = nil
			rec.FinishedAt = nil
			rec.ExitCode = nil
			if err := m.store.Save(rec); err != nil {
				return err
			}
			m.enqueue(rec.ID)
		}
	}
	return nil
}

func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
			m.process(ctx, id)
		}
	}
}

func (m *Manager) process(parentCtx context.Context, id string) {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	if rec.State != job.StateQueued {
		m.mu.Unlock()
		return
	}
	if err := rec.Transition(job.StateRunning, time.Now(), "build started"); err != nil {
		m.mu.Unlock()
		return
	}
	_ = m.store.Save(rec)
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, m.cfg.WorkerTimeout)
	defer cancel()

	result, err := m.builder.Build(ctx, builder.BuildJob{
		ID:           rec.ID,
		WorkDir:      m.store.WorkJobDir(rec.ID),
		SourceDir:    m.store.SourceDir(rec.ID),
		ArtifactsDir: m.store.ArtifactsJobDir(rec.ID),
		Manifest:     rec.Manifest,
	})

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if err != nil {
		if markErr := rec.MarkFailed(now, result.Message, err, result.ExitCode); markErr != nil {
			rec.State = job.StateFailed
			rec.UpdatedAt = now.UTC()
			rec.Error = err.Error()
			rec.Message = result.Message
			rec.ExitCode = &result.ExitCode
			rec.FinishedAt = &rec.UpdatedAt
		}
	} else {
		if markErr := rec.MarkSucceeded(now, result.Message, result.ExitCode); markErr != nil {
			rec.State = job.StateSucceeded
			rec.UpdatedAt = now.UTC()
			rec.Message = result.Message
			rec.Error = ""
			rec.ExitCode = &result.ExitCode
			rec.FinishedAt = &rec.UpdatedAt
		}
	}
	_ = m.store.Save(rec)
	_ = m.store.RemoveWorkDir(rec.ID)
}

func (m *Manager) enqueue(jobID string) {
	m.queue <- jobID
}

func newJobID() (string, error) {
	var buf [16]byte
	if _, err := crand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
