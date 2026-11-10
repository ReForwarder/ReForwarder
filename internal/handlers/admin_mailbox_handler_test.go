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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The OB-5 mailbox-administration battery (ADR-005 decision 3). The handler
// is exercised over the real okapi routing stack with the real admin JWT
// middleware (same shape as admin_queue_test.go); the mailstore behind it is
// an in-memory AdminMailboxStore fake, and the audit trail is recorded
// through the shared events fake database (qEvtState, admin_queue_test.go).
// The store-seam fake covers the handler's logic: envelope shapes, paging and
// filters, target validation, status mapping, and audit emission. The
// mailstore service behavior underneath (cascade delete, ref counts, quota
// enforcement) is covered by internal/services/mailstore/admin_test.go and
// the real-Postgres suites.

// ---------- the AdminMailboxStore fake ----------

type mbxMemberKey struct{ workspaceID, userID uint }

type fakeMailboxStore struct {
	mu        sync.Mutex
	mailboxes map[uint]*models.Mailbox
	nextID    uint
	users     map[uint]bool         // existing user ids
	members   map[mbxMemberKey]bool // workspace memberships
	calls     []string              // recorded seam calls, in order
	failNext  error                 // when set, mutations return it once
}

func newFakeMailboxStore() *fakeMailboxStore {
	return &fakeMailboxStore{
		mailboxes: map[uint]*models.Mailbox{},
		nextID:    500,
		users:     map[uint]bool{},
		members:   map[mbxMemberKey]bool{},
	}
}

func (f *fakeMailboxStore) seedUser(id uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[id] = true
}

func (f *fakeMailboxStore) seedMember(userID, workspaceID uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[mbxMemberKey{workspaceID, userID}] = true
}

func (f *fakeMailboxStore) seedMailbox(mb *models.Mailbox) *models.Mailbox {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	cp := *mb
	cp.ID = f.nextID
	f.mailboxes[cp.ID] = &cp
	return &cp
}

func (f *fakeMailboxStore) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeMailboxStore) madeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMailboxStore) AdminListMailboxes(filter mailstore.AdminMailboxFilter, limit, offset int) ([]models.Mailbox, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("list user=%d ws=%d status=%q search=%q", filter.UserID, filter.WorkspaceID, filter.Status, filter.Search))
	var out []models.Mailbox
	for _, mb := range f.mailboxes {
		if filter.UserID != 0 && mb.UserID != filter.UserID {
			continue
		}
		if filter.WorkspaceID != 0 && (mb.WorkspaceID == nil || *mb.WorkspaceID != filter.WorkspaceID) {
			continue
		}
		if filter.Status != "" && mb.Status != filter.Status {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(mb.Name), strings.ToLower(filter.Search)) {
			continue
		}
		out = append(out, *mb)
	}
	total := int64(len(out))
	// Oldest-first by insertion id, then window.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(out) {
		out = nil
	} else {
		out = out[offset:]
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

func (f *fakeMailboxStore) AdminGetMailbox(id uint) (*models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[id]
	if !ok {
		return nil, mailstore.ErrMailboxNotFound
	}
	cp := *mb
	return &cp, nil
}

func (f *fakeMailboxStore) AdminProvisionMailbox(userID uint, workspaceID *uint, name string, quotaBytes, quotaMessages int64) (*models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	f.record(fmt.Sprintf("provision user=%d name=%q qb=%d qm=%d", userID, name, quotaBytes, quotaMessages))
	for _, mb := range f.mailboxes {
		if mb.UserID == userID && mb.Name == name {
			return nil, mailstore.ErrMailboxNameTaken
		}
	}
	if quotaBytes < 0 || quotaMessages < 0 {
		return nil, errors.New("mailstore: quota_bytes must be >= 0")
	}
	f.nextID++
	mb := &models.Mailbox{
		ID:            f.nextID,
		UserID:        userID,
		WorkspaceID:   workspaceID,
		Name:          name,
		QuotaBytes:    quotaBytes,
		QuotaMessages: quotaMessages,
		Status:        models.MailboxStatusActive,
		CreatedAt:     time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC),
	}
	f.mailboxes[mb.ID] = mb
	return mb, nil
}

func (f *fakeMailboxStore) AdminSetMailboxQuota(id uint, quotaBytes, quotaMessages int64) (*models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[id]
	if !ok {
		return nil, mailstore.ErrMailboxNotFound
	}
	f.record(fmt.Sprintf("quota mailbox=%d qb=%d qm=%d", id, quotaBytes, quotaMessages))
	if quotaBytes < 0 || quotaMessages < 0 {
		return nil, errors.New("mailstore: quota_bytes must be >= 0")
	}
	mb.QuotaBytes = quotaBytes
	mb.QuotaMessages = quotaMessages
	cp := *mb
	return &cp, nil
}

func (f *fakeMailboxStore) AdminSetMailboxStatus(id uint, status models.MailboxStatus) (*models.Mailbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[id]
	if !ok {
		return nil, mailstore.ErrMailboxNotFound
	}
	f.record(fmt.Sprintf("status mailbox=%d status=%q", id, status))
	if status != models.MailboxStatusActive && status != models.MailboxStatusSuspended {
		return nil, fmt.Errorf("mailstore: admin status change must be active or suspended, got %q", status)
	}
	mb.Status = status
	cp := *mb
	return &cp, nil
}

func (f *fakeMailboxStore) AdminDeleteMailbox(id uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.mailboxes[id]; !ok {
		return mailstore.ErrMailboxNotFound
	}
	f.record(fmt.Sprintf("delete mailbox=%d", id))
	delete(f.mailboxes, id)
	return nil
}

func (f *fakeMailboxStore) AdminMailboxUsage(id uint) (*mailstore.MailboxUsageReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mb, ok := f.mailboxes[id]
	if !ok {
		return nil, mailstore.ErrMailboxNotFound
	}
	return &mailstore.MailboxUsageReport{
		MailboxID: mb.ID, UserID: mb.UserID, Name: mb.Name,
		QuotaBytes: mb.QuotaBytes, RawBytes: mb.UsedBytes, MessageCount: mb.MessageCount,
		BlobBytes: 1234, DistinctBlobs: 2,
	}, nil
}

func (f *fakeMailboxStore) AdminUserExists(userID uint) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[userID], nil
}

func (f *fakeMailboxStore) AdminUserInWorkspace(userID, workspaceID uint) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[mbxMemberKey{workspaceID, userID}], nil
}

// ---------- the users fake database (owner enrichment) ----------

var mbxUserColumns = []string{"id", "name", "email"}

type mbxUserState struct {
	mu   sync.Mutex
	rows [][]driver.Value
}

func (s *mbxUserState) seedUser(id uint, name, email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, []driver.Value{int64(id), name, email})
}

type mbxUserConn struct{ s *mbxUserState }

func (c *mbxUserConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *mbxUserConn) Close() error                        { return nil }
func (c *mbxUserConn) Begin() (driver.Tx, error)           { return mbxUserTx{}, nil }

type mbxUserTx struct{}

func (mbxUserTx) Commit() error   { return nil }
func (mbxUserTx) Rollback() error { return nil }

func (c *mbxUserConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	lower := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if !strings.Contains(lower, `from "users"`) && !strings.Contains(lower, "from users") {
		return nil, fmt.Errorf("mbxUserState: cannot route query: %s", query)
	}
	// The only shape issued is `... where id in ($1,$2,...)`.
	in := strings.Index(lower, " in (")
	if in < 0 {
		return nil, fmt.Errorf("mbxUserState: unsupported users query: %s", query)
	}
	inner := lower[strings.Index(lower[in:], "(")+in+1 : strings.Index(lower[in:], ")")+in]
	var ids []int64
	for _, tok := range strings.Split(inner, ",") {
		tok = strings.TrimSpace(tok)
		if !strings.HasPrefix(tok, "$") {
			return nil, fmt.Errorf("mbxUserState: non-bindvar id %q", tok)
		}
		var n int
		if _, err := fmt.Sscanf(tok, "$%d", &n); err != nil {
			return nil, err
		}
		for _, a := range args {
			if a.Ordinal == n {
				ids = append(ids, a.Value.(int64))
			}
		}
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	var rows [][]driver.Value
	for _, row := range c.s.rows {
		id := row[0].(int64)
		for _, want := range ids {
			if id == want {
				rows = append(rows, row)
				break
			}
		}
	}
	return &mbxUserRows{cols: mbxUserColumns, rows: rows}, nil
}

func (c *mbxUserConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("mbxUserState: unsupported exec: %s", query)
}

type mbxUserRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *mbxUserRows) Columns() []string { return r.cols }
func (r *mbxUserRows) Close() error      { return nil }
func (r *mbxUserRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

type mbxUserConnector struct{ state *mbxUserState }

func (c mbxUserConnector) Connect(context.Context) (driver.Conn, error) {
	return &mbxUserConn{s: c.state}, nil
}
func (c mbxUserConnector) Driver() driver.Driver { return mbxUserDrv{} }

type mbxUserDrv struct{}

func (mbxUserDrv) Open(string) (driver.Conn, error) { return nil, errors.New("use sql.OpenDB") }

func mbxUserGorm(t *testing.T, state *mbxUserState) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(mbxUserConnector{state})}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open users fake gorm: %v", err)
	}
	return gdb
}

// ---------- the server under test ----------

type mbxServer struct {
	o      *okapi.TestServer
	store  *fakeMailboxStore
	events *qEvtState
	users  *mbxUserState
}

func newMailboxAdminServer(t *testing.T) *mbxServer {
	t.Helper()
	store := newFakeMailboxStore()
	users := &mbxUserState{}
	evtState := newQEvtState()

	h := NewAdminMailboxHandler(store, mbxUserGorm(t, users))
	h.SetAuditLogger(audit.NewLogger(eventbus.New(repositories.NewEventRepository(qEvtGorm(t, evtState)))))

	o := okapi.NewTestServer(t)
	adminAuth := middlewares.JWTAdminAuth(&config.Config{JWTSecret: logsTestJWTSecret})
	g := o.Group("/admin", adminAuth.Middleware)
	g.Get("/mailboxes", okapi.H(h.List))
	g.Post("/mailboxes", okapi.H(h.Create))
	g.Patch("/mailboxes/{id:int}", okapi.H(h.Update))
	g.Delete("/mailboxes/{id:int}", okapi.H(h.Delete))
	g.Get("/mailboxes/{id:int}/usage", okapi.H(h.Usage))
	return &mbxServer{o: o, store: store, events: evtState, users: users}
}

func (s *mbxServer) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		// okapi's binder only decodes the JSON body when the content type says JSON.
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.o.ServeHTTP(rec, req)
	return rec
}

func (s *mbxServer) eventTypes() []string {
	var out []string
	for _, e := range s.events.events() {
		if v, ok := e["type"].(string); ok {
			out = append(out, v)
		}
	}
	return out
}

func mbxRow(t *testing.T, body string) map[string]any {
	t.Helper()
	var env struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if !env.Success {
		t.Fatalf("response not successful: %s", body)
	}
	return env.Data
}

func mbxSeedMailbox(store *fakeMailboxStore, owner uint, name string, status models.MailboxStatus, quota, used int64) *models.Mailbox {
	ws := uint(77)
	return store.seedMailbox(&models.Mailbox{
		UserID: owner, WorkspaceID: &ws, Name: name, QuotaBytes: quota,
		UsedBytes: used, MessageCount: used / 10, Status: status,
		CreatedAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
	})
}

// ---------- tests ----------

// TestAdminMailboxRoutesRequireAdminAuth pins the gate on every mailbox
// admin route: unauthenticated 401, non-admin 403, admin passes.
func TestAdminMailboxRoutesRequireAdminAuth(t *testing.T) {
	s := newMailboxAdminServer(t)
	s.store.seedUser(101)
	cases := []struct{ method, path string }{
		{http.MethodGet, "/admin/mailboxes"},
		{http.MethodPost, "/admin/mailboxes"},
		{http.MethodPatch, "/admin/mailboxes/1"},
		{http.MethodDelete, "/admin/mailboxes/1"},
		{http.MethodGet, "/admin/mailboxes/1/usage"},
	}
	admin := logsTestJWT(t, "admin")
	user := logsTestJWT(t, "user")
	s.store.seedMailbox(&models.Mailbox{UserID: 101, Name: "a@example.test", Status: models.MailboxStatusActive})
	for _, tc := range cases {
		if got := s.do(t, tc.method, tc.path, "", "").Code; got != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", tc.method, tc.path, got)
		}
		if got := s.do(t, tc.method, tc.path, user, "{}").Code; got != http.StatusForbidden {
			t.Errorf("%s %s non-admin = %d, want 403", tc.method, tc.path, got)
		}
	}
	// The admin token passes the gate on all five (4xx here would be a
	// validation answer, never 401/403).
	if got := s.do(t, http.MethodGet, "/admin/mailboxes", admin, "").Code; got != http.StatusOK {
		t.Errorf("admin list = %d, want 200", got)
	}
	if got := s.do(t, http.MethodPost, "/admin/mailboxes", admin, `{"user_id":101,"name":"x@example.test"}`).Code; got != http.StatusCreated {
		t.Errorf("admin provision = %d, want 201 (%s)", got, s.do(t, http.MethodPost, "/admin/mailboxes", admin, `{"user_id":101,"name":"x@example.test"}`).Body.String())
	}
	if got := s.do(t, http.MethodGet, "/admin/mailboxes/9999/usage", admin, "").Code; got != http.StatusNotFound {
		t.Errorf("admin usage of unknown mailbox = %d, want 404", got)
	}
}

// TestAdminMailboxListPagingFiltersAndOwnerEnrichment — the table endpoint
// pages with truthful totals, applies its filters, and enriches each row
// with the owner identity.
func TestAdminMailboxListPagingFiltersAndOwnerEnrichment(t *testing.T) {
	s := newMailboxAdminServer(t)
	admin := logsTestJWT(t, "admin")
	s.store.seedUser(101)
	s.store.seedUser(202)
	s.users.seedUser(101, "Alice", "alice@example.test")
	s.users.seedUser(202, "Bob", "bob@example.test")
	first := mbxSeedMailbox(s.store, 101, "alice@example.test", models.MailboxStatusActive, 1000, 100)
	second := mbxSeedMailbox(s.store, 101, "alice-2@example.test", models.MailboxStatusActive, 0, 0)
	suspended := mbxSeedMailbox(s.store, 202, "bob@example.test", models.MailboxStatusSuspended, 500, 50)

	rec := s.do(t, http.MethodGet, "/admin/mailboxes", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Success  bool             `json:"success"`
		Data     []map[string]any `json:"data"`
		Pageable struct {
			CurrentPage   int   `json:"current_page"`
			Size          int   `json:"size"`
			TotalElements int64 `json:"total_elements"`
			Empty         bool  `json:"empty"`
		} `json:"pageable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if !page.Success || page.Pageable.TotalElements != 3 || len(page.Data) != 3 || page.Pageable.Empty {
		t.Fatalf("unpaged list wrong: %+v", page)
	}
	if page.Data[0]["name"] != first.Name || page.Data[2]["name"] != suspended.Name {
		t.Fatalf("ordering wrong: %v", page.Data)
	}
	if page.Data[0]["owner_email"] != "alice@example.test" || page.Data[0]["owner_name"] != "Alice" {
		t.Fatalf("owner enrichment missing: %v", page.Data[0])
	}
	if _, ok := page.Data[0]["workspace_id"]; !ok {
		t.Fatal("workspace_id missing from the row")
	}

	// Owner filter.
	rec = s.do(t, http.MethodGet, "/admin/mailboxes?user=202", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filter list = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode filter list: %v", err)
	}
	if page.Pageable.TotalElements != 1 || page.Data[0]["user_id"].(float64) != 202 {
		t.Fatalf("owner filter wrong: %+v", page)
	}

	// Status filter.
	rec = s.do(t, http.MethodGet, "/admin/mailboxes?status=suspended", admin, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode status list: %v", err)
	}
	if page.Pageable.TotalElements != 1 || page.Data[0]["name"] != suspended.Name {
		t.Fatalf("status filter wrong: %+v", page)
	}

	// Invalid status filter is a 400, not a silent empty page.
	if got := s.do(t, http.MethodGet, "/admin/mailboxes?status=vaporized", admin, "").Code; got != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want 400", got)
	}

	// Search matches literally and case-insensitively.
	rec = s.do(t, http.MethodGet, "/admin/mailboxes?search=ALICE-2", admin, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode search list: %v", err)
	}
	if page.Pageable.TotalElements != 1 || page.Data[0]["name"] != second.Name {
		t.Fatalf("search wrong: %+v", page)
	}

	// Paging: size=2&page=1 (0-based) returns the third row alone.
	rec = s.do(t, http.MethodGet, "/admin/mailboxes?size=2&page=1", admin, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode paged list: %v", err)
	}
	if page.Pageable.TotalElements != 3 || len(page.Data) != 1 || page.Data[0]["name"] != suspended.Name {
		t.Fatalf("paging wrong: %+v", page)
	}
}

// TestAdminMailboxProvisionValidationAndAudit — provisioning validates its
// targets, maps conflicts, and emits admin.mailbox.provisioned.
func TestAdminMailboxProvisionValidationAndAudit(t *testing.T) {
	s := newMailboxAdminServer(t)
	admin := logsTestJWT(t, "admin")
	s.store.seedUser(101)
	s.store.seedMember(101, 77)

	// Happy path: 201, the created row, and one audit event.
	body := `{"user_id":101,"name":"new@example.test","quota_bytes":1024,"quota_messages":10,"workspace_id":77}`
	rec := s.do(t, http.MethodPost, "/admin/mailboxes", admin, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("provision = %d: %s", rec.Code, rec.Body.String())
	}
	row := mbxRow(t, rec.Body.String())
	if row["name"] != "new@example.test" || row["quota_bytes"].(float64) != 1024 || row["quota_messages"].(float64) != 10 {
		t.Fatalf("created row wrong: %v", row)
	}
	if row["workspace_id"].(float64) != 77 {
		t.Fatalf("workspace binding missing: %v", row)
	}
	events := s.eventTypes()
	if len(events) != 1 || events[0] != "admin.mailbox.provisioned" {
		t.Fatalf("audit events = %v, want one admin.mailbox.provisioned", events)
	}

	// Unknown owner → 404, no audit.
	if got := s.do(t, http.MethodPost, "/admin/mailboxes", admin, `{"user_id":999,"name":"x@example.test"}`).Code; got != http.StatusNotFound {
		t.Fatalf("unknown owner = %d, want 404", got)
	}
	// Workspace the user is not a member of → 400 (ADR-008 parent chain).
	if got := s.do(t, http.MethodPost, "/admin/mailboxes", admin, `{"user_id":101,"name":"x@example.test","workspace_id":88}`).Code; got != http.StatusBadRequest {
		t.Fatalf("non-member workspace = %d, want 400", got)
	}
	// Duplicate name for the same owner → 409.
	if got := s.do(t, http.MethodPost, "/admin/mailboxes", admin, body).Code; got != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", got)
	}
	// Negative quota → 400; missing name → 400; missing user_id → 400.
	if got := s.do(t, http.MethodPost, "/admin/mailboxes", admin, `{"user_id":101,"name":"n@example.test","quota_bytes":-1}`).Code; got != http.StatusBadRequest {
		t.Fatalf("negative quota = %d, want 400", got)
	}
	if got := s.do(t, http.MethodPost, "/admin/mailboxes", admin, `{"user_id":101,"name":"  "}`).Code; got != http.StatusBadRequest {
		t.Fatalf("blank name = %d, want 400", got)
	}
	if got := s.do(t, http.MethodPost, "/admin/mailboxes", admin, `{"name":"orphan@example.test"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("missing user_id = %d, want 400", got)
	}
	if got := len(s.eventTypes()); got != 1 {
		t.Fatalf("failed provisions must not audit, got %d events", got)
	}
}

// TestAdminMailboxPatchQuotaStatusAndAudit — PATCH applies quota and status
// changes, audited per change, and refuses the closed transition.
func TestAdminMailboxPatchQuotaStatusAndAudit(t *testing.T) {
	s := newMailboxAdminServer(t)
	admin := logsTestJWT(t, "admin")
	s.store.seedUser(101)
	s.users.seedUser(101, "Alice", "alice@example.test")
	mb := mbxSeedMailbox(s.store, 101, "patch@example.test", models.MailboxStatusActive, 1000, 100)

	// Quota change only.
	rec := s.do(t, http.MethodPatch, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, `{"quota_bytes":2048}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("quota patch = %d: %s", rec.Code, rec.Body.String())
	}
	if row := mbxRow(t, rec.Body.String()); row["quota_bytes"].(float64) != 2048 || row["quota_messages"].(float64) != 0 {
		t.Fatalf("quota patch row wrong: %v", row)
	}
	if events := s.eventTypes(); len(events) != 1 || events[0] != "admin.mailbox.updated" {
		t.Fatalf("audit events = %v, want admin.mailbox.updated", events)
	}

	// Suspend.
	rec = s.do(t, http.MethodPatch, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, `{"status":"suspended"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend = %d", rec.Code)
	}
	if row := mbxRow(t, rec.Body.String()); row["status"] != "suspended" {
		t.Fatalf("status not applied: %v", row)
	}
	// Reactivate.
	rec = s.do(t, http.MethodPatch, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, `{"status":"active"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate = %d", rec.Code)
	}
	types := s.eventTypes()
	if len(types) != 3 || types[1] != "admin.mailbox.suspended" || types[2] != "admin.mailbox.reactivated" {
		t.Fatalf("audit events = %v, want suspended then reactivated", types)
	}

	// Closed is refused; unknown status refused; empty body refused.
	if got := s.do(t, http.MethodPatch, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, `{"status":"closed"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("closed transition = %d, want 400", got)
	}
	if got := s.do(t, http.MethodPatch, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, `{"status":"flying"}`).Code; got != http.StatusBadRequest {
		t.Fatalf("unknown status = %d, want 400", got)
	}
	if got := s.do(t, http.MethodPatch, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, `{}`).Code; got != http.StatusBadRequest {
		t.Fatalf("empty patch = %d, want 400", got)
	}
	// Negative quota refused; unknown mailbox → 404.
	if got := s.do(t, http.MethodPatch, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, `{"quota_bytes":-2}`).Code; got != http.StatusBadRequest {
		t.Fatalf("negative quota patch = %d, want 400", got)
	}
	if got := s.do(t, http.MethodPatch, "/admin/mailboxes/424242", admin, `{"quota_bytes":1}`).Code; got != http.StatusNotFound {
		t.Fatalf("unknown mailbox patch = %d, want 404", got)
	}
	if len(s.eventTypes()) != 3 {
		t.Fatalf("failed patches must not audit, got %v", s.eventTypes())
	}
}

// TestAdminMailboxDeleteCascadeAndAudit — DELETE reaches the store's admin
// delete (the cascade itself is the mailstore service's, covered by
// internal/services/mailstore/admin_test.go), answers with the completion
// message, and is audited with the mailbox's final counters.
func TestAdminMailboxDeleteCascadeAndAudit(t *testing.T) {
	s := newMailboxAdminServer(t)
	admin := logsTestJWT(t, "admin")
	s.store.seedUser(101)
	mb := mbxSeedMailbox(s.store, 101, "gone@example.test", models.MailboxStatusActive, 4096, 500)

	rec := s.do(t, http.MethodDelete, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	data := mbxRow(t, rec.Body.String())
	if msg, _ := data["message"].(string); !strings.Contains(msg, "gone@example.test") {
		t.Fatalf("delete message wrong: %v", data)
	}
	calls := s.store.madeCalls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], fmt.Sprintf("delete mailbox=%d", mb.ID)) {
		t.Fatalf("store calls = %v, want exactly the admin delete of %d", calls, mb.ID)
	}
	events := s.events.events()
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	if events[0]["type"] != "admin.mailbox.deleted" {
		t.Fatalf("audit type = %v, want admin.mailbox.deleted", events[0]["type"])
	}
	var meta struct {
		MailboxID    uint  `json:"mailbox_id"`
		UserID       uint  `json:"user_id"`
		MessageCount int64 `json:"message_count"`
		UsedBytes    int64 `json:"used_bytes"`
	}
	if err := json.Unmarshal([]byte(events[0]["metadata"].(string)), &meta); err != nil {
		t.Fatalf("decode audit metadata: %v", err)
	}
	if meta.MailboxID != mb.ID || meta.UserID != 101 || meta.MessageCount != 50 || meta.UsedBytes != 500 {
		t.Fatalf("audit metadata wrong: %+v", meta)
	}

	// The delete is gone-for-good and repeats answer 404 without auditing.
	if got := s.do(t, http.MethodDelete, fmt.Sprintf("/admin/mailboxes/%d", mb.ID), admin, "").Code; got != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", got)
	}
	if len(s.eventTypes()) != 1 {
		t.Fatalf("failed deletes must not audit, got %v", s.eventTypes())
	}
}

// TestAdminMailboxUsage — the usage endpoint surfaces the mailstore's
// counters and the dedup-aware blob totals.
func TestAdminMailboxUsage(t *testing.T) {
	s := newMailboxAdminServer(t)
	admin := logsTestJWT(t, "admin")
	s.store.seedUser(101)
	mb := mbxSeedMailbox(s.store, 101, "usage@example.test", models.MailboxStatusActive, 8192, 512)

	rec := s.do(t, http.MethodGet, fmt.Sprintf("/admin/mailboxes/%d/usage", mb.ID), admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", rec.Code, rec.Body.String())
	}
	row := mbxRow(t, rec.Body.String())
	if row["mailbox_id"].(float64) != float64(mb.ID) || row["quota_bytes"].(float64) != 8192 ||
		row["raw_bytes"].(float64) != 512 || row["message_count"].(float64) != 51 ||
		row["blob_bytes"].(float64) != 1234 || row["distinct_blobs"].(float64) != 2 {
		t.Fatalf("usage report wrong: %v", row)
	}
	if got := s.do(t, http.MethodGet, "/admin/mailboxes/424242/usage", admin, "").Code; got != http.StatusNotFound {
		t.Fatalf("unknown usage = %d, want 404", got)
	}
}
