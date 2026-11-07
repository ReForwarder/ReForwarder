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
	"io"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// CA-06 dev fallback broker: the HTTP send path falls back to synchronous
// delivery when no queue producer is wired (dev mode never creates one —
// cmd/posta/server.go gates worker.NewProducer on !POSTA_DEV_MODE). These
// tests exercise the fallback pipeline (sendSync) end to end over a fake
// gorm driver, proving the same pipeline the worker path feeds — SMTP send,
// status/history writes, webhook events — runs without an enqueuer.

// fbState records what the fake driver saw.
type fbState struct {
	updates    []string // table names of every UPDATE seen
	inserts    []string // table names of every INSERT seen
	senderFrom string
	senderTo   []string
}

// fbRows is one table's canned result. Empty rows = empty result set.
type fbRows struct {
	columns []string
	rows    [][]driver.Value
}

// fbConnector is a catch-all database/sql driver: SELECTs answer from a
// per-table map, everything else reports success. It exists so the fallback
// pipeline can run without Postgres — the same pattern as the handlers
// package's fake gorm drivers.
type fbConnector struct{ state *fbState }

type fbConn struct {
	state     *fbState
	lastQuery string
}

type fbResult struct{ rows int64 }

func (r fbResult) LastInsertId() (int64, error) { return 1, nil }
func (r fbResult) RowsAffected() (int64, error) { return r.rows, nil }

type fbStmt struct{ conn *fbConn }

type fbTx struct{ conn *fbConn }

func (c fbConnector) Connect(context.Context) (driver.Conn, error) {
	return fbConn{state: c.state}, nil
}
func (c fbConnector) Driver() driver.Driver                        { return fbDrv{} }

type fbDrv struct{}

func (fbDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func (c fbConn) Prepare(query string) (driver.Stmt, error) {
	return fbStmt{conn: &fbConn{state: c.state, lastQuery: query}}, nil
}
func (c fbConn) Close() error                              { return nil }
func (c fbConn) Begin() (driver.Tx, error)                 { return fbTx{conn: &c}, nil }

func (t fbTx) Commit() error   { return nil }
func (t fbTx) Rollback() error { return nil }

func (s fbStmt) Close() error  { return nil }
func (s fbStmt) NumInput() int { return -1 }

func (s fbStmt) Exec(args []driver.Value) (driver.Result, error) {
	return fbExecResult(s.conn.state, s.conn.lastQuery)
}

func (s fbStmt) Query(args []driver.Value) (driver.Rows, error) {
	return fbQueryRows(s.conn.state, s.conn.lastQuery)
}

func (c fbConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	return fbExecResult(c.state, query)
}

func (c fbConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	return fbQueryRows(c.state, query)
}

// fbExecResult records every INSERT/UPDATE the pipeline issues and reports
// success.
func fbExecResult(state *fbState, query string) (driver.Result, error) {
	upper := strings.ToUpper(query)
	switch {
	case strings.Contains(upper, "INSERT"):
		table := fbTableName(query)
		if table != "" {
			state.inserts = append(state.inserts, table)
		}
	case strings.Contains(upper, "UPDATE"):
		table := fbTableName(query)
		if table != "" {
			state.updates = append(state.updates, table)
		}
	}
	return fbResult{rows: 1}, nil
}

// fbQueryRows answers the SELECTs the fallback pipeline issues: the SMTP
// server lookup returns one enabled relay, every other SELECT (webhook
// subscriptions, event lookups) is empty. INSERT ... RETURNING (how pgx/gorm
// issue Create) is also recorded here because it arrives as a query.
func fbQueryRows(state *fbState, query string) (driver.Rows, error) {
	upper := strings.ToUpper(query)
	if strings.Contains(upper, "SMTP_SERVERS") {
		return &fbRowSet{
			columns: []string{"id", "name", "host", "port", "username", "password", "encryption", "status", "user_id", "max_send_attempts"},
			rows: [][]driver.Value{{
				int64(5), "relay", "smtp.fb.test", int64(587), "u", "p", "starttls", "enabled", int64(7), int64(3),
			}},
		}, nil
	}
	if strings.Contains(upper, "INSERT") {
		if table := fbTableName(query); table != "" {
			state.inserts = append(state.inserts, table)
		}
	}
	return &fbRowSet{columns: []string{"id"}}, nil
}

// fbTableName extracts the quoted table name from a simple SQL statement.
func fbTableName(query string) string {
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

type fbRowSet struct {
	columns []string
	rows    [][]driver.Value
	idx     int
}

func (r *fbRowSet) Columns() []string { return r.columns }
func (r *fbRowSet) Close() error      { return nil }
func (r *fbRowSet) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func fbGorm(t *testing.T, state *fbState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(fbConnector{state: state}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open fake gorm: %v", err)
	}
	return gdb
}

// fbSender is the messageSender stub: it records what the pipeline asked the
// SMTP layer to deliver.
type fbSender struct {
	state *fbState
	err   error
}

func (f fbSender) Send(_ *models.SMTPServer, from string, to []string, _, _, _ string, _ []models.Attachment, _ map[string]string, _, _ string, _ bool) error {
	f.state.senderFrom = from
	f.state.senderTo = append([]string(nil), to...)
	return f.err
}

func newFallbackService(t *testing.T, state *fbState, sender messageSender) *Service {
	t.Helper()
	gdb := fbGorm(t, state)
	svc := NewService(
		nil, nil, nil, nil, nil, nil, false,
	)
	svc.sender = sender
	// The dispatcher runs over the same fake gorm: its webhook lookup returns
	// no rows, so no deliveries fire — the event path is exercised without
	// the network.
	svc.dispatcher = webhook.NewDispatcher(repositories.NewWebhookRepository(gdb))
	svc.recorder = emailhistory.NewRecorder(gdb)
	svc.emailRepo = repositories.NewEmailRepository(gdb)
	svc.smtpRepo = repositories.NewSMTPRepository(gdb)
	return svc
}

func TestSendSyncDeliversThroughFallbackPipelineWhenNoProducerWired(t *testing.T) {
	state := &fbState{}
	svc := newFallbackService(t, state, fbSender{state: state})

	em := &models.Email{ID: 42, UUID: "fb-uuid-1", Status: models.EmailStatusQueued, Sender: "noreply@fb.test"}
	req := &SendRequest{From: "noreply@fb.test", To: []string{"alice@example.com"}, Subject: "hi", HTML: "<p>hi</p>"}

	resp, err := svc.sendSync(em, 7, nil, req)
	if err != nil {
		t.Fatalf("fallback sendSync failed: %v", err)
	}
	if resp.Status != models.EmailStatusSent {
		t.Fatalf("status = %q, want sent", resp.Status)
	}
	if state.senderFrom != "noreply@fb.test" {
		t.Fatalf("SMTP from = %q, want noreply@fb.test", state.senderFrom)
	}
	if len(state.senderTo) != 1 || state.senderTo[0] != "alice@example.com" {
		t.Fatalf("SMTP to = %v, want [alice@example.com]", state.senderTo)
	}
	// The pipeline persists the outcome: the email row is saved (status +
	// sent_at) and a sent event lands in the message history.
	if len(state.updates) == 0 || state.updates[0] != "emails" {
		t.Fatalf("expected an emails UPDATE, got %v", state.updates)
	}
	if !fbContains(state.inserts, "email_events") {
		t.Fatalf("expected an email_events INSERT (message history), got %v", state.inserts)
	}
}

func TestSendSyncFailureMarksEmailFailedThroughFallbackPipeline(t *testing.T) {
	state := &fbState{}
	svc := newFallbackService(t, state, fbSender{state: state, err: errors.New("connection refused")})

	em := &models.Email{ID: 43, UUID: "fb-uuid-2", Status: models.EmailStatusQueued}
	req := &SendRequest{From: "noreply@fb.test", To: []string{"bob@example.com"}, Subject: "hi"}

	resp, err := svc.sendSync(em, 7, nil, req)
	if err == nil {
		t.Fatal("expected an error from a failed SMTP send")
	}
	if resp != nil {
		t.Fatalf("expected nil response, got %+v", resp)
	}
	if em.Status != models.EmailStatusFailed {
		t.Fatalf("status = %q, want failed", em.Status)
	}
	if !fbContains(state.inserts, "email_events") {
		t.Fatalf("expected a failed email_events INSERT, got %v", state.inserts)
	}
}

func fbContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
