package diagnostics

import (
	"errors"
	"strings"
	"testing"

	"github.com/mblsha/spadeforge/internal/job"
)

func TestBuildReport_ParsesAndDeduplicates(t *testing.T) {
	logs := map[string][]byte{
		"console.log": []byte(strings.Join([]string{
			"ERROR: [Synth 8-2716] syntax error near 'assign' [C:/work/spade.sv:99]",
			"ERROR: [Synth 8-2716] syntax error near 'assign' [C:/work/spade.sv:99]",
			"WARNING: [Common 17-55] ignored warning",
		}, "\n")),
		"vivado.log": []byte("ERROR: [Common 17-69] Command failed: Synthesis failed\n"),
	}

	report := BuildReport(logs)
	if report.ErrorCount != 2 {
		t.Fatalf("expected 2 errors, got %d", report.ErrorCount)
	}
	if report.WarningCount != 1 {
		t.Fatalf("expected 1 warning, got %d", report.WarningCount)
	}
	if len(report.Diagnostics) != 3 {
		t.Fatalf("expected 3 diagnostics, got %d", len(report.Diagnostics))
	}

	var found bool
	for _, d := range report.Diagnostics {
		if d.Code == "Synth 8-2716" {
			found = true
			if d.File != "C:/work/spade.sv" || d.Line != 99 {
				t.Fatalf("unexpected location: %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("expected syntax diagnostic in report")
	}
}

func TestInferFailure_PrefersStructuredDiagnostic(t *testing.T) {
	report := job.DiagnosticsReport{
		Diagnostics: []job.Diagnostic{
			{
				Severity: job.SeverityError,
				Code:     "Synth 8-2716",
				Tool:     "Synth",
				Message:  "syntax error near 'assign'",
				File:     "hdl/spade.sv",
				Line:     123,
			},
		},
	}
	kind, summary := InferFailure(report, "vivado invocation failed", errors.New("boom"))
	if kind != "syntax" {
		t.Fatalf("expected syntax kind, got %q", kind)
	}
	if !strings.Contains(summary, "hdl/spade.sv:123") {
		t.Fatalf("expected file:line in summary, got %q", summary)
	}
}

func TestInferFailure_FallbacksWhenNoDiagnostics(t *testing.T) {
	kind, summary := InferFailure(job.DiagnosticsReport{}, "vivado invocation failed", errors.New("boom"))
	if kind != "internal" {
		t.Fatalf("expected internal kind, got %q", kind)
	}
	if summary != "vivado invocation failed" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}
