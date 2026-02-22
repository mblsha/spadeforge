package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddr        = ":8080"
	defaultAuthHeader        = "X-Build-Token"
	defaultMaxUploadBytes    = int64(64 << 20) // 64 MiB
	defaultWorkerTimeout     = 10 * time.Minute
	defaultOpenFPGALoaderBin = "openFPGALoader"
	defaultDiscoveryEnabled  = true
	defaultDiscoveryService  = "_spadeloader._tcp"
	defaultDiscoveryDomain   = "local."
	defaultDiscoveryInstance = "spadeloader"
	defaultHistoryLimit      = 100
)

var boardNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Config controls spadeloader server behavior.
type Config struct {
	ListenAddr string
	BaseDir    string

	Token         string
	AuthHeader    string
	Allowlist     []string
	AllowedBoards []string

	OpenFPGALoaderBin string

	MaxUploadBytes int64
	WorkerTimeout  time.Duration

	HistoryLimit    int
	PreserveWorkDir bool
	UseFakeFlasher  bool

	DiscoveryEnabled  bool
	DiscoveryService  string
	DiscoveryDomain   string
	DiscoveryInstance string
}

func Default() Config {
	return Config{
		ListenAddr:        defaultListenAddr,
		AuthHeader:        defaultAuthHeader,
		OpenFPGALoaderBin: defaultOpenFPGALoaderBin,
		MaxUploadBytes:    defaultMaxUploadBytes,
		WorkerTimeout:     defaultWorkerTimeout,
		HistoryLimit:      defaultHistoryLimit,
		DiscoveryEnabled:  defaultDiscoveryEnabled,
		DiscoveryService:  defaultDiscoveryService,
		DiscoveryDomain:   defaultDiscoveryDomain,
		DiscoveryInstance: defaultDiscoveryInstance,
		PreserveWorkDir:   false,
		UseFakeFlasher:    false,
	}
}

func FromEnv() (Config, error) {
	cfg := Default()
	cfg.ListenAddr = getEnv("SPADELOADER_LISTEN_ADDR", cfg.ListenAddr)
	cfg.BaseDir = strings.TrimSpace(os.Getenv("SPADELOADER_BASE_DIR"))
	cfg.Token = strings.TrimSpace(os.Getenv("SPADELOADER_TOKEN"))
	cfg.AuthHeader = getEnv("SPADELOADER_AUTH_HEADER", cfg.AuthHeader)
	cfg.Allowlist = parseCSV(os.Getenv("SPADELOADER_ALLOWLIST"))
	cfg.AllowedBoards = parseCSV(os.Getenv("SPADELOADER_ALLOWED_BOARDS"))
	cfg.OpenFPGALoaderBin = getEnv("SPADELOADER_OPENFPGALOADER_BIN", cfg.OpenFPGALoaderBin)
	cfg.PreserveWorkDir = parseBoolEnv(os.Getenv("SPADELOADER_PRESERVE_WORK_DIR"))
	cfg.UseFakeFlasher = parseBoolEnv(os.Getenv("SPADELOADER_USE_FAKE_FLASHER"))
	cfg.DiscoveryEnabled = parseBoolEnvWithDefault(os.Getenv("SPADELOADER_DISCOVERY_ENABLE"), cfg.DiscoveryEnabled)
	cfg.DiscoveryService = getEnv("SPADELOADER_DISCOVERY_SERVICE", cfg.DiscoveryService)
	cfg.DiscoveryDomain = getEnv("SPADELOADER_DISCOVERY_DOMAIN", cfg.DiscoveryDomain)
	cfg.DiscoveryInstance = getEnv("SPADELOADER_DISCOVERY_INSTANCE", cfg.DiscoveryInstance)

	if v := strings.TrimSpace(os.Getenv("SPADELOADER_MAX_UPLOAD_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADELOADER_MAX_UPLOAD_BYTES: %w", err)
		}
		cfg.MaxUploadBytes = n
	}
	if v := strings.TrimSpace(os.Getenv("SPADELOADER_WORKER_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADELOADER_WORKER_TIMEOUT: %w", err)
		}
		cfg.WorkerTimeout = d
	}
	if v := strings.TrimSpace(os.Getenv("SPADELOADER_HISTORY_LIMIT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse SPADELOADER_HISTORY_LIMIT: %w", err)
		}
		cfg.HistoryLimit = n
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
	if c.WorkerTimeout <= 0 {
		return errors.New("worker timeout must be > 0")
	}
	if strings.TrimSpace(c.OpenFPGALoaderBin) == "" {
		return errors.New("openFPGALoader bin is required")
	}
	if c.HistoryLimit <= 0 {
		return errors.New("history limit must be > 0")
	}
	if c.HistoryLimit > defaultHistoryLimit {
		return fmt.Errorf("history limit must be <= %d", defaultHistoryLimit)
	}
	if c.DiscoveryEnabled {
		if strings.TrimSpace(c.DiscoveryService) == "" {
			return errors.New("discovery service is required when discovery is enabled")
		}
		if strings.TrimSpace(c.DiscoveryDomain) == "" {
			return errors.New("discovery domain is required when discovery is enabled")
		}
	}
	for _, entry := range c.Allowlist {
		if err := validateAllowEntry(entry); err != nil {
			return err
		}
	}
	for _, board := range c.AllowedBoards {
		if err := validateBoardName(board); err != nil {
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

func (c Config) HistoryDir() string {
	return filepath.Join(c.BaseDir, "history")
}

func (c Config) HistoryPath() string {
	return filepath.Join(c.HistoryDir(), "recent_designs.json")
}

func (c Config) AllowlistEnabled() bool {
	return len(c.Allowlist) > 0
}

func (c Config) BoardAllowlistEnabled() bool {
	return len(c.AllowedBoards) > 0
}

func (c Config) BoardAllowed(board string) bool {
	if !c.BoardAllowlistEnabled() {
		return true
	}
	target := strings.TrimSpace(board)
	for _, allowed := range c.AllowedBoards {
		if strings.EqualFold(strings.TrimSpace(allowed), target) {
			return true
		}
	}
	return false
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
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

func parseBoolEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseBoolEnvWithDefault(v string, fallback bool) bool {
	trimmed := strings.ToLower(strings.TrimSpace(v))
	if trimmed == "" {
		return fallback
	}
	switch trimmed {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
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

func validateBoardName(board string) error {
	if !boardNamePattern.MatchString(strings.TrimSpace(board)) {
		return fmt.Errorf("invalid allowed board %q; expected pattern %s", board, boardNamePattern.String())
	}
	return nil
}
