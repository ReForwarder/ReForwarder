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

package arf

import (
	"context"
	"fmt"
)

// Sender delivers one generated outbound feedback report. The production
// adapter (built in cmd wiring) stores the report as a system notification
// that the outbound pipeline relays verbatim — queued, signed, retried with
// the standard backoff, never through the tenant send path; tests inject
// fakes.
type Sender interface {
	SendReport(ctx context.Context, req *OutboundReportRequest) error
}

// SenderFunc adapts a function onto Sender.
type SenderFunc func(ctx context.Context, req *OutboundReportRequest) error

// SendReport implements Sender.
func (f SenderFunc) SendReport(ctx context.Context, req *OutboundReportRequest) error {
	if f == nil {
		return fmt.Errorf("arf: no report sender wired")
	}
	return f(ctx, req)
}
