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

package managesieve

import (
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
)

// Audit action types published for every ManageSieve session (the ADR-004
// decision-6 posture, SF-86: auth success/failure land in the events
// substrate with user + IP — the same trail the POP3/IMAP listeners write).
const (
	auditEventAuthSuccess = "managesieve.auth.success"
	auditEventAuthFailure = "managesieve.auth.failure"
)

// NewEventAuditLogger returns an AuditFunc publishing ManageSieve audit
// events into the event bus (models.EventCategoryAudit — the SF-74 audit
// trail). A nil bus yields nil (the server treats that as audit-off).
func NewEventAuditLogger(bus *eventbus.EventBus) AuditFunc {
	if bus == nil {
		return nil
	}
	return func(action string, userID uint, userEmail, clientIP string, meta map[string]any) {
		bus.PublishScoped(nil, models.EventCategoryAudit, action, &userID, userEmail, clientIP, "ManageSieve: "+action, meta)
	}
}

// auditAuthSuccess publishes a successful login.
func auditAuthSuccess(fn AuditFunc, user *models.User, ip string, mailboxID uint) {
	if fn == nil || user == nil {
		return
	}
	fn(auditEventAuthSuccess, user.ID, user.Email, ip, map[string]any{
		"protocol":   "managesieve",
		"email":      user.Email,
		"mailbox_id": mailboxID,
	})
}

// auditAuthFailure publishes a rejected login with the SERVER-SIDE reason
// (the wire answer stays a generic NO so the listener cannot be used to
// enumerate identities or credential states).
func auditAuthFailure(fn AuditFunc, email, ip, reason string) {
	if fn == nil {
		return
	}
	fn(auditEventAuthFailure, 0, email, ip, map[string]any{
		"protocol": "managesieve",
		"email":    email,
		"reason":   reason,
	})
}
