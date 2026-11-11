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
	"strings"
)

// digitalOceanDefaultBase is the DigitalOcean API v2 root.
const digitalOceanDefaultBase = "https://api.digitalocean.com/v2"

// digitalOceanPage is the record list page size.
const digitalOceanPage = 200

// DigitalOceanProvider publishes records through the DigitalOcean API v2
// (/v2/domains) with a personal access token (Authorization: Bearer). The
// token needs the `domain:read` + `domain:edit` scopes on the account that
// owns the zone; a token without them fails the pre-write read with
// ErrUnauthorized (ADR-007 decision 2 — no partial-zone writes).
type DigitalOceanProvider struct {
	token string
	base  string
	do    func(*http.Request) (*http.Response, error)
}

func NewDigitalOceanProvider(token, base string) *DigitalOceanProvider {
	return &DigitalOceanProvider{token: token, base: strings.TrimRight(base, "/"), do: httpClient.Do}
}

func (p *DigitalOceanProvider) Kind() string { return modelsProviderDigitalOcean }

// doError maps a failed DigitalOcean exchange onto the sentinels. DigitalOcean
// reports {"id": "unauthorized" | "not_found" | "throttled", "message": ...}
// with a matching HTTP status; the status is the primary classifier.
func doError(status int, code, msg string) error {
	err := ErrorOf(status, code)
	if msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// doJSON performs one authenticated call. DigitalOcean's envelope differs per
// endpoint, so the raw body is returned for the caller to decode.
func (p *DigitalOceanProvider) doJSON(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return nil, doError(resp.StatusCode, e.ID, e.Message)
	}
	return json.RawMessage(raw), nil
}

// verifyZone checks the domain exists (and the token can see it) with the
// domain GET — the read-only validation connect and publish run first.
func (p *DigitalOceanProvider) verifyZone(ctx context.Context, zone string) error {
	_, err := p.doJSON(ctx, http.MethodGet, "/domains/"+zone, nil)
	if err != nil {
		// DigitalOcean answers 404 for both "no such domain" and "domain in
		// another account"; report the distinction the token implies.
		return err
	}
	return nil
}

// doRecord mirrors one domain_records row. DigitalOcean uses relative names
// with "@" for the apex.
type doRecord struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Data     string `json:"data"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
}

// fqdnName expands a DO relative name to the absolute FQDN the spec carries.
func (p *DigitalOceanProvider) fqdnName(zone, name string) string {
	name = strings.TrimPrefix(name, "@")
	if name == "" {
		return normalizeName(zone)
	}
	return normalizeName(name) + "." + normalizeName(zone)
}

// relativeName converts the spec's FQDN to the DO relative form.
func relativeName(zone, fqdn string) string {
	f := normalizeName(fqdn)
	z := normalizeName(zone)
	if f == z {
		return "@"
	}
	return strings.TrimSuffix(f, "."+z)
}

func (p *DigitalOceanProvider) ReadRecords(ctx context.Context, zone string) ([]DNSRecordSpec, error) {
	if err := p.verifyZone(ctx, zone); err != nil {
		return nil, err
	}
	out := make([]DNSRecordSpec, 0, 16)
	for page := 1; ; page++ {
		raw, err := p.doJSON(ctx, http.MethodGet,
			fmt.Sprintf("/domains/%s/records?per_page=%d&page=%d", zone, digitalOceanPage, page), nil)
		if err != nil {
			return nil, err
		}
		var body struct {
			Records []doRecord `json:"domain_records"`
			Links   struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
			Meta struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("dnsmanager: digitalocean record list decode: %w", err)
		}
		for _, r := range body.Records {
			spec := DNSRecordSpec{Type: r.Type, Value: r.Data, TTL: r.TTL, Priority: r.Priority}
			spec.Name = p.fqdnName(zone, r.Name)
			out = append(out, spec)
		}
		if len(body.Records) < digitalOceanPage && body.Links.Pages.Next == "" {
			return out, nil
		}
	}
}

// PublishRecordSet upserts the set, reading the zone's records first so a
// mis-scoped token fails before any write.
func (p *DigitalOceanProvider) PublishRecordSet(ctx context.Context, zone string, records []DNSRecordSpec) error {
	if err := p.verifyZone(ctx, zone); err != nil {
		return &PublishError{Zone: zone, Cause: err}
	}
	existing, err := p.listRecords(ctx, zone)
	if err != nil {
		return &PublishError{Zone: zone, Cause: err}
	}
	byKey := make(map[recordKey]doRecord, len(existing))
	for _, r := range existing {
		spec := DNSRecordSpec{Type: r.Type, Value: r.Data, TTL: r.TTL, Priority: r.Priority}
		spec.Name = p.fqdnName(zone, r.Name)
		byKey[spec.key()] = r
	}

	applied := make([]DNSRecordSpec, 0, len(records))
	for _, spec := range records {
		payload := map[string]any{
			"type": spec.Type,
			"name": relativeName(zone, spec.Name),
			"data": spec.Value,
			"ttl":  ttlOr(spec.TTL, 300),
		}
		if spec.Type == "MX" {
			payload["priority"] = mxPriorityOr(spec.Priority)
		}
		var err error
		if prev, ok := byKey[spec.key()]; ok {
			err = p.putRecord(ctx, zone, prev.ID, payload)
		} else {
			err = p.postRecord(ctx, zone, payload)
		}
		if err != nil {
			return &PublishError{Zone: zone, Applied: applied, Failed: spec, Cause: err}
		}
		applied = append(applied, spec)
	}
	return nil
}

func (p *DigitalOceanProvider) listRecords(ctx context.Context, zone string) ([]doRecord, error) {
	var out []doRecord
	for page := 1; ; page++ {
		raw, err := p.doJSON(ctx, http.MethodGet,
			fmt.Sprintf("/domains/%s/records?per_page=%d&page=%d", zone, digitalOceanPage, page), nil)
		if err != nil {
			return nil, err
		}
		var body struct {
			Records []doRecord `json:"domain_records"`
			Links   struct {
				Pages struct {
					Next string `json:"next"`
				} `json:"pages"`
			} `json:"links"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("dnsmanager: digitalocean record list decode: %w", err)
		}
		out = append(out, body.Records...)
		if len(body.Records) < digitalOceanPage && body.Links.Pages.Next == "" {
			return out, nil
		}
	}
}

func (p *DigitalOceanProvider) postRecord(ctx context.Context, zone string, payload map[string]any) error {
	_, err := p.doJSON(ctx, http.MethodPost, "/domains/"+zone+"/records", payload)
	return err
}

func (p *DigitalOceanProvider) putRecord(ctx context.Context, zone string, recordID int, payload map[string]any) error {
	_, err := p.doJSON(ctx, http.MethodPut, fmt.Sprintf("/domains/%s/records/%d", zone, recordID), payload)
	return err
}

// DeleteRecord removes the record matching (type, name, value).
func (p *DigitalOceanProvider) DeleteRecord(ctx context.Context, zone string, rec DNSRecordSpec) error {
	if err := p.verifyZone(ctx, zone); err != nil {
		return err
	}
	existing, err := p.listRecords(ctx, zone)
	if err != nil {
		return err
	}
	for _, r := range existing {
		spec := DNSRecordSpec{Type: r.Type, Value: r.Data, TTL: r.TTL, Priority: r.Priority}
		spec.Name = p.fqdnName(zone, r.Name)
		if recordMatches(spec, rec) {
			_, err := p.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/domains/%s/records/%d", zone, r.ID), nil)
			return err
		}
	}
	return nil
}
