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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/jkaninda/okapi"
)

// OB-5 W1 webmail REST battery (ADR-005 decision 1) over the fake-driver
// harness (mail_fake_driver_test.go): real mailstore.Service + real
// MailHandler + real JWT middleware, everything HTTP-level exercised through
// the envelope. Isolation is the headline: another user's mailbox, folder,
// message and attachment read as 404 — never as a forbidden.

const mhTestSecret = "mail-handler-test-secret"

const (
	mhAliceID = 101
	mhBobID   = 102
)

// mhHarness wires the handler onto the okapi test server under the same
// gating as production routes (jwtOnly; no workspace middleware — personal
// scope) and holds fixtures for both users.
type mhHarness struct {
	f     *mhFixture
	o     *okapi.TestServer
	alice *mhUser
	bob   *mhUser
}

type mhUser struct {
	id    uint
	mb    *models.Mailbox
	inbox *models.MailFolder
	token string
}

func newMHHarness(t *testing.T) *mhHarness {
	t.Helper()
	f := newMHFixture(t)
	o := okapi.NewTestServer(t)
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: mhTestSecret})
	g := o.Group("/api/v1/mail", jwtAuth.Middleware)
	g.Get("/mailboxes", f.h.ListMailboxes)
	g.Get("/folders", okapi.H(f.h.ListFolders))
	g.Post("/folders", okapi.H(f.h.CreateFolder))
	g.Patch("/folders/{id:int}", okapi.H(f.h.PatchFolder))
	g.Delete("/folders/{id:int}", okapi.H(f.h.DeleteFolder))
	g.Post("/folders/{id:int}/expunge", okapi.H(f.h.ExpungeFolder))
	g.Get("/messages", okapi.H(f.h.ListMessages))
	g.Get("/messages/{uid:int}", okapi.H(f.h.GetMessage))
	g.Get("/messages/{uid:int}/attachments/{index:int}", okapi.H(f.h.DownloadAttachment))
	g.Post("/messages/{uid:int}/flags", okapi.H(f.h.UpdateFlags))
	g.Post("/messages/{uid:int}/move", okapi.H(f.h.MoveMessage))
	g.Post("/messages/{uid:int}/copy", okapi.H(f.h.CopyMessage))
	g.Delete("/messages/{uid:int}", okapi.H(f.h.DeleteMessage))
	g.Post("/search", okapi.H(f.h.Search))
	return &mhHarness{f: f, o: o}
}

// seedUser provisions a mailbox (INBOX included) and mints a session token.
func (h *mhHarness) seedUser(t *testing.T, userID uint, name string, quotaBytes int64) *mhUser {
	t.Helper()
	mb, err := h.f.svc.CreateMailbox(userID, nil, name, quotaBytes)
	if err != nil {
		t.Fatalf("seed mailbox %s: %v", name, err)
	}
	inbox, err := h.f.svc.GetFolderByPath(mb.ID, userID, 0, "INBOX")
	if err != nil {
		t.Fatalf("seed INBOX of %s: %v", name, err)
	}
	tok, err := okapi.GenerateJwtToken([]byte(mhTestSecret), map[string]any{
		"sub":   userID,
		"email": name + "@mail.example",
		"role":  "user",
		"aud":   "posta",
		"jti":   fmt.Sprintf("mh-%d", userID),
	}, time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return &mhUser{id: userID, mb: mb, inbox: inbox, token: tok}
}

func (h *mhHarness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.o.ServeHTTP(rec, req)
	return rec
}

// mhAppend ingests one raw message into a folder through the service (the
// delivery seam — the REST surface has no append endpoint; compose is W3).
func (h *mhHarness) mhAppend(t *testing.T, u *mhUser, folderID uint, subject, textBody string, atts ...mailstore.ParsedAttachment) *mailstore.AppendResult {
	t.Helper()
	raw := mhRawMessage(subject, "sender@example.com", u.mb.Name, textBody, atts)
	res, err := h.f.svc.Append(context.Background(), u.id, 0, mailstore.AppendIn{
		FolderID: folderID,
		Raw:      raw,
	})
	if err != nil {
		t.Fatalf("append %q: %v", subject, err)
	}
	return res
}

// mhRawMessage builds a minimal RFC 5322 message, optionally multipart with
// a text part, an HTML part (carrying the sanitizer's test payload) and
// attachments.
func mhRawMessage(subject, from, to, text string, atts []mailstore.ParsedAttachment) []byte {
	if len(atts) == 0 {
		return []byte("Message-ID: <mh-" + subject + "@test>\r\n" +
			"From: " + from + "\r\n" +
			"To: " + to + "\r\n" +
			"Subject: " + subject + "\r\n" +
			"Date: Mon, 02 Nov 2026 10:00:00 +0000\r\n" +
			"\r\n" +
			text + "\r\n")
	}
	boundary := "mh-boundary-42"
	var b strings.Builder
	fmt.Fprintf(&b, "Message-ID: <mh-%s@test>\r\n", subject)
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: Mon, 02 Nov 2026 10:00:00 +0000\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"mh-alt-7\"\r\n\r\n")
	b.WriteString("--mh-alt-7\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(text + "\r\n")
	b.WriteString("--mh-alt-7\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
	b.WriteString(mhEvilHTML + "\r\n")
	b.WriteString("--mh-alt-7--\r\n")
	for _, a := range atts {
		b.WriteString("--" + boundary + "\r\n")
		fmt.Fprintf(&b, "Content-Type: %s; name=\"%s\"\r\n", a.ContentType, a.Filename)
		b.WriteString("Content-Disposition: attachment; filename=\"" + a.Filename + "\"\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(base64.StdEncoding.EncodeToString(a.Content) + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}

// mhEvilHTML is the sanitizer payload: scripts, handlers, remote beacons.
const mhEvilHTML = `<div onclick="steal()"><p>Hello <b>world</b></p>` +
	`<script>alert(1)</script>` +
	`<img src="https://tracker.example/pixel.gif" alt="beacon">` +
	`<img src="data:image/png;base64,iVBORw0KGgo=" alt="inline">` +
	`<a href="javascript:evil()">bad link</a>` +
	`<a href="https://example.com/ok">ok link</a>` +
	`<iframe src="https://evil.example"></iframe>` +
	`<style>@import url(https://evil.example/x.css); p { color: red; background: url(https://evil.example/bg.png) }</style>` +
	`</div>`

func mhB64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// decodeEnvelope pulls {"success":…, "data":…} out of a response.
func mhDecodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (bool, json.RawMessage) {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope (status %d, body %s): %v", rec.Code, rec.Body.String(), err)
	}
	return env.Success, env.Data
}

// TestMail_Mailboxes_ListWithUsage — GET /mail/mailboxes lists only the
// session user's mailboxes with the ADR-003 §5 quota/usage counters filled.
func TestMail_Mailboxes_ListWithUsage(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 1<<20)
	h.seedUser(t, mhBobID, "bob@mail.example", 1<<20)
	h.mhAppend(t, alice, alice.inbox.ID, "Welcome", "plain text body")

	rec := h.do(t, http.MethodGet, "/api/v1/mail/mailboxes", alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (body %s)", rec.Code, rec.Body.String())
	}
	success, data := mhDecodeEnvelope(t, rec)
	if !success {
		t.Fatalf("envelope success = false (body %s)", rec.Body.String())
	}
	var mbs []MailboxResponse
	if err := json.Unmarshal(data, &mbs); err != nil {
		t.Fatalf("decode mailboxes: %v", err)
	}
	if len(mbs) != 1 {
		t.Fatalf("alice sees %d mailboxes, want exactly her own 1", len(mbs))
	}
	mb := mbs[0]
	if mb.Name != "alice@mail.example" {
		t.Fatalf("mailbox name = %q", mb.Name)
	}
	if mb.QuotaBytes != 1<<20 {
		t.Fatalf("quota_bytes = %d, want %d", mb.QuotaBytes, int64(1<<20))
	}
	if mb.MessageCount != 1 || mb.UsedBytes <= 0 || mb.BlobBytes <= 0 || mb.DistinctBlobs <= 0 {
		t.Fatalf("usage counters not surfaced: %+v", mb)
	}
}

// TestMail_Folders_CRUD — create, list, rename, subscribe toggle, delete.
func TestMail_Folders_CRUD(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)

	// Create under the root.
	rec := h.do(t, http.MethodPost, "/api/v1/mail/folders", alice.token,
		fmt.Sprintf(`{"mailbox":%d,"name":"Work"}`, alice.mb.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data := mhDecodeEnvelope(t, rec)
	var created MailFolderResponse
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatalf("decode created folder: %v", err)
	}
	if created.IMAPPath != "Work" || created.Name != "Work" {
		t.Fatalf("created folder = %+v", created)
	}

	// Create under a parent path.
	rec = h.do(t, http.MethodPost, "/api/v1/mail/folders", alice.token,
		fmt.Sprintf(`{"mailbox":%d,"name":"2026","parent_path":"Work"}`, alice.mb.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("nested create status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data = mhDecodeEnvelope(t, rec)
	var nested MailFolderResponse
	_ = json.Unmarshal(data, &nested)
	if nested.IMAPPath != "Work/2026" {
		t.Fatalf("nested path = %q, want Work/2026", nested.IMAPPath)
	}

	// List shows INBOX + both.
	rec = h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/folders?mailbox=%d", alice.mb.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data = mhDecodeEnvelope(t, rec)
	var folders []MailFolderResponse
	_ = json.Unmarshal(data, &folders)
	if len(folders) != 3 {
		t.Fatalf("folder count = %d, want 3 (INBOX, Work, Work/2026)", len(folders))
	}

	// Rename rewrites the descendant path.
	rec = h.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/mail/folders/%d", created.ID), alice.token,
		`{"name":"Business"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data = mhDecodeEnvelope(t, rec)
	var renamed MailFolderResponse
	_ = json.Unmarshal(data, &renamed)
	if renamed.IMAPPath != "Business" {
		t.Fatalf("renamed path = %q, want Business", renamed.IMAPPath)
	}

	// Unsubscribe.
	rec = h.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/mail/folders/%d", created.ID), alice.token,
		`{"subscribed":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe toggle status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data = mhDecodeEnvelope(t, rec)
	var unsub MailFolderResponse
	_ = json.Unmarshal(data, &unsub)
	if unsub.Subscribed {
		t.Fatal("subscribed should be false after the toggle")
	}

	// Duplicate path → 409; INBOX rename → 400.
	rec = h.do(t, http.MethodPost, "/api/v1/mail/folders", alice.token,
		fmt.Sprintf(`{"mailbox":%d,"name":"Business"}`, alice.mb.ID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want 409", rec.Code)
	}
	inboxID := alice.inbox.ID
	rec = h.do(t, http.MethodPatch, fmt.Sprintf("/api/v1/mail/folders/%d", inboxID), alice.token,
		`{"name":"Nope"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("INBOX rename status = %d, want 400", rec.Code)
	}

	// Delete (subtree): deleting Work/2026 leaves Business; deleting Business works.
	rec = h.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/mail/folders/%d", nested.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d (body %s)", rec.Code, rec.Body.String())
	}
	rec = h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/folders?mailbox=%d", alice.mb.ID), alice.token, "")
	_, data = mhDecodeEnvelope(t, rec)
	_ = json.Unmarshal(data, &folders)
	if len(folders) != 2 {
		t.Fatalf("folder count after delete = %d, want 2", len(folders))
	}
}

func mhItoa(v uint64) string {
	return fmt.Sprintf("%d", v)
}

// TestMail_Messages_ListPaginationAndFilters — the paged list: totals,
// page cutting, unread/flagged filters, 404 on a foreign folder.
func TestMail_Messages_ListPaginationAndFilters(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	for i := 1; i <= 5; i++ {
		h.mhAppend(t, alice, alice.inbox.ID, fmt.Sprintf("Msg %d", i), fmt.Sprintf("body %d", i))
	}

	listPage := func(query string) (rows []MailMessageSummary, total int64, code int) {
		t.Helper()
		rec := h.do(t, http.MethodGet, "/api/v1/mail/messages?folder="+mhItoa(uint64(alice.inbox.ID))+query, alice.token, "")
		if rec.Code != http.StatusOK {
			return nil, 0, rec.Code
		}
		var env struct {
			Success  bool                 `json:"success"`
			Data     []MailMessageSummary `json:"data"`
			Pageable struct {
				CurrentPage   int   `json:"current_page"`
				TotalElements int64 `json:"total_elements"`
			} `json:"pageable"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return env.Data, env.Pageable.TotalElements, rec.Code
	}

	rows, total, code := listPage("&page=1&size=2")
	if code != http.StatusOK || total != 5 || len(rows) != 2 {
		t.Fatalf("page 1: code=%d total=%d rows=%d, want 200/5/2", code, total, len(rows))
	}
	rows, total, _ = listPage("&page=3&size=2")
	if total != 5 || len(rows) != 1 {
		t.Fatalf("page 3: total=%d rows=%d, want 5/1", total, len(rows))
	}
	if rows[0].Subject != "Msg 5" {
		t.Fatalf("page 3 row = %q, want Msg 5 (UID order)", rows[0].Subject)
	}
	if len(rows[0].FlagsList) == 0 || rows[0].FlagsList[0] != "recent" {
		t.Fatalf("flags_list = %v, want [recent ...]", rows[0].FlagsList)
	}

	// Mark Msg 1 seen + Msg 2 flagged through the flags endpoint.
	flagsBody := func(uid uint32, body string) *httptest.ResponseRecorder {
		return h.do(t, http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/flags?folder=%d", uid, alice.inbox.ID), alice.token, body)
	}
	if rec := flagsBody(1, `{"seen":true}`); rec.Code != http.StatusOK {
		t.Fatalf("seen flag status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if rec := flagsBody(2, `{"flagged":true}`); rec.Code != http.StatusOK {
		t.Fatalf("flagged flag status = %d (body %s)", rec.Code, rec.Body.String())
	}
	rows, total, _ = listPage("&unread=true")
	if total != 4 {
		t.Fatalf("unread total = %d, want 4", total)
	}
	for _, r := range rows {
		for _, f := range r.FlagsList {
			if f == "seen" {
				t.Fatalf("unread filter returned a seen message: %+v", r)
			}
		}
	}
	rows, total, _ = listPage("&flagged=true")
	if total != 1 || len(rows) != 1 || rows[0].UID != 2 {
		t.Fatalf("flagged filter: total=%d rows=%+v, want exactly UID 2", total, rows)
	}

	// A foreign folder reads as 404.
	bob := h.seedUser(t, mhBobID, "bob@mail.example", 0)
	rec := h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages?folder=%d", alice.inbox.ID), bob.token, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign folder list status = %d, want 404", rec.Code)
	}
}

// TestMail_Message_ViewSanitizedBodyAndAttachments — the message view:
// metadata, sanitized HTML (scripts/handlers/remote loads gone, content and
// inline images kept), text fallback, attachment list, auto-\Seen.
func TestMail_Message_ViewSanitizedBodyAndAttachments(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	att := mailstore.ParsedAttachment{Filename: "report.txt", ContentType: "text/plain", Content: []byte("ATTACHMENT-BYTES-123")}
	res := h.mhAppend(t, alice, alice.inbox.ID, "Invoice", "see attached", att)

	rec := h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages/%d?folder=%d", res.UID, alice.inbox.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("view status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data := mhDecodeEnvelope(t, rec)
	var msg MailMessageResponse
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.Subject != "Invoice" || msg.From != "sender@example.com" || msg.UID != res.UID {
		t.Fatalf("metadata mismatch: %+v", msg)
	}
	if !msg.Body.HasHTML {
		t.Fatal("body.has_html = false, want true (message carries an HTML part)")
	}
	html := msg.Body.HTML
	for _, banned := range []string{"<script", "onclick", "javascript:", "<iframe", "tracker.example", "@import", "evil.example"} {
		if strings.Contains(strings.ToLower(html), strings.ToLower(banned)) {
			t.Fatalf("sanitized HTML still contains %q: %s", banned, html)
		}
	}
	for _, want := range []string{"Hello", "<b>world</b>", `data:image/png;base64,iVBORw0KGgo=`, `href="https://example.com/ok"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("sanitized HTML lost %q: %s", want, html)
		}
	}
	if !strings.Contains(msg.Body.Text, "see attached") {
		t.Fatalf("body.text = %q", msg.Body.Text)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].Index != 0 || msg.Attachments[0].Name != "report.txt" ||
		msg.Attachments[0].ContentType != "text/plain" || msg.Attachments[0].Size != int64(len("ATTACHMENT-BYTES-123")) {
		t.Fatalf("attachments = %+v", msg.Attachments)
	}
	seen := false
	for _, f := range msg.FlagsList {
		if f == "seen" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("viewing a message must mark it \\Seen (flags_list=%v)", msg.FlagsList)
	}

	// The text-only message returns no HTML at all.
	res2 := h.mhAppend(t, alice, alice.inbox.ID, "Plain", "just text")
	rec = h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages/%d?folder=%d", res2.UID, alice.inbox.ID), alice.token, "")
	_, data = mhDecodeEnvelope(t, rec)
	var plain MailMessageResponse
	_ = json.Unmarshal(data, &plain)
	if plain.Body.HasHTML || plain.Body.HTML != "" {
		t.Fatalf("text-only message grew an HTML part: %+v", plain.Body)
	}
	if !strings.Contains(plain.Body.Text, "just text") {
		t.Fatalf("text body = %q", plain.Body.Text)
	}
}

// TestMail_Message_IsolationAcrossUsers — another user's mailbox, folder,
// message, attachment, flags, move, delete and expunge all read as 404.
func TestMail_Message_IsolationAcrossUsers(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	bob := h.seedUser(t, mhBobID, "bob@mail.example", 0)
	res := h.mhAppend(t, alice, alice.inbox.ID, "Private", "secret")

	folderQ := fmt.Sprintf("?folder=%d", alice.inbox.ID)
	paths := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/mail/folders" + fmt.Sprintf("?mailbox=%d", alice.mb.ID), ""},
		{http.MethodPost, "/api/v1/mail/folders", fmt.Sprintf(`{"mailbox":%d,"name":"X"}`, alice.mb.ID)},
		{http.MethodPatch, fmt.Sprintf("/api/v1/mail/folders/%d", alice.inbox.ID), `{"subscribed":false}`},
		{http.MethodDelete, fmt.Sprintf("/api/v1/mail/folders/%d", alice.inbox.ID), ""},
		{http.MethodPost, fmt.Sprintf("/api/v1/mail/folders/%d/expunge", alice.inbox.ID), ""},
		{http.MethodGet, "/api/v1/mail/messages" + folderQ, ""},
		{http.MethodGet, fmt.Sprintf("/api/v1/mail/messages/%d%s", res.UID, folderQ), ""},
		{http.MethodGet, fmt.Sprintf("/api/v1/mail/messages/%d/attachments/0%s", res.UID, folderQ), ""},
		{http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/flags%s", res.UID, folderQ), `{"seen":true}`},
		{http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/move%s", res.UID, folderQ), fmt.Sprintf(`{"target":%d}`, bob.inbox.ID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/copy%s", res.UID, folderQ), fmt.Sprintf(`{"target":%d}`, bob.inbox.ID)},
		{http.MethodDelete, fmt.Sprintf("/api/v1/mail/messages/%d%s", res.UID, folderQ), ""},
	}
	for _, p := range paths {
		rec := h.do(t, p.method, p.path, bob.token, p.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s as bob: status = %d, want 404 (body %s)", p.method, p.path, rec.Code, rec.Body.String())
		}
	}
	// The message survives every refused mutation.
	m, err := h.f.svc.GetMessageByUID(alice.inbox.ID, res.UID, mhAliceID, 0)
	if err != nil {
		t.Fatalf("alice's message must survive bob's attempts: %v", err)
	}
	if m.Flags&models.FlagDeleted != 0 || m.Flags&models.FlagSeen != 0 {
		t.Fatalf("flags mutated across users: %d", m.Flags)
	}
}

// TestMail_Flags_CAS — if_mod_seq CAS: success when fresh, 409 when stale.
func TestMail_Flags_CAS(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	res := h.mhAppend(t, alice, alice.inbox.ID, "CAS", "body")

	flagsPath := fmt.Sprintf("/api/v1/mail/messages/%d/flags?folder=%d", res.UID, alice.inbox.ID)
	rec := h.do(t, http.MethodPost, flagsPath, alice.token, `{"flagged":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first store status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data := mhDecodeEnvelope(t, rec)
	var fr MailFlagsResponse
	if err := json.Unmarshal(data, &fr); err != nil {
		t.Fatalf("decode flags response: %v", err)
	}
	if fr.UID != res.UID || fr.Flags&models.FlagFlagged == 0 {
		t.Fatalf("flags response = %+v", fr)
	}
	m1 := fr.ModSeq

	// Fresh CAS succeeds.
	rec = h.do(t, http.MethodPost, flagsPath, alice.token, fmt.Sprintf(`{"draft":true,"if_mod_seq":%d}`, m1))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh CAS status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data = mhDecodeEnvelope(t, rec)
	var fr2 MailFlagsResponse
	_ = json.Unmarshal(data, &fr2)
	if fr2.ModSeq <= m1 {
		t.Fatalf("mod_seq must advance: %d after %d", fr2.ModSeq, m1)
	}

	// The same stale token now conflicts.
	rec = h.do(t, http.MethodPost, flagsPath, alice.token, fmt.Sprintf(`{"seen":true,"if_mod_seq":%d}`, m1))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale CAS status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}

	// Clearing works, and an empty body is a 400.
	rec = h.do(t, http.MethodPost, flagsPath, alice.token, `{"flagged":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d (body %s)", rec.Code, rec.Body.String())
	}
	rec = h.do(t, http.MethodPost, flagsPath, alice.token, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty flag store status = %d, want 400", rec.Code)
	}
}

// TestMail_QuotaRejectionSurfaced — a copy over the message-count quota
// answers 422 with the enforcer's typed explanation (the REST surface of the
// IMAP NO [OVERQUOTA] posture).
func TestMail_QuotaRejectionSurfaced(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	res := h.mhAppend(t, alice, alice.inbox.ID, "Only one", "body")
	target, err := h.f.svc.CreateFolder(alice.mb.ID, mhAliceID, 0, "", "Keep")
	if err != nil {
		t.Fatalf("create target folder: %v", err)
	}
	// QuotaMessages = 1: the mailbox already holds one message, so any copy
	// exceeds the count quota.
	if err := h.f.svc.SetMailboxMessageQuota(alice.mb.ID, mhAliceID, 0, 1); err != nil {
		t.Fatalf("set count quota: %v", err)
	}
	rec := h.do(t, http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/copy?folder=%d", res.UID, alice.inbox.ID), alice.token,
		fmt.Sprintf(`{"target":%d}`, target.ID))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-quota copy status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"quota", "message-count"} {
		if !strings.Contains(body, want) {
			t.Fatalf("quota response must surface %q (body %s)", want, body)
		}
	}
}

// TestMail_AttachmentDownload_AuthzAndType — the owner streams the stored
// bytes with the ingested content type; the foreign user gets 404; a bad
// index gets 404; an HTML attachment never renders inline.
func TestMail_AttachmentDownload_AuthzAndType(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	bob := h.seedUser(t, mhBobID, "bob@mail.example", 0)
	att := mailstore.ParsedAttachment{Filename: "data.bin", ContentType: "application/pdf", Content: []byte("PDF-BYTES-9876")}
	res := h.mhAppend(t, alice, alice.inbox.ID, "With attachment", "body", att)

	dl := func(token string, idx int) *httptest.ResponseRecorder {
		t.Helper()
		return h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages/%d/attachments/%d?folder=%d", res.UID, idx, alice.inbox.ID), token, "")
	}
	rec := dl(alice.token, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "PDF-BYTES-9876" {
		t.Fatalf("downloaded bytes = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content type = %q, want the ingested application/pdf", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("content disposition = %q, want attachment", cd)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("X-Content-Type-Options: nosniff must be set")
	}
	if rec := dl(bob.token, 0); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign download status = %d, want 404", rec.Code)
	}
	if rec := dl(alice.token, 9); rec.Code != http.StatusNotFound {
		t.Fatalf("bad index status = %d, want 404", rec.Code)
	}

	// An HTML attachment is forced to a non-renderable content type.
	htmlAtt := mailstore.ParsedAttachment{Filename: "page.html", ContentType: "text/html", Content: []byte("<p>x</p>")}
	res2 := h.mhAppend(t, alice, alice.inbox.ID, "HTML attachment", "body", htmlAtt)
	rec = h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages/%d/attachments/0?folder=%d", res2.UID, alice.inbox.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("html attachment status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("html attachment content type = %q, want application/octet-stream", ct)
	}
}

// TestMail_MoveCopyDeleteExpunge — the full lifecycle: move re-addresses by
// UID, copy duplicates, delete marks, expunge purges.
func TestMail_MoveCopyDeleteExpunge(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	r1 := h.mhAppend(t, alice, alice.inbox.ID, "One", "body one")
	r2 := h.mhAppend(t, alice, alice.inbox.ID, "Two", "body two")
	keep, err := h.f.svc.CreateFolder(alice.mb.ID, mhAliceID, 0, "", "Keep")
	if err != nil {
		t.Fatalf("create Keep: %v", err)
	}

	// Move One → Keep: new folder + fresh per-folder UID; source loses it.
	rec := h.do(t, http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/move?folder=%d", r1.UID, alice.inbox.ID), alice.token,
		fmt.Sprintf(`{"target":%d}`, keep.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("move status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data := mhDecodeEnvelope(t, rec)
	var mv MailMoveResponse
	if err := json.Unmarshal(data, &mv); err != nil {
		t.Fatalf("decode move: %v", err)
	}
	if mv.FolderID != keep.ID || mv.MessageID != r1.MessageID {
		t.Fatalf("move result = %+v (source %+v)", mv, r1)
	}

	// Copy Two → Keep: both folders now hold Two; Keep holds 2 rows.
	rec = h.do(t, http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/copy?folder=%d", r2.UID, alice.inbox.ID), alice.token,
		fmt.Sprintf(`{"target":%d}`, keep.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("copy status = %d (body %s)", rec.Code, rec.Body.String())
	}
	keepList := func() int {
		t.Helper()
		rec := h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages?folder=%d", keep.ID), alice.token, "")
		var env struct {
			Data     []MailMessageSummary `json:"data"`
			Pageable struct {
				TotalElements int64 `json:"total_elements"`
			} `json:"pageable"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return int(env.Pageable.TotalElements)
	}
	if n := keepList(); n != 2 {
		t.Fatalf("Keep holds %d messages after copy, want 2", n)
	}

	// Delete Two in INBOX → \Deleted; expunge purges exactly it.
	rec = h.do(t, http.MethodDelete, fmt.Sprintf("/api/v1/mail/messages/%d?folder=%d", r2.UID, alice.inbox.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d (body %s)", rec.Code, rec.Body.String())
	}
	rec = h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages?folder=%d", alice.inbox.ID), alice.token, "")
	var env struct {
		Data []MailMessageSummary `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if len(env.Data) != 1 || env.Data[0].UID != r2.UID {
		t.Fatalf("INBOX after delete = %+v, want the deleted row still listed", env.Data)
	}
	rec = h.do(t, http.MethodPost, fmt.Sprintf("/api/v1/mail/folders/%d/expunge", alice.inbox.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expunge status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_, data = mhDecodeEnvelope(t, rec)
	var ex MailExpungeResponse
	if err := json.Unmarshal(data, &ex); err != nil {
		t.Fatalf("decode expunge: %v", err)
	}
	if ex.Expunged != 1 || len(ex.UIDs) != 1 || ex.UIDs[0] != r2.UID {
		t.Fatalf("expunge result = %+v, want exactly UID %d", ex, r2.UID)
	}
	if n := keepList(); n != 2 {
		t.Fatalf("expunge of INBOX must not touch Keep (n=%d)", n)
	}
}

// TestMail_ListAndSearch — the FTS paths: the list endpoint's q parameter
// (through the IMAP search tree) and POST /mail/search (through the plain
// Searcher), both with pagination, flag combination and scoping.
func TestMail_ListAndSearch(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	h.mhAppend(t, alice, alice.inbox.ID, "Invoice from Acme", "please pay invoice 42")
	h.mhAppend(t, alice, alice.inbox.ID, "Holiday", "out of office")
	hit := h.mhAppend(t, alice, alice.inbox.ID, "Receipt", "receipt for invoice 42")

	// Fake Searcher: substring match over subject+body, honoring the folder
	// narrow and limit/offset. The real FTS tier (websearch_to_tsquery,
	// ranking, isolation) is proven against real Postgres in the mailstore
	// package's PG-gated suite; here the plumbing is under test.
	searcher := &mhFakeSearcher{state: h.f.state}
	h.f.svc.SetSearcher(searcher)

	// POST /mail/search: 2 hits, paged.
	rec := h.do(t, http.MethodPost, "/api/v1/mail/search", alice.token,
		fmt.Sprintf(`{"mailbox":%d,"q":"invoice","page":1,"size":1}`, alice.mb.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var env struct {
		Success  bool                 `json:"success"`
		Data     []MailMessageSummary `json:"data"`
		Pageable struct {
			CurrentPage   int   `json:"current_page"`
			TotalElements int64 `json:"total_elements"`
		} `json:"pageable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if env.Pageable.TotalElements != 2 || len(env.Data) != 1 {
		t.Fatalf("search page = %d rows, total %d, want 1/2", len(env.Data), env.Pageable.TotalElements)
	}

	// Folder narrowing rides through to the searcher scope.
	rec = h.do(t, http.MethodPost, "/api/v1/mail/search", alice.token,
		fmt.Sprintf(`{"folder":%d,"q":"invoice"}`, alice.inbox.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("folder search status = %d", rec.Code)
	}
	if !searcher.sawFolderNarrow {
		t.Fatal("the searcher must receive the folder narrow")
	}

	// List endpoint with q: through SearchIMAP's tree (flag keys + BODY key).
	rec = h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages?folder=%d&q=invoice", alice.inbox.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("q list status = %d (body %s)", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Pageable.TotalElements != 2 {
		t.Fatalf("q list total = %d, want 2", env.Pageable.TotalElements)
	}

	// q + flagged: the Receipt is flagged, the Invoice is not.
	if _, err := h.f.svc.SetFlags(mhAliceID, 0, mailstore.FlagsUpdate{
		MessageID: hit.MessageID, Set: models.FlagFlagged,
	}); err != nil {
		t.Fatalf("flag receipt: %v", err)
	}
	rec = h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages?folder=%d&q=invoice&flagged=true", alice.inbox.ID), alice.token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("q+flagged list status = %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Pageable.TotalElements != 1 || len(env.Data) != 1 || env.Data[0].UID != hit.UID {
		t.Fatalf("q+flagged = total %d rows %+v, want exactly the Receipt", env.Pageable.TotalElements, env.Data)
	}

	// Another user's mailbox yields an empty, non-leaking result.
	bob := h.seedUser(t, mhBobID, "bob@mail.example", 0)
	rec = h.do(t, http.MethodPost, "/api/v1/mail/search", bob.token,
		fmt.Sprintf(`{"mailbox":%d,"q":"invoice"}`, alice.mb.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign mailbox search status = %d, want 404", rec.Code)
	}

	// Search without a query is a 400; unwired searcher is a 503.
	rec = h.do(t, http.MethodPost, "/api/v1/mail/search", alice.token, `{"q":"  "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank q status = %d, want 400", rec.Code)
	}
	h.f.svc.SetSearcher(nil)
	rec = h.do(t, http.MethodPost, "/api/v1/mail/search", alice.token,
		fmt.Sprintf(`{"mailbox":%d,"q":"invoice"}`, alice.mb.ID))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired searcher status = %d, want 503", rec.Code)
	}
}

// mhFakeSearcher implements mailstore.Searcher over the fake state: substring
// match on subject + body_text, folder-narrow honored, limit/offset applied.
type mhFakeSearcher struct {
	state          *mhState
	sawFolderNarrow bool
}

func (s *mhFakeSearcher) Search(_ context.Context, scope mailstore.SearchScope, q mailstore.SearchQuery) ([]mailstore.SearchHit, int64, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	// Ownership scope check: the caller must name a mailbox it owns.
	var ownerID int64 = -1
	idxUser := s.state.colIndex("mailboxes", "user_id")
	for _, row := range s.state.tables["mailboxes"].rows {
		if id, ok := row[s.state.colIndex("mailboxes", "id")].(int64); ok && uint64(id) == uint64(scope.MailboxID) {
			ownerID = row[idxUser].(int64)
		}
	}
	if ownerID < 0 || uint64(ownerID) != uint64(scope.UserID) {
		return nil, 0, nil // foreign mailbox: empty page (isolation posture)
	}
	if q.FolderID != 0 {
		s.sawFolderNarrow = true
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	folderFilter := int64(q.FolderID)
	idxFolder := s.state.colIndex("mail_messages", "folder_id")
	idxSubject := s.state.colIndex("mail_messages", "subject")
	idxBody := s.state.colIndex("mail_messages", "body_text")
	// Folder rows must belong to the scoped mailbox for the narrow to be
	// meaningful; the fake keeps folder→mailbox via mail_folders.
	folderMB := make(map[int64]int64)
	idxFMB := s.state.colIndex("mail_folders", "mailbox_id")
	for _, row := range s.state.tables["mail_folders"].rows {
		if id, ok := row[s.state.colIndex("mail_folders", "id")].(int64); ok {
			folderMB[id] = row[idxFMB].(int64)
		}
	}
	var hits []mailstore.SearchHit
	for _, row := range s.state.tables["mail_messages"].rows {
		if q.FolderID != 0 {
			if fid, ok := row[idxFolder].(int64); !ok || fid != folderFilter {
				continue
			}
		} else if fid, ok := row[idxFolder].(int64); ok {
			if mbID, ok2 := folderMB[fid]; ok2 && mbID != int64(scope.MailboxID) {
				continue
			}
		}
		subj, _ := row[idxSubject].(string)
		body, _ := row[idxBody].(string)
		if strings.Contains(strings.ToLower(subj), text) || strings.Contains(strings.ToLower(body), text) {
			hits = append(hits, mailstore.SearchHit{
				MessageID: uint(row[s.state.colIndex("mail_messages", "id")].(int64)),
				FolderID:  uint(row[idxFolder].(int64)),
				Rank:      1,
			})
		}
	}
	total := int64(len(hits))
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if offset > len(hits) {
		hits = nil
	} else {
		hits = hits[offset:]
		if limit < len(hits) {
			hits = hits[:limit]
		}
	}
	return hits, total, nil
}

// TestMail_NilStoreAnswersUnavailable — the disabled-deployment posture:
// every endpoint answers 503 with the envelope instead of panicking.
func TestMail_NilStoreAnswersUnavailable(t *testing.T) {
	f := &MailHandler{}
	o := okapi.NewTestServer(t)
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: mhTestSecret})
	g := o.Group("/api/v1/mail", jwtAuth.Middleware)
	g.Get("/mailboxes", f.ListMailboxes)
	g.Get("/messages", okapi.H(f.ListMessages))
	tok, err := okapi.GenerateJwtToken([]byte(mhTestSecret), map[string]any{
		"sub": mhAliceID, "email": "x@example.com", "role": "user", "aud": "posta", "jti": "mh-nil",
	}, time.Hour)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	do := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		o.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := do(http.MethodGet, "/api/v1/mail/mailboxes"); got != http.StatusServiceUnavailable {
		t.Fatalf("nil-store mailboxes status = %d, want 503", got)
	}
	if got := do(http.MethodGet, "/api/v1/mail/messages?folder=1"); got != http.StatusServiceUnavailable {
		t.Fatalf("nil-store messages status = %d, want 503", got)
	}
}

// TestMail_MissingParams — addressing guards: missing folder/uid read as 400
// before any store call, unknown UIDs as 404.
func TestMail_MissingParams(t *testing.T) {
	h := newMHHarness(t)
	alice := h.seedUser(t, mhAliceID, "alice@mail.example", 0)
	res := h.mhAppend(t, alice, alice.inbox.ID, "Here", "body")

	if rec := h.do(t, http.MethodGet, "/api/v1/mail/messages", alice.token, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("list without folder status = %d, want 400", rec.Code)
	}
	if rec := h.do(t, http.MethodGet, "/api/v1/mail/folders", alice.token, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("folders without mailbox status = %d, want 400", rec.Code)
	}
	if rec := h.do(t, http.MethodGet, fmt.Sprintf("/api/v1/mail/messages/%d?folder=%d", 999, alice.inbox.ID), alice.token, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown uid status = %d, want 404", rec.Code)
	}
	if rec := h.do(t, http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/%d/move?folder=%d", res.UID, alice.inbox.ID), alice.token, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("move without target status = %d, want 400", rec.Code)
	}
}
