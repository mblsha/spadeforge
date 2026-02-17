package client

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func ExtractArtifactZip(raw []byte, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("open artifacts zip: %w", err)
	}

	cleanDest := filepath.Clean(destDir)
	for _, f := range zr.File {
		entry, err := sanitizeZipEntryName(f.Name)
		if err != nil {
			return err
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink entry not allowed: %s", f.Name)
		}

		targetPath := filepath.Join(cleanDest, filepath.FromSlash(entry))
		cleanTarget := filepath.Clean(targetPath)
		if !strings.HasPrefix(cleanTarget, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry escapes output dir: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanTarget, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		wf, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(wf, rc); err != nil {
			rc.Close()
			wf.Close()
			return err
		}
		if err := rc.Close(); err != nil {
			wf.Close()
			return err
		}
		if err := wf.Close(); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeZipEntryName(name string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if raw == "" {
		return "", fmt.Errorf("zip entry name cannot be empty")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("absolute zip entry path not allowed: %s", name)
	}
	if hasWindowsDrive(raw) {
		return "", fmt.Errorf("absolute zip entry path not allowed: %s", name)
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path traversal zip entry not allowed: %s", name)
	}
	return cleaned, nil
}

func hasWindowsDrive(p string) bool {
	return len(p) >= 2 && p[1] == ':'
}
