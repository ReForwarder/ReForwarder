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

package security

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// EventRecorder persists security_events rows. All persistence is
// best-effort: failures are logged (rate-limited) and returned, never
// panicked on, so a database hiccup can never take down the accept loop or
// request handling that feeds the detector.
type EventRecorder struct {
	repo    *repositories.SecurityEventRepository
	limiter *logThrottle
	now     func() time.Time
}

// NewEventRecorder returns an EventRecorder writing through the given
// repository. A nil repository makes the recorder a no-op.
func NewEventRecorder(repo *repositories.SecurityEventRepository) *EventRecorder {
	return &EventRecorder{
		repo:    repo,
		limiter: newLogThrottle(30 * time.Second),
		now:     time.Now,
	}
}

// BuildEvent assembles a security_events row. The evidence map is marshaled
// to the JSON string the column stores ("{}" when empty).
func (r *EventRecorder) BuildEvent(sourceIP, eventType, severity, protocol string, destPort *int, evidence map[string]any, riskScore int, action string) *models.SecurityEvent {
	row := &models.SecurityEvent{
		SourceIP:    sourceIP,
		EventType:   eventType,
		Severity:    severity,
		Protocol:    protocol,
		Evidence:    marshalEvidence(evidence),
		RiskScore:   riskScore,
		ActionTaken: action,
	}
	if row.Severity == "" {
		row.Severity = SeverityInfo
	}
	if row.ActionTaken == "" {
		row.ActionTaken = EventActionRecorded
	}
	if destPort != nil {
		p := *destPort
		row.DestPort = &p
	}
	return row
}

// Insert writes one security_events row, stamping CreatedAt when zero.
// Errors are logged rate-limited and returned to the caller.
func (r *EventRecorder) Insert(ctx context.Context, row *models.SecurityEvent) error {
	if r == nil || r.repo == nil || row == nil {
		return nil
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = r.now().UTC()
	}
	err := r.repo.Create(row)
	if err != nil && r.limiter.allow("security.event.insert", r.now()) {
		logger.Error("security: failed to persist security event",
			"error", err, "event_type", row.EventType, "source_ip", row.SourceIP)
	}
	return err
}

// SignalExceeded records that a detection signal crossed its threshold
// (event type = signal name, severity = warning, action = recorded).
func (r *EventRecorder) SignalExceeded(ctx context.Context, sourceIP string, sig Signal, protocol string, destPort *int, evidence map[string]any, riskScore int) error {
	return r.Insert(ctx, r.BuildEvent(sourceIP, string(sig), sig.EventSeverity(), protocol, destPort, evidence, riskScore, EventActionRecorded))
}

// BanCreated records the creation of a ban
// (event type ban.created, severity critical, action banned).
func (r *EventRecorder) BanCreated(ctx context.Context, sourceIP string, reason Reason, description string, evidence map[string]any, riskScore int) error {
	ev := map[string]any{"reason": string(reason), "description": description}
	for k, v := range evidence {
		if _, dup := ev[k]; !dup {
			ev[k] = v
		}
	}
	return r.Insert(ctx, r.BuildEvent(sourceIP, EventBanCreated, SeverityCritical, "", nil, ev, riskScore, EventActionBanned))
}

// BanLifted records that a ban was removed
// (event type ban.lifted, severity info, action recorded).
func (r *EventRecorder) BanLifted(ctx context.Context, sourceIP string, by string) error {
	return r.Insert(ctx, r.BuildEvent(sourceIP, EventBanLifted, SeverityInfo, "", nil, map[string]any{"by": by}, 0, EventActionRecorded))
}

// Rejected records that a connection from a blocked IP was refused
// (event type connection.rejected, severity warning, action rejected).
func (r *EventRecorder) Rejected(ctx context.Context, sourceIP string, protocol string, destPort *int, reason Reason) error {
	return r.Insert(ctx, r.BuildEvent(sourceIP, EventRejected, SeverityWarning, protocol, destPort, map[string]any{"reason": string(reason)}, 0, EventActionRejected))
}

// marshalEvidence serializes an evidence map to the JSON string stored in the
// security_events / security_bans evidence columns. Never fails: empty or
// unmarshalable evidence becomes "{}".
func marshalEvidence(evidence map[string]any) string {
	if len(evidence) == 0 {
		return "{}"
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// logThrottle gates repeated log lines: at most one line per key per ttl.
// The entry map is bounded so a flood of distinct keys cannot grow it without
// limit; when full it evicts expired entries first, then arbitrary ones
// (dropping a throttle entry only allows an extra log line).
type logThrottle struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	max  int
}

func newLogThrottle(ttl time.Duration) *logThrottle {
	return &logThrottle{seen: make(map[string]time.Time), ttl: ttl, max: 512}
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
