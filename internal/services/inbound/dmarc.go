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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package inbound

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DMARC aggregate report ingestion (R-17).
//
// The inbound receiver (SMTP + webhook) is where rua:-addressed RFC 7489
// aggregate reports arrive for the tenant's verified sending domains. This
// processor rides that pipeline without changing it: after an inbound record
// persists, the message's XML attachments are offered to the parser below,
// and every parse that yields a valid <feedback> document is stored as
// per-record rows. Detection is parse-driven — a message is a DMARC report
// iff one of its attachments parses as one — so no addresses or subjects need
// configuring.
//
// The processor is opt-in per process: EnableDMARC wires it; a Service
// without it ignores DMARC entirely (the pre-R-17 behavior). The parser lives
// here (not in provider_events) because the email package's tests import this
// package for the attachment MIME fixtures, and provider_events imports
// email — a parser on that side of the edge would close a test import cycle.

// EnableDMARC attaches DMARC aggregate-report ingestion to the service. db is
// the shared database handle the parsed reports persist to. Safe to call
// twice; the last handle wins.
func (s *Service) EnableDMARC(db *gorm.DB) {
	if db == nil {
		return
	}
	s.dmarc = &dmarcProcessor{db: db, blob: s.blobStore}
}

// processDMARC offers an ingested message to the DMARC processor. It never
// fails the ingestion: every error is logged and swallowed, and a service
// without a processor (or a record with no attachments) returns immediately.
func (s *Service) processDMARC(ctx context.Context, rec *models.InboundEmail) {
	if s.dmarc == nil || rec == nil || rec.AttachmentsJSON == "" {
		return
	}
	if err := s.dmarc.process(ctx, rec); err != nil {
		logger.Warn("dmarc: aggregate report ingestion skipped", "inbound_id", rec.UUID, "error", err)
	}
}

type dmarcProcessor struct {
	db   *gorm.DB
	blob blobStore
}

// blobStore mirrors storage/blob.Store (interface for tests).
type blobStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// ---------- RFC 7489 aggregate report parsing ----------

// DMARCAlignment is a policy_evaluated alignment verdict (pass/fail).
type DMARCAlignment string

const (
	DMARCPass DMARCAlignment = "pass"
	DMARCFail DMARCAlignment = "fail"
)

// DMARCPolicyPublished is the <policy_published> block: the policy the tenant
// publishes for the reported domain.
type DMARCPolicyPublished struct {
	Domain string
	ADKIM  string
	ASP    string
	Policy string
}

// DMARCRecord is one <record>: a source IP's tally over the report window.
type DMARCRecord struct {
	SourceIP    string
	Count       int
	DKIMAligned bool // dkim policy_evaluated alignment == pass
	SPFAligned  bool // spf policy_evaluated alignment == pass
	Disposition string
	HeaderFrom  string
	// DKIMDomains / SPFDomains carry the auth_results identifiers for
	// diagnostics (not aggregated, stored only on the parsed document).
	DKIMDomains []string
	SPFDomains  []string
}

// DMARCAggregateReport is the parsed document.
type DMARCAggregateReport struct {
	ReportID      string
	Reporter      string // <org_name>
	ReporterEmail string // <email>
	Domain        string // <policy_published><domain>
	Policy        DMARCPolicyPublished
	Start         time.Time
	End           time.Time
	Records       []DMARCRecord
}

// dmarcFeedback mirrors the RFC 7489 aggregate-report XML shape.
type dmarcFeedback struct {
	XMLName xml.Name `xml:"feedback"`
	Meta    struct {
		OrgName  string `xml:"org_name"`
		Email    string `xml:"email"`
		ReportID string `xml:"report_id"`
		DateRng  struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy struct {
		Domain string `xml:"domain"`
		ADKIM  string `xml:"adkim"`
		ASP    string `xml:"asp"`
		P      string `xml:"p"`
	} `xml:"policy_published"`
	Records []struct {
		Row struct {
			SourceIP string `xml:"source_ip"`
			Count    int    `xml:"count"`
			Eval     struct {
				Disposition string `xml:"disposition"`
				DKIM        string `xml:"dkim"`
				SPF         string `xml:"spf"`
			} `xml:"policy_evaluated"`
		} `xml:"row"`
		IDs struct {
			HeaderFrom string `xml:"header_from"`
		} `xml:"identifiers"`
		Auth struct {
			DKIMResults []struct {
				Domain   string `xml:"domain"`
				Selector string `xml:"selector"`
				Result   string `xml:"result"`
			} `xml:"dkim"`
			SPFResults []struct {
				Domain string `xml:"domain"`
				Scope  string `xml:"scope"`
				Result string `xml:"result"`
			} `xml:"spf"`
		} `xml:"auth_results"`
	} `xml:"record"`
}

// ParseDMARCAggregateReport parses an RFC 7489 aggregate report document.
// Every <record> becomes one DMARCRecord; a report with zero records is valid
// (providers send empty reports for quiet intervals). Structural problems —
// non-XML, a different root, a missing report id or published domain —
// return an error.
func ParseDMARCAggregateReport(raw []byte) (*DMARCAggregateReport, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("dmarc: empty report document")
	}
	var fb dmarcFeedback
	if err := xml.Unmarshal(raw, &fb); err != nil {
		return nil, fmt.Errorf("dmarc: not a DMARC aggregate report: %w", err)
	}
	if fb.XMLName.Local != "feedback" {
		return nil, fmt.Errorf("dmarc: unexpected root element %q", fb.XMLName.Local)
	}

	out := &DMARCAggregateReport{
		ReportID:      fb.Meta.ReportID,
		Reporter:      fb.Meta.OrgName,
		ReporterEmail: fb.Meta.Email,
		Domain:        fb.Policy.Domain,
		Policy: DMARCPolicyPublished{
			Domain: fb.Policy.Domain,
			ADKIM:  fb.Policy.ADKIM,
			ASP:    fb.Policy.ASP,
			Policy: fb.Policy.P,
		},
	}
	if fb.Meta.DateRng.Begin > 0 {
		out.Start = time.Unix(fb.Meta.DateRng.Begin, 0).UTC()
	}
	if fb.Meta.DateRng.End > 0 {
		out.End = time.Unix(fb.Meta.DateRng.End, 0).UTC()
	}
	if out.ReportID == "" {
		// Providers must send a report id; without it re-ingestion dedup is
		// meaningless, so refuse rather than double-count.
		return nil, fmt.Errorf("dmarc: aggregate report has no report_id")
	}
	if out.Domain == "" {
		return nil, fmt.Errorf("dmarc: aggregate report has no policy_published domain")
	}

	for _, r := range fb.Records {
		rec := DMARCRecord{
			SourceIP:    r.Row.SourceIP,
			Count:       r.Row.Count,
			DKIMAligned: strings.EqualFold(strings.TrimSpace(r.Row.Eval.DKIM), string(DMARCPass)),
			SPFAligned:  strings.EqualFold(strings.TrimSpace(r.Row.Eval.SPF), string(DMARCPass)),
			Disposition: r.Row.Eval.Disposition,
			HeaderFrom:  r.IDs.HeaderFrom,
		}
		for _, d := range r.Auth.DKIMResults {
			rec.DKIMDomains = append(rec.DKIMDomains, d.Domain)
		}
		for _, s := range r.Auth.SPFResults {
			rec.SPFDomains = append(rec.SPFDomains, s.Domain)
		}
		out.Records = append(out.Records, rec)
	}
	return out, nil
}

// ---------- ingestion ----------

// process inspects every attachment, parses the XML ones as RFC 7489
// aggregate reports and stores the valid ones. Returns an error only for
// systemic failures; per-attachment parse misses are expected (most mail is
// not a DMARC report) and silently skipped.
func (p *dmarcProcessor) process(ctx context.Context, rec *models.InboundEmail) error {
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
		report, err := ParseDMARCAggregateReport(raw)
		if err != nil {
			continue // not a DMARC aggregate report
		}
		n, err := p.store(rec, report)
		if err != nil {
			return fmt.Errorf("store report %q: %w", report.ReportID, err)
		}
		stored += n
		logger.Info("dmarc: aggregate report ingested", "inbound_id", rec.UUID, "domain", report.Domain,
			"reporter", report.Reporter, "report_id", report.ReportID, "records", len(report.Records), "rows", n)
	}
	if stored == 0 && len(atts) > 0 {
		logger.Debug("dmarc: no aggregate report found on inbound message", "inbound_id", rec.UUID, "attachments", len(atts))
	}
	return nil
}

// attachmentContent loads one attachment's bytes: inline base64 content when
// present, else the blob store. A blob-backed attachment without a store
// configured is skipped.
func (p *dmarcProcessor) attachmentContent(ctx context.Context, att models.InboundAttachmentMeta) ([]byte, error) {
	if att.Content != "" {
		raw, err := base64.StdEncoding.DecodeString(att.Content)
		if err != nil {
			return nil, err
		}
		return maybeGunzip(raw)
	}
	if att.StorageKey == "" || p.blob == nil {
		return nil, nil
	}
	rc, err := p.blob.Get(ctx, att.StorageKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return maybeGunzip(raw)
}

// maybeGunzip unwraps gzip content — providers commonly attach the XML
// gzipped (.gz).
func maybeGunzip(raw []byte) ([]byte, error) {
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// store persists one parsed report: one row per <record>, keyed by
// (user, report_id, source_ip) so re-ingesting the same report is idempotent.
// Returns the number of NEW rows.
func (p *dmarcProcessor) store(rec *models.InboundEmail, report *DMARCAggregateReport) (int, error) {
	rows := make([]models.DMARCReport, 0, len(report.Records))
	for _, r := range report.Records {
		domainID := rec.DomainID
		rows = append(rows, models.DMARCReport{
			UserID:        rec.UserID,
			WorkspaceID:   rec.WorkspaceID,
			DomainID:      &domainID,
			InboundID:     rec.ID,
			Domain:        report.Domain,
			Reporter:      report.Reporter,
			ReporterEmail: report.ReporterEmail,
			ReportID:      report.ReportID,
			SourceIP:      r.SourceIP,
			WindowStart:   report.Start,
			WindowEnd:     report.End,
			Count:         r.Count,
			DKIMAligned:   r.DKIMAligned,
			SPFAligned:    r.SPFAligned,
			Disposition:   stringOrNone(r.Disposition),
			HeaderFrom:    r.HeaderFrom,
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	res := p.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// stringOrNone normalizes an empty policy_evaluated disposition: providers
// omit it when the policy is p=none with no override.
func stringOrNone(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "none"
	}
	return s
}
