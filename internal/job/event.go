package job

import "time"

type Event struct {
	Seq int64 `json:"seq"`

	JobID string `json:"job_id"`
	Type  string `json:"type"`
	State State  `json:"state"`

	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`

	FailureKind    string `json:"failure_kind,omitempty"`
	FailureSummary string `json:"failure_summary,omitempty"`

	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	At          time.Time  `json:"at"`
}

func (e Event) Terminal() bool {
	return e.State == StateSucceeded || e.State == StateFailed
}
