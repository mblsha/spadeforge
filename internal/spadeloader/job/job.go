package job

import (
	"errors"
	"fmt"
	"time"
)

type State string

const (
	StateQueued    State = "QUEUED"
	StateRunning   State = "RUNNING"
	StateSucceeded State = "SUCCEEDED"
	StateFailed    State = "FAILED"
)

type Record struct {
	ID string `json:"id"`

	State       State  `json:"state"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
	CurrentStep string `json:"current_step,omitempty"`

	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	ExitCode *int `json:"exit_code,omitempty"`

	Board              string `json:"board"`
	DesignName         string `json:"design_name"`
	BitstreamName      string `json:"bitstream_name"`
	BitstreamSHA256    string `json:"bitstream_sha256"`
	BitstreamSizeBytes int64  `json:"bitstream_size_bytes"`
}

type NewRecordInput struct {
	Board              string
	DesignName         string
	BitstreamName      string
	BitstreamSHA256    string
	BitstreamSizeBytes int64
}

func New(id string, input NewRecordInput, now time.Time) *Record {
	n := now.UTC()
	return &Record{
		ID:                 id,
		State:              StateQueued,
		CreatedAt:          n,
		UpdatedAt:          n,
		Board:              input.Board,
		DesignName:         input.DesignName,
		BitstreamName:      input.BitstreamName,
		BitstreamSHA256:    input.BitstreamSHA256,
		BitstreamSizeBytes: input.BitstreamSizeBytes,
	}
}

func (r *Record) Transition(next State, now time.Time, message string) error {
	if !isValidTransition(r.State, next) {
		return fmt.Errorf("invalid transition %s -> %s", r.State, next)
	}
	n := now.UTC()
	r.State = next
	r.UpdatedAt = n
	r.Message = message
	if next == StateRunning {
		r.StartedAt = &n
		r.FinishedAt = nil
		r.ExitCode = nil
		r.Error = ""
		r.HeartbeatAt = &n
	}
	if next == StateSucceeded || next == StateFailed {
		r.FinishedAt = &n
		r.HeartbeatAt = &n
	}
	return nil
}

func (r *Record) MarkFailed(now time.Time, message string, err error, exitCode int) error {
	if err == nil {
		err = errors.New("flash failed")
	}
	if r.State != StateRunning && r.State != StateQueued {
		return fmt.Errorf("invalid state for failure: %s", r.State)
	}
	if r.State != StateFailed {
		if trErr := r.Transition(StateFailed, now, message); trErr != nil {
			return trErr
		}
	}
	r.Error = err.Error()
	r.ExitCode = &exitCode
	return nil
}

func (r *Record) MarkSucceeded(now time.Time, message string, exitCode int) error {
	if r.State != StateRunning {
		return fmt.Errorf("invalid state for success: %s", r.State)
	}
	if err := r.Transition(StateSucceeded, now, message); err != nil {
		return err
	}
	r.Error = ""
	r.ExitCode = &exitCode
	return nil
}

func (r *Record) Terminal() bool {
	return r.State == StateSucceeded || r.State == StateFailed
}

func isValidTransition(from, to State) bool {
	if from == to {
		return true
	}
	switch from {
	case StateQueued:
		return to == StateRunning || to == StateFailed
	case StateRunning:
		return to == StateSucceeded || to == StateFailed
	case StateSucceeded, StateFailed:
		return false
	default:
		return false
	}
}
