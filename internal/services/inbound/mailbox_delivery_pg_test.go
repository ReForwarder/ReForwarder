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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-3 wave W5 (flow F13) integration tests: the ingestion seam end to end —
// IngestRaw (the SMTP receiver's path) with a matched action=deliver route →
// auto-provision → mailstore.Append into the INBOX → fetch round-trip, and
// the typed over-quota → quarantine → release → deliver loop. They run
// against REAL Postgres (TEST_DATABASE_DSN gate, the repositories suite's
// pattern): the seam's correctness rests on real transactions, the raw-SQL
// UID claim and the full production schema — the fake driver cannot express
// any of those. Each test works in one transaction that is rolled back.

const mdTestDSNEnv = "TEST_DATABASE_DSN"

var (
	mdMigrateOnce sync.Once
	mdMigrateErr  error
)

// mdDB opens the gated test database and runs the full additive migration
// once, so these tests exercise the real schema, constraints included.
func mdDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(mdTestDSNEnv)
	if dsn == "" {
		t.Skipf("skipping: %s not set", mdTestDSNEnv)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available (%s): %v", dsn, err)
	}
	mdMigrateOnce.Do(func() { mdMigrateErr = migration.Run(db) })
	if mdMigrateErr != nil {
		t.Fatalf("migrate: %v", mdMigrateErr)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// mdFixture carries one tenant (user + workspace + verified domain + deliver
// catch-all route) and the wired ingest service + mailstore service, all
// bound to the test transaction.
type mdFixture struct {
	tx      *gorm.DB
	svc     *Service
	msSvc   *mailstore.Service
	userID  uint
	wsID    uint
	domName string
	quota   *QuarantineService
}

// mdNewFixture seeds a tenant whose domain's deliver route files mail into
// hosted mailboxes, with auto-provision as configured.
func mdNewFixture(t *testing.T, tx *gorm.DB, autoProvision bool) *mdFixture {
	t.Helper()
	// The production codec encrypts with the deployment key (SF-78); the
	// test key stands in for POSTA_ENCRYPTION_KEY.
	crypto.Init("mailbox-delivery-test-key")

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user := &models.User{Name: "md-" + suffix, Email: "md-" + suffix + "@test.local", PasswordHash: "x"}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	ws := &models.Workspace{Name: "md-" + suffix, Slug: "md-" + suffix, OwnerID: user.ID}
	if err := tx.Create(ws).Error; err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	domName := "md-" + suffix + ".test"
	dom := &models.Domain{
		UserID: user.ID, WorkspaceID: &ws.ID, Domain: domName,
		OwnershipVerified: true, VerificationToken: "tok-" + suffix,
	}
	if err := tx.Create(dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	route := &models.InboundRoute{
		UserID: user.ID, WorkspaceID: &ws.ID, DomainID: dom.ID,
		Pattern: "@" + domName, Action: models.InboundRouteActionDeliver,
		Priority: 10, Enabled: true,
	}
	if err := tx.Create(route).Error; err != nil {
		t.Fatalf("create route: %v", err)
	}

	st, err := os.MkdirTemp("", "inbound-md-blob-*")
	if err != nil {
		t.Fatalf("temp blob dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(st) })
	store, err := blob.New(blob.Config{Provider: "fs", FSBasePath: st})
	if err != nil {
		t.Fatalf("fs blob store: %v", err)
	}

	msSvc, err := mailstore.New(tx, mailstore.ZstdBlobCodec{}, store, nil)
	if err != nil {
		t.Fatalf("mailstore service: %v", err)
	}

	inboundRepo := repositories.NewInboundEmailRepository(tx)
	domainRepo := repositories.NewDomainRepository(tx)
	svc := NewService(
		inboundRepo,
		domainRepo,
		repositories.NewSuppressionRepository(tx),
		Config{MaxMessageSize: 1 << 20, MaxAttachmentSize: 1 << 20},
	)
	svc.SetBlobStore(store)
	svc.SetRouter(NewRouter(repositories.NewInboundRouteRepository(tx)))
	svc.SetMailboxDeliverer(NewMailboxDelivery(msSvc, domainRepo, MailboxDeliveryConfig{
		AutoProvision: autoProvision,
	}))
	qs := NewQuarantineService(repositories.NewInboundQuarantineRepository(tx), inboundRepo, domainRepo, svc)
	svc.SetQuarantineStore(qs)

	return &mdFixture{
		tx: tx, svc: svc, msSvc: msSvc,
		userID: user.ID, wsID: ws.ID, domName: domName, quota: qs,
	}
}

// mdRaw builds a small valid RFC 5322 message (CRLF line endings) for one
// recipient on the fixture's domain.
func (f *mdFixture) mdRaw(messageID, subject string) []byte {
	return []byte("From: Sender <sender@example.remote>\r\n" +
		"To: <ada@" + f.domName + ">\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Sun, 08 Nov 2026 10:00:00 +0000\r\n" +
		"Message-ID: <" + messageID + "@" + f.domName + ">\r\n" +
		"\r\n" +
		"Body of " + subject + " — delivered by the ingestion seam.\r\n")
}

// mdIngest runs the SMTP receiver's ingest path for one recipient.
func (f *mdFixture) mdIngest(t *testing.T, raw []byte) *models.InboundEmail {
	t.Helper()
	rec, err := f.svc.IngestRaw(context.Background(), raw,
		"sender@example.remote", []string{"ada@" + f.domName},
		models.InboundSourceSMTP, "")
	if err != nil {
		t.Fatalf("ingest raw: %v", err)
	}
	return rec
}

// mdInboxMessages returns the INBOX messages of the named mailbox, UID order.
func (f *mdFixture) mdInboxMessages(t *testing.T, mailboxID uint) []models.MailMessage {
	t.Helper()
	var inbox models.MailFolder
	if err := f.tx.Where("mailbox_id = ? AND imap_path = ?", mailboxID, "INBOX").First(&inbox).Error; err != nil {
		t.Fatalf("load INBOX of mailbox %d: %v", mailboxID, err)
	}
	var msgs []models.MailMessage
	if err := f.tx.Where("folder_id = ?", inbox.ID).Order("uid ASC").Find(&msgs).Error; err != nil {
		t.Fatalf("list messages of INBOX %d: %v", inbox.ID, err)
	}
	return msgs
}

// mdMailboxCount counts the user's mailboxes carrying the given name. The
// name embeds this run's unique domain suffix, so the count only ever sees
// rows this test created — a shared test database can hold committed
// leftovers whose small user_ids collide with a fresh user's id.
func (f *mdFixture) mdMailboxCount(t *testing.T, name string) int64 {
	t.Helper()
	var n int64
	if err := f.tx.Model(&models.Mailbox{}).Where("user_id = ? AND name = ?", f.userID, name).Count(&n).Error; err != nil {
		t.Fatalf("count mailboxes named %q: %v", name, err)
	}
	return n
}

// mdInboundCount counts the user's archived inbound rows (delta-checked by
// the callers for the same leftover-row reason as mdMailboxCount).
func (f *mdFixture) mdInboundCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.tx.Model(&models.InboundEmail{}).Where("user_id = ?", f.userID).Count(&n).Error; err != nil {
		t.Fatalf("count archived inbound: %v", err)
	}
	return n
}

// TestPGMailboxDeliveryRoundTrip — the full F13 flow: a deliver-routed SMTP
// ingest auto-provisions the recipient mailbox, appends into its INBOX
// (monotonic UID across deliveries, mailbox reused not duplicated), and the
// stored message fetches back byte-identical through the production
// encrypt/compress codec.
func TestPGMailboxDeliveryRoundTrip(t *testing.T) {
	tx := mdDB(t)
	f := mdNewFixture(t, tx, true)
	addr := "ada@" + f.domName
	archivedBase := f.mdInboundCount(t)

	raw1 := f.mdRaw("m1", "first delivery")
	rec1 := f.mdIngest(t, raw1)
	if rec1.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (delivery never holds a deliverable)", rec1.Status)
	}
	if n := f.mdMailboxCount(t, addr); n != 1 {
		t.Fatalf("mailboxes named %q = %d, want 1 (auto-provisioned)", addr, n)
	}
	mb, err := f.msSvc.FindMailboxByName(f.userID, addr)
	if err != nil {
		t.Fatalf("find auto-provisioned mailbox: %v", err)
	}
	if mb.Status != models.MailboxStatusActive || mb.WorkspaceID == nil || *mb.WorkspaceID != f.wsID {
		t.Fatalf("mailbox = %+v, want active in workspace %d", mb, f.wsID)
	}

	msgs := f.mdInboxMessages(t, mb.ID)
	if len(msgs) != 1 {
		t.Fatalf("INBOX messages = %d, want 1", len(msgs))
	}
	if msgs[0].UID != 1 || msgs[0].Subject != "first delivery" || msgs[0].FromAddr != "sender@example.remote" {
		t.Fatalf("first message = UID %d subject %q from %q", msgs[0].UID, msgs[0].Subject, msgs[0].FromAddr)
	}
	body, err := f.msSvc.GetMessageBody(context.Background(), msgs[0].ID, f.userID, 0)
	if err != nil {
		t.Fatalf("fetch body: %v", err)
	}
	if !bytes.Equal(body, raw1) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d (byte-exact RFC 5322)", len(body), len(raw1))
	}

	// Second delivery to the same address reuses the mailbox and claims the
	// next UID — monotonic, no duplicate mailbox.
	raw2 := f.mdRaw("m2", "second delivery")
	f.mdIngest(t, raw2)
	if n := f.mdMailboxCount(t, addr); n != 1 {
		t.Fatalf("mailboxes named %q after second delivery = %d, want 1 (reused)", addr, n)
	}
	msgs = f.mdInboxMessages(t, mb.ID)
	if len(msgs) != 2 || msgs[0].UID != 1 || msgs[1].UID != 2 {
		t.Fatalf("INBOX after second delivery = %d messages, UIDs %d,%d; want 2 messages, UIDs 1,2",
			len(msgs), uidAt(msgs, 0), uidAt(msgs, 1))
	}

	// The durable archive copy still exists alongside the mailbox copy
	// (delta against the shared database's leftover rows).
	if got := f.mdInboundCount(t) - archivedBase; got != 2 {
		t.Fatalf("archived inbound delta = %d, want 2 (mailstore delivery is additive)", got)
	}
}

// TestPGMailboxDeliveryAutoProvisionOff — with POSTA_MAILSTORE_AUTO_PROVISION
// off, a recipient with no mailbox behaves exactly as today (durable archive,
// status received, no mailbox, no mailstore rows); once the mailbox is
// provisioned by hand, delivery reaches its INBOX.
func TestPGMailboxDeliveryAutoProvisionOff(t *testing.T) {
	tx := mdDB(t)
	f := mdNewFixture(t, tx, false)
	addr := "ada@" + f.domName

	rec := f.mdIngest(t, f.mdRaw("off1", "no mailbox yet"))
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (archive-only must not hold)", rec.Status)
	}
	if n := f.mdMailboxCount(t, addr); n != 0 {
		t.Fatalf("mailboxes named %q = %d, want 0 (auto-provision off)", addr, n)
	}
	if _, err := f.msSvc.FindMailboxByName(f.userID, addr); !errors.Is(err, mailstore.ErrMailboxNotFound) {
		t.Fatalf("FindMailboxByName = (%v, %v), want ErrMailboxNotFound (no mailbox → archive only)", err, mailstore.ErrMailboxNotFound)
	}

	// Provision by hand (the operator's path), then the same address receives.
	if _, err := f.msSvc.CreateMailbox(f.userID, rec.WorkspaceID, addr, 0); err != nil {
		t.Fatalf("hand-provision mailbox: %v", err)
	}
	raw2 := f.mdRaw("off2", "now there is a mailbox")
	rec2 := f.mdIngest(t, raw2)
	if rec2.Status != models.InboundStatusReceived {
		t.Fatalf("second status = %q, want received", rec2.Status)
	}
	mb, err := f.msSvc.FindMailboxByName(f.userID, "ada@"+f.domName)
	if err != nil {
		t.Fatalf("find hand-provisioned mailbox: %v", err)
	}
	msgs := f.mdInboxMessages(t, mb.ID)
	if len(msgs) != 1 || msgs[0].Subject != "now there is a mailbox" {
		t.Fatalf("INBOX = %+v, want exactly the second delivery", msgs)
	}
	body, err := f.msSvc.GetMessageBody(context.Background(), msgs[0].ID, f.userID, 0)
	if err != nil {
		t.Fatalf("fetch body: %v", err)
	}
	if !bytes.Equal(body, raw2) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(body), len(raw2))
	}
}

// TestPGMailboxDeliveryOverQuotaQuarantinesTyped — an append refused by the
// quota enforcer quarantines the message with the typed reason (ADR-003 §5
// via ADR-002 §3): the inbound row is held, a pending quarantine row carries
// the mailbox_quota_exceeded reason with the structured quota fields, the
// mailstore stays untouched, and release after the quota is raised re-runs
// routing and files the message (never lost).
func TestPGMailboxDeliveryOverQuotaQuarantinesTyped(t *testing.T) {
	tx := mdDB(t)
	f := mdNewFixture(t, tx, true)

	// Pre-provision with a tiny byte quota so the append is refused.
	mb, err := f.msSvc.CreateMailbox(f.userID, nil, "ada@"+f.domName, 10)
	if err != nil {
		t.Fatalf("create tiny mailbox: %v", err)
	}
	raw := f.mdRaw("q1", "too big for the quota")
	rec := f.mdIngest(t, raw)
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %q, want quarantined (over-quota delivery)", rec.Status)
	}

	qrow, err := f.quota.repo.FindPendingByInboundEmailID(rec.ID)
	if err != nil {
		t.Fatalf("load quarantine row: %v", err)
	}
	if qrow.Status != models.InboundQuarantinePending {
		t.Fatalf("quarantine status = %q, want pending", qrow.Status)
	}
	const reasonPrefix = QuarantineReasonMailboxQuota
	if len(qrow.Reason) < len(reasonPrefix) || qrow.Reason[:len(reasonPrefix)] != reasonPrefix {
		t.Fatalf("quarantine reason = %q, want prefix %q", qrow.Reason, reasonPrefix)
	}
	// The typed reason carries the structured quota fields (mailbox, scope,
	// used, incoming, limit) so the hold is diagnosable without the logs.
	for _, want := range []string{"mailbox-bytes", fmt.Sprint(mb.ID), "incoming", "limit"} {
		if !bytes.Contains([]byte(qrow.Reason), []byte(want)) {
			t.Fatalf("quarantine reason %q missing quota field %q", qrow.Reason, want)
		}
	}

	// The mailstore stayed untouched: no message, no counter drift.
	if msgs := f.mdInboxMessages(t, mb.ID); len(msgs) != 0 {
		t.Fatalf("INBOX messages = %d, want 0 (append was refused)", len(msgs))
	}
	var mbAfter models.Mailbox
	if err := tx.First(&mbAfter, mb.ID).Error; err != nil {
		t.Fatalf("reload mailbox: %v", err)
	}
	if mbAfter.UsedBytes != 0 || mbAfter.MessageCount != 0 {
		t.Fatalf("mailbox counters = %d bytes / %d msgs, want 0/0", mbAfter.UsedBytes, mbAfter.MessageCount)
	}

	// Raise the quota and release: routing re-runs, the append succeeds, and
	// the hold settles to released.
	if err := f.msSvc.SetMailboxQuota(mb.ID, f.userID, 0, 1<<20); err != nil {
		t.Fatalf("raise quota: %v", err)
	}
	scope := repositories.ResourceScope{UserID: f.userID, WorkspaceID: rec.WorkspaceID}
	if _, err := f.quota.Release(context.Background(), scope, qrow.ID, f.userID); err != nil {
		t.Fatalf("release: %v", err)
	}
	var recAfter models.InboundEmail
	if err := tx.First(&recAfter, rec.ID).Error; err != nil {
		t.Fatalf("reload inbound row: %v", err)
	}
	if recAfter.Status != models.InboundStatusReceived {
		t.Fatalf("status after release = %q, want received", recAfter.Status)
	}
	msgs := f.mdInboxMessages(t, mb.ID)
	if len(msgs) != 1 || msgs[0].Subject != "too big for the quota" {
		t.Fatalf("INBOX after release = %+v, want the released message", msgs)
	}
	body, err := f.msSvc.GetMessageBody(context.Background(), msgs[0].ID, f.userID, 0)
	if err != nil {
		t.Fatalf("fetch body after release: %v", err)
	}
	if !bytes.Equal(body, raw) {
		t.Fatalf("round-trip after release: got %d bytes, want %d", len(body), len(raw))
	}
}

// TestPGMailboxDeliveryLeavesTransactionalEmailAndSubscriberStateUntouched —
// the seam never touches the transactional Email model or the marketing
// subscriber paths (ADR-003: the two stores share no rows): delivering twice
// moves no rows in emails / subscribers / subscriber_lists.
func TestPGMailboxDeliveryLeavesTransactionalEmailAndSubscriberStateUntouched(t *testing.T) {
	tx := mdDB(t)
	f := mdNewFixture(t, tx, true)

	count := func(t *testing.T, model interface{}) int64 {
		t.Helper()
		var n int64
		if err := tx.Model(model).Count(&n).Error; err != nil {
			t.Fatalf("count %T: %v", model, err)
		}
		return n
	}
	before := map[string]int64{
		"emails":           count(t, &models.Email{}),
		"subscribers":      count(t, &models.Subscriber{}),
		"subscriber_lists": count(t, &models.SubscriberList{}),
	}

	f.mdIngest(t, f.mdRaw("u1", "untouched one"))
	f.mdIngest(t, f.mdRaw("u2", "untouched two"))

	for table, want := range before {
		model := map[string]interface{}{
			"emails":           &models.Email{},
			"subscribers":      &models.Subscriber{},
			"subscriber_lists": &models.SubscriberList{},
		}[table]
		if got := count(t, model); got != want {
			t.Fatalf("%s rows after delivery = %d, want %d (marketing state untouched)", table, got, want)
		}
	}
	// And the mailstore did receive both.
	mb, err := f.msSvc.FindMailboxByName(f.userID, "ada@"+f.domName)
	if err != nil {
		t.Fatalf("find mailbox: %v", err)
	}
	if msgs := f.mdInboxMessages(t, mb.ID); len(msgs) != 2 {
		t.Fatalf("INBOX messages = %d, want 2", len(msgs))
	}
}

// uidAt safely reads a UID for diagnostics.
func uidAt(msgs []models.MailMessage, i int) uint32 {
	if i < len(msgs) {
		return msgs[i].UID
	}
	return 0
}
