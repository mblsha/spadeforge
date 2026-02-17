package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type CommandSpec struct {
	Name string
	Args []string
	Dir  string
}

type Runner interface {
	Run(ctx context.Context, spec CommandSpec, stdout, stderr io.Writer) (int, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, spec CommandSpec, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), err
	}
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	return -1, err
}

type VivadoBuilder struct {
	VivadoBin         string
	Runner            Runner
	OSName            string
	HeartbeatInterval time.Duration
}

func NewVivadoBuilder(vivadoBin string, runner Runner) *VivadoBuilder {
	if runner == nil {
		runner = OSRunner{}
	}
	return &VivadoBuilder{
		VivadoBin:         vivadoBin,
		Runner:            runner,
		OSName:            runtime.GOOS,
		HeartbeatInterval: 5 * time.Second,
	}
}

func (b *VivadoBuilder) Build(ctx context.Context, job BuildJob) (BuildResult, error) {
	report := func(step, message string) {
		if job.Progress != nil {
			job.Progress(ProgressUpdate{
				Step:        step,
				Message:     message,
				HeartbeatAt: time.Now().UTC(),
			})
		}
	}

	if err := os.MkdirAll(job.ArtifactsDir, 0o755); err != nil {
		return BuildResult{ExitCode: 1}, fmt.Errorf("create artifacts directory: %w", err)
	}
	if err := os.MkdirAll(job.WorkDir, 0o755); err != nil {
		return BuildResult{ExitCode: 1}, fmt.Errorf("create work directory: %w", err)
	}

	tclPath := filepath.Join(job.WorkDir, "build.tcl")
	tclContent := GenerateTCL(job)
	if err := os.WriteFile(tclPath, []byte(tclContent), 0o644); err != nil {
		return BuildResult{ExitCode: 1}, fmt.Errorf("write build.tcl: %w", err)
	}

	consolePath := filepath.Join(job.ArtifactsDir, "console.log")
	consoleFile, err := os.OpenFile(consolePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return BuildResult{ExitCode: 1}, fmt.Errorf("create console log: %w", err)
	}
	defer consoleFile.Close()

	report("launch", "starting vivado")
	progressWriter := newStepProgressWriter(consoleFile, report)
	heartbeatDone := make(chan struct{})
	heartbeatInterval := b.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				report("", "")
			}
		}
	}()

	spec := buildVivadoCommand(b.OSName, b.VivadoBin, tclPath, job.WorkDir)
	exitCode, runErr := b.Runner.Run(ctx, spec, progressWriter, progressWriter)
	close(heartbeatDone)
	progressWriter.Flush()

	copyIfExists(filepath.Join(job.WorkDir, "vivado.log"), filepath.Join(job.ArtifactsDir, "vivado.log"))
	copyIfExists(filepath.Join(job.WorkDir, "vivado.jou"), filepath.Join(job.ArtifactsDir, "vivado.jou"))

	if runErr != nil {
		return BuildResult{ExitCode: exitCode, Message: "vivado invocation failed"}, runErr
	}
	if exitCode != 0 {
		return BuildResult{ExitCode: exitCode, Message: "vivado exited non-zero"}, fmt.Errorf("vivado exited %d", exitCode)
	}

	bitPath := filepath.Join(job.ArtifactsDir, "design.bit")
	fi, err := os.Stat(bitPath)
	if err != nil {
		return BuildResult{ExitCode: exitCode, Message: "missing bitstream"}, fmt.Errorf("missing bitstream: %w", err)
	}
	if fi.Size() == 0 {
		return BuildResult{ExitCode: exitCode, Message: "empty bitstream"}, errors.New("bitstream is empty")
	}

	return BuildResult{ExitCode: exitCode, Message: "vivado build succeeded"}, nil
}

func GenerateTCL(job BuildJob) string {
	lines := []string{
		"set_msg_config -id {Common 17-55} -suppress",
		`puts "SPADEFORGE_STEP:read_sources"`,
	}
	includeArg := ""
	if len(job.Manifest.IncludeDirs) > 0 {
		absIncludeDirs := make([]string, 0, len(job.Manifest.IncludeDirs))
		for _, includeDir := range job.Manifest.IncludeDirs {
			absIncludeDirs = append(absIncludeDirs, filepath.ToSlash(filepath.Join(job.SourceDir, filepath.FromSlash(includeDir))))
		}
		includeArg = " -include_dirs " + tclBrace(strings.Join(absIncludeDirs, " "))
	}
	for _, src := range job.Manifest.Sources {
		lines = append(lines, fmt.Sprintf("read_verilog -sv%s %s", includeArg, tclBrace(filepath.ToSlash(filepath.Join(job.SourceDir, filepath.FromSlash(src))))))
	}
	for _, xdc := range job.Manifest.Constraints {
		lines = append(lines, fmt.Sprintf("read_xdc %s", tclBrace(filepath.ToSlash(filepath.Join(job.SourceDir, filepath.FromSlash(xdc))))))
	}
	lines = append(lines,
		`puts "SPADEFORGE_STEP:synth"`,
		fmt.Sprintf("synth_design -top %s -part %s", tclWord(job.Manifest.Top), tclWord(job.Manifest.Part)),
		`puts "SPADEFORGE_STEP:opt"`,
		"opt_design",
		`puts "SPADEFORGE_STEP:place"`,
		"place_design",
		`puts "SPADEFORGE_STEP:route"`,
		"route_design",
		`puts "SPADEFORGE_STEP:reports"`,
		fmt.Sprintf("report_timing_summary -file %s", tclBrace(filepath.ToSlash(filepath.Join(job.ArtifactsDir, "timing.rpt")))),
		fmt.Sprintf("report_utilization -file %s", tclBrace(filepath.ToSlash(filepath.Join(job.ArtifactsDir, "utilization.rpt")))),
		`puts "SPADEFORGE_STEP:bitstream"`,
		fmt.Sprintf("write_bitstream -force %s", tclBrace(filepath.ToSlash(filepath.Join(job.ArtifactsDir, "design.bit")))),
		"exit",
	)
	return strings.Join(lines, "\n") + "\n"
}

func buildVivadoCommand(osName, vivadoBin, tclPath, workDir string) CommandSpec {
	args := []string{"-mode", "batch", "-source", tclPath}
	if strings.EqualFold(osName, "windows") {
		return CommandSpec{
			Name: "cmd.exe",
			Args: append([]string{"/C", vivadoBin}, args...),
			Dir:  workDir,
		}
	}
	return CommandSpec{Name: vivadoBin, Args: args, Dir: workDir}
}

func copyIfExists(src, dst string) {
	rf, err := os.Open(src)
	if err != nil {
		return
	}
	defer rf.Close()

	wf, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	defer wf.Close()
	_, _ = io.Copy(wf, rf)
}

type stepProgressWriter struct {
	mu     sync.Mutex
	out    io.Writer
	buf    []byte
	report func(step, message string)
}

func newStepProgressWriter(out io.Writer, report func(step, message string)) *stepProgressWriter {
	return &stepProgressWriter{
		out:    out,
		report: report,
	}
}

func (w *stepProgressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.out.Write(p)
	if n <= 0 {
		return n, err
	}
	w.buf = append(w.buf, p[:n]...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
		w.handleLine(line)
	}
	return n, err
}

func (w *stepProgressWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) == 0 {
		return
	}
	line := strings.TrimSpace(string(w.buf))
	w.buf = nil
	w.handleLine(line)
}

func (w *stepProgressWriter) handleLine(line string) {
	if line == "" {
		return
	}
	if step, ok := parseStepLine(line); ok {
		w.report(step, "running "+step)
	}
}

func parseStepLine(line string) (string, bool) {
	const marker = "SPADEFORGE_STEP:"
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	step := strings.TrimSpace(line[idx+len(marker):])
	if step == "" {
		return "", false
	}
	for i, r := range step {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			step = step[:i]
			break
		}
	}
	if step == "" {
		return "", false
	}
	return step, true
}

func tclBrace(v string) string {
	return "{" + strings.ReplaceAll(v, "}", "\\}") + "}"
}

func tclWord(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "{}"
	}
	if strings.ContainsAny(trimmed, " \t\n") {
		return tclBrace(trimmed)
	}
	return trimmed
}
