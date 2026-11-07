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

package jobs

// SF-82 restore verification: prove the backup job's dump actually restores.
//
// The restore runbook (backup.go godoc) says a dump is plain-format SQL
// compressed with gzip, restored by piping it through psql into a fresh
// database. This test follows the DB-tier pattern (real Postgres via
// TEST_DATABASE_DSN, skipped when unavailable — same as the repositories
// tests) and executes the runbook end to end:
//
//	seed a probe table + row → BackupJob.Run (real pg_dump) → validate the
//	dump file structure (gzip, pg_dump header, CREATE TABLE present) →
//	restore the dump into a scratch database with psql → read the marker row
//	back from the scratch database.
//
// The test skips — honestly, not silently — when it cannot run: no test
// database, or no pg_dump/psql binaries on PATH (the golang:1.27 test image
// ships neither). It never fails a battery that simply lacks the tooling.

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const restoreTestDSNEnv = "TEST_DATABASE_DSN"

// restoreTestDSN mirrors the repositories package's testDB default so the
// same single env var drives both DB-tier test tiers.
func restoreTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(restoreTestDSNEnv)
	if dsn == "" {
		dsn = "host=localhost user=posta password=posta dbname=posta port=5432 sslmode=disable"
	}
	return dsn
}

func requireRestoreTooling(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"pg_dump", "psql"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("skipping: %s not on PATH (install postgresql-client to run the restore verification)", bin)
		}
	}
}

func openRestoreDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available (%s): %v", restoreTestDSNEnv, err)
	}
	return db
}

// TestBackupRestoreRunbookRoundTrip executes the restore runbook against a
// real dump produced by the real pg_dump-backed BackupJob.
func TestBackupRestoreRunbookRoundTrip(t *testing.T) {
	requireRestoreTooling(t)
	dsn := restoreTestDSN(t)
	db := openRestoreDB(t, dsn)

	// 1. Seed state the restored database must contain: a probe table with a
	// marker row, created outside any transaction so pg_dump sees it.
	probe := fmt.Sprintf("posta_restore_probe_%d", time.Now().UnixNano())
	marker := fmt.Sprintf("marker-%d", time.Now().UnixNano())
	if err := db.Exec(fmt.Sprintf(`CREATE TABLE %s (id serial primary key, marker text not null)`, probe)).Error; err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, probe)).Error
	})
	if err := db.Exec(fmt.Sprintf(`INSERT INTO %s (marker) VALUES ('%s')`, probe, marker)).Error; err != nil {
		t.Fatalf("insert probe row: %v", err)
	}

	// 2. Run the real backup job: real pg_dump, gzip, atomic rename — the
	// exact artifacts the runbook documents.
	dir := t.TempDir()
	job := NewBackupJob(dsn, dir, 7, "pg_dump", "")
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("backup job run: %v", err)
	}

	dumps, err := filepath.Glob(filepath.Join(dir, backupFilePrefix+"*"+backupFileSuffix))
	if err != nil || len(dumps) != 1 {
		t.Fatalf("expected exactly one dump in %s, found %v (err=%v)", dir, dumps, err)
	}
	dumpPath := dumps[0]

	// 3. Dump file structure validation: gzip magic, the pg_dump plain-format
	// header, and the probe table's CREATE statement must all be present.
	raw, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Fatalf("dump %s is not gzip-compressed (runbook step 3 expects gunzip -c)", dumpPath)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("open gzip stream: %v", err)
	}
	var sqlDump bytes.Buffer
	if _, err := sqlDump.ReadFrom(zr); err != nil {
		t.Fatalf("decompress dump: %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("close gzip stream: %v", err)
	}
	sqlText := sqlDump.String()
	if !strings.Contains(sqlText, "PostgreSQL database dump") {
		t.Fatal("dump is missing the pg_dump plain-format header; it is not a plain-format dump")
	}
	// pg_dump schema-qualifies identifiers ("CREATE TABLE public.<name>").
	if !strings.Contains(sqlText, "CREATE TABLE") || !strings.Contains(sqlText, probe) {
		t.Fatalf("dump does not contain the CREATE TABLE for %s — the seeded schema was not captured", probe)
	}

	// 4. Restore runbook step 3: recreate and restore into a SCRATCH database
	// (never the source), piping the dump through psql with ON_ERROR_STOP so
	// a partially applicable dump fails loudly instead of "succeeding".
	scratch := fmt.Sprintf("posta_restore_scratch_%d", time.Now().UnixNano())
	if err := db.Exec(fmt.Sprintf(`CREATE DATABASE %s`, scratch)).Error; err != nil {
		t.Fatalf("create scratch database %s: %v", scratch, err)
	}
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, scratch)).Error
	})

	scratchSQL := filepath.Join(t.TempDir(), "restore.sql")
	if err := os.WriteFile(scratchSQL, sqlDump.Bytes(), 0o600); err != nil {
		t.Fatalf("write restore script: %v", err)
	}
	scratchDSN := scratchDSNFor(dsn, scratch)
	restore := exec.Command("psql", scratchDSN, "-v", "ON_ERROR_STOP=1", "-f", scratchSQL)
	var restoreErr strings.Builder
	restore.Stderr = &restoreErr
	if err := restore.Run(); err != nil {
		t.Fatalf("psql restore into %s failed: %v\nstderr: %s", scratch, err, restoreErr.String())
	}

	// 5. The proof: the marker row must exist in the restored scratch
	// database — the runbook's "verify with a dashboard login" step,
	// mechanized.
	restored := mustOpenRestoreDB(t, scratchDSN)
	var got string
	if err := restored.Raw(fmt.Sprintf(`SELECT marker FROM %s LIMIT 1`, probe)).Scan(&got).Error; err != nil {
		t.Fatalf("probe row not found in restored database: %v", err)
	}
	if got != marker {
		t.Fatalf("restored marker = %q, want %q", got, marker)
	}
}

// scratchDSNFor rewrites a DSN's database name to the scratch database,
// handling key=value ("… dbname=posta …"), key=value-at-end
// ("… dbname=posta"), and URL ("postgres://…/posta") forms.
func scratchDSNFor(dsn, scratch string) string {
	if strings.Contains(dsn, "dbname=posta ") {
		return strings.Replace(dsn, "dbname=posta ", "dbname="+scratch+" ", 1)
	}
	if strings.HasSuffix(dsn, "dbname=posta") {
		return dsn[:len(dsn)-len("posta")] + scratch
	}
	// URL form: replace the path segment after the last slash.
	i := strings.LastIndex(dsn, "/")
	if i < 0 {
		return dsn + "/" + scratch
	}
	return dsn[:i+1] + scratch
}

// mustOpenRestoreDB connects and FAILS (never skips) — after the restore has
// run, a connect error is a verification failure, not missing tooling.
func mustOpenRestoreDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("connect to restored database: %v", err)
	}
	return db
}

// appendDBNameToURL rewrites the database name of a URL-form DSN
// (postgres://user:pass@host:port/db) to the scratch database.
func appendDBNameToURL(dsn, scratch string) string {
	i := strings.LastIndex(dsn, "/")
	if i < 0 {
		return dsn + "/" + scratch
	}
	return dsn[:i+1] + scratch
}
