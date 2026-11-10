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

// Sanitizer unit battery (ADR-005 decision 5): the mail view's HTML must
// carry no script-execution vector and no automatic remote fetch, while
// readable content and inline data: images survive.
func TestMailHTML_Sanitizer_StripsScriptVectorsAndRemoteLoads(t *testing.T) {
	out := sanitizeMailHTML(mhEvilHTML)
	lower := strings.ToLower(out)
	for _, banned := range []string{
		"<script", "onerror", "onclick", "javascript:", "<iframe", "tracker.example",
		"@import", "evil.example", "<object", "<embed",
	} {
		if strings.Contains(lower, banned) {
			t.Fatalf("sanitized output contains %q:\n%s", banned, out)
		}
	}
	for _, want := range []string{
		"Hello", "<b>world</b>", `data:image/png;base64,iVBORw0KGgo=`,
		`href="https://example.com/ok"`, "<p>",
	} {
		if !strings.Contains(lower, strings.ToLower(want)) {
			t.Fatalf("sanitized output lost %q:\n%s", want, out)
		}
	}
}

func TestMailHTML_Sanitizer_CSSRemoteFetchesNeutralized(t *testing.T) {
	out := sanitizeMailHTML(`<div style="background:url(https://attacker.example/x.png);color:blue">t</div>` +
		`<style>@import url("https://attacker.example/y.css"); a { color: url(https://attacker.example/z.gif) }</style>`)
	lower := strings.ToLower(out)
	if strings.Contains(lower, "attacker.example") {
		t.Fatalf("remote CSS fetch survived:\n%s", out)
	}
	if !strings.Contains(lower, "color:blue") {
		t.Fatalf("benign inline style was dropped:\n%s", out)
	}
	if !strings.Contains(lower, "a { color: url(blocked)") {
		t.Fatalf("benign style block should survive with the remote url neutralized:\n%s", out)
	}
}

func TestMailHTML_Sanitizer_DataImageAndTextKept(t *testing.T) {
	out := sanitizeMailHTML(`<img src="data:image/gif;base64,R0lGOD" alt="dot"><p>keep &amp; me</p>`)
	if !strings.Contains(out, "data:image/gif;base64,R0lGOD") {
		t.Fatalf("inline data image dropped:\n%s", out)
	}
	if !strings.Contains(out, "keep &amp; me") {
		t.Fatalf("text content corrupted:\n%s", out)
	}
}

func TestMailHTML_Sanitizer_FormUnwrappedTextKept(t *testing.T) {
	out := sanitizeMailHTML(`<p>Before</p><form action="https://evil.example/post"><input type="text"><button>Sign in</button></form><p>After</p>`)
	lower := strings.ToLower(out)
	if strings.Contains(lower, "<form") || strings.Contains(lower, "<input") || strings.Contains(lower, "<button") {
		t.Fatalf("form controls survived:\n%s", out)
	}
	for _, want := range []string{"Before", "Sign in", "After"} {
		if !strings.Contains(out, want) {
			t.Fatalf("form text lost %q:\n%s", want, out)
		}
	}
}

func TestMailHTML_Sanitizer_EmptyAndBrokenInput(t *testing.T) {
	if got := sanitizeMailHTML(""); got != "" {
		t.Fatalf("empty input = %q", got)
	}
	if got := sanitizeMailHTML("   "); got != "" {
		t.Fatalf("blank input = %q", got)
	}
	if got := sanitizeMailHTML("<p><b>unclosed"); !strings.Contains(got, "unclosed") {
		t.Fatalf("recovered markup lost content: %q", got)
	}
}
