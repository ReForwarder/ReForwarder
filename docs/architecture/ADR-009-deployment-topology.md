# ADR-009 — Deployment Topology & Scaling

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
