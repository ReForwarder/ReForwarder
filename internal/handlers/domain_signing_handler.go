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
	"fmt"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/jkaninda/okapi"
	"gorm.io/gorm"
)

// DomainSigningHandler manages the managed DKIM signing keys of one verified
// domain (roadmap R-02). Workspace-scoped: every endpoint resolves the domain
// through the caller's scope first, so a foreign domain answers 404.
//
// The private key is generated server-side, stored encrypted, and never
// returned: the only public artifact is the DNS TXT record to publish.
type DomainSigningHandler struct {
	keyRepo    *repositories.DomainSigningKeyRepository
	domainRepo *repositories.DomainRepository
	audit      *audit.Logger
}

func NewDomainSigningHandler(keyRepo *repositories.DomainSigningKeyRepository, domainRepo *repositories.DomainRepository, auditLogger *audit.Logger) *DomainSigningHandler {
	return &DomainSigningHandler{keyRepo: keyRepo, domainRepo: domainRepo, audit: auditLogger}
}

// CreateSigningKeyRequest is the body of POST /domains/{id}/signing-keys.
type CreateSigningKeyRequest struct {
	ID   uint `param:"id"`
	Body struct {
		Selector  string `json:"selector" required:"true" doc:"DKIM selector, e.g. posta or mail2026. The TXT record is published at <selector>._domainkey.<domain>."`
		Algorithm string `json:"algorithm" enum:"rsa-sha256,ed25519-sha256" doc:"Signing algorithm. rsa-sha256 is the default and universally accepted; ed25519-sha256 is smaller but newer."`
	} `json:"body"`
}

// RotateSigningKeyRequest is the body of POST /domains/{id}/signing-keys/rotate.
type RotateSigningKeyRequest struct {
	ID   uint `param:"id"`
	Body struct {
		Selector  string `json:"selector" required:"true" doc:"Selector of the replacement key. Must differ from the current one."`
		Algorithm string `json:"algorithm" enum:"rsa-sha256,ed25519-sha256"`
	} `json:"body"`
}

// GetSigningKeysRequest is the request of GET /domains/{id}/signing-keys.
type GetSigningKeysRequest struct {
	ID uint `param:"id"`
}

// SigningKeyActionRequest names one key of one domain for verify-dns/delete.
type SigningKeyActionRequest struct {
	ID    uint `param:"id"`
	KeyID uint `param:"keyId"`
}

// DnsRecordView is the DNS record the operator must publish.
type DnsRecordView struct {
	Host  string `json:"host"`
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   int    `json:"ttl"`
}

// SigningKeyView is one managed signing key. The private key is never part of
// the wire shape — models.DomainSigningKey.PrivateKeyPem carries no JSON tag,
// and this view re-states exactly what is public.
type SigningKeyView struct {
	ID             uint           `json:"id"`
	DomainID       uint           `json:"domain_id"`
	Domain         string         `json:"domain,omitempty"`
	Selector       string         `json:"selector"`
	Algorithm      string         `json:"algorithm"`
	Status         string         `json:"status"`
	DNSState       string         `json:"dns_state"`
	DNSPublishedAt *time.Time     `json:"dns_published_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	DNSRecord      *DnsRecordView `json:"dns_record,omitempty"`
}

// signingKeyView maps a stored key; the DNS record is rendered on demand.
func signingKeyView(k *models.DomainSigningKey, domainName, dnsValue string) SigningKeyView {
	v := SigningKeyView{
		ID:        k.ID,
		DomainID:  k.DomainID,
		Domain:    domainName,
		Selector:  k.Selector,
		Algorithm: k.Algorithm,
		Status:    k.Status,
		DNSState:  k.DNSState,
	}
	v.DNSPublishedAt = k.DNSPublishedAt
	v.CreatedAt = k.CreatedAt
	if dnsValue != "" {
		v.DNSRecord = &DnsRecordView{
			Host:  k.DNSHost(domainName),
			Type:  "TXT",
			Value: dnsValue,
			TTL:   3600,
		}
	}
	return v
}

// resolveDomain loads the domain and enforces scope ownership; a foreign or
// missing domain is indistinguishable (404).
func (h *DomainSigningHandler) resolveDomain(c *okapi.Context, domainID uint) (*models.Domain, error) {
	d, err := h.domainRepo.FindByID(domainID)
	if err != nil || !ownsResource(c, d.UserID, d.WorkspaceID) {
		return nil, gorm.ErrRecordNotFound
	}
	return d, nil
}

func (h *DomainSigningHandler) auditLog(c *okapi.Context, action, message string) {
	if h.audit != nil {
		h.audit.LogCtx(c, action, message, nil)
	}
}

// Create generates a key pair for the domain and returns it with the DNS TXT
// record to publish. Refused when an Active key already exists — use rotate.
func (h *DomainSigningHandler) Create(c *okapi.Context, req *CreateSigningKeyRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}

	algorithm := req.Body.Algorithm
	if algorithm == "" {
		algorithm = models.SigningAlgorithmRSA
	}
	if !email.ValidSigningAlgorithm(algorithm) {
		return c.AbortBadRequest("unsupported algorithm: use rsa-sha256 or ed25519-sha256")
	}
	if !email.ValidSelector(req.Body.Selector) {
		return c.AbortBadRequest("invalid selector: use letters, digits, dots, hyphens or underscores (max 63 per label)")
	}

	if _, err := h.keyRepo.FindActiveForDomainID(d.ID); err == nil {
		return c.AbortConflict("domain already has an active signing key; use the rotate endpoint")
	}

	encryptedPem, err := email.GenerateSigningKeyPem(algorithm)
	if err != nil {
		return c.AbortInternalServerError("failed to generate signing key")
	}

	k := &models.DomainSigningKey{
		UserID:        d.UserID,
		WorkspaceID:   d.WorkspaceID,
		DomainID:      d.ID,
		Selector:      req.Body.Selector,
		Algorithm:     algorithm,
		PrivateKeyPem: encryptedPem,
		Status:        models.DomainSigningStatusActive,
		DNSState:      models.DomainSigningDNSPending,
	}
	if err := h.keyRepo.Create(k); err != nil {
		return c.AbortConflict("failed to store signing key")
	}

	record, err := email.DnsTXTRecord(encryptedPem, algorithm)
	if err != nil {
		// The key was stored; surfacing its DNS record must not fail the call.
		record = ""
	}

	h.auditLog(c, "domain.signing_key.created",
		fmt.Sprintf("DKIM signing key created for %s (selector %s, %s)", d.Domain, k.Selector, algorithm))
	return created(c, signingKeyView(k, d.Domain, record))
}

// List returns the domain's keys with their DNS records — no private material.
func (h *DomainSigningHandler) List(c *okapi.Context, req *GetSigningKeysRequest) error {
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}

	keys, err := h.keyRepo.FindByDomainID(d.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to list signing keys")
	}

	out := make([]SigningKeyView, 0, len(keys))
	for i := range keys {
		record, rerr := email.DnsTXTRecord(keys[i].PrivateKeyPem, keys[i].Algorithm)
		if rerr != nil {
			record = ""
		}
		out = append(out, signingKeyView(&keys[i], d.Domain, record))
	}
	return ok(c, out)
}

// Rotate creates a replacement Active key (new selector) and demotes the
// current Active key to Retiring in one transaction. The retiring key keeps
// verifying old mail in transit; retire it (and delete it) once receivers no
// longer see it — the operator decides, no cron is involved.
func (h *DomainSigningHandler) Rotate(c *okapi.Context, req *RotateSigningKeyRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}

	algorithm := req.Body.Algorithm
	if algorithm == "" {
		algorithm = models.SigningAlgorithmRSA
	}
	if !email.ValidSigningAlgorithm(algorithm) {
		return c.AbortBadRequest("unsupported algorithm: use rsa-sha256 or ed25519-sha256")
	}
	if !email.ValidSelector(req.Body.Selector) {
		return c.AbortBadRequest("invalid selector: use letters, digits, dots, hyphens or underscores (max 63 per label)")
	}
	current, err := h.keyRepo.FindActiveForDomainID(d.ID)
	if err != nil {
		return c.AbortConflict("no active signing key to rotate; generate one first")
	}
	if req.Body.Selector == current.Selector {
		return c.AbortBadRequest("replacement selector must differ from the current one")
	}

	encryptedPem, err := email.GenerateSigningKeyPem(algorithm)
	if err != nil {
		return c.AbortInternalServerError("failed to generate signing key")
	}

	next := &models.DomainSigningKey{
		UserID:        d.UserID,
		WorkspaceID:   d.WorkspaceID,
		DomainID:      d.ID,
		Selector:      req.Body.Selector,
		Algorithm:     algorithm,
		PrivateKeyPem: encryptedPem,
		Status:        models.DomainSigningStatusActive,
		DNSState:      models.DomainSigningDNSPending,
	}
	if err := h.keyRepo.RotateActive(d.ID, next); err != nil {
		return c.AbortConflict("failed to rotate signing key")
	}

	record, err := email.DnsTXTRecord(encryptedPem, algorithm)
	if err != nil {
		record = ""
	}

	h.auditLog(c, "domain.signing_key.rotated",
		fmt.Sprintf("DKIM key for %s rotated: %s → %s (old key now retiring)", d.Domain, current.Selector, next.Selector))
	return created(c, signingKeyView(next, d.Domain, record))
}

// VerifyDNS re-checks whether the key's TXT record is live in DNS and records
// the publication state (published + timestamp, or pending-retry).
func (h *DomainSigningHandler) VerifyDNS(c *okapi.Context, req *SigningKeyActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	k, err := h.keyRepo.FindByID(req.KeyID)
	if err != nil || k.DomainID != d.ID {
		return c.AbortNotFound("signing key not found")
	}

	record, verr := email.VerifyDNSPublication(d.Domain, k.Selector, k.Algorithm)
	if verr != nil {
		k.DNSState = models.DomainSigningDNSPending
		_ = h.keyRepo.Update(k)
		return ok(c, okapi.M{
			"published": false,
			"dns_state": k.DNSState,
			"error":     verr.Error(),
			"key":       signingKeyView(k, d.Domain, record),
		})
	}

	k.DNSState = models.DomainSigningDNSPublished
	now := time.Now()
	k.DNSPublishedAt = &now
	if err := h.keyRepo.Update(k); err != nil {
		return c.AbortInternalServerError("failed to record DNS publication")
	}
	h.auditLog(c, "domain.signing_key.dns_published",
		fmt.Sprintf("DKIM TXT record for %s (selector %s) confirmed in DNS", d.Domain, k.Selector))
	return ok(c, okapi.M{
		"published": true,
		"dns_state": k.DNSState,
		"record":    record,
		"key":       signingKeyView(k, d.Domain, record),
	})
}

// Delete removes a key. Only a Retired key is deletable: an Active key signs
// mail, and a Retiring key is still verifying messages signed before rotation.
func (h *DomainSigningHandler) Delete(c *okapi.Context, req *SigningKeyActionRequest) error {
	if err := requireEdit(c); err != nil {
		return c.AbortForbidden("insufficient workspace permissions", err)
	}
	d, err := h.resolveDomain(c, req.ID)
	if err != nil {
		return c.AbortNotFound("domain not found")
	}
	k, err := h.keyRepo.FindByID(req.KeyID)
	if err != nil || k.DomainID != d.ID {
		return c.AbortNotFound("signing key not found")
	}
	if k.Status != models.DomainSigningStatusRetired {
		return c.AbortConflict("only a retired key can be deleted; retire it after its replacement is published and verified")
	}
	if err := h.keyRepo.Delete(k.ID); err != nil {
		return c.AbortInternalServerError("failed to delete signing key")
	}
	h.auditLog(c, "domain.signing_key.deleted",
		fmt.Sprintf("DKIM signing key for %s deleted (selector %s)", d.Domain, k.Selector))
	return noContent(c)
}
