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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/okapi"
)

// Section-11 gate: /info must surface the env-gated feature flags so the
// dashboard can hide the nav entries whose pages would only error (Messages/
// Forms/Spam Filters behind POSTA_MESSAGES_ENABLED, Inbound Emails behind
// POSTA_INBOUND_ENABLED, plus the relay and metrics endpoints).
func TestInfoFeatureFlags(t *testing.T) {
	o := okapi.NewTestServer(t)
	o.Get("/info", Info)

	request := func() AppInfo {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/info", nil)
		rec := httptest.NewRecorder()
		o.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /info = %d: %s", rec.Code, rec.Body.String())
		}
		var res struct {
			Success bool    `json:"success"`
			Data    AppInfo `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !res.Success {
			t.Fatalf("expected success envelope, got %s", rec.Body.String())
		}
		return res.Data
	}

	// Defaults: every feature flag is off, and the flags ride the documented
	// `features` object.
	t.Run("defaults are all disabled", func(t *testing.T) {
		info := request()
		if info.Features.MessagesEnabled || info.Features.InboundEnabled ||
			info.Features.RelayEnabled || info.Features.MetricsEnabled {
			t.Fatalf("expected all flags disabled by default, got %+v", info.Features)
		}
		if info.Name != "Posta" || info.Version == "" {
			t.Fatalf("unexpected base info: %+v", info)
		}
	})

	t.Run("env flags are surfaced", func(t *testing.T) {
		t.Setenv("POSTA_MESSAGES_ENABLED", "true")
		t.Setenv("POSTA_INBOUND_ENABLED", "true")
		t.Setenv("POSTA_SMTP_RELAY_ENABLED", "true")
		t.Setenv("POSTA_METRICS_ENABLED", "true")
		info := request()
		if !info.Features.MessagesEnabled || !info.Features.InboundEnabled ||
			!info.Features.RelayEnabled || !info.Features.MetricsEnabled {
			t.Fatalf("expected all flags enabled, got %+v", info.Features)
		}
	})

	t.Run("flags are independent", func(t *testing.T) {
		t.Setenv("POSTA_MESSAGES_ENABLED", "true")
		t.Setenv("POSTA_INBOUND_ENABLED", "false")
		t.Setenv("POSTA_SMTP_RELAY_ENABLED", "false")
		t.Setenv("POSTA_METRICS_ENABLED", "false")
		info := request()
		if !info.Features.MessagesEnabled {
			t.Fatal("messages_enabled should follow POSTA_MESSAGES_ENABLED=true")
		}
		if info.Features.InboundEnabled || info.Features.RelayEnabled || info.Features.MetricsEnabled {
			t.Fatalf("other flags must stay off, got %+v", info.Features)
		}
	})
}
