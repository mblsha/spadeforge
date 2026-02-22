package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("SPADELOADER_BASE_DIR", "/tmp/spadeloader-test")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Fatalf("ListenAddr = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.OpenFPGALoaderBin != "openFPGALoader" {
		t.Fatalf("OpenFPGALoaderBin = %q", cfg.OpenFPGALoaderBin)
	}
	if cfg.HistoryLimit != 100 {
		t.Fatalf("HistoryLimit = %d, want 100", cfg.HistoryLimit)
	}
	if !cfg.DiscoveryEnabled {
		t.Fatalf("expected discovery enabled")
	}
	if cfg.DiscoveryService != "_spadeloader._tcp" {
		t.Fatalf("DiscoveryService = %q", cfg.DiscoveryService)
	}
}

func TestFromEnvHistoryLimitValidation(t *testing.T) {
	t.Setenv("SPADELOADER_BASE_DIR", "/tmp/spadeloader-test")
	t.Setenv("SPADELOADER_HISTORY_LIMIT", "101")

	_, err := FromEnv()
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestAllowlistValidation(t *testing.T) {
	t.Setenv("SPADELOADER_BASE_DIR", "/tmp/spadeloader-test")
	t.Setenv("SPADELOADER_ALLOWLIST", "127.0.0.1,192.168.1.0/24")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error: %v", err)
	}
	if len(cfg.Allowlist) != 2 {
		t.Fatalf("len(Allowlist) = %d, want 2", len(cfg.Allowlist))
	}
}

func TestAllowedBoardsValidation(t *testing.T) {
	t.Setenv("SPADELOADER_BASE_DIR", "/tmp/spadeloader-test")
	t.Setenv("SPADELOADER_ALLOWED_BOARDS", "alchitry_au,my-board")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error: %v", err)
	}
	if len(cfg.AllowedBoards) != 2 {
		t.Fatalf("len(AllowedBoards) = %d, want 2", len(cfg.AllowedBoards))
	}
	if !cfg.BoardAllowed("ALCHITRY_AU") {
		t.Fatalf("expected board allowlist to be case-insensitive")
	}
	if cfg.BoardAllowed("unknown_board") {
		t.Fatalf("expected unknown_board to be rejected by allowlist")
	}
}

func TestAllowedBoardsValidationRejectsInvalidValue(t *testing.T) {
	t.Setenv("SPADELOADER_BASE_DIR", "/tmp/spadeloader-test")
	t.Setenv("SPADELOADER_ALLOWED_BOARDS", "bad board name")

	if _, err := FromEnv(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestDefaultBaseDirFor(t *testing.T) {
	t.Parallel()

	cacheRoot := "/tmp/cache-root"
	if got := defaultBaseDirFor("darwin", cacheRoot); got != filepath.Join(cacheRoot, "io.spadeforge.spadeloader") {
		t.Fatalf("darwin default base dir = %q", got)
	}
	if got := defaultBaseDirFor("linux", cacheRoot); got != filepath.Join(cacheRoot, "spadeloader") {
		t.Fatalf("linux default base dir = %q", got)
	}
	if got := defaultBaseDirFor("freebsd", cacheRoot); got != filepath.Join(cacheRoot, "spadeloader") {
		t.Fatalf("fallback default base dir = %q", got)
	}
}

func TestFromEnvUsesDefaultBaseDirWhenUnset(t *testing.T) {
	t.Setenv("SPADELOADER_BASE_DIR", "")

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("os.UserCacheDir unavailable: %v", err)
	}

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error: %v", err)
	}
	want := defaultBaseDirFor(runtime.GOOS, cacheDir)
	if cfg.BaseDir != want {
		t.Fatalf("BaseDir = %q, want %q", cfg.BaseDir, want)
	}
}
