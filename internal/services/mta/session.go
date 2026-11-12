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

package mta

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"time"

	"github.com/goposta/posta/internal/services/email"
)

// nowFunc is overridable in tests.
var nowFunc = time.Now

// SessionTarget tells the session runner what it is talking to. AUTH is only
// ever honored against configured smarthosts — never to a resolved MX host
// (ADR-001 §4).
type SessionTarget string

const (
	TargetMX        SessionTarget = "mx"
	TargetSmarthost SessionTarget = "smarthost"
)

// SessionCaps records the EHLO capabilities the remote advertised and the
// upgrades that actually happened during the session.
type SessionCaps struct {
	UpgradedTLS  bool
	PIPELINING   bool
	EightBitMIME bool
	SMTPUTF8     bool
	Auth         bool
	// CHUNKING (RFC 3030) — recorded by the session runner; when offered, the
	// end-of-message phase uses BDAT instead of DATA (SF-02).
	CHUNKING bool
}

// SessionOptions configures one outbound ESMTP session.
type SessionOptions struct {
	// Addr is the dial target, host:port.
	Addr string
	// ServerName is the TLS ServerName for STARTTLS verification (the MX
	// hostname, or the smarthost hostname).
	ServerName string
	// HeloDomain is the EHLO identifier this server presents.
	HeloDomain string
	// BindIP pins the source address (per-domain outbound bind). nil = default.
	BindIP net.IP
	// Target must be TargetMX for direct deliveries; AUTH is refused on MX
	// sessions regardless of what the remote advertises.
	Target SessionTarget
	// TLSConfig overrides the default STARTTLS configuration (&tls.Config
	// with ServerName). Certificate verification is always enabled.
	TLSConfig *tls.Config
	// Auth may only be set together with TargetSmarthost; the session runner
	// refuses AUTH on MX sessions outright (ADR-001 §4: AUTH never to MX).
	Auth smtp.Auth
	// CommandTimeout bounds each SMTP command. 0 → the R-22 session budget.
	CommandTimeout time.Duration
	// OverallBudget bounds the whole session from dial. 0 → the R-22 budget.
	OverallBudget time.Duration
	// STS is the outbound MTA-STS enforcement hook (SF-29, OB-6 W4,
	// ADR-007 §5) behind the small STSEnforcement interface. nil (the zero
	// value) falls back to the process-wide default (DefaultSTS, armed by the
	// executor wiring in cmd/posta); both nil = enforcement off and the
	// session behaves exactly as before. Only direct-MX sessions carry an
	// STS hook — smarthost/relay sessions are configured by operators, not
	// discovered, so MTA-STS does not apply to them (RFC 8461 §2 scope).
	STS STSEnforcement
	// ThrottleBytesPerSec caps this connection's outbound write rate
	// (SF-108, POSTA_DELIVERY_THROTTLE_KBPS; 0 = unlimited). The token
	// bucket lives on the connection, so commands AND the message payload
	// cross the wire at the paced rate.
	ThrottleBytesPerSec int64
}

// SessionResult is the outcome of one session: Delivered true exactly when
// the remote answered 2xx to the end-of-DATA.
type SessionResult struct {
	Delivered bool
	// Stage, Code and Msg describe the final SMTP reply (or the stage where a
	// connection-level failure happened; Code 0 = no SMTP reply).
	Stage string
	Code  int
	Msg   string
	Caps  SessionCaps
	// RemoteIP is the address the session connected to (attempt records).
	RemoteIP string
}

// runSession drives one outbound ESMTP conversation. Failures are returned as
// *email.SendError-shaped errors (via email.WrapSendError) so the worker's
// R-05 classification chain treats direct-MX failures exactly like relay
// failures. The connection is always closed before returning.
func runSession(ctx context.Context, opts SessionOptions, from string, rcpts []string, msg []byte) (*SessionResult, error) {
	if opts.Target == "" {
		opts.Target = TargetMX
	}
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = email.SessionBudget()
	}
	if opts.OverallBudget <= 0 {
		opts.OverallBudget = email.SessionBudget()
	}
	if opts.HeloDomain == "" {
		opts.HeloDomain = DefaultHeloDomain
	}
	tlsConfig := opts.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{ServerName: opts.ServerName}
	}
	if opts.Target == TargetMX && opts.Auth != nil {
		// Non-negotiable: AUTH is for smarthosts only, never for direct MX.
		return nil, errors.New("mta: AUTH is not permitted on MX sessions")
	}

	// MTA-STS outbound enforcement (SF-29, ADR-007 §5): resolve the
	// recipient domain's policy once, before the dial. The recipient domain
	// is re-derived from the validated recipients — never taken from client
	// input (ADR-001 non-negotiable #2). Deliver.go groups recipients by
	// domain before calling the runner, so the first parsable domain is the
	// session's domain; an unparsable set disables enforcement and lets the
	// existing recipient validation reject it. Discovery failures fall open
	// (a nil rule = no policy = the pre-existing opportunistic posture).
	var stsRule *STSRule
	if opts.Target == TargetMX {
		sts := opts.STS
		if sts == nil {
			sts = DefaultSTS()
		}
		if sts != nil && len(rcpts) > 0 {
			if d := domainOf(email.EnvelopeAddress(rcpts[0])); d != "" {
				stsRule = sts.SessionRule(ctx, d)
			}
		}
	}
	if stsRule.Enforced() && !stsRule.HostAllowed(opts.ServerName) {
		// RFC 8461 §10.5: a delivery path the policy forbids is a temporary
		// failure — the runner reports it and the executor's loop moves to
		// the next MX host (or defers), never a permanent user-facing bounce.
		return nil, &email.SendError{
			Stage: "STS", Code: 421,
			Msg: fmt.Sprintf("4.7.0 MTA-STS policy for the recipient domain does not include MX host %s", opts.ServerName),
			Err: fmt.Errorf("mta: MTA-STS: MX host %s is not in the recipient domain's enforce policy", opts.ServerName),
		}
	}

	overallEnd := nowFunc().Add(opts.OverallBudget)

	conn, client, err := email.DialSMTPWithBind(opts.Addr, opts.ServerName, opts.BindIP)
	if err != nil {
		return nil, email.WrapSendError("DIAL", "", err)
	}
	// SF-108: pace every byte this connection sends (commands and payload)
	// through a token bucket when a transfer throttle is configured.
	if opts.ThrottleBytesPerSec > 0 {
		conn = newThrottledConn(conn, opts.ThrottleBytesPerSec)
	}
	defer func() { _ = conn.Close() }()
	remoteIP := ""
	if addr := conn.RemoteAddr(); addr != nil {
		remoteIP = addr.String()
	}
	res := &SessionResult{RemoteIP: remoteIP}

	// budget sets the deadline for the next command: the per-command budget,
	// capped by the overall session budget. The deadline lands on the socket
	// DialSMTPWithBind returned; after STARTTLS the tls.Conn wraps the same
	// socket, so the deadline keeps applying (same arrangement as dialSMTP).
	budget := func() {
		d := nowFunc().Add(opts.CommandTimeout)
		if overallEnd.Before(d) {
			d = overallEnd
		}
		_ = conn.SetDeadline(d)
	}

	// EHLO identification. net/smtp defers the greeting to the first command;
	// an explicit Hello pins our EHLO name and captures capabilities.
	budget()
	if err := client.Hello(opts.HeloDomain); err != nil {
		return res, email.WrapSendError("EHLO", "", err)
	}
	res.Caps.PIPELINING, _ = client.Extension("PIPELINING")
	res.Caps.EightBitMIME, _ = client.Extension("8BITMIME")
	res.Caps.SMTPUTF8, _ = client.Extension("SMTPUTF8")
	res.Caps.Auth, _ = client.Extension("AUTH")
	res.Caps.CHUNKING, _ = client.Extension("CHUNKING")

	// STARTTLS is mandatory when the remote offers it (ADR-001 §4). The TLS
	// handshake verifies the certificate chain against tlsConfig — plain
	// continues only when the remote offers no STARTTLS at all.
	offered, _ := client.Extension("STARTTLS")
	if !offered && stsRule.Enforced() {
		// RFC 8461 §10.3: mode=enforce requires TLS; a policy-relevant MX
		// that does not advertise STARTTLS yields a temporary failure (the
		// message defers; it must not fall back to plaintext delivery).
		return res, &email.SendError{
			Stage: "STS", Code: 421,
			Msg: "4.7.0 MTA-STS policy requires STARTTLS but the MX host does not offer it",
			Err: fmt.Errorf("mta: MTA-STS: MX host %s does not offer STARTTLS under an enforce policy", opts.ServerName),
		}
	}
	if offered {
		budget()
		if err := client.StartTLS(tlsConfig); err != nil {
			return res, email.WrapSendError("STARTTLS", "", err)
		}
		res.Caps.UpgradedTLS = true
	}

	// AUTH only against configured smarthosts (MX sessions never reach here —
	// refused above).
	if opts.Target == TargetSmarthost && opts.Auth != nil {
		if !res.Caps.Auth {
			return res, email.WrapSendError("AUTH", "", errors.New("smarthost does not advertise AUTH"))
		}
		budget()
		if err := client.Auth(opts.Auth); err != nil {
			return res, email.WrapSendError("AUTH", "", err)
		}
	}

	// SMTPUTF8 must be negotiated when the envelope itself carries non-ASCII;
	// sending a UTF-8 envelope without the param is a protocol violation the
	// next hop is free to mangle. Fail closed (permanent policy rejection).
	if (!isASCIIString(from) || anyNonASCII(rcpts)) && !res.Caps.SMTPUTF8 {
		return res, &email.SendError{
			Stage: "MAIL FROM", Code: 553,
			Msg: "5.6.7 SMTPUTF8 required for a non-ASCII envelope but not offered",
			Err: errors.New("mta: SMTPUTF8 required but not offered by remote"),
		}
	}

	// MAIL FROM. net/smtp adds BODY=8BITMIME/SMTPUTF8 params automatically
	// when the remote advertises them — 8BITMIME is a superset declaration
	// for 7-bit bodies, and the UTF-8 envelope case was validated above.
	budget()
	if err := client.Mail(from); err != nil {
		return res, email.WrapSendError("MAIL FROM", from, err)
	}

	for _, r := range rcpts {
		if err := ctx.Err(); err != nil {
			return res, email.WrapSendError("RCPT TO", r, err)
		}
		budget()
		rcpt := email.EnvelopeAddress(r)
		if err := client.Rcpt(rcpt); err != nil {
			return res, email.WrapSendError("RCPT TO", rcpt, err)
		}
	}

	// Fresh budget for the end-of-message phase — the longest single
	// operation, same arrangement as the relay sender.
	budget()

	// SF-02 (RFC 3030): when the remote advertised CHUNKING, end the message
	// with BDAT instead of DATA — the payload crosses the wire verbatim (no
	// dot-stuffing), so byte-exact binary transparency holds end to end.
	if res.Caps.CHUNKING {
		berr := sendBdat(conn, msg)
		if berr != nil {
			return res, berr
		}
		res.Delivered = true
		res.Code = 250
		res.Msg = "2xx accepted at end of BDAT"
		_ = client.Quit()
		return res, nil
	}

	w, err := client.Data()
	if err != nil {
		return res, email.WrapSendError("DATA", "", err)
	}
	if _, err := w.Write(msg); err != nil {
		return res, email.WrapSendError("DATA", "", err)
	}
	if err := w.Close(); err != nil {
		return res, email.WrapSendError("DATA", "", err)
	}

	res.Delivered = true
	// The end-of-DATA 2xx is the delivery truth; net/smtp consumes the reply
	// inside Data().Close(), so record the class (250) with a stable
	// descriptor for the attempt record.
	res.Code = 250
	res.Msg = "2xx accepted at end of DATA"

	_ = client.Quit()
	return res, nil
}

// sendBdat ends the message transfer with one BDAT <n> LAST chunk (RFC 3030).
// The command and the reply go through the raw socket: net/smtp's Client has
// no BDAT, and the payload must NOT be dot-stuffed. The exchange is safe
// against the shared textproto pipeline because every earlier command
// (Hello/Mail/Rcpt) flushed its reply synchronously — the socket is quiescent
// between RCPT and this call, so the reply read here sees exactly the BDAT
// response and leaves the client's pipeline consistent for Quit.
func sendBdat(conn net.Conn, msg []byte) error {
	if _, err := fmt.Fprintf(conn, "BDAT %d LAST\r\n", len(msg)); err != nil {
		return email.WrapSendError("BDAT", "", err)
	}
	if _, err := conn.Write(msg); err != nil {
		return email.WrapSendError("BDAT", "", err)
	}
	tr := textproto.NewReader(bufio.NewReader(conn))
	_, _, err := tr.ReadResponse(250)
	if err != nil {
		return email.WrapSendError("BDAT", "", err)
	}
	return nil
}

// DefaultHeloDomain is the EHLO identifier used when none is configured.
// Operators should set the per-process EHLO name (POSTA_MTA_EHLO_NAME) to the
// hostname whose PTR record matches the outbound bind IP.
const DefaultHeloDomain = "posta.local"

// anyNonASCII reports whether any envelope recipient carries non-ASCII bytes.
func anyNonASCII(addrs []string) bool {
	for _, a := range addrs {
		if !isASCIIString(a) {
			return true
		}
	}
	return false
}

func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// throttledConn caps outbound writes at rate bytes/second through a token
// bucket with a one-second burst (SF-108). Reads pass through untouched; the
// connection's read deadlines keep bounding the session, so a throttled
// payload can extend the write phase but never the whole session unboundedly.
type throttledConn struct {
	net.Conn
	rate      float64
	tokens    float64
	maxTokens float64
	last      time.Time
}

func newThrottledConn(conn net.Conn, bytesPerSec int64) *throttledConn {
	rate := float64(bytesPerSec)
	if rate < 64 {
		rate = 64 // floor: one SMTP command per second even at absurd settings
	}
	return &throttledConn{
		Conn:      conn,
		rate:      rate,
		tokens:    rate,
		maxTokens: rate,
		last:      time.Now(),
	}
}

func (c *throttledConn) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		c.refill()
		avail := len(p) - written
		if float64(avail) > c.tokens {
			avail = int(c.tokens)
		}
		if avail <= 0 {
			// Sleep until one byte's worth of token accrues; the slice caps
			// the wait so budget changes (deadlines) surface promptly.
			time.Sleep(c.waitTime(float64(len(p) - written)))
			continue
		}
		n, err := c.Conn.Write(p[written : written+avail])
		written += n
		c.tokens -= float64(n)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// refill accrues tokens since the last refill, capped at the burst size.
func (c *throttledConn) refill() {
	now := time.Now()
	elapsed := now.Sub(c.last).Seconds()
	c.last = now
	c.tokens += elapsed * c.rate
	if c.tokens > c.maxTokens {
		c.tokens = c.maxTokens
	}
}

// waitTime is how long until deficit bytes have accrued at rate.
func (c *throttledConn) waitTime(deficit float64) time.Duration {
	needed := deficit - c.tokens
	if needed <= 0 {
		return time.Millisecond
	}
	d := time.Duration(needed / c.rate * float64(time.Second))
	if d > 200*time.Millisecond {
		d = 200 * time.Millisecond
	}
	if d < time.Millisecond {
		d = time.Millisecond
	}
	return d
}
