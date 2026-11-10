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

package handlers

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
	"github.com/goposta/posta/internal/services/mailstore"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The mail-handler test fake driver follows the repo's established in-memory
// pattern (internal/cron/jobs/queue_reconcile_test.go →
// internal/services/mailstore/fakedb_test.go → this file): a database/sql
// driver over in-memory tables wrapped in gorm.io/driver/postgres,
// implementing exactly the statement surface the mailstore service issues for
// the W1 REST surface. Transactions are no-ops (mutex-serialized autocommit);
// real transactional semantics stay covered by the mailstore package's
// real-Postgres suite. The blob codec is the mailstore pass-through
// IdentityBlobCodec (the fake codec) — content-addressed, no crypto.

var mhColumns = map[string][]string{
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
	"workspaces":     {"id", "mail_quota_bytes"},
}

type mhTable struct {
	rows [][]driver.Value
}

type mhState struct {
	mu     sync.Mutex
	tables map[string]*mhTable
	seq    int64
}

func newMHState() *mhState {
	t := make(map[string]*mhTable, len(mhColumns))
	for name := range mhColumns {
		t[name] = &mhTable{}
	}
	return &mhState{tables: t, seq: 5000}
}

func (s *mhState) colIndex(table, col string) int {
	for i, c := range mhColumns[table] {
		if c == col {
			return i
		}
	}
	return -1
}

func mhVal(v any) driver.Value {
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
		panic(fmt.Sprintf("mhVal: unsupported type %T", v))
	}
}

func mhArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return mhVal(a.Value)
		}
	}
	panic(fmt.Sprintf("mhState: missing bind var $%d", n))
}

func mhArgInt(args []driver.NamedValue, n int) int64 {
	v := mhArg(args, n)
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	}
	panic(fmt.Sprintf("mh fake: non-int arg for $%d", n))
}

func mhNormalizeSQL(q string) string {
	q = strings.ReplaceAll(q, `"`, "")
	return strings.Join(strings.Fields(strings.ToLower(q)), " ")
}

func mhStripQual(col string) string {
	col = strings.ReplaceAll(col, `"`, "")
	if i := strings.LastIndex(col, "."); i >= 0 {
		return col[i+1:]
	}
	return col
}

type mhPredicate struct {
	col    string
	op     string
	argN   int
	inVals []driver.Value
}

// mhParseWhere parses the WHERE subset the mailstore service emits for the
// W1 REST surface (same subset as the mailstore package's fake, see
// fakedb_test.go parseWhere).
func mhParseWhere(s *mhState, where string, args []driver.NamedValue) ([]mhPredicate, error) {
	var preds []mhPredicate
	for _, part := range strings.Split(where, " and ") {
		part = strings.TrimSpace(part)
		switch {
		case strings.Contains(part, " in ("):
			col := mhStripQual(strings.TrimSpace(strings.TrimSuffix(part[:strings.Index(part, " in (")], " ")))
			inner := part[strings.Index(part, "(")+1 : strings.LastIndex(part, ")")]
			var vals []driver.Value
			for _, tok := range strings.Split(inner, ",") {
				tok = strings.TrimSpace(tok)
				if strings.HasPrefix(tok, "$") {
					n, _ := strconv.Atoi(tok[1:])
					vals = append(vals, mhArg(args, n))
				} else {
					vals = append(vals, mhVal(tok))
				}
			}
			preds = append(preds, mhPredicate{col: col, op: "in", inVals: vals})
		case strings.HasPrefix(part, "(flags & "):
			inner := part[strings.Index(part, "$"):strings.Index(part, ")")]
			n, err := strconv.Atoi(strings.TrimPrefix(inner, "$"))
			if err != nil {
				return nil, fmt.Errorf("mh fake: bad flags predicate %q", part)
			}
			if strings.Contains(part, "= 0") {
				preds = append(preds, mhPredicate{col: "flags", op: "&=", argN: n})
			} else if strings.Contains(part, "> 0") {
				preds = append(preds, mhPredicate{col: "flags", op: "&>", argN: n})
			} else {
				return nil, fmt.Errorf("mh fake: unsupported flags predicate %q", part)
			}
		case strings.Contains(part, " >= "):
			pieces := strings.SplitN(part, " >= ", 2)
			preds = append(preds, mhPredicate{col: mhStripQual(strings.TrimSpace(pieces[0])), op: ">=", argN: mhBindNum(pieces[1])})
		case strings.Contains(part, " <= "):
			pieces := strings.SplitN(part, " <= ", 2)
			preds = append(preds, mhPredicate{col: mhStripQual(strings.TrimSpace(pieces[0])), op: "<=", argN: mhBindNum(pieces[1])})
		case strings.Contains(part, " > ") && !strings.HasPrefix(part, "(flags & "):
			pieces := strings.SplitN(part, " > ", 2)
			preds = append(preds, mhPredicate{col: mhStripQual(strings.TrimSpace(pieces[0])), op: ">", argN: mhBindNum(pieces[1])})
		case strings.Contains(part, "<"):
			pieces := strings.SplitN(part, "<", 2)
			preds = append(preds, mhPredicate{col: mhStripQual(strings.TrimSpace(pieces[0])), op: "<", argN: mhBindNum(pieces[1])})
		case strings.Contains(part, "="):
			pieces := strings.SplitN(part, "=", 2)
			preds = append(preds, mhPredicate{col: mhStripQual(strings.TrimSpace(pieces[0])), op: "=", argN: mhBindNum(pieces[1])})
		default:
			return nil, fmt.Errorf("mh fake: unsupported predicate %q", part)
		}
	}
	return preds, nil
}

func mhBindNum(rhs string) int {
	rhs = strings.TrimSpace(rhs)
	if !strings.HasPrefix(rhs, "$") {
		panic(fmt.Sprintf("mh fake: expected bindvar, got %q", rhs))
	}
	n, err := strconv.Atoi(rhs[1:])
	if err != nil {
		panic(fmt.Sprintf("mh fake: bad bindvar %q", rhs))
	}
	return n
}

func (p mhPredicate) match(s *mhState, table string, row []driver.Value, args []driver.NamedValue) bool {
	idx := s.colIndex(table, p.col)
	cell := row[idx]
	switch p.op {
	case "=":
		return mhEqual(cell, mhArg(args, p.argN))
	case "<":
		return mhCompare(cell, mhArg(args, p.argN)) < 0
	case ">":
		return mhCompare(cell, mhArg(args, p.argN)) > 0
	case "&>":
		f, ok := cell.(int64)
		return ok && f&mhArgInt(args, p.argN) > 0
	case "&=":
		f, ok := cell.(int64)
		return ok && f&mhArgInt(args, p.argN) == 0
	case "in":
		for _, v := range p.inVals {
			if mhEqual(cell, v) {
				return true
			}
		}
		return false
	case ">=":
		return mhCompare(cell, mhArg(args, p.argN)) >= 0
	case "<=":
		return mhCompare(cell, mhArg(args, p.argN)) <= 0
	}
	return false
}

func mhCompare(a, b driver.Value) int {
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

func mhEqual(a, b driver.Value) bool {
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

type mhConn struct{ s *mhState }

func (c *mhConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("mh fake: no prepare") }
func (c *mhConn) Close() error                        { return nil }
func (c *mhConn) Begin() (driver.Tx, error)           { return mhTx{}, nil }

type mhTx struct{}

func (mhTx) Commit() error   { return nil }
func (mhTx) Rollback() error { return nil }

type mhResult struct{ rows int64 }

func (r mhResult) LastInsertId() (int64, error) { return 0, nil }
func (r mhResult) RowsAffected() (int64, error) { return r.rows, nil }

type mhRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *mhRows) Columns() []string { return r.cols }
func (r *mhRows) Close() error      { return nil }
func (r *mhRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type mhConnector struct{ state *mhState }

func (c mhConnector) Connect(context.Context) (driver.Conn, error) { return &mhConn{s: c.state}, nil }
func (c mhConnector) Driver() driver.Driver                        { return mhDrv{} }

type mhDrv struct{}

func (mhDrv) Open(string) (driver.Conn, error) { return nil, errors.New("mh fake: use sql.OpenDB") }

var mhDriverOnce sync.Once

func mhGorm(t *testing.T, state *mhState) *gorm.DB {
	t.Helper()
	mhDriverOnce.Do(func() {
		sql.Register("mail-handler-fake", mhDrv{})
	})
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(mhConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open mail handler fake gorm: %v", err)
	}
	return gdb
}

// ─── QueryContext ────────────────────────────────────────────────────────────

func (c *mhConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	lower := mhNormalizeSQL(query)

	switch {
	case strings.HasPrefix(lower, "insert into mail_blob_refs") && strings.Contains(lower, "returning ref_count"):
		return c.queryBlobRefUpsert(lower, args)
	case strings.HasPrefix(lower, "insert into"):
		return c.queryInsert(lower, args)
	case strings.HasPrefix(lower, "update mail_folders"):
		return c.queryFolderClaim(lower, args)
	case strings.Contains(lower, "coalesce(last_referenced") && strings.Contains(lower, "from mail_blob_refs"):
		return c.queryBlobRefGCCandidates(args)
	case strings.Contains(lower, "delete from mail_blob_refs") && strings.Contains(lower, "ref_count <= 0"):
		return c.queryBlobRefGC()
	case strings.Contains(lower, "sum(used_bytes)"):
		return c.querySumUsedBytes(lower, args)
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
	return nil, fmt.Errorf("mh fake: cannot route query: %s", query)
}

func (c *mhConn) querySumUsedBytes(lower string, args []driver.NamedValue) (driver.Rows, error) {
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
	return &mhRows{cols: []string{"sum"}, rows: [][]driver.Value{{total}}}, nil
}

func (c *mhConn) queryStatusCounts(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	_, where, err := c.parseSelectTail(lower)
	if err != nil {
		return nil, err
	}
	matched, err := c.match("mail_messages", where, args)
	if err != nil {
		return nil, err
	}
	idxFlags := s.colIndex("mail_messages", "flags")
	unseenMask := mhArgInt(args, 1)
	recentMask := mhArgInt(args, 2)
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
	return &mhRows{cols: []string{"total_count", "unseen_count", "recent_count"},
		rows: [][]driver.Value{{total, unseen, recent}}}, nil
}

func (c *mhConn) queryBlobRefGC() (driver.Rows, error) {
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
	return &mhRows{cols: []string{"blob_hash"}, rows: reclaimed}, nil
}

func (c *mhConn) queryInsert(lower string, args []driver.NamedValue) (driver.Rows, error) {
	table, cols, ok := mhParseInsertHead(lower)
	if !ok {
		return nil, fmt.Errorf("mh fake: cannot parse insert: %s", lower)
	}
	valsStart := strings.Index(lower, " values (")
	if valsStart < 0 {
		return nil, fmt.Errorf("mh fake: insert without values: %s", lower)
	}
	valsEnd := strings.Index(lower[valsStart:], ")")
	vals := strings.Split(lower[valsStart+len(" values ("):valsStart+valsEnd], ",")
	if len(vals) != len(cols) {
		return nil, fmt.Errorf("mh fake: insert arity mismatch: %s", lower)
	}
	s := c.s
	tbl := s.tables[table]
	ci := s.colIndex(table, "id")
	row := make([]driver.Value, len(mhColumns[table]))
	for i, col := range cols {
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("mh fake: unknown insert column %q on %q", col, table)
		}
		tok := strings.TrimSpace(vals[i])
		var v driver.Value
		if strings.HasPrefix(tok, "$") {
			n, err := strconv.Atoi(tok[1:])
			if err != nil {
				return nil, fmt.Errorf("mh fake: bad bindvar %q in insert", tok)
			}
			v = mhArg(args, n)
		} else if tok == "1" {
			v = int64(1)
		} else {
			return nil, fmt.Errorf("mh fake: unsupported literal %q in insert", tok)
		}
		row[idx] = v
	}
	s.seq++
	row[ci] = s.seq
	tbl.rows = append(tbl.rows, row)
	return &mhRows{cols: []string{"id"}, rows: [][]driver.Value{{row[ci]}}}, nil
}

func mhParseInsertHead(lower string) (table string, cols []string, ok bool) {
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
		cols = append(cols, mhStripQual(strings.TrimSpace(c)))
	}
	return table, cols, true
}

func (c *mhConn) queryFolderClaim(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	whereStart := strings.Index(lower, " where ")
	if whereStart < 0 {
		return nil, fmt.Errorf("mh fake: folder claim without where: %s", lower)
	}
	where := lower[whereStart+len(" where "):]
	eq := strings.Index(where, "$")
	if eq < 0 {
		return nil, fmt.Errorf("mh fake: folder claim where without bindvar: %s", lower)
	}
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	id := mhArgInt(args, mhBindNum(tok))
	idxID := s.colIndex("mail_folders", "id")
	var victim []driver.Value
	for _, row := range s.tables["mail_folders"].rows {
		if row[idxID] == id {
			victim = row
			break
		}
	}
	if victim == nil {
		return &mhRows{cols: []string{"claimed_uid", "claimed_mod_seq"}}, nil
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
		return &mhRows{cols: []string{"claimed_uid", "claimed_mod_seq"}, rows: [][]driver.Value{
			{uidNext, modSeqNext},
		}}, nil
	}
	return &mhRows{cols: []string{"claimed_mod_seq"}, rows: [][]driver.Value{{victim[i].(int64) - 1}}}, nil
}

func (c *mhConn) queryCount(lower string, args []driver.NamedValue) (driver.Rows, error) {
	table, where, err := c.parseSelectTail(lower)
	if err != nil {
		return nil, err
	}
	matched, err := c.match(table, where, args)
	if err != nil {
		return nil, err
	}
	return &mhRows{cols: []string{"count"}, rows: [][]driver.Value{{int64(len(matched))}}}, nil
}

func (c *mhConn) parseSelectTail(lower string) (table, where string, err error) {
	from := strings.Index(lower, " from ")
	if from < 0 {
		return "", "", fmt.Errorf("mh fake: select without from")
	}
	rest := lower[from+len(" from "):]
	end := len(rest)
	for _, kw := range []string{" where ", " order by ", " limit ", " offset ", " returning ", " for update"} {
		if i := strings.Index(rest, kw); i >= 0 && i < end {
			end = i
		}
	}
	table = strings.TrimSpace(strings.ReplaceAll(rest[:end], `"`, ""))
	where = ""
	if i := strings.Index(rest, " where "); i >= 0 {
		where = rest[i+len(" where "):]
		for _, kw := range []string{" order by ", " limit ", " offset ", " returning ", " for update"} {
			if j := strings.Index(where, kw); j >= 0 {
				where = where[:j]
			}
		}
	}
	return table, strings.TrimSpace(where), nil
}

func (c *mhConn) match(table, where string, args []driver.NamedValue) ([][]driver.Value, error) {
	if where == "" {
		return c.matchAll(table)
	}
	preds, err := mhParseWhere(c.s, where, args)
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

func (c *mhConn) matchAll(table string) ([][]driver.Value, error) {
	return append([][]driver.Value(nil), c.s.tables[table].rows...), nil
}

func (c *mhConn) querySelect(table, lower string, args []driver.NamedValue) (driver.Rows, error) {
	table, where, err := c.parseSelectTail(lower)
	if err != nil {
		return nil, err
	}
	matched, err := c.match(table, where, args)
	if err != nil {
		return nil, err
	}
	if i := strings.Index(lower, " order by "); i >= 0 {
		rest := lower[i+len(" order by "):]
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
			col := mhStripQual(fields[0])
			idx := c.s.colIndex(table, col)
			if idx < 0 {
				return nil, fmt.Errorf("mh fake: unknown order column %q on %q", col, table)
			}
			desc := len(fields) > 1 && strings.EqualFold(fields[len(fields)-1], "desc")
			keys = append(keys, orderKey{idx: idx, desc: desc})
		}
		sort.SliceStable(matched, func(a, b int) bool {
			for _, k := range keys {
				cmp := mhCompare(matched[a][k.idx], matched[b][k.idx])
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
	offset := 0
	if i := strings.Index(lower, " offset "); i >= 0 {
		tok := strings.Fields(lower[i+len(" offset "):])[0]
		if n, err := strconv.Atoi(strings.TrimPrefix(tok, "$")); err == nil {
			offset = int(mhArgInt(args, n))
		}
	}
	if i := strings.Index(lower, " limit "); i >= 0 {
		tok := strings.Fields(lower[i+len(" limit "):])[0]
		limit := -1
		if strings.HasPrefix(tok, "$") {
			limit = int(mhArgInt(args, mhBindNum(tok)))
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
	cols := mhColumns[table]
	if strings.HasPrefix(lower, "select ") {
		sel := lower[len("select "):strings.Index(lower, " from ")]
		sel = strings.TrimSpace(strings.ReplaceAll(sel, `"`, ""))
		if sel != "*" && !strings.HasPrefix(sel, "count(") && !strings.HasPrefix(sel, "coalesce(") {
			cols = nil
			for _, cc := range strings.Split(sel, ",") {
				cols = append(cols, mhStripQual(strings.TrimSpace(cc)))
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
	return &mhRows{cols: cols, rows: rows}, nil
}

// ─── ExecContext ─────────────────────────────────────────────────────────────

func (c *mhConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	lower := mhNormalizeSQL(query)

	switch {
	case strings.HasPrefix(lower, "select pg_advisory_xact_lock"):
		return mhResult{rows: 0}, nil
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
	return nil, fmt.Errorf("mh fake: unsupported exec: %s", query)
}

func (c *mhConn) queryBlobRefUpsert(lower string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	valsStart := strings.Index(lower, " values (")
	inner := lower[valsStart+len(" values ("):]
	inner = inner[:strings.Index(inner, ")")]
	toks := strings.Split(inner, ",")
	hash := mhArg(args, mhBindNum(toks[0]))
	size := mhArg(args, mhBindNum(toks[1]))
	createdAt := mhArg(args, mhBindNum(toks[3]))
	lastRefInsert := mhArg(args, mhBindNum(toks[4]))
	lastRefUpdate := mhArg(args, 5)
	idxHash := s.colIndex("mail_blob_refs", "blob_hash")
	idxSize := s.colIndex("mail_blob_refs", "size")
	idxRef := s.colIndex("mail_blob_refs", "ref_count")
	idxLast := s.colIndex("mail_blob_refs", "last_referenced")
	idxCreated := s.colIndex("mail_blob_refs", "created_at")
	for _, row := range s.tables["mail_blob_refs"].rows {
		if row[idxHash] == hash {
			row[idxRef] = row[idxRef].(int64) + 1
			row[idxLast] = lastRefUpdate
			return &mhRows{cols: []string{"ref_count"}, rows: [][]driver.Value{{row[idxRef]}}}, nil
		}
	}
	s.seq++
	row := make([]driver.Value, len(mhColumns["mail_blob_refs"]))
	row[s.colIndex("mail_blob_refs", "id")] = s.seq
	row[idxHash] = hash
	row[idxSize] = size
	row[idxRef] = int64(1)
	row[idxLast] = lastRefInsert
	row[idxCreated] = createdAt
	s.tables["mail_blob_refs"].rows = append(s.tables["mail_blob_refs"].rows, row)
	return &mhRows{cols: []string{"ref_count"}, rows: [][]driver.Value{{int64(1)}}}, nil
}

func (c *mhConn) queryBlobRefGCCandidates(args []driver.NamedValue) (driver.Rows, error) {
	s := c.s
	cutoff := mhArg(args, 1)
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
		ta, ok1 := anchor.(time.Time)
		tc, ok2 := cutoff.(time.Time)
		if !ok1 || !ok2 || !ta.Before(tc) {
			continue
		}
		out = append(out, []driver.Value{row[idxHash], row[idxSize]})
	}
	return &mhRows{cols: []string{"blob_hash", "size"}, rows: out}, nil
}

func (c *mhConn) execCounterBump(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	where := lower[strings.Index(lower, " where ")+len(" where "):]
	eq := strings.Index(where, "$")
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	id := mhArgInt(args, mhBindNum(tok))
	idxID := s.colIndex("mailboxes", "id")
	idxUsed := s.colIndex("mailboxes", "used_bytes")
	idxCount := s.colIndex("mailboxes", "message_count")
	for _, row := range s.tables["mailboxes"].rows {
		if row[idxID] == id {
			row[idxUsed] = row[idxUsed].(int64) + mhArgInt(args, 1)
			row[idxCount] = row[idxCount].(int64) + mhArgInt(args, 2)
			return mhResult{rows: 1}, nil
		}
	}
	return mhResult{rows: 0}, nil
}

func (c *mhConn) execRefDecrement(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	where := lower[strings.Index(lower, " where ")+len(" where "):]
	eq := strings.Index(where, "$")
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	hash := mhArg(args, mhBindNum(tok))
	idxHash := s.colIndex("mail_blob_refs", "blob_hash")
	idxRef := s.colIndex("mail_blob_refs", "ref_count")
	idxLast := s.colIndex("mail_blob_refs", "last_referenced")
	now := mhArg(args, 1)
	for _, row := range s.tables["mail_blob_refs"].rows {
		if row[idxHash] == hash {
			row[idxRef] = row[idxRef].(int64) - 1
			row[idxLast] = now
			return mhResult{rows: 1}, nil
		}
	}
	return mhResult{rows: 0}, nil
}

func (c *mhConn) execBlobRefGCDelete(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	where := lower[strings.Index(lower, " where ")+len(" where "):]
	eq := strings.Index(where, "$")
	tok := where[eq:]
	if end := strings.IndexAny(tok, " )"); end >= 0 {
		tok = tok[:end]
	}
	hash := mhArg(args, mhBindNum(tok))
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
	return mhResult{rows: removed}, nil
}

func (c *mhConn) execUpdate(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	table := mhStripQual(strings.TrimSpace(lower[len("update "):strings.Index(lower, " set ")]))
	setStart := strings.Index(lower, " set ") + len(" set ")
	whereStart := strings.Index(lower, " where ")
	if whereStart < 0 {
		return nil, fmt.Errorf("mh fake: update without where: %s", lower)
	}
	var setters []struct {
		idx int
		val driver.Value
	}
	for _, pair := range strings.Split(lower[setStart:whereStart], ",") {
		pieces := strings.SplitN(pair, "=", 2)
		if len(pieces) != 2 {
			return nil, fmt.Errorf("mh fake: malformed SET pair %q", pair)
		}
		col := mhStripQual(strings.TrimSpace(pieces[0]))
		rhs := strings.TrimSpace(pieces[1])
		idx := s.colIndex(table, col)
		if idx < 0 {
			return nil, fmt.Errorf("mh fake: unknown update column %q on %q", col, table)
		}
		if !strings.HasPrefix(rhs, "$") {
			return nil, fmt.Errorf("mh fake: unsupported SET rhs %q", rhs)
		}
		setters = append(setters, struct {
			idx int
			val driver.Value
		}{idx, mhArg(args, mhBindNum(rhs))})
	}
	preds, err := mhParseWhere(s, strings.TrimSpace(lower[whereStart+len(" where "):]), args)
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
	return mhResult{rows: int64(matched)}, nil
}

func (c *mhConn) execDelete(lower string, args []driver.NamedValue) (driver.Result, error) {
	s := c.s
	table := mhStripQual(strings.TrimSpace(lower[len("delete from "):strings.Index(lower, " where ")]))
	preds, err := mhParseWhere(s, strings.TrimSpace(lower[strings.Index(lower, " where ")+len(" where "):]), args)
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
	return mhResult{rows: removed}, nil
}

// ─── Fixtures ────────────────────────────────────────────────────────────────

// mhFakeBlobStore is an in-memory blob.Store keyed exactly as the service
// keys objects (mailstore.BlobObjectKey). It is the storage half of the
// harness; the codec half is the pass-through mailstore.IdentityBlobCodec.
type mhFakeBlobStore struct {
	mu    sync.Mutex
	items map[string][]byte
}

func newMHFakeBlobStore() *mhFakeBlobStore {
	return &mhFakeBlobStore{items: map[string][]byte{}}
}

func (f *mhFakeBlobStore) Put(_ context.Context, key string, r io.Reader, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[key] = b
	return nil
}

func (f *mhFakeBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.items[key]
	if !ok {
		return nil, fmt.Errorf("mh fake store: no object %q", key)
	}
	return io.NopCloser(strings.NewReader(string(b))), nil
}

func (f *mhFakeBlobStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, key)
	return nil
}

// mhFixture is one harness instance: the fake state, the real
// *mailstore.Service over it (fake driver + IdentityBlobCodec + in-memory
// blob store) and the handler under test.
type mhFixture struct {
	state *mhState
	db    *gorm.DB
	svc   *mailstore.Service
	blobs *mhFakeBlobStore
	h     *MailHandler
}

func newMHFixture(t *testing.T) *mhFixture {
	t.Helper()
	state := newMHState()
	db := mhGorm(t, state)
	blobs := newMHFakeBlobStore()
	svc, err := mailstore.New(db, mailstore.IdentityBlobCodec{}, blobs, nil)
	if err != nil {
		t.Fatalf("new mailstore service over mh fake: %v", err)
	}
	return &mhFixture{
		state: state,
		db:    db,
		svc:   svc,
		blobs: blobs,
		h:     NewMailHandler(svc),
	}
}
