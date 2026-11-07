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

package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSinks pins the POSTA_LOG_SINKS grammar: default stdout, comma
// lists, case/whitespace tolerance, dedup, unknown-token errors, and the
// "file selected without a path degrades to stdout" rule.
func TestParseSinks(t *testing.T) {
	cases := []struct {
		name      string
		sinksCSV  string
		file      string
		wantSinks []string
		wantFile  bool
		wantErr   bool
	}{
		{name: "default is stdout", sinksCSV: "", file: "", wantSinks: []string{"stdout"}},
		{name: "explicit stdout", sinksCSV: "stdout", file: "", wantSinks: []string{"stdout"}},
		{name: "both sinks", sinksCSV: "stdout,file", file: "/var/log/posta.log", wantSinks: []string{"stdout", "file"}, wantFile: true},
		{name: "case and whitespace tolerated", sinksCSV: " Stdout , FILE ", file: "/var/log/posta.log", wantSinks: []string{"stdout", "file"}, wantFile: true},
		{name: "duplicates removed", sinksCSV: "stdout,stdout,file", file: "/var/log/posta.log", wantSinks: []string{"stdout", "file"}, wantFile: true},
		{name: "trailing comma tolerated", sinksCSV: "stdout,", file: "", wantSinks: []string{"stdout"}},
		{name: "file without path is disabled", sinksCSV: "stdout,file", file: "", wantSinks: []string{"stdout", "file"}, wantFile: false},
		{name: "path without sink selection disables file", sinksCSV: "stdout", file: "/var/log/posta.log", wantSinks: []string{"stdout"}, wantFile: false},
		{name: "unknown sink is an error", sinksCSV: "stdout,syslog", file: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig(tc.sinksCSV, tc.file)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.sinksCSV)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(cfg.Sinks, ",") != strings.Join(tc.wantSinks, ",") {
				t.Errorf("sinks = %v, want %v", cfg.Sinks, tc.wantSinks)
			}
			if cfg.FileEnabled() != tc.wantFile {
				t.Errorf("FileEnabled() = %v, want %v", cfg.FileEnabled(), tc.wantFile)
			}
		})
	}
}

// TestRotatingWriterWritesAndRotates pins the rotation behavior: entries land
// in the active file until the size cap, the overflow shifts into <path>.1,
// and the active file restarts empty.
func TestRotatingWriterWritesAndRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "posta.log")

	rw, err := newRotatingFile(path, 100, 4)
	if err != nil {
		t.Fatalf("open rotating file: %v", err)
	}
	defer rw.Close()

	if _, err := rw.Write([]byte("first-entry\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	assertFileContent(t, path, "first-entry\n")

	// This entry overflows the 100-byte cap: the current content rotates to
	// .1 and the active file holds only the new entry.
	big := strings.Repeat("x", 95) + "\n"
	if _, err := rw.Write([]byte(big)); err != nil {
		t.Fatalf("write overflow: %v", err)
	}
	assertFileContent(t, path, big)
	assertFileContent(t, backupPath(path, 1), "first-entry\n")
}

// TestRotatingWriterCapsFileCount pins the retention side: after enough
// rotations only the active file plus the configured number of backups remain.
func TestRotatingWriterCapsFileCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "posta.log")

	rw, err := newRotatingFile(path, 10, 2) // active + 2 backups
	if err != nil {
		t.Fatalf("open rotating file: %v", err)
	}
	defer rw.Close()

	// Each entry overflows the 10-byte cap → a rotation per write.
	for i := 0; i < 6; i++ {
		if _, err := rw.Write([]byte(strings.Repeat("y", 20))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	// 3 files total: active + .1 + .2 (backups=2). The oldest backups were
	// shifted away, not accumulated.
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 3 files after rotation, got %d: %v", len(entries), names)
	}
	// .2 holds the content written three entries ago; the active file holds
	// only the last entry.
	if _, err := os.Stat(backupPath(path, 3)); err == nil {
		t.Errorf("backup slot 3 must not exist (backups=2)")
	}
}

// TestStdoutTeeWritesToFile is the required tee assertion: while the file
// sink is installed, everything written to os.Stdout lands in the file (and
// Close restores the original stdout).
func TestStdoutTeeWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "posta.log")

	original := os.Stdout
	closer, err := installFileSink(path, 10<<20, 4)
	if err != nil {
		t.Fatalf("install file sink: %v", err)
	}

	// What the structured logger does under the hood: a plain stdout write.
	line := "time=2026-01-01 level=INFO msg=\"hello sink\"\n"
	if _, err := os.Stdout.Write([]byte(line)); err != nil {
		t.Fatalf("stdout write: %v", err)
	}

	if err := closer.Close(); err != nil {
		t.Fatalf("close file sink: %v", err)
	}
	if os.Stdout != original {
		t.Fatalf("Close must restore the original stdout")
	}

	assertFileContent(t, path, line)
}

// TestInitWithEnvTeesToFile runs the full env-driven Init path via t.Setenv:
// POSTA_LOG_SINKS=stdout,file + POSTA_LOG_FILE tees stdout into the file.
func TestInitWithEnvTeesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "posta.log")

	t.Setenv("POSTA_LOG_SINKS", "stdout,file")
	t.Setenv("POSTA_LOG_FILE", path)

	closer, err := Init()
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	line := "time=2026-01-01 level=INFO msg=\"env tee\"\n"
	if _, err := os.Stdout.Write([]byte(line)); err != nil {
		t.Fatalf("stdout write: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	assertFileContent(t, path, line)
}

// TestInitDisabledByDefault pins the default: with no env configured the
// closer is a no-op and stdout is untouched (the historical behavior).
func TestInitDisabledByDefault(t *testing.T) {
	t.Setenv("POSTA_LOG_SINKS", "")
	t.Setenv("POSTA_LOG_FILE", "")

	original := os.Stdout
	closer, err := Init()
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, ok := closer.(noopCloser); !ok {
		t.Errorf("expected a no-op closer, got %T", closer)
	}
	if os.Stdout != original {
		t.Errorf("stdout must be untouched when no file sink is configured")
	}
}

// TestInitBadSinkNameIsRefused: a typo must not silently drop logs.
func TestInitBadSinkNameIsRefused(t *testing.T) {
	t.Setenv("POSTA_LOG_SINKS", "stdout,journald")
	if closer, err := Init(); err == nil {
		closer.Close()
		t.Errorf("unknown sink must be an error")
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Errorf("file %s content = %q, want it to contain %q", path, data, want)
	}
}
