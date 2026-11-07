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

package spamfilter

import (
	"regexp"
	"strings"
)

// GTUBEString is the standard Generic Test for Unsolicited Bulk Email
// (the SpamAssassin GTUBE, defined by the Apache SpamAssassin project):
// a deliberately unique 68-character marker operators paste into a test
// message to prove the spam pipeline fires end to end. The string is a
// fixed, public test token — not a content filter.
//
// Match rule (SF-42's testable slice, per ADR-006): the marker must appear
// verbatim (case-sensitive) as a whitespace-free line of its own in a body
// part, matching the GTUBE spec's "single line, without whitespace" shape.
// HTML bodies are tag-stripped first, so `<p>` + marker + `</p>` on one line
// still matches; a plain substring match would also trigger inside, say, a
// quoted copy of the spec itself — the line-shape rule keeps that corner
// honest without pretending to be a full tokenizer.
const GTUBEString = "XJS*C4JDBQADN1.NSBN3*2IDNEN*GTUBE-STANDARD-ANTI-UBE-TEST-EMAIL*C.34X"

// gtubeReason is the reason string the scorer reports for a GTUBE hit.
const gtubeReason = "gtube_test_pattern"

// htmlTagPattern strips markup for the GTUBE line check. The marker itself
// contains no angle brackets, so stripping cannot corrupt a real hit.
var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// containsGTUBE reports whether any body line is exactly the GTUBE marker
// (allowing surrounding horizontal whitespace, which the spec's sample
// messages include).
func containsGTUBE(bodies ...string) bool {
	for _, body := range bodies {
		if body == "" {
			continue
		}
		for _, line := range strings.Split(htmlTagPattern.ReplaceAllString(body, ""), "\n") {
			if strings.TrimSpace(line) == GTUBEString {
				return true
			}
		}
	}
	return false
}
