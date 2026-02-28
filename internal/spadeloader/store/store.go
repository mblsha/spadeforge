package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/mblsha/spadeforge/internal/spadeloader/config"
	"github.com/mblsha/spadeforge/internal/spadeloader/job"
)

type Store struct {
	cfg config.Config
	mu  sync.Mutex
}

func New(cfg config.Config) *Store {
	return &Store{cfg: cfg}
}

func (s *Store) EnsureDirs() error {
	dirs := []string{s.cfg.BaseDir, s.cfg.JobsDir(), s.cfg.WorkDir(), s.cfg.ArtifactsDir(), s.cfg.HistoryDir()}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("ensure directory %q: %w", dir, err)
		}
	}
	return nil
}

func (s *Store) CreateJobLayout(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths := []string{s.JobDir(jobID), s.WorkJobDir(jobID), s.ArtifactsJobDir(jobID)}
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create path %q: %w", p, err)
		}
	}
	return nil
}

func (s *Store) WriteBitstream(jobID string, r io.Reader) (sha string, size int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.RequestBitstreamPath(jobID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, fmt.Errorf("open bitstream file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		return "", 0, fmt.Errorf("write bitstream file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (s *Store) Save(record *job.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.StatePath(record.ID)
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal job state: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}

func (s *Store) Load(jobID string) (*job.Record, error) {
	raw, err := os.ReadFile(s.StatePath(jobID))
	if err != nil {
		return nil, err
	}
	var rec job.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	return &rec, nil
}

func (s *Store) LoadAll() ([]*job.Record, error) {
	entries, err := os.ReadDir(s.cfg.JobsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	records := make([]*job.Record, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rec, err := s.Load(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("load job %q: %w", entry.Name(), err)
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

func (s *Store) RemoveWorkDir(jobID string) error {
	return os.RemoveAll(s.WorkJobDir(jobID))
}

func (s *Store) RemoveJobData(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths := []string{
		s.JobDir(jobID),
		s.ArtifactsJobDir(jobID),
		s.WorkJobDir(jobID),
	}
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("remove job path %q: %w", p, err)
		}
	}
	return nil
}

func (s *Store) JobDir(jobID string) string {
	return filepath.Join(s.cfg.JobsDir(), jobID)
}

func (s *Store) StatePath(jobID string) string {
	return filepath.Join(s.JobDir(jobID), "state.json")
}

func (s *Store) RequestBitstreamPath(jobID string) string {
	return filepath.Join(s.JobDir(jobID), "request.bit")
}

func (s *Store) WorkJobDir(jobID string) string {
	return filepath.Join(s.cfg.WorkDir(), jobID)
}

func (s *Store) ArtifactsJobDir(jobID string) string {
	return filepath.Join(s.cfg.ArtifactsDir(), jobID)
}

func (s *Store) ConsoleLogPath(jobID string) string {
	return filepath.Join(s.ArtifactsJobDir(jobID), "console.log")
}
