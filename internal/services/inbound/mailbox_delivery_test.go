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
	"errors"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/mailstore"
)

// OB-3 wave W5 (flow F13) unit checks that need no database: the default
// (unwired) seam must keep the exact pre-OB-3 behavior, and a nil/empty
// deliver request must be a no-op rather than a panic.

// TestDeliverActionWithoutSeamKeepsArchiveOnly — a Service without
// SetMailboxDeliverer executes a deliver route as pure archive behavior: the
// record's status is untouched, nothing panics.
func TestDeliverActionWithoutSeamKeepsArchiveOnly(t *testing.T) {
	svc := &Service{}
	rec := &models.InboundEmail{
		ID: 7, UUID: "u-7", UserID: 1, Status: models.InboundStatusReceived,
		Recipients: []string{"ada@verified.test"},
	}
	svc.applyRouteActions(context.Background(), rec, &models.InboundRoute{
		ID: 3, Action: models.InboundRouteActionDeliver, Pattern: "@verified.test",
	}, RouteMatchCatchAll, []byte("raw message bytes"))
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (no seam → archive only)", rec.Status)
	}
}

// TestMailboxDeliveryNilGuards — a nil seam receiver, a nil record and empty
// raw bytes are all clean no-ops.
func TestMailboxDeliveryNilGuards(t *testing.T) {
	var nilSeam *MailboxDelivery
	if res, err := nilSeam.DeliverToMailbox(context.Background(), nil, nil); err != nil || res != nil {
		t.Fatalf("nil seam receiver = (%v, %v), want (<nil>, <nil>)", res, err)
	}
	seam := NewMailboxDelivery(nil, nil, MailboxDeliveryConfig{AutoProvision: true})
	if res, err := seam.DeliverToMailbox(context.Background(), &models.InboundEmail{}, nil); err != nil || res != nil {
		t.Fatalf("empty raw = (%v, %v), want (<nil>, <nil>)", res, err)
	}
}

// TestDeliverActionUnknownErrorFallsBackToArchive — a seam failure that is
// NOT over-quota (e.g. the mailstore service is unavailable) must not
// quarantine: the record stays received and the durable archive stands.
func TestDeliverActionUnknownErrorFallsBackToArchive(t *testing.T) {
	svc := &Service{}
	svc.SetMailboxDeliverer(failingDeliverer{err: errors.New("mailstore unavailable")})
	rec := &models.InboundEmail{
		ID: 8, UUID: "u-8", UserID: 1, Status: models.InboundStatusReceived,
		Recipients: []string{"ada@verified.test"},
	}
	svc.applyRouteActions(context.Background(), rec, &models.InboundRoute{
		ID: 3, Action: models.InboundRouteActionDeliver, Pattern: "@verified.test",
	}, RouteMatchCatchAll, []byte("raw"))
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (non-quota failures never hold)", rec.Status)
	}
}

// TestDeliverActionOverQuotaQuarantinesWithoutStore — the over-quota branch
// flips the record to quarantined even when no quarantine store is wired (the
// status IS the record; the store only adds the review row).
func TestDeliverActionOverQuotaQuarantinesWithoutStore(t *testing.T) {
	svc := &Service{}
	svc.SetMailboxDeliverer(failingDeliverer{err: &mailstore.QuotaError{
		MailboxID: 42, Scope: mailstore.QuotaScopeMailboxBytes,
		Used: 100, Incoming: 200, Limit: 100,
	}})
	rec := &models.InboundEmail{
		ID: 9, UUID: "u-9", UserID: 1, Status: models.InboundStatusReceived,
		Recipients: []string{"ada@verified.test"},
	}
	svc.applyRouteActions(context.Background(), rec, &models.InboundRoute{
		ID: 3, Action: models.InboundRouteActionDeliver, Pattern: "@verified.test",
	}, RouteMatchCatchAll, []byte("raw"))
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %q, want quarantined (typed over-quota outcome)", rec.Status)
	}
}

// failingDeliverer is a MailboxDeliverer that always fails with err.
type failingDeliverer struct{ err error }

func (f failingDeliverer) DeliverToMailbox(context.Context, *models.InboundEmail, []byte) (*MailboxDeliveryResult, error) {
	return nil, f.err
}

// noOpDeliverer is a MailboxDeliverer that reports nothing delivered — the
// seam's own no-op paths (nil receiver, empty raw, unverified domain).
type noOpDeliverer struct{}

func (noOpDeliverer) DeliverToMailbox(context.Context, *models.InboundEmail, []byte) (*MailboxDeliveryResult, error) {
	return nil, nil
}

// TestDeliverActionWithNoOpSeamResult — a deliver action whose seam reports
// nothing delivered (nil result) must leave the record received without
// panicking (regression: the delivered-mailboxes event path dereferenced a
// nil result).
func TestDeliverActionWithNoOpSeamResult(t *testing.T) {
	svc := &Service{}
	svc.SetMailboxDeliverer(noOpDeliverer{})
	rec := &models.InboundEmail{
		ID: 10, UUID: "u-10", UserID: 1, Status: models.InboundStatusReceived,
		Recipients: []string{"ada@verified.test"},
	}
	svc.applyRouteActions(context.Background(), rec, &models.InboundRoute{
		ID: 3, Action: models.InboundRouteActionDeliver, Pattern: "@verified.test",
	}, RouteMatchCatchAll, []byte("raw"))
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received (no-op seam result is archive-only)", rec.Status)
	}
}
