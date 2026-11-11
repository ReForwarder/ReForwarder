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

package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/services/mta"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Tests for the two OB-6 W4 domain surfaces: the MTA-STS policy hosting
// document (Host-header-driven, RFC 8461 shape) and the TLS-RPT read view
// (workspace-scoped like every domain endpoint). Both run through a compact
// fake database/sql driver holding the domains and tls_rpt_reports tables
// (the dmarc_handler_test.go arrangement, self-contained here).

const trhDomainColumns = "id,user_id,workspace_id,domain,ownership_verified,spf_verified,dkim_verified,dmarc_verified,verification_token,created_at,delivery_mode,outbound_bind_ip,max_msgs_per_connection,hourly_budget,pool_id,mta_sts_mode"
const trhReportColumns = "id,user_id,workspace_id,domain_id,inbound_id,report_id,policy_domain,organization_name,contact_info,policy_type,mx_hosts,successful_sessions,failed_sessions,details_json,window_start,window_end,created_at"

type trhState struct {
	mu     sync.Mutex
	tables map[string]*trhTable
	seq    int64
}

type trhTable struct {
	cols []string
	rows [][]driver.Value
}

func newTrhState() *trhState {
	return &trhState{
		tables: map[string]*trhTable{
			"domains":         {cols: strings.Split(trhDomainColumns, ",")},
			"tls_rpt_reports": {cols: strings.Split(trhReportColumns, ",")},
		},
		seq: 900,
	}
}

func (s *trhState) seed(t *testing.T, table string, vals map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	cols := s.tables[table].cols
	row := make([]driver.Value, len(cols))
	defaults := map[string]driver.Value{
		"ownership_verified":      false,
		"spf_verified":            false,
		"dkim_verified":           false,
		"dmarc_verified":          false,
		"verification_token":      "tok",
		"delivery_mode":           "relay",
		"mta_sts_mode":            "",
		"successful_sessions":     int64(0),
		"failed_sessions":         int64(0),
		"policy_domain":           "",
		"mx_hosts":                "",
		"details_json":            "",
		"organization_name":       "",
		"contact_info":            "",
		"window_start":            time.Time{},
		"window_end":              time.Time{},
		"created_at":              time.Time{},
		"max_msgs_per_connection": int64(0),
		"hourly_budget":           int64(0),
	}
	for i, c := range cols {
		if d, ok := defaults[c]; ok {
			row[i] = d
		}
	}
	for c, v := range vals {
		idx := -1
		for i, col := range cols {
			if col == c {
				idx = i
			}
		}
		if idx < 0 {
			t.Fatalf("table %s has no column %s", table, c)
		}
		row[idx] = trhVal(v)
	}
	if row[0] == nil {
		s.seq++
		row[0] = s.seq
	}
	s.tables[table].rows = append(s.tables[table].rows, row)
}

func trhVal(v any) driver.Value {
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
	case *uint:
		if t == nil {
			return nil
		}
		return int64(*t)
	default:
		panic(fmt.Sprintf("trhVal: unsupported %T", v))
	}
}

func trhGorm(t *testing.T, state *trhState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(trhConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open trh fake gorm: %v", err)
	}
	return gdb
}

type trhConnector struct{ state *trhState }

func (c trhConnector) Connect(context.Context) (driver.Conn, error) { return &trhConn{s: c.state}, nil }
func (c trhConnector) Driver() driver.Driver                        { return trhDrv{} }

type trhDrv struct{}

func (trhDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

type trhConn struct{ s *trhState }

func (c *trhConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *trhConn) Close() error                        { return nil }
func (c *trhConn) Begin() (driver.Tx, error)           { return trhTx{}, nil }

type trhTx struct{}

func (trhTx) Commit() error   { return nil }
func (trhTx) Rollback() error { return nil }

type trhRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *trhRows) Columns() []string { return r.cols }
func (r *trhRows) Close() error      { return nil }
func (r *trhRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type trhResult struct{ rows int64 }

func (r trhResult) LastInsertId() (int64, error) { return 0, nil }
func (r trhResult) RowsAffected() (int64, error) { return r.rows, nil }

func trhTableOf(lower string) string {
	for _, name := range []string{"tls_rpt_reports", "domains"} {
		for _, pat := range []string{`from "` + name + `"`, `update "` + name + `"`, `into "` + name + `"`, `join ` + name} {
			if strings.Contains(lower, pat) {
				return name
			}
		}
	}
	return ""
}

func (c *trhConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	table := trhTableOf(lower)
	if table == "" {
		return nil, fmt.Errorf("trhState: cannot route query: %s", query)
	}
	if table == "tls_rpt_reports" && strings.Contains(lower, "report_count") {
		return c.handleSummary(lower, args)
	}
	matched, err := c.s.evalWhere(table, lower, args)
	if err != nil {
		return nil, err
	}
	if strings.Contains(lower, "order by window_end desc, id desc") {
		c.sortByWindowEnd(matched)
	} else if strings.Contains(lower, "order by created_at asc") {
		// stable: already in insertion order
	} else if strings.Contains(lower, "order by") && strings.Contains(lower, "id desc limit 1") {
		c.sortByIDDesc(matched)
	}
	if m := strings.Index(lower, " limit "); m >= 0 {
		if tok, err := trhBindToken(lower[m+len(" limit "):]); err == nil {
			if v, ok := trhArg(args, tok).(int64); ok && int(v) < len(matched) {
				matched = matched[:int(v)]
			}
		}
	}
	return &trhRows{cols: c.s.tables[table].cols, rows: matched}, nil
}

func (c *trhConn) sortByWindowEnd(rows [][]driver.Value) {
	weIdx := c.s.colIndex("tls_rpt_reports", "window_end")
	idIdx := c.s.colIndex("tls_rpt_reports", "id")
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			wa, _ := rows[j][weIdx].(time.Time)
			wb, _ := rows[j-1][weIdx].(time.Time)
			ia, _ := rows[j][idIdx].(int64)
			ib, _ := rows[j-1][idIdx].(int64)
			if wa.After(wb) || (wa.Equal(wb) && ia > ib) {
				rows[j], rows[j-1] = rows[j-1], rows[j]
			} else {
				break
			}
		}
	}
}

func (c *trhConn) sortByIDDesc(rows [][]driver.Value) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			ia, _ := rows[j][0].(int64)
			ib, _ := rows[j-1][0].(int64)
			if ia > ib {
				rows[j], rows[j-1] = rows[j-1], rows[j]
			} else {
				break
			}
		}
	}
}

// handleSummary emulates the TLSRPTRepository.Summary aggregate.
func (c *trhConn) handleSummary(lower string, args []driver.NamedValue) (driver.Rows, error) {
	matched, err := c.s.evalWhere("tls_rpt_reports", lower, args)
	if err != nil {
		return nil, err
	}
	okIdx := c.s.colIndex("tls_rpt_reports", "successful_sessions")
	failIdx := c.s.colIndex("tls_rpt_reports", "failed_sessions")
	weIdx := c.s.colIndex("tls_rpt_reports", "window_end")

	var okc, failc int64
	var latest *time.Time
	for _, row := range matched {
		o, _ := row[okIdx].(int64)
		f, _ := row[failIdx].(int64)
		okc += o
		failc += f
		if we, ok := row[weIdx].(time.Time); ok {
			t := we
			if latest == nil || t.After(*latest) {
				latest = &t
			}
		}
	}
	cols := []string{"report_count", "successful", "failed", "latest_window_end"}
	return &trhRows{cols: cols, rows: [][]driver.Value{{int64(len(matched)), okc, failc, latest}}}, nil
}

func (c *trhConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if strings.Contains(lower, "update") || strings.Contains(lower, "insert into") {
		return trhResult{rows: 1}, nil
	}
	return nil, fmt.Errorf("trhState: unsupported exec: %s", query)
}

func (s *trhState) colIndex(table, col string) int {
	for i, c := range s.tables[table].cols {
		if c == col {
			return i
		}
	}
	return -1
}

func (s *trhState) evalWhere(table, compact string, args []driver.NamedValue) ([][]driver.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	if i := strings.Index(compact, " where "); i >= 0 {
		where = compact[i+len(" where "):]
	}
	for _, cut := range []string{" order by ", " limit ", " returning "} {
		if i := strings.Index(where, cut); i >= 0 {
			where = where[:i]
		}
	}
	// Normalize LOWER(x) before stripping parens, so the FindVerifiedByName
	// form parses like a plain column comparison.
	where = strings.ReplaceAll(strings.ToLower(where), "lower(", "")
	where = strings.NewReplacer("(", " ", ")", " ").Replace(where)

	var preds []func([]driver.Value) bool
	for _, cond := range strings.Split(where, " and ") {
		cond = strings.TrimSpace(cond)
		if cond == "" {
			continue
		}
		switch {
		case strings.HasSuffix(cond, " is null"):
			idx := s.colIndex(table, trhColumnOf(strings.TrimSuffix(cond, " is null")))
			preds = append(preds, func(row []driver.Value) bool { return row[idx] == nil })
		case strings.Contains(cond, " like "):
			return nil, fmt.Errorf("trhState: unsupported LIKE condition %q", cond)
		default:
			eq := strings.Index(cond, " = ")
			if eq < 0 {
				return nil, fmt.Errorf("trhState: unsupported condition %q", cond)
			}
			left := trhColumnOf(cond[:eq])
			right := strings.TrimSpace(cond[eq+3:])
			// Normalize LOWER(x) = LOWER($n) — the FindVerifiedByName form.
			right = strings.TrimSuffix(strings.TrimPrefix(right, "lower("), ")")
			idx := s.colIndex(table, left)
			n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(right), "$"))
			if err != nil {
				return nil, fmt.Errorf("trhState: bad bindvar in %q", cond)
			}
			want := trhArg(args, n)
			preds = append(preds, func(row []driver.Value) bool { return trhEqual(row[idx], want) })
		}
	}
	var out [][]driver.Value
	for _, row := range s.tables[table].rows {
		ok := true
		for _, p := range preds {
			if !p(row) {
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

func trhColumnOf(expr string) string {
	expr = strings.TrimSpace(expr)
	if i := strings.LastIndex(expr, "."); i >= 0 {
		expr = expr[i+1:]
	}
	return strings.Trim(expr, `"`)
}

func trhArg(args []driver.NamedValue, n int) driver.Value {
	for _, a := range args {
		if a.Ordinal == n {
			return a.Value
		}
	}
	panic(fmt.Sprintf("trhState: no arg with ordinal %d", n))
}

func trhBindToken(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$") {
		return 0, fmt.Errorf("trhState: no bindvar in %q", s)
	}
	return strconv.Atoi(s[1:])
}

func trhEqual(a, b driver.Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok2 := b.(time.Time)
		return ok2 && ta.Equal(tb)
	}
	if sa, ok := a.(string); ok {
		if sb, ok2 := b.(string); ok2 {
			// LOWER(domain) = LOWER(?) — the fake normalizes both sides.
			return strings.EqualFold(sa, sb)
		}
	}
	return a == b
}

// ---------- policy hosting tests ----------

func TestMTASTSPolicyDocumentShape(t *testing.T) {
	state := newTrhState()
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true, "mta_sts_mode": "enforce",
	})
	gdb := trhGorm(t, state)
	h := NewMTASTSPolicyHandler(repositories.NewDomainRepository(gdb))
	h.SetLookupMX(func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mx1.example.com.", Pref: 10}, {Host: "mx2.example.com.", Pref: 20}}, nil
	})

	o := okapi.NewTestServer(t)
	o.Okapi.Get("/.well-known/mta-sts.txt", h.Get)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/mta-sts.txt", nil)
	req.Host = "mta-sts.example.com"
	o.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("policy get = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type = %q, want text/plain", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "max-age=86400" {
		t.Fatalf("cache-control = %q", cc)
	}
	body := rec.Body.String()
	wantLines := []string{
		"version: STS1",
		"mode: enforce",
		"mx: mx1.example.com",
		"mx: mx2.example.com",
		"max_age: 86400",
	}
	for _, want := range wantLines {
		if !strings.Contains(body, want+"\r\n") {
			t.Fatalf("policy document missing %q:\n%s", want, body)
		}
	}
	// The served document parses back through the client-side parser (the
	// hosting and consuming halves agree on the shape).
	if _, err := mta.ParseSTSPolicy([]byte(body), "served"); err != nil {
		t.Fatalf("served policy does not round-trip: %v", err)
	}
}

func TestMTASTSPolicyRefusesWhenNotOptedIn(t *testing.T) {
	state := newTrhState()
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true, "mta_sts_mode": "",
	})
	h := NewMTASTSPolicyHandler(repositories.NewDomainRepository(trhGorm(t, state)))
	h.SetLookupMX(func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mx1.example.com.", Pref: 10}}, nil
	})
	o := okapi.NewTestServer(t)
	o.Okapi.Get("/.well-known/mta-sts.txt", h.Get)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://mta-sts.example.com/.well-known/mta-sts.txt", nil)
	req.Host = "mta-sts.example.com"
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unopted domain must 404 (no policy), got %d", rec.Code)
	}
}

func TestMTASTSPolicyRefusesForeignHostAndMissingDomain(t *testing.T) {
	state := newTrhState()
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true, "mta_sts_mode": "enforce",
	})
	h := NewMTASTSPolicyHandler(repositories.NewDomainRepository(trhGorm(t, state)))
	h.SetLookupMX(func(context.Context, string) ([]*net.MX, error) {
		return []*net.MX{{Host: "mx1.example.com.", Pref: 10}}, nil
	})
	o := okapi.NewTestServer(t)
	o.Okapi.Get("/.well-known/mta-sts.txt", h.Get)

	// The dashboard origin never serves a tenant policy.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://app.example.com/.well-known/mta-sts.txt", nil)
	req.Host = "app.example.com"
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non mta-sts host must 404, got %d", rec.Code)
	}

	// An unknown domain 404s.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "https://mta-sts.stranger.com/.well-known/mta-sts.txt", nil)
	req.Host = "mta-sts.stranger.com"
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown domain must 404, got %d", rec.Code)
	}

	// An unverified domain 404s (fail-closed per ADR-008 §2).
	state.seed(t, "domains", map[string]any{
		"id": uint(11), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "unverified.com", "ownership_verified": false, "mta_sts_mode": "enforce",
	})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "https://mta-sts.unverified.com/.well-known/mta-sts.txt", nil)
	req.Host = "mta-sts.unverified.com"
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unverified domain must 404, got %d", rec.Code)
	}
}

func TestMTASTSPolicyRefusesWithoutResolvableMX(t *testing.T) {
	state := newTrhState()
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true, "mta_sts_mode": "none",
	})
	h := NewMTASTSPolicyHandler(repositories.NewDomainRepository(trhGorm(t, state)))
	h.SetLookupMX(func(context.Context, string) ([]*net.MX, error) {
		return nil, errors.New("no MX") // DNS failure → never a broken document
	})
	o := okapi.NewTestServer(t)
	o.Okapi.Get("/.well-known/mta-sts.txt", h.Get)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://mta-sts.example.com/.well-known/mta-sts.txt", nil)
	req.Host = "mta-sts.example.com"
	o.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unresolvable MX must 404, got %d", rec.Code)
	}
}

// ---------- TLS-RPT read surface tests ----------

func newTLSRPTServer(t *testing.T) (*okapi.TestServer, *trhState) {
	t.Helper()
	state := newTrhState()
	gdb := trhGorm(t, state)
	h := NewTLSRPTHandler(
		repositories.NewTLSRPTRepository(gdb),
		repositories.NewDomainRepository(gdb),
		nil, // no audit logger: the read tests do not assert the event trail
	)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		dmrWorkspaceBinding,
	)
	g.Get("/domains/{id:int}/tls-rpt", okapi.H(h.Get))
	return o, state
}

func TestTLSRPTGetSummaryAndRecent(t *testing.T) {
	o, state := newTLSRPTServer(t)
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true,
	})
	base := map[string]any{
		"user_id": uint(7), "workspace_id": wsID(3), "domain_id": uint(10),
		"report_id": "r1", "organization_name": "Reporter",
	}
	state.seed(t, "tls_rpt_reports", mergeTrh(base, map[string]any{
		"policy_domain":       "example.com",
		"successful_sessions": int64(100), "failed_sessions": int64(3),
		"window_start": time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		"window_end":   time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
	}))
	state.seed(t, "tls_rpt_reports", mergeTrh(base, map[string]any{
		"policy_domain":       "example.com",
		"successful_sessions": int64(50), "failed_sessions": int64(0),
		"window_start": time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC),
		"window_end":   time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC),
	}))
	// Foreign workspace row on the same domain: must never leak.
	state.seed(t, "tls_rpt_reports", mergeTrh(base, map[string]any{
		"user_id": uint(42), "workspace_id": wsID(99), "report_id": "r-leak",
		"policy_domain":       "example.com",
		"successful_sessions": int64(99999),
		"window_end":          time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC),
	}))

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/tls-rpt", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tls-rpt get = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool                 `json:"success"`
		Data    DomainTLSRPTResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.Summary.ReportCount != 2 {
		t.Fatalf("report_count = %d, want 2 (foreign row excluded)", res.Data.Summary.ReportCount)
	}
	if res.Data.Summary.SuccessfulSessions != 150 || res.Data.Summary.FailedSessions != 3 {
		t.Fatalf("session totals wrong: %+v", res.Data.Summary)
	}
	if res.Data.Summary.LatestReportID != "r1" {
		t.Fatalf("latest report id = %q (both rows share it; newest window first)", res.Data.Summary.LatestReportID)
	}
	if len(res.Data.Reports) != 2 {
		t.Fatalf("reports = %d, want 2", len(res.Data.Reports))
	}
	if res.Data.Reports[0].WindowEnd.Before(res.Data.Reports[1].WindowEnd) {
		t.Fatalf("reports must be newest-window-first")
	}
}

func TestTLSRPTGetForeignDomainNotFound(t *testing.T) {
	o, state := newTLSRPTServer(t)
	// The domain belongs to another workspace: a foreign scope reads a 404,
	// never the data.
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(42), "workspace_id": wsID(99),
		"domain": "example.com", "ownership_verified": true,
	})
	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/tls-rpt", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain = %d, want 404", rec.Code)
	}
}

func TestTLSRPTGetEmptySummary(t *testing.T) {
	o, state := newTLSRPTServer(t)
	state.seed(t, "domains", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": wsID(3),
		"domain": "example.com", "ownership_verified": true,
	})
	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/tls-rpt", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty tls-rpt get = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool                 `json:"success"`
		Data    DomainTLSRPTResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.Summary.ReportCount != 0 || len(res.Data.Reports) != 0 {
		t.Fatalf("empty domain must return the zero summary: %+v", res.Data)
	}
}

// ---------- helpers ----------

func mergeTrh(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
