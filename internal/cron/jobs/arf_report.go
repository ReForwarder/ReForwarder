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

package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/services/arf"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// TypeARFReport is the cron trigger for the daily outbound auth-failure
// report pass (SF-26, RFC 5965/6591). The job collects the previous UTC
// day's DKIM/SPF auth-failure verdicts from the inbound verification
// substrate, discovers each reported domain's abuse-reporting addresses from
// its live _arf DNS record (RFC 6591 §4), and hands one bounded batch of
// per-message feedback reports to the wired Sender (the production adapter
// stores them as system notifications the outbound pipeline relays verbatim).
//
// Policy decisions baked in (documented; the TLS-RPT/DMARC send precedents'
// decisions apply):
//   - one daily pass, bounded per reported domain per window (never a mailing
//     storm), and the deployment never mails external addresses on a quiet
//     day;
//   - reports are addressed ONLY to the mailto abuse-reporting addresses read
//     from the reported domain's published _arf DNS record at send time
//     (never to stored or user-supplied addresses);
//   - a send failure is logged and dropped: best-effort reports, no retry
//     queue.
const TypeARFReport = "cron:arf-report"

// ARFReportJob is the daily outbound auth-failure report pass.
type ARFReportJob struct {
	db          *gorm.DB
	agg         *arf.Aggregator
	txtLookup   arf.TXTLookup
	messageSrc  arf.MessageSource
	sender      arf.Sender
	fromAddress string
	orgName     string
	// version is the report's User-Agent version stamp.
	version string
	// now is overridable in tests.
	now func() time.Time
}

// NewARFReportJob builds the job. sender is the delivery seam; fromAddress
// and orgName come from the deployment identity in cmd wiring.
func NewARFReportJob(db *gorm.DB, agg *arf.Aggregator, sender arf.Sender, fromAddress, orgName, version string) *ARFReportJob {
	return &ARFReportJob{
		db:          db,
		agg:         agg,
		sender:      sender,
		fromAddress: strings.TrimSpace(fromAddress),
		orgName:     strings.TrimSpace(orgName),
		version:     strings.TrimSpace(version),
		now:         time.Now,
	}
}

// SetTXTLookup overrides the _arf TXT resolver (tests; production uses the
// system resolver).
func (j *ARFReportJob) SetTXTLookup(fn arf.TXTLookup) {
	if fn != nil {
		j.txtLookup = fn
	}
}

// SetMessageSource wires the offending-message fetcher for the report's
// third part (RFC 5965 §6.3's SHOULD). Unwired, the part is omitted.
func (j *ARFReportJob) SetMessageSource(fn arf.MessageSource) {
	if fn != nil {
		j.messageSrc = fn
	}
}

// SetNow overrides the clock (tests).
func (j *ARFReportJob) SetNow(fn func() time.Time) {
	if fn != nil {
		j.now = fn
	}
}

func (j *ARFReportJob) Name() string     { return "arf-report" }
func (j *ARFReportJob) Schedule() string { return "5 7 * * *" } // daily 07:05 UTC

// Run implements cron.Job. The aggregation runs in-process — deployment-wide
// verification telemetry, no tenant data payload beyond the verdicts, and
// the output is bounded per reported domain.
func (j *ARFReportJob) Run(ctx context.Context, _ *asynq.Client) error {
	if j.sender == nil || j.fromAddress == "" || j.orgName == "" {
		return nil // generator identity not configured; the pass is inert
	}

	end := j.now().UTC().Truncate(24 * time.Hour)
	start := end.Add(-24 * time.Hour)

	batches, skipped, err := j.agg.Aggregate(ctx, start, end)
	if err != nil {
		return err
	}
	if len(batches) == 0 {
		logger.Info("arf: no addressable auth failures in the window; nothing to report", "window_start", start, "window_end", end)
		return nil
	}
	if skipped > 0 {
		logger.Warn("arf: some verdict rows were not addressable and are not reported", "skipped", skipped)
	}

	sent := 0
	for _, batch := range batches {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// The live DNS read: the reported domain's _arf abuse-reporting
		// addresses — never stored addresses, never guessed.
		addrs, err := arf.DiscoverAddresses(ctx, j.txtLookup, batch.ReportedDomain)
		if err != nil || len(addrs) == 0 {
			logger.Debug("arf: reported domain publishes no _arf abuse address; skipping", "domain", batch.ReportedDomain, "error", err)
			continue
		}
		for _, row := range batch.Rows {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			var message []byte
			if j.messageSrc != nil {
				message, err = j.messageSrc(ctx, row.InboundID)
				if err != nil {
					// The report's message part is a SHOULD: omit it and
					// report the failure from the stored verdicts.
					logger.Warn("arf: offending message unavailable; reporting without it", "inbound_id", row.InboundID, "error", err)
					message = nil
				}
			}
			reportID := arf.OutboundReportID(row.InboundID, batch.Family, j.orgName)
			doc := arf.BuildAuthFailureReport(j.orgName, j.version, row, row.AuthResultsHeader, message, j.now())
			req := &arf.OutboundReportRequest{
				ReportID:       reportID,
				ReportedDomain: batch.ReportedDomain,
				Family:         batch.Family,
				InboundID:      row.InboundID,
				UserID:         row.UserID,
				WorkspaceID:    row.WorkspaceID,
				Document:       doc,
				Subject:        fmt.Sprintf("Auth-Failure Report for domain %s", batch.ReportedDomain),
				To:             addrs,
				From:           j.fromAddress,
			}
			if err := j.sender.SendReport(ctx, req); err != nil {
				logger.Error("arf: report send failed", "domain", batch.ReportedDomain, "report_id", reportID, "error", err)
				continue
			}
			sent++
			logger.Info("arf: report sent", "domain", batch.ReportedDomain, "report_id", reportID, "family", batch.Family)
		}
	}
	logger.Info("arf: outbound report pass complete", "reported_domains", len(batches), "sent", sent)
	return nil
}
