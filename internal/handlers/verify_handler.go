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

package handlers

import (
	"errors"

	"github.com/goposta/posta/internal/services/verifier"
	"github.com/jkaninda/okapi"
)

// VerifyHandler exposes the email-verification endpoint.
type VerifyHandler struct {
	svc *verifier.Service
}

func NewVerifyHandler(svc *verifier.Service) *VerifyHandler {
	return &VerifyHandler{svc: svc}
}

// VerifyAddressRequest is the body for POST /emails/verify. The email is
// validated with format:"email", so a syntactically malformed address is
// rejected with a 400 before the handler runs; the verifier still re-checks
// syntax as a safety net.
type VerifyAddressRequest struct {
	Fresh bool `query:"fresh" doc:"Bypass the cache and re-check the address"`
	Body  struct {
		Email string `json:"email" required:"true" format:"email" doc:"Email address to verify"`
	} `json:"body"`
}

// Verify checks whether an email address is valid/deliverable.
func (h *VerifyHandler) Verify(c *okapi.Context, req *VerifyAddressRequest) error {
	if h.svc == nil || !h.svc.Enabled() {
		return c.AbortNotFound("email verification is disabled")
	}

	res, err := h.svc.Verify(c.Request().Context(), getScope(c), req.Body.Email, req.Fresh)
	if err != nil {
		if errors.Is(err, verifier.ErrRateLimited) {
			return c.AbortTooManyRequests("email verification rate limit exceeded")
		}
		return c.AbortInternalServerError("failed to verify email")
	}
	return ok(c, res)
}
