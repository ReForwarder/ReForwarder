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

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/lib/pq"
)

// CampaignProcessor handles campaign:start and campaign:batch tasks.
type CampaignProcessor struct {
	campaignRepo     *repositories.CampaignRepository
	messageRepo      *repositories.CampaignMessageRepository
	listRepo         *repositories.SubscriberListRepository
	segmentRepo      *repositories.SegmentRepository
	subscriberRepo   *repositories.SubscriberRepository
	emailRepo        *repositories.EmailRepository
	templateRepo     *repositories.TemplateRepository
	versionRepo      *repositories.TemplateVersionRepository
	localizationRepo *repositories.TemplateLocalizationRepository
	trackingService  *tracking.Service
	settingsProvider *settings.Provider
	producer         *Producer
	dispatcher       *webhook.Dispatcher
}

func NewCampaignProcessor(
	campaignRepo *repositories.CampaignRepository,
	messageRepo *repositories.CampaignMessageRepository,
	listRepo *repositories.SubscriberListRepository,
	segmentRepo *repositories.SegmentRepository,
	subscriberRepo *repositories.SubscriberRepository,
	emailRepo *repositories.EmailRepository,
	templateRepo *repositories.TemplateRepository,
	versionRepo *repositories.TemplateVersionRepository,
	localizationRepo *repositories.TemplateLocalizationRepository,
	trackingService *tracking.Service,
	settingsProvider *settings.Provider,
	producer *Producer,
	dispatcher *webhook.Dispatcher,
) *CampaignProcessor {
	return &CampaignProcessor{
		campaignRepo:     campaignRepo,
		messageRepo:      messageRepo,
		listRepo:         listRepo,
		segmentRepo:      segmentRepo,
		subscriberRepo:   subscriberRepo,
		emailRepo:        emailRepo,
		templateRepo:     templateRepo,
		versionRepo:      versionRepo,
		localizationRepo: localizationRepo,
		trackingService:  trackingService,
		settingsProvider: settingsProvider,
		producer:         producer,
		dispatcher:       dispatcher,
	}
}

const campaignBatchSize = 100

// A/B winner-selection defaults and pacing (R-08).
const (
	defaultABTestMetric      = models.ABTestMetricOpenRate
	defaultABTestWindowHours = 24
	defaultABTestMinSample   = 50
	// abWinnerRecheckInterval: once the test window has closed but a variant
	// is still under the minimum sample, the winner check retries at this pace.
	abWinnerRecheckInterval = 15 * time.Minute
	// abWinnerMaxGrace bounds those retries: one hour after the window closes
	// the winner is picked from the best available data, so the held remainder
	// can never be stranded by an unreachable sample floor.
	abWinnerMaxGrace = time.Hour
)

// HandleCampaignStart processes a campaign:start task.
func (p *CampaignProcessor) HandleCampaignStart(_ context.Context, t *asynq.Task) error {
	var payload CampaignPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal campaign start payload: %w", err)
	}

	campaign, err := p.campaignRepo.FindByID(payload.CampaignID)
	if err != nil {
		return fmt.Errorf("campaign not found: %w", err)
	}

	if campaign.Status != models.CampaignStatusSending {
		logger.Info("campaign is not in sending status, skipping", "id", campaign.ID, "status", campaign.Status)
		return nil
	}

	// Resolve subscribers from the audience: a segment (saved condition) or a
	// subscriber list. Exactly one is set — the campaign handler enforces the
	// XOR at write time.
	var subscribers []models.Subscriber
	scope := repositories.ResourceScope{UserID: campaign.UserID, WorkspaceID: campaign.WorkspaceID}

	if campaign.SegmentID != nil {
		// Segment audience: resolve via the saved condition. List-scoped
		// unsubscribes are keyed by (list_id, subscriber_id), so they cannot
		// apply to a segment audience, which has no list; global suppressions
		// still apply later in the email send path (the email service filters
		// recipients against the workspace suppression list).
		segment, segErr := p.segmentRepo.FindByID(*campaign.SegmentID)
		if segErr != nil {
			return fmt.Errorf("segment not found: %w", segErr)
		}
		if !repositories.OwnsWorkspaceResource(scope, segment.WorkspaceID) {
			return fmt.Errorf("segment %d does not belong to the campaign's workspace", segment.ID)
		}
		subscribers, _, err = p.segmentRepo.FindSubscribersByCondition(scope, segment.Condition, -1, 0)
		if err != nil {
			return fmt.Errorf("failed to resolve segment audience: %w", err)
		}
	} else {
		list, listErr := p.listRepo.FindByID(campaign.ListID)
		if listErr != nil {
			return fmt.Errorf("subscriber list not found: %w", listErr)
		}

		if list.Type == models.SubscriberListTypeDynamic {
			// Dynamic list: resolve via filter rules
			subscribers, _, err = p.subscriberRepo.FindByFilterRules(scope, list.FilterRules, -1, 0)
			if err != nil {
				return fmt.Errorf("failed to resolve dynamic list: %w", err)
			}
		} else {
			// Static list: get all members
			subscribers, _, err = p.listRepo.ListMembers(list.ID, -1, 0)
			if err != nil {
				return fmt.Errorf("failed to list members: %w", err)
			}
		}
	}

	if len(subscribers) == 0 {
		logger.Info("campaign has no subscribers, marking as sent", "id", campaign.ID)
		_ = p.campaignRepo.UpdateStatus(campaign.ID, models.CampaignStatusSent)
		return nil
	}

	// List-scoped suppressions exist only for list audiences (see the segment
	// branch above for why they cannot apply to a segment).
	var suppressed map[uint]struct{}
	if campaign.SegmentID == nil {
		suppressedList, sErr := p.listRepo.SuppressedSubscriberIDs(campaign.ListID)
		if sErr != nil {
			logger.Warn("campaign: failed to load list suppressions, proceeding without", "list_id", campaign.ListID, "error", sErr)
			suppressedList = nil
		}
		suppressed = suppressedList
	}

	// Create campaign messages in bulk
	messages := make([]models.CampaignMessage, 0, len(subscribers))
	for _, sub := range subscribers {
		if sub.Status != models.SubscriberStatusSubscribed {
			continue
		}
		if _, opt := suppressed[sub.ID]; opt {
			continue
		}
		messages = append(messages, models.CampaignMessage{
			CampaignID:   campaign.ID,
			SubscriberID: sub.ID,
			Status:       models.CampaignMsgPending,
		})
	}

	if len(messages) == 0 {
		logger.Info("no eligible subscribers, marking campaign as sent", "id", campaign.ID)
		_ = p.campaignRepo.UpdateStatus(campaign.ID, models.CampaignStatusSent)
		return nil
	}

	// Assign A/B test variants if enabled. The hold-back model (R-08): each
	// variant's split percentage is its share of the FULL audience; the
	// recipients beyond the variant slices stay variant-less (held) and receive
	// the winning variant's content once the test window closes and the winner
	// is selected. A 100% combined split leaves nothing held — the winner
	// selection then runs as analytics-only.
	if campaign.ABTestEnabled && len(campaign.ABTestVariants) > 0 {
		rand.Shuffle(len(messages), func(i, j int) {
			messages[i], messages[j] = messages[j], messages[i]
		})
		assignABTestGroups(messages, campaign.ABTestVariants)

		held := 0
		for i := range messages {
			if messages[i].Variant == "" {
				held++
			}
		}
		logger.Info("campaign A/B test groups assigned", "campaign_id", campaign.ID,
			"test_group", len(messages)-held, "held_back", held)
	}

	if _, err := p.messageRepo.BulkCreate(messages); err != nil {
		return fmt.Errorf("failed to create campaign messages: %w", err)
	}

	logger.Info("campaign messages created, starting batch processing",
		"campaign_id", campaign.ID, "messages", len(messages))

	// Enqueue the first batch. A nil producer means the worker was built
	// without one (misconfigured deployment): fail the task so asynq retries
	// instead of panicking on the nil client.
	if p.producer == nil {
		return fmt.Errorf("campaign %d: producer not configured; cannot enqueue batch", campaign.ID)
	}

	// Timezone-aware sending (R-09): group the eligible recipients by their
	// subscriber timezone. Groups whose local hour is inside the configured
	// send window go out now; the rest are deferred by enqueueing the first
	// batch at the earliest window opening — every later batch tick re-checks
	// per-message eligibility (see HandleCampaignBatch), so pause/cancel and
	// DST transitions are always honored at execution time.
	firstDelay := time.Duration(0)
	if campaign.SendAtLocalTime {
		plans := planTimezoneGroups(messageTimezones(messages, subscribers), campaign.LocalSendHour, time.Now())
		firstDelay = earliestOpenDelay(plans)
		if firstDelay > 0 {
			logger.Info("campaign deferred to the first timezone send window",
				"campaign_id", campaign.ID, "first_open_in", firstDelay.String())
		}
	}
	if err := p.producer.EnqueueCampaignBatch(campaign.ID, firstDelay); err != nil {
		return err
	}
	if p.dispatcher != nil {
		p.dispatcher.DispatchCampaign(campaign.UserID, campaign.WorkspaceID, "campaign.started", campaign.ID, campaign.Name, campaign.FromEmail)
	}
	return nil
}

// HandleCampaignBatch processes a campaign:batch task.
// It loads a batch of pending messages, creates Email records, and enqueues them.
//
// Two deferred-delivery gates sit in front of the send loop:
//   - R-08 A/B hold-back: while the test window is open, variant-less (held)
//     messages are skipped; once the winner is selected they carry the winner
//     variant and flow through normally.
//   - R-09 timezone window: with send_at_local_time + a configured local send
//     hour, only messages whose subscriber's local wall clock is inside the
//     window are sent this tick; the rest wait for the next opening.
func (p *CampaignProcessor) HandleCampaignBatch(_ context.Context, t *asynq.Task) error {
	var payload CampaignPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal campaign batch payload: %w", err)
	}

	campaign, err := p.campaignRepo.FindByID(payload.CampaignID)
	if err != nil {
		return fmt.Errorf("campaign not found: %w", err)
	}

	if campaign.Status != models.CampaignStatusSending {
		logger.Info("campaign is not in sending status, skipping batch", "id", campaign.ID, "status", campaign.Status)
		return nil
	}

	// R-08 winner check. Runs before the pending fetch so messages that are
	// assigned the winner variant in this tick are immediately sendable.
	// recheckIn > 0 means the decision is still pending (window open or
	// insufficient sample) and the next check is due then.
	recheckIn, err := p.maybeSelectABWinner(campaign)
	if err != nil {
		return err
	}

	// Get pending messages with subscribers preloaded
	pendingMessages, err := p.messageRepo.FindPendingByCampaign(campaign.ID, campaignBatchSize)
	if err != nil {
		return fmt.Errorf("failed to get pending messages: %w", err)
	}

	// Nothing left at all: completion is deferred while the A/B decision is
	// still outstanding (analytics-only campaigns stay in `sending` until the
	// window closes and the winner is recorded).
	if len(pendingMessages) == 0 {
		if recheckIn > 0 {
			return p.enqueueDeferredBatch(campaign.ID, recheckIn)
		}
		return p.completeCampaign(campaign)
	}

	// Filter to the messages that may leave this tick.
	sendable := make([]models.CampaignMessage, 0, len(pendingMessages))
	for _, msg := range pendingMessages {
		if isHeldForWinner(campaign, msg.Variant) {
			continue
		}
		if !localSendEligibleNow(campaign, msg.Subscriber.Timezone, time.Now()) {
			continue
		}
		sendable = append(sendable, msg)
	}

	// Everything in the fetched sample is deferred (held for the winner or
	// outside its local send window): schedule the next tick for whichever
	// gate opens first instead of spinning through empty batches.
	if len(sendable) == 0 {
		delay := p.deferredBatchDelay(campaign, pendingMessages, recheckIn)
		return p.enqueueDeferredBatch(campaign.ID, delay)
	}

	// Resolve template name once so it can be stamped on each email record.
	var templateName string
	if tmpl, err := p.templateRepo.FindByID(campaign.TemplateID); err == nil && tmpl != nil {
		templateName = tmpl.Name
	}

	// Cache resolved content per language to avoid re-rendering for each subscriber
	contentCache := make(map[string]*resolvedContent)
	resolveForLang := func(lang string) *resolvedContent {
		if lang == "" {
			lang = campaign.Language
		}
		if lang == "" {
			lang = "en"
		}
		if cached, ok := contentCache[lang]; ok {
			return cached
		}
		c := p.resolveTemplateContent(campaign, lang)
		if c != nil {
			contentCache[lang] = c
		}
		return c
	}

	// Pre-resolve default content for the campaign language
	defaultContent := resolveForLang(campaign.Language)
	if defaultContent == nil {
		return fmt.Errorf("failed to resolve template content for campaign %d", campaign.ID)
	}

	// Click tracking is a global platform setting read once per batch; the
	// provider caches it, so a change takes effect on the next batch without
	// a restart. Open tracking and unsubscribe links are unaffected by it.
	clickTrackingEnabled := true
	if p.settingsProvider != nil {
		clickTrackingEnabled = p.settingsProvider.EmailTrackingEnabled()
	}

	for i := range sendable {
		msg := &sendable[i]

		// Resolve content for subscriber's language (falls back to campaign language)
		content := defaultContent
		if msg.Subscriber.Language != "" {
			if langContent := resolveForLang(msg.Subscriber.Language); langContent != nil {
				content = langContent
			}
		}

		// A/B variant subject override (also covers the winner send: held
		// messages carry the winning variant once the winner is selected)
		if campaign.ABTestEnabled && msg.Variant != "" {
			for _, v := range campaign.ABTestVariants {
				if v.Name == msg.Variant && v.Subject != "" {
					// Copy to avoid mutating cached content
					override := *content
					override.Subject = v.Subject
					content = &override
					break
				}
			}
		}

		// Format sender with display name
		sender := campaign.FromEmail
		if campaign.FromName != "" {
			sender = fmt.Sprintf("%s <%s>", campaign.FromName, campaign.FromEmail)
		}

		// Claim the message atomically (R-26): pending→queued in one UPDATE
		// before any email row exists, so a re-delivered batch task or the
		// stuck-campaign restart sweep can never double-claim a message another
		// batch is already sending (duplicate email rows). Deferred messages
		// (A/B hold-back, timezone window) are never claimed and stay pending.
		claimed, err := p.messageRepo.ClaimForSend(msg.ID)
		if err != nil {
			return fmt.Errorf("failed to claim campaign message %d: %w", msg.ID, err)
		}
		if !claimed {
			continue // another batch claimed it
		}

		// Create email record. Assign the UUID up front so the per-recipient web
		// view link can be built before persisting.
		em := &models.Email{
			UUID:         uuid.NewString(),
			UserID:       campaign.UserID,
			WorkspaceID:  campaign.WorkspaceID,
			Sender:       sender,
			Recipients:   pq.StringArray{msg.Subscriber.Email},
			Subject:      content.Subject,
			TemplateName: templateName,
			HTMLBody:     content.HTMLBody,
			TextBody:     content.TextBody,
			Status:       models.EmailStatusQueued,
			Provider:     email.ClassifyProvider(msg.Subscriber.Email),
		}

		if err := p.emailRepo.Create(em); err != nil {
			_ = p.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgFailed, "failed to create email record: "+err.Error())
			continue
		}

		if p.trackingService != nil {
			// Resolve reserved {{ posta_* }} system links for this recipient. The
			// generated /t/ URLs are skipped by ProcessHTML's click rewriter, so
			// substitute before it runs.
			webViewURL := p.trackingService.WebViewURL(em.UUID)
			unsubscribeURL := p.trackingService.UnsubscribeURL(msg.ID)
			em.HTMLBody = email.SubstituteSystemLinks(em.HTMLBody, webViewURL, unsubscribeURL)
			em.TextBody = email.SubstituteSystemLinks(em.TextBody, webViewURL, unsubscribeURL)
			em.Subject = email.SubstituteSystemLinks(em.Subject, webViewURL, unsubscribeURL)

			// Rewrite links for click tracking, then inject the open pixel. The
			// two are independent: with click tracking disabled the original
			// URLs are preserved and only the open pixel is added.
			if em.HTMLBody != "" {
				if clickTrackingEnabled {
					em.HTMLBody = p.trackingService.ProcessHTML(em.HTMLBody, campaign.ID, msg.ID)
				} else {
					em.HTMLBody = p.trackingService.InjectOpenPixel(em.HTMLBody, msg.ID)
				}
			}
			em.ListUnsubscribeURL = unsubscribeURL
			em.ListUnsubscribePost = true
			_ = p.emailRepo.Update(em)
		}

		// Link email to campaign message. The message is already queued — the
		// R-26 claim flipped it before the email row was created.
		_ = p.messageRepo.SetEmailID(msg.ID, em.ID)

		// Enqueue email for sending
		if err := p.producer.EnqueueEmailSend(em.ID, QueueBulk); err != nil {
			_ = p.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgFailed, "failed to enqueue email: "+err.Error())
		}
	}

	// Check if there are more pending messages
	remaining, err := p.messageRepo.CountPending(campaign.ID)
	if err != nil {
		return fmt.Errorf("failed to count remaining messages: %w", err)
	}

	if remaining > 0 {
		// The remaining pool may be held (A/B) or timezone-deferred; the next
		// tick re-evaluates both gates. Send-rate pacing applies on top.
		var delay time.Duration
		if campaign.SendRate > 0 {
			sent := len(sendable)
			delay = time.Duration(float64(sent) / float64(campaign.SendRate) * float64(time.Minute))
		}
		if recheckIn > delay {
			delay = recheckIn
		}
		return p.enqueueDeferredBatch(campaign.ID, delay)
	}

	if recheckIn > 0 {
		// Everything sent but the winner decision is still pending: hold
		// completion until the next check (analytics-only completion path).
		return p.enqueueDeferredBatch(campaign.ID, recheckIn)
	}

	// All done
	return p.completeCampaign(campaign)
}

// completeCampaign marks the campaign sent and fires the completed webhook.
func (p *CampaignProcessor) completeCampaign(campaign *models.Campaign) error {
	logger.Info("campaign sending complete", "id", campaign.ID)
	if err := p.campaignRepo.UpdateStatus(campaign.ID, models.CampaignStatusSent); err != nil {
		return err
	}
	if p.dispatcher != nil {
		p.dispatcher.DispatchCampaign(campaign.UserID, campaign.WorkspaceID, "campaign.completed", campaign.ID, campaign.Name, campaign.FromEmail)
	}
	return nil
}

// enqueueDeferredBatch schedules the next campaign batch tick after a delay.
// Deferred batches always re-check the live campaign state at execution time
// (the status guard at the top of HandleCampaignBatch), so a pause or cancel
// during a deferred window is honored. A non-positive delay is bumped to one
// minute so a deferral can never degenerate into a busy loop.
func (p *CampaignProcessor) enqueueDeferredBatch(campaignID uint, delay time.Duration) error {
	if p.producer == nil {
		return fmt.Errorf("campaign %d: producer not configured; cannot enqueue deferred batch", campaignID)
	}
	if delay <= 0 {
		delay = time.Minute
	}
	return p.producer.EnqueueCampaignBatch(campaignID, delay)
}

// deferredBatchDelay computes when the next batch tick can make progress for a
// campaign whose fetched sample was entirely deferred. It is the earlier of
// the A/B re-check time and the earliest timezone send-window opening across
// ALL still-pending messages (not just the fetched sample, which is only the
// head of the queue).
func (p *CampaignProcessor) deferredBatchDelay(campaign *models.Campaign, pending []models.CampaignMessage, recheckIn time.Duration) time.Duration {
	delay := recheckIn
	if campaign.SendAtLocalTime && campaign.LocalSendHour != nil {
		openings := p.pendingTimezoneOpenings(campaign, pending)
		if openings > 0 && (delay <= 0 || openings < delay) {
			delay = openings
		}
	}
	return delay
}

// pendingTimezoneOpenings returns the smallest duration until any pending
// message's local send window opens. It queries the timezone distribution of
// the whole pending pool; on query failure it falls back to the fetched
// sample, and when no opening can be derived it yields 0 (the caller's floor
// turns that into a one-minute re-check, which self-corrects).
func (p *CampaignProcessor) pendingTimezoneOpenings(campaign *models.Campaign, pending []models.CampaignMessage) time.Duration {
	counts, err := p.messageRepo.PendingTimezoneCounts(campaign.ID)
	if err != nil {
		logger.Warn("campaign: failed to load pending timezone distribution, using the fetched sample", "campaign_id", campaign.ID, "error", err)
		counts = make(map[string]int64)
		for _, msg := range pending {
			counts[msg.Subscriber.Timezone]++
		}
	}
	now := time.Now()
	earliest := time.Duration(0)
	for tz := range counts {
		loc := resolveSendLocation(tz)
		if localSendEligible(loc, *campaign.LocalSendHour, now) {
			return 0 // some group can go out right now
		}
		open := nextSendWindowOpen(loc, *campaign.LocalSendHour, now)
		if earliest == 0 || open < earliest {
			earliest = open
		}
	}
	return earliest
}

// resolvedContent holds the rendered template content for a campaign email.
type resolvedContent struct {
	Subject  string
	HTMLBody string
	TextBody string
}

// resolveTemplateContent looks up the template, renders it with campaign data, and injects CSS.
func (p *CampaignProcessor) resolveTemplateContent(campaign *models.Campaign, language string) *resolvedContent {
	tmpl, err := p.templateRepo.FindByID(campaign.TemplateID)
	if err != nil {
		logger.Error("campaign: template not found", "template_id", campaign.TemplateID)
		return nil
	}

	versionID := campaign.TemplateVersionID
	if versionID == nil {
		versionID = tmpl.ActiveVersionID
	}
	if versionID == nil {
		logger.Error("campaign: no active version", "template_id", campaign.TemplateID)
		return nil
	}

	v, err := p.versionRepo.FindByID(*versionID)
	if err != nil {
		logger.Error("campaign: version not found", "version_id", *versionID)
		return nil
	}

	lang := language
	if lang == "" {
		lang = campaign.Language
	}
	if lang == "" {
		lang = tmpl.DefaultLanguage
	}

	loc, err := p.localizationRepo.FindByVersionAndLanguage(*versionID, lang)
	if err != nil {
		if lang != tmpl.DefaultLanguage {
			loc, err = p.localizationRepo.FindByVersionAndLanguage(*versionID, tmpl.DefaultLanguage)
			if err != nil {
				logger.Error("campaign: localization not found", "version_id", *versionID, "language", lang)
				return nil
			}
		} else {
			return nil
		}
	}

	// Get CSS from stylesheet
	var css string
	if v.StyleSheet != nil {
		css = v.StyleSheet.CSS
	}

	// Render templates with campaign data
	renderer := email.NewTemplateRenderer()
	renderer.MissingKeyBehavior = "zero" // don't fail on missing keys in campaigns

	var data okapi.M
	if campaign.TemplateData != nil {
		data = okapi.M(campaign.TemplateData)
	}
	// Inject reserved {{ posta_* }} variables as sentinels. The campaign render is
	// shared across recipients, so the real per-recipient URLs are substituted
	// later in the per-message loop.
	data = okapi.M(email.WithSystemVars(data))

	rendered, err := renderer.Render(&email.RenderInput{
		SubjectTemplate: loc.SubjectTemplate,
		HTMLTemplate:    loc.HTMLTemplate,
		TextTemplate:    loc.TextTemplate,
		CSS:             css,
	}, data)
	if err != nil {
		logger.Error("campaign: template render failed", "error", err, "campaign_id", campaign.ID)
		// Fall back to raw content
		return &resolvedContent{
			Subject:  campaign.Subject,
			HTMLBody: loc.HTMLTemplate,
			TextBody: loc.TextTemplate,
		}
	}

	// Use rendered subject, but fall back to campaign.Subject if localization subject was empty
	subject := rendered.Subject
	if subject == "" {
		subject = campaign.Subject
	}

	return &resolvedContent{
		Subject:  subject,
		HTMLBody: rendered.HTML,
		TextBody: rendered.Text,
	}
}

// ---------- R-08: A/B hold-back assignment and winner selection ----------

// assignABTestGroups marks the test group: each variant takes its split
// percentage of the full audience (at least one recipient); recipients beyond
// the variant slices stay variant-less — the held remainder that receives the
// winner's content once the test window closes (R-08).
func assignABTestGroups(messages []models.CampaignMessage, variants models.ABTestVariants) {
	idx := 0
	for _, variant := range variants {
		if idx >= len(messages) {
			break
		}
		count := len(messages) * variant.SplitPercentage / 100
		if count == 0 {
			count = 1
		}
		end := idx + count
		if end > len(messages) {
			end = len(messages)
		}
		for i := idx; i < end; i++ {
			messages[i].Variant = variant.Name
		}
		idx = end
	}
	// messages[idx:] keep Variant "" — held back for the winner send.
}

// isHeldForWinner reports whether a pending message is part of the A/B
// hold-back remainder while the winner is still outstanding.
func isHeldForWinner(campaign *models.Campaign, variant string) bool {
	return campaign.ABTestEnabled && !campaign.ABTestWinnerSent && variant == ""
}

// abWinnerDecision is the outcome kind of a winner check.
type abWinnerDecision int

const (
	abDecisionWait     abWinnerDecision = iota // window open, or sample too small
	abDecisionSelected                         // a winner variant was chosen
)

// abWinnerResult carries the decision plus the stats it was based on — the
// same stats are persisted on the campaign row as the decision's audit record.
type abWinnerResult struct {
	Decision abWinnerDecision
	Winner   string
	Reason   string
	Stats    []models.ABVariantStat
}

// selectABWinner picks the winning variant from per-variant engagement stats.
//
// Selection rules (R-08): every variant must have at least minSample delivered
// messages (sample = status=sent count) or the check waits; the winner is the
// variant with the highest configured metric rate; ties go to the higher
// open-rate; an exact tie keeps the lower variant id (earlier in the variants
// list); with no opens and no clicks anywhere the first variant (A) wins.
func selectABWinner(variants models.ABTestVariants, stats []models.ABVariantStat, metric string, minSample int) abWinnerResult {
	if len(variants) == 0 {
		return abWinnerResult{Decision: abDecisionWait}
	}
	byName := make(map[string]models.ABVariantStat, len(stats))
	for _, s := range stats {
		byName[s.Variant] = s
	}
	ordered := make([]models.ABVariantStat, 0, len(variants))
	var totalOpens, totalClicks int64
	for _, v := range variants {
		s := byName[v.Name]
		s.Variant = v.Name
		ordered = append(ordered, s)
		totalOpens += s.Opened
		totalClicks += s.Clicked
	}

	for _, s := range ordered {
		if s.Sent < int64(minSample) {
			return abWinnerResult{Decision: abDecisionWait, Stats: ordered}
		}
	}

	if totalOpens == 0 && totalClicks == 0 {
		return abWinnerResult{
			Decision: abDecisionSelected,
			Winner:   ordered[0].Variant,
			Reason:   "no opens or clicks in any variant; defaulted to the first variant",
			Stats:    ordered,
		}
	}

	rate := func(s models.ABVariantStat) float64 {
		if s.Sent == 0 {
			return 0
		}
		if metric == models.ABTestMetricClickRate {
			return float64(s.Clicked) / float64(s.Sent)
		}
		return float64(s.Opened) / float64(s.Sent)
	}
	// Tie-break: the higher open-rate wins regardless of the chosen metric.
	openRate := func(s models.ABVariantStat) float64 {
		if s.Sent == 0 {
			return 0
		}
		return float64(s.Opened) / float64(s.Sent)
	}

	winner := ordered[0]
	for _, s := range ordered[1:] {
		switch {
		case rate(s) > rate(winner):
			winner = s
		case rate(s) == rate(winner) && openRate(s) > openRate(winner):
			winner = s
			// an exact tie keeps the earlier variant (lower variant id)
		}
	}
	return abWinnerResult{
		Decision: abDecisionSelected,
		Winner:   winner.Variant,
		Reason:   "highest " + metric,
		Stats:    ordered,
	}
}

// maybeSelectABWinner runs the R-08 winner check for A/B campaigns and
// unblocks the held remainder when a winner is chosen. It returns the duration
// until the next check while the decision is pending (0 when nothing is due),
// and mutates the passed campaign struct to reflect a fresh decision so the
// same tick can act on it.
//
// Exactly-once: the decision lands through a CAS on ab_test_winner_sent, so
// concurrent batch workers can never both claim the selection. The computed
// per-variant stats are persisted on the campaign row (ab_test_winner_stats)
// together with the decision reason — the durable audit record of the choice,
// including the sample each variant had at decision time.
func (p *CampaignProcessor) maybeSelectABWinner(campaign *models.Campaign) (time.Duration, error) {
	if !campaign.ABTestEnabled || campaign.ABTestWinnerSent || len(campaign.ABTestVariants) < 2 {
		return 0, nil
	}
	metric := campaign.ABTestMetric
	if metric != models.ABTestMetricClickRate {
		metric = defaultABTestMetric
	}
	windowHours := campaign.ABTestWindowHours
	if windowHours <= 0 {
		windowHours = defaultABTestWindowHours
	}
	minSample := campaign.ABTestMinSample
	if minSample <= 0 {
		minSample = defaultABTestMinSample
	}

	started := campaign.StartedAt
	if started == nil {
		if campaign.ScheduledAt != nil {
			t := *campaign.ScheduledAt
			started = &t
		} else {
			t := campaign.CreatedAt
			started = &t
		}
	}
	windowEnd := started.Add(time.Duration(windowHours) * time.Hour)
	now := time.Now()
	if now.Before(windowEnd) {
		// Test window still open: re-check just after it closes.
		return windowEnd.Sub(now) + time.Second, nil
	}

	stats, err := p.messageRepo.ABVariantStats(campaign.ID)
	if err != nil {
		return 0, fmt.Errorf("failed to load A/B variant stats for campaign %d: %w", campaign.ID, err)
	}

	result := selectABWinner(campaign.ABTestVariants, stats, metric, minSample)
	if result.Decision == abDecisionWait {
		graceEnd := windowEnd.Add(abWinnerMaxGrace)
		if now.Before(graceEnd) {
			recheck := abWinnerRecheckInterval
			if graceEnd.Before(now.Add(recheck)) {
				recheck = graceEnd.Sub(now) + time.Second
			}
			logger.Info("campaign A/B sample below the minimum; deferring winner selection",
				"campaign_id", campaign.ID, "recheck_in", recheck.String())
			return recheck, nil
		}
		// Grace exhausted: select from the best available data rather than
		// strand the held remainder forever (zero-engagement rule applies).
		logger.Warn("campaign A/B grace period exhausted; selecting winner from the best available data",
			"campaign_id", campaign.ID, "min_sample", minSample)
		result = selectABWinner(campaign.ABTestVariants, stats, metric, 0)
	}

	decisionAt := now.UTC().Format(time.RFC3339)
	statsData := models.TemplateData{
		"metric":       metric,
		"window_hours": windowHours,
		"min_sample":   minSample,
		"reason":       result.Reason,
		"decided_at":   decisionAt,
		"variants":     result.Stats,
	}
	won, err := p.campaignRepo.MarkABWinner(campaign.ID, result.Winner, statsData)
	if err != nil {
		return 0, fmt.Errorf("failed to persist the A/B winner for campaign %d: %w", campaign.ID, err)
	}
	if !won {
		// Another worker claimed the decision; adopt it without re-auditing.
		campaign.ABTestWinnerSent = true
		return 0, nil
	}
	campaign.ABTestWinner = result.Winner
	campaign.ABTestWinnerSent = true
	campaign.ABTestWinnerStats = statsData
	logger.Info("campaign A/B winner selected",
		"campaign_id", campaign.ID, "winner", result.Winner, "metric", metric,
		"reason", result.Reason, "decided_at", decisionAt, "stats", result.Stats)

	// Winner send: unblock the hold-back remainder with the winning variant.
	held, err := p.messageRepo.AssignVariantToHeld(campaign.ID, result.Winner)
	if err != nil {
		return 0, fmt.Errorf("failed to assign the winner variant to the held remainder of campaign %d: %w", campaign.ID, err)
	}
	if held > 0 {
		logger.Info("campaign A/B winner send unblocked",
			"campaign_id", campaign.ID, "winner", result.Winner, "held_recipients", held)
	}
	return 0, nil
}

// ---------- R-09: timezone-aware sending ----------

// resolveSendLocation maps a subscriber timezone to a time.Location. The
// documented fallbacks (R-09): an empty timezone joins the server-local batch
// (time.Local); a non-empty but unknown IANA name degrades gracefully to UTC.
// time.LoadLocation caches parsed zones, so per-message calls are cheap.
func resolveSendLocation(tz string) *time.Location {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		logger.Warn("campaign: unknown subscriber timezone, falling back to UTC", "timezone", tz)
		return time.UTC
	}
	return loc
}

// localSendEligible reports whether `now` falls inside the send window for
// loc. The window shape (R-09) is a single configured local hour: a group is
// eligible while its local wall-clock hour equals the configured send hour.
func localSendEligible(loc *time.Location, sendHour int, now time.Time) bool {
	return now.In(loc).Hour() == sendHour
}

// localSendEligibleNow is the per-message gate in the batch loop. Without
// local-time sending (or without a configured hour) everything is eligible —
// the send-immediately default.
func localSendEligibleNow(campaign *models.Campaign, subscriberTZ string, now time.Time) bool {
	if !campaign.SendAtLocalTime || campaign.LocalSendHour == nil {
		return true
	}
	return localSendEligible(resolveSendLocation(subscriberTZ), *campaign.LocalSendHour, now)
}

// nextSendWindowOpen returns how long until the next `sendHour`:00 wall-clock
// opening in loc, measured from now.
//
// DST is handled by construction (R-09): time.Date builds the candidate
// wall-clock time IN loc, so a spring-forward gap normalizes to the next valid
// instant and a fall-back repetition keeps the first occurrence; AddDate
// advances the calendar day so a deferred group keeps its local hour with the
// correct absolute delay across any transition.
func nextSendWindowOpen(loc *time.Location, sendHour int, now time.Time) time.Duration {
	localNow := now.In(loc)
	candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), sendHour, 0, 0, 0, loc)
	if !candidate.After(localNow) {
		// Today's window already passed (or is exactly now): next opening is
		// tomorrow at the same local hour.
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate.Sub(now)
}

// tzGroupPlan is the computed schedule for one timezone group.
type tzGroupPlan struct {
	Timezone    string
	EligibleNow bool
	NextOpenIn  time.Duration // 0 when EligibleNow
}

// planTimezoneGroups groups timezone values and computes each group's
// schedule. Without a configured send hour every group is immediately
// eligible: the send-immediately default.
func planTimezoneGroups(timezones []string, sendHour *int, now time.Time) []tzGroupPlan {
	seen := make(map[string]struct{}, len(timezones))
	plans := make([]tzGroupPlan, 0, 8)
	for _, tz := range timezones {
		if _, ok := seen[tz]; ok {
			continue
		}
		seen[tz] = struct{}{}
		plan := tzGroupPlan{Timezone: tz, EligibleNow: true}
		if sendHour != nil {
			loc := resolveSendLocation(tz)
			plan.EligibleNow = localSendEligible(loc, *sendHour, now)
			if !plan.EligibleNow {
				plan.NextOpenIn = nextSendWindowOpen(loc, *sendHour, now)
			}
		}
		plans = append(plans, plan)
	}
	return plans
}

// earliestOpenDelay returns how long until the first group can send: 0 when
// any group is immediately eligible (or when no send hour is configured).
func earliestOpenDelay(plans []tzGroupPlan) time.Duration {
	earliest := time.Duration(0)
	for _, plan := range plans {
		if plan.EligibleNow {
			return 0
		}
		if earliest == 0 || plan.NextOpenIn < earliest {
			earliest = plan.NextOpenIn
		}
	}
	return earliest
}

// messageTimezones maps the campaign's messages onto their subscriber
// timezones; messages without a matching subscriber record are skipped.
func messageTimezones(messages []models.CampaignMessage, subscribers []models.Subscriber) []string {
	tzBySub := make(map[uint]string, len(subscribers))
	for _, sub := range subscribers {
		tzBySub[sub.ID] = sub.Timezone
	}
	tzs := make([]string, 0, len(messages))
	for _, msg := range messages {
		if tz, ok := tzBySub[msg.SubscriberID]; ok {
			tzs = append(tzs, tz)
		}
	}
	return tzs
}
