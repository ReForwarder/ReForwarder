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

package handlers

import (
	"testing"

	"github.com/goposta/posta/internal/models"
)

// The delivery read paths sanitize stored error text and redact captured
// response bodies on the way out, so rows written before the SSRF guard —
// whose raw errors can carry internal IPs and ports — are cleaned for the API
// without destroying the stored history. These tests cover the pure helpers
// behind that; no database is involved.

func TestSanitizeDeliveries(t *testing.T) {
	deliveries := []models.WebhookDelivery{
		// Legacy row: a raw transport error leaking a loopback address and port.
		{ErrorMessage: `Post "http://127.0.0.1:8080/x": dial tcp 127.0.0.1:8080: connect: connection refused`},
		// The dispatcher's "HTTP <code>" notation is safe; kept verbatim.
		{ErrorMessage: "HTTP 500"},
		// No error at all must stay empty.
		{ErrorMessage: ""},
	}
	sanitizeDeliveries(deliveries)

	if got := deliveries[0].ErrorMessage; got != "connection failed" {
		t.Errorf("legacy dial error = %q, want %q", got, "connection failed")
	}
	if got := deliveries[1].ErrorMessage; got != "HTTP 500" {
		t.Errorf("HTTP status error = %q, want verbatim %q", got, "HTTP 500")
	}
	if got := deliveries[2].ErrorMessage; got != "" {
		t.Errorf("empty error = %q, want empty", got)
	}
}

func TestSanitizeAttempts(t *testing.T) {
	t.Run("private webhook URL redacts the body and sanitizes the error", func(t *testing.T) {
		attempts := []models.WebhookDeliveryAttempt{
			{DeliveryID: 7, Error: `dial tcp 10.0.0.5:9000: connect: connection refused`, ResponseBody: "internal secret"},
		}
		sanitizeAttempts(attempts, func(uint) string { return "http://127.0.0.1:8080/hook" })

		if got := attempts[0].Error; got != "connection failed" {
			t.Errorf("error = %q, want %q", got, "connection failed")
		}
		if got := attempts[0].ResponseBody; got != "" {
			t.Errorf("response body = %q, want redacted", got)
		}
	})

	t.Run("public webhook URL keeps the body and HTTP status errors", func(t *testing.T) {
		attempts := []models.WebhookDeliveryAttempt{
			{DeliveryID: 7, StatusCode: 500, Error: "HTTP 500", ResponseBody: `{"error":"not found"}`},
		}
		sanitizeAttempts(attempts, func(uint) string { return "https://example.com/hook" })

		if got := attempts[0].Error; got != "HTTP 500" {
			t.Errorf("error = %q, want verbatim %q", got, "HTTP 500")
		}
		if got := attempts[0].ResponseBody; got != `{"error":"not found"}` {
			t.Errorf("response body = %q, want kept", got)
		}
	})

	t.Run("unknown webhook fails safe and redacts the body", func(t *testing.T) {
		attempts := []models.WebhookDeliveryAttempt{
			{DeliveryID: 9, ResponseBody: "internal secret"},
		}
		sanitizeAttempts(attempts, func(uint) string { return "" })

		if got := attempts[0].ResponseBody; got != "" {
			t.Errorf("response body = %q, want redacted for unknown webhook", got)
		}
	})
}
