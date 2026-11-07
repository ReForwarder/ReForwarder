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

package jobs

import (
	"context"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// DKIMRetireJob automates the tail end of DKIM key rotation (SF-104, R-02
// semantics): a key left in `retiring` after a rotate is flipped to `retired`
// — which makes it deletable — once BOTH preconditions hold:
//
//  1. it has been in `retiring` for at least the grace period
//     (dkim_retire_grace_days, default 14; 0 retires an eligible key on the
//     next run; a negative value disables the pass entirely). Age is measured
//     from updated_at, which RotateActive sets when it demotes the key —
//     there is no dedicated retiring_at column, and a retiring key is not
//     otherwise updated (it no longer signs, so DNS verification never
//     touches it).
//  2. the domain's replacement Active key has its DNS record verified as
//     published (dns_state = published). If the replacement's TXT record is
//     still pending, the old key stays in `retiring` — retiring it before the
//     replacement signs would leave the domain unsigned.
//
// Auto-rotation (generating new keys) is deliberately NOT part of this job;
// rotation stays API-driven (POST .../signing-keys/rotate).
type DKIMRetireJob struct {
	db          *gorm.DB
	signingKeys *repositories.DomainSigningKeyRepository
	domains     *repositories.DomainRepository
	settings    *settings.Provider
	auditLog    *audit.Logger
}

func NewDKIMRetireJob(db *gorm.DB, sp *settings.Provider) *DKIMRetireJob {
	return &DKIMRetireJob{
		db:          db,
		signingKeys: repositories.NewDomainSigningKeyRepository(db),
		domains:     repositories.NewDomainRepository(db),
		settings:    sp,
		auditLog:    audit.NewLogger(eventbus.New(repositories.NewEventRepository(db))),
	}
}

func (j *DKIMRetireJob) Name() string     { return "dkim-retire" }
func (j *DKIMRetireJob) Schedule() string { return "30 3 * * *" } // daily at 03:30 UTC

// domainDKIMKeyAutoRetired is the audit action recorded for every automated
// transition (distinct from the operator-driven domain.signing_key.retired).
const domainDKIMKeyAutoRetired = "domain.dkim_key_auto_retired"

func (j *DKIMRetireJob) Run(_ context.Context, _ *asynq.Client) error {
	graceDays := j.settings.DKIMRetireGraceDays()
	if graceDays < 0 {
		return nil // disabled by configuration
	}
	grace := time.Duration(graceDays) * 24 * time.Hour

	var keys []models.DomainSigningKey
	if err := j.db.Where("status = ?", models.DomainSigningStatusRetiring).
		Order("id ASC").
		Find(&keys).Error; err != nil {
		logger.Error("dkim-retire: failed to list retiring keys", "error", err)
		return nil // a listing failure must not abort the cron run
	}

	retired := 0
	for _, key := range keys {
		if time.Since(key.UpdatedAt) < grace {
			continue // still inside the grace period
		}

		domain, err := j.domains.FindByID(key.DomainID)
		if err != nil {
			logger.Warn("dkim-retire: retiring key has no domain row", "key_id", key.ID, "error", err)
			continue
		}
		// "Enabled" here means the domain is verified for ownership — only
		// verified domains send and sign mail, so only theirs retire.
		if !domain.IsOwnershipVerified() {
			continue
		}

		// Replacement precondition: the domain's Active key must already be
		// confirmed published in DNS. No Active key at all → refuse too.
		replacement, err := j.signingKeys.FindActiveForDomainID(key.DomainID)
		if err != nil {
			logger.Info("dkim-retire: no active replacement, keeping key retiring",
				"key_id", key.ID, "domain_id", key.DomainID)
			continue
		}
		if replacement.DNSState != models.DomainSigningDNSPublished {
			logger.Info("dkim-retire: replacement DNS not verified, keeping key retiring",
				"key_id", key.ID, "replacement_id", replacement.ID, "dns_state", replacement.DNSState)
			continue
		}

		ok, err := j.signingKeys.MarkRetired(key.ID)
		if err != nil {
			logger.Error("dkim-retire: failed to retire key", "key_id", key.ID, "error", err)
			continue
		}
		if !ok {
			// Lost a concurrent retire (or the row changed) — nothing to do.
			continue
		}

		retired++
		j.auditLog.LogScoped(nil, 0, "system", "",
			domainDKIMKeyAutoRetired,
			"DKIM key automatically retired after grace period",
			map[string]any{
				"domain_id":      key.DomainID,
				"domain":         domain.Domain,
				"selector":       key.Selector,
				"key_id":         key.ID,
				"grace_days":     graceDays,
				"replacement_id": replacement.ID,
			})
	}

	if retired > 0 {
		logger.Info("dkim-retire: retired expired signing keys", "count", retired)
	}
	return nil
}
