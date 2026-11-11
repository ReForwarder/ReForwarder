// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Posta.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"slices"
	"testing"
)

// clearRedisEnv unsets every POSTA_REDIS_* var (Setenv registers restore,
// Unsetenv removes it so goutils.Env falls back to its default).
func clearRedisEnv(t *testing.T) {
	for _, k := range []string{
		"POSTA_REDIS_ADDR",
		"POSTA_REDIS_USERNAME",
		"POSTA_REDIS_PASSWORD",
		"POSTA_REDIS_DB",
		"POSTA_REDIS_URL",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// TestNewRedisConfigBackwardCompatible: legacy addr/password (or nothing) keeps
// the same connection settings as before DB/URL support was added.
func TestNewRedisConfigBackwardCompatible(t *testing.T) {
	t.Run("defaults unchanged", func(t *testing.T) {
		clearRedisEnv(t)
		rc := newRedisConfig()
		if rc.Addr != "localhost:6379" {
			t.Fatalf("Addr = %q, want localhost:6379", rc.Addr)
		}
		if rc.Password != "" || rc.Username != "" {
			t.Fatalf("credentials = %q/%q, want empty", rc.Username, rc.Password)
		}
		if rc.DB != 0 {
			t.Fatalf("DB = %d, want 0", rc.DB)
		}
	})

	t.Run("legacy addr+password still honored", func(t *testing.T) {
		clearRedisEnv(t)
		t.Setenv("POSTA_REDIS_ADDR", "redis.internal:6380")
		t.Setenv("POSTA_REDIS_PASSWORD", "s3cret")
		rc := newRedisConfig()
		if rc.Addr != "redis.internal:6380" || rc.Password != "s3cret" {
			t.Fatalf("got %q/%q", rc.Addr, rc.Password)
		}
		if rc.DB != 0 {
			t.Fatalf("DB = %d, want 0", rc.DB)
		}
	})
}

// TestNewRedisConfigDB verifies the new POSTA_REDIS_DB selector.
func TestNewRedisConfigDB(t *testing.T) {
	clearRedisEnv(t)
	t.Setenv("POSTA_REDIS_ADDR", "localhost:6379")
	t.Setenv("POSTA_REDIS_DB", "3")
	rc := newRedisConfig()
	if rc.DB != 3 {
		t.Fatalf("DB = %d, want 3", rc.DB)
	}
	if got := rc.RedisOptions().DB; got != 3 {
		t.Fatalf("RedisOptions().DB = %d, want 3", got)
	}
	if got := rc.AsynqRedisOpt().DB; got != 3 {
		t.Fatalf("AsynqRedisOpt().DB = %d, want 3", got)
	}
}

// TestNewRedisConfigURL: POSTA_REDIS_URL is parsed, overrides the discrete
// fields, and flows through both option builders.
func TestNewRedisConfigURL(t *testing.T) {
	clearRedisEnv(t)
	// Discrete values are set but must be overridden by the URL.
	t.Setenv("POSTA_REDIS_ADDR", "ignored:1111")
	t.Setenv("POSTA_REDIS_PASSWORD", "ignored")
	t.Setenv("POSTA_REDIS_URL", "redis://alice:hunter2@cache.example.com:6390/5")

	rc := newRedisConfig()
	if rc.Addr != "cache.example.com:6390" {
		t.Fatalf("Addr = %q", rc.Addr)
	}
	if rc.Username != "alice" || rc.Password != "hunter2" {
		t.Fatalf("creds = %q/%q", rc.Username, rc.Password)
	}
	if rc.DB != 5 {
		t.Fatalf("DB = %d, want 5", rc.DB)
	}

	ro := rc.RedisOptions()
	if ro.Addr != "cache.example.com:6390" || ro.Username != "alice" || ro.Password != "hunter2" || ro.DB != 5 {
		t.Fatalf("RedisOptions mismatch: %+v", ro)
	}
	ao := rc.AsynqRedisOpt()
	if ao.Addr != "cache.example.com:6390" || ao.Username != "alice" || ao.Password != "hunter2" || ao.DB != 5 {
		t.Fatalf("AsynqRedisOpt mismatch: %+v", ao)
	}
	// Plain redis:// URL must not carry TLS.
	if rc.TLSConfig != nil {
		t.Fatalf("TLSConfig should be nil for redis:// URL")
	}
}

// TestNewRedisConfigTLS: a rediss:// URL enables TLS in both option builders.
func TestNewRedisConfigTLS(t *testing.T) {
	clearRedisEnv(t)
	t.Setenv("POSTA_REDIS_URL", "rediss://cache.example.com:6390/1")
	rc := newRedisConfig()
	if rc.TLSConfig == nil {
		t.Fatal("TLSConfig is nil, want TLS enabled for rediss://")
	}
	if rc.RedisOptions().TLSConfig == nil {
		t.Fatal("RedisOptions().TLSConfig is nil")
	}
	if rc.AsynqRedisOpt().TLSConfig == nil {
		t.Fatal("AsynqRedisOpt().TLSConfig is nil")
	}
}

func TestAllowedCORSOrigins(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want []string
	}{
		{"explicit wins", Config{CORSOrigins: " https://a.test , https://b.test", AppWebURL: "https://web.test"}, []string{"https://a.test", "https://b.test"}},
		{"derived from urls", Config{AppWebURL: "https://web.test/app/", ApiBaseURL: "https://api.test:8443/v1"}, []string{"https://web.test", "https://api.test:8443"}},
		{"duplicate origin once", Config{AppWebURL: "https://posta.test", ApiBaseURL: "https://posta.test/api"}, []string{"https://posta.test"}},
		{"nothing configured", Config{}, nil},
		{"unparseable url skipped", Config{AppWebURL: "posta.test"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.AllowedCORSOrigins(); !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCronLeaderLeaseOffByDefault (GAP-03, ADR-009 §2): the multi-replica
// leader lease must be an explicit opt-in — a plain `New()` (no env) keeps
// single-replica behavior (lease off), and the TTL only moves when the
// operator sets it.
func TestCronLeaderLeaseOffByDefault(t *testing.T) {
	t.Run("off by default with default TTL", func(t *testing.T) {
		cfg := New()
		if cfg.CronLeaderLeaseEnabled {
			t.Fatal("POSTA_CRON_LEADER_LEASE_ENABLED must default to off")
		}
		if cfg.CronLeaseTTLSeconds != 60 {
			t.Fatalf("CronLeaseTTLSeconds = %d, want 60", cfg.CronLeaseTTLSeconds)
		}
	})

	t.Run("env opt-in and TTL honored", func(t *testing.T) {
		t.Setenv("POSTA_CRON_LEADER_LEASE_ENABLED", "true")
		t.Setenv("POSTA_CRON_LEASE_TTL_SECONDS", "30")
		cfg := New()
		if !cfg.CronLeaderLeaseEnabled {
			t.Fatal("env opt-in not honored")
		}
		if cfg.CronLeaseTTLSeconds != 30 {
			t.Fatalf("CronLeaseTTLSeconds = %d, want 30", cfg.CronLeaseTTLSeconds)
		}
	})
}

// TestObservabilityConfigDefaultsOff (OB-6 W5, SF-93/GAP-51): the OTLP push
// exporter and the error-monitoring hook ship NOTHING unless an operator
// configures an endpoint/URL — plain New() must leave both disabled, and the
// env keys must be honored when set.
func TestObservabilityConfigDefaultsOff(t *testing.T) {
	t.Run("defaults off", func(t *testing.T) {
		cfg := New()
		if cfg.OTLPEndpoint != "" {
			t.Fatalf("POSTA_OTLP_ENDPOINT must default to empty (exporter off), got %q", cfg.OTLPEndpoint)
		}
		if cfg.OTLPIntervalSecs != 60 {
			t.Fatalf("POSTA_OTLP_INTERVAL_SECS = %d, want 60", cfg.OTLPIntervalSecs)
		}
		if cfg.ErrorWebhookURL != "" {
			t.Fatalf("POSTA_ERROR_WEBHOOK_URL must default to empty (hook off), got %q", cfg.ErrorWebhookURL)
		}
	})

	t.Run("env opt-in honored", func(t *testing.T) {
		t.Setenv("POSTA_OTLP_ENDPOINT", "http://otel-collector:4318")
		t.Setenv("POSTA_OTLP_INTERVAL_SECS", "15")
		t.Setenv("POSTA_OTLP_HEADERS", "Authorization=Basic x")
		t.Setenv("POSTA_ERROR_WEBHOOK_URL", "https://errors.example.com/hook")
		t.Setenv("POSTA_ERROR_WEBHOOK_SECRET", "shh")
		cfg := New()
		if cfg.OTLPEndpoint != "http://otel-collector:4318" {
			t.Fatalf("OTLPEndpoint = %q", cfg.OTLPEndpoint)
		}
		if cfg.OTLPIntervalSecs != 15 {
			t.Fatalf("OTLPIntervalSecs = %d, want 15", cfg.OTLPIntervalSecs)
		}
		if cfg.OTLPHeaders != "Authorization=Basic x" {
			t.Fatalf("OTLPHeaders = %q", cfg.OTLPHeaders)
		}
		if cfg.ErrorWebhookURL != "https://errors.example.com/hook" || cfg.ErrorWebhookSecret != "shh" {
			t.Fatalf("error hook env not honored: %q/%q", cfg.ErrorWebhookURL, cfg.ErrorWebhookSecret)
		}
	})
}
