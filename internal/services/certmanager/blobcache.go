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
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/goposta/posta/internal/storage/blob"

	"golang.org/x/crypto/acme/autocert"
)

// BlobCache implements autocert.Cache over the project's blob store
// (ADR-007 decision 3: "Certificates cache to the blob store (FS/S3)").
// Keys are mapped under a single prefix ("certs/acme/" by default) with
// every character outside [A-Za-z0-9._-] replaced by '_' so autocert's key
// shapes ("example.com+rsa-2048", "<token>+http-01") stay flat, safe path
// segments on both the FS and S3 providers. The transform is injective on
// the shapes autocert produces (which never contain '_'), so Get(Put(k))
// round-trips.
type BlobCache struct {
	store  blob.Store
	prefix string
}

// NewBlobCache builds the certificate cache over store with the default
// prefix.
func NewBlobCache(store blob.Store) *BlobCache {
	return &BlobCache{store: store, prefix: "certs/acme"}
}

// NewBlobCacheWithPrefix builds the certificate cache with an explicit key
// prefix (POSTA_ACME_CACHE_PREFIX).
func NewBlobCacheWithPrefix(store blob.Store, prefix string) *BlobCache {
	if strings.TrimSpace(prefix) == "" {
		return NewBlobCache(store)
	}
	return &BlobCache{store: store, prefix: strings.Trim(prefix, "/")}
}

// blobKey maps an autocert cache key onto a blob-store key.
func (c *BlobCache) blobKey(key string) string {
	var b strings.Builder
	b.WriteString(c.prefix)
	b.WriteByte('/')
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Get returns the cached certificate data for key, or autocert.ErrCacheMiss
// when the blob does not exist (autocert's cache contract).
func (c *BlobCache) Get(ctx context.Context, key string) ([]byte, error) {
	if c.store == nil {
		return nil, autocert.ErrCacheMiss
	}
	rc, err := c.store.Get(ctx, c.blobKey(key))
	if err != nil {
		return nil, autocert.ErrCacheMiss
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("certmanager: read cached certificate %q: %w", key, err)
	}
	if len(data) == 0 {
		return nil, autocert.ErrCacheMiss
	}
	return data, nil
}

// Put stores the certificate data; the blob content type is PKIX PEM text.
func (c *BlobCache) Put(ctx context.Context, key string, data []byte) error {
	if c.store == nil {
		return errors.New("certmanager: BlobCache has no blob store")
	}
	if err := c.store.Put(ctx, c.blobKey(key), strings.NewReader(string(data)), "application/x-pem-file"); err != nil {
		return fmt.Errorf("certmanager: cache put %q: %w", key, err)
	}
	return nil
}

// Delete removes the cached certificate; a missing blob counts as deleted
// (autocert's cache contract: "If there's no such key in the cache, Delete
// returns nil").
func (c *BlobCache) Delete(ctx context.Context, key string) error {
	if c.store == nil {
		return errors.New("certmanager: BlobCache has no blob store")
	}
	return c.store.Delete(ctx, c.blobKey(key))
}
