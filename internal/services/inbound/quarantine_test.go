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
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// OB-2 quarantine tests (ADR-002 decision 3): the store seam's persistence,
// release re-routing (deliver / forward / re-hold / fail-closed), permanent
// audited deletion, and workspace isolation. Everything runs against the
// fake database/sql driver — a variant of the route_action_test.go fake with
// inbound_quarantine support, defined here so the two files stay independent.

// ---------- fake driver (quarantine variant of the ra* fake) ----------

type quConnector struct{ state *raState }

func (c quConnector) Connect(context.Context) (driver.Conn, error) { return &quConn{s: c.state}, nil }
func (c quConnector) Driver() driver.Driver                        { return quDrv{} }

type quDrv struct{}

func (quDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type quConn struct{ s *raState }

func (c *quConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *quConn) Close() error                        { return nil }
func (c *quConn) Begin() (driver.Tx, error)           { return quTx{}, nil }

type quTx struct{}

func (quTx) Commit() error   { return nil }
func (quTx) Rollback() error { return nil }

func (c *quConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	lower := quNormalize(query)
	switch {
	case strings.Contains(lower, `update "inbound_emails"`),
		strings.Contains(lower, `update "inbound_routes"`),
		strings.Contains(lower, `update "inbound_forward_tasks"`),
		strings.Contains(lower, `update "inbound_quarantine"`):
		return c.execUpdate(lower, args)
	case strings.Contains(lower, "delete from"):
		return c.execDelete(lower, args)
	default:
		if strings.Contains(lower, "insert into") {
			rows, err := c.QueryContext(ctx, query, args)
			if err != nil {
				return nil, err
			}
			var n int64
			for {
				if err := rows.Next([]driver.Value{make([]driver.Value, len(rows.Columns()))}); err != nil {
					break
				}
				n++
			}
			_ = rows.Close()
			return quResult{affected: n}, nil
		}
		return nil, fmt.Errorf("quState: unsupported exec: %s", query)
	}
}

func (c *quConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := quNormalize(query)
	switch {
	case strings.Contains(lower, "insert into"):
		return c.insert(query, args)
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "domains"`):
		return c.selectWhere(lower, args, "domains")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_routes"`):
		if c.s.failRoutes {
			return nil, errors.New("route store unavailable")
		}
		return c.selectWhere(lower, args, "inbound_routes")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_emails"`):
		return c.selectWhere(lower, args, "inbound_emails")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_forward_tasks"`):
		return c.selectWhere(lower, args, "inbound_forward_tasks")
	case strings.Contains(lower, "select") && strings.Contains(lower, `from "inbound_quarantine"`):
		return c.selectWhere(lower, args, "inbound_quarantine")
	default:
		return nil, fmt.Errorf("quState: cannot route query: %s", query)
	}
}

func quNormalize(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

func (c *quConn) insert(query string, args []driver.NamedValue) (driver.Rows, error) {
	table, cols, tuples, err := raParseInsert(query, args)
	if err != nil {
		return nil, err
	}
	ret := raReturningOf(query)
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	conflict := strings.Contains(quNormalize(query), "on conflict")
	var out [][]driver.Value
	for _, vals := range tuples {
		row := map[string]driver.Value{}
		for j, col := range cols {
			row[col] = raCanonical(vals[j])
		}
		if conflict {
			dup := false
			for _, existing := range s.table[table] {
				if fmt.Sprint(existing["inbound_email_id"]) == fmt.Sprint(row["inbound_email_id"]) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
		}
		s.seq++
		row["id"] = s.seq
		for _, col := range ret {
			if _, ok := row[col]; ok {
				continue
			}
			switch col {
			case "id":
				row[col] = s.seq
			case "uuid":
				row[col] = fmt.Sprintf("inb-fake-%d", s.seq)
			default:
				row[col] = raReturningDefaults[col]
			}
		}
		s.table[table] = append(s.table[table], row)
		out = append(out, raProject(row, ret))
	}
	return &quRows{cols: ret, rows: out}, nil
}

// selectWhere mirrors the ra fake: a WHERE of `col = $n` / `lower(col) =
// lower($n)` / `col <= $n` conjunctions, optional ORDER BY (honored for the
// repo's paging order), LIMIT ignored except where callers page on unique
// matches.
func (c *quConn) selectWhere(lower string, args []driver.NamedValue, table string) (driver.Rows, error) {
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = lower[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}

	s := c.s
	s.mu.Lock()
	matched := make([]map[string]driver.Value, 0, len(s.table[table]))
	for _, row := range s.table[table] {
		ok := true
		if strings.TrimSpace(where) != "" {
			for _, term := range strings.Split(where, " and ") {
				if !raMatchTerm(row, strings.TrimSpace(term), args) {
					ok = false
					break
				}
			}
		}
		if ok {
			matched = append(matched, row)
		}
	}
	orderBy := ""
	if i := strings.Index(lower, " order by "); i >= 0 {
		rest := lower[i+len(" order by "):]
		if j := strings.Index(rest, " limit "); j >= 0 {
			rest = rest[:j]
		}
		orderBy = rest
	}
	s.mu.Unlock()

	if strings.Contains(lower, "count(*)") {
		return &quRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
	}

	if orderBy != "" {
		keys := strings.Split(orderBy, ",")
		sort.SliceStable(matched, func(a, b int) bool {
			for _, k := range keys {
				f := strings.Fields(strings.TrimSpace(k))
				if len(f) == 0 {
					continue
				}
				col := strings.Trim(f[0], `"`)
				if i := strings.LastIndex(col, "."); i >= 0 {
					col = col[i+1:]
				}
				desc := len(f) > 1 && strings.HasPrefix(f[1], "desc")
				cmp := raCompareVal(matched[a][col], matched[b][col])
				if cmp == 0 {
					continue
				}
				if desc {
					return cmp > 0
				}
				return cmp < 0
			}
			return false
		})
	}

	cols := make([]string, 0)
	rows := make([][]driver.Value, 0, len(matched))
	if len(matched) > 0 {
		for col := range matched[0] {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, row := range matched {
			rows = append(rows, raProject(row, cols))
		}
	}
	return &quRows{cols: cols, rows: rows}, nil
}

func (c *quConn) execUpdate(lower string, args []driver.NamedValue) (driver.Result, error) {
	table := "inbound_emails"
	for _, t := range []string{"inbound_routes", "inbound_forward_tasks", "inbound_quarantine", "inbound_emails"} {
		if strings.Contains(lower, `update "`+t+`"`) {
			table = t
		}
	}
	si := strings.Index(lower, " set ")
	wi := strings.Index(lower, " where ")
	if si < 0 || wi < 0 {
		return nil, fmt.Errorf("quState: unsupported update shape: %s", lower)
	}
	setPart := lower[si+len(" set ") : wi]
	wherePart := lower[wi+len(" where "):]

	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	affected := int64(0)
	for _, row := range s.table[table] {
		ok := true
		for _, term := range strings.Split(wherePart, " and ") {
			if !raMatchTerm(row, strings.TrimSpace(term), args) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, pair := range strings.Split(setPart, ",") {
			kv := strings.Split(pair, "=")
			if len(kv) != 2 {
				continue
			}
			col := strings.Trim(strings.TrimSpace(kv[0]), `"`)
			ord, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(kv[1]), "$"))
			if err != nil {
				return nil, err
			}
			for _, a := range args {
				if a.Ordinal == ord {
					row[col] = raCanonical(a.Value)
				}
			}
		}
		affected++
	}
	return quResult{affected: affected}, nil
}

func (c *quConn) execDelete(lower string, args []driver.NamedValue) (driver.Result, error) {
	table := ""
	for _, t := range []string{"inbound_routes", "inbound_forward_tasks", "inbound_quarantine", "inbound_emails"} {
		if strings.Contains(lower, `delete from "`+t+`"`) {
			table = t
		}
	}
	if table == "" {
		return nil, fmt.Errorf("quState: unsupported delete: %s", lower)
	}
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()

	wherePart := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		wherePart = lower[i+len(" where "):]
	}
	kept := make([]map[string]driver.Value, 0, len(s.table[table]))
	affected := int64(0)
	for _, row := range s.table[table] {
		ok := true
		if strings.TrimSpace(wherePart) != "" {
			for _, term := range strings.Split(wherePart, " and ") {
				if !raMatchTerm(row, strings.TrimSpace(term), args) {
					ok = false
					break
				}
			}
		}
		if ok {
			affected++
			continue
		}
		kept = append(kept, row)
	}
	s.table[table] = kept
	return quResult{affected: affected}, nil
}

type quResult struct{ affected int64 }

func (r quResult) LastInsertId() (int64, error) { return 0, nil }
func (r quResult) RowsAffected() (int64, error) { return r.affected, nil }

type quRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *quRows) Columns() []string { return r.cols }
func (r *quRows) Close() error      { return nil }
func (r *quRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func quGorm(t *testing.T, state *raState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(quConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open quarantine fake gorm: %v", err)
	}
	return gdb
}

// ---------- helpers over the shared ra state ----------

// quSetRouteEnabled flips a seeded route's enabled flag (release tests change
// routing between ingest and release).
func (s *raState) quSetRouteEnabled(id uint, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.table["inbound_routes"] {
		if row["id"] == int64(id) {
			row["enabled"] = enabled
		}
	}
}

// quSetDomainVerified flips the domain's verification (the release
// fail-closed test).
func (s *raState) quSetDomainVerified(verified bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.table["domains"] {
		row["ownership_verified"] = verified
	}
}

// quBlobStore records blob deletions so the permanent-delete test can prove
// content is destroyed with the row.
type quBlobStore struct {
	mu      sync.Mutex
	deletes []string
}

func (b *quBlobStore) Put(_ context.Context, _ string, _ io.Reader, _ string) error { return nil }
func (b *quBlobStore) Get(_ context.Context, _ string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (b *quBlobStore) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deletes = append(b.deletes, key)
	return nil
}

func (b *quBlobStore) deletedKeys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.deletes))
	copy(out, b.deletes)
	return out
}

// ---------- harness ----------

var quScope = repositories.ResourceScope{UserID: 7, WorkspaceID: raWs(3)}

type quFixture struct {
	state    *raState
	svc      *Service
	qsvc     *QuarantineService
	inbounds *repositories.InboundEmailRepository
	gdb      *gorm.DB
	eq       *raEnqueuer
	webhooks *raWebhooks
	blobs    *quBlobStore
	events   <-chan *models.Event
}

func quNewFixture(t *testing.T, routes ...models.InboundRoute) *quFixture {
	t.Helper()
	state := newRaState()
	state.addDomain(1, 7, raWs(3), "routed.test", true)
	for _, r := range routes {
		state.addRoute(r.ID, 1, 7, raWs(3), r.Pattern, r.Action, r.Priority, r.Enabled)
	}

	gdb := quGorm(t, state)
	inboundRepo := repositories.NewInboundEmailRepository(gdb)
	domainRepo := repositories.NewDomainRepository(gdb)
	svc := NewService(inboundRepo, domainRepo, nil, Config{})
	svc.SetRouter(NewRouter(repositories.NewInboundRouteRepository(gdb)))
	f := &quFixture{
		state: state, gdb: gdb, eq: &raEnqueuer{}, webhooks: &raWebhooks{}, blobs: &quBlobStore{},
		inbounds: inboundRepo,
	}
	svc.SetForwardQueue(repositories.NewInboundForwardTaskRepository(gdb))
	svc.SetWebhookDispatcher(f.webhooks)
	svc.SetEnqueuer(f.eq)
	svc.SetBlobStore(f.blobs)

	f.qsvc = NewQuarantineService(
		repositories.NewInboundQuarantineRepository(gdb),
		inboundRepo,
		domainRepo,
		svc,
	)
	svc.SetQuarantineStore(f.qsvc)

	bus := eventbus.New(repositories.NewEventRepository(gdb))
	svc.SetEventBus(bus)
	events, unsub := bus.Subscribe()
	t.Cleanup(unsub)
	f.svc = svc
	f.events = events
	return f
}

// quIngestRaw ingests the fixture message for recipient on the SMTP path.
func (f *quFixture) quIngestRaw(t *testing.T, recipient string) (*models.InboundEmail, error) {
	t.Helper()
	return f.svc.IngestRaw(context.Background(), []byte(raFixtureRaw), "alice@example.test",
		[]string{recipient}, models.InboundSourceSMTP, "192.0.2.10")
}

// quPendingRow fetches the single quarantine row in the fake (or fails).
func (f *quFixture) quPendingRow(t *testing.T) map[string]driver.Value {
	t.Helper()
	rows := f.state.rows(t, "inbound_quarantine")
	if len(rows) != 1 {
		t.Fatalf("quarantine rows = %d, want 1", len(rows))
	}
	return rows[0]
}

// ---------- store seam: persistence at ingest ----------

// A quarantine route holds the durably persisted message: the inbound row
// flips to quarantined, the store seam writes the pending review record
// (reason, route provenance, workspace scoping, arrival time), and the
// message is never parsed or dispatched while held.
func TestQuarantineRoutePersistsPendingRecordViaSeam(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))

	rec, err := f.quIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("inbound status = %q, want quarantined", rec.Status)
	}

	row := f.quPendingRow(t)
	if row["inbound_email_id"] != int64(rec.ID) {
		t.Fatalf("record inbound ref = %v, want %d", row["inbound_email_id"], rec.ID)
	}
	if row["status"] != string(models.InboundQuarantinePending) {
		t.Fatalf("record status = %v, want pending", row["status"])
	}
	if row["route_id"] != int64(4) {
		t.Fatalf("record route_id = %v, want 4 (quarantine provenance)", row["route_id"])
	}
	if msg, _ := row["reason"].(string); !strings.Contains(msg, "quarantine route") {
		t.Fatalf("record reason = %q, want the route reason", msg)
	}
	if row["user_id"] != int64(7) || row["workspace_id"] != int64(3) || row["domain_id"] != int64(1) {
		t.Fatalf("record scope = user %v ws %v domain %v, want 7/3/1", row["user_id"], row["workspace_id"], row["domain_id"])
	}
	if row["received_at"] == nil {
		t.Fatal("record received_at must be set")
	}
	if row["score"] != nil {
		t.Fatalf("record score = %v, want NULL (SMTP raw path has no score yet)", row["score"])
	}
	if f.eq.parseCount() != 0 {
		t.Fatalf("held message must not be parsed, got %d parse tasks", f.eq.parseCount())
	}
	if f.webhooks.count() != 0 {
		t.Fatal("held message must not dispatch webhooks")
	}

	// Review state: still pending, release/delete surfaces see it.
	got, _, err := f.qsvc.List(quScope, repositories.InboundQuarantineFilter{}, 10, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %d items, err %v, want 1", len(got), err)
	}
	if got[0].Status != models.InboundQuarantinePending {
		t.Fatalf("listed status = %q, want pending", got[0].Status)
	}
}

// The store seam is idempotent per message: a repeated or racing quarantine
// action cannot mint a second pending record for the same message.
func TestQuarantineStoreIdempotentPerPendingMessage(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))

	rec, err := f.quIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	rt := &models.InboundRoute{ID: 4, Action: models.InboundRouteActionQuarantine}
	if err := f.qsvc.Quarantine(context.Background(), rec, rt, "matched quarantine route"); err != nil {
		t.Fatalf("second Quarantine call: %v", err)
	}
	if n := f.state.rowCount(t, "inbound_quarantine"); n != 1 {
		t.Fatalf("quarantine rows = %d, want 1 (idempotent store)", n)
	}
}

// ---------- release: routing re-runs at the post-ingest point ----------

// Release with the route changed to deliver: the record re-stamps the newly
// resolved route, the message returns to the received pipeline (parse
// enqueued), and the record is marked released with the reviewer stamped.
// A second release attempt is rejected — a disposition is taken once.
func TestQuarantineReleaseReRoutesToDeliverAndMarksReleased(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))
	rec, err := f.quIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	quRowID := int(f.quPendingRow(t)["id"].(int64))

	// Routing changed while the message was held: quarantine off, deliver on.
	f.state.quSetRouteEnabled(4, false)
	f.state.addRoute(5, 1, 7, raWs(3), "user@routed.test", models.InboundRouteActionDeliver, 10, true)

	row, err := f.qsvc.Release(context.Background(), quScope, uint(quRowID), 99)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if row.Status != models.InboundQuarantineReleased {
		t.Fatalf("record status = %q, want released", row.Status)
	}
	if row.ReviewedBy == nil || *row.ReviewedBy != 99 {
		t.Fatalf("reviewed_by = %v, want 99", row.ReviewedBy)
	}
	if row.ReviewedAt == nil || row.ReviewedAt.IsZero() {
		t.Fatal("reviewed_at must be set")
	}

	fresh, err := f.inbounds.FindByID(rec.ID)
	if err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if fresh.Status != models.InboundStatusReceived {
		t.Fatalf("inbound status = %q, want received after release", fresh.Status)
	}
	if fresh.RouteID == nil || *fresh.RouteID != 5 {
		t.Fatalf("route stamp = %v, want the re-resolved deliver route 5", fresh.RouteID)
	}
	if f.eq.parseCount() != 1 {
		t.Fatalf("parse enqueue count = %d, want 1 (release re-enters the pipeline)", f.eq.parseCount())
	}
	if f.webhooks.count() != 0 {
		t.Fatal("deliver release must not dispatch webhooks")
	}

	types := raEventTypes(raDrainEvents(t, f.events))
	found := false
	for _, ty := range types {
		if ty == "inbound.quarantine_released" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want inbound.quarantine_released", types)
	}

	// The disposition is final.
	if _, err := f.qsvc.Release(context.Background(), quScope, uint(quRowID), 99); !errors.Is(err, ErrQuarantineNotPending) {
		t.Fatalf("second release err = %v, want ErrQuarantineNotPending", err)
	}
}

// Release with the route changed to forward: routing re-runs and the forward
// task queue receives the row — the same post-ingest action path as ingest.
func TestQuarantineReleaseReRoutesToForwardQueuesTask(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))
	if _, err := f.quIngestRaw(t, "user@routed.test"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	quRowID := int(f.quPendingRow(t)["id"].(int64))

	f.state.quSetRouteEnabled(4, false)
	f.state.addRoute(5, 1, 7, raWs(3), "user@routed.test", models.InboundRouteActionForward, 10, true)
	f.state.setRouteTarget(5, "dest@elsewhere.test")

	if _, err := f.qsvc.Release(context.Background(), quScope, uint(quRowID), 99); err != nil {
		t.Fatalf("Release: %v", err)
	}
	tasks := f.state.rows(t, "inbound_forward_tasks")
	if len(tasks) != 1 {
		t.Fatalf("forward tasks = %d, want 1 after release", len(tasks))
	}
	if tasks[0]["target"] != "dest@elsewhere.test" {
		t.Fatalf("forward target = %v, want dest@elsewhere.test", tasks[0]["target"])
	}
	if f.eq.parseCount() != 1 {
		t.Fatalf("parse enqueue count = %d, want 1", f.eq.parseCount())
	}
	if f.webhooks.count() != 0 {
		t.Fatal("forward release must not dispatch webhooks")
	}
}

// Release with the route changed to another quarantine rule: routing re-runs
// and the message is held again under a NEW pending record — the old record
// stays released, the hold survives, and parsing stays skipped.
func TestQuarantineReleaseToQuarantineRouteReHoldsMessage(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))
	rec, err := f.quIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	quRowID := int(f.quPendingRow(t)["id"].(int64))

	f.state.quSetRouteEnabled(4, false)
	f.state.addRoute(6, 1, 7, raWs(3), "user@routed.test", models.InboundRouteActionQuarantine, 10, true)

	if _, err := f.qsvc.Release(context.Background(), quScope, uint(quRowID), 99); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if n := f.state.rowCount(t, "inbound_quarantine"); n != 2 {
		t.Fatalf("quarantine rows = %d, want 2 (old released + new pending)", n)
	}
	fresh, err := f.inbounds.FindByID(rec.ID)
	if err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if fresh.Status != models.InboundStatusQuarantined {
		t.Fatalf("inbound status = %q, want quarantined (re-held)", fresh.Status)
	}
	if f.eq.parseCount() != 0 {
		t.Fatalf("re-held message must not be parsed, got %d parse tasks", f.eq.parseCount())
	}
}

// Release fails closed when nothing matches anymore: no route (and no
// catch-all) resolves on the verified domain, the hold stands untouched.
func TestQuarantineReleaseFailsClosedWithoutRoute(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))
	rec, err := f.quIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	quRowID := int(f.quPendingRow(t)["id"].(int64))
	f.state.quSetRouteEnabled(4, false) // no enabled route remains

	if _, err := f.qsvc.Release(context.Background(), quScope, uint(quRowID), 99); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("release err = %v, want ErrNoRoute (fail closed)", err)
	}
	fresh, err := f.inbounds.FindByID(rec.ID)
	if err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if fresh.Status != models.InboundStatusQuarantined {
		t.Fatalf("inbound status = %q, want quarantined (hold stands)", fresh.Status)
	}
	if row := f.quPendingRow(t); row["status"] != string(models.InboundQuarantinePending) {
		t.Fatalf("record status = %v, want pending (untouched)", row["status"])
	}
	if f.eq.parseCount() != 0 {
		t.Fatal("failed release must not enqueue parsing")
	}
}

// Release fails closed when the domain lost verification since ingest: the
// non-negotiable — routing rules resolve only through tenant-verified
// domains — applies at release too.
func TestQuarantineReleaseFailsWhenDomainUnverified(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))
	rec, err := f.quIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	quRowID := int(f.quPendingRow(t)["id"].(int64))
	f.state.quSetDomainVerified(false)

	if _, err := f.qsvc.Release(context.Background(), quScope, uint(quRowID), 99); !errors.Is(err, ErrQuarantineUnverifiedDomain) {
		t.Fatalf("release err = %v, want ErrQuarantineUnverifiedDomain", err)
	}
	fresh, err := f.inbounds.FindByID(rec.ID)
	if err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if fresh.Status != models.InboundStatusQuarantined {
		t.Fatalf("inbound status = %q, want quarantined (hold stands)", fresh.Status)
	}
}

// ---------- delete: permanent + audited ----------

// Delete destroys the message for good: the inbound row is gone, blob
// content is removed, the record flips to the deleted tombstone with the
// reviewer stamped, and an audit event records what was destroyed. The
// disposition is final.
func TestQuarantineDeleteIsPermanentAndAudited(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))
	rec, err := f.quIngestRaw(t, "user@routed.test")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Blob content exists (raw .eml promoted + one attachment).
	rec.RawStorageKey = "inbound/" + rec.UUID + "/raw.eml"
	rec.AttachmentsJSON = `[{"filename":"a.bin","content_type":"application/octet-stream","size":3,"storage_key":"inbound/` + rec.UUID + `/0_a.bin"}]`
	if err := f.inbounds.Update(rec); err != nil {
		t.Fatalf("stage blob refs: %v", err)
	}
	quRowID := int(f.quPendingRow(t)["id"].(int64))

	row, err := f.qsvc.Delete(context.Background(), quScope, uint(quRowID), 99)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if row.Status != models.InboundQuarantineDeleted {
		t.Fatalf("record status = %q, want deleted", row.Status)
	}
	if row.ReviewedBy == nil || *row.ReviewedBy != 99 {
		t.Fatalf("reviewed_by = %v, want 99", row.ReviewedBy)
	}
	if row.ReviewedAt == nil || row.ReviewedAt.IsZero() {
		t.Fatal("reviewed_at must be set")
	}

	if n := f.state.rowCount(t, "inbound_emails"); n != 0 {
		t.Fatalf("inbound rows = %d, want 0 (permanent deletion)", n)
	}
	deletes := f.blobs.deletedKeys()
	if len(deletes) != 2 {
		t.Fatalf("blob deletes = %v, want the raw eml + the attachment key", deletes)
	}
	for _, want := range []string{rec.RawStorageKey, "inbound/" + rec.UUID + "/0_a.bin"} {
		ok := false
		for _, got := range deletes {
			if got == want {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("blob delete for %q missing, got %v", want, deletes)
		}
	}

	types := raEventTypes(raDrainEvents(t, f.events))
	found := false
	for _, ty := range types {
		if ty == "inbound.quarantine_deleted" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %v, want inbound.quarantine_deleted (audited)", types)
	}

	if _, err := f.qsvc.Delete(context.Background(), quScope, uint(quRowID), 99); !errors.Is(err, ErrQuarantineNotPending) {
		t.Fatalf("second delete err = %v, want ErrQuarantineNotPending", err)
	}
}

// ---------- workspace isolation ----------

// Quarantine is workspace-scoped end to end: another workspace's record is
// invisible to the list and unaddressable by release and delete — the
// cross-workspace calls answer "not found", never the row.
func TestQuarantineRecordsAreWorkspaceIsolated(t *testing.T) {
	f := quNewFixture(t, route(4, "user@routed.test", models.InboundRouteActionQuarantine, 10, true))
	if _, err := f.quIngestRaw(t, "user@routed.test"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	quRowID := uint(f.quPendingRow(t)["id"].(int64))

	other := repositories.ResourceScope{UserID: 8, WorkspaceID: raWs(4)}

	items, total, err := f.qsvc.List(other, repositories.InboundQuarantineFilter{}, 10, 0)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("other-workspace List = %d items, total %d, err %v, want empty", len(items), total, err)
	}
	ownItems, ownTotal, err := f.qsvc.List(quScope, repositories.InboundQuarantineFilter{}, 10, 0)
	if err != nil || ownTotal != 1 || len(ownItems) != 1 {
		t.Fatalf("own List = %d items, total %d, err %v, want 1", len(ownItems), ownTotal, err)
	}

	if _, err := f.qsvc.Release(context.Background(), other, quRowID, 8); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("other-workspace release err = %v, want ErrRecordNotFound", err)
	}
	if _, err := f.qsvc.Delete(context.Background(), other, quRowID, 8); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("other-workspace delete err = %v, want ErrRecordNotFound", err)
	}
	if row := f.quPendingRow(t); row["status"] != string(models.InboundQuarantinePending) {
		t.Fatalf("record status = %v, want pending (foreign calls were no-ops)", row["status"])
	}
	if n := f.state.rowCount(t, "inbound_emails"); n != 1 {
		t.Fatalf("inbound rows = %d, want 1 (foreign calls destroyed nothing)", n)
	}
}
