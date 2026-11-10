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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/jkaninda/okapi"
)

// MailHandler is the OB-5 webmail REST surface (ADR-005 decision 1):
// /api/v1/mail/* over the mailstore service for the session user. Scoping is
// strictly per user — a session reaches only its own mailboxes, never
// workspace-wide — because the mailstore enforces ownership below every call
// (service.go resolveMailbox/resolveFolder/resolveMessage) and this handler
// passes the session identity straight through; a foreign mailbox, folder or
// message reads as 404 exactly like a missing one.
//
// The optional workspace header (routes wire optionalWorkspace, mirroring
// /users/me) narrows the listing to mailboxes bound to that workspace;
// personal (unbound) mailboxes stay visible. It never widens access.
//
// A nil store is legal at construction (the deployment lacks the blob store
// or the at-rest encryption key — SF-78 posture) and every endpoint answers
// 503; see the wiring note in internal/routes/routes.go.
type MailHandler struct {
	store *mailstore.Service
}

// NewMailHandler wires the webmail REST surface onto the mailstore service.
func NewMailHandler(store *mailstore.Service) *MailHandler {
	return &MailHandler{store: store}
}

// ─── Response shapes ─────────────────────────────────────────────────────────

// MailboxResponse is one of the session user's mailboxes with the ADR-003 §5
// quota and usage counters (dedup-blind quota basis + dedup-aware blob bytes).
type MailboxResponse struct {
	ID            uint                 `json:"id"`
	UserID        uint                 `json:"user_id"`
	WorkspaceID   *uint                `json:"workspace_id,omitempty"`
	Name          string               `json:"name"`
	QuotaBytes    int64                `json:"quota_bytes"`
	QuotaMessages int64                `json:"quota_messages"`
	UsedBytes     int64                `json:"used_bytes"`
	MessageCount  int64                `json:"message_count"`
	BlobBytes     int64                `json:"blob_bytes"`
	DistinctBlobs int64                `json:"distinct_blobs"`
	Status        models.MailboxStatus `json:"status"`
	CreatedAt     time.Time            `json:"created_at"`
}

// MailFolderResponse is one folder of a mailbox with its live counters.
type MailFolderResponse struct {
	ID          uint       `json:"id"`
	MailboxID   uint       `json:"mailbox_id"`
	ParentID    *uint      `json:"parent_id,omitempty"`
	Name        string     `json:"name"`
	IMAPPath    string     `json:"imap_path"`
	UIDValidity uint32     `json:"uid_validity"`
	UIDNext     uint32     `json:"uid_next"`
	Subscribed  bool       `json:"subscribed"`
	TotalCount  int64      `json:"total_count"`
	UnseenCount int64      `json:"unseen_count"`
	CreatedAt   time.Time  `json:"created_at"`
}

// MailAttachmentMeta is one entry of a message's attachment list. Index is
// the 0-based position the download endpoint addresses.
type MailAttachmentMeta struct {
	Index       int   `json:"index"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// MailMessageSummary is the list-row shape (no body).
type MailMessageSummary struct {
	ID             uint      `json:"id"`
	UID            uint32    `json:"uid"`
	FolderID       uint      `json:"folder_id"`
	MessageID      string    `json:"message_id"`
	Subject        string    `json:"subject"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	Cc             string    `json:"cc,omitempty"`
	Date           time.Time `json:"date"`
	Size           int64     `json:"size"`
	Flags          uint32    `json:"flags"`
	FlagsList      []string  `json:"flags_list"`
	ModSeq         uint64    `json:"mod_seq"`
	Snippet        string    `json:"snippet,omitempty"`
	HasAttachments bool      `json:"has_attachments"`
	CreatedAt      time.Time `json:"created_at"`
}

// MailMessageBody carries the renderable parts: the plain text and the
// SANITIZED HTML (remote content blocked, scripts stripped — mail_html.go).
// The dashboard renders HTML in its sandbox="" iframe when HasHTML, else the
// text in a <pre>.
type MailMessageBody struct {
	Text    string `json:"text"`
	HTML    string `json:"html,omitempty"`
	HasHTML bool   `json:"has_html"`
}

// MailMessageResponse is the full message view: metadata + body + attachments.
type MailMessageResponse struct {
	ID          uint                `json:"id"`
	UID         uint32              `json:"uid"`
	FolderID    uint                `json:"folder_id"`
	MessageID   string              `json:"message_id"`
	Subject     string              `json:"subject"`
	From        string              `json:"from"`
	To          string              `json:"to"`
	Cc          string              `json:"cc,omitempty"`
	Date        time.Time           `json:"date"`
	Size        int64               `json:"size"`
	Flags       uint32              `json:"flags"`
	FlagsList   []string            `json:"flags_list"`
	ModSeq      uint64              `json:"mod_seq"`
	Body        MailMessageBody     `json:"body"`
	Attachments []MailAttachmentMeta `json:"attachments"`
	CreatedAt   time.Time           `json:"created_at"`
}

// MailFlagsResponse reports the post-store state of a flag update.
type MailFlagsResponse struct {
	UID    uint32 `json:"uid"`
	Flags  uint32 `json:"flags"`
	ModSeq uint64 `json:"mod_seq"`
}

// MailMoveResponse reports where a moved/copied message landed.
type MailMoveResponse struct {
	MessageID uint   `json:"message_id"`
	FolderID  uint   `json:"folder_id"`
	UID       uint32 `json:"uid"`
	ModSeq    uint64 `json:"mod_seq"`
}

// MailExpungeResponse reports what a folder expunge removed.
type MailExpungeResponse struct {
	Expunged  int64    `json:"expunged"`
	ByteDelta int64    `json:"byte_delta"`
	UIDs      []uint32 `json:"uids"`
}

// flagNames renders the IMAP system flags the bitmask carries, in stable
// order. \Recent rides along: the webmail read path never sets it (it is the
// protocol servers' session-selection semantic), but ingest does, and the UI
// shows what is stored.
func flagNames(flags uint32) []string {
	out := make([]string, 0, 6)
	for _, f := range []struct {
		mask uint32
		name string
	}{
		{models.FlagSeen, "seen"},
		{models.FlagAnswered, "answered"},
		{models.FlagFlagged, "flagged"},
		{models.FlagDraft, "draft"},
		{models.FlagDeleted, "deleted"},
		{models.FlagRecent, "recent"},
	} {
		if flags&f.mask != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

// ─── Requests ────────────────────────────────────────────────────────────────

type MailFolderListRequest struct {
	Mailbox uint `query:"mailbox" doc:"Mailbox ID; the session user's own"`
}

type MailFolderCreateRequest struct {
	Body struct {
		Mailbox    uint   `json:"mailbox" required:"true" doc:"Mailbox ID; the session user's own"`
		Name       string `json:"name" required:"true" minLength:"1" maxLength:"255" doc:"Folder name (a single path segment; the delimiter is \"/\")"`
		ParentPath string `json:"parent_path" doc:"IMAP path of the parent folder; empty creates a root folder"`
	} `json:"body"`
}

type MailFolderPatchRequest struct {
	ID int `param:"id"`
	Body struct {
		Name       *string `json:"name" doc:"New folder name (rename); INBOX is refused"`
		Subscribed *bool   `json:"subscribed" doc:"Toggle the folder subscription flag"`
	} `json:"body"`
}

type MailFolderIDRequest struct {
	ID int `param:"id"`
}

type MailMessageListRequest struct {
	Folder  uint   `query:"folder" doc:"Folder ID to list"`
	Page    int    `query:"page" default:"1"`
	Size    int    `query:"size" default:"25"`
	Unread  bool   `query:"unread" doc:"Only messages without \\Seen"`
	Flagged bool   `query:"flagged" doc:"Only messages with \\Flagged"`
	Q       string `query:"q" doc:"Full-text query (ADR-003 FTS); when set, unread/flagged combine with it"`
}

type MailMessageRequest struct {
	UID    int  `param:"uid" doc:"Message UID within the folder"`
	Folder uint `query:"folder" doc:"Folder ID holding the message"`
}

type MailMessageFlagsRequest struct {
	UID    int  `param:"uid"`
	Folder uint `query:"folder"`
	Body struct {
		Seen     *bool  `json:"seen" doc:"Set/clear \\Seen"`
		Flagged  *bool  `json:"flagged" doc:"Set/clear \\Flagged"`
		Answered *bool  `json:"answered" doc:"Set/clear \\Answered"`
		Draft    *bool  `json:"draft" doc:"Set/clear \\Draft"`
		// IfModSeq is the CONDSTORE-style precondition: when non-zero the
		// store is refused with 409 unless the message's mod_seq still
		// matches (mailstore.ErrFlagConflict).
		IfModSeq uint64 `json:"if_mod_seq" doc:"Optional CAS precondition: the mod_seq the caller last observed"`
	} `json:"body"`
}

type MailMessageMoveRequest struct {
	UID    int  `param:"uid"`
	Folder uint `query:"folder"`
	Body struct {
		Target uint `json:"target" required:"true" doc:"Destination folder ID (same mailbox)"`
	} `json:"body"`
}

type MailAttachmentRequest struct {
	UID   int `param:"uid" doc:"Message UID within the folder"`
	Index int `param:"index" doc:"0-based attachment index"`
	Folder uint `query:"folder" doc:"Folder ID holding the message"`
}

type MailSearchRequest struct {
	Body struct {
		Mailbox uint   `json:"mailbox" doc:"Mailbox ID to search (required unless folder is set)"`
		Folder  uint   `json:"folder" doc:"Optional folder ID narrowing the search"`
		Q       string `json:"q" required:"true" minLength:"1" doc:"Full-text query (websearch syntax)"`
		Page    int    `json:"page" doc:"1-based page number"`
		Size    int    `json:"size" doc:"Page size (default 50, max 200)"`
	} `json:"body"`
}

// ─── Endpoints ───────────────────────────────────────────────────────────────

// ListMailboxes returns the session user's mailboxes with quota/usage.
func (h *MailHandler) ListMailboxes(c *okapi.Context) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	mbs, err := h.store.ListMailboxes(userID, wsID)
	if err != nil {
		return mailInternal(c, "failed to list mailboxes", err)
	}
	out := make([]MailboxResponse, 0, len(mbs))
	for i := range mbs {
		usage, err := h.store.MailboxUsage(mbs[i].ID, userID, wsID)
		if err != nil {
			return mailAbort(c, err)
		}
		out = append(out, MailboxResponse{
			ID:            mbs[i].ID,
			UserID:        mbs[i].UserID,
			WorkspaceID:   mbs[i].WorkspaceID,
			Name:          mbs[i].Name,
			QuotaBytes:    mbs[i].QuotaBytes,
			QuotaMessages: mbs[i].QuotaMessages,
			UsedBytes:     usage.RawBytes,
			MessageCount:  usage.MessageCount,
			BlobBytes:     usage.BlobBytes,
			DistinctBlobs: usage.DistinctBlobs,
			Status:        mbs[i].Status,
			CreatedAt:     mbs[i].CreatedAt,
		})
	}
	return ok(c, out)
}

// ListFolders returns one mailbox's folders with counters.
func (h *MailHandler) ListFolders(c *okapi.Context, req *MailFolderListRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	if req.Mailbox == 0 {
		return c.AbortBadRequest("mailbox is required")
	}
	userID, wsID := mailScope(c)
	folders, err := h.store.ListFolders(req.Mailbox, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	out := make([]MailFolderResponse, 0, len(folders))
	for i := range folders {
		resp, err := h.folderResponse(req.Mailbox, userID, wsID, &folders[i])
		if err != nil {
			return mailAbort(c, err)
		}
		out = append(out, *resp)
	}
	return ok(c, out)
}

// CreateFolder creates a folder under an optional parent path.
func (h *MailHandler) CreateFolder(c *okapi.Context, req *MailFolderCreateRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	f, err := h.store.CreateFolder(req.Body.Mailbox, userID, wsID, req.Body.ParentPath, req.Body.Name)
	if err != nil {
		return mailAbort(c, err)
	}
	resp, err := h.folderResponse(req.Body.Mailbox, userID, wsID, f)
	if err != nil {
		return mailAbort(c, err)
	}
	return created(c, resp)
}

// PatchFolder renames and/or (un)subscribes one folder.
func (h *MailHandler) PatchFolder(c *okapi.Context, req *MailFolderPatchRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	if req.ID <= 0 || req.ID > math.MaxInt32 {
		return c.AbortBadRequest("folder id is required")
	}
	userID, wsID := mailScope(c)
	folderID := uint(req.ID)
	if req.Body.Name != nil {
		if _, err := h.store.RenameFolder(folderID, userID, wsID, *req.Body.Name); err != nil {
			return mailAbort(c, err)
		}
	}
	if req.Body.Subscribed != nil {
		if err := h.store.SetSubscribed(folderID, userID, wsID, *req.Body.Subscribed); err != nil {
			return mailAbort(c, err)
		}
	}
	if req.Body.Name == nil && req.Body.Subscribed == nil {
		return c.AbortBadRequest("nothing to update: set name and/or subscribed")
	}
	f, mb, err := h.store.GetFolderByID(folderID, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	resp, err := h.folderResponse(mb.ID, userID, wsID, f)
	if err != nil {
		return mailAbort(c, err)
	}
	return ok(c, resp)
}

// DeleteFolder removes a folder and its whole subtree with all messages.
func (h *MailHandler) DeleteFolder(c *okapi.Context, req *MailFolderIDRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	if req.ID <= 0 || req.ID > math.MaxInt32 {
		return c.AbortBadRequest("folder id is required")
	}
	userID, wsID := mailScope(c)
	if err := h.store.DeleteFolder(uint(req.ID), userID, wsID); err != nil {
		return mailAbort(c, err)
	}
	return ok(c, dto.MessageData{Message: "folder deleted"})
}

// ExpungeFolder permanently removes the \Deleted messages of one folder.
func (h *MailHandler) ExpungeFolder(c *okapi.Context, req *MailFolderIDRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	if req.ID <= 0 || req.ID > math.MaxInt32 {
		return c.AbortBadRequest("folder id is required")
	}
	userID, wsID := mailScope(c)
	res, err := h.store.Expunge(uint(req.ID), userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	return ok(c, MailExpungeResponse{Expunged: res.Expunged, ByteDelta: res.ByteDelta, UIDs: res.UIDs})
}

// ListMessages pages one folder's messages, optionally filtered (unread,
// flagged) and/or full-text searched (q, ADR-003 FTS). Without q the filter
// is pushed into the listing SQL; with q the search tree combines the FTS
// tier with the flag keys and the page is cut from the ranked hits.
func (h *MailHandler) ListMessages(c *okapi.Context, req *MailMessageListRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	if req.Folder == 0 {
		return c.AbortBadRequest("folder is required")
	}
	userID, wsID := mailScope(c)
	folder, _, err := h.store.GetFolderByID(req.Folder, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	page := req.Page
	if page < 1 {
		page = 1
	}
	size := req.Size
	if size < 1 {
		size = 25
	}
	q := strings.TrimSpace(req.Q)
	if q == "" {
		list, err := h.store.ListMessages(folder.ID, userID, wsID, mailstore.ListFilter{
			Unread:  req.Unread,
			Flagged: req.Flagged,
		}, page, size)
		if err != nil {
			return mailAbort(c, err)
		}
		out := make([]MailMessageSummary, 0, len(list.Messages))
		for i := range list.Messages {
			out = append(out, mailSummary(&list.Messages[i]))
		}
		return paginated(c, out, list.Total, page, size)
	}
	// Full-text path: BODY-style FTS + optional flag keys, evaluated against
	// the folder (ownership resolved inside SearchIMAP). Hits arrive
	// UID-ascending like the plain listing; the page is cut in Go and the
	// rows re-verified through the scoped loader.
	crit := &mailstore.SearchAnd{Children: []mailstore.SearchNode{mailstore.SearchBodyKey{Text: q}}}
	if req.Unread {
		crit.Children = append(crit.Children, mailstore.SearchFlagKey{Mask: models.FlagSeen, Set: false})
	}
	if req.Flagged {
		crit.Children = append(crit.Children, mailstore.SearchFlagKey{Mask: models.FlagFlagged, Set: true})
	}
	hits, err := h.store.SearchIMAP(c.Request().Context(), mailstore.IMAPSearchScope{
		UserID:      userID,
		WorkspaceID: wsID,
		FolderID:    folder.ID,
	}, crit)
	if err != nil {
		return mailAbort(c, err)
	}
	start := (page - 1) * size
	if start > len(hits) {
		start = len(hits)
	}
	end := start + size
	if end > len(hits) {
		end = len(hits)
	}
	ids := make([]uint, 0, end-start)
	for _, hit := range hits[start:end] {
		ids = append(ids, hit.MessageID)
	}
	rows, err := h.store.GetMessagesByIDs(ids, userID, wsID)
	if err != nil {
		return mailInternal(c, "failed to load search results", err)
	}
	out := make([]MailMessageSummary, 0, len(rows))
	for i := range rows {
		out = append(out, mailSummary(&rows[i]))
	}
	return paginated(c, out, int64(len(hits)), page, size)
}

// GetMessage returns one message's metadata, its renderable body (sanitized
// HTML when the message has one, else the plain text) and the attachment
// list. Mirroring IMAP BODY[] (non-peek) semantics, the read marks the
// message \Seen when it is not already (the webmail read/unread behavior,
// ADR-005 feature set).
func (h *MailHandler) GetMessage(c *okapi.Context, req *MailMessageRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	folder, uid, err := h.resolveMessageReq(req.Folder, req.UID, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	m, err := h.store.GetMessageByUID(folder.ID, uid, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	resp, err := h.messageResponse(c.Request().Context(), m, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	if m.Flags&models.FlagSeen == 0 {
		// Best-effort read side effect; the view is served regardless.
		if res, ferr := h.store.SetFlags(userID, wsID, mailstore.FlagsUpdate{
			MessageID: m.ID,
			Set:       models.FlagSeen,
		}); ferr == nil {
			resp.Flags = res.Flags
			resp.FlagsList = flagNames(res.Flags)
			resp.ModSeq = res.ModSeq
		}
	}
	return ok(c, resp)
}

// DownloadAttachment streams one attachment's stored content, honoring the
// ingested content type. Content-Disposition is always attachment (never
// inline): a text/html attachment must render as a download, not as a
// document in the dashboard origin.
func (h *MailHandler) DownloadAttachment(c *okapi.Context, req *MailAttachmentRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	folder, uid, err := h.resolveMessageReq(req.Folder, req.UID, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	m, err := h.store.GetMessageByUID(folder.ID, uid, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	atts, err := mailAttachments(m)
	if err != nil {
		return mailInternal(c, "failed to decode attachment metadata", err)
	}
	if req.Index < 0 || req.Index >= len(atts) {
		return c.AbortNotFound("attachment not found")
	}
	att := atts[req.Index]
	meta, data, err := h.store.GetAttachment(c.Request().Context(), m.ID, userID, wsID, att.BlobHash)
	if err != nil {
		return mailAbort(c, err)
	}
	contentType := strings.TrimSpace(meta.ContentType)
	if contentType == "" || strings.Contains(strings.ToLower(contentType), "text/html") {
		contentType = "application/octet-stream"
	}
	w := c.ResponseWriter()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	name := meta.Name
	if name == "" {
		name = fmt.Sprintf("attachment-%d", req.Index)
	}
	w.Header().Set("Content-Disposition", contentDisposition(name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	// Headers are already committed — a write failure here can only truncate
	// the body (same posture as the inbound attachment streamer).
	_, _ = w.Write(data)
	return nil
}

// UpdateFlags applies one flag update (seen/flagged/answered/draft) with
// optional UNCHANGEDSINCE-style CAS (if_mod_seq). A lost race reads as 409.
func (h *MailHandler) UpdateFlags(c *okapi.Context, req *MailMessageFlagsRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	folder, uid, err := h.resolveMessageReq(req.Folder, req.UID, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	m, err := h.store.GetMessageByUID(folder.ID, uid, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	var set, clear uint32
	apply := func(p *bool, flag uint32) {
		if p == nil {
			return
		}
		if *p {
			set |= flag
		} else {
			clear |= flag
		}
	}
	apply(req.Body.Seen, models.FlagSeen)
	apply(req.Body.Flagged, models.FlagFlagged)
	apply(req.Body.Answered, models.FlagAnswered)
	apply(req.Body.Draft, models.FlagDraft)
	if set == 0 && clear == 0 {
		return c.AbortBadRequest("nothing to update: set at least one of seen/flagged/answered/draft")
	}
	res, err := h.store.SetFlags(userID, wsID, mailstore.FlagsUpdate{
		MessageID:      m.ID,
		Set:            set,
		Clear:          clear,
		ExpectedModSeq: req.Body.IfModSeq,
	})
	if err != nil {
		return mailAbort(c, err)
	}
	return ok(c, MailFlagsResponse{UID: res.UID, Flags: res.Flags, ModSeq: res.ModSeq})
}

// MoveMessage relocates a message to another folder of the same mailbox.
func (h *MailHandler) MoveMessage(c *okapi.Context, req *MailMessageMoveRequest) error {
	return h.moveOrCopy(c, req, false)
}

// CopyMessage duplicates a message into another folder of the same mailbox
// (blobs are shared — only the row and its UID are new).
func (h *MailHandler) CopyMessage(c *okapi.Context, req *MailMessageMoveRequest) error {
	return h.moveOrCopy(c, req, true)
}

func (h *MailHandler) moveOrCopy(c *okapi.Context, req *MailMessageMoveRequest, isCopy bool) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	folder, uid, err := h.resolveMessageReq(req.Folder, req.UID, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	if req.Body.Target == 0 {
		return c.AbortBadRequest("target folder is required")
	}
	m, err := h.store.GetMessageByUID(folder.ID, uid, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	var res *mailstore.AppendResult
	if isCopy {
		res, err = h.store.CopyMessage(m.ID, req.Body.Target, userID, wsID)
	} else {
		res, err = h.store.MoveMessage(m.ID, req.Body.Target, userID, wsID)
	}
	if err != nil {
		return mailAbort(c, err)
	}
	return ok(c, MailMoveResponse{
		MessageID: res.MessageID,
		FolderID:  res.FolderID,
		UID:       res.UID,
		ModSeq:    res.ModSeq,
	})
}

// DeleteMessage sets \Deleted; the rows leave at folder expunge.
func (h *MailHandler) DeleteMessage(c *okapi.Context, req *MailMessageRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	folder, uid, err := h.resolveMessageReq(req.Folder, req.UID, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	m, err := h.store.GetMessageByUID(folder.ID, uid, userID, wsID)
	if err != nil {
		return mailAbort(c, err)
	}
	if err := h.store.MarkDeleted(userID, wsID, m.ID); err != nil {
		return mailAbort(c, err)
	}
	return ok(c, dto.MessageData{Message: "message marked deleted"})
}

// Search runs the ADR-003 FTS over one mailbox (optionally narrowed to a
// folder) and returns the matched messages ranked, paged.
func (h *MailHandler) Search(c *okapi.Context, req *MailSearchRequest) error {
	if h.store == nil {
		return mailUnavailable(c)
	}
	userID, wsID := mailScope(c)
	body := req.Body
	q := strings.TrimSpace(body.Q)
	if q == "" {
		return c.AbortBadRequest("q is required")
	}
	if body.Mailbox == 0 && body.Folder == 0 {
		return c.AbortBadRequest("mailbox or folder is required")
	}
	mailboxID := body.Mailbox
	folderID := uint(0)
	if body.Folder != 0 {
		f, mb, err := h.store.GetFolderByID(body.Folder, userID, wsID)
		if err != nil {
			return mailAbort(c, err)
		}
		mailboxID = mb.ID
		folderID = f.ID
	} else {
		// Whole-mailbox search: resolve the mailbox through the same scoped
		// path as every other endpoint, so a foreign mailbox reads as 404
		// (missing and foreign are indistinguishable — existence never
		// leaks) instead of leaning on the searcher's empty-page posture.
		if _, err := h.store.GetMailbox(mailboxID, userID, wsID); err != nil {
			return mailAbort(c, err)
		}
	}
	page := body.Page
	if page < 1 {
		page = 1
	}
	size := body.Size
	if size < 1 {
		size = 50
	}
	hits, total, err := h.store.Search(c.Request().Context(), mailstore.SearchScope{
		MailboxID: mailboxID,
		UserID:    userID,
	}, mailstore.SearchQuery{
		Text:     q,
		FolderID: folderID,
		Limit:    size,
		Offset:   (page - 1) * size,
	})
	if err != nil {
		return mailAbort(c, err)
	}
	ids := make([]uint, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.MessageID)
	}
	rows, err := h.store.GetMessagesByIDs(ids, userID, wsID)
	if err != nil {
		return mailInternal(c, "failed to load search results", err)
	}
	out := make([]MailMessageSummary, 0, len(rows))
	for i := range rows {
		out = append(out, mailSummary(&rows[i]))
	}
	return paginated(c, out, total, page, size)
}

// ─── Internals ───────────────────────────────────────────────────────────────

// mailUnavailable is the 503 every endpoint answers while the deployment has
// no usable mailstore (blob store / encryption key not configured).
func mailUnavailable(c *okapi.Context) error {
	return c.AbortServiceUnavailable("mail is not available on this deployment")
}

// mailScope extracts the session identity: the user id and, when the optional
// workspace middleware set one, the workspace id (0 = personal scope).
func mailScope(c *okapi.Context) (userID, workspaceID uint) {
	userID = uint(c.GetInt("user_id"))
	if ws := c.GetInt("workspace_id"); ws > 0 {
		workspaceID = uint(ws)
	}
	return userID, workspaceID
}

// mailInternal maps unexpected store failures to the error envelope.
func mailInternal(c *okapi.Context, msg string, err error) error {
	if err == nil {
		return c.AbortInternalServerError(msg)
	}
	return c.AbortInternalServerError(msg, err)
}

// mailAbort maps mailstore typed errors onto HTTP: foreign and missing
// resources read identically as 404 (isolation never leaks existence),
// quota refactors surface as 422 with the enforcer's explanation, CAS losses
// as 409.
func mailAbort(c *okapi.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mailstore.ErrMailboxNotFound),
		errors.Is(err, mailstore.ErrFolderNotFound),
		errors.Is(err, mailstore.ErrMessageNotFound),
		errors.Is(err, mailstore.ErrNotMailboxOwner):
		return c.AbortNotFound("mail resource not found")
	case errors.Is(err, mailstore.ErrQuotaExceeded):
		// 422 with the enforcer's typed explanation (scope, used, incoming,
		// limit) — the quota rejection surfaces instead of collapsing into a
		// generic failure, mirroring the IMAP NO [OVERQUOTA] posture.
		return c.AbortWithError(http.StatusUnprocessableEntity, err)
	case errors.Is(err, mailstore.ErrFlagConflict):
		return c.AbortConflict("message changed since last seen (mod_seq mismatch); re-fetch and retry")
	case errors.Is(err, mailstore.ErrFolderExists):
		return c.AbortConflict("folder already exists")
	case errors.Is(err, mailstore.ErrFolderImmutable):
		return c.AbortBadRequest("INBOX cannot be renamed or deleted")
	case errors.Is(err, mailstore.ErrMailboxInactive):
		return c.AbortForbidden("mailbox is not active")
	case errors.Is(err, mailstore.ErrSearchUnavailable):
		return c.AbortServiceUnavailable("mail search is not available on this deployment")
	case errors.Is(err, mailstore.ErrSearchQueryTooLong):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, mailstore.ErrEmptyMessage):
		return c.AbortBadRequest("refusing to store an empty message")
	case errors.Is(err, mailstore.ErrBlobCorrupt):
		return c.AbortInternalServerError("stored message content failed its integrity check", err)
	default:
		return mailInternal(c, "mailstore operation failed", err)
	}
}

// resolveMessageReq validates the (folder, uid) addressing shared by every
// message endpoint: both must be present and in range.
func (h *MailHandler) resolveMessageReq(folderID uint, uid int, userID, wsID uint) (*models.MailFolder, uint32, error) {
	if folderID == 0 {
		return nil, 0, errors.New("mail: folder is required")
	}
	if uid <= 0 || uid > math.MaxUint32 {
		return nil, 0, errors.New("mail: message uid out of range")
	}
	f, _, err := h.store.GetFolderByID(folderID, userID, wsID)
	if err != nil {
		return nil, 0, err
	}
	return f, uint32(uid), nil
}

// folderResponse builds the folder row plus its live counters.
func (h *MailHandler) folderResponse(mailboxID, userID, wsID uint, f *models.MailFolder) (*MailFolderResponse, error) {
	counts, err := h.store.StatusCounts(f.ID, userID, wsID)
	if err != nil {
		return nil, err
	}
	return &MailFolderResponse{
		ID:          f.ID,
		MailboxID:   f.MailboxID,
		ParentID:    f.ParentID,
		Name:        f.Name,
		IMAPPath:    f.IMAPPath,
		UIDValidity: f.UIDValidity,
		UIDNext:     f.UIDNext,
		Subscribed:  f.Subscribed,
		TotalCount:  counts.Messages,
		UnseenCount: counts.Unseen,
		CreatedAt:   f.CreatedAt,
	}, nil
}

// mailAttachments decodes a message row's stored attachment refs.
func mailAttachments(m *models.MailMessage) ([]models.MailAttachmentRef, error) {
	if m.AttachmentsJSON == "" {
		return nil, nil
	}
	var atts []models.MailAttachmentRef
	if err := json.Unmarshal([]byte(m.AttachmentsJSON), &atts); err != nil {
		return nil, fmt.Errorf("parse attachment refs of message %d: %w", m.ID, err)
	}
	return atts, nil
}

// messageResponse builds the full message view: metadata, sanitized body,
// attachment list. Body parse failures degrade to an empty body — the
// metadata and attachments stay available.
func (h *MailHandler) messageResponse(ctx context.Context, m *models.MailMessage, userID, wsID uint) (*MailMessageResponse, error) {
	resp := &MailMessageResponse{
		ID:          m.ID,
		UID:         m.UID,
		FolderID:    m.FolderID,
		MessageID:   m.MessageID,
		Subject:     m.Subject,
		From:        m.FromAddr,
		To:          m.ToAddr,
		Cc:          m.Cc,
		Date:        m.Date,
		Size:        m.Size,
		Flags:       m.Flags,
		FlagsList:   flagNames(m.Flags),
		ModSeq:      m.ModSeq,
		Attachments: []MailAttachmentMeta{},
		CreatedAt:   m.CreatedAt,
	}
	atts, err := mailAttachments(m)
	if err != nil {
		return nil, err
	}
	for i, a := range atts {
		resp.Attachments = append(resp.Attachments, MailAttachmentMeta{
			Index:       i,
			Name:        a.Name,
			ContentType: a.ContentType,
			Size:        a.Size,
		})
	}
	raw, err := h.store.GetMessageBody(ctx, m.ID, userID, wsID)
	if err != nil {
		// The row is readable even when its blob is gone (GC integrity
		// failure): serve metadata + attachments with an empty body rather
		// than failing the whole view.
		if errors.Is(err, mailstore.ErrBlobCorrupt) {
			resp.Body = MailMessageBody{}
			return resp, nil
		}
		return nil, err
	}
	parsed, perr := mailstore.ParseMessage(raw)
	if perr != nil {
		resp.Body = MailMessageBody{Text: m.Snippet}
		return resp, nil
	}
	resp.Body = MailMessageBody{
		Text:    parsed.TextBody,
		HTML:    sanitizeMailHTML(parsed.HTMLBody),
		HasHTML: parsed.HTMLBody != "",
	}
	return resp, nil
}

// mailSummary projects a message row onto the list-row shape.
func mailSummary(m *models.MailMessage) MailMessageSummary {
	return MailMessageSummary{
		ID:             m.ID,
		UID:            m.UID,
		FolderID:       m.FolderID,
		MessageID:      m.MessageID,
		Subject:        m.Subject,
		From:           m.FromAddr,
		To:             m.ToAddr,
		Cc:             m.Cc,
		Date:           m.Date,
		Size:           m.Size,
		Flags:          m.Flags,
		FlagsList:      flagNames(m.Flags),
		ModSeq:         m.ModSeq,
		Snippet:        m.Snippet,
		HasAttachments: m.AttachmentsJSON != "" && m.AttachmentsJSON != "[]",
		CreatedAt:      m.CreatedAt,
	}
}
