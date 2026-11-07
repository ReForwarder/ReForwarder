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

package webhook

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ADR-006 decision 3 tests: the synchronous filter consult. The endpoints
// are httptest loopback servers (the dispatcher's URL policy is relaxed for
// the harness, exactly like the delivery tests); the webhook rows come from
// a fake database/sql driver that answers the one SELECT the consult issues.

type wcState struct {
	mu       sync.Mutex
	webhooks []map[string]driver.Value
}

type wcConnector struct{ state *wcState }

func (c wcConnector) Connect(context.Context) (driver.Conn, error) { return &wcConn{s: c.state}, nil }
func (c wcConnector) Driver() driver.Driver                        { return wcDrv{} }

type wcDrv struct{}

func (wcDrv) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("use sql.OpenDB") }

type wcConn struct{ s *wcState }

func (c *wcConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("no prepare") }
func (c *wcConn) Close() error                        { return nil }
func (c *wcConn) Begin() (driver.Tx, error)           { return wcTx{}, nil }

type wcTx struct{}

func (wcTx) Commit() error   { return nil }
func (wcTx) Rollback() error { return nil }

func (c *wcConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("wcState: no exec expected")
}

func (c *wcConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if !strings.Contains(lower, `from "webhooks"`) {
		return nil, fmt.Errorf("wcState: unexpected query: %s", query)
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	cols := []string{"id", "user_id", "workspace_id", "url", "events", "filters", "secret", "created_at"}
	rows := make([][]driver.Value, 0, len(s.webhooks))
	for _, wh := range s.webhooks {
		rows = append(rows, wfColValues(cols, wh))
	}
	return &wcRows{cols: cols, vals: rows}, nil
}

func wfColValues(cols []string, row map[string]driver.Value) []driver.Value {
	out := make([]driver.Value, len(cols))
	for i, c := range cols {
		out[i] = row[c]
	}
	return out
}

type wcRows struct {
	cols []string
	vals [][]driver.Value
	i    int
}

func (r *wcRows) Columns() []string { return r.cols }
func (r *wcRows) Close() error      { return nil }
func (r *wcRows) Next(dest []driver.Value) error {
	if r.i >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.i])
	r.i++
	return nil
}

func consultDispatcher(t *testing.T, state *wcState) *Dispatcher {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(wcConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open consult fake gorm: %v", err)
	}
	d := NewDispatcher(repositories.NewWebhookRepository(gdb))
	// The harness speaks to loopback httptest servers; production keeps
	// ValidateWebhookURL + the guarded transport (the SSRF suite covers the
	// policy itself). Both gates are relaxed here, exactly like the delivery
	// tests — the consult reuses the same client the delivery path uses.
	d.urlPolicy = func(string) error { return nil }
	d.client = permissiveTestClient()
	return d
}

func seedWebhook(state *wcState, url, secret string, filters ...string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	row := map[string]driver.Value{
		"id":         int64(7),
		"user_id":    int64(3),
		"url":        url,
		"secret":     secret,
		"events":     "{email.inbound.filter}",
		"filters":    "{}",
		"created_at": time.Now().UTC(),
	}
	if len(filters) > 0 {
		row["filters"] = "{" + strings.Join(filters, ",") + "}"
	}
	state.webhooks = append(state.webhooks, row)
}

func TestConsultFilterReturnsSignedReply(t *testing.T) {
	state := &wcState{}
	var (
		mu       sync.Mutex
		gotSig   string
		gotUA    string
		gotBody  []byte
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotSig = r.Header.Get("X-Posta-Signature")
		gotUA = r.Header.Get("User-Agent")
		gotBody = body
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"disposition":"quarantine","score":3.5}`))
	}))
	defer srv.Close()
	seedWebhook(state, srv.URL, "shared-secret")

	d := consultDispatcher(t, state)
	reply, err := d.ConsultFilter(context.Background(), 3, nil, "email.inbound.filter",
		[]byte(`{"inbound_id":"abc"}`), "sender@example.net", 3*time.Second)
	if err != nil {
		t.Fatalf("ConsultFilter: %v", err)
	}
	if reply.Disposition != "quarantine" || reply.Score == nil || *reply.Score != 3.5 {
		t.Fatalf("reply = %+v", reply)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if gotSig != "sha256="+signPayload("shared-secret", []byte(`{"inbound_id":"abc"}`)) {
		t.Fatalf("signature header = %q, want the HMAC of the body", gotSig)
	}
	if !strings.Contains(gotUA, "Posta-Webhook") {
		t.Fatalf("user agent = %q", gotUA)
	}
	if string(gotBody) != `{"inbound_id":"abc"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestConsultFilterServerErrorFailsOpen(t *testing.T) {
	state := &wcState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	seedWebhook(state, srv.URL, "")

	d := consultDispatcher(t, state)
	if _, err := d.ConsultFilter(context.Background(), 3, nil, "email.inbound.filter", []byte(`{}`), "", time.Second); err == nil {
		t.Fatal("a failing endpoint must surface as an error (the caller fail-opens)")
	}
}

func TestConsultFilterBadReplyIsError(t *testing.T) {
	state := &wcState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"disposition":"nuke-from-orbit"}`))
	}))
	defer srv.Close()
	seedWebhook(state, srv.URL, "")

	d := consultDispatcher(t, state)
	if _, err := d.ConsultFilter(context.Background(), 3, nil, "email.inbound.filter", []byte(`{}`), "", time.Second); err == nil {
		t.Fatal("an unrecognized disposition must not pass as a verdict")
	}
}

func TestConsultFilterNoSubscribers(t *testing.T) {
	d := consultDispatcher(t, &wcState{})
	_, err := d.ConsultFilter(context.Background(), 3, nil, "email.inbound.filter", []byte(`{}`), "", time.Second)
	if err == nil || !strings.Contains(err.Error(), "no webhook subscribed") {
		t.Fatalf("err = %v, want the no-subscriber error", err)
	}
}

func TestConsultFilterSenderFilterSkips(t *testing.T) {
	state := &wcState{}
	var requests int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"disposition":"reject"}`))
	}))
	defer srv.Close()
	seedWebhook(state, srv.URL, "", "billing@example.net")

	d := consultDispatcher(t, state)
	if _, err := d.ConsultFilter(context.Background(), 3, nil, "email.inbound.filter", []byte(`{}`), "other@example.net", time.Second); err == nil {
		t.Fatal("no matching subscriber → the no-subscriber error, not silence")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 0 {
		t.Fatalf("filtered-out endpoint was called %d times", requests)
	}
}

func TestConsultFilterRespectsTimeout(t *testing.T) {
	state := &wcState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{"disposition":"reject"}`))
	}))
	defer srv.Close()
	seedWebhook(state, srv.URL, "")

	d := consultDispatcher(t, state)
	start := time.Now()
	if _, err := d.ConsultFilter(context.Background(), 3, nil, "email.inbound.filter", []byte(`{}`), "", 200*time.Millisecond); err == nil {
		t.Fatal("a slow endpoint must time out")
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("consult took %v, timeout not enforced", elapsed)
	}
}
