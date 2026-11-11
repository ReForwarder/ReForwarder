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

// SenderDomainExpr extracts the bare sending domain from emails.sender.
// Sender carries either a bare address ("hello@example.com") or RFC 5322
// display form ("Acme <hello@example.com>"): prefer the <addr-spec> when
// present, then take everything after the @. Lower-cased so the comparison
// matches how domains are stored.
//
// Postgres SQL; meant to be concatenated into queries that join emails.
// Lives here because two read paths need the identical expression (the R-07
// domain health stats and the OB-6 W3 reputation rollups) and must not drift.
const SenderDomainExpr = `LOWER(SPLIT_PART(BTRIM(COALESCE(NULLIF(SUBSTRING(sender FROM '<([^>]+)>'), ''), sender)), '@', 2))`
