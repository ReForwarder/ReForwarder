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

package settings

import (
	"testing"
	"time"
)

// providerWith builds a provider over a fixed cache. The TTL is long enough
// that the database is never touched, so the repo can stay nil.
func providerWith(values map[string]string) *Provider {
	return &Provider{cache: values, cacheTTL: time.Hour, loadedAt: time.Now()}
}

func TestEmailTrackingDefaultsToTrue(t *testing.T) {
	// Installations upgraded without the key keep the tracking behavior.
	p := providerWith(map[string]string{"maintenance_mode": "true"})
	if !p.EmailTrackingEnabled() {
		t.Fatal("missing key must default to true")
	}
}

func TestEmailTrackingReadsStoredValue(t *testing.T) {
	off := providerWith(map[string]string{"email_tracking_enabled": "false"})
	if off.EmailTrackingEnabled() {
		t.Fatal("stored false must disable tracking")
	}

	on := providerWith(map[string]string{"email_tracking_enabled": "true"})
	if !on.EmailTrackingEnabled() {
		t.Fatal("stored true must enable tracking")
	}
}

func TestEmailTrackingInvalidValueFallsBackToDefault(t *testing.T) {
	p := providerWith(map[string]string{"email_tracking_enabled": "banana"})
	if !p.EmailTrackingEnabled() {
		t.Fatal("unparseable value must fall back to the default (true)")
	}
}

func TestCampaignClickTrackingIsAliasForEmailTracking(t *testing.T) {
	// CampaignClickTrackingEnabled is a deprecated alias for
	// EmailTrackingEnabled (the old key was renamed): it must return exactly
	// what the new accessor returns.
	off := providerWith(map[string]string{"email_tracking_enabled": "false"})
	if off.CampaignClickTrackingEnabled() {
		t.Fatal("alias must read the email_tracking_enabled key")
	}
	on := providerWith(map[string]string{"email_tracking_enabled": "true"})
	if !on.CampaignClickTrackingEnabled() {
		t.Fatal("alias must read the email_tracking_enabled key")
	}
}
