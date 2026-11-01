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

package notification

import (
	"testing"

	"github.com/goposta/posta/internal/models"
)

func TestTemplateEnabled(t *testing.T) {
	off := &models.UserSetting{
		DailyReport:             false,
		NotifyBounceAlerts:      false,
		NotifyAPIKeyExpiry:      false,
		NotifyWorkspaceActivity: false,
	}
	on := &models.UserSetting{
		DailyReport:             true,
		NotifyBounceAlerts:      true,
		NotifyAPIKeyExpiry:      true,
		NotifyWorkspaceActivity: true,
	}

	gated := map[string]struct{ enabledFlag bool }{
		TemplateDailyReport:  {},
		TemplateBounceAlert:  {},
		TemplateAPIKeyExpiry: {},
		TemplateRoleChanged:  {},
	}
	for tmpl := range gated {
		if templateEnabled(tmpl, off) {
			t.Errorf("%s: want disabled when its flag is off", tmpl)
		}
		if !templateEnabled(tmpl, on) {
			t.Errorf("%s: want enabled when its flag is on", tmpl)
		}
	}

	for _, tmpl := range []string{
		TemplateLoginAlert, TemplateTwoFactorChange, TemplateAccountDeletion,
		TemplatePasswordChanged, TemplateWelcome, TemplateEmailVerify,
	} {
		if !templateEnabled(tmpl, off) {
			t.Errorf("%s: must always be allowed (not user-toggleable)", tmpl)
		}
	}

	if !templateEnabled("does_not_exist", off) {
		t.Error("unknown template should default to allowed")
	}
}
