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

package email

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-2 (ADR-002 decision 2): SendSystemForward — the system path the inbound
// forwarding executor hands copies to. These tests prove, at runtime, that
// the created row carries the system_forward provenance stamp, that the
// forwarding is recorded as a "forwarded" email_events row, and that the
// path never touches the consent layers (no suppression/unsubscribe lookups)
// — all over a fake database/sql driver, so they run in the plain battery.

// sfState captures every statement the fake driver sees, with args.
type sfState struct {
	mu          sync.Mutex
	inserts     []sfInsert // per-table inserts with their bound values
	selects     []string   // tables every SELECT touched
	returningID int64
}

type sfInsert struct {
	table  string
	values map[string]driver.Value
}

func (s *sfState) recordInsert(table string, values map[string]driver.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inserts = append(s.inserts, sfInsert{table: table, values: values})
}

func (s *sfState) insertsFor(table string) []sfInsert {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sfInsert
	for _, ins := range s.inserts {
		if ins.table == table {
			out = append(out, ins)
		}
	}
	return out
}

func (s *sfState) tables() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, ins := range s.inserts {
		out = append(out, ins.table)
	}
	return out
}

// sfConnector answers the handful of statements SendSystemForward issues and
// captures the bound INSERT values (the provenance assertion is on the actual
// bound value, not on source code).
type sfConnector struct{ state *sfState }

func (c sfConnector) Connect(context.Context) (driver.Conn, error) {
	return &sfConn{state: c.state}, nil
}
func (c sfConnector) Driver() driver.Driver { return sfDrv{} }

type sfDrv struct{}

func (sfDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type sfConn struct{ state *sfState }

func (c *sfConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *sfConn) Close() error                        { return nil }
func (c *sfConn) Begin() (driver.Tx, error)           { return sfTx{}, nil }

type sfTx struct{}

func (sfTx) Commit() error   { return nil }
func (sfTx) Rollback() error { return nil }

type sfResult struct{}

func (sfResult) LastInsertId() (int64, error) { return 1, nil }
func (sfResult) RowsAffected() (int64, error) { return 1, nil }

func (c *sfConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if table, values, ok := sfParseInsert(query, args); ok {
		c.state.recordInsert(table, values)
	}
	return sfResult{}, nil
}

func (c *sfConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	if table, values, ok := sfParseInsert(query, args); ok {
		c.state.recordInsert(table, values)
		// gorm reads the RETURNING columns; answer id (and any other column)
		// with the canned id so the caller sees a real row identity.
		cols := sfReturningOf(query)
		rows := make([][]driver.Value, 1)
		for range cols {
			rows[0] = append(rows[0], c.state.returningID)
		}
		if len(cols) == 0 {
			cols = []string{"id"}
			rows[0] = []driver.Value{c.state.returningID}
		}
		return &sfRows{cols: cols, rows: rows}, nil
	}
	if strings.Contains(lower, "select") {
		table := sfTableName(query)
		c.state.mu.Lock()
		c.state.selects = append(c.state.selects, table)
		c.state.mu.Unlock()
	}
	// Empty result for every plain SELECT (no SMTP servers, no subscriptions).
	return &sfRows{cols: []string{"id"}}, nil
}

// sfTableName extracts the first quoted table name from a statement.
func sfTableName(query string) string {
	i := strings.Index(query, `"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(query[i+1:], `"`)
	if j < 0 {
		return ""
	}
	return query[i+1 : i+1+j]
}

// sfParseInsert extracts the table and the column→value map of a single-row
// INSERT (gorm binds one arg per $N placeholder, in column order).
func sfParseInsert(query string, args []driver.NamedValue) (string, map[string]driver.Value, bool) {
	upper := strings.ToUpper(query)
	if !strings.Contains(upper, "INSERT INTO") {
		return "", nil, false
	}
	table := sfTableName(query)
	open := strings.Index(query, "(")
	closeIdx := strings.Index(query, ")")
	if open < 0 || closeIdx < open {
		return "", nil, false
	}
	colPart := query[open+1 : closeIdx]
	var cols []string
	for _, c := range strings.Split(colPart, ",") {
		col := strings.Trim(strings.TrimSpace(c), `"`)
		if col != "" {
			cols = append(cols, col)
		}
	}
	values := map[string]driver.Value{}
	for i, col := range cols {
		if i < len(args) {
			values[col] = args[i].Value
		}
	}
	return table, values, true
}

// sfReturningOf parses the RETURNING column list.
func sfReturningOf(query string) []string {
	lower := strings.ToLower(query)
	i := strings.Index(lower, "returning")
	if i < 0 {
		return nil
	}
	var cols []string
	for _, p := range strings.Split(query[i+len("returning"):], ",") {
		col := strings.Trim(strings.TrimSpace(p), `"`)
		if col != "" && !strings.Contains(col, " ") {
			cols = append(cols, col)
		}
	}
	return cols
}

type sfRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *sfRows) Columns() []string { return r.cols }
func (r *sfRows) Close() error      { return nil }
func (r *sfRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func sfGorm(t *testing.T, state *sfState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(sfConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open system-forward fake gorm: %v", err)
	}
	return gdb
}

// TestSendSystemForwardStampsProvenanceAndRecordsForwardedEvent proves the
// two runtime guarantees of ADR-002 decision 2 that live in the email
// service: the created row is stamped provenance=system_forward, and a
// "forwarded" email_events row is recorded for the copy — while no consent
// table (suppressions, unsubscribe lists) is ever touched.
func TestSendSystemForwardStampsProvenanceAndRecordsForwardedEvent(t *testing.T) {
	state := &sfState{returningID: 77}
	db := sfGorm(t, state)
	enq := &stubEnqueuer{}
	svc := NewService(repositories.NewEmailRepository(db), nil, nil, nil, nil, nil, false)
	svc.SetEnqueuer(enq)
	svc.SetRecorder(emailhistory.NewRecorder(db))

	resp, err := svc.SendSystemForward(context.Background(), &SystemForwardRequest{
		UserID:      11,
		WorkspaceID: nil,
		From:        "inbox@lists.example",
		To:          []string{"friend@external.test"},
		Subject:     "Fwd: Hello",
		Text:        "body",
		Headers: map[string]string{
			"Message-ID":             "<forward-1@lists.example>",
			"X-Posta-Forwarded-For":  "friend@external.test",
			"X-Posta-Forwarded-From": "someone@example.org",
		},
		Metadata: map[string]any{"inbound_id": "inb-1", "hops": 1},
	})
	if err != nil {
		t.Fatalf("SendSystemForward: %v", err)
	}
	if resp.Status != models.EmailStatusQueued {
		t.Errorf("status = %q, want queued (enqueued to the standard pipeline)", resp.Status)
	}
	if len(enq.enqueued) != 1 || enq.enqueued[0] != 77 {
		t.Errorf("enqueued = %v, want [77] (the created copy's row id)", enq.enqueued)
	}

	// The emails INSERT carries the provenance stamp. driver.Value arrives
	// via database/sql's converter, so named string types may surface as
	// plain strings — compare through fmt.Sprint, never by interface identity.
	emailInserts := state.insertsFor("emails")
	if len(emailInserts) != 1 {
		t.Fatalf("emails inserts = %d, want 1", len(emailInserts))
	}
	prov, ok := emailInserts[0].values["provenance"]
	if !ok {
		t.Fatal("emails INSERT has no provenance column")
	}
	if fmt.Sprint(prov) != string(models.EmailProvenanceSystemForward) {
		t.Errorf("provenance = %v, want %q", prov, models.EmailProvenanceSystemForward)
	}
	if status := emailInserts[0].values["status"]; fmt.Sprint(status) != string(models.EmailStatusPending) {
		t.Errorf("status = %v, want pending before enqueue", status)
	}
	if sender := emailInserts[0].values["sender"]; fmt.Sprint(sender) != "inbox@lists.example" {
		t.Errorf("sender = %v, want the verified-domain receiving address", sender)
	}
	if to := emailInserts[0].values["recipients"]; !strings.Contains(fmt.Sprint(to), "friend@external.test") {
		t.Errorf("recipients = %v, want the confirmed target", to)
	}

	// The copy's timeline gets the forwarded event.
	eventInserts := state.insertsFor("email_events")
	if len(eventInserts) != 1 {
		t.Fatalf("email_events inserts = %d, want 1", len(eventInserts))
	}
	if typ := eventInserts[0].values["type"]; fmt.Sprint(typ) != string(models.EmailEventForwarded) {
		t.Errorf("event type = %v, want %q", typ, models.EmailEventForwarded)
	}
	if meta := eventInserts[0].values["metadata"]; !strings.Contains(fmt.Sprint(meta), "inb-1") {
		t.Errorf("event metadata = %v, want the inbound reference", meta)
	}

	// Consent layers are never consulted: no INSERT (and below, no SELECT
	// against the suppression or unsubscribe tables).
	for _, table := range state.tables() {
		switch table {
		case "emails", "email_events":
		default:
			t.Errorf("unexpected INSERT into %q — the system path must only write the copy and its event", table)
		}
	}
	for _, sel := range state.selects {
		if strings.Contains(sel, "suppression") || strings.Contains(sel, "unsubscribe") {
			t.Errorf("SELECT touched %q — forwarding must never consult consent layers (ADR-002)", sel)
		}
	}
}

// TestSendSystemForwardValidatesTarget proves the system path still rejects
// malformed addresses and header injection (R-24) before any row is created.
func TestSendSystemForwardValidatesTarget(t *testing.T) {
	state := &sfState{}
	db := sfGorm(t, state)
	svc := NewService(repositories.NewEmailRepository(db), nil, nil, nil, nil, nil, false)

	if _, err := svc.SendSystemForward(context.Background(), &SystemForwardRequest{
		From: "inbox@lists.example",
		To:   []string{"not-an-address"},
	}); err == nil {
		t.Error("malformed target accepted")
	}
	if _, err := svc.SendSystemForward(context.Background(), &SystemForwardRequest{
		From:    "inbox@lists.example",
		To:      []string{"friend@external.test"},
		Subject: "x",
		Headers: map[string]string{"X-Evil": "a\r\nBcc: victim@example.test"},
	}); err == nil {
		t.Error("header injection accepted")
	}
	if len(state.tables()) != 0 {
		t.Errorf("rows written despite validation failure: %v", state.tables())
	}
}
