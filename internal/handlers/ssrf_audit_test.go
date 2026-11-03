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

package handlers

// Independent adversarial security-audit tests for the read-time sanitization
// layer added by commit 5c2678a (webhook_delivery_handler.go / logs_handler.go
// sanitize helpers). Written by the auditor; production code and existing
// test files are untouched. No database and no network are involved.

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webhook"
)

// auditIPv4Re matches any dotted-quad IPv4 literal.
var auditIPv4Re = regexp.MustCompile(`([0-9]{1,3}\.){3}[0-9]{1,3}`)

// auditLeakMarkers are internal-infrastructure markers that must never appear
// in API-served text: hex/decimal/octal loopback encodings, the metadata
// path, common internal service ports, and the DNS port.
var auditLeakMarkers = []string{
	"0x7f", "2130706433", "0177.", "meta-data", "security-credentials",
	":5432", ":3128", ":6379", ":53", "127.0.0.1",
}

// auditScanLeaks returns every leak found in s (empty means clean).
func auditScanLeaks(s string) []string {
	var found []string
	found = append(found, auditIPv4Re.FindAllString(s, -1)...)
	for _, m := range auditLeakMarkers {
		if strings.Contains(s, m) {
			found = append(found, m)
		}
	}
	return found
}

// TestAuditDeliveryRows_NeverLeakInfraDetail pushes hostile legacy error rows
// through the read-path sanitizer the delivery and logs endpoints use and
// asserts nothing internal survives: no IPv4 literal, no encoded loopback, no
// metadata path, no internal ports, no resolver detail. "HTTP <code>" rows
// must survive verbatim (they carry no infrastructure detail).
func TestAuditDeliveryRows_NeverLeakInfraDetail(t *testing.T) {
	deliveries := []models.WebhookDelivery{
		{ErrorMessage: `Post "http://127.0.0.1:8080/hook": dial tcp 127.0.0.1:8080: connect: connection refused`},
		{ErrorMessage: "dial tcp 10.1.2.3:5432: connect: connection refused"},
		{ErrorMessage: "lookup db.internal on 172.20.0.10:53: server misbehaving"},
		{ErrorMessage: `Get "http://169.254.169.254/latest/meta-data/iam/security-credentials/": dial tcp 169.254.169.254:80: i/o timeout`},
		{ErrorMessage: "proxyconnect tcp: dial tcp 192.168.0.9:3128: connect: connection refused"},
		{ErrorMessage: "read tcp 172.16.9.9:443->172.16.9.1:5555: read: connection reset by peer"},
		{ErrorMessage: "dial tcp: lookup 0x7f000001 on 10.0.0.2:53: no such host"},
		// Safe rows that must be preserved exactly:
		{ErrorMessage: "HTTP 500"},
		{ErrorMessage: "http 404"},
		{ErrorMessage: ""},
	}
	sanitizeDeliveries(deliveries)

	for i, d := range deliveries {
		if leaks := auditScanLeaks(d.ErrorMessage); len(leaks) > 0 {
			t.Errorf("delivery row %d leaks %v in %q", i, leaks, d.ErrorMessage)
		}
	}
	// Safe rows verbatim:
	if got := deliveries[7].ErrorMessage; got != "HTTP 500" {
		t.Errorf("HTTP 500 row rewritten to %q, want verbatim", got)
	}
	if got := deliveries[8].ErrorMessage; got != "http 404" {
		t.Errorf("http 404 row rewritten to %q, want verbatim", got)
	}
	if got := deliveries[9].ErrorMessage; got != "" {
		t.Errorf("empty row rewritten to %q, want empty", got)
	}
}

// TestAuditAttempts_RedactionMatrixPerDelivery exercises the attempt
// sanitizer the way the logs handler wires it: one URL lookup per owning
// delivery (private literal → redact; public → keep; unknown webhook →
// redact; hostname URL → currently kept, see the hardening note below).
func TestAuditAttempts_RedactionMatrixPerDelivery(t *testing.T) {
	urls := map[uint]string{
		1: "http://169.254.169.254/latest/meta-data/", // legacy private literal
		2: "https://hooks.example.com/hook",           // public — bodies are meaningful
		3: "",                                         // webhook deleted since — fail safe
		4: "http://rebind.test/hook",                  // hostname: static-valid, historically may have resolved internally
	}
	attempts := []models.WebhookDeliveryAttempt{
		{DeliveryID: 1, Error: "dial tcp 10.0.0.5:9000: connect: connection refused", ResponseBody: `{"secret":"internal"}`},
		{DeliveryID: 2, Error: "HTTP 500", ResponseBody: `{"error":"not found"}`},
		{DeliveryID: 3, Error: "", ResponseBody: `{"secret":"orphaned"}`},
		{DeliveryID: 4, Error: "", ResponseBody: `{"answer":"from-internal"}`},
	}
	sanitizeAttempts(attempts, func(id uint) string { return urls[id] })

	if got := attempts[0].ResponseBody; got != "" {
		t.Errorf("private-URL attempt body = %q, want redacted", got)
	}
	if leaks := auditScanLeaks(attempts[0].Error); len(leaks) > 0 {
		t.Errorf("private-URL attempt error leaks %v in %q", leaks, attempts[0].Error)
	}
	if got := attempts[1].ResponseBody; got != `{"error":"not found"}` {
		t.Errorf("public-URL attempt body = %q, want kept", got)
	}
	if got := attempts[1].Error; got != "HTTP 500" {
		t.Errorf("public-URL attempt error = %q, want verbatim", got)
	}
	if got := attempts[2].ResponseBody; got != "" {
		t.Errorf("unknown-webhook attempt body = %q, want redacted (fail safe)", got)
	}
	// Hardening pin: hostname URLs pass static validation, so bodies are
	// currently kept even for legacy rows whose hostname may once have
	// resolved internally. Forward-looking deliveries cannot create such rows
	// (the guarded dialer pins every connection), so this is a legacy-data
	// limitation, accepted and documented in the audit report.
	if got := attempts[3].ResponseBody; got != `{"answer":"from-internal"}` {
		t.Errorf("hostname-URL attempt body = %q (legacy heuristic keeps hostname bodies; flip this pin if the heuristic tightens)", got)
	}
}

// TestAuditCreateValidationErrorsAreAddressFree mirrors the webhook creation
// handler's two validation calls over hostile inputs and asserts the error
// text echoed into the 400 response ("invalid webhook URL: <text>") never
// carries an IPv4 literal or internal marker — including when the stub
// resolver's raw error contains the internal DNS server's address.
func TestAuditCreateValidationErrorsAreAddressFree(t *testing.T) {
	ctx := context.Background()
	raws := []string{
		"http://127.0.0.1:8080/x",
		"http://10.1.2.3/x",
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.1/x",
		"http://[::1]/x",
		"http://[fe80::1%25eth0]/x",
		"http://[::ffff:10.0.0.1]/x",
		"http://localhost/x",
		"http://localhost./x",
		"http://0x7f000001/x",
		"http://2130706433/x",
		"http://0177.0.0.1/x",
		"http://user:pass@example.com/x",
		"http://example.com:99999/x",
		"not a url",
		"",
	}
	for _, raw := range raws {
		t.Run(raw, func(t *testing.T) {
			if err := webhook.ValidateWebhookURL(raw); err != nil {
				if leaks := auditScanLeaks(err.Error()); len(leaks) > 0 {
					t.Errorf("static validation error for %q leaks %v in %q", raw, leaks, err.Error())
				}
			}
			// Resolution half with a hostile (loopback-answering) resolver —
			// the same call the Create handler makes with the real resolver.
			res := &auditStubResolver{addrs: []string{"127.0.0.1"}}
			if _, err := webhook.ResolvePublicURL(ctx, res, raw); err != nil {
				if leaks := auditScanLeaks(err.Error()); len(leaks) > 0 {
					t.Errorf("resolution error for %q leaks %v in %q", raw, leaks, err.Error())
				}
			}
		})
	}

	// The resolver-failure case: raw resolver detail must be dropped.
	resErr := &auditStubResolver{err: errors.New("lookup rebind.test on 10.0.0.2:53: read udp 10.0.0.1:5353: i/o timeout")}
	_, err := webhook.ResolvePublicURL(ctx, resErr, "http://rebind.test/hook")
	if err == nil {
		t.Fatal("want error from failing resolver")
	}
	if leaks := auditScanLeaks(err.Error()); len(leaks) > 0 {
		t.Errorf("resolver-failure error leaks %v in %q", leaks, err.Error())
	}
	if got := err.Error(); got != "webhook destination could not be resolved" {
		t.Errorf("resolver-failure error = %q, want the canned resolve message", got)
	}
}

// auditStubResolver adapts string addresses to webhook.Resolver for the
// handler-package tests above.
type auditStubResolver struct {
	addrs []string
	err   error
}

func (r *auditStubResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]net.IPAddr, 0, len(r.addrs))
	for _, s := range r.addrs {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}
