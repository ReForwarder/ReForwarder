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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/domain"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
)

// The domain health handler runs through the real okapi stack against the
// dmarc_handler_test.go fake database (domains + domain_signing_keys + events
// tables). The stored report's sending numbers are injected at the
// SendingStatsReader seam so no emails/bounces rows are needed; the Diagnose
// path exercises the real DNS verification against an RFC 2606 ".invalid"
// domain, where every lookup fails fast — which is exactly the "checks failed"
// outcome the endpoint must still report and persist.

// dmrFakeStats is the injected SendingStatsReader behind the stored report's
// sending view.
type dmrFakeStats struct{ sent, bounced, complained int64 }

func (s dmrFakeStats) SendingStats(repositories.ResourceScope, string, time.Time) (domain.SendingStats, error) {
	return domain.SendingStats{Sent: s.sent, Bounced: s.bounced, Complained: s.complained}, nil
}

func newDomainHealthServer(t *testing.T) (*okapi.TestServer, *dmrState) {
	t.Helper()
	state := newDmrState()
	gdb := dmrGorm(t, state)
	health := domain.NewHealthService(gdb, repositories.NewDomainSigningKeyRepository(gdb))
	health.SetStats(dmrFakeStats{sent: 200, bounced: 10, complained: 4})
	h := NewDomainHealthHandler(health,
		repositories.NewDomainRepository(gdb),
		audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))))
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		dmrWorkspaceBinding,
	)
	g.Get("/domains/{id:int}/health", okapi.H(h.Get))
	g.Post("/domains/{id:int}/health/diagnose", okapi.H(h.Diagnose))
	return o, state
}

// seedVerifiedDomain seeds a domain with the stored check booleans a report
// must mirror.
func seedVerifiedDomain(t *testing.T, s *dmrState, id uint, workspaceID *uint, userID uint, name string) {
	t.Helper()
	vals := map[string]any{
		"id": id, "user_id": userID, "domain": name,
		"ownership_verified": true, "spf_verified": true, "dkim_verified": true,
		"dmarc_verified":     false,
		"verification_token": "posta-verification-token",
		"created_at":         time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
	}
	if workspaceID != nil {
		vals["workspace_id"] = workspaceID
	}
	dmrSeed(t, s, "domains", vals)
}

func decodeHealthResponse(t *testing.T, rec *httptest.ResponseRecorder) domain.HealthReport {
	t.Helper()
	var res struct {
		Success bool                `json:"success"`
		Data    domain.HealthReport `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	return res.Data
}

// Get serves the health report from stored state: the recorded check
// booleans, the DNS records to set, and the sending rates from the injected
// stats reader. No DNS traffic.
func TestDomainHealthGetStoredReport(t *testing.T) {
	o, state := newDomainHealthServer(t)
	seedVerifiedDomain(t, state, 10, wsID(3), 7, "example.com")

	rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health get = %d: %s", rec.Code, rec.Body.String())
	}
	report := decodeHealthResponse(t, rec)
	if report.DomainID != 10 || report.Domain != "example.com" {
		t.Fatalf("report domain wrong: %+v", report)
	}
	if !report.OwnershipVerified || !report.SPF.Verified || !report.DKIM.Verified {
		t.Fatalf("stored checks must be served verbatim: %+v", report)
	}
	if report.DMARC.Verified {
		t.Fatal("dmarc check is not verified in storage")
	}
	if report.DMARC.Guidance == "" {
		t.Fatal("a failing check must carry operator guidance")
	}
	if report.Records == nil || report.Records.SPF.Host != "example.com" {
		t.Fatalf("report must include the DNS records to set: %+v", report.Records)
	}
	// The sending view comes from the injected stats reader: 10 bounces and 4
	// complaints over 200 sent messages.
	if report.Sending.Sent != 200 || report.Sending.Bounced != 10 {
		t.Fatalf("sending stats wrong: %+v", report.Sending)
	}
	if report.Sending.BounceRate != 5 || report.Sending.ComplaintRate != 2 {
		t.Fatalf("rates = %f/%f, want 5/2", report.Sending.BounceRate, report.Sending.ComplaintRate)
	}
	// No signing key stored: the signing view is guidance, not a key.
	if report.Signing.Enabled || report.Signing.Selector != "" {
		t.Fatalf("signing status wrong: %+v", report.Signing)
	}
	// A read is not a diagnosis: nothing is persisted, nothing audited.
	if rows := state.rowsOf("domains"); rows[0]["spf_verified"] != true {
		t.Fatalf("read must not touch stored state: %+v", rows[0])
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("health reads write no audit events, got %v", got)
	}
}

// A foreign domain is indistinguishable from a missing one, on both the read
// and the diagnose endpoint.
func TestDomainHealthForeignDomainNotFound(t *testing.T) {
	o, state := newDomainHealthServer(t)
	dmrSeedDomain(t, state, 10, wsID(99), 42, "example.com")

	if rec := doDMARCReq(t, o, http.MethodGet, "/workspaces/current/domains/10/health", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain health = %d, want 404", rec.Code)
	}
	if rec := doDMARCReq(t, o, http.MethodPost, "/workspaces/current/domains/10/health/diagnose", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign domain diagnose = %d, want 404", rec.Code)
	}
}

// Diagnose is edit-gated: a viewer is refused with 403 before anything runs,
// nothing is persisted and nothing is audited.
func TestDomainHealthDiagnoseRequiresEdit(t *testing.T) {
	o, state := newDomainHealthServer(t)
	dmrSeedDomain(t, state, 10, wsID(3), 7, "example.com")

	rec := doDMARCReq(t, o, http.MethodPost, "/workspaces/current/domains/10/health/diagnose",
		string(models.WorkspaceRoleViewer))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer diagnose = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if got := state.eventTypes(); len(got) != 0 {
		t.Fatalf("denied diagnose must write no audit events, got %v", got)
	}
	rows := state.rowsOf("domains")
	if rows[0]["ownership_verified"] != false {
		t.Fatalf("denied diagnose must not persist check state: %+v", rows[0])
	}
}

// A permitted diagnose re-runs the live checks (here: a reserved ".invalid"
// domain where every lookup fails), persists the fresh booleans on the domain,
// and writes the domain.health.diagnosed audit event.
func TestDomainHealthDiagnosePersistsAndAudits(t *testing.T) {
	o, state := newDomainHealthServer(t)
	dmrSeedDomain(t, state, 11, wsID(3), 7, "diagnose.invalid")

	rec := doDMARCReq(t, o, http.MethodPost, "/workspaces/current/domains/11/health/diagnose", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnose = %d: %s", rec.Code, rec.Body.String())
	}
	report := decodeHealthResponse(t, rec)
	if report.Domain != "diagnose.invalid" {
		t.Fatalf("report domain wrong: %+v", report)
	}
	if report.OwnershipVerified || report.SPF.Verified || report.DKIM.Verified || report.DMARC.Verified {
		t.Fatalf("failed lookups must report every check failed: %+v", report)
	}
	if got := state.eventTypes(); len(got) != 1 || got[0] != "domain.health.diagnosed" {
		t.Fatalf("diagnose must write one audit event, got %v", got)
	}
	rows := state.rowsOf("domains")
	if rows[0]["ownership_verified"] != false {
		t.Fatalf("fresh check state must be persisted: %+v", rows[0])
	}
	if rows[0]["workspace_id"] != int64(3) {
		t.Fatalf("persistence must keep the workspace binding: %+v", rows[0])
	}
}


// ---------- OB-1 SetDeliveryMode (route wiring by the coordinator) ----------

func newDomainServerWithRoutes(t *testing.T) (*okapi.TestServer, *dmrState) {
	t.Helper()
	state := newDmrState()
	gdb := dmrGorm(t, state)
	h := NewDomainHandler(repositories.NewDomainRepository(gdb))
	h.SetAuditLogger(audit.NewLogger(eventbus.New(repositories.NewEventRepository(gdb))))
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		dmrWorkspaceBinding,
	)
	g.Patch("/domains/{id:int}/delivery-mode", okapi.H(h.SetDeliveryMode))
	return o, state
}

func patchDeliveryMode(t *testing.T, o *okapi.TestServer, id int, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/workspaces/current/domains/"+itoa(id)+"/delivery-mode", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// The route group mounts the real JWT middleware and the workspace-binding
	// middleware, so the request must carry a dashboard token and name the
	// workspace — same arrangement doDMARCReq uses for the other domain routes.
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", dmrTestWorkspace)
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func TestDeliveryModeSwitchToDirect(t *testing.T) {
	o, state := newDomainServerWithRoutes(t)
	dmrSeed(t, state, "domains", map[string]any{
		"id": 10, "user_id": int64(1), "workspace_id": int64(3), "domain": "verified.example",
		"ownership_verified": true,
	})
	rec := patchDeliveryMode(t, o, 10, `{"delivery_mode":"direct"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var res struct {
		Success bool `json:"success"`
		Data    struct {
			Changed bool `json:"changed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Data.Changed {
		t.Fatalf("changed = false, want true")
	}
	rows := state.rowsOf("domains")
	if len(rows) != 1 || rows[0]["delivery_mode"] != "direct" {
		t.Fatalf("delivery_mode not persisted: %+v", rows)
	}
	events := state.rowsOf("events")
	found := false
	for _, e := range events {
		if e["type"] == "domain.delivery_mode_changed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("delivery-mode change not audited: %+v", events)
	}
}

func TestDeliveryModeDirectRequiresVerifiedDomain(t *testing.T) {
	o, state := newDomainServerWithRoutes(t)
	dmrSeed(t, state, "domains", map[string]any{
		"id": 11, "user_id": int64(1), "workspace_id": int64(3), "domain": "unverified.example",
		"ownership_verified": false,
	})
	rec := patchDeliveryMode(t, o, 11, `{"delivery_mode":"direct"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	rows := state.rowsOf("domains")
	if rows[0]["delivery_mode"] != "relay" {
		t.Fatalf("delivery_mode mutated on refusal: %+v", rows[0])
	}
}

func TestDeliveryModeRejectsInvalidMode(t *testing.T) {
	o, state := newDomainServerWithRoutes(t)
	dmrSeed(t, state, "domains", map[string]any{
		"id": 12, "user_id": int64(1), "workspace_id": int64(3), "domain": "any.example",
		"ownership_verified": true,
	})
	rec := patchDeliveryMode(t, o, 12, `{"delivery_mode":"open"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	rows := state.rowsOf("domains")
	if rows[0]["delivery_mode"] != "relay" {
		t.Fatalf("delivery_mode mutated on invalid mode: %+v", rows[0])
	}
}

func TestDeliveryModeForeignDomainNotFound(t *testing.T) {
	o, _ := newDomainServerWithRoutes(t)
	rec := patchDeliveryMode(t, o, 999, `{"delivery_mode":"direct"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeliveryModeSameModeNoop(t *testing.T) {
	o, state := newDomainServerWithRoutes(t)
	dmrSeed(t, state, "domains", map[string]any{
		"id": 13, "user_id": int64(1), "workspace_id": int64(3), "domain": "relay.example",
		"ownership_verified": true,
	})
	rec := patchDeliveryMode(t, o, 13, `{"delivery_mode":"relay"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var res struct {
		Success bool `json:"success"`
		Data    struct {
			Changed bool `json:"changed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Data.Changed {
		t.Fatalf("changed = true for same-mode noop")
	}
	events := state.rowsOf("events")
	for _, e := range events {
		if e["type"] == "domain.delivery_mode_changed" {
			t.Fatalf("noop must not audit: %+v", events)
		}
	}
}
