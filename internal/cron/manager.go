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

package cron

import (
	"context"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/robfig/cron/v3"
)

// JobStatus holds runtime state for a registered cron job.
type JobStatus struct {
	Name      string     `json:"name"`
	Schedule  string     `json:"schedule"`
	Running   bool       `json:"running"`
	LastRunAt *time.Time `json:"last_run_at"`
	LastError string     `json:"last_error,omitempty"`
	NextRunAt *time.Time `json:"next_run_at"`
}

// Manager is responsible for registering cron jobs, triggering them based
// on cron expressions, and enqueueing Asynq jobs.
type Manager struct {
	client   *asynq.Client
	jobs     []CronJob
	cron     *cron.Cron
	mu       sync.Mutex
	running  bool
	statuses map[string]*jobState
	// lease, when set, makes this manager a participant in multi-replica
	// leadership (GAP-03, ADR-009 §2): only the lease holder runs ticks.
	// Nil — the default, POSTA_CRON_LEADER_LEASE_ENABLED off — keeps the
	// single-replica behavior of every replica running every job.
	lease *LeaderLease
}

type jobState struct {
	running   bool
	lastRunAt *time.Time
	lastError string
}

// NewManager creates a new cron manager backed by the given Asynq client.
func NewManager(client *asynq.Client) *Manager {
	return &Manager{
		client:   client,
		cron:     cron.New(cron.WithLocation(time.UTC)),
		statuses: make(map[string]*jobState),
	}
}

// Register adds a cron job to the manager.
func (m *Manager) Register(job CronJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = append(m.jobs, job)
	m.statuses[job.Name()] = &jobState{}
}

// SetLeaderLease installs the multi-replica leadership gate (GAP-03, ADR-009
// §2). From then on every scheduled tick first acquires/renews the lease;
// a replica that does not hold it skips the tick, so exactly one replica
// executes each job per interval. Must be called before Start. Leaving it
// unset (the default) keeps single-replica behavior unchanged.
func (m *Manager) SetLeaderLease(l *LeaderLease) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lease = l
}

// RunOnce executes one scheduled tick of job through the manager's full path:
// the leadership gate first (when a lease is installed), then the run with
// its status recording. The scheduler's AddFunc closure delegates here; it is
// exported so tests and ops tooling can drive a tick deterministically
// instead of waiting out a cron expression.
func (m *Manager) RunOnce(ctx context.Context, job CronJob) {
	m.mu.Lock()
	lease := m.lease
	m.mu.Unlock()

	if lease != nil && !lease.Acquire(ctx) {
		// Another replica holds the lease (or Redis is down and the gate
		// fails closed). Not a run: no status recording, debug only.
		logger.Debug("cron tick skipped: not the lease leader", "name", job.Name())
		return
	}

	m.setRunning(job.Name(), true)
	defer m.setRunning(job.Name(), false)

	now := time.Now().UTC()
	if err := job.Run(ctx, m.client); err != nil {
		m.recordRun(job.Name(), now, err.Error())
		logger.Error("cron job failed", "name", job.Name(), "error", err)
	} else {
		m.recordRun(job.Name(), now, "")
		logger.Debug("cron job completed", "name", job.Name())
	}
}

// Start begins running all registered cron jobs.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return
	}

	for _, j := range m.jobs {
		job := j // capture for closure
		_, err := m.cron.AddFunc(job.Schedule(), func() {
			m.RunOnce(ctx, job)
		})
		if err != nil {
			logger.Error("failed to register cron job", "name", job.Name(), "schedule", job.Schedule(), "error", err)
			continue
		}
		logger.Info("registered cron job", "name", job.Name(), "schedule", job.Schedule())
	}

	m.cron.Start()
	m.running = true
	logger.Info("cron manager started", "jobs", len(m.jobs))
}

// Stop gracefully shuts down the cron scheduler.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.cron.Stop()
	m.running = false
	logger.Info("cron manager stopped")
}

// Jobs returns the current status of all registered cron jobs.
func (m *Manager) Jobs() []JobStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries := m.cron.Entries()

	result := make([]JobStatus, 0, len(m.jobs))
	for i, job := range m.jobs {
		st := m.statuses[job.Name()]
		js := JobStatus{
			Name:      job.Name(),
			Schedule:  job.Schedule(),
			Running:   st.running,
			LastRunAt: st.lastRunAt,
			LastError: st.lastError,
		}
		if m.running && i < len(entries) {
			next := entries[i].Next
			if !next.IsZero() {
				js.NextRunAt = &next
			}
		}
		result = append(result, js)
	}
	return result
}

func (m *Manager) setRunning(name string, running bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.statuses[name]; ok {
		st.running = running
	}
}

func (m *Manager) recordRun(name string, at time.Time, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.statuses[name]; ok {
		st.lastRunAt = &at
		st.lastError = errMsg
	}
}
