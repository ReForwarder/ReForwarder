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

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/dnsmanager"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

// DNSDriftCheckJob is the OB-6 drift re-verification cron (ADR-007 decision 1,
// GAP-08 residual): hourly, for every domain connected to a DNS provider, it
// compares the live provider records against the published baseline and
// persists the verdict (ok / drift / unknown). A drift transition records an
// audit alert (domain.dns_drift_detected / _resolved, fired by the service)
// and the verdict surfaces through the domain's DNS status endpoint — the
// health surface the operator watches.
//
// Failures are per-domain and never abort the sweep: a provider outage must
// not stop checking the other domains (the dkim-retire job's posture).
type DNSDriftCheckJob struct {
	svc *dnsmanager.Service
}

func NewDNSDriftCheckJob(svc *dnsmanager.Service) *DNSDriftCheckJob {
	return &DNSDriftCheckJob{svc: svc}
}

func (j *DNSDriftCheckJob) Name() string     { return "dns-drift-check" }
func (j *DNSDriftCheckJob) Schedule() string { return "23 * * * *" } // hourly at :23 UTC

func (j *DNSDriftCheckJob) Run(ctx context.Context, _ *asynq.Client) error {
	if j.svc == nil {
		return nil
	}
	domains, err := j.svc.ConnectedDomains()
	if err != nil {
		logger.Error("dns-drift-check: failed to list connected domains", "error", err)
		return nil // a listing failure must not abort the cron run
	}

	drift := 0
	checked := 0
	for i := range domains {
		d := domains[i]
		view, err := j.svc.CheckDrift(ctx, &d)
		if err != nil {
			logger.Warn("dns-drift-check: domain check failed (skipped)",
				"domain", d.Domain, "error", err)
			continue
		}
		checked++
		if view != nil && view.Status == models.DNSDriftFound {
			drift++
		}
	}
	if checked > 0 {
		logger.Info("dns-drift-check: sweep complete", "checked", checked, "drifted", drift)
	}
	return nil
}
