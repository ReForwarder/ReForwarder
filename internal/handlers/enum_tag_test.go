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
	"reflect"
	"testing"
)

func assertEnumTagsAreStrings(t *testing.T, v any) {
	t.Helper()
	walkEnumTags(t, reflect.TypeOf(v), "")
}

func walkEnumTags(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := field.Name
		if path != "" {
			name = path + "." + name
		}

		if enum, ok := field.Tag.Lookup("enum"); ok {
			kind := field.Type.Kind()
			if kind == reflect.Slice {
				kind = field.Type.Elem().Kind()
			}
			if kind != reflect.String {
				t.Errorf("%s has enum:%q but kind %s; okapi only validates enum on string fields, "+
					"so every request to this endpoint fails to bind", name, enum, field.Type)
			}
		}

		if field.Type.Kind() == reflect.Struct {
			walkEnumTags(t, field.Type, name)
		}
	}
}

// enumCheckedRequests are the request structs the binding guards walk. Add new
// request types here.
var enumCheckedRequests = []any{
	CreateFormRequest{},
	UpdateFormRequest{},
	FormIDRequest{},
	MessageListRequest{},
	UpdateMessageStateRequest{},
	AssignMessageRequest{},
	MarkSpamRequest{},
	ReplyMessageRequest{},
	MessageAnalyticsRequest{},
	CreateMessageFilterRequest{},
	UpdateMessageFilterRequest{},
	TestMessageFilterRequest{},
	AdminListDomainsRequest{},
	AdminSetDomainVerificationRequest{},
	ListNotificationsRequest{},
	CreateAnnouncementRequest{},
	NotificationIDsRequest{},
}

func TestRequestEnumTagsOnlyOnStrings(t *testing.T) {
	for _, req := range enumCheckedRequests {
		assertEnumTagsAreStrings(t, req)
	}
}

// okapi resolves a request's body by looking for a field named Body or tagged
// json:"body", then calls Bind on that field — which applies the same rule to
// the body's own fields. A payload with a field called "body" therefore binds as
// an empty struct: no JSON is decoded, and the failure surfaces as every
// required field being missing rather than as anything pointing at the cause.
//
// This walks the same request structs and fails on the shape that triggers it.
func TestRequestBodiesHaveNoBodyField(t *testing.T) {
	for _, req := range enumCheckedRequests {
		typ := reflect.TypeOf(req)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Name != "Body" && field.Tag.Get("json") != "body" {
				continue
			}
			body := field.Type
			for body.Kind() == reflect.Pointer {
				body = body.Elem()
			}
			if body.Kind() != reflect.Struct {
				continue
			}
			for j := 0; j < body.NumField(); j++ {
				inner := body.Field(j)
				if inner.Name == "Body" || inner.Tag.Get("json") == "body" {
					t.Errorf("%s.Body.%s is named Body or tagged json:\"body\"; okapi will treat it "+
						"as another body envelope and bind nothing, so every request to this endpoint "+
						"fails with the required fields missing", typ.Name(), inner.Name)
				}
			}
		}
	}
}

func TestParseNotifyMode(t *testing.T) {
	valid := map[string]string{
		"immediate": "immediate",
		"HOURLY":    "hourly",
		" daily ":   "daily",
		"off":       "off",
	}
	for in, want := range valid {
		got, ok := parseNotifyMode(in)
		if !ok || string(got) != want {
			t.Fatalf("parseNotifyMode(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}

	for _, in := range []string{"", "weekly", "nonsense"} {
		if _, ok := parseNotifyMode(in); ok {
			t.Fatalf("parseNotifyMode(%q) reported valid", in)
		}
	}
}
