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
	"strings"
	"time"

	"github.com/goposta/posta/internal/services/dmarc"
	"github.com/goposta/posta/internal/services/inbound/verify"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// TypeDMARCReport is the cron trigger for the daily outbound DMARC aggregate
// report pass (SF-25 send side). The job aggregates the previous UTC day's
// inbound DMARC verdicts per reported (From-) domain, reads each reported
// domain's live _dmarc DNS record for the policy-published block and the rua
// endpoints, and hands one report per domain to the wired Sender (the
// production adapter enqueues it through the system-forward pipeline).
//
// Policy decisions baked in (documented; the TLS-RPT send precedent's
// decisions apply verbatim):
//   - one daily pass, only when the window carried DMARC-evaluated messages —
//     the deployment never mails external addresses on a quiet day;
//   - reports are addressed ONLY to mailto rua endpoints read from the
//     reported domain's published DMARC record at send time (never to stored
//     or user-supplied addresses); non-mailto rua URIs (https:) are skipped;
//   - a send failure is logged and dropped: the next window's report
//     supersedes the lost one (RFC 7489 reports are best-effort aggregates;
//     no retry queue is kept for them).
const TypeDMARCReport = "cron:dmarc-report"

// DMARCReportJob is the daily outbound DMARC aggregate report pass.
type DMARCReportJob struct {
	db        *gorm.DB
	agg       *dmarc.Aggregator
	policyLookup func(ctx context.Context, domain string) (string, *verify.DMARCPolicy, error)
	sender    dmarc.Sender
	// fromAddress is the generator address (dmarc-report@<org domain>);
	// empty disables the job (nothing is ever sent without a usable
	// generator).
	fromAddress string
	// orgName is the RFC 7489 <org_name> of this deployment.
	orgName string
	// now is overridable in tests.
	now func() time.Time
}

// NewDMARCReportJob builds the job. sender is the delivery seam; fromAddress
// and orgName come from the deployment identity in cmd wiring.
func NewDMARCReportJob(db *gorm.DB, agg *dmarc.Aggregator, sender dmarc.Sender, fromAddress, orgName string) *DMARCReportJob {
	return &DMARCReportJob{
		db:          db,
		agg:         agg,
		sender:      sender,
		fromAddress: strings.TrimSpace(fromAddress),
		orgName:     strings.TrimSpace(orgName),
		now:         time.Now,
	}
}

// SetPolicyLookup overrides the live _dmarc policy resolver (tests;
// production uses the system resolver through the verify package's
// RFC 7489 §6.6.3 discovery).
func (j *DMARCReportJob) SetPolicyLookup(fn func(ctx context.Context, domain string) (string, *verify.DMARCPolicy, error)) {
	if fn != nil {
		j.policyLookup = fn
	}
}

// SetNow overrides the clock (tests).
func (j *DMARCReportJob) SetNow(fn func() time.Time) {
	if fn != nil {
		j.now = fn
	}
}

func (j *DMARCReportJob) Name() string     { return "dmarc-report" }
func (j *DMARCReportJob) Schedule() string { return "50 6 * * *" } // daily 06:50 UTC

// Run implements cron.Job. The aggregation runs in-process (unlike the
// per-workspace fan-out jobs) — it reads deployment-wide verification
// telemetry, has no tenant data payload (source IPs and verdict tallies
// only), and its output is at most a handful of reports.
func (j *DMARCReportJob) Run(ctx context.Context, _ *asynq.Client) error {
	if j.sender == nil || j.fromAddress == "" || j.orgName == "" {
		return nil // generator identity not configured; the pass is inert
	}
	if j.policyLookup == nil {
		res := verify.NetResolver{}
		j.policyLookup = func(ctx context.Context, domain string) (string, *verify.DMARCPolicy, error) {
			return verify.DMARCPolicyForReporting(ctx, res, domain)
		}
	}

	end := j.now().UTC().Truncate(24 * time.Hour)
	start := end.Add(-24 * time.Hour)

	aggs, skipped, err := j.agg.Aggregate(ctx, start, end)
	if err != nil {
		return err
	}
	if len(aggs) == 0 {
		logger.Info("dmarc: no DMARC-evaluated messages in the window; nothing to report", "window_start", start, "window_end", end)
		return nil
	}
	if skipped > 0 {
		logger.Warn("dmarc: some verification rows were not reportable and are not in the reports", "skipped", skipped)
	}

	sent := 0
	for _, agg := range aggs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// The live DNS read: the published policy (policy-published block +
		// rua endpoints) — never stored addresses, never guessed.
		recordDomain, policy, err := j.policyLookup(ctx, agg.PolicyDomain)
		if err != nil {
			logger.Debug("dmarc: reported domain publishes no usable DMARC policy; skipping", "domain", agg.PolicyDomain, "error", err)
			continue
		}
		ruas := dmarc.MailtoRuas(policy)
		if len(ruas) == 0 {
			logger.Debug("dmarc: reported domain publishes no mailto rua; skipping", "domain", recordDomain)
			continue
		}
		reportID := dmarc.OutboundAggregateReportID(start, end, j.orgName, recordDomain)
		// The report's policy-evaluated alignment uses the LIVE published
		// policy (adkim/aspf), exactly as the evaluator did when the stored
		// verdicts were computed.
		records := dmarc.BuildRecordAggregates(policy, recordDomain, agg.Rows)
		doc, err := dmarc.BuildAggregateReport(j.orgName, j.fromAddress, start, end, reportID, recordDomain, policy, records)
		if err != nil {
			logger.Error("dmarc: report build failed", "domain", recordDomain, "error", err)
			continue
		}
		req := &dmarc.OutboundReportRequest{
			ReportID:     reportID,
			PolicyDomain: recordDomain,
			WindowStart:  start,
			WindowEnd:    end,
			XML:          doc,
			To:           ruas,
			From:         j.fromAddress,
		}
		if err := j.sender.SendReport(ctx, req); err != nil {
			logger.Error("dmarc: report send failed", "domain", recordDomain, "report_id", reportID, "error", err)
			continue
		}
		sent++
		logger.Info("dmarc: report sent", "domain", recordDomain, "report_id", reportID, "rua_count", len(ruas), "records", len(records))
	}
	logger.Info("dmarc: outbound report pass complete", "reported_domains", len(aggs), "sent", sent)
	return nil
}
