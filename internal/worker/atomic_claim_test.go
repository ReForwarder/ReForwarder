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
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The R-26 atomic-claim tests run the real repositories and the real
// EmailSendHandler against the real gorm on a fake database/sql driver (the
// queue_reconcile_test.go arrangement): the fake stores rows, evaluates the
// = and IN comparisons the claims issue, applies SET pairs including the
// retry_count arithmetic, and reports RowsAffected so the CAS semantics are
// pinned — exactly one claimant wins a queued row.

type claimRow struct {
	cols map[string]driver.Value
}

type claimState struct {
	mu     sync.Mutex
	tables map[string][]*claimRow
	calls  []string
}

func newClaimState() *claimState {
	return &claimState{tables: map[string][]*claimRow{}}
}

func (s *claimState) insert(table string, vals map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := &claimRow{cols: map[string]driver.Value{}}
	for col, v := range vals {
		row.cols[col] = claimVal(v)
	}
	if _, ok := row.cols["id"]; !ok {
		row.cols["id"] = int64(len(s.tables[table]) + 1)
	}
	if _, ok := row.cols["uuid"]; !ok && table == "emails" {
		row.cols["uuid"] = fmt.Sprintf("uuid-%v", row.cols["id"])
	}
	if _, ok := row.cols["recipients"]; !ok && table == "emails" {
		row.cols["recipients"] = "{rcpt@example.com}"
	}
	s.tables[table] = append(s.tables[table], row)

}

func claimVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case int:
		return int64(t)
	case models.EmailStatus:
		return string(t)
	case models.CampaignMessageStatus:
		return string(t)
	case models.EmailQueueScheduleStatus:
		return string(t)
	case time.Time:
		return t
	case int64, float64, bool, string, []byte:
		return t
	default:
		panic(fmt.Sprintf("claimVal: unsupported type %T", v))
	}
}

// countSelects reports how many SELECTs touched a table (used to assert the
// losing delivery never reached the send path).
func (s *claimState) countSelects(table string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, q := range s.calls {
		lower := strings.Join(strings.Fields(strings.ToLower(q)), " ")
		if strings.Contains(lower, "select") && strings.Contains(lower, `from "`+table+`"`) {
			n++
		}
	}
	return n
}

func (s *claimState) statusOf(table string, id int64) driver.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.tables[table] {
		if row.cols["id"] == id {
			return row.cols["status"]
		}
	}
	return nil
}

func (s *claimState) intOf(table string, id int64, col string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.tables[table] {
		if row.cols["id"] == id {
			if v, ok := row.cols[col].(int64); ok {
				return v
			}
			return 0
		}
	}
	return 0
}

func (s *claimState) stringOf(table string, id int64, col string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.tables[table] {
		if row.cols["id"] == id {
			if v, ok := row.cols[col].(string); ok {
				return v
			}
		}
	}
	return ""
}

type claimConn struct{ s *claimState }

func (c *claimConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *claimConn) Close() error                        { return nil }
func (c *claimConn) Begin() (driver.Tx, error)           { return claimTx{}, nil }

type claimTx struct{}

func (claimTx) Commit() error   { return nil }
func (claimTx) Rollback() error { return nil }

type claimResult struct{ rows int64 }

func (r claimResult) LastInsertId() (int64, error) { return 0, nil }
func (r claimResult) RowsAffected() (int64, error) { return r.rows, nil }

type claimRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *claimRows) Columns() []string { return r.cols }
func (r *claimRows) Close() error      { return nil }
func (r *claimRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type claimConnector struct{ state *claimState }

func (c claimConnector) Connect(context.Context) (driver.Conn, error) {
	return &claimConn{s: c.state}, nil
}
func (c claimConnector) Driver() driver.Driver { return claimDrv{} }

type claimDrv struct{}

func (claimDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func claimGorm(t *testing.T, state *claimState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(claimConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open claim fake gorm: %v", err)
	}
	return gdb
}

func (c *claimConn) record(query string, _ []driver.NamedValue) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.calls = append(c.s.calls, query)
}

func (c *claimConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	switch {
	case strings.Contains(lower, `from "webhooks"`):
		// The dispatcher's goroutine may look for matching webhooks; none exist.
		return &claimRows{cols: []string{"id"}}, nil
	case strings.Contains(lower, `from "smtp_servers"`):
		// SF-02 relay-guard tests seed SMTP server rows; anything else in the
		// suite keeps the pre-existing "no server configured" answer.
		matched, err := c.s.evalWhere("smtp_servers", lower, args)
		if err != nil {
			return nil, err
		}
		matched = claimLimit(matched, lower)
		if len(matched) == 0 {
			return &claimRows{cols: []string{"id"}}, nil
		}
		cols := claimOrderedCols(matched[0])
		rows := make([][]driver.Value, len(matched))
		for i, r := range matched {
			for _, col := range cols {
				rows[i] = append(rows[i], r.cols[col])
			}
		}
		return &claimRows{cols: cols, rows: rows}, nil
	case strings.Contains(lower, `from "subscribers"`):
		// The FindPendingByCampaign preload: fixture rows carry no subscriber.
		return &claimRows{cols: []string{"id"}}, nil
	case strings.Contains(lower, `from "domains"`):
		// The send worker's delivery-mode lookup (OB-1) reads the sender
		// domain's configuration row; the fixture rows decide relay vs direct.
		matched, err := c.s.evalWhere("domains", lower, args)
		if err != nil {
			return nil, err
		}
		matched = claimLimit(matched, lower)
		if len(matched) == 0 {
			return &claimRows{cols: []string{"id"}}, nil
		}
		cols := claimOrderedCols(matched[0])
		rows := make([][]driver.Value, len(matched))
		for i, r := range matched {
			for _, col := range cols {
				rows[i] = append(rows[i], r.cols[col])
			}
		}
		return &claimRows{cols: cols, rows: rows}, nil
	case strings.Contains(lower, `from "emails"`), strings.Contains(lower, `from "campaign_messages"`),
		strings.Contains(lower, `from "campaigns"`), strings.Contains(lower, `from "email_queue_schedules"`):
		table := "emails"
		switch {
		case strings.Contains(lower, `from "campaign_messages"`):
			table = "campaign_messages"
		case strings.Contains(lower, `from "campaigns"`):
			table = "campaigns"
		case strings.Contains(lower, `from "email_queue_schedules"`):
			table = "email_queue_schedules"
		}
		matched, err := c.s.evalWhere(table, lower, args)
		if err != nil {
			return nil, err
		}
		matched = claimLimit(matched, lower)
		if len(matched) == 0 {
			return &claimRows{cols: []string{"id"}}, nil
		}
		cols := claimOrderedCols(matched[0])
		rows := make([][]driver.Value, len(matched))
		for i, r := range matched {
			for _, col := range cols {
				rows[i] = append(rows[i], r.cols[col])
			}
		}
		return &claimRows{cols: cols, rows: rows}, nil
	}
	return nil, fmt.Errorf("claimState: cannot route query: %s", query)
}

func (c *claimConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.record(query, args)
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := ""
	for _, cand := range []string{"emails", "campaign_messages", "email_queue_schedules"} {
		if strings.Contains(lower, `update "`+cand+`"`) {
			table = cand
			break
		}
	}
	if table == "" {
		return nil, fmt.Errorf("claimState: unsupported exec: %s", query)
	}
	return c.s.applyUpdate(table, lower, args)
}

// claimOrderedCols returns a stable column order for a row.
func claimOrderedCols(r *claimRow) []string {
	order := []string{"id", "uuid", "status", "error_message", "retry_count", "campaign_id",
		"email_id", "sender", "recipients", "subject", "user_id", "workspace_id",
		"created_at", "updated_at", "sent_at"}
	var out []string
	seen := map[string]bool{}
	for _, c := range order {
		if _, ok := r.cols[c]; ok {
			out = append(out, c)
			seen[c] = true
		}
	}
	for c := range r.cols {
		if !seen[c] {
			out = append(out, c)
		}
	}
	return out
}

// evalWhere evaluates a SELECT's WHERE clause (equality, IN, IS NULL).
func (s *claimState) evalWhere(table, compact string, args []driver.NamedValue) ([]*claimRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := compact
	if i := strings.Index(where, " where "); i >= 0 {
		where = where[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}
	preds, err := s.predsFor(where, args)
	if err != nil {
		return nil, err
	}
	var out []*claimRow
	for _, row := range s.tables[table] {
		ok := true
		for _, p := range preds {
			if !p(row.cols) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *claimState) predsFor(where string, args []driver.NamedValue) ([]func(map[string]driver.Value) bool, error) {
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)
	var preds []func(map[string]driver.Value) bool
	for _, cond := range strings.Split(where, " and ") {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		switch {
		case strings.HasSuffix(cond, " is null"):
			col := claimColumnOf(strings.TrimSuffix(cond, " is null"))
			preds = append(preds, func(cols map[string]driver.Value) bool { return cols[col] == nil })
		case strings.Contains(cond, " in "):
			i := strings.Index(cond, " in ")
			col := claimColumnOf(cond[:i])
			want := map[driver.Value]bool{}
			for _, part := range strings.Split(cond[i+len(" in "):], ",") {
				part = strings.TrimSpace(part)
				if !strings.HasPrefix(part, "$") {
					return nil, fmt.Errorf("claimState: unsupported IN member %q", part)
				}
				want[claimArg(args, part[1:])] = true
			}
			preds = append(preds, func(cols map[string]driver.Value) bool { return want[cols[col]] })
		default:
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("claimState: unsupported condition %q", cond)
			}
			col := claimColumnOf(cond[:eq])
			rhs := strings.TrimSpace(cond[eq+len(" = "):])
			if !strings.HasPrefix(rhs, "$") {
				return nil, fmt.Errorf("claimState: unsupported rhs %q", rhs)
			}
			want := claimArg(args, rhs[1:])
			preds = append(preds, func(cols map[string]driver.Value) bool {
				return claimEqual(cols[col], want)
			})
		}
	}
	return preds, nil
}

// applyUpdate applies an UPDATE's SET clause (binds and the retry_count
// arithmetic) to every row matching its WHERE, and reports RowsAffected so the
// CAS claims observe loser-gets-zero.
func (s *claimState) applyUpdate(table, lower string, args []driver.NamedValue) (driver.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	setStart := strings.Index(lower, " set ")
	whereStart := strings.Index(lower, " where ")
	if setStart < 0 || whereStart < 0 {
		return nil, fmt.Errorf("claimState: malformed update: %s", lower)
	}
	setClause := lower[setStart+len(" set ") : whereStart]
	type setter struct {
		col string
		val func(map[string]driver.Value) driver.Value
	}
	var setters []setter
	for _, pair := range strings.Split(setClause, ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return nil, fmt.Errorf("claimState: malformed SET pair %q", pair)
		}
		col := claimColumnOf(pair[:eq])
		rhs := strings.TrimSpace(pair[eq+1:])
		switch {
		case strings.HasPrefix(rhs, "$"):
			n, err := strconv.Atoi(rhs[1:])
			if err != nil {
				return nil, fmt.Errorf("claimState: bad bindvar in SET %q", pair)
			}
			v := claimArg(args, strconv.Itoa(n))
			setters = append(setters, setter{col: col, val: func(map[string]driver.Value) driver.Value { return v }})
		default:
			// retry_count = retry_count + 1 (gorm.Expr arithmetic).
			parts := strings.Fields(rhs)
			if len(parts) != 3 || claimColumnOf(parts[0]) != col || (parts[1] != "+" && parts[1] != "-") {
				return nil, fmt.Errorf("claimState: unsupported SET expression %q", pair)
			}
			delta, err := strconv.ParseInt(parts[2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("claimState: bad delta in SET %q", pair)
			}
			if parts[1] == "-" {
				delta = -delta
			}
			setters = append(setters, setter{col: col, val: func(cols map[string]driver.Value) driver.Value {
				cur, _ := cols[col].(int64)
				return cur + delta
			}})
		}
	}
	where := lower[whereStart+len(" where "):]
	preds, err := s.predsFor(where, args)
	if err != nil {
		return nil, err
	}
	if os.Getenv("CLAIM_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "claimState: table=%s args=%v\n", table, args)
	}
	matched := 0
	for _, row := range s.tables[table] {
		ok := true
		for _, p := range preds {
			if !p(row.cols) {
				ok = false
				break
			}
		}
		if ok {
			matched++
			for _, st := range setters {
				row.cols[st.col] = st.val(row.cols)
			}
		}
	}
	return claimResult{rows: int64(matched)}, nil
}

func claimColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func claimArg(args []driver.NamedValue, ordinal string) driver.Value {
	n, err := strconv.Atoi(ordinal)
	if err != nil {
		panic(fmt.Sprintf("claimState: bad ordinal %q", ordinal))
	}
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("claimState: no arg with ordinal %d", n))
}

func claimEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok2 := b.(time.Time)
		return ok2 && ta.Equal(tb)
	}
	if sa, ok := a.(string); ok {
		sb, ok2 := b.(string)
		if !ok2 {
			sb = fmt.Sprint(b)
		}
		return sa == sb
	}
	return a == b
}

// claimLimit applies LIMIT $n to a SELECT's matched rows (ORDER BY ignored).
func claimLimit(rows []*claimRow, compact string) []*claimRow {
	i := strings.Index(compact, " limit ")
	if i < 0 {
		return rows
	}
	n, err := strconv.Atoi(strings.TrimSpace(compact[i+len(" limit "):])[1:])
	if err != nil || n >= len(rows) {
		return rows
	}
	return rows[:n]
}

// ---------- claim tests ----------

func seedClaimEmail(t *testing.T, s *claimState, id int64, status models.EmailStatus, retryCount int64) {
	t.Helper()
	s.insert("emails", map[string]any{
		"id": id, "status": status, "retry_count": retryCount,
		"sender": "sender@example.com", "subject": "claim test",
		"created_at": time.Now(), "updated_at": time.Now(),
	})
}

// The queued→processing claim flips in one UPDATE. A row still processing is
// claimable too by design — asynq redelivers a task after a worker crash while
// the row is still processing, and no second task can exist for it — while
// terminal states are never claimable.
func TestClaimForProcessingAllowsExactlyOneClaim(t *testing.T) {
	state := newClaimState()
	seedClaimEmail(t, state, 11, models.EmailStatusQueued, 0)
	repo := repositories.NewEmailRepository(claimGorm(t, state))

	won, err := repo.ClaimForProcessing(11)
	if err != nil || !won {
		t.Fatalf("claim of queued row = (%v, %v), want (true, nil)", won, err)
	}
	if got := state.statusOf("emails", 11); got != string(models.EmailStatusProcessing) {
		t.Fatalf("status after claim = %v, want processing", got)
	}

	// A crash-redelivered task finds the row processing and may claim it.
	won, err = repo.ClaimForProcessing(11)
	if err != nil || !won {
		t.Fatalf("claim of processing row = (%v, %v), want (true, nil)", won, err)
	}

	// Terminal states are never claimable: a duplicate delivery of a task whose
	// email already sent/failed cannot re-enter the send path.
	seedClaimEmail(t, state, 12, models.EmailStatusSent, 0)
	if won, _ := repo.ClaimForProcessing(12); won {
		t.Fatal("sent email must not be claimable")
	}
	seedClaimEmail(t, state, 13, models.EmailStatusFailed, 0)
	if won, _ := repo.ClaimForProcessing(13); won {
		t.Fatal("failed email must not be claimable by the send claim")
	}
	// Unknown ids claim nothing.
	if won, _ := repo.ClaimForProcessing(999); won {
		t.Fatal("unknown email must not be claimable")
	}
}

// The retry claim flips failed→queued, bumps retry_count, clears the error, and
// can only be won once — a user retry and the retry poller cannot both claim.
func TestClaimForRetryClaimsOnceAndIncrementsOnce(t *testing.T) {
	state := newClaimState()
	seedClaimEmail(t, state, 21, models.EmailStatusFailed, 2)
	state.mu.Lock()
	for _, row := range state.tables["emails"] {
		if row.cols["id"] == int64(21) {
			row.cols["error_message"] = "SMTP send failed"
		}
	}
	state.mu.Unlock()
	repo := repositories.NewEmailRepository(claimGorm(t, state))

	won, err := repo.ClaimForRetry(21)
	if err != nil || !won {
		t.Fatalf("first retry claim = (%v, %v), want (true, nil)", won, err)
	}
	if got := state.statusOf("emails", 21); got != string(models.EmailStatusQueued) {
		t.Fatalf("status after retry claim = %v, want queued", got)
	}
	if got := state.intOf("emails", 21, "retry_count"); got != 3 {
		t.Fatalf("retry_count after claim = %d, want 3", got)
	}
	if got := state.stringOf("emails", 21, "error_message"); got != "" {
		t.Fatalf("error_message after claim = %q, want empty", got)
	}

	// The row is queued now: a racing second claim (stale failed row) loses.
	won, err = repo.ClaimForRetry(21)
	if err != nil || won {
		t.Fatalf("second retry claim = (%v, %v), want (false, nil)", won, err)
	}
	if got := state.intOf("emails", 21, "retry_count"); got != 3 {
		t.Fatalf("retry_count after losing claim = %d, want 3", got)
	}
}

// The campaign batch claim is one-directional: pending→queued exactly once.
func TestClaimForSendAllowsExactlyOneClaim(t *testing.T) {
	state := newClaimState()
	state.insert("campaign_messages", map[string]any{"id": int64(31), "status": models.CampaignMsgPending})
	repo := repositories.NewCampaignMessageRepository(claimGorm(t, state))

	won, err := repo.ClaimForSend(31)
	if err != nil || !won {
		t.Fatalf("first batch claim = (%v, %v), want (true, nil)", won, err)
	}
	if got := state.statusOf("campaign_messages", 31); got != string(models.CampaignMsgQueued) {
		t.Fatalf("status after batch claim = %v, want queued", got)
	}
	won, err = repo.ClaimForSend(31)
	if err != nil || won {
		t.Fatalf("second batch claim = (%v, %v), want (false, nil)", won, err)
	}
}

// FindPendingByCampaign claims its batch with FOR UPDATE SKIP LOCKED (R-26):
// the documented batch-claim mechanism. Two workers racing on one campaign
// must take disjoint batches, and the returned rows are exactly the campaign's
// still-pending messages in id order.
func TestFindPendingByCampaignUsesSkipLockedBatchClaim(t *testing.T) {
	state := newClaimState()
	state.insert("campaign_messages", map[string]any{
		"id": int64(61), "campaign_id": int64(9), "status": models.CampaignMsgPending,
	})
	state.insert("campaign_messages", map[string]any{
		"id": int64(62), "campaign_id": int64(9), "status": models.CampaignMsgPending,
	})
	state.insert("campaign_messages", map[string]any{
		"id": int64(63), "campaign_id": int64(9), "status": models.CampaignMsgQueued,
	})
	state.insert("campaign_messages", map[string]any{
		"id": int64(64), "campaign_id": int64(8), "status": models.CampaignMsgPending,
	})
	repo := repositories.NewCampaignMessageRepository(claimGorm(t, state))

	items, err := repo.FindPendingByCampaign(9, 50)
	if err != nil {
		t.Fatalf("FindPendingByCampaign: %v", err)
	}
	if len(items) != 2 || items[0].ID != 61 || items[1].ID != 62 {
		t.Fatalf("batch = %+v, want pending ids [61 62]", items)
	}

	// The batch SELECT carried the locking clause — the mechanism the tracker
	// documents ("FOR UPDATE SKIP LOCKED batch claims"), matching the
	// automation journey delay queue's claim query.
	var lockQuery string
	state.mu.Lock()
	for _, q := range state.calls {
		lower := strings.Join(strings.Fields(strings.ToLower(q)), " ")
		if strings.Contains(lower, `from "campaign_messages"`) && strings.Contains(lower, "select") {
			lockQuery = lower
			break
		}
	}
	state.mu.Unlock()
	if !strings.Contains(lockQuery, "for update skip locked") {
		t.Fatalf("batch SELECT must carry FOR UPDATE SKIP LOCKED, got: %s", lockQuery)
	}
}

// A duplicate email:send delivery must never re-enter the send path: the
// claim's UPDATE matches nothing once the row left queued, so the handler
// returns success without touching the SMTP layer again.
func TestEmailSendHandlerDuplicateDeliverySendsOnce(t *testing.T) {
	state := newClaimState()
	seedClaimEmail(t, state, 41, models.EmailStatusQueued, 0)
	gdb := claimGorm(t, state)

	handler := NewEmailSendHandler(
		repositories.NewEmailRepository(gdb),
		repositories.NewSMTPRepository(gdb),
		nil, nil, nil,
		nil, // nil dispatcher: atomic-claim test, not webhook delivery; Dispatch goroutine races with the non-thread-safe fake DB under -race.
	)

	payload, err := NewEmailSendTask(41)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	// First delivery: claim wins, no SMTP server is configured, the email is
	// marked failed.
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("first ProcessTask: %v", err)
	}
	if got := state.statusOf("emails", 41); got != string(models.EmailStatusFailed) {
		t.Fatalf("status after first delivery = %v, want failed", got)
	}
	if n := state.countSelects("smtp_servers"); n != 1 {
		t.Fatalf("smtp_servers selects after first delivery = %d, want 1", n)
	}

	// Duplicate delivery of the same task: the claim loses, the SMTP layer is
	// never consulted again, and the task succeeds (asynq must not retry it).
	if err := handler.ProcessTask(context.Background(), payload); err != nil {
		t.Fatalf("second ProcessTask must succeed: %v", err)
	}
	if n := state.countSelects("smtp_servers"); n != 1 {
		t.Fatalf("smtp_servers selects after duplicate delivery = %d, want 1 (send path re-entered)", n)
	}
	if got := state.statusOf("emails", 41); got != string(models.EmailStatusFailed) {
		t.Fatalf("status after duplicate delivery = %v, want failed", got)
	}
}
