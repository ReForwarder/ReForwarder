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

package email

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/queue"
	"github.com/goposta/posta/internal/services/ratelimit"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

const (
	DefaultMaxAttachmentSize int64 = 10 * 1024 * 1024 // 10MB per attachment
	DefaultMaxTotalSize      int64 = 25 * 1024 * 1024 // 25MB total

	defaultFromAddress = "noreply@localhost"
)

// EmailEnqueuer enqueues an email for background delivery.
// When set on the Service, emails are sent asynchronously via a worker.
type EmailEnqueuer interface {
	EnqueueEmailSend(emailID uint, queue string) error
	EnqueueEmailSendAt(emailID uint, queue string, sendAt time.Time) error
}

// messageSender is the delivery seam of the synchronous pipeline (sendSync).
// *SMTPSender is the production implementation; tests inject a stub so the
// CA-06 dev fallback broker can be exercised without a live SMTP server.
type messageSender interface {
	Send(server *models.SMTPServer, from string, to []string, subject, htmlBody, textBody string, attachments []models.Attachment, headers map[string]string, listUnsubscribeURL, listUnsubscribeMailto string, listUnsubscribePost bool) error
}

// PlanLimitsProvider resolves effective plan limits for a given workspace.
type PlanLimitsProvider interface {
	EffectiveLimits(workspaceID *uint) *PlanLimits
}

// PlanLimits holds the resolved limits from a plan or global settings.
type PlanLimits struct {
	HourlyRateLimit     int
	DailyRateLimit      int
	MaxAttachmentSizeMB int
	MaxBatchSize        int
}

// LinkGenerator produces the signed public links Posta injects into messages:
// the one-click (RFC 8058) unsubscribe URL and the hosted "view in browser" URL.
// Kept as a narrow interface so the email service does not depend directly on the
// tracking package.
type LinkGenerator interface {
	TxUnsubscribeURL(emailID uint) string
	WebViewURL(emailUUID string) string
}

type Service struct {
	emailRepo        *repositories.EmailRepository
	smtpRepo         *repositories.SMTPRepository
	templateRepo     *repositories.TemplateRepository
	suppressionRepo  *repositories.SuppressionRepository
	unsubRepo        *repositories.UnsubscribeListRepository
	versionRepo      *repositories.TemplateVersionRepository
	localizationRepo *repositories.TemplateLocalizationRepository
	contactRepo      *repositories.ContactRepository
	domainRepo       *repositories.DomainRepository
	userRepo         *repositories.UserRepository
	wsSettingRepo    *repositories.WorkspaceSettingRepository
	sender           messageSender
	renderer         *TemplateRenderer
	limiter          *ratelimit.RedisLimiter
	dispatcher       *webhook.Dispatcher
	enqueuer         EmailEnqueuer
	settings         *settings.Provider
	blobStore        blob.Store
	planLimits       PlanLimitsProvider
	linkGen          LinkGenerator
	recorder         *emailhistory.Recorder
	queuePlanner     QueuePlanner
	trackingSvc      *tracking.Service
	trackingEnabled  func() bool
	devMode          bool
	onSent           func()
	onFailed         func()
	onQueued         func()
}

func NewService(
	emailRepo *repositories.EmailRepository,
	smtpRepo *repositories.SMTPRepository,
	templateRepo *repositories.TemplateRepository,
	suppressionRepo *repositories.SuppressionRepository,
	limiter *ratelimit.RedisLimiter,
	dispatcher *webhook.Dispatcher,
	devMode bool,
) *Service {
	return &Service{
		emailRepo:       emailRepo,
		smtpRepo:        smtpRepo,
		templateRepo:    templateRepo,
		suppressionRepo: suppressionRepo,
		sender:          NewSMTPSender(),
		renderer:        NewTemplateRenderer(),
		limiter:         limiter,
		dispatcher:      dispatcher,
		devMode:         devMode,
	}
}

// SetVersionRepos sets the version and localization repositories for multi-language support.
func (s *Service) SetVersionRepos(vr *repositories.TemplateVersionRepository, lr *repositories.TemplateLocalizationRepository) {
	s.versionRepo = vr
	s.localizationRepo = lr
}

// SetContactRepo sets the contact repository for tracking recipient stats.
func (s *Service) SetContactRepo(cr *repositories.ContactRepository) {
	s.contactRepo = cr
}

// SetUnsubscribeListRepo wires the repository used to validate Posta-managed
// unsubscribe-list references at send time.
func (s *Service) SetUnsubscribeListRepo(r *repositories.UnsubscribeListRepository) {
	s.unsubRepo = r
}

// SetDomainVerification sets the repositories for enforcing verified-domain
// sending. Strict mode is read from the workspace settings when a workspace is
// active, falling back to the user's setting in legacy personal mode.
func (s *Service) SetDomainVerification(dr *repositories.DomainRepository, ur *repositories.UserRepository, wsr *repositories.WorkspaceSettingRepository) {
	s.domainRepo = dr
	s.userRepo = ur
	s.wsSettingRepo = wsr
}

// SetSettings sets the platform settings provider for dynamic configuration.
func (s *Service) SetSettings(sp *settings.Provider) {
	s.settings = sp
}

// SetPlanLimits sets the plan limits provider for plan-aware enforcement.
func (s *Service) SetPlanLimits(pl PlanLimitsProvider) {
	s.planLimits = pl
}

// SetEnqueuer sets the email enqueuer for asynchronous delivery.
// When set, emails are enqueued to a background worker instead of being sent synchronously.
func (s *Service) SetEnqueuer(eq EmailEnqueuer) {
	s.enqueuer = eq
}

// SetLinkGenerator wires the generator for Posta-injected public links — the
// one-click unsubscribe URL and the hosted "view in browser" URL used to resolve
// reserved {{ posta_* }} template variables.
func (s *Service) SetLinkGenerator(gen LinkGenerator) {
	s.linkGen = gen
}

// SetBlobStore sets the blob storage backend for persisting email attachments.
// When configured, attachment content is uploaded to the store and only metadata
// (with a storage key) is kept in the database, reducing DB pressure.
func (s *Service) SetBlobStore(bs blob.Store) {
	s.blobStore = bs
}

// SetRecorder wires the message-history event recorder so sends write their
// sent/failed timeline events. Nil (the default) disables recording; recording
// failures never affect sending either way.
func (s *Service) SetRecorder(r *emailhistory.Recorder) {
	s.recorder = r
}

// SetTracking wires open/click tracking for every outbound email: ts rewrites
// links and injects the open pixel (keyed on the email UUID) when enabled()
// reports true. A nil service disables tracking entirely; a nil enabled func
// is treated as always enabled.
func (s *Service) SetTracking(ts *tracking.Service, enabled func() bool) {
	s.trackingSvc = ts
	s.trackingEnabled = enabled
}

// uploadAttachments stores each attachment's content in blob storage and replaces
// the inline base64 content with a storage key reference. Returns the modified
// attachments with StorageKey set and Content cleared.
func (s *Service) uploadAttachments(ctx context.Context, emailUUID string, attachments []models.Attachment) ([]models.Attachment, error) {
	result := make([]models.Attachment, len(attachments))
	for i, att := range attachments {
		decoded, err := base64.StdEncoding.DecodeString(att.Content)
		if err != nil {
			return nil, fmt.Errorf("attachment %q: invalid base64: %w", att.Filename, err)
		}
		key := fmt.Sprintf("emails/%s/%d_%s", emailUUID, i, att.Filename)
		if err := s.blobStore.Put(ctx, key, bytes.NewReader(decoded), att.ContentType); err != nil {
			return nil, fmt.Errorf("failed to upload attachment %q: %w", att.Filename, err)
		}
		result[i] = models.Attachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			StorageKey:  key,
		}
	}
	return result, nil
}

// DownloadAttachments fetches attachment content from blob storage and populates
// the base64 Content field for each attachment that has a StorageKey.
func (s *Service) DownloadAttachments(ctx context.Context, attachments []models.Attachment) ([]models.Attachment, error) {
	return fetchAttachmentContent(ctx, s.blobStore, attachments)
}

func fetchAttachmentContent(ctx context.Context, store blob.Store, attachments []models.Attachment) ([]models.Attachment, error) {
	result := make([]models.Attachment, len(attachments))
	for i, att := range attachments {
		result[i] = att
		if att.StorageKey == "" {
			continue
		}
		rc, err := store.Get(ctx, att.StorageKey)
		if err != nil {
			return nil, fmt.Errorf("failed to download attachment %q: %w", att.Filename, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read attachment %q: %w", att.Filename, err)
		}
		result[i].Content = base64.StdEncoding.EncodeToString(data)
	}
	return result, nil
}

// persistAttachments prepares attachments for the email record. With a blob store
// the bytes are uploaded and the record keeps only the key; without one the base64
// content stays on the record, because the record is all the send worker gets.
func (s *Service) persistAttachments(ctx context.Context, keyPrefix string, atts []models.Attachment) (string, error) {
	if len(atts) == 0 {
		return "", nil
	}
	stored := atts
	if s.blobStore != nil {
		uploaded, err := s.uploadAttachments(ctx, keyPrefix, atts)
		if err != nil {
			return "", fmt.Errorf("attachment upload: %w", err)
		}
		stored = uploaded
	}
	b, _ := json.Marshal(stored)
	return string(b), nil
}

// LoadAttachments rebuilds the attachments to send from a stored email record.
// The send worker runs long after Send returned, so the record is all it has:
// whatever cannot be recovered here goes out as a zero-byte file. store may be nil.
func LoadAttachments(ctx context.Context, em *models.Email, store blob.Store) ([]models.Attachment, error) {
	if em.AttachmentsJSON == "" {
		return nil, nil
	}
	var atts []models.Attachment
	if err := json.Unmarshal([]byte(em.AttachmentsJSON), &atts); err != nil {
		return nil, fmt.Errorf("decode attachments: %w", err)
	}
	if store == nil {
		return atts, nil
	}
	return fetchAttachmentContent(ctx, store, atts)
}

// DeleteAttachments removes attachment blobs from storage for the given keys.
func (s *Service) DeleteAttachments(ctx context.Context, attachments []models.Attachment) {
	for _, att := range attachments {
		if att.StorageKey != "" {
			_ = s.blobStore.Delete(ctx, att.StorageKey)
		}
	}
}

// OnSent sets a callback invoked after each successful email send.
func (s *Service) OnSent(fn func()) { s.onSent = fn }

// OnFailed sets a callback invoked after each failed email send.
func (s *Service) OnFailed(fn func()) { s.onFailed = fn }

// OnQueued sets a callback invoked after each email is enqueued for delivery.
func (s *Service) OnQueued(fn func()) { s.onQueued = fn }

var (
	ErrUnsubscribeListNotFound = errors.New("unsubscribe list not found in scope")
	ErrUnsubscribeInvalid      = errors.New("invalid unsubscribe configuration")
)

// Unsubscribe configures the List-Unsubscribe headers (RFC 2369 / 8058) and, for
// the Posta-managed path, list-scoped suppression on a one-click opt-out.
type Unsubscribe struct {
	// ListID (Posta-managed): reference an existing UnsubscribeList by id.
	ListID *uint `json:"list_id,omitempty" doc:"Reference an existing UnsubscribeList by id (Posta-managed). Posta mints the signed one-click URL and a click suppresses the recipient on this list only. Mutually exclusive with url."`

	// URL (caller-managed): you own the endpoint; Posta only emits the header.
	// Also the RFC 8058 POST target when OneClick is set.
	URL string `json:"url,omitempty" format:"url" doc:"Caller-managed unsubscribe endpoint. Posta only emits the List-Unsubscribe header; you own the endpoint. Also the RFC 8058 POST target when one_click is true (which requires https). Mutually exclusive with list_id."`
	// Mailto is an optional mailto: unsubscribe (RFC 2369), emitted alongside URL.
	Mailto string `json:"mailto,omitempty" doc:"Optional mailto: URI emitted alongside the URL in the List-Unsubscribe header (RFC 2369). A bare address is accepted; Posta prepends 'mailto:' if missing."`
	// OneClick emits "List-Unsubscribe-Post: List-Unsubscribe=One-Click" (requires
	// an https URL for the caller-managed path).
	OneClick bool `json:"one_click,omitempty" doc:"Emit 'List-Unsubscribe-Post: List-Unsubscribe=One-Click' (RFC 8058). Applies to the https URL target only; the caller-managed path requires an https url. Implied true on the Posta-managed (list_id) path."`
}

// errRecipientSuppressed is the reason recorded on an email, and reported in a
// batch result, when the recipient is on the workspace suppression list.
const errRecipientSuppressed = "recipient is suppressed"

type SendRequest struct {
	From        string              `json:"from" required:"true" doc:"Sender address. Accepts a plain address (hello@example.com) or RFC 5322 display-name format (Acme <hello@example.com>)."`
	To          []string            `json:"to" required:"true" minItems:"1" doc:"Recipient addresses. Each accepts a plain address or RFC 5322 display-name format (e.g. Jonas <jonas@example.com>)."`
	Subject     string              `json:"subject" required:"true"`
	HTML        string              `json:"html"`
	Text        string              `json:"text"`
	Attachments []models.Attachment `json:"attachments,omitempty" doc:"Optional file attachments. Each must include filename, content_type, and base64-encoded content. Subject to per-attachment and total size limits set by your plan."`
	Headers     map[string]string   `json:"headers,omitempty"`
	// ExternalID is a caller-supplied correlation id stored on the message so
	// integrations can find their own sends in the logs API.
	ExternalID string `json:"external_id,omitempty" doc:"Optional caller-supplied correlation id. Stored on the message and searchable in the message history."`
	// SystemHeaders are headers Posta itself sets on internal sends, such as the
	// Message-ID, In-Reply-To, and Reply-To that thread a form reply. They are
	// never accepted from API callers and apply whether or not the platform
	// allows custom headers.
	SystemHeaders map[string]string `json:"-"`
	Unsubscribe   *Unsubscribe      `json:"unsubscribe,omitempty"`
	// Deprecated: use Unsubscribe. Kept for one minor release; mapped onto
	// Unsubscribe{URL, OneClick} in resolveUnsubscribe.
	ListUnsubscribeURL  string     `json:"list_unsubscribe_url,omitempty" deprecated:"true"`
	ListUnsubscribePost bool       `json:"list_unsubscribe_post,omitempty" deprecated:"true"`
	SendAt              *time.Time `json:"send_at,omitempty"`
	// SF-08 queue-layer schedule overrides (all optional; absent fields fall
	// back to the platform queue settings, and with default settings the
	// queue keeps its legacy behavior). retry_intervals_minutes is the
	// per-message retry interval list (minutes between attempts, oldest
	// first); notify_addresses are the per-recipient delivery-notification
	// targets informed through the existing event taxonomy when a
	// recipient's schedule reaches a terminal state; expires_at is the
	// message TTL deadline the queue expiry sweep honors.
	RetryIntervalsMinutes []int      `json:"retry_intervals_minutes,omitempty" doc:"Optional queue retry interval list in minutes (e.g. [5,30,120,1440]): the delays between delivery attempts this message's recipients get, oldest first. Overrides the platform queue_retry_intervals setting for this message. At most 24 entries, each 1..43200 minutes."`
	NotifyAddresses       []string   `json:"notify_addresses,omitempty" doc:"Optional per-recipient delivery-notification addresses. When a recipient's queue schedule reaches a terminal state (retry intervals exhausted, message expired), these addresses ride the email.failed event taxonomy (message-history event + webhook payload with the schedule trace)."`
	ExpiresAt             *time.Time `json:"expires_at,omitempty" doc:"Optional queue expiry deadline (RFC 3339, future). The queue expiry sweep marks the message failed with an email.failed event once the deadline passes without delivery. Overrides the platform queue_message_expiry_minutes setting for this message."`
	// DeliverBySeconds is the RFC 2852 DELIVERBY by-time in seconds, set ONLY
	// by the SMTP submission/relay receivers (SF-09) when the client carried
	// the BY parameter: Send() stamps it onto the message as the expiry
	// provenance while ExpiresAt carries the computed deadline. Never bound
	// from API callers — the API deadline surface is expires_at.
	DeliverBySeconds int `json:"-"`
	// RequireTLS is the RFC 8689 REQUIRETLS requirement (SF-02), set ONLY by
	// the MSA when the submitting client carried the REQUIRETLS MAIL FROM
	// parameter: Send() stamps it onto the message so the delivery paths
	// enforce TLS-only hops. Never bound from API callers.
	RequireTLS bool `json:"-"`
	// TemplateName is populated by internal template-send paths
	TemplateName string `json:"-"`
	// TemplateData carries the render data of a template send. It is set by
	// the internal template-send paths (SendWithTemplate, SendBatch,
	// SendTestByTemplateID), persisted on the message for the message history,
	// and never accepted from callers of the raw send endpoint.
	TemplateData map[string]any `json:"-"`
}

// resolvedUnsub is the normalized, validated unsubscribe config used internally.
// MaxDeliverBySeconds bounds a DELIVERBY by-time at 30 days — the same
// horizon cap the schedule layer uses for retry intervals. A longer by-time
// is indistinguishable from no deadline in practice and is refused rather
// than honored dishonestly.
const MaxDeliverBySeconds = 30 * 86400

// DeliverByDeadline validates a DELIVERBY by-time (RFC 2852) and computes the
// message deadline: acceptance time + by-time. The receiver dependency parses
// BY as an RCPT TO parameter (go-smtp v0.25.0 conn.go — a deviation from RFC
// 2852 §4, which defines BY on MAIL FROM; honoring works identically). A
// non-positive by-time is refused — the server cannot honor an immediate
// deadline (BY=0 would expire the message at acceptance) — as is a by-time
// above the cap. Errors map to the RFC 2852 §4.2 posture: a 501 refusal.
func DeliverByDeadline(by time.Duration, acceptedAt time.Time) (time.Time, int, error) {
	secs := int(by / time.Second)
	if by <= 0 || secs < 1 {
		return time.Time{}, 0, fmt.Errorf("BY parameter below server minimum (the by-time must be at least 1 second)")
	}
	if secs > MaxDeliverBySeconds {
		return time.Time{}, 0, fmt.Errorf("BY parameter above the %d-second server maximum", MaxDeliverBySeconds)
	}
	return acceptedAt.Add(by), secs, nil
}

// QueuePlanner is the SF-08 schedule-planning seam (services/queue.Manager).
// When set, Send() prepares the per-recipient retry/notify schedule rows and
// the message TTL before enqueueing. Nil (the default) keeps the pre-SF-08
// behavior: no schedule rows, no TTL stamping.
type QueuePlanner interface {
	EnsureForEmail(em *models.Email, req queue.ScheduleRequest) error
}

// SetQueuePlanner wires the SF-08 schedule-planning seam.
func (s *Service) SetQueuePlanner(p QueuePlanner) { s.queuePlanner = p }

// validateScheduleRequest checks the optional SF-08 queue schedule fields.
// All are optional; present values must be well-formed so the queue layer
// never shapes retries from garbage.
func validateScheduleRequest(req *SendRequest) error {
	if err := queue.SanitizeIntervals(req.RetryIntervalsMinutes); err != nil {
		return err
	}
	seen := make(map[string]bool, len(req.NotifyAddresses))
	for _, a := range req.NotifyAddresses {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		parsed, err := mail.ParseAddress(a)
		if err != nil {
			return fmt.Errorf("notify_addresses: %q is not a valid address", a)
		}
		if seen[strings.ToLower(parsed.Address)] {
			continue
		}
		seen[strings.ToLower(parsed.Address)] = true
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("expires_at must be in the future")
	}
	return nil
}

// scheduleRequestFor maps the request's queue schedule fields onto the
// planner's request shape.
func scheduleRequestFor(req *SendRequest) queue.ScheduleRequest {
	addrs := make([]string, 0, len(req.NotifyAddresses))
	for _, a := range req.NotifyAddresses {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if parsed, err := mail.ParseAddress(a); err == nil {
			a = parsed.Address
		}
		addrs = append(addrs, a)
	}
	return queue.ScheduleRequest{
		RetryIntervalsMinutes: req.RetryIntervalsMinutes,
		NotifyAddresses:       addrs,
		ExpiresAt:             req.ExpiresAt,
	}
}

type resolvedUnsub struct {
	listID   *uint  // Posta-managed list (validated + owned)
	url      string // caller-managed https URL
	mailto   string // normalized mailto: URI
	oneClick bool   // emit List-Unsubscribe-Post
}

// resolveUnsubscribe normalizes the request's unsubscribe config
func (s *Service) resolveUnsubscribe(scope repositories.ResourceScope, req *SendRequest) (*resolvedUnsub, error) {
	u := req.Unsubscribe
	// Map legacy fields onto the object when the new one is absent.
	if u == nil {
		if req.ListUnsubscribeURL == "" && !req.ListUnsubscribePost {
			return nil, nil
		}
		logger.Warn("list_unsubscribe_url/list_unsubscribe_post are deprecated; use the unsubscribe object")
		u = &Unsubscribe{URL: req.ListUnsubscribeURL, OneClick: req.ListUnsubscribePost}
	}

	if u.ListID != nil && u.URL != "" {
		return nil, fmt.Errorf("%w: list_id and url are mutually exclusive", ErrUnsubscribeInvalid)
	}

	mailto := strings.TrimSpace(u.Mailto)
	if mailto != "" && !strings.HasPrefix(strings.ToLower(mailto), "mailto:") {
		mailto = "mailto:" + mailto
	}

	// Posta-managed: validate + own the list. The one-click URL is minted later
	// (it needs the email id); OneClick is implied.
	if u.ListID != nil {
		if s.unsubRepo == nil {
			return nil, fmt.Errorf("%w: list-managed unsubscribe is not configured", ErrUnsubscribeInvalid)
		}
		if _, err := s.unsubRepo.FindByIDForScope(*u.ListID, scope); err != nil {
			return nil, fmt.Errorf("%w: list_id %d", ErrUnsubscribeListNotFound, *u.ListID)
		}
		return &resolvedUnsub{listID: u.ListID, mailto: mailto, oneClick: true}, nil
	}

	// Caller-managed.
	if u.URL == "" {
		if mailto == "" {
			return nil, nil
		}
		return &resolvedUnsub{mailto: mailto}, nil
	}
	if u.OneClick && !strings.HasPrefix(strings.ToLower(u.URL), "https://") {
		return nil, fmt.Errorf("%w: one_click requires an https url", ErrUnsubscribeInvalid)
	}
	return &resolvedUnsub{url: u.URL, mailto: mailto, oneClick: u.OneClick}, nil
}

type SendTemplateRequest struct {
	TemplateID   *uint               `json:"template_id,omitempty" doc:"Template ID (preferred, uses primary key index)"`
	Template     string              `json:"template" doc:"Template name (fallback when template_id is not provided)"`
	Language     string              `json:"language" doc:"Language code for template localization (BCP 47 / ISO 639-1, e.g. en, fr, fr-CA). Falls back to base language, the template default, then en."`
	From         string              `json:"from" doc:"Sender address. Accepts a plain address (hello@example.com) or RFC 5322 display-name format (Acme <hello@example.com>)."`
	To           []string            `json:"to" required:"true" minItems:"1" doc:"Recipient addresses. Each accepts a plain address or RFC 5322 display-name format (e.g. Jonas <jonas@example.com>)."`
	TemplateData map[string]any      `json:"template_data" doc:"Variables substituted into the template's {{ var_name }} placeholders. Reserved posta_* variables (e.g. posta_unsubscribe_url, posta_web_view_url) are injected automatically."`
	Attachments  []models.Attachment `json:"attachments,omitempty" doc:"Optional file attachments. Each must include filename, content_type, and base64-encoded content. Subject to per-attachment and total size limits set by your plan."`
	Unsubscribe  *Unsubscribe        `json:"unsubscribe,omitempty"`
}

type SendResponse struct {
	ID     string             `json:"id"`
	Status models.EmailStatus `json:"status"`
}

type BatchRequest struct {
	TemplateID  *uint            `json:"template_id,omitempty" doc:"Template ID (preferred, uses primary key index)"`
	Template    string           `json:"template" doc:"Template name (fallback when template_id is not provided)"`
	Language    string           `json:"language" doc:"Default language for the batch (BCP 47 / ISO 639-1, e.g. en, fr, fr-CA). Each recipient can override via recipients[].language."`
	From        string           `json:"from" doc:"Sender address. Accepts a plain address (hello@example.com) or RFC 5322 display-name format (Acme <hello@example.com>)."`
	Recipients  []BatchRecipient `json:"recipients" required:"true" minItems:"1"`
	Unsubscribe *Unsubscribe     `json:"unsubscribe,omitempty"`
}

type BatchRecipient struct {
	Email        string         `json:"email" required:"true" format:"email" doc:"Recipient address. Accepts a plain address or RFC 5322 display-name format (e.g. Jonas <jonas@example.com>)."`
	Language     string         `json:"language" doc:"Per-recipient language override (BCP 47 / ISO 639-1). Falls back to the batch-level language, then the template default."`
	TemplateData map[string]any `json:"template_data" doc:"Variables substituted into the template's {{ var_name }} placeholders. Reserved posta_* variables (e.g. posta_unsubscribe_url, posta_web_view_url) are injected automatically."`
}

type BatchResponse struct {
	Total   int           `json:"total"`
	Sent    int           `json:"sent"`
	Failed  int           `json:"failed"`
	Skipped int           `json:"skipped"`
	Results []BatchResult `json:"results"`
}

type BatchResult struct {
	Email  string             `json:"email"`
	ID     string             `json:"id,omitempty"`
	Status models.EmailStatus `json:"status"`
	Error  string             `json:"error,omitempty"`
}

// DryRunResponse is returned when a send request is validated without actually sending.
type DryRunResponse struct {
	Valid           bool     `json:"valid"`
	From            string   `json:"from"`
	To              []string `json:"to"`
	Subject         string   `json:"subject"`
	Recipients      int      `json:"recipients"`
	SuppressedCount int      `json:"suppressed_count"`
	AttachmentCount int      `json:"attachment_count"`
	HasHTML         bool     `json:"has_html"`
	HasText         bool     `json:"has_text"`
	RenderedSubject string   `json:"rendered_subject,omitempty"`
}

// ValidateSend runs all validation checks without persisting or sending.
func (s *Service) ValidateSend(ctx context.Context, userID uint, workspaceID *uint, userEmail string, req *SendRequest) (*DryRunResponse, error) {
	if err := validateAddresses(req.From, req.To); err != nil {
		return nil, err
	}

	// Header-bound strings are validated exactly like a real send (R-24), so a
	// dry run flags an injection attempt instead of accepting it.
	if err := validateSendHeaders(req.Subject, req.Headers); err != nil {
		return nil, fmt.Errorf("header validation: %w", err)
	}
	req.From = s.applyDefaultSenderName(workspaceID, req.From)

	if s.settings != nil && s.settings.MaintenanceMode() {
		return nil, fmt.Errorf("maintenance: email sending is temporarily disabled")
	}

	if err := s.limiter.Check(ctx, userEmail); err != nil {
		return nil, fmt.Errorf("rate_limit: %w", err)
	}

	if len(req.Attachments) > 0 {
		maxSize := DefaultMaxAttachmentSize
		if s.planLimits != nil {
			pl := s.planLimits.EffectiveLimits(workspaceID)
			if pl.MaxAttachmentSizeMB > 0 {
				maxSize = int64(pl.MaxAttachmentSizeMB) * 1024 * 1024
			}
		} else if s.settings != nil {
			maxSize = int64(s.settings.MaxAttachmentSizeMB()) * 1024 * 1024
		}
		if err := ValidateAttachments(req.Attachments, maxSize, DefaultMaxTotalSize); err != nil {
			return nil, fmt.Errorf("attachment validation: %w", err)
		}
	}

	if err := s.checkDomainVerification(userID, workspaceID, req.From); err != nil {
		return nil, err
	}

	scope := repositories.ResourceScope{UserID: userID, WorkspaceID: workspaceID}
	unsub, err := s.resolveUnsubscribe(scope, req)
	if err != nil {
		return nil, err
	}
	var unsubListID *uint
	if unsub != nil {
		unsubListID = unsub.listID
	}
	activeRecipients := req.To
	suppressedCount := 0
	if s.suppressionRepo != nil {
		filtered, err := s.suppressionRepo.FilterSuppressedForList(scope, req.To, unsubListID)
		if err != nil {
			return nil, fmt.Errorf("failed to check suppression list: %w", err)
		}
		suppressedCount = len(req.To) - len(filtered)
		activeRecipients = filtered
	}

	return &DryRunResponse{
		Valid:           true,
		From:            req.From,
		To:              activeRecipients,
		Subject:         req.Subject,
		Recipients:      len(activeRecipients),
		SuppressedCount: suppressedCount,
		AttachmentCount: len(req.Attachments),
		HasHTML:         req.HTML != "",
		HasText:         req.Text != "",
	}, nil
}

// ValidateSendWithTemplate validates a template send request without sending.
func (s *Service) ValidateSendWithTemplate(ctx context.Context, userID uint, workspaceID *uint, userEmail string, req *SendTemplateRequest) (*DryRunResponse, error) {
	tmpl, err := s.findTemplate(userID, workspaceID, req.TemplateID, req.Template)
	if err != nil {
		return nil, fmt.Errorf("template not found: %s", req.Template)
	}

	rendered, err := s.resolveAndRender(tmpl, req.Language, req.TemplateData)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	from := req.From
	if from == "" {
		from = defaultFromAddress
	}

	resp, err := s.ValidateSend(ctx, userID, workspaceID, userEmail, &SendRequest{
		From:        from,
		To:          req.To,
		Subject:     rendered.Subject,
		HTML:        rendered.HTML,
		Text:        rendered.Text,
		Attachments: req.Attachments,
		Unsubscribe: req.Unsubscribe,
	})
	if err != nil {
		return nil, err
	}
	resp.RenderedSubject = rendered.Subject
	return resp, nil
}

// ValidateSendBatch validates a batch send request without sending.
func (s *Service) ValidateSendBatch(ctx context.Context, userID uint, workspaceID *uint, userEmail string, req *BatchRequest) (*DryRunResponse, error) {
	from := req.From
	if from == "" {
		from = defaultFromAddress
	}
	if err := validateEmailAddress(from); err != nil {
		return nil, fmt.Errorf("invalid sender: %w", err)
	}
	for _, r := range req.Recipients {
		if err := validateEmailAddress(r.Email); err != nil {
			return nil, fmt.Errorf("invalid recipient: %w", err)
		}
	}

	maxBatch := 0
	if s.planLimits != nil {
		maxBatch = s.planLimits.EffectiveLimits(workspaceID).MaxBatchSize
	} else if s.settings != nil {
		maxBatch = s.settings.MaxBatchSize()
	}
	if maxBatch > 0 && len(req.Recipients) > maxBatch {
		return nil, fmt.Errorf("batch size %d exceeds maximum allowed (%d)", len(req.Recipients), maxBatch)
	}

	_, err := s.findTemplate(userID, workspaceID, req.TemplateID, req.Template)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	emails := make([]string, len(req.Recipients))
	for i, r := range req.Recipients {
		emails[i] = r.Email
	}

	scope := repositories.ResourceScope{UserID: userID, WorkspaceID: workspaceID}
	unsub, err := s.resolveUnsubscribe(scope, &SendRequest{Unsubscribe: req.Unsubscribe})
	if err != nil {
		return nil, err
	}
	var unsubListID *uint
	if unsub != nil {
		unsubListID = unsub.listID
	}
	activeRecipients := emails
	suppressedCount := 0
	if s.suppressionRepo != nil {
		filtered, err := s.suppressionRepo.FilterSuppressedForList(scope, emails, unsubListID)
		if err != nil {
			return nil, fmt.Errorf("failed to check suppression list: %w", err)
		}
		suppressedCount = len(emails) - len(filtered)
		activeRecipients = filtered
	}

	return &DryRunResponse{
		Valid:           true,
		From:            from,
		To:              activeRecipients,
		Subject:         req.Template,
		Recipients:      len(activeRecipients),
		SuppressedCount: suppressedCount,
	}, nil
}

func (s *Service) Send(ctx context.Context, userID, apiKeyID uint, workspaceID *uint, userEmail string, req *SendRequest) (*SendResponse, error) {
	// Validate email addresses
	if err := validateAddresses(req.From, req.To); err != nil {
		return nil, err
	}

	// Reject header injection up front (R-24): a CR/LF in the Subject or in a
	// custom header name/value would add arbitrary headers to the message.
	if err := validateSendHeaders(req.Subject, req.Headers); err != nil {
		return nil, fmt.Errorf("header validation: %w", err)
	}

	// SF-08: validate the optional queue schedule fields up front so a
	// malformed interval list or expiry never reaches the queue layer.
	if err := validateScheduleRequest(req); err != nil {
		return nil, fmt.Errorf("queue schedule validation: %w", err)
	}

	req.From = s.applyDefaultSenderName(workspaceID, req.From)

	// Enforce maintenance mode
	if s.settings != nil && s.settings.MaintenanceMode() {
		return nil, fmt.Errorf("maintenance: email sending is temporarily disabled")
	}

	// Rate limiting: use plan limits if available, otherwise global.
	if s.planLimits != nil {
		pl := s.planLimits.EffectiveLimits(workspaceID)
		if err := s.limiter.AllowWithLimits(ctx, userEmail, pl.HourlyRateLimit, pl.DailyRateLimit); err != nil {
			return nil, fmt.Errorf("rate_limit: %w", err)
		}
	} else {
		if err := s.limiter.Allow(ctx, userEmail); err != nil {
			return nil, fmt.Errorf("rate_limit: %w", err)
		}
	}

	// Validate attachments if present
	if len(req.Attachments) > 0 {
		maxSize := DefaultMaxAttachmentSize
		if s.planLimits != nil {
			pl := s.planLimits.EffectiveLimits(workspaceID)
			if pl.MaxAttachmentSizeMB > 0 {
				maxSize = int64(pl.MaxAttachmentSizeMB) * 1024 * 1024
			}
		} else if s.settings != nil {
			maxSize = int64(s.settings.MaxAttachmentSizeMB()) * 1024 * 1024
		}
		if err := ValidateAttachments(req.Attachments, maxSize, DefaultMaxTotalSize); err != nil {
			return nil, fmt.Errorf("attachment validation: %w", err)
		}
	}

	// Enforce verified-domain sending. Strict mode is scoped to the active
	// workspace (or the user in legacy personal mode).
	if err := s.checkDomainVerification(userID, workspaceID, req.From); err != nil {
		return nil, err
	}

	// Resolve + validate the unsubscribe config (Posta-managed list, or caller-managed
	// url/mailto). nil ⇒ no headers, no suppression — the transactional default.
	scope := repositories.ResourceScope{UserID: userID, WorkspaceID: workspaceID}
	unsub, err := s.resolveUnsubscribe(scope, req)
	if err != nil {
		return nil, err
	}
	var unsubListID *uint
	if unsub != nil {
		unsubListID = unsub.listID
	}

	// Filter out suppressed recipients (list-scoped when a list is named)
	if s.suppressionRepo != nil {
		filtered, err := s.suppressionRepo.FilterSuppressedForList(scope, req.To, unsubListID)
		if err != nil {
			return nil, fmt.Errorf("failed to check suppression list: %w", err)
		}
		if len(filtered) == 0 {
			// Log the suppressed email attempt
			var apiKeyPtr *uint
			if apiKeyID > 0 {
				apiKeyPtr = &apiKeyID
			}
			em := &models.Email{
				UserID:       userID,
				WorkspaceID:  workspaceID,
				APIKeyID:     apiKeyPtr,
				Sender:       req.From,
				Recipients:   req.To,
				Subject:      req.Subject,
				TemplateName: req.TemplateName,
				HTMLBody:     req.HTML,
				TextBody:     req.Text,
				Status:       models.EmailStatusSuppressed,
				ErrorMessage: "all recipients are suppressed",
				Provider:     ClassifyRecipients(req.To),
			}
			_ = s.emailRepo.Create(em)
			return &SendResponse{ID: em.UUID, Status: em.Status}, nil
		}
		// Log suppressed recipients individually if some were filtered out
		if len(filtered) < len(req.To) {
			suppressed := diffRecipients(req.To, filtered)
			var apiKeyPtr *uint
			if apiKeyID > 0 {
				apiKeyPtr = &apiKeyID
			}
			for _, addr := range suppressed {
				em := &models.Email{
					UserID:       userID,
					WorkspaceID:  workspaceID,
					APIKeyID:     apiKeyPtr,
					Sender:       req.From,
					Recipients:   []string{addr},
					Subject:      req.Subject,
					TemplateName: req.TemplateName,
					Status:       models.EmailStatusSuppressed,
					ErrorMessage: errRecipientSuppressed,
					Provider:     ClassifyProvider(addr),
				}
				_ = s.emailRepo.Create(em)
			}
		}
		req.To = filtered
	}

	// Persist attachments. The blob key prefix is a temporary stand-in for the
	// email UUID, which is not assigned until the record is created.
	attachmentsJSON, err := s.persistAttachments(ctx, fmt.Sprintf("%d", time.Now().UnixNano()), req.Attachments)
	if err != nil {
		return nil, err
	}

	// Auto-generate plain text from HTML when text body is not provided
	if req.Text == "" && req.HTML != "" {
		req.Text = HTMLToText(req.HTML)
	}

	// Resolve the headers once; the async worker reads them back from the row
	// and the sync path sends them directly, so both paths agree.
	customEnabled := s.settings != nil && s.settings.CustomHeadersEnabled()
	req.Headers = outboundHeaders(req, customEnabled)
	var headersJSON string
	if len(req.Headers) > 0 {
		b, _ := json.Marshal(req.Headers)
		headersJSON = string(b)
	}

	var apiKeyPtr *uint
	if apiKeyID > 0 {
		apiKeyPtr = &apiKeyID
	}

	// Posta-managed (list) URL is minted in the post-Create block (it needs em.ID),
	// which also sets ListUnsubscribePost for that path.
	var luURL, luMailto string
	var luPost bool
	if unsub != nil {
		luMailto = unsub.mailto
		if unsub.url != "" {
			luURL = unsub.url
			luPost = unsub.oneClick
		}
	}

	em := &models.Email{
		UUID:                  uuid.NewString(),
		UserID:                userID,
		WorkspaceID:           workspaceID,
		APIKeyID:              apiKeyPtr,
		Sender:                req.From,
		Recipients:            req.To,
		Subject:               req.Subject,
		TemplateName:          req.TemplateName,
		HTMLBody:              req.HTML,
		TextBody:              req.Text,
		ExternalID:            req.ExternalID,
		AttachmentsJSON:       attachmentsJSON,
		HeadersJSON:           headersJSON,
		TemplateData:          marshalTemplateData(req.TemplateData),
		ListUnsubscribeURL:    luURL,
		ListUnsubscribeMailto: luMailto,
		ListUnsubscribePost:   luPost,
		UnsubscribeListID:     unsubListID,
		Status:                models.EmailStatusPending,
		Provider:              ClassifyRecipients(req.To),
		// SF-09: the RFC 2852 DELIVERBY provenance rides the row so the expiry
		// events can distinguish a protocol-negotiated deadline from a TTL.
		DeliverBySeconds: req.DeliverBySeconds,
		// SF-02: the RFC 8689 REQUIRETLS requirement rides the row so every
		// delivery path can enforce TLS-only hops for this message.
		RequireTLS: req.RequireTLS,
	}

	if err := s.emailRepo.Create(em); err != nil {
		return nil, fmt.Errorf("failed to store email: %w", err)
	}

	needsLinks := HasSystemSentinels(em.HTMLBody) || HasSystemSentinels(em.TextBody) || HasSystemSentinels(em.Subject)
	if s.linkGen != nil && (needsLinks || em.UnsubscribeListID != nil) {
		webViewURL := s.linkGen.WebViewURL(em.UUID)
		unsubscribeURL := s.linkGen.TxUnsubscribeURL(em.ID)
		if needsLinks {
			em.HTMLBody = SubstituteSystemLinks(em.HTMLBody, webViewURL, unsubscribeURL)
			em.TextBody = SubstituteSystemLinks(em.TextBody, webViewURL, unsubscribeURL)
			em.Subject = SubstituteSystemLinks(em.Subject, webViewURL, unsubscribeURL)

			req.HTML = em.HTMLBody
			req.Text = em.TextBody
			req.Subject = em.Subject
		}
		// Posta-managed list: emit the RFC 8058 one-click List-Unsubscribe header.
		if em.UnsubscribeListID != nil {
			em.ListUnsubscribeURL = unsubscribeURL
			em.ListUnsubscribePost = true
		}
		_ = s.emailRepo.Update(em)
	}

	// Open/click tracking applies to every email, transactional and campaign
	// alike, once the final content is known (after system-link substitution,
	// before the enqueue/send decision). Campaign mail was already tracked at
	// batch time; ContainsTracking keeps this idempotent. The sync path sends
	// from req, so the rewritten content is mirrored there.
	s.applyTracking(em)
	req.HTML = em.HTMLBody
	req.Text = em.TextBody

	if s.devMode {
		em.Status = models.EmailStatusSent
		now := time.Now()
		em.SentAt = &now
		_ = s.emailRepo.Update(em)
		emailhistory.RecordSent(s.recorder, em, map[string]any{"dev_mode": true})
		if s.onSent != nil {
			s.onSent()
		}
		logger.Info("dev mode: email stored but not sent", "id", em.UUID)
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	// SF-08: prepare the queue schedule state before the message enters the
	// queue — per-recipient retry/notify schedule rows and the TTL deadline.
	// Best-effort: a planning failure is logged and never fails the send (the
	// queue keeps its legacy behavior for that message).
	if s.queuePlanner != nil {
		if err := s.queuePlanner.EnsureForEmail(em, scheduleRequestFor(req)); err != nil {
			logger.Warn("queue schedule planning failed; continuing without schedule state", "id", em.UUID, "error", err)
		}
	}

	// Scheduled sending
	if req.SendAt != nil && req.SendAt.After(time.Now()) && s.enqueuer != nil {
		em.Status = models.EmailStatusScheduled
		em.ScheduledAt = req.SendAt
		_ = s.emailRepo.Update(em)
		if err := s.enqueuer.EnqueueEmailSendAt(em.ID, "", *req.SendAt); err != nil {
			em.Status = models.EmailStatusFailed
			em.ErrorMessage = fmt.Sprintf("failed to schedule: %v", err)
			_ = s.emailRepo.Update(em)
			emailhistory.RecordFailed(s.recorder, em, em.ErrorMessage, nil)
			return nil, fmt.Errorf("failed to schedule email: %w", err)
		}
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	// Asynchronous path: enqueue the email for background delivery
	if s.enqueuer != nil {
		em.Status = models.EmailStatusQueued
		_ = s.emailRepo.Update(em)
		if err := s.enqueuer.EnqueueEmailSend(em.ID, ""); err != nil {
			em.Status = models.EmailStatusFailed
			em.ErrorMessage = fmt.Sprintf("failed to enqueue: %v", err)
			_ = s.emailRepo.Update(em)
			emailhistory.RecordFailed(s.recorder, em, em.ErrorMessage, nil)
			if s.onFailed != nil {
				s.onFailed()
			}
			return nil, fmt.Errorf("failed to enqueue email: %w", err)
		}
		if s.onQueued != nil {
			s.onQueued()
		}
		logger.Debug("email enqueued for background delivery", "id", em.UUID)
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	// CA-06 dev fallback broker: no queue producer is wired (dev mode never
	// creates one — cmd/posta/server.go gates worker.NewProducer on
	// !POSTA_DEV_MODE), so sends degrade to synchronous delivery through the
	// same pipeline (sendSync: the same SMTP sender, suppression-filtered
	// content, webhook events, and message-history records the worker path
	// produces). For brand-new sends the dev short-circuit above returns
	// first (store-without-send); the sync pipeline is what the retry path
	// and any enqueuer-less flow use. Dev-only by construction: a production
	// deployment always wires a producer. A production enqueue failure
	// deliberately does NOT degrade to sync — silently failing over would
	// mask a broken broker — the email is marked failed and the
	// queue-reconcile cron owns recovery.
	return s.sendSync(em, userID, workspaceID, req)
}

// SystemForwardRequest describes one inbound-forwarding re-send (OB-2,
// ADR-002 decision 2). The inbound forwarding executor is the only caller.
type SystemForwardRequest struct {
	UserID      uint
	WorkspaceID *uint
	// From is the receiving address on the tenant-verified domain the copy is
	// sent from — DKIM signing resolves its domain's keys at delivery time
	// (ADR-001 path). Never a client-supplied authority: the executor derives
	// it from the durable inbound record.
	From string
	To   []string
	// Subject/Text/HTML carry the copy's content. Headers are system headers
	// (Message-ID threading, X-Posta-Forwarded-For loop evidence) — never
	// accepted from external input.
	Subject     string
	Text        string
	HTML        string
	Headers     map[string]string
	Attachments []models.Attachment
	// Metadata lands on the copy's "forwarded" email_events row: the inbound
	// message's identity, the route that matched and the hop count.
	Metadata map[string]any
}

// SendSystemForward enqueues a system_forward copy of a durably stored inbound
// message through the standard outbound pipeline (OB-2, ADR-002 decision 2):
// the row is created with models.EmailProvenanceSystemForward, the pipeline's
// send worker signs it per ADR-001/domain_signing_keys at delivery time and
// retries with the standard backoff, and a "forwarded" email_events row marks
// the forwarding provenance on the copy's timeline.
//
// Deliberately narrower than Send: forwarding is NOT a marketing-send path
// (ADR-002 non-negotiable), so this path never consults or mutates
// suppression, unsubscribe, or consent layers, and never consumes the
// tenant's marketing rate limit — the forwarding executor applies its own
// per-target abuse caps. Verified-domain sending is structural here: the
// caller hands over the verified domain's receiving address.
func (s *Service) SendSystemForward(ctx context.Context, req *SystemForwardRequest) (*SendResponse, error) {
	if req == nil || len(req.To) == 0 {
		return nil, fmt.Errorf("system forward: no target recipient")
	}
	if err := validateAddresses(req.From, req.To); err != nil {
		return nil, err
	}
	// R-24: CR/LF/NUL never reach the message builder, on the system path too.
	if err := validateSendHeaders(req.Subject, req.Headers); err != nil {
		return nil, fmt.Errorf("header validation: %w", err)
	}
	if s.settings != nil && s.settings.MaintenanceMode() {
		return nil, fmt.Errorf("maintenance: email sending is temporarily disabled")
	}

	// System headers always apply (outboundHeaders): custom headers are not
	// involved on this path.
	headers := outboundHeaders(&SendRequest{SystemHeaders: req.Headers}, false)
	headersJSON := ""
	if len(headers) > 0 {
		b, _ := json.Marshal(headers)
		headersJSON = string(b)
	}

	attachmentsJSON, err := s.persistAttachments(ctx, fmt.Sprintf("%d", time.Now().UnixNano()), req.Attachments)
	if err != nil {
		return nil, err
	}
	if req.Text == "" && req.HTML != "" {
		req.Text = HTMLToText(req.HTML)
	}

	em := &models.Email{
		UUID:            uuid.NewString(),
		UserID:          req.UserID,
		WorkspaceID:     req.WorkspaceID,
		Sender:          req.From,
		Recipients:      req.To,
		Subject:         req.Subject,
		HTMLBody:        req.HTML,
		TextBody:        req.Text,
		AttachmentsJSON: attachmentsJSON,
		HeadersJSON:     headersJSON,
		// OB-2 provenance stamp: the re-send copy a forwarding executor
		// enqueued — the mark operators and queries distinguish forwarded
		// mail from tenant/API-originated sends.
		Provenance: models.EmailProvenanceSystemForward,
		Status:     models.EmailStatusPending,
		Provider:   ClassifyRecipients(req.To),
	}
	if err := s.emailRepo.Create(em); err != nil {
		return nil, fmt.Errorf("failed to store email: %w", err)
	}

	// ADR-002 decision 2: the forwarding is recorded on the copy's
	// message-history timeline. Recording failures never fail the enqueue.
	emailhistory.RecordForwarded(s.recorder, em, req.Metadata)

	// CA-06 dev fallback: without a wired producer the copy is stored and
	// marked sent without delivery, exactly like the API send path's
	// brand-new-send dev short-circuit (store-without-send).
	if s.devMode {
		em.Status = models.EmailStatusSent
		now := time.Now()
		em.SentAt = &now
		_ = s.emailRepo.Update(em)
		logger.Info("dev mode: system forward stored but not sent", "id", em.UUID)
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	if s.enqueuer != nil {
		em.Status = models.EmailStatusQueued
		_ = s.emailRepo.Update(em)
		if err := s.enqueuer.EnqueueEmailSend(em.ID, ""); err != nil {
			em.Status = models.EmailStatusFailed
			em.ErrorMessage = fmt.Sprintf("failed to enqueue: %v", err)
			_ = s.emailRepo.Update(em)
			emailhistory.RecordFailed(s.recorder, em, em.ErrorMessage, nil)
			return nil, fmt.Errorf("failed to enqueue system forward: %w", err)
		}
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	// No producer wired (enqueuer-less flow): synchronous delivery through the
	// same pipeline Send's fallback uses.
	return s.sendSync(em, req.UserID, req.WorkspaceID, &SendRequest{
		From:        req.From,
		To:          req.To,
		Subject:     req.Subject,
		HTML:        req.HTML,
		Text:        req.Text,
		Attachments: req.Attachments,
		Headers:     headers,
	})
}

// applyTracking rewrites the HTML body of a not-yet-tracked email with signed
// open/click tracking URLs keyed on the email UUID, and persists the row. It
// is a no-op when tracking is not configured or disabled, when the body is
// empty, or when the content was already tracked (campaign mail is tracked at
// batch time). A tracking failure never blocks the send.
func (s *Service) applyTracking(em *models.Email) {
	if s.trackingSvc == nil || em == nil || em.HTMLBody == "" {
		return
	}
	if s.trackingEnabled != nil && !s.trackingEnabled() {
		return
	}
	if tracking.ContainsTracking(em.HTMLBody) {
		return
	}
	tracked := s.trackingSvc.ProcessHTMLEmail(em.HTMLBody, em.UUID)
	if tracked == "" || tracked == em.HTMLBody {
		return
	}
	em.HTMLBody = tracked
	_ = s.emailRepo.Update(em)
}

// marshalTemplateData renders the template render data as the JSON string the
// jsonb column expects. An empty map stays empty so the column default
// applies; a marshal failure degrades to the structural default.
func marshalTemplateData(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	b, err := json.Marshal(data)
	if err != nil {
		logger.Warn("failed to marshal template data", "error", err)
		return "{}"
	}
	return string(b)
}

// sendSync performs synchronous SMTP delivery — the CA-06 dev fallback
// broker's pipeline (no enqueuer configured) and the retry path's fallback.
func (s *Service) sendSync(em *models.Email, userID uint, workspaceID *uint, req *SendRequest) (*SendResponse, error) {
	var smtpServer *models.SMTPServer
	var err error
	if workspaceID != nil {
		smtpServer, err = s.smtpRepo.FindFirstByWorkspaceID(*workspaceID)
	} else {
		smtpServer, err = s.smtpRepo.FindFirstByUserID(userID)
	}
	if err != nil {
		em.Status = models.EmailStatusFailed
		em.ErrorMessage = "no SMTP server configured"
		_ = s.emailRepo.Update(em)
		emailhistory.RecordFailed(s.recorder, em, em.ErrorMessage, nil)
		s.dispatcher.Dispatch(userID, workspaceID, "email.failed", em.UUID, req.From)
		if s.onFailed != nil {
			s.onFailed()
		}
		return nil, fmt.Errorf("no SMTP server configured")
	}

	if len(smtpServer.AllowedEmails) > 0 {
		bareFrom := extractEmail(req.From)
		allowed := false
		for _, e := range smtpServer.AllowedEmails {
			if e == bareFrom {
				allowed = true
				break
			}
		}
		if !allowed {
			em.Status = models.EmailStatusFailed
			em.ErrorMessage = fmt.Sprintf("sender %q is not in the allowed emails list", req.From)
			_ = s.emailRepo.Update(em)
			emailhistory.RecordFailed(s.recorder, em, em.ErrorMessage, nil)
			s.dispatcher.Dispatch(userID, workspaceID, "email.failed", em.UUID, req.From)
			if s.onFailed != nil {
				s.onFailed()
			}
			return nil, fmt.Errorf("sender %q is not in the allowed emails list", req.From)
		}
	}

	em.SMTPHostname = smtpServer.Host
	_ = s.emailRepo.Update(em)

	if err := s.sender.Send(smtpServer, req.From, req.To, req.Subject, req.HTML, req.Text, req.Attachments, req.Headers, em.ListUnsubscribeURL, em.ListUnsubscribeMailto, em.ListUnsubscribePost); err != nil {
		em.Status = models.EmailStatusFailed
		em.ErrorMessage = err.Error()
		_ = s.emailRepo.Update(em)
		emailhistory.RecordFailed(s.recorder, em, em.ErrorMessage, map[string]any{"smtp_hostname": smtpServer.Host})
		s.dispatcher.Dispatch(userID, workspaceID, "email.failed", em.UUID, req.From)
		if s.onFailed != nil {
			s.onFailed()
		}
		if s.contactRepo != nil {
			go s.contactRepo.RecordFailed(userID, workspaceID, req.To)
		}
		return nil, fmt.Errorf("failed to send email: %w", err)
	}

	now := time.Now()
	em.Status = models.EmailStatusSent
	em.SentAt = &now
	_ = s.emailRepo.Update(em)
	emailhistory.RecordSent(s.recorder, em, map[string]any{"smtp_hostname": smtpServer.Host})
	s.dispatcher.Dispatch(userID, workspaceID, "email.sent", em.UUID, req.From)
	if s.onSent != nil {
		s.onSent()
	}
	if s.contactRepo != nil {
		go s.contactRepo.RecordSent(userID, workspaceID, req.To)
	}

	return &SendResponse{ID: em.UUID, Status: em.Status}, nil
}

func (s *Service) SendWithTemplate(ctx context.Context, userID, apiKeyID uint, workspaceID *uint, userEmail string, req *SendTemplateRequest) (*SendResponse, error) {
	tmpl, err := s.findTemplate(userID, workspaceID, req.TemplateID, req.Template)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	rendered, err := s.resolveAndRender(tmpl, req.Language, req.TemplateData)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	from := req.From
	if from == "" {
		from = defaultFromAddress
	}

	return s.Send(ctx, userID, apiKeyID, workspaceID, userEmail, &SendRequest{
		From:         from,
		To:           req.To,
		Subject:      rendered.Subject,
		HTML:         rendered.HTML,
		Text:         rendered.Text,
		Attachments:  req.Attachments,
		TemplateName: tmpl.Name,
		TemplateData: req.TemplateData,
		Unsubscribe:  req.Unsubscribe,
	})
}

func (s *Service) SendBatch(ctx context.Context, userID, apiKeyID uint, workspaceID *uint, userEmail string, req *BatchRequest) (*BatchResponse, error) {
	maxBatch := 0
	if s.planLimits != nil {
		maxBatch = s.planLimits.EffectiveLimits(workspaceID).MaxBatchSize
	} else if s.settings != nil {
		maxBatch = s.settings.MaxBatchSize()
	}
	if maxBatch > 0 && len(req.Recipients) > maxBatch {
		return nil, fmt.Errorf("batch size %d exceeds maximum allowed (%d)", len(req.Recipients), maxBatch)
	}

	from := req.From
	if from == "" {
		from = defaultFromAddress
	}

	// Validate sender address
	if err := validateEmailAddress(from); err != nil {
		return nil, fmt.Errorf("invalid sender: %w", err)
	}
	// Validate all recipient addresses
	for _, r := range req.Recipients {
		if err := validateEmailAddress(r.Email); err != nil {
			return nil, fmt.Errorf("invalid recipient: %w", err)
		}
	}

	tmpl, err := s.findTemplate(userID, workspaceID, req.TemplateID, req.Template)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	// Resolve + validate the batch's unsubscribe config once upfront so a bad
	// list_id fails the batch immediately instead of replicating the error per row.
	scope := repositories.ResourceScope{UserID: userID, WorkspaceID: workspaceID}
	unsub, err := s.resolveUnsubscribe(scope, &SendRequest{Unsubscribe: req.Unsubscribe})
	if err != nil {
		return nil, err
	}
	var unsubListID *uint
	if unsub != nil {
		unsubListID = unsub.listID
	}

	resp := &BatchResponse{
		Total:   len(req.Recipients),
		Results: make([]BatchResult, 0, len(req.Recipients)),
	}

	for _, recipient := range req.Recipients {
		// Check suppression (list-scoped when a list is named)
		if s.suppressionRepo != nil {
			suppressed, err := s.suppressionRepo.IsSuppressedForList(scope, recipient.Email, unsubListID)
			if err == nil && suppressed {
				// Log the suppressed email
				var apiKeyPtr *uint
				if apiKeyID > 0 {
					apiKeyPtr = &apiKeyID
				}
				em := &models.Email{
					UserID:       userID,
					WorkspaceID:  workspaceID,
					APIKeyID:     apiKeyPtr,
					Sender:       from,
					Recipients:   []string{recipient.Email},
					Subject:      req.Template,
					TemplateName: tmpl.Name,
					Status:       models.EmailStatusSuppressed,
					ErrorMessage: errRecipientSuppressed,
					Provider:     ClassifyProvider(recipient.Email),
				}
				_ = s.emailRepo.Create(em)

				resp.Skipped++
				resp.Results = append(resp.Results, BatchResult{
					Email:  recipient.Email,
					ID:     em.UUID,
					Status: models.EmailStatusSuppressed,
					Error:  errRecipientSuppressed,
				})
				continue
			}
		}

		// Use per-recipient language, fall back to batch-level language
		lang := recipient.Language
		if lang == "" {
			lang = req.Language
		}

		rendered, err := s.resolveAndRender(tmpl, lang, recipient.TemplateData)
		if err != nil {
			resp.Failed++
			resp.Results = append(resp.Results, BatchResult{
				Email:  recipient.Email,
				Status: models.EmailStatusFailed,
				Error:  fmt.Sprintf("template render failed: %v", err),
			})
			continue
		}

		sendResp, err := s.Send(ctx, userID, apiKeyID, workspaceID, userEmail, &SendRequest{
			From:         from,
			To:           []string{recipient.Email},
			Subject:      rendered.Subject,
			HTML:         rendered.HTML,
			Text:         rendered.Text,
			TemplateName: tmpl.Name,
			TemplateData: recipient.TemplateData,
			Unsubscribe:  req.Unsubscribe,
		})
		if err != nil {
			resp.Failed++
			resp.Results = append(resp.Results, BatchResult{
				Email:  recipient.Email,
				Status: models.EmailStatusFailed,
				Error:  err.Error(),
			})
			continue
		}

		resp.Sent++
		resp.Results = append(resp.Results, BatchResult{
			Email:  recipient.Email,
			ID:     sendResp.ID,
			Status: sendResp.Status,
		})
	}

	return resp, nil
}

// SendTestByTemplateID sends a test email using a template looked up by ID.
// It uses apiKeyID=0 since test sends come from the dashboard, not an API key.
func (s *Service) SendTestByTemplateID(ctx context.Context, userID uint, workspaceID *uint, userEmail string, templateID uint, req *SendTestRequest) (*SendResponse, error) {
	tmpl, err := s.templateRepo.FindByID(templateID)
	if err != nil {
		return nil, fmt.Errorf("template not found")
	}

	rendered, err := s.resolveAndRender(tmpl, req.Language, req.TemplateData)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	from := req.From
	if from == "" {
		from = defaultFromAddress
	}

	return s.Send(ctx, userID, 0, workspaceID, userEmail, &SendRequest{
		From:         from,
		To:           req.To,
		Subject:      rendered.Subject,
		HTML:         rendered.HTML,
		Text:         rendered.Text,
		TemplateName: tmpl.Name,
		TemplateData: req.TemplateData,
	})
}

type SendTestRequest struct {
	To           []string       `json:"to" required:"true" minItems:"1" doc:"Recipient addresses. Each accepts a plain address or RFC 5322 display-name format (e.g. Jonas <jonas@example.com>)."`
	From         string         `json:"from" doc:"Sender address. Accepts a plain address (hello@example.com) or RFC 5322 display-name format (Acme <hello@example.com>)."`
	Language     string         `json:"language" doc:"Language code for template localization (BCP 47 / ISO 639-1, e.g. en, fr, fr-CA). Falls back to base language, the template default, then en."`
	TemplateData map[string]any `json:"template_data" doc:"Variables substituted into the template's {{ var_name }} placeholders. Reserved posta_* variables (e.g. posta_unsubscribe_url, posta_web_view_url) are injected automatically."`
}

// defaultRetryLimit is the fallback maximum number of manual retries when
// no SMTP server is configured or the server's MaxRetries is zero.
const defaultRetryLimit = 5

// RetryEmail re-enqueues a failed email for another delivery attempt.
// Only emails with status "failed" can be retried, and the retry count
// must not exceed the SMTP server's MaxRetries (or the default limit).
func (s *Service) RetryEmail(emailUUID string, userID uint, workspaceID *uint) (*SendResponse, error) {
	em, err := s.emailRepo.FindByUUID(emailUUID)
	if err != nil {
		return nil, fmt.Errorf("email not found")
	}
	if em.UserID != userID {
		return nil, fmt.Errorf("email not found")
	}
	// When called from a workspace-scoped context, verify the email belongs
	// to that workspace.
	if workspaceID != nil {
		if em.WorkspaceID == nil || *em.WorkspaceID != *workspaceID {
			return nil, fmt.Errorf("email not found")
		}
	}
	if em.Status != models.EmailStatusFailed {
		return nil, fmt.Errorf("only failed emails can be retried")
	}

	// Enforce retry limit from the SMTP server configuration.
	maxRetries := defaultRetryLimit
	if s.smtpRepo != nil {
		var smtpServer *models.SMTPServer
		if em.WorkspaceID != nil {
			smtpServer, _ = s.smtpRepo.FindFirstByWorkspaceID(*em.WorkspaceID)
		} else {
			smtpServer, _ = s.smtpRepo.FindFirstByUserID(em.UserID)
		}
		if smtpServer != nil && smtpServer.MaxRetries > 0 {
			maxRetries = smtpServer.MaxRetries
		}
	}
	if em.RetryCount >= maxRetries {
		return nil, fmt.Errorf("retry limit reached (%d/%d)", em.RetryCount, maxRetries)
	}

	em.RetryCount++

	// Claim the retry atomically (R-26): the failed→queued flip, the retry
	// counter bump, and the error clear happen in one UPDATE, so a user retry
	// and the 5-minute retry poller (or two user retries) can never both claim
	// this email and enqueue two tasks. The loser gets the conflict error.
	claimed, err := s.emailRepo.ClaimForRetry(em.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to claim email for retry: %w", err)
	}
	if !claimed {
		return nil, fmt.Errorf("only failed emails can be retried")
	}
	em.Status = models.EmailStatusQueued
	em.ErrorMessage = ""

	if s.enqueuer != nil {
		if err := s.enqueuer.EnqueueEmailSend(em.ID, ""); err != nil {
			em.Status = models.EmailStatusFailed
			em.ErrorMessage = fmt.Sprintf("failed to enqueue: %v", err)
			_ = s.emailRepo.Update(em)
			return nil, fmt.Errorf("failed to enqueue email: %w", err)
		}
		return &SendResponse{ID: em.UUID, Status: em.Status}, nil
	}

	// Synchronous fallback (no background worker configured). The row is
	// already claimed (queued), so no concurrent retry can re-enter; sendSync
	// owns the failed/sent transition from here. Reset the in-memory status to
	// the pre-claim state so its failure writes stay coherent.
	em.Status = models.EmailStatusFailed
	return s.sendSync(em, userID, em.WorkspaceID, &SendRequest{
		From:                em.Sender,
		To:                  em.Recipients,
		Subject:             em.Subject,
		HTML:                em.HTMLBody,
		Text:                em.TextBody,
		ListUnsubscribeURL:  em.ListUnsubscribeURL,
		ListUnsubscribePost: em.ListUnsubscribePost,
	})
}

// RenderTemplate resolves a template by ID or name and renders it with the given data,
// returning the rendered subject, HTML, and text without sending.
func (s *Service) RenderTemplate(userID uint, workspaceID *uint, templateID *uint, templateName, language string, data map[string]any) (*RenderedTemplate, error) {
	tmpl, err := s.findTemplate(userID, workspaceID, templateID, templateName)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}
	rendered, err := s.resolveAndRender(tmpl, language, data)
	if err != nil {
		return nil, err
	}
	// This is a preview (editor / dry-run): there is no real message yet, so show
	// the reserved {{ posta_* }} variables by name rather than leaking the internal
	// sentinels or generating throwaway links.
	rendered.HTML = SubstituteSystemLinks(rendered.HTML, VarWebView, VarUnsubscribe)
	rendered.Text = SubstituteSystemLinks(rendered.Text, VarWebView, VarUnsubscribe)
	rendered.Subject = SubstituteSystemLinks(rendered.Subject, VarWebView, VarUnsubscribe)
	return rendered, nil
}

// findTemplate looks up a template by ID or name. When templateID is provided
// it performs a fast primary-key lookup and verifies the caller owns the
// resource. Otherwise it falls back to a name-based search (workspace first,
// then personal).
func (s *Service) findTemplate(userID uint, workspaceID *uint, templateID *uint, name string) (*models.Template, error) {
	if templateID != nil && *templateID != 0 {
		tmpl, err := s.templateRepo.FindByID(*templateID)
		if err != nil {
			return nil, err
		}
		// Verify ownership: must belong to the same user/workspace.
		if workspaceID != nil {
			if tmpl.WorkspaceID == nil || *tmpl.WorkspaceID != *workspaceID {
				return nil, fmt.Errorf("template not found")
			}
		} else if tmpl.UserID != userID || tmpl.WorkspaceID != nil {
			return nil, fmt.Errorf("template not found")
		}
		return tmpl, nil
	}

	if name == "" {
		return nil, fmt.Errorf("template_id or template name is required")
	}

	if workspaceID == nil {
		return nil, fmt.Errorf("template %q not found: no active workspace", name)
	}
	tmpl, err := s.templateRepo.FindByWorkspaceName(*workspaceID, name)
	if err != nil {
		return nil, fmt.Errorf("template %q not found", name)
	}
	return tmpl, nil
}

// resolveAndRender resolves the template content using versioned localizations
// and renders it with the given data.
func (s *Service) resolveAndRender(tmpl *models.Template, language string, data map[string]any) (*RenderedTemplate, error) {
	if tmpl.ActiveVersionID == nil {
		return nil, fmt.Errorf("template %q has no active version", tmpl.Name)
	}

	v, err := s.versionRepo.FindByID(*tmpl.ActiveVersionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load active version: %w", err)
	}

	l := s.resolveLocalization(v.ID, language, tmpl.DefaultLanguage)
	if l == nil {
		return nil, fmt.Errorf("no localization found for template %q", tmpl.Name)
	}

	var css string
	if v.StyleSheet != nil {
		css = v.StyleSheet.CSS
	}
	input := &RenderInput{
		SubjectTemplate: l.SubjectTemplate,
		HTMLTemplate:    l.HTMLTemplate,
		TextTemplate:    l.TextTemplate,
		CSS:             css,
	}
	// Inject reserved {{ posta_* }} variables as sentinels; the real per-message
	// URLs are substituted later in Send once the email identity is known.
	return s.renderer.Render(input, WithSystemVars(data))
}

// resolveLocalization implements the language fallback strategy:
// requested language → base language (e.g. fr-CA → fr) → template default → "en"
func (s *Service) resolveLocalization(versionID uint, language, defaultLang string) *models.TemplateLocalization {
	if language == "" {
		language = defaultLang
	}
	if language == "" {
		language = "en"
	}

	// Try exact match
	l, err := s.localizationRepo.FindByVersionAndLanguage(versionID, language)
	if err == nil {
		return l
	}

	// Try base language (e.g. fr-CA → fr)
	if idx := strings.Index(language, "-"); idx > 0 {
		base := language[:idx]
		l, err = s.localizationRepo.FindByVersionAndLanguage(versionID, base)
		if err == nil {
			return l
		}
	}

	// Try template default language
	if language != defaultLang && defaultLang != "" {
		l, err = s.localizationRepo.FindByVersionAndLanguage(versionID, defaultLang)
		if err == nil {
			return l
		}
	}

	// Try "en" as final fallback
	if language != "en" && defaultLang != "en" {
		l, err = s.localizationRepo.FindByVersionAndLanguage(versionID, "en")
		if err == nil {
			return l
		}
	}

	return nil
}

// diffRecipients returns elements in original that are not in filtered.
func diffRecipients(original, filtered []string) []string {
	set := make(map[string]struct{}, len(filtered))
	for _, e := range filtered {
		set[e] = struct{}{}
	}
	var diff []string
	for _, e := range original {
		if _, ok := set[e]; !ok {
			diff = append(diff, e)
		}
	}
	return diff
}

// outboundHeaders returns the headers a message is sent with: the caller's
// custom headers, filtered, when the platform allows them, plus the system
// headers Posta set itself, which always apply and win on conflict.
func outboundHeaders(req *SendRequest, customEnabled bool) map[string]string {
	out := map[string]string{}
	if customEnabled {
		for k, v := range filterCustomHeaders(req.Headers) {
			out[k] = v
		}
	}
	for k, v := range req.SystemHeaders {
		for existing := range out {
			if strings.EqualFold(existing, k) {
				delete(out, existing)
			}
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// containsHeaderInjection reports whether s carries a character that would let
// it break out of a single header line: CR, LF, or NUL (R-24).
func containsHeaderInjection(s string) bool {
	return strings.ContainsAny(s, "\r\n\x00")
}

// validateHeaderContent rejects a header-bound string that contains CR, LF, or
// NUL. The value is rejected rather than stripped so a caller learns its
// payload is malformed instead of silently receiving different mail.
func validateHeaderContent(field, value string) error {
	if containsHeaderInjection(value) {
		return fmt.Errorf("%s contains forbidden control characters (CR/LF/NUL)", field)
	}
	return nil
}

// validateSendHeaders validates the header-bound parts of a send request
// (R-24): the Subject and every custom header name and value. A CR or LF in
// any of them would inject arbitrary headers into the outbound message (e.g. a
// smuggled Bcc); From/To are already covered by mail.ParseAddress.
func validateSendHeaders(subject string, headers map[string]string) error {
	if err := validateHeaderContent("subject", subject); err != nil {
		return err
	}
	for name, value := range headers {
		if err := validateHeaderContent("header name", name); err != nil {
			return err
		}
		if err := validateHeaderContent("header value", value); err != nil {
			return err
		}
	}
	return nil
}

// filterCustomHeaders removes headers that could conflict with system-set
// headers or be used for spoofing. It returns a sanitized copy.
//
// Date and Message-ID are deliberately allowed: buildMessage emits them only
// when the caller has not supplied its own, so an integration that needs a
// stable Message-ID (e.g. for threading against an external system) can set
// one while every other message still gets a generated RFC 5322 pair.
//
// Entries whose name or value carries CR/LF/NUL are dropped (R-24): the API
// paths reject them up front (validateSendHeaders), and this guard covers
// internal callers that reach the header filter without that validation.
func filterCustomHeaders(headers map[string]string) map[string]string {
	// Headers that are set by the system and must not be overridden.
	reserved := map[string]bool{
		"from":         true,
		"to":           true,
		"subject":      true,
		"mime-version": true,
		"content-type": true,
	}

	// Prefixes that must not appear in custom headers.
	blockedPrefixes := []string{
		"x-posta-",
		"dkim-",
		"arc-",
	}

	filtered := make(map[string]string, len(headers))
	for key, value := range headers {
		lower := strings.ToLower(key)
		if reserved[lower] {
			continue
		}
		blocked := false
		for _, prefix := range blockedPrefixes {
			if strings.HasPrefix(lower, prefix) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		if containsHeaderInjection(key) || containsHeaderInjection(value) {
			continue
		}
		filtered[key] = value
	}
	return filtered
}

func (s *Service) applyDefaultSenderName(workspaceID *uint, from string) string {
	if workspaceID == nil || s.wsSettingRepo == nil {
		return from
	}
	ws, err := s.wsSettingRepo.FindByWorkspaceID(*workspaceID)
	if err != nil || ws == nil {
		return from
	}
	return resolveSenderWithDefault(from, ws.DefaultSenderName)
}

func resolveSenderWithDefault(from, defaultName string) string {
	addr, err := mail.ParseAddress(from)
	if err != nil || addr.Name != "" {
		return from
	}
	defaultName = strings.TrimSpace(defaultName)
	if defaultName == "" {
		return from
	}
	if !isASCII(defaultName) {
		return fmt.Sprintf("%q <%s>", defaultName, addr.Address)
	}
	return (&mail.Address{Name: defaultName, Address: addr.Address}).String()
}

func (s *Service) checkDomainVerification(userID uint, workspaceID *uint, from string) error {
	if s.domainRepo == nil {
		return nil
	}

	// Resolve whether strict domain mode is on for this scope (R-23): an
	// explicit workspace override wins; a workspace without a settings row
	// inherits the platform-level require_domain_verification default (admin
	// toggle, default true). Per-user sends have no tri-state, so the stored
	// user flag decides.
	strict := false
	if workspaceID != nil {
		strict, _ = s.wsSettingOverrideOrPlatformDefault(*workspaceID)
	} else if s.userRepo != nil {
		user, err := s.userRepo.FindByID(userID)
		if err != nil {
			return fmt.Errorf("failed to load user: %w", err)
		}
		strict = user.RequireVerifiedDomain
	}

	if !strict {
		return nil
	}

	senderDomain := extractSenderDomain(from)
	if senderDomain == "" {
		return fmt.Errorf("domain_verification: could not extract domain from sender %q", from)
	}

	verified := false
	if workspaceID != nil {
		verified = s.domainRepo.IsOwnershipVerifiedInWorkspace(*workspaceID, senderDomain)
	} else {
		verified = s.domainRepo.IsOwnershipVerified(userID, senderDomain)
	}
	if !verified {
		return fmt.Errorf("domain_verification: domain %q is not verified. Add and verify the domain or disable strict domain mode", senderDomain)
	}

	return nil
}

// wsSettingOverrideOrPlatformDefault resolves strict domain mode for a
// workspace: the workspace's explicit settings row when one exists, otherwise
// the platform require_domain_verification setting (R-23), which defaults to
// true so out-of-the-box shared-server sending requires domain ownership.
func (s *Service) wsSettingOverrideOrPlatformDefault(workspaceID uint) (strict, override bool) {
	if s.wsSettingRepo != nil {
		if strict, ok := s.wsSettingRepo.RequireVerifiedDomainOverride(workspaceID); ok {
			return strict, true
		}
	}
	if s.settings != nil {
		return s.settings.RequireDomainVerification(), false
	}
	return false, false
}

// validateEmailAddress checks that a string is a valid RFC 5322 email address
func validateEmailAddress(addr string) error {
	parsed, err := mail.ParseAddress(addr)
	if err != nil {
		return fmt.Errorf("invalid email address %q: %w", addr, err)
	}
	parts := strings.SplitN(parsed.Address, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid email address %q: missing local part or domain", addr)
	}
	if !strings.Contains(parts[1], ".") {
		return fmt.Errorf("invalid email address %q: domain has no TLD", addr)
	}
	return nil
}

// validateAddresses validates the From and all To addresses.
func validateAddresses(from string, to []string) error {
	if err := validateEmailAddress(from); err != nil {
		return fmt.Errorf("invalid sender: %w", err)
	}
	for _, addr := range to {
		if err := validateEmailAddress(addr); err != nil {
			return fmt.Errorf("invalid recipient: %w", err)
		}
	}
	return nil
}

// extractSenderDomain returns the domain part of a sender address.
func extractSenderDomain(from string) string {
	bare := extractEmail(from)
	parts := strings.SplitN(bare, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}

// extractEmail extracts the bare email address from a string that may be in
// RFC 5322 format like "Display Name <user@example.com>" or just "user@example.com".
func extractEmail(from string) string {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return from
	}
	return addr.Address
}
