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

package webpush

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goposta/posta/internal/services/realtime"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The Fanout test proves the wiring: a published message.* event is pushed to
// the workspace members' push endpoints, a non-message event is not, and a
// push service that reports the subscription gone gets the row deleted.

// fnState carries what the fake driver serves and records.
type fnState struct {
	endpoint string
	p256dh   string
	auth     string
	deletes  atomic.Int32 // user_push_subscriptions DELETEs observed
}

// fnConnector answers the workspace-membership join with one subscription row
// and records DELETEs that name an endpoint.
type fnConnector struct{ state *fnState }

type fnConn struct{ state *fnState }

type fnDrv struct{}

func (fnDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func (c fnConnector) Connect(context.Context) (driver.Conn, error) { return fnConn{c.state}, nil }
func (c fnConnector) Driver() driver.Driver                        { return fnDrv{} }

func (c fnConn) Prepare(query string) (driver.Stmt, error) { return fnStmt{c}, nil }
func (c fnConn) Close() error                              { return nil }
func (c fnConn) Begin() (driver.Tx, error)                 { return fnTx{}, nil }

type fnStmt struct{ conn fnConn }

type fnTx struct{}

func (fnTx) Commit() error   { return nil }
func (fnTx) Rollback() error { return nil }

func (s fnStmt) Close() error  { return nil }
func (s fnStmt) NumInput() int { return -1 }

func (s fnStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(1), nil }
func (s fnStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.conn.state.rows(), nil
}

type fnResult struct{}

func (fnResult) LastInsertId() (int64, error) { return 1, nil }
func (fnResult) RowsAffected() (int64, error) { return 1, nil }

func (c fnConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	upper := strings.ToUpper(query)
	if strings.Contains(upper, "DELETE") && strings.Contains(upper, "USER_PUSH_SUBSCRIPTIONS") {
		c.state.deletes.Add(1)
	}
	return fnResult{}, nil
}

func (c fnConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return c.state.rows(), nil
}

func (s *fnState) rows() driver.Rows {
	return &fnRows{state: s}
}

type fnRows struct {
	state *fnState
	idx   int
}

func (r *fnRows) Columns() []string { return []string{"id", "user_id", "endpoint", "p256dh", "auth"} }
func (r *fnRows) Close() error      { return nil }
func (r *fnRows) Next(dest []driver.Value) error {
	if r.idx > 0 {
		return io.EOF
	}
	r.idx++
	dest[0] = int64(9)
	dest[1] = int64(7)
	dest[2] = r.state.endpoint
	dest[3] = r.state.p256dh
	dest[4] = r.state.auth
	return nil
}

func newFnDB(t *testing.T, state *fnState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(fnConnector{state: state}),
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

func TestFanoutPublishesWorkspaceEventsToPushSubscriptions(t *testing.T) {
	_, p256dh, auth := testSubscription(t)
	state := &fnState{p256dh: p256dh, auth: auth}
	gdb := newFnDB(t, state)

	// The push service 410s the first delivery (subscription gone) so the
	// fan-out must delete the stored row.
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// The fake row's endpoint must be the local push service; the delete
	// record keys on it too.
	state.endpoint = srv.URL

	pubB64, privB64, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{PublicKey: pubB64, PrivateKey: privB64})
	if err != nil {
		t.Fatal(err)
	}

	repo := repositories.NewUserPushSubscriptionRepository(gdb)
	fan := NewFanout(realtime.NewMemory(), svc, repo)

	// A message event reaches the push service; an email event does not.
	fan.Publish(context.Background(), realtime.Event{Type: "message.replied", WorkspaceID: 3, Topic: "ws:3"})
	fan.Publish(context.Background(), realtime.Event{Type: "email.sent", WorkspaceID: 3, Topic: "ws:3"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if state.deletes.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := requests.Load(); got != 1 {
		t.Fatalf("push service received %d requests, want 1 (message event only)", got)
	}
	if n := state.deletes.Load(); n != 1 {
		t.Fatalf("expected exactly one subscription row deleted after the 410, got %d", n)
	}
}
