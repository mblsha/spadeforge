package archive

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Limits struct {
	MaxFiles      int
	MaxTotalBytes int64
	MaxFileBytes  int64
}

func ExtractZipSecure(zipPath, dest string, limits Limits) ([]string, error) {
	if limits.MaxFiles <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxFileBytes <= 0 {
		return nil, errors.New("invalid extraction limits")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, fmt.Errorf("create extraction dest: %w", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	cleanDest := filepath.Clean(dest)
	var total int64
	var count int
	created := make([]string, 0, len(zr.File))

	for _, f := range zr.File {
		entryName, err := sanitizeZipEntryName(f.Name)
		if err != nil {
			return nil, err
		}
		count++
		if count > limits.MaxFiles {
			return nil, fmt.Errorf("zip has too many entries: %d > %d", count, limits.MaxFiles)
		}

		if f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink entry not allowed: %s", f.Name)
		}

		targetPath := filepath.Join(cleanDest, filepath.FromSlash(entryName))
		cleanTarget := filepath.Clean(targetPath)
		if !strings.HasPrefix(cleanTarget, cleanDest+string(os.PathSeparator)) {
			return nil, fmt.Errorf("zip entry escapes destination: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanTarget, 0o755); err != nil {
				return nil, fmt.Errorf("create directory %q: %w", cleanTarget, err)
			}
			continue
		}

		if f.UncompressedSize64 > uint64(limits.MaxFileBytes) {
			return nil, fmt.Errorf("zip entry too large: %s", f.Name)
		}

		total += int64(f.UncompressedSize64)
		if total > limits.MaxTotalBytes {
			return nil, fmt.Errorf("zip total size exceeds limit")
		}

		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
			return nil, fmt.Errorf("create parent directory: %w", err)
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open zip file %q: %w", f.Name, err)
		}

		wf, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("create output file %q: %w", cleanTarget, err)
		}

		maxCopy := limits.MaxFileBytes + 1
		n, copyErr := io.Copy(wf, io.LimitReader(rc, maxCopy))
		closeErr := rc.Close()
		writeCloseErr := wf.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("extract %q: %w", f.Name, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close zip file %q: %w", f.Name, closeErr)
		}
		if writeCloseErr != nil {
			return nil, fmt.Errorf("close output file %q: %w", cleanTarget, writeCloseErr)
		}
		if n > limits.MaxFileBytes {
			return nil, fmt.Errorf("zip entry exceeds max file bytes while extracting: %s", f.Name)
		}

		created = append(created, entryName)
	}

	return created, nil
}

func WriteZipFromDir(srcDir string, w io.Writer) error {
	zw := zip.NewWriter(w)
	defer zw.Close()

	cleanSrc := filepath.Clean(srcDir)
	return filepath.WalkDir(cleanSrc, func(pathNow string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(cleanSrc, pathNow)
		if err != nil {
			return err
		}
		zipName := filepath.ToSlash(rel)
		if zipName == "." || strings.HasPrefix(zipName, "../") {
			return fmt.Errorf("invalid relative path: %s", rel)
		}

		fi, err := d.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		header.Name = zipName
		header.Method = zip.Deflate

		wf, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		rf, err := os.Open(pathNow)
		if err != nil {
			return err
		}
		defer rf.Close()

		_, err = io.Copy(wf, rf)
		return err
	})
}

func sanitizeZipEntryName(name string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if raw == "" {
		return "", errors.New("zip entry name cannot be empty")
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
