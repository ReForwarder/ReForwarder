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

package repositories

import (
	"strings"
	"testing"
	"time"
)

func TestParseSearchQuery(t *testing.T) {
	date := func(s string) *time.Time {
		tt, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad test date %q: %v", s, err)
		}
		return &tt
	}
	rfc := func(s string) *time.Time {
		tt, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("bad test timestamp %q: %v", s, err)
		}
		return &tt
	}

	tests := []struct {
		name string
		q    string
		want EmailFilter
	}{
		{
			name: "empty",
			q:    "",
			want: EmailFilter{},
		},
		{
			name: "bare words become free text",
			q:    "weekly invoice",
			want: EmailFilter{Text: "weekly invoice"},
		},
		{
			name: "bare words merge into subject when subject: is given",
			q:    "subject:weekly invoice",
			want: EmailFilter{Subject: "weekly invoice"},
		},
		{
			name: "from and to operators",
			q:    "from:alice@x.com to:bob@y.com",
			want: EmailFilter{Sender: "alice@x.com", Recipient: "bob@y.com"},
		},
		{
			name: "quoted subject keeps spaces",
			q:    `subject:"weekly report"`,
			want: EmailFilter{Subject: "weekly report"},
		},
		{
			name: "has attachment flag (singular and plural)",
			q:    "has:attachments",
			want: EmailFilter{HasAttachment: true},
		},
		{
			name: "status is lowercased",
			q:    "status:Sent",
			want: EmailFilter{Statuses: []string{"sent"}},
		},
		{
			name: "status accepts a comma-separated list",
			q:    "status:sent,failed",
			want: EmailFilter{Statuses: []string{"sent", "failed"}},
		},
		{
			name: "after is parsed as start of day",
			q:    "after:2026-07-01",
			want: EmailFilter{After: date("2026-07-01")},
		},
		{
			name: "before is inclusive (next day) for date-only",
			q:    "before:2026-07-20",
			want: EmailFilter{Before: date("2026-07-21")},
		},
		{
			name: "before with RFC3339 instant is used as-is (no day rounding)",
			q:    "before:2026-07-20T12:00:00Z",
			want: EmailFilter{Before: rfc("2026-07-20T12:00:00Z")},
		},
		{
			name: "after with RFC3339 instant is used as-is",
			q:    "after:2026-07-01T08:30:00Z",
			want: EmailFilter{After: rfc("2026-07-01T08:30:00Z")},
		},
		{
			name: "invalid date is ignored",
			q:    "after:not-a-date",
			want: EmailFilter{},
		},
		{
			name: "operators mixed with free text",
			q:    `from:alice subject:report has:attachment status:failed after:2026-01-01 urgent`,
			want: EmailFilter{
				Sender:        "alice",
				Subject:       "report urgent",
				Statuses:      []string{"failed"},
				HasAttachment: true,
				After:         date("2026-01-01"),
			},
		},
		{
			name: "template operator",
			q:    "template:welcome-email",
			want: EmailFilter{Template: "welcome-email"},
		},
		{
			name: "provider, server and key operators",
			q:    `provider:"google workspace" server:smtp.acme.com key:billing`,
			want: EmailFilter{Providers: []string{"google workspace"}, Servers: []string{"smtp.acme.com"}, APIKeys: []string{"billing"}},
		},
		{
			name: "provider, server and key accept comma-separated lists",
			q:    `provider:Gmail,Yandex server:a.com,b.com key:"Billing, ,Ops"`,
			want: EmailFilter{Providers: []string{"Gmail", "Yandex"}, Servers: []string{"a.com", "b.com"}, APIKeys: []string{"Billing", "Ops"}},
		},
		{
			name: "unknown prefix falls back to free text",
			q:    "cc:someone",
			want: EmailFilter{Text: "cc:someone"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSearchQuery(tc.q)
			if got.Sender != tc.want.Sender {
				t.Errorf("Sender = %q, want %q", got.Sender, tc.want.Sender)
			}
			if got.Recipient != tc.want.Recipient {
				t.Errorf("Recipient = %q, want %q", got.Recipient, tc.want.Recipient)
			}
			if got.Subject != tc.want.Subject {
				t.Errorf("Subject = %q, want %q", got.Subject, tc.want.Subject)
			}
			if got.Text != tc.want.Text {
				t.Errorf("Text = %q, want %q", got.Text, tc.want.Text)
			}
			if strings.Join(got.Providers, ",") != strings.Join(tc.want.Providers, ",") {
				t.Errorf("Providers = %v, want %v", got.Providers, tc.want.Providers)
			}
			if strings.Join(got.Servers, ",") != strings.Join(tc.want.Servers, ",") {
				t.Errorf("Servers = %v, want %v", got.Servers, tc.want.Servers)
			}
			if strings.Join(got.APIKeys, ",") != strings.Join(tc.want.APIKeys, ",") {
				t.Errorf("APIKeys = %v, want %v", got.APIKeys, tc.want.APIKeys)
			}
			if got.Template != tc.want.Template {
				t.Errorf("Template = %q, want %q", got.Template, tc.want.Template)
			}
			if strings.Join(got.Statuses, ",") != strings.Join(tc.want.Statuses, ",") {
				t.Errorf("Statuses = %v, want %v", got.Statuses, tc.want.Statuses)
			}
			if got.HasAttachment != tc.want.HasAttachment {
				t.Errorf("HasAttachment = %v, want %v", got.HasAttachment, tc.want.HasAttachment)
			}
			if !timeEq(got.After, tc.want.After) {
				t.Errorf("After = %v, want %v", got.After, tc.want.After)
			}
			if !timeEq(got.Before, tc.want.Before) {
				t.Errorf("Before = %v, want %v", got.Before, tc.want.Before)
			}
		})
	}
}

func TestParseSearchQueryLengthCap(t *testing.T) {
	// A very long term must not build an unbounded predicate value.
	f := ParseSearchQuery("subject:" + strings.Repeat("a", 4000))
	if len(f.Subject) > maxSearchQueryLen {
		t.Errorf("subject not capped: len=%d, want <= %d", len(f.Subject), maxSearchQueryLen)
	}
	// A huge comma-separated status list must be bounded too.
	f = ParseSearchQuery("status:" + strings.Repeat("sent,", 4000))
	if len(f.Statuses) > maxSearchQueryLen {
		t.Errorf("statuses not capped: len=%d", len(f.Statuses))
	}
}

func TestOrderClause(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "created_at DESC"},                       // default preserved (unchanged)
		{"created_at", "created_at ASC NULLS LAST"},   // explicit asc
		{"-created_at", "created_at DESC NULLS LAST"}, // explicit desc
		{"sent_at", "sent_at ASC NULLS LAST"},
		{"-sent_at", "sent_at DESC NULLS LAST"},
		{"subject", "subject ASC NULLS LAST"},
		{"sender", "sender ASC NULLS LAST"},
		{"template", "template_name ASC NULLS LAST"}, // mapped column name
		{"-template", "template_name DESC NULLS LAST"},
		{"status", "status ASC NULLS LAST"},
		{"STATUS", "status ASC NULLS LAST"},                  // case-insensitive key
		{"bogus", "created_at DESC"},                         // unknown -> default
		{"-", "created_at DESC"},                             // lone dash -> default
		{"created_at; DROP TABLE emails", "created_at DESC"}, // injection -> default
		{"sender) --", "created_at DESC"},                    // injection -> default
	}
	for _, tc := range tests {
		if got := orderClause(tc.in); got != tc.want {
			t.Errorf("orderClause(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLikePattern(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"substring by default", "example.com", "%example.com%"},
		{"leading wildcard", "*@example.com", "%@example.com"},
		{"trailing wildcard", "john*", "john%"},
		{"case folded", "Alice@Acme.com", "%alice@acme.com%"},
		{"escapes percent", "50%", "%50\\%%"},
		{"escapes underscore", "a_b", "%a\\_b%"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := likePattern(tc.in); got != tc.want {
				t.Errorf("likePattern(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func timeEq(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
