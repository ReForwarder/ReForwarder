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
	"time"
)

// Search operator keys and the DB columns they map to. Kept as constants so the
// same literals aren't scattered across the parser and the sort whitelist.
const (
	opFrom     = "from"
	opTo       = "to"
	opSubject  = "subject"
	opTemplate = "template"
	opProvider = "provider"
	opServer   = "server"
	opKey      = "key"
	opStatus   = "status"
	opHas      = "has"
	opAfter    = "after"
	opBefore   = "before"

	colCreatedAt    = "created_at"
	colSentAt       = "sent_at"
	colSender       = "sender"
	colTemplateName = "template_name"
)

// EmailFilter captures optional list filters for outbound email queries.
type EmailFilter struct {
	Sender        string
	Recipient     string
	Subject       string
	Text          string
	Template      string
	Providers     []string
	Servers       []string
	APIKeys       []string
	Statuses      []string
	HasAttachment bool
	After         *time.Time
	Before        *time.Time
}

// maxSearchQueryLen bounds the raw search string before parsing so a single
// request cannot build pathologically large predicates (huge status lists, many
// wildcards, or very long LIKE terms) against the unindexed email columns.
const maxSearchQueryLen = 512

// ParseSearchQuery turns a Twitter-style search string into an EmailFilter.
// Supported operators: from: to: subject: template: provider: server: key:
// status: has:attachment after: before:
// provider:, server:, key: and status: accept comma-separated lists.
// Bare words (no known prefix) match the subject or any recipient, or narrow
// Subject when subject: is given.
//
// Example:
//
//	from:alice@x.com to:bob subject:"weekly report" has:attachment after:2026-01-01 status:sent invoice
func ParseSearchQuery(q string) EmailFilter {
	if len(q) > maxSearchQueryLen {
		q = q[:maxSearchQueryLen]
	}

	var f EmailFilter
	var free []string

	for _, tok := range tokenizeSearch(q) {
		key, val, ok := strings.Cut(tok, ":")
		if !ok {
			free = append(free, tok)
			continue
		}
		switch strings.ToLower(key) {
		case opFrom:
			f.Sender = val
		case opTo:
			f.Recipient = val
		case opSubject:
			f.appendSubject(val)
		case opTemplate:
			f.Template = val
		case opProvider:
			f.Providers = splitList(val)
		case opServer:
			f.Servers = splitList(val)
		case opKey:
			f.APIKeys = splitList(val)
		case opStatus:
			// Accept a comma-separated list, e.g. status:sent,failed.
			for _, s := range strings.Split(val, ",") {
				if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
					f.Statuses = append(f.Statuses, s)
				}
			}
		case opHas:
			if v := strings.ToLower(val); v == "attachment" || v == "attachments" {
				f.HasAttachment = true
			}
		case opAfter:
			if t, _, ok := parseSearchDate(val); ok {
				f.After = &t
			}
		case opBefore:
			if t, dateOnly, ok := parseSearchDate(val); ok {
				// A date-only bound covers the whole day (created_at < next
				// midnight). An explicit RFC3339 instant is used as given.
				if dateOnly {
					t = t.Add(24 * time.Hour)
				}
				f.Before = &t
			}
		default:
			// Unknown prefix: treat the whole token as free text.
			free = append(free, tok)
		}
	}

	if len(free) > 0 {
		if f.Subject != "" {
			f.appendSubject(strings.Join(free, " "))
		} else {
			f.Text = strings.Join(free, " ")
		}
	}
	return f
}

// splitList splits a comma-separated operator value, dropping empty items.
func splitList(val string) []string {
	var out []string
	for _, s := range strings.Split(val, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (f *EmailFilter) appendSubject(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if f.Subject == "" {
		f.Subject = s
		return
	}
	f.Subject += " " + s
}

// likePattern builds a case-insensitive SQL LIKE pattern from a user term.
// A `*` acts as a wildcard (mapped to `%`); without one the term matches as a
// substring. Any real LIKE metacharacters in the input are escaped so they stay
// literal (Postgres LIKE uses `\` as the default escape).
func likePattern(term string) string {
	s := strings.ToLower(strings.TrimSpace(term))
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	if strings.Contains(s, "*") {
		return strings.ReplaceAll(s, "*", "%")
	}
	return "%" + s + "%"
}

// likeAny builds a predicate matching col against any of the terms.
func likeAny(col string, terms []string) (string, []any) {
	conds := make([]string, len(terms))
	args := make([]any, len(terms))
	for i, t := range terms {
		conds[i] = col + " LIKE ?"
		args[i] = likePattern(t)
	}
	return "(" + strings.Join(conds, " OR ") + ")", args
}

// sortColumns whitelists the user-facing sort keys to real, safe column names.
// ORDER BY is built only from these constants — user input never reaches SQL.
var sortColumns = map[string]string{
	colCreatedAt: colCreatedAt,
	colSentAt:    colSentAt,
	opSubject:    opSubject,
	colSender:    colSender,
	opTemplate:   colTemplateName,
	opStatus:     opStatus,
}

// orderClause maps an optional sort key to a safe "column DIR" ORDER BY clause.
// A leading "-" means descending. Empty or unknown input falls back to the
// default (newest first), so the current behaviour is preserved.
func orderClause(sort string) string {
	const def = "created_at DESC"
	sort = strings.TrimSpace(sort)
	if sort == "" {
		return def
	}
	desc := false
	if strings.HasPrefix(sort, "-") {
		desc = true
		sort = sort[1:]
	}
	col, ok := sortColumns[strings.ToLower(strings.TrimSpace(sort))]
	if !ok {
		return def
	}
	// NULLS LAST keeps rows with a null sort value (e.g. unsent emails when
	// sorting by sent_at) at the bottom regardless of direction.
	if desc {
		return col + " DESC NULLS LAST"
	}
	return col + " ASC NULLS LAST"
}

// tokenizeSearch splits a query on whitespace while keeping double-quoted
// values together (quotes may follow an operator prefix, e.g. subject:"a b").
func tokenizeSearch(q string) []string {
	var tokens []string
	var b strings.Builder
	inQuote := false

	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}

	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t' || r == '\n') && !inQuote:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// parseSearchDate accepts a date-only (YYYY-MM-DD) or RFC3339 timestamp. It
// reports dateOnly=true when the value carried no time component, so callers can
// apply whole-day rounding only in that case (never to an explicit instant).
func parseSearchDate(s string) (t time.Time, dateOnly bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, false
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, false, true
	}
	return time.Time{}, false, false
}
