# ADR-009 — Deployment Topology & Scaling

> **📄 Status (2026-11-14):** 📌 **Accepted ADR** (2026-11-07) — binding decision record (one binary, gated listeners; multi-node deferred). Amendment-gated items (SQLite backend, store failover) are tracked in the PARTIAL-completion tracker.
> **Live status:** [docs/PROJECT_STATUS_DASHBOARD.md](../PROJECT_STATUS_DASHBOARD.md) · [docs/README.md](../README.md)

- **Status:** Accepted (2026-11-07 — Option B programme authorization)
- **Phase:** Cross-cutting (bindings applied in Phases 1–6; convergence items at Phase 6) · **Covers (parity register):** SF-76 (single data store), SF-105 (cluster fabric), SF-106 (multi-node failover), SF-107 (cluster invalidation), SF-109 (shutdown), SF-114 (deployment tooling), GAP-49 (Redis HA guidance)

## Context

ReForwarder deploys as a single Go binary (`posta`) with a `server` and a `worker` command over Postgres 17 + Redis 7 (+ optional S3), packaged by a multi-stage Dockerfile, compose files, a Makefile, and a multi-arch release workflow. Phases 1–5 add long-running listeners (inbound SMTP, submission, IMAP, POP3, webmail TLS) and worker-bound delivery. The decision record's control-plane/data-plane split is realized **logically** (packages + listeners), not as new deployables, unless a listener's lifecycle genuinely conflicts with the HTTP server.

## Decision

1. **One binary, gated listeners.** `posta server` gains config-gated listeners (inbound SMTP 25/2525, submission 587, MTA-STS HTTPS path, IMAP 143/993, POP3 110/995 — each default-off except where compose enables them); `posta worker` gains delivery/filters/mailstore sweeps. `posta doctor` extended to check listener readiness, DNS (MX/SPF/DKIM/return-path), PTR guidance, and clamd reachability (SF-115 continuation).
2. **Horizontal scale**: multiple `worker` replicas are supported from Phase 1 — asynq already coordinates multi-process claiming (Redis), R-26's CAS/SKIP-LOCKED claims are replica-safe, and ADR-002/003 sweeps are lock-guarded (Redis lease, closing GAP-03's leader-election concern for multi-replica crons). `server` replicas sit behind a standard reverse proxy; SSE fan-out uses the existing Redis pub/sub pattern where a second server replica exists.
3. **Single Postgres, single Redis (with HA guidance)**: no additional stateful services at Phases 1–5 (FDB/NATS/Kafka/external search rejected — do-not-implement table stands). GAP-49 delivers the Redis HA/SPOF mitigation runbook (managed Redis with replication, fail-open semantics documented per limiter, queue durability on Postgres as the backstop). Alternative data-store backends (SF-76) remain an ADR-level decision, not a config option.
4. **Cluster fabric (SF-105)**: Redis pub/sub remains the only fabric (bans today; mailbox IDLE notifications, cache invalidation, and listener config propagation join it). NATS/Zenoh/Kafka are Phase 6 evaluations only under measured need — recorded as BLOCKED BY ADR-009/OB-6, not dropped.
5. **Failover semantics (SF-106)**: event-locked, exactly-once processing is delivered by the claim substrate on every replica; store-level failover (multi-region Postgres) is a deployment concern documented in the runbook (managed HA), not application code.
6. **Graceful shutdown (SF-109)**: okapi owns the HTTP lifecycle; each new listener registers drain hooks (stop accepting, finish in-flight sessions/claims within a bounded grace period, flush attempt records) via the existing `OnShutdown` pattern; systemd sd-notify is not adopted (Docker is the deployment unit — documented equivalent).
7. **Caching invalidation across replicas (SF-107 completion)**: settings/security caches propagate invalidation over the Redis fabric on write (single-replica TTL expiry remains the fallback).
8. **Configuration**: all new listeners and limits are `POSTA_*` env/config settings with the same live-apply discipline (SF-98/110); compose.override remains the dev-enablement path; production guidance (ports, DNS records, PTR, TLS, HA) lands in the docs site (`docs/docs/`) per Phase 6 documentation reconciliation.

## Alternatives considered and rejected

- **Separate per-protocol deployables** (an "mta" container, an "imap" container): rejected — N deployables × config surfaces for protocols that share the same DB/session/config substrate; the gated-listener model gives the same operational isolation (a listener can be disabled without redeploying code).
- **Kubernetes Operator / helm chart at Phase 1**: deferred — compose + Docker release serves the current install base; a chart is a Phase 6 contribution opportunity (community demand), recorded in the programme docs.
- **Multi-region active-active**: rejected — out of scope; HA via managed services + runbook (GAP-49) is the supported posture.

## Consequences

- `cmd/posta` listener wiring + config schema extension; compose updates per enabled listeners; GAP-03 (cron leader lease) and GAP-49 (Redis HA runbook) close inside this ADR's scope; `doctor` extensions.
- Tests: listener lifecycle (start/stop/drain), multi-worker claim safety (existing concurrency battery extended to direct-delivery tasks), Redis-lease leadership, config-gate matrix (every listener off-by-default proof).
- The four non-negotiables are enforced at the listener boundary: submission/auth policies on the new ports mirror the relay and inbound receivers (no open relay; tenant-derived recipient domains; delivered-on-truth only; durable path before any routing action).

## Amendment (2026-11-18, SF-76): the SQLite dev-tier backend

**Decision**: a second relational data-store backend — **SQLite, driven by a
pure-Go driver (no CGO)** — is approved as an **embedded development /
experimentation tier only**, behind explicit configuration. **Postgres remains
the production store**; the production guidance, HA runbook and backup
automation (R-11's `pg_dump` posture) stay Postgres-only, and the alternative
backends of the reference platform's breadth (RocksDB, MySQL, FoundationDB,
HTTP KV) remain rejected — this amendment widens the store surface by exactly
one backend, for exactly one deployment posture.

**Rationale**: the do-not-implement table's "managed Postgres HA" reasoning
binds PRODUCTION deployments; it does not bind a developer cloning the tree to
experiment with the dashboard. The binary already isolates its storage behind
one seam (`internal/storage` connection wiring + gorm AutoMigrate over the
registered model fleet, `internal/storage/migration/migration.go`), so a
second backend is a bounded additive slice, not an architecture change. This
amendment discharges the "Amendment-gated items (SQLite backend…)" note in the
ADR's status banner and the tracker row that carried it.

**Configuration (additive, default unchanged)**: `POSTA_DB_DRIVER`
(`postgres` by default; `sqlite` opts into the dev tier — anything else is a
configuration error) and `POSTA_DB_SQLITE_PATH` (the database file; required
when the driver is `sqlite`). With the default driver the wiring executes the
exact pre-amendment code path (no behavior change for any existing
deployment); with `sqlite`, `internal/storage.ConnectSQLite` opens the file
through the pure-Go driver and the server runs `migration.Run` over the same
registered fleet.

**Migration compatibility**: gorm's SQLite dialector renders the model fleet's
DDL; the fleet's Postgres-idiomatic `DEFAULT gen_random_uuid()` column
defaults do not parse as SQLite expression defaults (SQLite requires a
parenthesized expression). The dev-tier connection therefore registers a
thin driver wrapper that rewrites exactly that default, for CREATE/ALTER DDL
only, to the SQLite-native equivalent
(`lower(hex(randomblob(16)))` — a random 32-hex-char identifier of the same
shape); the model tags stay untouched, so the Postgres schema is byte-identical
on every existing deployment. `jsonb`-typed columns are declared as-is (SQLite
accepts arbitrary type names) and carry the application's JSON semantics.

**Documented production guidance (the boundary of the dev tier)**:
- Postgres remains the only supported production store; the dev tier targets
  local development, CI experiments and dashboard evaluations.
- The dev tier does NOT cover the Postgres-idiomatic runtime paths: the
  mailstore's UID claim (`pg_advisory_xact_lock`), the queue's
  `FOR UPDATE SKIP LOCKED` claim substrate, and the Postgres FTS searcher are
  Postgres-only by construction — mail ingestion/queueing/worker flows are
  out of the dev tier's supported surface, which is the dashboard/API core
  over the migrated tables.
- Backup automation (R-11) stays `pg_dump`-based; a dev-tier file is backed up
  by copying the file while no process holds it open.
- The server logs the dev-tier posture loudly at startup; `doctor` guidance
  points operators back to Postgres.

**Consequences**: `internal/storage` gains the SQLite connection + the
driver wrapper; `internal/config` gains the two settings + validation; the
server's migration call stays `migration.Run` on both drivers; tests pin the
round-trip over the migrated fleet, the config gating, and the unchanged
Postgres default. The alternative backends remain rejected; a further
widening (a third backend, or promoting the dev tier) re-opens this amendment
by decision, not by config.

## Amendment (2026-11-18, SF-106): the application-side startup failover selector

§5's posture stands — the RUNTIME switchover of live store connections is a
deployment concern served by managed HA (the runbook:
`docs/docs/operations/deployment.md`, the HA pattern
`docs/docs/operations/redis-ha.md`). What this amendment adds is the
application-side preparation that is testable in a single-node environment:
a health-driven primary/replica DSN selector (`internal/storage/failover.go`)
behind `POSTA_DB_FAILOVER_DSN` (ordered endpoints, primary first; Postgres
only; at least two entries; off by default — unset keeps the wiring
byte-identical). At startup the process opens against the FIRST REACHABLE
endpoint instead of the single DSN; a failed primary fails over to the next
reachable standby, and a recovered primary is re-promoted on the next pick.
The runtime switchover of live connections is deliberately NOT application
code: a hot-swapped `*gorm.DB` under running transactions would be exactly
the double-delivery surface the exactly-once claim substrate (R-26) guards
against — that remainder belongs to managed HA, per §5.
