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
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The fake driver follows the repo's established in-memory pattern (see
// internal/cron/jobs/queue_reconcile_test.go): a database/sql driver over
// in-memory tables, wrapped in gorm.io/driver/postgres, implementing exactly
// the statement surface the mailstore service issues. Transactions are no-ops
// here (mutex-serialized autocommit); real transactional and atomic-claim
// semantics are covered by the real-Postgres suite (mailstore_pg_test.go).

var msColumns = map[string][]string{
	"mailboxes": {"id", "user_id", "workspace_id", "name", "quota_bytes", "quota_messages",
		"used_bytes", "message_count", "status", "retention_days", "retention_max_count", "created_at"},
	"mail_folders": {
		"id", "mailbox_id", "parent_id", "name", "imap_path",
		"uid_validity", "uid_next", "mod_seq_next", "subscribed",
		"retention_days", "retention_max_count", "created_at",
	},
	"mail_messages": {
		"id", "folder_id", "uid", "message_id", "subject", "from_addr", "to_addr", "cc",
		"date", "size", "flags", "mod_seq", "snippet", "body_blob_ref", "body_text",
		"body_size", "attachments_json", "created_at", "updated_at",
	},
	"mail_blob_refs": {"id", "blob_hash", "size", "ref_count", "last_referenced", "created_at"},
	// Quota wave (ADR-003 decision 5): the workspace aggregate ceiling reads
	// the ceiling off the workspace row; only the columns the enforcer reads
	// are modelled.
	"workspaces": {"id", "mail_quota_bytes"},
}

type msTable struct {
	rows [][]driver.Value // aligned with msColumns[table]
}

type msState struct {
	mu     sync.Mutex
	tables map[string]*msTable
	seq    int64
}

func newMSState() *msState {
	t := make(map[string]*msTable, len(msColumns))
	for name := range msColumns {
		t[name] = &msTable{}
	}
	return &msState{tables: t, seq: 1000}
}

func (s *msState) colIndex(table, col string) int {
	for i, c := range msColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

// msVal normalizes a Go value to a driver.Value for storage/comparison.
func msVal(v any) driver.Value {
	switch t := v.(type) {
	case nil:
		return nil
	case uint:
		return int64(t)
	case uint32:
		return int64(t)
	case uint64:
		return int64(t)
	case int:
		return int64(t)
	case int64, float64, bool, string, []byte, time.Time:
		return t
	case models.MailboxStatus:
		return string(t)
	default:
		panic(fmt.Sprintf("msVal: unsupported type %T", v))
	}
}

func msArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return msVal(a.Value)
		}
	}
	panic(fmt.Sprintf("msState: missing bind var $%d", n))
}

// normalizeSQL lowercases, collapses whitespace, and drops identifier quotes
// so GORM's `"mailboxes"."id"` forms match plain names.
func normalizeSQL(q string) string {
	q = strings.ReplaceAll(q, `"`, "")
	return strings.Join(strings.Fields(strings.ToLower(q)), " ")
}

// stripQual turns `"mail_messages"."uid"` / `mail_messages.uid` into `uid`.
func stripQual(col string) string {
	col = strings.ReplaceAll(col, `"`, "")
	if i := strings.LastIndex(col, "."); i >= 0 {
		return col[i+1:]
	}
	return col
}

type msPredicate struct {
	col    string
	op     string // "=", "&=", "&>", "in"
	argN   int
	inVals []driver.Value // for "in"
}

// parseWhere parses the WHERE subset the service emits:
//
//	col = $n            (optionally "t"."col")
//	col < $n            (retention age window on created_at)
//	col >= / <= $n      (OB-4 W1 IMAP SEARCH date/size/uid windows)
//	col ilike $n        (OB-4 W1 IMAP SEARCH header-substring fast path)
//	(flags & $n) = 0 | (flags & $n) > 0
//	id in ($1,$2,...)
func parseWhere(s *msState, table, where string, args []driver.NamedValue) ([]msPredicate, error) {
	var preds []msPredicate
	for _, part := range splitAnd(where) {
		part = strings.TrimSpace(part)
		switch {
		case strings.Contains(part, " in ("):
			col := stripQual(strings.TrimSpace(strings.TrimSuffix(part[:strings.Index(part, " in (")], " ")))
			inner := part[strings.Index(part, "(")+1 : strings.LastIndex(part, ")")]
			var vals []driver.Value
			for _, tok := range strings.Split(inner, ",") {
				tok = strings.TrimSpace(tok)
				if strings.HasPrefix(tok, "$") {
					n, _ := strconv.Atoi(tok[1:])
					vals = append(vals, msArg(args, n))
				} else {
					vals = append(vals, msVal(tok))
				}
			}
			preds = append(preds, msPredicate{col: col, op: "in", inVals: vals})
		case strings.Contains(part, " ilike "):
			// OB-4 W1: "subject ilike $n escape '\'" — case-insensitive
			// substring over a stored header column. The ESCAPE clause exists
			// so client-supplied %/_ are literals; the fake strips and
			// unescapes them like Postgres would.
			pieces := strings.SplitN(part, " ilike ", 2)
			lhs := strings.TrimSpace(pieces[0])
			rhs := strings.TrimSpace(pieces[1])
			if i := strings.Index(rhs, " escape "); i >= 0 {
				rhs = strings.TrimSpace(rhs[:i])
			}
			if !strings.HasPrefix(rhs, "$") {
				return nil, fmt.Errorf("fake: unsupported ilike rhs %q in %q", rhs, part)
			}
			preds = append(preds, msPredicate{col: stripQual(lhs), op: "ilike", argN: bindNum(rhs)})
		case strings.Contains(part, " >= "):
			pieces := strings.SplitN(part, " >= ", 2)
			col := stripQual(strings.TrimSpace(pieces[0]))
			rhs := strings.TrimSpace(pieces[1])
			if !strings.HasPrefix(rhs, "$") {
				return nil, fmt.Errorf("fake: unsupported >= rhs %q in %q", rhs, part)
			}
			preds = append(preds, msPredicate{col: col, op: ">=", argN: bindNum(rhs)})
		case strings.Contains(part, " <= "):
			// Must precede the "<" branch: "<=" contains "<".
			pieces := strings.SplitN(part, " <= ", 2)
			col := stripQual(strings.TrimSpace(pieces[0]))
			rhs := strings.TrimSpace(pieces[1])
			if !strings.HasPrefix(rhs, "$") {
				return nil, fmt.Errorf("fake: unsupported <= rhs %q in %q", rhs, part)
			}
			preds = append(preds, msPredicate{col: col, op: "<=", argN: bindNum(rhs)})
		case strings.Contains(part, " > ") && !strings.HasPrefix(part, "(flags & "):
			// OB-3 W2 (GAP-45 usage batching): "id > $n" keyset paging. Must
			// not swallow the "(flags & $n) > 0" predicate, which the next
			// case owns.
			pieces := strings.SplitN(part, " > ", 2)
			col := stripQual(strings.TrimSpace(pieces[0]))
			rhs := strings.TrimSpace(pieces[1])
			if !strings.HasPrefix(rhs, "$") {
				return nil, fmt.Errorf("fake: unsupported > rhs %q in %q", rhs, part)
			}
			preds = append(preds, msPredicate{col: col, op: ">", argN: bindNum(rhs)})
		case strings.HasPrefix(part, "(flags & "):
			// "(flags & $2) = 0" or "(flags & $2) > 0"
			inner := part[strings.Index(part, "$"):strings.Index(part, ")")]
			n, err := strconv.Atoi(strings.TrimPrefix(inner, "$"))
			if err != nil {
				return nil, fmt.Errorf("fake: bad flags predicate %q", part)
			}
			if strings.Contains(part, "= 0") {
				preds = append(preds, msPredicate{col: "flags", op: "&=", argN: n})
			} else if strings.Contains(part, "> 0") {
				preds = append(preds, msPredicate{col: "flags", op: "&>", argN: n})
			} else {
				return nil, fmt.Errorf("fake: unsupported flags predicate %q", part)
			}
		case strings.Contains(part, "<"):
			pieces := strings.SplitN(part, "<", 2)
			col := stripQual(strings.TrimSpace(pieces[0]))
			rhs := strings.TrimSpace(pieces[1])
			if !strings.HasPrefix(rhs, "$") {
				return nil, fmt.Errorf("fake: unsupported rhs %q in %q", rhs, part)
			}
			n, err := strconv.Atoi(strings.TrimPrefix(rhs, "$"))
			if err != nil {
				return nil, fmt.Errorf("fake: bad bindvar in %q", part)
			}
			preds = append(preds, msPredicate{col: col, op: "<", argN: n})
		case strings.Contains(part, "="):
			pieces := strings.SplitN(part, "=", 2)
			col := stripQual(strings.TrimSpace(pieces[0]))
			rhs := strings.TrimSpace(pieces[1])
			if !strings.HasPrefix(rhs, "$") {
				return nil, fmt.Errorf("fake: unsupported rhs %q in %q", rhs, part)
			}
			n, err := strconv.Atoi(strings.TrimPrefix(rhs, "$"))
			if err != nil {
				return nil, fmt.Errorf("fake: bad bindvar in %q", part)
			}
			preds = append(preds, msPredicate{col: col, op: "=", argN: n})
		default:
			return nil, fmt.Errorf("fake: unsupported predicate %q", part)
		}
	}
	return preds, nil
}

func splitAnd(where string) []string {
	return strings.Split(where, " and ")
}

func (p msPredicate) match(s *msState, table string, row []driver.Value, args []driver.NamedValue) bool {
	idx := s.colIndex(table, p.col)
	if idx < 0 {
		panic(fmt.Sprintf("fake: unknown column %q on %q", p.col, table))
	}
	cell := row[idx]
	switch p.op {
	case "=":
		return msEqual(cell, msArg(args, p.argN))
	case "<":
		return msCompare(cell, msArg(args, p.argN)) < 0
	case ">":
		return msCompare(cell, msArg(args, p.argN)) > 0
	case "&>":
		f, ok := cell.(int64)
		return ok && f&msArgInt(args, p.argN) > 0
	case "&=":
		f, ok := cell.(int64)
		return ok && f&msArgInt(args, p.argN) == 0
	case "in":
		for _, v := range p.inVals {
			if msEqual(cell, v) {
				return true
			}
		}
		return false
	case ">=":
		return msCompare(cell, msArg(args, p.argN)) >= 0
	case "<=":
		return msCompare(cell, msArg(args, p.argN)) <= 0
	case "ilike":
		sv, ok := cell.(string)
		av, ok2 := msArg(args, p.argN).(string)
		if !ok || !ok2 {
			return false
		}
		// Unescape the LIKE-escaped wildcards (likePattern's ESCAPE '\').
		pat := strings.NewReplacer(`\%`, "%", `\_`, "_", `\\`, `\`).Replace(av)
		pat = strings.TrimSuffix(strings.TrimPrefix(pat, "%"), "%")
		return strings.Contains(strings.ToLower(sv), strings.ToLower(pat))
	}
	return false
}

// msCompare orders two driver values of the same supported kind (int64,
// string, time.Time); mixed kinds compare equal (never matches an ordering
// predicate in practice).
func msCompare(a, b driver.Value) int {
	switch va := a.(type) {
	case int64:
		vb, ok := b.(int64)
		if !ok {
			return 0
		}
		switch {
		case va < vb:
			return -1
		case va > vb:
			return 1
		}
		return 0
	case string:
		vb, ok := b.(string)
		if !ok {
			return 0
		}
		switch {
		case va < vb:
			return -1
		case va > vb:
			return 1
		}
		return 0
	case time.Time:
		vb, ok := b.(time.Time)
		if !ok {
			return 0
		}
		switch {
		case va.Before(vb):
			return -1
		case va.After(vb):
			return 1
		}
		return 0
	}
	return 0
}

// inVals lives on msPredicate; helper for integer bindvars follows.

func msArgInt(args []driver.NamedValue, n int) int64 {
	v := msArg(args, n)
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	}
	panic(fmt.Sprintf("fake: non-int arg for $%d", n))
}

func msEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, oka := a.(int64)
	tb, okb := b.(int64)
	if oka && okb {
		return ta == tb
	}
	sa, oka := a.(string)
	sb, okb := b.(string)
	if oka && okb {
		return sa == sb
	}
	return a == b
}

type msConn struct{ s *msState }

func (c *msConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no prepare") }
func (c *msConn) Close() error                        { return nil }
func (c *msConn) Begin() (driver.Tx, error)           { return msTx{}, nil }

type msTx struct{}

func (msTx) Commit() error   { return nil }
func (msTx) Rollback() error { return nil }

type msResult struct{ rows int64 }

func (r msResult) LastInsertId() (int64, error) { return 0, nil }
func (r msResult) RowsAffected() (int64, error) { return r.rows, nil }

type msRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *msRows) Columns() []string { return r.cols }
func (r *msRows) Close() error      { return nil }
func (r *msRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type msConnector struct{ state *msState }

func (c msConnector) Connect(context.Context) (driver.Conn, error) { return &msConn{s: c.state}, nil }
func (c msConnector) Driver() driver.Driver                        { return msDrv{} }

type msDrv struct{}

func (msDrv) Open(string) (driver.Conn, error) { return nil, errors.New("fake: use sql.OpenDB") }

var msDriverOnce sync.Once

func msGorm(t *testing.T, state *msState) *gorm.DB {
	t.Helper()
	msDriverOnce.Do(func() {
		sql.Register("mailstore-fake", msDrv{})
	})
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(msConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open mailstore fake gorm: %v", err)
	}
	return gdb
}

// ─── QueryContext: SELECTs and INSERT ... RETURNING ───

func (c *msConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	lower := normalizeSQL(query)

	switch {
	// OB-3 W2: the reserve-phase ref upsert RETURNING ref_count (Raw().Scan)
	// — routed before the generic insert parser, which cannot read the
	// ON CONFLICT / RETURNING tail.
	case strings.HasPrefix(lower, "insert into mail_blob_refs") && strings.Contains(lower, "returning ref_count"):
		return c.queryBlobRefUpsert(lower, args)
	case strings.HasPrefix(lower, "insert into"):
		return c.queryInsert(lower, args)
	case strings.HasPrefix(lower, "update mail_folders"):
		return c.queryFolderClaim(lower, args)
	// OB-3 W2 (GAP-45): GC candidate listing — dead refs past the grace
	// cutoff; COALESCE(last_referenced, created_at) is beyond the generic
	// predicate parser, so it gets a dedicated handler. Routed before the
	// generic from-mail_blob_refs select.
	case strings.Contains(lower, "coalesce(last_referenced") && strings.Contains(lower, "from mail_blob_refs"):
		return c.queryBlobRefGCCandidates(args)
	// Retention sweep (quota wave): the blob-ref reclamation pass is a
	// DELETE ... RETURNING, so it arrives through QueryContext — route it
	// before the generic from-mail_blob_refs select.
	case strings.HasPrefix(lower, "delete from mail_blob_refs") && strings.Contains(lower, "ref_count <= 0"):
		return c.queryBlobRefGC()
	// Workspace aggregate ceiling (quota wave): SUM over member mailboxes.
	case strings.Contains(lower, "sum(used_bytes)"):
		return c.querySumUsedBytes(lower, args)
	// OB-4 W1: StatusCounts' single-round-trip aggregate — three counts, the
	// unseen/recent ones expressed with count(*) FILTER (WHERE ...). Routed
	// before the generic count(*) handler, which can only answer one column.
	case strings.Contains(lower, "filter (where") && strings.Contains(lower, "from mail_messages"):
		return c.queryStatusCounts(lower, args)
	case strings.Contains(lower, "count(*)"):
		return c.queryCount(lower, args)
	case strings.Contains(lower, "from workspaces"):
		return c.querySelect("workspaces", lower, args)
	case strings.Contains(lower, "from mail_folders"):
		return c.querySelect("mail_folders", lower, args)
	case strings.Contains(lower, "from mail_messages"):
		return c.querySelect("mail_messages", lower, args)
	case strings.Contains(lower, "from mailboxes"):
		return c.querySelect("mailboxes", lower, args)
	case strings.Contains(lower, "from mail_blob_refs"):
		return c.querySelect("mail_blob_refs", lower, args)
	}
	return nil, fmt.Errorf("fake: cannot route query: %s", query)
}

// querySumUsedBytes handles the quota enforcer's
// `select coalesce(sum(used_bytes), 0) from mailboxes where workspace_id = $1`.
func (c *msConn) querySumUsedBytes(lower string, args []driver.NamedValue) (driver.Rows, error) {
	table, where, err := c.parseSelectTail(lower)
	if err != nil {
		return nil, err
	}
	matched, err := c.match(table, where, args)
	if err != nil {
		return nil, err
	}
	idx := c.s.colIndex(table, "used_bytes")
	var total int64
	for _, row := range matched {
		if v, ok := row[idx].(int64); ok {
			total += v
		}
	}
	return &msRows{cols: []string{"sum"}, rows: [][]driver.Value{{total}}}, nil
}

// queryStatusCounts handles StatusCounts' single-round-trip aggregate:
//
//	select count(*) as total_count,
//	       count(*) filter (where (flags & $1) = 0) as unseen_count,
//	       count(*) filter (where (flags & $2) > 0) as recent_count
//	from mail_messages where folder_id = $3
func (c *msConn) queryStatusCounts(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	where := ""
	if i := strings.Index(lower, " where "); i >= 0 {
		where = strings.TrimSpace(lower[i+len(" where "):])
	}
	matched, err := c.match("mail_messages", where, args)
	if err != nil {
		return nil, err
	}
	idxFlags := s.colIndex("mail_messages", "flags")
	unseenMask := msArgInt(args, 1)
	recentMask := msArgInt(args, 2)
	var total, unseen, recent int64
	for _, row := range matched {
		total++
		if f, ok := row[idxFlags].(int64); ok {
			if f&unseenMask == 0 {
				unseen++
			}
			if f&recentMask > 0 {
				recent++
			}
		}
	}
	return &msRows{cols: []string{"total_count", "unseen_count", "recent_count"},
		rows: [][]driver.Value{{total, unseen, recent}}}, nil
}

// queryBlobRefGC handles the retention sweep's
// `delete from mail_blob_refs where ref_count <= 0 returning blob_hash`:
// reclaimed rows are removed and their hashes returned.
func (c *msConn) queryBlobRefGC() (driver.Rows, error) {
	s := c.s
	tbl := s.tables["mail_blob_refs"]
	idxHash := s.colIndex("mail_blob_refs", "blob_hash")
	idxRef := s.colIndex("mail_blob_refs", "ref_count")
	var reclaimed [][]driver.Value
	kept := tbl.rows[:0]
	for _, row := range tbl.rows {
		if rc, ok := row[idxRef].(int64); ok && rc <= 0 {
			reclaimed = append(reclaimed, []driver.Value{row[idxHash]})
			continue
		}
		kept = append(kept, row)
	}
	tbl.rows = kept
	return &msRows{cols: []string{"blob_hash"}, rows: reclaimed}, nil
}

// queryInsert handles GORM's `insert into "t" (...) values (...) returning "id"`.
func (c *msConn) queryInsert(lower string, args []driver.NamedValue) (driver.Rows, error) {
	table, cols, ok := parseInsertHead(lower)
	if !ok {
		return nil, fmt.Errorf("fake: cannot parse insert: %s", lower)
	}
	valsStart := strings.Index(lower, " values (")
	if valsStart < 0 {
		return nil, fmt.Errorf("fake: insert without values: %s", lower)
	}
	valsEnd := strings.Index(lower[valsStart:], ")")
	vals := strings.Split(lower[valsStart+len(" values ("):valsStart+valsEnd], ",")
	if len(vals) != len(cols) {
		return nil, fmt.Errorf("fake: insert arity mismatch: %s", lower)
	}
	s := c.s
	tbl := s.tables[table]
	ci := s.colIndex(table, "id")
	row := make([]driver.Value, len(msColumns[table]))
	for i, col := range cols {
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("fake: unknown insert column %q on %q", col, table)
		}
		tok := strings.TrimSpace(vals[i])
		var v driver.Value
		if strings.HasPrefix(tok, "$") {
			n, err := strconv.Atoi(tok[1:])
			if err != nil {
				return nil, fmt.Errorf("fake: bad bindvar %q in insert", tok)
			}
			v = msArg(args, n)
		} else if tok == "1" {
			v = int64(1)
		} else {
			return nil, fmt.Errorf("fake: unsupported literal %q in insert", tok)
		}
		row[idx] = v
	}
	s.seq++
	row[ci] = s.seq
	tbl.rows = append(tbl.rows, row)
	return &msRows{cols: []string{"id"}, rows: [][]driver.Value{{row[ci]}}}, nil
}

// parseInsertHead extracts the table name and column list.
func parseInsertHead(lower string) (table string, cols []string, ok bool) {
	rest := strings.TrimPrefix(lower, "insert into ")
	open := strings.Index(rest, "(")
	if open < 0 {
		return "", nil, false
	}
	table = strings.TrimSpace(strings.ReplaceAll(rest[:open], `"`, ""))
	closeIdx := strings.Index(rest[open:], ")")
	if closeIdx < 0 {
		return "", nil, false
	}
	for _, c := range strings.Split(rest[open+1:open+closeIdx], ",") {
		cols = append(cols, stripQual(strings.TrimSpace(c)))
	}
	return table, cols, true
}

// queryFolderClaim handles the atomic UID/mod-seq claims:
//
//	update mail_folders set uid_next = uid_next + 1, mod_seq_next = mod_seq_next + 1
//	  where id = $1 returning uid_next - 1 as claimed_uid, mod_seq_next - 1 as claimed_mod_seq
//	update mail_folders set mod_seq_next = mod_seq_next + 1
//	  where id = $1 returning mod_seq_next - 1 as claimed_mod_seq
func (c *msConn) queryFolderClaim(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	whereStart := strings.Index(lower, " where ")
	if whereStart < 0 {
		return nil, fmt.Errorf("fake: folder claim without where: %s", lower)
	}
	where := lower[whereStart+len(" where "):]
	eq := strings.Index(where, "$")
	if eq < 0 {
		return nil, fmt.Errorf("fake: folder claim where without bindvar: %s", lower)
	}
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	n, err := strconv.Atoi(strings.TrimPrefix(tok, "$"))
	if err != nil {
		return nil, fmt.Errorf("fake: bad bindvar in claim: %s", lower)
	}
	id := msArgInt(args, n)
	idxID := s.colIndex("mail_folders", "id")
	var victim []driver.Value
	for _, row := range s.tables["mail_folders"].rows {
		if row[idxID] == id {
			victim = row
			break
		}
	}
	if victim == nil {
		// No row matched: zero rows returned, so GORM's Scan sees no values.
		return &msRows{cols: []string{"claimed_uid", "claimed_mod_seq"}}, nil
	}
	withUID := strings.Contains(lower, "uid_next = uid_next + 1")
	if withUID {
		i := s.colIndex("mail_folders", "uid_next")
		victim[i] = victim[i].(int64) + 1
	}
	i := s.colIndex("mail_folders", "mod_seq_next")
	victim[i] = victim[i].(int64) + 1
	if withUID {
		uidNext := victim[s.colIndex("mail_folders", "uid_next")].(int64) - 1
		modSeqNext := victim[i].(int64) - 1
		return &msRows{cols: []string{"claimed_uid", "claimed_mod_seq"}, rows: [][]driver.Value{
			{uidNext, modSeqNext},
		}}, nil
	}
	return &msRows{cols: []string{"claimed_mod_seq"}, rows: [][]driver.Value{{victim[i].(int64) - 1}}}, nil
}

// queryCount handles `select count(*) from "t" where ...`.
func (c *msConn) queryCount(lower string, args []driver.NamedValue) (driver.Rows, error) {
	table, where, err := c.parseSelectTail(lower)
	if err != nil {
		return nil, err
	}
	matched, err := c.match(table, where, args)
	if err != nil {
		return nil, err
	}
	return &msRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
}

// parseSelectTail extracts the table and WHERE clause from a SELECT.
func (c *msConn) parseSelectTail(lower string) (table, where string, err error) {
	from := strings.Index(lower, " from ")
	if from < 0 {
		return "", "", fmt.Errorf("fake: select without from")
	}
	rest := lower[from+len(" from "):]
	end := len(rest)
	for _, kw := range []string{" where ", " order by ", " limit ", " offset ", " returning "} {
		if i := strings.Index(rest, kw); i >= 0 && i < end {
			end = i
		}
	}
	table = strings.TrimSpace(strings.ReplaceAll(rest[:end], `"`, ""))
	where = ""
	if i := strings.Index(rest, " where "); i >= 0 {
		where = rest[i+len(" where "):]
		// Trim trailing clauses from the WHERE string.
		for _, kw := range []string{" order by ", " limit ", " offset ", " returning "} {
			if j := strings.Index(where, kw); j >= 0 {
				where = where[:j]
			}
		}
	}
	return table, strings.TrimSpace(where), nil
}

// match applies WHERE predicates to a table.
func (c *msConn) match(table, where string, args []driver.NamedValue) ([][]driver.Value, error) {
	if where == "" {
		return append([][]driver.Value(nil), c.s.tables[table].rows...), nil
	}
	preds, err := parseWhere(c.s, table, where, args)
	if err != nil {
		return nil, err
	}
	var out [][]driver.Value
	for _, row := range c.s.tables[table].rows {
		ok := true
		for _, p := range preds {
			if !p.match(c.s, table, row, args) {
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

// querySelect handles SELECT */columns FROM t [WHERE ...] [ORDER BY c] [LIMIT n [OFFSET m]].
func (c *msConn) querySelect(table, lower string, args []driver.NamedValue) (driver.Rows, error) {
	table, where, err := c.parseSelectTail(lower)
	if err != nil {
		return nil, err
	}
	matched, err := c.match(table, where, args)
	if err != nil {
		return nil, err
	}
	// ORDER BY one or more columns (uid / id / imap_path / created_at), each
	// optionally DESC. Multi-key ordering (created_at ASC, uid ASC) is used by
	// the retention sweep's count purge.
	if i := strings.Index(lower, " order by "); i >= 0 {
		rest := lower[i+len(" order by "):]
		// Drop trailing clauses so they are not parsed as order keys.
		for _, kw := range []string{" limit ", " offset ", " for update", " returning "} {
			if j := strings.Index(rest, kw); j >= 0 {
				rest = rest[:j]
			}
		}
		type orderKey struct {
			idx  int
			desc bool
		}
		var keys []orderKey
		for _, part := range strings.Split(rest, ",") {
			fields := strings.Fields(strings.TrimSpace(part))
			if len(fields) == 0 {
				continue
			}
			col := stripQual(fields[0])
			idx := c.s.colIndex(table, col)
			if idx < 0 {
				return nil, fmt.Errorf("fake: unknown order column %q on %q", col, table)
			}
			desc := len(fields) > 1 && strings.EqualFold(fields[len(fields)-1], "desc")
			keys = append(keys, orderKey{idx: idx, desc: desc})
		}
		sort.SliceStable(matched, func(a, b int) bool {
			for _, k := range keys {
				cmp := msCompare(matched[a][k.idx], matched[b][k.idx])
				if cmp == 0 {
					continue
				}
				if k.desc {
					return cmp > 0
				}
				return cmp < 0
			}
			return false
		})
	}
	// LIMIT/OFFSET via bindvars.
	offset := 0
	if i := strings.Index(lower, " offset "); i >= 0 {
		tok := strings.Fields(lower[i+len(" offset "):])[0]
		if n, err := strconv.Atoi(strings.TrimPrefix(tok, "$")); err == nil {
			offset = int(msArgInt(args, n))
		}
	}
	if i := strings.Index(lower, " limit "); i >= 0 {
		tok := strings.Fields(lower[i+len(" limit "):])[0]
		limit := -1
		if strings.HasPrefix(tok, "$") {
			n, err := strconv.Atoi(tok[1:])
			if err != nil {
				return nil, fmt.Errorf("fake: bad limit bindvar: %s", lower)
			}
			limit = int(msArgInt(args, n))
		} else {
			limit, _ = strconv.Atoi(tok)
		}
		if limit >= 0 {
			if offset > len(matched) {
				matched = nil
			} else {
				matched = matched[offset:]
				if limit < len(matched) {
					matched = matched[:limit]
				}
			}
		}
	}
	// Which columns to return: Pluck emits a single column; otherwise *.
	// OB-4 W1: multi-column select lists (the fetch/search position scan
	// selects id + uid) split on commas like Postgres would.
	cols := msColumns[table]
	if strings.HasPrefix(lower, "select ") {
		sel := lower[len("select "):strings.Index(lower, " from ")]
		sel = strings.TrimSpace(strings.ReplaceAll(sel, `"`, ""))
		if sel != "*" && !strings.HasPrefix(sel, "count(") {
			cols = nil
			for _, c := range strings.Split(sel, ",") {
				cols = append(cols, stripQual(strings.TrimSpace(c)))
			}
		}
	}
	rows := make([][]driver.Value, 0, len(matched))
	for _, r := range matched {
		row := make([]driver.Value, len(cols))
		for i, col := range cols {
			row[i] = r[c.s.colIndex(table, col)]
		}
		rows = append(rows, row)
	}
	return &msRows{cols: cols, rows: rows}, nil
}

// ─── ExecContext: UPDATEs, DELETEs, and the raw blob-ref upsert/decrements ───

func (c *msConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	lower := normalizeSQL(query)

	switch {
	// OB-3 W2 (GAP-45): the per-hash advisory lock that serializes blob
	// reservation (append) against reclamation (GC). A no-op here — the fake
	// serializes every statement under its mutex, which is a strictly stronger
	// ordering than the lock provides on real Postgres.
	case strings.HasPrefix(lower, "select pg_advisory_xact_lock"):
		return msResult{rows: 0}, nil
	// OB-3 W2 (GAP-45): the GC's per-hash re-checked delete of a dead ref row
	// (runs inside the advisory-lock transaction; a failed blob delete rolls
	// this back so the candidate is retried by the next sweep).
	case strings.HasPrefix(lower, "delete from mail_blob_refs") && strings.Contains(lower, "ref_count <= 0"):
		return c.execBlobRefGCDelete(lower, args)
	case strings.HasPrefix(lower, "update mailboxes") && strings.Contains(lower, "used_bytes = used_bytes"):
		return c.execCounterBump(lower, args)
	case strings.HasPrefix(lower, "update mail_blob_refs"):
		return c.execRefDecrement(lower, args)
	case strings.HasPrefix(lower, "update "):
		return c.execUpdate(lower, args)
	case strings.HasPrefix(lower, "delete from "):
		return c.execDelete(lower, args)
	}
	return nil, fmt.Errorf("fake: unsupported exec: %s", query)
}

// execBlobRefUpsert was the ExecContext form of the ref upsert; OB-3 W2 moved
// the statement to RETURNING ref_count via QueryContext (queryBlobRefUpsert).

// queryBlobRefUpsert handles the reserve-phase upsert:
//
//	insert into mail_blob_refs (blob_hash, size, ref_count, created_at, last_referenced)
//	  values ($1,$2,1,$3,$4) on conflict (blob_hash) do update
//	  set ref_count = mail_blob_refs.ref_count + 1, last_referenced = $5
//	  returning ref_count
//
// It stamps last_referenced on both paths and returns the post-increment
// ref count (1 = the caller now holds the only reference).
func (c *msConn) queryBlobRefUpsert(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	valsStart := strings.Index(lower, " values (")
	inner := lower[valsStart+len(" values ("):]
	inner = inner[:strings.Index(inner, ")")]
	toks := strings.Split(inner, ",")
	hash := msArg(args, bindNum(toks[0]))
	size := msArg(args, bindNum(toks[1]))
	createdAt := msArg(args, bindNum(toks[3]))
	lastRefInsert := msArg(args, bindNum(toks[4]))
	lastRefUpdate := msArg(args, 5)
	idxHash := s.colIndex("mail_blob_refs", "blob_hash")
	idxSize := s.colIndex("mail_blob_refs", "size")
	idxRef := s.colIndex("mail_blob_refs", "ref_count")
	idxLast := s.colIndex("mail_blob_refs", "last_referenced")
	idxCreated := s.colIndex("mail_blob_refs", "created_at")
	for _, row := range s.tables["mail_blob_refs"].rows {
		if row[idxHash] == hash {
			row[idxRef] = row[idxRef].(int64) + 1
			row[idxLast] = lastRefUpdate
			return &msRows{cols: []string{"ref_count"}, rows: [][]driver.Value{{row[idxRef]}}}, nil
		}
	}
	s.seq++
	row := make([]driver.Value, len(msColumns["mail_blob_refs"]))
	row[s.colIndex("mail_blob_refs", "id")] = s.seq
	row[idxHash] = hash
	row[idxSize] = size
	row[idxRef] = int64(1)
	row[idxLast] = lastRefInsert
	row[idxCreated] = createdAt
	s.tables["mail_blob_refs"].rows = append(s.tables["mail_blob_refs"].rows, row)
	return &msRows{cols: []string{"ref_count"}, rows: [][]driver.Value{{int64(1)}}}, nil
}

// queryBlobRefGCCandidates handles the GC's candidate listing:
//
//	select blob_hash, size from mail_blob_refs
//	  where ref_count <= 0 and coalesce(last_referenced, created_at) < $1
//
// Only rows unreferenced past the grace cutoff come back.
func (c *msConn) queryBlobRefGCCandidates(args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	cutoff := msArg(args, 1)
	tbl := s.tables["mail_blob_refs"]
	idxHash := s.colIndex("mail_blob_refs", "blob_hash")
	idxSize := s.colIndex("mail_blob_refs", "size")
	idxRef := s.colIndex("mail_blob_refs", "ref_count")
	idxLast := s.colIndex("mail_blob_refs", "last_referenced")
	idxCreated := s.colIndex("mail_blob_refs", "created_at")
	var out [][]driver.Value
	for _, row := range tbl.rows {
		if rc, ok := row[idxRef].(int64); !ok || rc > 0 {
			continue
		}
		anchor := row[idxLast]
		if anchor == nil {
			anchor = row[idxCreated]
		}
		if !msBefore(anchor, cutoff) {
			continue
		}
		out = append(out, []driver.Value{row[idxHash], row[idxSize]})
	}
	return &msRows{cols: []string{"blob_hash", "size"}, rows: out}, nil
}

// msBefore compares two driver time values for the grace cutoff.
func msBefore(v, cutoff driver.Value) bool {
	t, ok1 := v.(time.Time)
	c, ok2 := cutoff.(time.Time)
	if !ok1 || !ok2 {
		return false
	}
	return t.Before(c)
}

func bindNum(tok string) int {
	tok = strings.TrimSpace(tok)
	if !strings.HasPrefix(tok, "$") {
		panic(fmt.Sprintf("fake: expected bindvar, got %q", tok))
	}
	n, err := strconv.Atoi(tok[1:])
	if err != nil {
		panic(fmt.Sprintf("fake: bad bindvar %q", tok))
	}
	return n
}

// execCounterBump handles `update mailboxes set used_bytes = used_bytes + $1,
// message_count = message_count + $2 where id = $3`.
func (c *msConn) execCounterBump(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	where := lower[strings.Index(lower, " where ")+len(" where "):]
	eq := strings.Index(where, "$")
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	id := msArgInt(args, bindNum(tok))
	idxID := s.colIndex("mailboxes", "id")
	idxUsed := s.colIndex("mailboxes", "used_bytes")
	idxCount := s.colIndex("mailboxes", "message_count")
	for _, row := range s.tables["mailboxes"].rows {
		if row[idxID] == id {
			row[idxUsed] = row[idxUsed].(int64) + msArgInt(args, 1)
			row[idxCount] = row[idxCount].(int64) + msArgInt(args, 2)
			return msResult{rows: 1}, nil
		}
	}
	return msResult{rows: 0}, nil
}

// execRefDecrement handles `update mail_blob_refs set ref_count = ref_count - 1,
// last_referenced = $1 where blob_hash = $2` (OB-3 W2: the release stamps the
// GC grace anchor).
func (c *msConn) execRefDecrement(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	where := lower[strings.Index(lower, " where ")+len(" where "):]
	eq := strings.Index(where, "$")
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	hash := msArg(args, bindNum(tok))
	idxHash := s.colIndex("mail_blob_refs", "blob_hash")
	idxRef := s.colIndex("mail_blob_refs", "ref_count")
	idxLast := s.colIndex("mail_blob_refs", "last_referenced")
	now := msArg(args, 1)
	for _, row := range s.tables["mail_blob_refs"].rows {
		if row[idxHash] == hash {
			row[idxRef] = row[idxRef].(int64) - 1
			row[idxLast] = now
			return msResult{rows: 1}, nil
		}
	}
	return msResult{rows: 0}, nil
}

// execBlobRefGCDelete handles the GC's per-hash re-checked delete:
// `delete from mail_blob_refs where blob_hash = $1 and ref_count <= 0`.
// It removes the row only while it is still dead (ref_count <= 0); the caller
// rolls its transaction back when the subsequent blob delete fails.
func (c *msConn) execBlobRefGCDelete(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	where := lower[strings.Index(lower, " where ")+len(" where "):]
	eq := strings.Index(where, "$")
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	hash := msArg(args, bindNum(tok))
	idxHash := s.colIndex("mail_blob_refs", "blob_hash")
	idxRef := s.colIndex("mail_blob_refs", "ref_count")
	removed := int64(0)
	kept := s.tables["mail_blob_refs"].rows[:0]
	for _, row := range s.tables["mail_blob_refs"].rows {
		if row[idxHash] == hash {
			if rc, ok := row[idxRef].(int64); ok && rc <= 0 {
				removed++
				continue
			}
		}
		kept = append(kept, row)
	}
	s.tables["mail_blob_refs"].rows = kept
	return msResult{rows: removed}, nil
}

// execUpdate handles `update "t" set "c"=$n[, ...] where <predicates>`.
func (c *msConn) execUpdate(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	table := stripQual(strings.TrimSpace(lower[len("update "):strings.Index(lower, " set ")]))
	setStart := strings.Index(lower, " set ") + len(" set ")
	whereStart := strings.Index(lower, " where ")
	if whereStart < 0 {
		return nil, fmt.Errorf("fake: update without where: %s", lower)
	}
	var setters []struct {
		idx int
		val driver.Value
	}
	for _, pair := range strings.Split(lower[setStart:whereStart], ",") {
		pieces := strings.SplitN(pair, "=", 2)
		if len(pieces) != 2 {
			return nil, fmt.Errorf("fake: malformed SET pair %q", pair)
		}
		col := stripQual(strings.TrimSpace(pieces[0]))
		rhs := strings.TrimSpace(pieces[1])
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("fake: unknown update column %q on %q", col, table)
		}
		if !strings.HasPrefix(rhs, "$") {
			return nil, fmt.Errorf("fake: unsupported SET rhs %q", rhs)
		}
		setters = append(setters, struct {
			idx int
			val driver.Value
		}{idx, msArg(args, bindNum(rhs))})
	}
	preds, err := parseWhere(s, table, strings.TrimSpace(lower[whereStart+len(" where "):]), args)
	if err != nil {
		return nil, err
	}
	matched := 0
	for _, row := range s.tables[table].rows {
		ok := true
		for _, p := range preds {
			if !p.match(s, table, row, args) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		matched++
		for _, st := range setters {
			row[st.idx] = st.val
		}
	}
	return msResult{rows: int64(matched)}, nil
}

// execDelete handles `delete from "t" where <predicates>`.
func (c *msConn) execDelete(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	table := stripQual(strings.TrimSpace(lower[len("delete from "):strings.Index(lower, " where ")]))
	preds, err := parseWhere(s, table, strings.TrimSpace(lower[strings.Index(lower, " where ")+len(" where "):]), args)
	if err != nil {
		return nil, err
	}
	tbl := s.tables[table]
	kept := tbl.rows[:0]
	removed := int64(0)
	for _, row := range tbl.rows {
		ok := true
		for _, p := range preds {
			if !p.match(s, table, row, args) {
				ok = false
				break
			}
		}
		if ok {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	tbl.rows = kept
	return msResult{rows: removed}, nil
}

// ─── Test fixtures ───

// fakeBlobStore is an in-memory blob.Store keyed exactly as the service keys
// objects (BlobObjectKey).
type fakeBlobStore struct {
	mu    sync.Mutex
	items map[string][]byte
}

func newFakeBlobStore() *fakeBlobStore { return &fakeBlobStore{items: map[string][]byte{}} }

func (f *fakeBlobStore) Put(_ context.Context, key string, r io.Reader, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[key] = b
	return nil
}

func (f *fakeBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.items[key]
	if !ok {
		return nil, fmt.Errorf("fake store: no object %q", key)
	}
	return io.NopCloser(strings.NewReader(string(b))), nil
}

func (f *fakeBlobStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, key)
	return nil
}

func (f *fakeBlobStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.items[key]
	return ok
}

// newFakeService builds a Service over the fake driver with the pass-through
// codec and a real in-memory blob store.
func newFakeService(t *testing.T) (*Service, *msState, *fakeBlobStore) {
	t.Helper()
	state := newMSState()
	db := msGorm(t, state)
	st := newFakeBlobStore()
	svc, err := New(db, IdentityBlobCodec{}, st, nil)
	if err != nil {
		t.Fatalf("new mailstore service: %v", err)
	}
	return svc, state, st
}

// seedUserMailbox creates a user + active mailbox and returns the mailbox.
func seedUserMailbox(t *testing.T, svc *Service, userID uint, name string, quota int64) *models.Mailbox {
	t.Helper()
	mb, err := svc.CreateMailbox(userID, nil, name, quota)
	if err != nil {
		t.Fatalf("create mailbox %s: %v", name, err)
	}
	return mb
}

// seedWorkspaceRow seeds the fake workspaces table with the columns the
// quota enforcer reads (the quota wave's workspace aggregate ceiling).
func seedWorkspaceRow(t *testing.T, state *msState, id uint, mailQuotaBytes int64) {
	t.Helper()
	row := make([]driver.Value, len(msColumns["workspaces"]))
	row[state.colIndex("workspaces", "id")] = int64(id)
	row[state.colIndex("workspaces", "mail_quota_bytes")] = mailQuotaBytes
	state.tables["workspaces"].rows = append(state.tables["workspaces"].rows, row)
}

// inboxFolder returns the mailbox's INBOX folder.
func inboxFolder(t *testing.T, svc *Service, mb *models.Mailbox) *models.MailFolder {
	t.Helper()
	f, err := svc.GetFolderByPath(mb.ID, mb.UserID, 0, "INBOX")
	if err != nil {
		t.Fatalf("get INBOX of mailbox %d: %v", mb.ID, err)
	}
	return f
}

// simpleRaw builds a minimal RFC 5322 message with a unique subject.
func simpleRaw(subject, from, to string, body string) []byte {
	return []byte("Message-ID: <" + subject + "@test>\r\n" +
		"From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Mon, 02 Nov 2026 10:00:00 +0000\r\n" +
		"\r\n" +
		body + "\r\n")
}
