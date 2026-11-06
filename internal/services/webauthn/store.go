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

package webauthn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goposta/posta/internal/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ErrCredentialNotFound is returned when a credential lookup or mutation
// matched no row owned by the requesting user.
var ErrCredentialNotFound = errors.New("credential not found")

// CredentialStore persists registered passkeys. The handler-level wiring is
// repo-backed (GormCredentialStore below); the interface keeps the service
// testable without a database.
type CredentialStore interface {
	// ListByUser returns the user's credentials, oldest first.
	ListByUser(userID uint) ([]models.WebAuthnCredential, error)
	// Create stores a freshly registered credential.
	Create(cred *models.WebAuthnCredential) error
	// Save updates an existing credential (sign count, last-used stamp).
	Save(cred *models.WebAuthnCredential) error
	// Rename renames a credential owned by userID. Reports
	// ErrCredentialNotFound when nothing matched.
	Rename(userID uint, credentialID, name string) error
	// Delete removes a credential owned by userID. Reports
	// ErrCredentialNotFound when nothing matched.
	Delete(userID uint, credentialID string) error
}

// GormCredentialStore is the repo-backed CredentialStore over the
// webauthn_credentials table. It deliberately lives next to the service (like
// handlers.RecoveryCodeStore does) instead of the repositories package: the
// store is the WebAuthn feature's private persistence, holding no logic any
// other subsystem needs.
type GormCredentialStore struct {
	db *gorm.DB
}

// NewGormCredentialStore builds the store on the given database handle.
func NewGormCredentialStore(db *gorm.DB) *GormCredentialStore {
	return &GormCredentialStore{db: db}
}

func (s *GormCredentialStore) ListByUser(userID uint) ([]models.WebAuthnCredential, error) {
	var creds []models.WebAuthnCredential
	err := s.db.Where("user_id = ?", userID).
		Order("created_at ASC").
		Find(&creds).Error
	return creds, err
}

func (s *GormCredentialStore) Create(cred *models.WebAuthnCredential) error {
	return s.db.Create(cred).Error
}

func (s *GormCredentialStore) Save(cred *models.WebAuthnCredential) error {
	return s.db.Save(cred).Error
}

func (s *GormCredentialStore) Rename(userID uint, credentialID, name string) error {
	res := s.db.Model(&models.WebAuthnCredential{}).
		Where("id = ? AND user_id = ?", credentialID, userID).
		Update("name", name)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func (s *GormCredentialStore) Delete(userID uint, credentialID string) error {
	res := s.db.Where("id = ? AND user_id = ?", credentialID, userID).
		Delete(&models.WebAuthnCredential{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// ChallengeStore holds the short-lived server-side state between the Begin and
// Finish steps of a WebAuthn ceremony: the challenge session data, and the
// passkey-verified marker that gates the second-factor step (R-19). Values are
// opaque JSON blobs; Take is one-shot so a challenge can never be replayed.
type ChallengeStore interface {
	// Save stores payload under key for ttl.
	Save(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	// Take returns and removes the payload stored under key. A missing or
	// expired entry reports an error.
	Take(ctx context.Context, key string) ([]byte, error)
}

const challengeKeyPrefix = "webauthn:challenge:"

// RedisChallengeStore keeps ceremony state in Redis, so challenges survive a
// process restart and multi-instance deployments share one state.
type RedisChallengeStore struct {
	redis *redis.Client
}

// NewRedisChallengeStore builds the Redis-backed store.
func NewRedisChallengeStore(client *redis.Client) *RedisChallengeStore {
	return &RedisChallengeStore{redis: client}
}

func (s *RedisChallengeStore) Save(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	return s.redis.Set(ctx, challengeKeyPrefix+key, payload, ttl).Err()
}

// Take uses GETDEL so the challenge is consumed atomically: a second finish
// attempt with the same challenge fails.
func (s *RedisChallengeStore) Take(ctx context.Context, key string) ([]byte, error) {
	payload, err := s.redis.GetDel(ctx, challengeKeyPrefix+key).Result()
	if err != nil {
		return nil, fmt.Errorf("challenge lookup failed: %w", err)
	}
	return []byte(payload), nil
}

// MemoryChallengeStore is the in-process fallback: single-instance
// deployments without Redis, and the handler tests. Entries expire lazily.
type MemoryChallengeStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	payload []byte
	expires time.Time
}

// NewMemoryChallengeStore builds the in-memory store.
func NewMemoryChallengeStore() *MemoryChallengeStore {
	return &MemoryChallengeStore{entries: map[string]memoryEntry{}}
}

func (s *MemoryChallengeStore) Save(_ context.Context, key string, payload []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = memoryEntry{payload: payload, expires: time.Now().Add(ttl)}
	return nil
}

func (s *MemoryChallengeStore) Take(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok || time.Now().After(entry.expires) {
		delete(s.entries, key)
		return nil, errors.New("challenge not found or expired")
	}
	delete(s.entries, key)
	return entry.payload, nil
}
