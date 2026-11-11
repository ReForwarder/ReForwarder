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
	"errors"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
)

// ErrPoolHasDomains is returned when a pool that still has domains assigned
// to it is deleted — the assignment must be cleared first so no domain
// silently loses its source-IP configuration.
var ErrPoolHasDomains = errors.New("ip pool still has domains assigned")

// IPPoolRepository owns the IP pool substrate (OB-6 W3, ADR-007 decision 7):
// pools + member IPs, the domain assignment, per-domain warm-up schedules,
// and the reputation rollups. Every workspace-facing query filters through
// the pool's/workspace's workspace_id — deployment-wide pools carry a nil
// workspace_id and are visible to (but not writable by) workspaces.
type IPPoolRepository struct {
	db *gorm.DB
}

func NewIPPoolRepository(db *gorm.DB) *IPPoolRepository {
	return &IPPoolRepository{db: db}
}

// ---------- pools ----------

func (r *IPPoolRepository) CreatePool(pool *models.IPPool) error {
	return r.db.Create(pool).Error
}

func (r *IPPoolRepository) UpdatePool(pool *models.IPPool) error {
	return r.db.Save(pool).Error
}

func (r *IPPoolRepository) FindPoolByID(id uint) (*models.IPPool, error) {
	var p models.IPPool
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// visiblePoolsCond is the WHERE fragment making a pool visible to a scope:
// the workspace's own pools plus the deployment-wide shared ones.
const visiblePoolsCond = "(ip_pools.workspace_id IS NULL OR ip_pools.workspace_id = ?)"

// FindVisiblePools lists the pools a workspace may send from: its own plus
// the deployment-wide ones. The admin surface uses FindAllPools.
func (r *IPPoolRepository) FindVisiblePools(workspaceID uint, limit, offset int) ([]models.IPPool, int64, error) {
	var items []models.IPPool
	var total int64

	r.db.Model(&models.IPPool{}).Where(visiblePoolsCond, workspaceID).Count(&total)
	if err := r.db.Where(visiblePoolsCond, workspaceID).
		Order("name ASC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// FindAllPools lists every pool (admin surface, deployment-wide).
func (r *IPPoolRepository) FindAllPools(limit, offset int) ([]models.IPPool, int64, error) {
	var items []models.IPPool
	var total int64

	r.db.Model(&models.IPPool{}).Count(&total)
	if err := r.db.Order("name ASC").
		Limit(limit).Offset(offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// DeletePool removes an empty pool. A pool still referenced by domains is
// refused with ErrPoolHasDomains — assignment clearing is a deliberate,
// auditable step.
func (r *IPPoolRepository) DeletePool(id uint) error {
	var refs int64
	if err := r.db.Model(&models.Domain{}).Where("pool_id = ?", id).Count(&refs).Error; err != nil {
		return err
	}
	if refs > 0 {
		return ErrPoolHasDomains
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("pool_id = ?", id).Delete(&models.IPPoolMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.IPPool{}, id).Error
	})
}

// DomainCountForPool reports how many domains currently send from the pool.
func (r *IPPoolRepository) DomainCountForPool(poolID uint) int64 {
	var n int64
	r.db.Model(&models.Domain{}).Where("pool_id = ?", poolID).Count(&n)
	return n
}

// ---------- members ----------

func (r *IPPoolRepository) AddMember(m *models.IPPoolMember) error {
	return r.db.Create(m).Error
}

func (r *IPPoolRepository) FindMemberByID(id uint) (*models.IPPoolMember, error) {
	var m models.IPPoolMember
	if err := r.db.First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *IPPoolRepository) UpdateMember(m *models.IPPoolMember) error {
	return r.db.Save(m).Error
}

func (r *IPPoolRepository) RemoveMember(id uint) error {
	return r.db.Delete(&models.IPPoolMember{}, id).Error
}

func (r *IPPoolRepository) PoolMembers(poolID uint) ([]models.IPPoolMember, error) {
	var out []models.IPPoolMember
	if err := r.db.Where("pool_id = ?", poolID).Order("ip ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// EnabledMemberIPs returns the IPs the executor may rotate across for a
// pool: enabled members only, in insertion order (the round-robin sequence
// follows it).
func (r *IPPoolRepository) EnabledMemberIPs(poolID uint) []string {
	var rows []models.IPPoolMember
	if err := r.db.Where("pool_id = ? AND enabled = ?", poolID, true).
		Order("id ASC").Find(&rows).Error; err != nil {
		return nil
	}
	ips := make([]string, 0, len(rows))
	for _, m := range rows {
		ips = append(ips, m.IP)
	}
	return ips
}

// ---------- domain assignment ----------

// SetDomainPool points (or clears, poolID nil) a domain's pool assignment.
// The caller (handler) validates visibility; the repository just writes.
func (r *IPPoolRepository) SetDomainPool(domainID uint, poolID *uint) error {
	updates := map[string]any{"pool_id": poolID}
	return r.db.Model(&models.Domain{}).Where("id = ?", domainID).Updates(updates).Error
}

// EnabledMemberIPsForDomain resolves the source-IP candidates for one
// sending domain: its pool's enabled members. Empty when the domain has no
// pool or the pool has no enabled members (the worker then falls back to the
// domain's static bind IP).
func (r *IPPoolRepository) EnabledMemberIPsForDomain(domainID uint) []string {
	var d models.Domain
	if err := r.db.Select("id", "pool_id").First(&d, domainID).Error; err != nil || d.PoolID == nil {
		return nil
	}
	return r.EnabledMemberIPs(*d.PoolID)
}

// ---------- warm-up schedules ----------

// UpsertWarmup replaces the domain's active warm-up schedule: the previous
// active rows are deactivated (kept for the record), the new one is inserted
// active. Runs in one transaction so a delivery can never see two actives.
func (r *IPPoolRepository) UpsertWarmup(s *models.IPWarmupSchedule) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.IPWarmupSchedule{}).
			Where("domain_id = ? AND active = ?", s.DomainID, true).
			Update("active", false).Error; err != nil {
			return err
		}
		return tx.Create(s).Error
	})
}

// ActiveWarmupForDomain returns the domain's active warm-up schedule, or nil
// (no error) when none is active.
func (r *IPPoolRepository) ActiveWarmupForDomain(domainID uint) (*models.IPWarmupSchedule, error) {
	var s models.IPWarmupSchedule
	err := r.db.Where("domain_id = ? AND active = ?", domainID, true).
		Order("id DESC").First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindWarmupByID loads one schedule row (scope checks at the handler).
func (r *IPPoolRepository) FindWarmupByID(id uint) (*models.IPWarmupSchedule, error) {
	var s models.IPWarmupSchedule
	if err := r.db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// ---------- reputation rollups ----------

// UpsertRollups writes rollup rows idempotently: an existing row for the same
// (workspace_id, ip, domain, day) key is overwritten with the fresh counts —
// re-running a day never double-counts (ADR-007 decision 7).
func (r *IPPoolRepository) UpsertRollups(rows []models.IPReputationRollup) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			row := &rows[i]
			var existing models.IPReputationRollup
			q := tx.Where("ip = ? AND domain = ? AND day = ?", row.IP, row.Domain, row.Day)
			if row.WorkspaceID != nil {
				q = q.Where("workspace_id = ?", *row.WorkspaceID)
			} else {
				q = q.Where("workspace_id IS NULL")
			}
			err := q.First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := tx.Create(row).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			row.ID = existing.ID
			row.CreatedAt = existing.CreatedAt
			if err := tx.Save(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RollupsForDomain returns the stored rollup rows for one sender domain
// inside the window, workspace-scoped, oldest day first — the trend input.
func (r *IPPoolRepository) RollupsForDomain(scope ResourceScope, domain string, from, to time.Time) ([]models.IPReputationRollup, error) {
	var out []models.IPReputationRollup
	q := r.db.Where("domain = ? AND day >= ? AND day < ?", domain, from, to)
	if scope.WorkspaceID == nil {
		// No workspace context: the scoped query matches nothing, mirroring
		// ApplyScope's fail-closed behavior.
		return []models.IPReputationRollup{}, nil
	}
	q = q.Where("workspace_id = ?", *scope.WorkspaceID)
	if err := q.Order("day ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// RollupsForIP returns the stored rollup rows for one source IP across every
// workspace (admin surface, deployment-wide), newest day first.
func (r *IPPoolRepository) RollupsForIP(ip string, from, to time.Time, limit int) ([]models.IPReputationRollup, error) {
	if limit <= 0 {
		limit = 500
	}
	var out []models.IPReputationRollup
	if err := r.db.Where("ip = ? AND day >= ? AND day < ?", ip, from, to).
		Order("day DESC").Limit(limit).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
