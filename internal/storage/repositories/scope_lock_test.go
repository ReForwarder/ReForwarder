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

import "testing"

func TestOwnsResourceIgnoresLegacyUserOwnership(t *testing.T) {
	ws := uint(7)
	other := uint(8)

	if !OwnsResource(ResourceScope{UserID: 1, WorkspaceID: &ws}, 999, &ws) {
		t.Fatal("a resource in the caller's workspace must be owned regardless of its user_id")
	}
	if OwnsResource(ResourceScope{UserID: 1, WorkspaceID: &ws}, 1, &other) {
		t.Fatal("a resource in another workspace must never be owned, even when user_id matches")
	}
	if OwnsResource(ResourceScope{UserID: 1, WorkspaceID: &ws}, 1, nil) {
		t.Fatal("an unscoped resource must not be owned once scoping is enforced")
	}
	if OwnsResource(ResourceScope{UserID: 1}, 1, nil) {
		t.Fatal("a scope with no workspace must own nothing")
	}
}
