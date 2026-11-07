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

package worker

import (
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/inbound"
)

// OB-2 W4 (ADR-002 decision 4): the direct-MTA delivery seam stamps the VERP
// bounce address as MAIL FROM when the return-path feature is on, and keeps
// today's envelope otherwise.

func verpTestEmail() *models.Email {
	return &models.Email{
		UUID:   "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		Sender: "Acme <noreply@mail.example.test>",
	}
}

// With the feature on, the direct-delivery envelope is the signed bounce
// address of the message UUID on the return-path subdomain.
func TestEnvelopeSenderUsesVerpAddressWhenEnabled(t *testing.T) {
	h := &EmailSendHandler{}
	h.SetVerp(inbound.NewVerp(inbound.VerpConfig{Subdomain: "bounce", Secret: "worker-test-secret"}))

	em := verpTestEmail()
	got := h.envelopeSender(em)
	if !strings.HasPrefix(got, "bounce+") {
		t.Fatalf("envelope sender %q is not a VERP bounce address", got)
	}
	if !strings.HasSuffix(got, "@bounce.mail.example.test") {
		t.Fatalf("envelope sender %q is not on the return-path subdomain", got)
	}
	if _, ok := h.verp.Decode(got); !ok {
		t.Fatalf("envelope sender %q does not decode under the same secret", got)
	}
}

// With the feature off the envelope is exactly today's plain sender address.
func TestEnvelopeSenderFallsBackWithoutVerp(t *testing.T) {
	h := &EmailSendHandler{} // no SetVerp
	if got := h.envelopeSender(verpTestEmail()); got != "noreply@mail.example.test" {
		t.Fatalf("envelope sender = %q, want the plain bare sender", got)
	}

	// A disabled codec (missing config) is equally inert.
	h2 := &EmailSendHandler{}
	h2.SetVerp(inbound.NewVerp(inbound.VerpConfig{}))
	if got := h2.envelopeSender(verpTestEmail()); got != "noreply@mail.example.test" {
		t.Fatalf("disabled-codec envelope sender = %q, want the plain bare sender", got)
	}
}
