package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mblsha/spadeforge/internal/spadeloader/job"
)

const currentVersion = 1

type Item struct {
	JobID              string    `json:"job_id"`
	DesignName         string    `json:"design_name"`
	Board              string    `json:"board"`
	BitstreamSHA256    string    `json:"bitstream_sha256"`
	BitstreamSizeBytes int64     `json:"bitstream_size_bytes"`
	SubmittedAt        time.Time `json:"submitted_at"`
	FinishedAt         time.Time `json:"finished_at"`
	State              job.State `json:"state"`
}

type filePayload struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

type Store struct {
	path  string
	limit int

	mu     sync.Mutex
	loaded bool
	items  []Item
}

func New(path string, limit int) *Store {
	if limit <= 0 {
		limit = 100
	}
	return &Store{path: path, limit: limit}
}

func (s *Store) Append(item Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.loadLocked(); err != nil {
		return err
	}

	filtered := make([]Item, 0, len(s.items)+1)
	filtered = append(filtered, item)
	for _, existing := range s.items {
		if existing.JobID == item.JobID {
			continue
		}
		filtered = append(filtered, existing)
	}
	if len(filtered) > s.limit {
		filtered = filtered[:s.limit]
	}
	s.items = filtered
	return s.persistLocked()
}

func (s *Store) List(limit int) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > s.limit {
		limit = s.limit
	}
	if limit > len(s.items) {
		limit = len(s.items)
	}
	out := make([]Item, limit)
	copy(out, s.items[:limit])
	return out, nil
}

func (s *Store) loadLocked() error {
	if s.loaded {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.items = nil
			s.loaded = true
			return nil
		}
		return fmt.Errorf("read history file: %w", err)
	}
	var payload filePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("parse history file: %w", err)
	}
	if payload.Version == 0 {
		payload.Version = currentVersion
	}
	s.items = append([]Item(nil), payload.Items...)
	if len(s.items) > s.limit {
		s.items = s.items[:s.limit]
	}
	s.loaded = true
	return nil
}

func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("ensure history dir: %w", err)
	}
	payload := filePayload{Version: currentVersion, Items: s.items}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal history: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write history temp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace history file: %w", err)
	}
	return nil
}
