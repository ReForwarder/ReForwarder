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

package worker

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/provider_events"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// R-05 integration: the binary permanentRejection predicate stays (it selects
// the errors worth classifying — 5xx at RCPT TO), but whether such a rejection
// suppresses is now the classifier's call. These tests pin the policy table
// and the classification trail handlePermanentRejection writes.

// TestPermanentRejectionClassificationPolicy pins the suppress-vs-retry
// decision for representative RCPT TO rejections: 550 user unknown is an
// invalid_recipient (suppress), 554 5.7.1 is a reputation block (retry),
// transient errors never reach the classifier.
func TestPermanentRejectionClassificationPolicy(t *testing.T) {
	rcptErr := func(code int, msg string) error {
		// permanentRejection only needs errors.As to reach the *SendError;
		// the policy decision uses Code/Msg.
		return &email.SendError{Stage: "RCPT TO", Recipient: "x@example.com", Code: code, Msg: msg}
	}

	cases := []struct {
		name         string
		err          error
		reachesClass bool // permanentRejection matched at all
		suppress     bool // classifier says permanent
		class        provider_events.Classification
	}{
		{
			name:         "550 5.1.1 user unknown -> invalid_recipient -> suppress",
			err:          rcptErr(550, "5.1.1 user unknown"),
			reachesClass: true,
			suppress:     true,
			class:        provider_events.ClassInvalidRecipient,
		},
		{
			name:         "550 no signal -> hard_bounce -> suppress",
			err:          rcptErr(550, "message rejected"),
			reachesClass: true,
			suppress:     true,
			class:        provider_events.ClassHardBounce,
		},
		{
			name:         "554 5.7.1 policy -> reputation -> retry",
			err:          rcptErr(554, "5.7.1 delivery not authorized"),
			reachesClass: true,
			suppress:     false,
			class:        provider_events.ClassReputation,
		},
		{
			name:         "553 5.1.3 bad mailbox syntax -> invalid_recipient -> suppress",
			err:          rcptErr(553, "5.1.3 bad destination mailbox address syntax"),
			reachesClass: true,
			suppress:     true,
			class:        provider_events.ClassInvalidRecipient,
		},
		{
			name:         "552 5.2.2 over quota -> mailbox_full -> retry",
			err:          rcptErr(552, "5.2.2 mailbox over quota"),
			reachesClass: true,
			suppress:     false,
			class:        provider_events.ClassMailboxFull,
		},
		{
			name:         "450 transient -> not even classified for suppression",
			err:          rcptErr(450, "4.2.1 mailbox busy"),
			reachesClass: false,
		},
		{
			name:         "connection failure -> not classified",
			err:          errors.New("dial tcp: connection refused"),
			reachesClass: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			se, ok := permanentRejection(c.err)
			if ok != c.reachesClass {
				t.Fatalf("permanentRejection() ok = %v, want %v", ok, c.reachesClass)
			}
			if !ok {
				return
			}
			cls := provider_events.ClassifySendError(se)
			if cls != c.class {
				t.Fatalf("classification = %s, want %s", cls, c.class)
			}
			if cls.Permanent() != c.suppress {
				t.Fatalf("Permanent() = %v, want %v", cls.Permanent(), c.suppress)
			}
		})
	}
}

// textprotoError is intentionally absent: the policy table passes the
// *email.SendError directly, which errors.As matches without wrapping.

// ---------- handlePermanentRejection classification trail ----------

// workerCapture collects the writes handlePermanentRejection attempts via
// gorm DryRun callbacks (the emailhistory/service_test.go arrangement).
type workerCapture struct {
	mu           sync.Mutex
	emailEvents  []models.EmailEvent
	bounces      []models.Bounce
	suppressions []models.Suppression
	raws         []string
}

func (c *workerCapture) addEmailEvent(ev models.EmailEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emailEvents = append(c.emailEvents, ev)
}

func (c *workerCapture) addBounce(b models.Bounce) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bounces = append(c.bounces, b)
}

func (c *workerCapture) addSuppression(s models.Suppression) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.suppressions = append(c.suppressions, s)
}

func (c *workerCapture) addRaw(sql string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raws = append(c.raws, sql)
}

type workerNopConnector struct{}

func (workerNopConnector) Connect(context.Context) (driver.Conn, error) { return workerNopConn{}, nil }
func (workerNopConnector) Driver() driver.Driver                        { return workerNopDriver{} }

type workerNopDriver struct{}

func (workerNopDriver) Open(string) (driver.Conn, error) { return workerNopConn{}, nil }

type workerNopConn struct{}

func (workerNopConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("nop conn") }
func (workerNopConn) Close() error                        { return nil }
func (workerNopConn) Begin() (driver.Tx, error)           { return nil, errors.New("nop conn") }

// newClassificationHandler builds an EmailSendHandler whose write side runs
// on a DryRun gorm session; the callbacks capture every write.
func newClassificationHandler(t *testing.T) (*EmailSendHandler, *workerCapture, *models.Email) {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(workerNopConnector{})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open test gorm: %v", err)
	}
	cap := &workerCapture{}
	if err := gdb.Callback().Create().Before("gorm:create").Register("worker-class-test:capture-create", func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *models.EmailEvent:
			cap.addEmailEvent(*dest)
		case *models.Bounce:
			cap.addBounce(*dest)
		case *models.Suppression:
			cap.addSuppression(*dest)
		}
	}); err != nil {
		t.Fatalf("register create capture: %v", err)
	}
	if err := gdb.Callback().Raw().Before("gorm:raw").Register("worker-class-test:capture-raw", func(tx *gorm.DB) {
		cap.addRaw(tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register raw capture: %v", err)
	}
	dry := gdb.Session(&gorm.Session{DryRun: true})

	// Dispatcher with no matching webhooks: the async Dispatch finds none and
	// delivers nothing.
	h := NewEmailSendHandler(
		repositories.NewEmailRepository(dry),
		nil, nil, nil, nil,
		nil, // nil dispatcher: Dispatch goroutine races with the non-thread-safe dry-run fake DB under -race.
	)
	h.SetSuppressionRepo(repositories.NewSuppressionRepository(dry))
	h.SetBounceRepo(repositories.NewBounceRepository(dry))
	h.SetRecorder(emailhistory.NewRecorder(dry))
	h.SetAutoSuppress(true)

	ws := uint(5)
	em := &models.Email{
		ID:          7,
		UUID:        "em-uuid-1",
		UserID:      3,
		WorkspaceID: &ws,
		Recipients:  pq.StringArray{"gone@example.com"},
		Status:      models.EmailStatusProcessing,
	}
	return h, cap, em
}

func TestHandlePermanentRejectionRecordsClassification(t *testing.T) {
	h, cap, em := newClassificationHandler(t)

	se := &email.SendError{
		Stage:     "RCPT TO",
		Recipient: "gone@example.com",
		Code:      550,
		Msg:       "5.1.1 user unknown",
		Err:       errors.New("SMTP RCPT TO failed: 550 5.1.1 user unknown"),
	}
	cls := provider_events.ClassifySendError(se)
	if cls != provider_events.ClassInvalidRecipient {
		t.Fatalf("classification = %s, want invalid_recipient", cls)
	}

	h.handlePermanentRejection(em, se, cls)

	// Email marked failed without retrying.
	if em.Status != models.EmailStatusFailed {
		t.Fatalf("email status = %s, want failed", em.Status)
	}

	// Bounce record: hard, with the classification leading the Reason.
	if len(cap.bounces) != 1 {
		t.Fatalf("bounce rows = %d, want 1", len(cap.bounces))
	}
	if cap.bounces[0].Type != models.BounceTypeHard || cap.bounces[0].Recipient != "gone@example.com" {
		t.Fatalf("bounce = %+v", cap.bounces[0])
	}
	if !strings.HasPrefix(cap.bounces[0].Reason, string(provider_events.ClassInvalidRecipient)) {
		t.Fatalf("bounce reason = %q, want the classification prefix", cap.bounces[0].Reason)
	}

	// Suppression row written.
	if len(cap.suppressions) != 1 {
		t.Fatalf("suppression rows = %d, want 1", len(cap.suppressions))
	}
	if cap.suppressions[0].Email != "gone@example.com" {
		t.Fatalf("suppression = %+v", cap.suppressions[0])
	}

	// Timeline: failed + bounced rows, both carrying the classification in
	// their metadata; the bounced one carries the stable dedup id.
	if len(cap.emailEvents) != 2 {
		t.Fatalf("timeline rows = %d, want failed+bounced", len(cap.emailEvents))
	}
	for _, ev := range cap.emailEvents {
		if !strings.Contains(ev.Metadata, `"classification":"invalid_recipient"`) {
			t.Fatalf("%s event metadata = %q, want the classification", ev.Type, ev.Metadata)
		}
	}
	var bounced models.EmailEvent
	for _, ev := range cap.emailEvents {
		if ev.Type == models.EmailEventBounced {
			bounced = ev
		}
	}
	if bounced.Type != models.EmailEventBounced || bounced.Error == "" {
		t.Fatalf("bounced event = %+v", bounced)
	}
	if bounced.ProviderEventID != "smtp:550:gone@example.com" {
		t.Fatalf("provider event id = %q, want the smtp dedup id", bounced.ProviderEventID)
	}
	// The write-once bounced_at timestamp update runs behind the bounced row.
	foundBouncedAt := false
	for _, sql := range cap.raws {
		if strings.Contains(sql, "bounced_at") {
			foundBouncedAt = true
		}
	}
	if !foundBouncedAt {
		t.Fatalf("no bounced_at timestamp update captured; raws = %v", cap.raws)
	}
}

func TestHandlePermanentRejectionReasonFallsBackToErrorText(t *testing.T) {
	h, cap, em := newClassificationHandler(t)

	se := &email.SendError{
		Stage:     "RCPT TO",
		Recipient: "gone@example.com",
		Code:      550,
		Err:       errors.New("SMTP RCPT TO failed: 550 message rejected"),
	}
	cls := provider_events.ClassifySendError(se)
	// The reply text arrives via Err: a bare 550 with no recognizable phrase
	// falls through to the 5xx default.
	if cls != provider_events.ClassHardBounce {
		t.Fatalf("classification = %s, want hard_bounce", cls)
	}
	h.handlePermanentRejection(em, se, cls)
	if len(cap.bounces) != 1 {
		t.Fatalf("bounce rows = %d", len(cap.bounces))
	}
	if !strings.HasPrefix(cap.bounces[0].Reason, string(provider_events.ClassHardBounce)) {
		t.Fatalf("bounce reason = %q", cap.bounces[0].Reason)
	}
}
