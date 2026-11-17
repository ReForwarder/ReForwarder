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
	"fmt"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver for the probe
	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

// The application-side store-failover preparation (SF-106): a health-driven
// primary/replica DSN selector. The selector holds an ordered DSN list (the
// primary first, then the standbys) and, probed with a connection check,
// picks the FIRST REACHABLE entry cyclically from the active position — a
// failed primary fails over to the next reachable standby, and a recovered
// primary is re-promoted on the next pick (health-driven). The config gate is
// `POSTA_DB_FAILOVER_DSN` (comma-separated, postgres only); with the gate off
// the connection wiring is byte-identical to the pre-SF-106 path.
//
// The BOUNDARY (ADR-009 §5, unchanged): the selector covers the STARTUP
// selection — which endpoint the process opens against. The RUNTIME
// switchover of live connections (draining, re-opening, re-routing in-flight
// transactions) is a store-level concern served by managed HA (the deployment
// runbook: `docs/docs/operations/deployment.md`, the HA pattern
// `docs/docs/operations/redis-ha.md`), not application code — a hot-swapped
// *gorm.DB under running transactions would be exactly the double-delivery
// surface the exactly-once claim substrate (R-26) guards against.

// DSNProbe reports whether a DSN is reachable right now. The real probe
// (postgresProbe) opens a connection and pings with a bounded timeout; tests
// inject a fake.
type DSNProbe func(dsn string) error

// FailoverSelector is the health-driven DSN picker. Safe for concurrent use.
type FailoverSelector struct {
	mu      sync.Mutex
	dsnList []string
	active  int // the index of the currently preferred entry
	probe   DSNProbe
}

// NewFailoverSelector builds a selector over the ordered DSN list (index 0 is
// the primary). The probe defaults to the real PostgreSQL connection check.
func NewFailoverSelector(dsnList []string, probe DSNProbe) *FailoverSelector {
	if probe == nil {
		probe = postgresProbe
	}
	return &FailoverSelector{dsnList: append([]string(nil), dsnList...), probe: probe}
}

// Active returns the currently preferred DSN (without probing).
func (s *FailoverSelector) Active() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dsnList[s.active]
}

// Pick probes entries and returns the first reachable DSN, advancing the
// active position to it. The PRIMARY is always probed first (health-driven
// re-promotion: a recovered primary is preferred on the next pick); when it
// is down, the list is walked cyclically from the active position. A probe
// failure at any entry is recorded (logged) and the next entry is tried; when
// every entry is unreachable the error carries the whole probe trail.
func (s *FailoverSelector) Pick() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.dsnList)
	// The probe order: the primary first, then the cycle from the active
	// position (skipping the already-probed primary).
	order := make([]int, 0, n)
	order = append(order, 0)
	for i := 0; i < n; i++ {
		idx := (s.active + i) % n
		if idx != 0 {
			order = append(order, idx)
		}
	}
	var trail string
	for _, idx := range order {
		dsn := s.dsnList[idx]
		err := s.probe(dsn)
		if err == nil {
			if idx != s.active {
				logger.Warn("store failover: endpoint changed", "from", s.dsnList[s.active], "to", dsn)
			}
			s.active = idx
			return dsn, nil
		}
		if trail != "" {
			trail += "; "
		}
		trail += fmt.Sprintf("%s: %v", endpointLabel(idx, dsn), err)
	}
	return "", fmt.Errorf("store failover: no reachable endpoint (%s)", trail)
}

// endpointLabel names an entry for the probe trail ("primary" for index 0,
// "replica-N" otherwise).
func endpointLabel(idx int, dsn string) string {
	if idx == 0 {
		return "primary"
	}
	return fmt.Sprintf("replica-%d (%s)", idx, shortDSN(dsn))
}

// shortDSN renders a DSN without credentials for logs.
func shortDSN(dsn string) string {
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == '@' {
			return dsn[i+1:]
		}
	}
	return dsn
}

// postgresProbe is the real health check: open a connection and ping within a
// bounded window.
func postgresProbe(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.PingContext(ctx)
}

// ConnectWithFailover opens the store against the first reachable endpoint of
// the ordered DSN list (SF-106's config-gated startup selection). It returns
// the chosen DSN so the wiring can log which endpoint the process landed on.
// The list must carry at least two entries — failover semantics with one is a
// lie — and the caller validates that (validate()).
func ConnectWithFailover(dsnList []string) (*gorm.DB, string, error) {
	if len(dsnList) < 2 {
		return nil, "", fmt.Errorf("store failover requires at least two DSN entries (primary + standby), got %d", len(dsnList))
	}
	sel := NewFailoverSelector(dsnList, nil)
	dsn, err := sel.Pick()
	if err != nil {
		return nil, "", err
	}
	db, err := ConnectPostgres(dsn)
	if err != nil {
		// The probe succeeded but the gorm open failed — try the next
		// reachable entry rather than giving up (the probe and the open are
		// not atomic).
		sel2 := NewFailoverSelector(dsnList, func(candidate string) error {
			if candidate == dsn {
				return fmt.Errorf("gorm open failed for the probed endpoint")
			}
			return postgresProbe(candidate)
		})
		dsn2, perr := sel2.Pick()
		if perr != nil {
			return nil, "", fmt.Errorf("store failover: every endpoint failed the connection: %w", perr)
		}
		db, err = ConnectPostgres(dsn2)
		if err != nil {
			return nil, "", err
		}
		dsn = dsn2
	}
	logger.Warn("store failover: connected", "endpoint", shortDSN(dsn),
		"standbys", len(dsnList)-1)
	return db, dsn, nil
}
