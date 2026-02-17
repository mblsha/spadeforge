package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	VivadoBin string
	Runner    Runner
	OSName    string
}

func NewVivadoBuilder(vivadoBin string, runner Runner) *VivadoBuilder {
	if runner == nil {
		runner = OSRunner{}
	}
	return &VivadoBuilder{
		VivadoBin: vivadoBin,
		Runner:    runner,
		OSName:    runtime.GOOS,
	}
}

func (b *VivadoBuilder) Build(ctx context.Context, job BuildJob) (BuildResult, error) {
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

	spec := buildVivadoCommand(b.OSName, b.VivadoBin, tclPath, job.WorkDir)
	exitCode, runErr := b.Runner.Run(ctx, spec, consoleFile, consoleFile)

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
		fmt.Sprintf("synth_design -top %s -part %s", tclWord(job.Manifest.Top), tclWord(job.Manifest.Part)),
		"opt_design",
		"place_design",
		"route_design",
		fmt.Sprintf("report_timing_summary -file %s", tclBrace(filepath.ToSlash(filepath.Join(job.ArtifactsDir, "timing.rpt")))),
		fmt.Sprintf("report_utilization -file %s", tclBrace(filepath.ToSlash(filepath.Join(job.ArtifactsDir, "utilization.rpt")))),
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
