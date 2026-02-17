package builder

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mblsha/spadeforge/internal/manifest"
)

func TestTclGeneration_ContainsExpectedReadCommands(t *testing.T) {
	job := BuildJob{
		SourceDir:    "/tmp/src",
		ArtifactsDir: "/tmp/artifacts",
		Manifest: manifest.Manifest{
			Top:         "top",
			Part:        "xc7a35tcsg324-1",
			Sources:     []string{"hdl/spade.sv", "hdl/extra.sv"},
			Constraints: []string{"constraints/top.xdc"},
			IncludeDirs: []string{"hdl/include"},
		},
	}
	tcl := GenerateTCL(job)
	if !strings.Contains(tcl, "read_verilog -sv") {
		t.Fatalf("expected read_verilog command")
	}
	if !strings.Contains(tcl, "read_xdc") {
		t.Fatalf("expected read_xdc command")
	}
	if !strings.Contains(tcl, "-include_dirs") {
		t.Fatalf("expected include dirs in read_verilog command")
	}
	if !strings.Contains(tcl, "synth_design -top top -part xc7a35tcsg324-1") {
		t.Fatalf("expected synth_design command")
	}
	if !strings.Contains(tcl, "SPADEFORGE_STEP:synth") {
		t.Fatalf("expected step marker in tcl")
	}
	if !strings.Contains(tcl, "write_bitstream -force") {
		t.Fatalf("expected write_bitstream command")
	}
}

func TestParseStepLine(t *testing.T) {
	step, ok := parseStepLine("INFO: SPADEFORGE_STEP:route")
	if !ok || step != "route" {
		t.Fatalf("expected parsed step route, got step=%q ok=%v", step, ok)
	}
	_, ok = parseStepLine("INFO: no marker")
	if ok {
		t.Fatalf("did not expect marker parse")
	}
}

func TestVivadoCommand_WrapsBatWithCmdExe(t *testing.T) {
	spec := buildVivadoCommand("windows", `C:\Xilinx\Vivado\bin\vivado.bat`, `C:\work\build.tcl`, `C:\work`)
	if spec.Name != "cmd.exe" {
		t.Fatalf("expected cmd.exe, got %s", spec.Name)
	}
	if len(spec.Args) < 3 || spec.Args[0] != "/C" {
		t.Fatalf("expected /C wrapper args, got %#v", spec.Args)
	}
}

func TestVivadoBuilder_InvokesRunnerWithCorrectArgs(t *testing.T) {
	runner := &recordingRunner{}
	vb := NewVivadoBuilder("vivado", runner)
	vb.OSName = "linux"

	job := makeBuildJob(t)
	runner.hook = func(spec CommandSpec) error {
		return os.WriteFile(filepath.Join(job.ArtifactsDir, "design.bit"), []byte("bit"), 0o644)
	}
	_, err := vb.Build(context.Background(), job)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if runner.spec.Name != "vivado" {
		t.Fatalf("unexpected binary: %s", runner.spec.Name)
	}
	if runner.spec.Dir != job.WorkDir {
		t.Fatalf("expected work dir %s, got %s", job.WorkDir, runner.spec.Dir)
	}
}

func TestVivadoBuilder_CollectsLogsAndReports(t *testing.T) {
	runner := &recordingRunner{}
	vb := NewVivadoBuilder("vivado", runner)
	vb.OSName = "linux"
	job := makeBuildJob(t)

	runner.hook = func(spec CommandSpec) error {
		if err := os.WriteFile(filepath.Join(spec.Dir, "vivado.log"), []byte("log"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(spec.Dir, "vivado.jou"), []byte("jou"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "design.bit"), []byte("bit"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(job.ArtifactsDir, "timing.rpt"), []byte("timing"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(job.ArtifactsDir, "utilization.rpt"), []byte("util"), 0o644)
	}

	_, err := vb.Build(context.Background(), job)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	for _, file := range []string{"console.log", "vivado.log", "vivado.jou", "timing.rpt", "utilization.rpt", "design.bit"} {
		if _, err := os.Stat(filepath.Join(job.ArtifactsDir, file)); err != nil {
			t.Fatalf("expected %s in artifacts: %v", file, err)
		}
	}
}

func TestBuildResult_SuccessWhenBitExistsAndExit0(t *testing.T) {
	runner := &recordingRunner{}
	vb := NewVivadoBuilder("vivado", runner)
	vb.OSName = "linux"
	job := makeBuildJob(t)

	runner.exitCode = 0
	runner.hook = func(spec CommandSpec) error {
		return os.WriteFile(filepath.Join(job.ArtifactsDir, "design.bit"), []byte("bit"), 0o644)
	}

	res, err := vb.Build(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got err: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", res.ExitCode)
	}
}

func TestBuildResult_FailWhenExitNonzeroEvenIfBitExists(t *testing.T) {
	runner := &recordingRunner{exitCode: 1, err: errors.New("failed")}
	vb := NewVivadoBuilder("vivado", runner)
	vb.OSName = "linux"
	job := makeBuildJob(t)

	runner.hook = func(spec CommandSpec) error {
		return os.WriteFile(filepath.Join(job.ArtifactsDir, "design.bit"), []byte("bit"), 0o644)
	}

	_, err := vb.Build(context.Background(), job)
	if err == nil {
		t.Fatalf("expected failure")
	}
}

func makeBuildJob(t *testing.T) BuildJob {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	artifacts := filepath.Join(root, "artifacts")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(filepath.Join(src, "hdl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hdl", "spade.sv"), []byte("module top;endmodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return BuildJob{
		ID:           "job",
		WorkDir:      work,
		SourceDir:    src,
		ArtifactsDir: artifacts,
		Manifest: manifest.Manifest{
			Top:     "top",
			Part:    "xc7a35tcsg324-1",
			Sources: []string{"hdl/spade.sv"},
		},
	}
}

type recordingRunner struct {
	spec     CommandSpec
	exitCode int
	err      error
	hook     func(spec CommandSpec) error
}

func (r *recordingRunner) Run(ctx context.Context, spec CommandSpec, stdout, stderr io.Writer) (int, error) {
	_ = ctx
	r.spec = spec
	if r.hook != nil {
		if err := r.hook(spec); err != nil {
			return 1, err
		}
	}
	if r.exitCode == 0 && r.err == nil {
		return 0, nil
	}
	return r.exitCode, r.err
}
