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

package mailstore

import (
	"context"
	"errors"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// Admin-surface suite (OB-5 W2, ADR-005 decision 3) over the same fake-driver
// harness as mailstore_test.go: the Admin* paths must reach any owner's
// mailbox, while the owner-scoped paths keep refusing cross-owner access. The
// AdminUserExists / AdminUserInWorkspace counts run against the users /
// workspace_members tables, which the fake driver does not model — they are
// covered by the admin HTTP handler battery (internal/handlers), whose seam
// fake owns those tables.

// TestAdminListMailboxesFiltersAndPaging — the platform-wide listing pages
// oldest-first with a truthful total, and every filter narrows correctly.
func TestAdminListMailboxesFiltersAndPaging(t *testing.T) {
	svc, _, _ := newFakeService(t)
	// Three mailboxes across two owners; creation order fixes the id order.
	mbA1 := seedUserMailbox(t, svc, aliceID, "alice@example.test", 1000)
	seedUserMailbox(t, svc, aliceID, "alice-2@example.test", 0)
	mbB1 := seedUserMailbox(t, svc, bobID, "bob@example.test", 0)
	if err := svc.SetMailboxStatus(mbB1.ID, bobID, 0, models.MailboxStatusSuspended); err != nil {
		t.Fatalf("suspend bob mailbox: %v", err)
	}

	// Unpaged: all three, oldest first, total 3.
	rows, total, err := svc.AdminListMailboxes(AdminMailboxFilter{}, 0, 0)
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("rows = %d total = %d, want 3/3", len(rows), total)
	}
	if rows[0].ID != mbA1.ID || rows[2].ID != mbB1.ID {
		t.Fatalf("ordering wrong: first=%d last=%d, want %d..%d", rows[0].ID, rows[2].ID, mbA1.ID, mbB1.ID)
	}

	// Owner filter: only alice's two.
	rows, total, err = svc.AdminListMailboxes(AdminMailboxFilter{UserID: aliceID}, 0, 0)
	if err != nil {
		t.Fatalf("admin list by user: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("alice rows = %d total = %d, want 2/2", len(rows), total)
	}
	for _, r := range rows {
		if r.UserID != aliceID {
			t.Fatalf("owner filter leaked user %d", r.UserID)
		}
	}

	// Status filter: only the suspended one.
	rows, total, err = svc.AdminListMailboxes(AdminMailboxFilter{Status: models.MailboxStatusSuspended}, 0, 0)
	if err != nil {
		t.Fatalf("admin list by status: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != mbB1.ID {
		t.Fatalf("suspended rows = %d total = %d, want bob's mailbox only", len(rows), total)
	}

	// Name search is a case-insensitive literal substring: a wildcard in the
	// input must match literally, not as a pattern.
	rows, total, err = svc.AdminListMailboxes(AdminMailboxFilter{Search: "ALICE-2"}, 0, 0)
	if err != nil {
		t.Fatalf("admin list by search: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Name != "alice-2@example.test" {
		t.Fatalf("search rows = %d total = %d, want alice-2 only", len(rows), total)
	}
	if rows, total, err = svc.AdminListMailboxes(AdminMailboxFilter{Search: "%"}, 0, 0); err != nil || total != 0 {
		t.Fatalf("bare %% must match nothing literally (rows=%d total=%d err=%v)", len(rows), total, err)
	}

	// Paging: size 2 page 2 (offset 2) carries the last row; total stays 3.
	rows, total, err = svc.AdminListMailboxes(AdminMailboxFilter{}, 2, 2)
	if err != nil {
		t.Fatalf("admin list paged: %v", err)
	}
	if total != 3 || len(rows) != 1 || rows[0].ID != mbB1.ID {
		t.Fatalf("paged rows = %d total = %d first = %d, want last page with bob only", len(rows), total, rows[0].ID)
	}
}

// TestAdminGetMailboxCrossOwner — the admin read reaches any owner's mailbox;
// the owner-scoped read of the same id still refuses the other user.
func TestAdminGetMailboxCrossOwner(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "alice@example.test", 0)

	got, err := svc.AdminGetMailbox(mb.ID)
	if err != nil {
		t.Fatalf("admin get: %v", err)
	}
	if got.ID != mb.ID || got.UserID != aliceID {
		t.Fatalf("admin get returned mailbox %+v", got)
	}
	if _, err := svc.AdminGetMailbox(99999); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatalf("unknown id err = %v, want ErrMailboxNotFound", err)
	}
	// The owner-scoped path is untouched by the admin additions.
	if _, err := svc.GetMailbox(mb.ID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("owner-scoped get err = %v, want ErrNotMailboxOwner", err)
	}
}

// TestAdminProvisionMailbox — provisioning creates mailbox + INBOX with the
// requested quotas, refuses duplicates with the typed error, and rejects
// negative quotas.
func TestAdminProvisionMailbox(t *testing.T) {
	svc, _, _ := newFakeService(t)

	mb, err := svc.AdminProvisionMailbox(aliceID, nil, "new@example.test", 2048, 50)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if mb.QuotaBytes != 2048 || mb.QuotaMessages != 50 || mb.Status != models.MailboxStatusActive {
		t.Fatalf("provisioned mailbox wrong: %+v", mb)
	}
	inboxFolder(t, svc, mb) // fatal inside when the INBOX row is missing

	// Duplicate name for the same owner → typed conflict.
	if _, err := svc.AdminProvisionMailbox(aliceID, nil, "new@example.test", 0, 0); !errors.Is(err, ErrMailboxNameTaken) {
		t.Fatalf("duplicate err = %v, want ErrMailboxNameTaken", err)
	}
	// Same name for a different owner is fine ((user_id, name) is the key).
	if _, err := svc.AdminProvisionMailbox(bobID, nil, "new@example.test", 0, 0); err != nil {
		t.Fatalf("same name other owner: %v", err)
	}

	if _, err := svc.AdminProvisionMailbox(aliceID, nil, "neg@example.test", -1, 0); err == nil {
		t.Fatal("negative byte quota must be refused")
	}
	if _, err := svc.AdminProvisionMailbox(aliceID, nil, "neg@example.test", 0, -5); err == nil {
		t.Fatal("negative message quota must be refused")
	}
}

// TestAdminQuotaAndStatus — the admin quota write lands on the mailbox the
// owner-scoped quota enforcement then honors, suspend/reactivate move only
// between active and suspended, and the closed transition stays reserved.
func TestAdminQuotaAndStatus(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "quota@example.test", 1000)
	inbox := inboxFolder(t, svc, mb)

	updated, err := svc.AdminSetMailboxQuota(mb.ID, 1000000, 2)
	if err != nil {
		t.Fatalf("admin quota: %v", err)
	}
	if updated.QuotaBytes != 1000000 || updated.QuotaMessages != 2 {
		t.Fatalf("quota not applied: %+v", updated)
	}

	// The new quotas bite the normal append path: message #3 exceeds the
	// message-count quota.
	for i := 0; i < 2; i++ {
		if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("q", "s@example.test", "quota@example.test", "x")}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("q3", "s@example.test", "quota@example.test", "x")}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("third append err = %v, want ErrQuotaExceeded", err)
	}

	// Lift the count cap so the suspend phase tests status, not quota.
	if _, err := svc.AdminSetMailboxQuota(mb.ID, 1000000, 0); err != nil {
		t.Fatalf("lift quota: %v", err)
	}

	if _, err := svc.AdminSetMailboxQuota(mb.ID, -1, 0); err == nil {
		t.Fatal("negative byte quota must be refused")
	}

	// Suspend → appends refuse; reactivate → appends work again.
	if _, err := svc.AdminSetMailboxStatus(mb.ID, models.MailboxStatusSuspended); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("s1", "s@example.test", "quota@example.test", "x")}); !errors.Is(err, ErrMailboxInactive) {
		t.Fatalf("append to suspended err = %v, want ErrMailboxInactive", err)
	}
	if _, err := svc.AdminSetMailboxStatus(mb.ID, models.MailboxStatusActive); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: simpleRaw("s2", "s@example.test", "quota@example.test", "x")}); err != nil {
		t.Fatalf("append after reactivate: %v", err)
	}

	// Closed is not an admin-surface status (deletion is the removal path).
	if _, err := svc.AdminSetMailboxStatus(mb.ID, models.MailboxStatusClosed); err == nil {
		t.Fatal("admin status change to closed must be refused")
	}
	// And unknown statuses are refused.
	if _, err := svc.AdminSetMailboxStatus(mb.ID, models.MailboxStatus("flying")); err == nil {
		t.Fatal("unknown status must be refused")
	}
}

// TestAdminDeleteMailboxCascade — the admin delete cascades another user's
// mailbox exactly like the owner-scoped delete: folders, messages and blob
// ref counts go to zero; the owner-scoped delete then reports not-found.
func TestAdminDeleteMailboxCascade(t *testing.T) {
	svc, state, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "admin-del@example.test", 0)
	inbox := inboxFolder(t, svc, mb)
	raw := simpleRaw("adel", "s@example.test", "admin-del@example.test", "payload")
	if _, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Sanity: the other user cannot delete it owner-scoped.
	if err := svc.DeleteMailbox(mb.ID, bobID, 0); !errors.Is(err, ErrNotMailboxOwner) {
		t.Fatalf("cross-owner delete err = %v, want ErrNotMailboxOwner", err)
	}
	// The admin path can.
	if err := svc.AdminDeleteMailbox(mb.ID); err != nil {
		t.Fatalf("admin delete: %v", err)
	}
	state.mu.Lock()
	folders := len(state.tables["mail_folders"].rows)
	messages := len(state.tables["mail_messages"].rows)
	refs := len(state.tables["mail_blob_refs"].rows)
	var held int64
	for _, r := range state.tables["mail_blob_refs"].rows {
		if c := r[state.colIndex("mail_blob_refs", "ref_count")].(int64); c > 0 {
			held = c
		}
	}
	state.mu.Unlock()
	if folders != 0 || messages != 0 {
		t.Fatalf("cascade incomplete: folders=%d messages=%d", folders, messages)
	}
	if held != 0 {
		t.Fatalf("blob ref still held after admin cascade: %d", held)
	}
	if refs == 0 {
		t.Fatal("blob refs vanished instead of being decremented to 0")
	}
	if _, err := svc.AdminGetMailbox(mb.ID); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatalf("mailbox survived admin delete: %v", err)
	}
	if err := svc.AdminDeleteMailbox(mb.ID); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatalf("second admin delete err = %v, want ErrMailboxNotFound", err)
	}
}

// TestAdminMailboxUsage — the usage report matches the counters the appends
// maintain and the dedup-aware blob totals.
func TestAdminMailboxUsage(t *testing.T) {
	svc, _, _ := newFakeService(t)
	mb := seedUserMailbox(t, svc, aliceID, "usage@example.test", 4096)
	inbox := inboxFolder(t, svc, mb)
	raw := simpleRaw("u1", "s@example.test", "usage@example.test", "hello usage")
	res, err := svc.Append(context.Background(), aliceID, 0, AppendIn{FolderID: inbox.ID, Raw: raw})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	report, err := svc.AdminMailboxUsage(mb.ID)
	if err != nil {
		t.Fatalf("admin usage: %v", err)
	}
	if report.MailboxID != mb.ID || report.UserID != aliceID || report.Name != mb.Name {
		t.Fatalf("report identity wrong: %+v", report)
	}
	if report.QuotaBytes != 4096 || report.RawBytes != int64(len(raw)) || report.MessageCount != 1 {
		t.Fatalf("counter mismatch: quota=%d raw=%d count=%d", report.QuotaBytes, report.RawBytes, report.MessageCount)
	}
	if report.BlobBytes <= 0 || report.DistinctBlobs < 1 {
		t.Fatalf("blob totals not reported: blob_bytes=%d distinct=%d", report.BlobBytes, report.DistinctBlobs)
	}
	if _, err := svc.GetMessage(res.MessageID, aliceID, 0); err != nil {
		t.Fatalf("message lost: %v", err)
	}

	if _, err := svc.AdminMailboxUsage(424242); !errors.Is(err, ErrMailboxNotFound) {
		t.Fatalf("unknown mailbox usage err = %v, want ErrMailboxNotFound", err)
	}
}
