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

package repositories

import (
	"time"

	"github.com/goposta/posta/internal/models"
)

// This file adds the R-08 A/B winner and R-09 timezone queries. They live in
// their own file so the winner/timzeone feature surface stays reviewable as
// one unit; the methods extend the existing campaign repositories.

// ABVariantStats returns the per-variant engagement snapshot for one campaign:
// the delivered (status=sent) message count per variant and how many of those
// messages were opened / clicked (the denormalized first-open/first-click
// timestamps on campaign_messages). Variants with no sent messages still
// appear with zero counts, so the winner check sees the full test group.
func (r *CampaignMessageRepository) ABVariantStats(campaignID uint) ([]models.ABVariantStat, error) {
	var stats []models.ABVariantStat
	err := r.db.Model(&models.CampaignMessage{}).
		Select(`variant,
			COUNT(*) FILTER (WHERE status = ?) AS sent,
			COUNT(*) FILTER (WHERE status = ? AND opened_at IS NOT NULL) AS opened,
			COUNT(*) FILTER (WHERE status = ? AND clicked_at IS NOT NULL) AS clicked`,
			models.CampaignMsgSent, models.CampaignMsgSent, models.CampaignMsgSent).
		Where("campaign_id = ? AND variant <> ''", campaignID).
		Group("variant").
		Order("variant").
		Scan(&stats).Error
	return stats, err
}

// CountPendingHeld counts the still-pending campaign messages that carry no
// variant — the hold-back remainder waiting for the A/B winner send. A/B
// campaigns where this is 0 at winner time were fully assigned at start
// (legacy flow or a 100% test group): the winner selection is then
// analytics-only.
func (r *CampaignMessageRepository) CountPendingHeld(campaignID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.CampaignMessage{}).
		Where("campaign_id = ? AND status = ? AND variant = ?", campaignID, models.CampaignMsgPending, "").
		Count(&count).Error
	return count, err
}

// AssignVariantToHeld marks the hold-back remainder with the winning variant,
// unblocking the normal batch loop to send it (the winner's subject override
// then applies through the existing per-message variant match). Idempotent.
// Returns the number of messages that now carry the winner variant.
func (r *CampaignMessageRepository) AssignVariantToHeld(campaignID uint, variant string) (int64, error) {
	res := r.db.Model(&models.CampaignMessage{}).
		Where("campaign_id = ? AND status = ? AND variant = ?", campaignID, models.CampaignMsgPending, "").
		Update("variant", variant)
	return res.RowsAffected, res.Error
}

// MarkABWinner persists the A/B winner decision with an exactly-once guard:
// the update only lands while ab_test_winner_sent is still false, so two
// racing batch workers can never both claim the selection. Returns false when
// the guard lost the race (another worker already selected the winner).
func (r *CampaignRepository) MarkABWinner(id uint, winner string, stats models.TemplateData) (bool, error) {
	updates := map[string]interface{}{
		"ab_test_winner":      winner,
		"ab_test_winner_sent": true,
		"updated_at":          time.Now(),
	}
	if stats != nil {
		updates["ab_test_winner_stats"] = stats
	}
	res := r.db.Model(&models.Campaign{}).
		Where("id = ? AND ab_test_winner_sent = ?", id, false).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// PendingTimezoneCounts maps the subscriber timezones of a campaign's
// still-pending messages to their message counts. The campaign batch loop uses
// it to schedule the next tick at the earliest send-window opening across all
// deferred timezone groups (R-09), not just the sample a batch fetch returned.
func (r *CampaignMessageRepository) PendingTimezoneCounts(campaignID uint) (map[string]int64, error) {
	type row struct {
		Timezone string
		Count    int64
	}
	var rows []row
	err := r.db.Model(&models.CampaignMessage{}).
		Select("COALESCE(subscribers.timezone, '') AS timezone, COUNT(*) AS count").
		Joins("JOIN subscribers ON subscribers.id = campaign_messages.subscriber_id").
		Where("campaign_messages.campaign_id = ? AND campaign_messages.status = ?", campaignID, models.CampaignMsgPending).
		Group("subscribers.timezone").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, r := range rows {
		counts[r.Timezone] = r.Count
	}
	return counts, nil
}

// TimezoneCounts maps every campaign message's subscriber timezone to its
// recipient count — the timezone distribution the campaign detail page shows
// for timezone-aware sending (R-09). Unlike PendingTimezoneCounts this covers
// all messages, sent ones included.
func (r *CampaignMessageRepository) TimezoneCounts(campaignID uint) (map[string]int64, error) {
	type row struct {
		Timezone string
		Count    int64
	}
	var rows []row
	err := r.db.Model(&models.CampaignMessage{}).
		Select("COALESCE(subscribers.timezone, '') AS timezone, COUNT(*) AS count").
		Joins("JOIN subscribers ON subscribers.id = campaign_messages.subscriber_id").
		Where("campaign_messages.campaign_id = ?", campaignID).
		Group("subscribers.timezone").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, r := range rows {
		counts[r.Timezone] = r.Count
	}
	return counts, nil
}
