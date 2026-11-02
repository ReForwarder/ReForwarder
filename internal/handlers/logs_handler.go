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
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// LogsHandler serves the workspace message-history and logs endpoints: the
// message list and detail (never including bodies — content lives behind the
// dedicated content endpoint), the message event timeline, the raw incoming
// webhook payloads, the outgoing webhook delivery history with its per-attempt
// rows, and the audit log with its CSV export.
type LogsHandler struct {
	db             *gorm.DB
	recorder       *emailhistory.Recorder
	settings       *settings.Provider
	emailRepo      *repositories.EmailRepository
	eventRepo      *repositories.EventRepository
	emailEventRepo *repositories.EmailEventRepository
	incomingRepo   *repositories.IncomingWebhookEventRepository
	deliveryRepo   *repositories.WebhookDeliveryRepository
	attemptRepo    *repositories.WebhookDeliveryAttemptRepository
}

// NewLogsHandler returns a LogsHandler over repositories built on db. The
// recorder is stored for the message-history capture path; the repositories
// can be replaced per instance via the Set* setters (used by tests).
func NewLogsHandler(db *gorm.DB, recorder *emailhistory.Recorder, sp *settings.Provider) *LogsHandler {
	return &LogsHandler{
		db:             db,
		recorder:       recorder,
		settings:       sp,
		emailRepo:      repositories.NewEmailRepository(db),
		eventRepo:      repositories.NewEventRepository(db),
		emailEventRepo: repositories.NewEmailEventRepository(db),
		incomingRepo:   repositories.NewIncomingWebhookEventRepository(db),
		deliveryRepo:   repositories.NewWebhookDeliveryRepository(db),
		attemptRepo:    repositories.NewWebhookDeliveryAttemptRepository(db),
	}
}

// SetRecorder replaces the emailhistory recorder.
func (h *LogsHandler) SetRecorder(r *emailhistory.Recorder) { h.recorder = r }

// SetSettings wires the settings provider backing the content-visibility gate.
func (h *LogsHandler) SetSettings(sp *settings.Provider) { h.settings = sp }

func (h *LogsHandler) SetEmailRepo(r *repositories.EmailRepository)           { h.emailRepo = r }
func (h *LogsHandler) SetEventRepo(r *repositories.EventRepository)           { h.eventRepo = r }
func (h *LogsHandler) SetEmailEventRepo(r *repositories.EmailEventRepository) { h.emailEventRepo = r }
func (h *LogsHandler) SetIncomingWebhookEventRepo(r *repositories.IncomingWebhookEventRepository) {
	h.incomingRepo = r
}
func (h *LogsHandler) SetWebhookDeliveryRepo(r *repositories.WebhookDeliveryRepository) {
	h.deliveryRepo = r
}
func (h *LogsHandler) SetWebhookDeliveryAttemptRepo(r *repositories.WebhookDeliveryAttemptRepository) {
	h.attemptRepo = r
}

// contentVisible reports whether email body content may leave the server. The
// setting defaults to hidden: when no provider is wired, bodies stay redacted.
func (h *LogsHandler) contentVisible() bool {
	return h.settings != nil && h.settings.EmailContentVisibility()
}

// isDeploymentAdmin reports whether the caller holds a dashboard admin session.
// Deployment-wide views are reserved for admins on the dashboard: an API key is
// never a deployment credential, whatever role its owner holds.
func isDeploymentAdmin(c *okapi.Context) bool {
	if c.GetString(middlewares.CtxAuthMethod) != middlewares.AuthMethodJWT {
		return false
	}
	return models.UserRole(c.GetString(middlewares.CtxUserRole)) == models.UserRoleAdmin
}

// ---------- Message history ----------

// MessageEventRow is one timeline entry attached to a message row.
type MessageEventRow struct {
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
}

// MessageRow is the slim message-history list row: every metadata column of an
// email, none of its bodies or content blobs. The full timeline is embedded so
// the UI can render status icons without one request per row.
type MessageRow struct {
	UUID           string             `json:"uuid"`
	ExternalID     string             `json:"external_id,omitempty"`
	Sender         string             `json:"sender"`
	Recipient      string             `json:"recipient"`
	Subject        string             `json:"subject"`
	Status         models.EmailStatus `json:"status"`
	TemplateName   string             `json:"template_name,omitempty"`
	BroadcastID    *uint              `json:"broadcast_id,omitempty"`
	BroadcastName  string             `json:"broadcast_name,omitempty"`
	ListID         *uint              `json:"list_id,omitempty"`
	Events         []MessageEventRow  `json:"events"`
	Error          string             `json:"error,omitempty"`
	Provider       string             `json:"provider,omitempty"`
	SMTPHostname   string             `json:"smtp_hostname,omitempty"`
	APIKeyID       *uint              `json:"api_key_id,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	SentAt         *time.Time         `json:"sent_at,omitempty"`
	DeliveredAt    *time.Time         `json:"delivered_at,omitempty"`
	OpenedAt       *time.Time         `json:"opened_at,omitempty"`
	ClickedAt      *time.Time         `json:"clicked_at,omitempty"`
	BouncedAt      *time.Time         `json:"bounced_at,omitempty"`
	ComplainedAt   *time.Time         `json:"complained_at,omitempty"`
	UnsubscribedAt *time.Time         `json:"unsubscribed_at,omitempty"`
}

type ListLogsMessagesRequest struct {
	Page        int    `query:"page" default:"0"`
	Size        int    `query:"size" default:"20"`
	Channel     string `query:"channel" doc:"Only 'email' exists; the platform sends email exclusively"`
	Contact     string `query:"contact"`
	MessageID   string `query:"message_id" doc:"Exact message UUID"`
	ExternalID  string `query:"external_id"`
	ListID      string `query:"list_id" doc:"Unsubscribe list the message was sent against"`
	TemplateID  string `query:"template_id" doc:"Template name substring"`
	BlockID     string `query:"block_id" doc:"Block list whose suppressed recipients received the message"`
	BroadcastID string `query:"broadcast_id" doc:"Campaign the message was sent by"`
}

// ListMessages returns a page of the workspace message history. The status
// filter is repeatable and non-exclusive: status=sent&status=opened matches
// messages that are either (a message carries every state it ever reached).
func (h *LogsHandler) ListMessages(c *okapi.Context, req *ListLogsMessagesRequest) error {
	// Repeatable query params are parsed by hand — binders collapse to the
	// last occurrence, and status=sent&status=opened must mean ANY-of.
	statuses := c.Request().URL.Query()["status"]

	if req.Channel != "" && !strings.EqualFold(req.Channel, "email") {
		return c.AbortBadRequest("unsupported channel: this platform sends email only")
	}
	listID, err := parseOptionalID(c, "list_id", req.ListID)
	if err != nil {
		return err
	}
	blockID, err := parseOptionalID(c, "block_id", req.BlockID)
	if err != nil {
		return err
	}
	broadcastID, err := parseOptionalID(c, "broadcast_id", req.BroadcastID)
	if err != nil {
		return err
	}

	filter := repositories.EmailFilter{
		StatusFlags: statuses,
		Recipient:   req.Contact,
		ExternalID:  req.ExternalID,
		Template:    req.TemplateID,
		MessageUUID: req.MessageID,
		ListID:      listID,
		BlockID:     blockID,
		BroadcastID: broadcastID,
	}

	page, size, _ := normalizePageParams(req.Page, req.Size)
	emails, total, err := h.emailRepo.ListFilteredMessages(page, size, getScope(c), filter)
	if err != nil {
		return c.AbortInternalServerError("failed to list messages")
	}

	rows := h.buildMessageRows(emails)
	return paginated(c, rows, total, page, size)
}

// buildMessageRows converts slim email rows into history rows and decorates
// them with their event timeline and broadcast context in batched queries.
func (h *LogsHandler) buildMessageRows(emails []models.Email) []MessageRow {
	rows := make([]MessageRow, 0, len(emails))
	uuids := make([]string, 0, len(emails))
	emailIDs := make([]uint, 0, len(emails))
	for _, em := range emails {
		recipient := ""
		if len(em.Recipients) > 0 {
			recipient = em.Recipients[0]
		}
		rows = append(rows, MessageRow{
			UUID:           em.UUID,
			ExternalID:     em.ExternalID,
			Sender:         em.Sender,
			Recipient:      recipient,
			Subject:        em.Subject,
			Status:         em.Status,
			TemplateName:   em.TemplateName,
			ListID:         em.UnsubscribeListID,
			Events:         []MessageEventRow{},
			Error:          em.ErrorMessage,
			Provider:       em.Provider,
			SMTPHostname:   em.SMTPHostname,
			APIKeyID:       em.APIKeyID,
			CreatedAt:      em.CreatedAt,
			SentAt:         em.SentAt,
			DeliveredAt:    em.DeliveredAt,
			OpenedAt:       em.OpenedAt,
			ClickedAt:      em.ClickedAt,
			BouncedAt:      em.BouncedAt,
			ComplainedAt:   em.ComplainedAt,
			UnsubscribedAt: em.UnsubscribedAt,
		})
		uuids = append(uuids, em.UUID)
		emailIDs = append(emailIDs, em.ID)
	}
	if len(rows) == 0 {
		return rows
	}

	if events, err := h.emailEventRepo.ListByEmailUUIDs(uuids); err == nil {
		byEmail := make(map[string][]MessageEventRow, len(uuids))
		for _, ev := range events {
			byEmail[ev.EmailUUID] = append(byEmail[ev.EmailUUID], MessageEventRow{
				Type:       ev.Type,
				OccurredAt: ev.OccurredAt,
			})
		}
		for i := range rows {
			if evs, ok := byEmail[rows[i].UUID]; ok {
				rows[i].Events = evs
			}
		}
	} else {
		logger.Warn("failed to load message events", "error", err)
	}

	byEmailID, names := h.broadcastContext(emailIDs)
	for i := range rows {
		if id := emailIDs[i]; byEmailID != nil {
			if campaignID, ok := byEmailID[id]; ok {
				rows[i].BroadcastID = &campaignID
				rows[i].BroadcastName = names[campaignID]
			}
		}
	}
	return rows
}

// broadcastContext resolves which campaign each email belongs to and the
// campaigns' names, in two batched queries. Both lookups are best-effort: a
// transactional message has no broadcast, and failures leave the fields empty.
func (h *LogsHandler) broadcastContext(emailIDs []uint) (map[uint]uint, map[uint]string) {
	if len(emailIDs) == 0 {
		return nil, nil
	}
	var links []struct {
		EmailID    *uint `gorm:"column:email_id"`
		CampaignID uint  `gorm:"column:campaign_id"`
	}
	if err := h.db.Model(&models.CampaignMessage{}).
		Select("email_id, campaign_id").
		Where("email_id IN ?", emailIDs).
		Scan(&links).Error; err != nil {
		logger.Warn("failed to resolve broadcast context", "error", err)
		return nil, nil
	}

	byEmailID := make(map[uint]uint, len(links))
	campaignIDs := make([]uint, 0, len(links))
	seen := make(map[uint]bool, len(links))
	for _, l := range links {
		if l.EmailID == nil {
			continue
		}
		byEmailID[*l.EmailID] = l.CampaignID
		if !seen[l.CampaignID] {
			seen[l.CampaignID] = true
			campaignIDs = append(campaignIDs, l.CampaignID)
		}
	}
	if len(campaignIDs) == 0 {
		return byEmailID, map[uint]string{}
	}

	var campaigns []struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := h.db.Model(&models.Campaign{}).
		Select("id, name").
		Where("id IN ?", campaignIDs).
		Scan(&campaigns).Error; err != nil {
		logger.Warn("failed to resolve broadcast names", "error", err)
		return byEmailID, map[uint]string{}
	}
	names := make(map[uint]string, len(campaigns))
	for _, cp := range campaigns {
		names[cp.ID] = cp.Name
	}
	return byEmailID, names
}

// parseOptionalID parses a numeric filter value. Absent stays nil; a malformed
// value is a client error rather than a silently ignored filter.
func parseOptionalID(c *okapi.Context, name, raw string) (*uint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return nil, c.AbortBadRequest(fmt.Sprintf("invalid %s: expected a positive integer", name))
	}
	idUint := uint(id)
	return &idUint, nil
}

// GetMessage returns one message without its bodies or content blobs — the
// same shape as the list rows, so a plain Get can never leak HTML.
func (h *LogsHandler) GetMessage(c *okapi.Context, req *LogsMessageRequest) error {
	em, err := h.emailRepo.FindByUUIDSlim(req.UUID)
	if err != nil || !ownsResource(c, em.UserID, em.WorkspaceID) {
		return c.AbortNotFound("message not found")
	}
	return ok(c, em)
}

// LogsMessageRequest addresses one message by UUID.
type LogsMessageRequest struct {
	UUID string `param:"uuid"`
}

// MessageAttachmentInfo describes an attachment without its content.
// Attachment size is not tracked on the email row, so it is not exposed.
type MessageAttachmentInfo struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	StorageKey  string `json:"storage_key,omitempty"`
}

// MessageContent is the payload of the content endpoint: the bodies, the
// render data, the headers and the attachment manifest of one message.
type MessageContent struct {
	Redacted     bool                    `json:"redacted,omitempty"`
	HTMLBody     string                  `json:"html_body,omitempty"`
	TextBody     string                  `json:"text_body,omitempty"`
	TemplateData map[string]any          `json:"template_data,omitempty"`
	Headers      map[string]any          `json:"headers,omitempty"`
	Attachments  []MessageAttachmentInfo `json:"attachments,omitempty"`
	TemplateName string                  `json:"template_name,omitempty"`
}

// GetMessageContent returns the bodies and render data of one message. When
// the platform's email_content_visibility setting hides content, it answers
// 200 with {redacted: true} — a privacy notice the UI renders in place of the
// preview, not an error.
func (h *LogsHandler) GetMessageContent(c *okapi.Context, req *LogsMessageRequest) error {
	em, err := h.emailRepo.FindByUUID(req.UUID)
	if err != nil || !ownsResource(c, em.UserID, em.WorkspaceID) {
		return c.AbortNotFound("message not found")
	}

	if !h.contentVisible() {
		return ok(c, MessageContent{Redacted: true})
	}

	content := MessageContent{
		HTMLBody:     em.HTMLBody,
		TextBody:     em.TextBody,
		TemplateName: em.TemplateName,
		Attachments:  []MessageAttachmentInfo{},
	}
	if em.TemplateData != "" {
		if data := mapJSON(em.TemplateData); data != nil {
			content.TemplateData = data
		}
	}
	if em.HeadersJSON != "" {
		if headers := mapJSON(em.HeadersJSON); headers != nil {
			content.Headers = headers
		}
	}
	if em.AttachmentsJSON != "" {
		var atts []models.Attachment
		if json.Unmarshal([]byte(em.AttachmentsJSON), &atts) == nil {
			for _, a := range atts {
				content.Attachments = append(content.Attachments, MessageAttachmentInfo{
					Filename:    a.Filename,
					ContentType: a.ContentType,
					StorageKey:  a.StorageKey,
				})
			}
		}
	}
	return ok(c, content)
}

// mapJSON parses a JSON object column into a generic map; anything else (empty,
// arrays, malformed) yields nil so the field is simply omitted.
func mapJSON(raw string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// GetMessageEvents returns the event timeline of one message, oldest first.
func (h *LogsHandler) GetMessageEvents(c *okapi.Context, req *LogsMessageRequest) error {
	em, err := h.emailRepo.FindByUUIDSlim(req.UUID)
	if err != nil || !ownsResource(c, em.UserID, em.WorkspaceID) {
		return c.AbortNotFound("message not found")
	}
	events, err := h.emailEventRepo.ListByEmailUUID(req.UUID)
	if err != nil {
		return c.AbortInternalServerError("failed to list message events")
	}
	if events == nil {
		events = []models.EmailEvent{}
	}
	return ok(c, events)
}

// ---------- Incoming webhooks ----------

// IncomingWebhookRow is a stored raw webhook payload without the payload
// itself — the body can be large and is only served by the detail endpoint.
type IncomingWebhookRow struct {
	ID           uint      `json:"id"`
	Source       string    `json:"source"`
	Type         string    `json:"type"`
	EmailUUID    string    `json:"email_uuid,omitempty"`
	Recipient    string    `json:"recipient"`
	MessageID    string    `json:"message_id,omitempty"`
	ProcessError string    `json:"process_error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type ListIncomingWebhooksRequest struct {
	Page      int    `query:"page" default:"0"`
	Size      int    `query:"size" default:"20"`
	Source    string `query:"source"`
	Type      string `query:"type"`
	Recipient string `query:"recipient"`
	EmailUUID string `query:"email_uuid"`
}

// ListIncomingWebhooks returns a page of stored webhook payloads for the
// current workspace. The store itself is deployment-wide (payloads captured
// before processing carry no workspace), so the list is scoped by joining the
// linked email; payloads that never resolved to a message stay hidden here.
func (h *LogsHandler) ListIncomingWebhooks(c *okapi.Context, req *ListIncomingWebhooksRequest) error {
	scope := getScope(c)
	if scope.WorkspaceID == nil {
		return paginated(c, []IncomingWebhookRow{}, 0, req.Page, req.Size)
	}

	page, size, offset := normalizePageParams(req.Page, req.Size)
	q := func(base *gorm.DB) *gorm.DB {
		q := base.Where(
			"email_uuid IN (SELECT uuid FROM emails WHERE workspace_id = ?)", *scope.WorkspaceID,
		)
		if req.Source != "" {
			q = q.Where("source = ?", req.Source)
		}
		if req.Type != "" {
			q = q.Where("type = ?", req.Type)
		}
		if req.Recipient != "" {
			q = q.Where("recipient ILIKE ?", "%"+req.Recipient+"%")
		}
		if req.EmailUUID != "" {
			q = q.Where("email_uuid = ?", req.EmailUUID)
		}
		return q
	}

	var total int64
	if err := q(h.db.Model(&models.IncomingWebhookEvent{})).Count(&total).Error; err != nil {
		return c.AbortInternalServerError("failed to list incoming webhooks")
	}

	var events []models.IncomingWebhookEvent
	if err := q(h.db).
		Order("created_at DESC").
		Limit(size).Offset(offset).
		Find(&events).Error; err != nil {
		return c.AbortInternalServerError("failed to list incoming webhooks")
	}

	rows := make([]IncomingWebhookRow, 0, len(events))
	for _, ev := range events {
		rows = append(rows, IncomingWebhookRow{
			ID:           ev.ID,
			Source:       ev.Source,
			Type:         ev.Type,
			EmailUUID:    ev.EmailUUID,
			Recipient:    ev.Recipient,
			MessageID:    ev.MessageID,
			ProcessError: ev.ProcessError,
			CreatedAt:    ev.CreatedAt,
		})
	}
	return paginated(c, rows, total, page, size)
}

// GetIncomingWebhook returns one stored payload including the raw body. The
// event is only served when it resolves to a message in the caller's scope;
// payloads that never matched a message are deployment diagnostics, visible to
// dashboard admins only.
func (h *LogsHandler) GetIncomingWebhook(c *okapi.Context, req *LogsWebhookEventRequest) error {
	event, err := h.incomingRepo.Get(uint(req.ID))
	if err != nil {
		return c.AbortNotFound("incoming webhook not found")
	}
	if event.EmailUUID == "" {
		if !isDeploymentAdmin(c) {
			return c.AbortNotFound("incoming webhook not found")
		}
	} else {
		em, err := h.emailRepo.FindByUUIDSlim(event.EmailUUID)
		if err != nil || !ownsResource(c, em.UserID, em.WorkspaceID) {
			return c.AbortNotFound("incoming webhook not found")
		}
	}
	return ok(c, event)
}

// LogsWebhookEventRequest addresses one stored payload by id.
type LogsWebhookEventRequest struct {
	ID int `param:"id"`
}

// ---------- Outgoing webhooks ----------

// OutgoingDeliveryRow is a webhook delivery with its recorded attempt count.
type OutgoingDeliveryRow struct {
	models.WebhookDelivery
	AttemptCount int64 `json:"attempt_count"`
}

type ListOutgoingWebhooksRequest struct {
	Page      int    `query:"page" default:"0"`
	Size      int    `query:"size" default:"20"`
	WebhookID string `query:"webhook_id"`
	Status    string `query:"status"`
}

// ListOutgoingWebhooks returns a page of outgoing webhook deliveries for the
// current workspace, each with the number of HTTP attempts recorded for it.
func (h *LogsHandler) ListOutgoingWebhooks(c *okapi.Context, req *ListOutgoingWebhooksRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)
	scope := getScope(c)

	var deliveries []models.WebhookDelivery
	var total int64
	var err error

	webhookID, whErr := parseOptionalID(c, "webhook_id", req.WebhookID)
	if whErr != nil {
		return whErr
	}
	status := models.WebhookDeliveryStatus(strings.ToLower(strings.TrimSpace(req.Status)))
	if status != "" && status != models.WebhookDeliverySuccess && status != models.WebhookDeliveryFailed {
		return c.AbortBadRequest("invalid status: expected success or failed")
	}

	if webhookID != nil || status != "" {
		// The repository's FindByScope has no column filters, so the filtered
		// page is queried here — same scope, order and pagination contract.
		q := func(base *gorm.DB) *gorm.DB {
			q := repositories.ApplyScope(base, scope)
			if webhookID != nil {
				q = q.Where("webhook_id = ?", *webhookID)
			}
			if status != "" {
				q = q.Where("status = ?", status)
			}
			return q
		}
		if err := q(h.db.Model(&models.WebhookDelivery{})).Count(&total).Error; err != nil {
			return c.AbortInternalServerError("failed to list outgoing webhooks")
		}
		if err = q(h.db).
			Order("created_at DESC").
			Limit(size).Offset(offset).
			Find(&deliveries).Error; err != nil {
			return c.AbortInternalServerError("failed to list outgoing webhooks")
		}
	} else {
		deliveries, total, err = h.deliveryRepo.FindByScope(scope, size, offset)
		if err != nil {
			return c.AbortInternalServerError("failed to list outgoing webhooks")
		}
	}

	rows, err := h.attachAttemptCounts(deliveries)
	if err != nil {
		return c.AbortInternalServerError("failed to list outgoing webhooks")
	}
	return paginated(c, rows, total, page, size)
}

// attachAttemptCounts counts recorded attempts per delivery in one grouped
// query. Counting failures leave the counts at zero rather than failing the page.
func (h *LogsHandler) attachAttemptCounts(deliveries []models.WebhookDelivery) ([]OutgoingDeliveryRow, error) {
	ids := make([]uint, 0, len(deliveries))
	for _, d := range deliveries {
		ids = append(ids, d.ID)
	}
	counts := make(map[uint]int64, len(ids))
	if len(ids) > 0 {
		var err error
		if counts, err = h.attemptRepo.CountByDeliveryIDs(ids); err != nil {
			return nil, err
		}
	}
	rows := make([]OutgoingDeliveryRow, 0, len(deliveries))
	for _, d := range deliveries {
		rows = append(rows, OutgoingDeliveryRow{WebhookDelivery: d, AttemptCount: counts[d.ID]})
	}
	return rows, nil
}

// GetOutgoingAttempts returns every recorded HTTP attempt of one delivery, in
// attempt order — the retry story the delivery row itself only summarizes.
func (h *LogsHandler) GetOutgoingAttempts(c *okapi.Context, req *LogsWebhookDeliveryRequest) error {
	var delivery models.WebhookDelivery
	if err := h.db.First(&delivery, uint(req.ID)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.AbortNotFound("outgoing webhook delivery not found")
		}
		return c.AbortInternalServerError("failed to load outgoing webhook delivery")
	}
	if !ownsResource(c, delivery.UserID, delivery.WorkspaceID) {
		return c.AbortNotFound("outgoing webhook delivery not found")
	}

	attempts, err := h.attemptRepo.ListByDeliveryID(delivery.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list delivery attempts")
	}
	if attempts == nil {
		attempts = []models.WebhookDeliveryAttempt{}
	}
	return ok(c, attempts)
}

// LogsWebhookDeliveryRequest addresses one delivery by id.
type LogsWebhookDeliveryRequest struct {
	ID int `param:"id"`
}

// ---------- Audit log ----------

// auditOutcomes is the closed set of audit outcomes (CONTRACTS §1): what the
// action did — 2xx/3xx are success, refusals are denied, everything else failed.
var auditOutcomes = map[string]bool{
	"success": true,
	"failure": true,
	"denied":  true,
}

type ListAuditLogsRequest struct {
	Page       int    `query:"page" default:"0"`
	Size       int    `query:"size" default:"20"`
	Action     string `query:"action"`
	Outcome    string `query:"outcome"`
	Actor      string `query:"actor"`
	TargetType string `query:"target_type"`
	TargetID   string `query:"target_id"`
	IP         string `query:"ip"`
	From       string `query:"from"`
	To         string `query:"to"`
	Scope      string `query:"scope" doc:"workspace (default) or deployment — deployment requires an admin session"`
}

// ListAudit returns a page of audit events. The workspace scope always filters
// to the current workspace; scope=deployment spans the whole platform and is
// reserved for dashboard admins — anyone else is refused with 403.
func (h *LogsHandler) ListAudit(c *okapi.Context, req *ListAuditLogsRequest) error {
	filter, err := h.buildAuditFilter(c, req.Scope, req.Action, req.Outcome, req.Actor,
		req.TargetType, req.TargetID, req.IP, req.From, req.To)
	if err != nil {
		return err
	}

	page, size, _ := normalizePageParams(req.Page, req.Size)
	events, total, err := h.eventRepo.FindFiltered(page, size, filter)
	if err != nil {
		return c.AbortInternalServerError("failed to list audit events")
	}
	return paginated(c, events, total, page, size)
}

// buildAuditFilter validates the shared audit filters. Deployment scope demands
// an admin session and clears the workspace restriction; workspace scope pins
// the filter to the caller's workspace.
func (h *LogsHandler) buildAuditFilter(c *okapi.Context, scope, action, outcome, actor, targetType, targetID, ip, from, to string) (repositories.AuditEventFilter, error) {
	filter := repositories.AuditEventFilter{
		Action:     action,
		Actor:      actor,
		TargetType: targetType,
		TargetID:   targetID,
		IP:         ip,
	}

	outcome = strings.ToLower(strings.TrimSpace(outcome))
	if outcome != "" {
		if !auditOutcomes[outcome] {
			return filter, c.AbortBadRequest("invalid outcome: expected success, failure or denied")
		}
		filter.Outcome = outcome
	}

	fromAt, ok := parseAuditTime(from, false)
	if !ok {
		return filter, c.AbortBadRequest("invalid from: expected RFC3339 or YYYY-MM-DD")
	}
	toAt, ok := parseAuditTime(to, true)
	if !ok {
		return filter, c.AbortBadRequest("invalid to: expected RFC3339 or YYYY-MM-DD")
	}
	filter.From = fromAt
	filter.To = toAt

	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "workspace":
		scope = "workspace"
		if wsID := c.GetInt("workspace_id"); wsID > 0 {
			wid := uint(wsID)
			filter.WorkspaceID = &wid
		} else {
			// No workspace binding: like ApplyScope, match nothing rather than
			// fall back to an unscoped query.
			logger.Warn("audit list: request has no workspace; matching nothing")
			wid := uint(0)
			filter.WorkspaceID = &wid
		}
	case "deployment":
		if !isDeploymentAdmin(c) {
			return filter, c.AbortForbidden("deployment-wide audit logs require an admin session")
		}
	default:
		return filter, c.AbortBadRequest("invalid scope: expected workspace or deployment")
	}
	return filter, nil
}

// parseAuditTime accepts an RFC3339 timestamp or a date-only value. A date-only
// "to" bound is advanced a day so the whole day is included — the repo compares
// created_at < to. Nil is returned when the value is empty. ok=false signals a
// malformed value the caller turns into a 400.
func parseAuditTime(raw string, end bool) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, true
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if end {
			t = t.Add(24 * time.Hour)
		}
		return &t, true
	}
	return nil, false
}

// auditExportMaxRows caps one CSV export.
const auditExportMaxRows = 100000

// auditExportFlushEvery streams the export in batches so the response starts
// moving before the whole body is rendered.
const auditExportFlushEvery = 500

// ExportAudit streams the audit log as CSV with the same filters and the same
// authorization as ListAudit. Filters are validated before the first byte so a
// refusal is still a JSON error; the body is written and flushed in batches —
// the response is never buffered whole.
func (h *LogsHandler) ExportAudit(c *okapi.Context) error {
	q := c.Request().URL.Query()
	filter, err := h.buildAuditFilter(c,
		q.Get("scope"), q.Get("action"), q.Get("outcome"), q.Get("actor"),
		q.Get("target_type"), q.Get("target_id"), q.Get("ip"),
		q.Get("from"), q.Get("to"))
	if err != nil {
		return err
	}

	events, err := h.eventRepo.ListForExport(filter, auditExportMaxRows)
	if err != nil {
		return c.AbortInternalServerError("failed to export audit events")
	}

	scopeName := strings.ToLower(strings.TrimSpace(q.Get("scope")))
	if scopeName != "deployment" {
		scopeName = "workspace"
	}

	w := c.ResponseWriter()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=\"audit-logs-%s-%s.csv\"", scopeName, time.Now().UTC().Format("20060102")))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	csvw := csv.NewWriter(w)
	_ = csvw.Write([]string{
		"id", "occurred_at", "workspace_id", "action", "category", "outcome",
		"actor_id", "actor_name", "target_type", "target_id", "ip_address",
		"message", "metadata",
	})

	flusher, canFlush := w.(http.Flusher)
	for i, ev := range events {
		workspaceID := ""
		if ev.WorkspaceID != nil {
			workspaceID = strconv.FormatUint(uint64(*ev.WorkspaceID), 10)
		}
		actorID := ""
		if ev.ActorID != nil {
			actorID = strconv.FormatUint(uint64(*ev.ActorID), 10)
		}
		_ = csvw.Write([]string{
			strconv.FormatUint(uint64(ev.ID), 10),
			ev.CreatedAt.UTC().Format(time.RFC3339Nano),
			workspaceID,
			ev.Type,
			string(ev.Category),
			ev.Outcome,
			actorID,
			ev.ActorName,
			ev.TargetType,
			ev.TargetID,
			ev.ClientIP,
			ev.Message,
			ev.Metadata,
		})
		if (i+1)%auditExportFlushEvery == 0 {
			csvw.Flush()
			if canFlush {
				flusher.Flush()
			}
		}
	}
	// A full page means the export was cut at the cap — say so in-band, the
	// way the audit trail itself reports truncation.
	if len(events) >= auditExportMaxRows {
		_ = csvw.Write([]string{
			"", "", "", "export.truncated", "", "", "", "", "", "", "",
			fmt.Sprintf("export stopped at the %d row cap", auditExportMaxRows),
			fmt.Sprintf("{\"max_rows\":%d}", auditExportMaxRows),
		})
	}
	csvw.Flush()
	if canFlush {
		flusher.Flush()
	}
	return nil
}
