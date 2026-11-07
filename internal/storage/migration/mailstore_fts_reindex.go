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

	"gorm.io/gorm"
)

// Mailstore search-index rebuild (OB-3 wave W3, ADR-003 decision 3). The
// search_tsv column is GENERATED ALWAYS, so Postgres keeps it correct on
// every write and the GIN index is maintained automatically — under normal
// operation nothing here needs to run. This is the operator recovery path
// (exposed as the admin-gated POST /admin/mailstore/search/reindex): it
// converges mail_messages to the canonical column definition from
// mailstoreTSVDefinition and rebuilds the GIN index from scratch, healing
// schema drift (e.g. a column created by an older/alternate definition) and
// index corruption in one step.
//
// It is safe to run repeatedly (idempotent) and on any starting state (column
// absent, present, or present-but-plain). search_tsv is derived data —
// mailstore never reads it through GORM writes (the model field is
// `gorm:"-:all"`) — so dropping and re-adding it loses nothing; the entire
// rebuild runs in one transaction so a failure leaves the previous schema in
// place. It takes an ACCESS EXCLUSIVE lock on mail_messages while the DDL
// runs and rewrites the table when re-adding the STORED column: run it in a
// maintenance window on large deployments.

// mailstoreReindexDropSQL drops the existing search_tsv column when one is
// present. The dependent GIN index is dropped by Postgres together with the
// column.
const mailstoreReindexDropSQL = `DO $$
BEGIN
	IF EXISTS (
		SELECT 1 FROM pg_attribute a
		WHERE a.attrelid = 'mail_messages'::regclass
		  AND a.attname = 'search_tsv'
		  AND NOT a.attisdropped
	) THEN
		EXECUTE 'ALTER TABLE mail_messages DROP COLUMN search_tsv';
	END IF;
END $$;`

// RebuildMailstoreSearch converges mail_messages.search_tsv and its GIN index
// to the canonical definitions. The whole sequence is one transaction: drop
// (if present) → re-add the canonical generated column → recreate the GIN
// index. Static DDL only — no user data is ever interpolated.
func RebuildMailstoreSearch(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("rebuild mailstore search index: %w", gorm.ErrInvalidDB)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(mailstoreReindexDropSQL).Error; err != nil {
			return fmt.Errorf("drop search_tsv: %w", err)
		}
		if err := tx.Exec(mailstoreTSVDefinition).Error; err != nil {
			return fmt.Errorf("re-create search_tsv: %w", err)
		}
		if err := tx.Exec(`DROP INDEX IF EXISTS idx_mail_messages_search_tsv`).Error; err != nil {
			return fmt.Errorf("drop stale search GIN index: %w", err)
		}
		if err := tx.Exec(`CREATE INDEX idx_mail_messages_search_tsv ON mail_messages USING GIN (search_tsv)`).Error; err != nil {
			return fmt.Errorf("re-create search GIN index: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("rebuild mailstore search index: %w", err)
	}
	return nil
}
