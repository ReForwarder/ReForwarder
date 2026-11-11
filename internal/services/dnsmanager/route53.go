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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// route53DefaultBase is the Route53 REST root (the service is global; the API
// endpoint lives in us-east-1).
const route53DefaultBase = "https://route53.amazonaws.com"

// route53APIVersion is the path prefix of the 2013-04-01 API.
const route53APIVersion = "/2013-04-01"

// Route53Provider publishes records through the Route53 ChangeResourceRecordSets
// API with an IAM access key pair signed via SigV4. Route53's ChangeBatch is
// atomic per POST — the whole record set lands in one call or nothing does —
// which is the strongest form of ADR-007's "no partial-zone writes". Access is
// still validated with a pre-write ListResourceRecordSets read so a mis-scoped
// key (no route53:ChangeResourceRecordSets on the zone's hosted zone) fails
// with ErrUnauthorized before anything is attempted.
type Route53Provider struct {
	cred Credential
	base string
	do   func(*http.Request) (*http.Response, error)
	// now is the signing clock; a field so tests can pin x-amz-date.
	now func() time.Time
}

func NewRoute53Provider(cred Credential, base string) *Route53Provider {
	if cred.Region == "" {
		cred.Region = "us-east-1"
	}
	return &Route53Provider{cred: cred, base: strings.TrimRight(base, "/"), do: httpClient.Do, now: time.Now}
}

func (p *Route53Provider) Kind() string { return modelsProviderRoute53 }

// ---- SigV4 (AWS signature version 4) ----
// Implemented locally rather than pulling an AWS SDK: the Route53 surface used
// here is three endpoints, and ADR-007's dependency posture prefers zero new
// control-plane dependencies (the autocert decision records the same choice).

const (
	sigV4Algorithm  = "AWS4-HMAC-SHA256"
	sigV4Service    = "route53"
	sigV4EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// signRequest computes the SigV4 Authorization header for the request in
// place. Route53 signs host, x-amz-date and (when present) x-amz-security-token.
func (p *Route53Provider) signRequest(req *http.Request, body []byte) {
	now := p.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if p.cred.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", p.cred.SessionToken)
	}

	payloadHash := sigV4EmptySHA256
	if len(body) > 0 {
		payloadHash = sha256Hex(body)
	}

	signedHeaders := "host;x-amz-date"
	if p.cred.SessionToken != "" {
		signedHeaders += ";x-amz-security-token"
	}

	canonicalHeaders := "host:" + req.URL.Host + "\n" +
		"x-amz-date:" + amzDate + "\n"
	if p.cred.SessionToken != "" {
		canonicalHeaders += "x-amz-security-token:" + p.cred.SessionToken + "\n"
	}

	// Canonical URI: the raw path; Route53's paths carry no %-encoding needs.
	canonicalURI := req.URL.Path
	// Canonical query string: Route53 reads use simple name=value pairs,
	// sorted, URL-encoded per RFC 3986.
	canonicalQuery := canonicalQueryOf(req.URL.RawQuery)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, p.cred.Region, sigV4Service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		sigV4Algorithm,
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+p.cred.SecretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(p.cred.Region))
	kService := hmacSHA256(kRegion, []byte(sigV4Service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		sigV4Algorithm, p.cred.AccessKeyID, scope, signedHeaders, signature))
}

// canonicalQueryOf sorts the raw query pairs and re-encodes them the AWS way
// (space becomes %20, not +).
func canonicalQueryOf(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	pairs := strings.Split(rawQuery, "&")
	sortStrings(pairs)
	out := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		k := awsURIEncode(kv[0])
		if len(kv) == 2 {
			out = append(out, k+"="+awsURIEncode(kv[1]))
		} else {
			out = append(out, k+"=")
		}
	}
	return strings.Join(out, "&")
}

func sortStrings(s []string) {
	// insertion sort — the pair counts here are tiny (≤3).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// awsURIEncode encodes everything but unreserved characters per RFC 3986
// (AWS requires %20 for spaces, not +).
func awsURIEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// doSigned performs a SigV4-signed XML call and returns the raw body.
func (p *Route53Provider) doSigned(ctx context.Context, method, pathAndQuery string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.base+pathAndQuery, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}
	p.signRequest(req, body)
	resp, err := p.do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading response: %v", ErrProviderUnavailable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, route53Error(resp.StatusCode, raw)
	}
	return raw, nil
}

// route53Error parses the ErrorResponse XML onto the sentinel errors.
func route53Error(status int, body []byte) error {
	var env struct {
		Error struct {
			Type    string `xml:"Type"`
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	msg := ""
	code := ""
	if err := xml.Unmarshal(body, &env); err == nil {
		code = env.Error.Code
		msg = env.Error.Message
	}
	err := ErrorOf(status, code)
	if msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// resolveZone maps the zone name to its hosted zone id via
// ListHostedZonesByName, verifying the exact-name match (a key scoped to
// another account returns nothing to match — the pre-write rejection).
func (p *Route53Provider) resolveZone(ctx context.Context, zone string) (string, error) {
	q := awsURIEncode("dnsname") + "=" + awsURIEncode(normalizeName(zone)) + "&" +
		awsURIEncode("maxitems") + "=1"
	raw, err := p.doSigned(ctx, http.MethodGet, route53APIVersion+"/hostedzonesbyname?"+q, nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		HostedZones []struct {
			ID   string `xml:"Id"`
			Name string `xml:"Name"`
		} `xml:"HostedZones>HostedZone"`
	}
	if err := xml.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("dnsmanager: route53 zone list decode: %w", err)
	}
	for _, z := range resp.HostedZones {
		if normalizeName(z.Name) == normalizeName(zone) {
			// Id arrives as "/hostedzone/Z123456"; the rrset endpoints accept
			// either the bare id or the full path.
			return strings.TrimPrefix(z.ID, "/hostedzone/"), nil
		}
	}
	return "", fmt.Errorf("%w: credentials have no %s hosted zone", ErrZoneNotFound, zone)
}

// r53RecordSet is one ResourceRecordSet: a name+type group holding values.
type r53RecordSet struct {
	Name     string
	Type     string
	TTL      int
	Values   []string
	Priority int // carried on the single-value MX set
}

// r53ListResponse mirrors ListResourceRecordSets.
type r53ListResponse struct {
	RecordSets []struct {
		Name     string `xml:"Name"`
		Type     string `xml:"Type"`
		TTL      int    `xml:"TTL"`
		Records  []struct {
			Value string `xml:"Value"`
		} `xml:"ResourceRecords>ResourceRecord"`
	} `xml:"ResourceRecordSets>ResourceRecordSet"`
	NextRecordName string `xml:"NextRecordName"`
	NextRecordType string `xml:"NextRecordType"`
}

// listRecordSets fetches every ResourceRecordSet of the zone (paginated).
func (p *Route53Provider) listRecordSets(ctx context.Context, zoneID string) ([]r53RecordSet, error) {
	var out []r53RecordSet
	startName := ""
	startType := ""
	for {
		q := awsURIEncode("maxitems") + "=100"
		if startName != "" {
			q += "&" + awsURIEncode("name") + "=" + awsURIEncode(startName) +
				"&" + awsURIEncode("type") + "=" + awsURIEncode(startType)
		}
		raw, err := p.doSigned(ctx, http.MethodGet, route53APIVersion+"/hostedzone/"+zoneID+"/rrset?"+q, nil)
		if err != nil {
			return nil, err
		}
		var resp r53ListResponse
		if err := xml.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("dnsmanager: route53 record list decode: %w", err)
		}
		for _, rs := range resp.RecordSets {
			set := r53RecordSet{Name: normalizeName(rs.Name), Type: rs.Type, TTL: rs.TTL}
			for _, r := range rs.Records {
				set.Values = append(set.Values, r.Value)
			}
			out = append(out, set)
		}
		if resp.NextRecordName == "" {
			return out, nil
		}
		startName = resp.NextRecordName
		startType = resp.NextRecordType
	}
}

// ReadRecords flattens the zone's record sets into specs. Values are reported
// as Route53 echoes them (TXT values arrive quoted; valueMatches compares
// quote-tolerantly).
func (p *Route53Provider) ReadRecords(ctx context.Context, zone string) ([]DNSRecordSpec, error) {
	zoneID, err := p.resolveZone(ctx, zone)
	if err != nil {
		return nil, err
	}
	sets, err := p.listRecordSets(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	out := make([]DNSRecordSpec, 0, 16)
	for _, set := range sets {
		for _, v := range set.Values {
			out = append(out, DNSRecordSpec{Type: set.Type, Name: set.Name, Value: v, TTL: set.TTL})
		}
	}
	return out, nil
}

// PublishRecordSet upserts the set in ONE ChangeBatch (atomic per AWS
// semantics). Values are grouped by (name, type) — Route53 forbids two changes
// to the same record set in one batch — and merged with existing values at
// that name+type so unrelated records the operator keeps in the zone (other
// apex TXT records, for instance) survive the upsert.
func (p *Route53Provider) PublishRecordSet(ctx context.Context, zone string, records []DNSRecordSpec) error {
	zoneID, err := p.resolveZone(ctx, zone)
	if err != nil {
		return &PublishError{Zone: zone, Cause: err}
	}
	// Pre-write access validation (a key without ChangeResourceRecordSets may
	// still read; the ChangeBatch below is the write check — the atomicity
	// means the failure is total, not partial).
	existing, err := p.listRecordSets(ctx, zoneID)
	if err != nil {
		return &PublishError{Zone: zone, Cause: err}
	}

	type group struct {
		name    string
		typ     string
		ttl     int
		values  []string
		changed bool
	}
	groups := make(map[recordKey]*group, len(records))
	for _, spec := range records {
		k := spec.key()
		g, ok := groups[k]
		if !ok {
			g = &group{name: normalizeName(spec.Name) + ".", typ: spec.Type}
			// Seed with the existing values NOT managed by this publish, so
			// unrelated records at the same name+type are preserved.
			for _, set := range existing {
				if set.Type == spec.Type && normalizeName(set.Name) == normalizeName(spec.Name) {
					g.ttl = set.TTL
					for _, v := range set.Values {
						if !specValueIn(specsOf(records), spec.Type, normalizeName(spec.Name), v) {
							g.values = append(g.values, v)
						}
					}
				}
			}
			groups[k] = g
		}
		// TXT values are stored quoted on Route53.
		g.values = append(g.values, route53Value(spec))
		g.changed = true
	}

	// One ChangeBatch, every group as UPSERT.
	var changes strings.Builder
	count := 0
	for _, g := range groups {
		rrset := fmt.Sprintf(
			"<ResourceRecordSet><Name>%s</Name><Type>%s</Type><TTL>%d</TTL><ResourceRecords>%s</ResourceRecords></ResourceRecordSet>",
			xmlEscape(g.name), g.typ, ttlOr(g.ttl, 300), recordValuesXML(g.values))
		changes.WriteString("<Change><Action>UPSERT</Action>" + rrset + "</Change>")
		count++
	}
	if count == 0 {
		return nil
	}
	body := xml.Header + fmt.Sprintf(
		`<ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">`+
			`<ChangeBatch><Changes>%s</Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`,
		changes.String())

	if _, err := p.doSigned(ctx, http.MethodPost, route53APIVersion+"/hostedzone/"+zoneID+"/rrset", []byte(body)); err != nil {
		return &PublishError{Zone: zone, Cause: err}
	}
	return nil
}

// specsOf is a tiny helper for the merge filter above.
func specsOf(records []DNSRecordSpec) []DNSRecordSpec { return records }

// specValueIn reports whether value is one of the planned values at
// (type, name) — used to keep only UNRELATED existing values in the merged set.
func specValueIn(specs []DNSRecordSpec, typ, name, value string) bool {
	for _, s := range specs {
		if s.Type == typ && normalizeName(s.Name) == name && valueMatches(route53Value(s), value) {
			return true
		}
	}
	return false
}

// route53Value formats the spec's value for Route53 storage: TXT values are
// quoted; CNAME/MX targets get a trailing dot; MX carries the priority prefix.
func route53Value(spec DNSRecordSpec) string {
	v := spec.Value
	switch spec.Type {
	case "TXT":
		v = strings.TrimSpace(v)
		if !strings.HasPrefix(v, `"`) {
			v = `"` + v + `"`
		}
		return v
	case "CNAME":
		return ensureTrailingDot(v)
	case "MX":
		return fmt.Sprintf("%d %s", mxPriorityOr(spec.Priority), ensureTrailingDot(v))
	default:
		return v
	}
}

func ensureTrailingDot(s string) string {
	s = normalizeName(s)
	return s + "."
}

func recordValuesXML(values []string) string {
	var b strings.Builder
	for _, v := range values {
		b.WriteString("<ResourceRecord><Value>" + xmlEscape(v) + "</Value></ResourceRecord>")
	}
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// DeleteRecord issues a ChangeBatch DELETE for the exact record (type, name,
// value). Route53 requires the DELETE to match an existing set exactly, so a
// group's other values are preserved by deleting only the matching value set.
func (p *Route53Provider) DeleteRecord(ctx context.Context, zone string, rec DNSRecordSpec) error {
	zoneID, err := p.resolveZone(ctx, zone)
	if err != nil {
		return err
	}
	sets, err := p.listRecordSets(ctx, zoneID)
	if err != nil {
		return err
	}
	for _, set := range sets {
		if set.Type != rec.Type || normalizeName(set.Name) != normalizeName(rec.Name) {
			continue
		}
		for _, v := range set.Values {
			if valueMatches(v, route53Value(rec)) {
				// Only this value: DELETE with a single-value set.
				if len(set.Values) == 1 {
					return p.changeRecord(ctx, zoneID, "DELETE",
						fmt.Sprintf("<ResourceRecordSet><Name>%s.</Name><Type>%s</Type><TTL>%d</TTL><ResourceRecords><ResourceRecord><Value>%s</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>",
							xmlEscape(normalizeName(rec.Name)), rec.Type, ttlOr(set.TTL, 300), xmlEscape(v)))
				}
				// Multi-value set: rewrite it without the deleted value.
				var kept []string
				for _, kv := range set.Values {
					if !valueMatches(kv, v) {
						kept = append(kept, kv)
					}
				}
				if len(kept) == 0 {
					return p.changeRecord(ctx, zoneID, "DELETE",
						fmt.Sprintf("<ResourceRecordSet><Name>%s.</Name><Type>%s</Type><TTL>%d</TTL><ResourceRecords><ResourceRecord><Value>%s</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>",
							xmlEscape(normalizeName(rec.Name)), rec.Type, ttlOr(set.TTL, 300), xmlEscape(v)))
				}
				return p.changeRecord(ctx, zoneID, "UPSERT",
					fmt.Sprintf("<ResourceRecordSet><Name>%s.</Name><Type>%s</Type><TTL>%d</TTL><ResourceRecords>%s</ResourceRecords></ResourceRecordSet>",
						xmlEscape(normalizeName(rec.Name)), rec.Type, ttlOr(set.TTL, 300), recordValuesXML(kept)))
			}
		}
	}
	return nil // nothing matching — deletion is idempotent
}

func (p *Route53Provider) changeRecord(ctx context.Context, zoneID, action, rrset string) error {
	body := xml.Header + fmt.Sprintf(
		`<ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">`+
			`<ChangeBatch><Changes><Change><Action>%s</Action>%s</Change></Changes></ChangeBatch>`+
			`</ChangeResourceRecordSetsRequest>`,
		action, rrset)
	_, err := p.doSigned(ctx, http.MethodPost, route53APIVersion+"/hostedzone/"+zoneID+"/rrset", []byte(body))
	return err
}
