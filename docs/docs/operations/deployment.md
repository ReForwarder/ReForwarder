---
sidebar_position: 1
title: Deployment & Upgrades
description: Deploying Posta — topology, migrations, listeners, multi-replica notes, health and upgrades (ADR-009)
---

# Deployment & Upgrades

Posta deploys as **one binary** (`posta`) with two commands — `server` (HTTP +
API + optional protocol listeners) and `worker` (queue processing) — over
**Postgres 17 and Redis 7**, plus optional S3-compatible blob storage
(ADR-009). This page consolidates the deployment path: install, migrations,
the config-gated listeners, scaling, health checks, and upgrade procedure.

## 1. Quick start (Docker Compose)

`compose.yml` at the repository root is the **local quick-start** topology —
it runs in dev mode with placeholder credentials:

```bash
docker compose up -d
# → server on http://localhost:9000, worker beside it,
#   Postgres (posta-db) and Redis (posta-redis) behind them.
```

For a deployable template with real secrets see `examples/`; production
refuses the placeholder values. All configuration is `POSTA_*` environment
variables (`docs/docs/getting-started/configuration.md`).

## 2. Topology

| Service | Image | Notes |
|---|---|---|
| `posta` (server) | built from `docker/Dockerfile` | HTTP API + dashboard; config-gated protocol listeners (§4) |
| `worker` | same image, `command: worker` | asynq queue processing; independently restartable |
| Postgres | `postgres:17-alpine` | the state of record — mail content (encrypted blobs), queues' source of truth, identities |
| Redis | `redis:7-alpine` | broker, cache, limits, security windows, pub/sub, cron leader lease — HA guidance in [Redis HA & Failure Semantics](./redis-ha.md) |

No additional stateful services (ADR-009 §3: no Kafka/NATS/FDB/external
search — the do-not-implement table stands).

## 3. Migrations — automatic, additive

Both commands run migrations at startup, before serving:

- `internal/storage/migration.Run` — GORM `AutoMigrate` over the registered
  models plus hand-written steps; **additive only** (new tables/columns; no
  drops or rewrites), so deployed instances upgrade in place
  (`cmd/posta/server.go:155`).
- `internal/storage/migration/upgrade.Run` — versioned upgrade steps
  (`internal/storage/migration/upgrade/`), with plan enforcement and an
  operator-unlocked downgrade gate (`cmd/posta/server.go:162`).

**Operational consequence:** upgrading is "start the new binary" — the first
replica to start applies pending migrations; keep a single migrator at a time
(start the server, then workers) for large upgrades. `posta doctor` checks an
install's readiness before you upgrade.

## 4. Listeners — off by default

Every protocol listener is config-gated and **default OFF** (ADR-009 §1); the
binary refuses nothing at `POSTA_PORT` until you opt in. Enable via env (all
shown commented in `compose.yml`):

| Listener | Env gate | Default ports | Auth |
|---|---|---|---|
| Submission (MSA) | `POSTA_SUBMISSION_ENABLED` | 587 | relay policy per ADR-001 §10 |
| Inbound SMTP | `POSTA_INBOUND_ENABLED` | 2525 | tenant-derived recipient domains |
| SMTP relay | `POSTA_SMTP_RELAY_ENABLED` | config | credential-authenticated |
| IMAP4rev1 | `POSTA_IMAP_ENABLED` | 143 (+ 993 implicit TLS) | **app passwords only**, STARTTLS required on 143 |
| POP3 | `POSTA_POP3_ENABLED` | 110 (+ 995 implicit TLS) | **app passwords only**, STLS required on 110 |
| MTA-STS HTTPS path | `POSTA_MTA_STS_ENABLED` | config | public policy surface |
| ACME certificates | `POSTA_ACME_ENABLED` | — | ACME account (in flight — see release notes) |

Listener ports are validated against collisions at boot (one process cannot
listen twice — `internal/config/config.go` `validate()`), and each listener
registers drain hooks for graceful shutdown (ADR-009 §6). When you enable a
listener in Docker, publish the port too (the commented `- "587:587"`-style
lines in `compose.yml`).

## 5. Scaling — multiple replicas

- **Workers scale horizontally out of the box**: asynq coordinates claiming in
  Redis; task claims are CAS/SKIP-LOCKED-safe (ADR-009 §2).
- **Server replicas** sit behind a standard reverse proxy; SSE fan-out flows
  over the Redis pub/sub fabric.
- **Cron jobs require the leader lease** when you run more than one server
  replica — without it, every replica runs every schedule and customer-facing
  alerts double-send:

  ```yaml
  # on EVERY server replica:
  POSTA_CRON_LEADER_LEASE_ENABLED: "true"
  POSTA_CRON_LEASE_TTL_SECONDS: "60"   # failover bound: one cron interval
  ```

  See [Redis HA & Failure Semantics](./redis-ha.md) for the failure-mode
  matrix and the multi-replica checklist.

## 6. Health & diagnostics

- `GET /healthz` — Postgres + Redis probes (Redis failure marks the service
  not-ready; `internal/health/health.go`).
- `posta doctor` — pre-upgrade readiness check (workspace scoping and more;
  `cmd/posta/main.go`).
- Worker liveness — Redis heartbeats surface in the admin panel
  (`internal/services/workermon/`).

## 7. Upgrades

1. Back up Postgres (the scheduled `pg_dump` backup job, R-11, is off by
   default — `POSTA_BACKUP_ENABLED`; restore runbook in
   `internal/cron/jobs/backup.go`).
2. `docker compose pull && docker compose up -d` (or your binary-equivalent
   flow). Migrations apply on boot — §3.
3. Watch `/healthz` and the admin scheduled-jobs panel; run `posta doctor`
   if anything looks off.
4. Rollback = run the previous image; additive migrations keep old binaries
   compatible with the widened schema (downgrade gate exists for the
   versioned steps).

## 8. Related

- [Redis HA & Failure Semantics](./redis-ha.md) — the SPOF runbook (GAP-49).
- `docs/architecture/ADR-009-deployment-topology.md` — the decision record
  this page operationalizes.
