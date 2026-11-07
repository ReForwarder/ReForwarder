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

package inbound

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The verification hook faces two things worth pinning with tests: the
// engines run against fake DNS and their verdict persists into
// inbound_auth_results idempotently (one row per inbound message, first
// verdict wins), and POSTA_INBOUND_VERIFY=false switches the whole step off.
// Both run through the real gorm on a fake database/sql driver (the
// dmarc_test.go pattern).

var arColumns = []string{"id", "user_id", "workspace_id", "inbound_id", "message_id",
	"client_ip", "envelope_from", "spf_result", "spf_domain", "spf_reason",
	"dkim_result", "dkim_domain", "dkim_selector", "dmarc_result", "dmarc_disposition",
	"dmarc_policy_domain", "dmarc_policy", "arc_result", "arc_domain",
	"iprev_result", "iprev_ptr", "auth_results_header", "created_at"}

type arState struct {
	mu   sync.Mutex
	rows [][]driver.Value
	seq  int64
}

func newArState() *arState { return &arState{seq: 900} }

func (s *arState) colIdx(col string) int {
	for i, c := range arColumns {
		if c == col {
			return i
		}
	}
	return -1
}

func (s *arState) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// value returns one column of the fixture row — every test in this file
// stores exactly one verdict, for inbound id 33.
func (s *arState) value(col string) driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row[s.colIdx("inbound_id")] == int64(33) {
			return row[s.colIdx(col)]
		}
	}
	return nil
}

func arGorm(t *testing.T, state *arState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(arConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open auth-results fake gorm: %v", err)
	}
	return gdb
}

type arConnector struct{ state *arState }

func (c arConnector) Connect(context.Context) (driver.Conn, error) { return &arConn{s: c.state}, nil }
func (c arConnector) Driver() driver.Driver                        { return arDrv{} }

type arDrv struct{}

func (arDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type arConn struct{ s *arState }

func (c *arConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *arConn) Close() error                        { return nil }
func (c *arConn) Begin() (driver.Tx, error)           { return arTx{}, nil }

type arTx struct{}

func (arTx) Commit() error   { return nil }
func (arTx) Rollback() error { return nil }

func (c *arConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	switch {
	case strings.Contains(lower, `insert into "inbound_auth_results"`):
		return c.insert(query, args)
	case strings.Contains(lower, "select") && strings.Contains(lower, `"inbound_auth_results"`):
		return c.selectRow(lower, args)
	default:
		return nil, fmt.Errorf("arState: cannot route query: %s", query)
	}
}

// insert answers INSERT ... ON CONFLICT DO NOTHING RETURNING "id": tuples are
// deduplicated on inbound_id, conflicting tuples produce no RETURNING row.
func (c *arConn) insert(query string, args []driver.NamedValue) (driver.Rows, error) {
	cols, tuples, err := arParseInsert(query, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	var out [][]driver.Value
	for _, vals := range tuples {
		row := make([]driver.Value, len(arColumns))
		for i := range row {
			row[i] = nil
		}
		for j, col := range cols {
			idx := s.colIdx(col)
			if idx < 0 {
				return nil, fmt.Errorf("arState: inbound_auth_results has no column %q", col)
			}
			row[idx] = arCanonical(vals[j])
		}
		dup := false
		for _, existing := range s.rows {
			if existing[s.colIdx("inbound_id")] == row[s.colIdx("inbound_id")] {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		s.seq++
		row[s.colIdx("id")] = s.seq
		s.rows = append(s.rows, row)
		out = append(out, []driver.Value{row[s.colIdx("id")]})
	}
	return &arRows{cols: []string{"id"}, rows: out}, nil
}

// selectRow answers the storedHeader() lookup:
// SELECT auth_results_header ... WHERE inbound_id = $1 ... LIMIT n.
func (c *arConn) selectRow(lower string, args []driver.NamedValue) (driver.Rows, error) {
	id, err := arWhereInboundID(lower, args)
	if err != nil {
		return nil, err
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row[s.colIdx("inbound_id")] == id {
			return &arRows{cols: []string{"auth_results_header"}, rows: [][]driver.Value{{row[s.colIdx("auth_results_header")]}}}, nil
		}
	}
	return &arRows{cols: []string{"auth_results_header"}}, nil
}

func (c *arConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.QueryContext(ctx, query, args); err != nil {
		return nil, err
	}
	return arResult{}, nil
}

type arResult struct{}

func (arResult) LastInsertId() (int64, error) { return 0, nil }
func (arResult) RowsAffected() (int64, error) { return 0, nil }

type arRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *arRows) Columns() []string { return r.cols }
func (r *arRows) Close() error      { return nil }
func (r *arRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// arParseInsert extracts the column list and per-tuple bound values of the
// single-row INSERT gorm issues (bindvars are $N tokens only).
func arParseInsert(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
	lower := strings.ToLower(query)
	vi := strings.Index(lower, "values (")
	if vi < 0 {
		return nil, nil, fmt.Errorf("arState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return nil, nil, fmt.Errorf("arState: insert without a column list: %s", query)
	}
	var cols []string
	for _, p := range strings.Split(query[open+1:close], ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}

	valText := query[vi+len(" values "):]
	if i := strings.Index(strings.ToLower(valText), " on conflict"); i >= 0 {
		valText = valText[:i]
	}
	valText = strings.TrimSpace(valText)
	valText = strings.TrimSuffix(strings.TrimPrefix(valText, "("), ")")

	var tuples [][]driver.Value
	for _, tuple := range strings.Split(valText, "),(") {
		parts := strings.Split(tuple, ",")
		if len(parts) != len(cols) {
			return nil, nil, fmt.Errorf("arState: %d columns but %d values in %q", len(cols), len(parts), tuple)
		}
		vals := make([]driver.Value, len(parts))
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(p, "$") {
				return nil, nil, fmt.Errorf("arState: non-bindvar value %q", p)
			}
			n, err := strconv.Atoi(p[1:])
			if err != nil {
				return nil, nil, err
			}
			found := false
			for _, a := range args {
				if a.Ordinal == n {
					vals[i] = a.Value
					found = true
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("arState: no arg with ordinal %d", n)
			}
		}
		tuples = append(tuples, vals)
	}
	return cols, tuples, nil
}

// arWhereInboundID extracts the bound value of "inbound_id = $N" from the
// normalized SQL text.
func arWhereInboundID(lower string, args []driver.NamedValue) (int64, error) {
	i := strings.Index(lower, "inbound_id = $")
	if i < 0 {
		return 0, fmt.Errorf("arState: select without inbound_id filter: %s", lower)
	}
	rest := lower[i+len("inbound_id = $"):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, err
	}
	for _, a := range args {
		if a.Ordinal == n {
			switch v := a.Value.(type) {
			case int64:
				return v, nil
			case uint:
				return int64(v), nil
			default:
				return 0, fmt.Errorf("arState: unexpected inbound_id arg type %T", a.Value)
			}
		}
	}
	return 0, fmt.Errorf("arState: no arg with ordinal %d", n)
}

func arCanonical(v driver.Value) driver.Value {
	switch t := v.(type) {
	case uint:
		return int64(t)
	case uint64:
		return int64(t)
	case int:
		return int64(t)
	default:
		return v
	}
}

// ---------- fixtures ----------

var arFixtureRaw = []byte("From: Alice <alice@example.test>\r\n" +
	"To: bob@recipient.test\r\nSubject: verification fixture\r\n\r\nHello\r\n")

func arTestInbound() *models.InboundEmail {
	ws := uint(3)
	return &models.InboundEmail{ID: 33, UUID: "inb-ar-1", UserID: 7, WorkspaceID: &ws, MessageID: "m-ar-1"}
}

func arDNSWith(spfRecord, dmarcRecord string) *arFakeDNS {
	return newArFakeDNS(spfRecord, dmarcRecord)
}

func arProcessor(t *testing.T, gdb *gorm.DB, dns *arFakeDNS) *verifyProcessor {
	t.Helper()
	return &verifyProcessor{
		db:         gdb,
		authservID: "posta.test",
		enabled:    true,
		lookupTXT:  dns.LookupTXT,
		lookupIP:   dns.LookupIP,
		lookupAddr: dns.LookupAddr,
		lookupMX:   dns.LookupMX,
	}
}

// The full synchronous path: engines evaluate over fake DNS and the verdict
// lands in inbound_auth_results; re-running verification on the same message
// adds nothing (idempotent persistence, unique inbound_id).
func TestInboundVerifyPersistsVerdictIdempotently(t *testing.T) {
	state := newArState()
	dns := arDNSWith(
		"v=spf1 ip4:192.0.2.0/24 -all",                    // SPF record for example.test
		"v=DMARC1; p=reject; rua=mailto:agg@example.test", // DMARC policy
	)
	p := arProcessor(t, arGorm(t, state), dns)
	rec := arTestInbound()

	p.run(rec, arFixtureRaw, "alice@example.test", "192.0.2.10")
	p.run(rec, arFixtureRaw, "alice@example.test", "192.0.2.10")

	if got := state.count(); got != 1 {
		t.Fatalf("two verifications stored %d rows, want 1 (idempotent)", got)
	}
	checks := map[string]driver.Value{
		"spf_result":        "pass",
		"spf_domain":        "example.test",
		"dkim_result":       "none",
		"dmarc_result":      "pass",
		"dmarc_disposition": "none",
		"dmarc_policy":      "reject",
		"user_id":           int64(7),
		"workspace_id":      int64(3),
		"message_id":        "m-ar-1",
		"client_ip":         "192.0.2.10",
		"envelope_from":     "alice@example.test",
	}
	for col, want := range checks {
		if got := state.value(col); got != want {
			t.Errorf("inbound_auth_results.%s = %v, want %v", col, got, want)
		}
	}
	header, _ := state.value("auth_results_header").(string)
	for _, want := range []string{"posta.test;", "spf=pass smtp.mailfrom=example.test", "dmarc=pass header.from=example.test"} {
		if !strings.Contains(header, want) {
			t.Errorf("auth_results_header %q missing %q", header, want)
		}
	}
}

// A DMARC failure records the RECOMMENDATION (reject) — nothing executes it,
// the row is the input the ADR-002 routing engine will consume.
func TestInboundVerifyRecordsRejectRecommendation(t *testing.T) {
	state := newArState()
	dns := arDNSWith(
		"v=spf1 ip4:192.0.2.0/24 -all",
		"v=DMARC1; p=reject",
	)
	p := arProcessor(t, arGorm(t, state), dns)

	p.run(arTestInbound(), arFixtureRaw, "alice@example.test", "198.51.100.9")

	if got := state.count(); got != 1 {
		t.Fatalf("stored %d rows, want 1", got)
	}
	for col, want := range map[string]driver.Value{
		"spf_result":          "fail",
		"dmarc_result":        "fail",
		"dmarc_disposition":   "reject",
		"dmarc_policy":        "reject",
		"dmarc_policy_domain": "example.test",
	} {
		if got := state.value(col); got != want {
			t.Errorf("inbound_auth_results.%s = %v, want %v", col, got, want)
		}
	}
	header, _ := state.value("auth_results_header").(string)
	if !strings.Contains(header, "spf=fail smtp.mailfrom=example.test") ||
		!strings.Contains(header, "dmarc=fail") {
		t.Errorf("auth_results_header %q does not carry the failure verdict", header)
	}
}

// storedHeader hands the persisted A-R value back to the parse flow, and
// withHeader merges it without touching an upstream MTA's header.
func TestInboundVerifyStoredHeaderRoundTrip(t *testing.T) {
	state := newArState()
	dns := arDNSWith("v=spf1 ip4:192.0.2.0/24 -all", "v=DMARC1; p=none")
	gdb := arGorm(t, state)
	p := arProcessor(t, gdb, dns)
	rec := arTestInbound()

	if got := p.storedHeader(rec.ID); got != "" {
		t.Fatalf("storedHeader before verification = %q, want empty", got)
	}
	p.run(rec, arFixtureRaw, "alice@example.test", "192.0.2.10")

	got := p.storedHeader(rec.ID)
	if !strings.HasPrefix(got, "posta.test; spf=pass") {
		t.Fatalf("storedHeader = %q, want the composed A-R value", got)
	}

	merged := withHeader(map[string]string{"From": "alice@example.test"}, "Authentication-Results", got)
	if merged["Authentication-Results"] != got {
		t.Fatalf("withHeader lost the value: %v", merged)
	}
	if merged["From"] != "alice@example.test" {
		t.Fatalf("withHeader dropped existing keys: %v", merged)
	}

	// An upstream MTA's header is never replaced (RFC 8601 append-only).
	upstream := map[string]string{"Authentication-Results": "other-mta.example; spf=pass"}
	out := withHeader(upstream, "Authentication-Results", got)
	if out["Authentication-Results"] != "other-mta.example; spf=pass" {
		t.Fatalf("withHeader overwrote the upstream header: %v", out)
	}
}

// POSTA_INBOUND_VERIFY=false switches the whole step off: no DNS, no rows,
// no goroutine.
func TestInboundVerifyDisabledByEnv(t *testing.T) {
	t.Setenv("POSTA_INBOUND_VERIFY", "false")
	state := newArState()
	gdb := arGorm(t, state)

	svc := &Service{}
	svc.EnableVerification(gdb, "posta.test")
	if svc.verifier == nil || svc.verifier.enabled {
		t.Fatalf("verification must be disabled under POSTA_INBOUND_VERIFY=false")
	}

	dns := arDNSWith("v=spf1 -all", "v=DMARC1; p=reject")
	svc.verifier.lookupTXT = dns.LookupTXT
	svc.verifier.lookupIP = dns.LookupIP
	svc.verifier.lookupAddr = dns.LookupAddr
	svc.verifier.lookupMX = dns.LookupMX

	svc.processVerification(arTestInbound(), arFixtureRaw, "alice@example.test", "192.0.2.10")
	time.Sleep(50 * time.Millisecond)
	if got := state.count(); got != 0 {
		t.Fatalf("disabled verification stored %d rows, want 0", got)
	}
}

// Default (no env): verification is on, and an empty authserv-id falls back
// to the machine hostname.
func TestEnableVerificationDefaults(t *testing.T) {
	t.Setenv("POSTA_INBOUND_VERIFY", "true")
	svc := &Service{}
	svc.EnableVerification(arGorm(t, newArState()), "")
	if svc.verifier == nil || !svc.verifier.enabled {
		t.Fatal("verification must default to enabled")
	}
	if strings.TrimSpace(svc.verifier.authservID) == "" {
		t.Fatal("empty authserv-id must fall back to the machine hostname")
	}

	// Explicit id wins.
	svc2 := &Service{}
	svc2.EnableVerification(arGorm(t, newArState()), "mx.example.test")
	if svc2.verifier.authservID != "mx.example.test" {
		t.Fatalf("authservID = %q, want mx.example.test", svc2.verifier.authservID)
	}
}

// No raw message content → nothing to verify, no row, even when enabled.
func TestInboundVerifySkipsWithoutRaw(t *testing.T) {
	state := newArState()
	dns := arDNSWith("v=spf1 -all", "v=DMARC1; p=reject")
	p := arProcessor(t, arGorm(t, state), dns)

	p.run(arTestInbound(), nil, "alice@example.test", "192.0.2.10")
	if got := state.count(); got != 0 {
		t.Fatalf("empty raw stored %d rows, want 0", got)
	}
}

// The asynchronous service-level hook: processVerification spawns the work
// and the row appears without blocking the caller (race-detector exercised).
func TestInboundVerifyAsyncHookPersists(t *testing.T) {
	state := newArState()
	dns := arDNSWith("v=spf1 ip4:192.0.2.0/24 -all", "v=DMARC1; p=none")
	gdb := arGorm(t, state)

	svc := &Service{}
	svc.EnableVerification(gdb, "posta.test")
	svc.verifier.lookupTXT = dns.LookupTXT
	svc.verifier.lookupIP = dns.LookupIP
	svc.verifier.lookupAddr = dns.LookupAddr
	svc.verifier.lookupMX = dns.LookupMX

	svc.processVerification(arTestInbound(), arFixtureRaw, "alice@example.test", "192.0.2.10")

	deadline := time.Now().Add(3 * time.Second)
	for state.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := state.count(); got != 1 {
		t.Fatalf("async verification stored %d rows, want 1", got)
	}
}

// A nil verifier (EnableVerification never called) is a no-op, matching the
// pre-verification behavior.
func TestInboundVerifyNilVerifierIsNoop(t *testing.T) {
	svc := &Service{}
	svc.processVerification(arTestInbound(), arFixtureRaw, "alice@example.test", "192.0.2.10") // must not panic
}

// ---------- ARC / iprev gating + persistence (ADR-002 decision 6) ----------

// Both optional engines are OFF by default: the row keeps the empty "not
// evaluated" markers and the composed header carries no arc=/iprev= fields —
// byte-identical to the pre-feature value.
func TestInboundVerifyARCIPRevDisabledByDefault(t *testing.T) {
	state := newArState()
	dns := arDNSWith("v=spf1 ip4:192.0.2.0/24 -all", "v=DMARC1; p=none")
	p := arProcessor(t, arGorm(t, state), dns)
	if p.arcEnabled || p.iprevEnabled {
		t.Fatal("ARC/iprev must default to disabled")
	}

	p.run(arTestInbound(), arFixtureRaw, "alice@example.test", "192.0.2.10")
	if got := state.count(); got != 1 {
		t.Fatalf("stored %d rows, want 1", got)
	}
	for col, want := range map[string]driver.Value{
		"arc_result":   "",
		"arc_domain":   "",
		"iprev_result": "",
		"iprev_ptr":    "",
	} {
		if got := state.value(col); got != want {
			t.Errorf("inbound_auth_results.%s = %v, want %q (not evaluated)", col, got, want)
		}
	}
	header, _ := state.value("auth_results_header").(string)
	if strings.Contains(header, "; arc=") || strings.Contains(header, "; iprev=") {
		t.Errorf("disabled engines annotated the header: %q", header)
	}
}

// Enabled engines with a plain (chain-free) message: arc=none is an
// evaluated verdict, iprev passes over the fake PTR data, and the columns
// persist both.
func TestInboundVerifyARCIPRevEnabledAnnotates(t *testing.T) {
	state := newArState()
	dns := arDNSWith("v=spf1 ip4:192.0.2.0/24 -all", "v=DMARC1; p=none")
	dns.ptr["192.0.2.10"] = []string{"mail.example.test."}
	dns.ip["mail.example.test."] = []net.IP{net.ParseIP("192.0.2.10")}

	svc := &Service{}
	t.Setenv("POSTA_INBOUND_ARC_ENABLED", "true")
	t.Setenv("POSTA_INBOUND_IPREV_ENABLED", "true")
	svc.EnableVerification(arGorm(t, state), "posta.test")
	svc.verifier.lookupTXT = dns.LookupTXT
	svc.verifier.lookupIP = dns.LookupIP
	svc.verifier.lookupAddr = dns.LookupAddr
	svc.verifier.lookupMX = dns.LookupMX
	if !svc.verifier.arcEnabled || !svc.verifier.iprevEnabled {
		t.Fatal("ARC/iprev must be enabled by their env flags")
	}

	svc.verifier.run(arTestInbound(), arFixtureRaw, "alice@example.test", "192.0.2.10")
	if got := state.count(); got != 1 {
		t.Fatalf("stored %d rows, want 1", got)
	}
	for col, want := range map[string]driver.Value{
		"arc_result":   "none", // evaluated: the fixture carries no ARC set
		"iprev_result": "pass",
		"iprev_ptr":    "mail.example.test",
	} {
		if got := state.value(col); got != want {
			t.Errorf("inbound_auth_results.%s = %v, want %q", col, got, want)
		}
	}
	header, _ := state.value("auth_results_header").(string)
	for _, want := range []string{"arc=none", "iprev=pass policy.iprev.ptr=mail.example.test"} {
		if !strings.Contains(header, want) {
			t.Errorf("auth_results_header %q missing %q", header, want)
		}
	}
}

// An enabled ARC engine over a malformed chain annotates arc=fail (the
// crypto round-trips live in the verify package tests; this pins the wiring
// through the hook and the row).
func TestInboundVerifyARCEnabledBrokenChainAnnotatesFail(t *testing.T) {
	state := newArState()
	dns := arDNSWith("v=spf1 ip4:192.0.2.0/24 -all", "v=DMARC1; p=none")
	p := arProcessor(t, arGorm(t, state), dns)
	p.arcEnabled = true

	// An ARC-Seal missing its cv= tag breaks the chain structure.
	broken := []byte("ARC-Seal: i=1; a=ed25519-sha256; d=forwarder.test; s=k; b=AAAA\r\n" +
		string(arFixtureRaw))

	p.run(arTestInbound(), broken, "alice@example.test", "192.0.2.10")
	if got := state.count(); got != 1 {
		t.Fatalf("stored %d rows, want 1", got)
	}
	if got := state.value("arc_result"); got != "fail" {
		t.Errorf("arc_result = %v, want fail", got)
	}
	header, _ := state.value("auth_results_header").(string)
	if !strings.Contains(header, "arc=fail") {
		t.Errorf("auth_results_header %q missing arc=fail", header)
	}
}

// Explicitly spelled-off flags keep the engines off.
func TestInboundVerifyARCIPRevExplicitOff(t *testing.T) {
	t.Setenv("POSTA_INBOUND_ARC_ENABLED", "false")
	t.Setenv("POSTA_INBOUND_IPREV_ENABLED", "0")
	svc := &Service{}
	svc.EnableVerification(arGorm(t, newArState()), "posta.test")
	if svc.verifier.arcEnabled || svc.verifier.iprevEnabled {
		t.Fatal("false/0 must keep ARC/iprev disabled")
	}
}

// ---------- fake DNS for the hook tests ----------

type arFakeDNS struct {
	txt map[string][]string
	ip  map[string][]net.IP
	ptr map[string][]string
}

func newArFakeDNS(spfRecord, dmarcRecord string) *arFakeDNS {
	return &arFakeDNS{
		txt: map[string][]string{
			"example.test":        {spfRecord},
			"_dmarc.example.test": {dmarcRecord},
		},
		ip:  map[string][]net.IP{},
		ptr: map[string][]string{},
	}
}

func (f *arFakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if recs, ok := f.txt[name]; ok {
		return recs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *arFakeDNS) LookupIP(_ context.Context, name string) ([]net.IP, error) {
	if addrs, ok := f.ip[name]; ok {
		return addrs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *arFakeDNS) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if names, ok := f.ptr[addr]; ok {
		return names, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: addr, IsNotFound: true}
}

func (f *arFakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}
