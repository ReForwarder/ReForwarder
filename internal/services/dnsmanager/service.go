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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/domain"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// CredentialStore persists per-domain provider credentials. Production backs
// it with repositories.DNSProviderRepository; tests inject an in-memory store.
type CredentialStore interface {
	UpsertCredential(cred *models.DNSProviderCredential) error
	FindCredential(domainID uint) (*models.DNSProviderCredential, error)
	DeleteCredential(domainID uint) (bool, error)
	FindCredentialDomains() ([]models.Domain, error)
}

// StateStore persists the publication/drift baseline per domain.
type StateStore interface {
	FindState(domainID uint) (*models.DNSPublicationState, error)
	UpsertState(st *models.DNSPublicationState) error
}

// DomainStore reads domains and persists the post-verify check state.
type DomainStore interface {
	FindByID(id uint) (*models.Domain, error)
	Update(d *models.Domain) error
}

// SigningKeyReader lists a domain's managed DKIM signing keys (the planner's
// TXT-record source, R-02).
type SigningKeyReader interface {
	FindByDomainID(domainID uint) ([]models.DomainSigningKey, error)
}

// Service is the OB-6 DNS automation orchestration (ADR-007 decision 1):
// per-domain provider connections with encrypted credentials, the
// plan→publish→verify flow, and the drift re-verification pass.
//
// The handlers audit operator actions; the service audits only the automated
// drift transitions (system actor, the dkim-retire job's pattern), so both
// trails stay coherent.
type Service struct {
	repo       CredentialStore
	stateStore StateStore
	domainRepo DomainStore
	keyRepo    SigningKeyReader
	// verify is the existing R-07 verification flow; publish ALWAYS runs it
	// (ADR-007 decision 1 — auto-publish never skips verification).
	verify func(*models.Domain) (*domain.VerificationResult, error)
	// dkimTXT derives the managed signing key's public DNS TXT value; wired
	// to email.DnsTXTRecord (kept as a seam so this package stays a leaf and
	// tests inject deterministic values).
	dkimTXT func(encryptedPem, algorithm string) (string, error)
	// planOptions carries the deployment's mail host + mail-routing opt-in.
	planOptions PlanOptions
	now         func() time.Time
	// newProvider is the adapter factory; a field so tests inject fakes.
	newProvider func(kind string, cred Credential, baseOverrides map[string]string) (DNSProvider, error)
	// baseOverrides points adapters at fake API servers (tests only).
	baseOverrides map[string]string
	// auditLog records the automated drift events (nil = no audit).
	auditLog DriftAlerter
}

// NewService builds the service over the gorm-backed stores.
func NewService(db *gorm.DB, planOptions PlanOptions) *Service {
	return &Service{
		repo:        repositories.NewDNSProviderRepository(db),
		stateStore:  repositories.NewDNSProviderRepository(db),
		domainRepo:  repositories.NewDomainRepository(db),
		keyRepo:     repositories.NewDomainSigningKeyRepository(db),
		verify:      domain.Verify,
		now:         time.Now,
		newProvider: NewProvider,
	}
}

// DriftAlerter receives the automated drift-transition events. *audit.Logger
// implements it; tests inject a recorder.
type DriftAlerter interface {
	LogScoped(workspaceID *uint, actorID uint, actorEmail, clientIP, action, message string, metadata map[string]any)
}

// NewServiceWithStores builds the service over injected stores (tests).
func NewServiceWithStores(creds CredentialStore, states StateStore, domains DomainStore, keys SigningKeyReader, planOptions PlanOptions) *Service {
	return &Service{
		repo:        creds,
		stateStore:  states,
		domainRepo:  domains,
		keyRepo:     keys,
		verify:      domain.Verify,
		planOptions: planOptions,
		now:         time.Now,
		newProvider: NewProvider,
	}
}

// SetAuditLogger wires the audit trail for the automated drift events.
func (s *Service) SetAuditLogger(l *audit.Logger) { s.auditLog = l }

// setAlerter is the raw seam (tests).
func (s *Service) setAlerter(a DriftAlerter) { s.auditLog = a }

func (s *Service) alert(workspaceID *uint, action, message string, metadata map[string]any) {
	if s.auditLog != nil {
		s.auditLog.LogScoped(workspaceID, 0, "system", "", action, message, metadata)
	}
}

// SetVerify overrides the verification flow (tests).
func (s *Service) SetVerify(fn func(*models.Domain) (*domain.VerificationResult, error)) {
	if fn != nil {
		s.verify = fn
	}
}

// SetDKIMTXT wires the managed signing key's public TXT value derivation
// (email.DnsTXTRecord) — production wiring; tests inject deterministic values.
func (s *Service) SetDKIMTXT(fn func(encryptedPem, algorithm string) (string, error)) {
	if fn != nil {
		s.dkimTXT = fn
	}
}

// SetBaseOverrides points the adapters at fake provider API servers (tests).
func (s *Service) SetBaseOverrides(m map[string]string) { s.baseOverrides = m }

// SetProviderFactory overrides the adapter factory (tests).
func (s *Service) SetProviderFactory(fn func(kind string, cred Credential, baseOverrides map[string]string) (DNSProvider, error)) {
	if fn != nil {
		s.newProvider = fn
	}
}

// PlanRecords computes the publishable record set for the domain, including
// the managed DKIM signing key's TXT when an Active key exists (R-02).
func (s *Service) PlanRecords(d *models.Domain) ([]PlannedRecord, error) {
	dkimTXT, err := s.activeDKIMTXT(d)
	if err != nil {
		return nil, err
	}
	return Plan(d, dkimTXT, s.planOptions), nil
}

// activeDKIMTXT resolves the Active managed signing key's public TXT value,
// or "" when the domain uses the legacy upstream CNAME (no managed key, or
// key material undecryptable — which surfaces as an error only when a key
// exists and cannot be read: a corrupt key must not be silently skipped).
func (s *Service) activeDKIMTXT(d *models.Domain) (string, error) {
	if s.dkimTXT == nil || s.keyRepo == nil {
		return "", nil
	}
	keys, err := s.keyRepo.FindByDomainID(d.ID)
	if err != nil {
		return "", nil // no keys (or the read failed) — legacy CNAME plan
	}
	for i := range keys {
		k := &keys[i]
		if k.Status != models.DomainSigningStatusActive {
			continue
		}
		v, err := s.dkimTXT(k.PrivateKeyPem, k.Algorithm)
		if err != nil {
			return "", fmt.Errorf("dnsmanager: derive DKIM DNS record for %s: %w", d.Domain, err)
		}
		return v, nil
	}
	return "", nil
}

// Connect validates the credential against the provider (read-only), stores
// it encrypted, and returns the masked view. The pre-store access check is
// ADR-007 decision 2's no-partial-zone guarantee applied at connect: a
// mis-scoped token is rejected HERE, with a typed error, before anything is
// stored or written.
func (s *Service) Connect(ctx context.Context, d *models.Domain, req ConnectRequest) (*CredentialView, error) {
	provider, err := s.newProvider(req.Provider, credentialOf(req), s.baseOverrides)
	if err != nil {
		return nil, err
	}
	// Read-only access check: resolves the zone and proves the credential
	// works. No DNS writes happen on this path.
	if _, err := provider.ReadRecords(ctx, d.Domain); err != nil {
		return nil, fmt.Errorf("dnsmanager: provider access check failed: %w", err)
	}
	enc, err := encryptCredential(credentialOf(req))
	if err != nil {
		return nil, err
	}
	// Cache the zone identifier when the provider surfaced one (only the
	// resolve path knows it; the read already validated it).
	row := &models.DNSProviderCredential{
		UserID:         d.UserID,
		WorkspaceID:    d.WorkspaceID,
		DomainID:       d.ID,
		Provider:       req.Provider,
		Credential:     enc,
		LastVerifiedAt: ptrTime(s.now()),
	}
	if err := s.repo.UpsertCredential(row); err != nil {
		return nil, fmt.Errorf("dnsmanager: store credential: %w", err)
	}
	return statusViewOf(row), nil
}

// Disconnect revokes the domain's provider credential (per-domain revocation,
// ADR-007 decision 2). Publication/drift state is kept as history; drift
// checks stop because no provider connection remains.
func (s *Service) Disconnect(d *models.Domain) (bool, error) {
	return s.repo.DeleteCredential(d.ID)
}

// ConnectedDomains lists every domain with a provider connection — the drift
// cron's scan set. Ownership resolves through each domain row's own columns
// (ADR-008 decision 1).
func (s *Service) ConnectedDomains() ([]models.Domain, error) {
	return s.repo.FindCredentialDomains()
}

// PublishDomainRecords runs the plan → publish → verify flow:
//
//  1. refuses unverified domains (fail-closed, ADR-008 decision 2) and
//     unconnected domains (typed errors);
//  2. plans the records (verification/SPF/DKIM/DMARC always; MX/return-path
//     per the deployment's mail-routing opt-in);
//  3. publishes through the provider (adapters validate access pre-write);
//  4. ALWAYS runs the existing verification flow (R-07) afterwards and
//     persists the fresh check state on the domain, exactly like the health
//     page's diagnose;
//  5. records the publication baseline the drift cron compares against.
//
// A provider failure returns the typed *PublishError after persisting it on
// the publication state row, so the UI shows what failed.
func (s *Service) PublishDomainRecords(ctx context.Context, d *models.Domain) (*PublishResult, error) {
	credRow, err := s.repo.FindCredential(d.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotConnected
		}
		return nil, err
	}
	// Fail-closed gate (ADR-008 decision 2): no publish for unverified domains.
	if !d.IsOwnershipVerified() {
		s.recordPublishFailure(d.ID, ErrDomainNotVerified)
		return nil, ErrDomainNotVerified
	}
	// Mail routing was requested but the deployment has no mail host: the
	// publish would silently narrow to the four deliverability records. That
	// is a configuration error worth surfacing, not a silent shortfall.
	if s.planOptions.IncludeMailRouting && s.planOptions.MailHost == "" {
		s.recordPublishFailure(d.ID, ErrMailHostMissing)
		return nil, ErrMailHostMissing
	}

	provider, _, err := s.providerFor(credRow)
	if err != nil {
		return nil, err
	}
	plan, err := s.PlanRecords(d)
	if err != nil {
		return nil, err
	}

	if err := provider.PublishRecordSet(ctx, d.Domain, plannedOf(plan)); err != nil {
		s.recordPublishFailure(d.ID, err)
		return nil, err
	}

	// Publication baseline for drift comparison.
	now := s.now()
	if err := s.stateStore.UpsertState(&models.DNSPublicationState{
		UserID:           d.UserID,
		WorkspaceID:      d.WorkspaceID,
		DomainID:         d.ID,
		PublishedRecords: marshalSpecs(plannedOf(plan)),
		PublishedAt:      &now,
		PublishError:     "",
		DriftStatus:      models.DNSDriftUnknown,
	}); err != nil {
		return nil, fmt.Errorf("dnsmanager: record publication state: %w", err)
	}

	// The R-07 verification flow ALWAYS follows the publish.
	result, err := s.verify(d)
	summary := &VerificationSummary{CheckedAt: now}
	if err != nil {
		// A DNS-resolution failure must not undo the publish; surface it in
		// the summary as all-false (the drift cron re-checks).
		logger.Warn("dnsmanager: post-publish verification failed", "domain", d.Domain, "error", err)
	} else {
		summary.OwnershipVerified = result.OwnershipVerified
		summary.SPFVerified = result.SPFVerified
		summary.DKIMVerified = result.DKIMVerified
		summary.DMARCVerified = result.DMARCVerified
		// Persist the fresh check state (mirrors the diagnose endpoint), so
		// stored booleans reflect what DNS says after the publish.
		d.OwnershipVerified = result.OwnershipVerified
		d.SPFVerified = result.SPFVerified
		d.DKIMVerified = result.DKIMVerified
		d.DMARCVerified = result.DMARCVerified
		if uerr := s.domainRepo.Update(d); uerr != nil {
			logger.Error("dnsmanager: persist verification state", "domain", d.Domain, "error", uerr)
		}
	}

	return &PublishResult{Records: plannedOf(plan), Verification: summary}, nil
}

// providerFor decrypts the stored credential and builds the adapter.
func (s *Service) providerFor(row *models.DNSProviderCredential) (DNSProvider, Credential, error) {
	cred, err := decryptCredential(row.Credential)
	if err != nil {
		return nil, Credential{}, fmt.Errorf("dnsmanager: stored credential unreadable: %w", err)
	}
	provider, err := s.newProvider(row.Provider, cred, s.baseOverrides)
	if err != nil {
		return nil, Credential{}, err
	}
	return provider, cred, nil
}

// recordPublishFailure persists a failed publish on the state row so the UI
// (and the drift status endpoint) can show it.
func (s *Service) recordPublishFailure(domainID uint, err error) {
	existing, findErr := s.stateStore.FindState(domainID)
	if findErr != nil {
		existing = &models.DNSPublicationState{
			UserID:      s.userIDFor(domainID),
			DomainID:    domainID,
			DriftStatus: models.DNSDriftUnknown,
		}
		if ws := s.workspaceFor(domainID); ws != nil {
			existing.WorkspaceID = ws
		}
	}
	existing.PublishError = err.Error()
	if uerr := s.stateStore.UpsertState(existing); uerr != nil {
		logger.Error("dnsmanager: record publish failure", "domain_id", domainID, "error", uerr)
	}
}

// userIDFor/workspaceFor fill the ownership columns of a fresh state row; the
// domain row is the parent (ADR-008 decision 1: ownership resolves through
// the strict parent chain domain → workspace).
func (s *Service) userIDFor(domainID uint) uint {
	if d, err := s.domainRepo.FindByID(domainID); err == nil {
		return d.UserID
	}
	return 0
}

func (s *Service) workspaceFor(domainID uint) *uint {
	if d, err := s.domainRepo.FindByID(domainID); err == nil {
		return d.WorkspaceID
	}
	return nil
}

// ---- Status / drift surface ----

// CredentialView is the masked credential view API responses carry. The raw
// token material NEVER appears — only the provider, a short fingerprint, and
// the last successful access check.
type CredentialView struct {
	Provider       string     `json:"provider"`
	Masked         string     `json:"masked"`
	Zone           string     `json:"zone,omitempty"`
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// statusViewOf masks a credential row for an API response.
func statusViewOf(row *models.DNSProviderCredential) *CredentialView {
	cred, err := decryptCredential(row.Credential)
	if err != nil {
		// An unreadable credential must not leak anything: mask to a fixed
		// fingerprint and surface through LastVerifiedAt == nil.
		return &CredentialView{
			Provider:  row.Provider,
			Masked:    "…unreadable…",
			Zone:      row.ZoneID,
			CreatedAt: row.CreatedAt,
		}
	}
	return &CredentialView{
		Provider:       row.Provider,
		Masked:         MaskCredential(cred),
		Zone:           row.ZoneID,
		LastVerifiedAt: row.LastVerifiedAt,
		CreatedAt:      row.CreatedAt,
	}
}

// MaskCredential builds the masked fingerprint shown in API responses: the
// credential's first four characters, an ellipsis, and its last four — never
// the whole secret. A too-short secret masks entirely.
func MaskCredential(cred Credential) string {
	secret := cred.Token
	if secret == "" {
		secret = cred.AccessKeyID
	}
	if secret == "" {
		return "…"
	}
	runes := []rune(secret)
	if len(runes) <= 8 {
		return strings.Repeat("•", len(runes))
	}
	return string(runes[:4]) + "…" + string(runes[len(runes)-4:])
}

// StatusView is GET .../dns-provider's payload: the masked connection, the
// current plan, and the publication/drift state.
type StatusView struct {
	Connected      *CredentialView        `json:"connected,omitempty"`
	Plan           []PlannedRecord        `json:"plan"`
	Publication    *PublicationView       `json:"publication,omitempty"`
	Drift          *DriftStatusView       `json:"drift,omitempty"`
}

// PublicationView mirrors the stored publication state.
type PublicationView struct {
	PublishedAt *time.Time `json:"published_at,omitempty"`
	PublishError string    `json:"publish_error,omitempty"`
	RecordCount int        `json:"record_count"`
}

// DriftStatusView is the drift health surface (GET .../dns-drift and the
// cron's output shape).
type DriftStatusView struct {
	Status           string        `json:"status"` // unknown | ok | drift
	Findings         []DriftFinding `json:"findings,omitempty"`
	LastCheckedAt    *time.Time    `json:"last_checked_at,omitempty"`
	PublishedAt      *time.Time    `json:"published_at,omitempty"`
	Connected        bool          `json:"connected"`
}

// DriftFinding is one record whose live state diverges from the plan.
type DriftFinding struct {
	Kind     string `json:"kind"` // missing | changed
	Record   string `json:"record"` // type + name of the planned record
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

// Status assembles the StatusView from stored state — no provider traffic.
func (s *Service) Status(d *models.Domain) (*StatusView, error) {
	view := &StatusView{}
	plan, err := s.PlanRecords(d)
	if err == nil {
		view.Plan = plan
	}
	if credRow, err := s.repo.FindCredential(d.ID); err == nil {
		view.Connected = statusViewOf(credRow)
	}
	if st, err := s.stateStore.FindState(d.ID); err == nil {
		view.Publication = publicationViewOf(st)
		view.Drift = driftViewOf(st)
	}
	return view, nil
}

func publicationViewOf(st *models.DNSPublicationState) *PublicationView {
	specs, _ := unmarshalSpecs(st.PublishedRecords)
	return &PublicationView{
		PublishedAt:  st.PublishedAt,
		PublishError: st.PublishError,
		RecordCount:  len(specs),
	}
}

func driftViewOf(st *models.DNSPublicationState) *DriftStatusView {
	v := &DriftStatusView{
		Status:        st.DriftStatus,
		LastCheckedAt: st.LastDriftCheckAt,
		PublishedAt:   st.PublishedAt,
	}
	var findings []DriftFinding
	if st.DriftDetail != "" && json.Unmarshal([]byte(st.DriftDetail), &findings) == nil {
		v.Findings = findings
	}
	return v
}

// DriftStatus returns the stored drift view (no provider traffic).
func (s *Service) DriftStatus(d *models.Domain) (*DriftStatusView, error) {
	connected := true
	if _, err := s.repo.FindCredential(d.ID); err != nil {
		connected = false
	}
	if st, err := s.stateStore.FindState(d.ID); err == nil {
		v := driftViewOf(st)
		v.Connected = connected
		return v, nil
	}
	return &DriftStatusView{Status: models.DNSDriftUnknown, Connected: connected}, nil
}

// ---- Drift detection ----

// CheckDrift compares the domain's live provider records against the planned
// set (the baseline the last publish wrote) and persists the verdict:
//
//	ok      — every planned record matches live;
//	drift   — at least one planned record is missing or changed;
//	unknown — never published through the automation layer, or no provider
//	          connection (nothing to compare against).
//
// A transition INTO drift (or out of it) records an audit event, so the
// operator's trail shows when a zone stopped matching the plan. Providers
// failing mid-check leave the stored status untouched (a provider outage is
// not drift).
func (s *Service) CheckDrift(ctx context.Context, d *models.Domain) (*DriftStatusView, error) {
	credRow, err := s.repo.FindCredential(d.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &DriftStatusView{Status: models.DNSDriftUnknown, Connected: false}, nil
		}
		return nil, err
	}
	st, err := s.stateStore.FindState(d.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &DriftStatusView{Status: models.DNSDriftUnknown, Connected: true}, nil
		}
		return nil, err
	}
	baseline, err := unmarshalSpecs(st.PublishedRecords)
	if err != nil || len(baseline) == 0 {
		return driftViewOf(st), nil // nothing published — nothing to compare
	}

	provider, _, err := s.providerFor(credRow)
	if err != nil {
		return nil, err
	}
	live, err := provider.ReadRecords(ctx, d.Domain)
	if err != nil {
		// A provider outage is not drift — leave the stored verdict alone.
		return nil, err
	}

	findings := driftFindings(baseline, live)
	now := s.now()
	previous := st.DriftStatus
	newStatus := models.DNSDriftOK
	if len(findings) > 0 {
		newStatus = models.DNSDriftFound
	}
	st.DriftStatus = newStatus
	st.DriftDetail = marshalFindings(findings)
	st.LastDriftCheckAt = &now
	if err := s.stateStore.UpsertState(st); err != nil {
		return nil, fmt.Errorf("dnsmanager: persist drift state: %w", err)
	}

	switch {
	case previous != models.DNSDriftFound && newStatus == models.DNSDriftFound:
		s.alert(d.WorkspaceID,
			"domain.dns_drift_detected",
			"DNS drift detected: live records diverge from the published plan",
			map[string]any{
				"domain_id": d.ID,
				"domain":    d.Domain,
				"provider":  credRow.Provider,
				"findings":  len(findings),
			})
	case previous == models.DNSDriftFound && newStatus == models.DNSDriftOK:
		s.alert(d.WorkspaceID,
			"domain.dns_drift_resolved",
			"DNS drift resolved: live records match the published plan again",
			map[string]any{
				"domain_id": d.ID,
				"domain":    d.Domain,
				"provider":  credRow.Provider,
			})
	}

	return driftViewOf(st), nil
}

// driftFindings diffs the planned baseline against the live records: a
// planned record missing live is "missing"; a planned record present with a
// different value is "changed". Unrelated live records (anything the plan
// does not manage) are NOT drift — the automation owns its records, not the
// zone.
func driftFindings(baseline, live []DNSRecordSpec) []DriftFinding {
	liveByKey := make(map[recordKey][]DNSRecordSpec, len(live))
	for _, l := range live {
		k := l.key()
		liveByKey[k] = append(liveByKey[k], l)
	}
	findings := make([]DriftFinding, 0, 4)
	for _, spec := range baseline {
		candidates := liveByKey[spec.key()]
		matched := false
		for _, c := range candidates {
			if recordMatches(c, spec) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		f := DriftFinding{
			Record:   spec.Type + " " + normalizeName(spec.Name),
			Expected: spec.Value,
		}
		if len(candidates) == 0 {
			f.Kind = "missing"
		} else {
			f.Kind = "changed"
			f.Actual = candidates[0].Value
		}
		findings = append(findings, f)
	}
	return findings
}

// ---- credential codec ----

func credentialOf(req ConnectRequest) Credential {
	return Credential{
		Token:           req.Token,
		AccessKeyID:     req.AccessKeyID,
		SecretAccessKey: req.SecretAccessKey,
		SessionToken:    req.SessionToken,
		Region:          req.Region,
	}
}

// encryptCredential serializes and encrypts the credential envelope with the
// at-rest key (SF-78 discipline: never stored unprotected).
func encryptCredential(cred Credential) (string, error) {
	raw, err := json.Marshal(cred)
	if err != nil {
		return "", err
	}
	enc, err := cryptoEncrypt(string(raw))
	if err != nil {
		return "", fmt.Errorf("dnsmanager: encrypt provider credential: %w", err)
	}
	return enc, nil
}

func decryptCredential(stored string) (Credential, error) {
	var cred Credential
	plain, err := cryptoDecrypt(stored)
	if err != nil {
		return cred, err
	}
	if err := json.Unmarshal([]byte(plain), &cred); err != nil {
		return cred, fmt.Errorf("dnsmanager: credential envelope decode: %w", err)
	}
	return cred, nil
}

// marshalSpecs / unmarshalSpecs carry the publication baseline as JSON.
func marshalSpecs(specs []DNSRecordSpec) string {
	raw, err := json.Marshal(specs)
	if err != nil {
		return ""
	}
	return string(raw)
}

func unmarshalSpecs(raw string) ([]DNSRecordSpec, error) {
	if raw == "" {
		return nil, nil
	}
	var specs []DNSRecordSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func marshalFindings(findings []DriftFinding) string {
	if len(findings) == 0 {
		return ""
	}
	raw, err := json.Marshal(findings)
	if err != nil {
		return ""
	}
	return string(raw)
}

func ptrTime(t time.Time) *time.Time { return &t }

// ---- crypto seam ----
// encryptCredential/decryptCredential use internal/services/crypto directly
// (AES-256-GCM at rest, SF-78). Tests configure the key with crypto.Init.

func cryptoEncrypt(plain string) (string, error) {
	return crypto.Encrypt(plain)
}

func cryptoDecrypt(stored string) (string, error) {
	return crypto.Decrypt(stored)
}
