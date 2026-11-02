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
	"encoding/json"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// openPixelRe matches the open-tracking pixel injected by tracking.ProcessHTML so
// it can be stripped from the hosted "view in browser" page — rendering a stored
// campaign body in a browser would otherwise re-fire the open and inflate metrics.
var openPixelRe = regexp.MustCompile(`(?i)<img[^>]+src=["'][^"']*/t/o/[^"']*["'][^>]*>`)

type TrackingHandler struct {
	trackingRepo    *repositories.TrackingRepository
	messageRepo     *repositories.CampaignMessageRepository
	campaignRepo    *repositories.CampaignRepository
	subRepo         *repositories.SubscriberRepository
	listRepo        *repositories.SubscriberListRepository
	emailRepo       *repositories.EmailRepository
	suppressionRepo *repositories.SuppressionRepository
	trackingService *tracking.Service
	dispatcher      *webhook.Dispatcher
	recorder        *emailhistory.Recorder
}

func NewTrackingHandler(
	trackingRepo *repositories.TrackingRepository,
	messageRepo *repositories.CampaignMessageRepository,
	campaignRepo *repositories.CampaignRepository,
	subRepo *repositories.SubscriberRepository,
	listRepo *repositories.SubscriberListRepository,
	emailRepo *repositories.EmailRepository,
	suppressionRepo *repositories.SuppressionRepository,
	trackingService *tracking.Service,
	dispatcher *webhook.Dispatcher,
) *TrackingHandler {
	return &TrackingHandler{
		trackingRepo:    trackingRepo,
		messageRepo:     messageRepo,
		campaignRepo:    campaignRepo,
		subRepo:         subRepo,
		listRepo:        listRepo,
		emailRepo:       emailRepo,
		suppressionRepo: suppressionRepo,
		trackingService: trackingService,
		dispatcher:      dispatcher,
	}
}

type TrackingOpenRequest struct {
	// MessageID carries the id segment of the tracking URL: either the numeric
	// campaign-message id (legacy campaign URLs) or the email UUID followed by
	// the ".gif" pixel suffix. parseTrackingRef tells the two apart.
	MessageID string `param:"message_id"`
	Sig       string `query:"sig"`
}

type TrackingClickRequest struct {
	// MessageID carries the id segment of the tracking URL: either the numeric
	// campaign-message id or the email UUID of the message the link was in.
	MessageID string `param:"message_id"`
	Hash      string `param:"hash"`
	Sig       string `query:"sig"`
}

type TrackingUnsubscribeRequest struct {
	Token string `param:"token"`
}

type TrackingWebViewRequest struct {
	Token string `param:"token"`
}

// trackingRefKind tells which kind of message a tracking URL points at.
type trackingRefKind int

const (
	trackingRefCampaign trackingRefKind = iota // legacy campaign-message URLs
	trackingRefEmail                           // email-UUID URLs (all mail)
)

// trackingRef is the resolved target of a tracking URL's id segment.
type trackingRef struct {
	kind      trackingRefKind
	messageID uint   // campaign-message id when kind == trackingRefCampaign
	emailUUID string // email uuid when kind == trackingRefEmail
}

// parseTrackingRef classifies the id segment of a /t/ URL: a plain integer
// addresses a campaign message (campaign mail signed before email-keyed
// tracking existed), anything else is the email UUID. A trailing ".gif" —
// the pixel suffix on open URLs — is ignored.
func parseTrackingRef(raw string) trackingRef {
	raw = strings.TrimSuffix(raw, ".gif")
	if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
		return trackingRef{kind: trackingRefCampaign, messageID: uint(id)}
	}
	return trackingRef{kind: trackingRefEmail, emailUUID: raw}
}

// SetRecorder wires the message-history event recorder so opens, clicks and
// unsubscribes also land on the email event timeline. Nil (the default)
// disables that recording; bot-filtered hits record nothing either way.
func (h *TrackingHandler) SetRecorder(r *emailhistory.Recorder) {
	h.recorder = r
}

// OpenPixel serves a 1x1 transparent GIF and records the open event.
// Signature is mandatory; unsigned or bad-sig requests get 404.
func (h *TrackingHandler) OpenPixel(c *okapi.Context, req *TrackingOpenRequest) error {
	ref := parseTrackingRef(req.MessageID)
	if req.Sig == "" {
		return c.AbortNotFound("not found")
	}
	if ref.kind == trackingRefCampaign {
		if !h.trackingService.VerifyOpenSig(ref.messageID, req.Sig) {
			return c.AbortNotFound("not found")
		}
	} else if !h.trackingService.VerifyOpenEmailSig(ref.emailUUID, req.Sig) {
		return c.AbortNotFound("not found")
	}

	ua := c.Request().UserAgent()
	if !isBotUA(ua) {
		go h.recordOpen(ref, c.RealIP(), ua)
	}

	c.ResponseWriter().Header().Set("Content-Type", "image/gif")
	c.ResponseWriter().Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	c.ResponseWriter().WriteHeader(http.StatusOK)
	_, _ = c.ResponseWriter().Write(transparentPixel)
	return nil
}

// ClickRedirect records the click and redirects to the original URL.
func (h *TrackingHandler) ClickRedirect(c *okapi.Context, req *TrackingClickRequest) error {
	ref := parseTrackingRef(req.MessageID)
	if req.Sig == "" {
		return c.AbortNotFound("not found")
	}
	if ref.kind == trackingRefCampaign {
		if !h.trackingService.VerifyClickSig(ref.messageID, req.Hash, req.Sig) {
			return c.AbortNotFound("not found")
		}
	} else if !h.trackingService.VerifyClickEmailSig(ref.emailUUID, req.Hash, req.Sig) {
		return c.AbortNotFound("not found")
	}

	link, err := h.trackingRepo.FindLinkByHash(req.Hash)
	if err != nil {
		return c.AbortNotFound("link not found")
	}

	// Validate redirect URL to prevent SSRF
	if !strings.HasPrefix(link.OriginalURL, "http://") && !strings.HasPrefix(link.OriginalURL, "https://") {
		return c.AbortBadRequest("invalid redirect URL")
	}

	ua := c.Request().UserAgent()
	if !isBotUA(ua) {
		go h.recordClick(ref, link, c.RealIP(), ua)
	}

	c.Redirect(http.StatusFound, link.OriginalURL)
	return nil
}

// UnsubscribePage shows a simple unsubscribe confirmation page.
func (h *TrackingHandler) UnsubscribePage(c *okapi.Context, req *TrackingUnsubscribeRequest) error {
	messageID, err := h.trackingService.VerifyUnsubscribeToken(req.Token)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Link expired", "This unsubscribe link is invalid or has expired.", "error")
	}

	msg, err := h.messageRepo.FindByCampaignMessageID(messageID)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Not found", "We couldn't find the message this link points to.", "error")
	}

	sub, err := h.subRepo.FindByID(msg.SubscriberID)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Not found", "We couldn't find the recipient for this link.", "error")
	}

	return trackingHTMLView(c, http.StatusOK, "unsubscribe", unsubData{
		Title:       "Unsubscribe",
		Heading:     "Unsubscribe from this list?",
		Message:     "You're about to stop receiving these emails at:",
		Recipient:   sub.Email,
		ButtonLabel: "Confirm unsubscribe",
		Fine:        "Other lists you're subscribed to won't be affected.",
	})
}

// TxUnsubscribePage renders a confirmation page for a transactional one-click
// unsubscribe link. The link is RFC 8058 compliant: the POST variant will
// opt the recipient out without any further interaction.
func (h *TrackingHandler) TxUnsubscribePage(c *okapi.Context, req *TrackingUnsubscribeRequest) error {
	emailID, err := h.trackingService.VerifyTxUnsubscribeToken(req.Token)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Link expired", "This unsubscribe link is invalid or has expired.", "error")
	}
	em, err := h.emailRepo.FindByID(emailID)
	if err != nil || em == nil {
		return trackingNotice(c, http.StatusNotFound, "Not found", "We couldn't find the message this link points to.", "error")
	}
	shown := ""
	if len(em.Recipients) > 0 {
		shown = em.Recipients[0]
	}
	return trackingHTMLView(c, http.StatusOK, "unsubscribe", unsubData{
		Title:       "Unsubscribe",
		Heading:     "Unsubscribe from these emails?",
		Message:     "You're about to stop receiving this type of email at:",
		Recipient:   shown,
		ButtonLabel: "Confirm unsubscribe",
		Fine:        "You'll still receive other essential messages from the sender.",
	})
}

// TxUnsubscribeConfirm processes a POST to the transactional unsubscribe link.
// It is the RFC 8058 one-click endpoint — idempotent and requires no session.
// All recipients on the email are added to the scoped suppression list.
func (h *TrackingHandler) TxUnsubscribeConfirm(c *okapi.Context, req *TrackingUnsubscribeRequest) error {
	emailID, err := h.trackingService.VerifyTxUnsubscribeToken(req.Token)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Link expired", "This unsubscribe link is invalid or has expired.", "error")
	}
	em, err := h.emailRepo.FindByID(emailID)
	if err != nil || em == nil {
		return trackingNotice(c, http.StatusNotFound, "Not found", "We couldn't find the message this link points to.", "error")
	}

	if h.suppressionRepo != nil {
		kind := models.SuppressionKindHard
		if em.UnsubscribeListID != nil {
			kind = models.SuppressionKindListUnsubscribe
		}
		for _, addr := range em.Recipients {
			if addr == "" {
				continue
			}
			_ = h.suppressionRepo.Upsert(&models.Suppression{
				UserID:      em.UserID,
				WorkspaceID: em.WorkspaceID,
				Email:       addr,
				ListID:      em.UnsubscribeListID,
				Kind:        kind,
				Reason:      "one_click_unsubscribe",
			})
			emailhistory.RecordUnsubscribed(h.recorder, em, addr)
			h.emitUnsubscribed(em, addr)
		}
	}

	return trackingNotice(c, http.StatusOK, "You're unsubscribed",
		"You'll no longer receive emails of this type from the sender.", "success")
}

func (h *TrackingHandler) emitUnsubscribed(em *models.Email, addr string) {
	if h.dispatcher == nil {
		return
	}
	payload := struct {
		Event     string `json:"event"`
		EmailUUID string `json:"email_uuid"`
		Email     string `json:"email"`
		ListID    *uint  `json:"list_id,omitempty"`
		Timestamp string `json:"timestamp"`
	}{
		Event:     "email.unsubscribed",
		EmailUUID: em.UUID,
		Email:     addr,
		ListID:    em.UnsubscribeListID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h.dispatcher.DispatchJSON(em.UserID, em.WorkspaceID, "email.unsubscribed", body, em.Sender)
}

// WebView renders a hosted copy of a sent email ("view in browser").
func (h *TrackingHandler) WebView(c *okapi.Context, req *TrackingWebViewRequest) error {
	emailUUID, err := h.trackingService.VerifyWebViewToken(req.Token)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Link expired", "This link is invalid or has expired.", "error")
	}
	em, err := h.emailRepo.FindByUUID(emailUUID)
	if err != nil || em == nil {
		return trackingNotice(c, http.StatusNotFound, "Not found", "We couldn't find the message this link points to.", "error")
	}

	body := em.HTMLBody
	if strings.TrimSpace(body) == "" {
		body = "<pre style=\"white-space:pre-wrap;font-family:sans-serif;padding:24px;margin:0\">" + html.EscapeString(em.TextBody) + "</pre>"
	}
	body = openPixelRe.ReplaceAllString(body, "")

	hdr := c.ResponseWriter().Header()
	hdr.Set("Content-Security-Policy", "default-src 'none'; img-src https: http: data:; style-src 'unsafe-inline' https:; font-src https: data:; base-uri 'none'; form-action 'none'")
	hdr.Set("X-Robots-Tag", "noindex, nofollow")
	hdr.Set("Referrer-Policy", "no-referrer")

	return trackingHTMLView(c, http.StatusOK, "webview", webViewData{
		Subject: em.Subject,
		Body:    template.HTML(body),
	})
}

// UnsubscribeConfirm processes the unsubscribe action.
func (h *TrackingHandler) UnsubscribeConfirm(c *okapi.Context, req *TrackingUnsubscribeRequest) error {
	messageID, err := h.trackingService.VerifyUnsubscribeToken(req.Token)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Link expired", "This unsubscribe link is invalid or has expired.", "error")
	}

	msg, err := h.messageRepo.FindByCampaignMessageID(messageID)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Not found", "We couldn't find the message this link points to.", "error")
	}

	camp, err := h.campaignRepo.FindByID(msg.CampaignID)
	if err != nil {
		return trackingNotice(c, http.StatusNotFound, "Not found", "We couldn't find the campaign this link points to.", "error")
	}

	// Suppress this subscriber on the campaign's list
	if h.listRepo != nil {
		_ = h.listRepo.SuppressMember(camp.ListID, msg.SubscriberID, "user_unsubscribed")
	}

	// Mark the campaign message itself.
	_ = h.messageRepo.UpdateUnsubscribedAt(msg.ID)

	// Record event for analytics.
	_ = h.trackingRepo.CreateEvent(&models.TrackingEvent{
		CampaignMessageID: msg.ID,
		EventType:         models.TrackingEventUnsubscribe,
		IP:                c.RealIP(),
		UserAgent:         c.Request().UserAgent(),
	})

	// Mirror the unsubscribe onto the message-history timeline of the email
	// the campaign message was delivered as (absent for older campaign mail).
	emailhistory.RecordUnsubscribed(h.recorder, h.emailForMessage(msg), "")

	return trackingNotice(c, http.StatusOK, "You're unsubscribed",
		"You've been removed from this mailing list. Other lists you're subscribed to are unaffected.", "success")
}

type CampaignAnalyticsRequest struct {
	ID int `param:"id"`
}

type CampaignAnalyticsResponse struct {
	Analytics        *repositories.CampaignAnalytics            `json:"analytics"`
	VariantAnalytics map[string]*repositories.CampaignAnalytics `json:"variant_analytics,omitempty"`
	Links            []models.TrackedLink                       `json:"links"`
	OpenSeries       []repositories.TimeSeriesPoint             `json:"open_series"`
	ClickSeries      []repositories.TimeSeriesPoint             `json:"click_series"`
}

func (h *TrackingHandler) CampaignAnalytics(c *okapi.Context, req *CampaignAnalyticsRequest) error {
	campaign, err := h.campaignRepo.FindByID(uint(req.ID))
	if err != nil || !ownsResource(c, campaign.UserID, campaign.WorkspaceID) {
		return c.AbortNotFound("campaign not found")
	}

	analytics, err := h.trackingRepo.CampaignAnalytics(campaign.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to load analytics")
	}

	variantAnalytics, _ := h.trackingRepo.CampaignAnalyticsByVariant(campaign.ID)

	links, _ := h.trackingRepo.FindLinksByCampaign(campaign.ID)
	openSeries, _ := h.trackingRepo.EventTimeSeries(campaign.ID, models.TrackingEventOpen)
	clickSeries, _ := h.trackingRepo.EventTimeSeries(campaign.ID, models.TrackingEventClick)

	return ok(c, CampaignAnalyticsResponse{
		Analytics:        analytics,
		VariantAnalytics: variantAnalytics,
		Links:            links,
		OpenSeries:       openSeries,
		ClickSeries:      clickSeries,
	})
}

func (h *TrackingHandler) recordOpen(ref trackingRef, ip, userAgent string) {
	var em *models.Email
	if ref.kind == trackingRefCampaign {
		msg, err := h.messageRepo.FindByCampaignMessageID(ref.messageID)
		if err != nil {
			return
		}

		// Record first open on the campaign message
		if msg.OpenedAt == nil {
			now := time.Now()
			msg.OpenedAt = &now
			_ = h.messageRepo.UpdateOpenedAt(msg.ID)
		}

		// Always record the event (for total open tracking)
		_ = h.trackingRepo.CreateEvent(&models.TrackingEvent{
			CampaignMessageID: msg.ID,
			EventType:         models.TrackingEventOpen,
			IP:                ip,
			UserAgent:         userAgent,
		})

		em = h.emailForMessage(msg)
	} else if e, err := h.emailRepo.FindByUUID(ref.emailUUID); err == nil {
		em = e
	}

	// Mirror the open onto the message-history timeline. The write-once
	// opened_at stamp lives in the Recorder; repeated opens still add rows.
	emailhistory.RecordOpened(h.recorder, em, ip, userAgent)
}

func (h *TrackingHandler) recordClick(ref trackingRef, link *models.TrackedLink, ip, userAgent string) {
	var em *models.Email
	if ref.kind == trackingRefCampaign {
		msg, err := h.messageRepo.FindByCampaignMessageID(ref.messageID)
		if err != nil {
			return
		}

		// Record first click on the campaign message
		if msg.ClickedAt == nil {
			now := time.Now()
			msg.ClickedAt = &now
			_ = h.messageRepo.UpdateClickedAt(msg.ID)
		}

		// Increment link click count
		h.trackingRepo.IncrementLinkClickCount(link.ID)

		if !h.trackingRepo.HasClickEvent(msg.ID, link.ID) {
			_ = h.trackingRepo.CreateEvent(&models.TrackingEvent{
				CampaignMessageID: msg.ID,
				EventType:         models.TrackingEventClick,
				TrackedLinkID:     &link.ID,
				IP:                ip,
				UserAgent:         userAgent,
			})
		}

		em = h.emailForMessage(msg)
	} else if e, err := h.emailRepo.FindByUUID(ref.emailUUID); err == nil {
		em = e
	}

	// Mirror the click onto the message-history timeline: one row per distinct
	// interaction, the original destination URL included.
	emailhistory.RecordClicked(h.recorder, em, link.OriginalURL, ip, userAgent)
}

// emailForMessage resolves the Email row a campaign message was delivered as,
// so campaign opens/clicks also land on the message-history timeline. Returns
// nil when the link does not exist yet (older campaign mail).
func (h *TrackingHandler) emailForMessage(msg *models.CampaignMessage) *models.Email {
	if msg == nil || msg.EmailID == nil || h.emailRepo == nil {
		return nil
	}
	em, err := h.emailRepo.FindByID(*msg.EmailID)
	if err != nil {
		return nil
	}
	return em
}
