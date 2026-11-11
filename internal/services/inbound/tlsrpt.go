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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package inbound

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/tlsrpt"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SMTP TLS report ingestion (SF-31 inbound half, OB-6 W4; ADR-007 §5).
//
// The pattern is the R-17 DMARC one (dmarc.go), per ADR-002's ingestion
// discipline: the message is durably stored first, then offered to the
// processor; every parse that yields a valid RFC 8460 document is stored as
// per-policy rows keyed (user, report_id, policy_domain) so re-ingestion is
// idempotent. Two differences from DMARC, both by design:
//
//  1. The RCPT allowlist: TLS-RPT reports arrive at the rua address the
//     domain publishes (_smtp._tls.<domain> v=TLSRPTv1; rua=mailto:...).
//     That address is a system address — no inbound route can exist for it —
//     so the receiver accepts tlsrpt@<verified-domain> through a dedicated
//     allowlist (mirror of the VERP return-path allowlist) and the ingest
//     path stores the message without routing-rule evaluation.
//  2. Detection is allowlist-driven rather than purely parse-driven: mail to
//     a tenant's tlsrpt@ address is offered to the parser, everything else
//     is not (the allowlist keeps the JSON parser off arbitrary inbound mail).
//
// The processor is opt-in per process: EnableTLSRPT wires it; a Service
// without it ignores TLS reports entirely. Abuse posture: strict JSON shape
// validation, bounded decompression (the DMARC gzip-bomb caps), idempotent
// storage, and the reports never feed routing, forwarding, or suppression.

// tlsRptLocalPart is the local part of the dedicated rua address this
// implementation publishes in its DNS guidance
// (rua=mailto:tlsrpt@<domain>). One fixed address keeps the record guidance
// and the RCPT allowlist in lockstep.
const tlsRptLocalPart = "tlsrpt"

// TlsRptAllowlist recognizes the dedicated TLS-RPT rua address on a verified
// domain (OB-6 W4). It is the TLS-RPT sibling of the VERP bounce allowlist:
// a system address accepted before ordinary recipient validation, resolved
// to its parent tenant domain. There is nothing to HMAC here (unlike VERP,
// the rua address is public by construction — it rides a public DNS record),
// so the abuse boundary is the parse-strict, size-capped, idempotent
// processor below.
type TlsRptAllowlist struct {
	local string
}

// NewTlsRptAllowlist builds the allowlist for the fixed tlsrpt local part.
func NewTlsRptAllowlist() *TlsRptAllowlist { return &TlsRptAllowlist{local: tlsRptLocalPart} }

// LooksLikeReport reports whether addr is the TLS-RPT system address shape
// (local part match, non-empty host). Cheap; the verified-domain check
// happens at the call site.
func (a *TlsRptAllowlist) LooksLikeReport(addr string) bool {
	if a == nil {
		return false
	}
	local, host, ok := splitAddress(strings.ToLower(strings.TrimSpace(addr)))
	return ok && local == a.local && host != ""
}

// ReportDomain returns the domain part of a recognized rua address.
func (a *TlsRptAllowlist) ReportDomain(addr string) (string, bool) {
	if !a.LooksLikeReport(addr) {
		return "", false
	}
	_, host, ok := splitAddress(strings.ToLower(strings.TrimSpace(addr)))
	return host, ok
}

// EnableTLSRPT attaches TLS-RPT report ingestion to the service (mirror of
// EnableDMARC). db is the shared database handle the parsed reports persist
// to. Safe to call twice; the last handle wins.
func (s *Service) EnableTLSRPT(db *gorm.DB) {
	if db == nil {
		return
	}
	s.tlsrpt = &tlsRptProcessor{db: db, blob: s.blobStore}
}

// SetTLSRPTAddress wires the dedicated rua allowlist for the receiver
// (setter-injection convention; nil or the zero value disables the address).
func (s *Service) SetTLSRPTAddress(a *TlsRptAllowlist) { s.tlsrptAddr = a }

// ResolveTLSRPTRecipient validates the envelope's recipients against the
// TLS-RPT allowlist (ADR-002 pattern: system address → parent tenant domain,
// no routing rules). The first recipient on a verified domain's tlsrpt@
// address wins.
func (s *Service) ResolveTLSRPTRecipient(recipients []string) (*models.Domain, string, bool) {
	if s.tlsrptAddr == nil || s.domainRepo == nil {
		// An unwired allowlist or repository degrades to "not a report
		// address" — never a panic on the ingest path.
		return nil, "", false
	}
	for _, r := range recipients {
		addr := strings.ToLower(strings.TrimSpace(r))
		domain, ok := s.tlsrptAddr.ReportDomain(addr)
		if !ok {
			continue
		}
		d, err := s.domainRepo.FindVerifiedByName(domain)
		if err != nil {
			continue
		}
		return d, addr, true
	}
	return nil, "", false
}

// processTLSRPT offers an ingested message to the TLS-RPT processor. It never
// fails the ingestion: every error is logged and swallowed, and a service
// without a processor (or a record with no attachments) returns immediately.
// Hooked at the same two points as the DMARC processor (ingest + parse).
func (s *Service) processTLSRPT(ctx context.Context, rec *models.InboundEmail) {
	if s.tlsrpt == nil || rec == nil || rec.AttachmentsJSON == "" {
		return
	}
	if err := s.tlsrpt.process(ctx, rec); err != nil {
		logger.Warn("tlsrpt: report ingestion skipped", "inbound_id", rec.UUID, "error", err)
	}
}

// ingestSystemTLSRPT durably ingests an inbound TLS report as a system
// message (ADR-002 non-negotiable: durable path first). Like
// ingestSystemBounce it never runs routing rules and never consults the
// suppression list (the sender is a remote MTA reporting TLS results); the
// report processing itself is best-effort and never fails the ingest.
//
// The report document is an attachment, so extraction happens on the parse
// pass: the async parse worker is enqueued (its ApplyParsed hook offers the
// record to the processor). Without a wired producer — dev mode — the parse
// and the report processing run synchronously so the report is never lost.
func (s *Service) ingestSystemTLSRPT(ctx context.Context, raw []byte, envelopeFrom string, envelopeTo []string, domain *models.Domain, rptAddress string, source models.InboundSource, clientIP string) (*models.InboundEmail, error) {
	start := time.Now()
	defer func() {
		if s.onIngestMs != nil {
			s.onIngestMs(time.Since(start).Seconds())
		}
	}()

	rec := &models.InboundEmail{
		UserID:      domain.UserID,
		WorkspaceID: domain.WorkspaceID,
		DomainID:    domain.ID,
		Sender:      strings.ToLower(strings.TrimSpace(envelopeFrom)),
		Recipients:  envelopeTo,
		Size:        int64(len(raw)),
		Status:      models.InboundStatusReceived,
		Source:      source,
		ReceivedAt:  time.Now().UTC(),
		RawContent:  raw,
	}
	if err := s.repo.Create(rec); err != nil {
		return nil, fmt.Errorf("persist inbound tlsrpt report: %w", err)
	}

	if s.blobStore != nil {
		rawKey := fmt.Sprintf("inbound/%s/raw.eml", rec.UUID)
		if err := s.blobStore.Put(ctx, rawKey, bytes.NewReader(raw), "message/rfc822"); err == nil {
			rec.RawStorageKey = rawKey
			rec.RawContent = nil
			if uerr := s.repo.Update(rec); uerr != nil {
				logger.Warn("failed to clear inline raw after blob upload", "uuid", rec.UUID, "error", uerr)
				rec.RawContent = raw
			}
		} else {
			logger.Warn("failed to store raw inbound eml", "uuid", rec.UUID, "error", err)
		}
	}

	// Report processing: asynchronously through the parse worker when one is
	// wired; synchronously otherwise (dev/producer-less deployments) so the
	// report is processed exactly once either way.
	if s.producer != nil {
		if err := s.producer.EnqueueInboundParse(rec.ID); err != nil {
			logger.Error("failed to enqueue inbound:parse for tlsrpt report", "inbound_id", rec.ID, "error", err)
			s.parseAndProcessTLSRPT(ctx, rec, raw)
		}
	} else {
		s.parseAndProcessTLSRPT(ctx, rec, raw)
	}

	if s.onReceived != nil {
		s.onReceived(source)
	}
	if s.onBytes != nil {
		s.onBytes(rec.Size)
	}
	_ = clientIP // SPF is not evaluated for system reports.
	return rec, nil
}

// parseAndProcessTLSRPT parses the raw message in-process and feeds the
// extracted attachments to the report processor (the no-producer fallback of
// ingestSystemTLSRPT). Best effort — parse failures are logged, the durable
// record stands regardless.
func (s *Service) parseAndProcessTLSRPT(ctx context.Context, rec *models.InboundEmail, raw []byte) {
	p, err := ParseRawEmail(raw)
	if err != nil {
		logger.Warn("tlsrpt: synchronous parse of report message failed", "inbound_id", rec.UUID, "error", err)
		return
	}
	if err := s.ApplyParsed(ctx, rec, p); err != nil {
		logger.Warn("tlsrpt: synchronous apply of report message failed", "inbound_id", rec.UUID, "error", err)
	}
}

type tlsRptProcessor struct {
	db   *gorm.DB
	blob blobStore
}

// tlsRptMaxDetailsBytes caps the re-encoded failure-details projection stored
// on the row. Reports describing pathological sessions can carry long detail
// arrays; the raw document stays on the durable inbound record, and the row
// keeps the first details that fit.
const tlsRptMaxDetailsBytes = 32 << 10

// tlsRptMaxDocumentBytes bounds one report document held in memory. It reuses
// the DMARC cap's reasoning (dmarc.go maxDecompressedReportBytes): legitimate
// TLS reports are far smaller, and the bound caps gzip-bomb expansion. The
// read helper is shared with the DMARC processor (same package).
const tlsRptMaxDocumentBytes = 16 << 20

// process inspects every attachment, parses the JSON ones as RFC 8460
// reports and stores the valid ones. Returns an error only for systemic
// failures; per-attachment parse misses are expected (most mail is not a TLS
// report) and silently skipped.
func (p *tlsRptProcessor) process(ctx context.Context, rec *models.InboundEmail) error {
	var atts []models.InboundAttachmentMeta
	if err := json.Unmarshal([]byte(rec.AttachmentsJSON), &atts); err != nil {
		return fmt.Errorf("decode attachments: %w", err)
	}

	stored := 0
	for _, att := range atts {
		raw, err := p.attachmentContent(ctx, att)
		if err != nil || len(raw) == 0 {
			continue
		}
		report, err := tlsrpt.ParseTLSRPTReport(raw)
		if err != nil {
			continue // not a TLS report
		}
		n, err := p.store(rec, report)
		if err != nil {
			return fmt.Errorf("store report %q: %w", report.ReportID, err)
		}
		stored += n
		logger.Info("tlsrpt: report ingested", "inbound_id", rec.UUID, "report_id", report.ReportID,
			"reporter", report.OrganizationName, "policies", len(report.Policies), "rows", n)
	}
	if stored == 0 && len(atts) > 0 {
		logger.Debug("tlsrpt: no TLS report found on inbound message", "inbound_id", rec.UUID, "attachments", len(atts))
	}
	return nil
}

// attachmentContent loads one attachment's bytes: inline base64 content when
// present, else the blob store; gzip is unwrapped with the same bounded
// reader the DMARC processor uses (R-17 gzip-bomb caps).
func (p *tlsRptProcessor) attachmentContent(ctx context.Context, att models.InboundAttachmentMeta) ([]byte, error) {
	if att.Content != "" {
		raw, err := base64.StdEncoding.DecodeString(att.Content)
		if err != nil {
			return nil, err
		}
		return tlsRptMaybeGunzip(raw)
	}
	if att.StorageKey == "" || p.blob == nil {
		return nil, nil
	}
	rc, err := p.blob.Get(ctx, att.StorageKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := readBounded(rc)
	if err != nil {
		return nil, err
	}
	return tlsRptMaybeGunzip(raw)
}

// tlsRptMaybeGunzip unwraps gzip report bodies (RFC 8460 §7.3: reports SHOULD
// be gzipped), bounding the decompressed stream at tlsRptMaxDocumentBytes.
func tlsRptMaybeGunzip(raw []byte) ([]byte, error) {
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, tlsRptMaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > tlsRptMaxDocumentBytes {
		return nil, fmt.Errorf("tlsrpt: report content exceeds the %d byte cap", tlsRptMaxDocumentBytes)
	}
	return out, nil
}

// ParseTLSRPTDocument parses a report document through the shared parser and
// enforces the ingest-side minimum: a report must carry at least the
// identity fields the storage row keys on.
func ParseTLSRPTDocument(raw []byte) (*tlsrpt.TLSReport, error) {
	return tlsrpt.ParseTLSRPTReport(raw)
}

// store persists one parsed report: one row per policies[] entry, keyed by
// (user, report_id, policy_domain) so re-ingesting the same report is
// idempotent. Returns the number of NEW rows.
func (p *tlsRptProcessor) store(rec *models.InboundEmail, report *tlsrpt.TLSReport) (int, error) {
	rows := make([]models.TLSRPTReport, 0, len(report.Policies))
	for i := range report.Policies {
		pol := &report.Policies[i]
		mxJSON, _ := json.Marshal(pol.MXHosts)
		domainID := rec.DomainID
		rows = append(rows, models.TLSRPTReport{
			UserID:             rec.UserID,
			WorkspaceID:        rec.WorkspaceID,
			DomainID:           &domainID,
			InboundID:          rec.ID,
			ReportID:           report.ReportID,
			PolicyDomain:       pol.PolicyDomain,
			OrganizationName:   report.OrganizationName,
			ContactInfo:        report.ContactInfo,
			PolicyType:         pol.PolicyType,
			MXHosts:            string(mxJSON),
			SuccessfulSessions: pol.SuccessfulSessions,
			FailedSessions:     pol.FailedSessions,
			DetailsJSON:        truncateDetails(pol.FailureDetails),
			WindowStart:        report.Start,
			WindowEnd:          report.End,
		})
	}
	if len(rows) == 0 {
		// An empty report is still a report: store the identity row so the
		// read surface shows the (quiet) report arrived, and dedup still
		// holds. One row with zeroed counts.
		domainID := rec.DomainID
		rows = append(rows, models.TLSRPTReport{
			UserID:       rec.UserID,
			WorkspaceID:  rec.WorkspaceID,
			DomainID:     &domainID,
			InboundID:    rec.ID,
			ReportID:     report.ReportID,
			PolicyDomain: "",
		})
	}
	res := p.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// truncateDetails re-encodes the failure-details array, bounded at
// tlsRptMaxDetailsBytes. An overflow keeps the prefix and records a truncation
// marker (the raw document remains available on the inbound record).
func truncateDetails(details []tlsrpt.TLSFailureDetail) string {
	if len(details) == 0 {
		return ""
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return ""
	}
	if len(encoded) <= tlsRptMaxDetailsBytes {
		return string(encoded)
	}
	// Keep the details that fit whole — never a partial JSON object.
	kept := make([]tlsrpt.TLSFailureDetail, 0, len(details))
	for _, d := range details {
		candidate, err := json.Marshal(append(kept, d))
		if err != nil || len(candidate) > tlsRptMaxDetailsBytes {
			break
		}
		kept = append(kept, d)
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return ""
	}
	return string(out)
}
