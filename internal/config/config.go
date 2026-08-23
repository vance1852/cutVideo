// Package config loads and validates runtime configuration for the cutVideo
// render orchestration service. Configuration is injected through environment
// variables so that no credential or endpoint is compiled into the binary.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults used when the corresponding environment variable is absent.
const (
	DefaultHTTPAddr           = ":8080"
	DefaultDatabaseDSN        = "file:cutvideo.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	DefaultSessionTTL         = 12 * time.Hour
	DefaultRenderLeaseTTL     = 2 * time.Minute
	DefaultWorkerPollInterval = 250 * time.Millisecond
	DefaultWorkerLeaseRenew   = 30 * time.Second
	DefaultWorkerConcurrency  = 2
	DefaultMaxRenderAttempts  = 3
	DefaultRetryBackoff       = 2 * time.Second
	DefaultShutdownGrace      = 10 * time.Second
	DefaultAssetRetention     = 720 * time.Hour
	DefaultRequestTimeout     = 15 * time.Second
	DefaultIdempotencyTTL     = 24 * time.Hour
)

// Config is the fully validated configuration tree for one server process.
type Config struct {
	Env      string
	HTTP     HTTPConfig
	Database DatabaseConfig
	Auth     AuthConfig
	Render   RenderConfig
	Worker   WorkerConfig
	Media    MediaConfig
	Logging  LoggingConfig
}

// HTTPConfig controls the public HTTP surface.
type HTTPConfig struct {
	Addr           string
	RequestTimeout time.Duration
	ShutdownGrace  time.Duration
}

// DatabaseConfig controls the relational store.
type DatabaseConfig struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
	RunMigrations   bool
}

// AuthConfig controls session and credential lifecycle.
type AuthConfig struct {
	SessionTTL     time.Duration
	PasswordPepper string
	IdempotencyTTL time.Duration
}

// RenderConfig controls render job scheduling policy.
type RenderConfig struct {
	LeaseTTL     time.Duration
	MaxAttempts  int
	RetryBackoff time.Duration
	Presets      []string
}

// WorkerConfig controls the background render worker pool.
type WorkerConfig struct {
	Enabled bool
	// Concurrency is the number of encoder goroutines polling the queue.
	Concurrency  int
	PollInterval time.Duration
	// LeaseRenewInterval is how often a worker refreshes the lease of the job it
	// is encoding. It must stay below the render lease TTL, otherwise the
	// housekeeping reaper reclaims seats that are still in use.
	LeaseRenewInterval time.Duration
	ReaperPeriod       time.Duration
}

// MediaConfig controls media ingest policy.
type MediaConfig struct {
	Retention      time.Duration
	MaxAssetBytes  int64
	AllowedFormats []string
}

// LoggingConfig controls structured logging.
type LoggingConfig struct {
	Level  string
	Format string
}

// Load reads configuration from the process environment and validates it.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// Lookup mirrors os.LookupEnv so tests can inject values.
type Lookup func(string) (string, bool)

// LoadFrom builds a configuration from an arbitrary lookup function.
func LoadFrom(lookup Lookup) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("config: lookup function is required")
	}
	cfg := Config{
		Env: stringVar(lookup, "CUTVIDEO_ENV", "development"),
		HTTP: HTTPConfig{
			Addr:           stringVar(lookup, "CUTVIDEO_HTTP_ADDR", DefaultHTTPAddr),
			RequestTimeout: DefaultRequestTimeout,
			ShutdownGrace:  DefaultShutdownGrace,
		},
		Database: DatabaseConfig{
			DSN:             stringVar(lookup, "CUTVIDEO_DATABASE_DSN", DefaultDatabaseDSN),
			MaxOpenConns:    1,
			MaxIdleConns:    1,
			ConnMaxIdleTime: 5 * time.Minute,
			RunMigrations:   boolVar(lookup, "CUTVIDEO_RUN_MIGRATIONS", true),
		},
		Auth: AuthConfig{
			SessionTTL:     DefaultSessionTTL,
			PasswordPepper: stringVar(lookup, "CUTVIDEO_PASSWORD_PEPPER", "cutvideo-local-pepper"),
			IdempotencyTTL: DefaultIdempotencyTTL,
		},
		Render: RenderConfig{
			LeaseTTL:     DefaultRenderLeaseTTL,
			MaxAttempts:  DefaultMaxRenderAttempts,
			RetryBackoff: DefaultRetryBackoff,
			Presets:      splitList(stringVar(lookup, "CUTVIDEO_RENDER_PRESETS", "proxy_540p,web_1080p,master_2160p")),
		},
		Worker: WorkerConfig{
			Enabled:            boolVar(lookup, "CUTVIDEO_WORKER_ENABLED", true),
			Concurrency:        DefaultWorkerConcurrency,
			PollInterval:       DefaultWorkerPollInterval,
			LeaseRenewInterval: DefaultWorkerLeaseRenew,
			ReaperPeriod:       30 * time.Second,
		},
		Media: MediaConfig{
			Retention:      DefaultAssetRetention,
			MaxAssetBytes:  32 << 30,
			AllowedFormats: splitList(stringVar(lookup, "CUTVIDEO_MEDIA_FORMATS", "mov,mp4,mxf,prores,r3d")),
		},
		Logging: LoggingConfig{
			Level:  stringVar(lookup, "CUTVIDEO_LOG_LEVEL", "info"),
			Format: stringVar(lookup, "CUTVIDEO_LOG_FORMAT", "json"),
		},
	}

	var err error
	if cfg.Auth.SessionTTL, err = durationVar(lookup, "CUTVIDEO_SESSION_TTL", DefaultSessionTTL); err != nil {
		return Config{}, err
	}
	if cfg.Render.LeaseTTL, err = durationVar(lookup, "CUTVIDEO_RENDER_LEASE_TTL", DefaultRenderLeaseTTL); err != nil {
		return Config{}, err
	}
	if cfg.Render.RetryBackoff, err = durationVar(lookup, "CUTVIDEO_RENDER_RETRY_BACKOFF", DefaultRetryBackoff); err != nil {
		return Config{}, err
	}
	if cfg.Worker.PollInterval, err = durationVar(lookup, "CUTVIDEO_WORKER_POLL_INTERVAL", DefaultWorkerPollInterval); err != nil {
		return Config{}, err
	}
	if cfg.Worker.LeaseRenewInterval, err = durationVar(lookup, "CUTVIDEO_WORKER_LEASE_RENEW_INTERVAL", DefaultWorkerLeaseRenew); err != nil {
		return Config{}, err
	}
	if cfg.HTTP.RequestTimeout, err = durationVar(lookup, "CUTVIDEO_HTTP_REQUEST_TIMEOUT", DefaultRequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HTTP.ShutdownGrace, err = durationVar(lookup, "CUTVIDEO_SHUTDOWN_GRACE", DefaultShutdownGrace); err != nil {
		return Config{}, err
	}
	if cfg.Media.Retention, err = durationVar(lookup, "CUTVIDEO_ASSET_RETENTION", DefaultAssetRetention); err != nil {
		return Config{}, err
	}
	if cfg.Render.MaxAttempts, err = intVar(lookup, "CUTVIDEO_RENDER_MAX_ATTEMPTS", DefaultMaxRenderAttempts); err != nil {
		return Config{}, err
	}
	if cfg.Worker.Concurrency, err = intVar(lookup, "CUTVIDEO_WORKER_CONCURRENCY", DefaultWorkerConcurrency); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate reports the first configuration constraint that is not satisfied.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTP.Addr) == "" {
		return errors.New("config: CUTVIDEO_HTTP_ADDR must not be empty")
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return errors.New("config: CUTVIDEO_DATABASE_DSN must not be empty")
	}
	if c.Auth.SessionTTL <= 0 {
		return errors.New("config: session ttl must be positive")
	}
	if c.Auth.PasswordPepper == "" {
		return errors.New("config: password pepper must not be empty")
	}
	if c.Render.LeaseTTL <= 0 {
		return errors.New("config: render lease ttl must be positive")
	}
	if c.Render.MaxAttempts < 1 {
		return errors.New("config: render max attempts must be at least 1")
	}
	if c.Render.RetryBackoff < 0 {
		return errors.New("config: render retry backoff must not be negative")
	}
	if len(c.Render.Presets) == 0 {
		return errors.New("config: at least one render preset is required")
	}
	if c.Worker.Concurrency < 1 {
		return errors.New("config: worker concurrency must be at least 1")
	}
	if c.Worker.PollInterval <= 0 {
		return errors.New("config: worker poll interval must be positive")
	}
	if c.Worker.LeaseRenewInterval <= 0 {
		return errors.New("config: worker lease renew interval must be positive")
	}
	if c.Worker.LeaseRenewInterval >= c.Render.LeaseTTL {
		return errors.New("config: worker lease renew interval must be shorter than the render lease ttl")
	}
	if c.Media.Retention <= 0 {
		return errors.New("config: asset retention must be positive")
	}
	if len(c.Media.AllowedFormats) == 0 {
		return errors.New("config: at least one media format is required")
	}
	if c.HTTP.RequestTimeout <= 0 {
		return errors.New("config: request timeout must be positive")
	}
	return nil
}

// AllowsPreset reports whether the render preset is configured.
func (c Config) AllowsPreset(preset string) bool {
	for _, candidate := range c.Render.Presets {
		if strings.EqualFold(candidate, preset) {
			return true
		}
	}
	return false
}

// AllowsFormat reports whether the media container format is accepted.
func (c Config) AllowsFormat(format string) bool {
	for _, candidate := range c.Media.AllowedFormats {
		if strings.EqualFold(candidate, format) {
			return true
		}
	}
	return false
}

func stringVar(lookup Lookup, key, fallback string) string {
	if raw, ok := lookup(key); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

func boolVar(lookup Lookup, key string, fallback bool) bool {
	raw, ok := lookup(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return parsed
}

func intVar(lookup Lookup, key string, fallback int) (int, error) {
	raw, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer: %w", key, err)
	}
	return parsed, nil
}

func durationVar(lookup Lookup, key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(key)
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration: %w", key, err)
	}
	return parsed, nil
}

func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
