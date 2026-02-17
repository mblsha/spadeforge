package diagnostics

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mblsha/spadeforge/internal/job"
)

func BuildReport(logs map[string][]byte) job.DiagnosticsReport {
	report := job.DiagnosticsReport{
		Schema:      1,
		GeneratedAt: time.Now().UTC(),
		Diagnostics: make([]job.Diagnostic, 0),
	}
	seen := map[string]struct{}{}

	for _, source := range []string{"vivado.log", "console.log"} {
		raw, ok := logs[source]
		if !ok {
			continue
		}
		reader := bufio.NewReader(bytes.NewReader(raw))
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				line = strings.TrimRight(line, "\r\n")
			}
			d, ok := parseLine(line, source)
			if !ok {
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
			} else {
				key := diagnosticKey(d)
				if _, dup := seen[key]; !dup {
					seen[key] = struct{}{}
					report.Diagnostics = append(report.Diagnostics, d)
					switch d.Severity {
					case job.SeverityError:
						report.ErrorCount++
					case job.SeverityWarning:
						report.WarningCount++
					default:
						report.InfoCount++
					}
				}
			}
			if err == io.EOF {
				break
			}
		}
	}
	return report
}

func InferFailure(report job.DiagnosticsReport, fallbackMessage string, buildErr error) (string, string) {
	if d, ok := firstError(report); ok {
		kind := classify(d)
		return kind, formatSummary(d)
	}
	msg := strings.TrimSpace(fallbackMessage)
	if msg == "" && buildErr != nil {
		msg = strings.TrimSpace(buildErr.Error())
	}
	if msg == "" {
		msg = "build failed"
	}
	return "internal", msg
}

func firstError(report job.DiagnosticsReport) (job.Diagnostic, bool) {
	for _, d := range report.Diagnostics {
		if d.Severity == job.SeverityError {
			return d, true
		}
	}
	return job.Diagnostic{}, false
}

func classify(d job.Diagnostic) string {
	lower := strings.ToLower(d.Message + " " + d.Code + " " + d.Tool + " " + d.File)
	tool := strings.ToLower(strings.TrimSpace(d.Tool))
	switch {
	case strings.Contains(lower, "syntax"):
		return "syntax"
	case strings.Contains(lower, "constraint") || strings.Contains(lower, ".xdc") || strings.Contains(lower, "nstd") || strings.Contains(lower, "ucio") || strings.HasPrefix(tool, "drc"):
		return "constraints"
	case strings.Contains(lower, "timing"):
		return "timing"
	case strings.HasPrefix(tool, "synth") || strings.Contains(lower, "synthesis failed") || strings.Contains(lower, "module '") && strings.Contains(lower, "not found"):
		return "synthesis"
	case strings.HasPrefix(tool, "place") || strings.HasPrefix(tool, "route") || strings.HasPrefix(tool, "vivado") || strings.Contains(lower, "bitstream"):
		return "implementation"
	default:
		return "internal"
	}
}

func formatSummary(d job.Diagnostic) string {
	code := strings.TrimSpace(d.Code)
	if code == "" {
		code = strings.TrimSpace(d.Tool)
	}
	where := ""
	if d.File != "" && d.Line > 0 {
		where = fmt.Sprintf(" (%s:%d)", d.File, d.Line)
	} else if d.File != "" {
		where = fmt.Sprintf(" (%s)", d.File)
	}
	if code != "" {
		return fmt.Sprintf("[%s] %s%s", code, d.Message, where)
	}
	return d.Message + where
}

func parseLine(rawLine, source string) (job.Diagnostic, bool) {
	line := strings.TrimSpace(rawLine)
	if line == "" {
		return job.Diagnostic{}, false
	}

	var severity job.DiagnosticSeverity
	var rest string
	switch {
	case strings.HasPrefix(line, "ERROR:"):
		severity = job.SeverityError
		rest = strings.TrimSpace(strings.TrimPrefix(line, "ERROR:"))
	case strings.HasPrefix(line, "CRITICAL WARNING:"):
		severity = job.SeverityWarning
		rest = strings.TrimSpace(strings.TrimPrefix(line, "CRITICAL WARNING:"))
	case strings.HasPrefix(line, "WARNING:"):
		severity = job.SeverityWarning
		rest = strings.TrimSpace(strings.TrimPrefix(line, "WARNING:"))
	case strings.HasPrefix(line, "INFO:"):
		severity = job.SeverityInfo
		rest = strings.TrimSpace(strings.TrimPrefix(line, "INFO:"))
	default:
		return job.Diagnostic{}, false
	}

	d := job.Diagnostic{
		Severity: severity,
		Source:   source,
		Raw:      line,
	}

	if strings.HasPrefix(rest, "[") {
		if end := strings.Index(rest, "]"); end > 1 {
			fullCode := strings.TrimSpace(rest[1:end])
			d.Code = fullCode
			if tool := firstToken(fullCode); tool != "" {
				d.Tool = tool
			}
			rest = strings.TrimSpace(rest[end+1:])
		}
	}

	msg, file, lineNo, col := splitTrailingLocation(rest)
	d.Message = msg
	d.File = file
	d.Line = lineNo
	d.Column = col

	if d.Message == "" {
		d.Message = rest
	}
	return d, true
}

func splitTrailingLocation(msg string) (string, string, int, int) {
	msg = strings.TrimSpace(msg)
	if !strings.HasSuffix(msg, "]") {
		return msg, "", 0, 0
	}
	start := strings.LastIndex(msg, " [")
	if start < 0 {
		return msg, "", 0, 0
	}
	location := strings.TrimSpace(msg[start+2 : len(msg)-1])
	if location == "" {
		return msg, "", 0, 0
	}
	path, line, col := parseLocation(location)
	if path == "" {
		return msg, "", 0, 0
	}
	return strings.TrimSpace(msg[:start]), path, line, col
}

func parseLocation(location string) (string, int, int) {
	parts := strings.Split(location, ":")
	if len(parts) < 2 {
		return location, 0, 0
	}

	last := strings.TrimSpace(parts[len(parts)-1])
	if n, ok := parseInt(last); ok {
		if len(parts) >= 3 {
			prev := strings.TrimSpace(parts[len(parts)-2])
			if ln, ok := parseInt(prev); ok {
				path := strings.Join(parts[:len(parts)-2], ":")
				return path, ln, n
			}
		}
		path := strings.Join(parts[:len(parts)-1], ":")
		return path, n, 0
	}
	return location, 0, 0
}

func parseInt(v string) (int, bool) {
	if v == "" {
		return 0, false
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func firstToken(v string) string {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func diagnosticKey(d job.Diagnostic) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d", d.Severity, d.Code, d.Message, d.File, d.Line, d.Column)
}
