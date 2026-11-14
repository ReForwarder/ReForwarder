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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package inbound

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

// SF-65 integration tests: the deliver action's alias/group expansion end to
// end over the mailbox-delivery suite's REAL-Postgres harness (mdDB,
// mdNewFixture — mailbox_delivery_pg_test.go): the expansion's correctness
// rests on the real (user_id, name) mailbox identity, the unique alias/group
// address indexes and the full production schema. Each test works in one
// transaction that is rolled back.

// agFixture extends mdFixture with the SF-65 expansion wired into the
// deliver seam and the alias/group repositories.
type agFixture struct {
	*mdFixture
	aliases *repositories.MailAliasRepository
	groups  *repositories.MailGroupRepository
}

// wsPtrOf turns the fixture workspace id into the nullable ownership column.
func wsPtrOf(wsID uint) *uint { return &wsID }

// newAGFixture seeds the md fixture and rewires the deliver seam with the
// SF-65 alias/group resolver (the production wiring in cmd/posta/server.go).
func newAGFixture(t *testing.T, autoProvision bool) *agFixture {
	t.Helper()
	tx := mdDB(t)
	base := mdNewFixture(t, tx, autoProvision)
	aliases := repositories.NewMailAliasRepository(base.tx)
	groups := repositories.NewMailGroupRepository(base.tx)
	delivery := NewMailboxDelivery(base.msSvc, repositories.NewDomainRepository(base.tx), MailboxDeliveryConfig{
		AutoProvision: autoProvision,
	})
	delivery.SetAliasGroups(repositories.NewMailAliasGroupResolver(base.tx))
	base.svc.SetMailboxDeliverer(delivery)
	return &agFixture{mdFixture: base, aliases: aliases, groups: groups}
}

// agIngestSeq keeps the per-message Message-IDs distinct within a test.
var agIngestSeq int

// agIngest runs the SMTP receiver's ingest path for arbitrary recipients on
// the fixture's verified domain.
func (f *agFixture) agIngest(t *testing.T, recipients ...string) *models.InboundEmail {
	t.Helper()
	agIngestSeq++
	msgID := fmt.Sprintf("ag-%d-%d", time.Now().UnixNano(), agIngestSeq)
	rec, err := f.svc.IngestRaw(context.Background(), f.mdRaw(msgID, "alias-group delivery"),
		"sender@example.remote", recipients, models.InboundSourceSMTP, "")
	if err != nil {
		t.Fatalf("ingest raw: %v", err)
	}
	return rec
}

// agMailbox provisions a named mailbox for the fixture's tenant user.
func (f *agFixture) agMailbox(t *testing.T, name string) *models.Mailbox {
	t.Helper()
	mb, err := f.msSvc.CreateMailbox(f.userID, wsPtrOf(f.wsID), name, 0)
	if err != nil {
		t.Fatalf("seed mailbox %s: %v", name, err)
	}
	return mb
}

// agInboxCount counts the INBOX messages of one mailbox.
func (f *agFixture) agInboxCount(t *testing.T, mb *models.Mailbox) int {
	t.Helper()
	return len(f.mdInboxMessages(t, mb.ID))
}

// TestPGAliasDelivery — mail to an alias address lands in the alias's target
// mailbox: exactly one copy, no mailbox auto-provisioned under the alias
// address, and the alias address does not resolve as a direct mailbox.
func TestPGAliasDelivery(t *testing.T) {
	f := newAGFixture(t, true)

	target := f.agMailbox(t, "ada@"+f.domName)

	alias := &models.MailAlias{
		UserID:      f.userID,
		WorkspaceID: wsPtrOf(f.wsID),
		MailboxID:   target.ID,
		Address:     "team@" + f.domName,
	}
	if err := f.aliases.Create(alias); err != nil {
		t.Fatalf("create alias: %v", err)
	}

	f.agIngest(t, "team@"+f.domName)

	if got := f.agInboxCount(t, target); got != 1 {
		t.Fatalf("target INBOX messages = %d, want exactly 1 (the alias delivery)", got)
	}
	// The alias address must not have provisioned a mailbox of its own —
	// the alias mapping replaces the direct resolution.
	if n := f.mdMailboxCount(t, "team@"+f.domName); n != 0 {
		t.Fatalf("mailboxes named %q = %d, want 0 (the alias must not shadow-provision)", "team@"+f.domName, n)
	}
}

// TestPGGroupFanOutIdempotent — mail to a group address fans out to every
// member mailbox exactly once, the anchor mailbox receives nothing from its
// own group, and a member reached through SEVERAL paths in one delivery
// (group membership + direct address + alias) still receives exactly one
// copy per message.
func TestPGGroupFanOutIdempotent(t *testing.T) {
	f := newAGFixture(t, true)

	mbA := f.agMailbox(t, "ada@"+f.domName)
	mbB := f.agMailbox(t, "bob@"+f.domName)

	anchor, err := f.msSvc.CreateMailbox(f.userID, wsPtrOf(f.wsID), "crew@"+f.domName, 0)
	if err != nil {
		t.Fatalf("provision group anchor: %v", err)
	}
	group := &models.MailGroup{
		UserID:      f.userID,
		WorkspaceID: wsPtrOf(f.wsID),
		MailboxID:   anchor.ID,
		Address:     "crew@" + f.domName,
	}
	if err := f.groups.Create(group, []uint{mbA.ID, mbB.ID}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	// Plain group delivery: both members exactly once, anchor untouched.
	f.agIngest(t, "crew@"+f.domName)
	if got := f.agInboxCount(t, mbA); got != 1 {
		t.Fatalf("member A INBOX = %d, want 1", got)
	}
	if got := f.agInboxCount(t, mbB); got != 1 {
		t.Fatalf("member B INBOX = %d, want 1", got)
	}
	if got := f.agInboxCount(t, anchor); got != 0 {
		t.Fatalf("anchor INBOX = %d, want 0 (the anchor receives nothing from its own group)", got)
	}

	// Idempotency across paths: one delivery addressing the group AND member
	// A directly AND an alias onto member A → member A still exactly one
	// copy from that message (total 2 after both deliveries), member B one.
	alias := &models.MailAlias{
		UserID:      f.userID,
		WorkspaceID: wsPtrOf(f.wsID),
		MailboxID:   mbA.ID,
		Address:     "ada-alias@" + f.domName,
	}
	if err := f.aliases.Create(alias); err != nil {
		t.Fatalf("create alias onto member A: %v", err)
	}
	f.agIngest(t, "crew@"+f.domName, "ada@"+f.domName, "ada-alias@"+f.domName)

	if got := f.agInboxCount(t, mbA); got != 2 {
		t.Fatalf("member A INBOX after the multi-path delivery = %d, want 2 (one per message)", got)
	}
	if got := f.agInboxCount(t, mbB); got != 2 {
		t.Fatalf("member B INBOX after the multi-path delivery = %d, want 2 (one per message)", got)
	}
}

// TestPGAliasGroupLoopProtection — a cyclic group configuration terminates:
// G1 contains G2's anchor and G2 contains G1's anchor; delivery to G1
// finishes (no hang, no duplicate) and the member mailboxes each receive
// exactly one copy while the two anchors receive nothing. The nested
// (acyclic) hop DOES expand: Y receives through G2.
func TestPGAliasGroupLoopProtection(t *testing.T) {
	f := newAGFixture(t, true)

	mbX := f.agMailbox(t, "x@"+f.domName)
	mbY := f.agMailbox(t, "y@"+f.domName)

	anchor1, err := f.msSvc.CreateMailbox(f.userID, wsPtrOf(f.wsID), "g1@"+f.domName, 0)
	if err != nil {
		t.Fatalf("anchor 1: %v", err)
	}
	anchor2, err := f.msSvc.CreateMailbox(f.userID, wsPtrOf(f.wsID), "g2@"+f.domName, 0)
	if err != nil {
		t.Fatalf("anchor 2: %v", err)
	}
	g1 := &models.MailGroup{UserID: f.userID, WorkspaceID: wsPtrOf(f.wsID), MailboxID: anchor1.ID, Address: "g1@" + f.domName}
	g2 := &models.MailGroup{UserID: f.userID, WorkspaceID: wsPtrOf(f.wsID), MailboxID: anchor2.ID, Address: "g2@" + f.domName}
	if err := f.groups.Create(g1, []uint{mbX.ID, anchor2.ID}); err != nil {
		t.Fatalf("create G1: %v", err)
	}
	if err := f.groups.Create(g2, []uint{mbY.ID, anchor1.ID}); err != nil {
		t.Fatalf("create G2: %v", err)
	}

	// Delivery to G1 walks G1 → [X, G2(anchor2)] → G2 → [Y, G1(anchor1)]
	// (already expanded: cycle cut). The delivery must terminate promptly
	// with X and Y one copy each, both anchors nothing.
	done := make(chan bool, 1)
	go func() {
		_, ingErr := f.svc.IngestRaw(context.Background(), f.mdRaw("ag-loop", "loop protection"),
			"sender@example.remote", []string{"g1@" + f.domName}, models.InboundSourceSMTP, "")
		if ingErr != nil {
			t.Errorf("ingest: %v", ingErr)
		}
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cyclic group delivery did not terminate within 10s (loop protection failed)")
	}

	if got := f.agInboxCount(t, mbX); got != 1 {
		t.Fatalf("member X INBOX = %d, want 1", got)
	}
	if got := f.agInboxCount(t, mbY); got != 1 {
		t.Fatalf("member Y INBOX = %d, want 1 (the nested group expanded)", got)
	}
	if got := f.agInboxCount(t, anchor1); got != 0 {
		t.Fatalf("anchor1 INBOX = %d, want 0", got)
	}
	if got := f.agInboxCount(t, anchor2); got != 0 {
		t.Fatalf("anchor2 INBOX = %d, want 0", got)
	}
}

// TestPGAliasGroupIsolation — the tenant boundary: (1) a group member
// mailbox owned by ANOTHER user can never receive (skipped silently; the
// other members still get their copies); (2) another user's alias for the
// same address does not resolve for this record's delivery — the direct path
// applies, provisioning under the record owner, and the foreign mapping is
// untouched.
func TestPGAliasGroupIsolation(t *testing.T) {
	f := newAGFixture(t, true)

	// A second tenant with its own workspace (suffix-differentiated for the
	// shared test database, the fixture's own pattern).
	suffix := fmt.Sprintf("%d-2", time.Now().UnixNano())
	other := &models.User{Name: "ag-" + suffix, Email: "ag-" + suffix + "@test.local", PasswordHash: "x"}
	if err := f.tx.Create(other).Error; err != nil {
		t.Fatalf("create second user: %v", err)
	}
	otherWS := &models.Workspace{Name: "ag-" + suffix, Slug: "ag-" + suffix, OwnerID: other.ID}
	if err := f.tx.Create(otherWS).Error; err != nil {
		t.Fatalf("create second workspace: %v", err)
	}
	otherMB, err := f.msSvc.CreateMailbox(other.ID, &otherWS.ID, "ada@"+f.domName, 0)
	if err != nil {
		t.Fatalf("seed foreign mailbox: %v", err)
	}

	// (1) The foreign mailbox as a group member: skipped, the rest delivered.
	mbA := f.agMailbox(t, "a@"+f.domName)
	anchor, err := f.msSvc.CreateMailbox(f.userID, wsPtrOf(f.wsID), "mixed@"+f.domName, 0)
	if err != nil {
		t.Fatalf("provision anchor: %v", err)
	}
	group := &models.MailGroup{
		UserID:      f.userID,
		WorkspaceID: wsPtrOf(f.wsID),
		MailboxID:   anchor.ID,
		Address:     "mixed@" + f.domName,
	}
	if err := f.groups.Create(group, []uint{mbA.ID, otherMB.ID}); err != nil {
		t.Fatalf("create mixed-ownership group: %v", err)
	}
	f.agIngest(t, "mixed@"+f.domName)
	if got := f.agInboxCount(t, mbA); got != 1 {
		t.Fatalf("own member INBOX = %d, want 1", got)
	}
	if got := f.agInboxCount(t, otherMB); got != 0 {
		t.Fatalf("foreign member INBOX = %d, want 0 (isolation: another user's mailbox never receives)", got)
	}

	// (2) The second user's alias for the SAME address the fixture user
	// delivers to: it must not resolve — the direct path applies (auto-
	// provision under the fixture user), and the foreign alias is untouched.
	otherAlias := &models.MailAlias{
		UserID:      other.ID,
		WorkspaceID: &otherWS.ID,
		MailboxID:   otherMB.ID,
		Address:     "ada@" + f.domName,
	}
	if err := f.aliases.Create(otherAlias); err != nil {
		t.Fatalf("create foreign alias: %v", err)
	}
	f.agIngest(t, "ada@"+f.domName)
	if got := f.agInboxCount(t, otherMB); got != 0 {
		t.Fatalf("foreign alias target INBOX = %d, want 0 (another user's alias must not resolve)", got)
	}
	// The direct path provisioned the record owner's mailbox for the address.
	own, err := f.msSvc.FindMailboxByName(f.userID, "ada@"+f.domName)
	if err != nil {
		t.Fatalf("own mailbox for the direct path not provisioned: %v", err)
	}
	if got := f.agInboxCount(t, own); got != 1 {
		t.Fatalf("own provisioned INBOX = %d, want 1 (the direct path applied)", got)
	}
}
