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

// Package health answers "can this process do its job right now?" for both the
// server and a dedicated worker.
//
// It lives here rather than in either caller so the two cannot drift: an
// operator pointing the same probe at a server pod and a worker pod should get
// the same shape and the same meaning of "ready".
package health

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// probeTimeout bounds a single dependency check. A probe that hangs is a failed
// probe: an orchestrator is asking whether to send work here, and no answer for
// thirty seconds is worse than a quick "no".
const probeTimeout = 2 * time.Second

const (
	StatusOK       = "ok"
	StatusReady    = "ready"
	StatusNotReady = "not ready"
)

// Status is the readiness payload. The field names are part of the operator
// contract — probes and dashboards read them — so they are kept stable.
type Status struct {
	Status   string `json:"status" example:"ready"`
	Database string `json:"database" example:"ok"`
	Redis    string `json:"redis" example:"ok"`
}

// Ready reports whether every dependency answered.
func (s Status) Ready() bool { return s.Status == StatusReady }

// Check pings each dependency and reports what it found. A nil dependency is
// reported as unconfigured rather than skipped: a worker with no Redis cannot
// take a job, and saying "ready" would be a lie.
func Check(ctx context.Context, db *gorm.DB, rdb *redis.Client) Status {
	s := Status{Status: StatusReady, Database: StatusOK, Redis: StatusOK}

	if err := pingDatabase(ctx, db); err != nil {
		s.Status, s.Database = StatusNotReady, err.Error()
	}
	if err := pingRedis(ctx, rdb); err != nil {
		s.Status, s.Redis = StatusNotReady, err.Error()
	}
	return s
}

func pingDatabase(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("not configured")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

func pingRedis(ctx context.Context, rdb *redis.Client) error {
	if rdb == nil {
		return errors.New("not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return rdb.Ping(ctx).Err()
}
