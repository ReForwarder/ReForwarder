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
	"strings"
	"testing"
)

// mailProtoEnv is a valid, enabled IMAP+POP3 configuration: both ports per
// protocol, a self-signed-style certificate pair, and the defaults for the
// limits. Individual cases override single keys with set.
func mailProtoEnv(t *testing.T, set map[string]string) {
	t.Helper()
	// The default environment is "production", whose ValidateSecurity gate
	// (JWT placeholder fatal) fires before this matrix's accepted cases can
	// be observed; the matrix tests the mail-protocol rules, so run in dev.
	t.Setenv("POSTA_ENV", "dev")
	for _, k := range []string{
		"POSTA_IMAP_ENABLED", "POSTA_IMAP_HOST", "POSTA_IMAP_PORT", "POSTA_IMAP_TLS_PORT",
		"POSTA_IMAP_TLS_CERT_FILE", "POSTA_IMAP_TLS_KEY_FILE",
		"POSTA_IMAP_MAX_SESSIONS_PER_USER", "POSTA_IMAP_MAX_CONNS_PER_IP",
		"POSTA_IMAP_COMMAND_RATE_LIMIT", "POSTA_IMAP_COMMAND_RATE_WINDOW",
		"POSTA_POP3_ENABLED", "POSTA_POP3_HOST", "POSTA_POP3_PORT", "POSTA_POP3_TLS_PORT",
		"POSTA_POP3_TLS_CERT_FILE", "POSTA_POP3_TLS_KEY_FILE",
		"POSTA_POP3_MAX_SESSIONS_PER_USER", "POSTA_POP3_MAX_CONNS_PER_IP",
		"POSTA_POP3_COMMAND_RATE_LIMIT", "POSTA_POP3_COMMAND_RATE_WINDOW",
		"POSTA_INBOUND_ENABLED", "POSTA_SMTP_RELAY_ENABLED", "POSTA_SUBMISSION_ENABLED",
		"POSTA_INBOUND_SMTP_PORT", "POSTA_SMTP_RELAY_PORT", "POSTA_SUBMISSION_PORT",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	for k, v := range set {
		t.Setenv(k, v)
	}
}

// baseMailEnv turns both protocols on with certificates on the loopback-free
// default bind — the production posture the validation matrix starts from.
func baseMailEnv() map[string]string {
	return map[string]string{
		"POSTA_IMAP_ENABLED":       "true",
		"POSTA_IMAP_TLS_CERT_FILE": "/certs/imap.pem",
		"POSTA_IMAP_TLS_KEY_FILE":  "/certs/imap.key",
		"POSTA_POP3_ENABLED":       "true",
		"POSTA_POP3_TLS_CERT_FILE": "/certs/pop3.pem",
		"POSTA_POP3_TLS_KEY_FILE":  "/certs/pop3.key",
	}
}

// TestMailProtocolsDisabledByDefault guards the default-off gate: a config
// built with none of the POSTA_IMAP_*/POSTA_POP3_* keys set leaves both
// protocols disabled with the standard ports pre-filled for when they are.
func TestMailProtocolsDisabledByDefault(t *testing.T) {
	mailProtoEnv(t, nil)
	cfg := New()
	if cfg.IMAP.Enabled || cfg.POP3.Enabled {
		t.Fatalf("mail protocols must be disabled by default: imap=%v pop3=%v", cfg.IMAP.Enabled, cfg.POP3.Enabled)
	}
	if cfg.IMAP.Port != 143 || cfg.IMAP.TLSPort != 993 {
		t.Fatalf("IMAP ports = %d/%d, want 143/993", cfg.IMAP.Port, cfg.IMAP.TLSPort)
	}
	if cfg.POP3.Port != 110 || cfg.POP3.TLSPort != 995 {
		t.Fatalf("POP3 ports = %d/%d, want 110/995", cfg.POP3.Port, cfg.POP3.TLSPort)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
}

// TestMailProtocolsValidationMatrix walks the config validation rules: every
// rejected case must fail with a message naming the offending key, and every
// accepted case must validate.
func TestMailProtocolsValidationMatrix(t *testing.T) {
	cases := []struct {
		name    string
		overlay map[string]string
		wantErr string // "" = must validate
	}{
		{
			name:    "enabled with certs on public bind is accepted",
			overlay: baseMailEnv(),
		},
		{
			name: "cleartext-only listener on loopback is accepted",
			overlay: map[string]string{
				"POSTA_IMAP_ENABLED":  "true",
				"POSTA_IMAP_HOST":     "127.0.0.1",
				"POSTA_IMAP_PORT":     "1143",
				"POSTA_IMAP_TLS_PORT": "0",
			},
		},
		{
			name: "cleartext-only listener on public bind is refused",
			overlay: map[string]string{
				"POSTA_IMAP_ENABLED":  "true",
				"POSTA_IMAP_TLS_PORT": "0",
			},
			wantErr: "POSTA_IMAP_PORT=143 without POSTA_IMAP_TLS_CERT_FILE",
		},
		{
			name: "implicit TLS port without certificate is refused",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_POP3_TLS_CERT_FILE"] = ""
				m["POSTA_POP3_TLS_KEY_FILE"] = ""
				return m
			}(),
			wantErr: "POSTA_POP3_TLS_PORT=995 requires",
		},
		{
			name:    "implicit TLS port without certificate is refused (key file missing)",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_POP3_TLS_KEY_FILE"] = ""; return m }(),
			wantErr: "POSTA_POP3_TLS_PORT=995 requires POSTA_POP3_TLS_CERT_FILE and POSTA_POP3_TLS_KEY_FILE",
		},
		{
			name: "both ports zero is refused",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_IMAP_PORT"] = "0"
				m["POSTA_IMAP_TLS_PORT"] = "0"
				return m
			}(),
			wantErr: "POSTA_IMAP_ENABLED=true needs POSTA_IMAP_PORT or POSTA_IMAP_TLS_PORT above 0",
		},
		{
			name:    "negative port is refused",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_IMAP_PORT"] = "-1"; return m }(),
			wantErr: "cannot be negative",
		},
		{
			name:    "port above 65535 is refused",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_IMAP_TLS_PORT"] = "70000"; return m }(),
			wantErr: "at most 65535",
		},
		{
			name:    "zero per-user session cap is refused",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_IMAP_MAX_SESSIONS_PER_USER"] = "0"; return m }(),
			wantErr: "POSTA_IMAP_MAX_SESSIONS_PER_USER must be at least 1",
		},
		{
			name:    "zero per-IP cap is refused",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_POP3_MAX_CONNS_PER_IP"] = "0"; return m }(),
			wantErr: "POSTA_POP3_MAX_CONNS_PER_IP must be at least 1",
		},
		{
			name:    "zero command rate limit is refused",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_IMAP_COMMAND_RATE_LIMIT"] = "0"; return m }(),
			wantErr: "POSTA_IMAP_COMMAND_RATE_LIMIT and POSTA_IMAP_COMMAND_RATE_WINDOW must be at least 1",
		},
		{
			name:    "zero command rate window is refused",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_POP3_COMMAND_RATE_WINDOW"] = "0"; return m }(),
			wantErr: "POSTA_POP3_COMMAND_RATE_LIMIT and POSTA_POP3_COMMAND_RATE_WINDOW must be at least 1",
		},
		{
			name:    "empty host is refused",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_IMAP_HOST"] = ""; return m }(),
			wantErr: "POSTA_IMAP_HOST cannot be empty",
		},
		{
			name: "identical plain ports collide",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_IMAP_PORT"] = "1143"
				m["POSTA_POP3_PORT"] = "1143"
				return m
			}(),
			wantErr: "POSTA_IMAP_PORT=1143 and POSTA_POP3_PORT=1143 collide",
		},
		{
			name: "identical TLS ports collide",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_IMAP_TLS_PORT"] = "1995"
				m["POSTA_POP3_TLS_PORT"] = "1995"
				return m
			}(),
			wantErr: "POSTA_IMAP_TLS_PORT=1995 and POSTA_POP3_TLS_PORT=1995 collide",
		},
		{
			name:    "plain and TLS ports across protocols collide",
			overlay: func() map[string]string { m := baseMailEnv(); m["POSTA_IMAP_PORT"] = "995"; return m }(),
			wantErr: "configured twice",
		},
		{
			name: "collision with the enabled inbound SMTP listener is refused",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_INBOUND_ENABLED"] = "true"
				m["POSTA_INBOUND_SMTP_PORT"] = "2525"
				m["POSTA_IMAP_PORT"] = "2525"
				return m
			}(),
			wantErr: "collides with POSTA_INBOUND_SMTP_PORT=2525",
		},
		{
			name: "collision with the HTTP port is refused",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_PORT"] = "9000"
				m["POSTA_IMAP_TLS_PORT"] = "9000"
				return m
			}(),
			wantErr: "collides with POSTA_PORT=9000",
		},
		{
			name: "disabled SMTP listeners do not block mail ports",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_SMTP_RELAY_PORT"] = "1143" // relay disabled: its port is free
				m["POSTA_IMAP_PORT"] = "1143"
				return m
			}(),
		},
		{
			name: "disabled protocol ignores its invalid limits",
			overlay: func() map[string]string {
				m := baseMailEnv()
				m["POSTA_IMAP_ENABLED"] = "false"
				m["POSTA_IMAP_COMMAND_RATE_LIMIT"] = "0"
				return m
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mailProtoEnv(t, tc.overlay)
			cfg := New()
			err := cfg.validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected the configuration to validate, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a validation error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestMailProtocolEnvParsing checks the env keys reach the struct with the
// documented names (an operator cannot debug a typo'd key without this).
func TestMailProtocolEnvParsing(t *testing.T) {
	mailProtoEnv(t, map[string]string{
		"POSTA_IMAP_ENABLED":               "true",
		"POSTA_IMAP_HOST":                  "192.0.2.10",
		"POSTA_IMAP_PORT":                  "10143",
		"POSTA_IMAP_TLS_PORT":              "10993",
		"POSTA_IMAP_HOSTNAME":              "mail.example.org",
		"POSTA_IMAP_TLS_CERT_FILE":         "c.pem",
		"POSTA_IMAP_TLS_KEY_FILE":          "c.key",
		"POSTA_IMAP_MAX_SESSIONS_PER_USER": "4",
		"POSTA_IMAP_MAX_CONNS_PER_IP":      "8",
		"POSTA_IMAP_COMMAND_RATE_LIMIT":    "120",
		"POSTA_IMAP_COMMAND_RATE_WINDOW":   "30",
		"POSTA_POP3_ENABLED":               "false",
	})
	cfg := New()
	if !cfg.IMAP.Enabled || cfg.POP3.Enabled {
		t.Fatalf("enabled flags parsed wrong: imap=%v pop3=%v", cfg.IMAP.Enabled, cfg.POP3.Enabled)
	}
	m := cfg.IMAP
	if m.Host != "192.0.2.10" || m.Port != 10143 || m.TLSPort != 10993 || m.Hostname != "mail.example.org" {
		t.Fatalf("listener fields parsed wrong: %+v", m)
	}
	if m.TLSCertFile != "c.pem" || m.TLSKeyFile != "c.key" {
		t.Fatalf("TLS files parsed wrong: %+v", m)
	}
	if m.MaxSessionsPerUser != 4 || m.MaxConnsPerIP != 8 || m.CommandRateLimit != 120 || m.CommandRateWindow != 30 {
		t.Fatalf("limits parsed wrong: %+v", m)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("parsed config must validate: %v", err)
	}
}

// TestMailProtocolsLoopbackHostVariants pins the loopback detector the
// cleartext exception relies on: loopback binds pass, look-alikes do not.
func TestMailProtocolsLoopbackHostVariants(t *testing.T) {
	loopback := []string{"localhost", "127.0.0.1", "127.9.9.9", "::1", "[::1]"}
	for _, h := range loopback {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	notLoopback := []string{"0.0.0.0", "", "example.com", "192.0.2.1", "::", "127.0.0.1.evil.com"}
	for _, h := range notLoopback {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}
