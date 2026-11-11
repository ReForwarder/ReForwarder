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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"strings"
	"testing"
)

// clearACMEEnv unsets every POSTA_ACME_* var so a test observes the loader
// defaults (Setenv registers restore; Unsetenv removes it immediately).
func clearACMEEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"POSTA_ACME_ENABLED", "POSTA_ACME_DIRECTORY_URL", "POSTA_ACME_EMAIL",
		"POSTA_ACME_DOMAINS", "POSTA_ACME_HTTPS_HOST", "POSTA_ACME_HTTPS_PORT",
		"POSTA_ACME_HTTPS_CERT_FILE", "POSTA_ACME_HTTPS_KEY_FILE",
		"POSTA_ACME_CACHE_PREFIX", "POSTA_ACME_SYNC_SECS",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// TestACMEDisabledByDefault guards the default-off gate (ADR-007 decision 3
// is additive and config-gated): a config built with no POSTA_ACME_* keys
// set leaves ACME fully off with the standard defaults pre-filled for when
// it is turned on.
func TestACMEDisabledByDefault(t *testing.T) {
	clearACMEEnv(t)
	cfg := New()
	if cfg.ACME.Enabled {
		t.Fatal("ACME must be disabled by default (POSTA_ACME_ENABLED unset)")
	}
	if cfg.ACME.HTTPSPort != 0 {
		t.Fatalf("webmail HTTPS port = %d, want 0 (plain HTTP only)", cfg.ACME.HTTPSPort)
	}
	if cfg.ACME.DirectoryURL != "" {
		t.Fatalf("directory URL = %q, want empty (autocert default)", cfg.ACME.DirectoryURL)
	}
	if cfg.ACME.CachePrefix != "certs/acme" || cfg.ACME.SyncSecs != 300 {
		t.Fatalf("cache prefix/sync secs = %q/%d, want certs/acme/300", cfg.ACME.CachePrefix, cfg.ACME.SyncSecs)
	}
}

// TestACMEEnvOverrides: the POSTA_ACME_* keys load as documented.
func TestACMEEnvOverrides(t *testing.T) {
	clearACMEEnv(t)
	t.Setenv("POSTA_ACME_ENABLED", "true")
	t.Setenv("POSTA_ACME_DIRECTORY_URL", "https://localhost:14000/dir")
	t.Setenv("POSTA_ACME_EMAIL", "acme@example.org")
	t.Setenv("POSTA_ACME_DOMAINS", "mail.a.example, mail.b.example")
	t.Setenv("POSTA_ACME_HTTPS_PORT", "8443")
	t.Setenv("POSTA_ACME_SYNC_SECS", "60")
	cfg := New()
	if !cfg.ACME.Enabled {
		t.Fatal("POSTA_ACME_ENABLED=true must enable ACME")
	}
	if cfg.ACME.DirectoryURL != "https://localhost:14000/dir" || cfg.ACME.Email != "acme@example.org" {
		t.Fatalf("directory/email = %q/%q", cfg.ACME.DirectoryURL, cfg.ACME.Email)
	}
	if len(cfg.ACME.Domains) != 2 || cfg.ACME.Domains[0] != "mail.a.example" || cfg.ACME.Domains[1] != "mail.b.example" {
		t.Fatalf("domains = %v, want the two listed hosts", cfg.ACME.Domains)
	}
	if cfg.ACME.HTTPSPort != 8443 || cfg.ACME.SyncSecs != 60 {
		t.Fatalf("https port/sync secs = %d/%d, want 8443/60", cfg.ACME.HTTPSPort, cfg.ACME.SyncSecs)
	}
}

// TestACMEValidationRequiresBlobStore: enabling ACME without a blob store is
// refused at startup — issued certificates are cached in the blob store and
// the hot reload re-reads them (ADR-007 decision 3), so a deployment without
// one has no working certificate lifecycle.
func TestACMEValidationRequiresBlobStore(t *testing.T) {
	cfg := &Config{ACME: AcmeConfig{Enabled: true, SyncSecs: 300}}
	err := cfg.validateACME()
	if err == nil {
		t.Fatal("ACME enabled without POSTA_BLOB_PROVIDER must be refused")
	}
	if !strings.Contains(err.Error(), "POSTA_BLOB_PROVIDER") {
		t.Fatalf("error %q should name the blob provider", err)
	}
	// With a blob provider configured the same config passes.
	cfg.BlobProvider = "fs"
	if err := cfg.validateACME(); err != nil {
		t.Fatalf("ACME enabled with a blob store must pass: %v", err)
	}
}

// TestACMEValidationDisabledIsInert: validation never fires when the feature
// is off — the default deployment keeps starting with no ACME keys at all.
func TestACMEValidationDisabledIsInert(t *testing.T) {
	cfg := &Config{ACME: AcmeConfig{Enabled: false, HTTPSPort: -1, SyncSecs: 0}}
	if err := cfg.validateACME(); err != nil {
		t.Fatalf("disabled ACME must skip validation: %v", err)
	}
}

// TestACMEValidationHTTPSAndSyncMatrix: the HTTPS listener port range and
// the static fallback certificate pair follow the same rules as the other
// TLS listeners; the hot-reload interval must be positive.
func TestACMEValidationHTTPSAndSyncMatrix(t *testing.T) {
	base := AcmeConfig{Enabled: true, SyncSecs: 300}
	cfg := &Config{BlobProvider: "fs", ACME: base}

	cfg.ACME.HTTPSPort = 8443
	if err := cfg.validateACME(); err != nil {
		t.Fatalf("valid HTTPS port rejected: %v", err)
	}
	cfg.ACME.HTTPSPort = -1
	if err := cfg.validateACME(); err == nil {
		t.Fatal("negative HTTPS port must be refused")
	}
	cfg.ACME = base
	cfg.ACME.HTTPSCertFile = "/certs/web.pem"
	if err := cfg.validateACME(); err == nil {
		t.Fatal("HTTPS cert without key must be refused")
	}
	cfg.ACME.HTTPSCertFile, cfg.ACME.HTTPSKeyFile = "", "/certs/web.key"
	if err := cfg.validateACME(); err == nil {
		t.Fatal("HTTPS key without cert must be refused")
	}
	cfg.ACME = base
	cfg.ACME.SyncSecs = 0
	if err := cfg.validateACME(); err == nil {
		t.Fatal("non-positive sync interval must be refused")
	}
}
