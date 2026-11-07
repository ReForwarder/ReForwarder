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

// Package logging implements the multi-sink log layer (SF-94).
//
// The structured logger (github.com/jkaninda/logger) writes its records to
// os.Stdout — its handler captures os.Stdout when it is constructed, and
// every call site in this codebase uses the package-level logger.Info/Warn/
// Error functions, which resolve to that same handler. This package adds
// sinks without restructuring any call site:
//
//	POSTA_LOG_SINKS  comma-separated list of "stdout" and/or "file"
//	                 (default "stdout" — today's behavior).
//	POSTA_LOG_FILE   path of the file sink; an empty value disables the file
//	                 sink even when it is selected (operator misconfiguration
//	                 degrades to stdout, it does not crash the server).
//
// When the file sink is enabled, os.Stdout is replaced by a tee that mirrors
// every stdout write into a rotating file. Because the jkaninda handler
// resolves os.Stdout at construction time, Init MUST run before the okapi
// config initializer builds the logger (in cmd/posta, before
// cfg.Initialize(app)). After the swap everything written to stdout —
// structured records, and the occasional direct fmt write — lands in both
// the terminal and the file.
//
// Rotation is deliberately simple and documented here rather than magical:
// the active file rotates when a write would push it past
// DefaultMaxSizeBytes (10 MiB), shifted into numbered backups
// <path>.1 … <path>.<maxRotateBackups> (oldest deleted), for a maximum of
// five files on disk (active + four backups). The tee is pumped by a
// background goroutine (see fileSink), so on a hard crash at most one pipe
// buffer (~64 KiB) of the very latest output may be lost; graceful shutdown
// drains the pipe via Close. os.Stderr is not teed — fatal startup errors
// stay on the console only.
package logging

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// SinkStdout is the console sink (the historical default).
	SinkStdout = "stdout"
	// SinkFile selects the rotating file sink (needs POSTA_LOG_FILE).
	SinkFile = "file"

	// DefaultMaxSizeBytes is the active file size that triggers rotation
	// (10 MiB), matching the size the SF-94 task specified.
	DefaultMaxSizeBytes int64 = 10 << 20
	// maxRotateBackups is how many rotated (<path>.N) copies are kept around
	// the active file — five files total on disk.
	maxRotateBackups = 4
)

// Config is the parsed sink configuration.
type Config struct {
	// Sinks is the de-duplicated, order-preserving sink list.
	Sinks []string
	// File is the POSTA_LOG_FILE path ("" = file sink disabled).
	File string
}

// FileEnabled reports whether the file sink is active: selected in
// POSTA_LOG_SINKS AND given a path.
func (c Config) FileEnabled() bool {
	for _, s := range c.Sinks {
		if s == SinkFile && c.File != "" {
			return true
		}
	}
	return false
}

// ParseConfig turns the raw environment values into a Config. Unknown sink
// names are an error — a typo must not silently drop logs.
func ParseConfig(sinksCSV, file string) (Config, error) {
	cfg := Config{File: strings.TrimSpace(file)}
	if strings.TrimSpace(sinksCSV) == "" {
		cfg.Sinks = []string{SinkStdout}
		return cfg, nil
	}
	seen := map[string]bool{}
	for _, raw := range strings.Split(sinksCSV, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue // tolerate a trailing comma
		}
		if name != SinkStdout && name != SinkFile {
			return Config{}, fmt.Errorf("unknown log sink %q (supported: %s, %s)", name, SinkStdout, SinkFile)
		}
		if !seen[name] {
			seen[name] = true
			cfg.Sinks = append(cfg.Sinks, name)
		}
	}
	if len(cfg.Sinks) == 0 {
		cfg.Sinks = []string{SinkStdout}
	}
	return cfg, nil
}

// Init applies the sink configuration to the process and returns a closer
// that releases the file sink (and restores the original stdout). When the
// file sink is not enabled, the returned closer is a no-op and stdout is
// untouched. A misconfigured file sink ("file" selected without a path) is
// reported on stdout and degrades to stdout-only — the server still starts.
func Init() (io.Closer, error) {
	cfg, err := ParseConfig(os.Getenv("POSTA_LOG_SINKS"), os.Getenv("POSTA_LOG_FILE"))
	if err != nil {
		return nil, err
	}
	if !cfg.FileEnabled() {
		if !cfg.FileSetButDisabled() {
			return noopCloser{}, nil
		}
		// "file" selected but no POSTA_LOG_FILE: degrade to stdout, loudly.
		fmt.Println("logging: POSTA_LOG_SINKS selects the file sink but POSTA_LOG_FILE is empty — file sink disabled, logging to stdout only")
		return noopCloser{}, nil
	}
	closer, err := installFileSink(cfg.File, DefaultMaxSizeBytes, maxRotateBackups)
	if err != nil {
		return nil, err
	}
	// This line travels through the fresh tee, so it is the first record in
	// the log file too — useful proof for operators that the sink is live.
	fmt.Println("logging: file sink enabled, mirroring stdout to", cfg.File)
	return closer, nil
}

// FileSetButDisabled reports the "file" selected with an empty path case.
func (c Config) FileSetButDisabled() bool {
	for _, s := range c.Sinks {
		if s == SinkFile && c.File == "" {
			return true
		}
	}
	return false
}

// installFileSink opens the rotating file and swaps os.Stdout for a tee over
// the current stdout and the file. os.Stdout is typed *os.File, so the tee is
// plumbed through a pipe: stdout becomes the pipe's write end and a pump
// goroutine mirrors each chunk into the console and the rotating file. It
// returns a closer that closes the file and restores the original stdout.
func installFileSink(path string, maxBytes int64, maxBackups int) (io.Closer, error) {
	rw, err := newRotatingFile(path, maxBytes, maxBackups)
	if err != nil {
		return nil, fmt.Errorf("logging: cannot open file sink %s: %w", path, err)
	}
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = rw.Close()
		return nil, fmt.Errorf("logging: cannot create the stdout tee pipe: %w", err)
	}
	sink := &fileSink{
		original: original,
		reader:   reader,
		writer:   writer,
		rotating: rw,
		done:     make(chan struct{}),
	}
	os.Stdout = writer
	go sink.pump()
	return sink, nil
}

// fileSink owns the tee plumbing: os.Stdout is replaced by the write end of
// a pipe, and a pump goroutine forwards everything from the read end into
// both the original stdout and the rotating file. Close restores the original
// stdout, drains the pipe and releases the file, so a graceful shutdown (or a
// test) leaves the process as it found it.
type fileSink struct {
	original *os.File // the real console stdout
	reader   *os.File // pipe read end (pumped)
	writer   *os.File // pipe write end — this WAS os.Stdout
	rotating *rotatingFile
	done     chan struct{}
}

func (f *fileSink) Close() error {
	if f.writer == nil {
		return nil
	}
	// Restore first so later writes go straight to the console again.
	os.Stdout = f.original
	// Closing the write end EOFs the pump, which drains what is buffered and
	// signals done.
	_ = f.writer.Close()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		// Pump stuck on a slow sink — release the handles anyway; at most the
		// pipe buffer is lost.
		_ = f.reader.Close()
	}
	return f.rotating.Close()
}

// pump forwards pipe output into both sinks. File-sink errors never kill the
// tee: the console keeps receiving records and a one-line warning is printed
// once, so a full disk cannot wedge the logger or silently hide it.
func (f *fileSink) pump() {
	defer close(f.done)
	buf := make([]byte, 32*1024)
	warned := false
	for {
		n, err := f.reader.Read(buf)
		if n > 0 {
			_, _ = f.original.Write(buf[:n])
			if _, err := f.rotating.Write(buf[:n]); err != nil && !warned {
				warned = true
				fmt.Fprintf(f.original, "logging: file sink write failed (%v); continuing on stdout only\n", err)
			}
		}
		if err != nil {
			return // EOF (closed writer) or unrecoverable read error
		}
	}
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// rotatingFile is a size-capped append-only writer. See the package doc for
// the rotation policy (10 MiB, numbered backups, oldest deleted).
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

func newRotatingFile(path string, maxBytes int64, backups int) (*rotatingFile, error) {
	if path == "" {
		return nil, fmt.Errorf("logging: file sink path is empty")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSizeBytes
	}
	if backups < 0 {
		backups = 0
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingFile{
		path:     path,
		maxBytes: maxBytes,
		backups:  backups,
		file:     f,
		size:     info.Size(),
	}, nil
}

// Write appends p, rotating first when the entry would overflow the active
// file. An entry larger than maxBytes still lands whole in a fresh file
// (rotation never splits or drops a record).
func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, fmt.Errorf("logging: file sink closed")
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotateLocked shifts <path>.N backups one slot down (deleting the oldest),
// renames the active file to <path>.1 and opens a fresh active file.
func (w *rotatingFile) rotateLocked() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	for i := w.backups; i >= 1; i-- {
		from := backupPath(w.path, i)
		if i == 1 {
			// The active file becomes the first backup.
			if err := os.Rename(w.path, from); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		to := backupPath(w.path, i)
		from = backupPath(w.path, i-1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	return nil
}

// backupPath is the numbered backup path for slot i (1-based, 1 = newest).
func backupPath(path string, i int) string {
	return fmt.Sprintf("%s.%d", path, i)
}

func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
