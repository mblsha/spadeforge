package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Build struct {
	Steps []string `json:"steps,omitempty"`
}

type Manifest struct {
	Schema      int      `json:"schema"`
	Project     string   `json:"project,omitempty"`
	Top         string   `json:"top"`
	Part        string   `json:"part"`
	Sources     []string `json:"sources"`
	Constraints []string `json:"constraints,omitempty"`
	IncludeDirs []string `json:"include_dirs,omitempty"`
	Build       Build    `json:"build,omitempty"`
}

func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Schema == 0 {
		m.Schema = 1
	}
	return m, nil
}

func (m *Manifest) Validate(root string) error {
	if strings.TrimSpace(m.Top) == "" {
		return errors.New("top is required")
	}
	if strings.TrimSpace(m.Part) == "" {
		return errors.New("part is required")
	}
	if len(m.Sources) == 0 {
		return errors.New("at least one source is required")
	}

	cleanedSources, err := sanitizeList(m.Sources)
	if err != nil {
		return fmt.Errorf("sources: %w", err)
	}
	cleanedConstraints, err := sanitizeList(m.Constraints)
	if err != nil {
		return fmt.Errorf("constraints: %w", err)
	}
	cleanedIncludeDirs, err := sanitizeList(m.IncludeDirs)
	if err != nil {
		return fmt.Errorf("include_dirs: %w", err)
	}

	m.Sources = cleanedSources
	m.Constraints = cleanedConstraints
	m.IncludeDirs = cleanedIncludeDirs

	for _, source := range m.Sources {
		if err := fileExistsUnderRoot(root, source); err != nil {
			return fmt.Errorf("source %q: %w", source, err)
		}
	}
	for _, c := range m.Constraints {
		if err := fileExistsUnderRoot(root, c); err != nil {
			return fmt.Errorf("constraint %q: %w", c, err)
		}
	}
	for _, d := range m.IncludeDirs {
		if err := dirExistsUnderRoot(root, d); err != nil {
			return fmt.Errorf("include_dir %q: %w", d, err)
		}
	}

	return nil
}

func sanitizeList(items []string) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		cleaned, err := sanitizePath(item)
		if err != nil {
			return nil, err
		}
		out = append(out, cleaned)
	}
	return out, nil
}

func sanitizePath(p string) (string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if raw == "" {
		return "", errors.New("path cannot be empty")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("absolute path %q not allowed", p)
	}
	if hasWindowsDrive(raw) {
		return "", fmt.Errorf("absolute path %q not allowed", p)
	}
	cleaned := path.Clean(raw)
	if cleaned == "." {
		return "", errors.New("path cannot be current directory")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path traversal %q not allowed", p)
	}
	if strings.Contains(cleaned, "//") {
		return "", fmt.Errorf("invalid path %q", p)
	}
	return cleaned, nil
}

func hasWindowsDrive(p string) bool {
	return len(p) >= 2 && p[1] == ':'
}

func fileExistsUnderRoot(root, rel string) error {
	full, err := safeJoin(root, rel)
	if err != nil {
		return err
	}
	fi, err := os.Stat(full)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("expected file, got directory")
	}
	return nil
}

func dirExistsUnderRoot(root, rel string) error {
	full, err := safeJoin(root, rel)
	if err != nil {
		return err
	}
	fi, err := os.Stat(full)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("expected directory")
	}
	return nil
}

func safeJoin(root, rel string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	cleanRoot := filepath.Clean(root)
	cleanFull := filepath.Clean(full)
	if cleanFull == cleanRoot {
		return "", fmt.Errorf("path resolves to root")
	}
	prefix := cleanRoot + string(os.PathSeparator)
	if !strings.HasPrefix(cleanFull, prefix) {
		return "", fmt.Errorf("path escapes root")
	}
	return cleanFull, nil
}
