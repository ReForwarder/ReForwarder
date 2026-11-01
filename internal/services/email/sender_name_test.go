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

package email

import "testing"

func TestResolveSenderWithDefault(t *testing.T) {
	cases := []struct {
		name        string
		from        string
		defaultName string
		want        string
	}{
		{"bare address gets default name", "hello@example.com", "Acme", "\"Acme\" <hello@example.com>"},
		{"existing display name is kept", "Support <hello@example.com>", "Acme", "Support <hello@example.com>"},
		{"empty default leaves bare address", "hello@example.com", "", "hello@example.com"},
		{"whitespace default leaves bare address", "hello@example.com", "   ", "hello@example.com"},
		{"unparseable input is untouched", "not-an-email", "Acme", "not-an-email"},
		{"name needing quoting is escaped", "hello@example.com", "Acme, Inc.", "\"Acme, Inc.\" <hello@example.com>"},
		{"non-ASCII name stays readable", "noreply@sanatory.ru", "Санаторий.ру", "\"Санаторий.ру\" <noreply@sanatory.ru>"},
		{"non-ASCII name with quote is escaped", "noreply@sanatory.ru", `Ёлка "Плюс"`, `"Ёлка \"Плюс\"" <noreply@sanatory.ru>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSenderWithDefault(tc.from, tc.defaultName); got != tc.want {
				t.Errorf("resolveSenderWithDefault(%q, %q) = %q, want %q", tc.from, tc.defaultName, got, tc.want)
			}
		})
	}
}
