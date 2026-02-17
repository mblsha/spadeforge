package store

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/mblsha/spadeforge/internal/config"
	"github.com/mblsha/spadeforge/internal/job"
)

type Store struct {
	cfg config.Config
	mu  sync.Mutex
}

func New(cfg config.Config) *Store {
	return &Store{cfg: cfg}
}

func (s *Store) EnsureDirs() error {
	dirs := []string{s.cfg.BaseDir, s.cfg.JobsDir(), s.cfg.WorkDir(), s.cfg.ArtifactsDir()}
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

	paths := []string{s.JobDir(jobID), s.WorkJobDir(jobID), s.SourceDir(jobID), s.ArtifactsJobDir(jobID)}
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create path %q: %w", p, err)
		}
	}
	return nil
}

func (s *Store) WriteRequestZip(jobID string, r io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.RequestZipPath(jobID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open request zip: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write request zip: %w", err)
	}
	return nil
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

func (s *Store) JobDir(jobID string) string {
	return filepath.Join(s.cfg.JobsDir(), jobID)
}

func (s *Store) StatePath(jobID string) string {
	return filepath.Join(s.JobDir(jobID), "state.json")
}

func (s *Store) RequestZipPath(jobID string) string {
	return filepath.Join(s.JobDir(jobID), "request.zip")
}

func (s *Store) WorkJobDir(jobID string) string {
	return filepath.Join(s.cfg.WorkDir(), jobID)
}

func (s *Store) SourceDir(jobID string) string {
	return filepath.Join(s.WorkJobDir(jobID), "src")
}

func (s *Store) ArtifactsJobDir(jobID string) string {
	return filepath.Join(s.cfg.ArtifactsDir(), jobID)
}
