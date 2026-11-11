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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// cloudflareDefaultBase is the Cloudflare REST API v4 root.
const cloudflareDefaultBase = "https://api.cloudflare.com/client/v4"

// cloudflarePage is the list page size used for zone record pagination.
const cloudflarePage = 100

// CloudflareProvider publishes records through the Cloudflare REST API v4
// with a scoped API token (Authorization: Bearer). The token the operator
// supplies must have Zone:Read + DNS:Edit on the domain's zone — anything
// narrower fails the pre-write access check with ErrUnauthorized, so a
// mis-scoped token never writes a partial record set (ADR-007 decision 2).
type CloudflareProvider struct {
	token string
	base  string
	// do is the HTTP executor; a field so tests inject behavior-free
	// transports without a listener (the fake servers cover the full path).
	do func(*http.Request) (*http.Response, error)
}

func NewCloudflareProvider(token, base string) *CloudflareProvider {
	return &CloudflareProvider{token: token, base: strings.TrimRight(base, "/"), do: httpClient.Do}
}

func (p *CloudflareProvider) Kind() string { return modelsProviderCloudflare }

// cfEnvelope is the common Cloudflare v4 response wrapper.
type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// cfError turns a failed Cloudflare exchange onto the sentinel errors.
func cfError(status int, env *cfEnvelope) error {
	code := ""
	msg := ""
	if env != nil && len(env.Errors) > 0 {
		msg = env.Errors[0].Message
		// Cloudflare auth failures surface as 9109 (invalid authorization) and
		// 10000 (authentication error); zone-missing as 7003/7101/7103.
		switch env.Errors[0].Code {
		case 9109, 10000:
			return fmt.Errorf("%w: %s", ErrUnauthorized, msg)
		case 7003, 7101, 7103:
			return fmt.Errorf("%w: %s", ErrZoneNotFound, msg)
		}
		code = fmt.Sprintf("%d", env.Errors[0].Code)
	}
	classified := ErrorOf(status, code)
	if msg != "" {
		return fmt.Errorf("%w: %s", classified, msg)
	}
	return classified
}

// doJSON performs a request and decodes the v4 envelope. A non-2xx status or
// success:false fails with the classified error.
func (p *CloudflareProvider) doJSON(ctx context.Context, method, path string, body any) (*cfEnvelope, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %v", ErrProviderUnavailable, err)
	}
	var env cfEnvelope
	if uerr := json.Unmarshal(raw, &env); uerr != nil {
		// A body that will not parse still carries the status classification.
		return nil, cfError(resp.StatusCode, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		return nil, cfError(resp.StatusCode, &env)
	}
	return &env, nil
}

// resolveZone finds the zone's provider id by exact name match. A token that
// cannot see the zone (wrong account, mis-scoped) returns no match — which is
// the ErrUnauthorized/ErrZoneNotFound boundary before any write.
func (p *CloudflareProvider) resolveZone(ctx context.Context, zone string) (string, error) {
	q := url.Values{}
	q.Set("name", zone)
	q.Set("per_page", "20")
	env, err := p.doJSON(ctx, http.MethodGet, "/zones?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	var zones []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(env.Result, &zones); err != nil {
		return "", fmt.Errorf("dnsmanager: cloudflare zone list decode: %w", err)
	}
	for _, z := range zones {
		if normalizeName(z.Name) == normalizeName(zone) {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("%w: token has no %s zone", ErrZoneNotFound, zone)
}

// cfRecord mirrors one dns_records row.
type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	// Priority appears for MX/SRV records.
	Priority int `json:"priority"`
}

// toSpec converts a Cloudflare row to the provider-neutral spec.
func (r cfRecord) toSpec() DNSRecordSpec {
	return DNSRecordSpec{Type: r.Type, Name: normalizeName(r.Name), Value: r.Content, TTL: r.TTL, Priority: r.Priority}
}

// key is the record identity the upsert map matches on.
func (r cfRecord) key() recordKey {
	return recordKey{typ: r.Type, name: normalizeName(r.Name)}
}

func (p *CloudflareProvider) ReadRecords(ctx context.Context, zone string) ([]DNSRecordSpec, error) {
	zoneID, err := p.resolveZone(ctx, zone)
	if err != nil {
		return nil, err
	}
	out := make([]DNSRecordSpec, 0, 16)
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("per_page", fmt.Sprintf("%d", cloudflarePage))
		q.Set("page", fmt.Sprintf("%d", page))
		env, err := p.doJSON(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var recs []cfRecord
		if err := json.Unmarshal(env.Result, &recs); err != nil {
			return nil, fmt.Errorf("dnsmanager: cloudflare record list decode: %w", err)
		}
		for _, r := range recs {
			out = append(out, r.toSpec())
		}
		if len(recs) < cloudflarePage {
			return out, nil
		}
	}
}

// PublishRecordSet upserts the set. It reads the zone's records FIRST: the
// read validates the credential and zone scope, so a mis-scoped token fails
// before any write (no partial-zone writes). Existing records identified by
// (type, name) are updated (PUT); the rest are created (POST).
func (p *CloudflareProvider) PublishRecordSet(ctx context.Context, zone string, records []DNSRecordSpec) error {
	zoneID, err := p.resolveZone(ctx, zone)
	if err != nil {
		return &PublishError{Zone: zone, Cause: err}
	}
	// Pre-write access validation + existing-record map for the upsert.
	existing, err := p.listRecords(ctx, zoneID)
	if err != nil {
		return &PublishError{Zone: zone, Cause: err}
	}
	byKey := make(map[recordKey]cfRecord, len(existing))
	for _, r := range existing {
		byKey[r.key()] = r
	}

	applied := make([]DNSRecordSpec, 0, len(records))
	for _, spec := range records {
		payload := map[string]any{
			"type":    spec.Type,
			"name":    spec.Name,
			"content": spec.Value,
			"ttl":     ttlOr(spec.TTL, 300),
		}
		if spec.Type == "MX" {
			payload["priority"] = mxPriorityOr(spec.Priority)
		}
		var err error
		if prev, ok := byKey[spec.key()]; ok {
			err = p.putRecord(ctx, zoneID, prev.ID, payload)
		} else {
			err = p.postRecord(ctx, zoneID, payload)
		}
		if err != nil {
			return &PublishError{Zone: zone, Applied: applied, Failed: spec, Cause: err}
		}
		applied = append(applied, spec)
	}
	return nil
}

func (p *CloudflareProvider) listRecords(ctx context.Context, zoneID string) ([]cfRecord, error) {
	var out []cfRecord
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("per_page", fmt.Sprintf("%d", cloudflarePage))
		q.Set("page", fmt.Sprintf("%d", page))
		env, err := p.doJSON(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var recs []cfRecord
		if err := json.Unmarshal(env.Result, &recs); err != nil {
			return nil, fmt.Errorf("dnsmanager: cloudflare record list decode: %w", err)
		}
		out = append(out, recs...)
		if len(recs) < cloudflarePage {
			return out, nil
		}
	}
}

func (p *CloudflareProvider) postRecord(ctx context.Context, zoneID string, payload map[string]any) error {
	_, err := p.doJSON(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", payload)
	return err
}

func (p *CloudflareProvider) putRecord(ctx context.Context, zoneID, recordID string, payload map[string]any) error {
	_, err := p.doJSON(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+recordID, payload)
	return err
}

// DeleteRecord removes the record matching (type, name, value). The zone read
// comes first for the same no-write-without-access discipline.
func (p *CloudflareProvider) DeleteRecord(ctx context.Context, zone string, rec DNSRecordSpec) error {
	zoneID, err := p.resolveZone(ctx, zone)
	if err != nil {
		return err
	}
	existing, err := p.listRecords(ctx, zoneID)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if recordMatches(r.toSpec(), rec) {
			_, err := p.doJSON(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+r.ID, nil)
			return err
		}
	}
	return nil // nothing matching — deletion is idempotent
}

func ttlOr(ttl, def int) int {
	if ttl > 0 {
		return ttl
	}
	return def
}

func mxPriorityOr(p int) int {
	if p > 0 {
		return p
	}
	return 10
}
