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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobs

import (
	"context"
	"strings"
	"time"

	"github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/services/tlsrpt"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// TypeTLSRPTReport is the cron trigger for the daily outbound TLS report pass
// (SF-31 outbound half + SF-104 slice, ADR-007 §5). The job aggregates the
// previous UTC day's direct-delivery sessions per remote policy domain,
// discovers each policy domain's rua endpoints from its live _smtp._tls TXT
// record, and hands one report per domain to the wired Sender (the production
// adapter enqueues it through the standard outbound pipeline).
//
// Policy decisions baked in (documented):
//   - one daily pass, only when the window carried attributable sessions —
//     the deployment never mails external addresses on a quiet day;
//   - reports are addressed ONLY to rua endpoints read from the remote
//     domain's published DNS TXT record at send time (never to stored or
//     user-supplied addresses);
//   - a send failure is logged and dropped: the next window's report
//     supersedes the lost one (RFC 8460 reports are best-effort aggregates;
//     no retry queue is kept for them).
const TypeTLSRPTReport = "cron:tlsrpt-report"

// TLSRPTReportJob is the daily outbound TLS report pass.
type TLSRPTReportJob struct {
	db        *gorm.DB
	agg       *tlsrpt.Aggregator
	txtLookup func(ctx context.Context, domain string) ([]string, error)
	sender    tlsrpt.Sender
	// fromAddress is the generator address (mta-sts-report@<identity>); empty
	// disables the job (nothing is ever sent without a usable generator).
	fromAddress string
	// orgName is the RFC 8460 organization-name of this deployment.
	orgName string
	// now is overridable in tests.
	now func() time.Time
}

// NewTLSRPTReportJob builds the job. sender is the delivery seam; fromAddress
// and orgName come from the deployment identity in cmd wiring.
func NewTLSRPTReportJob(db *gorm.DB, agg *tlsrpt.Aggregator, sender tlsrpt.Sender, fromAddress, orgName string) *TLSRPTReportJob {
	return &TLSRPTReportJob{
		db:          db,
		agg:         agg,
		sender:      sender,
		fromAddress: strings.TrimSpace(fromAddress),
		orgName:     orgName,
		now:         time.Now,
	}
}

// SetTXTLookup overrides the _smtp._tls TXT resolver (tests; production uses
// the system resolver).
func (j *TLSRPTReportJob) SetTXTLookup(fn func(ctx context.Context, domain string) ([]string, error)) {
	if fn != nil {
		j.txtLookup = fn
	}
}

// SetNow overrides the clock (tests).
func (j *TLSRPTReportJob) SetNow(fn func() time.Time) {
	if fn != nil {
		j.now = fn
	}
}

func (j *TLSRPTReportJob) Name() string     { return "tlsrpt-report" }
func (j *TLSRPTReportJob) Schedule() string { return "35 6 * * *" } // daily 06:35 UTC

// Run implements cron.Job. The aggregation runs in-process (unlike the
// per-workspace fan-out jobs) — it reads deployment-wide transport telemetry,
// has no tenant data payload, and its output is at most a handful of reports.
func (j *TLSRPTReportJob) Run(ctx context.Context, _ *asynq.Client) error {
	if j.sender == nil || j.fromAddress == "" || j.orgName == "" {
		return nil // generator identity not configured; the pass is inert
	}
	if j.txtLookup == nil {
		j.txtLookup = tlsrpt.LookupTXT
	}

	end := j.now().UTC().Truncate(24 * time.Hour)
	start := end.Add(-24 * time.Hour)

	aggs, skipped, err := j.agg.Aggregate(ctx, start, end)
	if err != nil {
		return err
	}
	if len(aggs) == 0 {
		logger.Info("tlsrpt: no attributable sessions in the window; nothing to report", "window_start", start, "window_end", end)
		return nil
	}
	if skipped > 0 {
		logger.Warn("tlsrpt: some attempts were unattributable and are not in the report", "skipped", skipped)
	}

	byDomain := map[string][]tlsrpt.SessionAggregate{}
	var domains []string
	for _, a := range aggs {
		if _, ok := byDomain[a.PolicyDomain]; !ok {
			domains = append(domains, a.PolicyDomain)
		}
		byDomain[a.PolicyDomain] = append(byDomain[a.PolicyDomain], a)
	}

	sent := 0
	for _, domain := range domains {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ruas := j.discoverRuas(ctx, domain)
		if len(ruas) == 0 {
			logger.Debug("tlsrpt: policy domain publishes no TLS-RPT rua; skipping", "domain", domain)
			continue
		}
		reportID := tlsrpt.OutboundReportID(start, domain)
		doc, err := tlsrpt.OutboundReport(j.orgName, start, end, reportID, byDomain[domain])
		if err != nil {
			logger.Error("tlsrpt: report build failed", "domain", domain, "error", err)
			continue
		}
		req := &tlsrpt.OutboundReportRequest{
			ReportID:     reportID,
			PolicyDomain: domain,
			WindowStart:  start,
			WindowEnd:    end,
			JSON:         doc,
			To:           ruas,
			From:         j.fromAddress,
		}
		if err := j.sender.SendReport(ctx, req); err != nil {
			logger.Error("tlsrpt: report send failed", "domain", domain, "report_id", reportID, "error", err)
			continue
		}
		sent++
		logger.Info("tlsrpt: report sent", "domain", domain, "report_id", reportID, "rua_count", len(ruas))
	}
	logger.Info("tlsrpt: outbound report pass complete", "policy_domains", len(domains), "sent", sent)
	return nil
}

// discoverRuas reads the policy domain's live _smtp._tls TXT record and
// returns its mailto rua endpoints. DNS failures yield nothing (the domain is
// skipped this pass — never guessed).
func (j *TLSRPTReportJob) discoverRuas(ctx context.Context, domain string) []string {
	txts, err := j.txtLookup(ctx, tlsrpt.TLSRPTTXTLabel+"."+domain)
	if err != nil {
		return nil
	}
	var ruas []string
	for _, txt := range txts {
		for _, r := range tlsrpt.ParseTLSRPTRecord(txt) {
			ruas = append(ruas, r)
		}
	}
	return uniqueStrings(ruas)
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// NewTLSRPTReportTask builds the asynq task for the cron scheduler.
func NewTLSRPTReportTask(opts ...asynq.Option) (*asynq.Task, error) {
	return asynq.NewTask(TypeTLSRPTReport, nil, opts...), nil
}

// compile-time assertion that the job satisfies the cron contract.
var _ cron.CronJob = (*TLSRPTReportJob)(nil)
