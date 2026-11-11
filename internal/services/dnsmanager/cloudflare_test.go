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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// errorsIs / errorsAs are tiny local shorthands for the sentinel checks.
func errorsIs(err, target error) bool { return errors.Is(err, target) }

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// cfFake is a fake Cloudflare API v4 server: a token gate, one zone, and a
// record store the assertions read after each publish. It reproduces the
// shapes the real API speaks (the envelope with success/errors/result, zone
// resolution by name, per-record POST/PUT/DELETE).
type cfFake struct {
	mu     sync.Mutex
	token  string
	zoneID string
	zone   string
	records []cfRecord
	seq    int
	// failStatus/failBody make every subsequent request answer with the given
	// status (0 = behave normally). Reset with clearFailure.
	failStatus int
	failBody   string
}

func newCfFake(t *testing.T) (*cfFake, string) {
	t.Helper()
	f := &cfFake{token: "cf-good-token", zoneID: "zone-1", zone: "example.com"}
	f.records = []cfRecord{f.mk("A", "example.com", "1.2.3.4", 1)}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *cfFake) mk(typ, name, content string, ttl int) cfRecord {
	f.seq++
	return cfRecord{ID: fmt.Sprintf("rec-%d", f.seq), Type: typ, Name: name, Content: content, TTL: ttl}
}

func (f *cfFake) fail(status int, body string) {
	f.mu.Lock()
	f.failStatus, f.failBody = status, body
	f.mu.Unlock()
}

func (f *cfFake) clearFailure() {
	f.mu.Lock()
	f.failStatus = 0
	f.mu.Unlock()
}

func (f *cfFake) envelope(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": status >= 200 && status < 300,
		"errors":  []any{},
		"result":  result,
	})
}

func (f *cfFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if got := r.Header.Get("Authorization"); got != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 9109, "message": "Invalid authorization token"}},
		})
		return
	}
	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		_, _ = w.Write([]byte(f.failBody))
		return
	}

	path := r.URL.Path
	switch {
	case path == "/zones" && r.Method == http.MethodGet:
		if r.URL.Query().Get("name") != f.zone {
			f.envelope(w, http.StatusOK, []cfRecord{})
			return
		}
		f.envelope(w, http.StatusOK, []map[string]any{{
			"id": f.zoneID, "name": f.zone, "status": "active",
		}})
	case strings.HasPrefix(path, "/zones/"+f.zoneID+"/dns_records"):
		f.serveRecords(w, r, strings.TrimPrefix(path, "/zones/"+f.zoneID+"/dns_records"))
	default:
		f.envelope(w, http.StatusNotFound, nil)
	}
}

func (f *cfFake) serveRecords(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case rest == "" && r.Method == http.MethodGet:
		f.envelope(w, http.StatusOK, f.records)
	case rest == "" && r.Method == http.MethodPost:
		var body struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
			TTL     int    `json:"ttl"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec := f.mk(body.Type, body.Name, body.Content, body.TTL)
		f.records = append(f.records, rec)
		f.envelope(w, http.StatusOK, rec)
	case strings.HasPrefix(rest, "/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(rest, "/")
		for i, rec := range f.records {
			if rec.ID == id {
				var body struct {
					Type    string `json:"type"`
					Name    string `json:"name"`
					Content string `json:"content"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.records[i].Content = body.Content
				f.envelope(w, http.StatusOK, f.records[i])
				return
			}
		}
		f.envelope(w, http.StatusNotFound, nil)
	case strings.HasPrefix(rest, "/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(rest, "/")
		for i, rec := range f.records {
			if rec.ID == id {
				f.records = append(f.records[:i], f.records[i+1:]...)
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]string{"id": id}})
				return
			}
		}
		f.envelope(w, http.StatusNotFound, nil)
	default:
		f.envelope(w, http.StatusNotFound, nil)
	}
}

func (f *cfFake) live(t *testing.T) []DNSRecordSpec {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]DNSRecordSpec, 0, len(f.records))
	for _, r := range f.records {
		out = append(out, r.toSpec())
	}
	return out
}

// Publish upserts the full set: existing records are updated in place (PUT)
// and new ones created (POST), preserving unrelated records.
func TestCloudflarePublishUpsertsRecords(t *testing.T) {
	fake, url := newCfFake(t)
	p := NewCloudflareProvider("cf-good-token", url)

	set := []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "posta-verification=tok-123", TTL: 300},
		{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300},
		{Type: "TXT", Name: "_dmarc.example.com", Value: "v=DMARC1; p=none", TTL: 300},
		{Type: "CNAME", Name: "posta._domainkey.example.com", Value: "posta._domainkey.posta", TTL: 300},
		{Type: "MX", Name: "example.com", Value: "mail.example.net", Priority: 10, TTL: 300},
	}
	if err := p.PublishRecordSet(context.Background(), "example.com", set); err != nil {
		t.Fatalf("publish: %v", err)
	}

	live := fake.live(t)
	for _, spec := range set {
		found := false
		for _, l := range live {
			if recordMatches(l, spec) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("published record %s %s = %q not found live among %+v", spec.Type, spec.Name, spec.Value, live)
		}
	}
	// The unrelated A record survived the publish.
	for _, l := range live {
		if l.Type == "A" && l.Value == "1.2.3.4" {
			return
		}
	}
	t.Fatal("unrelated A record was lost by the publish")
}

// A second publish UPDATES the existing records instead of duplicating them.
func TestCloudflarePublishIsIdempotent(t *testing.T) {
	fake, url := newCfFake(t)
	p := NewCloudflareProvider("cf-good-token", url)

	set := []DNSRecordSpec{{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300}}
	if err := p.PublishRecordSet(context.Background(), "example.com", set); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := p.PublishRecordSet(context.Background(), "example.com", set); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	count := 0
	for _, l := range fake.live(t) {
		if l.Type == "TXT" && normalizeName(l.Name) == "example.com" && strings.Contains(l.Value, "v=spf1") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("SPF record duplicated: %d live copies", count)
	}
}

// A mis-scoped token fails BEFORE any write (ADR-007 decision 2: no
// partial-zone writes) with ErrUnauthorized.
func TestCloudflareMisScopedTokenFailsBeforeWrites(t *testing.T) {
	fake, url := newCfFake(t)
	fake.token = "cf-other-token" // the presented token stops matching

	p := NewCloudflareProvider("cf-wrong-token", url)
	before := len(fake.live(t))
	err := p.PublishRecordSet(context.Background(), "example.com", []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "x"},
	})
	if err == nil {
		t.Fatal("publish with a rejected token must fail")
	}
	var pubErr *PublishError
	if !errorsAs(err, &pubErr) {
		t.Fatalf("error is not a *PublishError: %v", err)
	}
	if len(pubErr.Applied) != 0 {
		t.Fatalf("a rejected token must apply NOTHING, applied %d", len(pubErr.Applied))
	}
	if !errorsIs(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if len(fake.live(t)) != before {
		t.Fatal("the zone changed despite the rejected token")
	}
}

// Typed error classification: zone not found → ErrZoneNotFound.
func TestCloudflareZoneNotFoundIsTyped(t *testing.T) {
	_, url := newCfFake(t)
	p := NewCloudflareProvider("cf-good-token", url)
	_, err := p.ReadRecords(context.Background(), "other-domain.com")
	if !errorsIs(err, ErrZoneNotFound) {
		t.Fatalf("error = %v, want ErrZoneNotFound", err)
	}
}

// 429 from the provider classifies as ErrRateLimited.
func TestCloudflareRateLimitIsTyped(t *testing.T) {
	fake, url := newCfFake(t)
	fake.fail(http.StatusTooManyRequests, `{"success":false,"errors":[{"code":9108,"message":"ratelimited"}]}`)
	p := NewCloudflareProvider("cf-good-token", url)
	_, err := p.ReadRecords(context.Background(), "example.com")
	if !errorsIs(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

// A mid-publish failure is typed with the applied prefix — the operator sees
// exactly what landed.
func TestCloudflareMidPublishFailureCarriesApplied(t *testing.T) {
	fake, url := newCfFake(t)
	p := NewCloudflareProvider("cf-good-token", url)

	// Seed an existing _dmarc row so the second record takes the PUT (update)
	// path — the outage then kills that write, not the create.
	fake.mu.Lock()
	fake.records = append(fake.records, cfRecord{ID: "rec-dmarc", Type: "TXT", Name: "_dmarc.example.com", Content: "old"})
	fake.failStatus = 0
	fake.mu.Unlock()

	// Wrap: first record OK, then the outage kills the second write. Calls
	// 1-2 are the zone resolve + list reads, call 3 the first (successful)
	// write, call 4 the write the outage kills.
	inner := p.do
	calls := 0
	p.do = func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 3 && r.Method != http.MethodGet {
			return nil, fmt.Errorf("simulated outage")
		}
		return inner(r)
	}

	set := []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300},
		{Type: "TXT", Name: "_dmarc.example.com", Value: "v=DMARC1; p=reject", TTL: 300},
	}

	err := p.PublishRecordSet(context.Background(), "example.com", set)
	if err == nil {
		t.Fatal("mid-publish failure must surface")
	}
	var pubErr *PublishError
	if !errorsAs(err, &pubErr) {
		t.Fatalf("error is not a *PublishError: %v", err)
	}
	if len(pubErr.Applied) != 1 {
		t.Fatalf("applied = %d, want the 1 record that landed before the failure", len(pubErr.Applied))
	}
	if !errorsIs(err, ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
}

// DeleteRecord removes the matching record and is idempotent.
func TestCloudflareDeleteRecord(t *testing.T) {
	fake, url := newCfFake(t)
	p := NewCloudflareProvider("cf-good-token", url)

	rec := DNSRecordSpec{Type: "A", Name: "example.com", Value: "1.2.3.4"}
	if err := p.DeleteRecord(context.Background(), "example.com", rec); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, l := range fake.live(t) {
		if l.Type == "A" && l.Value == "1.2.3.4" {
			t.Fatal("record still live after deletion")
		}
	}
	// Second delete: nothing matching, still no error.
	if err := p.DeleteRecord(context.Background(), "example.com", rec); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

// ReadRecords returns the zone's records with normalized names.
func TestCloudflareReadRecordsNormalizesNames(t *testing.T) {
	_, url := newCfFake(t)
	p := NewCloudflareProvider("cf-good-token", url)
	recs, err := p.ReadRecords(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no records returned")
	}
	for _, r := range recs {
		if strings.HasSuffix(r.Name, ".") {
			t.Fatalf("name carries a trailing dot: %q", r.Name)
		}
	}
}
