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
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-2 W4 (ADR-002 decision 4) integration tests: the VERP return-path
// allowlist at RCPT, durable system-message ingest of DSN bounces, and the
// RFC 3464 match-back into email_events + the existing suppression chain.
// Self-contained fake database/sql driver (the verify_hook_test /
// route_action_test pattern) so no real Postgres is needed.

// ---------- fake driver state ----------

type vbState struct {
	mu    sync.Mutex
	seq   int64
	table map[string][]map[string]driver.Value
}

func newVbState() *vbState {
	return &vbState{seq: 500, table: map[string][]map[string]driver.Value{}}
}

func (s *vbState) addDomain(id, userID uint, wsID *uint, name string, verified bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := map[string]driver.Value{
		"id":                 int64(id),
		"user_id":            int64(userID),
		"domain":             name,
		"ownership_verified": verified,
		"created_at":         time.Now().UTC(),
	}
	if wsID != nil {
		row["workspace_id"] = int64(*wsID)
	}
	s.table["domains"] = append(s.table["domains"], row)
}

// addEmail seeds a sent outbound message the bounce address points at.
func (s *vbState) addEmail(id uint, uuid string, userID uint, wsID *uint, sender, recipient string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := map[string]driver.Value{
		"id":         int64(id),
		"uuid":       uuid,
		"user_id":    int64(userID),
		"sender":     sender,
		"recipients": "{" + recipient + "}",
		"status":     string(models.EmailStatusSent),
		"created_at": time.Now().UTC(),
	}
	if wsID != nil {
		row["workspace_id"] = int64(*wsID)
	}
	s.table["emails"] = append(s.table["emails"], row)
}

func (s *vbState) rows(t *testing.T, table string) []map[string]driver.Value {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]driver.Value, len(s.table[table]))
	copy(out, s.table[table])
	return out
}

func (s *vbState) rowCount(t *testing.T, table string) int {
	t.Helper()
	return len(s.rows(t, table))
}

func vbGorm(t *testing.T, state *vbState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(vbConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open bounce fake gorm: %v", err)
	}
	return gdb
}

type vbConnector struct{ state *vbState }

func (c vbConnector) Connect(context.Context) (driver.Conn, error) { return &vbConn{s: c.state}, nil }
func (c vbConnector) Driver() driver.Driver                        { return vbDrv{} }

type vbDrv struct{}

func (vbDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type vbConn struct{ s *vbState }

func (c *vbConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *vbConn) Close() error                        { return nil }
func (c *vbConn) Begin() (driver.Tx, error)           { return vbTx{}, nil }

type vbTx struct{}

func (vbTx) Commit() error   { return nil }
func (vbTx) Rollback() error { return nil }

func (c *vbConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := vbNormalize(query)
	switch {
	case strings.Contains(lower, "insert into"):
		return c.insert(query, args)
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "domains"`):
		return c.selectWhere(lower, args, "domains")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "emails"`):
		return c.selectWhere(lower, args, "emails")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "email_events"`):
		if strings.Contains(lower, "count(") {
			return c.countWhere(lower, args, "email_events")
		}
		return c.selectWhere(lower, args, "email_events")
	default:
		return nil, fmt.Errorf("vbState: cannot route query: %s", query)
	}
}

func (c *vbConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := vbNormalize(query)
	if strings.Contains(lower, "update ") {
		return c.execUpdate(lower, args)
	}
	if strings.Contains(lower, "insert into") {
		rows, err := c.insert(query, args)
		if err != nil {
			return nil, err
		}
		var n int64
		for {
			cols := rows.Columns()
			if err := rows.Next(make([]driver.Value, len(cols))); err != nil {
				break
			}
			n++
		}
		_ = rows.Close()
		return vbResult{affected: n}, nil
	}
	return nil, fmt.Errorf("vbState: unsupported exec: %s", query)
}

// execUpdate handles gorm's simple `UPDATE <table> SET col = $n, ... WHERE ...`
// statements (the Recorder's write-once status timestamps).
func (c *vbConn) execUpdate(lower string, args []driver.NamedValue) (driver.Result, error) {
	table := ""
	for _, t := range []string{"emails", "email_events", "inbound_emails", "bounces", "suppressions"} {
		if strings.Contains(lower, "update "+t) || strings.Contains(lower, `update "`+t+`"`) {
			table = t
		}
	}
	if table == "" {
		return nil, fmt.Errorf("vbState: unsupported update: %s", lower)
	}
	si := strings.Index(lower, " set ")
	wi := strings.Index(lower, " where ")
	if si < 0 || wi < 0 {
		return nil, fmt.Errorf("vbState: unsupported update shape: %s", lower)
	}
	setPart := lower[si+len(" set ") : wi]
	wherePart := lower[wi+len(" where "):]

	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	affected := int64(0)
	for _, row := range c.s.table[table] {
		if !vbMatchAll(row, wherePart, args) {
			continue
		}
		for _, pair := range strings.Split(setPart, ",") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 {
				continue
			}
			col := strings.Trim(strings.TrimSpace(kv[0]), `"`)
			ord, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(kv[1]), "$"))
			if err != nil {
				return nil, err
			}
			for _, a := range args {
				if a.Ordinal == ord {
					row[col] = vbCanonical(a.Value)
				}
			}
		}
		affected++
	}
	return vbResult{affected: affected}, nil
}

func (c *vbConn) insert(query string, args []driver.NamedValue) (driver.Rows, error) {
	table, cols, tuples, err := vbParseInsert(query, args)
	if err != nil {
		return nil, err
	}
	ret := vbReturningOf(query)
	conflict := strings.Contains(vbNormalize(query), "on conflict")

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	var out [][]driver.Value
	for _, vals := range tuples {
		row := map[string]driver.Value{}
		for j, col := range cols {
			row[col] = vbCanonical(vals[j])
		}
		if conflict {
			if vbInsertDuplicate(s.table[table], row) {
				continue
			}
		}
		s.seq++
		row["id"] = s.seq
		for _, col := range ret {
			if _, ok := row[col]; ok {
				continue
			}
			switch col {
			case "id":
				row[col] = s.seq
			case "uuid":
				row[col] = fmt.Sprintf("vb-fake-%d", s.seq)
			default:
				if v, ok := vbReturningDefaults[col]; ok {
					row[col] = v
				} else {
					row[col] = ""
				}
			}
		}
		s.table[table] = append(s.table[table], row)
		out = append(out, vbProject(row, ret))
	}
	return &vbRows{cols: ret, rows: out}, nil
}

// vbInsertDuplicate reports whether an ON CONFLICT DO NOTHING insert is a
// duplicate: email_events dedups on (email_uuid, type, provider_event_id),
// the same key the production unique index carries.
func vbInsertDuplicate(existing []map[string]driver.Value, row map[string]driver.Value) bool {
	for _, e := range existing {
		if fmt.Sprint(e["email_uuid"]) == fmt.Sprint(row["email_uuid"]) &&
			fmt.Sprint(e["type"]) == fmt.Sprint(row["type"]) &&
			fmt.Sprint(e["provider_event_id"]) == fmt.Sprint(row["provider_event_id"]) {
			return true
		}
	}
	return false
}

var vbReturningDefaults = map[string]driver.Value{
	"kind":         "hard",
	"retry_count":  int64(0),
	"route_action": "",
	"status":       string(models.InboundStatusReceived),
	"provider":     "",
}

func vbReturningOf(query string) []string {
	lower := strings.ToLower(query)
	i := strings.Index(lower, "returning")
	if i < 0 {
		return nil
	}
	var cols []string
	for _, p := range strings.Split(query[i+len("returning"):], ",") {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p != "" {
			cols = append(cols, p)
		}
	}
	return cols
}

func vbProject(row map[string]driver.Value, cols []string) []driver.Value {
	out := make([]driver.Value, len(cols))
	for i, c := range cols {
		out[i] = row[c]
	}
	return out
}

// vbParseInsert extracts the table, column list and bound values of gorm's
// single-tuple INSERT with $N bindvars.
func vbParseInsert(query string, args []driver.NamedValue) (string, []string, [][]driver.Value, error) {
	lower := strings.ToLower(query)
	ni := strings.Index(lower, "insert into")
	if ni < 0 {
		return "", nil, nil, fmt.Errorf("vbState: not an insert: %s", query)
	}
	rest := strings.TrimSpace(query[ni+len("insert into"):])
	end := strings.Index(rest, "(")
	if end < 0 {
		return "", nil, nil, fmt.Errorf("vbState: insert without columns: %s", query)
	}
	table := strings.Trim(strings.TrimSpace(rest[:end]), `"`)
	vi := strings.Index(lower, "values (")
	if vi < 0 {
		return table, nil, nil, fmt.Errorf("vbState: insert without VALUES: %s", query)
	}
	open := strings.LastIndex(query[:vi], "(")
	close := strings.LastIndex(query[:vi], ")")
	if open < 0 || close < open {
		return table, nil, nil, fmt.Errorf("vbState: insert without a column list: %s", query)
	}
	var cols []string
	for _, p := range strings.Split(query[open+1:close], ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(p), `"`))
	}

	valText := query[vi+len(" values "):]
	for _, cut := range []string{" on conflict", " returning"} {
		if i := strings.Index(strings.ToLower(valText), cut); i >= 0 {
			valText = valText[:i]
		}
	}
	valText = strings.TrimSpace(valText)
	valText = strings.TrimSuffix(strings.TrimPrefix(valText, "("), ")")

	var tuples [][]driver.Value
	for _, tuple := range strings.Split(valText, "),(") {
		parts := strings.Split(tuple, ",")
		if len(parts) != len(cols) {
			return table, nil, nil, fmt.Errorf("vbState: %d columns but %d values in %q", len(cols), len(parts), tuple)
		}
		vals := make([]driver.Value, len(parts))
		for i, p := range parts {
			p = strings.TrimSpace(p)
			if !strings.HasPrefix(p, "$") {
				return table, nil, nil, fmt.Errorf("vbState: non-bindvar value %q", p)
			}
			n, err := strconv.Atoi(p[1:])
			if err != nil {
				return table, nil, nil, err
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
				return table, nil, nil, fmt.Errorf("vbState: no arg with ordinal %d", n)
			}
		}
		tuples = append(tuples, vals)
	}
	return table, cols, tuples, nil
}

// selectWhere answers SELECT ... FROM <table> WHERE <equality conjunction>
// with an optional ORDER BY / LIMIT (ignored — callers use First over unique
// matches).
func (c *vbConn) selectWhere(lower string, args []driver.NamedValue, table string) (driver.Rows, error) {
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}

	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	var cols []string
	var out [][]driver.Value
	for _, row := range c.s.table[table] {
		if !vbMatchAll(row, where, args) {
			continue
		}
		if cols == nil {
			for _, k := range sortedKeys(row) {
				cols = append(cols, k)
			}
		}
		out = append(out, vbProject(row, cols))
	}
	return &vbRows{cols: cols, rows: out}, nil
}

// countWhere answers `SELECT count(*) FROM <table> WHERE ...`.
func (c *vbConn) countWhere(lower string, args []driver.NamedValue, table string) (driver.Rows, error) {
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	n := int64(0)
	for _, row := range c.s.table[table] {
		if vbMatchAll(row, where, args) {
			n++
		}
	}
	return &vbRows{cols: []string{"count"}, rows: [][]driver.Value{{n}}}, nil
}

func vbMatchAll(row map[string]driver.Value, where string, args []driver.NamedValue) bool {
	where = strings.TrimSpace(where)
	if where == "" {
		return true
	}
	for _, term := range strings.Split(where, " and ") {
		if !vbMatchTerm(row, strings.TrimSpace(term), args) {
			return false
		}
	}
	return true
}

func vbMatchTerm(row map[string]driver.Value, term string, args []driver.NamedValue) bool {
	term = strings.TrimSpace(term)
	if strings.HasSuffix(term, " is null") {
		col := strings.Trim(strings.TrimSpace(strings.TrimSuffix(term, " is null")), `"`)
		v, ok := row[col]
		return !ok || v == nil
	}
	i := strings.Index(term, "=")
	if i < 0 {
		return false
	}
	lhs := strings.TrimSpace(term[:i])
	rhs := strings.TrimSpace(term[i+1:])
	lhsLower := strings.HasPrefix(lhs, "lower(") && strings.HasSuffix(lhs, ")")
	if lhsLower {
		lhs = strings.TrimSuffix(strings.TrimPrefix(lhs, "lower("), ")")
	}
	rhsLower := strings.HasPrefix(rhs, "lower(") && strings.HasSuffix(rhs, ")")
	if rhsLower {
		rhs = strings.TrimSuffix(strings.TrimPrefix(rhs, "lower("), ")")
	}
	lhs = strings.Trim(lhs, `"`)

	var val driver.Value
	if strings.HasPrefix(rhs, "$") {
		n, err := strconv.Atoi(rhs[1:])
		if err != nil {
			return false
		}
		found := false
		for _, a := range args {
			if a.Ordinal == n {
				val, found = a.Value, true
				break
			}
		}
		if !found {
			return false
		}
	} else {
		val = strings.Trim(rhs, `'`)
	}

	rv, ok := row[lhs]
	if !ok {
		return false
	}
	if lhsLower {
		rv = strings.ToLower(fmt.Sprint(rv))
	}
	if rhsLower {
		val = strings.ToLower(fmt.Sprint(val))
	}
	return fmt.Sprint(rv) == fmt.Sprint(val)
}

func vbNormalize(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

func vbCanonical(v driver.Value) driver.Value {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC()
	default:
		return v
	}
}

func sortedKeys(row map[string]driver.Value) []string {
	out := make([]string, 0, len(row))
	for k := range row {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

type vbResult struct{ affected int64 }

func (r vbResult) LastInsertId() (int64, error) { return 0, nil }
func (r vbResult) RowsAffected() (int64, error) { return r.affected, nil }

type vbRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *vbRows) Columns() []string { return r.cols }
func (r *vbRows) Close() error      { return nil }
func (r *vbRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// ---------- fixtures ----------

const (
	vbTenantID    = uint(7)
	vbDomainID    = uint(1)
	vbEmailID     = uint(42)
	vbParentDom   = "mail.example.test"
	vbBounceDom   = "bounce.mail.example.test"
	vbSentSender  = "noreply@mail.example.test"
	vbSentRcpt    = "user@dest.example"
	vbUnknownUUID = "01234567-89ab-cdef-0123-456789abcdef"
)

func vbVerp() *Verp { return NewVerp(VerpConfig{Subdomain: "bounce", Secret: "vb-secret"}) }

func vbWs(v uint) *uint { return &v }

// vbFixture is the shared fixture: a verified parent domain, a sent message,
// and a service with the VERP codec and the DSN matcher wired.
type vbFixture struct {
	state *vbState
	gdb   *gorm.DB
	svc   *Service
	verp  *Verp
}

func vbNewFixture(t *testing.T) *vbFixture {
	t.Helper()
	state := newVbState()
	state.addDomain(vbDomainID, vbTenantID, vbWs(3), vbParentDom, true)
	state.addEmail(vbEmailID, verpTestUUID, vbTenantID, vbWs(3), vbSentSender, vbSentRcpt)

	gdb := vbGorm(t, state)
	verp := vbVerp()
	svc := NewService(
		repositories.NewInboundEmailRepository(gdb),
		repositories.NewDomainRepository(gdb),
		nil, // suppression repo: the DSN matcher owns its own
		Config{},
	)
	svc.SetVerp(verp)
	svc.EnableDSN(gdb)
	return &vbFixture{state: state, gdb: gdb, svc: svc, verp: verp}
}

func (f *vbFixture) smtpSession() *session {
	backend := &Backend{
		svc:        f.svc,
		domainRepo: repositories.NewDomainRepository(f.gdb),
		verp:       f.verp,
	}
	return &session{
		backend: backend,
		remote:  "192.0.2.10:4442",
		guard:   security.NewSessionGuard(nil, netip.MustParseAddr("192.0.2.10"), 2525, "vb-smtp-test"),
	}
}

func (f *vbFixture) bounceAddress(t *testing.T) string {
	t.Helper()
	addr, ok := f.verp.EnvelopeFrom(verpTestUUID, vbSentSender)
	if !ok {
		t.Fatalf("fixture could not build a bounce address")
	}
	return addr
}

func (f *vbFixture) unknownBounceAddress(t *testing.T) string {
	t.Helper()
	addr, ok := f.verp.EnvelopeFrom(vbUnknownUUID, vbSentSender)
	if !ok {
		t.Fatalf("fixture could not build an unknown-UUID bounce address")
	}
	return addr
}

func vbForgedAddress() string {
	// Well-formed shape, bogus signature.
	return "bounce+" + strings.Repeat("6", 32) + "=" + strings.Repeat("9", verpSigHexLen) + "@" + vbBounceDom
}

// vbBounceRaw builds a failed DSN bounce for the fixture's bounce address.
func vbBounceRaw(bounceAddr string, statusPart string) []byte {
	var b strings.Builder
	b.WriteString("From: MAILER-DAEMON@mx.remote.test\r\n")
	b.WriteString("To: " + bounceAddr + "\r\n")
	b.WriteString("Subject: Undelivered Mail Returned to Sender\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/report; report-type=delivery-status; boundary=\"=_vb\"\r\n")
	b.WriteString("\r\n")
	b.WriteString("--=_vb\r\nContent-Type: text/plain\r\n\r\nFailed.\r\n")
	b.WriteString("--=_vb\r\nContent-Type: message/delivery-status\r\n\r\n")
	b.WriteString(statusPart)
	if !strings.HasSuffix(statusPart, "\r\n") {
		b.WriteString("\r\n")
	}
	b.WriteString("--=_vb--\r\n")
	return []byte(b.String())
}

func vbFailedStatusPart() string {
	return strings.Join([]string{
		"Reporting-MTA: dns; mx.remote.test",
		"",
		"Original-Recipient: rfc822;" + vbSentRcpt,
		"Final-Recipient: rfc822;" + vbSentRcpt,
		"Action: failed",
		"Status: 5.1.1",
		"Diagnostic-Code: smtp; 550 5.1.1 user unknown",
		"",
	}, "\r\n")
}

func vbDeliveredStatusPart() string {
	return strings.Join([]string{
		"Reporting-MTA: dns; mx.remote.test",
		"",
		"Final-Recipient: rfc822;" + vbSentRcpt,
		"Action: delivered",
		"Status: 2.0.0",
		"",
	}, "\r\n")
}

func vbDelayedStatusPart() string {
	return strings.Join([]string{
		"Reporting-MTA: dns; mx.remote.test",
		"",
		"Final-Recipient: rfc822;" + vbSentRcpt,
		"Action: delayed",
		"Status: 4.4.1",
		"",
	}, "\r\n")
}

// ---------- RCPT allowlist ----------

// A signed bounce address is accepted through the return-path allowlist even
// though the bounce host is not a tenant mailbox domain.
func TestSMTPRcptAcceptsSignedBounceAddress(t *testing.T) {
	f := vbNewFixture(t)
	sess := f.smtpSession()

	if err := sess.Rcpt(f.bounceAddress(t), nil); err != nil {
		t.Fatalf("signed bounce address rejected: %v", err)
	}
	if len(sess.to) != 1 || sess.to[0] != f.bounceAddress(t) {
		t.Fatalf("bounce recipient not recorded: %v", sess.to)
	}
}

// A forged bounce address (bad signature) falls through to the ordinary
// chain and is rejected — the allowlist never accepts unverified addresses.
func TestSMTPRcptRefusesForgedBounceAddress(t *testing.T) {
	f := vbNewFixture(t)
	sess := f.smtpSession()

	err := sess.Rcpt(vbForgedAddress(), nil)
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 550 {
		t.Fatalf("err = %v, want 550 SMTPError", err)
	}
	if len(sess.to) != 0 {
		t.Fatalf("forged recipient must not be recorded: %v", sess.to)
	}
}

// A signed bounce address whose parent domain is not tenant-verified is
// rejected: routing resolves only through tenant-verified domains.
func TestSMTPRcptRefusesBounceOnUnverifiedParentDomain(t *testing.T) {
	f := vbNewFixture(t)
	sess := f.smtpSession()

	// A validly signed address for a parent domain no tenant verified.
	addr, ok := f.verp.EnvelopeFrom(verpTestUUID, "noreply@unverified.example.test")
	if !ok {
		t.Fatalf("fixture could not build an unverified-parent bounce address")
	}
	err := sess.Rcpt(addr, nil)
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 550 {
		t.Fatalf("err = %v, want 550 SMTPError", err)
	}
	if len(sess.to) != 0 {
		t.Fatalf("unverified-parent recipient must not be recorded: %v", sess.to)
	}
}

// With the feature off the receiver behaves exactly as before: a bounce-shaped
// address on a non-mailbox host gets the ordinary 550 relay-not-permitted.
func TestSMTPRcptFeatureOffRejectsBounceShape(t *testing.T) {
	f := vbNewFixture(t)
	f.svc.verp = nil
	sess := f.smtpSession()
	sess.backend.verp = nil

	err := sess.Rcpt(f.bounceAddress(t), nil)
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 550 {
		t.Fatalf("err = %v, want 550 SMTPError", err)
	}
}

// ---------- durable system-message ingest ----------

// A valid DSN bounce is durably ingested as a system message without any
// routing rule (bounces are not user mailboxes) and the parse task is queued.
func TestVerpBounceIngestDurablyStoresSystemMessage(t *testing.T) {
	f := vbNewFixture(t)
	addr := f.bounceAddress(t)

	rec, err := f.svc.IngestRaw(context.Background(), vbBounceRaw(addr, vbFailedStatusPart()),
		"MAILER-DAEMON@mx.remote.test", []string{addr}, models.InboundSourceSMTP, "192.0.2.10")
	if err != nil {
		t.Fatalf("ingest bounce: %v", err)
	}
	if rec == nil || rec.ID == 0 {
		t.Fatalf("bounce not durably persisted: %+v", rec)
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received", rec.Status)
	}
	if rec.RouteID != nil || rec.RouteAction != "" {
		t.Fatalf("system bounce must not carry a route stamp: route=%v action=%q", rec.RouteID, rec.RouteAction)
	}
	if got := f.state.rowCount(t, "inbound_emails"); got != 1 {
		t.Fatalf("inbound_emails rows = %d, want 1", got)
	}
}

// A forged bounce address fails validation at ingest (defense in depth — the
// RCPT layer already refused it) and nothing is persisted.
func TestVerpBounceForgedSignatureFallsThroughToReject(t *testing.T) {
	f := vbNewFixture(t)

	rec, err := f.svc.IngestRaw(context.Background(), []byte("Subject: x\r\n\r\nhi\r\n"),
		"MAILER-DAEMON@mx.remote.test", []string{vbForgedAddress()}, models.InboundSourceSMTP, "192.0.2.10")
	if !errors.Is(err, ErrUnverifiedDomain) {
		t.Fatalf("err = %v, want ErrUnverifiedDomain", err)
	}
	if rec != nil {
		t.Fatalf("forged bounce must not be persisted: %+v", rec)
	}
	if got := f.state.rowCount(t, "inbound_emails"); got != 0 {
		t.Fatalf("inbound_emails rows = %d, want 0", got)
	}
}

// With the feature off, bounce-shaped mail follows the ordinary path: the
// bounce host is not a verified domain, so ingest rejects it (zero behavior
// change).
func TestVerpBounceFeatureOffIsZeroBehaviorChange(t *testing.T) {
	f := vbNewFixture(t)
	f.svc.verp = nil
	f.svc.dsn = nil

	rec, err := f.svc.IngestRaw(context.Background(), vbBounceRaw(f.bounceAddress(t), vbFailedStatusPart()),
		"MAILER-DAEMON@mx.remote.test", []string{f.bounceAddress(t)}, models.InboundSourceSMTP, "192.0.2.10")
	if !errors.Is(err, ErrUnverifiedDomain) {
		t.Fatalf("err = %v, want ErrUnverifiedDomain", err)
	}
	if rec != nil || f.state.rowCount(t, "inbound_emails") != 0 {
		t.Fatalf("feature-off bounce must not be persisted")
	}
}

// ---------- DSN match-back ----------

// A failed DSN writes the bounce truth: an email_events bounced row on the
// original message, a hard Bounce row, and a suppression — the existing
// chain, unchanged.
func TestDSNMatchBackFailedWritesBouncedEventAndSuppression(t *testing.T) {
	f := vbNewFixture(t)
	addr := f.bounceAddress(t)

	if _, err := f.svc.IngestRaw(context.Background(), vbBounceRaw(addr, vbFailedStatusPart()),
		"MAILER-DAEMON@mx.remote.test", []string{addr}, models.InboundSourceSMTP, "192.0.2.10"); err != nil {
		t.Fatalf("ingest bounce: %v", err)
	}

	events := f.state.rows(t, "email_events")
	if len(events) != 1 {
		t.Fatalf("email_events rows = %d, want 1", len(events))
	}
	if events[0]["type"] != models.EmailEventBounced {
		t.Fatalf("event type = %v, want bounced", events[0]["type"])
	}
	if events[0]["email_uuid"] != verpTestUUID {
		t.Fatalf("event email_uuid = %v, want %s", events[0]["email_uuid"], verpTestUUID)
	}
	if events[0]["recipient"] != vbSentRcpt {
		t.Fatalf("event recipient = %v, want %s", events[0]["recipient"], vbSentRcpt)
	}
	if !strings.HasPrefix(fmt.Sprint(events[0]["provider_event_id"]), "dsn:"+models.EmailEventBounced+":") {
		t.Fatalf("provider_event_id = %v, want a dsn: key", events[0]["provider_event_id"])
	}

	bounces := f.state.rows(t, "bounces")
	if len(bounces) != 1 {
		t.Fatalf("bounces rows = %d, want 1", len(bounces))
	}
	if bounces[0]["recipient"] != vbSentRcpt || bounces[0]["type"] != string(models.BounceTypeHard) {
		t.Fatalf("bounce = recipient %v type %v, want %s/hard", bounces[0]["recipient"], bounces[0]["type"], vbSentRcpt)
	}
	suppressions := f.state.rows(t, "suppressions")
	if len(suppressions) != 1 {
		t.Fatalf("suppressions rows = %d, want 1", len(suppressions))
	}
	if suppressions[0]["email"] != vbSentRcpt {
		t.Fatalf("suppression email = %v, want %s", suppressions[0]["email"], vbSentRcpt)
	}
}

// Re-ingesting the same bounce (task replay) is a no-op on the match-back:
// one event, one bounce row, one suppression — idempotent per DSN record.
func TestDSNMatchBackIsIdempotentPerDSNRecord(t *testing.T) {
	f := vbNewFixture(t)
	addr := f.bounceAddress(t)
	raw := vbBounceRaw(addr, vbFailedStatusPart())

	for i := 0; i < 2; i++ {
		if _, err := f.svc.IngestRaw(context.Background(), raw,
			"MAILER-DAEMON@mx.remote.test", []string{addr}, models.InboundSourceSMTP, "192.0.2.10"); err != nil {
			t.Fatalf("ingest bounce (pass %d): %v", i+1, err)
		}
	}

	if got := f.state.rowCount(t, "email_events"); got != 1 {
		t.Fatalf("email_events rows = %d, want 1 after replay", got)
	}
	if got := f.state.rowCount(t, "bounces"); got != 1 {
		t.Fatalf("bounces rows = %d, want 1 after replay", got)
	}
	if got := f.state.rowCount(t, "suppressions"); got != 1 {
		t.Fatalf("suppressions rows = %d, want 1 after replay", got)
	}
}

// A delivered DSN records the delivery truth and touches nothing else.
func TestDSNMatchBackDeliveredWritesDeliveredEvent(t *testing.T) {
	f := vbNewFixture(t)
	addr := f.bounceAddress(t)

	if _, err := f.svc.IngestRaw(context.Background(), vbBounceRaw(addr, vbDeliveredStatusPart()),
		"MAILER-DAEMON@mx.remote.test", []string{addr}, models.InboundSourceSMTP, "192.0.2.10"); err != nil {
		t.Fatalf("ingest bounce: %v", err)
	}

	events := f.state.rows(t, "email_events")
	if len(events) != 1 {
		t.Fatalf("email_events rows = %d, want 1", len(events))
	}
	if events[0]["type"] != models.EmailEventDelivered {
		t.Fatalf("event type = %v, want delivered", events[0]["type"])
	}
	if got := f.state.rowCount(t, "bounces"); got != 0 {
		t.Fatalf("bounces rows = %d, want 0 (delivered is not a bounce)", got)
	}
	if got := f.state.rowCount(t, "suppressions"); got != 0 {
		t.Fatalf("suppressions rows = %d, want 0 (delivered never suppresses)", got)
	}
}

// A delayed DSN is transient: no timeline event and no suppression — the
// report stays on the durable inbound record only.
func TestDSNMatchBackDelayedWritesNoEvents(t *testing.T) {
	f := vbNewFixture(t)
	addr := f.bounceAddress(t)

	if _, err := f.svc.IngestRaw(context.Background(), vbBounceRaw(addr, vbDelayedStatusPart()),
		"MAILER-DAEMON@mx.remote.test", []string{addr}, models.InboundSourceSMTP, "192.0.2.10"); err != nil {
		t.Fatalf("ingest bounce: %v", err)
	}

	if got := f.state.rowCount(t, "email_events"); got != 0 {
		t.Fatalf("email_events rows = %d, want 0 (delayed is not delivery truth)", got)
	}
	if got := f.state.rowCount(t, "bounces"); got != 0 {
		t.Fatalf("bounces rows = %d, want 0", got)
	}
	if got := f.state.rowCount(t, "suppressions"); got != 0 {
		t.Fatalf("suppressions rows = %d, want 0", got)
	}
	if got := f.state.rowCount(t, "inbound_emails"); got != 1 {
		t.Fatalf("inbound_emails rows = %d, want 1 (the bounce itself is durable)", got)
	}
}

// A signed bounce whose UUID resolves to no known message is stored as an
// ordinary inbound record without any match-back side effects.
func TestDSNMatchBackUnknownBounceUUIDIsNoop(t *testing.T) {
	f := vbNewFixture(t)
	addr := f.unknownBounceAddress(t)

	if _, err := f.svc.IngestRaw(context.Background(), vbBounceRaw(addr, vbFailedStatusPart()),
		"MAILER-DAEMON@mx.remote.test", []string{addr}, models.InboundSourceSMTP, "192.0.2.10"); err != nil {
		t.Fatalf("ingest bounce: %v", err)
	}

	if got := f.state.rowCount(t, "inbound_emails"); got != 1 {
		t.Fatalf("inbound_emails rows = %d, want 1 (the bounce is durable regardless)", got)
	}
	if got := f.state.rowCount(t, "email_events"); got != 0 {
		t.Fatalf("email_events rows = %d, want 0", got)
	}
	if got := f.state.rowCount(t, "suppressions"); got != 0 {
		t.Fatalf("suppressions rows = %d, want 0", got)
	}
}
