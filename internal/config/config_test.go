package config

import (
	"os"
	"testing"
)

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
	if cfg.PreserveWorkDir {
		t.Fatalf("expected PreserveWorkDir disabled by default")
	}
	if cfg.VivadoBin == "" {
		t.Fatalf("expected default vivado bin")
	}
	if !cfg.DiscoveryEnabled {
		t.Fatalf("expected discovery enabled by default")
	}
	if cfg.DiscoveryService == "" {
		t.Fatalf("expected default discovery service")
	}
	if cfg.DiscoveryDomain == "" {
		t.Fatalf("expected default discovery domain")
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

func TestConfig_FromEnv_PreserveWorkDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("SPADEFORGE_BASE_DIR", base)
	t.Setenv("SPADEFORGE_PRESERVE_WORK_DIR", "1")
	t.Setenv("SPADEFORGE_LISTEN_ADDR", ":8081")
	t.Setenv("SPADEFORGE_DISCOVERY_ENABLE", "0")
	_ = os.Unsetenv("SPADEFORGE_ALLOWLIST")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("from env failed: %v", err)
	}
	if !cfg.PreserveWorkDir {
		t.Fatalf("expected PreserveWorkDir enabled")
	}
	if cfg.DiscoveryEnabled {
		t.Fatalf("expected discovery disabled")
	}
}
