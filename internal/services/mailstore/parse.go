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
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"

	xcharset "golang.org/x/net/html/charset"
)

// This is a self-contained RFC 5322/MIME parser for ingest. It deliberately
// does NOT reuse internal/services/inbound's parser: the ingestion wave will
// wire inbound → mailstore, and a reverse dependency would create an import
// cycle. Behavior mirrors the inbound pipeline's shape (same stdlib
// primitives) so both paths agree on what a message looks like.

// ParsedAttachment is one decoded attachment from the MIME tree.
type ParsedAttachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// ParsedMessage is the normalized ingest shape.
type ParsedMessage struct {
	MessageID   string
	From        string
	To          []string
	Cc          []string
	Subject     string
	Date        time.Time
	TextBody    string
	HTMLBody    string
	Attachments []ParsedAttachment
}

// ParseMessage parses raw RFC 5322 bytes into a ParsedMessage.
func ParseMessage(raw []byte) (*ParsedMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	p := &ParsedMessage{}
	dec := &mail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetReader}}
	if v := msg.Header.Get("Message-Id"); v != "" {
		p.MessageID = strings.Trim(v, "<> \t")
	} else if v := msg.Header.Get("Message-ID"); v != "" {
		p.MessageID = strings.Trim(v, "<> \t")
	}
	if addrs, err := dec.ParseList(msg.Header.Get("From")); err == nil && len(addrs) > 0 {
		p.From = addrs[0].Address
	} else if raw := msg.Header.Get("From"); raw != "" {
		p.From = strings.TrimSpace(raw)
	}
	p.To = addressList(dec, msg.Header.Get("To"))
	p.Cc = addressList(dec, msg.Header.Get("Cc"))
	p.Subject = decodeHeader(msg.Header.Get("Subject"))
	if d, err := mail.ParseDate(msg.Header.Get("Date")); err == nil {
		p.Date = d.UTC()
	} else {
		p.Date = time.Now().UTC()
	}
	if err := walkPart(msg.Header, msg.Body, &p.TextBody, &p.HTMLBody, &p.Attachments, 0); err != nil {
		return nil, fmt.Errorf("walk mime tree: %w", err)
	}
	return p, nil
}

// maxMIMEDepth bounds recursive multipart descent.
const maxMIMEDepth = 20

// walkPart recurses through the MIME tree collecting text bodies and
// attachments (disposition attachment, or a non-text leaf part with a name).
func walkPart(h map[string][]string, r io.Reader, text, html *string, atts *[]ParsedAttachment, depth int) error {
	if depth > maxMIMEDepth {
		return fmt.Errorf("mime nesting deeper than %d", maxMIMEDepth)
	}
	header := fromMailHeader(h)
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		mediaType = "text/plain"
	}
	filename := params["name"]
	isAttachment := false
	if cdType, cdParams, cdErr := mime.ParseMediaType(header.Get("Content-Disposition")); cdErr == nil {
		if strings.EqualFold(cdType, "attachment") {
			isAttachment = true
		}
		if n, ok := cdParams["filename"]; ok {
			filename = n
		}
	} else if strings.EqualFold(header.Get("Content-Disposition"), "attachment") {
		isAttachment = true
	}

	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		mr := multipart.NewReader(r, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("multipart part: %w", err)
			}
			if err := walkPart(part.Header, part, text, html, atts, depth+1); err != nil {
				return err
			}
		}
	case isAttachment || (filename != "" && !strings.HasPrefix(mediaType, "text/")):
		data, err := decodeBody(header, r)
		if err != nil {
			return err
		}
		if filename == "" {
			filename = "unnamed"
		}
		*atts = append(*atts, ParsedAttachment{
			Filename:    decodeHeader(filename),
			ContentType: mediaType,
			Content:     data,
		})
	case strings.EqualFold(mediaType, "text/plain"):
		data, err := decodeBody(header, r)
		if err != nil {
			return err
		}
		if *text == "" {
			*text = string(data)
		}
	case strings.EqualFold(mediaType, "text/html"):
		data, err := decodeBody(header, r)
		if err != nil {
			return err
		}
		if *html == "" {
			*html = string(data)
		}
	default:
		// Unrecognized leaf: skip rather than fail ingest — a message must
		// never be lost because it contains an exotic part.
	}
	return nil
}

// decodeBody applies Content-Transfer-Encoding.
func decodeBody(header textHeader, r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read part body: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Transfer-Encoding"))) {
	case "base64":
		out := make([]byte, base64.StdEncoding.DecodedLen(len(raw)))
		n, err := base64.StdEncoding.Decode(out, stripWhitespace(raw))
		if err != nil {
			// Lenient: keep what decoded. A message must not be lost over a
			// slightly malformed base64 tail.
			if n == 0 {
				return nil, nil
			}
			return out[:n], nil
		}
		return out[:n], nil
	case "quoted-printable":
		out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil {
			return raw, nil // lenient fallback
		}
		return out, nil
	default:
		return raw, nil
	}
}

func stripWhitespace(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
		default:
			out = append(out, c)
		}
	}
	return out
}

// textHeader is the minimal header view walkPart needs.
type textHeader interface {
	Get(key string) string
}

func fromMailHeader(h map[string][]string) textHeader {
	return textHeaderFunc(func(key string) string {
		if vs, ok := h[key]; ok && len(vs) > 0 {
			return vs[0]
		}
		return ""
	})
}

type textHeaderFunc func(key string) string

func (f textHeaderFunc) Get(key string) string { return f(key) }

func addressList(dec *mail.AddressParser, header string) []string {
	addrs, err := dec.ParseList(header)
	if err != nil || len(addrs) == 0 {
		if header == "" {
			return nil
		}
		// Lenient: keep the raw value as a single entry.
		return []string{strings.TrimSpace(header)}
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Address != "" {
			out = append(out, a.Address)
		}
	}
	return out
}

func decodeHeader(v string) string {
	if v == "" {
		return ""
	}
	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	if decoded, err := dec.DecodeHeader(v); err == nil {
		return decoded
	}
	return v
}

// charsetReader converts non-UTF-8 charsets via golang.org/x/net/html/charset,
// same as the inbound pipeline.
func charsetReader(charsetName string, input io.Reader) (io.Reader, error) {
	enc, _ := xcharset.Lookup(charsetName)
	if enc == nil {
		return nil, fmt.Errorf("unhandled charset %q", charsetName)
	}
	return enc.NewDecoder().Reader(input), nil
}
