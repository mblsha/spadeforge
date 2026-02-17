package config

import "testing"

func TestConfig_Defaults(t *testing.T) {
	cfg := Default()
	if cfg.ListenAddr == "" {
		t.Fatalf("expected default listen addr")
	}
	if cfg.AuthHeader == "" {
		t.Fatalf("expected default auth header")
	}
	if cfg.MaxUploadBytes <= 0 {
		t.Fatalf("expected MaxUploadBytes > 0")
	}
	if cfg.MaxExtractedFiles <= 0 {
		t.Fatalf("expected MaxExtractedFiles > 0")
	}
	if cfg.MaxExtractedTotalBytes <= 0 {
		t.Fatalf("expected MaxExtractedTotalBytes > 0")
	}
	if cfg.MaxExtractedFileBytes <= 0 {
		t.Fatalf("expected MaxExtractedFileBytes > 0")
	}
	if cfg.WorkerTimeout <= 0 {
		t.Fatalf("expected WorkerTimeout > 0")
	}
	if cfg.RetentionDays < 0 {
		t.Fatalf("expected RetentionDays >= 0")
	}
	if cfg.VivadoBin == "" {
		t.Fatalf("expected default vivado bin")
	}
}

func TestConfig_Validate(t *testing.T) {
	cfg := Default()
	cfg.BaseDir = t.TempDir()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate failed: %v", err)
	}

	cfg2 := cfg
	cfg2.BaseDir = ""
	if err := cfg2.Validate(); err == nil {
		t.Fatalf("expected error for missing base dir")
	}

	cfg3 := cfg
	cfg3.Allowlist = []string{"not-an-ip"}
	if err := cfg3.Validate(); err == nil {
		t.Fatalf("expected error for invalid allowlist")
	}
}
