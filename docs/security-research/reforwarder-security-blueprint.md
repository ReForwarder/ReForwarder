# Report F — The ReForwarder Implementation Blueprint: Adopting Stalwart's Abuse-Prevention Design in Go

| | |
|---|---|
| **Subject repository** | ReForwarder (`github.com/goposta/posta`, Posta fork rebranded ReForwarder), working tree at `C:\Users\Ibrahim\Desktop\ReForwarder` |
| **Commit analyzed** | `191d961d9d6f5337b2a06022e97e1f990940397a` (HEAD of default branch, 2026-11-02) |
| **Stalwart reference** | `stalwartlabs/stalwart` clone at `C:\Users\Ibrahim\.zcode\tmp\stalwart-analysis\stalwart`, commit `23b69755cb3cd8ecd952fe0443c5ac067a506184` (tag `v1.0.1`) |
| **Date** | 2026-11-01 |
| **Method** | 20-agent Stalwart investigation (Reports A–D of this series) + ReForwarder repository recon. Every ReForwarder file:line cited below was re-verified against the pinned commit at write time; every Stalwart mechanism was re-verified in the clone. |
| **Report series** | Report F of the ReForwarder security-research series (companion to Reports A, B, C, D) |
| **STATUS** | **DESIGN ONLY — NOTHING IN THIS REPORT HAS BEEN IMPLEMENTED.** Implementation is explicitly out of scope and will not begin until the user approves after reviewing the reports. The backend is currently treated as read-only inherited upstream code; any implementation would be a separate, approved phase. |

## Table of Contents

1. [Goals and non-goals](#1-goals-and-non-goals)
2. [License verdict: why design-adoption, not code-adoption](#2-license-verdict-why-design-adoption-not-code-adoption)
3. [What to adopt from Stalwart (priority-ordered)](#3-what-to-adopt-from-stalwart-priority-ordered)
   - 3.1 [P0 — Accept-time IP blocklist with allowlist-wins and CIDR](#31-p0--accept-time-ip-blocklist-with-allowlist-wins-and-cidr)
   - 3.2 [P0 — Fail2ban with dual keys: IP and hash(login)](#32-p0--fail2ban-with-dual-keys-ip-and-hashlogin)
   - 3.3 [P0 — Per-request blocklist re-check for HTTP](#33-p0--per-request-blocklist-re-check-for-http)
   - 3.4 [P1 — BanService as the single writer (fixing the ReloadBlockedIps footgun)](#34-p1--banservice-as-the-single-writer-fixing-the-reloadblockedips-footgun)
   - 3.5 [P1 — HTTP banned-path globs](#35-p1--http-banned-path-globs)
   - 3.6 [P1 — Finite default ban TTLs and a progressive escalation ladder](#36-p1--finite-default-ban-ttls-and-a-progressive-escalation-ladder)
   - 3.7 [P1 — Self-lockout protections by construction](#37-p1--self-lockout-protections-by-construction)
   - 3.8 [P2 — SMTP connect-stage throttles, session quotas, and accept-time connection rate](#38-p2--smtp-connect-stage-throttles-session-quotas-and-accept-time-connection-rate)
   - 3.9 [P2 — Typed security event model](#39-p2--typed-security-event-model)
   - 3.10 [P3 — Deferred adopts](#310-p3--deferred-adopts)
4. [Fresh designs Stalwart cannot supply](#4-fresh-designs-stalwart-cannot-supply)
5. [Architecture sketch](#5-architecture-sketch)
   - 5.1 [Module layout](#51-module-layout)
   - 5.2 [Data model: banned_ips and allowed_ips](#52-data-model-banned_ips-and-allowed_ips)
   - 5.3 [The in-memory snapshot (copy-on-write, RWMutex-free reads)](#53-the-in-memory-snapshot-copy-on-write-rwmutex-free-reads)
   - 5.4 [The enforcement-order pipeline (12 steps)](#54-the-enforcement-order-pipeline-12-steps)
   - 5.5 [The fail-open / fail-closed matrix](#55-the-fail-open--fail-closed-matrix)
6. [Admin surface sketch (frontend, later phase)](#6-admin-surface-sketch-frontend-later-phase)
7. [Effort and risk table](#7-effort-and-risk-table)
8. [STOP — awaiting user approval](#8-stop--awaiting-user-approval)
9. [Sources](#9-sources)

---

## 1. Goals and non-goals

**Goal.** Give ReForwarder a single, centralized abuse-prevention layer covering both of its public surfaces — the HTTP API (POSTA_PORT, default 9000; form ingest; login; admin UI) and the two SMTP listeners (inbound receiver `POSTA_INBOUND_ENABLED`, default off; relay AUTH listener `POSTA_SMTP_RELAY_ENABLED`, default off) — plus the worker health server's incidental exposure. The layer should decide *before* expensive work happens (parse, bcrypt, GORM queries, Redis round-trips) whether a client IP is welcome, and it should learn that decision from observable misbehavior (failed logins, protocol garbage, loitering, scans) rather than only from static configuration.

Today ReForwarder has competent *rate limiting* (Section 3 shows where) but no *abuse memory*: an attacker who fails forty logins slowly, or sweeps the listener ports, or sends garbage pre-auth, is never identified across requests, never banned, and never shown on any screen. Stalwart solves exactly this problem with a small set of mechanisms (Reports A–D), and the design transfers to Go well. This blueprint specifies what to adopt, what must be designed fresh, and how the pieces map onto ReForwarder's existing seams.

**Non-goals.**

1. **Importing Stalwart code.** No Rust source, in original or transliterated form, will be copied (see Section 2). What is adopted is *documented behavior* — the mechanisms, defaults, ordering, and failure modes established in Reports A–D.
2. **Rewriting the inherited backend beyond the seams named here.** ReForwarder's backend is treated as read-only inherited upstream code. The blueprint names precise hook points (middleware chain, SMTP accept paths, event bus) so that an approved implementation phase can be a strictly additive diff.
3. **Replacing existing rate limiters.** The `RedisLimiter`, `Quotas`, and in-process `IPRateLimiter` stay. The ban layer sits *in front of* them and is fed by them; it does not duplicate them.
4. **Signature-based content filtering, spam scoring, DNSBL queries.** Out of scope; these are post-DATA, message-scoped concerns (Report D §7) that ReForwarder's `messagescan` service already addresses separately.
5. **Multi-tenant abuse isolation.** One global abuse-prevention domain is designed; per-workspace quotas already exist (`internal/services/messages/ratelimit.go`).

---

## 2. License verdict: why design-adoption, not code-adoption

**Confidence: HIGH.** License fields read directly from the clone; ReForwarder SPDX headers read from source.

Stalwart declares its licensing in every crate manifest as a dual license, e.g. `crates/main/Cargo.toml:9` and `crates/common/Cargo.toml`:

```toml
license = "AGPL-3.0-only OR LicenseRef-SEL"
```

The `LicenseRef-SEL` side (Stalwart Enterprise License) grants nothing without a paid subscription, so for ReForwarder it is inert. The `AGPL-3.0-only` side **is** compatible with ReForwarder's own license — ReForwarder is `AGPL-3.0-or-later` (SPDX headers verified across the tree, e.g. `internal/services/messages/ratelimit.go:20`), and a work may incorporate AGPL-3.0-only material into an AGPL-3.0-or-later work. So the license, on its own, does not forbid adoption.

Nevertheless the recommendation is **design-adoption only**, for three reasons stated plainly:

1. **Language boundary.** Stalwart's mechanisms live in Rust with async-trait stores, `trc` event streams, and a settings graph (`Core`) that has no Go counterpart. "Copying" would in practice mean a line-by-line transliteration, which is slow, error-prone, and produces Go that fights Go idiom (`netip`, `atomic.Pointer`, okapi middleware).
2. **Diff hygiene.** ReForwarder is a fork of Posta (`github.com/goposta/posta`). Reimplementing from documented behavior — the four reports in this series — keeps the diff-to-upstream minimal and reviewable, which matters for every future upstream merge. Vendoring translated security code would make those merges painful.
3. **Attribution and obligation surface.** If any code *were* ever transcribed from the AGPL-3.0-only side, AGPL obligations would attach to that code — source disclosure obligations, license notices, and attribution requirements that a fork maintainer must then track forever. Design-adoption from a written specification carries no such encumbrance.

**Decision:** adopt mechanisms, interfaces, defaults, and ordering from the documented behavior in Reports A–D; write Go from scratch; cite Stalwart as the design inspiration in documentation ("fail2ban design after Stalwart Mail Server's network security module") without reproducing source. If, someday, a specific algorithm were transcribed verbatim, the AGPL-3.0-only side of the dual license permits it — but that decision should be made consciously, with the obligations above accepted knowingly.

---

## 3. What to adopt from Stalwart (priority-ordered)

Each item gives: the Stalwart mechanism (file refs into the pinned clone), why it fits ReForwarder, and the ReForwarder mapping.

### 3.1 P0 — Accept-time IP blocklist with allowlist-wins and CIDR

**Stalwart mechanism.** The engine is `BlockedIps` (`crates/common/src/network/security.rs:70-74`): a hash set of exact IPs and a vector of IP networks, each with a TTL (`IpWithTtl`, security.rs:76-80), plus a `has_blocked_networks` boolean that lets the common case (exact IPs only) skip network scans. Enforcement at accept is `is_ip_blocked` in the shared listener path (`crates/common/src/network/listen.rs:277`): a blocked connection is dropped **silently** — no protocol banner, no HTTP status — traced only as `SecurityEvent::IpBlocked` (Report A §5.1). The allowlist (`is_ip_allowed`, security.rs:455+) is checked *before* every blocklist and fail2ban evaluation: **allowlist wins, always** (Report C §5). Networks are supported on both lists via prefix matching.

**Why it fits ReForwarder.** ReForwarder already has the hardest half of this: per-identity allowlists (`IPAllowed`, `internal/middlewares/middleware.go:263-279`, exact-IP-or-CIDR) enforced for API keys (middleware.go:232) and relay credentials (`internal/services/smtprelay/backend.go:107`). What it lacks is the *global* counterpart — a server-wide blocklist consulted at accept time, before any authentication, parsing, or database work. The two lists are symmetric concepts and slot into the same `netip` idioms.

**ReForwarder mapping.**

- Representation: `map[netip.Addr]time.Time` for exact IPs (with expiry), `[]netip.Prefix` for CIDR entries; canonicalize `::ffff:a.b.c.d` via `netip.Addr.Unmap` before lookup (Stalwart does the same before any check, `listen.rs:266-273` — Report A §5.1 step 2).
- Enforcement points, in priority order: (a) the HTTP middleware chain as the outermost middleware; (b) the inbound SMTP server's accept/connect hook (`internal/services/inbound/smtp_server.go`); (c) the relay backend's AUTH path (`internal/services/smtprelay/backend.go`, beside the existing `IPAllowed` call at backend.go:107); (d) the worker health server (cheap, same middleware).
- Blocked HTTP responses: bare `403` with no body explanation (Stalwart's HTTP behavior, Report A §5.3); blocked SMTP connections: drop after accept without banner. Rationale transfers unchanged — no fingerprint for scanners to iterate on.
- The check must read **in-memory state only** (Section 5.3); it must never depend on Redis or Postgres being healthy (Section 5.5).

### 3.2 P0 — Fail2ban with dual keys: IP and hash(login)

This is the single most transferable design in Stalwart.

**Stalwart mechanism.** `is_auth_fail2banned(ip, login)` (`crates/common/src/network/security.rs:336-371`) maintains **two counters per auth failure** under one rate object (`KV_RATE_LIMIT_AUTH`):

- an **IP key** (`ip_to_bytes(ip)`) — catches password spray from one host;
- a **login key** (`blake3::hash(login.as_bytes())`) — catches targeted guessing against one account from a botnet of many hosts.

The check at auth time requires **both** counters to be under limit (`is_allowed = ip_ok && login_ok`); if either exceeds the threshold, `ban_and_reset` bans the IP and resets both counters (security.rs:316-333). Counters are fixed-window, increment-on-check, backed by the in-memory store — they fail open when the store is unavailable. `is_ip_allowed` is consulted first, so allowlisted IPs are never banned by this path.

**Why it fits ReForwarder.** ReForwarder already publishes the exact signal this design needs, on a DB-backed event bus, and **nothing consumes it**. Every failed login emits `user.login_failed` carrying the attempted email, the client IP, and a reason (`internal/handlers/user_handler.go:459-466`; call sites: `unknown_email` :470, `bad_password` :476, `inactive` :481, `bad_2fa` :497). Recon verified there is no fail2ban/lockout/ban consumer anywhere in the tree. Meanwhile the only login protection is `RedisLimiter.AllowLogin` (`internal/services/ratelimit/redis_limiter.go:163-187`): **10 attempts / 15 min keyed on IP alone** (defaults overridable via the admin settings provider, `LoginRateLimitCount()`/`LoginRateLimitWindowMinutes()`, `internal/services/settings/provider.go:154`). IP-only limiting is exactly the gap the login-key counter closes: distributed credential stuffing against one account sails through it.

**ReForwarder mapping.**

- A small subscriber service (`internal/services/f2b`) consumes `*models.Event` from the existing bus (`EventBus.Subscribe()`, `internal/services/eventbus/eventbus.go:89`), filters `user.login_failed`, and increments two Redis fixed-window counters using the existing `ratelimit.Counter` idiom (INCR + EXPIRE NX in one pipeline, `internal/services/ratelimit/quota.go:60-73`):
  - `f2b:auth:ip:<ip>` — window and threshold from config;
  - `f2b:auth:login:<sha256(email)>` — SHA-256 replaces Stalwart's blake3; hashing keeps plaintext usernames out of Redis keys (a property worth preserving).
- A check function `IsAuthFail2Banned(ip, login) bool` evaluates **both** counters (read, not increment — the increment happens on the failure event, matching Stalwart's increment-on-check from the auth-failure side) and, when either is over threshold, calls `BanService.Ban(ip, ReasonAuthFailure, ttl, createdByRule)` and resets both counters.
- The check is invoked at the top of the login handler (and can be surfaced as a middleware variant of `LoginRateLimitMiddleware`, `middleware.go:380-388`, so it composes with the existing 429 path). Distinct response: `403 account temporarily locked` vs. the limiter's `429`, so operators can tell the two apart in logs.
- On ban: also emit a typed security event (Section 3.9) and write the persistent `offense_count` (Section 3.6).
- Threshold defaults (mirroring Report D §2.2's documented Stalwart behavior of a small fixed window, adjusted to ReForwarder's existing 10/15min login-limiter norm): **5 failures / 15 min** per key, both keys sharing one threshold config; either key tripping bans the IP.

**Net effect:** spray is caught by the IP key (as today's limiter does, but with a *ban* instead of a rolling 429), and targeted guessing is caught by the login key — which today nothing catches.

### 3.3 P0 — Per-request blocklist re-check for HTTP

**Stalwart mechanism.** For HTTP, Stalwart re-resolves the client IP from forwarded headers **on every request** and re-checks the blocklist per request, returning 403 mid-connection (`crates/common/src/network/forwarded.rs:15,35-79`; Report A §5.2). SMTP-family protocols, by contrast, pin the IP at accept.

**Why it fits ReForwarder.** Two ReForwarder realities make the per-request re-check more than a nicety:

1. **Keep-alive.** okapi serves HTTP/1.1 keep-alive connections; a client that connects pre-ban would otherwise ride the connection indefinitely past its ban.
2. **Proxy-IP volatility.** Behind `POSTA_TRUSTED_PROXIES` (currently flagged as spoofable when unset — `internal/config/security.go:157-163`), the *effective* client IP (`c.RealIP()`) can change between requests on the same connection as X-Forwarded-For varies. A connection-scoped decision would be keyed to the wrong identity.

**ReForwarder mapping.** The ban middleware (Section 5.1) re-evaluates the snapshot on **every request**, not once per connection. Because the snapshot read is a lock-free map lookup plus (rare) prefix scan (Section 5.3), the cost per request is nanoseconds — Stalwart makes exactly this trade in Rust for the same reason. The middleware also re-reads `c.RealIP()` per request, which okapi already computes per request.

### 3.4 P1 — BanService as the single writer (fixing the ReloadBlockedIps footgun)

**Stalwart mechanism — and its footgun.** Stalwart's automatic bans propagate live (`RegistryChange::Insert(BlockedIp)` broadcast, applied incrementally by peers), but **admin CRUD of `BlockedIp`/`AllowedIp` objects does not propagate at all**: creating or deleting a block via the registry API is inert until an operator fires the separate `ReloadBlockedIps` action or restarts the process (Report C §6.1-6.2; `crates/jmap/src/registry/set.rs` has no broadcast; `crates/common/src/cache/invalidate.rs:92-120` has no `BlockedIp` case). There is no unban function anywhere in the codebase. This asymmetry is the report's headline footgun.

**The design correction.** ReForwarder should have **one component, `BanService`, as the single writer and single propagator** for both lists, so that automatic and administrative writes are indistinguishable — every write takes the same path: persist to Postgres → update the local snapshot → publish the delta on Redis pub/sub → every instance applies it to its snapshot within milliseconds. No reload action, no restart, no stale window in either direction. This is the P1 grouping rather than P0 only because the blocklist (3.1) and fail2ban (3.2) can ship first with instance-local snapshots; the moment a second instance or an admin UI enters the picture, propagation becomes mandatory.

**ReForwarder mapping.**

- Postgres tables `banned_ips` and `allowed_ips` (GORM models, registered in the existing `AutoMigrate` list, `internal/storage/migration/migration.go`), snapshot into memory, propagated over the **existing Redis 7 pub/sub** (`go-redis/v9`; ReForwarder already runs a Redis pub/sub for realtime — `internal/services/realtime/redis.go:61-84` — so the operational pattern is proven in-tree).
- Schema and interface: Section 5.2 and 5.3.
- Expiry: entries carry `expires_at`; the snapshot holds the expiry and evaluates lazily per lookup (Stalwart's approach, Report C §4.4), with a low-frequency sweeper (minute-scale) deleting expired Postgres rows to keep tables bounded.
- Every write — from the f2b rule, from the admin API, from a future CLI — goes through `BanService`, which makes "who banned this" a data field (`created_by`), not a behavioral difference. That is precisely the invariant whose absence creates Stalwart's footgun.

### 3.5 P1 — HTTP banned-path globs

**Stalwart mechanism.** `is_http_banned_path` (`security.rs:266-277`): a configured list of glob patterns is matched against the request path; a hit **instantly bans the requesting IP** (`block_ip` with `BlockReason::PortScanning`) without any counter (Report B §2.4, Report D §2.6). In Stalwart this is evaluated in the HTTP fallback (404) path, so it catches scanners probing for well-known exploit endpoints without taxing the normal routing hot path.

**Why it fits ReForwarder.** ReForwarder's HTTP surface is a public API plus form ingest — precisely the kind of surface that vulnerability scanners sweep (`/wp-admin`, `/.env`, `/phpmyadmin`...). Today each probe costs a route match and a 404; nothing accumulates. Instant-ban-on-banned-path converts a scanner's own reconnaissance into its exclusion.

**ReForwarder mapping.**

- Config: a list of glob patterns in env/admin settings (`POSTA_HTTP_BANNED_PATHS` comma list, e.g. `/wp-*`, `/.env*`, `/phpmyadmin/*`), compiled once at startup with Go's `path.Match` or a small glob lib.
- Evaluation points: (a) the 404 fallback of the okapi router — primary, matching Stalwart; (b) optionally as a cheap pre-auth middleware check on all requests, since the pattern list is tiny and a scanner hitting a *real* route is still probing. Report D's analysis supports the fallback-only placement as the safe default; pre-auth placement is a config flag, default off, because a legitimate client that mistypes a path matching a glob gets banned.
- On hit: `BanService.Ban(ip, ReasonPortScanning, ttl, rule)` + 403 immediately; allowlist consulted first (3.7).

### 3.6 P1 — Finite default ban TTLs and a progressive escalation ladder

**Stalwart mechanism — and its flaw.** Ban periods come from per-reason config keys (`auth-ban-period`, `abuse-ban-period`, `loiter-ban-period`, `scan-ban-period`, all `Option<Duration>` — `crates/registry/src/schema/structs.rs:5106-5120`, mapped in `ban_period`, security.rs:217-224). **When unset, the ban is permanent** (`expires_at.unwrap_or(u64::MAX)`, security.rs:~373-380; Report C §3). And after banning, `ban_and_reset` wipes the triggering counters (security.rs:316-333) with no persisted memory of the offense.

The permanent default is a self-DoS vector: a ban is triggered by *authentication failures*, which means an attacker (or a misconfigured internal service, or one office NAT whose users fat-finger passwords) can **weaponize the ban system against innocent parties behind the same address** — permanently. Report C §3 flags this; it must not be inherited.

**The design correction (mandatory).**

- **Finite defaults, per reason.** Proposed: `rcpt_failure` 1h, `auth_failure` 15m, `loitering` 15m, `port_scan` 1h (scan evidence is stronger), banned-path 24h. Permanent bans **only** via explicit admin action (`expires_at = NULL`).
- **Progressive escalation ladder.** ReForwarder must persist what Stalwart throws away. On each ban, upsert a per-subject `offense_count` row in Postgres; the TTL of the *next* ban for that subject is looked up from the ladder **15m → 1h → 24h → 7d → (admin review)** keyed on that persistent count. This is ReForwarder-specific design (Section 4, "progressive bans": verified absent in Stalwart).
- **Counter reset, but with memory.** Keep `ban_and_reset`'s counter wipe — without it, the surviving Redis counter would instantly re-ban the IP the moment the ban expires. But persist `offense_count` *before* wiping, so the repeat-offender signal survives in Postgres, not in the ephemeral counter. Stalwart's wipe is only a flaw because nothing is persisted first.

### 3.7 P1 — Self-lockout protections by construction

**Stalwart mechanism.** Two safeguards exist. First, allowlist-wins: `is_ip_allowed` is consulted before every fail2ban check and before banned-path bans (security.rs:229-359, 266-277; Report C §5). Second, a global break-glass: `STALWART_RECOVERY_MODE` empties the fail-rate and banned-path configuration at startup (`security.rs:170-193`; Report A §8), disabling all automatic banning in one switch.

**Why it fits ReForwarder.** ReForwarder operators will administer the system over the same HTTP listener that fail2ban watches. A single admin who fat-fingers their password five times (3.2's default threshold) must never lock the operations team out. Stalwart's answer — a restart-level env var — is too coarse.

**ReForwarder mapping (by construction, not by procedure).**

- **Seeded allowlist migration:** the same AutoMigrate phase that creates `allowed_ips` seeds it with `127.0.0.1/8`, `::1/128`, and the configured operator networks (`POSTA_OPERATOR_ALLOWLIST`, a new env parsed at boot; empty by default). Loopback can never ban itself out of existence.
- **Allowlist beats everything:** `Evaluate(ip)` returns *allow* without consulting blocklist, connection rate, or f2b. This subsumes Stalwart's global recovery mode.
- **Per-rule enabled flag:** each `allowed_ips` row carries `enabled` (Section 5.2), so an operator can mute an entry without deleting it — finer-grained than Stalwart's all-or-nothing recovery mode, and it means "recovery" is a settings toggle (`f2b_enabled=false` via the admin settings provider, following the `login_rate_limit_count` pattern, `Settings.vue:28-29` for the UI precedent) rather than a process restart.
- **Documentation rule:** the fail-open/closed matrix (Section 5.5) states explicitly that a Redis outage disables f2b *but not* the blocklist — so "turn off Redis" is never accidentally a recovery procedure.

### 3.8 P2 — SMTP connect-stage throttles, session quotas, and accept-time connection rate

**Stalwart mechanism.** Two object types (Report D §6): **throttles** evaluated at connect stage — per-IP/inbound/relay concurrency and message-rate keys (`throttle` objects with `keys`, `limit`, `window`) — and **session quotas** bounding each SMTP session by elapsed time (`valid_until`) and transferred bytes (Report D §6). Concurrency limiting at accept is enforced in the shared listener path (`listen.rs:286`, over-limit connections dropped silently as `LimitEvent::ConcurrentConnection`).

**Why it fits ReForwarder.** The inbound and relay listeners (`emersion/go-smtp` backends) currently have only the in-process `IPRateLimiter` fixed window (`internal/services/inbound/ratelimit.go:30-86`) attached for connect-rate shedding. They have no concurrency cap per IP, no per-session byte/time budget, and nothing shared across instances.

**ReForwarder mapping.**

- Session quotas: wrap the `go-smtp` session with a deadline context (created at accept; Section 5.4 step 8) and a byte counter on the DATA reader; exceed either → close session with a transient SMTP error. Config keys: `POSTA_SMTP_SESSION_MAX_MINUTES` (default 10), `POSTA_SMTP_SESSION_MAX_BYTES` (default 25 MiB).
- Connect-stage throttle objects: a config-declared list (per-listener concurrency per IP, per listener total concurrency), enforced with an in-process counting map mirroring `IPRateLimiter`'s style; Redis-backed only if multi-instance SMTP becomes real (default: in-process, matching today's deployment shape).
- Two **new** counters (Section 4): per-IP **connection rate at accept** (Redis fixed window: `f2b:conn:ip:<ip>`, threshold/window config) — Stalwart has connection *concurrency* but no connect-*rate* memory (Report B §4) — and **connect-without-data** counting: an accepted connection that closes (or times out) before issuing any SMTP verb increments `f2b:scan:ip:<ip>`, which feeds the same ban path as Stalwart's `scan` detector (security.rs:261-264). Both are P2 because they protect the listeners that are **default-off** today.

### 3.9 P2 — Typed security event model

**Stalwart mechanism (partial).** Stalwart emits security happenings as `trc` events (`SecurityEvent::IpBlocked`, `IpBlockExpired`, etc.) — diagnostic streams, not durable audit (Report C §10: no persistence; Report A §6). ReForwarder can do strictly better cheaply, because it already has the infrastructure.

**ReForwarder mapping.** Every enforcement decision — allow, block, ban, expiry, f2b trip, threshold change — emits a typed event `{type, ip, reason, rule, ttl, actor}`:

1. **In-process, immediately:** `EventBus.PublishSimple(models.EventCategorySecurity, ...)` — a new category constant beside `EventCategoryUser/Email/System/Audit` (`internal/models/event.go:27-30`). The admin Events stream already subscribes to the bus (`internal/handlers/event_handler.go:130`), so the admin Events view picks security events up with zero new plumbing; a dedicated SSE channel for the future security dashboard is a small follow-on.
2. **Persisted audit:** the bus is DB-backed, so events land in the events table automatically; a thin `security_events` view/projection (or reuse of the events table with the new category) gives history and search. This is the audit/history persistence Stalwart OSS lacks (Section 4).
3. **Optional webhook:** reuse `internal/services/webhook` for "POST security events to an external sink" — operator-controlled, default off.
4. **Prometheus counters, later:** `POSTA_METRICS_ENABLED` already exists (`internal/config/config.go:306`); when metrics ship, `bans_total{reason}`, `f2b_trips_total{key_kind}`, `blocked_requests_total` are one-to-one with the emitted event types.

### 3.10 P3 — Deferred adopts

All verified as Stalwart behaviors or close analogs (Reports B/C/D §4-5, §10); all useful; none urgent for ReForwarder's current threat model. Listed with one-line mappings:

- **Per-account request-rate/concurrency for API keys.** Redis fixed window (reuse `ratelimit.Quota`) keyed `ratelimit:apikey:<id>` + an in-process semaphore for concurrency; enforced in the API-key middleware right after `IPAllowed` (middleware.go:232). Stalwart analog: per-account rate limiting in the JMAP/API path.
- **Port-scan heuristics.** Distinct-ports-per-IP within an LRU window; scope to the ports ReForwarder actually exposes (HTTP 9000, the two SMTP listeners when enabled, worker health port) — anything else can never be legitimately reached, so "probed ≥ N distinct exposed ports in M minutes" is a clean signal. Stalwart has only the `scan` fail2ban counter fed by parse errors (security.rs:261-264); the multi-port sweep detector itself must be fresh design (Section 4).
- **IPv6 /64 aggregation option for rate keys.** Rate-limit keys (only keys — never bans) may aggregate IPv6 clients to /64 to blunt cheap /64 rotation; exact-IP bans stay exact to avoid banning innocents sharing a /64. Config flag, default off.
- **Account lockout on N consecutive failures.** The login-key counter from 3.2 already carries the signal; add a *user-level* consequence (set `user.active=false`-adjacent flag or a `locked_until` timestamp on `models.User`) when the login counter trips N times, with admin-unlock via the existing Users admin surface. Distinct from IP bans: stops the guessing even when the attacker rotates IPs.
- **Slowloris read deadlines on SMTP listeners.** Deadline contexts at accept (already proposed in 3.8) plus per-verb read timeouts configured on the `go-smtp` server; direct analog of Stalwart's connection/timeout hardening (Report A §4, Report D §4).
- **Basic-auth-parity anonymous limiting.** Any endpoint that accepts credentials (login, SMTP AUTH) must apply the same per-IP counter regardless of transport — i.e., ensure form-ingest-style quotas (which already fail closed, `quota.go:33-38`) also wrap the login endpoint symmetric to the SMTP AUTH path. This closes the "HTTP login is limited, SMTP AUTH is only locally limited" asymmetry noted in recon.

---

## 4. Fresh designs Stalwart cannot supply

Report B's exhaustive sweep (§1, §6) and Report C's verified-absent list (§10) establish that Stalwart v1.0.1 — despite a mature blocklist/fail2ban core — has **none** of the following. ReForwarder's blueprint must design them from scratch; they are listed here so the later implementation phase can distinguish "adopted design" (Section 3) from "new design" (this section).

| # | Fresh design | What exists in Stalwart (verified absent otherwise) | ReForwarder design sketch |
|---|---|---|---|
| 1 | **Dedicated port-scan detection** | No multi-port sweep, connect+abort, or rapid-same-port tracker anywhere (Report B §1); only the `scan` parse-error counter | LRU window per IP over *observed local ports* (accept hook sees listener identity); trip thresholds: ≥3 distinct exposed ports / 5 min, or ≥5 connect-without-data on one port / 5 min → `Ban(ReasonPortScan)` |
| 2 | **Per-IP connection rate at accept** | Concurrency cap only (`listen.rs:286`); no connect-rate memory | Redis fixed window `f2b:conn:ip:<ip>` incremented in the accept hook; trips → silent drop + event (3.8) |
| 3 | **Account lockout** | No per-account lockout; login-key f2b exists but bans *IPs* | `locked_until` on `models.User` set from the f2b login-counter trips; unlock via admin Users view (3.10) |
| 4 | **Progressive bans** | Fixed per-reason period; permanent default; no offense memory | `offense_count` upsert in Postgres at ban time; TTL ladder 15m→1h→24h→7d (3.6) |
| 5 | **Audit/history persistence of security state** | Events are transient `trc` streams; no ban history table (Report C §10) | Typed events on the DB-backed bus + optional `security_events` projection; ban rows themselves are durable in `banned_ips` (3.9) |

Each is independent; none blocks the P0/P1 adoptions. Items 1 and 2 feed the same `BanService` and event model, so their marginal cost after Sections 3.1/3.4/3.9 is small.

---

## 5. Architecture sketch

### 5.1 Module layout

New packages, following ReForwarder's `internal/services/<name>` convention:

```
internal/services/bansvc/
  service.go      BanService interface + implementation (single writer, Section 3.4)
  store.go        GORM repositories over banned_ips / allowed_ips / offense counts
  snapshot.go     in-memory BlockedIps snapshot (copy-on-write, Section 5.3)
  propagate.go    Redis pub/sub: publish deltas, apply remote deltas, resync on boot
internal/services/f2b/
  subscriber.go   consumes event bus -> Redis counters -> BanService (Section 3.2)
  check.go        IsAuthFail2Banned(ip, login) — dual-counter read at login time
internal/middlewares/
  ban_middleware.go   HTTP enforcement: per-request snapshot check, banned paths, 403
```

Hook points in existing files (all verified present):

- `internal/middlewares/middleware.go` — ban middleware composed into the global chain ahead of auth; sits beside `LoginRateLimitMiddleware` (:380-388) which gains an f2b pre-check.
- `internal/services/inbound/smtp_server.go` — accept hook: snapshot check → connection-rate counter → deadline ctx → session quotas.
- `internal/services/smtprelay/backend.go` — the AUTH/backend path already consulting `IPAllowed` (:107) gains the same accept hook via its serverResources equivalent, plus f2b on AUTH failure (relay credential failures publish to the same bus or increment f2b counters directly).
- `internal/handlers/user_handler.go` — `publishLoginFailed` (:459-466) unchanged; the f2b subscriber is the new consumer.
- `internal/routes/admin_routes.go` — future `/admin/banned-ips` group beside the existing JWT-gated admin group (:34-38), wired to `bansvc` (Section 6; **future phase**).

Configuration follows the two-tier pattern already used for login limits: `POSTA_*` env keys parsed in `internal/config` (e.g. `POSTA_BANS_ENABLED`, `POSTA_F2B_AUTH_ENABLED`, `POSTA_F2B_AUTH_MAX_FAILURES`, `POSTA_F2B_AUTH_WINDOW_MINUTES`, `POSTA_BAN_TTL_*`, `POSTA_HTTP_BANNED_PATHS`, `POSTA_OPERATOR_ALLOWLIST`), with runtime-tunable overrides via the admin settings provider exactly like `LoginRateLimitCount()` (`internal/services/settings/provider.go:154`), so thresholds are adjustable from the admin Settings UI without restart.

The core interface:

```go
type Reason string // rcpt_failure | auth_failure | loitering | port_scan | manual | other

type BanService interface {
    Ban(ctx context.Context, ip netip.Addr, r Reason, ttl time.Duration, by Actor) error
    Allow(ctx context.Context, entry AllowedIP) error          // allowlist write
    IsBlocked(ip netip.Addr) bool                              // snapshot read, lock-free
    IsAllowed(ip netip.Addr) bool                              // allowlist wins over all
    Evaluate(ip netip.Addr) Decision                           // allow | blocked | banned (+ttl, rule)
}
```

`Evaluate` is the one function every enforcement point calls; `IsBlocked`/`IsAllowed` are its fast-path building blocks.

### 5.2 Data model: banned_ips and allowed_ips

GORM models registered in `internal/storage/migration/migration.go`'s `AutoMigrate` list. Mirroring Stalwart's `BlockedIp`/`AllowedIp` registry shapes (Report C §1) and `BlockReason` (`crates/registry/src/schema/enums.rs:261-269`), with the corrections of 3.4/3.6/3.7 applied:

```go
// BannedIP is one blocklist entry. expires_at NULL = permanent (admin only).
type BannedIP struct {
    ID         uint
    Subject    string     // netip.Addr or CIDR text, canonical form
    IsCIDR     bool
    Reason     string     // rcpt_failure|auth_failure|loitering|port_scan|manual|other
    ExpiresAt  *time.Time // NULL = permanent
    CreatedBy  string     // rule|admin|system
    OffenseNo  int        // progressive ladder position (Section 3.6)
    Note       string     // free text for admin entries
    CreatedAt  time.Time
    // index: (subject unique), (expires_at) for the sweeper
}

// AllowedIP is one allowlist entry. Allowlist beats blocklist AND fail2ban.
type AllowedIP struct {
    ID        uint
    Subject   string  // exact IP or CIDR
    IsCIDR    bool
    Label     string  // "office NAT", "monitoring", ...
    Enabled   bool    // per-rule kill switch (Section 3.7)
    CreatedBy string  // seed|admin
    CreatedAt time.Time
}
```

Plus a small `offense_counts(subject string, count int, last_at time.Time)` table (or columns reused from the most recent expired ban) powering the TTL ladder. The migration that creates these tables **also seeds** the loopback/operator allowlist rows (3.7).

### 5.3 The in-memory snapshot (copy-on-write, RWMutex-free reads)

Stalwart's `BlockedIps` (security.rs:70-74) is an immutable struct swapped under a lock — reads are unsynchronized field access on the current version. The Go idiom for the same guarantee is `atomic.Pointer` swap with copy-on-write updates: reads never take a lock and never allocate; writes rebuild the structure and swap. Sketch (~28 lines):

```go
type Snapshot struct {
    exact    map[netip.Addr]time.Time // expiry; empty time = permanent
    prefixes []prefixTTL              // []struct{netip.Prefix; time.Time}
    hasCIDR  bool                     // fast path when no CIDR rows (Stalwart's
                                      // has_blocked_networks, security.rs:73)
}

type Store struct {
    snap atomic.Pointer[Snapshot]
    mu   sync.Mutex // serializes writers only; readers never lock
}

func (s *Store) IsBlocked(ip netip.Addr) bool {
    ip = ip.Unmap() // canonicalize v4-mapped v6 (listen.rs:266-273 analog)
    snap := s.snap.Load()
    if exp, ok := snap.exact[ip]; ok && (exp.IsZero() || time.Now().Before(exp)) {
        return true
    }
    if !snap.hasCIDR {
        return false
    }
    for _, p := range snap.prefixes { // tiny: allow/block CIDR lists are O(10s)
        if p.Contains(ip) && (p.exp.IsZero() || time.Now().Before(p.exp)) {
            return true
        }
    }
    return false
}

// writers: mu.Lock(); next := shallowCopy(cur); mutate; s.snap.Store(next)
```

The same structure (separate instance) serves the allowlist, where `Contains` hit ⇒ `Evaluate` short-circuits to *allow*. Expiry is lazy per lookup (Stalwart's approach, Report C §4.4); a minute-scale sweeper prunes Postgres rows and triggers one snapshot rebuild so memory stays bounded.

### 5.4 The enforcement-order pipeline (12 steps)

Order matters: cheap and identity-establishing checks first; learning checks (counters) after parsing begins; account-scoped checks last. Adopted from the investigation's tracing (Report A §5 for the shared accept path; steps 6/8/9/12 are ReForwarder-specific placements). **Every step emits a typed event** (3.9) on decision, and every step consults the allowlist only at step 4 — allowlist-wins means steps 5-12 are skipped entirely for allowed IPs (matching Stalwart's `is_ip_allowed` guard before each fail2ban, security.rs:229-359).

| # | Step | ReForwarder touchpoint | Backing state |
|---|---|---|---|
| 1 | **accept** TCP connection | listener accept loops (okapi HTTP server; `go-smtp` server goroutine) | — |
| 2 | **PROXY / trusted-proxy unwrap** | `c.RealIP()` equivalent at accept for SMTP; okapi's per-request resolution for HTTP; `POSTA_TRUSTED_PROXIES` (config/security.go:157-163 warning stands) | static config |
| 3 | **canonicalize** v4-mapped IPv6 | `netip.Addr.Unmap()` once, before any keyed check | — |
| 4 | **allowlist check** | `Snapshot.IsAllowed` | in-memory (Postgres-backed) |
| 5 | **blocklist check** | `Snapshot.IsBlocked`; silent drop (SMTP) / bare 403 (HTTP) | in-memory (Postgres-backed) |
| 6 | **per-IP connection rate** | accept hook counter (3.8; fresh design #2) | Redis window, fail-open |
| 7 | **listener concurrency** | per-listener counting map (3.8); over-limit → silent drop | in-process |
| 8 | **spawn with deadline ctx** | session goroutine + `context.WithTimeout` (session quota, 3.8) | — |
| 9 | **pre-auth parse-error scan counter** (+ HTTP banned-path globs at 404 fallback) | inbound relay/`go-smtp` parse-error hook → `f2b:scan:ip:<ip>`; okapi 404 fallback → glob check (3.5) | Redis window, fail-open |
| 10 | **auth (fail2ban)** | `IsAuthFail2Banned(ip, login)` at login handler + SMTP AUTH; dual keys (3.2); relay credential failures join here | Redis counters → BanService |
| 11 | **post-auth account rate / concurrency** | API-key middleware after `IPAllowed` (middleware.go:232); per-account windows (3.10) | Redis window |
| 12 | **teardown loiter hook** | session close path: time-spent-without-data feeds `f2b:loiter:ip:<ip>` (Stalwart's loiter detector, security.rs:282-288) | Redis window, fail-open |

P0 scope = steps 1-5 + 10 (with step 9's banned-path half). P2 adds 6-9 and 12 fully. The pipeline is identical for HTTP and SMTP listeners except where noted (HTTP re-runs steps 2-5 per request per 3.3; SMTP pins at accept).

### 5.5 The fail-open / fail-closed matrix

Each dependency's failure mode is chosen consciously and documented — mixing them accidentally is how such systems lock out the world. Stalwart's own precedent: its fail2ban counters fail open (in-memory store unavailable ⇒ check passes) while its blocklist lives in memory independent of any store (Report C §4).

| Check | Backing | On outage | Rationale |
|---|---|---|---|
| Blocklist / allowlist (steps 4-5) | In-memory snapshot, Postgres-sourced | **Neither.** Last snapshot persists; if never loaded, snapshot is empty ⇒ fail-open, logged loudly | A ban layer that can lock out everyone because Redis blinked is worse than no ban layer; Postgres outage ⇒ stale-but-present enforcement |
| Fail2ban counters (steps 9, 10, 12) | Redis fixed windows | **Fail-open** (counter unavailable ⇒ allow) — matches Stalwart and matches existing `AllowLogin` behavior (`redis_limiter.go:180-181`) | Availability of the login path outranks limiting during a Redis outage; attacker gain is bounded (no bans, limiter already degraded identically) |
| Per-IP connection rate (step 6) | Redis | **Fail-open** | Same as f2b; the in-process concurrency cap (step 7) still bounds damage |
| Expensive protected ops (form ingest quotas) | Redis via `Quotas` | **Fail-closed** (`ErrQuotaUnavailable` treated as exceeded — existing verified behavior, `quota.go:31-38`) | Already ReForwarder's documented choice for public write endpoints; unchanged |
| Ban writes (BanService.Ban) | Postgres + pub/sub | **Buffered retry**: write failure logs + counts against a local fallback set (bounded) flushed on recovery | Losing a ban during a Postgres blip is acceptable; silently pretending it succeeded is not — hence the event emission is part of the same transactional path |

The matrix also encodes the recovery rule from 3.7: there is no configuration in which a Redis outage disables the blocklist, so "turn off Redis" can never serve as an accidental recovery mode.

---

## 6. Admin surface sketch (frontend, later phase)

The admin frontend already has every pattern needed; the security page is an assembly job following existing conventions exactly.

**Admin Security view** (`web/src/views/admin/Security.vue`, following the `Users.vue` table pattern — verified: `usePagination` composable (:9, :56), debounced search (:53-72), `badge badge-dot` status chips (:299), `ConfirmDialog` for destructive actions):

- **Banned IPs tab:** table with columns IP, reason (badge; color per reason enum), created, expires ("permanent" chip when `expires_at IS NULL`), created_by, offense #, and an **Unblock** action behind `ConfirmDialog` (the unban is a `BanService` write like any other — no reload action, per 3.4). Search filters on IP prefix; pagination server-side via the same pageable convention.
- **Allowlist tab:** same table shape over `allowed_ips` with an Enabled toggle (per-rule kill switch, 3.7) and Add/Edit modal (subject, label, enabled).
- **Offense history tab** (optional, cheap once events persist): filtered view of the security event category.

**Admin nav:** a `Security` item in the admin section of `DashboardLayout.vue` (the section verified at DashboardLayout.vue:221-237) and a tile in `AdminQuickLinks.vue`; route registered with `meta: { admin: true }` so the existing admin guard applies (verified guard logic: `web/src/router/index.ts:140-164`, `to.meta.admin && !auth.isAdmin`).

**Threshold settings:** f2b thresholds, ban TTLs, and banned paths land in the admin **Settings** view's existing `Security` category (`web/src/views/admin/Settings.vue` — verified: `category: 'Security'` rows including `login_rate_limit_count` / `login_rate_limit_window_minutes` at :28-29, category tab list at :44), rendered by the same two-column card mechanism, driving the settings-provider keys from 5.1.

**API additions required — FLAGGED AS FUTURE APPROVED PHASE.** This page needs `/admin/banned-ips` CRUD (list/create/delete/patch-enable), `/admin/allowed-ips` CRUD, and a live-events SSE endpoint for the security category — all **backend endpoints that do not exist today** (`internal/routes/admin_routes.go` verified to contain none of these). The backend is currently read-only inherited code; these endpoints are precisely the kind of additive change proposed for the separate approved implementation phase. **A frontend-only fallback is impossible for enforcement** — no amount of admin UI bans anyone; the UI is the viewing console for the backend layer specified in Sections 3-5. It should be built in the same phase as, or after, `bansvc`.

---

## 7. Effort and risk table

| Component | Effort | Risk | Dependency |
|---|---|---|---|
| Blocklist/allowlist snapshot + `Evaluate` (3.1, 5.3) | **M** | Low — pure in-memory; no new infra | `banned_ips`/`allowed_ips` migration (5.2) |
| HTTP ban middleware + per-request re-check (3.1, 3.3) | **S** | Low — outermost middleware; 403 path | Snapshot |
| SMTP accept hooks, inbound + relay (3.1) | **M** | Medium — `go-smtp` serverResources/accept seam must be validated against the vendored API; listeners are default-off, which limits blast radius | Snapshot |
| Dual-key fail2ban subscriber + check (3.2) | **M** | Medium — first consumer of `user.login_failed`; must handle bus backpressure and Redis fail-open path | Event bus (exists), Redis (exists), Snapshot (for bans) |
| BanService single-writer + Redis pub/sub propagation (3.4) | **M** | Medium — the pub/sub resync-on-boot path needs care; existing `realtime` pub/sub is the in-tree precedent | Snapshot, migration |
| Finite TTLs + escalation ladder + offense persistence (3.6) | **S** | Low | BanService, migration |
| Seeded allowlist + enabled flags + settings toggles (3.7) | **S** | Low — migration seeding must be idempotent | Migration, settings provider (exists) |
| Banned-path globs (3.5) | **S** | Low — 404-fallback placement avoids hot path | Snapshot, okapi 404 hook |
| Typed security events + category (3.9) | **S** | Low — bus + Events view already exist | Event bus (exists) |
| SMTP session quotas + deadlines (3.8) | **M** | Medium — byte-counting reader wrapper on DATA; default-off listeners again limit exposure | Accept hooks |
| Connect-rate + connect-without-data counters (3.8, §4 #2) | **S** | Low | Accept hooks |
| Fresh: port-scan detector (§4 #1) | **M** | Medium — heuristics tuning; scoped to exposed ports to bound false positives | Accept hooks, BanService |
| Fresh: account lockout (§4 #3) | **S** | Medium — self-lockout UX; mitigated by 3.7 and admin unlock | f2b login counter, Users admin surface |
| Admin Security frontend (Section 6) | **M** | Low — established patterns only | **Backend endpoints (future approved phase)** |
| Prometheus counters (3.9) | **S** | Low | `POSTA_METRICS_ENABLED` (exists), events |

Sequencing if approved: blocklist + snapshot + HTTP middleware first (enforcement exists day one, even before fail2ban feeds it), then fail2ban, then BanService propagation, then the rest.

---

## 8. STOP — awaiting user approval

**Nothing described in this report has been implemented.** No code, migrations, routes, or frontend changes exist or have been staged. This document is a design for review, to be read together with Reports B and C (and A and D) of this series.

Implementation — the `bansvc`/`f2b` packages, the middleware and SMTP hooks, the migrations, the admin endpoints, and the admin Security page — is **explicitly out of scope until the user reviews this blueprint and approves a phase**. The backend is treated as read-only inherited upstream code; any of the touchpoints named above would be additive changes to it and therefore belong to a separately approved phase with its own plan.

---

## 9. Sources

**ReForwarder (all verified against commit `191d961d9d6f5337b2a06022e97e1f990940397a`):**

- `go.mod` — module `github.com/goposta/posta`, Go 1.27.1
- `internal/services/ratelimit/redis_limiter.go:163-187` — `AllowLogin` (IP-only, 10/15min defaults, **fails open** on Redis error); `:34` settings interface
- `internal/services/ratelimit/quota.go:30-100` — `Quotas`/`Counter` (INCR+EXPIRE NX pipeline; **fails closed**: `ErrQuotaUnavailable`)
- `internal/services/inbound/ratelimit.go:30-86` — in-process `IPRateLimiter`
- `internal/handlers/user_handler.go:459-466` — `publishLoginFailed`; call sites `:470` (`unknown_email`), `:476` (`bad_password`), `:481` (`inactive`), `:497` (`bad_2fa`)
- `internal/middlewares/middleware.go:232` (API-key `IPAllowed` enforcement), `:263-279` (`IPAllowed`, exact-IP-or-CIDR), `:380-388` (`LoginRateLimitMiddleware`, 429)
- `internal/routes/admin_routes.go:34-38` — `/admin` group, `jwtAdminAuth`
- `internal/config/security.go:157-163` — `POSTA_TRUSTED_PROXIES` unset warning
- `internal/config/config.go:306` — `POSTA_METRICS_ENABLED`
- `internal/storage/migration/migration.go` — `AutoMigrate` list; `internal/storage/migration/` (constraints, upgrade)
- `internal/services/messages/ratelimit.go` — form-ingest multi-dimension quotas, honeypot handling, SPDX `AGPL-3.0-or-later` header
- `internal/services/eventbus/eventbus.go:64-89` — `PublishSimple`/`Subscribe`; `internal/models/event.go:24-30` — event categories
- `internal/services/realtime/redis.go:61-84` — existing Redis pub/sub pattern
- `internal/handlers/event_handler.go:130` — admin events SSE via bus subscription
- `internal/services/smtprelay/backend.go:46,59,107` — relay limiter + credential `IPAllowed`
- `internal/services/settings/provider.go:154` — `LoginRateLimitCount()` provider pattern
- `web/src/views/admin/Users.vue` (:9, :53-72, :299) — table pattern; `web/src/views/admin/Settings.vue` (:19-44) — Security settings category; `web/src/views/admin/` (AdminQuickLinks.vue, Events.vue, et al.); `web/src/api/admin.ts:10` (`adminApi`); `web/src/router/index.ts:140-164` (admin guard); `web/src/layouts/DashboardLayout.vue:221-237` (admin nav)

**Stalwart (all verified against clone at commit `23b69755cb3cd8ecd952fe0443c5ac067a506184`, tag v1.0.1):**

- `crates/common/src/network/security.rs:70-80` (`BlockedIps`, `IpWithTtl`), `:217-224` (`ban_period` → permanent default), `:229-263` (rcpt/scan/loiter fail2ban), `:266-277` (`is_http_banned_path`), `:316-333` (`ban_and_reset` counter wipe), `:336-371` (`is_auth_fail2banned` dual keys), `:373+` (`block_ip`, `u64::MAX`), `:455+` (`is_ip_allowed`), `:170-193` (recovery-mode config filtering)
- `crates/common/src/network/listen.rs:266-299` — accept path: canonicalization, `is_ip_blocked` silent drop, concurrency limiter
- `crates/common/src/network/forwarded.rs:15,35-79,106-125` — per-request client-IP resolution, loopback-claim rejection
- `crates/registry/src/schema/enums.rs:261-269` — `BlockReason` enum
- `crates/registry/src/schema/structs.rs:5106-5120` — `*ban_period` config fields
- `crates/jmap/src/registry/mapping/action.rs:84-135`, `crates/common/src/cache/reload.rs`, `invalidate.rs:92-120`, `crates/jmap/src/registry/set.rs` — the `ReloadBlockedIps` footgun (no unban function; admin CRUD does not broadcast)
- `crates/main/Cargo.toml:9`, `crates/common/Cargo.toml`, et al. — `license = "AGPL-3.0-only OR LicenseRef-SEL"`

**Companion reports in this series (`C:\Users\Ibrahim\Desktop\ReForwarder\docs\security-research\`):**

- Report A — `stalwart-security-architecture.md` (§4 mechanism inventory, §5 enforcement pipeline, §6 storage, §8 recovery mode, §10 verified-absent list)
- Report B — `stalwart-port-scanner-detection.md` (§1 verdict: no port-scan detection; §4 closest analogs; §5 implications for ReForwarder)
- Report C — `stalwart-ip-blocking-engine.md` (§1 list shapes, §2 ban flow, §3 permanent-by-default, §4 enforcement points, §5 allowlist precedence, §6 unblocking footgun, §10 verified-absent list)
- Report D — `stalwart-detection-mechanisms.md` (§2 fail2ban engine: counters, detectors, ban-and-reset; §6 throttles/greylisting; §10 explicit absences)
