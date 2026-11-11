---
sidebar_position: 2
title: Redis HA & Failure Semantics
description: Redis high-availability guidance, the per-role failure matrix, and recovery drills (GAP-49, ADR-009 §3)
---

# Redis HA & Failure Semantics

Posta runs on **one Postgres and one Redis** (ADR-009 §3). Redis is the only
stateful service besides Postgres, and it plays more roles than any other
component: queue broker, cache, rate limits, security windows, session
revocation, nonces, worker heartbeats, the pub/sub fabric, and (since GAP-03)
the cron leader lease. This runbook is the SPOF mitigation ADR-009 §3 / risk
RSK-9 commit to: what each role does when Redis is unavailable, how to deploy
Redis with replication, and how to recover.

## The role & failure-mode matrix

Every Redis consumer in Posta has a **deliberate** failure semantic. Nothing
fails by accident; the table below is verified at the cited callsites.

| Redis role | Where | Redis down → | Semantics |
|---|---|---|---|
| **Queue broker** (asynq) | `POSTA_REDIS_ADDR` → `AsynqRedisOpt()`; workers claim tasks | No enqueue, no claim. `emails` rows stay `status=queued` in Postgres | Fail-closed while down; **Postgres is the backstop** — see below |
| **Cron leader lease** (GAP-03) | `internal/cron/lease.go` (`LeaderLease.Acquire`) | Tick skipped — non-holders must not run just because the lease vanished | **Fail-closed** (mutual exclusion is the feature) |
| **Cron dedup claims** (GAP-03) | `internal/cron/lease.go` (`ClaimStore.ClaimOnce`) | Alert jobs proceed; a duplicate is possible, a suppressed customer alert is not | **Fail-open** |
| **Send rate limits** | `internal/services/email/service.go:678-686` (`Allow`/`AllowWithLimits` → `rate_limit:` → 429) | Sends refused for the outage window | **Fail-closed** |
| **Login rate limit** | `internal/services/ratelimit/redis_limiter.go:180-183` (`AllowLogin` returns nil on Redis error) | Logins keep working; brute-force window unenforced | **Fail-open** |
| **API-key rate limits** | `internal/middlewares/middleware.go:390-405` (`apiKeyRateLimitCheck`) | Requests proceed; per-key caps unenforced | **Fail-open** |
| **Quotas** | `internal/services/ratelimit/quota.go` (see `TestQuotasFailClosed`) | Requests refused rather than over-quota | **Fail-closed** |
| **Security engine windows** (brute-force/abuse detectors) | `internal/services/security/engine.go:76-81`, `POSTA_SECURITY_FAIL_OPEN` (default true, `internal/config/config.go:583`) | Detection paused, auth keeps working; set `POSTA_SECURITY_FAIL_OPEN=false` to reverse | **Fail-open by default, operator-selectable** |
| **Session revocation (JTI blacklist)** | `internal/services/session/session.go:53-55` | Revoked-token check skipped — revocation is delayed, not bypassed permanently (revocation rows persist; the check resumes) | **Fail-open** |
| **Form nonces (replay protection)** | `internal/services/messages/nonce.go:87-90` (error → no replay verdict) | Submissions accepted; replay window unenforced | **Fail-open** |
| **Cache** (dashboard/analytics) | `internal/services/cache/cache.go:60-62` (Get error → miss) | Cache misses; queries hit Postgres | **Fail-open** (degraded latency) |
| **Worker heartbeats** | `internal/services/workermon/heartbeat.go:87-93` | Liveness table empty in the admin panel; workers keep working | **Fail-open** (observability only) |
| **Pub/sub fabric** (SSE fan-out, security propagation, mailbox IDLE) | `internal/services/realtime/redis.go:104+`, `internal/services/security/propagate.go:114+`, `internal/services/mailstore/notifier.go:236+` | Live updates pause; go-redis resubscribes on reconnect; state of record lives in Postgres/blobs (clients re-fetch) | **Fail-open**, events during the gap are lost (re-sync on reconnect) |
| **Health probes** | `internal/health/health.go:62-70` (Redis ping marks `/healthz` not-ready) | Monitoring signal — this is how you *see* the outage | Report only |

Two deliberate asymmetries worth restating in prose:

- **The cron leader lease fails closed while its dedup claims fail open.**
  The lease exists so exactly one replica runs each cron tick; on a Redis
  error, "run anyway" would reintroduce the duplicate customer-facing sends
  GAP-03 fixed. The dedup claim is the opposite: it only guards an edge case
  (crash between lease acquire and send), so when Redis is unavailable it must
  not suppress the alert itself.
- **Send limits fail closed, login/API-key limits fail open.** A send burns
  reputation and money; over-limit *sending* is the failure mode quotas
  protect against (same reason quotas fail closed). Locking every human out
  of login because Redis blipped is the opposite trade.

## Queue durability: Postgres is the backstop

The asynq queue lives in Redis, so "Redis lost" would sound like "queued mail
lost". It is not, because Postgres owns the work:

1. `emails.status='queued'` is the source of truth for pending sends (written
   before enqueue).
2. Send tasks use **deterministic task IDs** (`worker.EmailSendTaskID`), so a
   task's existence can be queried by key, not by scanning.
3. `QueueReconcileJob` (`internal/cron/jobs/queue_reconcile.go`, every 5
   minutes — `queue_reconcile.go:83`) re-enqueues any `status=queued` email
   whose task no longer exists (`ErrTaskNotFound`) or sits dead-lettered. The
   claim is a Postgres CAS (`UPDATE ... WHERE status='queued' AND
   updated_at < cutoff`, `queue_reconcile.go:55-60`), so even without the
   leader lease two replicas reconcile at-most-once.

Consequences:

- A Redis **flush** or failover to an empty replica loses at most the asynq
  bookkeeping; queued emails re-enter the queue within one reconcile interval
  (≤ 5 min + schedule skew).
- Delivery is **at-least-once**, not exactly-once: a crash between SMTP
  acceptance and the DB status write can re-send. Attempt history is in
  `mta_delivery_attempts` (OB-1); downstream dedup keys/message-ids are the
  consumer-side seam (ADR-008 §3).
- IMAP/POP3/webmail and all mail content are untouched by a Redis loss —
  mail lives in Postgres + content-addressed blobs (ADR-003); Redis only
  carries IDLE notifications, which re-sync.

## HA deployment guidance

### Recommended: managed Redis with automatic failover

The application addresses Redis through a **single endpoint**
(`POSTA_REDIS_ADDR`, or `POSTA_REDIS_URL` with `rediss://` for TLS —
`internal/config/config.go`, `newRedisConfig`). The supported HA posture is
therefore a managed service that keeps a stable endpoint across failover:

| Provider | Option | Notes |
|---|---|---|
| AWS ElastiCache | Replication group, multi-AZ, cluster-mode off | Stable primary endpoint DNS; automatic failover ≈ 30–60 s |
| GCP Memorystore | Standard tier (HA) | Regional, automatic failover, stable endpoint |
| Azure Cache for Redis | Standard/Premium (replication built in) | Stable hostname; TLS forced (use `rediss://`) |
| Redis Cloud / Upstash | Active-passive or standard | Stable endpoint; check eviction settings below |
| Self-hosted (VM/bare metal) | Redis replication + Sentinel behind a stable VIP/DNS, or a TCP proxy (haproxy `tcp` mode) in front of the primary | Sentinel alone doesn't help this app: the client has a fixed address, so something must move the address — VIP, DNS update, or proxy |

Posta's client (go-redis, single endpoint) reconnects automatically after a
failover; in-flight commands fail once and the callers above absorb them per
the matrix.

### Redis server settings that matter

- **`maxmemory-policy noeviction`** (the default — do not change it). Under
  memory pressure, `allkeys-lru`-style policies silently delete asynq tasks,
  leader leases, and security windows. Size the instance instead; the reconcile
  job backstops evicted *tasks* only if the `emails` row says `queued` —
  eviction is not a supported way to shed queue load.
- **Persistence**: `appendonly yes` + `appendfsync everysec`. AOF bounds
  queue loss on a primary crash to ~1 s and keeps task state across restarts.
  The bundled compose volume keeps RDB snapshots by default — fine for the
  quick-start profile, light for production queues.
- **Auth + TLS**: set `requirepass` (or ACLs) and wire it via
  `POSTA_REDIS_PASSWORD` / `POSTA_REDIS_URL=rediss://…`. Health checks must
  pass the same auth.
- **`replica-read-only yes`** (default) on replicas; promoted replicas are
  writable again automatically.
- **DB selection**: `POSTA_REDIS_DB` (default 0) selects the logical DB —
  share an instance only with matching DB indexes and no eviction policy
  surprises (SF-76 keeps Postgres+Redis the only stateful services).

### Sentinel quick-start (self-hosted)

`compose.sentinel.yml` at the repository root boots a 1 primary + 1 replica +
3 sentinel stack for failover drills. It was verified end-to-end at
HEAD (2026-11-11): boot clean, quorum agrees on the primary, stopping the
primary promotes the replica within ~10 s, the restarted old primary rejoins
as a replica.

```bash
docker compose -f compose.sentinel.yml up -d
docker compose -f compose.sentinel.yml exec sentinel-1 \
  redis-cli -p 26379 -a sentinelpass sentinel get-master-addr-by-name posta
# → 172.28.0.10 6379 (the primary). Then:
docker compose -f compose.sentinel.yml stop redis-primary   # drill
# → sentinels promote 172.28.0.11 (the replica); `start redis-primary`
#   rejoins it as a replica of the new master.
```

Two hard-won notes baked into that file:

- **Sentinel must monitor an address, not a hostname.** With
  `sentinel monitor posta redis-primary 6379 2` (even with
  `resolve-hostnames yes`), the master stayed stuck in subjective-down and no
  quorum ever formed. The drill stack pins fixed container IPs on a dedicated
  subnet and monitors the IP — the quorum then votes and fails over cleanly.
  In production, monitor the primary's stable IP/VIP.
- The stack is for **drills and small self-hosted installs**. Because Posta
  addresses a single endpoint, put a proxy or VIP in front of the primary as
  described above; pointing `POSTA_REDIS_ADDR` at a sentinel process does not
  route data traffic.

## Failure drills

Run these on staging before relying on the production posture (pairs with the
fault-injection item tracked as audit I-J NV-5 in `docs/project/BACKLOG.md`):

1. **Redis restart** (process down, data intact): expect `/healthz` to report
   Redis not-ready within one probe interval; expect queued sends to pause and
   resume; expect the cron leader lease to re-acquire on the next tick
   (fail-closed ticks while down — no double-runs); expect login/API-key paths
   to keep working (fail-open) and sends to answer `429` (fail-closed).
2. **Data loss** (`FLUSHALL`, or failover to an empty replica): expect
   `QueueReconcileJob` to re-enqueue queued sends within ~5 minutes; expect
   the leader lease to be re-taken by whichever replica ticks first; expect
   users to stay logged in (JTI check fails open) and form nonces to be
   reset (replay window restarts).
3. **Primary failover** (managed or sentinel): expect one round of failed
   commands during promotion, absorbed per the matrix; verify `noeviction`
   and AOF settings survived provisioning; verify `mta_delivery_attempts`
   shows no gap in attempt records for the window.

## Monitoring

- **`/healthz`** (and `/readyz`) already ping Redis
  (`internal/health/health.go`) — alarm on `redis != ok`.
- **Worker heartbeats** (`workermon`) vanish when the workers lose Redis —
  alarm on an empty heartbeat set while workers run.
- **Delivery lag**: `emails` rows in `status=queued` older than a few minutes
  indicate the queue or its broker is stuck (the reconcile job's CAS window
  makes a few minutes' age normal under backlog, not before it).
- Delivery lag alarms are the Phase 6 observability item on RSK-9
  (`docs/architecture/RISK-REGISTER.md`); the probes above are the manual
  equivalent today.

## Multi-replica checklist

When you run more than one `server`/`worker` replica:

- [ ] `POSTA_CRON_LEADER_LEASE_ENABLED=true` on **every** replica (GAP-03,
      ADR-009 §2). Off, every replica runs every cron schedule — bounce
      alerts and daily reports **will double-send**.
- [ ] All replicas point at the same Redis (`POSTA_REDIS_ADDR`/`URL`) — the
      lease, dedup claims, asynq claiming (R-26 CAS/SKIP-LOCKED), and the
      pub/sub fabric all assume shared Redis.
- [ ] `POSTA_CRON_LEASE_TTL_SECONDS` (default 60) kept ≥ the smallest cron
      interval (the campaign scheduler runs every minute) so failover
      completes within one interval.
- [ ] `server` replicas behind a reverse proxy; SSE fan-out flows over the
      Redis pub/sub fabric (ADR-009 §2).
