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
	"strings"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// DomainSigningKeyRepository persists DKIM signing keys per verified domain.
type DomainSigningKeyRepository struct {
	db *gorm.DB
}

func NewDomainSigningKeyRepository(db *gorm.DB) *DomainSigningKeyRepository {
	return &DomainSigningKeyRepository{db: db}
}

func (r *DomainSigningKeyRepository) Create(k *models.DomainSigningKey) error {
	return r.db.Create(k).Error
}

func (r *DomainSigningKeyRepository) Update(k *models.DomainSigningKey) error {
	return r.db.Save(k).Error
}

func (r *DomainSigningKeyRepository) Delete(id uint) error {
	return r.db.Delete(&models.DomainSigningKey{}, id).Error
}

func (r *DomainSigningKeyRepository) FindByID(id uint) (*models.DomainSigningKey, error) {
	var k models.DomainSigningKey
	if err := r.db.First(&k, id).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// FindByDomainID lists a domain's keys, newest first (any status).
func (r *DomainSigningKeyRepository) FindByDomainID(domainID uint) ([]models.DomainSigningKey, error) {
	var keys []models.DomainSigningKey
	if err := r.db.Where("domain_id = ?", domainID).
		Order("id DESC").Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// FindActiveByDomain returns the key that should sign mail for a sending
// domain: the newest Active key whose DNS record has been confirmed. Domain
// names are matched case-insensitively. gorm.ErrRecordNotFound means "no
// signing key for this domain" — a valid mode (unsigned / upstream CNAME
// signer), not an error.
func (r *DomainSigningKeyRepository) FindActiveByDomain(domain string) (*models.DomainSigningKey, error) {
	var k models.DomainSigningKey
	err := r.db.
		Joins("JOIN domains ON domains.id = domain_signing_keys.domain_id").
		Where("domains.domain = ? AND domain_signing_keys.status = ? AND domain_signing_keys.dns_state = ?",
			strings.ToLower(strings.TrimSpace(domain)),
			models.DomainSigningStatusActive,
			models.DomainSigningDNSPublished).
		Order("domain_signing_keys.id DESC").
		First(&k).Error
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// FindActiveForDomainID returns the Active key of one domain by its id —
// used by the rotation endpoint to find what it is replacing.
func (r *DomainSigningKeyRepository) FindActiveForDomainID(domainID uint) (*models.DomainSigningKey, error) {
	var k models.DomainSigningKey
	err := r.db.Where("domain_id = ? AND status = ?", domainID, models.DomainSigningStatusActive).
		Order("id DESC").First(&k).Error
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// RotateActive atomically demotes the domain's current Active key to Retiring
// and inserts the replacement as Active. The UPDATE ... WHERE status='active'
// is a CAS: two concurrent rotations cannot both demote the same key (the
// second sees no active row). The new key is created with status Active and
// dns_state pending-retry — the DNS record must be published and verified
// before it signs mail.
func (r *DomainSigningKeyRepository) RotateActive(domainID uint, newKey *models.DomainSigningKey) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.DomainSigningKey{}).
			Where("domain_id = ? AND status = ?", domainID, models.DomainSigningStatusActive).
			Update("status", models.DomainSigningStatusRetiring).Error; err != nil {
			return err
		}
		return tx.Create(newKey).Error
	})
}

// MarkRetired completes the manual retirement step (R-02): the operator flips
// a Retiring key to Retired once receivers no longer see its signatures, which
// makes it deletable. The UPDATE ... WHERE status='retiring' is a CAS — an
// Active key (rotate it first), an already-Retired key, or a key that lost a
// concurrent retire matches no row and returns false. Retired keys sign
// nothing (CanSign is false for anything but Active+published).
func (r *DomainSigningKeyRepository) MarkRetired(id uint) (bool, error) {
	res := r.db.Model(&models.DomainSigningKey{}).
		Where("id = ? AND status = ?", id, models.DomainSigningStatusRetiring).
		Update("status", models.DomainSigningStatusRetired)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
