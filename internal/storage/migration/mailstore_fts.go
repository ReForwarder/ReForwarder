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

// mailstoreTSVDefinition is the generated-column definition for
// mail_messages.search_tsv (ADR-003 decision 3: Postgres FTS over
// subject/from/to/body-text with GIN). Weights: A = subject, B = from + to/cc,
// C = body text. Only immutable expressions are allowed in a STORED generated
// column, hence the explicit 'english' regconfig on to_tsvector (the
// single-argument form is stable and would be rejected).
const mailstoreTSVDefinition = `ALTER TABLE mail_messages ADD COLUMN search_tsv tsvector
		GENERATED ALWAYS AS (
			setweight(to_tsvector('english', coalesce(subject, '')), 'A') ||
			setweight(to_tsvector('english', coalesce(from_addr, '')), 'B') ||
			setweight(to_tsvector('english', coalesce(to_addr, '') || ' ' || coalesce(cc, '')), 'B') ||
			setweight(to_tsvector('english', coalesce(body_text, '')), 'C')
		) STORED`

// mailstoreEnsureSQL idempotently creates the mail_messages.search_tsv
// generated column and its GIN index — SQL GORM cannot express (the model
// field is `gorm:"-:all"`; GORM must never write the column). Static DDL only:
// no user data is ever interpolated.
//
// If the column exists but plain (an earlier schema state before this
// definition converged), it is dropped and re-added — the column holds derived
// data only, so that is safe.
const mailstoreEnsureSQL = `DO $$
BEGIN
	IF NOT EXISTS (
		SELECT 1 FROM pg_attribute a
		WHERE a.attrelid = 'mail_messages'::regclass
		  AND a.attname = 'search_tsv'
		  AND NOT a.attisdropped
	) THEN
		EXECUTE $tsv$` + mailstoreTSVDefinition + `$tsv$;
	ELSIF EXISTS (
		SELECT 1 FROM pg_attribute a
		WHERE a.attrelid = 'mail_messages'::regclass
		  AND a.attname = 'search_tsv'
		  AND NOT a.attisdropped
		  AND a.attgenerated = ''
	) THEN
		EXECUTE 'ALTER TABLE mail_messages DROP COLUMN search_tsv';
		EXECUTE $tsv$` + mailstoreTSVDefinition + `$tsv$;
	END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_mail_messages_search_tsv ON mail_messages USING GIN (search_tsv);`

// EnsureMailstoreSearch applies mailstoreEnsureSQL. It is called from Run
// (fresh installs: the upgrade framework seals the step registry without
// applying it, so AutoMigrate-time SQL is the only thing that runs there) and
// from the "2026-11-07-mailstore-fts" upgrade step (existing versioned
// databases). Both paths are safe to run repeatedly.
func EnsureMailstoreSearch(db *gorm.DB) error {
	if err := db.Exec(mailstoreEnsureSQL).Error; err != nil {
		return fmt.Errorf("ensure mail_messages.search_tsv + GIN: %w", err)
	}
	return nil
}
