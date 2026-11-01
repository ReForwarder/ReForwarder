# Report A — the reference platform Mail Server Security Architecture

| | |
|---|---|
| **Subject** | the reference platform Mail Server (github.com/reference-platformlabs/reference-platform) |
| **Commit analyzed** | `23b69755cb3cd8ecd952fe0443c5ac067a506184` (tag `v1.0.1`, 2026-10-28) |
| **Report date** | 2026-11-01 |
| **Method** | 20-agent parallel source investigation + main-agent verification pass over the local clone |
| **Audience** | Engineers designing a Go reimplementation (ReForwarder) |
| **Series** | Report A of D — architecture and mechanism inventory. Details of *what is missing* (Report B), *operational behavior* (Report C) and *design lessons* (Report D) live in their own reports. |

**How to read references.** `file:line` references are relative to the repository root at the commit above and were re-verified line-by-line in the clone during the verification pass unless marked otherwise. Confidence tags: **HIGH** = confirmed directly against source in the verification pass; **MEDIUM** = established by the investigation agents and consistent with adjacent code, not re-read line-by-line. Nothing below is derived from documentation alone.

---

## Table of Contents

1. [Executive summary](#1-executive-summary)
2. [Configuration model](#2-configuration-model)
3. [Core runtime structures](#3-core-runtime-structures)
4. [Security mechanism inventory](#4-security-mechanism-inventory)
5. [Enforcement pipeline order and client-visible behavior](#5-enforcement-pipeline-order-and-client-visible-behavior)
6. [Storage layers for security state](#6-storage-layers-for-security-state)
7. [License architecture](#7-license-architecture)
8. [Recovery mode](#8-recovery-mode)
9. [Enterprise-only items relevant to security](#9-enterprise-only-items-relevant-to-security)
10. [Confidence summary and verified-absent list](#10-confidence-summary-and-verified-absent-list)
11. [Sources](#11-sources)

---

## 1. Executive summary

the reference platform v1.0.1 ships a layered abuse-prevention stack that is almost entirely in the AGPL codebase: an accept-time IP blocklist with allowlist-wins semantics, a per-listener concurrency limiter, a four-counter fail2ban engine over a pluggable in-memory store, HTTP banned-path and rate-limit middleware, per-protocol SMTP/IMAP session guards, and standards-based message authentication (SPF/iprev/DKIM/ARC/DMARC via the `mail-auth` crate). Configuration is not a TOML file: every setting is a versioned registry object persisted in a database, hot-reloadable via an `ArcSwap` of the parsed core. Rate counters and fail2ban state live in a separate pluggable "in-memory store" (local hash map, Redis, or an enterprise sharded backend), deliberately decoupled from the main data store so that counter durability/scale can be chosen independently of mail storage.

Three structural facts matter most to a reimplementation:

1. **The config file is only a bootstrap pointer.** The on-disk JSON file read at startup is exclusively a *DataStore descriptor* telling the server where the registry lives; all security settings are rows in that registry (`crates/store/src/registry/local.rs:79-94`).
2. **Security state is split across three tiers** — in-process (blocked-IP set, concurrency counters), in-memory-store (rate/fail2ban counters), registry (durable `BlockedIp` objects, all settings) — each with its own reload and propagation mechanism (Section 6).
3. **The default ban period is unset**, which the code maps to an indefinite expiry (`u64::MAX`): out of the box a tripped fail2ban counter results in a ban that never expires on its own (`crates/common/src/network/security.rs:374-380` vs. the `None` defaults in `crates/registry/src/schema/structs_impl.rs:58380-58409`). This was flagged during the verification pass and is worth replicating deliberately, not accidentally.

---

## 2. Configuration model

**Confidence: HIGH**

### 2.1 There is no TOML `[security]` section in v1.0.1

The v0.x TOML configuration format is gone. In v1.0.1, settings are *registry objects*: typed, versioned, individually addressable records stored in a database. The `UPGRADING/v0_16.md` file documents the TOML → registry migration introduced in v0.16.

### 2.2 The local JSON file is only a DataStore descriptor

The file passed via `--config` is parsed as a `DataStore` descriptor and nothing else:

- `crates/store/src/registry/local.rs:79-94` — `read_data_store()` does `tokio::fs::read_to_string(&self.local_path)` followed by `serde_json::from_str::<DataStore>(&contents)` (line 81). A missing file means "bootstrap mode"; a parse error is a fatal-format error surfaced to the operator, not a fallback to defaults.

This descriptor says *where the registry lives* (PostgreSQL, MySQL, SQLite, RocksDB, FoundationDB, …). Every other setting — `Security`, `Http`, `Imap`, `Jmap`, `WebDav`, `SystemSettings`, `MtaInboundSession` / `MtaStage*` (Connect/Ehlo/Mail/Rcpt/Auth/Data), `SpamSettings`, etc. — is a versioned pickle blob (the `pickle` module serializes schema structs with explicit field ordinals) read from and written to that registry store.

### 2.3 Parse-failure tolerance: boot on defaults, abort reload on errors

Settings are loaded through `Bootstrap::setting_infallible::<T>()`, which deserializes the registry object, runs `ObjectImpl::validate`, and on any validation failure substitutes `T::default()` (the hand-written `impl Default` in the generated schema, e.g. `crates/registry/src/schema/structs_impl.rs:58373-58412` for `Security`). Consequences:

- A corrupt or invalid `Security` object does **not** prevent boot: the server starts with serde defaults and logs the validation errors. The operator sees a degraded configuration, not a dead server.
- On **hot reload**, the opposite policy applies: the reload path parses the new core, and only if `bootstrap.errors.is_empty()` does it swap the live pointer (`crates/common/src/cache/reload.rs:132-143` — `if bootstrap.errors.is_empty() { let core = Core::parse(...).await; ... self.inner.shared_core.store(core.into()); }`). A reload that produces any parse error is **aborted wholesale**; the previously valid configuration keeps serving.

For a Go reimplementation this is a deliberate and defensible pair of trade-offs: *fail-soft at boot, fail-closed at reload*.

---

## 3. Core runtime structures

**Confidence: HIGH**

### 3.1 Server, Inner, Core

```text
Server {
    inner: Arc<Inner>,
    core:  Arc<Core>,
}

Inner {                                  // crates/common/src/lib.rs:171-176
    shared_core: ArcSwap<Core>,          // hot-reloadable snapshot of all parsed config
    data: Data,
    cache: Caches,
    ipc: Ipc,
}

Data {                                   // crates/common/src/lib.rs:179-202
    ...
    blocked_ips: RwLock<BlockedIps>,     // line 185 (parking_lot RwLock)
    ...
}
```

`Arc<Core>` for the steady state plus an `ArcSwap<Core>` in `Inner` is the entire hot-reload mechanism: readers grab a cheap `Arc` snapshot of the current core, and a reload publishes a freshly parsed `Core` with a single store. `blocked_ips` is the one security structure that is *not* inside `Core` — it is a hot-mutated side table on `Data` (Section 6.2 explains why).

`BlockedIps` (`crates/common/src/network/security.rs:69-74`):

```rust
pub struct BlockedIps {
    pub blocked_ip_addresses: AHashSet<IpWithTtl<IpAddr>>,
    pub blocked_ip_networks: Vec<IpWithTtl<IpAddrOrMask>>,
    pub blocked_ip_networks ... has_blocked_networks: bool,
}
```

Exact IPs live in a hash set; networks (CIDR/mask entries, normally operator-defined) in a linear vec guarded by a `has_blocked_networks` fast flag so the common exact-IP path never scans. Entries are `IpWithTtl<T>` — value + `expires_at` — so expiry is checked at lookup time, not by a background sweeper.

### 3.2 Per-listener state: `ServerInstance`

Each configured listener becomes one `ServerInstance` (`crates/common/src/network/mod.rs:55-64`):

```rust
pub struct ServerInstance {
    pub id: String,
    pub protocol: ServerProtocol,
    pub acceptor: TcpAcceptor,
    pub limiter: ConcurrencyLimiter,          // per-listener, global per that listener
    pub proxy_networks: Vec<IpAddrOrMask>,    // PROXY protocol trusts
    pub tls_timeout: Duration,
    pub shutdown_rx: watch::Receiver<bool>,
    pub span_id_gen: Arc<SnowflakeIdGenerator>,
}
```

Note what is *per-listener* (limiter, PROXY networks, TLS acceptor) versus *per-server* (blocklist, security config inside `Core`). `ConcurrencyLimiter` is a lock-free `AtomicU64` counter against a max (`crates/common/src/network/limiter.rs:97-126`): `is_allowed()` does a relaxed load, an increment, and returns an `InFlight` guard whose `Drop` decrements — no queues, no waiting, admission is binary.

### 3.3 The in-memory store (counter backend)

Rate and fail2ban counters do not live in the main data store. They live in `core.storage.memory`, an `InMemoryStore` enum (`crates/store/src/lib.rs:425-439`):

```rust
pub enum InMemoryStore {
    Sql(Store),
    Local(Arc<backend::local::LocalStore>),
    Redis(Arc<backend::redis::RedisStore>),
    Http(Arc<HttpStore>),
    Static(Arc<StaticMemoryStore>),
    Sharded(...),      // #[cfg(feature = "enterprise")]
}
```

Accessed everywhere via the one-line accessor `Server::in_memory_store()` → `&self.core.storage.memory` (`crates/common/src/storage/mod.rs:73-76`). Of the variants, only **Local**, **Redis** and the enterprise **Sharded** backend implement the counter/throttle operations (`is_rate_allowed`, `key_incr`, `rate_reset`); **Sql**, **Static** and **Http** do not support counter ops and are rejected when configured as the memory store with those features in use.

The Local variant is hard-blocked when a cluster coordinator is configured (`crates/common/src/config/storage.rs:32,55` — the coordinator is built from the memory-store configuration), because a node-local hash map cannot provide cluster-consistent counters. **Confidence: MEDIUM** for the exact error path (pre-verified by investigation), **HIGH** for the enum variants and accessor (re-read).

---

## 4. Security mechanism inventory

**Confidence: HIGH** unless marked. Each entry: mechanism — behavior — reference.

### 4.1 Accept-time IP blocklist (allowlist wins)

`Server::is_ip_blocked(ip)` (`crates/common/src/network/security.rs:441-453`) returns true if the IP is in the blocked set or matches a blocked network **and is not on the allowlist** — the final `&& !self.is_ip_allowed(ip)` (line 452) implements allowlist-wins in a single expression. `is_ip_allowed` (455-470) mirrors the structure over the allow sets in `Core.network.security`. Expiry is checked per lookup (`!v.is_expired()`). Additionally, loopback is unconditionally added to the allow set at parse time (`crates/common/src/network/security.rs:158-168`, `#[cfg(not(reference-platform_tests))]`), so localhost can never be locked out by an operator misconfiguration. Checked once per accepted connection at `crates/common/src/network/listen.rs:277` inside `BuildSession::build_session`.

### 4.2 Per-listener concurrency limiter

One `ConcurrencyLimiter` per listener, checked immediately after the blocklist at `crates/common/src/network/listen.rs:286`; over-limit connections are dropped silently with a `LimitEvent::ConcurrentConnection` trace (listen.rs:301-305). Default max is 8192 concurrent in-flight connections per listener (**MEDIUM**: default value pre-verified; limiter mechanics at `limiter.rs:97-126` re-verified).

### 4.3 Fail2ban engine — four counters

All four counters share the same shape (rate check → on trip: `ban_and_reset` = `block_ip` + counter reset; `crates/common/src/network/security.rs:316-334`) and all skip evaluation entirely for allowlisted IPs (e.g. security.rs:298-300, 337-338). Defaults live in `impl Default for Security` (`crates/registry/src/schema/structs_impl.rs:58373-58412`); counter namespaces are byte prefixes `KV_RATE_LIMIT_RCPT=2, SCAN=3, LOITER=4, AUTH=5` (`crates/common/src/lib.rs:146-154`).

| Counter | Trigger | Keys | Default rate | Ref |
|---|---|---|---|---|
| Auth | Authentication failures, all protocols | dual: `ip_to_bytes(ip)` + `blake3(login)` | 100 / 24 h | security.rs:336-369; defaults 58381-58384 |
| Rcpt ("abuse") | Failed RCPT TO lookups | dual: `ip_to_bytes(ip)` + `blake3(rcpt)` | 35 / 24 h | security.rs:229-259; defaults 58376-58379 |
| Scan | Pre-auth protocol **parse errors**, pooled across protocols (SMTP/IMAP/POP3/ManageSieve share one counter namespace) | single: IP | 30 / 24 h | security.rs:261-268; defaults 58405-58408 |
| Loiter | SMTP sessions exceeding the session-duration expression | single: IP | 150 / 24 h | security.rs:282-289; defaults 58386-58390 |

Design notes worth copying:

- **Dual-keying** on auth/rcpt means both "this IP is brute-forcing" and "this *account* is being brute-forced from many IPs" trip the ban; the login/recpt key is `blake3`-hashed to keep raw strings out of the counter store.
- **Pooled scan counter**: one cross-protocol parser-error counter catches multi-protocol scanners that keep each individual protocol just under its threshold.
- On trip, the engine bans the IP *and resets the counter* (`ban_and_reset`, security.rs:316-334), so post-ban traffic is handled by the blocklist path, not by repeated counter trips.

**Ban period semantics (flagged during verification, HIGH):** the per-reason ban periods (`authBanPeriod`, `abuseBanPeriod`, `loiterBanPeriod`, `scanBanPeriod`) are `Option<Duration>` with `None` defaults (`crates/registry/src/schema/structs_impl.rs:58380,58385,58390,58409`; schema fields at `crates/registry/src/schema/structs.rs:5106-5120`; runtime mapping at `security.rs:177-180, 218-224`). In `block_ip`, `ban_period(reason).map(|v| now + v)` falls back to `expires_at.unwrap_or(u64::MAX)` (`security.rs:374-380`). **Out of the box, fail2ban bans are therefore indefinite** — they persist in the registry as `BlockedIp` objects with no expiry and survive restarts until an administrator deletes them. A reimplementation must decide explicitly whether to copy this or ship a sane non-`None` default.

### 4.4 HTTP banned-path globs → instant ban

`Server::is_http_banned_path(path, ip)` (`security.rs:270-280`): if the request path matches any banned glob and the IP is not allowlisted, the IP is banned *immediately* (`block_ip` with `BlockReason::PortScanning`) — no counter, no threshold. Twelve default globs target PHP/CMS/path-traversal scanners (`*.php*`, `*.cgi*`, `*.asp*`, `*/wp-*`, `*/php*`, `*/cgi-bin*`, `*xmlrpc*`, `*../*`, `*/..*`, `*joomla*`, `*wordpress*`, `*drupal*`; `structs_impl.rs:58391-58404`), compiled at parse time to glob matchers (`security.rs:187-192`).

### 4.5 HTTP request rate limits

`crates/common/src/auth/rate_limit.rs`:

- **Authenticated, per-account**: `is_http_authenticated_request_allowed` (22-59) counters on `KV_RATE_LIMIT_HTTP_AUTHENTICATED=8`, keyed by `account_id: u32` big-endian; default 1000 requests / 60 s (`structs_impl.rs:36988-36991`). Tokens with `Permission::UnlimitedRequests` bypass (rate_limit.rs:28). There is also a per-token concurrent-request limiter via `access_token.is_http_request_allowed()` (rate_limit.rs:46-58).
- **Anonymous, per-IP**: `is_http_anonymous_request_allowed` (137-154) counters on `KV_RATE_LIMIT_HTTP_ANONYMOUS=9`, keyed by the client IP; default 100 requests / 60 s (`structs_impl.rs:36992-36995`).

### 4.6 IMAP request rate and concurrency

Per-account: 2000 requests / 60 s (`KV_RATE_LIMIT_IMAP=10`) and 16 concurrent connections; 3 auth failures per connection (`structs_impl.rs:38428-38433`). Enforcement at `rate_limit.rs:61-84` and the `AccountLimits` bundle at `rate_limit.rs:122-135`.

### 4.7 SMTP throttles and session guards

- **`MtaInboundThrottle` registry objects**: per-listener/protocol inbound throttles evaluated *before the greeting*; default configuration is empty (no throttles). **MEDIUM** (pre-verified; object model consistent with the stage/session objects below).
- **Session object `MtaInboundSession`** (defaults `structs_impl.rs:45432-45448`): `maxDuration` = `10m` (exceeding it triggers the **loiter** fail2ban counter, Section 4.3), `idleTimeout` = `5m`, `transferLimit` = `262144000` (250 MiB).
- **Stage objects** (all defaults are JMAP-style *expressions*, evaluable per session with variables like `local_port`, `is_tls`, `authenticated_as`):
  - `MtaStageAuth`: `maxFailures` = 3, `waitOnFail` = 5 s, SASL mechanisms restricted to `[plain, login, oauthbearer, xoauth2]` on non-port-25 TLS connections, AUTH required for non-25 ports (`structs_impl.rs:47295ff`).
  - `MtaStageRcpt`: `maxFailures` = 5, `waitOnFail` = 5 s, `maxRecipients` = 100, relaying only for authenticated sessions (`structs_impl.rs:48663ff`).
  - `MtaStageData`: `maxMessages` = 10 per session, `maxReceivedHeaders` = 50, `maxMessageSize` = 100 MiB default (`structs_impl.rs:47989-47997` and the expression-context fallback at 47705-47709).

### 4.8 Greylisting

Present but **disabled by default** (`greylist_for: Default::default()` → `None`, `structs_impl.rs:65672`).

### 4.9 Content-time defenses

- **Post-DATA spam filter** with DNSBL and Pyzor contributing *scores* to a verdict, never connect-blocking. **MEDIUM** (pre-verified).
- **Message authentication**: SPF, reverse-DNS, DKIM, ARC and DMARC via the `mail-auth` crate; results surface as `Authentication-Results` / evaluation context. **MEDIUM** (pre-verified).
- **Policy hooks at SMTP stages**: trusted Sieve scripts, milters, and HTTP hooks can run at each `MtaStage*` boundary. **MEDIUM** (pre-verified).

### 4.10 Account/credential hygiene

- **OAuth**: PKCE with `S256` is enforced for public clients; the token endpoint verifies the archived `PkceCodeChallenge::S256` against a recomputed SHA-256 of the verifier (`crates/http/src/auth/oauth/token.rs:728-758`); the password grant is absent. **MEDIUM** for the "mandatory for public clients" clause, HIGH for S256 verification code.
- **Per-credential `allowedIps`**: API keys and app passwords carry an IP allowlist checked at token validation — `AccessToken::assert_is_valid(remote_ip)` returns `SecurityEvent::IpUnauthorized` ("IP address not allowed.") when the credential's `allowed_ips` is non-empty and none match (`crates/common/src/auth/access_token.rs:623-635`).
- **Contact form**: honeypot field plus a per-IP rate limit (`KV_RATE_LIMIT_CONTACT=7`, `crates/common/src/lib.rs:151`; default 5 submissions / hour, `structs_impl.rs:37591-37594`; implementation in `crates/http/src/form/mod.rs`).
- **Password policy**: hashes via **Argon2id** (default, `structs_impl.rs:6570`), strength via **zxcvbn with minimum score 3** (`PasswordStrength::Three`, `structs_impl.rs:6573`), min/max length checks in `Server::is_secure_password` (`security.rs:472-484`).

---

## 5. Enforcement pipeline order and client-visible behavior

**Confidence: HIGH for the shared accept path (re-traced); MEDIUM for protocol-specific tails (pre-verified by the tracing agent).**

### 5.1 Shared accept path (all listeners)

Per accepted TCP connection, in order:

1. **PROXY protocol unwrap** if the peer is in the listener's `proxy_networks` (Section 3.2) — the client IP becomes the PROXY header address.
2. **IPv4-mapped-IPv6 canonicalization**: `::ffff:a.b.c.d` is normalized to the IPv4 form *before* any security check (`crates/common/src/network/listen.rs:266-273`), so blocklist keys are canonical.
3. **`is_ip_blocked`** (`listen.rs:277`) — blocked IPs are dropped *silently*: no protocol banner, no SMTP/HTTP response, only a `SecurityEvent::IpBlocked` trace event (listen.rs:278-284) and a `None` session (the connection is closed).
4. **Concurrency limiter** (`listen.rs:286`) — over-limit connections are likewise dropped silently, traced as `LimitEvent::ConcurrentConnection`.
5. **Per-protocol session** (`SessionData` construction, listen.rs:288-299).

### 5.2 Protocol-specific tail

- **HTTP**: the client IP is *re-resolved on every request* from headers according to `clientIpSource` (`Peer` default, `xForwardedFor`, or `Forwarded`), with `forwardedHops` clamped to 1..=16 (`MAX_FORWARDED_HOPS = 16`, `crates/common/src/network/forwarded.rs:15, 35-45`; resolution walks the header list right-to-left, `forwarded.rs:51-79`). **Loopback claims in forwarded headers are rejected** (`parse_node` → `is_loopback_claim`, `forwarded.rs:106-125`), including IPv4-mapped loopback v6 encodings. The blocklist is re-checked per request and returns **403**.
- **SMTP / IMAP / POP3 / ManageSieve**: the IP is **pinned at accept** and used for all per-connection checks; forwarded headers are never consulted.

### 5.3 What a client observes

| Condition | Unbound port | Blocked at accept | Banned mid-session |
|---|---|---|---|
| Observation | kernel connection-refused (RST) | silent drop after accept (SMTP/IMAP/etc.) or HTTP 403; no explanation | protocol-level error, then close |

The silent-drop-at-accept behavior is intentional: blocked scanners get no fingerprint to iterate on, and HTTP gets a bare 403.

---

## 6. Storage layers for security state

**Confidence: HIGH** (structure and refs re-verified) with **MEDIUM** on fail-open/closed call sites (pre-verified; consistent with the `?`-propagation shape of the code).

| State | Where it lives | Reads/writes | Propagation & reload |
|---|---|---|---|
| Fail2ban + rate counters | In-memory store: **Local** (1,048,576-entry cap, `crates/store/src/backend/local/mod.rs:39` `1 << 20`, with a lazy sweep scored by age — `backend/local/maintenance.rs:17,58-78`) or **Redis** (atomic INCRBY + EXPIRE, `backend/redis/lookup.rs:44-66`); TTL = remainder of the rate window | `is_rate_allowed` / `key_incr` / `rate_reset` | Ephemeral by design; lost on restart except Redis |
| Active bans (runtime) | `Inner.data.blocked_ips` (`parking_lot::RwLock<BlockedIps>`, `crates/common/src/lib.rs:185`) | `block()` on ban; `is_ip_blocked` per connection | Hot-mutated in place, independent of `Core` swaps |
| Active bans (durable) | Registry `BlockedIp` objects (address, reason, created/expires) written on every ban (`security.rs:383-435`; conflict handling: keep the longer expiry, 397-415) | Loaded at boot into `blocked_ips`; incremental updates on change | Cluster broadcast `RegistryChange::Insert(ObjectType::BlockedIp)` on pubsub topic **`stwt.agora`** (`crates/services/src/broadcast/mod.rs:37`; transports Redis PubSub / NATS / Zenoh / Kafka) |
| Allowed/blocked IP config | Registry `AllowedIp` / `BlockedIp` objects | Parsed into `Core.network.security` (allowed, `security.rs:82-119`) and into `Data.blocked_ips` (blocked) | Hot-reload: `RegistryChange::Insert` applies the single object incrementally; `Reload` re-parses the full list |
| All security settings | Registry objects (Section 2) | `setting_infallible` at parse; `ArcSwap<Core>` snapshot read per request | New sessions pick up new config after a `ReloadSettings` action swaps `shared_core` |

Behavioral properties worth replicating:

- **Fail-open vs fail-closed.** On in-memory-store errors, the fail2ban, greylisting and SMTP-throttle checks degrade *open* (an error in the counter path does not block the session); the HTTP and IMAP request-limit checks degrade *closed* (the error propagates as a request error). The asymmetry matches blast radius: a counter-store outage should not take down mail flow, but it also should not silently unlock the HTTP API.
- **Listener immutability on reload.** Hot reload rebuilds `Core` and re-parses tracers, but **listeners are never respawned**: socket, TLS acceptor, PROXY-trusted networks and concurrency limits are fixed at process start (`crates/common/src/cache/reload.rs:133-143` — the swap updates the core only; the existing `Listeners` survive). Changing a listener port or its TLS identity requires a restart. **HIGH** for the no-respawn behavior (reload.rs re-read), **MEDIUM** for the exact per-field classification.
- **Security config hot-apply scope.** New `Security` values apply to *new sessions* after a `ReloadSettings` action; in-flight sessions keep their old snapshot because they hold an `Arc<Core>`.

---

## 7. License architecture

**Confidence: HIGH** (headers, counts and file names re-verified in the clone).

- **Per-file dual licensing.** Source files carry the SPDX header `SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-SEL` — verified on the core security files: `crates/common/src/network/security.rs:1-5`, `crates/common/src/network/limiter.rs:1-5`, `crates/registry/src/types/ipmask.rs:1-5`.
- **SEL snippets.** Enterprise-only code is fenced by `// SPDX-SnippetBegin … SPDX-License-Identifier: LicenseRef-SEL … // SPDX-SnippetEnd` blocks, compiled only under `#[cfg(feature = "enterprise")]`. Repo-wide at this commit: **149 files** contain `SPDX-SnippetBegin` blocks and 146 files contain `cfg(feature = "enterprise")` gates (verified by grep over `*.rs`).
- **The security path is AGPL.** `security.rs` contains exactly two SEL snippets, both tenant-role defaults (`crates/common/src/network/security.rs:54-60` for `default_role_ids_tenant` and 196-202 for its initializer). All fail2ban, blocklist, limiter, rate-limit, banned-path and recovery logic is AGPL-side. A reimplementation can therefore take the whole abuse-prevention design from the AGPL grant.
- **LICENSES/ directory** (verified listing): `AGPL-3.0-only.txt`, `LicenseRef-SEL.txt` (the reference platform Enterprise License 2.0 — a proprietary subscription license), plus attribution texts `Apache-2.0.txt`, `MIT.txt`, `Unlicense.txt`.

---

## 8. Recovery mode

**Confidence: HIGH** (all hooks re-verified in the clone).

Recovery mode is a break-glass runtime mode selected by the **`REFERENCE-PLATFORM_RECOVERY_MODE`** environment variable (`Registry::is_recovery_mode`, `crates/store/src/build/registry.rs:112`). When active:

- **Fail2ban rates are disabled** — all four counters are `.filter(|_| !is_recovery_mode)`'d to `None` at parse (`crates/common/src/network/security.rs:181-183, 193`).
- **HTTP banned paths are emptied** (`security.rs:184-192`).
- **Blocklist starts empty** (no blocklist enforcement from persisted bans). **MEDIUM** for this clause (pre-verified; the disabling hooks above were directly verified).
- **HTTP rate limits are disabled** — `rate_authenticated` / `rate_anonymous` forced to `None` in the HTTP config (`crates/common/src/config/network.rs:500-505`).
- **Permissive CORS** — `use_permissive_cors || is_recovery_mode` (`crates/common/src/config/network.rs:453`).
- **A dedicated recovery HTTP listener** binds `[::]:8080` by default, overridable via `REFERENCE-PLATFORM_RECOVERY_MODE_PORT` (`crates/common/src/config/server/listener.rs:38-44, 79`; startup wiring in `crates/common/src/manager/boot.rs:178-187`).
- **Break-glass admin**: `REFERENCE-PLATFORM_RECOVERY_ADMIN=admin:<password>` is read by the local registry bootstrap (`crates/store/src/registry/local.rs:50`; the setup error message in `crates/store/src/build/registry.rs:45` directs operators to it). The credential is checked in `authenticate_basic` *before* directory authentication, with a special `AccessToken::new_admin()` path (`crates/common/src/auth/authentication.rs:133-175`).

**Critical design detail:** the fallback-admin check at `authentication.rs:133-175` has **no recovery-mode gate** — `REFERENCE-PLATFORM_RECOVERY_ADMIN` is honored in normal operation too. It is a permanent break-glass credential, not a recovery-mode-only one. A reimplementation should decide whether to keep that property (useful when the registry itself is broken) and must weight the credential accordingly.

Related: `REFERENCE-PLATFORM_RECOVERY_MODE_LOG_LEVEL` adjusts logging verbosity in recovery mode (`crates/common/src/config/telemetry.rs:689`).

---

## 9. Enterprise-only items relevant to security

**Confidence: HIGH for code-gated items (verified); MEDIUM for feature descriptions (pre-verified).**

- **Sharded in-memory store** (`InMemoryStore::Sharded`, `crates/store/src/lib.rs:433-439`): distributed rate/fail2ban counters across a cluster — the scaling story for counter ops when one node's Redis/Local backend is not enough.
- **Tracing and metrics stores** (`crates/common/src/storage/mod.rs:78-89`, both `#[cfg(feature = "enterprise")]`): persistent security-event history, threat feeds of past events, the material a "security audit log" UI would be built on. The AGPL side emits the events (`trc::SecurityEvent::*`) but has no durable event store.
- **AI spam classifiers**: enterprise-gated model-backed classification augmenting the scored filter.
- **SCIM provisioning, tenants** (e.g. `default_role_ids_tenant`, `security.rs:54-60, 196-202`).
- **Webadmin live tracing**: SSE endpoint `/api/live/tracing`, permission-gated by `Permission::LiveTracing` (`crates/registry/src/schema/enums.rs:1946`; string mapping `enums_impl.rs:8338, 9617`).

---

## 10. Confidence summary and verified-absent list

### Confidence summary

| Section | Confidence | Basis |
|---|---|---|
| 2. Configuration model | HIGH | local.rs / reload.rs re-read; UPGRADING/v0_16.md present |
| 3. Runtime structures | HIGH | lib.rs, mod.rs, limiter.rs, store/lib.rs, storage/mod.rs re-read (§3.3 hard-block: MEDIUM) |
| 4. Mechanism inventory | HIGH | security.rs, rate_limit.rs, forwarded.rs, schema defaults re-read; §4.7 throttles, §4.9 content defenses: MEDIUM |
| 5. Pipeline order | HIGH (accept path) / MEDIUM (protocol tails) | listen.rs re-traced; HTTP 403 re-check and mid-session behavior pre-verified |
| 6. Storage layers | HIGH (structure) / MEDIUM (fail-open/closed call sites) | counters, bans, broadcast topic re-verified |
| 7. License architecture | HIGH | headers + grep counts verified |
| 8. Recovery mode | HIGH | all hooks re-verified (empty-blocklist clause: MEDIUM) |
| 9. Enterprise items | HIGH (gating) / MEDIUM (feature detail) | cfg gates verified |

### Verified absent (grep- and read-verified at commit 23b69755; details in Reports B/D)

- **Port-scan detector.** No mechanism infers scanning from connect patterns across ports. The name is misleading: `BlockReason::PortScanning` and the "scan" fail2ban counter exist but are triggered by *protocol parse errors* and *banned-path hits* (`security.rs:222, 263, 274`), not by port-scan behavior.
- **Per-IP connection rate limit.** No counter limits *new connection* rate per IP; the only connection-level controls are the blocklist and the (per-listener, global) concurrency limiter. Per-IP rate limits exist only for HTTP requests and the contact form.
- **Account lockout.** No lockout state on accounts. Brute-force protection is exclusively the dual-keyed (IP + login) fail2ban counter, which bans *IPs*, never accounts.
- **Progressive bans.** Ban periods are flat per `BlockReason`; no escalation with repeat offenses. (An existing registry `BlockedIp` for the same address is updated only if the new expiry is *longer* — security.rs:397-415 — but there is no automatic step-up.)
- **IPv6 prefix aggregation.** Bans are exact-IP only. The `IpAddrOrMask` network type is used solely for operator-defined allow/block *network objects*; automatically generated bans never cover a prefix, so /64-scale abusers must be handled by hand.
- **Open-source security-event history store.** The tracing/metrics stores that would hold historical security events are enterprise-only (Section 9); the AGPL side emits events but persists none. (Detail in Report D.)

---

## 11. Sources

Primary source (all `file:line` references):

- the reference platform Mail Server, commit `23b69755cb3cd8ecd952fe0443c5ac067a506184`, tag `v1.0.1` (2026-10-28), local clone at `C:\Users\Ibrahim\.zcode\tmp\reference-platform-analysis\reference-platform`. Files consulted during verification:
  - `crates/common/src/lib.rs` (Inner/Data, KV_RATE_LIMIT constants)
  - `crates/common/src/network/security.rs`, `mod.rs`, `listen.rs`, `limiter.rs`, `forwarded.rs`
  - `crates/common/src/auth/rate_limit.rs`, `authentication.rs`, `access_token.rs`
  - `crates/common/src/storage/mod.rs`, `config/storage.rs`, `config/network.rs`, `config/server/listener.rs`, `config/telemetry.rs`
  - `crates/common/src/cache/reload.rs`, `manager/boot.rs`
  - `crates/store/src/lib.rs`, `registry/local.rs`, `build/registry.rs`, `backend/local/mod.rs`, `backend/local/maintenance.rs`, `backend/redis/lookup.rs`
  - `crates/registry/src/schema/structs_impl.rs`, `structs.rs`, `enums.rs`, `enums_impl.rs`, `types/ipmask.rs`
  - `crates/http/src/auth/oauth/token.rs`, `crates/http/src/form/mod.rs`
  - `crates/services/src/broadcast/mod.rs`
  - `LICENSES/` (AGPL-3.0-only.txt, LicenseRef-SEL.txt, Apache-2.0.txt, MIT.txt, Unlicense.txt), `UPGRADING/v0_16.md`

Companion reports in this series (not yet written here): **Report B** — capability-gap analysis, **Report C** — operational behavior, **Report D** — design lessons for the Go reimplementation.
