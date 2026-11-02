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

package security

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jkaninda/logger"
)

// ParseRemoteIP extracts the client IP from a remote address string —
// "192.0.2.1:51344", "[2001:db8::1]:25", a bare address, or the value of
// okapi's RealIP — and normalizes it (IPv4-in-IPv6 spellings collapse to
// plain IPv4, matching the ban snapshot's keying). An unparseable value
// yields the zero Addr, which every enforcement hook treats as "skip".
func ParseRemoteIP(remote string) netip.Addr {
	s := strings.TrimSpace(remote)
	if s == "" {
		return netip.Addr{}
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// CountingListener is the accept-level enforcement point for the SMTP
// listeners: every accepted connection is checked against the allowlist and
// the ban snapshot (cheap, in-memory — never Redis or the database on the
// accept path), recorded as a new connection, and wrapped so that a client
// which closes without ever sending a byte counts as a connect abort.
//
// A banned IP is dropped silently, the reference platform-style: the socket is closed
// before a single byte is written or read, and the accept loop continues —
// no error is surfaced to smtp.Server.Serve, which would otherwise log and
// retry. Accept errors from the wrapped listener pass through to Serve,
// which applies its own retry-with-backoff (5ms doubling to 1s), the same
// pattern net/http and this repo's HTTP server use.
type CountingListener struct {
	net.Listener
	eng   *Engine
	port  int
	proto string
}

// NewCountingListener wraps l with the accept-level checks. A nil eng turns
// the wrapper into a pure passthrough.
func NewCountingListener(l net.Listener, eng *Engine, port int, proto string) *CountingListener {
	return &CountingListener{Listener: l, eng: eng, port: port, proto: proto}
}

// Accept returns the next connection, enforcing allowlist, bans and
// connect-recording on the way. Banned peers never leave this loop.
func (l *CountingListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.eng == nil || !l.eng.Enabled() {
			return conn, nil // feature disabled: pure passthrough
		}

		ip := ParseRemoteIP(conn.RemoteAddr().String())
		if !ip.IsValid() {
			return conn, nil // unparseable peer (e.g. unix socket): skip checks
		}

		if l.eng.Bans != nil {
			if l.eng.Bans.IsAllowed(ip) {
				return newCountingConn(conn, l.eng, ip, l.port, l.proto), nil
			}
			if blocked, ban := l.eng.Bans.IsBlocked(ip); blocked {
				// Silent drop: close without writing anything. Serve only
				// reacts to Accept *errors*, so this must not return one.
				reason := ""
				if ban != nil {
					reason = ban.ReasonCode
				}
				logger.Warn("security: dropped connection from banned IP",
					"ip", ip.String(), "port", l.port, "reason", reason)
				_ = conn.Close()
				continue
			}
		}

		if l.eng.Detect != nil {
			// Fail-open: a recording failure never blocks an accepted
			// connection; the detector logs and swallows Redis errors.
			if err := l.eng.Detect.RecordConnect(context.Background(), ip, l.port, l.proto); err != nil {
				logger.Warn("security: failed to record connection", "ip", ip.String(), "error", err)
			}
		}
		return newCountingConn(conn, l.eng, ip, l.port, l.proto), nil
	}
}

// countingConn counts the bytes read from the client. When the connection is
// closed with zero bytes ever read, the peer connected and sent nothing —
// the pre-EHLO abort go-smtp's backend API cannot see (a session only exists
// after the first EHLO, so its Logout never fires) — and is recorded as a
// connect abort. This also covers plaintext probes of implicit-TLS ports.
//
// The wrapper sits underneath any TLS layer: go-smtp's STARTTLS wraps
// exactly this conn with tls.Server, so TLS traffic still flows through
// Read, and for implicit TLS the eager handshake's bytes are counted too.
type countingConn struct {
	net.Conn
	eng   *Engine
	ip    netip.Addr
	port  int
	proto string

	readBytes atomic.Int64
	closeOnce sync.Once
}

func newCountingConn(conn net.Conn, eng *Engine, ip netip.Addr, port int, proto string) *countingConn {
	return &countingConn{Conn: conn, eng: eng, ip: ip, port: port, proto: proto}
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.readBytes.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() {
		if c.eng == nil || c.eng.Detect == nil || !c.ip.IsValid() {
			return
		}
		if c.readBytes.Load() != 0 {
			return // the client spoke; not a connect abort
		}
		if err := c.eng.Detect.RecordAbort(context.Background(), c.ip, c.port, c.proto); err != nil {
			logger.Warn("security: failed to record connect abort", "ip", c.ip.String(), "error", err)
		}
	})
	return err
}

// TLS handshake limits for TLSServerListener: the eager handshake runs
// outside go-smtp (which cannot report TLS failures through any hook at
// v0.25.0), so it is bounded by a wall-clock timeout and a concurrency
// semaphore. A slowloris therefore stalls accepts at most tlsHandshakeTimeout
// per slot while the kernel listen backlog absorbs concurrent connects.
const (
	tlsHandshakeTimeout     = 10 * time.Second
	maxConcurrentHandshakes = 16
)

// TLSServerListener wraps l (normally a CountingListener) with an eager TLS
// handshake performed in the accept path. go-smtp type-asserts every accepted
// conn to *tls.Conn and handshakes before its 220 greeting, but a failed
// handshake is invisible to the backend — the only observable is a log line.
// Handshaking here instead gives a structured failure hook: each failed
// handshake is recorded as an incomplete handshake with protocol "tls"
// (TLS probing), the raw conn is closed, and the accept loop continues.
//
// The returned conn is the plain *tls.Conn — never wrap it further, or
// go-smtp's assertion fails and it skips its own (now no-op) handshake.
// crypto/tls treats a second Handshake on a completed conn as a no-op.
//
// Note: STARTTLS listeners must NOT be wrapped with this — those clients
// connect in plaintext and upgrade in-band, which go-smtp drives itself.
func TLSServerListener(l net.Listener, tlsCfg *tls.Config, eng *Engine, port int) net.Listener {
	t := &tlsServerListener{
		inner:      l,
		tlsCfg:     tlsCfg,
		eng:        eng,
		port:       port,
		handshakes: make(chan struct{}, maxConcurrentHandshakes),
		ready:      make(chan net.Conn),
		acceptErrs: make(chan error, 1),
		done:       make(chan struct{}),
	}
	go t.acceptLoop()
	return t
}

type tlsServerListener struct {
	inner      net.Listener
	tlsCfg     *tls.Config
	eng        *Engine
	port       int
	handshakes chan struct{} // semaphore bounding concurrent handshakes
	ready      chan net.Conn // handshaked conns awaiting pickup
	acceptErrs chan error    // fatal inner-accept errors, at most one pending
	done       chan struct{}
	closeOnce  sync.Once
}

// acceptLoop pumps raw conns into bounded handshake workers and surfaces
// fatal accept errors. Transient accept failures back off exactly like
// net/http and go-smtp's Serve do (5ms doubling to 1s).
func (t *tlsServerListener) acceptLoop() {
	var tempDelay time.Duration
	for {
		conn, err := t.inner.Accept()
		if err != nil {
			select {
			case <-t.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				// The owner closed the listener: surface the sentinel once so
				// the accept consumer can finish, then wait for shutdown.
				select {
				case t.acceptErrs <- err:
				case <-t.done:
				}
				<-t.done
				return
			}
			// Everything else is treated as transient and retried with the
			// same exponential backoff net/http and go-smtp's Serve use
			// (5ms doubling to 1s) — net.Error.Temporary is deprecated and
			// misclassifies several retryable errors anyway.
			if tempDelay == 0 {
				tempDelay = 5 * time.Millisecond
			} else {
				tempDelay *= 2
			}
			if max := time.Second; tempDelay > max {
				tempDelay = max
			}
			select {
			case <-t.done:
				return
			case <-time.After(tempDelay):
			}
			continue
		}

		tempDelay = 0
		select {
		case t.handshakes <- struct{}{}:
		case <-t.done:
			_ = conn.Close()
			return
		}
		go t.handshake(conn)
	}
}

// handshake performs one eager TLS handshake and hands the established
// *tls.Conn to Accept, or records the failure and discards the conn.
func (t *tlsServerListener) handshake(conn net.Conn) {
	defer func() { <-t.handshakes }()

	tlsConn := tls.Server(conn, t.tlsCfg)
	hctx, cancel := context.WithTimeout(context.Background(), tlsHandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(hctx); err != nil {
		if t.eng != nil && t.eng.Detect != nil {
			if ip := ParseRemoteIP(conn.RemoteAddr().String()); ip.IsValid() {
				if err := t.eng.Detect.RecordHandshakeFail(context.Background(), ip, t.port, "tls"); err != nil {
					logger.Warn("security: failed to record TLS handshake failure", "ip", ip.String(), "error", err)
				}
			}
		}
		_ = conn.Close()
		return
	}

	select {
	case t.ready <- tlsConn:
	case <-t.done:
		_ = tlsConn.Close()
	}
}

func (t *tlsServerListener) Accept() (net.Conn, error) {
	select {
	case conn := <-t.ready:
		return conn, nil
	case err := <-t.acceptErrs:
		return nil, err
	case <-t.done:
		return nil, net.ErrClosed
	}
}

func (t *tlsServerListener) Close() error {
	t.closeOnce.Do(func() { close(t.done) })
	return t.inner.Close()
}

func (t *tlsServerListener) Addr() net.Addr { return t.inner.Addr() }
