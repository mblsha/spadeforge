package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr              = ":8080"
	defaultAuthHeader              = "X-Build-Token"
	defaultMaxUploadBytes    int64 = 256 << 20
	defaultMaxFiles                = 4096
	defaultMaxExtractedTotal int64 = 1024 << 20
	defaultMaxExtractedFile  int64 = 256 << 20
	defaultWorkerTimeout           = 2 * time.Hour
	defaultRetentionDays           = 14
	defaultVivadoBin               = "vivado"
)

// Config controls server behavior.
type Config struct {
	ListenAddr string
	BaseDir    string

	Token      string
	AuthHeader string
	Allowlist  []string

	MaxUploadBytes         int64
	MaxExtractedFiles      int
	MaxExtractedTotalBytes int64
	MaxExtractedFileBytes  int64

	WorkerTimeout time.Duration
	RetentionDays int

	VivadoBin string
}

func Default() Config {
	return Config{
		ListenAddr:             defaultListenAddr,
		AuthHeader:             defaultAuthHeader,
		MaxUploadBytes:         defaultMaxUploadBytes,
		MaxExtractedFiles:      defaultMaxFiles,
		MaxExtractedTotalBytes: defaultMaxExtractedTotal,
		MaxExtractedFileBytes:  defaultMaxExtractedFile,
		WorkerTimeout:          defaultWorkerTimeout,
		RetentionDays:          defaultRetentionDays,
		VivadoBin:              defaultVivadoBin,
	}
}

func FromEnv() (Config, error) {
	cfg := Default()
	cfg.ListenAddr = getEnv("SPADEFORGE_LISTEN_ADDR", cfg.ListenAddr)
	cfg.BaseDir = strings.TrimSpace(os.Getenv("SPADEFORGE_BASE_DIR"))
	cfg.Token = strings.TrimSpace(os.Getenv("SPADEFORGE_TOKEN"))
	cfg.AuthHeader = getEnv("SPADEFORGE_AUTH_HEADER", cfg.AuthHeader)
	cfg.Allowlist = parseCSV(os.Getenv("SPADEFORGE_ALLOWLIST"))
	cfg.VivadoBin = getEnv("SPADEFORGE_VIVADO_BIN", cfg.VivadoBin)

	if v := strings.TrimSpace(os.Getenv("SPADEFORGE_MAX_UPLOAD_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADEFORGE_MAX_UPLOAD_BYTES: %w", err)
		}
		cfg.MaxUploadBytes = n
	}
	if v := strings.TrimSpace(os.Getenv("SPADEFORGE_MAX_EXTRACTED_FILES")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADEFORGE_MAX_EXTRACTED_FILES: %w", err)
		}
		cfg.MaxExtractedFiles = n
	}
	if v := strings.TrimSpace(os.Getenv("SPADEFORGE_MAX_EXTRACTED_TOTAL_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADEFORGE_MAX_EXTRACTED_TOTAL_BYTES: %w", err)
		}
		cfg.MaxExtractedTotalBytes = n
	}
	if v := strings.TrimSpace(os.Getenv("SPADEFORGE_MAX_EXTRACTED_FILE_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADEFORGE_MAX_EXTRACTED_FILE_BYTES: %w", err)
		}
		cfg.MaxExtractedFileBytes = n
	}
	if v := strings.TrimSpace(os.Getenv("SPADEFORGE_WORKER_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADEFORGE_WORKER_TIMEOUT: %w", err)
		}
		cfg.WorkerTimeout = d
	}
	if v := strings.TrimSpace(os.Getenv("SPADEFORGE_RETENTION_DAYS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADEFORGE_RETENTION_DAYS: %w", err)
		}
		cfg.RetentionDays = n
	}

	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.BaseDir) == "" {
		return errors.New("base dir is required")
	}
	if strings.TrimSpace(c.ListenAddr) == "" {
		return errors.New("listen addr is required")
	}
	if strings.TrimSpace(c.AuthHeader) == "" {
		return errors.New("auth header is required")
	}
	if c.MaxUploadBytes <= 0 {
		return errors.New("max upload bytes must be > 0")
	}
	if c.MaxExtractedFiles <= 0 {
		return errors.New("max extracted files must be > 0")
	}
	if c.MaxExtractedTotalBytes <= 0 {
		return errors.New("max extracted total bytes must be > 0")
	}
	if c.MaxExtractedFileBytes <= 0 {
		return errors.New("max extracted file bytes must be > 0")
	}
	if c.WorkerTimeout <= 0 {
		return errors.New("worker timeout must be > 0")
	}
	if c.RetentionDays < 0 {
		return errors.New("retention days must be >= 0")
	}
	if strings.TrimSpace(c.VivadoBin) == "" {
		return errors.New("vivado bin is required")
	}
	for _, entry := range c.Allowlist {
		if err := validateAllowEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) JobsDir() string {
	return filepath.Join(c.BaseDir, "jobs")
}

func (c Config) WorkDir() string {
	return filepath.Join(c.BaseDir, "work")
}

func (c Config) ArtifactsDir() string {
	return filepath.Join(c.BaseDir, "artifacts")
}

func (c Config) AllowlistEnabled() bool {
	return len(c.Allowlist) > 0
}

func getEnv(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

func parseCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func validateAllowEntry(entry string) error {
	if entry == "" {
		return errors.New("allowlist entry cannot be empty")
	}
	if strings.Contains(entry, "/") {
		if _, _, err := net.ParseCIDR(entry); err != nil {
			return fmt.Errorf("invalid allowlist cidr %q: %w", entry, err)
		}
		return nil
	}
	if ip := net.ParseIP(entry); ip == nil {
		return fmt.Errorf("invalid allowlist ip %q", entry)
	}
	return nil
}
