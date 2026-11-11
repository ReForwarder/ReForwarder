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
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// r53Fake is a fake Route53 API (2013-04-01, XML): one hosted zone, its
// record sets, and a change log capturing every ChangeBatch POST.
type r53Fake struct {
	mu        sync.Mutex
	zoneID    string
	zone      string
	sets      []r53RecordSet
	changes   []string // raw action+name summaries of ChangeBatch POSTs
	batchCount int
	failStatus int
	failBody   string
}

func newR53Fake(t *testing.T) (*r53Fake, string) {
	t.Helper()
	f := &r53Fake{zoneID: "Z123456", zone: "example.com."}
	f.sets = []r53RecordSet{
		{Name: "example.com.", Type: "A", TTL: 300, Values: []string{"1.2.3.4"}},
		// An unrelated apex TXT record the zone owner keeps.
		{Name: "example.com.", Type: "TXT", TTL: 300, Values: []string{`"some-unrelated-ownership-token"`}},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *r53Fake) fail(status int, body string) {
	f.mu.Lock()
	f.failStatus, f.failBody = status, body
	f.mu.Unlock()
}

func (f *r53Fake) errorXML(status int, code, message string) {
	f.fail(status, fmt.Sprintf(
		`<ErrorResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error></ErrorResponse>`,
		code, message))
}

func (f *r53Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// SigV4 discipline: every request carries the signed Authorization header
	// and x-amz-date.
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256") || r.Header.Get("X-Amz-Date") == "" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(f.errorBody("AccessDenied", "unsigned request")))
		return
	}

	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		_, _ = w.Write([]byte(f.failBody))
		return
	}

	path := r.URL.Path
	switch {
	case path == "/2013-04-01/hostedzonesbyname" && r.Method == http.MethodGet:
		if name := r.URL.Query().Get("dnsname"); !strings.EqualFold(strings.TrimSuffix(name, "."), strings.TrimSuffix(f.zone, ".")) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<ListHostedZonesByNameResponse><HostedZones></HostedZones></ListHostedZonesByNameResponse>`))
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<ListHostedZonesByNameResponse><HostedZones><HostedZone><Id>/hostedzone/%s</Id><Name>%s</Name></HostedZone></HostedZones></ListHostedZonesByNameResponse>`, f.zoneID, f.zone)
	case strings.HasPrefix(path, "/2013-04-01/hostedzone/"+f.zoneID+"/rrset") && r.Method == http.MethodGet:
		var b strings.Builder
		b.WriteString(`<ListResourceRecordSetsResponse><ResourceRecordSets>`)
		for _, set := range f.sets {
			b.WriteString(`<ResourceRecordSet><Name>` + set.Name + `</Name><Type>` + set.Type + `</Type><TTL>` + fmt.Sprintf("%d", set.TTL) + `</TTL><ResourceRecords>`)
			for _, v := range set.Values {
				b.WriteString(`<ResourceRecord><Value>` + v + `</Value></ResourceRecord>`)
			}
			b.WriteString(`</ResourceRecords></ResourceRecordSet>`)
		}
		b.WriteString(`</ResourceRecordSets></ListResourceRecordSetsResponse>`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	case strings.HasPrefix(path, "/2013-04-01/hostedzone/"+f.zoneID+"/rrset") && r.Method == http.MethodPost:
		var req struct {
			XMLName    xml.Name `xml:"ChangeResourceRecordSetsRequest"`
			ChangeBatch struct {
				Changes []struct {
					Action          string `xml:"Action"`
					ResourceRecordSet struct {
						Name    string   `xml:"Name"`
						Type    string   `xml:"Type"`
						TTL     int      `xml:"TTL"`
						Records []string `xml:"ResourceRecords>ResourceRecord>Value"`
					} `xml:"ResourceRecordSet"`
				} `xml:"Changes>Change"`
			} `xml:"ChangeBatch"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.batchCount++
		applied := map[string]*r53RecordSet{}
		for _, ch := range req.ChangeBatch.Changes {
			key := ch.ResourceRecordSet.Type + " " + ch.ResourceRecordSet.Name
			f.changes = append(f.changes, ch.Action+" "+key)
			switch ch.Action {
			case "UPSERT":
				set, ok := applied[key]
				if !ok {
					set = &r53RecordSet{
						Name:   ch.ResourceRecordSet.Name,
						Type:   ch.ResourceRecordSet.Type,
						TTL:    ch.ResourceRecordSet.TTL,
						Values: ch.ResourceRecordSet.Records,
					}
					applied[key] = set
				} else {
					set.Values = append(set.Values, ch.ResourceRecordSet.Records...)
				}
			case "DELETE":
				for i, set := range f.sets {
					if set.Type == ch.ResourceRecordSet.Type && strings.EqualFold(set.Name, ch.ResourceRecordSet.Name) {
						var kept []string
						for _, v := range set.Values {
							if !containsExact(ch.ResourceRecordSet.Records, v) {
								kept = append(kept, v)
							}
						}
						if len(kept) == 0 {
							f.sets = append(f.sets[:i], f.sets[i+1:]...)
						} else {
							f.sets[i].Values = kept
						}
						break
					}
				}
			}
		}
		// UPSERTs land at the END of the fake's handler — mirrors AWS applying
		// the whole batch atomically.
		for key, set := range applied {
			_ = key
			replaced := false
			for i, existing := range f.sets {
				if existing.Type == set.Type && strings.EqualFold(existing.Name, set.Name) {
					f.sets[i] = *set
					replaced = true
					break
				}
			}
			if !replaced {
				f.sets = append(f.sets, *set)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<ChangeResourceRecordSetsResponse><ChangeInfo><Status>PENDING</Status></ChangeInfo></ChangeResourceRecordSetsResponse>`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(f.errorBody("NotFound", path)))
	}
}

func containsExact(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func (f *r53Fake) errorBody(code, message string) string {
	return fmt.Sprintf(
		`<ErrorResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error></ErrorResponse>`,
		code, message)
}

func (f *r53Fake) liveValues(t *testing.T, typ, name string) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, set := range f.sets {
		if set.Type == typ && strings.EqualFold(set.Name, name) {
			out = append(out, set.Values...)
		}
	}
	return out
}

func r53Provider(url string) *Route53Provider {
	return NewRoute53Provider(Credential{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI"}, url)
}

// Publish sends ONE ChangeBatch (atomic per AWS semantics — ADR-007's
// no-partial-zone-writes), with TXT values quoted and CNAME/MX targets
// trailing-dotted.
func TestRoute53PublishIsOneAtomicBatch(t *testing.T) {
	fake, url := newR53Fake(t)
	p := r53Provider(url)

	set := []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "posta-verification=tok-123", TTL: 300},
		{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300},
		{Type: "CNAME", Name: "posta._domainkey.example.com", Value: "posta._domainkey.posta", TTL: 300},
		{Type: "MX", Name: "example.com", Value: "mail.example.net", Priority: 10, TTL: 300},
	}
	if err := p.PublishRecordSet(context.Background(), "example.com", set); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if fake.batchCount != 1 {
		t.Fatalf("publish issued %d ChangeBatch POSTs, want exactly 1 (atomic)", fake.batchCount)
	}

	// TXT values are stored quoted on Route53.
	for _, want := range []string{`"posta-verification=tok-123"`, `"v=spf1 include:_spf.posta ~all"`} {
		if !containsExact(fake.liveValues(t, "TXT", "example.com."), want) {
			t.Fatalf("TXT value %s missing among %v", want, fake.liveValues(t, "TXT", "example.com."))
		}
	}
	// MX carries the priority prefix and the trailing dot.
	mx := fake.liveValues(t, "MX", "example.com.")
	if len(mx) != 1 || mx[0] != "10 mail.example.net." {
		t.Fatalf("MX set = %v, want [10 mail.example.net.]", mx)
	}
	// The unrelated apex TXT record SURVIVED the upsert (merge, not replace).
	if !containsExact(fake.liveValues(t, "TXT", "example.com."), `"some-unrelated-ownership-token"`) {
		t.Fatal("unrelated apex TXT record lost by the publish")
	}
}

// Two TXT records at the same name are grouped into ONE ResourceRecordSet —
// Route53 forbids two changes to the same name+type in a batch.
func TestRoute53PublishGroupsSameNameType(t *testing.T) {
	fake, url := newR53Fake(t)
	p := r53Provider(url)

	set := []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "v=spf1 ~all", TTL: 300},
		{Type: "TXT", Name: "example.com", Value: "posta-verification=tok-123", TTL: 300},
	}
	if err := p.PublishRecordSet(context.Background(), "example.com", set); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(fake.changes) != 1 {
		t.Fatalf("changes = %v, want a single grouped UPSERT change", fake.changes)
	}
}

// A mis-scoped key (AccessDenied from AWS) fails BEFORE any write.
func TestRoute53MisScopedKeyFailsBeforeWrites(t *testing.T) {
	fake, url := newR53Fake(t)
	fake.errorXML(http.StatusForbidden, "AccessDenied", "The security token included in the request is invalid.")
	p := r53Provider(url)

	err := p.PublishRecordSet(context.Background(), "example.com", []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "x"},
	})
	if !errorsIs(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if fake.batchCount != 0 {
		t.Fatalf("%d batches were written despite the rejected key", fake.batchCount)
	}
}

// NoSuchHostedZone classifies as ErrZoneNotFound; Throttling as
// ErrRateLimited.
func TestRoute53TypedErrorClassification(t *testing.T) {
	fake, url := newR53Fake(t)

	fake.errorXML(http.StatusBadRequest, "NoSuchHostedZone", "No hosted zone found")
	p := r53Provider(url)
	if _, err := p.ReadRecords(context.Background(), "missing-zone.net"); !errorsIs(err, ErrZoneNotFound) {
		t.Fatalf("error = %v, want ErrZoneNotFound", err)
	}

	fake.errorXML(http.StatusTooManyRequests, "Throttling", "Request throttled")
	if _, err := p.ReadRecords(context.Background(), "example.com"); !errorsIs(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

// ReadRecords flattens record sets into specs.
func TestRoute53ReadRecordsFlattensSets(t *testing.T) {
	_, url := newR53Fake(t)
	p := r53Provider(url)
	recs, err := p.ReadRecords(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.Type == "A" && normalizeName(r.Name) == "example.com" && r.Value == "1.2.3.4" {
			found = true
		}
		if strings.HasSuffix(r.Name, ".") {
			t.Fatalf("name carries a trailing dot: %q", r.Name)
		}
	}
	if !found {
		t.Fatalf("A record missing among %+v", recs)
	}
}

// DeleteRecord removes exactly the managed value and leaves the zone's other
// values at that name+type untouched.
func TestRoute53DeleteRecordPreservesSiblings(t *testing.T) {
	fake, url := newR53Fake(t)
	p := r53Provider(url)

	if err := p.PublishRecordSet(context.Background(), "example.com", []DNSRecordSpec{
		{Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.posta ~all", TTL: 300},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := p.DeleteRecord(context.Background(), "example.com", DNSRecordSpec{
		Type: "TXT", Name: "example.com", Value: "v=spf1 include:_spf.posta ~all",
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	values := fake.liveValues(t, "TXT", "example.com.")
	if containsExact(values, `"v=spf1 include:_spf.posta ~all"`) {
		t.Fatalf("deleted value still live: %v", values)
	}
	if !containsExact(values, `"some-unrelated-ownership-token"`) {
		t.Fatalf("sibling value lost by the delete: %v", values)
	}
}
