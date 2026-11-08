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

package pop3

import (
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
)

// Audit action types published for every POP3 session (ADR-004 decision 6,
// SF-74 continuation: auth success/failure and expunge actions land in the
// events substrate with user + IP).
const (
	auditEventAuthSuccess = "pop3.auth.success"
	auditEventAuthFailure = "pop3.auth.failure"
	auditEventSessionEnd  = "pop3.session.end"
)

// AuditFunc receives the per-session audit events (same shape as the IMAP
// server's audit hook). Production wiring installs the eventbus-backed
// adapter (NewEventAuditLogger); a nil hook audits nothing — audit failure
// never breaks a session.
type AuditFunc func(action string, userID uint, userEmail, clientIP string, meta map[string]any)

// NewEventAuditLogger returns an AuditFunc publishing POP3 audit events into
// the event bus (models.EventCategoryAudit — the SF-74 audit trail). A nil
// bus yields nil (the server treats that as audit-off).
func NewEventAuditLogger(bus *eventbus.EventBus) AuditFunc {
	if bus == nil {
		return nil
	}
	return func(action string, userID uint, userEmail, clientIP string, meta map[string]any) {
		bus.PublishScoped(nil, models.EventCategoryAudit, action, &userID, userEmail, clientIP, "POP3: "+action, meta)
	}
}

// auditAuthSuccess publishes a successful login.
func auditAuthSuccess(fn AuditFunc, user *models.User, ip string) {
	if fn == nil || user == nil {
		return
	}
	fn(auditEventAuthSuccess, user.ID, user.Email, ip, map[string]any{
		"protocol": "pop3",
		"email":    user.Email,
	})
}

// auditAuthFailure publishes a rejected login with the SERVER-SIDE reason
// (the wire answer stays a generic -ERR so the mail port cannot be used to
// enumerate identities or credential states).
func auditAuthFailure(fn AuditFunc, email, ip, reason string) {
	if fn == nil {
		return
	}
	fn(auditEventAuthFailure, 0, email, ip, map[string]any{
		"protocol": "pop3",
		"email":    email,
		"reason":   reason,
	})
}

// auditSessionEnd publishes the session summary with the UPDATE-phase tally.
func auditSessionEnd(fn AuditFunc, user *models.User, ip string, deleted, expunged, retrieved int, quitClean bool) {
	if fn == nil || user == nil {
		return
	}
	fn(auditEventSessionEnd, user.ID, user.Email, ip, map[string]any{
		"protocol":   "pop3",
		"deleted":    deleted,
		"expunged":   expunged,
		"retrieved":  retrieved,
		"quit_clean": quitClean,
	})
}
