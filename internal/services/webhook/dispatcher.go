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

package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
)

const (
	defaultMaxRetries    = 3
	defaultRetryBaseWait = 2 * time.Second
	// maxRetryWait caps the exponential retry backoff: waits grow as
	// base * 2^(attempt-1) until they hit this ceiling (R-06 backoff fix).
	maxRetryWait   = 60 * time.Second
	defaultTimeout = 10 * time.Second
	// maxAttemptBodyBytes caps the response body kept per attempt row.
	maxAttemptBodyBytes = 2048
)

// retryDelay computes the wait before the retry following attempt `attempt`
// (1-based): min(base * 2^(attempt-1), maxRetryWait) + jitter(0, base/4).
// True exponential backoff with a hard cap and jitter — the previous
// implementation slept base*attempt (linear) under an "exponential" comment.
// The shift is guarded against overflow: a nonsensically large attempt number
// degrades to the cap instead of wrapping negative.
func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := defaultRetryBaseWait << (attempt - 1)
	if backoff <= 0 || backoff > maxRetryWait {
		backoff = maxRetryWait
	}
	return backoff + time.Duration(rand.Int63n(int64(defaultRetryBaseWait/4)))
}

// proxyAckEnforced is the only accepted ProxyAckPolicy value.
const proxyAckEnforced = "enforced"

// Config holds configurable settings for the webhook dispatcher.
type Config struct {
	MaxRetries int
	Timeout    time.Duration
	// ProxyURL routes all outbound webhook requests through a proxy,
	// hiding the server's IP address from webhook endpoints.
	// Supports http://, https://, and socks5:// schemes.
	ProxyURL string
	// ProxyAckPolicy gates proxy mode on an explicit operator
	// acknowledgement. The only accepted value is "enforced"
	// (POSTA_WEBHOOK_PROXY_ACK_POLICY=enforced): it asserts the operator
	// accepts the residual proxy-side assumptions that remain on top of the
	// SSRF hardening — the proxy's own egress ACL behavior, split-horizon
	// reachability of the pinned public IP from the proxy, and rare proxies
	// that refuse IP-form targets. With destination pinning the SSRF
	// time-of-check/time-of-use window itself is closed; the acknowledgement
	// covers what the operator's proxy adds. Any other value keeps the
	// dispatcher in guarded direct mode even when ProxyURL is set.
	ProxyAckPolicy string

	// BatchEnabled turns on SF-90 batch delivery: events for the same
	// endpoint are grouped (up to BatchMaxEvents, or flushed after
	// BatchFlushInterval) instead of delivered per event. Default false —
	// per-event delivery remains the default behavior.
	BatchEnabled bool
	// BatchMaxEvents is how many events a batch may carry before an
	// immediate flush. Zero selects the default (25).
	BatchMaxEvents int
	// BatchFlushInterval is how long events may wait in the batch buffer
	// before an interval flush. Zero selects the default (5s).
	BatchFlushInterval time.Duration
	// MaxPendingEvents caps how many events may sit queued for ONE webhook
	// endpoint; when reached, the oldest queued event is dropped (logged +
	// counted via the OnEventsDropped callback →
	// posta_webhook_events_dropped_total). Zero selects the default (1000).
	// The cap is inert while batching is off (no pending backlog exists).
	MaxPendingEvents int
}

type Dispatcher struct {
	repo         *repositories.WebhookRepository
	deliveryRepo *repositories.WebhookDeliveryRepository
	attemptRepo  *repositories.WebhookDeliveryAttemptRepository
	client       *http.Client
	maxRetries   int
	// resolver is the DNS seam for the proxy-mode pre-flight (and any
	// resolution the pinned proxy dialing performs). Production leaves it
	// nil, which selects net.DefaultResolver; tests inject a stub so
	// resolution is controlled without touching the network.
	resolver Resolver
	// proxyDial reports that deliveries are routed through a configured
	// proxy. The dial target is the proxy itself, so post() pre-flights the
	// destination with a policy-checked resolution, and the pinned proxy
	// dialing layer (proxydial.go) re-validates and pins the destination on
	// every proxied connection.
	proxyDial bool
	// urlPolicy is the static SSRF gate every delivery URL must pass before a
	// request is built. Production always uses ValidateWebhookURL; tests that
	// exercise delivery semantics over loopback httptest servers relax it,
	// because loopback destinations are blocked by policy. That blocking
	// behavior itself is covered by ssrf_test.go and
	// TestDeliverPolicyBlockFailsFast.
	urlPolicy func(string) error
	// backoffDelay is the retry-wait seam: production uses retryDelay
	// (exponential with cap and jitter); tests inject a deterministic
	// function to pin the delay curve without waiting on real time.
	backoffDelay func(attempt int) time.Duration
	// sleep pauses between retries; tests replace it to observe the computed
	// delays instead of actually waiting.
	sleep             func(time.Duration)
	onDeliverySuccess func()
	onDeliveryFailed  func()
	onDeliveryDone    func(float64) // duration in seconds
	onEventsDropped   func()

	// SF-90 batch delivery state. batchEnabled is false (per-event mode)
	// until SetConfig turns batching on; batches holds one buffer per
	// webhook endpoint, guarded by batchMu.
	batchEnabled       bool
	batchMaxEvents     int
	batchFlushInterval time.Duration
	maxPendingEvents   int
	batchMu            sync.Mutex
	batches            map[uint]*batchBuffer
}

func NewDispatcher(repo *repositories.WebhookRepository) *Dispatcher {
	return &Dispatcher{
		repo:         repo,
		maxRetries:   defaultMaxRetries,
		urlPolicy:    ValidateWebhookURL,
		backoffDelay: retryDelay,
		sleep:        time.Sleep,
		client: &http.Client{
			Timeout:   defaultTimeout,
			Transport: NewGuardedTransport(nil),
			// Redirects are not followed (product-wide decision): a 3xx
			// becomes the final recorded response, so a redirect can never
			// hop the SSRF policy to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// SetConfig applies configurable settings to the dispatcher.
//
// Proxy mode is opt-in twice over: a ProxyURL alone does NOT enable it —
// cfg.ProxyAckPolicy must be "enforced" (POSTA_WEBHOOK_PROXY_ACK_POLICY).
// Without the acknowledgement the proxy is ignored and the dispatcher stays
// in guarded direct mode. When enabled, the outbound transport is built by
// the pinned proxy dialing layer (proxydial.go), which pins every proxied
// connection to the pre-validated public destination IP.
func (d *Dispatcher) SetConfig(cfg Config) {
	if cfg.MaxRetries > 0 {
		d.maxRetries = cfg.MaxRetries
	}
	if cfg.Timeout > 0 {
		d.client.Timeout = cfg.Timeout
	}
	if cfg.ProxyURL != "" {
		// Fail-safe first: a proxy URL that cannot be parsed must never
		// switch the dispatcher out of guarded direct mode.
		parsed, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			logger.Error("invalid webhook proxy URL, requests will be sent directly", "error", err)
			return
		}
		if cfg.ProxyAckPolicy != proxyAckEnforced {
			logger.Error("webhook proxy configured but POSTA_WEBHOOK_PROXY_ACK_POLICY=enforced is not set — ignoring proxy (direct mode)")
			return
		}
		transport, err := pinnedProxyTransport(parsed, d.resolver)
		if err != nil {
			logger.Error("invalid webhook proxy URL, requests will be sent directly", "error", err)
			return
		}
		d.proxyDial = true
		d.client.Transport = transport
		logger.Info("webhook proxy configured", "proxy", cfg.ProxyURL)
	}

	// SF-90 batch delivery + pending cap. BatchEnabled is false by default:
	// per-event dispatch stays the default behavior.
	d.batchEnabled = cfg.BatchEnabled
	d.batchMaxEvents = cfg.BatchMaxEvents
	if d.batchMaxEvents <= 0 {
		d.batchMaxEvents = defaultBatchMaxEvents
	}
	d.batchFlushInterval = cfg.BatchFlushInterval
	if d.batchFlushInterval <= 0 {
		d.batchFlushInterval = defaultBatchFlush
	}
	d.maxPendingEvents = cfg.MaxPendingEvents
	if d.maxPendingEvents <= 0 {
		d.maxPendingEvents = defaultMaxPendingEvents
	}
}

// OnEventsDropped sets a callback invoked each time an event is dropped by
// the per-endpoint pending cap (SF-90). Wired to the
// posta_webhook_events_dropped_total metric.
func (d *Dispatcher) OnEventsDropped(fn func()) { d.onEventsDropped = fn }

// pinnedProxyTransport builds the outbound transport for proxy mode. Supported
// schemes: http, https (pinned request-rewriting RoundTripper) and socks5
// (transport dialing through the pinned SOCKS5 dialer).
//
// SSRF enforcement differs by mode. Direct deliveries dial through the guarded
// transport, which pins every connection to a validated public IP. Proxy mode
// pins the destination as well: instead of forwarding the hostname to the
// proxy (which would let the proxy resolve DNS on its side and reopen a
// time-of-check/time-of-use window between the pre-flight validation and the
// connection), the proxy is asked to connect to the validated public IP
// literal, with the original hostname preserved for the Host header and TLS
// ServerName. The dial target is never the guarded dialer — reaching the
// proxy itself is a plain hardened dial (see proxydial.go).
func pinnedProxyTransport(parsed *url.URL, res Resolver) (http.RoundTripper, error) {
	switch parsed.Scheme {
	case schemeHTTP, schemeHTTPS:
		return newPinnedProxyRoundTripper(parsed, res), nil

	case "socks5":
		dialFn, err := newPinnedSOCKS5Dialer(parsed, res)
		if err != nil {
			return nil, err
		}
		return &http.Transport{
			DialContext:           dialFn,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https, or socks5)", parsed.Scheme)
	}
}

// SetDeliveryRepo sets the repository used to record delivery attempts.
func (d *Dispatcher) SetDeliveryRepo(repo *repositories.WebhookDeliveryRepository) {
	d.deliveryRepo = repo
}

// SetAttemptRepo sets the repository used to record the per-attempt history.
// Without it the final delivery record is still written, but individual retry
// attempts are not kept.
func (d *Dispatcher) SetAttemptRepo(repo *repositories.WebhookDeliveryAttemptRepository) {
	d.attemptRepo = repo
}

// OnDeliverySuccess sets a callback invoked after each successful webhook delivery.
func (d *Dispatcher) OnDeliverySuccess(fn func()) { d.onDeliverySuccess = fn }

// OnDeliveryFailed sets a callback invoked after each failed webhook delivery.
func (d *Dispatcher) OnDeliveryFailed(fn func()) { d.onDeliveryFailed = fn }

// OnDeliveryDone sets a callback invoked after each delivery attempt with its duration.
func (d *Dispatcher) OnDeliveryDone(fn func(float64)) { d.onDeliveryDone = fn }

type Payload struct {
	Event     string `json:"event"`
	EmailID   string `json:"email_id"`
	Timestamp string `json:"timestamp"`
}

// CampaignPayload is the outbound shape for campaign lifecycle events
// (campaign.started, campaign.completed).
type CampaignPayload struct {
	Event      string `json:"event"`
	CampaignID uint   `json:"campaign_id"`
	Name       string `json:"name"`
	Timestamp  string `json:"timestamp"`
}

// Dispatch sends webhook notifications for the given event using the default
// outbound payload shape ({event, email_id, timestamp}). Runs asynchronously.
// workspaceID scopes which webhooks fire: nil targets the user's personal
// webhooks, a value targets that workspace's webhooks.
func (d *Dispatcher) Dispatch(userID uint, workspaceID *uint, event string, emailID string, from string) {
	payload := Payload{
		Event:     event,
		EmailID:   emailID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("failed to marshal webhook payload", "error", err)
		return
	}

	d.DispatchJSON(userID, workspaceID, event, body, from)
}

// DispatchCampaign sends webhook notifications for a campaign lifecycle event
// using the campaign payload shape ({event, campaign_id, name, timestamp}).
// Scoping follows the same rules as Dispatch. Runs asynchronously.
func (d *Dispatcher) DispatchCampaign(userID uint, workspaceID *uint, event string, campaignID uint, name string, from string) {
	payload := CampaignPayload{
		Event:      event,
		CampaignID: campaignID,
		Name:       name,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		logger.Error("failed to marshal campaign webhook payload", "error", err)
		return
	}

	d.DispatchJSON(userID, workspaceID, event, body, from)
}

// DispatchJSON sends a pre-serialized JSON body to all webhooks subscribed to event
// within the given scope (personal when workspaceID is nil, otherwise the
// workspace) that pass the sender-address filter. Runs asynchronously.
// Callers own the payload shape — this is the escape hatch used by the inbound
// pipeline (which has a richer payload than the outbound Payload struct).
func (d *Dispatcher) DispatchJSON(userID uint, workspaceID *uint, event string, body []byte, fromAddr string) {
	go func() {
		scope := repositories.ResourceScope{UserID: userID, WorkspaceID: workspaceID}
		webhooks, err := d.repo.FindByScopeAndEvent(scope, event)
		if err != nil {
			logger.Error("failed to find webhooks", "error", err)
			return
		}

		for _, wh := range webhooks {
			if !matchesFilters(wh.Filters, fromAddr) {
				continue
			}
			// SF-90: with batching enabled (POSTA_WEBHOOK_BATCH_ENABLED),
			// events group per endpoint instead of firing per event;
			// per-event delivery remains the default.
			if d.batchEnabled {
				d.dispatchBatched(userID, wh, event, body)
				continue
			}
			d.deliverWithRetry(userID, wh, body, event)
		}
	}()
}

// matchesFilters checks if the sender address matches the webhook's filters.
func matchesFilters(filters []string, from string) bool {
	if len(filters) == 0 {
		return true
	}
	if from == "" {
		return true
	}

	addr := from
	if parsed, err := mail.ParseAddress(from); err == nil {
		addr = parsed.Address
	}
	addr = strings.ToLower(addr)

	domain := ""
	if parts := strings.SplitN(addr, "@", 2); len(parts) == 2 {
		domain = parts[1]
	}

	for _, f := range filters {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if f == addr {
			return true
		}
		if domain != "" && f == domain {
			return true
		}
	}
	return false
}

// deliverWithRetry attempts to deliver a webhook with exponential backoff retries.
// Every HTTP attempt — successful or not — is kept as a webhook_delivery_attempts
// row so the delivery history shows the full retry story, not only its final
// outcome.
func (d *Dispatcher) deliverWithRetry(userID uint, wh models.Webhook, body []byte, event string) {
	var lastErr string
	var lastStatus int
	var attempts []models.WebhookDeliveryAttempt

	start := time.Now()

	for attempt := 1; attempt <= d.maxRetries; attempt++ {
		attemptedAt := time.Now().UTC()
		statusCode, responseBody, err := d.post(wh.URL, wh.Secret, body)
		lastStatus = statusCode

		// Keep the per-attempt outcome regardless of what happens next: the
		// rows are attached to the delivery once it exists (below). Error text
		// is sanitized at write time — attempt rows are user-visible delivery
		// history and must never carry transport internals (IPs, ports,
		// resolver detail).
		recorded := models.WebhookDeliveryAttempt{
			Attempt:      attempt,
			StatusCode:   statusCode,
			ResponseBody: responseBody,
			CreatedAt:    attemptedAt,
		}
		if err != nil {
			recorded.Error = SanitizeDeliveryError(err.Error())
		}
		attempts = append(attempts, recorded)

		if err == nil && statusCode < 400 {
			delivery := d.recordDelivery(userID, wh.ID, wh.WorkspaceID, event, models.WebhookDeliverySuccess, statusCode, "", attempt, body)
			d.recordAttempts(delivery, attempts)
			if d.onDeliverySuccess != nil {
				d.onDeliverySuccess()
			}
			if d.onDeliveryDone != nil {
				d.onDeliveryDone(time.Since(start).Seconds())
			}
			logger.Info("webhook delivered", "url", wh.URL, "event", event, "attempt", attempt)
			return
		}

		if errors.Is(err, ErrPolicyBlocked) {
			// Policy rejections are deterministic: the same URL resolves and
			// judges the same way on every retry, so retrying would only
			// repeat the outcome — fail fast, no sleep, no further attempts.
			// The raw reason stays in server logs; what is recorded is the
			// sanitized message.
			logger.Warn("webhook destination blocked by security policy", "url", wh.URL, "attempt", attempt, "error", err)
			d.failDelivery(userID, wh, event, lastStatus, SanitizeDeliveryError(err.Error()), attempt, attempts, start, body)
			return
		}

		if err != nil {
			lastErr = SanitizeDeliveryError(err.Error())
			logger.Warn("webhook delivery failed", "url", wh.URL, "attempt", attempt, "error", err)
		} else {
			lastErr = fmt.Sprintf("HTTP %d", statusCode)
			logger.Warn("webhook returned error status", "url", wh.URL, "attempt", attempt, "status", statusCode)
		}

		if attempt < d.maxRetries {
			d.sleep(d.backoffDelay(attempt))
		}
	}

	// All retries exhausted
	d.failDelivery(userID, wh, event, lastStatus, lastErr, d.maxRetries, attempts, start, body)
}

// failDelivery records the final failed delivery together with its buffered
// attempt rows and fires the failure callbacks. Shared by the exhausted-
// retries path and the fail-fast policy-block path.
func (d *Dispatcher) failDelivery(userID uint, wh models.Webhook, event string, httpStatus int, errMsg string, attempt int, attempts []models.WebhookDeliveryAttempt, start time.Time, body []byte) {
	delivery := d.recordDelivery(userID, wh.ID, wh.WorkspaceID, event, models.WebhookDeliveryFailed, httpStatus, errMsg, attempt, body)
	d.recordAttempts(delivery, attempts)
	if d.onDeliveryFailed != nil {
		d.onDeliveryFailed()
	}
	if d.onDeliveryDone != nil {
		d.onDeliveryDone(time.Since(start).Seconds())
	}
}

// signPayload computes an HMAC-SHA256 signature of body using the given secret.
func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignBody is the exported form of the dispatcher's payload signature
// (OB-6 observability, GAP-51/SF-96): the error-monitoring hook and the
// metric-alert engine sign their POSTs with the same HMAC-SHA256 scheme and
// the same X-Posta-Signature header so receivers verify every Posta webhook
// stream with one verification path. This adds no new scheme — it re-uses
// signPayload verbatim.
func SignBody(secret string, body []byte) string {
	return signPayload(secret, body)
}

// post sends a single HTTP POST to the webhook URL with an HMAC signature and
// returns the status code plus the (truncated) response body so the attempt
// history can keep what the endpoint answered. The SSRF policy is enforced
// before the request is built: the static URL checks always run, and in proxy
// mode a pre-flight policy-checked resolution doubles as the first
// destination gate — the pinned proxy transport then re-validates and pins
// the destination on every proxied connection (proxydial.go). In direct mode
// the guarded transport re-checks the destination at dial time on every
// connection.
func (d *Dispatcher) post(rawURL string, secret string, body []byte) (int, string, error) {
	if err := d.urlPolicy(rawURL); err != nil {
		return 0, "", err
	}
	if d.proxyDial {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := ResolvePublicURL(ctx, d.resolver, rawURL); err != nil {
			return 0, "", err
		}
	}

	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Posta-Webhook/1.0")
	if secret != "" {
		req.Header.Set("X-Posta-Signature", "sha256="+signPayload(secret, body))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody := truncateAttemptBody(resp.Body)
	return resp.StatusCode, responseBody, nil
}

// truncateAttemptBody reads up to maxAttemptBodyBytes from the response body
// for the attempt history.
func truncateAttemptBody(r io.Reader) string {
	buf := make([]byte, maxAttemptBodyBytes+1)
	n, _ := io.ReadFull(r, buf)
	if n <= 0 {
		return ""
	}
	if n > maxAttemptBodyBytes {
		n = maxAttemptBodyBytes
	}
	return string(buf[:n])
}

// recordDelivery persists a webhook delivery record if the delivery repo is
// configured. It returns the stored delivery (nil when recording is disabled
// or failed) so per-attempt rows can reference it. The outbound payload is
// stored with the delivery so the R-06 replay endpoint can re-send the exact
// original body; deliveries recorded before payload persistence carry an
// empty payload and cannot be replayed.
func (d *Dispatcher) recordDelivery(userID, webhookID uint, workspaceID *uint, event string, status models.WebhookDeliveryStatus, httpStatus int, errMsg string, attempt int, body []byte) *models.WebhookDelivery {
	if d.deliveryRepo == nil {
		return nil
	}
	delivery := &models.WebhookDelivery{
		WebhookID:      webhookID,
		UserID:         userID,
		WorkspaceID:    workspaceID,
		Event:          event,
		Status:         status,
		HTTPStatusCode: httpStatus,
		ErrorMessage:   errMsg,
		Attempt:        attempt,
		Payload:        string(body),
	}
	if err := d.deliveryRepo.Create(delivery); err != nil {
		logger.Error("failed to record webhook delivery", "error", err)
		return nil
	}
	return delivery
}

// recordAttempts stores one webhook_delivery_attempts row per HTTP attempt.
// The rows are buffered until the delivery exists so they can reference it;
// attempt rows for a delivery that could not be recorded are skipped rather
// than orphaned.
func (d *Dispatcher) recordAttempts(delivery *models.WebhookDelivery, attempts []models.WebhookDeliveryAttempt) {
	if d.attemptRepo == nil || delivery == nil {
		return
	}
	for i := range attempts {
		attempts[i].DeliveryID = delivery.ID
		if err := d.attemptRepo.Create(&attempts[i]); err != nil {
			logger.Error("failed to record webhook delivery attempt", "error", err, "delivery_id", delivery.ID, "attempt", attempts[i].Attempt)
		}
	}
}

// ReplayResult reports the outcome of one operator-initiated replay attempt.
type ReplayResult struct {
	// Attempt is the attempt row's number: prior max + 1, so replay history
	// continues the original delivery's numbering.
	Attempt    int
	StatusCode int
	Status     models.WebhookDeliveryStatus
	Error      string
}

// ReplayDelivery re-sends a previously recorded webhook delivery exactly once
// through the same guarded post() path as live deliveries (static SSRF gate +
// guarded transport + proxy pre-flight — no bypass), appends one attempt row
// referencing the SAME delivery with attempt = prior max + 1, and updates the
// delivery row's final outcome. The delivery's own Attempt field keeps the
// original delivery's attempt count: replay rows are the rows numbered above
// it, which is also how the per-delivery replay rate limit counts them.
func (d *Dispatcher) ReplayDelivery(wh models.Webhook, delivery *models.WebhookDelivery, body []byte) (*ReplayResult, error) {
	next := delivery.Attempt + 1
	if d.attemptRepo != nil {
		if max, err := d.attemptRepo.MaxAttemptNumber(delivery.ID); err != nil {
			logger.Warn("replay: could not read prior attempt max, falling back to the delivery's attempt count", "delivery_id", delivery.ID, "error", err)
		} else if max >= next {
			next = max + 1
		}
	}

	start := time.Now()
	attemptedAt := time.Now().UTC()
	statusCode, responseBody, err := d.post(wh.URL, wh.Secret, body)

	result := &ReplayResult{
		Attempt:    next,
		StatusCode: statusCode,
		Status:     models.WebhookDeliveryFailed,
	}
	errMsg := ""
	if err != nil {
		result.Error = SanitizeDeliveryError(err.Error())
		errMsg = result.Error
	} else if statusCode >= 400 {
		result.Error = fmt.Sprintf("HTTP %d", statusCode)
		errMsg = result.Error
	} else {
		result.Status = models.WebhookDeliverySuccess
	}

	// The attempt row is the delivery-history record of the replay; error text
	// is sanitized exactly like live attempts.
	recorded := models.WebhookDeliveryAttempt{
		DeliveryID:   delivery.ID,
		Attempt:      next,
		StatusCode:   statusCode,
		ResponseBody: responseBody,
		CreatedAt:    attemptedAt,
	}
	if err != nil {
		recorded.Error = SanitizeDeliveryError(err.Error())
	}
	if d.attemptRepo != nil {
		if err := d.attemptRepo.Create(&recorded); err != nil {
			logger.Error("replay: failed to record attempt", "error", err, "delivery_id", delivery.ID, "attempt", next)
		}
	}
	if d.deliveryRepo != nil {
		if err := d.deliveryRepo.UpdateReplayOutcome(delivery.ID, result.Status, statusCode, errMsg); err != nil {
			logger.Error("replay: failed to update delivery outcome", "error", err, "delivery_id", delivery.ID)
		}
	}

	if result.Status == models.WebhookDeliverySuccess {
		if d.onDeliverySuccess != nil {
			d.onDeliverySuccess()
		}
	} else if d.onDeliveryFailed != nil {
		d.onDeliveryFailed()
	}
	if d.onDeliveryDone != nil {
		d.onDeliveryDone(time.Since(start).Seconds())
	}
	return result, nil
}
