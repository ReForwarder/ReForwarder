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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/mailcompose"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/jkaninda/okapi"
)

// OB-5 W3 compose handler tests: the HTTP mapping of the compose seam's typed
// outcomes — the ADR-006 infected refusal (409 with the signature), the quota
// refusal (409 with the scope detail), the staging gate (400), the 503
// not-configured posture, and user isolation at the route layer — over the
// REAL compose service backed by minimal in-memory fakes.

// --- minimal fakes (the mailcompose package's own suite covers behavior;
// these just need to make the exercised paths run) ---

type mcStore struct {
	mu        sync.Mutex
	mailboxes map[uint]*models.Mailbox
	byName    map[string]*models.Mailbox
	messages  map[uint]*models.MailMessage // row id -> message
	raws      map[uint][]byte              // row id -> stored raw
	appendErr error
	appends   []mailstore.AppendIn
	nextID    uint
}

func newMCStore() *mcStore {
	return &mcStore{
		mailboxes: map[uint]*models.Mailbox{},
		byName:    map[string]*models.Mailbox{},
		messages:  map[uint]*models.MailMessage{},
		raws:      map[uint][]byte{},
		nextID:    10,
	}
}

func (s *mcStore) mint() uint { s.nextID++; return s.nextID }

func (s *mcStore) seed(userID uint, name string) uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb := &models.Mailbox{ID: s.nextID, UserID: userID, Name: name, Status: models.MailboxStatusActive}
	s.nextID++
	s.mailboxes[mb.ID] = mb
	s.byName[name] = mb
	return mb.ID
}

func (s *mcStore) FindMailboxByName(userID uint, name string) (*models.Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.byName[name]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrMailboxNotFound
	}
	return mb, nil
}

func (s *mcStore) CreateMailbox(userID uint, workspaceID *uint, name string, quotaBytes int64) (*models.Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb := &models.Mailbox{ID: s.nextID, UserID: userID, Name: name, QuotaBytes: quotaBytes, Status: models.MailboxStatusActive}
	s.nextID++
	s.mailboxes[mb.ID] = mb
	s.byName[name] = mb
	return mb, nil
}

func (s *mcStore) GetMailbox(mailboxID, userID, _ uint) (*models.Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrMailboxNotFound
	}
	return mb, nil
}

func (s *mcStore) GetFolderByPath(mailboxID, userID, _ uint, path string) (*models.MailFolder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb, ok := s.mailboxes[mailboxID]
	if !ok || mb.UserID != userID {
		return nil, mailstore.ErrMailboxNotFound
	}
	return &models.MailFolder{ID: mailboxID*10 + 1, MailboxID: mailboxID, Name: path, IMAPPath: path, UIDNext: 1}, nil
}

func (s *mcStore) CreateFolder(mailboxID, userID, _ uint, _, name string) (*models.MailFolder, error) {
	return s.GetFolderByPath(mailboxID, userID, 0, name)
}

func (s *mcStore) Append(_ context.Context, userID, _ uint, in mailstore.AppendIn) (*mailstore.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return nil, s.appendErr
	}
	mb := s.mailboxes[in.MailboxID]
	if mb == nil || mb.UserID != userID {
		return nil, mailstore.ErrNotMailboxOwner
	}
	uid := uint32(len(s.appends) + 1)
	msg := &models.MailMessage{ID: s.mint(), FolderID: in.FolderID, UID: uid, Flags: in.Flags}
	s.messages[msg.ID] = msg
	s.raws[msg.ID] = in.Raw
	s.appends = append(s.appends, in)
	return &mailstore.AppendResult{MessageID: msg.ID, FolderID: in.FolderID, UID: uid, ModSeq: 1}, nil
}

func (s *mcStore) GetMessageByUID(folderID uint, uid uint32, userID, _ uint) (*models.MailMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if m.FolderID != folderID || m.UID != uid {
			continue
		}
		mb := s.mailboxes[m.FolderID/10]
		if mb == nil || mb.UserID != userID {
			return nil, mailstore.ErrNotMailboxOwner
		}
		return m, nil
	}
	return nil, mailstore.ErrMessageNotFound
}

func (s *mcStore) GetMessageBody(_ context.Context, messageID, userID, _ uint) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[messageID]
	if !ok {
		return nil, mailstore.ErrMessageNotFound
	}
	mb := s.mailboxes[m.FolderID/10]
	if mb == nil || mb.UserID != userID {
		return nil, mailstore.ErrNotMailboxOwner
	}
	return s.raws[messageID], nil
}

func (s *mcStore) MarkDeleted(_, _ uint, messageID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[messageID]
	if !ok {
		return mailstore.ErrMessageNotFound
	}
	m.Flags |= models.FlagDeleted
	return nil
}

func (s *mcStore) Expunge(folderID, userID, _ uint) (*mailstore.ExpungeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mb := s.mailboxes[folderID/10]
	if mb == nil || mb.UserID != userID {
		return nil, mailstore.ErrNotMailboxOwner
	}
	var uids []uint32
	for id, m := range s.messages {
		if m.FolderID != folderID || m.Flags&models.FlagDeleted == 0 {
			continue
		}
		uids = append(uids, m.UID)
		delete(s.messages, id)
		delete(s.raws, id)
	}
	return &mailstore.ExpungeResult{Expunged: int64(len(uids)), UIDs: uids}, nil
}

func (s *mcStore) ReserveBlob(_ context.Context, content []byte) (mailstore.BlobRef, error) {
	return mailstore.BlobRef{Hash: fmt.Sprintf("hash-%d", len(content)), Size: int64(len(content))}, nil
}
func (s *mcStore) ReleaseBlob(context.Context, string) error { return nil }
func (s *mcStore) ReadBlob(_ context.Context, hash string) ([]byte, error) {
	return []byte("content-of-" + hash), nil
}
func (s *mcStore) SweepStaleAttachmentUploads(context.Context, uint, time.Duration) (int64, error) {
	return 0, nil
}

type mcSender struct{ calls int }

func (s *mcSender) SendMailCompose(context.Context, *email.MailComposeRequest) (*email.SendResponse, error) {
	s.calls++
	return &email.SendResponse{ID: "out-1", Status: models.EmailStatusQueued}, nil
}

type mcScanner struct{ verdict string }

func (s *mcScanner) Scan(context.Context, []byte) (spamfilter.ScanResult, error) {
	if s.verdict != "" {
		return spamfilter.ScanResult{Infected: true, Signature: s.verdict}, nil
	}
	return spamfilter.ScanResult{}, nil
}
func (s *mcScanner) Enabled() bool                     { return true }
func (s *mcScanner) FailMode() spamfilter.ClamFailMode { return spamfilter.ClamFailOpen }

type mcStaging struct {
	rows map[string]*models.MailAttachmentUpload
}

func newMCStaging() *mcStaging { return &mcStaging{rows: map[string]*models.MailAttachmentUpload{}} }

func (m *mcStaging) FindByUserAndHash(userID uint, hash string) (*models.MailAttachmentUpload, error) {
	if row, ok := m.rows[fmt.Sprintf("%d|%s", userID, hash)]; ok {
		return row, nil
	}
	return nil, nil
}
func (m *mcStaging) Save(u *models.MailAttachmentUpload) error {
	m.rows[fmt.Sprintf("%d|%s", u.UserID, u.BlobHash)] = u
	return nil
}
func (m *mcStaging) DeleteByUserAndHashes(uint, []string) (int64, error) { return 0, nil }

type mcDelivery struct{}

func (mcDelivery) FindEmailByUUIDForUser(string, uint) (*models.Email, error) { return nil, nil }
func (mcDelivery) ListEventsByEmailUUID(string) ([]models.EmailEvent, error)  { return nil, nil }

// --- harness ---

const mcTestSecret = "mc-test-secret"

func mcHarness(t *testing.T, svc *mailcompose.Service) *okapi.TestServer {
	t.Helper()
	o := okapi.NewTestServer(t)
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: mcTestSecret})
	h := NewMailComposeHandler(svc)
	g := o.Group("/api/v1/mail", jwtAuth.Middleware)
	g.Post("/send", okapi.H(h.Send))
	g.Post("/attachments", h.UploadAttachment)
	g.Post("/drafts", okapi.H(h.SaveDraft))
	g.Put("/drafts/{uid:int}", okapi.H(h.UpdateDraft))
	g.Post("/drafts/{uid:int}/send", okapi.H(h.SendDraft))
	g.Get("/messages/{uid:int}/reply-frame", okapi.H(h.ReplyFrame))
	g.Get("/messages/{uid:int}/delivery-status", okapi.H(h.DeliveryStatus))
	return o
}

func mcDo(t *testing.T, o *okapi.TestServer, method, path, token, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// mcToken mints a dashboard-session token signed with THIS harness's secret
// (mcTestSecret) — the leToken helper signs with the login-enforcement
// harness's own secret, which the JWT middleware below would rightly refuse.
func mcToken(t *testing.T, userID int64) string {
	t.Helper()
	tok, err := okapi.GenerateJwtToken([]byte(mcTestSecret), map[string]any{
		"sub":   userID,
		"email": fmt.Sprintf("mc-%d@example.test", userID),
		"role":  "user",
		"aud":   "posta",
		"jti":   fmt.Sprintf("jti-mc-%d", userID),
	}, time.Hour)
	if err != nil {
		t.Fatalf("GenerateJwtToken: %v", err)
	}
	return tok
}

func newComposeService(store *mcStore, scanner mailcompose.Scanner, sender *mcSender) *mailcompose.Service {
	return mailcompose.New(store, sender, scanner, newMCStaging(), mcDelivery{}, mailcompose.Config{AutoProvision: true})
}

func TestMailCompose_SendHappyPathOverHTTP(t *testing.T) {
	store := newMCStore()
	store.seed(7, "ada@example.test")
	o := mcHarness(t, newComposeService(store, &mcScanner{}, &mcSender{}))

	body := `{"from":"ada@example.test","to":["bob@external.test"],"cc":["carol@external.test"],"bcc":["dave@hidden.test"],"subject":"HTTP send","text":"hello"}`
	rec := mcDo(t, o, http.MethodPost, "/api/v1/mail/send", mcToken(t, 7), body, "application/json")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if len(store.appends) != 1 {
		t.Fatalf("appends = %d, want 1", len(store.appends))
	}
	raw := string(store.appends[0].Raw)
	if !strings.Contains(raw, "Subject: HTTP send") || !strings.Contains(raw, "To: bob@external.test") {
		t.Errorf("stored raw incomplete:\n%s", raw)
	}
	if strings.Contains(raw, "dave@hidden.test") {
		t.Errorf("stored raw leaks bcc:\n%s", raw)
	}
}

func TestMailCompose_UploadMultipartAndRefuseInfectedWith409(t *testing.T) {
	store := newMCStore()
	staging := newMCStaging()
	o := mcHarness(t, mailcompose.New(store, &mcSender{}, &mcScanner{verdict: "Eicar-Signature"}, staging, mcDelivery{}, mailcompose.Config{AutoProvision: true}))

	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "eicar.txt")
	_, _ = fw.Write([]byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"))
	_ = w.Close()

	rec := mcDo(t, o, http.MethodPost, "/api/v1/mail/attachments", mcToken(t, 7), buf.String(), w.FormDataContentType())
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (body %s), want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Eicar-Signature") {
		t.Errorf("refusal does not carry the signature: %s", rec.Body.String())
	}
	if len(staging.rows) != 0 {
		t.Errorf("infected upload staged %d rows", len(staging.rows))
	}
}

func TestMailCompose_QuotaRefusalMapsTo409(t *testing.T) {
	store := newMCStore()
	store.seed(7, "ada@example.test")
	store.appendErr = &mailstore.QuotaError{MailboxID: 1, Scope: mailstore.QuotaScopeMailboxBytes, Used: 100, Incoming: 10, Limit: 100}
	o := mcHarness(t, newComposeService(store, &mcScanner{}, &mcSender{}))

	body := `{"from":"ada@example.test","to":["bob@external.test"],"subject":"q","text":"x"}`
	rec := mcDo(t, o, http.MethodPost, "/api/v1/mail/send", mcToken(t, 7), body, "application/json")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d (body %s), want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mailbox-bytes") {
		t.Errorf("refusal lacks the quota scope: %s", rec.Body.String())
	}
}

func TestMailCompose_UnstagedAttachmentRefMapsTo400(t *testing.T) {
	store := newMCStore()
	store.seed(7, "ada@example.test")
	o := mcHarness(t, newComposeService(store, &mcScanner{}, &mcSender{}))

	body := `{"from":"ada@example.test","to":["b@x.test"],"subject":"s","text":"t","attachments":[{"blob_hash":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}]}`
	rec := mcDo(t, o, http.MethodPost, "/api/v1/mail/send", mcToken(t, 7), body, "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (body %s), want 400", rec.Code, rec.Body.String())
	}
}

func TestMailCompose_DraftLifecycleOverHTTP(t *testing.T) {
	store := newMCStore()
	mbID := store.seed(7, "ada@example.test")
	o := mcHarness(t, newComposeService(store, &mcScanner{}, &mcSender{}))
	tok := mcToken(t, 7)

	// Save.
	body := `{"from":"ada@example.test","to":["b@x.test"],"subject":"draft","text":"wip"}`
	rec := mcDo(t, o, http.MethodPost, "/api/v1/mail/drafts", tok, body, "application/json")
	if rec.Code != http.StatusCreated {
		t.Fatalf("save draft status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if len(store.appends) != 1 || store.appends[0].Flags&models.FlagDraft == 0 {
		t.Fatalf("draft append missing or unflagged: %+v", store.appends)
	}

	// Update with mailbox_id query.
	upd := `{"from":"ada@example.test","to":["b@x.test","c@x.test"],"subject":"draft 2","text":"wip2"}`
	rec = mcDo(t, o, http.MethodPut, fmt.Sprintf("/api/v1/mail/drafts/1?mailbox_id=%d", mbID), tok, upd, "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("update draft status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if len(store.appends) != 2 {
		t.Fatalf("appends after update = %d, want 2 (replace appends a new revision)", len(store.appends))
	}

	// Foreign user cannot update.
	rec = mcDo(t, o, http.MethodPut, fmt.Sprintf("/api/v1/mail/drafts/1?mailbox_id=%d", mbID), mcToken(t, 8), upd, "application/json")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign update status = %d (body %s), want 404", rec.Code, rec.Body.String())
	}

	// Missing mailbox_id is a 400.
	rec = mcDo(t, o, http.MethodPut, "/api/v1/mail/drafts/1", tok, upd, "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing mailbox_id status = %d, want 400", rec.Code)
	}

	// Send-from-draft over HTTP: content flows, draft removed, Sent filed.
	rec = mcDo(t, o, http.MethodPost, fmt.Sprintf("/api/v1/mail/drafts/2/send?mailbox_id=%d", mbID), tok, "", "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("send draft status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if len(store.appends) != 3 {
		t.Fatalf("appends after send = %d, want 3 (draft, replace, sent copy)", len(store.appends))
	}
	if !strings.Contains(string(store.appends[2].Raw), "Subject: draft 2") {
		t.Errorf("sent copy content = %.60q, want the stored draft's content", store.appends[2].Raw)
	}
}

func TestMailCompose_ReplyFrameRequiresQueryParams(t *testing.T) {
	store := newMCStore()
	o := mcHarness(t, newComposeService(store, &mcScanner{}, &mcSender{}))

	rec := mcDo(t, o, http.MethodGet, "/api/v1/mail/messages/1/reply-frame", mcToken(t, 7), "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (folder_id required)", rec.Code)
	}
}

func TestMailCompose_UnconfiguredAnswers503(t *testing.T) {
	o := okapi.NewTestServer(t)
	jwtAuth := middlewares.JWTAuth(&config.Config{JWTSecret: mcTestSecret})
	g := o.Group("/api/v1/mail", jwtAuth.Middleware)
	disabled := NewMailComposeHandler(nil)
	g.Post("/send", okapi.H(disabled.Send))
	g.Post("/attachments", func(c *okapi.Context) error { return disabled.UploadAttachment(c) })

	rec := mcDo(t, o, http.MethodPost, "/api/v1/mail/send", mcToken(t, 7), `{"from":"a@b.test","to":["c@d.test"],"subject":"s"}`, "application/json")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (body %s), want 503", rec.Code, rec.Body.String())
	}

	rec2 := mcDo(t, o, http.MethodPost, "/api/v1/mail/attachments", mcToken(t, 7), "--x--", "multipart/form-data; boundary=x")
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("attachments status = %d, want 503", rec2.Code)
	}
}
