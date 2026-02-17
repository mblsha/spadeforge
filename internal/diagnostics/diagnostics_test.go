package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mblsha/spadeforge/internal/job"
)

func TestBuildReport_ParsesAndDeduplicatesAcrossLogs(t *testing.T) {
	syntax := fixture(t, "live_old_api_syntax.log")
	logs := map[string][]byte{
		"vivado.log":  syntax,
		"console.log": syntax,
	}

	report := BuildReport(logs)
	if report.ErrorCount != 4 {
		t.Fatalf("expected 4 errors after dedupe, got %d", report.ErrorCount)
	}
	if report.WarningCount != 0 {
		t.Fatalf("expected 0 warnings, got %d", report.WarningCount)
	}
	if len(report.Diagnostics) != 4 {
		t.Fatalf("expected 4 diagnostics, got %d", len(report.Diagnostics))
	}

	var found bool
	for _, d := range report.Diagnostics {
		if d.Code == "Synth 8-2716" {
			found = true
			if d.File == "" || d.Line != 649 {
				t.Fatalf("unexpected location: %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("expected syntax diagnostic in report")
	}
}

func TestBuildReport_ParsesCriticalWarningsAndDRC(t *testing.T) {
	drc := fixture(t, "live_old_api_bad_xdc_and_drc.log")
	report := BuildReport(map[string][]byte{
		"vivado.log": drc,
	})
	if report.WarningCount != 2 {
		t.Fatalf("expected 2 warnings, got %d", report.WarningCount)
	}
	if report.ErrorCount != 4 {
		t.Fatalf("expected 4 errors, got %d", report.ErrorCount)
	}

	var sawXDCWarning bool
	var sawDRC bool
	for _, d := range report.Diagnostics {
		if d.Code == "Common 17-163" {
			sawXDCWarning = true
			if d.File == "" || d.Line != 2 {
				t.Fatalf("expected xdc location, got %+v", d)
			}
		}
		if d.Code == "DRC NSTD-1" {
			sawDRC = true
			if d.Tool != "DRC" {
				t.Fatalf("expected DRC tool, got %+v", d)
			}
		}
	}
	if !sawXDCWarning {
		t.Fatalf("expected critical warning from bad xdc")
	}
	if !sawDRC {
		t.Fatalf("expected DRC error")
	}
}

func TestInferFailure_ClassifiesLiveSampleFormats(t *testing.T) {
	syntaxReport := BuildReport(map[string][]byte{
		"vivado.log": fixture(t, "live_old_api_syntax.log"),
	})
	kind, summary := InferFailure(syntaxReport, "vivado invocation failed", errors.New("boom"))
	if kind != "syntax" {
		t.Fatalf("expected syntax kind, got %q", kind)
	}
	if !strings.Contains(summary, "syntax_error.sv:649") {
		t.Fatalf("expected file:line in summary, got %q", summary)
	}

	badTopReport := BuildReport(map[string][]byte{
		"vivado.log": fixture(t, "live_old_api_bad_top.log"),
	})
	kind, summary = InferFailure(badTopReport, "vivado invocation failed", errors.New("boom"))
	if kind != "synthesis" {
		t.Fatalf("expected synthesis kind, got %q (summary=%q)", kind, summary)
	}

	drcReport := BuildReport(map[string][]byte{
		"vivado.log": fixture(t, "live_old_api_bad_xdc_and_drc.log"),
	})
	kind, summary = InferFailure(drcReport, "vivado invocation failed", errors.New("boom"))
	if kind != "constraints" {
		t.Fatalf("expected constraints kind, got %q (summary=%q)", kind, summary)
	}
	if !strings.Contains(summary, "DRC NSTD-1") {
		t.Fatalf("expected DRC summary, got %q", summary)
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

func TestBuildReport_HandlesVeryLongLogLines(t *testing.T) {
	longMsg := strings.Repeat("x", 90*1024)
	line := "ERROR: [Synth 8-2716] " + longMsg + " [C:/work/top.sv:10]\n"
	report := BuildReport(map[string][]byte{
		"vivado.log": []byte(line),
	})
	if report.ErrorCount != 1 {
		t.Fatalf("expected 1 error from long line, got %d", report.ErrorCount)
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic from long line, got %d", len(report.Diagnostics))
	}
	if report.Diagnostics[0].Code != "Synth 8-2716" {
		t.Fatalf("unexpected diagnostic: %+v", report.Diagnostics[0])
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}
