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
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package certmanager

import (
	"bytes"
	"context"
	"testing"

	"golang.org/x/crypto/acme/autocert"
)

// TestBlobCacheRoundTrip: Put→Get returns the exact certificate bytes for
// every autocert key shape (domain+key-algorithm, the http-01 challenge
// token key), the payload is keyed under the blob prefix, Delete removes
// it, and Delete of an absent key is nil (autocert's cache contract).
func TestBlobCacheRoundTrip(t *testing.T) {
	store := testBlobStore(t)
	bc := NewBlobCache(store)

	keys := []string{
		"mail.a.example+rsa-2048",
		"mail.b.example+ecdsa-p256",
		"token123+http-01",
	}
	payloads := map[string][]byte{}
	for _, k := range keys {
		data := bytes.Repeat([]byte(k), 3)
		payloads[k] = data
		if err := bc.Put(context.Background(), k, data); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	ctx := context.Background()
	for _, k := range keys {
		got, err := bc.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get(%q): %v", k, err)
		}
		if !bytes.Equal(got, payloads[k]) {
			t.Fatalf("Get(%q) round trip mismatch", k)
		}
	}

	// The blob prefix is applied and '+' sanitizes to '_': the raw autocert
	// key is not the blob key.
	if _, err := store.Get(ctx, "mail.a.example+rsa-2048"); err == nil {
		t.Fatal("cache keys must be namespaced under the blob prefix")
	}
	raw, err := store.Get(ctx, "certs/acme/mail.a.example_rsa-2048")
	if err != nil {
		t.Fatalf("cached blob missing under the prefix: %v", err)
	}
	_ = raw.Close()

	if err := bc.Delete(ctx, "mail.a.example+rsa-2048"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := bc.Get(ctx, "mail.a.example+rsa-2048"); err != autocert.ErrCacheMiss {
		t.Fatalf("Get after Delete = %v, want ErrCacheMiss", err)
	}
	if err := bc.Delete(ctx, "never-existed"); err != nil {
		t.Fatalf("Delete of an absent key must be nil, got %v", err)
	}
}

// TestBlobCacheMissingKeyIsCacheMiss: a cache miss maps to
// autocert.ErrCacheMiss (not a wrapped error), which autocert relies on to
// decide between "no cert cached" and "cache broken".
func TestBlobCacheMissingKeyIsCacheMiss(t *testing.T) {
	bc := NewBlobCache(testBlobStore(t))
	_, err := bc.Get(context.Background(), "absent.example+rsa-2048")
	if err != autocert.ErrCacheMiss {
		t.Fatalf("Get of an absent key = %v, want autocert.ErrCacheMiss", err)
	}
}

// TestBlobCacheKeySanitization: characters outside [A-Za-z0-9._-] become
// '_' so keys stay safe flat path segments on the FS and S3 providers, and
// the mapping is deterministic.
func TestBlobCacheKeySanitization(t *testing.T) {
	bc := NewBlobCacheWithPrefix(testBlobStore(t), "/custom/prefix/")
	got := bc.blobKey("a/b+c.d-e+f")
	if got != "custom/prefix/a_b_c.d-e_f" {
		t.Fatalf("blobKey = %q, want custom/prefix/a_b_c.d-e_f", got)
	}
	if bc.blobKey("mail.example.com+rsa-2048") != "custom/prefix/mail.example.com_rsa-2048" {
		t.Fatalf("blobKey = %q, want custom/prefix/mail.example.com_rsa-2048 ('+' sanitizes to '_')", bc.blobKey("mail.example.com+rsa-2048"))
	}
}
