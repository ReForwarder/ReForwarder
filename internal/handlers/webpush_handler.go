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
	"fmt"
	"net/url"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/webpush"
	"github.com/goposta/posta/internal/storage/repositories"
)

// WebPushHandler serves the SF-89 user-scoped browser push endpoints: the
// VAPID public key the browser subscribes with, and CRUD over the calling
// user's own push subscriptions. Every route lives under /users/me and is
// JWT-only — an API key cannot register a browser.
type WebPushHandler struct {
	service *webpush.Service
	repo    *repositories.UserPushSubscriptionRepository
}

// NewWebPushHandler builds the handler; service may be nil (push disabled —
// the config endpoint then answers enabled=false and the card hides itself).
func NewWebPushHandler(service *webpush.Service, repo *repositories.UserPushSubscriptionRepository) *WebPushHandler {
	return &WebPushHandler{service: service, repo: repo}
}

// PushConfigResponse is what the Profile card reads first: whether push is
// enabled and which VAPID key to subscribe with.
type PushConfigResponse struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key,omitempty"`
}

// GetConfig answers the push capability probe.
func (h *WebPushHandler) GetConfig(c *okapi.Context) error {
	if h.service == nil {
		return ok(c, PushConfigResponse{Enabled: false})
	}
	return ok(c, PushConfigResponse{Enabled: true, PublicKey: h.service.PublicKey()})
}

// SubscribeRequest is the browser's PushSubscription, reduced to what the
// backend stores: the endpoint URL and the RFC 8291 key material.
type SubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	UserAgent string `json:"user_agent,omitempty"`
}

// validate gates what we are willing to store: the endpoint must be an https
// push-service URL (http on loopback keeps local dev workable), and the RFC
// 8291 key material must be present — the keys are what make the stored row
// useless to anyone but the browser that issued them.
func (r *SubscribeRequest) validate() error {
	if r.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	u, err := url.Parse(r.Endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("endpoint must be a valid push-service URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return fmt.Errorf("endpoint must be an https push-service URL")
	}
	if r.Keys.P256DH == "" || r.Keys.Auth == "" {
		return fmt.Errorf("keys.p256dh and keys.auth are required")
	}
	return nil
}

// isLoopbackHost reports whether host is a loopback address.
func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Subscribe stores (or refreshes) the calling user's push subscription.
func (h *WebPushHandler) Subscribe(c *okapi.Context, req *SubscribeRequest) error {
	if h.service == nil || h.repo == nil {
		return c.AbortBadRequest("web push is not enabled")
	}
	if err := req.validate(); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	userID := uint(c.GetInt("user_id"))

	sub := &models.UserPushSubscription{
		UserID:    userID,
		Endpoint:  req.Endpoint,
		P256DH:    req.Keys.P256DH,
		Auth:      req.Keys.Auth,
		UserAgent: req.UserAgent,
	}
	if err := h.repo.Upsert(sub); err != nil {
		logger.Error("webpush: cannot store subscription", "error", err)
		return c.AbortInternalServerError("could not store push subscription")
	}
	count, _ := h.repo.CountByUser(userID)
	return ok(c, map[string]any{"message": "subscribed", "count": count})
}

// List returns the calling user's own subscriptions.
func (h *WebPushHandler) List(c *okapi.Context) error {
	if h.repo == nil {
		return ok(c, []models.UserPushSubscription{})
	}
	userID := uint(c.GetInt("user_id"))
	subs, err := h.repo.FindByUser(userID)
	if err != nil {
		return c.AbortInternalServerError("could not list push subscriptions")
	}
	if subs == nil {
		subs = []models.UserPushSubscription{}
	}
	return ok(c, subs)
}

// PushSubscriptionIDRequest carries the path parameter.
type PushSubscriptionIDRequest struct {
	ID uint `param:"id"`
}

// Unsubscribe removes one of the calling user's subscriptions by ID. The
// user_id filter makes cross-user deletes impossible.
func (h *WebPushHandler) Unsubscribe(c *okapi.Context, req *PushSubscriptionIDRequest) error {
	if h.repo == nil {
		return c.AbortBadRequest("web push is not enabled")
	}
	userID := uint(c.GetInt("user_id"))
	deleted, err := h.repo.Delete(userID, req.ID)
	if err != nil {
		return c.AbortInternalServerError("could not remove push subscription")
	}
	if !deleted {
		return c.AbortNotFound("push subscription not found")
	}
	return ok(c, map[string]string{"message": "unsubscribed"})
}
