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

package handlers

import (
	"strings"
	"testing"
)

func TestStyleSheetNameFromHref(t *testing.T) {
	cases := map[string]string{
		"styles.css":            "styles",
		"css/styles.css":        "styles",
		"/assets/css/main.css":  "main",
		"styles.css?v=2":        "styles",
		"theme.css#section":     "theme",
		"https://x.io/base.css": "base",
		`a\b\win.css`:           "win",
		"":                      "",
	}
	for in, want := range cases {
		if got := styleSheetNameFromHref(in); got != want {
			t.Errorf("styleSheetNameFromHref(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractStyleSheetLinks(t *testing.T) {
	html := `<head>
		<link rel="stylesheet" href="css/styles.css?v=2">
		<link href="other.css" rel="STYLESHEET"/>
		<link rel="icon" href="favicon.ico">
		<link rel=preload href="ignored.css">
	</head>`

	got := extractStyleSheetLinks(html)
	want := []string{"styles", "other"}
	if len(got) != len(want) {
		t.Fatalf("extractStyleSheetLinks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("extractStyleSheetLinks[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractStyleAndBodyStripsLinks(t *testing.T) {
	html := `<html><head>
		<style>.a{color:red}</style>
		<link rel="stylesheet" href="styles.css">
	</head><body><p>Hi</p></body></html>`

	css, body := extractStyleAndBody(html)
	if css != ".a{color:red}" {
		t.Errorf("css = %q, want %q", css, ".a{color:red}")
	}
	if strings.Contains(body, "<link") || strings.Contains(body, "<style") {
		t.Errorf("body still contains stripped tags: %q", body)
	}
	if !strings.Contains(body, "<p>Hi</p>") {
		t.Errorf("body missing content: %q", body)
	}
}
