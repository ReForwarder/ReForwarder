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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/services/email"
)

// fakeSTS arms the process default (and restores it) with a rule source the
// tests control directly — no DNS or HTTPS involved.
type fakeSTS struct {
	rule *STSRule
}

func (f *fakeSTS) SessionRule(context.Context, string) *STSRule { return f.rule }

// armFakeSTS sets the process-wide hook and registers cleanup.
func armFakeSTS(t *testing.T, rule *STSRule) {
	t.Helper()
	prev := DefaultSTS()
	SetDefaultSTS(&fakeSTS{rule: rule})
	t.Cleanup(func() { SetDefaultSTS(prev) })
}

// deliverViaMX runs one direct-MX session against the in-process harness
// server using the test TLS trust pool.
func deliverViaMX(t *testing.T, srv *mxServer, opts SessionOptions) (*SessionResult, error) {
	t.Helper()
	if opts.TLSConfig == nil && srv.cert != nil {
		opts.TLSConfig = clientTLSConfig(t, srv.cert)
	}
	opts.Target = TargetMX
	opts.HeloDomain = "posta.test"
	return runSession(context.Background(), opts, "sender@sender.example",
		[]string{"rcpt@recipient.example"}, []byte("Subject: t\r\n\r\nbody"))
}

func TestSessionSTSRefusesHostOutsidePolicy(t *testing.T) {
	armFakeSTS(t, &STSRule{Mode: STSModeEnforce, MX: []string{"allowed.example"}})
	srv := newMXServer(t, withTLS())

	_, err := deliverViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "forbidden.example", // not in the policy
	})
	var se *email.SendError
	if !errors.As(err, &se) || se.Stage != "STS" || se.Permanent() {
		t.Fatalf("host outside policy: err = %v, want a transient STS SendError", err)
	}
	if se.Code != 421 {
		t.Fatalf("STS refusal code = %d, want 421 (defer, never a hard bounce)", se.Code)
	}
	if srv.sessionCount() != 0 {
		t.Fatalf("the session must be refused before dialing, %d sessions observed", srv.sessionCount())
	}
}

func TestSessionSTSEnforceRequiresSTARTTLS(t *testing.T) {
	armFakeSTS(t, &STSRule{Mode: STSModeEnforce, MX: []string{"mx.test"}})
	// No withTLS(): the server never offers STARTTLS.
	srv := newMXServer(t)

	_, err := deliverViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	var se *email.SendError
	if !errors.As(err, &se) || se.Stage != "STS" || se.Permanent() {
		t.Fatalf("missing STARTTLS under enforce: err = %v, want a transient STS SendError", err)
	}
	if !strings.Contains(strings.ToUpper(se.Msg), "STARTTLS") {
		t.Fatalf("STS STARTTLS refusal text wrong: %q", se.Msg)
	}
}

func TestSessionSTSEnforceDeliversViaPolicyHostWithTLS(t *testing.T) {
	armFakeSTS(t, &STSRule{Mode: STSModeEnforce, MX: []string{"mx.test"}})
	srv := newMXServer(t, withTLS())

	res, err := deliverViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	if err != nil {
		t.Fatalf("enforce-compliant session failed: %v", err)
	}
	if !res.Delivered || !res.Caps.UpgradedTLS {
		t.Fatalf("delivered=%v tls=%v, want true/true", res.Delivered, res.Caps.UpgradedTLS)
	}
	if !srv.sawTLS() {
		t.Fatalf("enforce delivery must ride STARTTLS")
	}
}

func TestSessionSTSTestingModeDoesNotBlock(t *testing.T) {
	// RFC 8461: testing records and reports, never refuses — plaintext
	// delivery to a testing-policy host still succeeds.
	armFakeSTS(t, &STSRule{Mode: STSModeTesting, MX: []string{"mx.test"}})
	srv := newMXServer(t) // no STARTTLS offered

	res, err := deliverViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	if err != nil || !res.Delivered {
		t.Fatalf("testing mode must not block: res=%v err=%v", res, err)
	}
}

func TestSessionSTSNoneModeIsNoop(t *testing.T) {
	armFakeSTS(t, &STSRule{Mode: STSModeNone})
	srv := newMXServer(t)

	res, err := deliverViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "anything.example",
	})
	if err != nil || !res.Delivered {
		t.Fatalf("none mode must not gate: res=%v err=%v", res, err)
	}
}

func TestSessionSTSOffByDefault(t *testing.T) {
	// No process hook armed: every existing behavior is unchanged (this is
	// the pre-SF-29 posture the relay wave and all prior tests rely on).
	SetDefaultSTS(nil)
	srv := newMXServer(t) // plaintext

	res, err := deliverViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	if err != nil || !res.Delivered {
		t.Fatalf("enforcement-off session must behave as before: res=%v err=%v", res, err)
	}
}

func TestSessionSTSDefaultCoversNilOption(t *testing.T) {
	// The executor wiring contract: deliver.go builds SessionOptions without
	// an STS field this round, so an ARMED process default must cover a nil
	// opts.STS — the untouched opts literal still enforces.
	armFakeSTS(t, &STSRule{Mode: STSModeEnforce, MX: []string{"allowed.example"}})
	srv := newMXServer(t, withTLS())

	_, err := runSession(context.Background(),
		SessionOptions{
			Addr:       srv.addr(),
			ServerName: "forbidden.example",
			STS:        nil,
		}, "sender@sender.example", []string{"rcpt@recipient.example"}, []byte("x"))
	var se *email.SendError
	if !errors.As(err, &se) || se.Stage != "STS" {
		t.Fatalf("armed default must cover a nil opts.STS: err = %v", err)
	}
}

func TestSessionSTSExplicitHookEnforces(t *testing.T) {
	SetDefaultSTS(nil) // no process default
	srv := newMXServer(t, withTLS())

	_, err := runSession(context.Background(),
		SessionOptions{
			Addr:       srv.addr(),
			ServerName: "forbidden.example",
			STS:        &fakeSTS{rule: &STSRule{Mode: STSModeEnforce, MX: []string{"allowed.example"}}},
		}, "sender@sender.example", []string{"rcpt@recipient.example"}, []byte("x"))
	var se *email.SendError
	if !errors.As(err, &se) || se.Stage != "STS" {
		t.Fatalf("explicit hook must enforce: err = %v", err)
	}
}

func TestSessionSTSEnforcementNeverTouchesSmarthostSessions(t *testing.T) {
	// MTA-STS applies to MX delivery only; a smarthost session (operator-
	// configured relay) must not consult the hook.
	armFakeSTS(t, &STSRule{Mode: STSModeEnforce, MX: []string{"allowed.example"}})
	srv := newMXServer(t)

	opts := SessionOptions{
		Addr:       srv.addr(),
		ServerName: "forbidden.example",
		Target:     TargetSmarthost,
	}
	// No Auth: the smarthost branch only AUTHs when Auth != nil.
	res, err := runSession(context.Background(), opts, "sender@sender.example",
		[]string{"rcpt@recipient.example"}, []byte("x"))
	if err != nil || !res.Delivered {
		t.Fatalf("smarthost sessions bypass MTA-STS: res=%v err=%v", res, err)
	}
}
