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

package jobs

import (
	"context"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/domain"
	"github.com/goposta/posta/internal/services/dnsmanager"
	"gorm.io/gorm"
)

// The drift cron job (OB-6, ADR-007 decision 1) is a thin sweep over the
// dnsmanager service's CheckDrift: one connected domain per credential, the
// verdict persisted per domain, a provider failure skipping its domain without
// aborting the sweep. The service's store interfaces are implemented by these
// in-memory fakes, so no SQL is involved.

type driftFakeCreds struct {
	mu   sync.Mutex
	rows map[uint]*models.DNSProviderCredential
	list []models.Domain
}

func (m *driftFakeCreds) UpsertCredential(cred *models.DNSProviderCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *cred
	m.rows[cred.DomainID] = &cp
	return nil
}

func (m *driftFakeCreds) FindCredential(domainID uint) (*models.DNSProviderCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[domainID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *driftFakeCreds) DeleteCredential(domainID uint) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rows[domainID]
	delete(m.rows, domainID)
	return ok, nil
}

func (m *driftFakeCreds) FindCredentialDomains() ([]models.Domain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]models.Domain{}, m.list...), nil
}

type driftFakeStates struct {
	mu   sync.Mutex
	rows map[uint]*models.DNSPublicationState
}

func (m *driftFakeStates) FindState(domainID uint) (*models.DNSPublicationState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[domainID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *driftFakeStates) UpsertState(st *models.DNSPublicationState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *st
	m.rows[st.DomainID] = &cp
	return nil
}

type driftFakeDomains struct{ rows map[uint]*models.Domain }

func (m *driftFakeDomains) FindByID(id uint) (*models.Domain, error) {
	if row, ok := m.rows[id]; ok {
		return row, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *driftFakeDomains) Update(d *models.Domain) error { return nil }

// driftZoneProvider keys on the zone (domain) name: "flaky.com" fails.
type driftZoneProvider struct {
	live map[string][]dnsmanager.DNSRecordSpec
}

func (p *driftZoneProvider) Kind() string { return "fake" }

func (p *driftZoneProvider) PublishRecordSet(_ context.Context, zone string, records []dnsmanager.DNSRecordSpec) error {
	p.live[zone] = records
	return nil
}

func (p *driftZoneProvider) ReadRecords(_ context.Context, zone string) ([]dnsmanager.DNSRecordSpec, error) {
	if zone == "flaky.com" {
		return nil, dnsmanager.ErrProviderUnavailable
	}
	return p.live[zone], nil
}

func (p *driftZoneProvider) DeleteRecord(_ context.Context, _ string, _ dnsmanager.DNSRecordSpec) error {
	return nil
}

// A nil service is inert (the dev-mode wiring guard).
func TestDNSDriftCheckJobNilServiceIsInert(t *testing.T) {
	job := NewDNSDriftCheckJob(nil)
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("nil service must be inert: %v", err)
	}
}

// The sweep checks every connected domain: the healthy domain's verdict
// persists (drift found), the flaky domain's check fails without aborting the
// sweep, and a third domain that matches reports ok.
func TestDNSDriftCheckJobSweepsConnectedDomains(t *testing.T) {
	crypto.Init("dns-drift-job-test-key")

	driftDomain := &models.Domain{ID: 1, UserID: 5, Domain: "drifted.com"}
	okDomain := &models.Domain{ID: 2, UserID: 5, Domain: "healthy.com"}
	flakyDomain := &models.Domain{ID: 3, UserID: 5, Domain: "flaky.com"}

	creds := &driftFakeCreds{rows: map[uint]*models.DNSProviderCredential{}}
	states := &driftFakeStates{rows: map[uint]*models.DNSPublicationState{}}
	domains := &driftFakeDomains{rows: map[uint]*models.Domain{
		1: driftDomain, 2: okDomain, 3: flakyDomain,
	}}
	for _, d := range []*models.Domain{driftDomain, okDomain, flakyDomain} {
		d.OwnershipVerified = true
	}
	creds.list = []models.Domain{*driftDomain, *okDomain, *flakyDomain}

	provider := &driftZoneProvider{live: map[string][]dnsmanager.DNSRecordSpec{}}
	// Baselines: driftDomain loses a record; healthy matches; flaky fails.
	provider.live["drifted.com"] = []dnsmanager.DNSRecordSpec{
		{Type: "TXT", Name: "drifted.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300},
	}
	provider.live["healthy.com"] = []dnsmanager.DNSRecordSpec{
		{Type: "TXT", Name: "healthy.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300},
	}

	svc := dnsmanager.NewServiceWithStores(creds, states, domains, nil, dnsmanager.PlanOptions{})
	svc.SetProviderFactory(func(string, dnsmanager.Credential, map[string]string) (dnsmanager.DNSProvider, error) {
		return provider, nil
	})
	svc.SetVerify(func(*models.Domain) (*domain.VerificationResult, error) {
		return &domain.VerificationResult{}, nil
	})

	// Connect (through the real path, so credentials are encrypted as in
	// production) and publish baselines for the two healthy-provider domains.
	for _, d := range []*models.Domain{driftDomain, okDomain} {
		if _, err := svc.Connect(context.Background(), d, dnsmanager.ConnectRequest{
			Provider: models.DNSProviderCloudflare, Token: "job-test-token",
		}); err != nil {
			t.Fatalf("connect %s: %v", d.Domain, err)
		}
		if _, err := svc.PublishDomainRecords(context.Background(), d); err != nil {
			t.Fatalf("seed publish for %s: %v", d.Domain, err)
		}
	}
	// drifted.com's zone loses its DMARC record after the publish — the drift
	// the sweep must find.
	trimmed := make([]dnsmanager.DNSRecordSpec, 0, len(provider.live["drifted.com"]))
	for _, r := range provider.live["drifted.com"] {
		if r.Name != "_dmarc.drifted.com" {
			trimmed = append(trimmed, r)
		}
	}
	provider.live["drifted.com"] = trimmed

	job := NewDNSDriftCheckJob(svc)
	if err := job.Run(context.Background(), nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	drifted, err := states.FindState(1)
	if err != nil {
		t.Fatalf("drifted state missing: %v", err)
	}
	if drifted.DriftStatus != models.DNSDriftFound {
		t.Fatalf("drifted.com status = %s, want drift", drifted.DriftStatus)
	}
	healthy, err := states.FindState(2)
	if err != nil {
		t.Fatalf("healthy state missing: %v", err)
	}
	if healthy.DriftStatus != models.DNSDriftOK {
		t.Fatalf("healthy.com status = %s, want ok", healthy.DriftStatus)
	}
	// The flaky domain's stored verdict is untouched (outage ≠ drift).
	flaky, err := states.FindState(3)
	if err == nil && flaky.DriftStatus != models.DNSDriftUnknown {
		t.Fatalf("flaky.com status changed during the outage: %s", flaky.DriftStatus)
	}
}

// The scan set is the connected domains (the service's ConnectedDomains
// delegates to the credential store).
func TestDNSDriftCheckJobScanSetIsConnectedDomains(t *testing.T) {
	creds := &driftFakeCreds{rows: map[uint]*models.DNSProviderCredential{}}
	creds.list = []models.Domain{{ID: 4, UserID: 5, Domain: "one.com"}, {ID: 5, UserID: 5, Domain: "two.com"}}
	svc := dnsmanager.NewServiceWithStores(creds, &driftFakeStates{rows: map[uint]*models.DNSPublicationState{}},
		&driftFakeDomains{rows: map[uint]*models.Domain{}}, nil, dnsmanager.PlanOptions{})
	set, err := svc.ConnectedDomains()
	if err != nil {
		t.Fatalf("scan set: %v", err)
	}
	if len(set) != 2 || set[0].Domain != "one.com" || set[1].Domain != "two.com" {
		t.Fatalf("scan set = %+v, want the two connected domains", set)
	}
}
