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

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDumper produces a deterministic dump without a postgres client.
type fakeDumper struct {
	body string
	err  error
}

func (f *fakeDumper) Dump(_ context.Context, w io.Writer) error {
	if f.err != nil {
		return f.err
	}
	_, err := io.WriteString(w, f.body)
	return err
}

func newBackupTestJob(t *testing.T, dumper BackupDumper) (*BackupJob, string) {
	t.Helper()
	dir := t.TempDir()
	job := NewBackupJob("host=localhost user=posta password=posta dbname=posta", dir, 14, "pg_dump", "0 3 * * *")
	job.SetDumper(dumper)
	job.now = func() time.Time { return time.Date(2026, 3, 15, 3, 0, 0, 0, time.UTC) }
	return job, dir
}

func TestBackupJobWritesGzipDump(t *testing.T) {
	dumper := &fakeDumper{body: "--\n-- PostgreSQL database dump\nCREATE TABLE probe(id int);\n"}
	job, dir := newBackupTestJob(t, dumper)

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	name := backupFilePrefix + "20260315-030000" + backupFileSuffix
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("dump file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("dump file mode = %v, want 0600 (the dump carries personal data)", info.Mode().Perm())
	}

	// The staging file must be gone and the content must gunzip back intact.
	if _, err := os.Stat(path + backupPartSuffix); !os.IsNotExist(err) {
		t.Error("staging .part file survived a successful dump")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open dump: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("dump is not valid gzip: %v", err)
	}
	var got bytes.Buffer
	if _, err := io.Copy(&got, gz); err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if got.String() != dumper.body {
		t.Errorf("round-trip mismatch: got %q", got.String())
	}
}

func TestBackupJobDumpFailureLeavesNoFile(t *testing.T) {
	job, dir := newBackupTestJob(t, &fakeDumper{err: context.DeadlineExceeded})

	if err := job.Run(context.Background(), nil); err == nil {
		t.Fatal("Run must report a failed dump")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("failed dump left %d files behind, want 0", len(entries))
	}
}

// An empty dump is worse than no dump (it would "restore" an empty database),
// so it is refused.
func TestBackupJobRefusesEmptyDump(t *testing.T) {
	job, dir := newBackupTestJob(t, &fakeDumper{body: ""})
	if err := job.Run(context.Background(), nil); err == nil {
		t.Fatal("Run must refuse an empty dump")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("empty dump left %d files behind, want 0", len(entries))
	}
}

// Retention removes only the job's own dumps, only the stale ones, and keeps
// the dump that was just taken.
func TestBackupJobRetentionPrunesOnlyOwnStaleDumps(t *testing.T) {
	dumper := &fakeDumper{body: "CREATE TABLE probe(id int);"}
	job, dir := newBackupTestJob(t, dumper)

	now := job.now()
	older := func(name string, age time.Duration) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		past := now.Add(-age)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatalf("set mtime on %s: %v", name, err)
		}
	}

	older(backupFilePrefix+"20260101-030000"+backupFileSuffix, 30*24*time.Hour) // own + stale: pruned
	older(backupFilePrefix+"20260310-030000"+backupFileSuffix, 2*24*time.Hour)  // own + fresh: kept
	older(backupFilePrefix+"20260101-030000"+backupPartSuffix, 30*24*time.Hour) // staging leftover: not ours to touch
	older("foreign-backup-20260101.sql.gz", 30*24*time.Hour)                    // wrong prefix: never touched

	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, name := range []string{
		backupFilePrefix + "20260101-030000" + backupFileSuffix,
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been pruned", name)
		}
	}
	for _, name := range []string{
		backupFilePrefix + "20260310-030000" + backupFileSuffix,
		backupFilePrefix + "20260101-030000" + backupPartSuffix,
		"foreign-backup-20260101.sql.gz",
		backupFilePrefix + "20260315-030000" + backupFileSuffix,
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should have survived retention: %v", name, err)
		}
	}
}

// The pg_dump error path must point the operator at the missing binary instead
// of failing with an opaque exit status.
func TestBackupJobMissingPgDumpMessage(t *testing.T) {
	job, _ := newBackupTestJob(t, nil)
	job.dumper = nil // use the real pg_dump dumper
	job.pgDumpBin = "definitely-not-pg-dump-" + strings.ToLower(t.Name())

	err := (&pgDumpDumper{bin: job.pgDumpBin, dsn: "host=localhost"}).Dump(context.Background(), io.Discard)
	if err == nil {
		t.Fatal("expected an error for a missing pg_dump binary")
	}
	if !strings.Contains(err.Error(), "postgresql-client") {
		t.Errorf("error %q should tell the operator how to fix it", err)
	}
}
