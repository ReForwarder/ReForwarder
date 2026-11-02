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

package seeder

import (
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
)

// SeedDefaultSettings creates default platform settings if they don't already exist.
func SeedDefaultSettings(repo *repositories.SettingRepository) {
	// Content-retention windows default to the record retention so that an upgrade
	// never starts scrubbing content earlier than emails were already kept.
	retentionDays := "30"
	if s, err := repo.FindByKey("retention_days"); err == nil && s.Value != "" {
		retentionDays = s.Value
	}

	defaults := []models.Setting{
		{Key: "registration_enabled", Value: models.SettingFalse, Type: models.SettingTypeBool},
		{Key: "require_email_verification", Value: models.SettingTrue, Type: models.SettingTypeBool},
		{Key: "require_domain_verification", Value: models.SettingTrue, Type: models.SettingTypeBool},
		{Key: "default_rate_limit_hourly", Value: "100", Type: models.SettingTypeInt},
		{Key: "default_rate_limit_daily", Value: "1000", Type: models.SettingTypeInt},
		{Key: "max_batch_size", Value: "100", Type: models.SettingTypeInt},
		{Key: "max_attachment_size_mb", Value: "10", Type: models.SettingTypeInt},
		{Key: "retention_days", Value: "30", Type: models.SettingTypeInt},
		{Key: "email_body_retention_days", Value: retentionDays, Type: models.SettingTypeInt},
		{Key: "email_attachment_retention_days", Value: retentionDays, Type: models.SettingTypeInt},
		{Key: "global_bounce_threshold", Value: "5", Type: models.SettingTypeInt},
		{Key: "smtp_timeout_seconds", Value: "30", Type: models.SettingTypeInt},
		{Key: "maintenance_mode", Value: models.SettingFalse, Type: models.SettingTypeBool},
		{Key: "allowed_signup_domains", Value: "", Type: models.SettingTypeString},
		{Key: "two_factor_required", Value: models.SettingFalse, Type: models.SettingTypeBool},
		{Key: "login_rate_limit_count", Value: "10", Type: models.SettingTypeInt},
		{Key: "login_rate_limit_window_minutes", Value: "15", Type: models.SettingTypeInt},
		{Key: "audit_log_retention_days", Value: "90", Type: models.SettingTypeInt},
		{Key: "webhook_delivery_retention_days", Value: "30", Type: models.SettingTypeInt},
		{Key: "email_events_retention_days", Value: "180", Type: models.SettingTypeInt},
		// Content of sent email is visible in the dashboard by default;
		// operators can flip this off for privacy-sensitive installations.
		{Key: "email_content_visibility", Value: models.SettingTrue, Type: models.SettingTypeBool},
		{Key: "custom_headers_enabled", Value: models.SettingFalse, Type: models.SettingTypeBool},
		{Key: "password_reset_enabled", Value: models.SettingFalse, Type: models.SettingTypeBool},
		// Tracking for all email (links + open pixel). Replaces the old
		// campaign-only key, which is migrated below.
		{Key: "email_tracking_enabled", Value: models.SettingTrue, Type: models.SettingTypeBool},
		// Security engine tunables (Admin → Security). The master switches are
		// environment variables (POSTA_SECURITY_*); only tunables are seeded.
		{Key: "security_risk_threshold", Value: "40", Type: models.SettingTypeInt},
		{Key: "security_instant_ban_ratio", Value: "2", Type: models.SettingTypeInt},
		{Key: "security_sig_distinct_ports_threshold", Value: "5", Type: models.SettingTypeInt},
		{Key: "security_sig_distinct_ports_window_seconds", Value: "60", Type: models.SettingTypeInt},
		{Key: "security_sig_new_connections_threshold", Value: "30", Type: models.SettingTypeInt},
		{Key: "security_sig_new_connections_window_seconds", Value: "60", Type: models.SettingTypeInt},
		{Key: "security_sig_incomplete_handshakes_threshold", Value: "10", Type: models.SettingTypeInt},
		{Key: "security_sig_incomplete_handshakes_window_seconds", Value: "60", Type: models.SettingTypeInt},
		{Key: "security_sig_connect_aborts_threshold", Value: "12", Type: models.SettingTypeInt},
		{Key: "security_sig_connect_aborts_window_seconds", Value: "60", Type: models.SettingTypeInt},
		{Key: "security_sig_malformed_preauth_threshold", Value: "15", Type: models.SettingTypeInt},
		{Key: "security_sig_malformed_preauth_window_seconds", Value: "300", Type: models.SettingTypeInt},
		{Key: "security_sig_suspicious_paths_threshold", Value: "5", Type: models.SettingTypeInt},
		{Key: "security_sig_suspicious_paths_window_seconds", Value: "60", Type: models.SettingTypeInt},
		{Key: "security_sig_auth_failures_threshold", Value: "10", Type: models.SettingTypeInt},
		{Key: "security_sig_auth_failures_window_seconds", Value: "300", Type: models.SettingTypeInt},
		{Key: "security_weight_distinct_ports", Value: "35", Type: models.SettingTypeInt},
		{Key: "security_weight_new_connections", Value: "20", Type: models.SettingTypeInt},
		{Key: "security_weight_incomplete_handshakes", Value: "20", Type: models.SettingTypeInt},
		{Key: "security_weight_connect_aborts", Value: "15", Type: models.SettingTypeInt},
		{Key: "security_weight_malformed_preauth", Value: "15", Type: models.SettingTypeInt},
		{Key: "security_weight_suspicious_paths", Value: "25", Type: models.SettingTypeInt},
		{Key: "security_weight_auth_failures", Value: "20", Type: models.SettingTypeInt},
		{Key: "security_ban_tier_1_hours", Value: "1", Type: models.SettingTypeInt},
		{Key: "security_ban_tier_2_hours", Value: "24", Type: models.SettingTypeInt},
		{Key: "security_ban_tier_3_hours", Value: "168", Type: models.SettingTypeInt},
		{Key: "security_ban_tier_4_hours", Value: "720", Type: models.SettingTypeInt},
		{Key: "security_events_retention_days", Value: "30", Type: models.SettingTypeInt},
	}

	for i := range defaults {
		if _, err := repo.FindByKey(defaults[i].Key); err != nil {
			if err := repo.Upsert(&defaults[i]); err != nil {
				logger.Error("failed to seed setting", "key", defaults[i].Key, "error", err)
			}
		}
	}

	// One-time data migration: campaign click tracking became tracking for all
	// email, so campaign_click_tracking_enabled is superseded by
	// email_tracking_enabled. Preserve the operator's value and run only when
	// the new key is missing — an existing email_tracking_enabled row always
	// wins, so later changes to the legacy row are never picked up.
	if _, err := repo.FindByKey("email_tracking_enabled"); err != nil {
		if legacy, legacyErr := repo.FindByKey("campaign_click_tracking_enabled"); legacyErr == nil && legacy.Value != "" {
			if err := repo.Upsert(&models.Setting{
				Key:   "email_tracking_enabled",
				Value: legacy.Value,
				Type:  legacy.Type,
			}); err != nil {
				logger.Error("failed to migrate campaign_click_tracking_enabled setting", "error", err)
			}
		}
	}

	logger.Info("default platform settings seeded")
}
