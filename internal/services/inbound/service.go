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

package inbound

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

var (
	// ErrUnverifiedDomain is returned when none of the recipient domains match an
	// ownership-verified domain. Maps to SMTP 550.
	ErrUnverifiedDomain = errors.New("recipient domain is not verified")
	// ErrSizeExceeded is returned when the raw message exceeds the configured limit.
	// Maps to SMTP 552.
	ErrSizeExceeded = errors.New("message size exceeds limit")
	// ErrAttachmentTooLarge is returned when a single attachment is larger than
	// the configured per-attachment limit.
	ErrAttachmentTooLarge = errors.New("attachment exceeds per-attachment size limit")
	// ErrDuplicate is returned when a message with the same Message-ID already
	// exists for the resolved user. Callers should treat this as success (idempotent).
	ErrDuplicate = errors.New("duplicate message-id")
	// ErrSenderSuppressed is returned when the sender is on the recipient's
	// suppression list.
	ErrSenderSuppressed = errors.New("sender is suppressed")
)

// Enqueuer enqueues inbound-processing tasks. Satisfied by worker.Producer.
type Enqueuer interface {
	EnqueueInboundProcess(inboundEmailID uint) error
	EnqueueInboundParse(inboundEmailID uint) error
}

// ThreadRouter attaches an inbound email to an existing conversation (a web
// form thread) when one of its recipients is that conversation's reply
// address. It reports false when no recipient matched.
type ThreadRouter interface {
	RouteReply(ctx context.Context, rec *models.InboundEmail, recipients []string) (bool, error)
}

type Service struct {
	repo            *repositories.InboundEmailRepository
	domainRepo      *repositories.DomainRepository
	suppressionRepo *repositories.SuppressionRepository
	blobStore       blob.Store
	producer        Enqueuer
	bus             *eventbus.EventBus
	threadRouter    ThreadRouter
	// router evaluates inbound routing rules (OB-2, ADR-002 decision 1);
	// nil until SetRouter, and ingestion then behaves exactly as before
	// (no route validation, everything delivers).
	router *Router
	// forwardTasks is the durable forwarding queue (ADR-002 decision 2);
	// the forwarding executor (wave 2) consumes the rows.
	forwardTasks ForwardTaskQueue
	// webhooks dispatches route action=webhook through the existing HMAC
	// webhook machinery.
	webhooks WebhookDispatcher
	// quarantine persists quarantine rows for route action=quarantine; nil
	// until the quarantine wave wires a store.
	quarantine QuarantineStore
	// mailDelivery is the OB-3 hosted-mailbox ingestion seam (ADR-003, flow
	// F13): a matched route action=deliver additionally files the message
	// into the recipient mailbox's INBOX, quota-enforced. nil (the default)
	// keeps the exact pre-OB-3 behavior: archive only.
	mailDelivery MailboxDeliverer
	// dmarc ingests RFC 7489 aggregate reports from the inbound pipeline
	// (R-17); nil until EnableDMARC, and ingestion then behaves exactly as
	// before.
	dmarc *dmarcProcessor
	// verifier runs message-level SPF/DKIM/DMARC verification after durable
	// ingest (SF-22/23/24); nil until EnableVerification, and ingestion then
	// behaves exactly as before.
	verifier *verifyProcessor
	// verp encodes/validates the VERP bounce addresses of the return-path
	// feature (OB-2, ADR-002 decision 4); nil or disabled until SetVerp, and
	// ingestion then behaves exactly as before (no bounce allowlist).
	verp *Verp
	// tlsrpt ingests RFC 8460 SMTP TLS reports from the inbound pipeline
	// (SF-31, OB-6 W4, ADR-007 §5); nil until EnableTLSRPT, and ingestion
	// then behaves exactly as before.
	tlsrpt *tlsRptProcessor
	// tlsrptAddr is the dedicated rua RCPT allowlist (the TLS-RPT sibling of
	// the VERP bounce allowlist); nil until SetTLSRPTAddress, and the
	// receiver then accepts no report addresses.
	tlsrptAddr *TlsRptAllowlist
	// dsn matches RFC 3464 delivery reports back to the original outbound
	// message (OB-2 decision 4); nil until EnableDSN, and bounce ingestion
	// then behaves exactly as before (no match-back).
	dsn           *dsnMatcher
	maxMsgSize    int64
	maxAttachSize int64
	onReceived    func(source models.InboundSource)
	onRejected    func(reason string)
	onBytes       func(int64)
	onIngestMs    func(float64)
}

// Config holds runtime configuration for the inbound service.
type Config struct {
	MaxMessageSize    int64
	MaxAttachmentSize int64
}

func NewService(
	repo *repositories.InboundEmailRepository,
	domainRepo *repositories.DomainRepository,
	suppressionRepo *repositories.SuppressionRepository,
	cfg Config,
) *Service {
	return &Service{
		repo:            repo,
		domainRepo:      domainRepo,
		suppressionRepo: suppressionRepo,
		maxMsgSize:      cfg.MaxMessageSize,
		maxAttachSize:   cfg.MaxAttachmentSize,
	}
}

// SetBlobStore configures the blob store for attachment persistence.
// When nil, attachment content is stored inline (base64) on the InboundEmail record.
func (s *Service) SetBlobStore(bs blob.Store) { s.blobStore = bs }

// SetEnqueuer configures the worker-task enqueuer.
func (s *Service) SetEnqueuer(eq Enqueuer) { s.producer = eq }

// SetEventBus configures the event bus used to publish inbound.received events.
func (s *Service) SetEventBus(b *eventbus.EventBus) { s.bus = b }

// SetThreadRouter enables routing replies to thread addresses onto their
// conversation instead of the generic inbound pipeline.
func (s *Service) SetThreadRouter(r ThreadRouter) { s.threadRouter = r }

// SetRouter enables ADR-002 recipient routing: recipients on verified domains
// must resolve to an enabled route (exact, sub-address base, or catch-all)
// before the receiver accepts responsibility, and the matched action is
// executed after durable ingest. A nil router (the default) keeps the
// pre-routing behavior: every verified-domain recipient delivers.
func (s *Service) SetRouter(r *Router) { s.router = r }

// SetForwardQueue attaches the durable forwarding queue (ADR-002 decision 2).
// Wiring must always provide it when routing is enabled — a matched forward
// route without a queue is an operator-visible gap, logged loudly.
func (s *Service) SetForwardQueue(q ForwardTaskQueue) { s.forwardTasks = q }

// SetWebhookDispatcher attaches the webhook machinery used by route
// action=webhook (HMAC signing and the SSRF guard live inside it).
func (s *Service) SetWebhookDispatcher(d WebhookDispatcher) { s.webhooks = d }

// SetQuarantineStore attaches the quarantine row store (filled by the
// quarantine wave). Until wired, the inbound row's quarantined status is the
// only record of a quarantined message.
func (s *Service) SetQuarantineStore(qs QuarantineStore) { s.quarantine = qs }

// SetMailboxDeliverer attaches the OB-3 hosted-mailbox delivery seam
// (ADR-003, flow F13): route action=deliver then files the message into the
// recipient mailbox's INBOX, quota-enforced, over-quota quarantined with a
// typed reason. A nil seam (the default) keeps archive-only behavior.
func (s *Service) SetMailboxDeliverer(d MailboxDeliverer) { s.mailDelivery = d }

// SetVerp enables the return-path feature (OB-2, ADR-002 decision 4): the
// receiver then accepts VERP bounce addresses (bounce+<uuid+sig>@<subdomain>.
// <sending-domain>) through a dedicated allowlist and ingests them as system
// messages, and outbound direct delivery can stamp VERP envelope senders. A
// nil or disabled codec keeps today's behavior unchanged.
func (s *Service) SetVerp(v *Verp) { s.verp = v }

// RouteOwnsNotification reports whether a route action carries the message's
// own notification semantics, so the generic email.inbound process dispatch
// (parse → process worker chain) must not also run for it: forward routes
// queue the forwarding executor, webhook routes dispatch at ingest,
// quarantined messages wait for review. Deliver (and an empty action, i.e.
// no routing) keeps today's pipeline.
func RouteOwnsNotification(action models.InboundRouteAction) bool {
	switch action {
	case models.InboundRouteActionForward, models.InboundRouteActionWebhook, models.InboundRouteActionQuarantine:
		return true
	default:
		return false
	}
}

// RouteThread offers rec to the thread router. When the email is attached to a
// thread it is marked forwarded and the caller must not run the generic
// email.inbound processing for it. A router failure is logged and the email
// falls back to ordinary inbound handling, so it is never lost.
func (s *Service) RouteThread(ctx context.Context, rec *models.InboundEmail, candidates ...[]string) bool {
	if s.threadRouter == nil || rec == nil {
		return false
	}
	routed, err := s.threadRouter.RouteReply(ctx, rec, uniqueAddresses(candidates...))
	if err != nil {
		logger.Error("thread routing failed; handling as ordinary inbound", "inbound_id", rec.UUID, "error", err)
		return false
	}
	if !routed {
		return false
	}
	rec.Status = models.InboundStatusForwarded
	if err := s.repo.Update(rec); err != nil {
		logger.Error("failed to mark thread reply forwarded", "inbound_id", rec.UUID, "error", err)
	}
	return true
}

func uniqueAddresses(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, a := range list {
			key := strings.ToLower(strings.TrimSpace(a))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a)
		}
	}
	return out
}

// OnReceived sets a callback invoked after each successfully ingested inbound message.
func (s *Service) OnReceived(fn func(source models.InboundSource)) { s.onReceived = fn }

// OnRejected sets a callback invoked after each rejected inbound message.
func (s *Service) OnRejected(fn func(reason string)) { s.onRejected = fn }

// OnBytes sets a callback invoked with the total inbound byte count.
func (s *Service) OnBytes(fn func(int64)) { s.onBytes = fn }

// OnIngestDuration sets a callback invoked with ingestion duration in seconds.
func (s *Service) OnIngestDuration(fn func(float64)) { s.onIngestMs = fn }

// computeDedupHash derives a stable hash for messages without a Message-ID, used
// on the webhook path to reject near-duplicate retries.
func computeDedupHash(sender string, recipients []string, subject string, size int64) string {
	return "fh-" + fmt.Sprintf("%s|%s|%s|%d", strings.ToLower(sender), strings.ToLower(strings.Join(recipients, ",")), subject, size)
}

// Ingest is the single entry point for both SMTP and webhook paths.
// It resolves a verified tenant domain from the recipient list, persists the
// record (and attachments to blob storage when configured), and enqueues an
// async task to dispatch the email.inbound webhook.
func (s *Service) Ingest(ctx context.Context, p *ParsedEmail, source models.InboundSource) (*models.InboundEmail, error) {
	start := time.Now()
	defer func() {
		if s.onIngestMs != nil {
			s.onIngestMs(time.Since(start).Seconds())
		}
	}()

	if s.maxMsgSize > 0 && int64(len(p.Raw)) > s.maxMsgSize {
		s.rejected("size_exceeded")
		return nil, ErrSizeExceeded
	}

	domain, recipient, err := s.resolveDomain(p.To)
	if err != nil {
		s.rejected("unverified_domain")
		return nil, err
	}

	recipients := p.To
	if len(recipients) == 0 && recipient != "" {
		recipients = []string{recipient}
	}

	// OB-2 (ADR-002 decision 1): recipient routing validation happens before
	// the receiver accepts responsibility. A verified-domain recipient with
	// no matching route and no catch-all is explicitly rejected — persisted
	// like every other synchronous reject.
	route, matchKind, rerr := s.resolveRoute(domain, recipients)
	if errors.Is(rerr, ErrNoRoute) {
		s.rejected("no_route")
		return s.persistRejected(p, domain, source, "no matching inbound route"), ErrNoRoute
	} else if rerr != nil {
		// Route store unavailable: a temporary failure, not a policy reject.
		return nil, rerr
	}

	// Suppression check — skip delivery (record as rejected) if sender is suppressed.
	if s.suppressionRepo != nil && p.From != "" {
		scope := repositories.ResourceScope{UserID: domain.UserID, WorkspaceID: domain.WorkspaceID}
		if yes, _ := s.suppressionRepo.IsSuppressed(scope, strings.ToLower(p.From)); yes {
			s.rejected("sender_suppressed")
			return s.persistRejected(p, domain, source, "sender is on suppression list"), ErrSenderSuppressed
		}
	}

	// Idempotency: first by Message-ID when present, else by content-hash fallback.
	if p.MessageID != "" {
		if existing, err := s.repo.FindByMessageID(domain.UserID, p.MessageID); err == nil && existing != nil {
			return existing, ErrDuplicate
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Warn("failed to check message-id dedup", "error", err)
		}
	}

	var dedupHash string
	if p.MessageID == "" {
		dedupHash = computeDedupHash(p.From, recipients, p.Subject, int64(len(p.Raw)))
		if existing, err := s.repo.FindByDedupHash(domain.UserID, dedupHash); err == nil && existing != nil {
			return existing, ErrDuplicate
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Warn("failed to check dedup hash", "error", err)
		}
	}

	headersJSON, _ := json.Marshal(p.Headers)

	rec := &models.InboundEmail{
		UserID:      domain.UserID,
		WorkspaceID: domain.WorkspaceID,
		DomainID:    domain.ID,
		MessageID:   p.MessageID,
		DedupHash:   dedupHash,
		Sender:      p.From,
		Recipients:  recipients,
		Subject:     p.Subject,
		TextBody:    p.TextBody,
		HTMLBody:    p.HTMLBody,
		HeadersJSON: string(headersJSON),
		Size:        int64(len(p.Raw)),
		Status:      models.InboundStatusReceived,
		Source:      source,
		ReceivedAt:  time.Now().UTC(),
		// OB-2: the routing decision rides on the record — the post-ingest
		// executor reads the action from here, never from client input.
		RouteID:     inboundRouteID(route),
		RouteAction: inboundRouteAction(route),
	}

	if err := s.repo.Create(rec); err != nil {
		return nil, fmt.Errorf("persist inbound email: %w", err)
	}

	// Persist raw .eml bytes (best effort) so operators can export later.
	if s.blobStore != nil && len(p.Raw) > 0 {
		rawKey := fmt.Sprintf("inbound/%s/raw.eml", rec.UUID)
		if err := s.blobStore.Put(ctx, rawKey, bytes.NewReader(p.Raw), "message/rfc822"); err == nil {
			rec.RawStorageKey = rawKey
			_ = s.repo.Update(rec)
		} else {
			logger.Warn("failed to store raw inbound eml", "uuid", rec.UUID, "error", err)
		}
	}

	attachments, uploadedKeys, err := s.persistAttachments(ctx, rec.UUID, p.Attachments)
	if err != nil {
		// Best-effort cleanup of partial uploads so blob storage doesn't leak.
		if s.blobStore != nil {
			for _, k := range uploadedKeys {
				_ = s.blobStore.Delete(ctx, k)
			}
			if rec.RawStorageKey != "" {
				_ = s.blobStore.Delete(ctx, rec.RawStorageKey)
				rec.RawStorageKey = ""
			}
		}
		rec.Status = models.InboundStatusFailed
		rec.ErrorMessage = err.Error()
		_ = s.repo.Update(rec)
		s.rejected("attachment_error")
		return rec, err
	}
	if len(attachments) > 0 {
		if data, mErr := json.Marshal(attachments); mErr == nil {
			rec.AttachmentsJSON = string(data)
			_ = s.repo.Update(rec)
		}
	}

	routed := s.RouteThread(ctx, rec, recipients, p.Cc)

	// OB-2: execute the matched route's action — only after the record is
	// durably persisted (ADR-002 non-negotiable: durable path before any
	// routing action). Thread-routed replies follow the thread router's
	// semantics instead. The raw bytes ride along for the OB-3 mailbox
	// delivery seam (flow F13).
	if !routed {
		s.applyRouteActions(ctx, rec, route, matchKind, p.Raw)
	}

	// R-17: offer the stored message to the DMARC aggregate-report processor
	// (a no-op unless EnableDMARC wired one). Never fails the ingestion.
	s.processDMARC(ctx, rec)

	// SF-31: offer the stored message to the TLS-RPT processor (a no-op
	// unless EnableTLSRPT wired one). Never fails the ingestion.
	s.processTLSRPT(ctx, rec)

	// SF-22/23/24: verify SPF/DKIM/DMARC asynchronously — durable ingest
	// already happened, so delivery never waits on DNS. The webhook path has
	// no connecting-IP knowledge (the TCP peer is the forwarding provider,
	// not the mail source), so SPF honestly evaluates to none there; DKIM
	// and DMARC still run over the raw message.
	s.processVerification(rec, p.Raw, p.From, "")

	// OB-3 (flow F13): a deliver action whose mailbox append was refused for
	// quota quarantines the record inside applyRouteActions — such a held
	// record must not also run the generic process dispatch, exactly as for a
	// route action=quarantine. Delivered / archive-only records keep it.
	if s.producer != nil && !routed && !RouteOwnsNotification(inboundRouteAction(route)) &&
		rec.Status == models.InboundStatusReceived {
		if err := s.producer.EnqueueInboundProcess(rec.ID); err != nil {
			logger.Error("failed to enqueue inbound:process", "inbound_id", rec.ID, "error", err)
		}
	}

	if s.bus != nil {
		actor := rec.UserID
		s.bus.PublishSimple(
			models.EventCategoryEmail,
			"email.inbound.received",
			&actor,
			"",
			"",
			fmt.Sprintf("Inbound email received from %s", rec.Sender),
			map[string]any{
				"inbound_id":   rec.UUID,
				"sender":       rec.Sender,
				"recipients":   []string(rec.Recipients),
				"subject":      rec.Subject,
				"source":       string(rec.Source),
				"size":         rec.Size,
				"workspace_id": rec.WorkspaceID,
			},
		)
	}

	if s.onReceived != nil {
		s.onReceived(source)
	}
	if s.onBytes != nil {
		s.onBytes(rec.Size)
	}
	return rec, nil
}

// IngestRaw is the SMTP receiver's store-first path: only envelope facts and
// the raw bytes are known here (parsing happens asynchronously on the parse
// worker). clientIP is the connecting client's address for SPF (SF-22);
// empty means unknown.
func (s *Service) IngestRaw(ctx context.Context, raw []byte, envelopeFrom string, envelopeTo []string, source models.InboundSource, clientIP string) (*models.InboundEmail, error) {
	start := time.Now()
	defer func() {
		if s.onIngestMs != nil {
			s.onIngestMs(time.Since(start).Seconds())
		}
	}()

	if s.maxMsgSize > 0 && int64(len(raw)) > s.maxMsgSize {
		s.rejected("size_exceeded")
		return nil, ErrSizeExceeded
	}

	// OB-2 (ADR-002 decision 4): a VERP bounce recipient is a system address,
	// not a user mailbox — resolved through the return-path allowlist to its
	// parent tenant domain, never through routing rules, and never subject to
	// the sender-suppression check (the sender is a remote mailer-daemon).
	if d, bounceAddr, ok := s.resolveBounceRecipient(envelopeTo); ok {
		return s.ingestSystemBounce(ctx, raw, envelopeFrom, envelopeTo, d, bounceAddr, source, clientIP)
	}

	// SF-31 (OB-6 W4, ADR-007 §5): a TLS-RPT rua recipient is likewise a
	// system address — the dedicated allowlist resolves it to the tenant
	// domain, and the report stores durably without routing-rule evaluation
	// (no route can exist for tlsrpt@; that is the point of the allowlist).
	if d, rptAddr, ok := s.ResolveTLSRPTRecipient(envelopeTo); ok {
		return s.ingestSystemTLSRPT(ctx, raw, envelopeFrom, envelopeTo, d, rptAddr, source, clientIP)
	}

	domain, _, err := s.resolveDomain(envelopeTo)
	if err != nil {
		s.rejected("unverified_domain")
		return nil, err
	}

	// OB-2 (ADR-002 decision 1): routing validation before acceptance — an
	// enabled route (exact, sub-address base, or catch-all) must exist for a
	// verified-domain recipient, else the message is explicitly rejected and
	// persisted.
	route, matchKind, rerr := s.resolveRoute(domain, envelopeTo)
	if errors.Is(rerr, ErrNoRoute) {
		s.rejected("no_route")
		return s.persistRejectedRaw(raw, strings.ToLower(strings.TrimSpace(envelopeFrom)), envelopeTo, domain, source, "no matching inbound route"), ErrNoRoute
	} else if rerr != nil {
		// Route store unavailable: a temporary failure, not a policy reject.
		return nil, rerr
	}

	sender := strings.ToLower(strings.TrimSpace(envelopeFrom))
	if s.suppressionRepo != nil && sender != "" {
		scope := repositories.ResourceScope{UserID: domain.UserID, WorkspaceID: domain.WorkspaceID}
		if yes, _ := s.suppressionRepo.IsSuppressed(scope, sender); yes {
			s.rejected("sender_suppressed")
			return s.persistRejectedRaw(raw, sender, envelopeTo, domain, source, "sender is on suppression list"), ErrSenderSuppressed
		}
	}

	rec := &models.InboundEmail{
		UserID:      domain.UserID,
		WorkspaceID: domain.WorkspaceID,
		DomainID:    domain.ID,
		Sender:      sender,
		Recipients:  envelopeTo,
		Size:        int64(len(raw)),
		Status:      models.InboundStatusReceived,
		Source:      source,
		ReceivedAt:  time.Now().UTC(),
		RawContent:  raw,
		// OB-2: the routing decision rides on the record (see Ingest).
		RouteID:     inboundRouteID(route),
		RouteAction: inboundRouteAction(route),
	}
	if err := s.repo.Create(rec); err != nil {
		return nil, fmt.Errorf("persist inbound email: %w", err)
	}

	if s.blobStore != nil {
		rawKey := fmt.Sprintf("inbound/%s/raw.eml", rec.UUID)
		if err := s.blobStore.Put(ctx, rawKey, bytes.NewReader(raw), "message/rfc822"); err == nil {
			rec.RawStorageKey = rawKey
			rec.RawContent = nil
			if uerr := s.repo.Update(rec); uerr != nil {
				logger.Warn("failed to clear inline raw after blob upload", "uuid", rec.UUID, "error", uerr)
				rec.RawContent = raw
			}
		} else {
			logger.Warn("failed to store raw inbound eml", "uuid", rec.UUID, "error", err)
		}
	}

	// OB-2: execute the matched route's action — only after the durable
	// record exists (ADR-002 non-negotiable), and before the parse task is
	// enqueued so a quarantined record is never picked up for parsing. The
	// raw bytes ride along for the OB-3 mailbox delivery seam (flow F13).
	s.applyRouteActions(ctx, rec, route, matchKind, raw)

	// Quarantined records are held unparsed — the quarantine wave's release
	// re-runs routing and parsing; everything else proceeds to the parse
	// worker.
	if s.producer != nil && rec.Status == models.InboundStatusReceived {
		if err := s.producer.EnqueueInboundParse(rec.ID); err != nil {
			logger.Error("failed to enqueue inbound:parse", "inbound_id", rec.ID, "error", err)
		}
	}

	// SF-22/23/24: verify asynchronously after durable ingest — the SMTP
	// caller gets its 250 without waiting on DNS. The connecting IP is known
	// here, so SPF evaluates against the real client.
	s.processVerification(rec, raw, envelopeFrom, clientIP)

	if s.onReceived != nil {
		s.onReceived(source)
	}
	if s.onBytes != nil {
		s.onBytes(rec.Size)
	}
	return rec, nil
}

// resolveBounceRecipient validates the envelope's recipients against the VERP
// return-path allowlist (OB-2, ADR-002 decision 4): the first recipient that
// decodes as a validly signed bounce address (bounce+<uuid+sig>@<subdomain>.)
// on a tenant-verified parent domain wins. The signature is checked here —
// forged addresses fall through to ordinary recipient handling, which rejects
// them (the bounce host is not a tenant mailbox domain).
func (s *Service) resolveBounceRecipient(recipients []string) (*models.Domain, string, bool) {
	if !s.verp.Enabled() {
		return nil, "", false
	}
	for _, r := range recipients {
		addr := strings.ToLower(strings.TrimSpace(r))
		if !s.verp.LooksLikeBounce(addr) {
			continue
		}
		if _, ok := s.verp.Decode(addr); !ok {
			continue
		}
		_, host, ok := splitAddress(addr)
		if !ok {
			continue
		}
		parent, ok := s.verp.BounceParentDomain(host)
		if !ok {
			continue
		}
		d, err := s.domainRepo.FindVerifiedByName(parent)
		if err != nil {
			continue
		}
		return d, addr, true
	}
	return nil, "", false
}

// ingestSystemBounce durably ingests a DSN bounce as a system message
// (ADR-002 non-negotiable: durable path first), then matches the RFC 3464
// report back to the original outbound message. It never runs routing rules
// (bounces are not user mailboxes — no inbound_routes evaluation, no route
// stamp) and never consults the suppression list for the remote sender. The
// match-back is best effort: a DSN parse or match failure never fails the
// ingest that already stored the message.
func (s *Service) ingestSystemBounce(ctx context.Context, raw []byte, envelopeFrom string, envelopeTo []string, domain *models.Domain, bounceAddress string, source models.InboundSource, clientIP string) (*models.InboundEmail, error) {
	start := time.Now()
	defer func() {
		if s.onIngestMs != nil {
			s.onIngestMs(time.Since(start).Seconds())
		}
	}()

	rec := &models.InboundEmail{
		UserID:      domain.UserID,
		WorkspaceID: domain.WorkspaceID,
		DomainID:    domain.ID,
		Sender:      strings.ToLower(strings.TrimSpace(envelopeFrom)),
		Recipients:  envelopeTo,
		Size:        int64(len(raw)),
		Status:      models.InboundStatusReceived,
		Source:      source,
		ReceivedAt:  time.Now().UTC(),
		RawContent:  raw,
	}
	if err := s.repo.Create(rec); err != nil {
		return nil, fmt.Errorf("persist inbound bounce: %w", err)
	}

	if s.blobStore != nil {
		rawKey := fmt.Sprintf("inbound/%s/raw.eml", rec.UUID)
		if err := s.blobStore.Put(ctx, rawKey, bytes.NewReader(raw), "message/rfc822"); err == nil {
			rec.RawStorageKey = rawKey
			rec.RawContent = nil
			if uerr := s.repo.Update(rec); uerr != nil {
				logger.Warn("failed to clear inline raw after blob upload", "uuid", rec.UUID, "error", uerr)
				rec.RawContent = raw
			}
		} else {
			logger.Warn("failed to store raw inbound eml", "uuid", rec.UUID, "error", err)
		}
	}

	// DSN match-back (OB-2 decision 4): parse the report and write the
	// delivery truth onto the original message. Best effort — see above.
	s.processBounce(ctx, rec, raw, bounceAddress)

	// The bounce content still goes through the async parse worker so the
	// record is fully searchable like any other inbound mail.
	if s.producer != nil {
		if err := s.producer.EnqueueInboundParse(rec.ID); err != nil {
			logger.Error("failed to enqueue inbound:parse", "inbound_id", rec.ID, "error", err)
		}
	}

	if s.onReceived != nil {
		s.onReceived(source)
	}
	if s.onBytes != nil {
		s.onBytes(rec.Size)
	}
	_ = clientIP // SPF is not evaluated for system bounces; the DSN carries the truth.
	return rec, nil
}

func (s *Service) ApplyParsed(ctx context.Context, rec *models.InboundEmail, p *ParsedEmail) error {
	if p.MessageID != "" {
		if existing, err := s.repo.FindByMessageID(rec.UserID, p.MessageID); err == nil && existing != nil && existing.ID != rec.ID {
			rec.MessageID = p.MessageID
			rec.Status = models.InboundStatusRejected
			rec.ErrorMessage = "duplicate message-id"
			_ = s.repo.Update(rec)
			return ErrDuplicate
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Warn("failed to check message-id dedup", "error", err)
		}
	}

	recipients := rec.Recipients
	if len(p.To) > 0 {
		recipients = p.To
	}

	var dedupHash string
	if p.MessageID == "" {
		dedupHash = computeDedupHash(p.From, recipients, p.Subject, rec.Size)
		if existing, err := s.repo.FindByDedupHash(rec.UserID, dedupHash); err == nil && existing != nil && existing.ID != rec.ID {
			rec.DedupHash = dedupHash
			rec.Status = models.InboundStatusRejected
			rec.ErrorMessage = "duplicate content"
			_ = s.repo.Update(rec)
			return ErrDuplicate
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Warn("failed to check dedup hash", "error", err)
		}
	}

	// SF-49 slice: merge the persisted Authentication-Results header into the
	// parsed-headers view when the async verification already recorded a
	// verdict for this message. Existing A-R headers (an upstream MTA's) are
	// never touched; the verdict row stays authoritative.
	headers := p.Headers
	if s.verifier != nil && rec != nil {
		if ar := s.verifier.storedHeader(rec.ID); ar != "" {
			headers = withHeader(headers, "Authentication-Results", ar)
		}
	}
	headersJSON, _ := json.Marshal(headers)

	rec.MessageID = p.MessageID
	rec.DedupHash = dedupHash
	if p.From != "" {
		rec.Sender = p.From
	}
	if len(p.To) > 0 {
		rec.Recipients = p.To
	}
	rec.Subject = p.Subject
	rec.TextBody = p.TextBody
	rec.HTMLBody = p.HTMLBody
	rec.HeadersJSON = string(headersJSON)
	// OB-2: a record quarantined by its routing action keeps that status —
	// the parse worker must not silently release it back to received.
	if rec.Status != models.InboundStatusQuarantined {
		rec.Status = models.InboundStatusReceived
		rec.ErrorMessage = ""
	}

	attachments, uploadedKeys, err := s.persistAttachments(ctx, rec.UUID, p.Attachments)
	if err != nil {
		if s.blobStore != nil {
			for _, k := range uploadedKeys {
				_ = s.blobStore.Delete(ctx, k)
			}
		}
		return fmt.Errorf("persist attachments: %w", err)
	}
	if len(attachments) > 0 {
		if data, mErr := json.Marshal(attachments); mErr == nil {
			rec.AttachmentsJSON = string(data)
		}
	}

	if err := s.repo.Update(rec); err != nil {
		return fmt.Errorf("update inbound email: %w", err)
	}

	// R-17: the SMTP path parses asynchronously (IngestRaw stores only the
	// raw message), so ApplyParsed is where attachments first exist. Offer
	// the parsed record to the DMARC processor (a no-op unless wired).
	s.processDMARC(ctx, rec)

	// SF-31: same for the TLS-RPT processor — attachments exist only after
	// the parse pass (a no-op unless wired).
	s.processTLSRPT(ctx, rec)

	// OB-2: a quarantined record is under review — it never emits the
	// ordinary "received" event.
	if s.bus != nil && rec.Status != models.InboundStatusQuarantined {
		actor := rec.UserID
		s.bus.PublishSimple(
			models.EventCategoryEmail,
			"email.inbound.received",
			&actor,
			"",
			"",
			fmt.Sprintf("Inbound email received from %s", rec.Sender),
			map[string]any{
				"inbound_id":   rec.UUID,
				"sender":       rec.Sender,
				"recipients":   []string(rec.Recipients),
				"subject":      rec.Subject,
				"source":       string(rec.Source),
				"size":         rec.Size,
				"workspace_id": rec.WorkspaceID,
			},
		)
	}
	return nil
}

// LoadRaw returns the raw RFC 5322 bytes for a stored inbound record, reading
// from the inline RawContent column first and falling back to the blob store
// when the row has already been promoted.
func (s *Service) LoadRaw(ctx context.Context, rec *models.InboundEmail) ([]byte, error) {
	if len(rec.RawContent) > 0 {
		return rec.RawContent, nil
	}
	if rec.RawStorageKey == "" || s.blobStore == nil {
		return nil, fmt.Errorf("raw content unavailable for inbound %s", rec.UUID)
	}
	rc, err := s.blobStore.Get(ctx, rec.RawStorageKey)
	if err != nil {
		return nil, fmt.Errorf("fetch raw blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read raw blob: %w", err)
	}
	return raw, nil
}

// persistRejectedRaw records a synchronous-time rejection from IngestRaw where
// only envelope information is known — no parsed headers, no subject.
func (s *Service) persistRejectedRaw(raw []byte, sender string, recipients []string, domain *models.Domain, source models.InboundSource, reason string) *models.InboundEmail {
	rec := &models.InboundEmail{
		UserID:       domain.UserID,
		WorkspaceID:  domain.WorkspaceID,
		DomainID:     domain.ID,
		Sender:       sender,
		Recipients:   recipients,
		Size:         int64(len(raw)),
		Status:       models.InboundStatusRejected,
		Source:       source,
		ReceivedAt:   time.Now().UTC(),
		ErrorMessage: reason,
	}
	if err := s.repo.Create(rec); err != nil {
		logger.Error("failed to persist rejected inbound email", "error", err)
		return nil
	}
	return rec
}

// resolveDomain walks the recipient list and returns the first ownership-verified
// tenant domain that matches. Returns ErrUnverifiedDomain if none match.
func (s *Service) resolveDomain(recipients []string) (*models.Domain, string, error) {
	for _, r := range recipients {
		addr := strings.ToLower(strings.TrimSpace(r))
		at := strings.LastIndex(addr, "@")
		if at < 0 || at == len(addr)-1 {
			continue
		}
		d, err := s.domainRepo.FindVerifiedByName(addr[at+1:])
		if err != nil {
			continue
		}
		return d, addr, nil
	}
	return nil, "", ErrUnverifiedDomain
}

// resolveRoute resolves the routing rule for a message whose domain the
// caller has already verified (OB-2, ADR-002 decision 1). Returns
// (nil, "", nil) when routing is disabled — the pre-routing behavior. The
// first recipient that resolves wins; ErrNoRoute means no enabled rule and
// no catch-all matched any recipient on the verified domain (the explicit
// default-reject policy).
func (s *Service) resolveRoute(domain *models.Domain, recipients []string) (*models.InboundRoute, RouteMatchKind, error) {
	if s.router == nil {
		return nil, "", nil
	}
	for _, rcpt := range recipients {
		route, kind, err := s.router.ResolveRecipient(domain, rcpt)
		if err == nil {
			return route, kind, nil
		}
		if !errors.Is(err, ErrNoRoute) {
			return nil, "", err
		}
	}
	return nil, "", ErrNoRoute
}

// inboundRouteID copies the matched route's ID for the inbound row stamp.
func inboundRouteID(route *models.InboundRoute) *uint {
	if route == nil {
		return nil
	}
	id := route.ID
	return &id
}

// inboundRouteAction copies the matched route's action for the inbound row
// stamp (empty when routing is disabled or nothing matched).
func inboundRouteAction(route *models.InboundRoute) models.InboundRouteAction {
	if route == nil {
		return ""
	}
	return route.Action
}

// persistAttachments stores attachment content in blob storage (or inline when
// no store is configured). Returns attachment metadata plus the list of blob keys
// that were successfully uploaded — the caller uses the keys to clean up on
// later failure.
func (s *Service) persistAttachments(ctx context.Context, uuid string, atts []ParsedAttachment) ([]models.InboundAttachmentMeta, []string, error) {
	if len(atts) == 0 {
		return nil, nil, nil
	}
	out := make([]models.InboundAttachmentMeta, 0, len(atts))
	keys := make([]string, 0, len(atts))
	for i, a := range atts {
		if s.maxAttachSize > 0 && a.Size > s.maxAttachSize {
			return nil, keys, fmt.Errorf("%w: %q (%d bytes)", ErrAttachmentTooLarge, a.Filename, a.Size)
		}
		meta := models.InboundAttachmentMeta{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Size:        a.Size,
		}
		if s.blobStore != nil {
			key := fmt.Sprintf("inbound/%s/%d_%s", uuid, i, sanitizeFilename(a.Filename))
			if err := s.blobStore.Put(ctx, key, bytes.NewReader(a.Content), a.ContentType); err != nil {
				return nil, keys, fmt.Errorf("store attachment %q: %w", a.Filename, err)
			}
			meta.StorageKey = key
			keys = append(keys, key)
		} else {
			meta.Content = base64.StdEncoding.EncodeToString(a.Content)
		}
		out = append(out, meta)
	}
	return out, keys, nil
}

func (s *Service) persistRejected(p *ParsedEmail, domain *models.Domain, source models.InboundSource, reason string) *models.InboundEmail {
	rec := &models.InboundEmail{
		UserID:       domain.UserID,
		WorkspaceID:  domain.WorkspaceID,
		DomainID:     domain.ID,
		MessageID:    p.MessageID,
		Sender:       p.From,
		Recipients:   p.To,
		Subject:      p.Subject,
		Size:         int64(len(p.Raw)),
		Status:       models.InboundStatusRejected,
		Source:       source,
		ReceivedAt:   time.Now().UTC(),
		ErrorMessage: reason,
	}
	if err := s.repo.Create(rec); err != nil {
		logger.Error("failed to persist rejected inbound email", "error", err)
		return nil
	}
	return rec
}

func (s *Service) rejected(reason string) {
	if s.onRejected != nil {
		s.onRejected(reason)
	}
}

const defaultAttachmentName = "attachment"

func sanitizeFilename(name string) string {
	if name == "" {
		return defaultAttachmentName
	}
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
	if out == "" {
		return defaultAttachmentName
	}
	return out
}
