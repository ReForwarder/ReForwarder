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

package blob

import (
	"context"
	"fmt"
	"io"
)

// Store abstracts object/blob storage for email attachments.
// Implementations may target S3-compatible services, local filesystem, etc.
type Store interface {
	// Put writes data to the given key and returns the key on success.
	Put(ctx context.Context, key string, r io.Reader, contentType string) error
	// Get returns a reader for the given key.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object at the given key.
	Delete(ctx context.Context, key string) error
}

// Config holds the configuration for the blob store.
type Config struct {
	// Provider is the storage backend: "s3" or "fs" (filesystem).
	Provider string
	// S3 settings
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3UseSSL          bool
	S3ForcePathStyle  bool
	// Filesystem settings
	FSBasePath string
}

// New creates a new Store based on the configuration.
func New(cfg Config) (Store, error) {
	switch cfg.Provider {
	case "s3":
		return newS3Store(cfg)
	case "fs", "filesystem":
		return newFSStore(cfg)
	case "":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported blob storage provider: %q", cfg.Provider)
	}
}
