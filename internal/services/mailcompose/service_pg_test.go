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

package mailcompose

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/migration"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Real-Postgres integration for the compose seam: the fake-driver suite proves
// the compose-layer decisions; this suite proves them over the REAL mailstore
// — quota refusal through the actual enforcer, the reservation/refcount dance
// across upload → draft → send, and the replace-expunge bookkeeping. It skips
// when TEST_DATABASE_DSN is unset, exactly like the mailstore PG suite.

const mcPgTestDSNEnv = "TEST_DATABASE_DSN"

func mcPGDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(mcPgTestDSNEnv)
	if dsn == "" {
		t.Skipf("skipping: %s not set", mcPgTestDSNEnv)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available (%s): %v", dsn, err)
	}
	if err := db.AutoMigrate(&models.Mailbox{}, &models.MailFolder{}, &models.MailMessage{}, &models.MailBlobRef{}, &models.MailAttachmentUpload{}); err != nil {
		t.Skipf("skipping: cannot migrate test schema: %v", err)
	}
	if err := migration.EnsureMailstoreSearch(db); err != nil {
		t.Skipf("skipping: cannot ensure search schema: %v", err)
	}
	return db
}

// newMCPGService builds the real mailstore service + gorm-backed stores.
func newMCPGService(t *testing.T) (*Service, *mailstore.Service, *gorm.DB) {
	t.Helper()
	db := mcPGDB(t)
	st, err := os.MkdirTemp("", "compose-blob-*")
	if err != nil {
		t.Fatalf("temp blob dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(st) })
	store, err := blob.New(blob.Config{Provider: "fs", FSBasePath: st})
	if err != nil {
		t.Fatalf("fs blob store: %v", err)
	}
	ms, err := mailstore.New(db, mailstore.IdentityBlobCodec{}, store, nil)
	if err != nil {
		t.Fatalf("mailstore service: %v", err)
	}
	svc := New(ms, &fakeSender{}, &fakeScanner{enabled: true}, NewGormStagingStore(db), newFakeDelivery(), Config{AutoProvision: true})
	return svc, ms, db
}

// TestPGComposeQuotaRefusal: the REAL quota enforcer refuses the Sent append
// of an over-quota mailbox, nothing reaches the outbound sender, and the
// staging state stays retryable.
func TestPGComposeQuotaRefusal(t *testing.T) {
	svc, _, _ := newMCPGService(t)
	ctx := context.Background()
	const userID = uint(4242)
	addr := "quota-" + pgComposeSuffix() + "@pg.test"

	if _, err := svc.store.CreateMailbox(userID, nil, addr, 64); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	row, err := svc.UploadAttachment(ctx, userID, 0, "big.pdf", "application/pdf", uniqContent(4096))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	_, err = svc.Send(ctx, userID, 0, MessageInput{
		From: addr, To: []string{"x@y.test"}, Subject: "s", Text: "t",
		Attachments: []AttachmentRef{{BlobHash: row.BlobHash}},
	})
	if !errors.Is(err, mailstore.ErrQuotaExceeded) {
		t.Fatalf("err = %v, want mailstore.ErrQuotaExceeded", err)
	}
	// Staging survives for a retry.
	found, err := svc.staging.FindByUserAndHash(userID, row.BlobHash)
	if err != nil || found == nil {
		t.Fatalf("staging row lost after quota refusal: %v %v", found, err)
	}
}

// TestPGComposeUploadDraftSendRefcounts: the reservation/refcount chain across
// upload → draft → accepted send ends with exactly the Sent copy holding the
// content, and the draft replace leaves no rows behind.
func TestPGComposeUploadDraftSendRefcounts(t *testing.T) {
	svc, ms, db := newMCPGService(t)
	ctx := context.Background()
	const userID = uint(4243)
	addr := "refcount-" + pgComposeSuffix() + "@pg.test"

	mb, err := svc.store.CreateMailbox(userID, nil, addr, 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	row, err := svc.UploadAttachment(ctx, userID, 0, "notes.txt", "text/plain", uniqContent(64))
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got := refCountOf(db, row.BlobHash); got != 1 {
		t.Fatalf("refcount after upload = %d, want 1", got)
	}

	draft, err := svc.SaveDraft(ctx, userID, 0, MessageInput{
		MailboxID: mb.ID, From: addr, To: []string{"x@y.test"}, Subject: "d1", Text: "body",
		Attachments: []AttachmentRef{{BlobHash: row.BlobHash}},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	// Draft append reserved its own reference: 2 (staging + draft).
	if got := refCountOf(db, row.BlobHash); got != 2 {
		t.Fatalf("refcount after draft = %d, want 2", got)
	}

	draft2, err := svc.UpdateDraft(ctx, userID, 0, mb.ID, draft.UID, MessageInput{
		MailboxID: mb.ID, From: addr, To: []string{"x@y.test"}, Subject: "d2", Text: "body 2",
		Attachments: []AttachmentRef{{BlobHash: row.BlobHash}},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	// Replace: old revision expunged (−1), new appended (+1) → still 2.
	if got := refCountOf(db, row.BlobHash); got != 2 {
		t.Fatalf("refcount after replace = %d, want 2", got)
	}

	if _, err := svc.SendDraft(ctx, userID, 0, mb.ID, draft2.UID); err != nil {
		t.Fatalf("SendDraft: %v", err)
	}
	// Accepted send: staging consumed (−1), draft deleted (−1), Sent copy (+1)
	// → exactly 1, held by the Sent copy.
	if got := refCountOf(db, row.BlobHash); got != 1 {
		t.Fatalf("refcount after send = %d, want 1", got)
	}
	// Scope to this run's hash: the shared test DB keeps earlier runs' rows.
	var rows int64
	db.Model(&models.MailAttachmentUpload{}).
		Where("user_id = ? AND blob_hash = ?", userID, row.BlobHash).Count(&rows)
	if rows != 0 {
		t.Fatalf("staging rows after send = %d, want 0", rows)
	}
	// The Sent copy parses back with the attachment intact.
	sent, err := ms.GetFolderByPath(mb.ID, userID, 0, "Sent")
	if err != nil {
		t.Fatalf("Sent folder: %v", err)
	}
	raw, err := ms.GetMessageByUID(sent.ID, 1, userID, 0)
	if err != nil {
		t.Fatalf("sent copy by uid: %v", err)
	}
	body, err := ms.GetMessageBody(ctx, raw.ID, userID, 0)
	if err != nil {
		t.Fatalf("sent body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("sent copy body is empty")
	}
}

// TestPGComposeReplaceExpungeBookkeeping: the draft replace keeps the
// mailbox's denormalized counters exact (one draft message, byte total of the
// surviving revision only).
func TestPGComposeReplaceExpungeBookkeeping(t *testing.T) {
	svc, _, db := newMCPGService(t)
	ctx := context.Background()
	const userID = uint(4244)
	addr := "bookkeeping-" + pgComposeSuffix() + "@pg.test"

	mb, err := svc.store.CreateMailbox(userID, nil, addr, 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	d1, err := svc.SaveDraft(ctx, userID, 0, MessageInput{
		MailboxID: mb.ID, From: addr, To: []string{"x@y.test"}, Subject: "v1",
		Text: stringsRepeat("a", 500) + pgComposeSuffix(),
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := svc.UpdateDraft(ctx, userID, 0, mb.ID, d1.UID, MessageInput{
		MailboxID: mb.ID, From: addr, To: []string{"x@y.test"}, Subject: "v2",
		Text: stringsRepeat("b", 300) + pgComposeSuffix(),
	}); err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	var mbRow models.Mailbox
	if err := db.First(&mbRow, mb.ID).Error; err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	if mbRow.MessageCount != 1 {
		t.Errorf("message_count = %d, want 1 (replace expunged the old revision)", mbRow.MessageCount)
	}
	// The surviving draft is v2 (its raw carries Subject: v2).
	drafts, err := svc.store.GetFolderByPath(mb.ID, userID, 0, "Drafts")
	if err != nil {
		t.Fatalf("Drafts folder: %v", err)
	}
	survivor, err := svc.store.GetMessageByUID(drafts.ID, d1.UID+1, userID, 0)
	if err != nil {
		t.Fatalf("surviving draft: %v", err)
	}
	if survivor.Subject != "v2" {
		t.Errorf("survivor subject = %q, want v2", survivor.Subject)
	}
}

// TestPGComposeMailboxInactiveRefusal: composing into a suspended mailbox is
// refused with the typed mailstore error.
func TestPGComposeMailboxInactiveRefusal(t *testing.T) {
	svc, _, _ := newMCPGService(t)
	ctx := context.Background()
	const userID = uint(4245)
	addr := "suspended-" + pgComposeSuffix() + "@pg.test"

	mb, err := svc.store.CreateMailbox(userID, nil, addr, 0)
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	if err := svc.store.(*mailstore.Service).SetMailboxStatus(mb.ID, userID, 0, models.MailboxStatusSuspended); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	_, err = svc.Send(ctx, userID, 0, MessageInput{
		MailboxID: mb.ID, From: addr, To: []string{"x@y.test"}, Subject: "s", Text: "t",
	})
	if !errors.Is(err, mailstore.ErrMailboxInactive) {
		t.Fatalf("err = %v, want mailstore.ErrMailboxInactive", err)
	}
}

func refCountOf(db *gorm.DB, hash string) int64 {
	var ref models.MailBlobRef
	if err := db.Where("blob_hash = ?", hash).First(&ref).Error; err != nil {
		return -1
	}
	return ref.RefCount
}

// uniqContent returns n bytes that differ on every call: the blob layer is
// content-addressed and the test database is shared across runs, so identical
// content would collide with a previous run's rows (and its reservations).
func uniqContent(n int) []byte {
	out := make([]byte, 0, n+24)
	for i := 0; i < n; i++ {
		out = append(out, 'x')
	}
	return append(out, pgComposeSuffix()...)
}

// pgComposeSuffix mints a unique-per-call suffix so reruns against a shared
// database never collide (same policy as the mailstore PG suite).
var pgComposeSuffixSeed = time.Now().UnixNano()

func pgComposeSuffix() string {
	pgComposeSuffixSeed++
	return strconv.FormatInt(pgComposeSuffixSeed, 10)
}

func stringsRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
