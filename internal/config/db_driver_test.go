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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"testing"
)

// SF-76 tests (the ADR-009 amendment): the data-store backend gate. The
// default is postgres — the pre-SF-76 behavior, unchanged — and the sqlite
// dev tier requires a database file path. Validation rejects unknown drivers
// before anything connects.

func clearDBDriverEnv(t *testing.T) {
	t.Helper()
	// The JWT-secret placeholder check is orthogonal to the DB gate; dev
	// posture bypasses it so the assertions stay on the driver rules.
	t.Setenv("POSTA_ENV", "dev")
	for _, k := range []string{"POSTA_DB_DRIVER", "POSTA_DB_SQLITE_PATH", "POSTA_DB_URL"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

// TestDBDriverDefaultUnchanged — without POSTA_DB_DRIVER the backend is
// postgres (the only production store) and the config validates exactly as it
// did before the amendment.
func TestDBDriverDefaultUnchanged(t *testing.T) {
	clearDBDriverEnv(t)
	cfg := New()
	if cfg.Database.DriverName() != DBDriverPostgres {
		t.Fatalf("default driver = %q, want postgres", cfg.Database.DriverName())
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("the default config must validate: %v", err)
	}
}

// TestDBDriverSQLiteRequiresPath — opting into the dev tier requires the
// database file.
func TestDBDriverSQLiteRequiresPath(t *testing.T) {
	clearDBDriverEnv(t)
	t.Setenv("POSTA_DB_DRIVER", DBDriverSQLite)
	cfg := New()
	if cfg.Database.DriverName() != DBDriverSQLite {
		t.Fatalf("driver = %q, want sqlite", cfg.Database.DriverName())
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("sqlite without POSTA_DB_SQLITE_PATH must not validate")
	}

	t.Setenv("POSTA_DB_SQLITE_PATH", "data/posta-dev.db")
	cfg = New()
	if err := cfg.validate(); err != nil {
		t.Fatalf("sqlite with a path must validate: %v", err)
	}
}

// TestDBDriverUnknownRejected — anything but postgres/sqlite is a
// configuration error (fail-closed before any connection attempt).
func TestDBDriverUnknownRejected(t *testing.T) {
	clearDBDriverEnv(t)
	t.Setenv("POSTA_DB_DRIVER", "rocksdb")
	cfg := New()
	if err := cfg.validate(); err == nil {
		t.Fatal("an unknown driver must not validate")
	}
}
