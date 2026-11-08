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

package imap

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// nopConnector backs a connection-less gorm session: the audit tests run the
// event bus over a DryRun statement builder (the established repo-test
// pattern, see internal/storage/repositories/email_event_repo_test.go), so
// the INSERT is built but never executed and events are observed on the
// bus's subscriber channel.
type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nil, io.EOF }
func (nopConnector) Driver() driver.Driver                        { return nopDriver{} }

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error)               { return nil, io.EOF }
func (nopDriver) OpenConnector(string) (driver.Connector, error) { return nopConnector{}, nil }

func dryGorm(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(nopConnector{}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
		DryRun:                 true,
	})
	if err != nil {
		t.Fatalf("open dry-run gorm: %v", err)
	}
	return gdb
}

// auditBus wires the event bus over the DryRun session and returns the
// subscriber channel used to observe what the audit logger publishes.
func auditBus(t *testing.T) (*eventbus.EventBus, <-chan *models.Event, func()) {
	t.Helper()
	bus := eventbus.New(repositories.NewEventRepository(dryGorm(t)))
	ch, unsub := bus.Subscribe()
	return bus, ch, unsub
}

func awaitEvent(t *testing.T, ch <-chan *models.Event) *models.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the audit event")
		return nil
	}
}

// TestIMAPEventAuditLoggerEmitsAuditEvents: the eventbus-backed audit sink
// lands each audited action in the events substrate as an audit-category row
// with the protocol-scoped type, the actor identity and the client IP
// (ADR-004 decision 6, SF-74 continuation).
func TestIMAPEventAuditLoggerEmitsAuditEvents(t *testing.T) {
	bus, ch, unsub := auditBus(t)
	defer unsub()
	fn := NewEventAuditLogger(bus)
	if fn == nil {
		t.Fatal("NewEventAuditLogger returned nil for a live bus")
	}

	fn(AuditAuthSuccess, 42, "user@example.com", "192.0.2.5", map[string]any{"protocol": "imap"})
	ev := awaitEvent(t, ch)
	if ev.Category != models.EventCategoryAudit {
		t.Fatalf("Category = %q, want audit", ev.Category)
	}
	if ev.Type != AuditAuthSuccess {
		t.Fatalf("Type = %q, want %q", ev.Type, AuditAuthSuccess)
	}
	if ev.ActorID == nil || *ev.ActorID != 42 {
		t.Fatalf("ActorID = %v, want 42", ev.ActorID)
	}
	if ev.ActorName != "user@example.com" || ev.ClientIP != "192.0.2.5" {
		t.Fatalf("actor/ip = %q/%q", ev.ActorName, ev.ClientIP)
	}
	if !strings.HasPrefix(ev.Message, "IMAP: ") {
		t.Fatalf("Message = %q, want the IMAP: prefix", ev.Message)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(ev.Metadata), &meta); err != nil {
		t.Fatalf("metadata is not JSON: %v (%q)", err, ev.Metadata)
	}
	if meta["protocol"] != "imap" {
		t.Fatalf("metadata protocol = %v", meta["protocol"])
	}

	// The ADR-named mailbox actions each carry their own type.
	for _, tc := range []struct{ action, want string }{
		{AuditSelect, "imap.select"},
		{AuditExpunge, "imap.expunge"},
		{AuditAppend, "imap.append"},
		{AuditStore, "imap.store"},
	} {
		fn(tc.action, 42, "user@example.com", "192.0.2.5", nil)
		ev := awaitEvent(t, ch)
		if ev.Type != tc.want {
			t.Fatalf("%s: Type = %q, want %q", tc.action, ev.Type, tc.want)
		}
	}
}

// TestIMAPEventAuditLoggerNilBusYieldsNil: a deployment without the event
// substrate gets a nil sink and the server audits nothing (never panics).
func TestIMAPEventAuditLoggerNilBusYieldsNil(t *testing.T) {
	if NewEventAuditLogger(nil) != nil {
		t.Fatal("NewEventAuditLogger(nil) must return nil")
	}
	// auditf on a server without an audit sink is a no-op.
	srv := &Server{}
	srv.auditf(AuditAuthSuccess, 1, "u@example.com", "192.0.2.5", nil)
}

// TestIMAPServerAuthFailureEmitsAuditEvent: a rejected LOGIN over the wire
// (app-password seam, DryRun repos resolve no user) produces exactly one
// imap.auth.failure audit row with the presented identity and the peer IP —
// the SF-74 continuation end to end through the session layer.
func TestIMAPServerAuthFailureEmitsAuditEvent(t *testing.T) {
	bus, ch, unsub := auditBus(t)
	defer unsub()

	srv, addr := newLifecycleServer(t, NewEventAuditLogger(bus))
	_ = srv // the server must stay alive for the duration of the session
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	readGreeting(t, conn)

	// AUTHENTICATE PLAIN with a user the (DryRun) repo cannot resolve.
	initial := base64.StdEncoding.EncodeToString([]byte("\x00nouser@example.com\x00wrong-secret"))
	if _, err := conn.Write([]byte("a1 AUTHENTICATE PLAIN " + initial + "\r\n")); err != nil {
		t.Fatalf("write AUTHENTICATE: %v", err)
	}
	reply := readReply(t, conn, "a1")
	if !strings.Contains(reply, "AUTHENTICATIONFAILED") {
		t.Fatalf("auth reply = %q, want the generic NO [AUTHENTICATIONFAILED]", reply)
	}

	ev := awaitEvent(t, ch)
	if ev.Type != AuditAuthFailure {
		t.Fatalf("Type = %q, want %q", ev.Type, AuditAuthFailure)
	}
	if ev.ActorName != "nouser@example.com" {
		t.Fatalf("ActorName = %q, want the presented identity", ev.ActorName)
	}
	if ev.ClientIP == "" {
		t.Fatal("the audit event must carry the client IP")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(ev.Metadata), &meta); err != nil {
		t.Fatalf("metadata is not JSON: %v", err)
	}
	if meta["protocol"] != "imap" {
		t.Fatalf("metadata protocol = %v", meta["protocol"])
	}
}
