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

package errorhandlers

import (
	"github.com/goposta/posta/internal/dto"
	"github.com/jkaninda/okapi"
)

// CustomErrorHandler returns an okapi.ErrorHandler that formats errors
func CustomErrorHandler() okapi.ErrorHandler {
	return func(c *okapi.Context, code int, message string, err error) error {
		return c.JSON(code, dto.ErrorResponseBody{
			Success: false,
			Data:    nil,
			Error: &dto.ErrorInfo{
				Code:    httpStatusToCode(code),
				Error:   err.Error(),
				Message: message,
			},
		})
	}
}

func httpStatusToCode(status int) string {
	switch status {
	case 400:
		return "BAD_REQUEST"
	case 401:
		return "UNAUTHORIZED"
	case 403:
		return "FORBIDDEN"
	case 404:
		return "NOT_FOUND"
	case 405:
		return "METHOD_NOT_ALLOWED"
	case 409:
		return "CONFLICT"
	case 422:
		return "UNPROCESSABLE_ENTITY"
	case 429:
		return "TOO_MANY_REQUESTS"
	case 500:
		return "INTERNAL_SERVER_ERROR"
	case 502:
		return "BAD_GATEWAY"
	case 503:
		return "SERVICE_UNAVAILABLE"
	default:
		return "ERROR"
	}
}
