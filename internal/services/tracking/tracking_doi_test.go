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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tracking

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// doiTestService builds a service over a nil repository: the DOI token
// functions are pure HMAC math and never touch storage (RF-F075).
func doiTestService(t *testing.T) *Service {
	t.Helper()
	return NewService(nil, "https://doi.example.com/", []byte("doi-test-hmac-key"))
}

func TestDOIConfirmTokenRoundTrip(t *testing.T) {
	s := doiTestService(t)
	token := s.SignDOIConfirmToken(11, 42, time.Hour)
	listID, subscriberID, err := s.VerifyDOIConfirmToken(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if listID != 11 || subscriberID != 42 {
		t.Fatalf("round trip = (%d,%d), want (11,42)", listID, subscriberID)
	}
}

func TestDOIConfirmURLShape(t *testing.T) {
	s := doiTestService(t)
	url := s.DOIConfirmURL(3, 9)
	// The trailing slash of the base URL must be normalized away.
	if !strings.HasPrefix(url, "https://doi.example.com/t/confirm/") {
		t.Fatalf("url = %q, want the /t/confirm/ prefix on the trimmed base", url)
	}
	token := strings.TrimPrefix(url, "https://doi.example.com/t/confirm/")
	listID, subscriberID, err := s.VerifyDOIConfirmToken(token)
	if err != nil || listID != 3 || subscriberID != 9 {
		t.Fatalf("token from URL: (%d,%d,%v), want (3,9,nil)", listID, subscriberID, err)
	}
}

func TestDOIConfirmTokenExpired(t *testing.T) {
	s := doiTestService(t)
	token := s.SignDOIConfirmToken(1, 2, -time.Minute)
	if _, _, err := s.VerifyDOIConfirmToken(token); err == nil {
		t.Fatal("a negatively-TTL token must not verify")
	}
}

func TestDOIConfirmTokenTamper(t *testing.T) {
	s := doiTestService(t)
	token := s.SignDOIConfirmToken(5, 6, time.Hour)
	// Flip the payload to another (list, subscriber) pair without re-signing.
	parts := strings.SplitN(token, ".", 2)
	forged := parts[0] + "." + "AAAA"
	if _, _, err := s.VerifyDOIConfirmToken(forged); err == nil {
		t.Fatal("a re-signed payload must not verify")
	}
	// A valid signature over a DIFFERENT payload must not verify against the
	// original payload either (extend the payload, keep the signature).
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	tampered := base64.RawURLEncoding.EncodeToString(append(payload, 'x')) + "." + parts[1]
	if _, _, err := s.VerifyDOIConfirmToken(tampered); err == nil {
		t.Fatal("an extended payload with the original signature must not verify")
	}
}

func TestDOIConfirmTokenWrongKindRejected(t *testing.T) {
	s := doiTestService(t)
	// Unsubscribe and web-view tokens must not open the DOI confirm handler.
	if _, _, err := s.VerifyDOIConfirmToken(s.SignUnsubscribeToken(7)); err == nil {
		t.Fatal("an unsubscribe token must not verify as a DOI token")
	}
	if _, _, err := s.VerifyDOIConfirmToken(s.SignWebViewToken("some-uuid", time.Hour)); err == nil {
		t.Fatal("a web-view token must not verify as a DOI token")
	}
	// And the DOI token must not verify as either of those.
	if _, err := s.VerifyUnsubscribeToken(s.SignDOIConfirmToken(1, 2, time.Hour)); err == nil {
		t.Fatal("a DOI token must not verify as an unsubscribe token")
	}
	if _, err := s.VerifyWebViewToken(s.SignDOIConfirmToken(1, 2, time.Hour)); err == nil {
		t.Fatal("a DOI token must not verify as a web-view token")
	}
}

func TestDOIConfirmTokenGarbage(t *testing.T) {
	s := doiTestService(t)
	for _, token := range []string{"", "noseparator", "!!!.$$$", "a.b.c"} {
		if _, _, err := s.VerifyDOIConfirmToken(token); err == nil {
			t.Fatalf("token %q must not verify", token)
		}
	}
}
