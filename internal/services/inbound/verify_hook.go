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
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/inbound/verify"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Message-level SPF/DKIM/DMARC verification (SF-22/SF-23/SF-24, ADR-002
// Phase-3 slice). The engines run ASYNCHRONOUSLY after durable ingest — the
// SMTP 250 and the webhook 202 never wait on DNS — and persist one verdict
// row per message into inbound_auth_results. The DMARC disposition is a
// RECOMMENDATION recorded for the ADR-002 routing engine; nothing here
// rejects, bounces or quarantines mail.
//
// Injection surface (SF-49 slice): the composed RFC 8601
// Authentication-Results value (authserv-id = the instance hostname) is
// stored on the verdict row — the authoritative record — and merged into the
// parsed headers view at parse time (ApplyParsed) when the verdict already
// exists. The stored raw .eml bytes are never rewritten after ingest: the
// message may already have been forwarded, and falsifying the archive (or
// racing the parse/forward workers for the headers map) would trade a clean
// audit trail for a cosmetic header.

// envInboundVerify gates the whole verification step (engines + persistence):
// POSTA_INBOUND_VERIFY, default true. When the inbound receiver is disabled
// no inbound service is constructed, so nothing runs either way.
const envInboundVerify = "POSTA_INBOUND_VERIFY"

// verificationTimeout bounds one message's DNS-heavy evaluation. SPF is
// budget-limited to 10 lookups, DKIM to its selector lookups, DMARC to two;
// the overall wall clock still needs a cap.
const verificationTimeout = 30 * time.Second

// EnableVerification attaches the SPF/DKIM/DMARC verification engines to the
// service. db is the shared database handle the verdicts persist to;
// authservID is the RFC 8601 Authentication-Results identifier (the instance
// hostname — callers pass the inbound hostname). Safe to call twice; the
// last handle wins. Mirrors EnableDMARC.
func (s *Service) EnableVerification(db *gorm.DB, authservID string) {
	if db == nil {
		return
	}
	if authservID = strings.TrimSpace(authservID); authservID == "" {
		if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
			authservID = host
		} else {
			authservID = "localhost"
		}
	}
	s.verifier = &verifyProcessor{
		db:         db,
		authservID: authservID,
		enabled:    verificationEnabled(),
	}
}

// verificationEnabled reads POSTA_INBOUND_VERIFY (default true).
func verificationEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envInboundVerify))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// verifyProcessor owns one service's verification pipeline: DNS seams for
// tests, the gorm handle for persistence, the authserv-id for A-R.
type verifyProcessor struct {
	db         *gorm.DB
	authservID string
	enabled    bool

	// Test seams (nil → the Go default resolver).
	lookupTXT  func(ctx context.Context, name string) ([]string, error)
	lookupIP   func(ctx context.Context, name string) ([]net.IP, error)
	lookupAddr func(ctx context.Context, addr string) ([]string, error)
	lookupMX   func(ctx context.Context, name string) ([]*net.MX, error)
}

// processVerification offers an ingested message to the verifier. It never
// blocks delivery and never fails the ingestion: every error is logged and
// swallowed, and a service without a verifier returns immediately.
func (s *Service) processVerification(rec *models.InboundEmail, raw []byte, envelopeFrom, clientIP string) {
	if s.verifier == nil || rec == nil || len(raw) == 0 {
		return
	}
	if !s.verifier.enabled {
		return
	}
	go s.verifier.run(rec, raw, envelopeFrom, clientIP)
}

// run evaluates one message and persists the verdict. Runs on its own
// goroutine with an independent context — the SMTP session's context dies
// with the DATA call, long before DNS finishes.
func (v *verifyProcessor) run(rec *models.InboundEmail, raw []byte, envelopeFrom, clientIP string) {
	if rec == nil || len(raw) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), verificationTimeout)
	defer cancel()

	sampleKey := rec.UUID
	if sampleKey == "" {
		sampleKey = fmt.Sprintf("inbound-%d", rec.ID)
	}
	results := verify.Evaluate(ctx, v.resolver(), verify.Input{
		ClientIP:     clientIP,
		EnvelopeFrom: envelopeFrom,
		Raw:          raw,
		SampleKey:    sampleKey,
	})
	header := verify.AuthenticationResults(v.authservID, results)
	v.persist(rec, results, header)
}

// resolver builds the DNS resolver for the engines, honoring test seams.
func (v *verifyProcessor) resolver() verify.Resolver {
	if v.lookupTXT == nil && v.lookupIP == nil && v.lookupAddr == nil && v.lookupMX == nil {
		return verify.NetResolver{}
	}
	return seamResolver{v}
}

type seamResolver struct{ v *verifyProcessor }

func (s seamResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return s.v.lookupTXT(ctx, name)
}

func (s seamResolver) LookupIP(ctx context.Context, name string) ([]net.IP, error) {
	return s.v.lookupIP(ctx, name)
}

func (s seamResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	return s.v.lookupAddr(ctx, addr)
}

func (s seamResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return s.v.lookupMX(ctx, name)
}

// persist stores the verdict row. The unique inbound_id index plus
// ON CONFLICT DO NOTHING makes re-verification (or a racing re-ingest)
// idempotent: first verdict wins, nothing is duplicated.
func (v *verifyProcessor) persist(rec *models.InboundEmail, results verify.Results, header string) {
	row := &models.InboundAuthResult{
		UserID:            rec.UserID,
		WorkspaceID:       rec.WorkspaceID,
		InboundID:         rec.ID,
		MessageID:         trunc(rec.MessageID, 255),
		ClientIP:          trunc(results.ClientIP, 64),
		EnvelopeFrom:      trunc(results.EnvelopeFrom, 320),
		SPFResult:         orNone(results.SPF.Result),
		SPFDomain:         trunc(results.SPF.Domain, 255),
		SPFReason:         trunc(results.SPF.Reason, 255),
		DKIMResult:        orNone(results.DKIM.Result),
		DKIMDomain:        trunc(results.DKIM.Domain, 255),
		DKIMSelector:      trunc(results.DKIM.Selector, 255),
		DMARCResult:       orNone(results.DMARC.Result),
		DMARCDisposition:  orNone(results.DMARC.Disposition),
		DMARCPolicyDomain: trunc(results.DMARC.PolicyDomain, 255),
		DMARCPolicy:       trunc(results.DMARC.Policy, 20),
		AuthResultsHeader: header,
	}
	if err := v.db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		logger.Warn("inbound verification: failed to persist auth results",
			"inbound_id", rec.UUID, "error", err)
	}
}

// storedHeader returns the persisted Authentication-Results header value for
// an inbound message, or "" when no verdict exists yet (verification is
// asynchronous, so the parse worker may get here first).
func (v *verifyProcessor) storedHeader(inboundID uint) string {
	var row models.InboundAuthResult
	if err := v.db.Select("auth_results_header").Where("inbound_id = ?", inboundID).First(&row).Error; err != nil {
		return ""
	}
	return row.AuthResultsHeader
}

// withHeader returns headers with name set to value. When the message
// already carries such a header (an upstream MTA's Authentication-Results),
// it is returned untouched — RFC 8601 headers are append-only and the map
// cannot represent two values; our verdict stays authoritative on the
// inbound_auth_results row.
func withHeader(headers map[string]string, name, value string) map[string]string {
	if _, ok := headers[name]; ok {
		return headers
	}
	out := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		out[k] = v
	}
	out[name] = value
	return out
}

// verifyResultNone is the RFC 8601 "no result" verdict value.
const verifyResultNone = "none"

// orNone normalizes an empty result to "none".
func orNone(s string) string {
	if s == "" {
		return verifyResultNone
	}
	return s
}

// trunc bounds a value to its column size (rune-safe).
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
