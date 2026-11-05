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

// Package webauthn implements passkey support (R-19) on top of
// github.com/go-webauthn/webauthn. The Service adapts Posta's user and
// credential models to the library's expectations, persists the resulting
// credentials through a CredentialStore, and keeps the begin/finish ceremony
// state (challenges, and the passkey-verified marker gating the second-factor
// step) in a ChallengeStore so a challenge is single-use server-side.
//
// Protocol correctness (signature verification, challenge binding, origin
// checks, clone detection) is the library's job; this package owns the wiring:
// keying challenges to the browser session or the login email, converting the
// stored credential rows into webauthn.Credential values, and stamping
// last-used/counter state back after a successful assertion.
package webauthn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	lib "github.com/go-webauthn/webauthn/webauthn"
	"github.com/goposta/posta/internal/models"
)

const (
	// challengeTTL bounds how long a begun ceremony may stay open. Five
	// minutes comfortably covers a user reaching for their authenticator.
	challengeTTL = 5 * time.Minute
	// pendingTOTPTTL bounds the passkey-verified marker: after that window the
	// client starts over with the passkey.
	pendingTOTPTTL = 3 * time.Minute
)

// Config carries the WebAuthn Relying Party identity. RPID must equal the
// dashboard origin's effective domain (no scheme, no port) — the browser
// refuses ceremonies whose scope does not match it — and RPOrigins lists the
// exact origins assertions may come from.
type Config struct {
	RPID          string
	RPDisplayName string
	RPOrigins     []string
}

// Service wraps the library's WebAuthn instance with Posta persistence.
type Service struct {
	wa     *lib.WebAuthn
	creds  CredentialStore
	chall  ChallengeStore
}

// New builds the service. An error means the configuration itself is invalid
// (empty RPID, malformed origin) — callers should start without passkeys.
func New(cfg Config, creds CredentialStore, chall ChallengeStore) (*Service, error) {
	if cfg.RPID == "" || len(cfg.RPOrigins) == 0 {
		return nil, errors.New("webauthn: RPID and RPOrigins are required")
	}
	wa, err := lib.New(&lib.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn: %w", err)
	}
	return &Service{wa: wa, creds: creds, chall: chall}, nil
}

// webAuthnUser adapts models.User plus the user's stored credentials to the
// library's User interface. The user handle is the user ID in big-endian
// bytes: stable for the account's lifetime, so the authenticator keeps
// binding assertions to the same handle it registered under.
type webAuthnUser struct {
	user *models.User
	creds []lib.Credential
}

func (w webAuthnUser) WebAuthnID() []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(w.user.ID))
	return buf
}

func (w webAuthnUser) WebAuthnName() string { return w.user.Email }

func (w webAuthnUser) WebAuthnDisplayName() string {
	if w.user.Name != "" {
		return w.user.Name
	}
	return w.user.Email
}

func (w webAuthnUser) WebAuthnCredentials() []lib.Credential { return w.creds }

// libraryCredentials converts stored rows into the library's shape.
func libraryCredentials(rows []models.WebAuthnCredential) []lib.Credential {
	out := make([]lib.Credential, 0, len(rows))
	for i := range rows {
		out = append(out, libraryCredential(&rows[i]))
	}
	return out
}

func libraryCredential(row *models.WebAuthnCredential) lib.Credential {
	id, _ := base64.RawURLEncoding.DecodeString(row.ID)
	pk, _ := base64.StdEncoding.DecodeString(row.PublicKey)
	transport := make([]protocol.AuthenticatorTransport, 0, len(row.Transport))
	for _, t := range row.Transport {
		transport = append(transport, protocol.AuthenticatorTransport(t))
	}
	return lib.Credential{
		ID:              id,
		PublicKey:       pk,
		AttestationType: row.AttestationType,
		Transport:       transport,
		Authenticator:   lib.Authenticator{SignCount: row.SignCount},
	}
}

// newCredentialRow converts a library credential plus the ceremony outcome into
// a stored row.
func newCredentialRow(userID uint, cred *lib.Credential) *models.WebAuthnCredential {
	transport := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transport = append(transport, string(t))
	}
	return &models.WebAuthnCredential{
		ID:              base64.RawURLEncoding.EncodeToString(cred.ID),
		UserID:          userID,
		PublicKey:       base64.StdEncoding.EncodeToString(cred.PublicKey),
		AttestationType: cred.AttestationType,
		Transport:       transport,
		SignCount:       cred.Authenticator.SignCount,
	}
}

// BeginRegistration starts a registration ceremony for the authenticated user.
// The ceremony state is stored under ceremonyKey — the caller's session JTI —
// so the finish step binds to the same browser session.
func (s *Service) BeginRegistration(ctx context.Context, user *models.User, ceremonyKey string) (*protocol.CredentialCreation, error) {
	existing, err := s.creds.ListByUser(user.ID)
	if err != nil {
		return nil, fmt.Errorf("webauthn: load credentials: %w", err)
	}
	creation, sessionData, err := s.wa.BeginRegistration(webAuthnUser{user: user, creds: libraryCredentials(existing)})
	if err != nil {
		return nil, fmt.Errorf("webauthn: begin registration: %w", err)
	}
	if err := s.storeChallenge(ctx, "register:"+ceremonyKey, sessionData); err != nil {
		return nil, err
	}
	return creation, nil
}

// FinishRegistration validates the client's attestation response (the raw
// ceremony body) and stores the resulting credential. The challenge is
// consumed — a replayed finish fails on the missing ceremony state.
func (s *Service) FinishRegistration(ctx context.Context, user *models.User, ceremonyKey string, body io.Reader) (*models.WebAuthnCredential, error) {
	sessionData, err := s.takeChallenge(ctx, "register:"+ceremonyKey)
	if err != nil {
		return nil, errors.New("webauthn: registration ceremony expired or already used, start again")
	}
	existing, err := s.creds.ListByUser(user.ID)
	if err != nil {
		return nil, fmt.Errorf("webauthn: load credentials: %w", err)
	}
	cred, err := s.wa.FinishRegistration(webAuthnUser{user: user, creds: libraryCredentials(existing)}, *sessionData, requestFrom(body))
	if err != nil {
		return nil, fmt.Errorf("webauthn: %w", err)
	}

	row := newCredentialRow(user.ID, cred)
	if err := s.creds.Create(row); err != nil {
		return nil, fmt.Errorf("webauthn: store credential: %w", err)
	}
	return row, nil
}

// BeginLogin starts an assertion ceremony for the user. The ceremony state is
// stored under the (lower-cased) email — the login flow's only identifier at
// this point — so the finish step can find it without a session.
func (s *Service) BeginLogin(ctx context.Context, user *models.User) (*protocol.CredentialAssertion, error) {
	existing, err := s.creds.ListByUser(user.ID)
	if err != nil {
		return nil, fmt.Errorf("webauthn: load credentials: %w", err)
	}
	if len(existing) == 0 {
		return nil, errors.New("webauthn: no passkey registered for this account")
	}
	assertion, sessionData, err := s.wa.BeginLogin(webAuthnUser{user: user, creds: libraryCredentials(existing)})
	if err != nil {
		return nil, fmt.Errorf("webauthn: begin login: %w", err)
	}
	if err := s.storeChallenge(ctx, "login:"+user.Email, sessionData); err != nil {
		return nil, err
	}
	return assertion, nil
}

// FinishLogin validates the client's assertion response (the raw credential
// JSON, extracted by the handler from its request wrapper) and returns the
// used credential with the authenticator's fresh sign count stamped back into
// storage.
func (s *Service) FinishLogin(ctx context.Context, user *models.User, body io.Reader) (*models.WebAuthnCredential, error) {
	sessionData, err := s.takeChallenge(ctx, "login:"+user.Email)
	if err != nil {
		return nil, errors.New("webauthn: login ceremony expired or already used, start again")
	}
	existing, err := s.creds.ListByUser(user.ID)
	if err != nil {
		return nil, fmt.Errorf("webauthn: load credentials: %w", err)
	}
	cred, err := s.wa.FinishLogin(webAuthnUser{user: user, creds: libraryCredentials(existing)}, *sessionData, requestFrom(body))
	if err != nil {
		return nil, fmt.Errorf("webauthn: %w", err)
	}

	// Persist the authenticator's counter advance and the last-used stamp so
	// clone detection keeps working across logins.
	credentialID := base64.RawURLEncoding.EncodeToString(cred.ID)
	for i := range existing {
		if existing[i].ID == credentialID {
			existing[i].SignCount = cred.Authenticator.SignCount
			now := time.Now()
			existing[i].LastUsedAt = &now
			if err := s.creds.Save(&existing[i]); err != nil {
				return nil, fmt.Errorf("webauthn: update credential: %w", err)
			}
			return &existing[i], nil
		}
	}
	return nil, errors.New("webauthn: credential vanished mid-ceremony")
}

// ListCredentials returns the user's registered passkeys.
func (s *Service) ListCredentials(userID uint) ([]models.WebAuthnCredential, error) {
	return s.creds.ListByUser(userID)
}

// RenameCredential renames the user's own credential.
func (s *Service) RenameCredential(userID uint, credentialID, name string) error {
	return s.creds.Rename(userID, credentialID, name)
}

// DeleteCredential removes the user's own credential.
func (s *Service) DeleteCredential(userID uint, credentialID string) error {
	return s.creds.Delete(userID, credentialID)
}

// MarkTwoFactorPending records that the user identified by userID has just
// cleared the passkey step and still owes the second factor. The returned
// one-shot token, delivered to the client, redeems exactly one second-factor
// completion within pendingTOTPTTL.
func (s *Service) MarkTwoFactorPending(ctx context.Context, userID uint) (string, error) {
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("webauthn: pending token: %w", err)
	}
	value, err := json.Marshal(map[string]any{"user_id": userID})
	if err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(token)
	if err := s.chall.Save(ctx, "pending:"+key, value, pendingTOTPTTL); err != nil {
		return "", fmt.Errorf("webauthn: store pending marker: %w", err)
	}
	return key, nil
}

// TakeTwoFactorPending consumes a pending marker and reports the user it
// stands for. Unknown, expired or already-used tokens are refused.
func (s *Service) TakeTwoFactorPending(ctx context.Context, token string) (uint, error) {
	payload, err := s.chall.Take(ctx, "pending:"+token)
	if err != nil {
		return 0, errors.New("webauthn: second-factor window expired, sign in again")
	}
	var marker struct {
		UserID uint `json:"user_id"`
	}
	if err := json.Unmarshal(payload, &marker); err != nil || marker.UserID == 0 {
		return 0, errors.New("webauthn: invalid second-factor marker")
	}
	return marker.UserID, nil
}

func (s *Service) storeChallenge(ctx context.Context, key string, sessionData *lib.SessionData) error {
	payload, err := json.Marshal(sessionData)
	if err != nil {
		return fmt.Errorf("webauthn: encode ceremony state: %w", err)
	}
	if err := s.chall.Save(ctx, key, payload, challengeTTL); err != nil {
		return fmt.Errorf("webauthn: store ceremony state: %w", err)
	}
	return nil
}

func (s *Service) takeChallenge(ctx context.Context, key string) (*lib.SessionData, error) {
	payload, err := s.chall.Take(ctx, key)
	if err != nil {
		return nil, err
	}
	var sessionData lib.SessionData
	if err := json.Unmarshal(payload, &sessionData); err != nil {
		return nil, fmt.Errorf("webauthn: decode ceremony state: %w", err)
	}
	return &sessionData, nil
}
