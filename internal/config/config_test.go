package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/vance1852/cutVideo/internal/config"
)

func lookupFrom(values map[string]string) config.Lookup {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	cfg, err := config.LoadFrom(lookupFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HTTP.Addr != config.DefaultHTTPAddr {
		t.Fatalf("unexpected addr %q", cfg.HTTP.Addr)
	}
	if cfg.Auth.SessionTTL != config.DefaultSessionTTL {
		t.Fatalf("unexpected session ttl %s", cfg.Auth.SessionTTL)
	}
	if !cfg.AllowsPreset("WEB_1080P") {
		t.Fatal("preset matching must be case insensitive")
	}
	if cfg.AllowsFormat("wav") {
		t.Fatal("unlisted container formats must be rejected")
	}
}

func TestLoadFromParsesOverrides(t *testing.T) {
	cfg, err := config.LoadFrom(lookupFrom(map[string]string{
		"CUTVIDEO_HTTP_ADDR":                   "127.0.0.1:9099",
		"CUTVIDEO_SESSION_TTL":                 "45m",
		"CUTVIDEO_RENDER_LEASE_TTL":            "90s",
		"CUTVIDEO_RENDER_MAX_ATTEMPTS":         "5",
		"CUTVIDEO_RENDER_PRESETS":              "proxy_540p, web_1080p",
		"CUTVIDEO_WORKER_CONCURRENCY":          "4",
		"CUTVIDEO_WORKER_ENABLED":              "false",
		"CUTVIDEO_WORKER_LEASE_RENEW_INTERVAL": "20s",
		"CUTVIDEO_ASSET_RETENTION":             "48h",
		"CUTVIDEO_MEDIA_FORMATS":               "mov,mxf",
		"CUTVIDEO_RENDER_RETRY_BACKOFF":        "3s",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HTTP.Addr != "127.0.0.1:9099" {
		t.Fatalf("unexpected addr %q", cfg.HTTP.Addr)
	}
	if cfg.Auth.SessionTTL != 45*time.Minute {
		t.Fatalf("unexpected session ttl %s", cfg.Auth.SessionTTL)
	}
	if cfg.Render.LeaseTTL != 90*time.Second || cfg.Render.MaxAttempts != 5 || cfg.Render.RetryBackoff != 3*time.Second {
		t.Fatalf("unexpected render config %+v", cfg.Render)
	}
	if len(cfg.Render.Presets) != 2 || cfg.Render.Presets[1] != "web_1080p" {
		t.Fatalf("preset list not trimmed: %#v", cfg.Render.Presets)
	}
	if cfg.Worker.Enabled || cfg.Worker.Concurrency != 4 || cfg.Worker.LeaseRenewInterval != 20*time.Second {
		t.Fatalf("unexpected worker config %+v", cfg.Worker)
	}
	if cfg.Media.Retention != 48*time.Hour {
		t.Fatalf("unexpected retention %s", cfg.Media.Retention)
	}
}

func TestLoadFromRejectsBadValues(t *testing.T) {
	if _, err := config.LoadFrom(lookupFrom(map[string]string{"CUTVIDEO_SESSION_TTL": "forever"})); err == nil {
		t.Fatal("a non duration session ttl must fail")
	}
	if _, err := config.LoadFrom(lookupFrom(map[string]string{"CUTVIDEO_WORKER_CONCURRENCY": "many"})); err == nil {
		t.Fatal("a non numeric concurrency must fail")
	}
	if _, err := config.LoadFrom(lookupFrom(map[string]string{"CUTVIDEO_RENDER_MAX_ATTEMPTS": "0"})); err == nil {
		t.Fatal("zero attempts must fail validation")
	}
	if _, err := config.LoadFrom(lookupFrom(map[string]string{"CUTVIDEO_RENDER_PRESETS": " , "})); err == nil {
		t.Fatal("an empty preset list must fail validation")
	}
	if _, err := config.LoadFrom(lookupFrom(map[string]string{
		"CUTVIDEO_RENDER_LEASE_TTL":            "20s",
		"CUTVIDEO_WORKER_LEASE_RENEW_INTERVAL": "40s",
	})); err == nil {
		t.Fatal("a lease renew interval longer than the lease ttl must fail validation")
	}
}

func TestValidateReportsMissingSecrets(t *testing.T) {
	cfg, err := config.LoadFrom(lookupFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Auth.PasswordPepper = ""
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "pepper") {
		t.Fatalf("expected a pepper validation error, got %v", err)
	}
}
