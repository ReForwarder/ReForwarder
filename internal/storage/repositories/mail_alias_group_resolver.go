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

import "gorm.io/gorm"

// MailAliasGroupResolver combines the alias and group repositories under the
// inbound.AliasGroupResolver seam (SF-65): the deliver action's expansion
// resolves recipient addresses through the owner-scoped mail_aliases and
// mail_groups tables. All three methods scope by the record owner's user_id
// — another user's mapping for the same address never resolves here.
type MailAliasGroupResolver struct {
	aliases *MailAliasRepository
	groups  *MailGroupRepository
}

func NewMailAliasGroupResolver(db *gorm.DB) *MailAliasGroupResolver {
	return &MailAliasGroupResolver{
		aliases: NewMailAliasRepository(db),
		groups:  NewMailGroupRepository(db),
	}
}

// ResolveAliasAddress returns the target mailbox ID for the owner's alias at
// address, ok=false when none.
func (r *MailAliasGroupResolver) ResolveAliasAddress(userID uint, address string) (uint, bool) {
	return r.aliases.ResolveAliasAddress(userID, address)
}

// ResolveGroupAddress returns the member mailbox IDs of the owner's group at
// address, ok=false when none.
func (r *MailAliasGroupResolver) ResolveGroupAddress(userID uint, address string) ([]uint, bool) {
	return r.groups.ResolveGroupAddress(userID, address)
}

// ResolveGroupByMailbox returns the member mailbox IDs when the mailbox
// anchors one of the owner's groups, ok=false otherwise.
func (r *MailAliasGroupResolver) ResolveGroupByMailbox(userID, mailboxID uint) ([]uint, bool) {
	return r.groups.ResolveGroupByMailbox(userID, mailboxID)
}
