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
	"database/sql/driver"
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
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// The RF-F075 double-opt-in handler tests run on the automation harness's
// fake database/sql driver (extended in place with the subscriber_list_members
// table, DELETE statements and batch inserts). The subscription, confirmation
// and resend flows are served through the real okapi stack; the confirmation
// links round-trip through the REAL tracking service HMAC, so a confirmed
// member in these tests is one whose token actually verified.

const (
	doiTestFrom = "doi@test.example"
)

// ---------- test doubles ----------

type doiMailerStub struct {
	mu   sync.Mutex
	sent []*email.SystemDOIConfirmationRequest
}

func (m *doiMailerStub) SendSystemDOIConfirmation(_ context.Context, req *email.SystemDOIConfirmationRequest) (*email.SendResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *req
	m.sent = append(m.sent, &cp)
	return &email.SendResponse{ID: fmt.Sprintf("doi-%d", len(m.sent)), Status: models.EmailStatusQueued}, nil
}

func (m *doiMailerStub) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *doiMailerStub) last() *email.SystemDOIConfirmationRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return nil
	}
	return m.sent[len(m.sent)-1]
}

func (m *doiMailerStub) all() []*email.SystemDOIConfirmationRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*email.SystemDOIConfirmationRequest(nil), m.sent...)
}

type doiTriggerSpy struct {
	mu     sync.Mutex
	events []automation.TriggerEvent
}

func (s *doiTriggerSpy) Enroll(_ context.Context, ev automation.TriggerEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *doiTriggerSpy) firedListSubscribed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.events {
		if ev.Kind == models.AutomationEventListSubscribed {
			n++
		}
	}
	return n
}

// ---------- server + fixtures ----------

func newDOIServer(t *testing.T, state *autoHState) (*okapi.TestServer, *doiMailerStub, *doiTriggerSpy, *tracking.Service) {
	t.Helper()
	gdb := autoGorm(t, state)
	subRepo := repositories.NewSubscriberRepository(gdb)
	listRepo := repositories.NewSubscriberListRepository(gdb)

	mailer := &doiMailerStub{}
	spy := &doiTriggerSpy{}
	svc := tracking.NewService(nil, "http://doi.test/", []byte("doi-test-hmac-key"))

	listH := NewSubscriberListHandler(listRepo, subRepo)
	listH.SetAutomationTrigger(spy)
	listH.SetDOIConfirmation(mailer, svc, doiTestFrom)
	subH := NewSubscriberHandler(subRepo)
	subH.SetAutomationTrigger(spy)
	subH.SetListDOIImport(listRepo, mailer, svc, doiTestFrom)

	trackingH := NewTrackingHandler(nil, nil, nil, subRepo, listRepo, nil, nil, svc, nil)
	trackingH.SetAutomationTrigger(spy)

	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		testWorkspaceBinding,
	)
	g.Post("/subscriber-lists/subscribe", okapi.H(listH.Subscribe))
	g.Post("/subscriber-lists", okapi.H(listH.Create))
	g.Put("/subscriber-lists/{id:int}", okapi.H(listH.Update))
	g.Post("/subscriber-lists/{id:int}/members", okapi.H(listH.AddMember))
	g.Post("/subscriber-lists/{id:int}/members/{subscriberId:int}/resend-confirmation", okapi.H(listH.ResendConfirmation))
	g.Post("/subscribers/import/json", okapi.H(subH.BulkImportJSON))

	pub := o.Group("/t")
	pub.Get("/confirm/{token}", okapi.H(trackingH.DOIConfirmPage))
	pub.Post("/confirm/{token}", okapi.H(trackingH.DOIConfirm))
	return o, mailer, spy, svc
}

func doiSeedList(t *testing.T, state *autoHState, id uint, name string, doubleOptIn bool) {
	t.Helper()
	autoHSeed(t, state, "subscriber_lists", map[string]any{
		"id": id, "user_id": uint(7), "workspace_id": uint(3),
		"name": name, "type": "static", "double_opt_in": doubleOptIn,
	})
}

func doiSeedSubscriber(t *testing.T, state *autoHState, id uint, email string) {
	t.Helper()
	autoHSeed(t, state, "subscribers", map[string]any{
		"id": id, "user_id": uint(7), "workspace_id": uint(3),
		"email": email, "status": string(models.SubscriberStatusSubscribed),
	})
}

// doiMemberRows returns every membership row for one (list, subscriber) pair
// as column maps — the fake driver has no unique constraint, so the count
// itself is an assertion (a DOI re-subscribe must not duplicate the row).
func doiMemberRows(t *testing.T, state *autoHState, listID, subscriberID uint) []map[string]driver.Value {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	table := state.tables["subscriber_list_members"]
	listIdx, subIdx := -1, -1
	for i, c := range autoHColumns["subscriber_list_members"] {
		switch c {
		case "list_id":
			listIdx = i
		case "subscriber_id":
			subIdx = i
		}
	}
	var out []map[string]driver.Value
	for _, row := range table.rows {
		if row[listIdx] == int64(listID) && row[subIdx] == int64(subscriberID) {
			m := make(map[string]driver.Value, len(row))
			for i, c := range autoHColumns["subscriber_list_members"] {
				m[c] = row[i]
			}
			out = append(out, m)
		}
	}
	return out
}

func doiDoReq(t *testing.T, o *okapi.TestServer, method, path, workspace string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if workspace != "" {
		req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
		req.Header.Set("X-Test-Workspace", workspace)
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

func doiSubscribeBody(email, list string) io.Reader {
	return strings.NewReader(fmt.Sprintf(`{"email":%q,"list":%q}`, email, list))
}

func doiMemberStatus(t *testing.T, state *autoHState, listID, subscriberID uint) (string, int) {
	t.Helper()
	rows := doiMemberRows(t, state, listID, subscriberID)
	if len(rows) == 0 {
		t.Fatalf("no membership row for (list %d, subscriber %d)", listID, subscriberID)
	}
	status, _ := rows[0]["status"].(string)
	return status, len(rows)
}

// ---------- subscribe ----------

func TestSubscribeWithoutDOISubscribesImmediately(t *testing.T) {
	state := newAutoHState()
	o, mailer, spy, _ := newDOIServer(t, state)
	doiSeedList(t, state, 5, "Plain", false)

	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscriber-lists/subscribe", autoTestWorkspace, doiSubscribeBody("a@example.com", "Plain"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data ListSubscribeResponse `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Action != "subscribed" || resp.Data.ConfirmationSent {
		t.Fatalf("action = %q confirmation_sent = %v, want subscribed/false", resp.Data.Action, resp.Data.ConfirmationSent)
	}
	if status, n := doiMemberStatus(t, state, 5, resp.Data.SubscriberID); status != string(models.MemberStatusConfirmed) || n != 1 {
		t.Fatalf("membership = %q (x%d), want confirmed (x1)", status, n)
	}
	if spy.firedListSubscribed() != 1 {
		t.Fatalf("list.subscribed fired %d times, want 1 (welcome automations must enroll)", spy.firedListSubscribed())
	}
	if mailer.count() != 0 {
		t.Fatalf("confirmation emails sent = %d, want 0 when DOI is off", mailer.count())
	}
}

func TestSubscribeWithDOICreatesPendingAndSendsConfirmation(t *testing.T) {
	state := newAutoHState()
	o, mailer, spy, _ := newDOIServer(t, state)
	doiSeedList(t, state, 6, "Members", true)

	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscriber-lists/subscribe", autoTestWorkspace, doiSubscribeBody("b@example.com", "Members"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data ListSubscribeResponse `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Action != "pending_confirmation" || !resp.Data.ConfirmationSent {
		t.Fatalf("action = %q confirmation_sent = %v, want pending_confirmation/true", resp.Data.Action, resp.Data.ConfirmationSent)
	}
	if status, n := doiMemberStatus(t, state, 6, resp.Data.SubscriberID); status != string(models.MemberStatusPending) || n != 1 {
		t.Fatalf("membership = %q (x%d), want pending (x1)", status, n)
	}
	if got := spy.firedListSubscribed(); got != 0 {
		t.Fatalf("list.subscribed fired %d times before confirmation, want 0", got)
	}
	if mailer.count() != 1 {
		t.Fatalf("confirmation emails = %d, want 1", mailer.count())
	}
	msg := mailer.last()
	if msg == nil || !strings.Contains(msg.Subject, "Members") || !strings.Contains(msg.HTML, "/t/confirm/") {
		t.Fatalf("confirmation email missing list name or /t/confirm/ link: %+v", msg)
	}
	if msg.From != doiTestFrom || len(msg.To) != 1 || msg.To[0] != "b@example.com" {
		t.Fatalf("confirmation envelope from=%q to=%v, want %q/[b@example.com]", msg.From, msg.To, doiTestFrom)
	}
}

func TestSubscribeWithDOIRepeatResendsWithoutDuplicate(t *testing.T) {
	state := newAutoHState()
	o, mailer, spy, _ := newDOIServer(t, state)
	doiSeedList(t, state, 6, "Members", true)

	for i := 0; i < 2; i++ {
		rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscriber-lists/subscribe", autoTestWorkspace, doiSubscribeBody("c@example.com", "Members"))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d (body %s)", i, rec.Code, rec.Body.String())
		}
	}
	if mailer.count() != 2 {
		t.Fatalf("confirmation emails = %d, want 2 (re-subscribe re-sends)", mailer.count())
	}
	if got := spy.firedListSubscribed(); got != 0 {
		t.Fatalf("list.subscribed fired %d times, want 0", got)
	}
	var members int
	state.mu.Lock()
	for range state.tables["subscriber_list_members"].rows {
		members++
	}
	state.mu.Unlock()
	if members != 1 {
		t.Fatalf("membership rows = %d, want 1 (repeat subscribe must not duplicate)", members)
	}
}

// ---------- hosted confirmation ----------

func doiConfirmToken(t *testing.T, o *okapi.TestServer, svc *tracking.Service, listID uint, email, list string) (subscriberID uint, token string) {
	t.Helper()
	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscriber-lists/subscribe", autoTestWorkspace, doiSubscribeBody(email, list))
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe: status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			SubscriberID uint   `json:"subscriber_id"`
			ListID       uint   `json:"list_id"`
			Action       string `json:"action"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Action != "pending_confirmation" {
		t.Fatalf("action = %q, want pending_confirmation", resp.Data.Action)
	}
	tok := svc.SignDOIConfirmToken(listID, resp.Data.SubscriberID, 72*time.Hour)
	return resp.Data.SubscriberID, tok
}

func TestDOIConfirmEndToEnd(t *testing.T) {
	state := newAutoHState()
	o, mailer, spy, svc := newDOIServer(t, state)
	doiSeedList(t, state, 6, "Members", true)

	subscriberID, token := doiConfirmToken(t, o, svc, 6, "d@example.com", "Members")
	status, _ := doiMemberStatus(t, state, 6, subscriberID)
	if status != string(models.MemberStatusPending) {
		t.Fatalf("pre-confirm membership = %q, want pending", status)
	}

	// GET renders the consent page (no side effects).
	page := doiDoReq(t, o, http.MethodGet, "/t/confirm/"+token, "", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("GET page status = %d (body %s)", page.Code, page.Body.String())
	}
	pageBody := page.Body.String()
	if !strings.Contains(pageBody, "d@example.com") || !strings.Contains(pageBody, "Members") || !strings.Contains(pageBody, `method="POST"`) {
		t.Fatal("confirm page must show the recipient, the list name and the POST form")
	}
	if status, _ := doiMemberStatus(t, state, 6, subscriberID); status != string(models.MemberStatusPending) {
		t.Fatal("the GET must not confirm the membership (email-scanner prefetch safety)")
	}

	// POST confirms, fires list.subscribed, and stamps consent evidence.
	done := doiDoReq(t, o, http.MethodPost, "/t/confirm/"+token, "", nil)
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), "Subscription confirmed") {
		t.Fatalf("POST status = %d body %q, want a success notice", done.Code, done.Body.String())
	}
	row := doiMemberRows(t, state, 6, subscriberID)[0]
	if row["status"] != string(models.MemberStatusConfirmed) {
		t.Fatalf("post-confirm membership = %v, want confirmed", row["status"])
	}
	if _, ok := row["confirmed_at"].(time.Time); !ok {
		t.Fatalf("confirmed_at = %v, want a timestamp (consent evidence)", row["confirmed_at"])
	}
	if got := spy.firedListSubscribed(); got != 1 {
		t.Fatalf("list.subscribed fired %d times after confirmation, want 1", got)
	}

	// A repeat click is idempotent: no second trigger, no error.
	again := doiDoReq(t, o, http.MethodPost, "/t/confirm/"+token, "", nil)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), "Already confirmed") {
		t.Fatalf("repeat POST status = %d body %q, want the already-confirmed notice", again.Code, again.Body.String())
	}
	if got := spy.firedListSubscribed(); got != 1 {
		t.Fatalf("list.subscribed fired %d times after repeat, want 1", got)
	}
	if mailer.count() != 1 {
		t.Fatalf("confirmation emails = %d, want 1 (no re-send on confirm)", mailer.count())
	}
}

func TestDOIConfirmExpiredAndUnknownTokensLookIdentical(t *testing.T) {
	state := newAutoHState()
	o, _, _, svc := newDOIServer(t, state)
	doiSeedList(t, state, 6, "Members", true)
	doiSeedSubscriber(t, state, 8, "e@example.com")

	expired := doiDoReq(t, o, http.MethodGet, "/t/confirm/"+svc.SignDOIConfirmToken(6, 8, -time.Minute), "", nil)
	unknown := doiDoReq(t, o, http.MethodGet, "/t/confirm/"+svc.SignDOIConfirmToken(6, 8, time.Hour), "", nil)
	if expired.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d/%d, want 404/404", expired.Code, unknown.Code)
	}
	if expired.Body.String() != unknown.Body.String() {
		t.Fatal("an unknown membership must be indistinguishable from an expired token (no enumeration)")
	}

	// A structurally valid token for pairs that never existed behaves the same.
	missing := doiDoReq(t, o, http.MethodGet, "/t/confirm/"+svc.SignDOIConfirmToken(6, 999, time.Hour), "", nil)
	if missing.Code != http.StatusNotFound || missing.Body.String() != expired.Body.String() {
		t.Fatalf("missing-pair status = %d, want 404 with the identical notice", missing.Code)
	}
}

func TestDOIConfirmCannotResurrectOptedOutMembership(t *testing.T) {
	state := newAutoHState()
	o, _, _, svc := newDOIServer(t, state)
	doiSeedList(t, state, 6, "Members", true)

	subscriberID, token := doiConfirmToken(t, o, svc, 6, "f@example.com", "Members")
	if done := doiDoReq(t, o, http.MethodPost, "/t/confirm/"+token, "", nil); done.Code != http.StatusOK {
		t.Fatalf("confirm status = %d", done.Code)
	}
	// The subscriber opts out: SuppressMember deletes the membership row.
	listRepo := repositories.NewSubscriberListRepository(autoGorm(t, state))
	if err := listRepo.SuppressMember(6, subscriberID, "api"); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	after := doiDoReq(t, o, http.MethodPost, "/t/confirm/"+token, "", nil)
	if after.Code != http.StatusNotFound {
		t.Fatalf("post-optout POST status = %d, want 404 (a stale token must not resurrect withdrawn consent)", after.Code)
	}
}

// ---------- add member + resend ----------

func TestAddMemberHonorsListDOI(t *testing.T) {
	state := newAutoHState()
	o, mailer, spy, _ := newDOIServer(t, state)
	doiSeedList(t, state, 6, "Members", true)
	doiSeedSubscriber(t, state, 8, "g@example.com")

	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscriber-lists/6/members", autoTestWorkspace, strings.NewReader(`{"subscriber_id":8}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if status, _ := doiMemberStatus(t, state, 6, 8); status != string(models.MemberStatusPending) {
		t.Fatalf("membership = %q, want pending on a DOI list", status)
	}
	if mailer.count() != 1 {
		t.Fatalf("confirmation emails = %d, want 1", mailer.count())
	}
	if got := spy.firedListSubscribed(); got != 0 {
		t.Fatalf("list.subscribed fired %d times, want 0 pre-confirmation", got)
	}
}

func TestResendConfirmationLifecycle(t *testing.T) {
	state := newAutoHState()
	o, mailer, _, svc := newDOIServer(t, state)
	doiSeedList(t, state, 6, "Members", true)

	subscriberID, _ := doiConfirmToken(t, o, svc, 6, "h@example.com", "Members")
	before := mailer.count()

	resend := doiDoReq(t, o, http.MethodPost, fmt.Sprintf("/workspaces/current/subscriber-lists/6/members/%d/resend-confirmation", subscriberID), autoTestWorkspace, nil)
	if resend.Code != http.StatusOK || mailer.count() != before+1 {
		t.Fatalf("resend status = %d, emails %d -> %d, want 200 and +1", resend.Code, before, mailer.count())
	}

	// Confirming, then resending again, is rejected: consent mail to a
	// confirmed member is noise.
	doiDoReq(t, o, http.MethodPost, "/t/confirm/"+svc.SignDOIConfirmToken(6, subscriberID, time.Hour), "", nil)
	confirmed := doiDoReq(t, o, http.MethodPost, fmt.Sprintf("/workspaces/current/subscriber-lists/6/members/%d/resend-confirmation", subscriberID), autoTestWorkspace, nil)
	if confirmed.Code != http.StatusBadRequest {
		t.Fatalf("resend on confirmed membership status = %d, want 400", confirmed.Code)
	}

	// A non-member and an unknown list are 404s.
	nonMember := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscriber-lists/6/members/999/resend-confirmation", autoTestWorkspace, nil)
	if nonMember.Code != http.StatusNotFound {
		t.Fatalf("resend for non-member status = %d, want 404", nonMember.Code)
	}
	wrongList := doiDoReq(t, o, http.MethodPost, fmt.Sprintf("/workspaces/current/subscriber-lists/999/members/%d/resend-confirmation", subscriberID), autoTestWorkspace, nil)
	if wrongList.Code != http.StatusNotFound {
		t.Fatalf("resend for unknown list status = %d, want 404", wrongList.Code)
	}
}

// ---------- list settings ----------

func TestCreateAndUpdateDoubleOptIn(t *testing.T) {
	state := newAutoHState()
	o, _, _, _ := newDOIServer(t, state)

	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscriber-lists", autoTestWorkspace, strings.NewReader(`{"name":"DOI List","double_opt_in":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			ID          uint `json:"id"`
			DoubleOptIn bool `json:"double_opt_in"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !created.Data.DoubleOptIn {
		t.Fatal("created list must carry double_opt_in = true")
	}

	// An update WITHOUT the field must not silently turn DOI off (the *bool
	// contract), while an explicit false does.
	rec = doiDoReq(t, o, http.MethodPut, fmt.Sprintf("/workspaces/current/subscriber-lists/%d", created.Data.ID), autoTestWorkspace, strings.NewReader(`{"description":"renamed"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d", rec.Code)
	}
	var updated struct {
		Data struct {
			DoubleOptIn bool `json:"double_opt_in"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !updated.Data.DoubleOptIn {
		t.Fatal("an update omitting double_opt_in must keep it enabled")
	}
	rec = doiDoReq(t, o, http.MethodPut, fmt.Sprintf("/workspaces/current/subscriber-lists/%d", created.Data.ID), autoTestWorkspace, strings.NewReader(`{"double_opt_in":false}`))
	if err := jsonUnmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.Data.DoubleOptIn {
		t.Fatal("an explicit double_opt_in=false update must disable it")
	}
}

// ---------- imports ----------

func TestImportJSONWithListDOIOnCreatesPendingAndMails(t *testing.T) {
	state := newAutoHState()
	o, mailer, spy, _ := newDOIServer(t, state)
	doiSeedList(t, state, 9, "Imported", true)

	payload := `{"subscribers":[{"email":"i1@example.com"},{"email":"i2@example.com"}],"list_id":9}`
	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscribers/import/json", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Created           int  `json:"created"`
			MembersAdded      int  `json:"members_added"`
			ConfirmationsSent int  `json:"confirmations_sent"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Created != 2 || resp.Data.MembersAdded != 2 || resp.Data.ConfirmationsSent != 2 {
		t.Fatalf("counters = %+v, want created 2, members_added 2, confirmations_sent 2", resp.Data)
	}
	if mailer.count() != 2 {
		t.Fatalf("confirmation emails = %d, want 2", mailer.count())
	}
	if got := spy.firedListSubscribed(); got != 0 {
		t.Fatalf("list.subscribed fired %d times for pending imports, want 0", got)
	}
	state.mu.Lock()
	statusIdx := 0
	for i, c := range autoHColumns["subscriber_list_members"] {
		if c == "status" {
			statusIdx = i
		}
	}
	pending := 0
	for _, row := range state.tables["subscriber_list_members"].rows {
		if row[statusIdx] == string(models.MemberStatusPending) {
			pending++
		}
	}
	state.mu.Unlock()
	if pending != 2 {
		t.Fatalf("pending memberships = %d, want 2", pending)
	}
}

func TestImportJSONWithListDOIOffImportsConfirmed(t *testing.T) {
	state := newAutoHState()
	o, mailer, _, _ := newDOIServer(t, state)
	doiSeedList(t, state, 9, "Imported", false)

	payload := `{"subscribers":[{"email":"j1@example.com"},{"email":"j2@example.com"}],"list_id":9}`
	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscribers/import/json", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			MembersAdded      int `json:"members_added"`
			ConfirmationsSent int `json:"confirmations_sent"`
		} `json:"data"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.MembersAdded != 2 || resp.Data.ConfirmationsSent != 0 {
		t.Fatalf("counters = %+v, want members_added 2, confirmations_sent 0", resp.Data)
	}
	if mailer.count() != 0 {
		t.Fatalf("confirmation emails = %d, want 0 when the list has no DOI", mailer.count())
	}
	state.mu.Lock()
	statusIdx := 0
	for i, c := range autoHColumns["subscriber_list_members"] {
		if c == "status" {
			statusIdx = i
		}
	}
	confirmed := 0
	for _, row := range state.tables["subscriber_list_members"].rows {
		if row[statusIdx] == string(models.MemberStatusConfirmed) {
			confirmed++
		}
	}
	state.mu.Unlock()
	if confirmed != 2 {
		t.Fatalf("confirmed memberships = %d, want 2", confirmed)
	}
}

// A duplicate email (a contact that already exists) must still get a
// CONFIRMABLE link: the token must bind to the existing contact's id, never
// to the zero id of the skipped BulkCreate row (whose confirm page 404s
// forever).
func TestImportDuplicateEmailGetsResolvableToken(t *testing.T) {
	state := newAutoHState()
	o, mailer, _, svc := newDOIServer(t, state)
	doiSeedList(t, state, 9, "Imported", true)
	doiSeedSubscriber(t, state, 20, "dup@example.com")

	payload := `{"subscribers":[{"email":"fresh@example.com"},{"email":"dup@example.com"}],"list_id":9}`
	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscribers/import/json", autoTestWorkspace, strings.NewReader(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if mailer.count() != 2 {
		t.Fatalf("confirmation emails = %d, want 2 (duplicates join the list too)", mailer.count())
	}
	resolvable := 0
	for i, msg := range mailer.all() {
		token := doiTokenFromBody(t, msg.HTML)
		listID, subscriberID, err := svc.VerifyDOIConfirmToken(token)
		if err != nil {
			t.Fatalf("email %d: token does not verify: %v", i, err)
		}
		if listID != 9 {
			t.Fatalf("email %d: token bound to list %d, want 9", i, listID)
		}
		if subscriberID == 0 {
			t.Fatal("a confirmation token bound to subscriber 0 can never be redeemed")
		}
		if subscriberID == 20 {
			resolvable++
		}
	}
	if resolvable != 1 {
		t.Fatalf("duplicate contact's token bound to id 20 %d times, want exactly 1", resolvable)
	}
}

// doiTokenFromBody extracts the /t/confirm/ token from the email's HTML body.
func doiTokenFromBody(t *testing.T, htmlBody string) string {
	t.Helper()
	const marker = "/t/confirm/"
	i := strings.Index(htmlBody, marker)
	if i < 0 {
		t.Fatalf("no confirm URL in body: %.200s", htmlBody)
	}
	rest := htmlBody[i+len(marker):]
	end := strings.IndexAny(rest, "\"'< ")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func TestImportWithUnknownListAbortsBeforeSideEffects(t *testing.T) {
	state := newAutoHState()
	o, _, _, _ := newDOIServer(t, state)

	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscribers/import/json", autoTestWorkspace, strings.NewReader(`{"subscribers":[{"email":"k1@example.com"}],"list_id":1234}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown target list", rec.Code)
	}
	state.mu.Lock()
	subs, members := len(state.tables["subscribers"].rows), len(state.tables["subscriber_list_members"].rows)
	state.mu.Unlock()
	if subs != 0 || members != 0 {
		t.Fatalf("a rejected import must not create contacts or memberships, got %d/%d", subs, members)
	}
}

func TestImportIntoDynamicListRejected(t *testing.T) {
	state := newAutoHState()
	o, _, _, _ := newDOIServer(t, state)
	autoHSeed(t, state, "subscriber_lists", map[string]any{
		"id": uint(10), "user_id": uint(7), "workspace_id": uint(3),
		"name": "Dynamic", "type": "dynamic", "double_opt_in": false,
	})
	rec := doiDoReq(t, o, http.MethodPost, "/workspaces/current/subscribers/import/json", autoTestWorkspace, strings.NewReader(`{"subscribers":[{"email":"l1@example.com"}],"list_id":10}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a dynamic target list", rec.Code)
	}
}
