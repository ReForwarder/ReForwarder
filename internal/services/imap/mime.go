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

package imap

import (
	"bytes"
	"net/mail"
	"strconv"
	"strings"

	"github.com/goposta/posta/internal/services/mailstore"
)

// headerField is one raw header field: lowercased name plus its unfolded
// value (folded continuation lines joined with single spaces).
type headerField struct {
	name  string
	value string
}

// headerFields splits a raw header block into fields, keeping folded
// continuation lines with their field and skipping malformed lines. This is
// the imap package's own header reader over the stored raw bytes (the
// mailstore keeps its ingest parser internal).
func headerFields(header []byte) []headerField {
	var out []headerField
	start := 0
	for start < len(header) {
		end := start
		for end < len(header) && header[end] != '\n' {
			end++
		}
		if end < len(header) {
			end++ // include the newline
		}
		line := strings.TrimRight(string(header[start:end]), "\r\n")
		start = end
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			// Folded continuation of the previous field.
			if len(out) > 0 {
				out[len(out)-1].value += " " + strings.TrimSpace(line)
			}
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue // no field name (or an empty one)
		}
		name := line[:colon]
		if strings.ContainsAny(name, " \t") {
			continue // a field name cannot contain spaces before the colon
		}
		out = append(out, headerField{
			name:  strings.ToLower(name),
			value: strings.TrimSpace(line[colon+1:]),
		})
	}
	return out
}

// headerValue returns one field's unfolded value ("" when absent), first
// occurrence only.
func headerValue(header []byte, field string) string {
	field = strings.ToLower(field)
	for _, f := range headerFields(header) {
		if f.name == field {
			return f.value
		}
	}
	return ""
}

// ENVELOPE and BODYSTRUCTURE rendering (RFC 9051 §4.3.5.7 — the FETCH
// response shapes). The envelope is built from the stored raw message's
// header block (net/mail address parsing); the bodystructure renders the W1
// BuildMimeStructure tree.
//
// Documented limitation: message/rfc822 parts' encapsulated ENVELOPE renders
// as NIL (the W1 MimePart tree does not carry nested raw headers).

// envelopeText is one message's parsed envelope inputs.
type envelopeText struct {
	Date      string
	Subject   string
	From      []mail.Address
	Sender    []mail.Address
	ReplyTo   []mail.Address
	To        []mail.Address
	Cc        []mail.Address
	Bcc       []mail.Address
	InReplyTo string
	MessageID string
}

// buildEnvelope parses the header block of raw message bytes into envelopeText.
func buildEnvelope(raw []byte) envelopeText {
	env := envelopeText{}
	header := rawHeaderBlock(raw)
	env.Date = headerValue(header, "date")
	env.Subject = headerValue(header, "subject")
	env.From = parseAddrList(header, "from")
	env.Sender = parseAddrList(header, "sender")
	env.ReplyTo = parseAddrList(header, "reply-to")
	env.To = parseAddrList(header, "to")
	env.Cc = parseAddrList(header, "cc")
	env.Bcc = parseAddrList(header, "bcc")
	env.InReplyTo = headerValue(header, "in-reply-to")
	env.MessageID = headerValue(header, "message-id")
	if env.Sender == nil {
		env.Sender = env.From
	}
	if env.ReplyTo == nil {
		env.ReplyTo = env.From
	}
	return env
}

// rawHeaderBlock returns the message's header bytes (through the first blank
// line), tolerating LF-only endings.
func rawHeaderBlock(raw []byte) []byte {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+2]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i+1]
	}
	return raw
}

// parseAddrList parses one header field's address list (empty when absent or
// malformed).
func parseAddrList(header []byte, field string) []mail.Address {
	v := headerValue(header, field)
	if v == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(v)
	if err != nil {
		if a, aerr := mail.ParseAddress(v); aerr == nil {
			return []mail.Address{*a}
		}
		return nil
	}
	out := make([]mail.Address, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, *a)
	}
	return out
}

// render builds the IMAP ENVELOPE parenthesized structure.
func (e envelopeText) render() string {
	var b strings.Builder
	b.WriteByte('(')
	b.WriteString(nilString(e.Date))
	b.WriteByte(' ')
	b.WriteString(nilString(e.Subject))
	b.WriteByte(' ')
	b.WriteString(renderAddrList(e.From))
	b.WriteByte(' ')
	b.WriteString(renderAddrList(e.Sender))
	b.WriteByte(' ')
	b.WriteString(renderAddrList(e.ReplyTo))
	b.WriteByte(' ')
	b.WriteString(renderAddrList(e.To))
	b.WriteByte(' ')
	b.WriteString(renderAddrList(e.Cc))
	b.WriteByte(' ')
	b.WriteString(renderAddrList(e.Bcc))
	b.WriteByte(' ')
	b.WriteString(nilString(e.InReplyTo))
	b.WriteByte(' ')
	b.WriteString(nilString(e.MessageID))
	b.WriteByte(')')
	return b.String()
}

// renderAddrList renders "(addr addr)" or NIL.
func renderAddrList(addrs []mail.Address) string {
	if len(addrs) == 0 {
		return "NIL"
	}
	var b strings.Builder
	b.WriteByte('(')
	for i, a := range addrs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('(')
		b.WriteString(nilString(a.Name))
		b.WriteString(" NIL ")
		b.WriteString(nilString(a.Address))
		b.WriteString(" NIL)")
	}
	b.WriteByte(')')
	return b.String()
}

// nilString renders "" as the IMAP NIL atom (quoted strings get escaped).
func nilString(s string) string {
	if s == "" {
		return "NIL"
	}
	return quoteString(s)
}

// renderBodyStructure renders a MimePart tree as BODY (extensible=false) or
// BODYSTRUCTURE (extensible=true). A nil tree renders NIL (the client asked
// for structure on a message whose bytes are unreadable — an honest answer,
// not a fake one).
func renderBodyStructure(p *mailstore.MimePart, extensible bool) string {
	if p == nil {
		return "NIL"
	}
	var b strings.Builder
	renderPart(&b, p, extensible)
	return b.String()
}

func renderPart(b *strings.Builder, p *mailstore.MimePart, extensible bool) {
	if p.Type == "multipart" {
		b.WriteByte('(')
		for _, c := range p.Children {
			renderPart(b, c, extensible)
		}
		// Multipart: subtype (uppercased on the wire), then extension fields
		// when extensible.
		b.WriteString(" \"" + strings.ToUpper(p.Subtype) + "\"")
		if extensible {
			b.WriteString(" " + renderParams(p.Params))
			b.WriteString(" " + renderDisposition(p.Disposition, p.Filename))
			b.WriteString(" NIL NIL") // language, location
		}
		b.WriteByte(')')
		return
	}
	b.WriteByte('(')
	// Media types are uppercased on the wire (RFC 9051 bodystructure).
	b.WriteString("\"" + strings.ToUpper(p.Type) + "\" \"" + strings.ToUpper(p.Subtype) + "\"")
	b.WriteString(" " + renderParams(p.Params))
	b.WriteString(" NIL") // Content-ID: not tracked in the structure tree
	b.WriteString(" NIL") // description
	b.WriteString(" \"" + strings.ToUpper(p.Encoding) + "\"")
	b.WriteString(" " + intString(p.Size))
	if p.Type == "text" {
		b.WriteString(" " + intString(p.Lines))
	}
	if p.IsMessage {
		// message/rfc822: envelope, body, lines. The encapsulated envelope
		// renders NIL — documented limitation (the W1 MimePart tree carries
		// no nested raw headers).
		b.WriteString(" NIL")
		if len(p.Children) > 0 {
			b.WriteByte(' ')
			renderPart(b, p.Children[0], extensible)
		} else {
			b.WriteString(" NIL")
		}
		b.WriteString(" " + intString(p.Lines))
	}
	if extensible {
		b.WriteString(" NIL") // MD5
		b.WriteString(" " + renderDisposition(p.Disposition, p.Filename))
		b.WriteString(" NIL NIL") // language, location
	}
	b.WriteByte(')')
}

// renderParams renders ("KEY" "value" …) or NIL.
func renderParams(params map[string]string) string {
	if len(params) == 0 {
		return "NIL"
	}
	var b strings.Builder
	b.WriteByte('(')
	first := true
	for _, k := range sortedKeys(params) {
		v := params[k]
		if v == "" {
			continue
		}
		if !first {
			b.WriteByte(' ')
		}
		first = false
		b.WriteString(quoteString(strings.ToUpper(k)))
		b.WriteByte(' ')
		b.WriteString(quoteString(v))
	}
	if first {
		return "NIL"
	}
	b.WriteByte(')')
	return b.String()
}

// renderDisposition renders ("ATTACHMENT" ("FILENAME" "x")) / ("INLINE" NIL) / NIL.
func renderDisposition(disposition, filename string) string {
	if disposition == "" && filename == "" {
		return "NIL"
	}
	if disposition == "" {
		disposition = "inline"
	}
	var b strings.Builder
	b.WriteByte('(')
	b.WriteString(quoteString(strings.ToUpper(disposition)))
	b.WriteByte(' ')
	if filename == "" {
		b.WriteString("NIL")
	} else {
		b.WriteString(`("FILENAME" ` + quoteString(filename) + `)`)
	}
	b.WriteByte(')')
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// insertion sort for deterministic responses
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func intString(n int64) string { return strconv.FormatInt(n, 10) }
