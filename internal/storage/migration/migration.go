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

package migration

import (
	"fmt"

	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// Run executes all schema migrations and adds FK constraints.
func Run(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&models.User{},
		&models.Plan{},
		&models.Workspace{},
		&models.WorkspaceMember{},
		&models.WorkspaceInvitation{},
		&models.OAuthProvider{},
		&models.OAuthAccount{},
		&models.WorkspaceSSOConfig{},
		&models.APIKey{},
		&models.APIKeyUsageDaily{},
		&models.SMTPCredential{},
		&models.Email{},
		&models.InboundEmail{},
		&models.InboundAuthResult{},
		&models.InboundRoute{},         // OB-2: routing rules (ADR-002 decision 1)
		&models.InboundForwardTask{},   // OB-2: durable forwarding queue (ADR-002 decision 2)
		&models.InboundForwardTarget{}, // OB-2: confirmed forward destinations (ADR-002 decision 2)
		&models.InboundQuarantine{},    // OB-2: quarantine review (ADR-002 decision 3)
		&models.DMARCReport{},
		// OB-6 W4 (SF-31, ADR-007 §5): inbound SMTP TLS reports per the
		// ADR-002 ingestion pattern — additive table, idempotent on
		// (user_id, report_id, policy_domain).
		&models.TLSRPTReport{},
		&models.StyleSheet{},
		&models.Template{},
		&models.TemplateVersion{},
		&models.TemplateLocalization{},
		&models.Language{},
		&models.SMTPServer{},
		&models.Server{},
		// SF-08 (persistent multi-queue manager): per-recipient retry/notify
		// schedule rows, additively consulted at claim time to schedule the
		// retry and carrying the per-recipient notify addresses. Additive
		// table; emails.expires_at (the TTL deadline the expiry sweep honors)
		// is added by the Email AutoMigrate above. No upgrade step is needed:
		// new table + new nullable column with defaults upgrade deployed
		// instances in place (the OB-6 W3 ip_pools precedent).
		&models.EmailQueueSchedule{},
		&models.Webhook{},
		&models.Domain{},
		&models.DomainSigningKey{},
		&models.MTADeliveryAttempt{},
		&models.Bounce{},
		&models.Suppression{},
		&models.UnsubscribeList{},
		&models.Contact{},
		&models.ContactList{},
		&models.ContactListMember{},
		&models.Event{},
		&models.SecurityBan{},
		&models.SecurityEvent{},
		&models.SecurityAllowlist{},
		&models.Setting{},
		&models.SettingHistory{}, // SF-100: per-key settings change history + rollback
		&models.UserSetting{},
		&models.WorkspaceSetting{},
		&models.WebhookDelivery{},
		&models.WebhookDeliveryAttempt{},
		&models.EmailEvent{},
		&models.IncomingWebhookEvent{},
		&models.Session{},
		&models.WebAuthnCredential{},
		&models.UserPushSubscription{}, // SF-89: browser Web Push subscriptions (user-scoped)
		&models.UserEmailVerification{},
		&models.PasswordResetToken{},
		&models.UserRecoveryCode{},
		&models.UserAppPassword{}, // SF-35/SF-67: app passwords for the mail-client surfaces (ADR-004)
		&models.RefreshToken{},    // CA-03: rotating refresh tokens
		&models.Subscriber{},
		&models.SubscriberList{},
		&models.SubscriberListMember{},
		&models.SubscriberListUnsubscribe{},
		&models.Segment{},
		&models.Campaign{},
		&models.CampaignMessage{},
		&models.TrackedLink{},
		&models.TrackingEvent{},
		&models.Form{},
		&models.Message{},
		&models.MessageReply{},
		&models.MessageFilter{},
		&models.Notification{},
		&models.Announcement{},
		&models.UpgradeStep{},
		&models.UpdateStatus{},
		&models.Automation{},
		&models.AutomationJourney{},
		&models.AutomationNodeRun{},
		&models.AutomationTriggerLog{},
		// OB-3 mailstore (ADR-003): additive hosted-mailbox storage. Metadata
		// only — bodies/attachments are content-addressed blobs in blob.Store.
		&models.Mailbox{},
		&models.MailFolder{},
		&models.MailMessage{},
		&models.MailBlobRef{}, // GAP-45 storage half: content-hash ref counts for dedup + GC
		// OB-5 compose (ADR-005 W3): per-user compose-attachment staging rows —
		// ownership + ADR-006 malware verdict per uploaded blob ref. Additive
		// table; the attachment bytes themselves live in the blob store.
		&models.MailAttachmentUpload{},
		// OB-6 W3 (ADR-007 decision 7): IP pools, warm-up schedules and
		// reputation rollups — additive tables only; every row carries
		// workspace_id (or an explicit nil for deployment-wide pools), per
		// ADR-008 §1. domains.pool_id is added by the Domain AutoMigrate
		// above; mta_delivery_attempts.source_ip likewise. No upgrade step is
		// needed: the additions are new tables/columns with defaults, so the
		// AutoMigrate upgrades existing deployments in place (the
		// mta_delivery_attempts OB-1 precedent).
		&models.IPPool{},
		&models.IPPoolMember{},
		&models.IPWarmupSchedule{},
		&models.IPReputationRollup{},
		// OB-6 W1 DNS automation (ADR-007 decision 1): per-domain provider
		// credentials (encrypted at rest, SF-78) + the publication/drift
		// baseline the drift re-verification cron compares against. Additive
		// tables; every row carries the workspace ownership columns per
		// ADR-008 §1, and domain_id is unique (one connection / baseline per
		// domain).
		&models.DNSProviderCredential{},
		&models.DNSPublicationState{},
	); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	// run constraints
	runConstraints(db)

	// OB-3 (ADR-003 decision 3): the mail_messages.search_tsv tsvector
	// generated column + GIN index are SQL GORM cannot express. Ensured here
	// so fresh installs get them (the upgrade framework seals its registry on
	// a fresh database without applying steps) and mirrored in the
	// "2026-11-07-mailstore-fts" upgrade step for existing databases. Both are
	// idempotent.
	if err := EnsureMailstoreSearch(db); err != nil {
		return fmt.Errorf("failed to ensure mailstore search schema: %w", err)
	}

	logger.Info("database migrated")
	return nil
}
