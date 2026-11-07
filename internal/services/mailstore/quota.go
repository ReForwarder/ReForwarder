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

package mailstore

import (
	"errors"
	"fmt"

	"github.com/goposta/posta/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Quota scopes reported on QuotaError (ADR-003 decision 5).
const (
	QuotaScopeMailboxBytes    = "mailbox-bytes"
	QuotaScopeMailboxMessages = "mailbox-messages"
	QuotaScopeWorkspaceBytes  = "workspace-bytes"
)

// QuotaError is the typed over-quota rejection the ingestion seam surfaces
// (ADR-003 decision 5): callers test with errors.Is(err, ErrQuotaExceeded)
// and map the details to protocol responses (e.g. IMAP NO [OVERQUOTA] in
// OB-4, SMTP 552 for an ADR-002 forward that lands in a mailbox).
type QuotaError struct {
	MailboxID   uint
	Scope       string // one of the QuotaScope* constants
	Used        int64  // usage the decision saw (bytes, or message count)
	Incoming    int64  // what the append would add (bytes; 1 for a message)
	Limit       int64  // the quota/ceiling that was hit
	WorkspaceID uint   // set only for QuotaScopeWorkspaceBytes
}

// Error implements error.
func (e *QuotaError) Error() string {
	switch e.Scope {
	case QuotaScopeMailboxBytes:
		return fmt.Sprintf("mailstore: mailbox %d over byte quota: used %d + incoming %d > quota %d",
			e.MailboxID, e.Used, e.Incoming, e.Limit)
	case QuotaScopeMailboxMessages:
		return fmt.Sprintf("mailstore: mailbox %d over message-count quota: %d held + 1 incoming > quota %d",
			e.MailboxID, e.Used, e.Limit)
	case QuotaScopeWorkspaceBytes:
		return fmt.Sprintf("mailstore: workspace %d aggregate ceiling exceeded: used %d + incoming %d > ceiling %d",
			e.WorkspaceID, e.Used, e.Incoming, e.Limit)
	default:
		return "mailstore: quota exceeded"
	}
}

// Is makes every *QuotaError match the ErrQuotaExceeded sentinel so callers
// can keep testing against the stable typed error.
func (e *QuotaError) Is(target error) bool { return target == ErrQuotaExceeded }

// DefaultQuotaEnforcer enforces the full ADR-003 decision 5 quota policy,
// replacing the W1 byte-only stub:
//
//   - per-mailbox byte quota (Mailbox.QuotaBytes, 0 = unlimited),
//   - per-mailbox message-count quota (Mailbox.QuotaMessages, 0 = unlimited),
//   - workspace aggregate byte ceiling (Workspace.MailQuotaBytes, 0 =
//     unlimited; mailboxes without a workspace are covered by no ceiling).
//
// CheckAppend must be called inside the caller's append transaction: it
// re-reads the mailbox row under SELECT ... FOR UPDATE so concurrent appends
// to one mailbox serialize before deciding (a plain read would let two
// transactions both pass a nearly-full quota), and — when a workspace ceiling
// applies — locks the workspace row before summing member-mailbox usage, so
// every admission decision in the workspace sees a stable sum. Lock order is
// mailbox row → workspace row; the purge paths (expunge, folder delete,
// retention sweep) take the mailbox row first as well, so no append/purge
// pair can AB-BA deadlock.
//
// A nil tx runs the pure per-mailbox checks only (unit-level boundary tests);
// the Service always passes its transaction.
type DefaultQuotaEnforcer struct{}

// CheckAppend implements QuotaEnforcer.
func (DefaultQuotaEnforcer) CheckAppend(tx *gorm.DB, mb *models.Mailbox, incomingBytes int64) error {
	if mb == nil {
		return ErrMailboxNotFound
	}
	fresh := *mb
	if tx != nil {
		var locked models.Mailbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&locked, mb.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMailboxNotFound
			}
			return fmt.Errorf("lock mailbox %d for quota check: %w", mb.ID, err)
		}
		fresh = locked
	}
	if err := checkMailboxQuota(&fresh, incomingBytes); err != nil {
		return err
	}
	if tx != nil && fresh.WorkspaceID != nil {
		return checkWorkspaceCeiling(tx, &fresh, incomingBytes)
	}
	return nil
}

// checkMailboxQuota is the pure per-mailbox decision (bytes + message count);
// incomingBytes is 0 for dedup-sharing copies, which consume message-count
// quota but no new bytes.
func checkMailboxQuota(mb *models.Mailbox, incomingBytes int64) error {
	if mb.QuotaBytes > 0 && mb.UsedBytes+incomingBytes > mb.QuotaBytes {
		return &QuotaError{
			MailboxID: mb.ID, Scope: QuotaScopeMailboxBytes,
			Used: mb.UsedBytes, Incoming: incomingBytes, Limit: mb.QuotaBytes,
		}
	}
	if mb.QuotaMessages > 0 && mb.MessageCount+1 > mb.QuotaMessages {
		return &QuotaError{
			MailboxID: mb.ID, Scope: QuotaScopeMailboxMessages,
			Used: mb.MessageCount, Incoming: 1, Limit: mb.QuotaMessages,
		}
	}
	return nil
}

// checkWorkspaceCeiling enforces the workspace aggregate byte ceiling. The
// ceiling is read first without a lock (the common unlimited case must not
// serialize workspace appends); when a positive ceiling applies, the
// workspace row is locked FOR UPDATE before the sum so concurrent appends
// across the workspace's mailboxes serialize on it.
func checkWorkspaceCeiling(tx *gorm.DB, mb *models.Mailbox, incomingBytes int64) error {
	wid := *mb.WorkspaceID
	var ws models.Workspace
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ws, wid).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // workspace row vanished; nothing to enforce against
		}
		return fmt.Errorf("lock workspace %d for quota ceiling: %w", wid, err)
	}
	if ws.MailQuotaBytes <= 0 {
		return nil
	}
	var used int64
	if err := tx.Raw(
		`SELECT COALESCE(SUM(used_bytes), 0) FROM mailboxes WHERE workspace_id = ?`, wid,
	).Scan(&used).Error; err != nil {
		return fmt.Errorf("sum mailbox usage of workspace %d: %w", wid, err)
	}
	if used+incomingBytes > ws.MailQuotaBytes {
		return &QuotaError{
			MailboxID: mb.ID, Scope: QuotaScopeWorkspaceBytes,
			Used: used, Incoming: incomingBytes, Limit: ws.MailQuotaBytes, WorkspaceID: wid,
		}
	}
	return nil
}
