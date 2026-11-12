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
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Message-level SPF/DKIM/DMARC verification (SF-22/SF-23/SF-24, ADR-002
// Phase-3 slice) plus the optional ARC and iprev annotations (SF-27 / SF-32
// partial, ADR-002 decision 6). The engines run ASYNCHRONOUSLY after durable
// ingest — the SMTP 250 and the webhook 202 never wait on DNS — and persist
// one verdict row per message into inbound_auth_results. The DMARC
// disposition is a RECOMMENDATION recorded for the ADR-002 routing engine;
// nothing here rejects, bounces or quarantines mail — ARC and iprev are
// annotate-only by decision.
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

// envInboundARC / envInboundIPRev gate the optional ARC (SF-27) and iprev
// (SF-32 partial) engines (ADR-002 decision 6). Both default to FALSE —
// explicit opt-in — so an upgrade changes nothing for existing deployments.
const (
	envInboundARC   = "POSTA_INBOUND_ARC_ENABLED"
	envInboundIPRev = "POSTA_INBOUND_IPREV_ENABLED"
)

// verificationTimeout bounds one message's DNS-heavy evaluation. SPF is
// budget-limited to 10 lookups, DKIM to its selector lookups, DMARC to two;
// the overall wall clock still needs a cap.
const verificationTimeout = 30 * time.Second

// EnableVerification attaches the SPF/DKIM/DMARC verification engines (and
// the optional ARC/iprev annotations) to the service. db is the shared
// database handle the verdicts persist to; authservID is the RFC 8601
// Authentication-Results identifier (the instance hostname — callers pass
// the inbound hostname). Safe to call twice; the last handle wins. Mirrors
// EnableDMARC.
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
		db:           db,
		authservID:   authservID,
		enabled:      verificationEnabled(),
		arcEnabled:   arcEnabled(),
		iprevEnabled: iprevEnabled(),
		iprevMode:    iprevMode(),
		svc:          s,
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

// envExplicitlyOn parses an opt-in flag: only the affirmative spellings
// enable the feature (default false).
func envExplicitlyOn(env string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(env))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// arcEnabled reads POSTA_INBOUND_ARC_ENABLED (default false).
func arcEnabled() bool { return envExplicitlyOn(envInboundARC) }

// iprevEnabled reads POSTA_INBOUND_IPREV_ENABLED (default false).
func iprevEnabled() bool { return envExplicitlyOn(envInboundIPRev) }

// iprev operator modes (SF-32): the verdict is always evaluated and persisted
// when the engine is on; the mode decides what happens to a definitive fail.
const (
	envInboundIPRevMode = "POSTA_INBOUND_IPREV_MODE"

	// IPRevModeAnnotate (default) — verdict stays advisory: the persisted
	// row and the Authentication-Results header are the whole effect.
	IPRevModeAnnotate = "annotate"
	// IPRevModeTag — a definitive fail is additionally stamped onto the
	// record's spam reasons (visible in the API/UI), status unchanged.
	IPRevModeTag = "tag"
	// IPRevModeReject — a definitive fail moves the durably stored record to
	// rejected through the ADR-006 filter disposition path (an explicit,
	// event-backed reject — never a silent drop). The message was already
	// accepted at DATA, so this is a post-accept disposition, not an SMTP
	// 550 (no backscatter).
	IPRevModeReject = "reject"
)

// iprevMode reads POSTA_INBOUND_IPREV_MODE (annotate | tag | reject); the
// default and any unknown value fall back to annotate — enforcement is
// strictly opt-in.
func iprevMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envInboundIPRevMode))) {
	case IPRevModeTag:
		return IPRevModeTag
	case IPRevModeReject:
		return IPRevModeReject
	default:
		return IPRevModeAnnotate
	}
}

// verifyProcessor owns one service's verification pipeline: DNS seams for
// tests, the gorm handle for persistence, the authserv-id for A-R, and the
// config gates for the optional ARC/iprev engines.
type verifyProcessor struct {
	db         *gorm.DB
	authservID string
	enabled    bool

	arcEnabled   bool
	iprevEnabled bool
	// iprevMode is the SF-32 operator mode; svc routes reject dispositions
	// through the service's filter path (nil svc → tag/annotate only).
	iprevMode string
	svc       *Service

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
		ARCEnabled:   v.arcEnabled,
		IPRevEnabled: v.iprevEnabled,
	})
	header := verify.AuthenticationResults(v.authservID, results)
	v.persist(rec, results, header)
	// SF-32: the operator mode decides what a definitive fail does — the
	// annotate default stops here (verdict already persisted).
	v.applyIPRevMode(rec, results.IPRev)
}

// applyIPRevMode applies the SF-32 operator mode to a definitive
// forward-confirmation failure. Non-fail verdicts (none/temperror/permerror)
// and the annotate mode never change the record — enforcement binds only to
// a proven fail, never to a DNS absence or lookup error.
func (v *verifyProcessor) applyIPRevMode(rec *models.InboundEmail, verdict *verify.IPRevVerdict) {
	if v.iprevMode == IPRevModeAnnotate || verdict == nil || verdict.Result != verify.IPRevFail {
		return
	}
	reason := fmt.Sprintf("iprev: %s", verdict.Reason)
	switch v.iprevMode {
	case IPRevModeTag:
		// Stamp the reason onto the record for the API/UI; status unchanged.
		rec.SpamReasons = joinReasons(append(splitReasons(rec.SpamReasons), reason))
		if err := v.db.Model(&models.InboundEmail{}).Where("id = ?", rec.ID).
			Update("spam_reasons", rec.SpamReasons).Error; err != nil {
			logger.Warn("inbound verification: iprev tag stamp failed",
				"inbound_id", rec.UUID, "error", err)
		}
	case IPRevModeReject:
		if v.svc == nil {
			return
		}
		// Through the ADR-006 disposition path: status flipped + taxonomy
		// event — explicit and visible, never a silent drop.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := v.svc.ApplyFilterDisposition(ctx, rec, FilterDisposition{
			Disposition: spamfilter.DispositionReject,
			Reasons:     []string{reason},
		}); err != nil {
			logger.Error("inbound verification: iprev reject disposition failed",
				"inbound_id", rec.UUID, "error", err)
		}
	}
}

// splitReasons splits the comma-joined spam-reason vocabulary back into its
// list (the inverse of joinReasons).
func splitReasons(joined string) []string {
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	parts := strings.Split(joined, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
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
// idempotent: first verdict wins, nothing is duplicated. ARC/iprev columns
// stay empty ("") when those engines are disabled — an empty value is "not
// evaluated", distinct from "none" (evaluated, nothing found).
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
		ARCResult:         arcResult(results.ARC),
		ARCDomain:         arcDomain(results.ARC),
		IPRevResult:       iprevResult(results.IPRev),
		IPRevPTR:          iprevPTR(results.IPRev),
		AuthResultsHeader: header,
	}
	if err := v.db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		logger.Warn("inbound verification: failed to persist auth results",
			"inbound_id", rec.UUID, "error", err)
	}
}

// arcResult / iprevResult map a nil (not evaluated) verdict to "" and an
// evaluated one through orNone — the empty column means "engine off",
// distinct from "none" (engine ran, nothing found).
func arcResult(v *verify.ARCVerdict) string {
	if v == nil {
		return ""
	}
	return orNone(v.Result)
}

func iprevResult(v *verify.IPRevVerdict) string {
	if v == nil {
		return ""
	}
	return orNone(v.Result)
}

// arcDomain extracts the most recent seal's d= from an evaluated ARC
// verdict.
func arcDomain(v *verify.ARCVerdict) string {
	if v == nil {
		return ""
	}
	return trunc(v.Domain, 255)
}

// iprevPTR extracts the confirming PTR host from an evaluated iprev verdict.
func iprevPTR(v *verify.IPRevVerdict) string {
	if v == nil {
		return ""
	}
	return trunc(v.PTR, 255)
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
