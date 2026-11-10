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

package routes

import (
	"net/http"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/handlers"
	"github.com/jkaninda/okapi"
)

// tagMail groups the OB-5 webmail routes in the generated specification.
// Defined here (not tags.go) because only this file uses it.
const tagMail = "Mail"

// mailRoutes returns the OB-5 webmail REST surface (ADR-005 decision 1):
// /api/v1/mail/* over the mailstore for the session user. Gating matches
// /users/me: jwtOnly (a machine API key must not read mailboxes) plus the
// optional workspace header, which narrows mailbox listings to the workspace
// without ever widening user scoping.
func (r *Router) mailRoutes() []okapi.RouteDefinition {
	group := r.v1.Group("/mail", r.mw.jwtOnly, r.mw.optionalWorkspace).WithTagInfo(okapi.GroupTag{
		Name: tagMail,
		Description: "Webmail over the hosted mailbox store (ADR-005): mailboxes, folders, " +
			"messages, flags, attachments and full-text search — strictly scoped to the session " +
			"user's own mailboxes. Requires a dashboard session; message HTML is served sanitized " +
			"(remote content blocked) for display in a sandboxed iframe.",
	})
	group.WithBearerAuth()

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodGet,
			Path:        "/mailboxes",
			Handler:     r.h.mail.ListMailboxes,
			Group:       group,
			Summary:     "List my mailboxes",
			Description: "The session user's mailboxes with their ADR-003 §5 quota and usage counters (raw bytes, message count, dedup-aware blob bytes).",
			Response:    &dto.Response[[]handlers.MailboxResponse]{},
		},
		{
			Method:      http.MethodGet,
			Path:        "/folders",
			Handler:     okapi.H(r.h.mail.ListFolders),
			Group:       group,
			Summary:     "List folders",
			Description: "One mailbox's folder tree with live total/unseen counters, ordered by IMAP path.",
			Request:     &handlers.MailFolderListRequest{},
			Response:    &dto.Response[[]handlers.MailFolderResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/folders",
			Handler:     okapi.H(r.h.mail.CreateFolder),
			Group:       group,
			Summary:     "Create a folder",
			Description: "Creates a folder under the optional parent IMAP path (empty = root). INBOX case-insensitivity applies to the first path segment.",
			Request:     &handlers.MailFolderCreateRequest{},
			Response:    &dto.Response[handlers.MailFolderResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPatch,
			Path:        "/folders/{id:int}",
			Handler:     okapi.H(r.h.mail.PatchFolder),
			Group:       group,
			Summary:     "Rename or (un)subscribe a folder",
			Description: "Renames (rewriting descendant IMAP paths) and/or toggles the subscription flag. Renaming INBOX is refused.",
			Request:     &handlers.MailFolderPatchRequest{},
			Response:    &dto.Response[handlers.MailFolderResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Folder ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/folders/{id:int}",
			Handler:     okapi.H(r.h.mail.DeleteFolder),
			Group:       group,
			Summary:     "Delete a folder",
			Description: "Deletes the folder and its whole subtree with all messages. Deleting INBOX is refused.",
			Request:     &handlers.MailFolderIDRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Folder ID"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/folders/{id:int}/expunge",
			Handler:     okapi.H(r.h.mail.ExpungeFolder),
			Group:       group,
			Summary:     "Expunge a folder",
			Description: "Permanently removes the folder's \\Deleted-marked messages (IMAP EXPUNGE semantics); UIDs of survivors stay stable.",
			Request:     &handlers.MailFolderIDRequest{},
			Response:    &dto.Response[handlers.MailExpungeResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("id", "integer", "Folder ID"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/messages",
			Handler:     okapi.H(r.h.mail.ListMessages),
			Group:       group,
			Summary:     "List messages in a folder",
			Description: "Paged listing of one folder (UID ascending), filterable by unread/flagged and by the ADR-003 full-text query `q`; with `q` the flag filters combine with the search.",
			Request:     &handlers.MailMessageListRequest{},
			Response:    &dto.PageableResponse[handlers.MailMessageSummary]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/messages/{uid:int}",
			Handler:     okapi.H(r.h.mail.GetMessage),
			Group:       group,
			Summary:     "Read a message",
			Description: "Metadata plus the renderable body (sanitized HTML when present — remote content blocked — else plain text) and the attachment list. Marks the message \\Seen like an IMAP BODY[] fetch.",
			Request:     &handlers.MailMessageRequest{},
			Response:    &dto.Response[handlers.MailMessageResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID within the folder"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/messages/{uid:int}/attachments/{index:int}",
			Handler:     okapi.H(r.h.mail.DownloadAttachment),
			Group:       group,
			Summary:     "Download an attachment",
			Description: "Streams the stored attachment bytes honoring the ingested content type, always as a download (Content-Disposition: attachment).",
			Request:     &handlers.MailAttachmentRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID within the folder"),
				okapi.DocPathParam("index", "integer", "0-based attachment index"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/messages/{uid:int}/flags",
			Handler:     okapi.H(r.h.mail.UpdateFlags),
			Group:       group,
			Summary:     "Update message flags",
			Description: "Sets/clears seen/flagged/answered/draft. With if_mod_seq the store is CAS: a concurrent change answers 409 (UNCHANGEDSINCE semantics).",
			Request:     &handlers.MailMessageFlagsRequest{},
			Response:    &dto.Response[handlers.MailFlagsResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID within the folder"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(409, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/messages/{uid:int}/move",
			Handler:     okapi.H(r.h.mail.MoveMessage),
			Group:       group,
			Summary:     "Move a message",
			Description: "Relocates the message to another folder of the same mailbox; the destination UID is returned.",
			Request:     &handlers.MailMessageMoveRequest{},
			Response:    &dto.Response[handlers.MailMoveResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID within the folder"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/messages/{uid:int}/copy",
			Handler:     okapi.H(r.h.mail.CopyMessage),
			Group:       group,
			Summary:     "Copy a message",
			Description: "Duplicates the message into another folder of the same mailbox (stored blobs are shared, not copied).",
			Request:     &handlers.MailMessageMoveRequest{},
			Response:    &dto.Response[handlers.MailMoveResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID within the folder"),
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodDelete,
			Path:        "/messages/{uid:int}",
			Handler:     okapi.H(r.h.mail.DeleteMessage),
			Group:       group,
			Summary:     "Delete a message",
			Description: "Marks the message \\Deleted; the row leaves permanently at folder expunge (POST /mail/folders/{id}/expunge).",
			Request:     &handlers.MailMessageRequest{},
			Response:    &dto.Response[dto.MessageData]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID within the folder"),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/search",
			Handler:     okapi.H(r.h.mail.Search),
			Group:       group,
			Summary:     "Search mail",
			Description: "ADR-003 full-text search (websearch syntax) across one mailbox, optionally narrowed to a folder; ranked, paged results.",
			Request:     &handlers.MailSearchRequest{},
			Response:    &dto.PageableResponse[handlers.MailMessageSummary]{},
			Options: []okapi.RouteOption{
				okapi.DocErrorResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(503, &dto.ErrorResponseBody{}),
			},
		},
	}
}
