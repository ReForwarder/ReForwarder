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

package messages

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNonceInvalid = errors.New("invalid nonce")
	ErrNonceExpired = errors.New("nonce expired")
	ErrNonceReplay  = errors.New("nonce already used")
	ErrNonceTooFast = errors.New("submitted too quickly")
)

const nonceTTL = 30 * time.Minute

type Nonce struct {
	Value     string `json:"nonce"`
	ExpiresIn int    `json:"expires_in"`
}

func (s *Service) IssueNonce(ctx context.Context, formKey string) (*Nonce, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	issued := time.Now().Unix()
	body := fmt.Sprintf("%s.%d.%s", formKey, issued, base64.RawURLEncoding.EncodeToString(raw))
	token := body + "." + sign(s.hmacKey, body)

	return &Nonce{Value: token, ExpiresIn: int(nonceTTL.Seconds())}, nil
}

func (s *Service) VerifyNonce(ctx context.Context, formKey, token string, minFillSeconds int) error {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return ErrNonceInvalid
	}
	body := strings.Join(parts[:3], ".")
	if !hmac.Equal([]byte(sign(s.hmacKey, body)), []byte(parts[3])) {
		return ErrNonceInvalid
	}
	if parts[0] != formKey {
		return ErrNonceInvalid
	}

	issued, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ErrNonceInvalid
	}
	age := time.Since(time.Unix(issued, 0))
	if age > nonceTTL || age < 0 {
		return ErrNonceExpired
	}
	if minFillSeconds > 0 && age < time.Duration(minFillSeconds)*time.Second {
		return ErrNonceTooFast
	}

	if s.redis != nil {
		key := "form:nonce:" + sign(s.hmacKey, token)
		set, err := s.redis.SetNX(ctx, key, 1, nonceTTL).Result()
		if err == nil && !set {
			return ErrNonceReplay
		}
	}
	return nil
}

func sign(key []byte, body string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
