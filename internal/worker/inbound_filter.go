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
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// InboundFilter is the spam/malware step of the ingest pipeline (OB-2,
// ADR-006). It runs in the WORKER — the parse handler for SMTP-path mail and
// the process handler for webhook-ingested mail — never in the SMTP session:
// durable ingest already happened, so a slow clamd scan or an external
// filter consult delays no SMTP response (ADR-006's ordering + performance
// decisions). Order inside the pipeline: malware scan first (a virus verdict
// ends processing), then spam scoring (auth verdicts → DNSBL → header
// heuristics → URLs → GTUBE → reputation), then the optional synchronous
// external-hook consult, then the disposition.
//
// Disposition is applied through the inbound service so the durable record,
// the quarantine seam and the event taxonomy (inbound.spam_flagged /
// inbound.malware_detected / inbound.quarantined) stay in one place.
//
// Documented boundary (parallel-wave seam): route actions (forward/webhook)
// execute at ingest time, before the worker runs. This filter therefore
// cannot veto an already-queued forward or webhook dispatch; it stamps the
// record's score/threat columns so downstream consumers can check them.

// authVerdictWait bounds how long scoring waits for the asynchronous
// SPF/DKIM/DMARC verdict row before scoring without it.
const authVerdictWait = 2 * time.Second

// malwareFloorScore stamps malware positives above every spam threshold.
const malwareFloorScore = 100.0

// FilterConsultant is the external-filter-hook seam. *webhook.Dispatcher
// satisfies it (the consult reuses the HMAC + SSRF machinery).
type FilterConsultant interface {
	ConsultFilter(ctx context.Context, userID uint, workspaceID *uint, event string, body []byte, fromAddr string, timeout time.Duration) (*webhook.FilterReply, error)
}

// InboundFilterOptions is the configuration the cmd wiring assembles from
// environment (POSTA_SPAM_* / POSTA_CLAMAV_*).
type InboundFilterOptions struct {
	// Enabled gates the whole step (POSTA_SPAM_FILTER_ENABLED, default true
	// when wired).
	Enabled bool
	// ExternalHook enables the synchronous filter consult
	// (POSTA_SPAM_EXTERNAL_FILTER_ENABLED, default false).
	ExternalHook bool
	// ExternalHookEvent is the webhook event the consult fires
	// (POSTA_SPAM_EXTERNAL_FILTER_EVENT, default "email.inbound.filter").
	ExternalHookEvent string
	// ExternalHookTimeout bounds the consult
	// (POSTA_SPAM_EXTERNAL_FILTER_TIMEOUT, default 5s).
	ExternalHookTimeout time.Duration
}

// InboundFilter orchestrates scan → score → consult → disposition for one
// durable inbound record.
type InboundFilter struct {
	repo      *repositories.InboundEmailRepository
	svc       *inbound.Service
	blobStore blob.Store
	filter    spamfilter.SpamFilter
	scanner   *spamfilter.ClamdClient
	hook      FilterConsultant
	opts      InboundFilterOptions
	onFlagged func()
	onScanned func()
}

// NewInboundFilter assembles the step. filter may be nil together with
// Enabled=false to disable the pipeline; scanner nil disables malware
// scanning; hook nil disables the external consult.
func NewInboundFilter(
	repo *repositories.InboundEmailRepository,
	svc *inbound.Service,
	blobStore blob.Store,
	filter spamfilter.SpamFilter,
	scanner *spamfilter.ClamdClient,
	hook FilterConsultant,
	opts InboundFilterOptions,
) *InboundFilter {
	if opts.ExternalHookEvent == "" {
		opts.ExternalHookEvent = "email.inbound.filter"
	}
	if opts.ExternalHookTimeout == 0 {
		opts.ExternalHookTimeout = webhook.FilterReplyTimeout
	}
	return &InboundFilter{
		repo:      repo,
		svc:       svc,
		blobStore: blobStore,
		filter:    filter,
		scanner:   scanner,
		hook:      hook,
		opts:      opts,
	}
}

// OnFlagged sets a callback invoked whenever a disposition quarantines or
// rejects a message (metrics wiring).
func (f *InboundFilter) OnFlagged(fn func()) { f.onFlagged = fn }

// OnScanned sets a callback invoked after each malware scan batch.
func (f *InboundFilter) OnScanned(fn func()) { f.onScanned = fn }

// Run executes the filter for one durably stored, freshly parsed record.
// It reports whether the caller may continue the ordinary pipeline (parse →
// thread routing → notification). The record must have status received —
// route-owned (quarantined/forwarded/webhook) records never reach the
// workers in that state.
func (f *InboundFilter) Run(ctx context.Context, rec *models.InboundEmail) bool {
	if f == nil || !f.opts.Enabled || f.filter == nil || rec == nil {
		return true
	}

	// 1. Malware (GAP-47): positives quarantine with the signature recorded.
	if f.scanner != nil && f.scanner.Enabled() {
		if f.scanAttachments(ctx, rec) {
			return false // disposition applied
		}
		if f.onScanned != nil {
			f.onScanned()
		}
	}

	// 2. Spam scoring (ADR-006 decision 1).
	score, reasons := f.score(ctx, rec)

	// 3. Disposition from the admin-configurable thresholds.
	disp := f.filter.Disposition(score)

	// 4. External filter hook (ADR-006 decision 3): synchronous, bounded,
	// fail-open — a reply can only worsen the disposition, never rescue one.
	reasons, disp = f.consultHook(ctx, rec, score, reasons, disp)

	inboundDisp := inbound.FilterDisposition{
		Disposition: disp,
		Score:       score,
		Reasons:     reasons,
	}
	switch disp {
	case spamfilter.DispositionDeliver:
		if err := f.svc.ApplyFilterDisposition(ctx, rec, inboundDisp); err != nil {
			logger.Warn("filter: failed to stamp deliver score",
				"inbound_id", rec.UUID, "error", err)
		}
		return true
	case spamfilter.DispositionQuarantine, spamfilter.DispositionReject:
		if err := f.svc.ApplyFilterDisposition(ctx, rec, inboundDisp); err != nil {
			logger.Error("filter: failed to apply disposition",
				"inbound_id", rec.UUID, "disposition", string(disp), "error", err)
			// The record stands durably; continuing the pipeline could
			// notify/forward a message the filter just rejected — stop here
			// and let the operator see the log. Fail toward quarantine
			// semantics (no further processing), never toward delivery.
			return false
		}
		if f.onFlagged != nil {
			f.onFlagged()
		}
		return false
	}
	return true
}

// score assembles the scoring input from the durable record and runs the
// scorer. Every unavailable signal abstains (no verdict row yet, no client
// IP, reputation outage) — the score only reflects what is known.
func (f *InboundFilter) score(ctx context.Context, rec *models.InboundEmail) (float64, []string) {
	verdict := f.svc.WaitAuthResults(ctx, rec.ID, authVerdictWait)
	headers := map[string]string{}
	if rec.HeadersJSON != "" {
		_ = json.Unmarshal([]byte(rec.HeadersJSON), &headers)
	}
	msg := &spamfilter.Message{
		InboundID:    rec.ID,
		UUID:         rec.UUID,
		UserID:       rec.UserID,
		Sender:       rec.Sender,
		Recipients:   []string(rec.Recipients),
		Subject:      rec.Subject,
		TextBody:     rec.TextBody,
		HTMLBody:     rec.HTMLBody,
		Headers:      headers,
		Size:         rec.Size,
		ReceivedAt:   rec.ReceivedAt,
		ClientIP:     verdictClientIP(verdict),
		Auth:         inbound.AuthVerdictFromRow(verdict),
		SenderDomain: senderDomain(rec.Sender),
	}
	score, reasons := f.filter.Score(msg)
	logger.Debug("filter scored inbound message",
		"inbound_id", rec.UUID, "score", score, "reasons", strings.Join(reasons, ","))
	return score, reasons
}

// verdictClientIP extracts the connecting IP recorded by the verification
// hook (the SMTP path's durable record of it); empty on the webhook path.
func verdictClientIP(row *models.InboundAuthResult) string {
	if row == nil {
		return ""
	}
	return row.ClientIP
}

// scanAttachments scans every stored attachment through clamd. It reports
// whether the record was disposed (true) or scanning can continue (false).
// A scanner/transport failure follows the configured fail mode: open logs
// and continues; closed quarantines unscanned content.
func (f *InboundFilter) scanAttachments(ctx context.Context, rec *models.InboundEmail) (disposed bool) {
	atts := f.loadAttachments(ctx, rec)
	if len(atts) == 0 {
		return false
	}
	for _, att := range atts {
		result, err := f.scanner.Scan(ctx, att.Content)
		if err != nil {
			// Oversized samples were skipped, not failed.
			if result.Skipped {
				logger.Warn("filter: attachment exceeds clamd stream limit; not scanned",
					"inbound_id", rec.UUID, "filename", att.Filename)
				continue
			}
			logger.Error("filter: clamd scan failed",
				"inbound_id", rec.UUID, "filename", att.Filename, "error", err)
			if f.scanner.FailMode() == spamfilter.ClamFailClosed {
				disp := inbound.FilterDisposition{
					Disposition: spamfilter.DispositionQuarantine,
					Score:       malwareFloorScore,
					Reasons:     []string{"clamd_unavailable"},
				}
				if aerr := f.svc.ApplyFilterDisposition(ctx, rec, disp); aerr != nil {
					logger.Error("filter: failed to quarantine on clamd outage",
						"inbound_id", rec.UUID, "error", aerr)
				}
				if f.onFlagged != nil {
					f.onFlagged()
				}
				return true
			}
			continue // fail-open: unscanned content proceeds
		}
		if result.Infected {
			disp := inbound.FilterDisposition{
				Disposition:     spamfilter.DispositionQuarantine,
				Score:           malwareFloorScore,
				Reasons:         []string{"malware_detected"},
				ThreatSignature: result.Signature,
				Malware:         true,
			}
			if aerr := f.svc.ApplyFilterDisposition(ctx, rec, disp); aerr != nil {
				logger.Error("filter: failed to quarantine malware positive",
					"inbound_id", rec.UUID, "error", aerr)
			}
			logger.Warn("filter: malware detected in inbound attachment",
				"inbound_id", rec.UUID, "filename", att.Filename, "signature", result.Signature)
			if f.onFlagged != nil {
				f.onFlagged()
			}
			return true
		}
	}
	return false
}

// loadAttachments materializes every stored attachment's bytes from the
// record: blob storage when the content was promoted, inline base64
// otherwise. Load failures yield a nil-content entry — the scan loop treats
// a nil sample as unscannable and applies the fail mode.
func (f *InboundFilter) loadAttachments(ctx context.Context, rec *models.InboundEmail) []spamfilter.Attachment {
	var stored []models.InboundAttachmentMeta
	if rec.AttachmentsJSON == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(rec.AttachmentsJSON), &stored); err != nil {
		logger.Warn("filter: failed to decode attachments json",
			"inbound_id", rec.UUID, "error", err)
		return nil
	}
	out := make([]spamfilter.Attachment, 0, len(stored))
	for _, m := range stored {
		att := spamfilter.Attachment{Filename: m.Filename, ContentType: m.ContentType}
		switch {
		case m.StorageKey != "" && f.blobStore != nil:
			rc, err := f.blobStore.Get(ctx, m.StorageKey)
			if err == nil {
				content, rerr := io.ReadAll(rc)
				_ = rc.Close()
				if rerr == nil {
					att.Content = content
				} else {
					logger.Warn("filter: failed to read attachment blob",
						"inbound_id", rec.UUID, "key", m.StorageKey, "error", rerr)
				}
			} else {
				logger.Warn("filter: failed to open attachment blob",
					"inbound_id", rec.UUID, "key", m.StorageKey, "error", err)
			}
		case m.Content != "":
			if content, err := base64.StdEncoding.DecodeString(m.Content); err == nil {
				att.Content = content
			} else {
				logger.Warn("filter: failed to decode inline attachment",
					"inbound_id", rec.UUID, "filename", m.Filename, "error", err)
			}
		}
		out = append(out, att)
	}
	return out
}

// filterConsultPayload is the external filter-request body: a message
// reference plus metadata — never raw content (ADR-006 decision 3).
type filterConsultPayload struct {
	Event       string   `json:"event"`
	Timestamp   string   `json:"timestamp"`
	InboundID   string   `json:"inbound_id"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	Subject     string   `json:"subject"`
	MessageID   string   `json:"message_id,omitempty"`
	Size        int64    `json:"size"`
	Source      string   `json:"source"`
	ReceivedAt  string   `json:"received_at"`
	LocalScore  float64  `json:"local_score"`
	LocalReason []string `json:"local_reasons,omitempty"`
}

// consultHook runs the optional synchronous external filter consult. Fail
// mode is one-way: ANY error (no subscriber, timeout, bad reply) means "no
// external opinion" — the local disposition stands. A reply disposition can
// only escalate: quarantine over deliver, reject over anything.
func (f *InboundFilter) consultHook(ctx context.Context, rec *models.InboundEmail, score float64, reasons []string, disp spamfilter.Disposition) ([]string, spamfilter.Disposition) {
	if !f.opts.ExternalHook || f.hook == nil {
		return reasons, disp
	}
	body, err := json.Marshal(filterConsultPayload{
		Event:       f.opts.ExternalHookEvent,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		InboundID:   rec.UUID,
		From:        rec.Sender,
		To:          []string(rec.Recipients),
		Subject:     rec.Subject,
		MessageID:   rec.MessageID,
		Size:        rec.Size,
		Source:      string(rec.Source),
		ReceivedAt:  rec.ReceivedAt.UTC().Format(time.RFC3339),
		LocalScore:  score,
		LocalReason: reasons,
	})
	if err != nil {
		logger.Warn("filter: failed to marshal consult payload", "inbound_id", rec.UUID, "error", err)
		return reasons, disp
	}
	reply, err := f.hook.ConsultFilter(ctx, rec.UserID, rec.WorkspaceID, f.opts.ExternalHookEvent, body, rec.Sender, f.opts.ExternalHookTimeout)
	if err != nil {
		// Fail-open by design (ADR-006): the hook is an enrichment.
		logger.Debug("filter: external consult unavailable", "inbound_id", rec.UUID, "error", err)
		return reasons, disp
	}
	switch reply.Disposition {
	case "reject":
		return append(reasons, "external_filter:reject"), spamfilter.DispositionReject
	case "quarantine":
		if disp == spamfilter.DispositionDeliver {
			disp = spamfilter.DispositionQuarantine
		}
		return append(reasons, "external_filter:quarantine"), disp
	default:
		return reasons, disp
	}
}
