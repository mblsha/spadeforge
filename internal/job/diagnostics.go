package job

import "time"

type DiagnosticSeverity string

const (
	SeverityError   DiagnosticSeverity = "ERROR"
	SeverityWarning DiagnosticSeverity = "WARNING"
	SeverityInfo    DiagnosticSeverity = "INFO"
)

type Diagnostic struct {
	Severity DiagnosticSeverity `json:"severity"`
	Tool     string             `json:"tool,omitempty"`
	Code     string             `json:"code,omitempty"`
	Message  string             `json:"message"`
	File     string             `json:"file,omitempty"`
	Line     int                `json:"line,omitempty"`
	Column   int                `json:"column,omitempty"`
	Source   string             `json:"source,omitempty"`
	Raw      string             `json:"raw,omitempty"`
}

type DiagnosticsReport struct {
	Schema       int          `json:"schema"`
	GeneratedAt  time.Time    `json:"generated_at"`
	ErrorCount   int          `json:"error_count"`
	WarningCount int          `json:"warning_count"`
	InfoCount    int          `json:"info_count"`
	Diagnostics  []Diagnostic `json:"diagnostics"`
}
