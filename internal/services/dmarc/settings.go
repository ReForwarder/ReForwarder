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

package dmarc

import "strings"

// DomainSettings is the per-domain report-settings surface (SF-63): the
// domain owner's preferences for the aggregate reports sent ABOUT their
// domain. The settings live on the domain row (report_rua,
// report_interval_hours, report_max_bytes) and are consumed by the SF-25
// send-side cron only when the domain row is ownership-verified — the
// DNS-control proof that makes the tenant the authority over reports about
// its own domain.
type DomainSettings struct {
	// Rua is the tenant-specified rua endpoint list (comma-separated plain
	// addresses). Non-empty REPLACES the DNS-discovered endpoints for this
	// domain — the owner's explicit choice for where reports about their
	// domain land.
	Rua string
	// IntervalHours is the minimum hours between aggregate reports about the
	// domain. 0 = the deployment's daily cadence.
	IntervalHours int
	// MaxBytes is the size policy: a report about the domain larger than this
	// many bytes is dropped (best-effort reporting, the rua size-directive
	// analog). 0 = unbounded.
	MaxBytes int
}

// RuaList splits the stored comma-separated rua list into addresses.
func (s *DomainSettings) RuaList() []string {
	if s == nil || strings.TrimSpace(s.Rua) == "" {
		return nil
	}
	var out []string
	for _, a := range strings.Split(s.Rua, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// NormalizeRuaList validates and normalizes a tenant-provided rua list: each
// entry must be a plain address or a mailto: URI (the mailto scheme is
// stripped, '!'-extensions dropped); the result is a comma-separated list of
// plain addresses, deduplicated case-insensitively.
func NormalizeRuaList(raw string) (string, error) {
	var out []string
	seen := map[string]bool{}
	for _, a := range strings.Split(raw, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(a), "mailto:") {
			a = a[len("mailto:"):]
		}
		if bang := strings.IndexByte(a, '!'); bang >= 0 {
			a = a[:bang]
		}
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// A bare-address validation (an @ with a domain part); full
		// mail.ParseAddress runs at the handler boundary.
		if !strings.Contains(a, "@") || strings.HasPrefix(a, "@") || strings.HasSuffix(a, "@") {
			return "", errBadRua(a)
		}
		if seen[strings.ToLower(a)] {
			continue
		}
		seen[strings.ToLower(a)] = true
		out = append(out, a)
	}
	return strings.Join(out, ","), nil
}

func errBadRua(a string) error {
	return &RuaError{Entry: a}
}

// RuaError reports an invalid rua entry.
type RuaError struct{ Entry string }

func (e *RuaError) Error() string {
	return "invalid rua address: " + e.Entry
}
