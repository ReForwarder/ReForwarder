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

package repositories

import (
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// The RF-F075 membership queries use SQL the in-process fake driver cannot
// execute (joins, batched filters, FK enforcement), so they are pinned
// against real Postgres — skipped when TEST_DATABASE_DSN is unreachable,
// like the other repo tests.

func doiTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&models.Subscriber{}, &models.SubscriberList{}, &models.SubscriberListMember{}); err != nil {
		t.Skipf("skipping: cannot migrate DOI schema: %v", err)
	}
	return db
}

func doiSeedUser(t *testing.T, tx *gorm.DB, email string) uint {
	t.Helper()
	u := &models.User{Name: "doi", Email: email, PasswordHash: "x"}
	if err := tx.Create(u).Error; err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return u.ID
}

func doiSeedContact(t *testing.T, tx *gorm.DB, uid uint, email string) uint {
	t.Helper()
	sub := &models.Subscriber{UserID: uid, Email: email, Status: models.SubscriberStatusSubscribed, SubscribedAt: &time.Time{}}
	*sub.SubscribedAt = time.Now()
	if err := tx.Create(sub).Error; err != nil {
		t.Fatalf("seed subscriber %s: %v", email, err)
	}
	return sub.ID
}

func doiSeedList(t *testing.T, tx *gorm.DB, uid uint) uint {
	t.Helper()
	list := &models.SubscriberList{UserID: uid, Name: "DOI repo test", Type: models.SubscriberListTypeStatic}
	if err := tx.Create(list).Error; err != nil {
		t.Fatalf("seed list: %v", err)
	}
	return list.ID
}

func TestListConfirmedMembersExcludesPending(t *testing.T) {
	db := doiTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	uid := doiSeedUser(t, tx, "doi-owner@test.local")
	repo := NewSubscriberListRepository(tx)

	listID := doiSeedList(t, tx, uid)
	confirmedID := doiSeedContact(t, tx, uid, "confirmed@test.local")
	pendingID := doiSeedContact(t, tx, uid, "pending@test.local")
	for _, m := range []models.SubscriberListMember{
		{ListID: listID, SubscriberID: confirmedID, Status: models.MemberStatusConfirmed},
		{ListID: listID, SubscriberID: pendingID, Status: models.MemberStatusPending},
	} {
		if err := tx.Create(&m).Error; err != nil {
			t.Fatalf("seed member: %v", err)
		}
	}

	members, total, err := repo.ListConfirmedMembers(listID, -1, 0)
	if err != nil {
		t.Fatalf("ListConfirmedMembers: %v", err)
	}
	if total != 1 || len(members) != 1 {
		t.Fatalf("confirmed audience = %d rows (total %d), want 1/1", len(members), total)
	}
	if members[0].ID != confirmedID {
		t.Fatalf("audience member = %d, want the confirmed subscriber %d (pending %d excluded)", members[0].ID, confirmedID, pendingID)
	}
}

func TestConfirmMemberTransitionsAndIdempotence(t *testing.T) {
	db := doiTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	uid := doiSeedUser(t, tx, "doi-owner@test.local")
	repo := NewSubscriberListRepository(tx)

	listID := doiSeedList(t, tx, uid)
	pendingID := doiSeedContact(t, tx, uid, "confirmable@test.local")
	legacyID := doiSeedContact(t, tx, uid, "legacy@test.local")
	for _, m := range []models.SubscriberListMember{
		{ListID: listID, SubscriberID: pendingID, Status: models.MemberStatusPending},
		{ListID: listID, SubscriberID: legacyID}, // no explicit status: the column default
	} {
		if err := tx.Create(&m).Error; err != nil {
			t.Fatalf("seed member: %v", err)
		}
	}

	// A pre-DOI-era row (no status set at insert) reads back confirmed — the
	// column default must keep every pre-DOI membership confirmed.
	status, ok := repo.MemberStatus(listID, legacyID)
	if !ok || status != models.MemberStatusConfirmed {
		t.Fatalf("default membership status = %q (ok %v), want confirmed", status, ok)
	}

	updated, err := repo.ConfirmMember(listID, pendingID)
	if err != nil || !updated {
		t.Fatalf("first confirm = (%v,%v), want (true,nil)", updated, err)
	}
	status, ok = repo.MemberStatus(listID, pendingID)
	if !ok || status != models.MemberStatusConfirmed {
		t.Fatalf("post-confirm status = %q (ok %v), want confirmed", status, ok)
	}
	member, err := repo.FindMember(listID, pendingID)
	if err != nil || member.ConfirmedAt == nil {
		t.Fatalf("confirmed_at = %v (err %v), want stamped (consent evidence)", member.ConfirmedAt, err)
	}

	// The second confirm is a no-op — the hosted page renders the
	// already-confirmed notice instead of re-firing the trigger.
	updated, err = repo.ConfirmMember(listID, pendingID)
	if err != nil || updated {
		t.Fatalf("repeat confirm = (%v,%v), want (false,nil)", updated, err)
	}
}

func TestListMembersWithStatusReturnsMembershipStatus(t *testing.T) {
	db := doiTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	uid := doiSeedUser(t, tx, "doi-owner@test.local")
	repo := NewSubscriberListRepository(tx)

	listID := doiSeedList(t, tx, uid)
	confirmedID := doiSeedContact(t, tx, uid, "view-confirmed@test.local")
	pendingID := doiSeedContact(t, tx, uid, "view-pending@test.local")
	for _, m := range []models.SubscriberListMember{
		{ListID: listID, SubscriberID: confirmedID, Status: models.MemberStatusConfirmed},
		{ListID: listID, SubscriberID: pendingID, Status: models.MemberStatusPending},
	} {
		if err := tx.Create(&m).Error; err != nil {
			t.Fatalf("seed member: %v", err)
		}
	}

	members, total, err := repo.ListMembersWithStatus(listID, -1, 0)
	if err != nil {
		t.Fatalf("ListMembersWithStatus: %v", err)
	}
	if total != 2 || len(members) != 2 {
		t.Fatalf("member view = %d rows (total %d), want 2/2 (the dashboard sees pending rows)", len(members), total)
	}
	byID := map[uint]models.SubscriberListMemberStatus{}
	for _, m := range members {
		byID[m.ID] = m.MemberStatus
	}
	if byID[confirmedID] != models.MemberStatusConfirmed || byID[pendingID] != models.MemberStatusPending {
		t.Fatalf("member statuses = %v, want confirmed+pending mapped to their subscribers", byID)
	}
}

func TestAddMemberPersistsExplicitPending(t *testing.T) {
	db := doiTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	uid := doiSeedUser(t, tx, "doi-owner@test.local")
	repo := NewSubscriberListRepository(tx)

	listID := doiSeedList(t, tx, uid)
	pendingID := doiSeedContact(t, tx, uid, "explicit-pending@test.local")

	// An explicit pending insert must stick — gorm's default tag applies only
	// to the zero value, never overriding a set status (RF-F075).
	if err := repo.AddMember(&models.SubscriberListMember{ListID: listID, SubscriberID: pendingID, Status: models.MemberStatusPending}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if status, _ := repo.MemberStatus(listID, pendingID); status != models.MemberStatusPending {
		t.Fatalf("explicit pending insert read back %q", status)
	}

	// A re-add of the same pair is the ON CONFLICT no-op, not a second row.
	var count int64
	tx.Model(&models.SubscriberListMember{}).Where("list_id = ? AND subscriber_id = ?", listID, pendingID).Count(&count)
	if count != 1 {
		t.Fatalf("membership rows = %d, want 1", count)
	}
}
