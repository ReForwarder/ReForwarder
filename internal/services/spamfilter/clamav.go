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

package spamfilter

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ClamAV via the clamd TCP protocol (GAP-47, ADR-006 decision 2): no bundled
// engine, no signature database in this codebase — clamd is the boundary.
// The client speaks the INSTREAM command: zINSTREAM\0, then the sample as
// length-prefixed chunks (4-byte big-endian length + bytes), terminated by a
// zero-length chunk, then one response line ending in NUL:
//
//	stream: OK
//	stream: Eicar-Signature FOUND
//
// Anything else (or a transport failure) is an error, and the CALLER decides
// via the fail mode what an unavailable scanner means — this client never
// guesses. Scans run in the ingest worker, never in the SMTP session (the
// durable-path guarantee makes that safe; ADR-006's performance note).

// ClamFailMode decides what a clamd transport failure means for the message.
type ClamFailMode string

const (
	// ClamFailOpen treats an unavailable scanner as "not scanned — continue".
	// Default: a malware outage must not stop the mail flow.
	ClamFailOpen ClamFailMode = "open"
	// ClamFailClosed treats an unavailable scanner as "unscannable —
	// quarantine": nothing unscanned enters the pipeline.
	ClamFailClosed ClamFailMode = "closed"
)

// ClamFailModeFromEnv normalizes the POSTA_CLAMAV_FAIL_MODE value; anything
// unlisted falls back to open (documented default).
func ClamFailModeFromEnv(v string) ClamFailMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "closed":
		return ClamFailClosed
	default:
		return ClamFailOpen
	}
}

// ClamdConfig configures one ClamdClient.
type ClamdConfig struct {
	Host     string        // empty → scanning disabled
	Port     int           // default 3310
	Timeout  time.Duration // per scan; 0 → 30s
	FailMode ClamFailMode  // open (default) | closed
	// MaxStreamBytes mirrors clamd's StreamMaxLength default (25 MiB).
	// Samples above it are skipped (reported as Skipped), not sent — clamd
	// would reject the stream anyway.
	MaxStreamBytes int64
}

const (
	defaultClamdPort      = 3310
	defaultClamdTimeout   = 30 * time.Second
	defaultClamdMaxStream = int64(25 * 1024 * 1024)
	clamdStreamChunk      = 4096
	clamdInstreamCommand  = "zINSTREAM\x00"
)

// ScanResult is one attachment's outcome.
type ScanResult struct {
	Infected  bool
	Signature string // clamd's reported signature when Infected
	Skipped   bool   // sample above MaxStreamBytes was not scanned
}

// clamdDialer is the transport seam (tests serve an in-process fake clamd).
type clamdDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// netDialer adapts net.Dialer.
type netDialer struct{ d *net.Dialer }

func (n netDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return n.d.DialContext(ctx, network, addr)
}

// ClamdClient scans samples against one clamd endpoint.
type ClamdClient struct {
	cfg    ClamdConfig
	dialer clamdDialer
}

// NewClamdClient builds a client; a nil dialer selects net.Dialer.
func NewClamdClient(cfg ClamdConfig) *ClamdClient {
	if cfg.Port == 0 {
		cfg.Port = defaultClamdPort
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultClamdTimeout
	}
	if cfg.MaxStreamBytes == 0 {
		cfg.MaxStreamBytes = defaultClamdMaxStream
	}
	if cfg.FailMode == "" {
		cfg.FailMode = ClamFailOpen
	}
	return &ClamdClient{cfg: cfg, dialer: netDialer{d: &net.Dialer{}}}
}

// Enabled reports whether a clamd endpoint is configured.
func (c *ClamdClient) Enabled() bool { return c != nil && c.cfg.Host != "" }

// FailMode exposes the configured failure policy.
func (c *ClamdClient) FailMode() ClamFailMode {
	if c == nil || c.cfg.FailMode == "" {
		return ClamFailOpen
	}
	return c.cfg.FailMode
}

// Scan sends one sample through INSTREAM. The returned error covers every
// transport/protocol failure (unreachable clamd, timeout, malformed
// response, size-limit rejection); Infected/Signature carry the verdict.
func (c *ClamdClient) Scan(ctx context.Context, sample []byte) (ScanResult, error) {
	if !c.Enabled() {
		return ScanResult{}, errors.New("clamav: no clamd endpoint configured")
	}
	if int64(len(sample)) > c.cfg.MaxStreamBytes {
		return ScanResult{Skipped: true}, nil
	}

	dctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	conn, err := c.dialer.DialContext(dctx, "tcp", net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port)))
	if err != nil {
		return ScanResult{}, fmt.Errorf("clamav: connect: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Deadline covers the whole exchange (send + read).
	_ = conn.SetDeadline(time.Now().Add(c.cfg.Timeout))

	if _, err := io.WriteString(conn, clamdInstreamCommand); err != nil {
		return ScanResult{}, fmt.Errorf("clamav: send command: %w", err)
	}
	var lenBuf [4]byte
	for start := 0; start < len(sample); start += clamdStreamChunk {
		end := start + clamdStreamChunk
		if end > len(sample) {
			end = len(sample)
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(end-start))
		if _, err := conn.Write(lenBuf[:]); err != nil {
			return ScanResult{}, fmt.Errorf("clamav: send chunk length: %w", err)
		}
		if _, err := conn.Write(sample[start:end]); err != nil {
			return ScanResult{}, fmt.Errorf("clamav: send chunk: %w", err)
		}
	}
	// Zero-length chunk terminates the stream.
	binary.BigEndian.PutUint32(lenBuf[:], 0)
	if _, err := conn.Write(lenBuf[:]); err != nil {
		return ScanResult{}, fmt.Errorf("clamav: terminate stream: %w", err)
	}

	resp, err := readClamdResponse(conn)
	if err != nil {
		return ScanResult{}, err
	}
	return parseClamdResponse(resp)
}

// Ping checks the endpoint with clamd's PING → PONG handshake.
func (c *ClamdClient) Ping(ctx context.Context) error {
	if !c.Enabled() {
		return errors.New("clamav: no clamd endpoint configured")
	}
	dctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	conn, err := c.dialer.DialContext(dctx, "tcp", net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port)))
	if err != nil {
		return fmt.Errorf("clamav: connect: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(c.cfg.Timeout))
	if _, err := io.WriteString(conn, "zPING\x00"); err != nil {
		return fmt.Errorf("clamav: send ping: %w", err)
	}
	resp, err := readClamdResponse(conn)
	if err != nil {
		return err
	}
	if strings.TrimSpace(resp) != "PONG" {
		return fmt.Errorf("clamav: unexpected ping response %q", resp)
	}
	return nil
}

// readClamdResponse reads one NUL-terminated clamd response line.
func readClamdResponse(conn net.Conn) (string, error) {
	var buf [1]byte
	var sb strings.Builder
	for {
		if _, err := io.ReadFull(conn, buf[:]); err != nil {
			return "", fmt.Errorf("clamav: read response: %w", err)
		}
		if buf[0] == 0 {
			break
		}
		sb.WriteByte(buf[0])
		if sb.Len() > 4096 {
			return "", errors.New("clamav: response exceeds 4096 bytes")
		}
	}
	return sb.String(), nil
}

// parseClamdResponse maps a clamd INSTREAM answer onto a ScanResult.
// "stream: OK" → clean; "stream: <signature> FOUND" → infected; anything
// else (e.g. "size limit exceeded", "lstat() failed") → error.
func parseClamdResponse(resp string) (ScanResult, error) {
	line := strings.TrimSpace(resp)
	switch {
	case line == "stream: OK":
		return ScanResult{}, nil
	case strings.HasSuffix(line, " FOUND"):
		body := strings.TrimSpace(strings.TrimSuffix(line, " FOUND"))
		sig := strings.TrimSpace(strings.TrimPrefix(body, "stream:"))
		return ScanResult{Infected: true, Signature: sig}, nil
	default:
		return ScanResult{}, fmt.Errorf("clamav: unexpected response %q", line)
	}
}
