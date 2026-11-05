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

package provider_events

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/goposta/posta/internal/services/email"
)

// Classification is the R-05 verdict on why a message failed (or succeeded).
// The enum is deliberately provider-neutral: both the SMTP-response path
// (worker, SendError) and the provider-webhook path (NormalizedEvent) reduce
// to it, so analytics and the suppression policy can treat them alike.
type Classification string

const (
	ClassHardBounce        Classification = "hard_bounce"
	ClassSoftBounce        Classification = "soft_bounce"
	ClassMailboxFull       Classification = "mailbox_full"
	ClassInvalidRecipient  Classification = "invalid_recipient"
	ClassDNSFailure        Classification = "dns_failure"
	ClassConnectionFailure Classification = "connection_failure"
	ClassTLSFailure        Classification = "tls_failure"
	ClassReputation        Classification = "reputation"
	ClassRateLimited       Classification = "rate_limited"
	ClassDeferred          Classification = "deferred"
	ClassBlocked           Classification = "blocked"
	ClassAuthFailure       Classification = "auth_failure"
	ClassComplaint         Classification = "complaint"
	ClassDelivered         Classification = "delivered"
	ClassUnknown           Classification = "unknown"
)

// Permanent reports whether the classification is a permanent recipient
// failure: retrying is pointless and auto-suppression should apply. Everything
// else — soft bounces, deferrals, reputation blocks, auth problems — is worth
// retrying (or is not a failure at all). This is the single switch the worker
// and the ingestion service use to decide suppress-vs-retry (R-05).
func (c Classification) Permanent() bool {
	switch c {
	case ClassHardBounce, ClassInvalidRecipient:
		return true
	}
	return false
}

// bounce markers adapters may prefix to a Diagnostic. They carry the
// provider's own permanence judgement ("Permanent"/"Transient" for SES,
// "HardBounce"/"SoftBounce" for Postmark, severity for Mailgun) into the
// classifier in a form no free-text reason can accidentally imitate.
const (
	markerPermanent = "bounce:permanent"
	markerHard      = "bounce:hard"
	markerTransient = "bounce:transient"
	markerSoft      = "bounce:soft"
)

// Classify maps one provider event (or SMTP failure) to a Classification.
// provider is informational (reserved for provider-specific overrides);
// eventType is a NormalizedEvent event type; smtpCode/enhancedCode carry a
// structured SMTP reply when the provider reported one (0/"" when not) and
// diagnostic is the free-text reason. Codes and enhanced codes embedded in the
// diagnostic text are extracted when no structured code is given.
func Classify(provider, eventType string, smtpCode int, enhancedCode, diagnostic string) Classification {
	switch eventType {
	case EventDelivered:
		return ClassDelivered
	case EventComplained:
		return ClassComplaint
	case EventBounced:
		if cls := classifySMTP(smtpCode, enhancedCode, diagnostic); cls != ClassUnknown {
			return cls
		}
		// A bounce with no usable signal defaults to soft: wrongly
		// suppressing a deliverable address is worse than retrying a dead one.
		return ClassSoftBounce
	case EventRejected:
		// A rejection with no signal is permanent policy (5xx-shaped) by
		// definition; classifyFailure refines it when codes or text help.
		if cls := classifySMTP(smtpCode, enhancedCode, diagnostic); cls != ClassUnknown {
			return cls
		}
		return ClassHardBounce
	case EventDeferred:
		// The provider says transient: a result that looks permanent is
		// demoted to plain deferred (e.g. SES deferrals with 5xx-looking
		// diagnostic text), while specific transient classes still win.
		cls := classifySMTP(smtpCode, enhancedCode, diagnostic)
		if cls == ClassUnknown || cls.Permanent() {
			return ClassDeferred
		}
		return cls
	case EventOpened, EventClicked:
		// Engagement events carry no delivery verdict.
		return ClassUnknown
	default:
		return ClassUnknown
	}
}

// ClassifySendError classifies an SMTP failure the worker caught. The enhanced
// status code, when present, arrives inside SendError.Msg ("550 5.1.1 user
// unknown"), so it is passed as the diagnostic and extracted there. When Msg
// is empty (connection-level failure — no SMTP reply at all) the wrapped error
// text carries the signal instead.
func ClassifySendError(se *email.SendError) Classification {
	if se == nil {
		return ClassUnknown
	}
	diagnostic := se.Msg
	if diagnostic == "" && se.Err != nil {
		diagnostic = se.Err.Error()
	}
	return Classify("smtp", EventBounced, se.Code, "", diagnostic)
}

var (
	// smtpEnhancedInText matches "550 5.1.1 user unknown" / "5.1.1 user unknown".
	reEnhancedInText = regexp.MustCompile(`(?:[45]\d{2})?\s*(\d\.\d{1,3}\.\d{1,3})\b`)
	// smtpCodeInText matches a leading bare reply code ("550 user unknown").
	reCodeInText = regexp.MustCompile(`^([45]\d{2})\b`)
)

// classifySMTP classifies by structured SMTP reply information, in order of
// authority: adapter permanence markers > enhanced status code > diagnostic
// text patterns > reply-code class. Codes embedded in the diagnostic text are
// used when no structured code was supplied.
func classifySMTP(code int, enhancedCode, diagnostic string) Classification {
	// The adapter's permanence judgement outranks everything: it is the
	// provider's own verdict on the very bounce being classified.
	if cls, ok := classifyMarker(diagnostic); ok {
		return cls
	}

	if code == 0 || enhancedCode == "" {
		// Pull "550 5.1.1 …" or "5.1.1 …" out of the text — providers and
		// net/smtp both embed the enhanced code in the reply text. A bare
		// leading "550 …" counts as the reply code when none was supplied.
		if m := reEnhancedInText.FindStringSubmatch(diagnostic); m != nil {
			enhancedCode = m[1]
			if code == 0 {
				if m := reCodeInText.FindStringSubmatch(strings.TrimSpace(diagnostic)); m != nil {
					c, err := strconv.Atoi(m[1])
					if err == nil {
						code = c
					}
				}
			}
		}
	}

	if cls, ok := classifyEnhanced(enhancedCode); ok {
		return cls
	}
	if cls, ok := classifyDiagnosticText(diagnostic); ok {
		return cls
	}
	switch {
	case code >= 500 && code < 600:
		return ClassHardBounce
	case code >= 400 && code < 500:
		return ClassDeferred
	default:
		return ClassUnknown
	}
}

// classifyMarker recognizes the adapter-set bounce permanence markers.
func classifyMarker(diagnostic string) (Classification, bool) {
	d := strings.ToLower(diagnostic)
	switch {
	case d == "":
		return "", false
	case strings.Contains(d, markerPermanent), strings.Contains(d, markerHard):
		return ClassHardBounce, true
	case strings.Contains(d, markerTransient), strings.Contains(d, markerSoft):
		return ClassSoftBounce, true
	}
	return "", false
}

// classifyEnhanced maps an RFC 3463 enhanced status code "x.y.z" (the leading
// x is the severity class and only distinguishes 4.2.1 from 5.2.1, so it is
// kept where it matters). Returns ok=false when the code is empty or unknown —
// the caller falls through to text and code-class rules.
func classifyEnhanced(enhanced string) (Classification, bool) {
	enhanced = strings.TrimSpace(enhanced)
	if enhanced == "" {
		return "", false
	}
	parts := strings.Split(enhanced, ".")
	if len(parts) != 3 {
		return "", false
	}
	// Key on "y.z" with the leading severity digit where the pair is ambiguous.
	yz := parts[1] + "." + parts[2]
	switch yz {
	// Recipient / mailbox addressing (subject 1).
	case "1.1", "1.6", "1.10", "1.9", "1.13", "1.14", "1.15", "1.16", "1.17", "1.18":
		return ClassInvalidRecipient, true
	case "1.2":
		return ClassDNSFailure, true // bad destination system address
	case "1.3", "1.4", "1.5":
		return ClassInvalidRecipient, true // bad destination / address syntax

	// Mailbox status (subject 2). x.2.2 is full in both severities —
	// "452 4.2.2" and "552 5.2.2" are the same quota problem.
	case "2.2":
		return ClassMailboxFull, true
	case "5.2.1":
		return ClassInvalidRecipient, true // mailbox disabled / not accepting
	case "5.2.3", "5.2.4":
		return ClassMailboxFull, true // message length / mailing-list expansion exceeded
	case "5.2.5", "5.2.6":
		return ClassReputation, true

	// Network & routing (subject 4).
	case "4.1", "4.2":
		return ClassConnectionFailure, true // no answer from host / bad connection
	case "4.3", "4.4":
		return ClassDNSFailure, true // directory server failure / unable to route
	case "4.7":
		return ClassConnectionFailure, true // too much mail data (transient resource)
	case "4.15", "4.16", "4.17", "4.18":
		return ClassConnectionFailure, true

	// Security & policy (subject 7).
	case "7.9", "7.10", "7.11", "7.12", "7.13", "7.14", "7.15", "7.16", "7.17", "7.18", "7.19", "7.20", "7.21", "7.22", "7.23", "7.24", "7.25", "7.27", "7.28", "7.29", "7.30", "7.31":
		return ClassAuthFailure, true
	case "7.1", "7.26":
		return ClassReputation, true // delivery not authorized / spam policy
	}
	if parts[1] == "7" {
		// Unenumerated 5.7.x policy rejections: policy-verbotten content or
		// sender standing.
		return ClassReputation, true
	}
	if parts[1] == "3" && parts[2] == "1" {
		return ClassMailboxFull, true // mail system full
	}
	return "", false
}

// diagnosticPattern pairs free-text markers with the class they imply, most
// specific first. All matches are lowercase substrings; the table carries the
// common cases operators actually see (gmail rate limiting, Outlook
// greylisting, quota errors, blacklists, …).
var diagnosticPatterns = []struct {
	matches []string
	class   Classification
}{
	// Adapter markers: the provider's own permanence judgement.
	{[]string{markerPermanent, markerHard}, ClassHardBounce},
	{[]string{markerTransient, markerSoft}, ClassSoftBounce},

	// Recipient does not exist / cannot receive.
	{[]string{
		"user unknown", "unknown user", "unknown recipient", "recipient unknown",
		"no such user", "no such mailbox", "no such recipient", "no mailbox",
		"user not found", "recipient not found", "address not found",
		"does not exist", "doesn't exist", "not a valid mailbox", "valid mailbox",
		"invalid recipient", "invalid address", "invalid mailbox", "recipient rejected",
		"address rejected", "mailbox unavailable", "mailbox disabled",
		"account disabled", "has been disabled", "user is inactive",
		"recipient address rejected", "bad destination mailbox",
		"mailbox has moved", "user has moved",
	}, ClassInvalidRecipient},

	// Quota / storage.
	{[]string{
		"mailbox full", "mailbox is full", "over quota", "quota exceeded",
		"quota exhausted", "insufficient storage", "storage allocation",
		"mail system full", "mailbox has exceeded", "user is over quota",
		"system storage",
	}, ClassMailboxFull},

	// Rate limiting (gmail: "rate limit"; others: throttling, daily caps).
	{[]string{
		"rate limit", "rate limited", "throttl", "sending limit", "sending quota",
		"daily limit", "daily quota", "too many", "maximum send", "429 too many",
	}, ClassRateLimited},

	// Authentication.
	{[]string{
		"authentication", "auth failed", "auth failure", "credentials",
		"username and password", "login failed", "authentication required",
		"535 authentication", "auth unsuccessful",
	}, ClassAuthFailure},

	// TLS / certificate problems.
	{[]string{
		"tls", "ssl", "handshake", "certificate", "starttls", "cipher",
		"protocol version", "secure connection",
	}, ClassTLSFailure},

	// DNS.
	{[]string{
		"dns", "nxdomain", "host not found", "domain not found",
		"domain does not exist", "name server", "no mx", "mx record",
		"lookup failed", "resolver", "domain of sender", "mail domain",
	}, ClassDNSFailure},

	// Connection-level failures.
	{[]string{
		"connection refused", "connection reset", "connection timed out",
		"timed out", "timeout", "no route to host", "network unreachable",
		"host unreachable", "unreachable", "refused", "temporarily unable to connect",
		"deadline exceeded",
	}, ClassConnectionFailure},

	// Reputation / policy blocks (ClassBlocked last — the broadest marker).
	{[]string{
		"blacklist", "black list", "blocklist", "block list", "blocked using",
		"spamhaus", "spamcop", "barracuda", "reputation", "policy reasons",
		"access denied", "not authorized", "delivery not authorized",
		"relay access denied", "relay denied", "prohibited", "spam",
		"rejected by policy", "policy rejection", "banned", "unsatisfactory",
		string(ClassBlocked),
	}, ClassReputation},

	// Greylisting / transient deferral (outlook: "greylisted").
	{[]string{
		"greylist", "graylist", "grey list", "gray list", "try again later",
		"please retry", "try later", "temporarily deferred", "temporary failure",
		"temporary problem", "temporary error", string(ClassDeferred), "queued for",
		"delayed", "temporarily unavailable", "resource temporarily",
	}, ClassDeferred},
}

// classifyDiagnosticText scans the diagnostic for known failure phrases.
// First matching pattern wins.
func classifyDiagnosticText(diagnostic string) (Classification, bool) {
	d := strings.ToLower(diagnostic)
	if d == "" {
		return "", false
	}
	for _, p := range diagnosticPatterns {
		for _, m := range p.matches {
			if strings.Contains(d, m) {
				return p.class, true
			}
		}
	}
	return "", false
}
