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

package mta

import (
	"errors"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/services/email"
)

// SF-02 tests: the RFC 8689 REQUIRETLS enforcement matrix on the session
// runner, on the same in-process MX harness as the STS tests. A
// REQUIRETLS-marked message must never cross a plaintext hop: a next hop
// without STARTTLS is refused with a temporary failure (the executor's loop
// tries the next MX host or defers), and the REQUIRETLS parameter propagates
// on the outbound MAIL FROM exactly when the next hop advertises the
// extension.

// deliverRequireTLSViaMX is deliverViaMX with the message's REQUIRETLS
// requirement set.
func deliverRequireTLSViaMX(t *testing.T, srv *mxServer, opts SessionOptions) (*SessionResult, error) {
	t.Helper()
	opts.RequireTLS = true
	return deliverViaMX(t, srv, opts)
}

// Downgrade refusal: a REQUIRETLS-marked message meeting an MX host that
// offers no STARTTLS is refused before any envelope command — never a
// plaintext delivery.
func TestSessionRequireTLSRefusesPlaintextHost(t *testing.T) {
	srv := newMXServer(t) // no STARTTLS offered

	res, err := deliverRequireTLSViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	var se *email.SendError
	if !errors.As(err, &se) || se.Stage != "REQUIRETLS" || se.Permanent() {
		t.Fatalf("plaintext host under REQUIRETLS: err = %v, want a transient REQUIRETLS SendError", err)
	}
	if se.Code != 421 {
		t.Fatalf("REQUIRETLS refusal code = %d, want 421 (defer, never a hard bounce)", se.Code)
	}
	if res != nil && res.Delivered {
		t.Fatal("the message must not be delivered over a plaintext hop")
	}
	for _, cmd := range srv.commandsSeen() {
		if strings.HasPrefix(cmd, "MAIL FROM") {
			t.Fatalf("the envelope must not reach a plaintext hop, saw %q", cmd)
		}
	}
}

// Matrix row: REQUIRETLS message + STARTTLS-offering host that advertises
// the extension → delivered over TLS with the parameter propagating on the
// outbound MAIL FROM (RFC 8689 §5.1).
func TestSessionRequireTLSDeliversOverTLSAndPropagates(t *testing.T) {
	srv := newMXServer(t, withTLS(), withCaps("REQUIRETLS"))

	res, err := deliverRequireTLSViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	if err != nil {
		t.Fatalf("REQUIRETLS delivery over TLS failed: %v", err)
	}
	if !res.Delivered || !res.Caps.UpgradedTLS {
		t.Fatalf("delivered=%v tls=%v, want true/true", res.Delivered, res.Caps.UpgradedTLS)
	}
	if !res.Caps.REQUIRETLS {
		t.Fatal("the remote's REQUIRETLS advertisement was not recorded")
	}
	if !srv.sawTLS() {
		t.Fatal("REQUIRETLS delivery must ride STARTTLS")
	}
	var mailLine string
	for _, cmd := range srv.commandsSeen() {
		if strings.HasPrefix(cmd, "MAIL FROM") {
			mailLine = cmd
		}
	}
	if !strings.Contains(mailLine, "REQUIRETLS") {
		t.Fatalf("outbound MAIL FROM = %q, want the REQUIRETLS parameter propagated", mailLine)
	}
}

// Matrix row: REQUIRETLS message + STARTTLS-offering host that does NOT
// advertise the extension → delivered over TLS WITHOUT the parameter (the
// hop's TLS satisfied the requirement; it does not propagate further,
// RFC 8689 §5.1).
func TestSessionRequireTLSOmitsParamWhenRemoteDoesNotAdvertise(t *testing.T) {
	srv := newMXServer(t, withTLS()) // STARTTLS, no REQUIRETLS advertisement

	res, err := deliverRequireTLSViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	if err != nil {
		t.Fatalf("REQUIRETLS delivery without a REQUIRETLS-capable next hop: %v", err)
	}
	if !res.Delivered || !srv.sawTLS() {
		t.Fatalf("delivered=%v tls-seen=%v, want the TLS delivery", res.Delivered, srv.sawTLS())
	}
	var mailLine string
	for _, cmd := range srv.commandsSeen() {
		if strings.HasPrefix(cmd, "MAIL FROM") {
			mailLine = cmd
		}
	}
	if strings.Contains(strings.ToUpper(mailLine), "REQUIRETLS") {
		t.Fatalf("outbound MAIL FROM = %q, want no REQUIRETLS parameter (remote does not advertise)", mailLine)
	}
}

// Control row: a plain message keeps the pre-SF-02 opportunistic posture —
// a host without STARTTLS still delivers plaintext.
func TestSessionPlainMessageStillDeliversOverPlaintextHost(t *testing.T) {
	srv := newMXServer(t) // no STARTTLS offered

	res, err := deliverViaMX(t, srv, SessionOptions{
		Addr:       srv.addr(),
		ServerName: "mx.test",
	})
	if err != nil || !res.Delivered {
		t.Fatalf("plain message over a plaintext host: res=%v err=%v, want delivered (legacy posture)", res, err)
	}
	if res.Caps.UpgradedTLS {
		t.Fatal("no STARTTLS was offered; the session must not claim a TLS upgrade")
	}
}
