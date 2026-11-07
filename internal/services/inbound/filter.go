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
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// Filter disposition application (OB-2, ADR-006 decision 5). The spam/malware
// pipeline runs in the ingest WORKER — after durable ingest, never in the
// SMTP session — and lands here: the durable record moves to the disposition
// the filter decided, and every non-deliver disposition writes the ADR-006
// event taxonomy (inbound.spam_flagged, inbound.malware_detected,
// inbound.quarantined). A disposition can never lose the message: reject
// marks the row rejected (it stays queryable), quarantine marks it
// quarantined (operator review per ADR-002 §3, whose store/release wave wires
// the QuarantineStore seam).

// FilterDisposition is one filter outcome for one durable record.
type FilterDisposition struct {
	Disposition spamfilter.Disposition
	// Score is the final spam score (malware positives carry the malware
	// floor score for observability).
	Score float64
	// Reasons lists the contributing signals (scorer reason vocabulary).
	Reasons []string
	// ThreatSignature is the ClamAV signature when the disposition came from
	// a malware positive; empty for a pure spam disposition.
	ThreatSignature string
	// Malware marks the malware-detected path (selects the event type).
	Malware bool
}

// filterSpamFloorScore is the score stamped on malware positives so threshold
// reporting stays coherent (a malware record is above every spam threshold).
const filterSpamFloorScore = 100.0

// ApplyFilterDisposition moves a durably stored inbound record to the filter's
// disposition and writes the taxonomy events. deliver is a no-op stamp (the
// record keeps its status); quarantine and reject flip the status and publish
// events. Errors are returned for the caller to log — the durable record
// stands regardless.
func (s *Service) ApplyFilterDisposition(ctx context.Context, rec *models.InboundEmail, d FilterDisposition) error {
	if s == nil || rec == nil {
		return errors.New("filter disposition: nil record")
	}
	if !spamfilter.Valid(d.Disposition) {
		return errors.New("filter disposition: unknown disposition")
	}

	rec.SpamScore = &d.Score
	rec.SpamReasons = joinReasons(d.Reasons)
	if d.ThreatSignature != "" {
		rec.ThreatSignature = d.ThreatSignature
	}

	switch d.Disposition {
	case spamfilter.DispositionDeliver:
		// Score/reasons are stamped for observability; the pipeline continues.
		return s.repo.Update(rec)

	case spamfilter.DispositionQuarantine:
		rec.Status = models.InboundStatusQuarantined
		if err := s.repo.Update(rec); err != nil {
			return err
		}
		// Quarantine row via the ADR-002 §3 seam (nil route — no routing rule
		// drove this disposition). Best effort: the row status is already the
		// durable truth; the store wave owns the richer record.
		if s.quarantine != nil {
			if err := s.quarantine.Quarantine(ctx, rec, nil, filterReasonText(d)); err != nil {
				logger.Error("failed to create quarantine row for filter disposition",
					"inbound_id", rec.UUID, "error", err)
			}
		}
		if d.Malware {
			s.publishInboundEvent("inbound.malware_detected", rec, nil, map[string]any{
				"signature": d.ThreatSignature,
				"score":     d.Score,
			})
		} else {
			s.publishInboundEvent("inbound.spam_flagged", rec, nil, map[string]any{
				"score":       d.Score,
				"reasons":     d.Reasons,
				"disposition": string(d.Disposition),
			})
		}
		s.publishInboundEvent("inbound.quarantined", rec, nil, map[string]any{
			"reason": filterReasonText(d),
		})
		return nil

	case spamfilter.DispositionReject:
		rec.Status = models.InboundStatusRejected
		rec.ErrorMessage = "spam filter: " + filterReasonText(d)
		if err := s.repo.Update(rec); err != nil {
			return err
		}
		// Post-ingest reject (ADR-006: never a silent drop without an event).
		if d.Malware {
			s.publishInboundEvent("inbound.malware_detected", rec, nil, map[string]any{
				"signature":   d.ThreatSignature,
				"score":       d.Score,
				"disposition": string(d.Disposition),
			})
		} else {
			s.publishInboundEvent("inbound.spam_flagged", rec, nil, map[string]any{
				"score":       d.Score,
				"reasons":     d.Reasons,
				"disposition": string(d.Disposition),
			})
		}
		return nil
	}
	return nil
}

// filterReasonText renders the disposition's short reason text.
func filterReasonText(d FilterDisposition) string {
	if d.Malware && d.ThreatSignature != "" {
		return "malware detected: " + d.ThreatSignature
	}
	if len(d.Reasons) > 0 {
		return "spam score " + trimFloat(d.Score) + " (" + joinReasons(d.Reasons) + ")"
	}
	return "spam score " + trimFloat(d.Score)
}

// joinReasons joins the scorer reasons for the record's reason column.
func joinReasons(reasons []string) string {
	out := ""
	for i, r := range reasons {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out
}

// trimFloat renders a score compactly for text fields.
func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// WaitAuthResults returns the persisted SPF/DKIM/DMARC verdict for a record,
// waiting up to maxWait for the asynchronous verification (P3-1 hook) to
// persist its row. When verification is disabled, or the wait elapses, it
// returns nil — the caller scores with "none" (an unavailable signal
// abstains; ADR-006 baseline rule). A 250ms poll cadence keeps the added
// parse latency within one DNS-typical window.
func (s *Service) WaitAuthResults(ctx context.Context, inboundID uint, maxWait time.Duration) *models.InboundAuthResult {
	if s == nil || s.verifier == nil || !s.verifier.enabled || s.verifier.db == nil {
		return nil
	}
	deadline := time.Now().Add(maxWait)
	for {
		var row models.InboundAuthResult
		err := s.verifier.db.Where("inbound_id = ?", inboundID).First(&row).Error
		if err == nil {
			return &row
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Warn("filter: failed to read auth results", "inbound_id", inboundID, "error", err)
			return nil
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// AuthVerdictFromRow maps the persisted verdict row onto the scorer input.
func AuthVerdictFromRow(row *models.InboundAuthResult) spamfilter.AuthVerdict {
	if row == nil {
		return spamfilter.AuthVerdict{}
	}
	return spamfilter.AuthVerdict{
		SPF:              row.SPFResult,
		DKIM:             row.DKIMResult,
		DMARC:            row.DMARCResult,
		DMARCDisposition: row.DMARCDisposition,
	}
}
