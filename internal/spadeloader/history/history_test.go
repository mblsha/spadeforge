package history

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mblsha/spadeforge/internal/spadeloader/job"
)

func TestAppendTrimsToLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "recent_designs.json")
	s := New(path, 100)
	now := time.Now().UTC()

	for i := 0; i < 101; i++ {
		err := s.Append(Item{
			JobID:              fmt.Sprintf("job-%03d", i),
			DesignName:         fmt.Sprintf("design-%03d", i),
			Board:              "alchitry_au",
			BitstreamSHA256:    fmt.Sprintf("sha-%03d", i),
			BitstreamSizeBytes: int64(i + 1),
			SubmittedAt:        now.Add(time.Duration(i) * time.Second),
			FinishedAt:         now.Add(time.Duration(i+1) * time.Second),
			State:              job.StateSucceeded,
		})
		if err != nil {
			t.Fatalf("Append(%d) error: %v", i, err)
		}
	}

	items, err := s.List(100)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(items) != 100 {
		t.Fatalf("len(items) = %d, want 100", len(items))
	}
	if items[0].JobID != "job-100" {
		t.Fatalf("items[0].JobID = %q, want job-100", items[0].JobID)
	}
	if items[len(items)-1].JobID != "job-001" {
		t.Fatalf("items[last].JobID = %q, want job-001", items[len(items)-1].JobID)
	}

	reloaded := New(path, 100)
	reloadedItems, err := reloaded.List(100)
	if err != nil {
		t.Fatalf("reloaded.List() error: %v", err)
	}
	if len(reloadedItems) != 100 {
		t.Fatalf("len(reloadedItems) = %d, want 100", len(reloadedItems))
	}
	if reloadedItems[0].JobID != "job-100" {
		t.Fatalf("reloaded first JobID = %q, want job-100", reloadedItems[0].JobID)
	}
}
