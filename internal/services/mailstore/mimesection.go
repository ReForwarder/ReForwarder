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

import (
	"bytes"
	"mime"
	"strconv"
	"strings"
)

// Byte-level MIME section extraction for FETCH BODY[...] (OB-4 wave W1,
// ADR-004 decision 1). The mailstore keeps the raw RFC 5322 message as the
// source of truth (models.MailMessage.BodyBlobRef), so sections are located
// in the stored bytes — never reassembled and never transfer-decoded (IMAP
// returns part content exactly as transmitted; clients decode).
//
// Supported section specs (RFC 9051 §4.3.5.7 subset, the ADR-004 command list):
//
//	""            the entire raw message
//	HEADER        top-level header block incl. the separating blank line
//	HEADER.FIELDS (list)     matching header fields + blank line
//	HEADER.FIELDS.NOT (list) complement + blank line
//	TEXT          body after the top-level header block
//	1, 2, 2.1 …   located MIME part by 1-based path (multipart children and
//	              message/rfc822 encapsulations numbered per RFC)
//	n.MIME        the MIME entity header of part n
//	n.HEADER / n.TEXT / n.HEADER.FIELDS…   only for message/rfc822 parts and
//	              the message itself (per RFC); other parts yield an empty
//	              section
//
// A section that does not exist yields an empty result, not an error — the
// IMAP response for a missing section is an empty/NIL section.

const lineEnding = "\r\n"

// mimeEntity is one entity of the MIME tree: the full raw bytes plus the
// header/body split. isMsg marks entities the HEADER/TEXT specifiers apply to
// (the message itself and message/rfc822 encapsulations).
type mimeEntity struct {
	raw    []byte // headers + body, exactly as stored
	header []byte // header block INCLUDING the terminating blank line
	body   []byte // everything after the blank line
	isMsg  bool
}

// parseEntity splits an entity's raw bytes at the first blank line. An entity
// with no blank line is treated as header-only (empty body).
func parseEntity(raw []byte, isMsg bool) *mimeEntity {
	e := &mimeEntity{raw: raw, isMsg: isMsg}
	if i := findBlankLine(raw); i >= 0 {
		e.header = raw[:i]
		e.body = raw[i:]
		return e
	}
	e.header = raw
	e.body = nil
	return e
}

// findBlankLine locates the end of the header block: the first "\n\n" or
// "\n\r\n", returning the index just past the blank line's terminator — the
// blank line itself belongs to the header section (RFC 5322), so
// header = raw[:i] includes it and body = raw[i:] starts exactly where the
// TEXT specifier begins.
func findBlankLine(raw []byte) int {
	for i := 0; i+1 < len(raw); i++ {
		if raw[i] != '\n' {
			continue
		}
		if raw[i+1] == '\n' {
			return i + 2
		}
		if raw[i+1] == '\r' && i+2 < len(raw) && raw[i+2] == '\n' {
			return i + 3
		}
	}
	return -1
}

// headerField is one parsed header field: the lowercased name and the raw
// lines of the field (the field line plus any folded continuation lines),
// terminators stripped.
type headerField struct {
	name  string
	lines [][]byte
}

// parseHeaderFields splits a raw header block into fields, keeping folded
// continuation lines with their field. Malformed leading lines (no colon)
// are skipped.
func parseHeaderFields(header []byte) []headerField {
	var fields []headerField
	start := 0
	for start < len(header) {
		end := start
		for end < len(header) && header[end] != '\n' {
			end++
		}
		if end < len(header) {
			end++ // include the \n
		}
		line := bytes.TrimRight(header[start:end], "\r\n")
		start = end
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			// Folded continuation of the previous field.
			if len(fields) > 0 {
				fields[len(fields)-1].lines = append(fields[len(fields)-1].lines, line)
			}
			continue
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		// A field name cannot contain spaces before the colon.
		name := line[:colon]
		if bytes.ContainsAny(name, " \t") {
			continue
		}
		fields = append(fields, headerField{
			name:  strings.ToLower(string(name)),
			lines: [][]byte{line},
		})
	}
	return fields
}

// headerValue returns one field's unfolded value ("" when absent). Folding
// whitespace is normalized to a single space, which is what MIME parsers expect.
func headerValue(header []byte, name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	for _, f := range parseHeaderFields(header) {
		if f.name != name {
			continue
		}
		for i, l := range f.lines {
			val := l
			if c := bytes.IndexByte(val, ':'); c >= 0 {
				val = val[c+1:]
			}
			if i == 0 {
				b.WriteString(strings.TrimSpace(string(val)))
			} else {
				b.WriteByte(' ')
				b.WriteString(strings.TrimSpace(string(val)))
			}
		}
		break // first occurrence only
	}
	return b.String()
}

// headerFieldLines renders the HEADER.FIELDS / HEADER.FIELDS.NOT section:
// the matching fields' raw lines (with continuations) each terminated by
// CRLF, followed by the terminating blank line (RFC 9051 examples).
func headerFieldLines(header []byte, want []string, not bool) []byte {
	needle := make(map[string]struct{}, len(want))
	for _, w := range want {
		needle[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
	var out bytes.Buffer
	for _, f := range parseHeaderFields(header) {
		_, hit := needle[f.name]
		if hit != not {
			for _, l := range f.lines {
				out.Write(l)
				out.WriteString(lineEnding)
			}
		}
	}
	out.WriteString(lineEnding)
	return out.Bytes()
}

// contentType parses the entity's Content-Type header; a missing or malformed
// header defaults to text/plain per RFC 2045.
func (e *mimeEntity) contentType() (mediaType string, params map[string]string) {
	h := headerValue(e.header, "content-type")
	if h == "" {
		return "text/plain", nil
	}
	mt, params, err := mime.ParseMediaType(h)
	if err != nil {
		return "text/plain", nil
	}
	return mt, params
}

func (e *mimeEntity) isMultipart() bool {
	mt, _ := e.contentType()
	return strings.HasPrefix(strings.ToLower(mt), "multipart/")
}

func (e *mimeEntity) isMessage() bool {
	mt, _ := e.contentType()
	return strings.EqualFold(mt, "message/rfc822")
}

// splitMultipart splits a multipart entity's body into the raw child parts,
// excluding the boundary delimiter lines, the CRLF that belongs to each
// delimiter (RFC 2046 §5.1.1), the preamble and the epilogue. A missing close
// boundary is tolerated leniently (the last part runs to end of body), which
// is how real-world parsers behave and keeps slightly broken stored mail
// fetchable rather than lost.
func splitMultipart(body []byte, boundary string) [][]byte {
	if boundary == "" {
		return nil
	}
	dash := "--" + boundary
	needle := []byte("\n" + dash)

	// First delimiter: either the body starts with it (no preamble) or the
	// first "\n"+dash line.
	var first int
	if bytes.HasPrefix(body, []byte(dash)) {
		first = 0
	} else if j := bytes.Index(body, needle); j >= 0 {
		first = j + 1
	} else {
		return nil
	}

	var parts [][]byte
	pos := first + len(dash)
	if bytes.HasPrefix(body[pos:], []byte("--")) {
		return nil // empty multipart (close delimiter immediately)
	}
	if p := bytes.IndexByte(body[pos:], '\n'); p >= 0 {
		pos += p + 1
	} else {
		return nil
	}
	for {
		next := -1
		if j := bytes.Index(body[pos:], needle); j >= 0 {
			next = pos + j + 1
		}
		var end int
		if next < 0 {
			// No further delimiter: the part runs to the end of the body.
			end = len(body)
		} else {
			end = next
			if end > 0 && body[end-1] == '\n' {
				end--
				if end > 0 && body[end-1] == '\r' {
					end--
				}
			}
		}
		parts = append(parts, body[pos:end])
		if next < 0 {
			break
		}
		pos = next + len(dash)
		if bytes.HasPrefix(body[pos:], []byte("--")) {
			break // closing delimiter; epilogue ignored
		}
		if p := bytes.IndexByte(body[pos:], '\n'); p >= 0 {
			pos += p + 1
		} else {
			break
		}
	}
	return parts
}

// sectionSpec is a parsed BODY section fetch request (the mailstore-level
// shape; the OB-4 protocol layer translates wire syntax into it).
type sectionSpec struct {
	Part      string   // "", "1", "1.2", "2", …
	Terminal  string   // "", "HEADER", "TEXT", "MIME", "HEADER.FIELDS"
	Fields    []string // HEADER.FIELDS list
	FieldsNot bool     // true = HEADER.FIELDS.NOT
}

// parseSectionSpec splits a section string ("2.HEADER.FIELDS", "1.3", "TEXT",
// "2.MIME") into the numeric part path and the terminal specifier. The
// HEADER.FIELDS field list is carried by the caller (BodySection.Fields)
// because the wire syntax puts it in parentheses after the keyword.
func parseSectionSpec(spec string) (sectionSpec, bool) {
	out := sectionSpec{}
	if spec == "" {
		return out, true
	}
	segs := strings.Split(spec, ".")
	i := 0
	for ; i < len(segs); i++ {
		seg := segs[i]
		if isDigits(seg) {
			if len(seg) > 10 { // overflow guard; part numbers never reach 10 digits
				return out, false
			}
			continue
		}
		break
	}
	if i == 0 {
		// No leading numeric path: the whole spec is a terminal on the message.
		term := strings.ToUpper(spec)
		if !isTerminalKeyword(term) || term == "MIME" {
			// MIME applies to parts only — a bare "MIME" is malformed.
			return out, false
		}
		out.Terminal = term
		return out, true
	}
	out.Part = strings.Join(segs[:i], ".")
	if i < len(segs) {
		term := strings.ToUpper(strings.Join(segs[i:], "."))
		// Only a single terminal keyword may follow the part path.
		if len(segs)-i != 1 || !isTerminalKeyword(term) {
			return out, false
		}
		out.Terminal = term
	}
	return out, true
}

func isTerminalKeyword(t string) bool {
	switch t {
	case "HEADER", "TEXT", "MIME", "HEADER.FIELDS", "HEADER.FIELDS.NOT":
		return true
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// locateSection walks the raw message bytes and returns the requested section.
// A second return of false means the section does not exist (caller responds
// with an empty section).
func locateSection(raw []byte, spec sectionSpec) ([]byte, bool) {
	top := parseEntity(raw, true)

	// Terminal-only specs on the message itself.
	if spec.Part == "" {
		return terminalSection(top, spec)
	}

	// Numeric descent. containerOK marks entities that may own numbered
	// children even when not multipart: the top-level message and any
	// message/rfc822 encapsulation (RFC 9051: non-multipart messages "only
	// have a part 1"; leaf MIME parts have no children at all).
	cur := top
	containerOK := true
	for _, seg := range strings.Split(spec.Part, ".") {
		child, ok := descendMIME(cur, seg, containerOK)
		if !ok {
			return nil, false
		}
		cur = child
		media, _ := cur.contentType()
		containerOK = strings.EqualFold(media, "message/rfc822")
	}
	switch spec.Terminal {
	case "":
		return cur.raw, true
	case "MIME":
		if cur.header == nil {
			return nil, false
		}
		return cur.header, true
	case "HEADER":
		if !cur.isMsg {
			return nil, false
		}
		return cur.header, true
	case "TEXT":
		if !cur.isMsg {
			return nil, false
		}
		return cur.body, true
	case "HEADER.FIELDS", "HEADER.FIELDS.NOT":
		if !cur.isMsg {
			return nil, false
		}
		return headerFieldLines(cur.header, spec.Fields, spec.Terminal == "HEADER.FIELDS.NOT"), true
	}
	return nil, false
}

// terminalSection applies a terminal-only spec to the message entity.
func terminalSection(e *mimeEntity, spec sectionSpec) ([]byte, bool) {
	switch spec.Terminal {
	case "":
		return e.raw, true
	case "HEADER":
		return e.header, true
	case "TEXT":
		return e.body, true
	case "HEADER.FIELDS", "HEADER.FIELDS.NOT":
		return headerFieldLines(e.header, spec.Fields, spec.Terminal == "HEADER.FIELDS.NOT"), true
	case "MIME":
		// MIME applies to parts, not to the whole message.
		return nil, false
	}
	return nil, false
}

// descendMIME selects the seg-th child of one entity, following the RFC 9051
// numbering model: multipart children are numbered 1..n; a message/rfc822
// entity re-enters its encapsulated message (a multipart encapsulation's
// parts become the entity's children; a simple one contributes part "1" = its
// body); the top-level message that is not multipart has part "1" = its
// content; leaf MIME parts have no children.
func descendMIME(e *mimeEntity, seg string, containerOK bool) (*mimeEntity, bool) {
	n := 0
	for i := 0; i < len(seg); i++ {
		n = n*10 + int(seg[i]-'0')
		if n > 1_000_000 {
			return nil, false // absurd depth guard
		}
	}
	if n == 0 {
		return nil, false
	}
	mt, params := e.contentType()
	switch {
	case strings.HasPrefix(strings.ToLower(mt), "multipart/"):
		children := splitMultipart(e.body, params["boundary"])
		if n < 1 || n > len(children) {
			return nil, false
		}
		return parseEntity(children[n-1], isMessageEntity(children[n-1])), true
	case strings.EqualFold(mt, "message/rfc822"):
		enc := parseEntity(e.body, true)
		if enc.isMultipart() {
			_, ep := enc.contentType()
			children := splitMultipart(enc.body, ep["boundary"])
			if n < 1 || n > len(children) {
				return nil, false
			}
			return parseEntity(children[n-1], isMessageEntity(children[n-1])), true
		}
		if n != 1 {
			return nil, false
		}
		// The encapsulated message is simple: part 1 is its body content.
		return &mimeEntity{raw: enc.body, header: nil, body: enc.body, isMsg: false}, true
	case containerOK:
		// The top-level message, not multipart: part 1 is its content.
		if n != 1 {
			return nil, false
		}
		return &mimeEntity{raw: e.body, header: nil, body: e.body, isMsg: false}, true
	}
	return nil, false // leaf part: no children
}

// isMessageEntity reports whether raw entity bytes carry a message/rfc822
// Content-Type (their HEADER/TEXT specifiers then apply).
func isMessageEntity(raw []byte) bool {
	return parseEntity(raw, false).isMessage()
}

// MimePart is one node of the message's MIME structure tree — the input the
// OB-4 protocol layer renders BODY/BODYSTRUCTURE responses from (FETCH macros
// include them). Size is the entity's content size in octets; Lines counts
// content lines for text/message parts (RFC 9051 §4.3.5.7).
type MimePart struct {
	PartID      string            `json:"part_id"`
	Type        string            `json:"type"`
	Subtype     string            `json:"subtype"`
	Params      map[string]string `json:"params,omitempty"`
	Encoding    string            `json:"encoding"`
	Size        int64             `json:"size"`
	Lines       int64             `json:"lines"`
	Disposition string            `json:"disposition,omitempty"`
	Filename    string            `json:"filename,omitempty"`
	IsMessage   bool              `json:"is_message,omitempty"`
	Children    []*MimePart       `json:"children,omitempty"`
}

// maxStructureDepth bounds structure-tree recursion (kept in step with the
// ingest parser's maxMIMEDepth).
const maxStructureDepth = 20

// BuildMimeStructure walks the raw message and returns its MIME structure
// tree. It is a pure function over the stored bytes, so it can never disagree
// with what BODY[...] section fetch returns.
func BuildMimeStructure(raw []byte) *MimePart {
	return buildMimePart(parseEntity(raw, true), "", 0)
}

func buildMimePart(e *mimeEntity, partID string, depth int) *MimePart {
	if depth > maxStructureDepth {
		return &MimePart{PartID: partID, Type: "application", Subtype: "octet-stream"}
	}
	mt, params := e.contentType()
	if params == nil {
		params = map[string]string{}
	}
	p := &MimePart{
		PartID:      partID,
		Encoding:    strings.ToLower(strings.TrimSpace(headerValue(e.header, "content-transfer-encoding"))),
		Size:        int64(len(e.body)),
		Params:      params,
		Disposition: dispositionType(e.header),
		Filename:    filenameOf(params, e.header),
	}
	if p.Encoding == "" {
		p.Encoding = "7bit"
	}
	if i := strings.IndexByte(mt, '/'); i > 0 {
		p.Type, p.Subtype = strings.ToLower(mt[:i]), strings.ToLower(mt[i+1:])
	} else {
		p.Type, p.Subtype = strings.ToLower(mt), ""
	}
	p.IsMessage = e.isMessage()
	if p.Type == "text" || p.IsMessage {
		p.Lines = int64(bytes.Count(e.body, []byte("\n")))
	}
	switch {
	case p.Type == "multipart":
		children := splitMultipart(e.body, params["boundary"])
		for i, c := range children {
			p.Children = append(p.Children, buildMimePart(parseEntity(c, false), childID(partID, i+1), depth+1))
		}
	case e.isMessage():
		p.IsMessage = true
		enc := parseEntity(e.body, true)
		if enc.isMultipart() {
			_, ep := enc.contentType()
			for i, c := range splitMultipart(enc.body, ep["boundary"]) {
				p.Children = append(p.Children, buildMimePart(parseEntity(c, false), childID(partID, i+1), depth+1))
			}
		} else {
			p.Children = append(p.Children, buildMimePart(&mimeEntity{raw: enc.body, body: enc.body}, childID(partID, 1), depth+1))
		}
	}
	return p
}

func childID(parent string, n int) string {
	if parent == "" {
		return itoaPart(n)
	}
	return parent + "." + itoaPart(n)
}

func itoaPart(n int) string {
	return strconv.Itoa(n)
}

func dispositionType(header []byte) string {
	cd := headerValue(header, "content-disposition")
	if cd == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	return strings.ToLower(mt)
}

func filenameOf(params map[string]string, header []byte) string {
	if n := params["name"]; n != "" {
		return n
	}
	if cd := headerValue(header, "content-disposition"); cd != "" {
		if _, cdParams, err := mime.ParseMediaType(cd); err == nil {
			if n := cdParams["filename"]; n != "" {
				return n
			}
		}
	}
	return ""
}
