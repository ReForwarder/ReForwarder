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

package dnsmanager

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/domain"
	"gorm.io/gorm"
)

// ---- in-memory stores ----

type memCreds struct {
	mu   sync.Mutex
	rows map[uint]*models.DNSProviderCredential
}

func newMemCreds() *memCreds { return &memCreds{rows: map[uint]*models.DNSProviderCredential{}} }

func (m *memCreds) UpsertCredential(cred *models.DNSProviderCredential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *cred
	m.rows[cred.DomainID] = &cp
	return nil
}

func (m *memCreds) FindCredential(domainID uint) (*models.DNSProviderCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[domainID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *memCreds) DeleteCredential(domainID uint) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rows[domainID]
	delete(m.rows, domainID)
	return ok, nil
}

func (m *memCreds) FindCredentialDomains() ([]models.Domain, error) {
	return nil, nil
}

type memStates struct {
	mu   sync.Mutex
	rows map[uint]*models.DNSPublicationState
}

func newMemStates() *memStates {
	return &memStates{rows: map[uint]*models.DNSPublicationState{}}
}

func (m *memStates) FindState(domainID uint) (*models.DNSPublicationState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[domainID]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (m *memStates) UpsertState(st *models.DNSPublicationState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *st
	m.rows[st.DomainID] = &cp
	return nil
}

// memDomains satisfies the DomainStore seam for the verification-persist path.
type memDomains struct{ rows map[uint]*models.Domain }

func newMemDomains(ds ...*models.Domain) *memDomains {
	m := &memDomains{rows: map[uint]*models.Domain{}}
	for _, d := range ds {
		m.rows[d.ID] = d
	}
	return m
}

func (m *memDomains) FindByID(id uint) (*models.Domain, error) {
	if row, ok := m.rows[id]; ok {
		return row, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (m *memDomains) Update(d *models.Domain) error {
	cp := *d
	m.rows[d.ID] = &cp
	return nil
}

// recorderDriftAlerter records the drift transition actions.
type recorderDriftAlerter struct{ actions []string }

func (r *recorderDriftAlerter) LogScoped(_ *uint, _ uint, _, _, action, _ string, _ map[string]any) {
	r.actions = append(r.actions, action)
}

// fakeProvider is the injectable DNSProvider behind the service tests: it
// answers ReadRecords with a fixed live set, records what PublishRecordSet
// received, and can fail on demand with a typed cause.
type fakeProvider struct {
	mu        sync.Mutex
	live      []DNSRecordSpec
	published []DNSRecordSpec
	readErr   error
	pubErr    error
	verified  *int // counts verify calls through the service's verify seam
}

func (f *fakeProvider) Kind() string { return "fake" }

func (f *fakeProvider) PublishRecordSet(_ context.Context, _ string, records []DNSRecordSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pubErr != nil {
		return f.pubErr
	}
	f.published = append(f.published, records...)
	f.live = append([]DNSRecordSpec{}, records...)
	return nil
}

func (f *fakeProvider) ReadRecords(_ context.Context, _ string) ([]DNSRecordSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	return append([]DNSRecordSpec{}, f.live...), nil
}

func (f *fakeProvider) DeleteRecord(_ context.Context, _ string, _ DNSRecordSpec) error { return nil }

// memKeys satisfies the SigningKeyReader seam with a fixed active key.
type memKeys struct{ keys []models.DomainSigningKey }

func (m *memKeys) FindByDomainID(uint) ([]models.DomainSigningKey, error) {
	return m.keys, nil
}

// svcTest is one service-test assembly: memory stores, a fake provider
// factory, an injectable verify seam, and the domain under test.
type svcTest struct {
	t        *testing.T
	svc      *Service
	provider *fakeProvider
	alerter  *recorderDriftAlerter
	creds    *memCreds
	states   *memStates
	domains  *memDomains
	verifyCalls *int
}

func newSvcTest(t *testing.T, d *models.Domain, planOpts PlanOptions) *svcTest {
	t.Helper()
	cryptoInitForTests(t)
	provider := &fakeProvider{}
	verifyCalls := 0
	creds := newMemCreds()
	states := newMemStates()
	domains := newMemDomains(d)
	keys := &memKeys{keys: []models.DomainSigningKey{{
		ID: 1, DomainID: d.ID, Selector: "posta", Algorithm: models.SigningAlgorithmEd25519,
		Status: models.DomainSigningStatusActive, PrivateKeyPem: "enc:test",
	}}}
	svc := NewServiceWithStores(creds, states, domains, keys, planOpts)
	svc.SetProviderFactory(func(string, Credential, map[string]string) (DNSProvider, error) {
		return provider, nil
	})
	svc.SetVerify(func(*models.Domain) (*domain.VerificationResult, error) {
		verifyCalls++
		return &domain.VerificationResult{OwnershipVerified: true, SPFVerified: true}, nil
	})
	alerter := &recorderDriftAlerter{}
	svc.setAlerter(alerter)
	return &svcTest{
		t: t, svc: svc, provider: provider, alerter: alerter,
		creds: creds, states: states, domains: domains,
		verifyCalls: &verifyCalls,
	}
}

var cryptoOnceForTests sync.Once

// cryptoInitForTests pins the at-rest encryption key once for the package's
// tests (the production path calls crypto.Init at boot).
func cryptoInitForTests(t *testing.T) {
	t.Helper()
	cryptoOnceForTests.Do(func() {
		crypto.Init("dnsmanager-test-key-0123456789abcdef")
	})
	if !crypto.Enabled() {
		t.Fatal("at-rest encryption must be enabled for the credential tests")
	}
}

// connect wires a credential for the test domain through the real Connect
// path (so the stored row is encrypted exactly as production stores it).
func (x *svcTest) connect(d *models.Domain) *CredentialView {
	x.t.Helper()
	view, err := x.svc.Connect(context.Background(), d, ConnectRequest{
		Provider: models.DNSProviderCloudflare,
		Token:    "tok-secret-9876",
	})
	if err != nil {
		x.t.Fatalf("connect: %v", err)
	}
	return view
}

// ---- credential safety ----

// MaskCredential never reveals the middle of the secret; a short secret is
// masked entirely.
func TestMaskCredentialNeverRevealsSecret(t *testing.T) {
	long := MaskCredential(Credential{Token: "cf-api-token-1234567890abcd"})
	if long != "cf-a…abcd" {
		t.Fatalf("mask = %q, want cf-a…abcd", long)
	}
	if strings.Contains(long, "1234567890") {
		t.Fatalf("mask leaks the secret middle: %q", long)
	}
	short := MaskCredential(Credential{Token: "tiny"})
	if strings.Contains(short, "tiny") {
		t.Fatalf("short secret leaks: %q", short)
	}
	access := MaskCredential(Credential{AccessKeyID: "AKIAIOSFODNN7EXAMPLE"})
	if access != "AKIA…MPLE" {
		t.Fatalf("access-key mask = %q", access)
	}
}

// Connect stores the credential encrypted (enc: prefix, no plaintext) and the
// API view is masked: the raw token appears nowhere in the response.
func TestConnectStoresEncryptedAndMasksResponse(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.connect(d)

	row, err := x.creds.FindCredential(d.ID)
	if err != nil {
		t.Fatalf("stored credential missing: %v", err)
	}
	if !strings.HasPrefix(row.Credential, "enc:") {
		t.Fatalf("credential stored unencrypted: %q", row.Credential[:12])
	}
	if strings.Contains(row.Credential, "tok-secret-9876") {
		t.Fatal("the raw token is readable in the stored credential")
	}
	// Decrypt takes the FULL stored value (it strips the enc: prefix itself).
	dec, err := crypto.Decrypt(row.Credential)
	if err != nil || !strings.Contains(dec, "tok-secret-9876") {
		t.Fatalf("stored credential does not decrypt to the original token: %v", err)
	}

	view := x.connect(d)
	wire, _ := json.Marshal(view)
	if strings.Contains(string(wire), "tok-secret-9876") {
		t.Fatal("the API response leaks the raw token")
	}
	if view.Masked != "tok-…9876" {
		t.Fatalf("masked fingerprint = %q, want tok-…9876", view.Masked)
	}
}

// A mis-scoped credential is rejected at connect time with a typed error and
// nothing is stored (ADR-007 decision 2's pre-write validation, applied to
// the credential itself).
func TestConnectRejectsRejectedCredential(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.provider.readErr = ErrUnauthorized

	_, err := x.svc.Connect(context.Background(), d, ConnectRequest{
		Provider: models.DNSProviderCloudflare, Token: "tok-secret-9876",
	})
	if !errorsIs(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if _, findErr := x.creds.FindCredential(d.ID); !errorsIs(findErr, gorm.ErrRecordNotFound) {
		t.Fatal("a rejected credential must not be stored")
	}
}

// An unknown provider kind is typed at the adapter factory.
func TestConnectUnknownProviderIsTyped(t *testing.T) {
	if _, err := NewProvider("dnsmadeeasy", Credential{}, nil); err == nil ||
		!strings.Contains(err.Error(), "unknown DNS provider") {
		t.Fatalf("error = %v, want the unknown-provider message", err)
	}
	// The three supported kinds all construct.
	for _, kind := range []string{models.DNSProviderCloudflare, models.DNSProviderRoute53, models.DNSProviderDigitalOcean} {
		if _, err := NewProvider(kind, Credential{Token: "t"}, nil); err != nil {
			t.Fatalf("provider %q must construct: %v", kind, err)
		}
	}
}

// Per-domain revocation: Disconnect removes the credential.
func TestDisconnectRemovesCredential(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.connect(d)

	removed, err := x.svc.Disconnect(d)
	if err != nil || !removed {
		t.Fatalf("disconnect: removed=%v err=%v", removed, err)
	}
	if _, err := x.creds.FindCredential(d.ID); !errorsIs(err, gorm.ErrRecordNotFound) {
		t.Fatal("credential survived the revocation")
	}
}

// ---- publish flow ----

// Publish requires a connection (typed ErrNotConnected).
func TestPublishRequiresConnection(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	_, err := x.svc.PublishDomainRecords(context.Background(), d)
	if !errorsIs(err, ErrNotConnected) {
		t.Fatalf("error = %v, want ErrNotConnected", err)
	}
}

// Fail-closed (ADR-008 decision 2): an unverified domain is refused and the
// provider is never called.
func TestPublishRefusesUnverifiedDomain(t *testing.T) {
	d := planDomain("example.com", false)
	x := newSvcTest(t, d, PlanOptions{})
	x.connect(d)

	_, err := x.svc.PublishDomainRecords(context.Background(), d)
	if !errorsIs(err, ErrDomainNotVerified) {
		t.Fatalf("error = %v, want ErrDomainNotVerified", err)
	}
	if len(x.provider.published) != 0 {
		t.Fatal("the provider was called for an unverified domain")
	}
}

// The publish → verify invariant (ADR-007 decision 1): a successful publish
// ALWAYS runs the R-07 verification flow and persists its outcome on the
// domain, and the publication baseline is recorded for drift comparison.
func TestPublishAlwaysRunsVerificationAndRecordsBaseline(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.connect(d)

	res, err := x.svc.PublishDomainRecords(context.Background(), d)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if *x.verifyCalls != 1 {
		t.Fatalf("verification ran %d times after the publish, want exactly 1", *x.verifyCalls)
	}
	if res.Verification == nil || !res.Verification.SPFVerified {
		t.Fatal("the publish result lost the verification summary")
	}
	// The verification outcome persists on the domain (mirrors diagnose).
	updated, _ := x.domains.FindByID(d.ID)
	if !updated.OwnershipVerified || !updated.SPFVerified {
		t.Fatalf("verification state not persisted: %+v", updated)
	}
	// The baseline records what was published.
	st, err := x.states.FindState(d.ID)
	if err != nil {
		t.Fatalf("publication state missing: %v", err)
	}
	specs, err := unmarshalSpecs(st.PublishedRecords)
	if err != nil || len(specs) != 4 {
		t.Fatalf("baseline = %d records (err %v), want the 4 planned deliverability records", len(specs), err)
	}
	if st.PublishError != "" {
		t.Fatalf("publish error left set after a success: %q", st.PublishError)
	}
	if st.PublishedAt == nil {
		t.Fatal("PublishedAt not recorded")
	}
}

// A provider publish failure is typed, carries the applied prefix, and is
// persisted on the state row for the UI.
func TestPublishFailureIsTypedAndRecorded(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.connect(d)

	x.provider.pubErr = &PublishError{
		Zone: d.Domain,
		Applied: []DNSRecordSpec{{Type: "TXT", Name: "example.com", Value: "v=spf1 ~all"}},
		Failed:  DNSRecordSpec{Type: "TXT", Name: "_dmarc.example.com", Value: "v=DMARC1"},
		Cause:   ErrUnauthorized,
	}

	_, err := x.svc.PublishDomainRecords(context.Background(), d)
	if !errorsIs(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	var pubErr *PublishError
	if !errorsAs(err, &pubErr) {
		t.Fatalf("error is not a *PublishError: %v", err)
	}
	if len(pubErr.Applied) != 1 {
		t.Fatalf("applied prefix lost: %d", len(pubErr.Applied))
	}
	st, stateErr := x.states.FindState(d.ID)
	if stateErr != nil {
		t.Fatalf("state missing after the failure: %v", stateErr)
	}
	if !strings.Contains(st.PublishError, "provider rejected the credentials") {
		t.Fatalf("PublishError not persisted: %q", st.PublishError)
	}
	// No baseline was written (nothing published).
	if len(st.PublishedRecords) != 0 {
		t.Fatal("a failed publish must not write a publication baseline")
	}
}

// Mail routing requested without a mail host is a typed configuration error,
// not a silent narrowing to the four deliverability records.
func TestPublishMailRoutingWithoutMailHostIsTyped(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{IncludeMailRouting: true})
	x.connect(d)

	_, err := x.svc.PublishDomainRecords(context.Background(), d)
	if !errorsIs(err, ErrMailHostMissing) {
		t.Fatalf("error = %v, want ErrMailHostMissing", err)
	}
	if len(x.provider.published) != 0 {
		t.Fatal("the provider was called despite the configuration error")
	}
}

// The managed DKIM key's TXT record feeds the plan through the dkimTXT seam.
func TestPublishPlanUsesManagedDKIMKey(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.svc.SetDKIMTXT(func(encryptedPem, algorithm string) (string, error) {
		return "v=DKIM1; h=sha256; k=ed25519; p=pubkey", nil
	})
	x.connect(d)

	res, err := x.svc.PublishDomainRecords(context.Background(), d)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	found := false
	for _, r := range res.Records {
		if r.Type == "TXT" && normalizeName(r.Name) == "posta._domainkey.example.com" &&
			r.Value == "v=DKIM1; h=sha256; k=ed25519; p=pubkey" {
			found = true
		}
	}
	if !found {
		t.Fatalf("managed DKIM TXT missing from the published set: %+v", res.Records)
	}
}

// ---- drift detection ----

func (x *svcTest) seedBaseline(d *models.Domain) {
	x.t.Helper()
	x.connect(d)
	if _, err := x.svc.PublishDomainRecords(context.Background(), d); err != nil {
		x.t.Fatalf("seed publish: %v", err)
	}
}

// Live records matching the baseline report ok.
func TestDriftOKWhenLiveMatchesBaseline(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.seedBaseline(d)

	view, err := x.svc.CheckDrift(context.Background(), d)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if view.Status != models.DNSDriftOK {
		t.Fatalf("status = %s, want ok (findings %v)", view.Status, view.Findings)
	}
	if len(x.alerter.actions) != 0 {
		t.Fatalf("no alert expected on ok: %v", x.alerter.actions)
	}
}

// A missing or changed record is drift: typed findings, persisted verdict,
// and an alert event on the transition.
func TestDriftDetectsMissingAndChanged(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.seedBaseline(d)

	// Live diverges: the DMARC record vanished and SPF changed.
	live := []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "posta-verification=tok-123", TTL: 300},
		{Type: "TXT", Name: "example.com", Value: "v=spf1 -all", TTL: 300}, // changed
		{Type: "CNAME", Name: "posta._domainkey.example.com", Value: "posta._domainkey.posta", TTL: 300},
	}
	x.provider.mu.Lock()
	x.provider.live = live
	x.provider.mu.Unlock()

	view, err := x.svc.CheckDrift(context.Background(), d)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if view.Status != models.DNSDriftFound {
		t.Fatalf("status = %s, want drift", view.Status)
	}
	if len(view.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (missing DMARC + changed SPF): %+v", len(view.Findings), view.Findings)
	}
	kinds := map[string]string{}
	for _, f := range view.Findings {
		kinds[f.Kind] = f.Record
	}
	if _, ok := kinds["missing"]; !ok {
		t.Fatalf("missing finding absent: %+v", view.Findings)
	}
	if _, ok := kinds["changed"]; !ok {
		t.Fatalf("changed finding absent: %+v", view.Findings)
	}
	// The alert fired exactly once (into drift).
	if len(x.alerter.actions) != 1 || x.alerter.actions[0] != "domain.dns_drift_detected" {
		t.Fatalf("alerts = %v, want one drift_detected", x.alerter.actions)
	}
	// The verdict persists as the health surface.
	stored, _ := x.states.FindState(d.ID)
	if stored.DriftStatus != models.DNSDriftFound {
		t.Fatalf("stored status = %s, want drift", stored.DriftStatus)
	}
}

// A second check with live matching again resolves the drift and fires the
// resolved alert.
func TestDriftResolutionFiresResolvedAlert(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.seedBaseline(d)

	x.provider.mu.Lock()
	x.provider.live = []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "posta-verification=tok-123", TTL: 300},
	} // SPF/DKIM/DMARC "missing"
	x.provider.mu.Unlock()
	if _, err := x.svc.CheckDrift(context.Background(), d); err != nil {
		t.Fatalf("check 1: %v", err)
	}

	x.provider.mu.Lock()
	x.provider.live = []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "posta-verification=tok-123", TTL: 300},
		{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300},
		{Type: "CNAME", Name: "posta._domainkey.example.com", Value: "posta._domainkey.posta", TTL: 300},
		{Type: "TXT", Name: "_dmarc.example.com", Value: "v=DMARC1; p=none; rua=mailto:dmarc@example.com", TTL: 300},
	}
	x.provider.mu.Unlock()
	view, err := x.svc.CheckDrift(context.Background(), d)
	if err != nil {
		t.Fatalf("check 2: %v", err)
	}
	if view.Status != models.DNSDriftOK {
		t.Fatalf("status = %s, want ok after resolution", view.Status)
	}
	want := []string{"domain.dns_drift_detected", "domain.dns_drift_resolved"}
	if len(x.alerter.actions) != 2 || x.alerter.actions[0] != want[0] || x.alerter.actions[1] != want[1] {
		t.Fatalf("alerts = %v, want %v", x.alerter.actions, want)
	}
}

// A provider outage is NOT drift: the error surfaces, the stored verdict
// stays untouched, no alert fires.
func TestDriftProviderOutageLeavesVerdictUntouched(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.seedBaseline(d)

	x.provider.mu.Lock()
	x.provider.readErr = ErrProviderUnavailable
	x.provider.mu.Unlock()

	if _, err := x.svc.CheckDrift(context.Background(), d); !errorsIs(err, ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
	stored, _ := x.states.FindState(d.ID)
	if stored.DriftStatus != models.DNSDriftUnknown {
		t.Fatalf("stored status changed to %s during an outage", stored.DriftStatus)
	}
	if len(x.alerter.actions) != 0 {
		t.Fatalf("alerts fired during an outage: %v", x.alerter.actions)
	}
}

// Nothing published (or no baseline) → unknown, and the provider is never
// consulted.
func TestDriftWithoutBaselineIsUnknown(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.connect(d)

	view, err := x.svc.CheckDrift(context.Background(), d)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if view.Status != models.DNSDriftUnknown {
		t.Fatalf("status = %s, want unknown without a baseline", view.Status)
	}
	if len(x.provider.published) != 0 {
		t.Fatal("the provider was consulted without a baseline")
	}
}

// Unrelated live records are NOT drift: the automation owns its records, not
// the whole zone.
func TestDriftIgnoresUnrelatedRecords(t *testing.T) {
	d := planDomain("example.com", true)
	x := newSvcTest(t, d, PlanOptions{})
	x.seedBaseline(d)

	x.provider.mu.Lock()
	x.provider.live = append(append([]DNSRecordSpec{}, x.provider.live...),
		DNSRecordSpec{Type: "A", Name: "www.example.com", Value: "5.6.7.8"})
	x.provider.mu.Unlock()

	view, err := x.svc.CheckDrift(context.Background(), d)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if view.Status != models.DNSDriftOK {
		t.Fatalf("status = %s, want ok — unrelated records are not drift", view.Status)
	}
}

// ---- plan quoting semantics shared with drift comparison ----

// valueMatches is quote- and case-tolerant: the three providers echo TXT
// content with different quoting/whitespace conventions.
func TestValueMatchesAcrossProviderConventions(t *testing.T) {
	cases := [][2]string{
		{`"v=spf1 include:_spf.posta ~all"`, `v=spf1 include:_spf.posta ~all`},
		{`V=DMARC1; p=none`, `v=dmarc1;  p=none`},
		{`"quoted"`, `"quoted"`},
	}
	for i, c := range cases {
		if !valueMatches(c[0], c[1]) {
			t.Fatalf("case %d: %q must match %q", i, c[0], c[1])
		}
	}
	if valueMatches(`"v=spf1 -all"`, `"v=spf1 ~all"`) {
		t.Fatal("different values must not match")
	}
}

// normalizeName strips trailing dots and lower-cases (Route53 echoes FQDNs
// with a trailing dot; drift comparison must be stable).
func TestNormalizeNameStable(t *testing.T) {
	if normalizeName("Example.COM.") != normalizeName("example.com") {
		t.Fatal("normalization is not stable")
	}
}
