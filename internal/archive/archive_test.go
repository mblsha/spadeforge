package archive

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractZip_RejectsDotDotPaths(t *testing.T) {
	zipPath := writeZipFile(t, map[string]string{"../evil.txt": "nope"})
	_, err := ExtractZipSecure(zipPath, filepath.Join(t.TempDir(), "out"), defaultLimits())
	if err == nil {
		t.Fatalf("expected error for traversal path")
	}
}

func TestExtractZip_RejectsAbsolutePaths(t *testing.T) {
	zipPath := writeZipFile(t, map[string]string{"/abs.txt": "nope"})
	_, err := ExtractZipSecure(zipPath, filepath.Join(t.TempDir(), "out"), defaultLimits())
	if err == nil {
		t.Fatalf("expected error for absolute path")
	}
}

func TestExtractZip_RejectsSymlinks(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "in.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "link"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("target")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ExtractZipSecure(zipPath, filepath.Join(t.TempDir(), "out"), defaultLimits())
	if err == nil {
		t.Fatalf("expected symlink to be rejected")
	}
}

func TestExtractZip_EnforcesLimits(t *testing.T) {
	zipPath := writeZipFile(t, map[string]string{"a.txt": "abcd"})

	_, err := ExtractZipSecure(zipPath, filepath.Join(t.TempDir(), "out1"), Limits{
		MaxFiles:      1,
		MaxTotalBytes: 3,
		MaxFileBytes:  10,
	})
	if err == nil {
		t.Fatalf("expected total bytes limit error")
	}

	_, err = ExtractZipSecure(zipPath, filepath.Join(t.TempDir(), "out2"), Limits{
		MaxFiles:      1,
		MaxTotalBytes: 10,
		MaxFileBytes:  3,
	})
	if err == nil {
		t.Fatalf("expected max file bytes limit error")
	}

	zipPath2 := writeZipFile(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	_, err = ExtractZipSecure(zipPath2, filepath.Join(t.TempDir(), "out3"), Limits{
		MaxFiles:      1,
		MaxTotalBytes: 10,
		MaxFileBytes:  10,
	})
	if err == nil {
		t.Fatalf("expected max files limit error")
	}
}

func TestWriteZipFromDir_IncludesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "console.log"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "timing.rpt"), []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := WriteZipFromDir(dir, &out); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range zr.File {
		found[f.Name] = true
	}
	if !found["console.log"] || !found["nested/timing.rpt"] {
		t.Fatalf("expected files in zip, got %v", found)
	}
}

func writeZipFile(t *testing.T, files map[string]string) string {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "in.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
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
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return zipPath
}

func defaultLimits() Limits {
	return Limits{MaxFiles: 100, MaxTotalBytes: 1024 * 1024, MaxFileBytes: 1024 * 1024}
}
