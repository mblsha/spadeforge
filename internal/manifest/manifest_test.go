package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestValidate_MissingFields(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hdl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hdl", "spade.sv"), []byte("module top;endmodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := Manifest{Part: "xc7", Sources: []string{"hdl/spade.sv"}}
	if err := m.Validate(root); err == nil {
		t.Fatalf("expected error for missing top")
	}

	m = Manifest{Top: "top", Sources: []string{"hdl/spade.sv"}}
	if err := m.Validate(root); err == nil {
		t.Fatalf("expected error for missing part")
	}

	m = Manifest{Top: "top", Part: "xc7"}
	if err := m.Validate(root); err == nil {
		t.Fatalf("expected error for missing sources")
	}
}

func TestManifestValidate_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	m := Manifest{Top: "top", Part: "xc7", Sources: []string{"../spade.sv"}}
	if err := m.Validate(root); err == nil {
		t.Fatalf("expected path traversal rejection")
	}
}

func TestManifestValidate_ReferencedFilesMustExist(t *testing.T) {
	root := t.TempDir()
	m := Manifest{Top: "top", Part: "xc7", Sources: []string{"hdl/spade.sv"}}
	if err := m.Validate(root); err == nil {
		t.Fatalf("expected missing file rejection")
	}
}

func TestManifestValidate_Success(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hdl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "constraints"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hdl", "spade.sv"), []byte("module top;endmodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "constraints", "top.xdc"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	m := Manifest{
		Top:         "top",
		Part:        "xc7a35tcsg324-1",
		Sources:     []string{"hdl/spade.sv"},
		Constraints: []string{"constraints/top.xdc"},
	}
	if err := m.Validate(root); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
}
