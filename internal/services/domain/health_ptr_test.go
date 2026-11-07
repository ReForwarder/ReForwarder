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

package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// The PTR guidance slice (OB-1, ADR-001 §7): reverse-DNS guidance for the
// outbound bind IP surfaces only on direct-mode domains, as guidance text —
// never enforced at delivery time.
func TestPTRGuidanceRelayModeOmitted(t *testing.T) {
	// healthTestDomain is ownership-verified, so the report consults the
	// stats reader — always inject the fake, never the nil-DB gorm default.
	svc := newHealthTestService(&fakeStats{})
	d := healthTestDomain() // relay mode (zero DeliveryMode)

	report, err := svc.Report(d, 0)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.PTR != nil {
		t.Fatalf("PTR section = %+v, want nil for a relay-mode domain", report.PTR)
	}
}

func TestPTRGuidanceDirectModeWithoutBindIP(t *testing.T) {
	svc := newHealthTestService(&fakeStats{})
	svc.lookupPTR = func(ip string) ([]string, error) {
		t.Fatal("no PTR lookup may run without a bound IP")
		return nil, nil
	}
	d := healthTestDomain()
	d.DeliveryMode = models.DeliveryModeDirect

	report, err := svc.Report(d, 0)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.PTR == nil {
		t.Fatal("direct-mode domain must carry a PTR guidance section")
	}
	if report.PTR.Match != PTRMatchUnbound {
		t.Fatalf("Match = %q, want unbound", report.PTR.Match)
	}
	if !strings.Contains(report.PTR.Guidance, "outbound bind IP") {
		t.Fatalf("guidance must instruct setting the bind IP: %q", report.PTR.Guidance)
	}
}

func TestPTRGuidanceMatchAndMismatch(t *testing.T) {
	d := healthTestDomain()
	d.DeliveryMode = models.DeliveryModeDirect
	d.OutboundBindIP = "198.51.100.7"

	t.Run("match against the sending domain", func(t *testing.T) {
		svc := newHealthTestService(&fakeStats{})
		svc.lookupPTR = func(ip string) ([]string, error) {
			if ip != "198.51.100.7" {
				t.Fatalf("lookup of %q, want the bound IP", ip)
			}
			return []string{"example.com."}, nil
		}
		report, err := svc.Report(d, 0)
		if err != nil {
			t.Fatalf("Report: %v", err)
		}
		if report.PTR.Match != PTRMatchMatch || report.PTR.PTRName != "example.com" {
			t.Fatalf("PTR = %+v, want a match on example.com (trailing dot stripped)", report.PTR)
		}
	})

	t.Run("match against the configured EHLO name", func(t *testing.T) {
		svc := newHealthTestService(&fakeStats{})
		svc.SetEhloDomain("mx.posta.test")
		svc.lookupPTR = func(ip string) ([]string, error) {
			return []string{"mx.posta.test"}, nil
		}
		report, err := svc.Report(d, 0)
		if err != nil {
			t.Fatalf("Report: %v", err)
		}
		if report.PTR.Match != PTRMatchMatch {
			t.Fatalf("Match = %q, want match", report.PTR.Match)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		svc := newHealthTestService(&fakeStats{})
		svc.SetEhloDomain("mx.posta.test")
		svc.lookupPTR = func(ip string) ([]string, error) {
			return []string{"something-else.example.net"}, nil
		}
		report, err := svc.Report(d, 0)
		if err != nil {
			t.Fatalf("Report: %v", err)
		}
		if report.PTR.Match != PTRMatchMismatch {
			t.Fatalf("Match = %q, want mismatch", report.PTR.Match)
		}
		if !strings.Contains(report.PTR.Guidance, "POSTA_MTA_EHLO_NAME") {
			t.Fatalf("mismatch guidance must mention the EHLO setting: %q", report.PTR.Guidance)
		}
	})
}

func TestPTRGuidanceMissingRecord(t *testing.T) {
	svc := newHealthTestService(&fakeStats{})
	d := healthTestDomain()
	d.DeliveryMode = models.DeliveryModeDirect
	d.OutboundBindIP = "198.51.100.7"
	svc.lookupPTR = func(ip string) ([]string, error) {
		return nil, errors.New("no PTR record")
	}

	report, err := svc.Report(d, 0)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.PTR.Match != PTRMatchMissing {
		t.Fatalf("Match = %q, want missing", report.PTR.Match)
	}
	if !strings.Contains(report.PTR.Guidance, "PTR") {
		t.Fatalf("guidance must explain the PTR record: %q", report.PTR.Guidance)
	}
}

// Direct-mode mail still reports health even when the resolver errors — the
// lookup failure becomes missing-PTR guidance, not a report failure.
func TestPTRGuidanceLookupErrorDoesNotFailReport(t *testing.T) {
	svc := newHealthTestService(&fakeStats{})
	svc.lookupPTR = func(ip string) ([]string, error) {
		return nil, errors.New("resolver down")
	}
	d := healthTestDomain()
	d.DeliveryMode = models.DeliveryModeDirect
	d.OutboundBindIP = "198.51.100.7"

	report, err := svc.Report(d, DefaultHealthWindow)
	if err != nil {
		t.Fatalf("Report must not fail on PTR lookup errors: %v", err)
	}
	if report.DomainID != d.ID || report.CheckedAt.IsZero() {
		t.Fatalf("report = %+v, want the normal payload", report)
	}
}
