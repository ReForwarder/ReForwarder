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

package imap

import (
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/eventbus"
)

// Audit event types on the events substrate (ADR-004 decision 6, SF-74
// continuation): AUTH success/failure plus every mail-mutating action lands
// in the audit category with user + IP, the same shape the POP3 server's
// audit logger uses. Action-specific events use the "imap.<action>" type
// namespace.
const (
	AuditAuthSuccess        = "imap.auth.success"
	AuditAuthFailure        = "imap.auth.failure"
	AuditSelect             = "imap.select"
	AuditAppend             = "imap.append"
	AuditAppendQuotaDenied  = "imap.append.quota_denied"
	AuditStore              = "imap.store"
	AuditStoreConflict      = "imap.store.conflict"
	AuditExpunge            = "imap.expunge"
	AuditCopy               = "imap.copy"
	AuditMove               = "imap.move"
	AuditSearch             = "imap.search"
	AuditFetch              = "imap.fetch"
	AuditCleartextAuthDeny  = "imap.auth.cleartext_denied"
)

// NewEventAuditLogger returns an AuditFunc publishing into the events
// substrate through the given bus. A nil bus yields a nil audit sink (the
// server audits nothing rather than panicking).
func NewEventAuditLogger(bus *eventbus.EventBus) AuditFunc {
	if bus == nil {
		return nil
	}
	return func(action string, userID uint, userEmail, clientIP string, meta map[string]any) {
		bus.PublishScoped(nil, models.EventCategoryAudit, action, &userID, userEmail, clientIP, "IMAP: "+action, meta)
	}
}
