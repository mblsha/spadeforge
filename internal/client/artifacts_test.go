package client

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractArtifactZip_ExtractsFiles(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"console.log":       "ok",
		"design.bit":        "bit",
		"nested/timing.rpt": "timing",
	})
	outDir := filepath.Join(t.TempDir(), "out")
	if err := ExtractArtifactZip(raw, outDir); err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	for _, path := range []string{
		filepath.Join(outDir, "console.log"),
		filepath.Join(outDir, "design.bit"),
		filepath.Join(outDir, "nested", "timing.rpt"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s: %v", path, err)
		}
	}
}

func TestExtractArtifactZip_RejectsPathTraversal(t *testing.T) {
	raw := buildZip(t, map[string]string{"../evil.txt": "x"})
	if err := ExtractArtifactZip(raw, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatalf("expected traversal rejection")
	}
}

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
