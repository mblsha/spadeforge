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

	events          map[string][]job.Event
	nextEventSeq    map[string]int64
	subscribers     map[string]map[chan job.Event]struct{}
	maxEventsPerJob int

	once sync.Once
}

func New(cfg config.Config, st *store.Store, b builder.Builder) *Manager {
	return &Manager{
		cfg:             cfg,
		store:           st,
		builder:         b,
		jobs:            map[string]*job.Record{},
		queue:           make(chan string, 4096),
		events:          map[string][]job.Event{},
		nextEventSeq:    map[string]int64{},
		subscribers:     map[string]map[chan job.Event]struct{}{},
		maxEventsPerJob: 512,
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
	m.emitEventLocked(rec, "queued")
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
			rec.FailureKind = ""
			rec.FailureSummary = ""
			rec.CurrentStep = ""
			rec.StartedAt = nil
			rec.FinishedAt = nil
			rec.HeartbeatAt = nil
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
	rec.CurrentStep = "launch"
	_ = m.store.Save(rec)
	m.emitEventLocked(rec, "running")
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, m.cfg.WorkerTimeout)
	defer cancel()

	result, buildErr := m.builder.Build(ctx, builder.BuildJob{
		ID:           rec.ID,
		WorkDir:      m.store.WorkJobDir(rec.ID),
		SourceDir:    m.store.SourceDir(rec.ID),
		ArtifactsDir: m.store.ArtifactsJobDir(rec.ID),
		Manifest:     rec.Manifest,
		Progress:     m.progressUpdater(rec.ID),
	})

	finalState := job.StateSucceeded
	if buildErr != nil {
		finalState = job.StateFailed
	}

	diagReport := m.writeDiagnosticsReport(rec.ID)
	failureKind := ""
	failureSummary := ""
	if finalState == job.StateFailed {
		failureKind, failureSummary = inferFailure(diagReport, result.Message, buildErr)
	}
	_ = m.writeArtifactManifest(rec.ID, finalState, result, diagReport, failureKind, failureSummary)

	m.mu.Lock()
	rec, ok = m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	if buildErr != nil {
		if markErr := rec.MarkFailed(now, result.Message, buildErr, result.ExitCode); markErr != nil {
			rec.State = job.StateFailed
			rec.UpdatedAt = now.UTC()
			rec.Error = buildErr.Error()
			rec.Message = result.Message
			rec.ExitCode = &result.ExitCode
			rec.FinishedAt = &rec.UpdatedAt
		}
		rec.FailureKind = failureKind
		rec.FailureSummary = failureSummary
		rec.CurrentStep = "failed"
		m.emitEventLocked(rec, "failed")
	} else {
		if markErr := rec.MarkSucceeded(now, result.Message, result.ExitCode); markErr != nil {
			rec.State = job.StateSucceeded
			rec.UpdatedAt = now.UTC()
			rec.Message = result.Message
			rec.Error = ""
			rec.ExitCode = &result.ExitCode
			rec.FinishedAt = &rec.UpdatedAt
		}
		rec.FailureKind = ""
		rec.FailureSummary = ""
		rec.CurrentStep = "done"
		m.emitEventLocked(rec, "succeeded")
	}
	_ = m.store.Save(rec)
	jobID := rec.ID
	preserveWorkDir := m.cfg.PreserveWorkDir
	m.mu.Unlock()

	if !preserveWorkDir {
		_ = m.store.RemoveWorkDir(jobID)
	}
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

func (m *Manager) progressUpdater(jobID string) builder.ProgressFunc {
	return func(update builder.ProgressUpdate) {
		m.mu.Lock()
		defer m.mu.Unlock()
		rec, ok := m.jobs[jobID]
		if !ok || rec.State != job.StateRunning {
			return
		}

		now := update.HeartbeatAt.UTC()
		if now.IsZero() {
			now = time.Now().UTC()
		}
		rec.UpdatedAt = now
		rec.HeartbeatAt = &now
		if update.Step != "" {
			rec.CurrentStep = update.Step
		}
		if update.Message != "" {
			rec.Message = update.Message
		}
		_ = m.store.Save(rec)
		m.emitEventLocked(rec, "progress")
	}
}

func (m *Manager) SubscribeEvents(jobID string, since int64) ([]job.Event, <-chan job.Event, func(), bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.jobs[jobID]
	if !ok {
		return nil, nil, nil, false
	}

	backlog := m.eventsSinceLocked(jobID, since)
	if rec.Terminal() {
		return backlog, nil, func() {}, true
	}

	ch := make(chan job.Event, 128)
	if m.subscribers[jobID] == nil {
		m.subscribers[jobID] = map[chan job.Event]struct{}{}
	}
	m.subscribers[jobID][ch] = struct{}{}
	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		subs := m.subscribers[jobID]
		if subs == nil {
			return
		}
		if _, ok := subs[ch]; ok {
			delete(subs, ch)
			close(ch)
		}
		if len(subs) == 0 {
			delete(m.subscribers, jobID)
		}
	}
	return backlog, ch, cancel, true
}

func (m *Manager) eventsSinceLocked(jobID string, since int64) []job.Event {
	src := m.events[jobID]
	if len(src) == 0 {
		return nil
	}
	out := make([]job.Event, 0, len(src))
	for _, ev := range src {
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out
}

func (m *Manager) emitEventLocked(rec *job.Record, eventType string) {
	now := time.Now().UTC()
	seq := m.nextEventSeq[rec.ID] + 1
	m.nextEventSeq[rec.ID] = seq

	var heartbeat *time.Time
	if rec.HeartbeatAt != nil {
		hb := rec.HeartbeatAt.UTC()
		heartbeat = &hb
	}
	var exitCode *int
	if rec.ExitCode != nil {
		ec := *rec.ExitCode
		exitCode = &ec
	}

	ev := job.Event{
		Seq:            seq,
		JobID:          rec.ID,
		Type:           eventType,
		State:          rec.State,
		Step:           rec.CurrentStep,
		Message:        rec.Message,
		Error:          rec.Error,
		FailureKind:    rec.FailureKind,
		FailureSummary: rec.FailureSummary,
		HeartbeatAt:    heartbeat,
		ExitCode:       exitCode,
		At:             now,
	}
	list := append(m.events[rec.ID], ev)
	if len(list) > m.maxEventsPerJob {
		list = list[len(list)-m.maxEventsPerJob:]
	}
	m.events[rec.ID] = list

	for ch := range m.subscribers[rec.ID] {
		select {
		case ch <- ev:
		default:
		}
	}
}
