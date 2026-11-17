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

package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"

	"github.com/jkaninda/logger"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	modernc "modernc.org/sqlite"
)

// The SQLite dev-tier backend (SF-76, the ADR-009 amendment): an embedded
// development / experimentation tier behind POSTA_DB_DRIVER=sqlite, driven by
// the pure-Go modernc.org/sqlite driver — no CGO. Postgres remains the
// production store; the dev tier's documented boundary (the Postgres-idiomatic
// runtime paths the tier does not serve) is in the ADR amendment and the
// configuration docs.

// sqliteDriverName is the database/sql registration name of the wrapped
// pure-Go driver.
const sqliteDriverName = "posta-sqlite-dev"

// sqliteDevOnce registers the DDL-portability wrapper exactly once.
// The wrapper exists for one reason: the registered model fleet carries
// Postgres-idiomatic `DEFAULT gen_random_uuid()` column defaults, which
// SQLite cannot parse as an expression default (SQLite requires a
// parenthesized expression). The wrapper rewrites exactly that default — for
// CREATE/ALTER DDL only — to the SQLite-native equivalent
// `lower(hex(randomblob(16)))` (a random 32-hex-char identifier of the same
// shape). The model tags stay untouched, so the Postgres schema is
// byte-identical on every existing deployment.
var (
	sqliteDevOnce       sync.Once
	sqliteDevRegistered bool
)

func registerSQLiteDevDriver() {
	sqliteDevOnce.Do(func() {
		sql.Register(sqliteDriverName, &sqliteDevDriver{inner: &modernc.Driver{}})
		sqliteDevRegistered = true
	})
}

// sqliteDevDriver wraps the pure-Go driver.
type sqliteDevDriver struct{ inner *modernc.Driver }

func (d *sqliteDevDriver) Open(name string) (driver.Conn, error) {
	inner, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &sqliteDevConn{inner: inner}, nil
}

// sqliteDDLDefault is the Postgres default clause the model fleet carries,
// rewritten to its SQLite-native equivalent.
const (
	sqlitePGDefault    = "default gen_random_uuid()"
	sqliteDefaultRewrt = "default (lower(hex(randomblob(16))))"
)

// portSQLiteDDL rewrites the un-parseable Postgres default clauses in DDL
// statements. Only CREATE/ALTER statements are touched; reads and writes pass
// through byte-for-byte.
func portSQLiteDDL(query string) string {
	lower := strings.ToLower(query)
	if !strings.HasPrefix(lower, "create ") && !strings.HasPrefix(lower, "alter ") {
		return query
	}
	if !strings.Contains(lower, sqlitePGDefault) {
		return query
	}
	// Case-preserving is unnecessary for the matched fragment: the default
	// clause is emitted lowercase-idiomatically by gorm; a mixed-case match
	// still rewrites because the comparison used the lowered copy and the
	// replacement is applied positionally.
	hi := strings.Index(lower, sqlitePGDefault)
	if hi < 0 {
		return query
	}
	out := query[:hi] + sqliteDefaultRewrt
	rest := query[hi+len(sqlitePGDefault):]
	// A DDL statement can carry more than one defaulted column.
	for {
		lowerRest := strings.ToLower(rest)
		j := strings.Index(lowerRest, sqlitePGDefault)
		if j < 0 {
			return out + rest
		}
		out += rest[:j] + sqliteDefaultRewrt
		rest = rest[j+len(sqlitePGDefault):]
	}
}

// sqliteDevConn delegates everything to the inner connection, rewriting DDL
// on the paths gorm exercises (Prepare → Stmt for most statements; the
// optional direct Exec/Query paths for completeness).
type sqliteDevConn struct{ inner driver.Conn }

func (c *sqliteDevConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.inner.Prepare(portSQLiteDDL(query))
	if err != nil {
		return nil, err
	}
	return &sqliteDevStmt{inner: stmt}, nil
}

func (c *sqliteDevConn) Close() error              { return c.inner.Close() }
func (c *sqliteDevConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

// BeginTx delegates when the inner connection supports it.
func (c *sqliteDevConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	bt, ok := c.inner.(driver.ConnBeginTx)
	if !ok {
		return c.Begin()
	}
	return bt.BeginTx(ctx, opts)
}

func (c *sqliteDevConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return ec.ExecContext(ctx, portSQLiteDDL(query), args)
}

func (c *sqliteDevConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return qc.QueryContext(ctx, portSQLiteDDL(query), args)
}

// Ping delegates when supported (database/sql prefers Conn.Pinger).
func (c *sqliteDevConn) Ping(ctx context.Context) error {
	p, ok := c.inner.(driver.Pinger)
	if !ok {
		return driver.ErrSkip
	}
	return p.Ping(ctx)
}

type sqliteDevStmt struct{ inner driver.Stmt }

func (s *sqliteDevStmt) Close() error  { return s.inner.Close() }
func (s *sqliteDevStmt) NumInput() int { return s.inner.NumInput() }

func (s *sqliteDevStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}

func (s *sqliteDevStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

// ExecContext/QueryContext delegate when the inner statement supports them
// (the rewritten DDL already went through Prepare; these paths carry the
// regular reads/writes untouched).
func (s *sqliteDevStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := s.inner.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return ec.ExecContext(ctx, args)
}

func (s *sqliteDevStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := s.inner.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return qc.QueryContext(ctx, args)
}

// ConnectSQLite opens the dev-tier SQLite database (the ADR-009 amendment,
// SF-76) without running migrations — mirroring ConnectPostgres's contract.
// The dialector is gorm's SQLite one bound to the pure-Go wrapped driver (no
// CGO).
func ConnectSQLite(path string) (*gorm.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("sqlite dev tier requires a database file path")
	}
	registerSQLiteDevDriver()
	dialector := sqlite.Dialector{
		DriverName: sqliteDriverName,
		DSN:        path,
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the sqlite dev database: %w", err)
	}
	logger.Warn("SQLite DEV-TIER database connected — Postgres remains the only supported production store (ADR-009 amendment, SF-76)")
	return db, nil
}
