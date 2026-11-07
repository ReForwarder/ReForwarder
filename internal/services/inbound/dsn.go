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

package inbound

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
)

// RFC 3464 delivery-status-notification parsing (OB-2, ADR-002 decision 4;
// parity SF-10). A bounce accepted through the VERP return-path allowlist is
// a multipart/report;report-type=delivery-status message: per-message fields
// (Reporting-MTA), then one per-recipient field block per affected address.
// This parser extracts those fields so the match-back can turn the report
// into delivery truth on the original message's timeline.
//
// Size caps mirror the DMARC parser's discipline (R-17): every body read is
// bounded, so a crafted "bounce" cannot exhaust receiver memory. The parser
// is failure-isolated — ParseDSN errors never fail the durable ingest that
// stored the message.

const (
	// maxDSNRawBytes bounds how much of the bounce message the parser will
	// even look at. Real DSNs are a few KiB; the receiver's own message cap
	// is much larger, but the DSN parser must not depend on that.
	maxDSNRawBytes = 8 << 20
	// maxDSNPartBytes bounds one MIME part (a delivery-status body, a
	// returned message). Anything larger is refused, not truncated.
	maxDSNPartBytes = 1 << 20
	// maxDSNRecipientRecords bounds the per-recipient blocks parsed from one
	// delivery-status body. VERP bounce addresses are per-message, so a real
	// report carries one record; the cap tolerates consolidated reports.
	maxDSNRecipientRecords = 100
)

// DSNAction is the per-recipient disposition of a delivery-status record
// (RFC 3464 §2.3.3): failed, delayed, or delivered. relayed/expanded are
// transport intermediates — kept as their raw value, never matched back.
type DSNAction string

const (
	DSNActionFailed    DSNAction = "failed"
	DSNActionDelayed   DSNAction = "delayed"
	DSNActionDelivered DSNAction = "delivered"
)

// DSNRecord is one per-recipient block of the delivery-status part.
type DSNRecord struct {
	// Action is the normalized (lowercased) Action field.
	Action DSNAction
	// Status is the enhanced status code token, e.g. "5.1.1".
	Status string
	// OriginalRecipient is the envelope recipient the DSN is about — the
	// addr-type prefix ("rfc822;") stripped, lowercased. Empty when the
	// report omitted the field; FinalRecipient is the fallback.
	OriginalRecipient string
	// FinalRecipient is the Final-Recipient field, normalized like
	// OriginalRecipient.
	FinalRecipient string
	// DiagnosticCode is the remote MTA's diagnostic text (may be empty).
	DiagnosticCode string
	// RemoteMTA is the Remote-MTA field, when present.
	RemoteMTA string
}

// DSN is a parsed RFC 3464 delivery-status notification.
type DSN struct {
	// ReportingMTA is the per-message Reporting-MTA field (addr-type stripped).
	ReportingMTA string
	// Records holds one entry per per-recipient field block.
	Records []DSNRecord
}

// Recipient returns the address a record is about: the Original-Recipient
// when present, else the Final-Recipient.
func (r DSNRecord) Recipient() string {
	if r.OriginalRecipient != "" {
		return r.OriginalRecipient
	}
	return r.FinalRecipient
}

// ErrNotDSN reports that a message is not an RFC 3464 delivery-status
// notification (no multipart/report carrying a message/delivery-status part).
var ErrNotDSN = errors.New("not an RFC 3464 delivery-status notification")

// ParseDSN parses the raw RFC 5322 bytes of a bounce message. It returns
// ErrNotDSN for anything without a delivery-status part — callers log and
// move on; the bounce stays durably stored either way.
func ParseDSN(raw []byte) (*DSN, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty message", ErrNotDSN)
	}
	if len(raw) > maxDSNRawBytes {
		raw = raw[:maxDSNRawBytes]
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse DSN headers: %w", err)
	}
	mediaType, params, err := parseContentType(msg.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable content type", ErrNotDSN)
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil, fmt.Errorf("%w: content type %q", ErrNotDSN, mediaType)
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return nil, fmt.Errorf("%w: multipart without boundary", ErrNotDSN)
	}

	dsn := &DSN{}
	mr := multipart.NewReader(io.LimitReader(msg.Body, maxDSNRawBytes), boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// A malformed trailing part must not discard the records already
			// parsed from earlier parts.
			if len(dsn.Records) > 0 {
				return dsn, nil
			}
			return nil, fmt.Errorf("walk DSN multipart: %w", err)
		}
		if isDeliveryStatusPart(part) {
			records, reportingMTA, err := parseDeliveryStatus(io.LimitReader(part, maxDSNPartBytes+1))
			if err != nil {
				continue // malformed status part: try the next one
			}
			dsn.Records = append(dsn.Records, records...)
			if dsn.ReportingMTA == "" {
				dsn.ReportingMTA = reportingMTA
			}
		}
	}
	if len(dsn.Records) == 0 {
		return nil, fmt.Errorf("%w: no message/delivery-status part", ErrNotDSN)
	}
	return dsn, nil
}

// isDeliveryStatusPart reports whether a MIME part carries the per-recipient
// delivery-status fields. message/delivery-status is the RFC 3464 type; a
// plain-text fallback covers MTAs that mislabel the part (the field blocks
// are text either way).
func isDeliveryStatusPart(part *multipart.Part) bool {
	mediaType, _, err := parseContentType(part.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == "message/delivery-status"
}

// parseDeliveryStatus reads one delivery-status body: the per-message field
// block, a blank line, then one per-recipient field block per affected
// recipient (RFC 3464 §2.2). Field names are matched case-insensitively;
// unknown fields are ignored. The body is read bounded like the DMARC
// parser's readBounded: a part over the cap is refused, never truncated.
func parseDeliveryStatus(r io.Reader) ([]DSNRecord, string, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxDSNPartBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read delivery-status part: %w", err)
	}
	if len(body) > maxDSNPartBytes {
		return nil, "", fmt.Errorf("delivery-status part exceeds the %d byte DSN cap", maxDSNPartBytes)
	}
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(body)))

	// Per-message fields.
	msgHeader, err := reader.ReadMIMEHeader()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("read per-message DSN fields: %w", err)
	}
	reportingMTA := addrTypeValue(msgHeader.Get("Reporting-MTA"))

	var records []DSNRecord
	for len(records) < maxDSNRecipientRecords {
		header, err := reader.ReadMIMEHeader()
		// The final record block may end at part EOF without a trailing
		// blank line — the header is still valid, so it is recorded before
		// the loop breaks on the error.
		if len(header) > 0 {
			records = append(records, DSNRecord{
				Action:            dsnActionOf(header.Get("Action")),
				Status:            dsnStatusOf(header.Get("Status")),
				OriginalRecipient: dsnAddressOf(header.Get("Original-Recipient")),
				FinalRecipient:    dsnAddressOf(header.Get("Final-Recipient")),
				DiagnosticCode:    strings.TrimSpace(header.Get("Diagnostic-Code")),
				RemoteMTA:         addrTypeValue(header.Get("Remote-MTA")),
			})
		}
		if err != nil {
			// EOF: the field sequence is done. Anything else: a malformed
			// record block ends it — keep what parsed cleanly.
			break
		}
	}
	if len(records) == 0 && reportingMTA == "" {
		return nil, "", errors.New("delivery-status body has no field blocks")
	}
	return records, reportingMTA, nil
}

// dsnActionOf normalizes the Action field; unknown values keep their raw
// lowercased form so nothing is silently reclassified.
func dsnActionOf(v string) DSNAction {
	return DSNAction(strings.ToLower(strings.TrimSpace(firstToken(v))))
}

// dsnStatusOf extracts the enhanced-status token from e.g.
// "5.1.1 (host example.net said: 550 ...)". Quoted free text after the code
// is dropped.
func dsnStatusOf(v string) string {
	return strings.ToLower(strings.TrimSpace(firstToken(v)))
}

// dsnAddressOf normalizes an "addr-type;address" field ("rfc822;user@host")
// to the bare lowercased address. A value without an addr-type prefix is
// taken as the address itself.
func dsnAddressOf(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, ";"); i >= 0 {
		v = strings.TrimSpace(v[i+1:])
	}
	v = strings.Trim(v, "<>")
	return strings.ToLower(v)
}

// addrTypeValue strips an "addr-type;value" prefix for fields like
// Reporting-MTA / Remote-MTA.
func addrTypeValue(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, ";"); i >= 0 {
		return strings.TrimSpace(v[i+1:])
	}
	return v
}

// firstToken returns the whitespace-delimited first token of v.
func firstToken(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, " \t"); i >= 0 {
		return v[:i]
	}
	return v
}

// parseContentType parses a Content-Type header defensively: an unparseable
// or empty value is reported as an error rather than defaulted, so the
// caller can distinguish "not multipart" from "unreadable".
func parseContentType(v string) (string, map[string]string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "text/plain", nil, nil
	}
	mt, params, err := mime.ParseMediaType(v)
	if err != nil {
		return "", nil, err
	}
	return strings.ToLower(mt), params, nil
}
