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

package security

import (
	"fmt"
	"strings"
)

// Reason is the machine-readable reason code stored on a SecurityBan row
// (models.SecurityBan.ReasonCode).
type Reason string

const (
	ReasonPortScan       Reason = "PORT_SCAN_DETECTED"
	ReasonRapidConn      Reason = "RAPID_CONNECTION_BURST"
	ReasonConnAbuse      Reason = "CONNECTION_ABUSE"
	ReasonIncompleteHS   Reason = "INCOMPLETE_HANDSHAKE_ABUSE"
	ReasonTLSProbing     Reason = "TLS_PROBING"
	ReasonSMTPAbuse      Reason = "SMTP_PROTOCOL_ABUSE"
	ReasonHTTPRecon      Reason = "HTTP_RECONNAISSANCE"
	ReasonMalformedProto Reason = "MALFORMED_PROTOCOL_ABUSE"
	ReasonAuthBruteForce Reason = "AUTH_BRUTE_FORCE"
	ReasonManual         Reason = "MANUAL_ADMIN_BLOCK"
)

// Signal is one sliding-window abuse counter tracked by the Detector in
// Redis. Signal values double as settings key fragments
// (security_sig_<name>_threshold, security_weight_<name>) and as
// security_events event types when a signal crosses its threshold.
type Signal string

const (
	SigDistinctPorts  Signal = "distinct_ports" // members = port numbers
	SigNewConnections Signal = "new_connections"
	SigIncompleteHS   Signal = "incomplete_handshakes"
	SigConnectAborts  Signal = "connect_aborts"
	SigMalformed      Signal = "malformed_preauth"
	SigSuspiciousPath Signal = "suspicious_paths"
	SigAuthFailures   Signal = "auth_failures"
)

// Severity values for security_events rows.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Action values for security_events rows (models.SecurityEvent.ActionTaken).
const (
	EventActionRecorded = "recorded"
	EventActionBanned   = "banned"
	EventActionRejected = "rejected"
)

// Event types for ban lifecycle security_events rows. Detection events use
// the signal name itself as the event type.
const (
	EventBanCreated = "ban.created"
	EventBanLifted  = "ban.lifted"
	EventRejected   = "connection.rejected"
)

// signalMeta carries per-signal presentation metadata used when writing
// security_events rows.
type signalMeta struct {
	Label    string
	Severity string
}

var signalMetadata = map[Signal]signalMeta{
	SigDistinctPorts:  {Label: "Distinct ports", Severity: SeverityWarning},
	SigNewConnections: {Label: "New connections", Severity: SeverityWarning},
	SigIncompleteHS:   {Label: "Incomplete handshakes", Severity: SeverityWarning},
	SigConnectAborts:  {Label: "Connect aborts", Severity: SeverityWarning},
	SigMalformed:      {Label: "Malformed pre-auth", Severity: SeverityWarning},
	SigSuspiciousPath: {Label: "Suspicious paths", Severity: SeverityWarning},
	SigAuthFailures:   {Label: "Auth failures", Severity: SeverityWarning},
}

// AllSignals returns every tracked signal in a fixed evaluation order.
func AllSignals() []Signal {
	return []Signal{
		SigDistinctPorts,
		SigNewConnections,
		SigIncompleteHS,
		SigConnectAborts,
		SigMalformed,
		SigSuspiciousPath,
		SigAuthFailures,
	}
}

// Label returns the human-readable signal name for the admin UI.
func (s Signal) Label() string {
	if m, ok := signalMetadata[s]; ok {
		return m.Label
	}
	return string(s)
}

// EventSeverity returns the severity written to security_events rows when
// this signal crosses its threshold.
func (s Signal) EventSeverity() string {
	if m, ok := signalMetadata[s]; ok && m.Severity != "" {
		return m.Severity
	}
	return SeverityWarning
}

// ReasonForSignal maps a signal (and protocol, for TLS probing) to the ban
// reason used on instant bans. TLS probing is tracked as incomplete
// handshakes with protocol "tls".
func ReasonForSignal(sig Signal, proto string) Reason {
	switch sig {
	case SigDistinctPorts:
		return ReasonPortScan
	case SigNewConnections:
		return ReasonRapidConn
	case SigConnectAborts:
		return ReasonConnAbuse
	case SigIncompleteHS:
		if strings.EqualFold(proto, "tls") {
			return ReasonTLSProbing
		}
		return ReasonIncompleteHS
	case SigMalformed:
		return ReasonMalformedProto
	case SigSuspiciousPath:
		return ReasonHTTPRecon
	case SigAuthFailures:
		return ReasonAuthBruteForce
	default:
		return ReasonSMTPAbuse
	}
}

// BanDescription builds the human-readable sentence stored on the ban row,
// e.g. "This IP connected to 7 different exposed service ports within
// 42 seconds".
func (s Signal) BanDescription(count, windowSeconds int, proto string) string {
	switch s {
	case SigDistinctPorts:
		return fmt.Sprintf("This IP connected to %d different exposed service ports within %d seconds", count, windowSeconds)
	case SigNewConnections:
		return fmt.Sprintf("This IP opened %d new connections within %d seconds", count, windowSeconds)
	case SigIncompleteHS:
		if strings.EqualFold(proto, "tls") {
			return fmt.Sprintf("This IP failed %d TLS handshakes within %d seconds", count, windowSeconds)
		}
		return fmt.Sprintf("This IP failed to complete %d protocol handshakes within %d seconds", count, windowSeconds)
	case SigConnectAborts:
		return fmt.Sprintf("This IP dropped %d connections before sending any command within %d seconds", count, windowSeconds)
	case SigMalformed:
		return fmt.Sprintf("This IP sent %d malformed pre-auth protocol messages within %d seconds", count, windowSeconds)
	case SigSuspiciousPath:
		return fmt.Sprintf("This IP probed %d suspicious URL paths within %d seconds", count, windowSeconds)
	case SigAuthFailures:
		return fmt.Sprintf("This IP made %d failed authentication attempts within %d seconds", count, windowSeconds)
	default:
		return fmt.Sprintf("This IP triggered the %s detection signal %d times within %d seconds", s, count, windowSeconds)
	}
}
