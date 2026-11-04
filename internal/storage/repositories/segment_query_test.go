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
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// The segment condition builder is tested by capturing the SQL gorm produces
// under DryRun — no database is contacted (the fake connector errors if one
// ever were), which keeps these tests portable while pinning real rendered
// SQL: placeholder bindings, AND/OR nesting, the workspace scope, and the
// verbatim engagement subqueries shared with the legacy dynamic-list engine.

// ---------- DryRun harness ----------

type segQueryErrConn struct{}

func (segQueryErrConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("segment query test: no queries expected")
}
func (segQueryErrConn) Close() error              { return nil }
func (segQueryErrConn) Begin() (driver.Tx, error) { return segQueryErrTx{}, nil }

type segQueryErrTx struct{}

func (segQueryErrTx) Commit() error   { return nil }
func (segQueryErrTx) Rollback() error { return nil }

func (c segQueryErrConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("segment query test: no queries expected under DryRun")
}
func (c segQueryErrConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("segment query test: no queries expected under DryRun")
}

type segQueryErrConnector struct{}

func (segQueryErrConnector) Connect(context.Context) (driver.Conn, error) {
	return segQueryErrConn{}, nil
}
func (segQueryErrConnector) Driver() driver.Driver { return segQueryErrDriver{} }

type segQueryErrDriver struct{}

func (segQueryErrDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use sql.OpenDB")
}

var segQuerySharedDB *gorm.DB

// segQueryDB opens the DryRun-only gorm handle shared by these tests.
func segQueryDB(t *testing.T) *gorm.DB {
	t.Helper()
	if segQuerySharedDB != nil {
		return segQuerySharedDB
	}
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(segQueryErrConnector{}),
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open DryRun gorm: %v", err)
	}
	segQuerySharedDB = gdb
	return gdb
}

// captureSegmentQuery renders the SQL and bind vars of a finished query chain
// built in DryRun mode.
func captureSegmentQuery(t *testing.T, build func(db *gorm.DB) *gorm.DB) (string, []any) {
	t.Helper()
	q := build(segQueryDB(t).Session(&gorm.Session{DryRun: true}))
	if q.Error != nil {
		t.Fatalf("dry-run build failed: %v", q.Error)
	}
	return q.Statement.SQL.String(), q.Statement.Vars
}

// captureLegacyFilterRules renders the WHERE of the legacy applyFilterRules
// engine for the given rules.
func captureLegacyFilterRules(t *testing.T, rules models.FilterRules) string {
	t.Helper()
	repo := NewSubscriberRepository(segQueryDB(t))
	sql, _ := captureSegmentQuery(t, func(db *gorm.DB) *gorm.DB {
		return repo.applyFilterRules(db.Model(&models.Subscriber{}), rules).Find(&[]models.Subscriber{})
	})
	if i := strings.Index(sql, " WHERE "); i >= 0 {
		sql = sql[i+len(" WHERE "):]
	}
	return sql
}

func normalizeSegmentSQL(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	// The legacy capture goes through the gorm callbacks, which rewrite `?`
	// placeholders into `$N`; the builder emits `?`. Normalize both to `?`.
	return bindvarRe.ReplaceAllString(s, "?")
}

var bindvarRe = regexp.MustCompile(`\$[0-9]+`)

func segmentCondition(rules ...models.SegmentConditionRule) models.SegmentCondition {
	return models.SegmentCondition{
		Logic: models.SegmentLogicAnd,
		Groups: []models.SegmentConditionGroup{{
			Logic: models.SegmentLogicAnd,
			Rules: rules,
		}},
	}
}

// segArgEqual compares rendered bind vars, timestamps included.
func segArgEqual(got, want any) bool {
	if gt, ok := got.(time.Time); ok {
		wt, ok2 := want.(time.Time)
		return ok2 && gt.Equal(wt)
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

// ---------- operator → SQL mapping: parameterized everywhere ----------

func TestSegmentConditionOperatorsAreParameterized(t *testing.T) {
	conditions := []struct {
		name     string
		cond     models.SegmentCondition
		wantSQL  []string // substrings that must appear in the built fragment
		notInSQL []string // values that must never appear in the SQL text
		wantArgs []any
	}{
		{
			name:     "email equals",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "email", Operator: "equals", Value: "a@b.co"}),
			wantSQL:  []string{"email = ?"},
			notInSQL: []string{"a@b.co"},
			wantArgs: []any{"a@b.co"},
		},
		{
			name:     "name not equals",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "name", Operator: "not_equals", Value: "Bob"}),
			wantSQL:  []string{"name <> ?"},
			notInSQL: []string{"Bob"},
			wantArgs: []any{"Bob"},
		},
		{
			name:     "email contains",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "email", Operator: "contains", Value: "example.com"}),
			wantSQL:  []string{"email ILIKE ?"},
			notInSQL: []string{"example.com"},
			wantArgs: []any{"%example.com%"},
		},
		{
			name:     "email not contains escapes wildcards",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "email", Operator: "not_contains", Value: "100%_x\\y"}),
			wantSQL:  []string{"email NOT ILIKE ?"},
			notInSQL: []string{"100%"},
			wantArgs: []any{`%100\%\_x\\y%`},
		},
		{
			name:     "name starts with",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "name", Operator: "starts_with", Value: "Al"}),
			wantSQL:  []string{"name ILIKE ?"},
			wantArgs: []any{"Al%"},
		},
		{
			name:     "name ends with",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "name", Operator: "ends_with", Value: "ice"}),
			wantSQL:  []string{"name ILIKE ?"},
			wantArgs: []any{"%ice"},
		},
		{
			name:     "timezone is empty",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "timezone", Operator: "is_empty"}),
			wantSQL:  []string{"(timezone IS NULL OR timezone = '')"},
			wantArgs: nil,
		},
		{
			name:     "language is not empty",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "language", Operator: "is_not_empty"}),
			wantSQL:  []string{"(language IS NOT NULL AND language <> '')"},
			wantArgs: nil,
		},
		{
			name:     "status equals",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "status", Operator: "equals", Value: "subscribed"}),
			wantSQL:  []string{"status = ?"},
			wantArgs: []any{"subscribed"},
		},
		{
			name:     "created_at greater than parses the date",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "created_at", Operator: "greater_than", Value: "2026-01-31"}),
			wantSQL:  []string{"created_at > ?"},
			wantArgs: []any{time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)},
		},
		{
			name:     "subscribed_at less than RFC3339",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "subscribed_at", Operator: "less_than", Value: "2026-01-31T10:00:00Z"}),
			wantSQL:  []string{"subscribed_at < ?"},
			wantArgs: []any{time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)},
		},
		{
			name:     "unsubscribed_at is empty is a pure NULL check",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "unsubscribed_at", Operator: "is_empty"}),
			wantSQL:  []string{"unsubscribed_at IS NULL"},
			wantArgs: nil,
		},
		{
			name:     "custom field equals binds the value",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "custom_fields.plan", Operator: "equals", Value: "pro"}),
			wantSQL:  []string{`custom_fields::jsonb->>'plan' = ?`},
			notInSQL: []string{"pro"},
			wantArgs: []any{"pro"},
		},
		{
			name:     "custom field exists",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "custom_fields.plan", Operator: "exists"}),
			wantSQL:  []string{`custom_fields::jsonb->>'plan' IS NOT NULL`},
			wantArgs: nil,
		},
		{
			name:     "custom field not exists",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "custom_fields.plan", Operator: "not_exists"}),
			wantSQL:  []string{`custom_fields::jsonb->>'plan' IS NULL`},
			wantArgs: nil,
		},
		{
			name:     "custom field greater than casts to numeric",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "custom_fields.age", Operator: "greater_than", Value: 30}),
			wantSQL:  []string{`(NULLIF(custom_fields::jsonb->>'age', '')::numeric > ?)`},
			wantArgs: []any{float64(30)},
		},
		{
			name:     "custom field is empty",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "custom_fields.note", Operator: "is_empty"}),
			wantSQL:  []string{`(custom_fields::jsonb->>'note' IS NULL OR custom_fields::jsonb->>'note' = '')`},
			wantArgs: nil,
		},
		{
			name:     "status one_of expands into bindvars",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "status", Operator: "one_of", Value: []any{"subscribed", "unsubscribed"}}),
			wantSQL:  []string{"status IN (?,?)"},
			notInSQL: []string{"subscribed"},
			wantArgs: []any{"subscribed", "unsubscribed"},
		},
		{
			name:     "language not one of",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "language", Operator: "not_one_of", Value: []any{"en"}}),
			wantSQL:  []string{"language NOT IN (?)"},
			wantArgs: []any{"en"},
		},
		{
			name:     "custom field one of",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "custom_fields.tier", Operator: "one_of", Value: []any{"gold", "silver"}}),
			wantSQL:  []string{`custom_fields::jsonb->>'tier' IN (?,?)`},
			wantArgs: []any{"gold", "silver"},
		},
		{
			name: "engagement opened_campaign (id)",
			cond: segmentCondition(models.SegmentConditionRule{Field: "engagement.opened_campaign", Operator: "equals", Value: 5}),
			wantSQL: []string{
				"EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id " +
					"AND campaign_messages.campaign_id = ? AND campaign_messages.opened_at IS NOT NULL)",
			},
			wantArgs: []any{float64(5)},
		},
		{
			name: "engagement clicked_campaign (id)",
			cond: segmentCondition(models.SegmentConditionRule{Field: "engagement.clicked_campaign", Operator: "equals", Value: 7}),
			wantSQL: []string{
				"EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id " +
					"AND campaign_messages.campaign_id = ? AND campaign_messages.clicked_at IS NOT NULL)",
			},
			wantArgs: []any{float64(7)},
		},
		{
			name: "engagement not_opened_campaign (id)",
			cond: segmentCondition(models.SegmentConditionRule{Field: "engagement.not_opened_campaign", Operator: "equals", Value: 7}),
			wantSQL: []string{
				"NOT EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id " +
					"AND campaign_messages.campaign_id = ? AND campaign_messages.opened_at IS NOT NULL)",
			},
			wantArgs: []any{float64(7)},
		},
		{
			name: "engagement received_campaign (id)",
			cond: segmentCondition(models.SegmentConditionRule{Field: "engagement.received_campaign", Operator: "equals", Value: 9}),
			wantSQL: []string{
				"EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id " +
					"AND campaign_messages.campaign_id = ?)",
			},
			wantArgs: []any{float64(9)},
		},
		{
			name:     "engagement opened_campaign boolean true is campaign-agnostic",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "engagement.opened_campaign", Operator: "equals", Value: true}),
			wantSQL:  []string{"EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at IS NOT NULL)"},
			wantArgs: nil,
		},
		{
			name:     "engagement opened_campaign boolean false negates",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "engagement.opened_campaign", Operator: "equals", Value: false}),
			wantSQL:  []string{"NOT EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at IS NOT NULL)"},
			wantArgs: nil,
		},
		{
			name:     "engagement inactive_days greater_than",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "engagement.inactive_days", Operator: "greater_than", Value: 30}),
			wantSQL:  []string{"NOT EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at > NOW() - INTERVAL '1 day' * ?)"},
			wantArgs: []any{float64(30)},
		},
		{
			name:     "engagement inactive_days less_than flips polarity",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "engagement.inactive_days", Operator: "less_than", Value: 7}),
			wantSQL:  []string{"EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at > NOW() - INTERVAL '1 day' * ?)"},
			wantArgs: []any{float64(7)},
		},
		{
			name:     "engagement active_days greater_than",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "engagement.active_days", Operator: "greater_than", Value: 14}),
			wantSQL:  []string{"EXISTS (SELECT 1 FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at > NOW() - INTERVAL '1 day' * ?)"},
			wantArgs: []any{float64(14)},
		},
		{
			name:     "engagement total_opens_gte",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "engagement.total_opens_gte", Operator: "greater_than", Value: 3}),
			wantSQL:  []string{"(SELECT COUNT(*) FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.opened_at IS NOT NULL) >= ?"},
			wantArgs: []any{float64(3)},
		},
		{
			name:     "engagement total_clicks_gte less_than",
			cond:     segmentCondition(models.SegmentConditionRule{Field: "engagement.total_clicks_gte", Operator: "less_than", Value: 2}),
			wantSQL:  []string{"(SELECT COUNT(*) FROM campaign_messages WHERE campaign_messages.subscriber_id = subscribers.id AND campaign_messages.clicked_at IS NOT NULL) < ?"},
			wantArgs: []any{float64(2)},
		},
	}

	for _, tc := range conditions {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := BuildSegmentConditionSQL(tc.cond)
			if err != nil {
				t.Fatalf("BuildSegmentConditionSQL: %v", err)
			}
			for _, want := range tc.wantSQL {
				if !strings.Contains(sql, want) {
					t.Errorf("SQL %q does not contain %q", sql, want)
				}
			}
			for _, banned := range tc.notInSQL {
				if strings.Contains(sql, banned) {
					t.Errorf("SQL %q must not embed the value %q — values must be bound", sql, banned)
				}
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, tc.wantArgs)
			}
			for i, want := range tc.wantArgs {
				if !segArgEqual(args[i], want) {
					t.Errorf("args[%d] = %v, want %v", i, args[i], want)
				}
			}
		})
	}
}

// ---------- logic nesting ----------

func TestSegmentConditionGroupNesting(t *testing.T) {
	// Two OR groups, each of two AND rules.
	cond := models.SegmentCondition{
		Logic: models.SegmentLogicOr,
		Groups: []models.SegmentConditionGroup{
			{
				Logic: models.SegmentLogicAnd,
				Rules: []models.SegmentConditionRule{
					{Field: "email", Operator: "contains", Value: "@a.co"},
					{Field: "status", Operator: "equals", Value: "subscribed"},
				},
			},
			{
				Logic: models.SegmentLogicOr,
				Rules: []models.SegmentConditionRule{
					{Field: "language", Operator: "equals", Value: "fr"},
					{Field: "timezone", Operator: "is_empty"},
				},
			},
		},
	}
	sql, args, err := BuildSegmentConditionSQL(cond)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := "(email ILIKE ? AND status = ?) OR (language = ? OR (timezone IS NULL OR timezone = ''))"
	if sql != want {
		t.Errorf("SQL =\n%s\nwant\n%s", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args = %#v, want 3", args)
	}

	// Top-level AND between groups.
	cond.Logic = models.SegmentLogicAnd
	sql, _, err = BuildSegmentConditionSQL(cond)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(sql, ") AND (") {
		t.Errorf("AND groups must be joined by AND: %s", sql)
	}
}

// ---------- SQL injection resistance ----------

func TestSegmentConditionRejectsInjectionInCustomKey(t *testing.T) {
	payloads := []string{
		`x"; DROP TABLE subscribers;--`,
		`x' OR '1'='1`,
		"key with space",
		"key;delete",
		"key.dot",
		`key"quote`,
		"key'quote",
		"key-dash", // contract: alnum + underscore only
		"key$newline",
		"",
	}
	for _, key := range payloads {
		cond := segmentCondition(models.SegmentConditionRule{Field: "custom_fields." + key, Operator: "equals", Value: "v"})
		errs := ValidateSegmentCondition(cond)
		if len(errs) == 0 {
			t.Errorf("key %q must be rejected", key)
			continue
		}
		if _, _, err := BuildSegmentConditionSQL(cond); err == nil {
			t.Errorf("key %q: builder must refuse to emit SQL", key)
		}
	}

	// No injection path can survive into built SQL either: for a valid
	// condition, the payload must appear nowhere in the SQL text.
	cond := segmentCondition(
		models.SegmentConditionRule{Field: "email", Operator: "contains", Value: "'; DROP TABLE subscribers;--"},
	)
	sql, _, err := BuildSegmentConditionSQL(cond)
	if err != nil {
		t.Fatalf("valid condition must build: %v", err)
	}
	if strings.Contains(sql, "DROP TABLE") {
		t.Errorf("SQL %q must not contain the payload", sql)
	}
	if !strings.Contains(sql, "ILIKE ?") {
		t.Errorf("SQL %q must bind the payload as a parameter", sql)
	}
}

// ---------- validation errors ----------

func TestSegmentConditionValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		cond    models.SegmentCondition
		wantSub []string
	}{
		{
			name:    "unknown field",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "emailx", Operator: "equals", Value: "x"}),
			wantSub: []string{`group 1 rule 1: unknown field "emailx"`},
		},
		{
			name:    "operator not allowed for the field",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "status", Operator: "contains", Value: "x"}),
			wantSub: []string{`group 1 rule 1: operator "contains" is not allowed for field "status"`},
		},
		{
			name:    "unknown operator",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "email", Operator: "matches", Value: "x"}),
			wantSub: []string{`group 1 rule 1: operator "matches" is not allowed for field "email"`},
		},
		{
			name:    "missing value",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "email", Operator: "equals"}),
			wantSub: []string{`group 1 rule 1: value is required for operator "equals"`},
		},
		{
			name:    "unparseable date",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "created_at", Operator: "greater_than", Value: "not-a-date"}),
			wantSub: []string{"use RFC3339 or YYYY-MM-DD"},
		},
		{
			name:    "date as a number",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "subscribed_at", Operator: "less_than", Value: 42}),
			wantSub: []string{"date value must be a string"},
		},
		{
			name:    "one_of without an array",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "status", Operator: "one_of", Value: "subscribed"}),
			wantSub: []string{`operator "one_of" requires a non-empty array value`},
		},
		{
			name:    "one_of with an empty array",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "status", Operator: "one_of", Value: []any{}}),
			wantSub: []string{`operator "one_of" requires a non-empty array value`},
		},
		{
			name:    "text equals with a non-string",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "email", Operator: "equals", Value: 42}),
			wantSub: []string{"value must be a string"},
		},
		{
			name:    "unknown engagement field",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "engagement.hovers", Operator: "equals", Value: 1}),
			wantSub: []string{`unknown field "engagement.hovers"`},
		},
		{
			name:    "engagement scalar with a non-numeric value",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "engagement.total_opens_gte", Operator: "greater_than", Value: "lots"}),
			wantSub: []string{`field "engagement.total_opens_gte" requires a numeric value`},
		},
		{
			name:    "engagement campaign field with a bad operator",
			cond:    segmentCondition(models.SegmentConditionRule{Field: "engagement.opened_campaign", Operator: "contains", Value: 1}),
			wantSub: []string{`operator "contains" is not allowed for field "engagement.opened_campaign"`},
		},
		{
			name:    "bad logic and empty groups",
			cond:    models.SegmentCondition{Logic: "xor"},
			wantSub: []string{`logic must be "and" or "or"`, "condition must contain at least one group"},
		},
		{
			name: "empty group",
			cond: models.SegmentCondition{
				Logic:  models.SegmentLogicAnd,
				Groups: []models.SegmentConditionGroup{{Logic: models.SegmentLogicAnd}},
			},
			wantSub: []string{"group 1: must contain at least one rule"},
		},
		{
			name: "rule addressing is 1-based",
			cond: models.SegmentCondition{
				Logic: models.SegmentLogicAnd,
				Groups: []models.SegmentConditionGroup{
					{Logic: models.SegmentLogicAnd, Rules: []models.SegmentConditionRule{
						{Field: "email", Operator: "equals", Value: "ok@example.com"},
						{Field: "bogus", Operator: "equals", Value: "x"},
					}},
					{Logic: models.SegmentLogicAnd, Rules: []models.SegmentConditionRule{
						{Field: "email", Operator: "equals", Value: 5},
					}},
				},
			},
			wantSub: []string{`group 1 rule 2: unknown field "bogus"`, "group 2 rule 1: value must be a string"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateSegmentCondition(tc.cond)
			if len(errs) == 0 {
				t.Fatal("expected validation errors, got none")
			}
			joined := strings.Join(errs, "; ")
			for _, want := range tc.wantSub {
				if !strings.Contains(joined, want) {
					t.Errorf("errors %q do not mention %q", joined, want)
				}
			}
			if _, _, err := BuildSegmentConditionSQL(tc.cond); err == nil {
				t.Error("builder must refuse an invalid condition")
			}
		})
	}
}

// ---------- workspace scope ----------

func TestSegmentQueryAlwaysScopedToWorkspace(t *testing.T) {
	workspace := uint(3)
	scope := ResourceScope{UserID: 7, WorkspaceID: &workspace}
	cond := segmentCondition(models.SegmentConditionRule{Field: "email", Operator: "equals", Value: "alice@example.com"})
	expr, condArgs, err := BuildSegmentConditionSQL(cond)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// This is the exact composition SegmentRepository.FindSubscribersByCondition
	// issues for its count and its page query.
	t.Run("scope and condition both bound", func(t *testing.T) {
		var total int64
		sql, vars := captureSegmentQuery(t, func(db *gorm.DB) *gorm.DB {
			return ApplyScope(db.Model(&models.Subscriber{}), scope).Where(expr, condArgs...).Count(&total)
		})
		if !strings.Contains(sql, "workspace_id = $1") {
			t.Errorf("count SQL missing workspace scope: %s", sql)
		}
		if !strings.Contains(sql, "email = $2") {
			t.Errorf("count SQL missing the condition: %s", sql)
		}
		if len(vars) != 2 || fmt.Sprint(vars[0]) != fmt.Sprint(workspace) {
			t.Errorf("vars = %#v, want the workspace first", vars)
		}

		sql, vars = captureSegmentQuery(t, func(db *gorm.DB) *gorm.DB {
			return ApplyScope(db, scope).Where(expr, condArgs...).Order("created_at DESC").Limit(20).Offset(40).Find(&[]models.Subscriber{})
		})
		if !strings.Contains(sql, "workspace_id = $1") || !strings.Contains(sql, "email = $2") {
			t.Errorf("find SQL missing scope or condition: %s", sql)
		}
		// LIMIT/OFFSET append their own vars; the scope and condition come first.
		if len(vars) < 2 || fmt.Sprint(vars[0]) != fmt.Sprint(workspace) || vars[1] != "alice@example.com" {
			t.Errorf("find vars = %#v, want the workspace and condition first", vars)
		}
	})

	// A scope without a workspace matches nothing — never the whole table.
	t.Run("scopeless request matches nothing", func(t *testing.T) {
		sql, _ := captureSegmentQuery(t, func(db *gorm.DB) *gorm.DB {
			return ApplyScope(db.Model(&models.Subscriber{}), ResourceScope{UserID: 7}).Find(&[]models.Subscriber{})
		})
		if !strings.Contains(sql, "1 = 0") {
			t.Errorf("scopeless SQL must be the 1 = 0 guard, got: %s", sql)
		}
	})
}

// ---------- engagement subqueries and flat compatibility with the legacy engine ----------

func TestSegmentEngagementSubqueriesMatchLegacyEngine(t *testing.T) {
	// The legacy engine has no operator for engagement rules; each field is
	// matched by its case branch with the value bound as the parameter.
	engagementRules := []struct {
		field    string
		operator string
		value    any
	}{
		{"engagement.opened_campaign", "equals", float64(5)},
		{"engagement.clicked_campaign", "equals", float64(6)},
		{"engagement.not_opened_campaign", "equals", float64(7)},
		{"engagement.received_campaign", "equals", float64(8)},
		{"engagement.inactive_days", "greater_than", float64(30)},
		{"engagement.active_days", "greater_than", float64(14)},
		{"engagement.total_opens_gte", "greater_than", float64(3)},
		{"engagement.total_clicks_gte", "greater_than", float64(2)},
	}
	for _, rule := range engagementRules {
		t.Run(rule.field, func(t *testing.T) {
			legacy := captureLegacyFilterRules(t, models.FilterRules{{Field: rule.field, Operator: "", Value: rule.value}})
			cond := segmentCondition(models.SegmentConditionRule{Field: rule.field, Operator: rule.operator, Value: rule.value})
			built, _, err := BuildSegmentConditionSQL(cond)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			built = strings.TrimSuffix(strings.TrimPrefix(built, "("), ")")
			if normalizeSegmentSQL(legacy) != normalizeSegmentSQL(built) {
				t.Errorf("legacy:\n%s\nsegment:\n%s", legacy, built)
			}
		})
	}
}

// TestSegmentBuilderFlatCompatWithLegacy pins the refactor contract: the new
// builder with a single flat AND group must produce the same WHERE as the
// legacy applyFilterRules engine for the same rules (modulo whitespace and the
// group parentheses).
func TestSegmentBuilderFlatCompatWithLegacy(t *testing.T) {
	rules := []models.FilterRule{
		{Field: "email", Operator: "eq", Value: "a@example.com"},
		{Field: "name", Operator: "contains", Value: "Ali"},
		{Field: "status", Operator: "eq", Value: "subscribed"},
		{Field: "custom_fields.plan", Operator: "eq", Value: "pro"},
		{Field: "engagement.opened_campaign", Operator: "", Value: float64(5)},
		{Field: "engagement.total_opens_gte", Operator: "", Value: float64(3)},
	}
	legacy := captureLegacyFilterRules(t, rules)

	segmentRules := make([]models.SegmentConditionRule, 0, len(rules))
	for _, r := range rules {
		op := mapLegacyOperator(r.Operator)
		if strings.HasPrefix(r.Field, "engagement.") {
			// The legacy engine has no operator for engagement rules; the
			// segment one expresses the polarity through the operator.
			op = opEquals
			if _, scalar := segmentScalarEngagementFields[strings.TrimPrefix(r.Field, "engagement.")]; scalar {
				op = opGreaterThan
			}
		}
		segmentRules = append(segmentRules, models.SegmentConditionRule{
			Field:    r.Field,
			Operator: op,
			Value:    r.Value,
		})
	}
	built, _, err := BuildSegmentConditionSQL(segmentCondition(segmentRules...))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// The flat comparison is all-AND, so parentheses (the builder's group
	// parens and gorm's per-fragment parens around subquery-bearing
	// expressions) never change the semantics — strip them before comparing.
	stripParens := func(s string) string {
		return normalizeSegmentSQL(strings.NewReplacer("(", " ", ")", " ").Replace(s))
	}
	if stripParens(legacy) != stripParens(built) {
		t.Errorf("legacy:\n%s\ngot:\n%s", legacy, built)
	}
}

// mapLegacyOperator translates the legacy dynamic-list operator vocabulary to
// the segment one for the operators the two engines share.
func mapLegacyOperator(op string) string {
	switch op {
	case "eq":
		return opEquals
	case "neq":
		return opNotEquals
	case "gt":
		return opGreaterThan
	case "lt":
		return opLessThan
	default:
		return op // contains / starts_with / ends_with are identical
	}
}
