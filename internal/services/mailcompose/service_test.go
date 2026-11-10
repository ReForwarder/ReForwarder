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
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/spamfilter"
)

// OB-5 W3 (ADR-005 decision 2 + ADR-006 scan hook) compose-seam behavior:
// the accepted-send order (gate → Sent copy → outbound), the ADR-006
// refusal/fail-mode semantics, the quota refusal ordering, the draft
// lifecycle, the reply-frame shape, and user isolation.

func cleanScanner() *fakeScanner { return &fakeScanner{enabled: true} }

func stage(t *testing.T, svc *Service, userID uint, name, content string) AttachmentRef {
	t.Helper()
	row, err := svc.UploadAttachment(context.Background(), userID, 0, name, "application/pdf", []byte(content))
	if err != nil {
		t.Fatalf("stage %q: %v", name, err)
	}
	return AttachmentRef{BlobHash: row.BlobHash, Name: row.Name, ContentType: row.ContentType}
}

// TestSendStoresSentCopyAndEnqueuesOutbound is the happy path (ask item 1):
// the Sent folder receives the message (quota-enforced Append, \Seen), the
// outbound pipeline receives the transport copy, and the two share the
// minted identity (Message-ID + the X-Posta-Email-Uuid linkage).
func TestSendStoresSentCopyAndEnqueuesOutbound(t *testing.T) {
	store := newFakeMailstore()
	mb := store.seedMailbox(7, 0, "ada@example.test", 0)
	sender := &fakeSender{}
	svc := newTestService(store, sender, cleanScanner(), newMemStaging(), newFakeDelivery())

	ref := stage(t, svc, 7, "notes.pdf", "pdf-bytes-1")
	res, err := svc.Send(context.Background(), 7, 0, MessageInput{
		MailboxID:   mb.ID,
		From:        "Ada <ada@example.test>",
		To:          []string{"bob@external.test"},
		Cc:          []string{"carol@external.test"},
		Bcc:         []string{"dave@hidden.test"},
		Subject:     "Quarterly notes",
		Text:        "See attached.",
		HTML:        "<p>See attached.</p>",
		Attachments: []AttachmentRef{ref},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Sent copy: exactly one append, into the Sent folder, flagged \Seen.
	if len(store.appends) != 1 {
		t.Fatalf("appends = %d, want 1", len(store.appends))
	}
	sent := store.appends[0]
	if sent.Folder != "Sent" {
		t.Errorf("sent copy landed in %q, want Sent", sent.Folder)
	}
	if sent.Flags&models.FlagSeen == 0 {
		t.Errorf("sent copy flags = %d, want \\Seen set", sent.Flags)
	}
	raw := string(sent.Raw)
	for _, want := range []string{
		"Subject: Quarterly notes",
		"To: bob@external.test",
		"Cc: carol@external.test",
		"Message-ID: <" + res.MessageID + ">",
		"X-Posta-Email-Uuid: " + res.EmailUUID,
		"notes.pdf",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("stored raw is missing %q\nraw:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, "dave@hidden.test") || strings.Contains(strings.ToLower(raw), "bcc:") {
		t.Errorf("stored raw leaks the bcc recipient:\n%s", raw)
	}

	// Outbound copy: envelope carries to+cc+bcc; identity matches the Sent copy.
	if sender.calls() != 1 {
		t.Fatalf("outbound calls = %d, want 1", sender.calls())
	}
	req := sender.reqs[0]
	got := map[string]bool{}
	for _, r := range req.To {
		got[r] = true
	}
	for _, want := range []string{"bob@external.test", "carol@external.test", "dave@hidden.test"} {
		if !got[want] {
			t.Errorf("outbound envelope missing %q (got %v)", want, req.To)
		}
	}
	if req.UserID != 7 || req.From != "Ada <ada@example.test>" || req.Subject != "Quarterly notes" {
		t.Errorf("outbound request mismatch: %+v", req)
	}
	if req.MessageID != res.MessageID || req.UUID != res.EmailUUID {
		t.Errorf("identity mismatch between copies: outbound (%s,%s) vs result (%s,%s)",
			req.UUID, req.MessageID, res.EmailUUID, res.MessageID)
	}
	if res.UID == 0 || res.FolderID == 0 || res.MailboxID != mb.ID {
		t.Errorf("send result addressing incomplete: %+v", res)
	}

	// The staged ref was consumed: its staging reservation is released and the
	// row is gone. (The fake's Append stores the whole raw under one hash — it
	// does not re-extract attachments; the real per-attachment reservation is
	// covered by the real-Postgres integration run.)
	if store.refCount(ref.BlobHash) != 0 {
		t.Errorf("staged blob reservation not released: ref count = %d, want 0", store.refCount(ref.BlobHash))
	}
}

// TestSendRefusesUnstagedAttachment is the send-time scan-gate (ask item 2):
// a ref this user never staged is refused before anything happens, and a
// positive-verdict content can never enter through a forged ref either —
// because staging only accepts clean uploads, the gate is the single entry.
func TestSendRefusesUnstagedAttachment(t *testing.T) {
	store := newFakeMailstore()
	mb := store.seedMailbox(7, 0, "ada@example.test", 0)
	sender := &fakeSender{}
	staging := newMemStaging()
	svc := newTestService(store, sender, cleanScanner(), staging, newFakeDelivery())

	_, err := svc.Send(context.Background(), 7, 0, MessageInput{
		MailboxID: mb.ID,
		From:      "ada@example.test",
		To:        []string{"bob@external.test"},
		Subject:   "with attachment",
		Text:      "hi",
		Attachments: []AttachmentRef{
			{BlobHash: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Name: "evil.bin", ContentType: "application/octet-stream"},
		},
	})
	if !errors.Is(err, ErrAttachmentNotStaged) {
		t.Fatalf("err = %v, want ErrAttachmentNotStaged", err)
	}
	if sender.calls() != 0 || len(store.appends) != 0 {
		t.Errorf("refused send leaked state: outbound calls=%d appends=%d", sender.calls(), len(store.appends))
	}
}

// TestUploadAttachmentInfectedRefusedWithSignature (ask item 2, ADR-006):
// a positive scan verdict refuses the upload with the signature; nothing is
// staged, nothing is reserved in the blob layer.
func TestUploadAttachmentInfectedRefusedWithSignature(t *testing.T) {
	store := newFakeMailstore()
	scanner := &fakeScanner{enabled: true, verdict: "Eicar-Signature"}
	sender := &fakeSender{}
	staging := newMemStaging()
	svc := newTestService(store, sender, scanner, staging, newFakeDelivery())

	_, err := svc.UploadAttachment(context.Background(), 7, 0, "eicar.txt", "text/plain", []byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"))
	var infected *AttachmentInfectedError
	if !errors.As(err, &infected) {
		t.Fatalf("err = %v, want *AttachmentInfectedError", err)
	}
	if infected.Signature != "Eicar-Signature" {
		t.Errorf("signature = %q, want Eicar-Signature", infected.Signature)
	}
	if staging.count(7) != 0 {
		t.Errorf("infected upload staged %d rows, want 0", staging.count(7))
	}
	for hash := range store.refs {
		if store.refs[hash] != 0 {
			t.Errorf("infected upload left blob ref %s at %d", hash, store.refs[hash])
		}
	}
}

// TestUploadAttachmentScannerUnavailableFailClosed (ADR-006): an unreachable
// scanner under a closed fail mode refuses the upload.
func TestUploadAttachmentScannerUnavailableFailClosed(t *testing.T) {
	store := newFakeMailstore()
	scanner := &fakeScanner{enabled: true, failMode: spamfilter.ClamFailClosed, err: errors.New("connect: refused")}
	svc := newTestService(store, &fakeSender{}, scanner, newMemStaging(), newFakeDelivery())

	_, err := svc.UploadAttachment(context.Background(), 7, 0, "doc.pdf", "application/pdf", []byte("pdf"))
	if !errors.Is(err, ErrScannerUnavailable) {
		t.Fatalf("err = %v, want ErrScannerUnavailable", err)
	}
}

// TestUploadAttachmentScannerUnavailableFailOpen (ADR-006): the open fail
// mode (the default) accepts the upload unscanned and records that state.
func TestUploadAttachmentScannerUnavailableFailOpen(t *testing.T) {
	store := newFakeMailstore()
	scanner := &fakeScanner{enabled: true, failMode: spamfilter.ClamFailOpen, err: errors.New("connect: refused")}
	staging := newMemStaging()
	svc := newTestService(store, &fakeSender{}, scanner, staging, newFakeDelivery())

	row, err := svc.UploadAttachment(context.Background(), 7, 0, "doc.pdf", "application/pdf", []byte("pdf"))
	if err != nil {
		t.Fatalf("fail-open upload: %v", err)
	}
	if row.ScanState != string(models.MailAttachmentScanUnavailable) {
		t.Errorf("scan state = %q, want %q", row.ScanState, models.MailAttachmentScanUnavailable)
	}
	// Fail-open content can still be sent — the deployment chose open.
	sender := &fakeSender{}
	svc2 := newTestService(store, sender, cleanScanner(), staging, newFakeDelivery())
	if _, err := svc2.Send(context.Background(), 7, 0, MessageInput{
		From: "ada@example.test", To: []string{"b@x.test"}, Subject: "s", Text: "t",
		Attachments: []AttachmentRef{{BlobHash: row.BlobHash}},
	}); err != nil {
		t.Fatalf("send with fail-open attachment: %v", err)
	}
}

// TestSendQuotaRefusalPreventsOutbound: the Sent copy is filed BEFORE the
// outbound enqueue, so an over-quota mailbox refuses the send before anything
// enters the pipeline (ask item 5 — quota refusal).
func TestSendQuotaRefusalPreventsOutbound(t *testing.T) {
	store := newFakeMailstore()
	mb := store.seedMailbox(7, 0, "ada@example.test", 1024)
	store.appendErr = &mailstore.QuotaError{
		MailboxID: mb.ID, Scope: mailstore.QuotaScopeMailboxBytes,
		Used: 1024, Incoming: 40, Limit: 1024,
	}
	sender := &fakeSender{}
	staging := newMemStaging()
	svc := newTestService(store, sender, cleanScanner(), staging, newFakeDelivery())
	ref := stage(t, svc, 7, "notes.txt", "tiny")

	_, err := svc.Send(context.Background(), 7, 0, MessageInput{
		MailboxID: mb.ID, From: "ada@example.test", To: []string{"bob@external.test"},
		Subject: "over quota", Text: "x", Attachments: []AttachmentRef{ref},
	})
	if !errors.Is(err, mailstore.ErrQuotaExceeded) {
		t.Fatalf("err = %v, want mailstore.ErrQuotaExceeded", err)
	}
	var qe *mailstore.QuotaError
	if !errors.As(err, &qe) || qe.Scope != mailstore.QuotaScopeMailboxBytes {
		t.Errorf("quota detail missing: %v", err)
	}
	if sender.calls() != 0 {
		t.Errorf("quota-refused send entered the outbound pipeline %d time(s)", sender.calls())
	}
	if staging.count(7) != 1 {
		t.Errorf("staging rows = %d, want 1 (kept so the user can retry)", staging.count(7))
	}
}

// TestDraftLifecycle covers ask item 3: create → update (same Message-ID, old
// revision expunged) → send-from-draft (stored content delivered, draft
// deleted, Sent copy filed).
func TestDraftLifecycle(t *testing.T) {
	store := newFakeMailstore()
	mb := store.seedMailbox(7, 0, "ada@example.test", 0)
	sender := &fakeSender{}
	staging := newMemStaging()
	svc := newTestService(store, sender, cleanScanner(), staging, newFakeDelivery())
	ctx := context.Background()

	// Create.
	ref := stage(t, svc, 7, "deck.pdf", "deck-bytes")
	draft, err := svc.SaveDraft(ctx, 7, 0, MessageInput{
		MailboxID: mb.ID, From: "ada@example.test", To: []string{"bob@external.test"},
		Subject: "Draft one", Text: "work in progress", Attachments: []AttachmentRef{ref},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	drafts := store.folderFor(mb.ID, "Drafts")
	if drafts == nil {
		t.Fatal("Drafts folder was not provisioned")
	}
	if !store.uidExists(drafts.ID, draft.UID) {
		t.Fatal("draft not found at the reported UID")
	}
	// The draft message carries the \Draft flag and no \Seen.
	draftMsg, _ := store.GetMessageByUID(drafts.ID, draft.UID, 7, 0)
	if draftMsg.Flags&models.FlagDraft == 0 {
		t.Errorf("draft flags = %d, want \\Draft set", draftMsg.Flags)
	}

	// Update: new UID, old expunged, Message-ID preserved, refs still valid.
	draft2, err := svc.UpdateDraft(ctx, 7, 0, mb.ID, draft.UID, MessageInput{
		From: "ada@example.test", To: []string{"bob@external.test", "carol@external.test"},
		Subject: "Draft one (edited)", Text: "still work in progress", Attachments: []AttachmentRef{ref},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if draft2.UID == draft.UID {
		t.Fatal("update did not allocate a new UID")
	}
	if draft2.MessageID != draft.MessageID {
		t.Errorf("Message-ID changed across edits: %q -> %q", draft.MessageID, draft2.MessageID)
	}
	if store.uidExists(drafts.ID, draft.UID) {
		t.Error("old draft revision survived the replace")
	}
	if staging.count(7) != 1 {
		t.Errorf("staging rows after update = %d, want 1 (rows survive draft edits)", staging.count(7))
	}

	// Send-from-draft: content comes from the STORED draft.
	res, err := svc.SendDraft(ctx, 7, 0, mb.ID, draft2.UID)
	if err != nil {
		t.Fatalf("SendDraft: %v", err)
	}
	if sender.calls() != 1 {
		t.Fatalf("outbound calls = %d, want 1", sender.calls())
	}
	req := sender.reqs[0]
	if req.Subject != "Draft one (edited)" || req.Text != "still work in progress" {
		t.Errorf("sent content does not match the stored draft: %+v", req)
	}
	if len(req.Attachments) != 1 || req.Attachments[0].Filename != "deck.pdf" {
		t.Errorf("draft attachment not carried to the outbound copy: %+v", req.Attachments)
	}
	if len(req.To) != 2 {
		t.Errorf("envelope recipients = %v, want both draft to-addrs", req.To)
	}
	// The draft is gone; the Sent copy exists; the staging rows are consumed.
	if store.uidExists(drafts.ID, draft2.UID) {
		t.Error("draft survived an accepted send")
	}
	if len(store.appends) != 3 { // draft save + draft replace + sent copy
		t.Fatalf("appends = %d, want 3", len(store.appends))
	}
	if store.appends[2].Folder != "Sent" {
		t.Errorf("send-from-draft filed the copy in %q, want Sent", store.appends[2].Folder)
	}
	if staging.count(7) != 0 {
		t.Errorf("staging rows after accepted send = %d, want 0", staging.count(7))
	}
	_ = res
}

// TestSendDraftRefusalKeepsDraft: a refused send (pipeline error) leaves the
// draft intact — deletion happens only on acceptance.
func TestSendDraftRefusalKeepsDraft(t *testing.T) {
	store := newFakeMailstore()
	mb := store.seedMailbox(7, 0, "ada@example.test", 0)
	sender := &fakeSender{err: errors.New("pipeline down")}
	svc := newTestService(store, sender, cleanScanner(), newMemStaging(), newFakeDelivery())
	ctx := context.Background()

	draft, err := svc.SaveDraft(ctx, 7, 0, MessageInput{
		MailboxID: mb.ID, From: "ada@example.test", To: []string{"bob@external.test"},
		Subject: "keep me", Text: "body",
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	drafts := store.folderFor(mb.ID, "Drafts")
	if _, err := svc.SendDraft(ctx, 7, 0, mb.ID, draft.UID); err == nil {
		t.Fatal("SendDraft unexpectedly succeeded with a failing pipeline")
	}
	if !store.uidExists(drafts.ID, draft.UID) {
		t.Error("refused send deleted the draft")
	}
}

// TestReplyFrameShape covers ask item 4: the prefill for reply, reply-all
// (minus the mailbox's own address) and forward.
func TestReplyFrameShape(t *testing.T) {
	store := newFakeMailstore()
	mb := store.seedMailbox(7, 0, "ada@example.test", 0)
	svc := newTestService(store, &fakeSender{}, cleanScanner(), newMemStaging(), newFakeDelivery())

	// Seed the original message in the mailbox's INBOX via a direct append.
	inbox := store.folderFor(mb.ID, "INBOX")
	original := "Reply-To: Bob <bob@external.test>\r\nFrom: bob@external.test\r\nTo: ada@example.test, carol@external.test\r\nCc: dave@external.test\r\nSubject: Lunch tomorrow\r\nDate: Thu, 05 Nov 2026 10:00:00 +0000\r\nMessage-ID: <lunch-1@external.test>\r\nReferences: <thread-root@external.test>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nAre we on for lunch?\r\n"
	if _, err := store.Append(context.Background(), 7, 0, mailstore.AppendIn{MailboxID: mb.ID, FolderID: inbox.ID, Raw: []byte(original)}); err != nil {
		t.Fatalf("seed original: %v", err)
	}
	ctx := context.Background()

	// Reply.
	frame, err := svc.ReplyFrame(ctx, 7, 0, mb.ID, inbox.ID, 1, FrameReply)
	if err != nil {
		t.Fatalf("ReplyFrame(reply): %v", err)
	}
	if frame.To == nil || len(frame.To) != 1 || addressOnly(frame.To[0]) != "bob@external.test" {
		t.Errorf("reply To = %v, want the Reply-To address", frame.To)
	}
	if frame.Subject != "Re: Lunch tomorrow" {
		t.Errorf("reply Subject = %q", frame.Subject)
	}
	if frame.InReplyTo != "lunch-1@external.test" {
		t.Errorf("InReplyTo = %q", frame.InReplyTo)
	}
	if !strings.Contains(frame.References, "<thread-root@external.test>") || !strings.Contains(frame.References, "<lunch-1@external.test>") {
		t.Errorf("References = %q, want the thread chain", frame.References)
	}
	if !strings.Contains(frame.QuoteText, "> Are we on for lunch?") || !strings.Contains(frame.QuoteText, "bob@external.test") {
		t.Errorf("quote text malformed: %q", frame.QuoteText)
	}

	// Reply-all: everyone but the reply target and the mailbox's own address.
	all, err := svc.ReplyFrame(ctx, 7, 0, mb.ID, inbox.ID, 1, FrameReplyAll)
	if err != nil {
		t.Fatalf("ReplyFrame(reply_all): %v", err)
	}
	ccSet := map[string]bool{}
	for _, a := range all.Cc {
		ccSet[addressOnly(a)] = true
	}
	if !ccSet["carol@external.test"] || !ccSet["dave@external.test"] {
		t.Errorf("reply-all Cc = %v, want carol + dave", all.Cc)
	}
	if ccSet["ada@example.test"] || ccSet["bob@external.test"] {
		t.Errorf("reply-all Cc leaks self/target: %v", all.Cc)
	}

	// Forward: fresh recipients, Fwd: subject, no threading headers.
	fwd, err := svc.ReplyFrame(ctx, 7, 0, mb.ID, inbox.ID, 1, FrameForward)
	if err != nil {
		t.Fatalf("ReplyFrame(forward): %v", err)
	}
	if fwd.Subject != "Fwd: Lunch tomorrow" || len(fwd.To) != 0 || fwd.InReplyTo != "" {
		t.Errorf("forward frame wrong: subject=%q to=%v inReplyTo=%q", fwd.Subject, fwd.To, fwd.InReplyTo)
	}

	// Unknown mode refused.
	if _, err := svc.ReplyFrame(ctx, 7, 0, mb.ID, inbox.ID, 1, "sideways"); err == nil {
		t.Error("unknown frame mode accepted")
	}
}

// TestDeliveryStatusSurfacesOutboundTimeline: the Sent copy's X-Posta-Email-Uuid
// header keys the email_events timeline (ask item 1's delivery-status clause).
func TestDeliveryStatusSurfacesOutboundTimeline(t *testing.T) {
	store := newFakeMailstore()
	mb := store.seedMailbox(7, 0, "ada@example.test", 0)
	sender := &fakeSender{}
	delivery := newFakeDelivery()
	svc := newTestService(store, sender, cleanScanner(), newMemStaging(), delivery)
	ctx := context.Background()

	res, err := svc.Send(ctx, 7, 0, MessageInput{
		MailboxID: mb.ID, From: "ada@example.test", To: []string{"bob@external.test"},
		Subject: "status check", Text: "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	delivery.seed(res.EmailUUID, 7, &models.Email{
		Status: models.EmailStatusSent, Subject: "status check", Sender: "ada@example.test",
		Recipients: []string{"bob@external.test"},
	},
		models.EmailEvent{Type: models.EmailEventComposed},
		models.EmailEvent{Type: models.EmailEventSent},
	)

	sent := store.appends[0]
	status, err := svc.DeliveryStatus(ctx, 7, 0, sent.FolderID, res.UID)
	if err != nil {
		t.Fatalf("DeliveryStatus: %v", err)
	}
	if status.EmailUUID != res.EmailUUID || status.Status != string(models.EmailStatusSent) {
		t.Errorf("delivery status mismatch: %+v", status)
	}
	if len(status.Events) != 2 || status.Events[0].Type != models.EmailEventComposed || status.Events[1].Type != models.EmailEventSent {
		t.Errorf("events = %+v, want composed+sent", status.Events)
	}

	// A stored copy with no outbound linkage (not compose-produced) reads as
	// not-found rather than leaking some other row.
	other, err := svc.Send(ctx, 7, 0, MessageInput{MailboxID: mb.ID, From: "ada@example.test", To: []string{"b@x.test"}, Subject: "two", Text: "x"})
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	delivery.mu.Lock()
	delete(delivery.emails, other.EmailUUID)
	delivery.mu.Unlock()
	if _, err := svc.DeliveryStatus(ctx, 7, 0, other.FolderID, other.UID); !errors.Is(err, mailstore.ErrMessageNotFound) {
		t.Errorf("unlinked message delivery status err = %v, want not found", err)
	}
}

// TestComposeIsolationBetweenUsers: nothing of user A's compose state is
// reachable by user B — drafts by folder scoping, attachment refs by the
// staging owner, delivery status by the Email row's owner.
func TestComposeIsolationBetweenUsers(t *testing.T) {
	store := newFakeMailstore()
	mbA := store.seedMailbox(7, 0, "ada@example.test", 0)
	mbB := store.seedMailbox(8, 0, "bob@example.test", 0)
	sender := &fakeSender{}
	staging := newMemStaging()
	delivery := newFakeDelivery()
	svc := newTestService(store, sender, cleanScanner(), staging, delivery)
	ctx := context.Background()

	// A stages a ref and saves a draft.
	ref := stage(t, svc, 7, "private.txt", "ada-private-content")
	draft, err := svc.SaveDraft(ctx, 7, 0, MessageInput{
		MailboxID: mbA.ID, From: "ada@example.test", To: []string{"x@y.test"},
		Subject: "A's draft", Text: "secret-ish", Attachments: []AttachmentRef{ref},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	// B cannot reference A's staged ref.
	if _, err := svc.Send(ctx, 8, 0, MessageInput{
		MailboxID: mbB.ID, From: "bob@example.test", To: []string{"x@y.test"},
		Subject: "steal", Text: "try", Attachments: []AttachmentRef{ref},
	}); !errors.Is(err, ErrAttachmentNotStaged) {
		t.Errorf("B using A's ref: err = %v, want ErrAttachmentNotStaged", err)
	}

	// B cannot read A's draft through the frame path (folder scoping refuses
	// the foreign folder).
	draftsA := store.folderFor(mbA.ID, "Drafts")
	if _, err := svc.ReplyFrame(ctx, 8, 0, mbB.ID, draftsA.ID, draft.UID, FrameReply); !errors.Is(err, mailstore.ErrNotMailboxOwner) {
		t.Errorf("B reading A's folder: err = %v, want owner refusal", err)
	}

	// B cannot update A's draft even knowing mailbox+folder+uid.
	if _, err := svc.UpdateDraft(ctx, 8, 0, mbA.ID, draft.UID, MessageInput{From: "bob@example.test"}); !errors.Is(err, ErrNotMailboxOwner) {
		t.Errorf("B updating A's draft: err = %v, want ErrNotMailboxOwner", err)
	}

	// B cannot read A's delivery status.
	res, err := svc.Send(ctx, 7, 0, MessageInput{MailboxID: mbA.ID, From: "ada@example.test", To: []string{"x@y.test"}, Subject: "a-mail", Text: "x"})
	if err != nil {
		t.Fatalf("A send: %v", err)
	}
	if _, err := svc.DeliveryStatus(ctx, 8, 0, res.FolderID, res.UID); err == nil {
		t.Error("B read A's delivery status")
	}

	// B's send from A's mailbox id is refused.
	if _, err := svc.Send(ctx, 8, 0, MessageInput{
		MailboxID: mbA.ID, From: "bob@example.test", To: []string{"x@y.test"}, Subject: "hi", Text: "x",
	}); !errors.Is(err, ErrNotMailboxOwner) {
		t.Errorf("B sending from A's mailbox: err = %v, want ErrNotMailboxOwner", err)
	}
}

// TestSendAutoProvisionsMailboxAndFolders: composing from an address with no
// mailbox yet provisions it (named by the From address, inbound-seam
// convention) and the Sent folder on first use.
func TestSendAutoProvisionsMailboxAndFolders(t *testing.T) {
	store := newFakeMailstore()
	sender := &fakeSender{}
	svc := newTestService(store, sender, cleanScanner(), newMemStaging(), newFakeDelivery())

	res, err := svc.Send(context.Background(), 7, 0, MessageInput{
		From: "Newbie <new.address@example.test>", To: []string{"x@y.test"},
		Subject: "first", Text: "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	mb, err := store.FindMailboxByName(7, "new.address@example.test")
	if err != nil {
		t.Fatalf("mailbox not provisioned: %v", err)
	}
	if res.MailboxID != mb.ID {
		t.Errorf("result mailbox = %d, want provisioned %d", res.MailboxID, mb.ID)
	}
	if store.folderFor(mb.ID, "Sent") == nil {
		t.Error("Sent folder not provisioned")
	}
	if len(store.appends) != 1 || store.appends[0].Folder != "Sent" {
		t.Errorf("unexpected appends: %+v", store.appends)
	}
}

// TestSendNoMailboxWhenAutoProvisionDisabled: the same situation under a
// disabled auto-provision refuses with the typed error.
func TestSendNoMailboxWhenAutoProvisionDisabled(t *testing.T) {
	store := newFakeMailstore()
	sender := &fakeSender{}
	svc := New(store, sender, cleanScanner(), newMemStaging(), newFakeDelivery(), Config{AutoProvision: false})

	_, err := svc.Send(context.Background(), 7, 0, MessageInput{
		From: "ghost@example.test", To: []string{"x@y.test"}, Subject: "s", Text: "t",
	})
	if !errors.Is(err, ErrNoMailbox) {
		t.Fatalf("err = %v, want ErrNoMailbox", err)
	}
	if sender.calls() != 0 {
		t.Error("refused send reached the outbound pipeline")
	}
}

// TestUploadDedupsIdenticalContent: re-uploading the same bytes under one user
// stages once (one reservation) and refreshes the metadata.
func TestUploadDedupsIdenticalContent(t *testing.T) {
	store := newFakeMailstore()
	scanner := cleanScanner()
	staging := newMemStaging()
	svc := newTestService(store, &fakeSender{}, scanner, staging, newFakeDelivery())

	a, err := svc.UploadAttachment(context.Background(), 7, 0, "a.pdf", "application/pdf", []byte("same-bytes"))
	if err != nil {
		t.Fatalf("upload a: %v", err)
	}
	b, err := svc.UploadAttachment(context.Background(), 7, 0, "b.pdf", "application/pdf", []byte("same-bytes"))
	if err != nil {
		t.Fatalf("upload b: %v", err)
	}
	if a.BlobHash != b.BlobHash {
		t.Fatalf("identical content hashed differently: %s vs %s", a.BlobHash, b.BlobHash)
	}
	if staging.count(7) != 1 {
		t.Errorf("staging rows = %d, want 1", staging.count(7))
	}
	if b.Name != "b.pdf" {
		t.Errorf("re-upload did not refresh metadata: name=%q", b.Name)
	}
	if store.refCount(a.BlobHash) != 1 {
		t.Errorf("ref count = %d, want 1 (dedup, single reservation)", store.refCount(a.BlobHash))
	}
	if scanner.scans != 2 {
		t.Errorf("scans = %d, want 2 (each upload is scanned)", scanner.scans)
	}
}
