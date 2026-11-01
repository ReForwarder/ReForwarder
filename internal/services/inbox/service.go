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

package inbox

import (
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"gorm.io/gorm"
)

// Service maintains the in-app inbox: the health alerts Posta derives for a
// workspace, and the announcements a platform administrator broadcasts.
//
// It is deliberately separate from services/notification, which sends email.
// The two answer different questions — email is a push into somebody's day, the
// inbox is what the product says about itself when you open it — and a condition
// worth a banner is usually not worth a mail.
type Service struct {
	db   *gorm.DB
	repo *repositories.NotificationRepository
}

func NewService(db *gorm.DB, repo *repositories.NotificationRepository) *Service {
	return &Service{db: db, repo: repo}
}

// SyncWorkspaceHealth reconciles one member's alerts for one workspace against
// the current snapshot: firing rules are upserted, and anything that was firing
// and no longer is gets resolved.
//
// It runs on the dashboard read rather than on a timer because the snapshot is
// already in hand there, and because a banner that lags the numbers printed next
// to it would be worse than no banner.
func (s *Service) SyncWorkspaceHealth(userID uint, workspaceID uint, role models.WorkspaceRole, snap Snapshot) error {
	ws := workspaceID
	firing := evaluate(snap)

	keep := make([]string, 0, len(firing))
	for _, r := range firing {
		if roleRank(role) < roleRank(r.minRole) {
			// A viewer cannot verify a domain or rotate a key. Telling them is
			// noise they have no way to clear.
			continue
		}
		keep = append(keep, r.dedupKey)
		n := &models.Notification{
			UserID:      userID,
			WorkspaceID: &ws,
			Kind:        models.NotificationKindAlert,
			Category:    r.category,
			Severity:    r.severity,
			Title:       r.title,
			Body:        r.body,
			Link:        r.link,
			ActionText:  r.action,
			DedupKey:    r.dedupKey,
			Fingerprint: r.fingerprint,
		}
		if err := s.repo.Upsert(n); err != nil {
			return err
		}
	}

	return s.repo.ResolveExcept(userID, &ws, keep)
}

// Announce broadcasts a platform notice to every user who can sign in, and
// records how many deliveries it produced.
//
// Fan-out writes one row per user rather than a single shared row so that read
// and dismissed state stay per-person, which is the whole point of the inbox.
// Posta is self-hosted and user counts are small; the insert is batched.
func (s *Service) Announce(a *models.Announcement) error {
	var users []models.User
	if err := s.db.Model(&models.User{}).Select("id").Where("active = ?", true).Find(&users).Error; err != nil {
		return err
	}

	now := time.Now().UTC()
	a.SentAt = &now
	a.Recipients = len(users)
	if err := s.db.Create(a).Error; err != nil {
		return err
	}

	rows := make([]models.Notification, 0, len(users))
	for i := range users {
		rows = append(rows, models.Notification{
			UserID:      users[i].ID,
			Kind:        models.NotificationKindAnnouncement,
			Category:    models.NotificationCategoryPlatform,
			Severity:    a.Severity,
			Title:       a.Title,
			Body:        a.Message,
			Link:        a.Link,
			DedupKey:    AnnouncementKey(a.ID),
			Fingerprint: fmt.Sprintf("announcement=%d", a.ID),
		})
	}
	if err := s.repo.CreateBatch(rows); err != nil {
		return err
	}
	return nil
}

// Retract removes an announcement and the deliveries it made. An operator who
// broadcast the wrong thing should be able to take it back, not just append a
// correction to everyone's inbox.
func (s *Service) Retract(id uint) error {
	if _, err := s.repo.DeleteForAnnouncement(AnnouncementKey(id)); err != nil {
		return err
	}
	return s.db.Delete(&models.Announcement{}, id).Error
}

// Deliver posts a single item to one user. Cron jobs that already email a user
// about a condition use it so the same news is waiting in the UI.
func (s *Service) Deliver(userID uint, workspaceID *uint, n models.Notification) error {
	n.UserID = userID
	n.WorkspaceID = workspaceID
	if n.Kind == "" {
		n.Kind = models.NotificationKindAlert
	}
	return s.repo.Upsert(&n)
}

// AnnouncementKey is the dedup key every delivery of one announcement shares,
// which is also how a retraction finds them again.
func AnnouncementKey(id uint) string { return fmt.Sprintf("announcement:%d", id) }
