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

package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/goposta/posta/internal/models"
)

// SF-42: attachment blocklists. A blocked filename extension or SHA-256
// content digest quarantines the record on sight through the ordinary
// disposition path — the cheapest check in the filter step, before clamd and
// scoring. Both lists default empty (no-op).

func blocklistAttachment(filename, content string) string {
	meta := []models.InboundAttachmentMeta{{
		Filename: filename,
		ContentType: "application/octet-stream",
		Content:  base64.StdEncoding.EncodeToString([]byte(content)),
	}}
	b, _ := json.Marshal(meta)
	return string(b)
}

func TestAttachmentBlocklistExtensionQuarantines(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.AttachmentsJSON = blocklistAttachment("invoice.exe", "MZ payload")
	})

	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{
		Enabled:           true,
		BlockedExtensions: []string{".exe", ".scr"},
	})
	if f.Run(context.Background(), rec) {
		t.Fatal("a blocked extension must dispose the record")
	}
	if rec.Status != models.InboundStatusQuarantined {
		t.Fatalf("status = %q, want quarantined", rec.Status)
	}
	if !strings.Contains(rec.SpamReasons, "attachment_blocked:.exe") {
		t.Fatalf("reasons = %q, want the extension reason", rec.SpamReasons)
	}
	// The disposition is explicit and event-backed, never a silent drop.
	if events := h.drain(t); len(events) == 0 {
		t.Fatal("blocked attachment must publish events")
	}
}

func TestAttachmentBlocklistExtensionMatchesCaseAndBareForm(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.AttachmentsJSON = blocklistAttachment("REPORT.SCR", "payload")
	})
	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{
		Enabled:           true,
		BlockedExtensions: []string{"scr"}, // bare form normalizes to .scr
	})
	if f.Run(context.Background(), rec) {
		t.Fatal("an uppercase blocked extension must dispose the record")
	}
	if !strings.Contains(rec.SpamReasons, "attachment_blocked:.scr") {
		t.Fatalf("reasons = %q", rec.SpamReasons)
	}
	h.drain(t)
}

func TestAttachmentBlocklistHashQuarantines(t *testing.T) {
	h := newWfHarness(t)
	content := "known-bad-binary-bytes"
	sum := sha256.Sum256([]byte(content))
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.AttachmentsJSON = blocklistAttachment("invoice.pdf", content)
	})

	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{
		Enabled:       true,
		BlockedHashes: []string{hex.EncodeToString(sum[:])},
	})
	if f.Run(context.Background(), rec) {
		t.Fatal("a blocked content digest must dispose the record")
	}
	if !strings.Contains(rec.SpamReasons, "attachment_blocked:sha256") {
		t.Fatalf("reasons = %q, want the digest reason", rec.SpamReasons)
	}
	h.drain(t)
}

func TestAttachmentBlocklistCleanProceeds(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.AttachmentsJSON = blocklistAttachment("invoice.pdf", "%PDF-1.4 clean")
	})

	sum := sha256.Sum256([]byte("something-else-entirely"))
	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{
		Enabled:           true,
		BlockedExtensions: []string{".exe"},
		BlockedHashes:     []string{hex.EncodeToString(sum[:])},
	})
	if !f.Run(context.Background(), rec) {
		t.Fatal("an unblocked attachment must proceed through the pipeline")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received", rec.Status)
	}
	h.drain(t)
}

func TestAttachmentBlocklistDisabledByDefault(t *testing.T) {
	h := newWfHarness(t)
	rec := h.newRecord(t, func(r *models.InboundEmail) {
		r.AttachmentsJSON = blocklistAttachment("invoice.exe", "MZ payload")
	})

	// No blocklists configured: the .exe attachment is only scored, never
	// blocked.
	f := build(h, stubFilter{score: 0.5}, nil, nil, InboundFilterOptions{Enabled: true})
	if !f.Run(context.Background(), rec) {
		t.Fatal("with no blocklists the record must proceed")
	}
	if rec.Status != models.InboundStatusReceived {
		t.Fatalf("status = %q, want received", rec.Status)
	}
	h.drain(t)
}
