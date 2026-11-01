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
	"strings"
	"testing"
	"time"
)

func newTestService() *Service {
	return NewService(nil, "https://posta.example.com/", []byte("test-hmac-key"))
}

func TestWebViewTokenRoundTrip(t *testing.T) {
	s := newTestService()
	const uuid = "11111111-2222-3333-4444-555555555555"

	tok := s.SignWebViewToken(uuid, time.Hour)
	got, err := s.VerifyWebViewToken(tok)
	if err != nil {
		t.Fatalf("verify returned error: %v", err)
	}
	if got != uuid {
		t.Fatalf("uuid mismatch: got %q want %q", got, uuid)
	}
}

func TestWebViewURL(t *testing.T) {
	s := newTestService()
	url := s.WebViewURL("abc")
	if !strings.HasPrefix(url, "https://posta.example.com/t/v/") {
		t.Fatalf("unexpected web view URL: %q", url)
	}
	if s.WebViewURL("") != "" {
		t.Fatalf("empty uuid should yield empty url")
	}
}

func TestWebViewTokenExpired(t *testing.T) {
	s := newTestService()
	tok := s.SignWebViewToken("abc", -time.Minute) // already expired
	if _, err := s.VerifyWebViewToken(tok); err == nil {
		t.Fatalf("expected expired token to be rejected")
	}
}

func TestWebViewTokenTampered(t *testing.T) {
	s := newTestService()
	tok := s.SignWebViewToken("abc", time.Hour)
	if _, err := s.VerifyWebViewToken(tok + "x"); err == nil {
		t.Fatalf("expected tampered signature to be rejected")
	}
}

func TestWebViewTokenWrongKind(t *testing.T) {
	s := newTestService()
	// A transactional unsubscribe token must not validate as a web-view token.
	txTok := s.SignTxUnsubscribeToken(42)
	if _, err := s.VerifyWebViewToken(txTok); err == nil {
		t.Fatalf("expected wrong-kind token to be rejected")
	}
}
