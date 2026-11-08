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

import "testing"

// TestParseSeqSetGrammar covers the RFC 9051 §4.1.2.6 sequence-set grammar
// the OB-4 servers feed ParseSeqSet: single numbers, ranges, lists, reversed
// pairs, the * wildcard, and the malformed shapes that must be refused.
func TestParseSeqSetGrammar(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"single", "7", false},
		{"star", "*", false},
		{"range", "2:4", false},
		{"list", "1,3,5:7,9", false},
		{"reversed", "5:2", false},
		{"star range", "9:*", false},
		{"star first", "*:2", false},
		{"star both", "*:*", false},
		{"empty", "", true},
		{"empty element", "1,,3", true},
		{"zero", "0", true},
		{"zero in range", "0:2", true},
		{"trailing colon", "1:", true},
		{"leading colon", ":1", true},
		{"non numeric", "1:x", true},
		{"overflow", "4294967296", true},
		{"max uint32", "4294967295", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := ParseSeqSet(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSeqSet(%q) = %v, want error", tc.in, set)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSeqSet(%q): %v", tc.in, err)
			}
			if len(set) == 0 {
				t.Fatalf("ParseSeqSet(%q) returned an empty set", tc.in)
			}
		})
	}
}

// TestSeqSetWildcardResolution proves the wildcard semantics the fetch and
// search executors rely on: * resolves against the caller's max, reversed
// ranges normalize, and the RFC 9051 §4.1.2.6 quirk that "559:*" always
// includes the last message even when 559 exceeds every assigned UID.
func TestSeqSetWildcardResolution(t *testing.T) {
	cases := []struct {
		name  string
		set   string
		max   uint32
		match []uint32
		miss  []uint32
	}{
		{"plain", "1:3", 10, []uint32{1, 2, 3}, []uint32{4, 10}},
		{"star is max", "*", 10, []uint32{10}, []uint32{1, 9}},
		{"star to max", "8:*", 10, []uint32{8, 9, 10}, []uint32{7}},
		{"reversed star", "*:2", 10, []uint32{2, 3, 10}, []uint32{1}},
		{"star both is just max", "*:*", 10, []uint32{10}, []uint32{1, 5}},
		// The quirk: 559:* on a folder whose top UID is 12 still matches 12.
		{"n above max includes max", "559:*", 12, []uint32{12}, []uint32{11}},
		{"empty max", "*", 1, []uint32{1}, []uint32{2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := ParseSeqSet(tc.set)
			if err != nil {
				t.Fatalf("ParseSeqSet(%q): %v", tc.set, err)
			}
			resolved := set.Resolve(tc.max)
			for _, n := range tc.match {
				if !resolved.Contains(n) {
					t.Errorf("set %q (max %d) should contain %d", tc.set, tc.max, n)
				}
			}
			for _, n := range tc.miss {
				if resolved.Contains(n) {
					t.Errorf("set %q (max %d) should not contain %d", tc.set, tc.max, n)
				}
			}
			if set.ContainsWildcard() != (tc.set == "*:*" || hasStar(tc.set)) {
				t.Errorf("ContainsWildcard for %q: got %v", tc.set, set.ContainsWildcard())
			}
		})
	}
}

func hasStar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '*' {
			return true
		}
	}
	return false
}
