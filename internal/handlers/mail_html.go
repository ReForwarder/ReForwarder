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

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Mail-view HTML sanitization (OB-5 wave W1, ADR-005 decision 5): mail HTML
// is parsed, stripped of script-execution vectors and remote-loading
// references, and re-rendered. The dashboard then renders the result in its
// sandboxed iframe (web/src/views/logs/MessageDetail.vue pattern: srcdoc +
// sandbox=""), so scripts and same-origin access are blocked a second time at
// the browser boundary. Defense is in the sanitizer first because the iframe
// alone does NOT block network fetches: a sandbox="" iframe still loads
// <img src="https://attacker/…"> — exactly the remote-content beacon ADR-005
// blocks by default.
//
// Rules (kept small and testable, favoring removal over rewriting):
//   - Elements dropped with their content: script, iframe/frame/frameset,
//     object, embed, applet, base, meta, link, svg, math, template, noscript,
//     title, head, style when it cannot be sanitized in place (never — style
//     text is sanitized instead, see sanitizeCSS).
//   - <form> and its controls are unwrapped: their text stays, the
//     interactive element does not (a phishing form inside the mail view
//     would otherwise be the one UI element the sandbox cannot see).
//   - Every on* attribute is removed (event handlers).
//   - src/href/action/formaction/poster/background values with
//     javascript:/vbscript:/data: schemes are removed; img sources survive
//     only as data:image/* — every http(s), protocol-relative, relative and
//     cid: source is removed, which is the remote-content block. Other
//     elements keep https hrefs (links are inert inside sandbox="" — no
//     scripts, no top navigation, no popups).
//   - Inline style attributes and <style> text have remote url(...) and
//     @import references neutralized.

// mailDropElements are removed together with their children.
var mailDropElements = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Iframe:   true,
	atom.Frame:    true,
	atom.Frameset: true,
	atom.Object:   true,
	atom.Embed:    true,
	atom.Applet:   true,
	atom.Base:     true,
	atom.Meta:     true,
	atom.Link:     true,
	atom.Svg:      true,
	atom.Math:     true,
	atom.Template: true,
	atom.Noscript: true,
	atom.Title:    true,
	atom.Head:     true,
}

// mailUnwrapElements are removed but their children are kept. For the
// childless form controls (input) unwrapping is simply removal.
var mailUnwrapElements = map[atom.Atom]bool{
	atom.Form:     true,
	atom.Button:   true,
	atom.Select:   true,
	atom.Textarea: true,
	atom.Option:   true,
	atom.Input:    true,
}

// mailURLAttributes carry fetchable URLs.
var mailURLAttributes = map[string]bool{
	"href":       true,
	"src":        true,
	"action":     true,
	"formaction": true,
	"poster":     true,
	"background": true,
	"srcset":     true,
	"xlink:href": true,
}

// sanitizeMailHTML renders display-safe HTML from an email's HTML part.
// Parse errors return "" — the caller falls back to the text part, and a
// message is never lost over malformed markup.
func sanitizeMailHTML(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(raw), ctx)
	if err != nil {
		return ""
	}
	// Attach the fragment to the context node so every element — including
	// top-level ones like a bare <form> — passes through the same
	// drop/unwrap/attribute dispatch.
	for _, n := range nodes {
		ctx.AppendChild(n)
	}
	sanitizeMailNode(ctx)
	var out strings.Builder
	for c := ctx.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&out, c); err != nil {
			return ""
		}
	}
	return out.String()
}

// sanitizeMailNode strips one subtree in place: dropping/unwrapping elements
// and attributes as the package rules define.
func sanitizeMailNode(n *html.Node) {
	if n.Type == html.ElementNode && n.DataAtom == atom.Style {
		// <style> text carries CSS, not markup: neutralize remote fetches in
		// place. Style elements never have element children.
		var text strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			text.WriteString(c.Data)
		}
		n.FirstChild = nil
		n.LastChild = nil
		n.AppendChild(&html.Node{
			Type: html.TextNode,
			Data: sanitizeCSS(text.String()),
		})
		return
	}
	// Collect then mutate: node surgery while ranging is unsafe.
	var kids []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		kids = append(kids, c)
	}
	for _, c := range kids {
		sanitizeMailChild(c, n)
	}
	if n.Type != html.ElementNode {
		return
	}
	// Attribute pass for this element.
	attrs := n.Attr[:0]
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		switch {
		case strings.HasPrefix(key, "on"):
			continue // event handlers
		case mailURLAttributes[key]:
			if !mailURLAllowed(n.DataAtom, a.Val) {
				continue
			}
		case key == "style":
			a.Val = sanitizeCSS(a.Val)
		}
		attrs = append(attrs, a)
	}
	n.Attr = attrs
}

// sanitizeMailChild decides one child of parent: drop it, unwrap it (recurse
// into its children in place), or keep it (recurse).
func sanitizeMailChild(c, parent *html.Node) {
	if c.Type != html.ElementNode {
		sanitizeMailNode(c) // text/comment: attribute pass is a no-op
		return
	}
	a := c.DataAtom
	switch {
	case mailDropElements[a]:
		parent.RemoveChild(c)
	case mailUnwrapElements[a]:
		// Hoist the children into the parent at the child's position (the
		// html package requires a detach before re-insert), then dispatch
		// each hoisted child through the same drop/unwrap/keep decision —
		// form controls nested one level deeper must not survive.
		var kids []*html.Node
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			kids = append(kids, k)
		}
		for _, k := range kids {
			c.RemoveChild(k)
			parent.InsertBefore(k, c)
		}
		parent.RemoveChild(c)
		for _, k := range kids {
			sanitizeMailChild(k, parent)
		}
	default:
		sanitizeMailNode(c)
	}
}

// mailURLAllowed decides whether a URL attribute survives sanitization.
// Images (and other embedded content) may only carry inline data:image
// payloads — every remote or unresolvable reference is removed, which is the
// ADR-005 remote-content block. Anchors keep http(s) (and relative) targets:
// they are inert in the sandboxed iframe and the user clicks them
// deliberately.
func mailURLAllowed(tag atom.Atom, val string) bool {
	v := strings.TrimSpace(strings.ToLower(val))
	switch {
	case v == "":
		return false
	case strings.HasPrefix(v, "javascript:"), strings.HasPrefix(v, "vbscript:"),
		strings.HasPrefix(v, "data:text/html"), strings.HasPrefix(v, "data:application"),
		strings.HasPrefix(v, "data:image/svg"):
		// Script-bearing schemes, plus SVG (it hosts script and foreign
		// content) even under the data: scheme.
		return false
	}
	// data:image/* payloads are self-contained: no network fetch, so they are
	// safe to keep everywhere.
	if strings.HasPrefix(v, "data:image/") {
		return true
	}
	if strings.HasPrefix(v, "data:") {
		return false
	}
	// Embedded-content tags must not reference remote or unresolvable
	// sources. (srcset is a single attribute but holds a candidate list —
	// treated with the img rule through its presence in mailURLAttributes.)
	switch tag {
	case atom.Img, atom.Image, atom.Source, atom.Video, atom.Audio, atom.Track,
		atom.Input:
		return false // http(s), protocol-relative, relative, cid: — all blocked
	}
	// Links and friends keep absolute http(s) URLs and relative targets.
	return true
}

// sanitizeCSS neutralizes remote fetches inside CSS text (style attributes
// and <style> blocks): @import is dropped and url(...) references with a
// scheme or protocol-relative target are emptied. Relative url() references
// (none can resolve inside about:srcdoc) and data: URLs stay.
func sanitizeCSS(css string) string {
	lower := strings.ToLower(css)
	if !strings.Contains(lower, "url(") && !strings.Contains(lower, "@import") &&
		!strings.Contains(lower, "image-set(") {
		return css
	}
	var b strings.Builder
	b.Grow(len(css))
	lowered := strings.ToLower(css)
	for i := 0; i < len(css); {
		switch {
		case strings.HasPrefix(lowered[i:], "@import"):
			// Drop the whole at-rule: everything up to the next ';' (or end).
			for j := i; j < len(css); j++ {
				if css[j] == ';' {
					i = j + 1
					break
				}
				if j == len(css)-1 {
					i = len(css)
					break
				}
			}
			continue
		case strings.HasPrefix(lowered[i:], "url("):
			end := indexCloseParen(css, i)
			if end < 0 {
				b.WriteString(css[i:])
				i = len(css)
				continue
			}
			inner := strings.TrimSpace(css[i+len("url(") : end])
			inner = strings.Trim(inner, `'"`)
			if isRemoteCSSURL(inner) {
				b.WriteString("url(blocked)") // keep layout, kill the fetch
			} else {
				b.WriteString(css[i : end+1])
			}
			i = end + 1
			continue
		case strings.HasPrefix(lowered[i:], "image-set("):
			end := indexCloseParen(css, i)
			if end < 0 {
				b.WriteString(css[i:])
				i = len(css)
				continue
			}
			b.WriteString("none")
			i = end + 1
			continue
		default:
			b.WriteByte(css[i])
			i++
		}
	}
	return b.String()
}

// indexCloseParen returns the index of the ')' closing the construct opened
// at open (which points at the opening word), accounting for quoted strings.
func indexCloseParen(s string, open int) int {
	inS, inD := false, false
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case ')':
			if !inS && !inD {
				return i
			}
		}
	}
	return -1
}

// isRemoteCSSURL reports whether a url() payload would fetch from the
// network: absolute schemes (http, https, ftp, cid, …) and protocol-relative
// targets. data:image/* stays.
func isRemoteCSSURL(v string) bool {
	if v == "" {
		return false
	}
	if strings.HasPrefix(v, "data:image/") {
		return false
	}
	if strings.HasPrefix(v, "data:") {
		return true // non-image data URLs in CSS: treat as remote/blocked
	}
	if strings.HasPrefix(v, "//") {
		return true
	}
	if i := strings.Index(v, ":"); i > 0 {
		// A colon before any '/', '?' or '#' means a scheme (http:, cid:, …).
		prefix := v[:i]
		if !strings.ContainsAny(prefix, "/?#") {
			return true
		}
	}
	return false
}
