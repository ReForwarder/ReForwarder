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

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/domain"
	"github.com/goposta/posta/internal/services/dnsmanager"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// The DNS provider handler (OB-6, ADR-007 decision 1) runs through the real
// okapi stack over the dmarc_handler_test.go fake database (domains table).
// The DNS service behind it is wired with in-memory stores and a fake
// provider (dnsprovHarness below) so no dns_* SQL is needed; credential
// encryption runs for real through crypto.Init.

// ---- harness: in-memory stores + fake provider ----

type dnsProvMemCreds struct {
	mu   sync.Mutex
	rows map[uint]*models.DNSProviderCredential
}

func (m *dnsProvMemCreds) UpsertCredential(cred *models.DNSProviderCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *cred
	m.rows[cred.DomainID] = &cp
	return nil
}

func (m *dnsProvMemCreds) FindCredential(domainID uint) (*models.DNSProviderCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[domainID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *dnsProvMemCreds) DeleteCredential(domainID uint) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rows[domainID]
	delete(m.rows, domainID)
	return ok, nil
}

func (m *dnsProvMemCreds) FindCredentialDomains() ([]models.Domain, error) { return nil, nil }

type dnsProvMemStates struct {
	mu   sync.Mutex
	rows map[uint]*models.DNSPublicationState
}

func (m *dnsProvMemStates) FindState(domainID uint) (*models.DNSPublicationState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[domainID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *dnsProvMemStates) UpsertState(st *models.DNSPublicationState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *st
	m.rows[st.DomainID] = &cp
	return nil
}

// dnsProvFakeProvider answers provider calls; FailReads makes every read fail
// with the given typed cause (the mis-scoped-token rejection).
type dnsProvFakeProvider struct {
	mu        sync.Mutex
	published []DNSPlannedRecordRecord
}

// DNSPlannedRecordRecord aliases the spec type for the harness.
type DNSPlannedRecordRecord = dnsmanager.DNSRecordSpec

var (
	dnsProvFailReadsMu sync.Mutex
	dnsProvFailReads   error
	dnsProvPublished   []DNSPlannedRecordRecord
)

// SetDNSProvFailReads points the fake provider's reads at a typed failure.
func SetDNSProvFailReads(err error) {
	dnsProvFailReadsMu.Lock()
	dnsProvFailReads = err
	dnsProvFailReadsMu.Unlock()
}

// DNSProvPublished returns everything the fake provider accepted.
func DNSProvPublished() []DNSPlannedRecordRecord {
	dnsProvFailReadsMu.Lock()
	defer dnsProvFailReadsMu.Unlock()
	return append([]DNSPlannedRecordRecord{}, dnsProvPublished...)
}

func (f *dnsProvFakeProvider) Kind() string { return "fake" }

func (f *dnsProvFakeProvider) PublishRecordSet(_ context.Context, _ string, records []dnsmanager.DNSRecordSpec) error {
	dnsProvFailReadsMu.Lock()
	defer dnsProvFailReadsMu.Unlock()
	dnsProvPublished = append(dnsProvPublished, records...)
	return nil
}

func (f *dnsProvFakeProvider) ReadRecords(_ context.Context, _ string) ([]dnsmanager.DNSRecordSpec, error) {
	dnsProvFailReadsMu.Lock()
	defer dnsProvFailReadsMu.Unlock()
	if dnsProvFailReads != nil {
		return nil, dnsProvFailReads
	}
	return nil, nil
}

func (f *dnsProvFakeProvider) DeleteRecord(_ context.Context, _ string, _ dnsmanager.DNSRecordSpec) error {
	return nil
}

// dnsProvMemDomains satisfies the service's DomainStore seam without SQL.
type dnsProvMemDomains struct {
	mu   sync.Mutex
	rows map[uint]*models.Domain
}

func (m *dnsProvMemDomains) FindByID(id uint) (*models.Domain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.rows[id]; ok {
		return row, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *dnsProvMemDomains) Update(d *models.Domain) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *d
	m.rows[d.ID] = &cp
	return nil
}

// newDNSProvHarness builds the service the handler tests run against. The
// verification seam is stubbed (no real DNS traffic in tests).
func newDNSProvHarness(t *testing.T) *dnsmanager.Service {
	t.Helper()
	cryptoOnceForDNSProvTests.Do(func() {
		crypto.Init("dns-provider-handler-test-key")
	})
	if !crypto.Enabled() {
		t.Fatal("at-rest encryption must be enabled for the credential tests")
	}
	dnsProvFailReadsMu.Lock()
	dnsProvPublished = nil
	dnsProvFailReads = nil
	dnsProvFailReadsMu.Unlock()

	svc := dnsmanager.NewServiceWithStores(
		&dnsProvMemCreds{rows: map[uint]*models.DNSProviderCredential{}},
		&dnsProvMemStates{rows: map[uint]*models.DNSPublicationState{}},
		&dnsProvMemDomains{rows: map[uint]*models.Domain{}},
		nil, // keys: the legacy CNAME plan path
		dnsmanager.PlanOptions{},
	)
	svc.SetProviderFactory(func(string, dnsmanager.Credential, map[string]string) (dnsmanager.DNSProvider, error) {
		return &dnsProvFakeProvider{}, nil
	})
	svc.SetVerify(func(*models.Domain) (*domain.VerificationResult, error) {
		return &domain.VerificationResult{OwnershipVerified: true, SPFVerified: true}, nil
	})
	return svc
}

var cryptoOnceForDNSProvTests sync.Once

// dnsProvServer wires the handler and registers the five routes with the same
// auth chain the health tests use.
func dnsProvServer(t *testing.T, state *dmrState) *okapi.TestServer {
	t.Helper()
	gdb := dmrGorm(t, state)
	svc := newDNSProvHarness(t)
	h := NewDNSProviderHandler(svc, repositories.NewDomainRepository(gdb), nil)
	o := okapi.NewTestServer(t)
	g := o.Group("/workspaces/current",
		middlewares.Authenticate(middlewares.JWTAuth(&config.Config{JWTSecret: logsTestJWTSecret}), nil, nil, nil),
		dmrWorkspaceBinding,
	)
	g.Post("/domains/{id:int}/dns-provider", okapi.H(h.Connect))
	g.Get("/domains/{id:int}/dns-provider", okapi.H(h.Status))
	g.Delete("/domains/{id:int}/dns-provider", okapi.H(h.Disconnect))
	g.Post("/domains/{id:int}/dns-records/publish", okapi.H(h.Publish))
	g.Get("/domains/{id:int}/dns-drift", okapi.H(h.Drift))
	return o
}

// dnsDo performs an authenticated request as user 7 in workspace 9 (the
// identity the shared test JWT carries).
func dnsDo(t *testing.T, o *okapi.TestServer, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	reader := strings.NewReader("")
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+logsTestJWT(t, "user"))
	req.Header.Set("X-Test-Workspace", "9")
	rec := httptest.NewRecorder()
	o.ServeHTTP(rec, req)
	return rec
}

// ---- tests ----

// Connect stores the credential encrypted and the response carries ONLY the
// masked fingerprint — the raw token appears nowhere in the response body.
func TestDNSProviderConnectMasksCredential(t *testing.T) {
	state := newDmrState()
	dmrSeedDomain(t, state, 1, wsID(9), 7, "example.com")
	o := dnsProvServer(t, state)

	rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-provider", map[string]any{"provider": "cloudflare", "token": "cf-secret-token-4567"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("connect status = %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cf-secret-token-4567") {
		t.Fatal("the connect response leaks the raw token")
	}
	if !strings.Contains(rec.Body.String(), `"masked":"cf-s…4567"`) {
		t.Fatalf("the response lost the masked fingerprint: %s", rec.Body.String())
	}
}

// A mis-scoped token is rejected with 401 and nothing is stored.
func TestDNSProviderConnectRejectsMisScopedToken(t *testing.T) {
	state := newDmrState()
	dmrSeedDomain(t, state, 1, wsID(9), 7, "example.com")
	o := dnsProvServer(t, state)

	SetDNSProvFailReads(dnsmanager.ErrUnauthorized)
	t.Cleanup(func() { SetDNSProvFailReads(nil) })

	rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-provider", map[string]any{"provider": "cloudflare", "token": "cf-secret-token-4567"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cf-secret-token-4567") {
		t.Fatal("the failure response leaks the raw token")
	}
}

// Tenant scoping: a foreign domain is indistinguishable from a missing one
// (404 on every DNS automation endpoint).
func TestDNSProviderForeignDomainIs404(t *testing.T) {
	state := newDmrState()
	dmrSeedDomain(t, state, 1, wsID(9), 7, "example.com")
	dmrSeedDomain(t, state, 2, wsID(10), 8, "foreign.com")
	o := dnsProvServer(t, state)

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := dnsDo(t, o, method, "/workspaces/current/domains/2/dns-provider", nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s on a foreign domain = %d, want 404", method, rec.Code)
		}
	}
	if rec := dnsDo(t, o, http.MethodGet, "/workspaces/current/domains/2/dns-drift", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("drift on a foreign domain = %d, want 404", rec.Code)
	}
}

// Publish for an ownership-unverified domain is refused (fail-closed,
// ADR-008 decision 2) with 409.
func TestDNSProviderPublishRefusesUnverifiedDomain(t *testing.T) {
	state := newDmrState()
	dmrSeedDomain(t, state, 1, wsID(9), 7, "example.com")
	o := dnsProvServer(t, state)

	if rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-provider", map[string]any{"provider": "cloudflare", "token": "cf-secret-token-4567"}); rec.Code != http.StatusCreated {
		t.Fatalf("connect failed: %d %s", rec.Code, rec.Body.String())
	}

	rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-records/publish", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("publish status = %d, want 409 (unverified domain): %s", rec.Code, rec.Body.String())
	}
}

// The drift endpoint serves the stored verdict (unknown before any publish).
func TestDNSProviderDriftStatusUnknownBeforePublish(t *testing.T) {
	state := newDmrState()
	dmrSeedDomain(t, state, 1, wsID(9), 7, "example.com")
	o := dnsProvServer(t, state)

	rec := dnsDo(t, o, http.MethodGet, "/workspaces/current/domains/1/dns-drift", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("drift status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Status    string `json:"status"`
			Connected bool   `json:"connected"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Success || body.Data.Status != models.DNSDriftUnknown || body.Data.Connected {
		t.Fatalf("drift view wrong: %+v", body.Data)
	}
}

// The publish happy path: connected + verified domain publishes the planned
// set; the response carries the verification summary (publish always verifies)
// and the provider received exactly the planned records.
func TestDNSProviderPublishHappyPath(t *testing.T) {
	state := newDmrState()
	seedVerifiedDomain(t, state, 1, wsID(9), 7, "example.com")
	o := dnsProvServer(t, state)

	if rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-provider", map[string]any{"provider": "cloudflare", "token": "cf-secret-token-4567"}); rec.Code != http.StatusCreated {
		t.Fatalf("connect failed: %d %s", rec.Code, rec.Body.String())
	}

	rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-records/publish", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Records      []dnsmanager.DNSRecordSpec `json:"records"`
			Verification *struct {
				SPFVerified bool `json:"spf_verified"`
			} `json:"verification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if len(body.Data.Records) == 0 {
		t.Fatal("publish result carries no records")
	}
	if body.Data.Verification == nil {
		t.Fatal("publish result lost the verification summary (publish must ALWAYS verify)")
	}
	if len(DNSProvPublished()) != len(body.Data.Records) {
		t.Fatalf("provider received %d records, handler reported %d",
			len(DNSProvPublished()), len(body.Data.Records))
	}
}

// Disconnect revokes per domain: after DELETE the status shows no connection.
func TestDNSProviderDisconnectRevokes(t *testing.T) {
	state := newDmrState()
	dmrSeedDomain(t, state, 1, wsID(9), 7, "example.com")
	o := dnsProvServer(t, state)

	if rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-provider", map[string]any{"provider": "route53", "access_key_id": "AKIAIOSFODNN7EXAMPLE", "secret_access_key": "secret"}); rec.Code != http.StatusCreated {
		t.Fatalf("connect failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := dnsDo(t, o, http.MethodDelete, "/workspaces/current/domains/1/dns-provider", nil); rec.Code != http.StatusOK {
		t.Fatalf("disconnect status = %d: %s", rec.Code, rec.Body.String())
	}

	rec := dnsDo(t, o, http.MethodGet, "/workspaces/current/domains/1/dns-provider", nil)
	var body struct {
		Data struct {
			Connected *struct {
				Provider string `json:"provider"`
			} `json:"connected"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.Data.Connected != nil {
		t.Fatalf("the connection survived the revocation: %+v", body.Data.Connected)
	}
}

// The handler's typed-error mapping at connect: the provider reporting a
// missing zone reads as 404 from the endpoint.
func TestDNSProviderConnectMapsZoneNotFound(t *testing.T) {
	state := newDmrState()
	dmrSeedDomain(t, state, 1, wsID(9), 7, "example.com")
	o := dnsProvServer(t, state)

	SetDNSProvFailReads(dnsmanager.ErrZoneNotFound)
	t.Cleanup(func() { SetDNSProvFailReads(nil) })

	rec := dnsDo(t, o, http.MethodPost, "/workspaces/current/domains/1/dns-provider", map[string]any{"provider": "cloudflare", "token": "cf-secret-token-4567"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("connect with zone-not-found = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
