package client

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mblsha/spadeforge/internal/manifest"
)

type BundleSpec struct {
	Project     string
	Top         string
	Part        string
	Sources     []string
	Constraints []string
	IncludeDirs []string
}

func BuildBundle(spec BundleSpec) ([]byte, error) {
	if strings.TrimSpace(spec.Top) == "" {
		return nil, fmt.Errorf("top is required")
	}
	if strings.TrimSpace(spec.Part) == "" {
		return nil, fmt.Errorf("part is required")
	}
	if len(spec.Sources) == 0 {
		return nil, fmt.Errorf("at least one source is required")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifestSources := make([]string, 0, len(spec.Sources))
	manifestConstraints := make([]string, 0, len(spec.Constraints))

	seen := map[string]struct{}{}

	for _, src := range spec.Sources {
		rel := "hdl/" + filepath.Base(src)
		if err := addFile(zw, rel, src); err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, dup := seen[rel]; dup {
			_ = zw.Close()
			return nil, fmt.Errorf("duplicate bundle path: %s", rel)
		}
		seen[rel] = struct{}{}
		manifestSources = append(manifestSources, filepath.ToSlash(rel))
	}

	for _, c := range spec.Constraints {
		rel := "constraints/" + filepath.Base(c)
		if err := addFile(zw, rel, c); err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, dup := seen[rel]; dup {
			_ = zw.Close()
			return nil, fmt.Errorf("duplicate bundle path: %s", rel)
		}
		seen[rel] = struct{}{}
		manifestConstraints = append(manifestConstraints, filepath.ToSlash(rel))
	}

	mf := manifest.Manifest{
		Schema:      1,
		Project:     spec.Project,
		Top:         spec.Top,
		Part:        spec.Part,
		Sources:     manifestSources,
		Constraints: manifestConstraints,
		IncludeDirs: spec.IncludeDirs,
		Build: manifest.Build{
			Steps: []string{"synth", "impl", "bitstream"},
		},
	}
	rawManifest, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	mfw, err := zw.Create("manifest.json")
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	if _, err := mfw.Write(rawManifest); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func addFile(zw *zip.Writer, archivePath, sourcePath string) error {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", sourcePath, err)
	}
	w, err := zw.Create(filepath.ToSlash(archivePath))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(raw))
	return err
}
