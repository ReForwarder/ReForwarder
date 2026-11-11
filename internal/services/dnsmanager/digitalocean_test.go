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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// doFake is a fake DigitalOcean API v2 server (bearer-token gated): a domain
// with relative-named records ("@" for the apex) and the v2 shapes.
type doFake struct {
	mu      sync.Mutex
	token   string
	domain  string
	records []doRecord
	seq     int
	failStatus int
	failBody   string
}

func newDoFake(t *testing.T) (*doFake, string) {
	t.Helper()
	f := &doFake{token: "do-good-token", domain: "example.com"}
	f.records = []doRecord{{ID: 11, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *doFake) fail(status int, body string) {
	f.mu.Lock()
	f.failStatus, f.failBody = status, body
	f.mu.Unlock()
}

func (f *doFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if got := r.Header.Get("Authorization"); got != "Bearer "+f.token {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"id":"unauthorized","message":"Unable to authenticate you"}`))
		return
	}
	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		_, _ = w.Write([]byte(f.failBody))
		return
	}

	path := r.URL.Path
	switch {
	case path == "/domains/example.com" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"domain": map[string]string{"name": f.domain}})
	case path == "/domains/example.com/records" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"domain_records": f.records,
			"meta":           map[string]any{"total": len(f.records)},
		})
	case path == "/domains/example.com/records" && r.Method == http.MethodPost:
		var body doRecord
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.seq++
		body.ID = 100 + f.seq
		f.records = append(f.records, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"domain_record": body})
	case strings.HasPrefix(path, "/domains/example.com/records/") && r.Method == http.MethodPut:
		var id int
		if _, err := fmt.Sscanf(strings.TrimPrefix(path, "/domains/example.com/records/"), "%d", &id); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for i, rec := range f.records {
			if rec.ID == id {
				_ = json.NewDecoder(r.Body).Decode(&f.records[i])
				f.records[i].ID = id
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"domain_record": f.records[i]})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasPrefix(path, "/domains/example.com/records/") && r.Method == http.MethodDelete:
		var id int
		_, _ = fmt.Sscanf(strings.TrimPrefix(path, "/domains/example.com/records/"), "%d", &id)
		for i, rec := range f.records {
			if rec.ID == id {
				f.records = append(f.records[:i], f.records[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"id":"not_found","message":"not found"}`))
	}
}

func (f *doFake) live(t *testing.T) []DNSRecordSpec {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &DigitalOceanProvider{}
	out := make([]DNSRecordSpec, 0, len(f.records))
	for _, r := range f.records {
		spec := DNSRecordSpec{Type: r.Type, Value: r.Data, TTL: r.TTL, Priority: r.Priority}
		spec.Name = p.fqdnName(f.domain, r.Name)
		out = append(out, spec)
	}
	return out
}

// Publish upserts the set against relative names: existing records update in
// place, new ones are created, unrelated records survive.
func TestDigitalOceanPublishUpsertsRecords(t *testing.T) {
	fake, url := newDoFake(t)
	p := NewDigitalOceanProvider("do-good-token", url)

	set := []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "posta-verification=tok-123", TTL: 300},
		{Type: "TXT", Name: "_dmarc.example.com", Value: "v=DMARC1; p=none", TTL: 300},
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
			t.Fatalf("published %s %s not found live among %+v", spec.Type, spec.Name, live)
		}
	}
	// Unrelated apex A record survived.
	for _, l := range live {
		if l.Type == "A" && l.Value == "1.2.3.4" {
			return
		}
	}
	t.Fatal("unrelated A record lost by the publish")
}

// A token without access fails the pre-write read with ErrUnauthorized and
// applies nothing (no partial-zone writes, ADR-007 decision 2).
func TestDigitalOceanBadTokenFailsBeforeWrites(t *testing.T) {
	fake, url := newDoFake(t)
	p := NewDigitalOceanProvider("do-wrong-token", url)

	before := len(fake.live(t))
	err := p.PublishRecordSet(context.Background(), "example.com", []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "x"},
	})
	if !errorsIs(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	var pubErr *PublishError
	if !errorsAs(err, &pubErr) {
		t.Fatalf("error is not a *PublishError: %v", err)
	}
	if len(pubErr.Applied) != 0 {
		t.Fatalf("nothing must be applied on a rejected token, applied %d", len(pubErr.Applied))
	}
	if len(fake.live(t)) != before {
		t.Fatal("the zone changed despite the rejected token")
	}
}

// A domain that does not exist classifies as ErrZoneNotFound.
func TestDigitalOceanZoneNotFoundIsTyped(t *testing.T) {
	_, url := newDoFake(t)
	p := NewDigitalOceanProvider("do-good-token", url)
	_, err := p.ReadRecords(context.Background(), "other-domain.com")
	if !errorsIs(err, ErrZoneNotFound) {
		t.Fatalf("error = %v, want ErrZoneNotFound", err)
	}
}

// 429 classifies as ErrRateLimited.
func TestDigitalOceanRateLimitIsTyped(t *testing.T) {
	fake, url := newDoFake(t)
	fake.fail(http.StatusTooManyRequests, `{"id":"throttled","message":"too many requests"}`)
	p := NewDigitalOceanProvider("do-good-token", url)
	_, err := p.ReadRecords(context.Background(), "example.com")
	if !errorsIs(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

// DeleteRecord removes the matching record and is idempotent.
func TestDigitalOceanDeleteRecord(t *testing.T) {
	fake, url := newDoFake(t)
	p := NewDigitalOceanProvider("do-good-token", url)

	rec := DNSRecordSpec{Type: "A", Name: "example.com", Value: "1.2.3.4"}
	if err := p.DeleteRecord(context.Background(), "example.com", rec); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := p.DeleteRecord(context.Background(), "example.com", rec); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	for _, l := range fake.live(t) {
		if l.Type == "A" && l.Value == "1.2.3.4" {
			t.Fatal("record still live after deletion")
		}
	}
}
