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

package upgrade

import (
	"fmt"

	"gorm.io/gorm"
)

func applyAPIKeyScopes(tx *gorm.DB) error {
	if err := tx.Exec(
		`UPDATE api_keys SET scopes = '{send}' WHERE scopes IS NULL OR scopes = '{}'`,
	).Error; err != nil {
		return fmt.Errorf("backfill api_key scopes: %w", err)
	}
	return nil
}
