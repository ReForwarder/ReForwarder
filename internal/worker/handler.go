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
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

// EmailSendHandler processes email:send tasks from the Asynq queue.
type EmailSendHandler struct {
	emailRepo       *repositories.EmailRepository
	smtpRepo        *repositories.SMTPRepository
	serverRepo      *repositories.ServerRepository
	domainRepo      *repositories.DomainRepository
	contactRepo     *repositories.ContactRepository
	messageRepo     *repositories.CampaignMessageRepository
	campaignRepo    *repositories.CampaignRepository
	suppressionRepo *repositories.SuppressionRepository
	bounceRepo      *repositories.BounceRepository
	autoSuppress    bool
	sender          *email.SMTPSender
	stamper         *email.Stamper
	dispatcher      *webhook.Dispatcher
	blobStore       blob.Store
	recorder        *emailhistory.Recorder
	automations     automation.AutomationTrigger
	subscriberRepo  *repositories.SubscriberRepository
	onSent          func()
	onFailed        func()
}

func NewEmailSendHandler(
	emailRepo *repositories.EmailRepository,
	smtpRepo *repositories.SMTPRepository,
	serverRepo *repositories.ServerRepository,
	domainRepo *repositories.DomainRepository,
	contactRepo *repositories.ContactRepository,
	dispatcher *webhook.Dispatcher,
) *EmailSendHandler {
	return &EmailSendHandler{
		emailRepo:   emailRepo,
		smtpRepo:    smtpRepo,
		serverRepo:  serverRepo,
		domainRepo:  domainRepo,
		contactRepo: contactRepo,
		sender:      email.NewSMTPSender(),
		dispatcher:  dispatcher,
	}
}

// SetBlobStore sets the blob storage backend for fetching attachment content.
func (h *EmailSendHandler) SetBlobStore(bs blob.Store) { h.blobStore = bs }

// SetCampaignMessageRepo sets the campaign message repository so that
// campaign message statuses are updated when emails are sent or fail.
func (h *EmailSendHandler) SetCampaignMessageRepo(r *repositories.CampaignMessageRepository) {
	h.messageRepo = r
}

// SetCampaignRepo lets the handler consult the campaign's live status before
// sending. When set, already-queued emails for cancelled/paused campaigns are
// dropped instead of dispatched.
func (h *EmailSendHandler) SetCampaignRepo(r *repositories.CampaignRepository) {
	h.campaignRepo = r
}

// SetStamper enables X-Mailer/X-Posta-* header stamping and optional
// X-Posta-Signature HMAC. Nil disables stamping.
func (h *EmailSendHandler) SetStamper(s *email.Stamper) {
	h.stamper = s
}

// SetSuppressionRepo enables auto-suppression of recipients permanently
// rejected by the receiving server.
func (h *EmailSendHandler) SetSuppressionRepo(r *repositories.SuppressionRepository) {
	h.suppressionRepo = r
}

// SetBounceRepo lets the handler record a hard bounce when a recipient is
// permanently rejected.
func (h *EmailSendHandler) SetBounceRepo(r *repositories.BounceRepository) {
	h.bounceRepo = r
}

// SetRecorder wires the message-history event recorder so send transitions
// write their timeline events. Nil (the default) disables recording; recording
// failures never affect sending either way.
func (h *EmailSendHandler) SetRecorder(r *emailhistory.Recorder) {
	h.recorder = r
}

// SetSubscriberRepo lets bounced emails resolve the subscriber so automation
// triggers keyed on email.bounced can fire. Nil (default) disables that.
func (h *EmailSendHandler) SetSubscriberRepo(r *repositories.SubscriberRepository) {
	h.subscriberRepo = r
}

// SetAutomationTriggers wires the automation engine so email.bounced events
// can enroll matching automations. Nil (the default) disables that.
func (h *EmailSendHandler) SetAutomationTriggers(t automation.AutomationTrigger) {
	h.automations = t
}

// SetAutoSuppress toggles auto-suppression on permanent (5xx) recipient
// rejection. When false, such failures fall back to the normal retry path.
func (h *EmailSendHandler) SetAutoSuppress(enabled bool) {
	h.autoSuppress = enabled
}

// OnSent sets a callback invoked after each successful email send.
func (h *EmailSendHandler) OnSent(fn func()) { h.onSent = fn }

// OnFailed sets a callback invoked after each permanently failed email send.
func (h *EmailSendHandler) OnFailed(fn func()) { h.onFailed = fn }

// ProcessTask handles an email:send task.
func (h *EmailSendHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	var payload EmailSendPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	em, err := h.emailRepo.FindByID(payload.EmailID)
	if err != nil {
		return fmt.Errorf("email not found: %w", err)
	}

	// If this email was enqueued as part of a campaign, respect the live
	// campaign status. A paused campaign releases the message back to pending
	// so a Resume will re-dispatch it; cancelled drops it permanently.
	if h.messageRepo != nil && h.campaignRepo != nil {
		if msg, mErr := h.messageRepo.FindByEmailID(em.ID); mErr == nil {
			if camp, cErr := h.campaignRepo.FindByID(msg.CampaignID); cErr == nil {
				switch camp.Status {
				case models.CampaignStatusCancelled:
					_ = h.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgSkipped, "campaign cancelled")
					em.Status = models.EmailStatusFailed
					em.ErrorMessage = "campaign cancelled"
					_ = h.emailRepo.Update(em)
					logger.Info("worker: dropping email for cancelled campaign", "email_id", em.ID, "campaign_id", camp.ID)
					return nil
				case models.CampaignStatusPaused:
					_ = h.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgPending, "")
					em.Status = models.EmailStatusQueued
					_ = h.emailRepo.Update(em)
					logger.Info("worker: releasing email back to pending (campaign paused)", "email_id", em.ID, "campaign_id", camp.ID)
					return nil
				}
			}
		}
	}

	// Mark as processing
	em.Status = models.EmailStatusProcessing
	_ = h.emailRepo.Update(em)

	// Resolve sender address for validation/domain matching
	senderAddr := em.Sender
	if parsed, err := mail.ParseAddress(em.Sender); err == nil {
		senderAddr = parsed.Address
	}

	// Server selection:
	// 1. Try the workspace or user's own per-account SMTP server first.
	// 2. Fall back to a shared server whose allowed_domains covers the sender domain.
	var smtpServer *models.SMTPServer
	var sharedServerID uint // non-zero when a shared server is used

	var userServer *models.SMTPServer
	if em.WorkspaceID != nil {
		userServer, err = h.smtpRepo.FindFirstByWorkspaceID(*em.WorkspaceID)
	} else {
		userServer, err = h.smtpRepo.FindFirstByUserID(em.UserID)
	}
	if err == nil {
		// Validate the sender against the per-user allowed emails list
		if len(userServer.AllowedEmails) > 0 {
			allowed := false
			for _, e := range userServer.AllowedEmails {
				if e == senderAddr {
					allowed = true
					break
				}
			}
			if !allowed {
				h.markFailed(em, fmt.Sprintf("sender %q is not in the allowed emails list", em.Sender))
				return nil
			}
		}
		smtpServer = userServer
	} else {
		// No per-user server – look for a shared server by sender domain
		if h.serverRepo != nil {
			domain := senderDomain(senderAddr)
			if domain != "" {
				shared, serr := h.serverRepo.FindEnabledByDomain(domain)
				if serr == nil {
					// In strict mode the sender's domain must be ownership-verified.
					if shared.SecurityMode == models.ServerSecurityModeStrict {
						if h.domainRepo == nil || !h.domainRepo.IsOwnershipVerified(em.UserID, domain) {
							h.markFailed(em, fmt.Sprintf("shared server %q requires verified domain ownership for %q", shared.Name, domain))
							return nil
						}
					}
					smtpServer = shared.ToSMTPServer()
					sharedServerID = shared.ID
				}
			}
		}
	}

	if smtpServer == nil {
		h.markFailed(em, "no SMTP server configured for this account or domain")
		// Don't retry – adding a server won't happen automatically.
		return nil
	}

	em.SMTPHostname = smtpServer.Host
	_ = h.emailRepo.Update(em)

	// Auto-generate plain text from HTML if not provided
	if em.TextBody == "" && em.HTMLBody != "" {
		em.TextBody = email.HTMLToText(em.HTMLBody)
	}

	// Rebuild the attachments from the record, pulling blob-stored content if needed.
	attachments, err := email.LoadAttachments(ctx, em, h.blobStore)
	if err != nil {
		h.markFailed(em, fmt.Sprintf("failed to load attachments: %v", err))
		return nil
	}
	// An attachment with no payload would go out as a zero-byte file; fail loudly instead.
	for _, att := range attachments {
		if att.Content == "" {
			h.markFailed(em, fmt.Sprintf("attachment %q has no content", att.Filename))
			return nil
		}
	}

	// Parse custom headers from stored JSON
	var headers map[string]string
	if em.HeadersJSON != "" {
		_ = json.Unmarshal([]byte(em.HeadersJSON), &headers)
	}
	if headers == nil {
		headers = make(map[string]string)
	}

	// Drop recipients that became suppressed since this email was enqueued
	recipients := []string(em.Recipients)
	if h.suppressionRepo != nil {
		scope := repositories.ResourceScope{UserID: em.UserID, WorkspaceID: em.WorkspaceID}
		if filtered, fErr := h.suppressionRepo.FilterSuppressedForList(scope, recipients, em.UnsubscribeListID); fErr == nil {
			if len(filtered) == 0 {
				em.Status = models.EmailStatusSuppressed
				em.ErrorMessage = "all recipients are suppressed"
				_ = h.emailRepo.Update(em)
				if h.messageRepo != nil {
					if msg, mErr := h.messageRepo.FindByEmailID(em.ID); mErr == nil {
						_ = h.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgSkipped, "recipient suppressed")
					}
				}
				logger.Info("worker: skipping email, all recipients suppressed", "id", em.ID)
				return nil
			}
			recipients = filtered
		}
	}

	// Resolve the campaign message (if any) once: it drives campaign-aware
	// header stamping and the campaign_id metadata on history events.
	var campMsg *models.CampaignMessage
	if h.messageRepo != nil {
		if m, err := h.messageRepo.FindByEmailID(em.ID); err == nil {
			campMsg = m
		}
	}

	// Stamp Posta headers. Campaign mail gets campaign-aware headers; everything
	// else gets transactional headers. A final HMAC signs the message identity.
	if h.stamper != nil {
		if campMsg != nil {
			// Opens and clicks are independent since the global click-tracking
			// toggle: the pixel is always injected for campaign mail, but the
			// /t/c/ rewrite only happened when the setting was enabled at batch
			// time. Report each flag from what the body actually contains.
			tracked := em.HTMLBody != "" && em.ListUnsubscribeURL != ""
			clickTracked := tracked && strings.Contains(em.HTMLBody, "/t/c/")
			h.stamper.StampCampaign(
				headers, em,
				campMsg.CampaignID, campMsg.ID,
				tracked, clickTracked,
			)
		} else {
			h.stamper.StampTransactional(headers, em)
		}
		h.stamper.Sign(headers, em, recipients, em.Subject)
	}

	if err := h.sender.Send(smtpServer, em.Sender, recipients, em.Subject, em.HTMLBody, em.TextBody, attachments, headers, em.ListUnsubscribeURL, em.ListUnsubscribeMailto, em.ListUnsubscribePost); err != nil {
		// Increment the shared server's failure counter regardless of outcome.
		if sharedServerID != 0 && h.serverRepo != nil {
			go h.serverRepo.IncrementFailedCount(sharedServerID)
		}

		// A permanent rejection at RCPT TO (5xx, e.g. 550 user unknown) is a
		// hard bounce: suppress the recipient and stop retrying — retrying a
		// 5xx is pointless.
		if se, ok := permanentRejection(err); ok && h.autoSuppress {
			h.handlePermanentRejection(em, se)
			logger.Info("worker: recipient permanently rejected, suppressed", "id", em.ID, "recipient", se.Recipient, "code", se.Code)
			return nil
		}

		em.RetryCount++
		em.Status = models.EmailStatusFailed
		em.ErrorMessage = err.Error()
		_ = h.emailRepo.Update(em)
		emailhistory.RecordFailed(h.recorder, em, em.ErrorMessage, h.eventMetadata(smtpServer, campMsg, sharedServerID, em.RetryCount))
		logger.Debug("worker: email send failed, will retry", "id", em.ID, "attempt", em.RetryCount, "error", err)
		// Return error so Asynq retries the task
		return fmt.Errorf("SMTP send failed: %w", err)
	}

	// Success
	now := time.Now()
	em.Status = models.EmailStatusSent
	em.SentAt = &now
	em.ErrorMessage = ""
	_ = h.emailRepo.Update(em)
	emailhistory.RecordSent(h.recorder, em, h.eventMetadata(smtpServer, campMsg, sharedServerID, 0))
	// Increment the shared server's success counter
	if sharedServerID != 0 && h.serverRepo != nil {
		go h.serverRepo.IncrementSentCount(sharedServerID)
	}
	h.dispatcher.Dispatch(em.UserID, em.WorkspaceID, "email.sent", em.UUID, em.Sender)
	if h.onSent != nil {
		h.onSent()
	}
	if h.contactRepo != nil {
		go h.contactRepo.RecordSent(em.UserID, em.WorkspaceID, recipients)
	}
	if h.messageRepo != nil {
		if msg, err := h.messageRepo.FindByEmailID(em.ID); err == nil {
			_ = h.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgSent, "")
		}
	}
	logger.Info("worker: email sent successfully", "id", em.ID)

	return nil
}

func (h *EmailSendHandler) markFailed(em *models.Email, reason string) {
	em.Status = models.EmailStatusFailed
	em.ErrorMessage = reason
	_ = h.emailRepo.Update(em)
	emailhistory.RecordFailed(h.recorder, em, reason, nil)
	h.dispatcher.Dispatch(em.UserID, em.WorkspaceID, "email.failed", em.UUID, em.Sender)
	if h.onFailed != nil {
		h.onFailed()
	}
	if h.contactRepo != nil {
		go h.contactRepo.RecordFailed(em.UserID, em.WorkspaceID, em.Recipients)
	}
	if h.messageRepo != nil {
		if msg, err := h.messageRepo.FindByEmailID(em.ID); err == nil {
			_ = h.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgFailed, reason)
		}
	}
}

// eventMetadata builds the denormalized metadata carried on sent/failed
// history events: which server delivered (or rejected) the message, the
// campaign it belongs to, and the retry state at the time of the event.
func (h *EmailSendHandler) eventMetadata(smtpServer *models.SMTPServer, campMsg *models.CampaignMessage, sharedServerID uint, retryCount int) map[string]any {
	metadata := map[string]any{}
	if smtpServer != nil && smtpServer.Host != "" {
		metadata["smtp_hostname"] = smtpServer.Host
	}
	if campMsg != nil && campMsg.CampaignID != 0 {
		metadata["campaign_id"] = campMsg.CampaignID
	}
	if sharedServerID != 0 {
		metadata["shared_server_id"] = sharedServerID
	}
	if retryCount > 0 {
		metadata["retry_count"] = retryCount
	}
	return metadata
}

// permanentRejection reports whether err represents a permanent recipient
// rejection (a 5xx reply at RCPT TO, e.g. 550 user unknown) that should trigger
// auto-suppression. Transient errors (4xx), connection failures, and failures
// at other SMTP stages (MAIL FROM, DATA) return false so they keep retrying.
func permanentRejection(err error) (*email.SendError, bool) {
	var se *email.SendError
	if errors.As(err, &se) && se.Permanent() && se.Stage == "RCPT TO" && se.Recipient != "" {
		return se, true
	}
	return nil, false
}

// handlePermanentRejection records a hard bounce, adds the recipient to the
// suppression list (idempotently) and marks the email failed without retrying.
// Called when the receiving server permanently rejects a recipient (5xx at
// RCPT TO, e.g. 550 user unknown).
func (h *EmailSendHandler) handlePermanentRejection(em *models.Email, se *email.SendError) {
	reason := se.Msg
	if reason == "" {
		reason = se.Error()
	}

	// Record a hard bounce for audit/metrics.
	if h.bounceRepo != nil {
		bounce := &models.Bounce{
			UserID:      em.UserID,
			WorkspaceID: em.WorkspaceID,
			EmailID:     em.ID,
			Recipient:   se.Recipient,
			Type:        models.BounceTypeHard,
			Reason:      reason,
		}
		if err := h.bounceRepo.Create(bounce); err == nil {
			metrics.IncrementBounce(string(models.BounceTypeHard))
		}
	}

	// Add to the suppression list (idempotent — ignore "already suppressed").
	if h.suppressionRepo != nil {
		suppression := &models.Suppression{
			UserID:      em.UserID,
			WorkspaceID: em.WorkspaceID,
			Email:       se.Recipient,
			Reason:      fmt.Sprintf("auto-suppressed: SMTP %d %s", se.Code, reason),
		}
		if err := h.suppressionRepo.Upsert(suppression); err == nil {
			metrics.IncrementSuppression()
		}
	}

	em.Status = models.EmailStatusFailed
	em.ErrorMessage = se.Error()
	_ = h.emailRepo.Update(em)
	emailhistory.RecordFailed(h.recorder, em, em.ErrorMessage, nil)
	// A 5xx at RCPT TO is a hard bounce: record it as such on the timeline.
	// The stable provider event id dedups replays of the same task.
	emailhistory.RecordBounced(h.recorder, em, reason,
		fmt.Sprintf("smtp:%d:%s", se.Code, se.Recipient),
		map[string]any{"smtp_code": se.Code, "smtp_stage": se.Stage, "recipient": se.Recipient})

	// email.bounced automation trigger: resolve the subscriber so the engine
	// can enroll matching automations (nil repo/engine skips silently).
	if h.automations != nil && h.subscriberRepo != nil {
		if sub, serr := h.subscriberRepo.FindByEmail(repositories.ResourceScope{
			UserID:      em.UserID,
			WorkspaceID: em.WorkspaceID,
		}, se.Recipient); serr == nil {
			automation.RecordBouncedTrigger(h.automations, sub)
		}
	}

	h.dispatcher.Dispatch(em.UserID, em.WorkspaceID, "email.failed", em.UUID, em.Sender)
	if h.onFailed != nil {
		h.onFailed()
	}
	if h.contactRepo != nil {
		go h.contactRepo.RecordFailed(em.UserID, em.WorkspaceID, em.Recipients)
	}
	if h.messageRepo != nil {
		if msg, err := h.messageRepo.FindByEmailID(em.ID); err == nil {
			_ = h.messageRepo.UpdateStatus(msg.ID, models.CampaignMsgFailed, reason)
		}
	}
}

// senderDomain extracts the domain part of an email address.
func senderDomain(addr string) string {
	parts := strings.SplitN(addr, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}

// ExhaustedErrorHandler marks emails as permanently failed when Asynq exhausts
// all retries. It implements asynq.ErrorHandler.
type ExhaustedErrorHandler struct {
	emailRepo  *repositories.EmailRepository
	dispatcher *webhook.Dispatcher
	recorder   *emailhistory.Recorder
	onFailed   func()
}

func NewExhaustedErrorHandler(emailRepo *repositories.EmailRepository, dispatcher *webhook.Dispatcher, onFailed func()) *ExhaustedErrorHandler {
	return &ExhaustedErrorHandler{
		emailRepo:  emailRepo,
		dispatcher: dispatcher,
		onFailed:   onFailed,
	}
}

// SetRecorder wires the message-history event recorder. Nil (the default)
// disables recording.
func (e *ExhaustedErrorHandler) SetRecorder(r *emailhistory.Recorder) {
	e.recorder = r
}

func (e *ExhaustedErrorHandler) HandleError(_ context.Context, t *asynq.Task, err error) {
	if t.Type() != TypeEmailSend {
		return
	}
	var payload EmailSendPayload
	if jsonErr := json.Unmarshal(t.Payload(), &payload); jsonErr != nil {
		logger.Error("exhausted handler: failed to unmarshal payload", "error", jsonErr)
		return
	}
	em, findErr := e.emailRepo.FindByID(payload.EmailID)
	if findErr != nil {
		return
	}
	em.Status = models.EmailStatusFailed
	em.ErrorMessage = fmt.Sprintf("permanently failed after retries: %v", err)
	_ = e.emailRepo.Update(em)
	emailhistory.RecordFailed(e.recorder, em, em.ErrorMessage, map[string]any{"retry_count": em.RetryCount})
	e.dispatcher.Dispatch(em.UserID, em.WorkspaceID, "email.failed", em.UUID, em.Sender)
	if e.onFailed != nil {
		e.onFailed()
	}
	logger.Error("worker: email permanently failed", "id", em.ID, "error", err)
}

// AutomationRunHandler processes automation:run tasks by advancing one journey
// through the automation engine.
type AutomationRunHandler struct {
	engine *automation.Engine
}

// NewAutomationRunHandler wraps an engine for the asynq mux.
func NewAutomationRunHandler(engine *automation.Engine) *AutomationRunHandler {
	return &AutomationRunHandler{engine: engine}
}

// ProcessTask executes the journey named in the payload. Returning an error
// lets asynq retry the task — correct here: the engine's journey-level CAS
// makes a re-run after an infra blip safe, while per-node failures and retries
// are handled inside the engine (which returns nil for them).
func (h *AutomationRunHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	var payload AutomationRunPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("failed to unmarshal automation run payload: %w", err)
	}
	return h.engine.ExecuteJourney(ctx, payload.JourneyID)
}
