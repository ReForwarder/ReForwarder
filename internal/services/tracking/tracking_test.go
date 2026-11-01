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

package tracking

import (
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// newStubbedService returns a service whose link persistence is stubbed, so
// click rewriting can be exercised without a database.
func newStubbedService() *Service {
	s := NewService(nil, "https://mail.example.com/", []byte("test-hmac-key"))
	s.findOrCreateLink = func(campaignID uint, originalURL, hash string) (*models.TrackedLink, error) {
		return &models.TrackedLink{CampaignID: campaignID, OriginalURL: originalURL, Hash: hash}, nil
	}
	return s
}

const sampleHTML = `<!doctype html><html><body>` +
	`<p><a href="https://example.com/pricing?plan=pro&promo=spring#faq">Pricing</a></p>` +
	`<p><a href='https://other.org/a?x=1&amp;y=2'>Other</a></p>` +
	`<p><a href="https://example.com/shop?item=42">Shop</a></p>` +
	`<p><a href="https://example.com/shop?item=42">Shop again</a></p>` +
	`<p><a href="https://mail.example.com/t/u/tok.sig">Unsubscribe</a></p>` +
	`<p><a href="mailto:hi@example.com">Mail</a></p>` +
	`</body></html>`

// clickURLs extracts the hash and signature from every rewritten /t/c/ link.
func clickURLs(t *testing.T, html string) [][2]string {
	t.Helper()
	var out [][2]string
	rest := html
	for {
		i := strings.Index(rest, "/t/c/42/")
		if i < 0 {
			return out
		}
		rest = rest[i+len("/t/c/42/"):]
		sigStart := strings.Index(rest, "?sig=")
		if sigStart < 0 {
			t.Fatalf("tracked URL without signature: %q", rest[:min(len(rest), 80)])
		}
		hash := rest[:sigStart]
		rest = rest[sigStart+len("?sig="):]
		sigEnd := strings.IndexAny(rest, "\"'")
		if sigEnd <= 0 {
			t.Fatalf("unterminated signature in tracked URL: %q", rest[:min(len(rest), 80)])
		}
		out = append(out, [2]string{hash, rest[:sigEnd]})
		rest = rest[sigEnd:]
	}
}

func TestProcessHTMLRewritesLinksAndInjectsPixel(t *testing.T) {
	s := newStubbedService()
	out := s.ProcessHTML(sampleHTML, 7, 42)

	clicks := clickURLs(t, out)
	if len(clicks) != 4 {
		t.Fatalf("expected 4 rewritten links (system and mailto links excluded), got %d", len(clicks))
	}

	// Every tracked URL must carry a valid HMAC signature for the message.
	for _, c := range clicks {
		if !s.VerifyClickSig(42, c[0], c[1]) {
			t.Fatalf("click signature invalid for hash %q", c[0])
		}
	}

	// The two identical URLs must share one hash; distinct URLs must not.
	if clicks[2][0] != clicks[3][0] {
		t.Fatalf("identical URLs must map to the same hash: %q vs %q", clicks[2][0], clicks[3][0])
	}
	if clicks[0][0] == clicks[2][0] {
		t.Fatalf("distinct URLs must map to distinct hashes: %q", clicks[0][0])
	}

	// The hash must be derived from the campaign + original URL, so query
	// parameters, fragments, and HTML-escaped characters are part of it.
	if clicks[0][0] != hashLink(7, "https://example.com/pricing?plan=pro&promo=spring#faq") {
		t.Fatalf("hash does not encode the original URL with query and fragment")
	}
	if clicks[1][0] != hashLink(7, "https://other.org/a?x=1&amp;y=2") {
		t.Fatalf("hash does not encode the HTML-escaped URL verbatim")
	}

	// System links, non-http schemes, and the pixel placement.
	if !strings.Contains(out, `href="https://mail.example.com/t/u/tok.sig"`) {
		t.Fatalf("unsubscribe link must not be rewritten")
	}
	if !strings.Contains(out, `href="mailto:hi@example.com"`) {
		t.Fatalf("mailto link must not be rewritten")
	}
	if !strings.Contains(out, `<img src="https://mail.example.com/t/o/42.gif?sig=`) {
		t.Fatalf("open-tracking pixel must be injected")
	}
	if strings.Count(out, "</body>") != 1 ||
		strings.Index(out, "/t/o/42.gif") > strings.Index(out, "</body>") {
		t.Fatalf("pixel must be injected before </body>")
	}
}

func TestDisabledClickTrackingPreservesOriginalURLs(t *testing.T) {
	s := newStubbedService()
	out := s.InjectOpenPixel(sampleHTML, 42)

	// No click-tracking redirects anywhere.
	if strings.Contains(out, "/t/c/") {
		t.Fatalf("click-tracking redirects must not be generated when disabled")
	}

	// Original URLs byte-for-byte: query params, fragment, and escaped chars.
	for _, want := range []struct {
		href  string
		count int
	}{
		{`href="https://example.com/pricing?plan=pro&promo=spring#faq"`, 1},
		{`href='https://other.org/a?x=1&amp;y=2'`, 1},
		{`href="https://example.com/shop?item=42"`, 2}, // duplicate link stays duplicated
	} {
		if strings.Count(out, want.href) != want.count {
			t.Fatalf("original URL not preserved verbatim (%d copies, want %d): %s", strings.Count(out, want.href), want.count, want.href)
		}
	}

	// Unsubscribe and mailto links untouched, open pixel still injected.
	if !strings.Contains(out, `href="https://mail.example.com/t/u/tok.sig"`) ||
		!strings.Contains(out, `href="mailto:hi@example.com"`) {
		t.Fatalf("system and non-http links must be untouched")
	}
	if !strings.Contains(out, `<img src="https://mail.example.com/t/o/42.gif?sig=`) {
		t.Fatalf("open pixel must still be injected when click tracking is disabled")
	}
	if !s.VerifyOpenSig(42, openSig(t, out)) {
		t.Fatalf("injected pixel signature must be valid")
	}
}

// openSig extracts the signature from the open-tracking pixel URL.
func openSig(t *testing.T, html string) string {
	t.Helper()
	i := strings.Index(html, "/t/o/42.gif?sig=")
	if i < 0 {
		t.Fatalf("no open pixel found")
	}
	rest := html[i+len("/t/o/42.gif?sig="):]
	end := strings.IndexAny(rest, "\"'")
	if end < 0 {
		t.Fatalf("unterminated pixel signature")
	}
	return rest[:end]
}

func TestProcessHTMLMatchesSplitComposition(t *testing.T) {
	s := newStubbedService()
	withBody := "<html><body><a href=\"https://example.com/x?a=1&amp;b=2\">x</a></body></html>"
	withoutBody := "<p><a href=\"https://example.com/y#top\">y</a></p>"

	for _, html := range []string{withBody, withoutBody} {
		combined := s.ProcessHTML(html, 7, 42)
		split := s.InjectOpenPixel(s.RewriteLinksForClicks(html, 7, 42), 42)
		if combined != split {
			t.Fatalf("ProcessHTML must equal RewriteLinksForClicks + InjectOpenPixel")
		}
	}
}

func TestInjectOpenPixelPlacement(t *testing.T) {
	s := newStubbedService()

	if got := s.InjectOpenPixel("", 42); got != "" {
		t.Fatalf("empty HTML must stay empty, got %q", got)
	}

	before := s.InjectOpenPixel("<p>hi</p></body>", 42)
	if !strings.HasSuffix(before, "</body>") || !strings.Contains(before, "/t/o/42.gif") {
		t.Fatalf("pixel must go before an existing </body>: %q", before)
	}

	appended := s.InjectOpenPixel("<p>hi</p>", 42)
	if !strings.HasSuffix(appended, "/>") || !strings.Contains(appended, "/t/o/42.gif") {
		t.Fatalf("pixel must be appended when </body> is absent: %q", appended)
	}

	// Only the first </body> gains the pixel.
	two := s.InjectOpenPixel("<p>a</p></body><p>b</p></body>", 42)
	if strings.Count(two, "/t/o/42.gif") != 1 {
		t.Fatalf("exactly one pixel must be injected: %q", two)
	}
}

func TestProcessHTMLEmpty(t *testing.T) {
	s := newStubbedService()
	if got := s.ProcessHTML("", 7, 42); got != "" {
		t.Fatalf("empty HTML must stay empty, got %q", got)
	}
}
