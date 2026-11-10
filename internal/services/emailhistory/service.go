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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package emailhistory records the message-history event timeline of outbound
// email: one email_events row per lifecycle event (sent, failed, opened,
// clicked, bounced, complained, unsubscribed, delivered) plus a write-once
// denormalized timestamp on the email row itself so the logs list can filter
// without joining the timeline.
//
// The package is deliberately forgiving: the Recorder swallows every error
// behind a bounded, rate-limited log line and never returns an error to its
// callers, because a recording failure must never fail a send.
package emailhistory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EventInput describes one lifecycle event to record. Type takes the
// models.EmailEvent* constants; the remaining fields are denormalized copies
// of what happened so the timeline row is self-contained.
type EventInput struct {
	EmailUUID       string
	WorkspaceID     *uint
	Type            string // models.EmailEventSent/Delivered/Failed/Opened/Clicked/Bounced/Complained/Unsubscribed
	OccurredAt      time.Time
	Provider        string
	ProviderEventID string
	Recipient       string
	IP              string
	UserAgent       string
	URL             string // clicked destination URL
	Error           string
	Metadata        map[string]any
}

// eventTimestampColumns maps an event type to the write-once timestamp column
// it denormalizes onto the email row. "failed" has no column: failures are
// visible through the events timeline and the email status itself.
var eventTimestampColumns = map[string]string{
	models.EmailEventSent:         "sent_at",
	models.EmailEventDelivered:    "delivered_at",
	models.EmailEventOpened:       "opened_at",
	models.EmailEventClicked:      "clicked_at",
	models.EmailEventBounced:      "bounced_at",
	models.EmailEventComplained:   "complained_at",
	models.EmailEventUnsubscribed: "unsubscribed_at",
}

// knownEventTypes is the closed set of event types the timeline accepts;
// anything else is dropped rather than written into a column other code
// switches on.
var knownEventTypes = map[string]bool{
	models.EmailEventSent:         true,
	models.EmailEventDelivered:    true,
	models.EmailEventFailed:       true,
	models.EmailEventOpened:       true,
	models.EmailEventClicked:      true,
	models.EmailEventBounced:      true,
	models.EmailEventComplained:   true,
	models.EmailEventUnsubscribed: true,
	models.EmailEventForwarded:    true,
	models.EmailEventComposed:     true, // OB-5 webmail compose (ADR-005 W3)
}

// Recorder persists message-history events. It is safe for concurrent use and
// a nil *Recorder is a valid no-op, so call sites never need a nil check.
type Recorder struct {
	db       *gorm.DB
	now      func() time.Time
	throttle *logThrottle
}

// NewRecorder returns a Recorder backed by db. A nil db yields a no-op
// recorder, so wiring can stay unconditional.
func NewRecorder(db *gorm.DB) *Recorder {
	return &Recorder{
		db:       db,
		now:      time.Now,
		throttle: newLogThrottle(),
	}
}

// WithNow overrides the clock, for deterministic tests.
func (r *Recorder) WithNow(fn func() time.Time) {
	if r != nil && fn != nil {
		r.now = fn
	}
}

// Record writes one event row and stamps the matching write-once timestamp on
// the email row. Every failure — bad input, database error, marshal error,
// even a panic — is contained: at most a rate-limited log line escapes, and
// the caller's flow is never interrupted.
func (r *Recorder) Record(ctx context.Context, in EventInput) {
	defer func() {
		// The recorder runs inside the sending pipeline; a panic here must
		// never bubble into a failed send.
		if rec := recover(); rec != nil {
			logger.Error("emailhistory: recovered from panic while recording event", "panic", rec)
		}
	}()

	if r == nil || r.db == nil {
		return
	}
	if in.EmailUUID == "" || !knownEventTypes[in.Type] {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := r.now()
	occurredAt := in.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = now
	}

	providerEventID := in.ProviderEventID
	if providerEventID == "" && (in.Type == models.EmailEventOpened || in.Type == models.EmailEventClicked) {
		// Opens and clicks have no provider to name them: dedup on a hash of
		// the interaction signature within a day bucket, so the first open of
		// a day is kept and automatic re-fetches collapse into it while
		// genuinely different interactions still get their own row.
		providerEventID = interactionEventID(in.Type, in.URL, in.IP, in.UserAgent, occurredAt)
	}

	row := &models.EmailEvent{
		EmailUUID:       in.EmailUUID,
		WorkspaceID:     in.WorkspaceID,
		Type:            in.Type,
		OccurredAt:      occurredAt,
		Provider:        in.Provider,
		ProviderEventID: providerEventID,
		Recipient:       in.Recipient,
		IP:              in.IP,
		UserAgent:       in.UserAgent,
		URL:             in.URL,
		Error:           in.Error,
		Metadata:        marshalMetadata(in.Metadata),
		CreatedAt:       now,
	}

	db := r.db.WithContext(ctx)
	// The (email_uuid, type, provider_event_id) unique index dedups provider
	// replays; a conflict simply skips the insert.
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		r.logError("insert event", err)
		return
	}

	// Write-once denormalized timestamp: an existing value is never
	// overwritten, so "first opened" survives repeated events. "failed" has no
	// timestamp column — the events row and the email status carry it.
	if col, ok := eventTimestampColumns[in.Type]; ok {
		if err := repositories.NewEmailRepository(r.db).
			UpdateStatusTimestamp(in.EmailUUID, col, occurredAt); err != nil {
			r.logError("write status timestamp", err)
		}
	}
}

// marshalMetadata renders the metadata map as the JSON string the jsonb column
// expects, falling back to the structural default.
func marshalMetadata(metadata map[string]any) string {
	if len(metadata) == 0 {
		return "{}"
	}
	b, err := json.Marshal(metadata)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// interactionEventID builds the dedup key of an open/click without a provider
// event id: a hash of the URL, client signature and a day bucket.
func interactionEventID(eventType, url, ip, userAgent string, t time.Time) string {
	bucket := t.UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(strings.Join([]string{eventType, url, ip, userAgent, bucket}, "|")))
	return hex.EncodeToString(h[:])[:32]
}

// logThrottle gates repeated log lines: at most one line per key per ttl. The
// entry map is bounded so a flood of distinct keys cannot grow it without
// limit; when full it evicts expired entries first, then arbitrary ones
// (dropping a throttle entry only allows an extra log line).
type logThrottle struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	max  int
}

// logThrottleTTL is how long a repeated log line stays suppressed.
const logThrottleTTL = time.Minute

func newLogThrottle() *logThrottle {
	return &logThrottle{seen: make(map[string]time.Time), ttl: logThrottleTTL, max: 512}
}

// allow reports whether a log line for key should be emitted now, recording
// the emission when true.
func (t *logThrottle) allow(key string, now time.Time) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.seen[key]; ok && now.Sub(last) < t.ttl {
		return false
	}
	if len(t.seen) >= t.max {
		for k, last := range t.seen {
			if now.Sub(last) >= t.ttl {
				delete(t.seen, k)
			}
		}
		for k := range t.seen {
			if len(t.seen) < t.max {
				break
			}
			delete(t.seen, k)
		}
	}
	t.seen[key] = now
	return true
}

// logError emits a rate-limited error line — recording must never flood the
// log when the database is down, but one line per failure class per minute
// keeps the problem visible.
func (r *Recorder) logError(action string, err error) {
	key := action + ":" + truncateKey(err.Error())
	if r.throttle.allow(key, r.now()) {
		logger.Error("emailhistory: failed to "+action, "error", err)
	}
}

// truncateKey bounds a log-throttle key so high-cardinality error strings
// cannot grow entries without limit.
func truncateKey(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
