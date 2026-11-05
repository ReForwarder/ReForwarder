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

// BackupJob (R-11): scheduled database backups with retention.
//
// Posta stores every message, subscriber, suppression, and automation in
// Postgres; without backups an operator has no disaster story. This job shells
// out to pg_dump (plain format), gzips the stream, and writes it atomically to
// the backup directory, then prunes dumps older than the retention window.
//
// Enable with POSTA_BACKUP_ENABLED=true. The cron expression, target directory,
// retention window, and pg_dump binary are POSTA_BACKUP_SCHEDULE,
// POSTA_BACKUP_DIR, POSTA_BACKUP_RETENTION_DAYS, and POSTA_BACKUP_PG_DUMP_BIN.
//
// The runtime image does not ship the postgres client: an operator either
// mounts it (package postgresql-client) or runs a dedicated sidecar with the
// same environment that points POSTA_BACKUP_PG_DUMP_BIN at its own binary. When
// the binary is missing the job fails loudly on every tick instead of failing
// silently forever.
//
// Each dump is written to a temporary ".part" file and renamed on success, so a
// crash mid-dump never leaves a truncated file looking like a good backup; only
// fully written dumps are ever rotated in.
//
// Restore runbook
//
// The dumps are plain-format SQL compressed with gzip:
//
//	# 1. Stop writers (scale the API/worker down, or pause sending) so the
//	#    restored database does not take new mail during the restore.
//	# 2. Inspect what you are about to run — a dump is a script of SQL.
//	gunzip -c /var/lib/posta/backups/posta-backup-20260315-030000.sql.gz | less
//	# 3. Recreate and restore (the plain dump recreates every table).
//	dropdb -h <host> -U posta --if-exists posta
//	createdb -h <host> -U posta -O posta posta
//	gunzip -c posta-backup-20260315-030000.sql.gz | psql -h <host> -U posta -d posta
//	# 4. Restart the app; verify with a dashboard login and a queue check.
//	# 5. Re-enable writers.
//
// Blob storage (attachment payloads, stylesheets) is NOT part of the dump: back
// up the S3 bucket or the filesystem volume (POSTA_BLOB_FS_PATH) separately,
// restoring it to the same path prefix. Redis holds only queue state, which
// self-heals (the queue-reconcile cron re-enqueues lost tasks).
//
// Tenant-level backups ride the existing workspace export; this job covers the
// whole-database story only.

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

// backupFilePrefix / backupFileSuffix define the file pattern the retention
// sweep owns. Anything else in the directory is never touched.
const (
	backupFilePrefix = "posta-backup-"
	backupFileSuffix = ".sql.gz"
	backupPartSuffix = ".part"
	backupTimeLayout = "20060102-150405"
)

// BackupDumper produces the raw pg_dump stream. It is an interface so tests can
// inject a fake instead of a real postgres client.
type BackupDumper interface {
	// Dump writes the plain-format SQL dump to w. It must return an error when
	// the dump did not complete.
	Dump(ctx context.Context, w io.Writer) error
}

type BackupJob struct {
	dsn           string
	dir           string
	retentionDays int
	pgDumpBin     string
	schedule      string
	dumper        BackupDumper // nil: use the pg_dump implementation
	now           func() time.Time
}

// NewBackupJob builds the backup job from config (R-11).
func NewBackupJob(dsn, dir string, retentionDays int, pgDumpBin, schedule string) *BackupJob {
	if retentionDays <= 0 {
		retentionDays = 14
	}
	if dir == "" {
		dir = "backups"
	}
	if pgDumpBin == "" {
		pgDumpBin = "pg_dump"
	}
	if schedule == "" {
		schedule = "0 3 * * *"
	}
	return &BackupJob{
		dsn:           dsn,
		dir:           dir,
		retentionDays: retentionDays,
		pgDumpBin:     pgDumpBin,
		schedule:      schedule,
		now:           time.Now,
	}
}

// SetDumper overrides the dump source (tests).
func (j *BackupJob) SetDumper(d BackupDumper) { j.dumper = d }

func (j *BackupJob) Name() string     { return "backup" }
func (j *BackupJob) Schedule() string { return j.schedule }

// Run performs one backup and then the retention sweep. A dump failure aborts
// the run — pruning must never delete history because today's dump failed.
func (j *BackupJob) Run(ctx context.Context, _ *asynq.Client) error {
	path, bytes, err := j.runDump(ctx)
	if err != nil {
		return err
	}
	logger.Info("backup: dump written", "path", path, "bytes", bytes)

	removed := j.prune()
	if removed > 0 {
		logger.Info("backup: retention pruned old dumps", "count", removed, "retention_days", j.retentionDays)
	}
	return nil
}

// runDump streams the dump into <dir>/posta-backup-<ts>.sql.gz via a .part
// staging file and returns the final path and compressed size.
func (j *BackupJob) runDump(ctx context.Context) (string, int64, error) {
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return "", 0, fmt.Errorf("backup: cannot create backup dir %s: %w", j.dir, err)
	}
	name := backupFilePrefix + j.now().Format(backupTimeLayout) + backupFileSuffix
	final := filepath.Join(j.dir, name)
	part := final + backupPartSuffix

	dumper := j.dumper
	if dumper == nil {
		dumper = &pgDumpDumper{bin: j.pgDumpBin, dsn: j.dsn}
	}

	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("backup: cannot open staging file: %w", err)
	}

	// counting sits between the dumper and the compressor and counts the
	// uncompressed SQL bytes: gzip always writes its own header/footer through
	// to the file, so a file size above zero proves nothing about the dump.
	gz := gzip.NewWriter(f)
	counting := &countingWriter{w: gz}
	dumpErr := dumper.Dump(ctx, counting)
	closeErr := errors.Join(gz.Close(), f.Close())
	if dumpErr != nil || closeErr != nil {
		_ = os.Remove(part) // never leave a truncated staging file behind
		if dumpErr != nil {
			return "", 0, fmt.Errorf("backup: dump failed: %w", dumpErr)
		}
		return "", 0, fmt.Errorf("backup: closing dump failed: %w", closeErr)
	}

	if counting.n == 0 {
		// An empty dump is worse than no dump: restoring one would silently
		// wipe the database.
		_ = os.Remove(part)
		return "", 0, fmt.Errorf("backup: dump is empty; refusing to keep it")
	}

	info, err := os.Stat(part)
	if err != nil {
		_ = os.Remove(part)
		return "", 0, fmt.Errorf("backup: staging file vanished: %w", err)
	}

	// Atomic on the same filesystem: readers never observe a half-written dump.
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return "", 0, fmt.Errorf("backup: cannot finalize dump: %w", err)
	}
	return final, info.Size(), nil
}

// prune deletes dumps older than the retention window, oldest first. Only files
// matching the job's own naming pattern are candidates.
func (j *BackupJob) prune() int {
	cutoff := j.now().Add(-time.Duration(j.retentionDays) * 24 * time.Hour)
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		logger.Warn("backup: cannot read backup dir for retention", "dir", j.dir, "error", err)
		return 0
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var old []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), backupFilePrefix) || !strings.HasSuffix(e.Name(), backupFileSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			old = append(old, candidate{filepath.Join(j.dir, e.Name()), info.ModTime()})
		}
	}
	sort.Slice(old, func(a, b int) bool { return old[a].mod.Before(old[b].mod) })
	removed := 0
	for _, c := range old {
		if err := os.Remove(c.path); err != nil {
			logger.Warn("backup: retention could not remove old dump", "path", c.path, "error", err)
			continue
		}
		removed++
	}
	return removed
}

// countingWriter counts the bytes that flowed through it (the uncompressed
// dump size).
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// pgDumpDumper runs the pg_dump binary: plain format, no ownership statements,
// data and schema together. The connection string is passed as an argument; for
// key=value DSNs pg_dump honors it directly, for URLs likewise.
type pgDumpDumper struct {
	bin string
	dsn string
}

func (d *pgDumpDumper) Dump(ctx context.Context, w io.Writer) error {
	cmd := exec.CommandContext(ctx, d.bin,
		"--no-owner",
		"--no-privileges",
		"--format=plain",
		d.dsn,
	)
	// The DSN may carry the credentials; keep them out of the child's stdout
	// mixing and let pg_dump report its own errors on stderr.
	var stderr strings.Builder
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if _, lookErr := exec.LookPath(d.bin); lookErr != nil {
			return fmt.Errorf("pg_dump (%s) not found: install postgresql-client in the container or set POSTA_BACKUP_PG_DUMP_BIN: %s", d.bin, msg)
		}
		return fmt.Errorf("pg_dump failed: %s", msg)
	}
	return nil
}

// Compile-time check that BackupJob satisfies the cron.CronJob contract.
var _ interface {
	Name() string
	Schedule() string
	Run(ctx context.Context, client *asynq.Client) error
} = (*BackupJob)(nil)
