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
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/goposta/posta/internal/worker"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
)

type CampaignHandler struct {
	campaignRepo   *repositories.CampaignRepository
	messageRepo    *repositories.CampaignMessageRepository
	listRepo       *repositories.SubscriberListRepository
	segmentRepo    *repositories.SegmentRepository
	subscriberRepo *repositories.SubscriberRepository
	templateRepo   *repositories.TemplateRepository
	domainRepo     *repositories.DomainRepository
	producer       *worker.Producer
}

func NewCampaignHandler(
	campaignRepo *repositories.CampaignRepository,
	messageRepo *repositories.CampaignMessageRepository,
	listRepo *repositories.SubscriberListRepository,
	segmentRepo *repositories.SegmentRepository,
	subscriberRepo *repositories.SubscriberRepository,
	templateRepo *repositories.TemplateRepository,
	domainRepo *repositories.DomainRepository,
	producer *worker.Producer,
) *CampaignHandler {
	return &CampaignHandler{
		campaignRepo:   campaignRepo,
		messageRepo:    messageRepo,
		listRepo:       listRepo,
		segmentRepo:    segmentRepo,
		subscriberRepo: subscriberRepo,
		templateRepo:   templateRepo,
		domainRepo:     domainRepo,
		producer:       producer,
	}
}

type CreateCampaignRequest struct {
	Body struct {
		Name              string                 `json:"name" required:"true"`
		Subject           string                 `json:"subject" required:"true"`
		FromEmail         string                 `json:"from_email" required:"true"`
		FromName          string                 `json:"from_name"`
		TemplateID        uint                   `json:"template_id" required:"true"`
		TemplateVersionID *uint                  `json:"template_version_id"`
		Language          string                 `json:"language"`
		TemplateData      map[string]interface{} `json:"template_data"`
		// Audience: exactly one of list_id / segment_id (XOR).
		ListID          *uint `json:"list_id"`
		SegmentID       *uint `json:"segment_id"`
		SendRate        int   `json:"send_rate"`
		SendAtLocalTime bool  `json:"send_at_local_time"`
		// LocalSendHour is the R-09 send window: hour 0-23 in each subscriber's
		// timezone. nil = send immediately regardless of local time.
		LocalSendHour *int `json:"local_send_hour"`
		// A/B test (R-08): variants + winner-selection config. Variant split
		// percentages are shares of the FULL audience and may sum to <= 100 —
		// the remainder is held back and receives the winner's content.
		ABTestEnabled     bool                   `json:"ab_test_enabled"`
		ABTestVariants    []models.ABTestVariant `json:"ab_test_variants"`
		ABTestMetric      string                 `json:"ab_test_metric"`
		ABTestWindowHours int                    `json:"ab_test_window_hours"`
		ABTestMinSample   int                    `json:"ab_test_min_sample"`
		ScheduledAt       *time.Time             `json:"scheduled_at"`
	} `json:"body"`
}

type UpdateCampaignRequest struct {
	ID   int `param:"id"`
	Body struct {
		Name              string                 `json:"name"`
		Subject           string                 `json:"subject"`
		FromEmail         string                 `json:"from_email"`
		FromName          string                 `json:"from_name"`
		TemplateID        *uint                  `json:"template_id"`
		TemplateVersionID *uint                  `json:"template_version_id"`
		Language          string                 `json:"language"`
		TemplateData      map[string]interface{} `json:"template_data"`
		// Audience: providing either switches the audience to it; providing
		// both is rejected. Omitting both leaves the audience unchanged.
		ListID          *uint                  `json:"list_id"`
		SegmentID       *uint                  `json:"segment_id"`
		SendRate        *int                   `json:"send_rate"`
		SendAtLocalTime *bool                  `json:"send_at_local_time"`
		LocalSendHour   *int                   `json:"local_send_hour"`
		ABTestEnabled   *bool                  `json:"ab_test_enabled"`
		ABTestVariants  []models.ABTestVariant `json:"ab_test_variants"`
		ABTestMetric    *string                `json:"ab_test_metric"`
		// ABTestWindowHours / ABTestMinSample: nil leaves them unchanged; 0
		// resets to the documented default.
		ABTestWindowHours *int       `json:"ab_test_window_hours"`
		ABTestMinSample   *int       `json:"ab_test_min_sample"`
		ScheduledAt       *time.Time `json:"scheduled_at"`
	} `json:"body"`
}

type ListCampaignsRequest struct {
	Page   int    `query:"page" default:"0"`
	Size   int    `query:"size" default:"20"`
	Status string `query:"status"`
}

type CampaignActionRequest struct {
	ID int `param:"id"`
}

type ListCampaignMessagesRequest struct {
	ID     int    `param:"id"`
	Page   int    `query:"page" default:"0"`
	Size   int    `query:"size" default:"20"`
	Status string `query:"status"`
}

type CampaignStats struct {
	Total   int64 `json:"total"`
	Pending int64 `json:"pending"`
	Queued  int64 `json:"queued"`
	Sent    int64 `json:"sent"`
	Failed  int64 `json:"failed"`
	Skipped int64 `json:"skipped"`
}

type CampaignWithStats struct {
	models.Campaign
	Stats *CampaignStats `json:"stats,omitempty"`
}

func (h *CampaignHandler) Create(c *okapi.Context, req *CreateCampaignRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	scope := getScope(c)

	if req.Body.Name == "" || req.Body.Subject == "" || req.Body.FromEmail == "" {
		return c.AbortBadRequest("name, subject, and from_email are required")
	}

	if req.Body.ABTestEnabled {
		if err := validateABTestConfig(req.Body.ABTestVariants, req.Body.ABTestMetric, req.Body.ABTestWindowHours, req.Body.ABTestMinSample); err != nil {
			return c.AbortBadRequest(err.Error())
		}
	}
	if req.Body.LocalSendHour != nil && (*req.Body.LocalSendHour < 0 || *req.Body.LocalSendHour > 23) {
		return c.AbortBadRequest("local_send_hour must be between 0 and 23")
	}

	lang := req.Body.Language
	if lang == "" {
		lang = "en"
	}

	listID, segmentID, err := h.resolveAudience(c, req.Body.ListID, req.Body.SegmentID)
	if err != nil {
		return err
	}

	campaign := &models.Campaign{
		UserID:            scope.UserID,
		WorkspaceID:       scope.WorkspaceID,
		Name:              req.Body.Name,
		Subject:           req.Body.Subject,
		FromEmail:         req.Body.FromEmail,
		FromName:          req.Body.FromName,
		TemplateID:        req.Body.TemplateID,
		TemplateVersionID: req.Body.TemplateVersionID,
		Language:          lang,
		TemplateData:      req.Body.TemplateData,
		Status:            models.CampaignStatusDraft,
		ListID:            derefCampaignUint(listID),
		SegmentID:         segmentID,
		SendRate:          req.Body.SendRate,
		SendAtLocalTime:   req.Body.SendAtLocalTime,
		LocalSendHour:     req.Body.LocalSendHour,
		ABTestEnabled:     req.Body.ABTestEnabled,
		ABTestVariants:    req.Body.ABTestVariants,
		ABTestMetric:      req.Body.ABTestMetric,
		ABTestWindowHours: req.Body.ABTestWindowHours,
		ABTestMinSample:   req.Body.ABTestMinSample,
		ScheduledAt:       req.Body.ScheduledAt,
	}
	normalizeABTestConfig(campaign)

	if err := h.campaignRepo.Create(campaign); err != nil {
		return c.AbortInternalServerError("failed to create campaign")
	}
	return created(c, campaign)
}

func (h *CampaignHandler) List(c *okapi.Context, req *ListCampaignsRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)
	items, total, err := h.campaignRepo.FindByScope(getScope(c), req.Status, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list campaigns")
	}

	// Single grouped query instead of N per-campaign stats calls.
	ids := make([]uint, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	countsByID, cerr := h.messageRepo.CountByStatusForCampaigns(ids)
	if cerr != nil {
		// Stats are best-effort; still return the list.
		countsByID = nil
	}

	result := make([]CampaignWithStats, len(items))
	for i, item := range items {
		result[i] = CampaignWithStats{Campaign: item, Stats: statsFromCounts(countsByID[item.ID])}
	}
	return paginated(c, result, total, page, size)
}

func (h *CampaignHandler) Get(c *okapi.Context, req *CampaignActionRequest) error {
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	stats := h.buildStats(campaign.ID)
	return ok(c, CampaignWithStats{Campaign: *campaign, Stats: stats})
}

func (h *CampaignHandler) Update(c *okapi.Context, req *UpdateCampaignRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	if campaign.Status != models.CampaignStatusDraft {
		return c.AbortBadRequest("can only update draft campaigns")
	}

	if req.Body.Name != "" {
		campaign.Name = req.Body.Name
	}
	if req.Body.Subject != "" {
		campaign.Subject = req.Body.Subject
	}
	if req.Body.FromEmail != "" {
		campaign.FromEmail = req.Body.FromEmail
	}
	if req.Body.FromName != "" {
		campaign.FromName = req.Body.FromName
	}
	if req.Body.TemplateID != nil {
		campaign.TemplateID = *req.Body.TemplateID
	}
	if req.Body.TemplateVersionID != nil {
		campaign.TemplateVersionID = req.Body.TemplateVersionID
	}
	if req.Body.Language != "" {
		campaign.Language = req.Body.Language
	}
	if req.Body.TemplateData != nil {
		campaign.TemplateData = req.Body.TemplateData
	}
	if req.Body.ListID != nil || req.Body.SegmentID != nil {
		listID, segmentID, err := h.resolveAudience(c, req.Body.ListID, req.Body.SegmentID)
		if err != nil {
			return err
		}
		if segmentID != nil {
			campaign.SegmentID = segmentID
			campaign.ListID = 0
		} else {
			campaign.ListID = *listID
			campaign.SegmentID = nil
		}
	}
	if req.Body.SendRate != nil {
		campaign.SendRate = *req.Body.SendRate
	}
	if req.Body.SendAtLocalTime != nil {
		campaign.SendAtLocalTime = *req.Body.SendAtLocalTime
	}
	if req.Body.LocalSendHour != nil {
		if *req.Body.LocalSendHour < 0 || *req.Body.LocalSendHour > 23 {
			return c.AbortBadRequest("local_send_hour must be between 0 and 23")
		}
		campaign.LocalSendHour = req.Body.LocalSendHour
	}
	if req.Body.ABTestEnabled != nil {
		campaign.ABTestEnabled = *req.Body.ABTestEnabled
	}
	if req.Body.ABTestVariants != nil || req.Body.ABTestMetric != nil ||
		req.Body.ABTestWindowHours != nil || req.Body.ABTestMinSample != nil {
		if req.Body.ABTestVariants != nil {
			campaign.ABTestVariants = req.Body.ABTestVariants
		}
		if req.Body.ABTestMetric != nil {
			campaign.ABTestMetric = *req.Body.ABTestMetric
		}
		if req.Body.ABTestWindowHours != nil {
			campaign.ABTestWindowHours = *req.Body.ABTestWindowHours
		}
		if req.Body.ABTestMinSample != nil {
			campaign.ABTestMinSample = *req.Body.ABTestMinSample
		}
		if err := validateABTestConfig(campaign.ABTestVariants, campaign.ABTestMetric, campaign.ABTestWindowHours, campaign.ABTestMinSample); err != nil {
			return c.AbortBadRequest(err.Error())
		}
	}
	if req.Body.ScheduledAt != nil {
		campaign.ScheduledAt = req.Body.ScheduledAt
	}
	normalizeABTestConfig(campaign)
	now := time.Now()
	campaign.UpdatedAt = &now

	if err := h.campaignRepo.Update(campaign); err != nil {
		return c.AbortInternalServerError("failed to update campaign")
	}
	return ok(c, campaign)
}

func (h *CampaignHandler) Delete(c *okapi.Context, req *CampaignActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	// Block deletion mid-flight so the worker doesn't race against row removal.
	// Callers should Cancel first, then Delete.
	if campaign.Status == models.CampaignStatusSending {
		return c.AbortBadRequest("cancel the campaign before deleting it")
	}
	// Snapshot stats so post-deletion analytics survive.
	snapshot := h.snapshotFor(campaign)
	if err := h.campaignRepo.Delete(campaign.ID, snapshot); err != nil {
		return c.AbortInternalServerError("failed to delete campaign")
	}
	return noContent(c)
}

func (h *CampaignHandler) Send(c *okapi.Context, req *CampaignActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	if campaign.Status != models.CampaignStatusDraft {
		return c.AbortBadRequest("can only send draft campaigns")
	}

	// Deliverability check: surface unverified sender domains as a warning in
	// logs rather than blocking, since the SMTP-server/shared-server layer has
	// its own policy and legitimate setups exist where ownership isn't yet
	// proven (DNS propagation, shared-server tenants, etc.).
	h.warnSenderDeliverable(campaign)

	if campaign.ScheduledAt != nil && campaign.ScheduledAt.After(time.Now()) {
		if err := h.campaignRepo.TransitionStatus(
			campaign.ID,
			[]models.CampaignStatus{models.CampaignStatusDraft},
			models.CampaignStatusScheduled,
		); err != nil {
			return translateTransitionErr(c, err, "failed to schedule campaign")
		}
		campaign.Status = models.CampaignStatusScheduled
		return ok(c, campaign)
	}

	if h.producer == nil {
		return c.AbortInternalServerError("background worker is not configured; cannot send campaigns")
	}

	// Atomically claim the draft -> sending transition. Losing the race returns 409
	// so a double-click or concurrent Send doesn't double-enqueue.
	if err := h.campaignRepo.TransitionStatus(
		campaign.ID,
		[]models.CampaignStatus{models.CampaignStatusDraft},
		models.CampaignStatusSending,
	); err != nil {
		return translateTransitionErr(c, err, "failed to update campaign status")
	}

	// Idempotent enqueue: TaskID dedupes retries of the same campaign start.
	if err := h.producer.EnqueueCampaignStart(campaign.ID); err != nil {
		// Roll back so the campaign is retryable, not stuck.
		_ = h.campaignRepo.TransitionStatus(
			campaign.ID,
			[]models.CampaignStatus{models.CampaignStatusSending},
			models.CampaignStatusDraft,
		)
		return c.AbortInternalServerError("failed to enqueue campaign")
	}
	campaign.Status = models.CampaignStatusSending
	return ok(c, campaign)
}

func (h *CampaignHandler) Pause(c *okapi.Context, req *CampaignActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	if err := h.campaignRepo.TransitionStatus(
		campaign.ID,
		[]models.CampaignStatus{models.CampaignStatusSending},
		models.CampaignStatusPaused,
	); err != nil {
		return translateTransitionErr(c, err, "failed to pause campaign")
	}
	campaign.Status = models.CampaignStatusPaused
	return ok(c, campaign)
}

func (h *CampaignHandler) Resume(c *okapi.Context, req *CampaignActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	if h.producer == nil {
		return c.AbortInternalServerError("background worker is not configured; cannot resume campaigns")
	}
	if err := h.campaignRepo.TransitionStatus(
		campaign.ID,
		[]models.CampaignStatus{models.CampaignStatusPaused},
		models.CampaignStatusSending,
	); err != nil {
		return translateTransitionErr(c, err, "failed to resume campaign")
	}
	if err := h.producer.EnqueueCampaignBatch(campaign.ID, 0); err != nil {
		_ = h.campaignRepo.TransitionStatus(
			campaign.ID,
			[]models.CampaignStatus{models.CampaignStatusSending},
			models.CampaignStatusPaused,
		)
		return c.AbortInternalServerError("failed to enqueue campaign batch")
	}
	campaign.Status = models.CampaignStatusSending
	return ok(c, campaign)
}

func (h *CampaignHandler) Cancel(c *okapi.Context, req *CampaignActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	if err := h.campaignRepo.TransitionStatus(
		campaign.ID,
		[]models.CampaignStatus{
			models.CampaignStatusSending,
			models.CampaignStatusPaused,
			models.CampaignStatusScheduled,
		},
		models.CampaignStatusCancelled,
	); err != nil {
		return translateTransitionErr(c, err, "failed to cancel campaign")
	}
	// Drain: mark still-pending messages as skipped. Already-queued emails are
	// filtered at dispatch time by EmailSendHandler checking the campaign status.
	_, _ = h.messageRepo.SkipPendingForCampaign(campaign.ID, "campaign cancelled")
	campaign.Status = models.CampaignStatusCancelled
	return ok(c, campaign)
}

func (h *CampaignHandler) Duplicate(c *okapi.Context, req *CampaignActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	scope := getScope(c)

	clone := &models.Campaign{
		UserID:            scope.UserID,
		WorkspaceID:       scope.WorkspaceID,
		Name:              campaign.Name + " (copy)",
		Subject:           campaign.Subject,
		FromEmail:         campaign.FromEmail,
		FromName:          campaign.FromName,
		TemplateID:        campaign.TemplateID,
		TemplateVersionID: campaign.TemplateVersionID,
		Language:          campaign.Language,
		TemplateData:      cloneTemplateData(campaign.TemplateData),
		Status:            models.CampaignStatusDraft,
		ListID:            campaign.ListID,
		SegmentID:         campaign.SegmentID,
		SendRate:          campaign.SendRate,
		SendAtLocalTime:   campaign.SendAtLocalTime,
		LocalSendHour:     campaign.LocalSendHour,
		ABTestEnabled:     campaign.ABTestEnabled,
		ABTestVariants:    append(models.ABTestVariants(nil), campaign.ABTestVariants...),
		ABTestMetric:      campaign.ABTestMetric,
		ABTestWindowHours: campaign.ABTestWindowHours,
		ABTestMinSample:   campaign.ABTestMinSample,
		// Winner state is intentionally not copied: the duplicate runs a fresh
		// test.
	}

	if err := h.campaignRepo.Create(clone); err != nil {
		return c.AbortInternalServerError("failed to duplicate campaign")
	}
	return created(c, clone)
}

func (h *CampaignHandler) ListMessages(c *okapi.Context, req *ListCampaignMessagesRequest) error {
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	page, size, offset := normalizePageParams(req.Page, req.Size)
	items, total, err := h.messageRepo.FindByCampaign(campaign.ID, req.Status, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list campaign messages")
	}
	return paginated(c, items, total, page, size)
}

// findScoped centralizes the "find + workspace ownership check" pattern so it
// can't be forgotten at a callsite.
func (h *CampaignHandler) findScoped(c *okapi.Context, id uint) (*models.Campaign, error) {
	return h.campaignRepo.FindByIDForScope(id, getScope(c))
}

// ---------- R-08: A/B winner config, validation and stats ----------

// validateABTestConfig checks the A/B winner-selection config (R-08). Variant
// split percentages are shares of the FULL audience and may sum to at most
// 100 — the remainder is held back and receives the winner's content.
func validateABTestConfig(variants []models.ABTestVariant, metric string, windowHours, minSample int) error {
	if len(variants) > 0 {
		total := 0
		for _, v := range variants {
			if v.Name == "" || v.SplitPercentage <= 0 {
				return errors.New("each variant must have a name and positive split percentage")
			}
			total += v.SplitPercentage
		}
		if total > 100 {
			return errors.New("variant split percentages must sum to at most 100 — the remainder receives the winner")
		}
	}
	if metric != "" && metric != models.ABTestMetricOpenRate && metric != models.ABTestMetricClickRate {
		return errors.New("ab_test_metric must be open_rate or click_rate")
	}
	if windowHours < 0 || windowHours > 24*365 {
		return errors.New("ab_test_window_hours must be between 0 and 8760")
	}
	if minSample < 0 || minSample > 1_000_000 {
		return errors.New("ab_test_min_sample must be between 0 and 1000000")
	}
	return nil
}

// normalizeABTestConfig applies the documented defaults (R-08) so a stored A/B
// campaign never carries zero-valued config: metric open_rate, window 24h,
// minimum sample 50 per variant.
func normalizeABTestConfig(campaign *models.Campaign) {
	if !campaign.ABTestEnabled {
		return
	}
	switch campaign.ABTestMetric {
	case models.ABTestMetricOpenRate, models.ABTestMetricClickRate:
	default:
		campaign.ABTestMetric = models.ABTestMetricOpenRate
	}
	if campaign.ABTestWindowHours <= 0 {
		campaign.ABTestWindowHours = 24
	}
	if campaign.ABTestMinSample <= 0 {
		campaign.ABTestMinSample = 50
	}
}

// ABVariantStatView is one variant row of the campaign detail's A/B table.
type ABVariantStatView struct {
	Variant   string  `json:"variant"`
	Sent      int64   `json:"sent"`
	Opened    int64   `json:"opened"`
	Clicked   int64   `json:"clicked"`
	OpenRate  float64 `json:"open_rate"`
	ClickRate float64 `json:"click_rate"`
	Winner    bool    `json:"winner"`
}

// CampaignABTestStats is the A/B winner state for the campaign detail page:
// the config, the winner decision (with the stats it was made on, when
// available) and the live per-variant engagement.
type CampaignABTestStats struct {
	Enabled      bool                `json:"enabled"`
	Metric       string              `json:"metric"`
	WindowHours  int                 `json:"window_hours"`
	MinSample    int                 `json:"min_sample"`
	Winner       string              `json:"winner,omitempty"`
	WinnerSent   bool                `json:"winner_sent"`
	WindowEndsAt *time.Time          `json:"window_ends_at,omitempty"`
	Decision     models.TemplateData `json:"decision,omitempty"`
	Variants     []ABVariantStatView `json:"variants"`
}

// ABTestStats serves GET /campaigns/{id}/ab-test (R-08).
func (h *CampaignHandler) ABTestStats(c *okapi.Context, req *CampaignActionRequest) error {
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}

	stats := CampaignABTestStats{
		Enabled:     campaign.ABTestEnabled,
		Metric:      campaign.ABTestMetric,
		WindowHours: campaign.ABTestWindowHours,
		MinSample:   campaign.ABTestMinSample,
		Winner:      campaign.ABTestWinner,
		WinnerSent:  campaign.ABTestWinnerSent,
		Decision:    campaign.ABTestWinnerStats,
	}
	if stats.Metric == "" {
		stats.Metric = models.ABTestMetricOpenRate
	}
	if stats.WindowHours <= 0 {
		stats.WindowHours = 24
	}
	if stats.MinSample <= 0 {
		stats.MinSample = 50
	}

	if campaign.ABTestEnabled && len(campaign.ABTestVariants) > 0 {
		started := campaign.StartedAt
		if started == nil {
			started = &campaign.CreatedAt
		}
		windowEnd := started.Add(time.Duration(stats.WindowHours) * time.Hour)
		stats.WindowEndsAt = &windowEnd

		variantStats, err := h.messageRepo.ABVariantStats(campaign.ID)
		if err != nil {
			return c.AbortInternalServerError("failed to load A/B variant stats")
		}
		byName := make(map[string]models.ABVariantStat, len(variantStats))
		for _, s := range variantStats {
			byName[s.Variant] = s
		}
		stats.Variants = make([]ABVariantStatView, 0, len(campaign.ABTestVariants))
		for _, v := range campaign.ABTestVariants {
			s := byName[v.Name]
			view := ABVariantStatView{Variant: v.Name, Sent: s.Sent, Opened: s.Opened, Clicked: s.Clicked}
			if s.Sent > 0 {
				view.OpenRate = float64(s.Opened) / float64(s.Sent) * 100
				view.ClickRate = float64(s.Clicked) / float64(s.Sent) * 100
			}
			view.Winner = campaign.ABTestWinnerSent && campaign.ABTestWinner == v.Name
			stats.Variants = append(stats.Variants, view)
		}
	}
	return ok(c, stats)
}

// ---------- R-09: timezone distribution ----------

// CampaignTimezoneCount is one timezone row of the distribution list.
type CampaignTimezoneCount struct {
	// Timezone is the raw stored value: an IANA name, empty (server-local
	// fallback) or anything else the subscriber record carried.
	Timezone string `json:"timezone"`
	Count    int64  `json:"count"`
}

// CampaignTimezones is the campaign detail's timezone-aware sending view (R-09).
type CampaignTimezones struct {
	SendAtLocalTime bool                    `json:"send_at_local_time"`
	LocalSendHour   *int                    `json:"local_send_hour,omitempty"`
	Timezones       []CampaignTimezoneCount `json:"timezones"`
}

// Timezones serves GET /campaigns/{id}/timezones (R-09): a simple count list
// of subscriber timezones plus the campaign's timezone-aware configuration.
func (h *CampaignHandler) Timezones(c *okapi.Context, req *CampaignActionRequest) error {
	campaign, err := h.findScoped(c, uint(req.ID))
	if err != nil {
		return c.AbortNotFound("campaign not found")
	}
	counts, err := h.messageRepo.TimezoneCounts(campaign.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to load timezone distribution")
	}
	out := CampaignTimezones{
		SendAtLocalTime: campaign.SendAtLocalTime,
		LocalSendHour:   campaign.LocalSendHour,
		Timezones:       make([]CampaignTimezoneCount, 0, len(counts)),
	}
	for tz, count := range counts {
		out.Timezones = append(out.Timezones, CampaignTimezoneCount{Timezone: tz, Count: count})
	}
	sort.Slice(out.Timezones, func(i, j int) bool {
		if out.Timezones[i].Count != out.Timezones[j].Count {
			return out.Timezones[i].Count > out.Timezones[j].Count
		}
		return out.Timezones[i].Timezone < out.Timezones[j].Timezone
	})
	return ok(c, out)
}

// errCampaignAudienceRejected is the sentinel returned after the audience
// validation has aborted the request — callers must treat any error as
// "response already written" (same contract as the other abort sentinels,
// because okapi's Abort helpers return nil once the response is committed).
var errCampaignAudienceRejected = errors.New("campaign audience rejected (response already written)")

// resolveAudience enforces the campaign audience rule: exactly one of
// list_id / segment_id, and the referenced resource must belong to the
// caller's scope. The list check closes a gap — list ownership was never
// validated before; a foreign list_id now 404s like any other cross-tenant
// lookup. On any rejection the response is already written and the sentinel
// is returned so Create/Update cannot continue past it.
func (h *CampaignHandler) resolveAudience(c *okapi.Context, listID, segmentID *uint) (*uint, *uint, error) {
	switch {
	case listID != nil && segmentID != nil:
		_ = c.AbortBadRequest("set either list_id or segment_id, not both")
		return nil, nil, errCampaignAudienceRejected
	case listID == nil && segmentID == nil:
		_ = c.AbortBadRequest("an audience is required: set list_id or segment_id")
		return nil, nil, errCampaignAudienceRejected
	case segmentID != nil:
		segment, err := h.segmentRepo.FindByID(*segmentID)
		if err != nil || !ownsResource(c, segment.UserID, segment.WorkspaceID) {
			_ = c.AbortNotFound("segment not found")
			return nil, nil, errCampaignAudienceRejected
		}
		return nil, segmentID, nil
	default:
		list, err := h.listRepo.FindByID(*listID)
		if err != nil || !ownsResource(c, list.UserID, list.WorkspaceID) {
			_ = c.AbortNotFound("subscriber list not found")
			return nil, nil, errCampaignAudienceRejected
		}
		return listID, nil, nil
	}
}

// derefCampaignUint maps an optional list id to the column's zero-value.
func derefCampaignUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

// warnSenderDeliverable logs (but does not block) when the sender domain is
// registered for this user/workspace but lacks ownership/SPF/DKIM verification.
// Blocking belongs to the SMTP-server layer, which already enforces per-server
// policy — here we just give the operator a hint that their bounce rate is
// about to spike.
func (h *CampaignHandler) warnSenderDeliverable(campaign *models.Campaign) {
	if h.domainRepo == nil {
		return
	}
	at := strings.LastIndexByte(campaign.FromEmail, '@')
	if at < 0 || at == len(campaign.FromEmail)-1 {
		return
	}
	domain := strings.ToLower(campaign.FromEmail[at+1:])
	d, err := h.domainRepo.FindByUserIDAndDomain(campaign.UserID, domain)
	if err != nil || d == nil {
		return
	}
	if !d.OwnershipVerified || !d.SPFVerified || !d.DKIMVerified {
		logger.Warn("campaign: sending from domain with incomplete DNS verification",
			"campaign_id", campaign.ID,
			"domain", domain,
			"ownership_verified", d.OwnershipVerified,
			"spf_verified", d.SPFVerified,
			"dkim_verified", d.DKIMVerified,
		)
	}
}

// snapshotFor captures the pre-deletion stats of a campaign so reports survive.
func (h *CampaignHandler) snapshotFor(campaign *models.Campaign) models.TemplateData {
	stats := h.buildStats(campaign.ID)
	if stats == nil {
		return nil
	}
	snapshot := models.TemplateData{
		"total":       stats.Total,
		"pending":     stats.Pending,
		"queued":      stats.Queued,
		"sent":        stats.Sent,
		"failed":      stats.Failed,
		"skipped":     stats.Skipped,
		"snapshot_at": time.Now().UTC().Format(time.RFC3339),
		"from_email":  campaign.FromEmail,
		"list_id":     campaign.ListID,
		"template_id": campaign.TemplateID,
	}
	if campaign.SegmentID != nil {
		snapshot["segment_id"] = *campaign.SegmentID
	}
	return snapshot
}

// cloneTemplateData is a shallow copy at the top level; campaign template data
// is JSON so shared references below the top level would only matter if a
// caller mutated them, which is not a pattern we use.
func cloneTemplateData(src models.TemplateData) models.TemplateData {
	if src == nil {
		return nil
	}
	dst := make(models.TemplateData, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// translateTransitionErr maps an atomic transition failure to either 409 (on
// conflict) or 500 (on any other DB error).
func translateTransitionErr(c *okapi.Context, err error, genericMsg string) error {
	if errors.Is(err, repositories.ErrStatusConflict) {
		return c.AbortConflict("campaign is not in a state that allows this action")
	}
	return c.AbortInternalServerError(genericMsg)
}

// statsFromCounts projects a status->count map into the CampaignStats shape.
func statsFromCounts(counts map[models.CampaignMessageStatus]int64) *CampaignStats {
	if counts == nil {
		return nil
	}
	stats := &CampaignStats{}
	for status, count := range counts {
		stats.Total += count
		switch status {
		case models.CampaignMsgPending:
			stats.Pending = count
		case models.CampaignMsgQueued:
			stats.Queued = count
		case models.CampaignMsgSent:
			stats.Sent = count
		case models.CampaignMsgFailed:
			stats.Failed = count
		case models.CampaignMsgSkipped:
			stats.Skipped = count
		}
	}
	return stats
}

// buildStats computes stats for a campaign from the message counts.
func (h *CampaignHandler) buildStats(campaignID uint) *CampaignStats {
	counts, err := h.messageRepo.CountByStatus(campaignID)
	if err != nil {
		return nil
	}
	stats := &CampaignStats{}
	for status, count := range counts {
		stats.Total += count
		switch status {
		case models.CampaignMsgPending:
			stats.Pending = count
		case models.CampaignMsgQueued:
			stats.Queued = count
		case models.CampaignMsgSent:
			stats.Sent = count
		case models.CampaignMsgFailed:
			stats.Failed = count
		case models.CampaignMsgSkipped:
			stats.Skipped = count
		}
	}
	return stats
}
