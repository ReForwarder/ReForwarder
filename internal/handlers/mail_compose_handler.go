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
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/goposta/posta/internal/services/mailcompose"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/jkaninda/okapi"
)

// MailComposeHandler is the OB-5 webmail compose surface (ADR-005 decision 2,
// wave W3): attachment staging with the ADR-006 scan gate, send into the
// Sent folder + outbound pipeline, the draft lifecycle, the reply/forward
// prefill frame, and the delivery-status view over email_events.
//
// Every endpoint is user-scoped (JWT dashboard session only — an API key is
// not a mailbox owner) and delegates ownership enforcement to the compose
// service / mailstore scoping; a nil service (mailstore or encryption key not
// configured) answers 503 on every route instead of panicking, matching the
// webauthn/webpush disabled-wiring convention.
type MailComposeHandler struct {
	svc *mailcompose.Service
}

func NewMailComposeHandler(svc *mailcompose.Service) *MailComposeHandler {
	return &MailComposeHandler{svc: svc}
}

func (h *MailComposeHandler) available(c *okapi.Context) bool {
	if h.svc == nil {
		_ = c.AbortServiceUnavailable("webmail compose is not available on this deployment")
		return false
	}
	return true
}

func composeScope(c *okapi.Context) (uint, uint) {
	userID := uint(c.GetInt("user_id"))
	ws := c.GetInt("workspace_id")
	if ws < 0 {
		ws = 0
	}
	return userID, uint(ws)
}

// ---- request/response shapes ----

// composeMessageBody is the shared to/cc/bcc + subject + body + attachment-ref
// payload of send and draft requests (ADR-005 decision 2's compose shape).
type composeMessageBody struct {
	MailboxID   uint                          `json:"mailbox_id,omitempty"`
	From        string                        `json:"from" required:"true"`
	To          []string                      `json:"to" required:"true"`
	Cc          []string                      `json:"cc,omitempty"`
	Bcc         []string                      `json:"bcc,omitempty"`
	Subject     string                        `json:"subject"`
	Text        string                        `json:"text,omitempty"`
	HTML        string                        `json:"html,omitempty"`
	Attachments []ComposeAttachmentRefRequest `json:"attachments,omitempty"`
}

type ComposeAttachmentRefRequest struct {
	BlobHash    string `json:"blob_hash" required:"true"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type ComposeSendRequest struct {
	Body composeMessageBody `json:"body"`
}

// ComposeUpdateDraftRequest carries the draft UID (path) and the same body.
type ComposeUpdateDraftRequest struct {
	UID  uint               `param:"uid"`
	Body composeMessageBody `json:"body"`
}

func (r *composeMessageBody) toInput() mailcompose.MessageInput {
	atts := make([]mailcompose.AttachmentRef, 0, len(r.Attachments))
	for _, a := range r.Attachments {
		atts = append(atts, mailcompose.AttachmentRef{BlobHash: a.BlobHash, Name: a.Name, ContentType: a.ContentType})
	}
	return mailcompose.MessageInput{
		MailboxID:   r.MailboxID,
		From:        r.From,
		To:          r.To,
		Cc:          r.Cc,
		Bcc:         r.Bcc,
		Subject:     r.Subject,
		Text:        r.Text,
		HTML:        r.HTML,
		Attachments: atts,
	}
}

// Response aliases: the compose service owns the payload shapes; the handler
// re-exports them so the route table stays package-local.
type (
	SendResult             = mailcompose.SendResult
	DraftResult            = mailcompose.DraftResult
	ReplyFrameResponse     = mailcompose.ReplyFrame
	DeliveryStatusResponse = mailcompose.DeliveryStatus
)

// ComposeAttachmentRefResponse is the upload endpoint's ref: the content
// address compose requests reference. It mirrors models.MailAttachmentRef.
type ComposeAttachmentRefResponse struct {
	BlobHash    string `json:"blob_hash"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	ScanState   string `json:"scan_state"`
}

// Send sends a composed message: Sent copy + outbound pipeline.
func (h *MailComposeHandler) Send(c *okapi.Context, req *ComposeSendRequest) error {
	if !h.available(c) {
		return nil
	}
	userID, wsID := composeScope(c)
	res, err := h.svc.Send(c.Request().Context(), userID, wsID, req.Body.toInput())
	if err != nil {
		return mapComposeError(c, err)
	}
	return created(c, res)
}

// UploadAttachment stages one or more multipart file parts through the ADR-006
// scan gate and returns their refs.
func (h *MailComposeHandler) UploadAttachment(c *okapi.Context) error {
	if !h.available(c) {
		return nil
	}
	userID, wsID := composeScope(c)
	req := c.Request()
	if err := req.ParseMultipartForm(composeMaxMultipartMemory); err != nil {
		return c.AbortBadRequest("expected a multipart/form-data upload")
	}
	form := req.MultipartForm
	if form == nil || len(form.File) == 0 {
		return c.AbortBadRequest("no file part in the upload")
	}
	refs := make([]ComposeAttachmentRefResponse, 0, len(form.File))
	for _, headers := range form.File {
		for _, fh := range headers {
			file, err := fh.Open()
			if err != nil {
				return c.AbortBadRequest("could not read the uploaded file")
			}
			// One byte past the cap proves truncation instead of hiding it.
			content, err := io.ReadAll(io.LimitReader(file, composeMaxAttachmentBytes+1))
			_ = file.Close()
			if err != nil {
				return c.AbortBadRequest("could not read the uploaded file")
			}
			row, err := h.svc.UploadAttachment(c.Request().Context(), userID, wsID, fh.Filename, fh.Header.Get("Content-Type"), content)
			if err != nil {
				return mapComposeError(c, err)
			}
			refs = append(refs, ComposeAttachmentRefResponse{
				BlobHash:    row.BlobHash,
				Name:        row.Name,
				ContentType: row.ContentType,
				Size:        row.Size,
				ScanState:   row.ScanState,
			})
		}
	}
	return created(c, refs)
}

// SaveDraft stores a new draft in the mailbox's Drafts folder.
func (h *MailComposeHandler) SaveDraft(c *okapi.Context, req *ComposeSendRequest) error {
	if !h.available(c) {
		return nil
	}
	userID, wsID := composeScope(c)
	res, err := h.svc.SaveDraft(c.Request().Context(), userID, wsID, req.Body.toInput())
	if err != nil {
		return mapComposeError(c, err)
	}
	return created(c, res)
}

type ComposeDraftUIDRequest struct {
	UID uint `param:"uid"`
}

// UpdateDraft replaces a draft by Drafts-folder UID (mailbox_id query param).
func (h *MailComposeHandler) UpdateDraft(c *okapi.Context, req *ComposeUpdateDraftRequest) error {
	if !h.available(c) {
		return nil
	}
	mailboxID, got := composeMailboxQuery(c)
	if !got {
		return nil
	}
	userID, wsID := composeScope(c)
	res, err := h.svc.UpdateDraft(c.Request().Context(), userID, wsID, mailboxID, uint32(req.UID), req.Body.toInput())
	if err != nil {
		return mapComposeError(c, err)
	}
	return ok(c, res)
}

// SendDraft sends the stored draft and deletes it after acceptance.
func (h *MailComposeHandler) SendDraft(c *okapi.Context, req *ComposeDraftUIDRequest) error {
	if !h.available(c) {
		return nil
	}
	mailboxID, got := composeMailboxQuery(c)
	if !got {
		return nil
	}
	userID, wsID := composeScope(c)
	res, err := h.svc.SendDraft(c.Request().Context(), userID, wsID, mailboxID, uint32(req.UID))
	if err != nil {
		return mapComposeError(c, err)
	}
	return ok(c, res)
}

type ComposeFrameRequest struct {
	UID uint `param:"uid"`
}

// ReplyFrame returns the composer prefill for reply / reply-all / forward.
func (h *MailComposeHandler) ReplyFrame(c *okapi.Context, req *ComposeFrameRequest) error {
	if !h.available(c) {
		return nil
	}
	q := c.Request().URL.Query()
	folderID, err := composeUintQuery(q, "folder_id")
	if err != nil {
		return c.AbortBadRequest("folder_id query parameter is required")
	}
	mailboxID, err := composeUintQuery(q, "mailbox_id")
	if err != nil {
		return c.AbortBadRequest("mailbox_id query parameter is required")
	}
	mode := q.Get("mode")
	userID, wsID := composeScope(c)
	frame, err := h.svc.ReplyFrame(c.Request().Context(), userID, wsID, mailboxID, folderID, uint32(req.UID), mode)
	if err != nil {
		return mapComposeError(c, err)
	}
	return ok(c, frame)
}

// DeliveryStatus returns the outbound email_events timeline of a sent message.
func (h *MailComposeHandler) DeliveryStatus(c *okapi.Context, req *ComposeFrameRequest) error {
	if !h.available(c) {
		return nil
	}
	q := c.Request().URL.Query()
	folderID, err := composeUintQuery(q, "folder_id")
	if err != nil {
		return c.AbortBadRequest("folder_id query parameter is required")
	}
	userID, wsID := composeScope(c)
	status, err := h.svc.DeliveryStatus(c.Request().Context(), userID, wsID, folderID, uint32(req.UID))
	if err != nil {
		return mapComposeError(c, err)
	}
	return ok(c, status)
}

// ---- shared helpers ----

const (
	composeMaxMultipartMemory = 32 << 20
	composeMaxAttachmentBytes = 25 << 20
)

func composeMailboxQuery(c *okapi.Context) (uint, bool) {
	v, err := composeUintQuery(c.Request().URL.Query(), "mailbox_id")
	if err != nil {
		_ = c.AbortBadRequest("mailbox_id query parameter is required")
		return 0, false
	}
	return v, true
}

func composeUintQuery(q map[string][]string, key string) (uint, error) {
	values, ok := q[key]
	if !ok || len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	v, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return uint(v), nil
}

// mapComposeError translates compose/mailstore typed outcomes onto the API
// error envelope. Refusals carry their reason: ADR-006 positives answer 409
// with the clamd signature, quota refusals answer 409 with the quota scope,
// ownership and existence collapse into 404 (never confirm foreign rows).
func mapComposeError(c *okapi.Context, err error) error {
	var infected *mailcompose.AttachmentInfectedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &infected):
		return c.AbortConflict(err.Error())
	case errors.Is(err, mailstore.ErrQuotaExceeded):
		detail := err.Error()
		var qe *mailstore.QuotaError
		if errors.As(err, &qe) {
			detail = fmt.Sprintf("mailbox quota exceeded (%s): used %d + incoming %d > limit %d",
				qe.Scope, qe.Used, qe.Incoming, qe.Limit)
		}
		return c.AbortConflict(detail)
	case errors.Is(err, mailcompose.ErrScannerUnavailable):
		return c.AbortServiceUnavailable("malware scanner unavailable and the fail mode is closed; try again later")
	case errors.Is(err, mailcompose.ErrAttachmentTooLarge):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, mailcompose.ErrAttachmentNotStaged):
		return c.AbortBadRequest("attachment reference was not staged by this user (upload it first)")
	case errors.Is(err, mailcompose.ErrNoMailbox):
		return c.AbortConflict("no mailbox exists for this sender address and auto-provisioning is disabled")
	case errors.Is(err, mailcompose.ErrNotMailboxOwner),
		errors.Is(err, mailstore.ErrNotMailboxOwner),
		errors.Is(err, mailstore.ErrMessageNotFound),
		errors.Is(err, mailstore.ErrFolderNotFound),
		errors.Is(err, mailstore.ErrMailboxNotFound):
		return c.AbortNotFound("message not found")
	case errors.Is(err, mailstore.ErrMailboxInactive):
		return c.AbortConflict("mailbox is suspended")
	case errors.Is(err, mailcompose.ErrNothingToSend):
		return c.AbortBadRequest("message has no recipients")
	case errors.Is(err, mailstore.ErrFolderExists):
		return c.AbortConflict(err.Error())
	default:
		return c.AbortInternalServerError("compose failed", err)
	}
}
