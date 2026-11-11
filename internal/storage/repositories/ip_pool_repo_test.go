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

package repositories

import (
	"strings"
	"testing"
	"time"
)

// asInt64 renders a bound driver.Value as int64 regardless of the driver's
// integer representation (int64, uint64, uint, ...).
func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case uint64:
		return int64(n)
	case uint:
		return int64(n)
	case int:
		return int64(n)
	default:
		return -1
	}
}

// The workspace-facing pool read is scope-filtered: the SQL must carry the
// (own OR shared) visibility condition bound to the caller's workspace — a
// foreign workspace's private pool can never appear in the result set.
func TestIPPoolRepoVisiblePoolsScopeFilter(t *testing.T) {
	gdb, cap := dryCapture(t)
	r := NewIPPoolRepository(gdb)
	ws := uint(3)

	if _, _, err := r.FindVisiblePools(ws, 20, 0); err != nil {
		t.Fatalf("FindVisiblePools: %v", err)
	}
	stmt := cap.lastQuery()
	sqlText := strings.ToLower(stmt.sql)
	if !strings.Contains(sqlText, "ip_pools.workspace_id is null or ip_pools.workspace_id = $") {
		t.Fatalf("visibility filter missing from SQL: %s", stmt.sql)
	}
	// The workspace id is the first bound variable (the LIMIT follows).
	if len(stmt.vars) < 1 || asInt64(stmt.vars[0]) != 3 {
		t.Fatalf("workspace binding wrong: %v", stmt.vars)
	}
}

// The reputation rollup read is workspace-scoped and window-bounded; a
// nil-workspace scope matches nothing (fail closed, ApplyScope semantics).
func TestIPPoolRepoRollupsForDomainScopeFilter(t *testing.T) {
	gdb, cap := dryCapture(t)
	r := NewIPPoolRepository(gdb)
	ws := uint(7)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(14 * 24 * time.Hour)

	if _, err := r.RollupsForDomain(ResourceScope{WorkspaceID: &ws}, "dest.example", from, to); err != nil {
		t.Fatalf("RollupsForDomain: %v", err)
	}
	stmt := cap.lastQuery()
	sqlText := strings.ToLower(stmt.sql)
	if !strings.Contains(sqlText, "workspace_id = $") {
		t.Fatalf("workspace filter missing from SQL: %s", stmt.sql)
	}
	if !strings.Contains(sqlText, "day >= $") || !strings.Contains(sqlText, "day < $") {
		t.Fatalf("window bounds missing from SQL: %s", stmt.sql)
	}
	// The workspace id is bound (here as the trailing variable of the
	// WHERE conjunction).
	if len(stmt.vars) < 1 || asInt64(stmt.vars[len(stmt.vars)-1]) != 7 {
		t.Fatalf("workspace binding wrong: %v", stmt.vars)
	}

	// No workspace → matches nothing, without even building a leaky query.
	rows, err := r.RollupsForDomain(ResourceScope{}, "dest.example", from, to)
	if err != nil {
		t.Fatalf("nil-scope RollupsForDomain: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("nil scope must return no rows, got %d", len(rows))
	}
}

// The pool-delete guard counts referencing domains before deleting (the
// ErrPoolHasDomains refusal keeps assigned domains from silently losing
// their source-IP configuration).
func TestIPPoolRepoDeletePoolCountsDomainRefs(t *testing.T) {
	gdb, cap := dryCapture(t)
	r := NewIPPoolRepository(gdb)

	_ = r.DeletePool(9)
	// The first statement in the transaction is the domain-reference count.
	if len(cap.queries) == 0 {
		t.Fatal("no query captured for the delete guard")
	}
	first := strings.ToLower(cap.queries[0].sql)
	if !strings.Contains(first, "pool_id = $") || !strings.Contains(first, "count(") {
		t.Fatalf("reference count query missing: %s", cap.queries[0].sql)
	}
}
