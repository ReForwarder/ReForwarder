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

	"github.com/goposta/posta/internal/services/inbound/verify"
)

// ARFTXTLabel is the RFC 6591 §4 DNS label: the abuse-reporting addresses for
// a domain are published as TXT records at _arf.<domain>.
const ARFTXTLabel = "_arf"

// TXTLookup resolves one name's TXT records (the production seam is the
// system resolver; tests inject fakes).
type TXTLookup func(ctx context.Context, name string) ([]string, error)

// DiscoverAddresses reads the reported domain's live _arf TXT record and
// returns its mailto abuse-reporting addresses. DNS failures yield nothing
// (the domain is skipped this pass — never guessed), mirroring the TLS-RPT /
// DMARC send precedents' DNS-only rule.
func DiscoverAddresses(ctx context.Context, lookup TXTLookup, domain string) ([]string, error) {
	if lookup == nil {
		lookup = func(ctx context.Context, name string) ([]string, error) {
			return verify.NetResolver{}.LookupTXT(ctx, name)
		}
	}
	txts, err := lookup(ctx, ARFTXTLabel+"."+domain)
	if err != nil {
		return nil, fmt.Errorf("arf: _arf txt lookup for %s: %w", domain, err)
	}
	return MailtoAddresses(txts), nil
}
