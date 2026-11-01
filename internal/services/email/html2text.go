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

import (
	"html"
	"regexp"
	"strings"
)

// HTMLToText converts an HTML string to readable plain text.
// It strips tags, converts common block elements to appropriate whitespace,
// extracts link URLs, and decodes HTML entities.
func HTMLToText(input string) string {
	if input == "" {
		return ""
	}

	s := input

	// Convert <br>, <br/>, <br /> to newlines
	brRe := regexp.MustCompile(`(?i)<br\s*/?>`)
	s = brRe.ReplaceAllString(s, "\n")

	// Convert </p> to double newlines
	closePRe := regexp.MustCompile(`(?i)</p\s*>`)
	s = closePRe.ReplaceAllString(s, "\n\n")

	// Convert <p ...> to double newlines (except the very first one)
	openPRe := regexp.MustCompile(`(?i)<p[^>]*>`)
	s = openPRe.ReplaceAllString(s, "\n\n")

	// Convert <li> to "- " prefix
	liRe := regexp.MustCompile(`(?i)<li[^>]*>`)
	s = liRe.ReplaceAllString(s, "\n- ")

	// Convert <a href="url">text</a> to "text (url)"
	linkRe := regexp.MustCompile(`(?i)<a\s[^>]*href\s*=\s*["']([^"']*)["'][^>]*>(.*?)</a>`)
	s = linkRe.ReplaceAllString(s, "$2 ($1)")

	// Strip all remaining HTML tags
	tagRe := regexp.MustCompile(`<[^>]*>`)
	s = tagRe.ReplaceAllString(s, "")

	// Decode HTML entities (handles &amp;, &lt;, &gt;, &nbsp;, &#xxxx; etc.)
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = html.UnescapeString(s)

	// Collapse runs of spaces/tabs on each line (preserve newlines)
	spaceRe := regexp.MustCompile(`[^\S\n]+`)
	s = spaceRe.ReplaceAllString(s, " ")

	// Collapse 3+ consecutive newlines into 2
	nlRe := regexp.MustCompile(`\n{3,}`)
	s = nlRe.ReplaceAllString(s, "\n\n")

	s = strings.TrimSpace(s)

	return s
}
