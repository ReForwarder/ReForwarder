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

package models

// SubscriberListMemberStatus is the per-membership consent state of one
// subscriber on one static list (RF-F075). The subscriber's own status
// (SubscriberStatus) stays a global, per-contact property; the membership
// status is what double opt-in governs: on a DOI list a membership starts
// pending and becomes confirmed when the subscriber redeems the emailed
// token. Only confirmed members resolve into campaign audiences.
type SubscriberListMemberStatus string

const (
	// MemberStatusPending marks a membership awaiting double-opt-in
	// confirmation. Pending members are never campaign-eligible.
	MemberStatusPending SubscriberListMemberStatus = "pending"
	// MemberStatusConfirmed marks a confirmed membership — the state every
	// membership had before double opt-in existed, and the column default,
	// so existing rows upgrade in place unchanged.
	MemberStatusConfirmed SubscriberListMemberStatus = "confirmed"
)

// EmailProvenanceSystemDOI stamps the transactional Email row of a
// double-opt-in confirmation message (RF-F075). A confirmation is the
// recipient's own requested action, not a marketing send: the system_doi
// path never consults suppression, unsubscribe or consent layers, and never
// consumes the tenant's marketing rate limit (the forwarding posture).
const EmailProvenanceSystemDOI = "system_doi"

// SubscriberWithMemberStatus is one row of a list's member view: the
// subscriber columns plus the membership's own consent status. member_status
// is membership-scoped (pending until double-opt-in confirmation) and is not
// the subscriber's global status.
type SubscriberWithMemberStatus struct {
	Subscriber
	MemberStatus SubscriberListMemberStatus `json:"member_status"`
}
