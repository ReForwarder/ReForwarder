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

package repositories

import (
	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DNSProviderRepository persists per-domain DNS provider credentials
// (models.DNSProviderCredential) and publication/drift state
// (models.DNSPublicationState) for the OB-6 DNS automation layer (ADR-007).
type DNSProviderRepository struct {
	db *gorm.DB
}

func NewDNSProviderRepository(db *gorm.DB) *DNSProviderRepository {
	return &DNSProviderRepository{db: db}
}

// UpsertCredential stores the credential for one domain. One connection per
// domain: reconnecting replaces the stored credential (the operator's
// revocation path is DeleteCredential).
func (r *DNSProviderRepository) UpsertCredential(cred *models.DNSProviderCredential) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain_id"}},
		UpdateAll: true,
	}).Create(cred).Error
}

// FindCredential returns the domain's stored credential, or
// gorm.ErrRecordNotFound when the domain is not connected to a provider.
func (r *DNSProviderRepository) FindCredential(domainID uint) (*models.DNSProviderCredential, error) {
	var cred models.DNSProviderCredential
	if err := r.db.Where("domain_id = ?", domainID).First(&cred).Error; err != nil {
		return nil, err
	}
	return &cred, nil
}

// DeleteCredential removes the domain's provider credential (per-domain
// revocation). Returns whether a row was removed.
func (r *DNSProviderRepository) DeleteCredential(domainID uint) (bool, error) {
	res := r.db.Where("domain_id = ?", domainID).Delete(&models.DNSProviderCredential{})
	return res.RowsAffected > 0, res.Error
}

// FindCredentialDomains returns every domain that has a provider credential
// connected — the drift cron's scan set (ADR-007 decision 1).
func (r *DNSProviderRepository) FindCredentialDomains() ([]models.Domain, error) {
	var domains []models.Domain
	err := r.db.
		Joins("JOIN dns_provider_credentials ON dns_provider_credentials.domain_id = domains.id").
		Order("domains.id ASC").
		Find(&domains).Error
	return domains, err
}

// FindState returns the domain's publication/drift state row, or
// gorm.ErrRecordNotFound when nothing was ever published.
func (r *DNSProviderRepository) FindState(domainID uint) (*models.DNSPublicationState, error) {
	var st models.DNSPublicationState
	if err := r.db.Where("domain_id = ?", domainID).First(&st).Error; err != nil {
		return nil, err
	}
	return &st, nil
}

// UpsertState stores the publication/drift state for one domain.
func (r *DNSProviderRepository) UpsertState(st *models.DNSPublicationState) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain_id"}},
		UpdateAll: true,
	}).Create(st).Error
}
