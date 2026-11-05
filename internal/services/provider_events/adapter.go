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

package provider_events

import (
	"net/http"
	"sort"
	"strings"
)

// ProviderAdapter adapts one upstream email provider to the normalized event
// vocabulary. Implementations live one per file (ses.go, sendgrid.go,
// postmark.go, mailgun.go).
type ProviderAdapter interface {
	// Name is the lowercase provider slug used as the {provider} path
	// parameter and as the normalized Provider value.
	Name() string
	// VerifySignature checks the provider's webhook authentication for the
	// exact raw request body and headers. Returning an error makes the HTTP
	// layer answer 401 without parsing anything.
	VerifySignature(rawBody []byte, headers http.Header, config ProviderConfig) error
	// ParseEvent converts the raw payload into normalized events. A payload
	// the adapter recognizes but that carries no events (SES subscription
	// confirmations, SendGrid "processed", Postmark SubscriptionChange, …)
	// yields an empty slice with a nil error.
	ParseEvent(rawBody []byte) ([]NormalizedEvent, error)
}

// ProviderConfig carries the provider's webhook signing material. Secrets come
// from environment variables (POSTA_PROVIDER_SES_SECRET etc.), so an empty
// Secret means the integration is not configured and the endpoint answers 501.
type ProviderConfig struct {
	Secret string
}

// adapters is the provider registry backing the {provider} path dispatch.
// Register a new adapter here and it is routable immediately.
var adapters = map[string]ProviderAdapter{
	"ses":      &SESAdapter{},
	"sendgrid": &SendGridAdapter{},
	"postmark": &PostmarkAdapter{},
	"mailgun":  &MailgunAdapter{},
}

// Adapter returns the adapter registered for a provider slug, or nil.
func Adapter(name string) ProviderAdapter {
	if name == "" {
		return nil
	}
	return adapters[strings.ToLower(name)]
}

// ProviderNames returns the registered provider slugs, sorted.
func ProviderNames() []string {
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
