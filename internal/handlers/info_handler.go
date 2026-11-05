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
	"github.com/goposta/posta/internal/config"
	"github.com/jkaninda/okapi"
)

type AppInfo struct {
	Name        string `json:"name" example:"Posta"`
	Version     string `json:"version" example:"v1.0.0"`
	CommitID    string `json:"commit_id" example:"abc1234"`
	OpenAPIDocs bool   `json:"openapi_docs" example:"true"`
	// Features mirrors the env-gated subsystems (Section-11 gate): the
	// dashboard hides the nav entries whose pages would only error when the
	// subsystem is disabled (Messages/Forms/Spam Filters, Inbound Emails).
	Features AppFeatures `json:"features"`
}

// AppFeatures reports which feature-flagged subsystems this install enables.
// Every field defaults to false so the dashboard hides the corresponding
// pages unless the backend says otherwise.
type AppFeatures struct {
	MessagesEnabled bool `json:"messages_enabled" example:"false"`
	InboundEnabled  bool `json:"inbound_enabled" example:"false"`
	RelayEnabled    bool `json:"relay_enabled" example:"false"`
	MetricsEnabled  bool `json:"metrics_enabled" example:"false"`
}

func Info(c *okapi.Context) error {
	cfg := config.New()
	return ok(c, AppInfo{
		Name:        "Posta",
		Version:     config.Version,
		CommitID:    config.CommitID,
		OpenAPIDocs: cfg.OpenAPIDocs,
		Features: AppFeatures{
			MessagesEnabled: cfg.MessagesEnabled,
			InboundEnabled:  cfg.InboundEnabled,
			RelayEnabled:    cfg.SMTPRelayEnabled,
			MetricsEnabled:  cfg.MetricsEnabled,
		},
	})
}
