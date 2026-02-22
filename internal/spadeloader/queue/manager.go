package queue

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/mblsha/spadeforge/internal/spadeloader/config"
	"github.com/mblsha/spadeforge/internal/spadeloader/flasher"
	"github.com/mblsha/spadeforge/internal/spadeloader/history"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
	"github.com/mblsha/spadeforge/internal/spadeloader/store"
)

type SubmitRequest struct {
	Board         string
	DesignName    string
	BitstreamName string
	Bitstream     io.Reader
}

type Manager struct {
	cfg     config.Config
	store   *store.Store
	flasher flasher.Flasher
	history *history.Store

	mu    sync.RWMutex
	jobs  map[string]*job.Record
	queue chan string

	events          map[string][]job.Event
	nextEventSeq    map[string]int64
	subscribers     map[string]map[chan job.Event]struct{}
	maxEventsPerJob int
	subscriberBuf   int

	once sync.Once
}

func New(cfg config.Config, st *store.Store, f flasher.Flasher, h *history.Store) *Manager {
	return &Manager{
		cfg:             cfg,
		store:           st,
		flasher:         f,
		history:         h,
		jobs:            map[string]*job.Record{},
		queue:           make(chan string, 4096),
		events:          map[string][]job.Event{},
		nextEventSeq:    map[string]int64{},
		subscribers:     map[string]map[chan job.Event]struct{}{},
		maxEventsPerJob: 512,
		subscriberBuf:   128,
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

func (m *Manager) Submit(_ context.Context, req SubmitRequest) (*job.Record, error) {
	if req.Bitstream == nil {
		return nil, fmt.Errorf("bitstream reader is required")
	}
	id, err := newJobID()
	if err != nil {
		return nil, fmt.Errorf("generate job id: %w", err)
	}

	if err := m.store.CreateJobLayout(id); err != nil {
		return nil, err
	}
	sha, size, err := m.store.WriteBitstream(id, req.Bitstream)
	if err != nil {
		return nil, err
	}

	rec := job.New(id, job.NewRecordInput{
		Board:              req.Board,
		DesignName:         req.DesignName,
		BitstreamName:      req.BitstreamName,
		BitstreamSHA256:    sha,
		BitstreamSizeBytes: size,
	}, time.Now())
	if err := m.store.Save(rec); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.jobs[id] = rec
	m.emitEventLocked(rec, "queued")
	m.mu.Unlock()

	m.enqueue(id)
	copyRec := *rec
	return &copyRec, nil
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

func (m *Manager) ReadConsoleLog(jobID string) ([]byte, error) {
	path := m.store.ConsoleLogPath(jobID)
	return os.ReadFile(path)
}

func (m *Manager) ListRecentDesigns(limit int) ([]history.Item, error) {
	return m.history.List(limit)
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
	if err := rec.Transition(job.StateRunning, time.Now(), "flash started"); err != nil {
		m.mu.Unlock()
		return
	}
	rec.CurrentStep = "flash"
	board := rec.Board
	designName := rec.DesignName
	_ = m.store.Save(rec)
	m.emitEventLocked(rec, "running")
	m.mu.Unlock()
	log.Printf("[spadeloader job %s] started board=%q design=%q", id, board, designName)

	ctx, cancel := context.WithTimeout(parentCtx, m.cfg.WorkerTimeout)
	result, flashErr := m.flasher.Flash(ctx, flasher.FlashJob{
		ID:            id,
		Board:         board,
		BitstreamPath: m.store.RequestBitstreamPath(id),
		ArtifactsDir:  m.store.ArtifactsJobDir(id),
		Progress:      m.progressUpdater(id),
	})
	cancel()

	m.mu.Lock()
	rec, ok = m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	if flashErr != nil {
		if markErr := rec.MarkFailed(now, result.Message, flashErr, result.ExitCode); markErr != nil {
			rec.State = job.StateFailed
			rec.UpdatedAt = now.UTC()
			rec.Error = flashErr.Error()
			rec.Message = result.Message
			rec.ExitCode = &result.ExitCode
			rec.FinishedAt = &rec.UpdatedAt
		}
		rec.CurrentStep = "failed"
		m.emitEventLocked(rec, "failed")
		log.Printf("[spadeloader job %s] failed message=%q error=%v", id, result.Message, flashErr)
	} else {
		if markErr := rec.MarkSucceeded(now, result.Message, result.ExitCode); markErr != nil {
			rec.State = job.StateSucceeded
			rec.UpdatedAt = now.UTC()
			rec.Message = result.Message
			rec.Error = ""
			rec.ExitCode = &result.ExitCode
			rec.FinishedAt = &rec.UpdatedAt
		}
		rec.CurrentStep = "done"
		m.emitEventLocked(rec, "succeeded")
		log.Printf("[spadeloader job %s] succeeded message=%q", id, result.Message)
	}
	_ = m.store.Save(rec)

	historyItem := history.Item{
		JobID:              rec.ID,
		DesignName:         rec.DesignName,
		Board:              rec.Board,
		BitstreamSHA256:    rec.BitstreamSHA256,
		BitstreamSizeBytes: rec.BitstreamSizeBytes,
		SubmittedAt:        rec.CreatedAt,
		State:              rec.State,
	}
	if rec.FinishedAt != nil {
		historyItem.FinishedAt = rec.FinishedAt.UTC()
	}
	jobID := rec.ID
	preserveWorkDir := m.cfg.PreserveWorkDir
	m.mu.Unlock()

	if err := m.history.Append(historyItem); err != nil {
		log.Printf("[spadeloader job %s] failed to append history: %v", jobID, err)
	}

	if !preserveWorkDir {
		_ = m.store.RemoveWorkDir(jobID)
	}
}

func (m *Manager) progressUpdater(jobID string) flasher.ProgressFunc {
	return func(update flasher.ProgressUpdate) {
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

	buf := m.subscriberBuf
	if buf <= 0 {
		buf = 1
	}
	ch := make(chan job.Event, buf)
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
		Seq:         seq,
		JobID:       rec.ID,
		Type:        eventType,
		State:       rec.State,
		Step:        rec.CurrentStep,
		Message:     rec.Message,
		Error:       rec.Error,
		HeartbeatAt: heartbeat,
		ExitCode:    exitCode,
		At:          now,
	}
	list := append(m.events[rec.ID], ev)
	if len(list) > m.maxEventsPerJob {
		list = list[len(list)-m.maxEventsPerJob:]
	}
	m.events[rec.ID] = list

	for ch := range m.subscribers[rec.ID] {
		publishEvent(ch, ev)
	}
}

func publishEvent(ch chan job.Event, ev job.Event) {
	select {
	case ch <- ev:
		return
	default:
	}

	// For non-terminal updates, dropping events is acceptable when a subscriber is slow.
	if !ev.Terminal() {
		return
	}

	// Guarantee terminal delivery: evict one queued event and retry.
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- ev:
	default:
	}
}
