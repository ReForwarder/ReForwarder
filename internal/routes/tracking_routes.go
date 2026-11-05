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

// trackingRoutes returns public (no auth) route definitions for open/click/unsubscribe tracking.
func (r *Router) trackingRoutes() []okapi.RouteDefinition {
	return []okapi.RouteDefinition{
		{
			Method:  http.MethodGet,
			Path:    "/t/o/{message_id:int}.gif",
			Handler: okapi.H(r.h.tracking.OpenPixel),
			Tags:    []string{tagTracking},
			Summary: "Open tracking pixel",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
		{
			Method:  http.MethodGet,
			Path:    "/t/c/{message_id:int}/{hash}",
			Handler: okapi.H(r.h.tracking.ClickRedirect),
			Tags:    []string{tagTracking},
			Summary: "Click tracking redirect",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
		{
			Method:  http.MethodGet,
			Path:    "/t/u/{token}",
			Handler: okapi.H(r.h.tracking.UnsubscribePage),
			Tags:    []string{tagTracking},
			Summary: "Unsubscribe page",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
		{
			Method:  http.MethodPost,
			Path:    "/t/u/{token}",
			Handler: okapi.H(r.h.tracking.UnsubscribeConfirm),
			Tags:    []string{tagTracking},
			Summary: "Confirm unsubscribe",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
		{
			Method:  http.MethodGet,
			Path:    "/t/u/tx/{token}",
			Handler: okapi.H(r.h.tracking.TxUnsubscribePage),
			Tags:    []string{tagTracking},
			Summary: "Transactional unsubscribe page",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
		{
			Method:  http.MethodPost,
			Path:    "/t/u/tx/{token}",
			Handler: okapi.H(r.h.tracking.TxUnsubscribeConfirm),
			Tags:    []string{tagTracking},
			Summary: "Transactional one-click unsubscribe (RFC 8058)",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
		{
			Method:  http.MethodGet,
			Path:    "/t/v/{token}",
			Handler: okapi.H(r.h.tracking.WebView),
			Tags:    []string{tagTracking},
			Summary: "View email in browser",
			Options: []okapi.RouteOption{okapi.DocHide()},
		},
	}
}

// bounceWebhookRoutes returns the bounce webhook route (authenticated via API key).
func (r *Router) bounceWebhookRoutes() []okapi.RouteDefinition {
	bounceGroup := r.v1.Group(pathWebhookBounce, r.mw.apiKey).WithTagInfo(okapi.GroupTag{
		Name:        tagWebhooks,
		Description: "Inbound webhook endpoints that receive bounce and complaint notifications from upstream mail providers. Authenticated with an API key.",
	}).WithSecurity([]map[string][]string{{"ApiKeyAuth": {}}})

	return []okapi.RouteDefinition{
		{
			Method:   http.MethodPost,
			Path:     "",
			Handler:  okapi.H(r.h.bounceWebhook.HandleBounce),
			Group:    bounceGroup,
			Summary:  "Bounce notification webhook",
			Request:  &handlers.BounceNotification{},
			Response: &handlers.BounceResponse{},
		},
	}
}

// providerWebhookRoutes returns the provider event ingestion webhook (R-04):
// POST /api/v1/webhooks/provider/{provider}, authenticated via API key like
// the bounce webhook, with the provider's own signature verification layered
// on top inside the handler. Registered as a raw handler (no request binding)
// so the signature check sees the body byte-exact.
func (r *Router) providerWebhookRoutes() []okapi.RouteDefinition {
	providerGroup := r.v1.Group(pathProviderWebhook, r.mw.apiKey).WithTagInfo(okapi.GroupTag{
		Name:        tagWebhooks,
		Description: "Inbound webhook endpoints that receive bounce and complaint notifications from upstream mail providers. Authenticated with an API key.",
	}).WithSecurity([]map[string][]string{{"ApiKeyAuth": {}}})

	return []okapi.RouteDefinition{
		{
			Method:      http.MethodPost,
			Path:        "/{provider}",
			Handler:     r.h.providerEvent.Handle,
			Group:       providerGroup,
			Summary:     "Provider event webhook (SES, SendGrid, Postmark, Mailgun)",
			Description: "Ingests delivery events (delivered, bounced, deferred, rejected, complained, opened, clicked) from an upstream email provider, verifies the provider signature, records them on the message-history timeline and auto-suppresses permanently bounced recipients. Answers 501 until the provider secret is configured.",
			Response:    &handlers.ProviderEventResponse{},
			Options: []okapi.RouteOption{
				okapi.DocPathParam("provider", "string", "Provider slug: ses, sendgrid, postmark or mailgun"),
				okapi.DocErrorResponse(401, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(404, &dto.ErrorResponseBody{}),
				okapi.DocErrorResponse(501, &dto.ErrorResponseBody{}),
			},
		},
	}
}

// trackingAnalyticsRoutes returns the authenticated campaign analytics route,
// scoped to the active workspace (workspace-only migration §7).
func (r *Router) trackingAnalyticsRoutes() []okapi.RouteDefinition {
	wsGroup := r.v1.Group("/workspaces/current", r.mw.auth, r.mw.workspace).WithTagInfo(okapi.GroupTag{
		Name:        tagCampaigns,
		Description: "Email campaign analytics — open, click, bounce, and engagement metrics for campaigns in the active workspace.",
	})
	wsGroup.WithBearerAuth()

	return []okapi.RouteDefinition{
		{
			Method:   http.MethodGet,
			Path:     "/campaigns/{id:int}/analytics",
			Handler:  okapi.H(r.h.tracking.CampaignAnalytics),
			Group:    wsGroup,
			Summary:  "Get campaign analytics",
			Response: &handlers.CampaignAnalyticsResponse{},
			Options:  []okapi.RouteOption{okapi.DocPathParam("id", "integer", "Campaign ID")},
		},
	}
}
