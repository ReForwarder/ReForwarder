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

package jobs

import (
	"database/sql"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SF-73: tests for the staged account-destruction cron job. The job is the
// third stage of the chain: an admin schedules deletion (handlers/admin_handler.go
// DeleteUser — sets ScheduledDeletionAt, deactivates the account, emits the
// user.deletion_scheduled event), a 7-day grace period passes, and this job
// permanently destroys every account whose date has come.
//
// The suite runs against a fake database/sql driver (the dkim_retire_test.go
// pattern) holding the tables DeleteAllUserData sweeps, so the
// `go test ./internal/cron/jobs/...` gate needs no Postgres. The fake
// implements exactly the statement shapes user_repo.go issues and fails
// closed on anything else.

// acColumns is the uniform column set every table row carries; columns a
// table never uses simply stay nil.
var acColumns = []string{
	"id", "user_id", "workspace_id", "owner_id", "actor_id",
	"campaign_id", "campaign_message_id", "template_id", "version_id",
	"list_id", "webhook_id", "name", "email", "password_hash",
	"scheduled_deletion_at", "created_at",
}

// acUserRowCols is what the fake answers for `SELECT * FROM users` — a subset
// of models.User fields gorm scans by name.
var acUserRowCols = []string{"id", "name", "email", "password_hash", "scheduled_deletion_at"}

type acState struct {
	mu        sync.Mutex
	tables    map[string][]map[string]driver.Value
	seq       int64
	// failDeleteOn injects an error: table -> user_id -> error, fired when a
	// DELETE statement whose WHERE binds that user_id touches that table.
	failDeleteOn map[string]map[int64]error
}

func newACState() *acState {
	return &acState{
		tables:       map[string][]map[string]driver.Value{},
		seq:          1000,
		failDeleteOn: map[string]map[int64]error{},
	}
}

func (s *acState) colVal(row map[string]driver.Value, col string) driver.Value { return row[col] }

func (s *acState) seed(table string, vals map[string]any) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := map[string]driver.Value{}
	for _, c := range acColumns {
		row[c] = nil
	}
	for col, v := range vals {
		row[col] = acVal(v)
	}
	if row["id"] == nil {
		s.seq++
		row["id"] = s.seq
	}
	s.tables[table] = append(s.tables[table], row)
	return row["id"].(int64)
}

func acVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case bool:
		return t
	case string:
		return t
	case time.Time:
		return t
	case *time.Time:
		if t == nil {
			return nil
		}
		return *t
	default:
		panic(fmt.Sprintf("acVal: unsupported type %T", v))
	}
}

func (s *acState) count(table string, col string, eq int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, row := range s.tables[table] {
		if row[col] != nil && row[col].(int64) == eq {
			n++
		}
	}
	return n
}

func (s *acState) rowCount(table string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tables[table])
}

func (s *acState) idsOf(table, col string, eq int64) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, row := range s.tables[table] {
		if row[col] != nil && row[col].(int64) == eq {
			out = append(out, row["id"].(int64))
		}
	}
	return out
}

func acGorm(t *testing.T, state *acState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(acConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open account-cleanup fake gorm: %v", err)
	}
	return gdb
}

// acNormalize lowercases, collapses whitespace, strips identifier quotes and
// table-qualifies column prefixes, and rewrites $n bindvars to ?.
func acNormalize(query string) string {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	lower = strings.ReplaceAll(lower, "\"", "")
	lower = regexp.MustCompile(`\$\d+`).ReplaceAllString(lower, "?")
	lower = regexp.MustCompile(`\b[a-z_]+\.`).ReplaceAllString(lower, "")
	return lower
}

var (
	acDeleteInList  = regexp.MustCompile(`^delete from (\w+) where (\w+) in \((\?(?:,\?)*)\)$`)
	acDeleteInSub   = regexp.MustCompile(`^delete from (\w+) where (\w+) in \((.*select .*)\)$`)
	acSelectParent  = regexp.MustCompile(`^select id from (\w+) where user_id = \?$`)
	acSelectOwner   = regexp.MustCompile(`^select id from (\w+) where owner_id = \?$`)
	acSelectInSub   = regexp.MustCompile(`^select id from (\w+) where (\w+) in \((.*select .*)\)$`)
	acDeleteSimple  = regexp.MustCompile(`^delete from (\w+) where (\w+) = \?$`)
	acUpdateSimple  = regexp.MustCompile(`^update (\w+) set (.+) where (\w+) = \?$`)
	acSelectDue     = regexp.MustCompile(`^select \* from users where scheduled_deletion_at is not null and scheduled_deletion_at <= \?$`)
	acSelectUsersID = regexp.MustCompile(`^select (\w+) from (\w+) where user_id = \?$`)
)

type acConnector struct{ state *acState }

func (c acConnector) Connect(context.Context) (driver.Conn, error) { return &acConn{s: c.state}, nil }
func (c acConnector) Driver() driver.Driver                        { return acDrv{} }

type acDrv struct{}

func (acDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type acConn struct{ s *acState }

func (c *acConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *acConn) Close() error                        { return nil }
func (c *acConn) Begin() (driver.Tx, error)           { return acTx{}, nil }

type acTx struct{}

func (acTx) Commit() error   { return nil }
func (acTx) Rollback() error { return nil }

func (c *acConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q := acNormalize(query)
	if m := acSelectDue.FindStringSubmatch(q); m != nil {
		var cutoff time.Time
		if len(args) == 1 {
			cutoff = args[0].Value.(time.Time)
		}
		return c.selectDueUsers(cutoff)
	}
	if m := acSelectUsersID.FindStringSubmatch(q); m != nil {
		// `SELECT id FROM <t> WHERE user_id = ?` (contact_lists, workspaces
		// subselects) — used by Raw().Scan(&ids) callers.
		ids := c.s.idsOf(m[2], "user_id", int64OfArg(args[0].Value))
		return acRowsFromCols([]string{"id"}, int64Rows(ids)), nil
	}
	if m := acSelectOwner.FindStringSubmatch(q); m != nil {
		ids := c.s.idsOf(m[1], "owner_id", int64OfArg(args[0].Value))
		return acRowsFromCols([]string{"id"}, int64Rows(ids)), nil
	}
	return nil, fmt.Errorf("acState: cannot route query: %s", query)
}

func (c *acConn) selectDueUsers(cutoff time.Time) (driver.Rows, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	var rows [][]driver.Value
	for _, row := range c.s.tables["users"] {
		sd, ok := row["scheduled_deletion_at"].(time.Time)
		if !ok || sd.After(cutoff) {
			continue
		}
		rows = append(rows, []driver.Value{
			row["id"], row["name"], row["email"], row["password_hash"], row["scheduled_deletion_at"],
		})
	}
	return &acRows{cols: acUserRowCols, rows: rows}, nil
}

// ExecContext handles every statement shape DeleteAllUserData issues; anything
// else fails closed, so a schema/query drift in user_repo.go breaks the test
// instead of silently passing.
func (c *acConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	q := acNormalize(query)

	// `DELETE FROM t WHERE col IN (?, ?, …)` — gorm's expansion of `IN ?`
	// with the id slice (contact_list_members, workspace_* sweeps).
	if m := acDeleteInList.FindStringSubmatch(q); m != nil {
		table, col := m[1], m[2]
		ids := make([]int64, 0, len(args))
		for _, a := range args {
			ids = append(ids, int64OfArg(a.Value))
		}
		n := c.s.deleteWhereIn(table, col, ids)
		return acResult{rows: int64(n)}, nil
	}
	if m := acDeleteInSub.FindStringSubmatch(q); m != nil {
		table, childCol, sub := m[1], m[2], m[3]
		ids, err := c.evalSubselect(sub, args)
		if err != nil {
			return nil, err
		}
		if err := c.maybeFail(table, args); err != nil {
			return nil, err
		}
		n := c.s.deleteWhereIn(table, childCol, ids)
		return acResult{rows: int64(n)}, nil
	}
	if m := acDeleteSimple.FindStringSubmatch(q); m != nil {
		table, col := m[1], m[2]
		if err := c.maybeFail(table, args); err != nil {
			return nil, err
		}
		n := c.s.deleteWhereEq(table, col, int64OfArg(args[0].Value))
		return acResult{rows: int64(n)}, nil
	}
	if m := acUpdateSimple.FindStringSubmatch(q); m != nil {
		table, sets, col := m[1], m[2], m[3]
		// only shape issued today: `set active_version_id = null`
		if !strings.Contains(sets, "= null") {
			return nil, fmt.Errorf("acState: unsupported update sets: %s", query)
		}
		c.s.nullifyCol(table, col, int64OfArg(args[0].Value))
		return acResult{}, nil
	}
	return nil, fmt.Errorf("acState: unsupported exec: %s", query)
}

// evalSubselect resolves `select id from P where user_id = ?` and the
// double-nested `select id from P1 where c1 in (select id from P2 where user_id = ?)`.
func (c *acConn) evalSubselect(sub string, args []driver.NamedValue) ([]int64, error) {
	if m := acSelectParent.FindStringSubmatch(sub); m != nil {
		return c.s.idsOf(m[1], "user_id", int64OfArg(args[0].Value)), nil
	}
	if m := acSelectInSub.FindStringSubmatch(sub); m != nil {
		inner, err := c.evalSubselect(m[3], args)
		if err != nil {
			return nil, err
		}
		var out []int64
		for _, id := range inner {
			out = append(out, c.s.idsOf(m[1], m[2], id)...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("acState: cannot route subselect: %s", sub)
}

// maybeFail fires the injected per-user delete error BEFORE any row is
// removed, mirroring the first statement of the aborted transaction.
func (c *acConn) maybeFail(table string, args []driver.NamedValue) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	m, ok := c.s.failDeleteOn[table]
	if !ok || len(args) == 0 {
		return nil
	}
	if err, ok := m[int64OfArg(args[0].Value)]; ok && err != nil {
		return err
	}
	return nil
}

func (s *acState) deleteWhereEq(table, col string, eq int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var kept []map[string]driver.Value
	removed := 0
	for _, row := range s.tables[table] {
		v, ok := row[col].(int64)
		if ok && v == eq {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	s.tables[table] = kept
	return removed
}

func (s *acState) deleteWhereIn(table, col string, ids []int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var kept []map[string]driver.Value
	removed := 0
	for _, row := range s.tables[table] {
		v, ok := row[col].(int64)
		if ok && want[v] {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	s.tables[table] = kept
	return removed
}

func (s *acState) nullifyCol(table, matchCol string, eq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.tables[table] {
		if v, ok := row[matchCol].(int64); ok && v == eq {
			row["active_version_id"] = nil
		}
	}
}

func int64OfArg(v driver.Value) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case uint:
		return int64(t)
	case int:
		return int64(t)
	default:
		panic(fmt.Sprintf("acState: expected integer arg, got %T", v))
	}
}

func int64Rows(ids []int64) [][]driver.Value {
	rows := make([][]driver.Value, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, []driver.Value{id})
	}
	return rows
}

func acRowsFromCols(cols []string, rows [][]driver.Value) driver.Rows {
	return &acRows{cols: cols, rows: rows}
}

type acResult struct{ rows int64 }

func (r acResult) LastInsertId() (int64, error) { return 0, nil }
func (r acResult) RowsAffected() (int64, error) { return r.rows, nil }

type acRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *acRows) Columns() []string { return r.cols }
func (r *acRows) Close() error      { return nil }
func (r *acRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// ---- fixtures ----

// acSeedUser seeds a user row; scheduled == nil means the account is not
// scheduled for deletion (never due), a past date means due now, a future
// date means still inside the grace window. Dates are relative to the test
// clock so the suite does not rot with wall-clock time.
func acSeedUser(s *acState, email string, scheduled *time.Time) uint {
	return uint(s.seed("users", map[string]any{
		"name": "user-" + email, "email": email, "password_hash": "x",
		"scheduled_deletion_at": scheduled,
	}))
}

// acDue is a scheduled_deletion_at one day in the past (due for destruction).
func acDue() *time.Time {
	past := time.Now().Add(-24 * time.Hour)
	return &past
}

// acGrace is a scheduled_deletion_at two days out (grace window).
func acGrace() *time.Time {
	future := time.Now().Add(48 * time.Hour)
	return &future
}

func acRun(t *testing.T, s *acState) {
	t.Helper()
	job := NewAccountCleanupJob(repositories.NewUserRepository(acGorm(t, s)))
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("job run: %v", err)
	}
}

// TestAccountCleanupStagedDeletionOrder pins the stage gate of the
// destruction chain: accounts still inside the grace window (scheduled into
// the future) must be untouched; only accounts whose scheduled_deletion_at
// has passed are destroyed.
func TestAccountCleanupStagedDeletionOrder(t *testing.T) {
	s := newACState()
	due := acSeedUser(s, "due@test.local", acDue())
	notDue := acSeedUser(s, "grace@test.local", acGrace())
	acSeedUser(s, "plain@test.local", nil) // ScheduledDeletionAt = nil → never due

	acRun(t, s)

	if s.rowCount("users") != 2 {
		t.Fatalf("users remaining = %d, want 2 (due user destroyed, others untouched)", s.rowCount("users"))
	}
	if len(s.idsOf("users", "id", int64(due))) != 0 {
		t.Fatal("due user should have been destroyed")
	}
	if len(s.idsOf("users", "id", int64(notDue))) != 1 {
		t.Fatal("not-yet-due user must survive the grace window")
	}
	if s.count("users", "id", int64(due)) != 0 {
		t.Fatal("due user row must be gone")
	}
}

// TestAccountCleanupGranteeCredentialsRevoked covers the grantee cleanup
// half: API keys, sessions and other credentials owned by the destroyed
// account are removed, while another account's credentials stay intact.
func TestAccountCleanupGranteeCredentialsRevoked(t *testing.T) {
	s := newACState()
	victim := acSeedUser(s, "victim@test.local", acDue())
	bystander := acSeedUser(s, "bystander@test.local", acGrace())
	_ = bystander

	for _, cred := range []string{"api_keys", "sessions"} {
		s.seed(cred, map[string]any{"user_id": victim})
		s.seed(cred, map[string]any{"user_id": bystander})
	}

	acRun(t, s)

	for _, cred := range []string{"api_keys", "sessions"} {
		if got := s.count(cred, "user_id", int64(victim)); got != 0 {
			t.Errorf("%s: victim rows = %d, want 0 (credentials revoked)", cred, got)
		}
		if got := s.count(cred, "user_id", int64(bystander)); got != 1 {
			t.Errorf("%s: bystander rows = %d, want 1 (untouched)", cred, got)
		}
	}
}

// TestAccountCleanupGDPRDataSweep covers the full-data sweep: every
// user-owned chain (campaigns → messages → tracking, subscriber lists,
// templates → versions → localizations, contact lists, webhooks →
// deliveries, owned workspaces + memberships, actor events) must be removed
// with the account.
func TestAccountCleanupGDPRDataSweep(t *testing.T) {
	s := newACState()
	victim := acSeedUser(s, "gdpr@test.local", acDue())
	bystander := acSeedUser(s, "keep@test.local", acGrace())

	campaign := s.seed("campaigns", map[string]any{"user_id": victim})
	otherCampaign := s.seed("campaigns", map[string]any{"user_id": bystander})
	campaignMessage := s.seed("campaign_messages", map[string]any{"campaign_id": campaign, "user_id": victim})
	s.seed("tracking_events", map[string]any{"campaign_message_id": campaignMessage})
	s.seed("tracked_links", map[string]any{"campaign_id": campaign})
	s.seed("tracking_events", map[string]any{"campaign_message_id": int64(777)}) // other campaign's chain
	_ = otherCampaign

	list := s.seed("subscriber_lists", map[string]any{"user_id": victim})
	s.seed("subscriber_list_members", map[string]any{"list_id": list})
	s.seed("subscriber_list_unsubscribes", map[string]any{"list_id": list})

	tpl := s.seed("templates", map[string]any{"user_id": victim})
	version := s.seed("template_versions", map[string]any{"template_id": tpl})
	s.seed("template_localizations", map[string]any{"version_id": version})

	clist := s.seed("contact_lists", map[string]any{"user_id": victim})
	s.seed("contact_list_members", map[string]any{"list_id": clist})

	webhook := s.seed("webhooks", map[string]any{"user_id": victim})
	s.seed("webhook_deliveries", map[string]any{"webhook_id": webhook, "user_id": victim})

	ws := s.seed("workspaces", map[string]any{"owner_id": victim, "user_id": victim})
	s.seed("workspace_members", map[string]any{"user_id": victim})
	s.seed("workspace_invitations", map[string]any{"workspace_id": ws})
	s.seed("workspace_members", map[string]any{"workspace_id": ws, "user_id": int64(42)}) // other member of victim's ws

	s.seed("events", map[string]any{"actor_id": victim})
	s.seed("emails", map[string]any{"user_id": victim})
	s.seed("inbound_emails", map[string]any{"user_id": victim})
	s.seed("contacts", map[string]any{"user_id": victim})
	s.seed("suppressions", map[string]any{"user_id": victim})
	s.seed("domains", map[string]any{"user_id": victim})

	// Bystander gets one row in every swept table so the sweep's user scoping
	// (not table-nuking) is what the assertions observe.
	s.seed("subscriber_lists", map[string]any{"user_id": bystander})
	s.seed("templates", map[string]any{"user_id": bystander})
	s.seed("contact_lists", map[string]any{"user_id": bystander})
	s.seed("webhooks", map[string]any{"user_id": bystander})
	s.seed("events", map[string]any{"actor_id": bystander})
	s.seed("emails", map[string]any{"user_id": bystander})
	s.seed("inbound_emails", map[string]any{"user_id": bystander})
	s.seed("contacts", map[string]any{"user_id": bystander})
	s.seed("suppressions", map[string]any{"user_id": bystander})
	s.seed("domains", map[string]any{"user_id": bystander})

	acRun(t, s)

	// Chained rows: gone for the victim, preserved for the bystander's chain.
	for _, tc := range []struct {
		table string
		col   string
		id    int64
		want  int
		desc  string
	}{
		{"tracking_events", "campaign_message_id", campaignMessage, 0, "tracking events on victim's messages"},
		{"tracked_links", "campaign_id", int64(campaign), 0, "tracked links on victim's campaign"},
		{"subscriber_list_members", "list_id", int64(list), 0, "list members on victim's list"},
		{"subscriber_list_unsubscribes", "list_id", int64(list), 0, "unsubscribes on victim's list"},
		{"template_versions", "template_id", int64(tpl), 0, "versions of victim's template"},
		{"template_localizations", "version_id", int64(version), 0, "localizations of victim's version"},
		{"contact_list_members", "list_id", int64(clist), 0, "contact members of victim's list"},
		{"webhook_deliveries", "webhook_id", int64(webhook), 0, "deliveries of victim's webhook"},
		{"tracking_events", "campaign_message_id", 777, 1, "other chain's tracking events preserved"},
	} {
		if got := s.count(tc.table, tc.col, tc.id); got != tc.want {
			t.Errorf("%s (%s): rows = %d, want %d", tc.table, tc.desc, got, tc.want)
		}
	}

	for _, table := range []string{"campaigns", "subscriber_lists", "templates", "contact_lists", "webhooks", "emails", "inbound_emails", "contacts", "suppressions", "domains"} {
		if got := s.count(table, "user_id", int64(victim)); got != 0 {
			t.Errorf("%s: victim rows = %d, want 0", table, got)
		}
		if got := s.count(table, "user_id", int64(bystander)); got != 1 {
			t.Errorf("%s: bystander rows = %d, want 1", table, got)
		}
	}

	if got := s.count("events", "actor_id", int64(victim)); got != 0 {
		t.Errorf("events: victim actor rows = %d, want 0 (GDPR: actor history removed)", got)
	}
	if got := s.count("events", "actor_id", int64(bystander)); got != 1 {
		t.Errorf("events: bystander actor rows = %d, want 1", got)
	}

	// Owned workspace + its memberships/invitations.
	if got := s.count("workspaces", "owner_id", int64(victim)); got != 0 {
		t.Errorf("workspaces: owned by victim = %d, want 0", got)
	}
	if got := s.count("workspace_members", "workspace_id", int64(ws)); got != 0 {
		t.Errorf("workspace_members: victim's workspace = %d, want 0", got)
	}
	if got := s.count("workspace_invitations", "workspace_id", int64(ws)); got != 0 {
		t.Errorf("workspace_invitations: victim's workspace = %d, want 0", got)
	}
}

// TestAccountCleanupFailureIsolation covers the per-user continue: a user
// whose sweep fails mid-transaction stays intact (the real backend rolls the
// transaction back; the fake injects the error on the sweep's first
// statement so no partial state is possible), and the remaining due users
// are still destroyed.
func TestAccountCleanupFailureIsolation(t *testing.T) {
	s := newACState()
	ok := acSeedUser(s, "ok1@test.local", acDue())
	failing := acSeedUser(s, "failing@test.local", acDue())
	ok2 := acSeedUser(s, "ok2@test.local", acDue())

	// Inject before any row of that transaction is touched.
	s.failDeleteOn["tracking_events"] = map[int64]error{
		int64(failing): errors.New("boom"),
	}

	acRun(t, s)

	if got := s.count("users", "id", int64(ok)); got != 0 {
		t.Error("first due user should be destroyed")
	}
	if got := s.count("users", "id", int64(ok2)); got != 0 {
		t.Error("user after the failing one should still be destroyed")
	}
	if got := s.count("users", "id", int64(failing)); got != 1 {
		t.Error("failed user must survive (transaction aborted, retry next tick)")
	}
	if s.rowCount("campaigns") != 1 { // failing user's campaigns intact
		// no campaigns seeded here, just sanity
		t.Logf("campaigns rows: %d", s.rowCount("campaigns"))
	}
}
