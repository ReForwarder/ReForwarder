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

package routes

import (
	"net/http"

	"github.com/goposta/posta/internal/dto"
	"github.com/goposta/posta/internal/handlers"
	"github.com/jkaninda/okapi"
)

// mailComposeRoutes returns the OB-5 webmail compose surface (ADR-005 wave W3):
// attachment staging (ADR-006 scan gate), send into the Sent folder + outbound
// pipeline, the draft lifecycle, the reply/forward prefill frame, and the
// delivery-status view.
//
// Scoping: every route is user-scoped behind the dashboard JWT (an API key is
// not a mailbox owner) with the optional workspace middleware — the active
// workspace, when one is selected, narrows the mailstore scoping exactly like
// the other workspace-aware user surfaces.
//
// Addressing convention (kept uniform across this surface): {uid} path
// parameters are folder UIDs and every folder-scoped route takes mailbox_id
// (drafts) or folder_id + mailbox_id (the /mail/messages reads) as query
// parameters — UIDs are per-folder by design (ADR-003 §6).
func (r *Router) mailComposeRoutes() []okapi.RouteDefinition {
	composeGroup := r.v1.Group("/mail", r.mw.jwtOnly, r.mw.optionalWorkspace).WithTagInfo(okapi.GroupTag{
		Name:        tagMailCompose,
		Description: "Webmail compose (ADR-005): staged attachments (malware-scanned per ADR-006), send into the Sent folder and the outbound pipeline, drafts, reply/forward prefill, and delivery status over email_events. Requires a dashboard session.",
	})
	composeGroup.WithBearerAuth()

	routes := []okapi.RouteDefinition{
		{
			Method:      http.MethodPost,
			Path:        "/attachments",
			Handler:     r.h.mailCompose.UploadAttachment,
			Group:       composeGroup,
			Summary:     "Stage a compose attachment",
			Description: "Uploads one or more files (multipart/form-data, repeatable `file` parts). Each is scanned against clamd (ADR-006): a positive is refused with 409 and the signature, an unavailable scanner is refused under a closed fail mode and accepted unscanned under an open one. Content is stored in the encrypted blob layer; the response is the ref compose requests carry.",
			Response:    &dto.Response[[]handlers.ComposeAttachmentRefResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(409, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/send",
			Handler:     okapi.H(r.h.mailCompose.Send),
			Group:       composeGroup,
			Summary:     "Send a composed message",
			Description: "Files the message into the mailbox's Sent folder (quota-enforced) and enqueues the transport copy through the outbound pipeline (ADR-001 direct/relay per sending domain, DKIM-signed, attempts recorded) with provenance mail-compose. Attachment refs must have been staged by this user and scanned clean. Delivery truth surfaces on the sent message through email_events (GET /mail/messages/{uid}/delivery-status).",
			Request:     &handlers.ComposeSendRequest{},
			Response:    &dto.Response[handlers.SendResult]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(409, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/drafts",
			Handler:     okapi.H(r.h.mailCompose.SaveDraft),
			Group:       composeGroup,
			Summary:     "Save a draft",
			Description: "Stores the message as a Drafts-folder message (\\Draft flag) through the mailstore's quota-enforced append. Attachment refs must have been staged by this user.",
			Request:     &handlers.ComposeSendRequest{},
			Response:    &dto.Response[handlers.DraftResult]{},
			Options: []okapi.RouteOption{
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(409, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPut,
			Path:        "/drafts/{uid:int}",
			Handler:     okapi.H(r.h.mailCompose.UpdateDraft),
			Group:       composeGroup,
			Summary:     "Update a draft",
			Description: "Replaces the draft at the given Drafts-folder UID: the edited copy is appended carrying the original Message-ID and the previous revision is deleted and expunged. The mailbox is addressed with the mailbox_id query parameter.",
			Request:     &handlers.ComposeUpdateDraftRequest{},
			Response:    &dto.Response[handlers.DraftResult]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Draft UID in the Drafts folder"),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodPost,
			Path:        "/drafts/{uid:int}/send",
			Handler:     okapi.H(r.h.mailCompose.SendDraft),
			Group:       composeGroup,
			Summary:     "Send a stored draft",
			Description: "Sends the draft's stored content through the standard send path (Sent folder + outbound pipeline) and deletes the draft only after the send was accepted — a refused send leaves the draft intact. The mailbox is addressed with the mailbox_id query parameter.",
			Request:     &handlers.ComposeDraftUIDRequest{},
			Response:    &dto.Response[handlers.SendResult]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Draft UID in the Drafts folder"),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocResponse(409, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/messages/{uid:int}/reply-frame",
			Handler:     okapi.H(r.h.mailCompose.ReplyFrame),
			Group:       composeGroup,
			Summary:     "Reply/forward prefill frame",
			Description: "Returns the composer prefill for one stored message: reply target (Reply-To when present), reply-all Cc resolution (minus the mailbox's own address), Re:/Fwd: subject, In-Reply-To/References, and the bounded quoted text. The original HTML rides along for the frontend's sandboxed, remote-content-blocked renderer. Query: folder_id, mailbox_id, mode=reply|reply_all|forward (default reply).",
			Request:     &handlers.ComposeFrameRequest{},
			Response:    &dto.Response[handlers.ReplyFrameResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID in the folder"),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
		{
			Method:      http.MethodGet,
			Path:        "/messages/{uid:int}/delivery-status",
			Handler:     okapi.H(r.h.mailCompose.DeliveryStatus),
			Group:       composeGroup,
			Summary:     "Delivery status of a sent message",
			Description: "Reads the outbound pipeline's email_events timeline for a compose-sent message (linked through the message's X-Posta-Email-Uuid header). Delivered is only ever reported from provider/MX truth (ADR-001), never from queue acceptance. Query: folder_id.",
			Request:     &handlers.ComposeFrameRequest{},
			Response:    &dto.Response[handlers.DeliveryStatusResponse]{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("uid", "integer", "Message UID in the Sent folder"),
				okapi.DocResponse(400, &dto.ErrorResponseBody{}),
				okapi.DocResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocResponse(503, &dto.ErrorResponseBody{}),
			},
		},
	}
	return routes
}
