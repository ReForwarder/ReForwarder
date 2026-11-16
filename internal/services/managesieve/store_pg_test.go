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

package managesieve

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-86 integration tests: the FilterScriptStore adapter over the REAL
// FilterScriptRepository against REAL Postgres (TEST_DATABASE_DSN gate, the
// repositories/inbound suites' pattern): the activation semantics rest on
// real transactions (SetActiveForMailbox's disable-all + enable-one pair) and
// the full production schema. Each test works in one transaction that is
// rolled back.

const msStoreTestDSNEnv = "TEST_DATABASE_DSN"

var (
	msStoreMigrateOnce sync.Once
	msStoreMigrateErr  error
)

// msStoreDB opens the gated test database and runs the full additive
// migration once, so these tests exercise the real schema.
func msStoreDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(msStoreTestDSNEnv)
	if dsn == "" {
		t.Skipf("skipping: %s not set", msStoreTestDSNEnv)
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("skipping: no test database available (%s): %v", dsn, err)
	}
	msStoreMigrateOnce.Do(func() { msStoreMigrateErr = migration.Run(db) })
	if msStoreMigrateErr != nil {
		t.Fatalf("migrate: %v", msStoreMigrateErr)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// txMailboxFinder is the test's MailboxFinder: the same ListMailboxes read
// mailstore.Service serves, narrowed to the seam the adapter consumes.
type txMailboxFinder struct {
	tx *gorm.DB
}

func (f txMailboxFinder) ListMailboxes(userID, workspaceID uint) ([]models.Mailbox, error) {
	var out []models.Mailbox
	if err := f.tx.Where("user_id = ?", userID).Order("id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// msStoreFixture seeds one user with two mailboxes (the lowest-ID ACTIVE one
// is the primary the adapter must resolve).
type msStoreFixture struct {
	tx        *gorm.DB
	userID    uint
	primaryID uint
	otherID   uint
	store     *FilterScriptStore
	suspended uint
}

func newMSStoreFixture(t *testing.T) *msStoreFixture {
	t.Helper()
	tx := msStoreDB(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	user := &models.User{Name: "msv-" + suffix, Email: "msv-" + suffix + "@test.local", PasswordHash: "x"}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	primary := &models.Mailbox{UserID: user.ID, Name: "ada@" + suffix + ".test", Status: models.MailboxStatusActive}
	if err := tx.Create(primary).Error; err != nil {
		t.Fatalf("create primary mailbox: %v", err)
	}
	other := &models.Mailbox{UserID: user.ID, Name: "other@" + suffix + ".test", Status: models.MailboxStatusActive}
	if err := tx.Create(other).Error; err != nil {
		t.Fatalf("create other mailbox: %v", err)
	}
	suspended := &models.Mailbox{UserID: user.ID, Name: "held@" + suffix + ".test", Status: models.MailboxStatusSuspended}
	if err := tx.Create(suspended).Error; err != nil {
		t.Fatalf("create suspended mailbox: %v", err)
	}
	repo := repositories.NewFilterScriptRepository(tx)
	store, err := NewFilterScriptStore(repo, txMailboxFinder{tx: tx})
	if err != nil {
		t.Fatalf("NewFilterScriptStore: %v", err)
	}
	return &msStoreFixture{
		tx: tx, userID: user.ID,
		primaryID: primary.ID, otherID: other.ID, suspended: suspended.ID,
		store: store,
	}
}

// msDoc is a valid document within the bounded grammar.
func msDoc(folder string) string {
	return `{"conditions":[{"field":"subject","operator":"contains","value":"x"}],` +
		`"actions":[{"type":"fileinto","target":"` + folder + `"}]}`
}

// TestPGStorePrimaryMailboxResolution — PrimaryMailboxID resolves the
// lowest-ID ACTIVE mailbox (the suspended mailbox never wins).
func TestPGStorePrimaryMailboxResolution(t *testing.T) {
	f := newMSStoreFixture(t)
	got, err := f.store.PrimaryMailboxID(f.userID)
	if err != nil {
		t.Fatalf("PrimaryMailboxID: %v", err)
	}
	if got != f.primaryID {
		t.Fatalf("PrimaryMailboxID = %d, want the lowest-ID active mailbox %d", got, f.primaryID)
	}
}

// TestPGStorePutListGetRoundTrip — PutScript stores INACTIVE on the primary
// mailbox, GetScript returns the document with its active flag, ListScripts
// returns the entries in evaluation order, and a foreign mailbox ID is
// refused.
func TestPGStorePutListGetRoundTrip(t *testing.T) {
	f := newMSStoreFixture(t)

	// A foreign mailbox is refused before any write.
	if err := f.store.PutScript(f.userID, f.otherID, "stray", msDoc("INBOX/X")); err == nil {
		t.Fatalf("PutScript on a non-primary mailbox: want refusal")
	}

	if err := f.store.PutScript(f.userID, f.primaryID, "first", msDoc("INBOX/A")); err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	body, active, err := f.store.GetScript(f.userID, "first")
	if err != nil {
		t.Fatalf("GetScript: %v", err)
	}
	if active {
		t.Fatalf("a fresh put must be INACTIVE")
	}
	if body != msDoc("INBOX/A") {
		t.Fatalf("document round-trip = %q", body)
	}

	if err := f.store.PutScript(f.userID, f.primaryID, "second", msDoc("INBOX/B")); err != nil {
		t.Fatalf("PutScript second: %v", err)
	}
	entries, err := f.store.ListScripts(f.userID)
	if err != nil {
		t.Fatalf("ListScripts: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListScripts = %d entries, want 2", len(entries))
	}
	if entries[0].Name != "first" || entries[1].Name != "second" {
		t.Fatalf("ListScripts order = %q, %q", entries[0].Name, entries[1].Name)
	}
	if entries[0].Active || entries[1].Active {
		t.Fatalf("ListScripts activation = %v, %v; want both inactive", entries[0].Active, entries[1].Active)
	}

	// A missing name reads as missing.
	if _, _, err := f.store.GetScript(f.userID, "missing"); err == nil {
		t.Fatalf("GetScript on a missing name: want an error")
	}
}

// TestPGStoreActivationSemantics — SETACTIVE enables exactly the named row
// and disables the others (single active per mailbox); the empty name
// deactivates all; deleting the active script is refused at the server and
// the adapter's delete path refuses... the DELETE ITSELF is the server's
// guard; here the adapter delete removes a named row.
func TestPGStoreActivationSemantics(t *testing.T) {
	f := newMSStoreFixture(t)

	if err := f.store.PutScript(f.userID, f.primaryID, "a", msDoc("INBOX/A")); err != nil {
		t.Fatalf("PutScript a: %v", err)
	}
	if err := f.store.PutScript(f.userID, f.primaryID, "b", msDoc("INBOX/B")); err != nil {
		t.Fatalf("PutScript b: %v", err)
	}
	if err := f.store.PutScript(f.userID, f.primaryID, "c", msDoc("INBOX/C")); err != nil {
		t.Fatalf("PutScript c: %v", err)
	}

	// Activate b.
	if err := f.store.SetActive(f.userID, "b"); err != nil {
		t.Fatalf("SetActive b: %v", err)
	}
	for name, want := range map[string]bool{"a": false, "b": true, "c": false} {
		_, active, err := f.store.GetScript(f.userID, name)
		if err != nil {
			t.Fatalf("GetScript %s: %v", name, err)
		}
		if active != want {
			t.Fatalf("script %s active = %v, want %v", name, active, want)
		}
	}

	// Switch the active script to c: b deactivates (one active per mailbox).
	if err := f.store.SetActive(f.userID, "c"); err != nil {
		t.Fatalf("SetActive c: %v", err)
	}
	_, bActive, _ := f.store.GetScript(f.userID, "b")
	_, cActive, _ := f.store.GetScript(f.userID, "c")
	if bActive || !cActive {
		t.Fatalf("after SETACTIVE c: b active=%v c active=%v; want b=false c=true", bActive, cActive)
	}

	// Activate a missing name: refused.
	if err := f.store.SetActive(f.userID, "missing"); err == nil {
		t.Fatalf("SetActive on a missing name: want an error")
	}

	// The empty name deactivates everything.
	if err := f.store.SetActive(f.userID, ""); err != nil {
		t.Fatalf("SetActive \"\": %v", err)
	}
	entries, err := f.store.ListScripts(f.userID)
	if err != nil {
		t.Fatalf("ListScripts: %v", err)
	}
	for _, e := range entries {
		if e.Active {
			t.Fatalf("script %s still active after SETACTIVE \"\"", e.Name)
		}
	}
}

// TestPGStoreUpsertKeepsActivation — re-putting the ACTIVE script's name
// replaces the document and KEEPS the enabled flag (PUTSCRIPT never changes
// activation; RFC 5804).
func TestPGStoreUpsertKeepsActivation(t *testing.T) {
	f := newMSStoreFixture(t)

	if err := f.store.PutScript(f.userID, f.primaryID, "main", msDoc("INBOX/M")); err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	if err := f.store.SetActive(f.userID, "main"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := f.store.PutScript(f.userID, f.primaryID, "main", msDoc("INBOX/M2")); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	body, active, err := f.store.GetScript(f.userID, "main")
	if err != nil {
		t.Fatalf("GetScript: %v", err)
	}
	if body != msDoc("INBOX/M2") {
		t.Fatalf("re-put body = %q", body)
	}
	if !active {
		t.Fatalf("re-put must keep the active flag")
	}

	// And exactly one row exists for the name (upsert, not duplicate).
	entries, err := f.store.ListScripts(f.userID)
	if err != nil {
		t.Fatalf("ListScripts: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Name == "main" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("upsert produced %d rows named main, want 1", n)
	}
}

// TestPGStoreDeleteAndIsolation — DeleteScript removes the named row; a
// script bound to ANOTHER USER's mailbox is invisible (the scoping posture).
func TestPGStoreDeleteAndIsolation(t *testing.T) {
	f := newMSStoreFixture(t)

	if err := f.store.PutScript(f.userID, f.primaryID, "gone", msDoc("INBOX/G")); err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	if err := f.store.DeleteScript(f.userID, "gone"); err != nil {
		t.Fatalf("DeleteScript: %v", err)
	}
	if _, _, err := f.store.GetScript(f.userID, "gone"); err == nil {
		t.Fatalf("deleted script still readable")
	}

	// Isolation: a second user sees nothing of the first user's scripts.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()+1)
	other := &models.User{Name: "msv-o-" + suffix, Email: "msv-o-" + suffix + "@test.local", PasswordHash: "x"}
	if err := f.tx.Create(other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	box := &models.Mailbox{UserID: other.ID, Name: "o@" + suffix + ".test", Status: models.MailboxStatusActive}
	if err := f.tx.Create(box).Error; err != nil {
		t.Fatalf("create other mailbox: %v", err)
	}
	entries, err := f.store.ListScripts(other.ID)
	if err != nil {
		t.Fatalf("ListScripts other: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("foreign user sees %d scripts", len(entries))
	}
}
