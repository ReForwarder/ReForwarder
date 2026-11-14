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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conditions

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/goposta/posta/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// SF-103 tests, part 2: the cross-currency consistency invariants — the
// contract every adapter shares, asserted across all three at once:
// currency naming, the mode separation (ErrModeNotSupported), empty-set
// semantics, parameter binding (values are bindvars, never interpolated —
// the whitelisted custom-field key being the one documented exception), and
// per-currency unknown-field behavior.

// dryStubDriver refuses every operation: DryRun builds SQL without
// executing, so nothing ever reaches the driver.
type dryStubDriver struct{}

func (dryStubDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("dry stub: use OpenDB")
}

type dryStubConn struct{}

func (dryStubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("dry stub: no prepare")
}
func (dryStubConn) Close() error              { return nil }
func (dryStubConn) Begin() (driver.Tx, error) { return dryStubTx{}, nil }

type dryStubTx struct{}

func (dryStubTx) Commit() error   { return nil }
func (dryStubTx) Rollback() error { return nil }

type dryStubConnector struct{}

func (dryStubConnector) Connect(context.Context) (driver.Conn, error) { return dryStubConn{}, nil }
func (dryStubConnector) Driver() driver.Driver                        { return dryStubDriver{} }

var (
	dryDBOnce sync.Once
	dryDB     *gorm.DB
)

// dryOnce opens the DryRun gorm handle once per test binary.
func dryOnce(t *testing.T) *gorm.DB {
	t.Helper()
	dryDBOnce.Do(func() {
		sql.Register("conditions-dry-stub", dryStubDriver{})
		db, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(dryStubConnector{})}), &gorm.Config{
			DisableAutomaticPing:   true,
			SkipDefaultTransaction: true,
		})
		if err != nil {
			t.Fatalf("open dry-run gorm: %v", err)
		}
		dryDB = db
	})
	return dryDB
}

// dryQuery builds a DryRun SELECT over subscribers so WhereSQL's effect is
// inspectable without a database.
func dryQuery(t *testing.T) func() *gorm.DB {
	t.Helper()
	db := dryOnce(t)
	return func() *gorm.DB {
		return db.Session(&gorm.Session{DryRun: true}).Model(&models.Subscriber{})
	}
}

// sqlOf materializes the built SQL + bind vars of a DryRun query. The
// terminal Find triggers gorm's SQL build (DryRun stops before execution —
// the stub driver is never reached).
func sqlOf(t *testing.T, q *gorm.DB) (string, []any) {
	t.Helper()
	result := q.Find(&models.Subscriber{})
	if result.Error != nil {
		t.Fatalf("query build error: %v", result.Error)
	}
	s := result.Statement.SQL.String()
	vars := append([]any(nil), result.Statement.Vars...)
	return strings.Join(strings.Fields(s), " "), vars
}

// TestCurrencyNaming pins the vocabulary: each adapter names its dialect.
func TestCurrencyNaming(t *testing.T) {
	if got := NewAutomationEvaluator(nil, nil, nil).Currency(); got != CurrencyAutomation {
		t.Fatalf("automation adapter Currency = %q, want %q", got, CurrencyAutomation)
	}
	if got := NewFilterRuleEvaluator(nil).Currency(); got != CurrencyFilterRule {
		t.Fatalf("FilterRule adapter Currency = %q, want %q", got, CurrencyFilterRule)
	}
	if got := NewSegmentEvaluator(models.SegmentCondition{}).Currency(); got != CurrencySegment {
		t.Fatalf("Segment adapter Currency = %q, want %q", got, CurrencySegment)
	}
}

// TestModeSeparation pins the mode contract: each adapter serves exactly one
// evaluation mode and returns ErrModeNotSupported for the other — a caller
// can never accidentally evaluate a SQL currency in memory or vice versa.
func TestModeSeparation(t *testing.T) {
	auto := NewAutomationEvaluator([]models.FilterRule{rule("email", "equals", "x")}, nil, nil)
	fr := NewFilterRuleEvaluator(models.FilterRules{rule("email", "eq", "x")})
	seg := NewSegmentEvaluator(models.SegmentCondition{Logic: models.SegmentLogicAnd, Groups: []models.SegmentConditionGroup{{Logic: models.SegmentLogicAnd,
		Rules: []models.SegmentConditionRule{{Field: "email", Operator: "equals", Value: "x"}},
	}}})

	if _, err := fr.MatchesSubscriber(sub()); !errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("FilterRule MatchesSubscriber = %v, want ErrModeNotSupported", err)
	}
	if _, err := seg.MatchesSubscriber(sub()); !errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("Segment MatchesSubscriber = %v, want ErrModeNotSupported", err)
	}
	if _, err := auto.WhereSQL(nil); !errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("automation WhereSQL = %v, want ErrModeNotSupported", err)
	}
	// Each adapter still serves its own mode.
	if _, err := auto.MatchesSubscriber(sub()); err != nil {
		t.Fatalf("automation MatchesSubscriber errored: %v", err)
	}
	base := dryQuery(t)
	if _, err := fr.WhereSQL(base()); err != nil {
		t.Fatalf("FilterRule WhereSQL errored: %v", err)
	}
	if _, err := seg.WhereSQL(base()); err != nil {
		t.Fatalf("Segment WhereSQL errored: %v", err)
	}
}

// TestEmptySetMatchesEverything pins the shared invariant across the SQL
// currencies: an empty condition set adds NO where clause — the built SQL is
// byte-identical to the base query. (The in-memory currency's vacuous match
// is pinned in memory_test.go.)
func TestEmptySetMatchesEverything(t *testing.T) {
	base := dryQuery(t)

	fr := NewFilterRuleEvaluator(nil)
	q, err := fr.WhereSQL(base())
	if err != nil {
		t.Fatalf("FilterRule WhereSQL: %v", err)
	}
	baseSQL, _ := sqlOf(t, base())
	emptySQL, _ := sqlOf(t, q)
	if baseSQL != emptySQL {
		t.Fatalf("an empty FilterRule set changed the SQL:\nbase:   %s\napplied: %s", baseSQL, emptySQL)
	}

	// The Segment currency's empty case is a documented divergence, pinned
	// as such: its validation refuses a condition with no groups (every
	// caller guards with Condition.IsEmpty() before reaching the builder),
	// so the empty-set invariant there is enforced by validation, not by an
	// empty WHERE.
	segEmpty := NewSegmentEvaluator(models.SegmentCondition{})
	if _, err := segEmpty.WhereSQL(base()); err == nil {
		t.Fatal("the Segment currency must refuse an empty condition document")
	}
}

// TestValuesAreBoundNotInterpolated pins the injection posture both SQL
// currencies share: rule values land as `?` bindvars, never in the SQL text;
// a custom-field key is the one documented exception and survives the strict
// whitelist first.
func TestValuesAreBoundNotInterpolated(t *testing.T) {
	base := dryQuery(t)

	// FilterRule: the value is a bindvar.
	fr := NewFilterRuleEvaluator(models.FilterRules{rule("email", "eq", "attacker'--")})
	q, err := fr.WhereSQL(base())
	if err != nil {
		t.Fatalf("FilterRule WhereSQL: %v", err)
	}
	s, vars := sqlOf(t, q)
	if !strings.Contains(s, "email =") || !strings.Contains(s, "$1") {
		t.Fatalf("FilterRule SQL = %s, want the email = <bindvar> shape", s)
	}
	if len(vars) != 1 || vars[0] != "attacker'--" {
		t.Fatalf("FilterRule vars = %v, want the raw value bound", vars)
	}
	if strings.Contains(s, "attacker") {
		t.Fatalf("FilterRule SQL leaks the value into the text: %s", s)
	}

	// Segment: the same posture through its own vocabulary.
	seg := NewSegmentEvaluator(models.SegmentCondition{Logic: models.SegmentLogicAnd, Groups: []models.SegmentConditionGroup{{Logic: models.SegmentLogicAnd,
		Rules: []models.SegmentConditionRule{{Field: "email", Operator: "equals", Value: "attacker'--"}},
	}}})
	q2, err := seg.WhereSQL(base())
	if err != nil {
		t.Fatalf("Segment WhereSQL: %v", err)
	}
	s2, vars2 := sqlOf(t, q2)
	if !strings.Contains(s2, "email =") || !strings.Contains(s2, "$1") || len(vars2) != 1 || vars2[0] != "attacker'--" {
		t.Fatalf("Segment SQL/vars = %s / %v, want the bound value", s2, vars2)
	}

	// The custom-field key exception: the whitelisted key is interpolated,
	// and the FilterRule currency SKIPS an invalid key (no clause) while the
	// Segment currency refuses the whole condition (validation error).
	frBad := NewFilterRuleEvaluator(models.FilterRules{rule("custom_fields.bad key'; --", "eq", "x")})
	q3, err := frBad.WhereSQL(base())
	if err != nil {
		t.Fatalf("FilterRule bad-key WhereSQL: %v", err)
	}
	s3, _ := sqlOf(t, q3)
	if strings.Contains(s3, "custom_fields") {
		t.Fatalf("an invalid custom-field key must skip the rule: %s", s3)
	}
	segBad := models.SegmentCondition{Logic: models.SegmentLogicAnd, Groups: []models.SegmentConditionGroup{{Logic: models.SegmentLogicAnd,
		Rules: []models.SegmentConditionRule{{Field: "custom_fields.bad key'; --", Operator: "equals", Value: "x"}},
	}}}
	if errs := ValidateSegmentCondition(segBad); len(errs) == 0 {
		t.Fatal("an invalid custom-field key must fail segment validation")
	}
	if _, _, err := BuildSegmentConditionSQL(segBad); err == nil {
		t.Fatal("BuildSegmentConditionSQL must refuse an invalid custom-field key")
	}
}

// TestANDCombinationAcrossSQLCurrencies pins the shared AND semantics: every
// rule of a set restricts the query — both clauses appear, both values bound.
func TestANDCombinationAcrossSQLCurrencies(t *testing.T) {
	base := dryQuery(t)

	fr := NewFilterRuleEvaluator(models.FilterRules{
		rule("email", "eq", "a@x.y"),
		rule("status", "eq", "subscribed"),
	})
	q, err := fr.WhereSQL(base())
	if err != nil {
		t.Fatalf("FilterRule WhereSQL: %v", err)
	}
	s, vars := sqlOf(t, q)
	if !(strings.Contains(s, "email =") && strings.Contains(s, "status =")) || len(vars) != 2 {
		t.Fatalf("FilterRule AND combination drifted: %s / %v", s, vars)
	}

	seg := NewSegmentEvaluator(models.SegmentCondition{Logic: models.SegmentLogicAnd, Groups: []models.SegmentConditionGroup{{Logic: models.SegmentLogicAnd,
		Rules: []models.SegmentConditionRule{
			{Field: "email", Operator: "equals", Value: "a@x.y"},
			{Field: "status", Operator: "equals", Value: "subscribed"},
		},
	}}})
	q2, err := seg.WhereSQL(base())
	if err != nil {
		t.Fatalf("Segment WhereSQL: %v", err)
	}
	s2, vars2 := sqlOf(t, q2)
	if !(strings.Contains(s2, "email =") && strings.Contains(s2, "status =")) || len(vars2) != 2 {
		t.Fatalf("Segment AND combination drifted: %s / %v", s2, vars2)
	}
}

// TestUnknownFieldBehaviorPerCurrency pins the deliberate per-currency
// divergence the unification preserves (not merges): the in-memory currency
// never matches an unknown field, the FilterRule SQL currency silently skips
// it (its documented legacy posture), and the Segment currency's validation
// rejects the field outright.
func TestUnknownFieldBehaviorPerCurrency(t *testing.T) {
	if EvaluateFilterRules([]models.FilterRule{rule("wat", "equals", "x")}, sub(), nil) {
		t.Fatal("the in-memory currency must never match an unknown field")
	}

	base := dryQuery(t)
	fr := NewFilterRuleEvaluator(models.FilterRules{rule("wat", "eq", "x")})
	q, err := fr.WhereSQL(base())
	if err != nil {
		t.Fatalf("FilterRule WhereSQL: %v", err)
	}
	s, _ := sqlOf(t, q)
	if strings.Contains(s, "wat") {
		t.Fatalf("the FilterRule SQL currency must skip an unknown field: %s", s)
	}

	seg := models.SegmentCondition{Logic: models.SegmentLogicAnd, Groups: []models.SegmentConditionGroup{{Logic: models.SegmentLogicAnd,
		Rules: []models.SegmentConditionRule{{Field: "wat", Operator: "equals", Value: "x"}},
	}}}
	if errs := ValidateSegmentCondition(seg); len(errs) == 0 {
		t.Fatal("the Segment currency must reject an unknown field at validation")
	}
	if _, _, err := BuildSegmentConditionSQL(seg); err == nil {
		t.Fatal("the Segment builder must refuse an unknown field")
	}
}
