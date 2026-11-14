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

package mta

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/goposta/posta/internal/services/conditions"
	"github.com/goposta/posta/internal/storage/repositories"
)

// Transport policy resolution (SF-50, ADR-001 amendment): the two MTA gate
// points — RCPT on the gated submission listener and the direct-to-MX
// delivery executor — resolve the sender domain's enabled transport policies
// through the unified condition evaluator's transport currency and apply the
// FIRST matching policy's overrides (priority ASC then id ASC; later
// policies do not stack). Evaluation is strictly fail-closed: any evaluator
// or store error returns an error and the gate answers the conservative
// outcome (a temporary 451 at RCPT; a deferred send at delivery) rather than
// letting mail past a gate that might have rejected it.

// TransportFacts is the envelope-fact evaluation subject. Which facts are
// populated depends on the gate point (RCPT knows helo + authenticated_as;
// delivery knows sender_domain + message_size); the resolver passes the
// whole map to the evaluator, and a rule naming a missing fact is an error
// (fail-closed) — a policy referencing a fact its gate point never supplies
// would otherwise silently never match.
type TransportFacts struct {
	MailFrom        string
	RcptTo          string
	Helo            string
	AuthenticatedAs string
	SenderDomain    string
	MessageSize     int64
}

// toMap renders the facts for the evaluator. Address-like values are
// lowercased (the transport comparisons are EqualFold anyway; normalization
// keeps the fact map canonical); message_size is a decimal string.
func (f TransportFacts) toMap() map[string]string {
	m := map[string]string{
		conditions.TransportFieldMailFrom:        strings.ToLower(strings.TrimSpace(f.MailFrom)),
		conditions.TransportFieldRcptTo:          strings.ToLower(strings.TrimSpace(f.RcptTo)),
		conditions.TransportFieldHelo:            strings.TrimSpace(f.Helo),
		conditions.TransportFieldAuthenticatedAs: strings.TrimSpace(f.AuthenticatedAs),
		conditions.TransportFieldSenderDomain:    strings.ToLower(strings.TrimSpace(f.SenderDomain)),
		conditions.TransportFieldMessageSize:     strconv.FormatInt(f.MessageSize, 10),
	}
	return m
}

// TransportOverride is the matching policy's route override. Zero values
// mean "untouched" — the base per-domain policy applies.
type TransportOverride struct {
	// Reject refuses the recipient at RCPT with Reason (550 5.7.1).
	Reject bool
	Reason string
	// BindIP pins the outbound source address.
	BindIP string
	// MsgRatePerMin overrides the per-message send-rate pacing.
	MsgRatePerMin int
	// MaxMessageSize overrides the outbound size cap (octets).
	MaxMessageSize int64
	// HourlyBudget overrides the per-hour delivery budget.
	HourlyBudget int
	// PolicyName names the matching policy (logs + attempt rows).
	PolicyName string
}

// TransportPolicyResolver is the seam both gate points consume. The
// repository-backed resolver satisfies it; nil seams (the default until
// wired) disable the transport-policy gates — the pre-SF-50 behavior,
// byte-for-byte.
type TransportPolicyResolver interface {
	// ResolveForRcpt resolves the policies for one RCPT gate decision.
	// matched=false means no policy matched (or none are enabled). An
	// evaluation or store error is returned for the caller's fail-closed
	// handling — it never degrades to "no match".
	ResolveForRcpt(ctx context.Context, workspaceID uint, senderDomain string, facts TransportFacts) (*TransportOverride, bool, error)
	// ResolveForDelivery is the delivery gate's resolution (same contract).
	ResolveForDelivery(ctx context.Context, workspaceID uint, senderDomain string, facts TransportFacts) (*TransportOverride, bool, error)
}

// TransportPolicyResolverService resolves policies over the repository.
type TransportPolicyResolverService struct {
	policies *repositories.TransportPolicyRepository
}

// NewTransportPolicyResolver builds the repository-backed resolver.
func NewTransportPolicyResolver(policies *repositories.TransportPolicyRepository) *TransportPolicyResolverService {
	return &TransportPolicyResolverService{policies: policies}
}

// ResolveForRcpt implements TransportPolicyResolver. RCPT carries the
// pre-delivery facts (helo + authenticated_as known); sender_domain is the
// submitting domain and message_size is unknown at RCPT (declared SIZE only,
// already separately capped) — a policy referencing message_size errors at
// this gate (fail-closed), because a size condition cannot be honestly
// evaluated before the body arrives.
func (r *TransportPolicyResolverService) ResolveForRcpt(ctx context.Context, workspaceID uint, senderDomain string, facts TransportFacts) (*TransportOverride, bool, error) {
	return r.resolve(workspaceID, senderDomain, facts)
}

// ResolveForDelivery implements TransportPolicyResolver.
func (r *TransportPolicyResolverService) ResolveForDelivery(ctx context.Context, workspaceID uint, senderDomain string, facts TransportFacts) (*TransportOverride, bool, error) {
	return r.resolve(workspaceID, senderDomain, facts)
}

// resolve loads the domain's enabled policies in evaluation order and
// returns the first matching policy's override. Errors surface; a nil
// override with matched=false is the no-match outcome.
func (r *TransportPolicyResolverService) resolve(workspaceID uint, senderDomain string, facts TransportFacts) (*TransportOverride, bool, error) {
	if r == nil || r.policies == nil {
		return nil, false, nil
	}
	policies, err := r.policies.ListEnabledForDomain(workspaceID, strings.ToLower(strings.TrimSpace(senderDomain)))
	if err != nil {
		return nil, false, fmt.Errorf("load transport policies for %q: %w", senderDomain, err)
	}
	if len(policies) == 0 {
		return nil, false, nil
	}
	factMap := facts.toMap()
	for i := range policies {
		p := &policies[i]
		match, err := conditions.EvaluateTransportRules(p.Conditions, factMap)
		if err != nil {
			return nil, false, fmt.Errorf("transport policy %q (id %d): %w", p.Name, p.ID, err)
		}
		if !match {
			continue
		}
		return &TransportOverride{
			Reject:         p.Reject,
			Reason:         p.RejectReason,
			BindIP:         p.BindIP,
			MsgRatePerMin:  p.MsgRatePerMin,
			MaxMessageSize: p.MaxMessageSize,
			HourlyBudget:   p.HourlyBudget,
			PolicyName:     p.Name,
		}, true, nil
	}
	return nil, false, nil
}

// merge applies a transport override onto the resolved per-domain policy for
// one delivery. The override can only TIGHTEN the baseline or pin the source
// IP: a zero-valued override field leaves the base policy's value untouched.
func mergeTransportOverride(p *Policy, o *TransportOverride) {
	if p == nil || o == nil {
		return
	}
	if o.MsgRatePerMin > 0 {
		p.MsgRatePerMin = o.MsgRatePerMin
	}
	if o.MaxMessageSize > 0 {
		p.MaxMessageSize = o.MaxMessageSize
	}
	if o.HourlyBudget > 0 {
		p.HourlyBudget = o.HourlyBudget
	}
}
