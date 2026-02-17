package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mblsha/spadeforge/internal/builder"
	"github.com/mblsha/spadeforge/internal/diagnostics"
	"github.com/mblsha/spadeforge/internal/job"
)

const (
	diagnosticsFileName    = "diagnostics.json"
	artifactManifestName   = "artifact_manifest.json"
	defaultConsoleTailLine = 200
	maxConsoleTailLines    = 5000
)

func (m *Manager) ReadDiagnostics(jobID string) ([]byte, error) {
	return os.ReadFile(filepath.Join(m.store.ArtifactsJobDir(jobID), diagnosticsFileName))
}

func (m *Manager) ReadConsoleTail(jobID string, lines int) ([]byte, error) {
	raw, err := m.ReadConsoleLog(jobID)
	if err != nil {
		return nil, err
	}
	if lines <= 0 {
		lines = defaultConsoleTailLine
	}
	if lines > maxConsoleTailLines {
		lines = maxConsoleTailLines
	}
	return tailLastLines(raw, lines), nil
}

func (m *Manager) writeDiagnosticsReport(jobID string) job.DiagnosticsReport {
	artDir := m.store.ArtifactsJobDir(jobID)
	_ = os.MkdirAll(artDir, 0o755)
	logs := map[string][]byte{}
	for _, name := range []string{"vivado.log", "console.log"} {
		raw, err := os.ReadFile(filepath.Join(artDir, name))
		if err == nil {
			logs[name] = raw
		}
	}
	report := diagnostics.BuildReport(logs)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(artDir, diagnosticsFileName), raw, 0o644)
	}
	return report
}

func inferFailure(report job.DiagnosticsReport, fallbackMessage string, buildErr error) (string, string) {
	return diagnostics.InferFailure(report, fallbackMessage, buildErr)
}

type artifactFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type artifactManifest struct {
	Schema int `json:"schema"`

	JobID       string    `json:"job_id"`
	GeneratedAt time.Time `json:"generated_at"`
	State       job.State `json:"state"`

	FailureKind    string `json:"failure_kind,omitempty"`
	FailureSummary string `json:"failure_summary,omitempty"`

	ResultMessage string `json:"result_message,omitempty"`
	ExitCode      int    `json:"exit_code"`

	RequestBundleSHA256 string `json:"request_bundle_sha256,omitempty"`

	Builder struct {
		Name    string `json:"name"`
		Version string `json:"version,omitempty"`
		Binary  string `json:"binary,omitempty"`
	} `json:"builder"`

	Diagnostics struct {
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
		Info     int `json:"info"`
	} `json:"diagnostics"`

	Files []artifactFile `json:"files"`
}

func (m *Manager) writeArtifactManifest(
	jobID string,
	finalState job.State,
	result builder.BuildResult,
	report job.DiagnosticsReport,
	failureKind string,
	failureSummary string,
) error {
	artDir := m.store.ArtifactsJobDir(jobID)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		return err
	}

	files, err := collectArtifactFiles(artDir)
	if err != nil {
		return err
	}
	reqHash, _ := sha256File(m.store.RequestZipPath(jobID))
	builderName, builderVersion, builderBinary := m.builderInfo(filepath.Join(artDir, "vivado.log"))

	meta := artifactManifest{
		Schema:              1,
		JobID:               jobID,
		GeneratedAt:         time.Now().UTC(),
		State:               finalState,
		FailureKind:         failureKind,
		FailureSummary:      failureSummary,
		ResultMessage:       result.Message,
		ExitCode:            result.ExitCode,
		RequestBundleSHA256: reqHash,
		Files:               files,
	}
	meta.Builder.Name = builderName
	meta.Builder.Version = builderVersion
	meta.Builder.Binary = builderBinary
	meta.Diagnostics.Errors = report.ErrorCount
	meta.Diagnostics.Warnings = report.WarningCount
	meta.Diagnostics.Info = report.InfoCount

	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(artDir, artifactManifestName), raw, 0o644)
}

func (m *Manager) builderInfo(vivadoLogPath string) (name string, version string, binary string) {
	switch m.builder.(type) {
	case *builder.FakeBuilder:
		return "fake", "fake", "fake"
	default:
		name = "vivado"
		binary = m.cfg.VivadoBin
		raw, err := os.ReadFile(vivadoLogPath)
		if err == nil {
			version = parseVivadoVersion(raw)
		}
		if version == "" {
			version = "unknown"
		}
		return name, version, binary
	}
}

func collectArtifactFiles(artDir string) ([]artifactFile, error) {
	files := make([]artifactFile, 0)
	err := filepath.WalkDir(artDir, func(pathNow string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(artDir, pathNow)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == artifactManifestName {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		sum, err := sha256File(pathNow)
		if err != nil {
			return err
		}
		files = append(files, artifactFile{
			Path:   rel,
			Size:   fi.Size(),
			SHA256: sum,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func sha256File(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func parseVivadoVersion(raw []byte) string {
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Vivado v") {
			idx := strings.Index(line, "Vivado v")
			if idx >= 0 {
				frag := strings.Fields(line[idx:])
				if len(frag) >= 2 {
					return strings.TrimPrefix(frag[1], "v")
				}
			}
		}
	}
	return ""
}

func tailLastLines(raw []byte, lines int) []byte {
	if lines <= 0 {
		return raw
	}
	s := string(raw)
	parts := strings.Split(s, "\n")
	if len(parts) == 0 {
		return raw
	}
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) <= lines {
		return []byte(strings.Join(parts, "\n") + "\n")
	}
	start := len(parts) - lines
	return []byte(strings.Join(parts[start:], "\n") + "\n")
}
