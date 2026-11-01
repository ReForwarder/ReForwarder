# Report E — Stalwart Source Reference Map

Structured source-code reference index for the Stalwart security investigation series (Reports A–D, F–G).

| Field | Value |
|---|---|
| Repository | Stalwart Mail Server (`stalwartlabs/stalwart`) |
| Analyzed commit | `23b69755cb3cd8ecd952fe0443c5ac067a506184` (pinned, = tag `v1.0.1`) |
| Local clone | `C:\Users\Ibrahim\.zcode\tmp\stalwart-analysis\stalwart` (clean working tree at the pinned commit) |
| Date of analysis | 2026-11-01 |
| Method | Static source reads of the pinned snapshot (line-numbered `grep`/`sed` passes against the clone); every line reference below was verified against commit `23b69755`. No behavioral tests were executed (see §6). |

All line numbers refer to the files as they exist at commit `23b69755` (v1.0.1). They are navigation aids, not guarantees against future drift.

---

## Table of Contents

1. [Repository orientation](#1-repository-orientation)
2. [Critical files table](#2-critical-files-table)
   - [2.1 Network security core (fail2ban, blocklists, TLS, limits)](#21-network-security-core)
   - [2.2 Authentication and rate limiting](#22-authentication-and-rate-limiting)
   - [2.3 Configuration, registry, and hot reload](#23-configuration-registry-and-hot-reload)
   - [2.4 Storage layer (rate-limit KV, local store, Redis)](#24-storage-layer)
   - [2.5 Services (maintenance, broadcast) and expression engine](#25-services-and-expression-engine)
   - [2.6 Protocol front ends (HTTP, SMTP, IMAP, POP3, ManageSieve)](#26-protocol-front-ends)
   - [2.7 Telemetry (trc events, webhooks) and JMAP management](#27-telemetry-and-jmap-management)
3. [Config object reference](#3-config-object-reference)
4. [KV namespace table](#4-kv-namespace-table)
5. [Events reference](#5-events-reference)
6. [Tests](#6-tests)
7. [License map](#7-license-map)
8. [Sources](#8-sources)

---

## 1. Repository orientation

Stalwart is a single **Cargo workspace** (resolver `"2"`) composed of **35 crates** under `crates/`, with the server binary in `crates/main`. The pinned commit `23b69755` is exactly tag `v1.0.1` (`git log`: `23b69755cb3cd8ecd952fe0443c5ac067a506184 (HEAD -> main, tag: v1.0.1) v1.0.1`).

Workspace members (in `Cargo.toml` order):

| # | Crate | # | Crate | # | Crate |
|---|---|---|---|---|---|
| 1 | `main` | 13 | `dav-proto` | 25 | `utils` |
| 2 | `types` | 14 | `scim-proto` | 26 | `common` |
| 3 | `http` | 15 | `scim` | 27 | `trc` |
| 4 | `http-proto` | 16 | `dav` | 28 | `telemetry` |
| 5 | `jmap` | 17 | `groupware` | 29 | `text-extract` |
| 6 | `jmap-proto` | 18 | `spam-filter` | 30 | `smtp-proto` |
| 7 | `email` | 19 | `nlp` | 31 | `smtp-report` |
| 8 | `imap` | 20 | `store` | 32 | `calcard` |
| 9 | `imap-proto` | 21 | `coordinator` | 33 | `sieve` |
| 10 | `smtp` | 22 | `directory` | 34 | `jmap-tools` |
| 11 | `managesieve` | 23 | `registry` | 35 | `s3` |
| 12 | `pop3` | 24 | `services` | | |

Security-relevant crate roles, in brief:

- **`common`** — the security core: network listener plumbing, fail2ban/blocklists, rate limiting, authentication, config parsing, expression engine, telemetry webhooks.
- **`registry`** — typed schema of every config/management JMAP object (since v0.16, *all* settings are JMAP objects in the datastore; see §3).
- **`store`** — rate-limit/lock/greylist KV dispatch plus local and Redis backends.
- **`services`** — task manager (store maintenance), cluster broadcast (publisher/backlog/subscriber).
- **`http` / `smtp` / `imap` / `pop3` / `managesieve`** — protocol front ends that call the security core.
- **`trc`** — event/telemetry system (event enums, levels, Prometheus metric names).

Repo layout (top level):

```
Cargo.toml / Cargo.lock      workspace manifest + lock
crates/                      35 workspace crates
resources/                   runtime resources (webadmin assets, etc.)
api/                         API artifacts/schemas
LICENSES/                    license texts (AGPL-3.0-only, Apache-2.0, LicenseRef-SEL, MIT, Unlicense)
UPGRADING/                   per-version upgrade notes (v0_04.md ... v0_16.md)
Dockerfile / Dockerfile.build / Dockerfile.fdb / docker-bake.hcl
install.sh
AGENTS.md / CHANGELOG.md / CONTRIBUTING.md / README.md
SECURITY.md / SECURITY_PROCESS.md / SECURITY_TEMPLATE.md
img/ / CNAME
```

Note on configuration model: since **v0.16** (`UPGRADING/v0_16.md`), TOML configuration is gone; a single small `config.json` on disk describes **only the datastore**, and every other setting (including rate limits and blocked IPs) lives in the datastore as JMAP objects managed through `/jmap` and `stalwart-cli apply`. This report's "config objects" (§3) are therefore registry objects, not TOML keys.

---

## 2. Critical files table

Columns: **File** (path relative to the repo root) · **Key items** (functions/structs with line numbers) · **Role** · **Used by** (who calls/depends on it).

### 2.1 Network security core

| File | Key items | Role | Used by |
|---|---|---|---|
| `crates/common/src/network/security.rs` | `Security::parse` (:83-215); `ban_period` (:217-225); `is_rcpt_fail2banned` (:229-259); `is_scanner_fail2banned` (:261-268); `is_loiter_fail2banned` (:282-289); `is_ip_fail2banned` (:291-314); `ban_and_reset` (:316-334); `is_auth_fail2banned` (:336-369); `block_ip` (:371-435); `is_ip_blocked` (:441-453); `is_ip_allowed` (:455-470); `is_secure_password` (:472-495); `BlockedIps` struct (:69-74: blocked addresses + networks + `has_blocked_networks`); `BlockedIps::parse` (:510-573); `IpWithTtl<T>` (:76-80, `is_expired` :587-590) | The fail2ban engine: per-namespace counters (RCPT/scan/loiter/auth), ban decisions with per-reason ban periods, IP block/allow enforcement with TTL, password-strength screening. Reads `Security` registry object; persists counters under KV prefixes 2–5 (§4). | `listen.rs` (connect-time block check), `http/src/request.rs`, `smtp/src/inbound/{spawn,session,rcpt}.rs`, `imap/src/core/client.rs`, `pop3/src/client.rs`, `managesieve/src/core/client.rs`, auth failure paths |
| `crates/common/src/network/listen.rs` | `Listener::spawn` (:33-246); PROXY protocol unwrap (:135-188, incl. `ProxiedStream::create_from_tokio` timeout and mapped-IPv6→IPv4 canonicalization); accept-loop error handling (:197-225: continue on `ConnectionAborted/Reset/Interrupted`, `AcceptError` event, exponential `accept_backoff` capped at `MAX_ACCEPT_BACKOFF`, shutdown select); `build_session` (:249-313 — canonicalize remote IP :266-273, `is_ip_blocked` :277, concurrency limiter :286, `LimitEvent::ConcurrentConnection` :298-308); socket options (`SocketOpts`, keepalive consts :315-372, `apply` :353-372); `ServerInstance::tls_accept` (:467-524, `tls_timeout`-bounded handshake, handshake/handshake-error/not-configured events) | TCP accept loop for every listener: PROXY-v2 trusted hop unwrap, connect-time IP blocklist and global concurrency gate, TLS handshake, socket tuning. | All protocol servers (`http`, `smtp`, `imap`, `pop3`, `managesieve`) via `SessionManager::spawn` |
| `crates/common/src/network/limiter.rs` | `ConcurrencyLimiter` (:18-26) and impl (:97-126: `new`/`is_allowed`/`check_is_allowed`/`max_concurrent`); `AccountLimits` (:28-33) with `AccountLimit`/`AccountRate` enums (:35-46), `AccountConcurrency` (:48-52, `try_acquire` :87-94), impls (:128-202: bitmask `enabled`, per-account `is_allowed` :165-173); `InFlight`/`InFlightSlot` (:54-76, RAII decrement via `Drop`); `LimiterResult` (:204-218, `Disabled` maps to `Untracked` in-flight); `RateCounter` (:220-222) with bitpacked batching (:220-415, `batch_size` :260, batch flush/learned state) | Two-tier concurrency+rate limiting: a per-listener global concurrency limiter (connect time) and per-account HTTP/IMAP request limits+rates (request time). `RateCounter` batches counter increments to amortize KV writes. | `listen.rs` (connect gate), `common/src/auth/rate_limit.rs`, `imap/pop3` request paths |
| `crates/common/src/network/forwarded.rs` | `MAX_FORWARDED_HOPS: u8 = 16` (:15); `RemoteIpSource` enum (:20); `resolve(&self, headers) -> ClientIp` (:51-79, hop-clamped at :36); loopback-claim rejection (`is_loopback_claim` :122-125, used at :119) | Resolves the client IP from `Forwarded`/`X-Forwarded-For` headers per `clientIpSource`, bounding trusted hops and refusing loopback claims (anti-spoofing for proxy deployments). | `http/src/request.rs` (:891), SMTP/IMAP `RemoteIp` handling |
| `crates/common/src/network/mod.rs` | `SessionData<T>` (:87-97: stream, in-flight guard, local/remote ip+port, protocol, instance); `SessionManager` trait + `spawn` (:111-265: connection-start span send :149-163, session dispatch, connection-end span :256-263); `ip_to_bytes` (:290-297) and `ip_to_bytes_prefix` (:299) | Session bookkeeping: per-connection telemetry spans, protocol dispatch into per-service handlers, IP→bytes key encoding used by the fail2ban counters. | `listen.rs`, every protocol front end |

### 2.2 Authentication and rate limiting

| File | Key items | Role | Used by |
|---|---|---|---|
| `crates/common/src/auth/authentication.rs` | `Server::authenticate` (:52-94, with 50-500 ms artificial delay on the auth path); `auth_failure` (:96-121); fallback/recovery admin authentication in `authenticate_basic` (:123+, recovery-admin branch :133-175) | Central authentication entry point: credential verification, uniform failure handling (event + delay), brute-force counter wiring, emergency `recovery admin` account support. | HTTP/SMTP/IMAP/POP3/ManageSieve auth flows |
| `crates/common/src/auth/rate_limit.rs` | `is_http_authenticated_request_allowed` (:22-59); `is_imap_request_allowed` (:61-84); `account_limits` (:122-135, builds `AccountLimits` from registry rates); `is_http_anonymous_request_allowed` (:137-154) | Per-account (authenticated HTTP, IMAP) and per-IP (anonymous HTTP) request throttles; drives both `AccountLimits` construction and the anonymous-limiter KV counters. | `http/src/request.rs`, `http/src/auth/authenticate.rs`, `imap/src/core/client.rs`, `pop3/src/client.rs` |
| `crates/common/src/auth/sasl.rs` | `accepts_bearer_tokens` (:83-87) | TLS-gated OAuth bearer-token acceptance (`is_tls \|\| bootstrap \|\| recovery` mode). | SASL mechanism negotiation (SMTP/IMAP auth) |
| `crates/common/src/auth/access_token.rs` | `assert_is_valid(remote_ip)` (:623-635, checks `scope.allowed_ips`) | Enforces per-credential IP pinning on OAuth access tokens. | OAuth-authorized requests (HTTP, IMAP, POP3: `pop3/src/op/authenticate.rs` :154+) |
| `crates/common/src/lib.rs` | `Server` (:166-169: `inner: Arc<Inner>`, `core: Arc<Core>`); `Inner` (:171-202: `shared_core: ArcSwap<Core>`, `Data`, `Caches`, `Ipc`); `Data.blocked_ips: RwLock<BlockedIps>` (:185); KV namespace constants (:144-163, see §4) | Global server state; the `RwLock<BlockedIps>` is the in-memory blocklist mirror reloaded from the registry (§2.3). | Everything (`Server` is the root handle) |

### 2.3 Configuration, registry, and hot reload

| File | Key items | Role | Used by |
|---|---|---|---|
| `crates/common/src/cache/reload.rs` | `reload_registry` (:33) / `reload` (:37-176): incremental insert of `ObjectType::BlockedIp` (:41-59); full `RegistryChange::Reload` re-parse incl. `BlockedIps::parse` (:89-94) and `Listeners::parse` (:133-143 — listeners are re-parsed but **never respawned**); `ReloadResult` (:179+) | Hot-reload path for registry changes: blocked-IP updates land in `Inner.blocked_ips` without restart; listener changes require restart to take effect on sockets. | JMAP management API (`Action::ReloadBlockedIps`, §2.7), cluster broadcast |
| `crates/common/src/config/network.rs` | `Network::parse` (:171+); `Http::parse` (:420+); `max_buffered_memory` derivation (:362-371: `available_memory()/BUFFERED_MEMORY_SHARE`, 1 GiB fallback with `"Unable to detect the available memory, using 1 GiB for maxBufferedMemory"`) | Boot-time parse of network settings from the registry: proxy networks, per-listener instances, memory budget. | `common` boot, `listen.rs` |
| `crates/common/src/config/inner.rs` | `Core`/`Data` build — `BlockedIps::parse(bp)` at boot (:79) | Assembles the shared `Core` (incl. initial blocklist) before listeners start. | `common` boot |
| `crates/registry/src/types/ipmask.rs` | `IpAddrOrMask::matches` (:62-109); `to_index_key` (:111-126); `FromStr` (:129-173) | IP-or-CIDR mask type used by `BlockedIp`/`AllowedIp`/`proxyTrustedNetworks`; network matching and index-key encoding. | `security.rs` (`BlockedIps`), `forwarded.rs`, schema |
| `crates/registry/src/schema/structs.rs` | `Security` singleton (:5102-5121, 9 properties); `BlockedIp` (:746-755: `address: IpAddrOrMask`, `reason: BlockReason`, `createdAt`, `expiresAt`); `AllowedIp` (:302-311) | Typed JMAP objects for the security settings and blocklist entries. | registry store, `security.rs`, JMAP set handlers |
| `crates/registry/src/schema/enums.rs` | `BlockReason` (:259-269: `RcptToFailure=0, AuthFailure=1, Loitering=2, PortScanning=3, Manual=4, Other=5`) | Ban-reason taxonomy persisted with each `BlockedIp`. | `security.rs::block_ip`, `ban_period` |
| `crates/registry/src/schema/structs_impl.rs` | `impl Default for Security` (:58373-58418: rates 35 / 100 / 150 / 30 per day for abuse/auth-loiter/scan respectively + 12 `scanBanPaths` globs); `Rate::validate` (:53596+, count 1..=1,000,000, period ≥ 1 ms) | Factory defaults and validation for security settings. | registry bootstrap, `stalwart-cli apply` |
| `crates/store/src/build/registry.rs` | registry init; bootstrap mode (:23-53, temporary administrator banner; `is_bootstrap_mode` :117) | First-boot registry construction; bootstrap mode relaxes bearer-token TLS gating (see `sasl.rs`). | boot, SASL |
| `crates/common/src/manager/boot.rs` | CLI flags (:44-58: `-c/--config`, `-e/--export`, `-i/--import`, `-o/--console`, `-V/--version`) and arg parsing (:75+) | Server entry-point options. | `crates/main` |
| `crates/common/src/manager/defaults.rs` | factory-seeded throttle objects (:112-141, e.g. "Sender IP throttle" 5/s, sender→rcpt 25/h); default `Tracer::Log` (:514-528: `/var/log/stalwart`, daily rotate) | Seeds a minimal usable config when the registry is empty. | boot |

### 2.4 Storage layer

| File | Key items | Role | Used by |
|---|---|---|---|
| `crates/store/src/dispatch/lookup.rs` | `RateWindow` (:23-44: `index = now / period`, `bucket()` = prefix byte \|\| key \|\| index BE-64 :37-43); `key_exists` (:201-220); `is_rate_allowed` (:225-247); `rate_reset` (:249-252); backend dispatch (`InMemoryStore`: Local / Redis / enterprise Sharded) | KV operations for counters, locks, leases, and the greylist; rate windows are fixed buckets derived from the configured period. | `security.rs`, `rate_limit.rs`, `limiter.rs` (`RateCounter`), SMTP throttle, greylist |
| `crates/store/src/backend/local/{mod,entry,maintenance}.rs` | `DEFAULT_MAX_ENTRIES = 1 << 20` (`mod.rs:39`), `EVICTION_DIVISOR = 16` (:40, eviction batch = max/16 :54); `SWEEP_INTERVAL = 30` s (`maintenance.rs:13`); `eviction_score` (`entry.rs:233`); lease/lock entry semantics (`entry.rs:144-149`) | In-memory KV backend: 1M-entry cap, 30 s expiry sweep, score-based eviction, locks/leases held as non-evictable entries. | default in-memory store when no Redis is configured |
| `crates/store/src/backend/redis/{mod,lookup,pool}.rs` | Lua `INCRBY`+`EXPIRE` rate script (`mod.rs:59-80`); `SET ... NX EX` lock/lease acquire (`lookup.rs:397-420+`); compare-and-delete token release; prefix deletion via `SCAN` (`SCAN_COUNT = 100`, `lookup.rs:18`, scan loop :466-486) | Distributed rate-limit/lock backend (atomic scripts avoid read-modify-write races). | optional in-memory store backend |
| `crates/services/src/task_manager/tasks/maintenance.rs` | `store_maintenance` (:68/:88/:130-411); `resetRateLimiters` prefix list (:386-397: `KV_RATE_LIMIT_{RCPT,SCAN,LOITER,AUTH,SMTP,CONTACT,HTTP_AUTHENTICATED,HTTP_ANONYMOUS,IMAP,CIMD}`) | Periodic store maintenance; rate-limiter reset wipes the listed KV prefixes — note `KV_RATE_LIMIT_AVAILABILITY` (12) is **not** in the list. | task manager |
| `crates/services/src/broadcast/{publisher,backlog,subscriber}.rs` | `MAX_PENDING_EVENTS = 65_536` (`backlog.rs:21`); `MAX_PENDING_BYTES = 8 * 1024 * 1024` (`backlog.rs:22`); `MAX_INVALIDATIONS_PER_EVENT = 1024` (:26); low-water trimming (:305-323) | Cluster event fan-out with bounded backlog (back-pressure by count *and* bytes). | coordinator, registry reload propagation |

### 2.5 Services and expression engine

| File | Key items | Role | Used by |
|---|---|---|---|
| `crates/common/src/expr/eval.rs` | `eval_if` (:25-92) | Evaluates `IfBlock` expressions (settings stored as expressions with `if_/then/else_`). | every expression-typed setting (SMTP params, session policy) |
| `crates/common/src/expr/functions/asynch.rs` | `key_exists` (:139 area, dispatch arm :115-171 range); `key_incr` (:167 area) | Async expression functions exposing the rate-limit KV to config expressions (custom throttles). | SMTP throttle keys, custom rules |
| `crates/smtp/src/core/{params,throttle}.rs` | `Throttle::is_allowed` (`throttle.rs:167-218`); `new_key` overloads (:20-90); session-parameter evaluation with code fallbacks (`params.rs`, e.g. `auth_errors_max` 3 :48, `rcpt_errors_max` 10 :69, `rcpt_max` 100 :74, 30 s waits :52/:73, 250 MiB message fallback :17, 15 min/5 min timeouts :21/:26); `ResolveVariable` for SMTP sessions (`inbound/session.rs:663-736`: `Rcpt`, `Sender`, `HeloDomain`, `RemoteIp`, `Asn`, `Country`, ...) | Per-stage SMTP throttling (keyed by arbitrary session variables) and session-parameter evaluation. Registry expression values win; the `params.rs` constants apply only when an expression evaluates empty. | SMTP inbound stages |

### 2.6 Protocol front ends

| File | Key items | Role | Used by |
|---|---|---|---|
| `crates/http/src/request.rs` | `handle_session` (:870+); XFF/`clientIpSource` resolution (:885-941, incl. `XForwardedMissing`/`XForwardedInvalid` handling and `Invalid` → 400); per-request blocklist check (:945-957, `IpBlocked` → 403 JSON problem); banned-path check (:846-868, `is_http_banned_path` :858); rate-limit call sites (:359-483 routing table: anonymous limiter on unauthenticated routes); HTTP parse-failure scan check (:1036-1060, `is_scanner_fail2banned` gated on `is_peer()`) | HTTP/JMAP request pipeline: identity resolution, blocklist, banned paths, rate limiting, scanner detection. | webadmin, JMAP, OAuth endpoints |
| `crates/http/src/auth/authenticate.rs` | Basic/Bearer decode and dispatch (:71-99 — the `basic` arm does **not** call the anonymous limiter; `bearer` and unknown mechanisms do :82-84/:91-93); cached-credential fast path with authenticated limiter (:57-65); post-auth authenticated limiter (:131-140) | HTTP authentication middleware; Basic auth bypasses the anonymous IP limiter and goes straight to credential verification (then the authenticated limiter). | all authenticated HTTP routes |
| `crates/smtp/src/inbound/spawn.rs` | `init_conn` (:76+); `handle_conn` (:138+); loitering check `is_loiter_fail2banned` (:192) | SMTP connection setup and read loop; idle-connection loiterer detection. | SMTP listener |
| `crates/smtp/src/inbound/session.rs` | `ingest` (:35+); parse-error scan check (:272-308: `is_scanner_fail2banned` on `UnknownCommand`/`InvalidResponse` for unauthenticated sessions, `ScanBan` event :285-291) | SMTP command stream parsing and scanner ban. | `handle_conn` |
| `crates/smtp/src/inbound/rcpt.rs` | `rcpt_error` (:407-451: `rcpt_errors_wait` sleep, counter, `is_rcpt_fail2banned` :414, disconnect at `rcpt_errors_max` :410) | RCPT-TO failure accounting → fail2ban. | RCPT stage |
| `crates/smtp/src/inbound/auth.rs` | `auth_error` (:324-341: `auth_errors_wait` sleep, `auth_errors` counter, disconnect at `auth_errors_max` :328) | AUTH failure accounting (SMTP side; the fail2ban write happens via `is_auth_fail2banned` on the auth path). | AUTH stage |
| `crates/smtp/src/inbound/data.rs` | `can_send_data` (:1065-1096+: MAIL/RCPT preconditions) | DATA gate. | DATA stage |
| `crates/smtp/src/inbound/mail.rs` / `spam.rs` | MAIL FROM stage handling; spam-filter invocation on DATA | Envelope + content filtering stages. | SMTP pipeline |
| `crates/smtp-proto/src/request/receiver.rs` | `MAX_LINE_LENGTH: usize = 4096` (:11) | Command-line length cap (DoS bound; over-long lines feed the scanner path). | SMTP parser |
| `crates/imap/src/core/client.rs` | SASL-failure scan check (:67-94: `is_scanner_fail2banned` on `PARSE` response while `NotAuthenticated`); per-request rate gate `Session::is_allowed` (:361-370: `is_imap_request_allowed` for authenticated state, exempting `CAPABILITY/NOOP/LOGOUT/ID`) | IMAP request loop: scanner detection + per-account request throttling. | IMAP session |
| `crates/imap/src/op/authenticate.rs` | `register_auth_failure` (:228-245: counts to `core.imap.max_auth_failures`, then `AuthEvent::TooManyAttempts`) | IMAP pre-auth failure cap (protocol-level; complements fail2ban). | IMAP AUTH |
| `crates/pop3/src/client.rs` + `op/authenticate.rs` | scan check (`client.rs:68`); `register_auth_failure` (`op/authenticate.rs:188+`, `max_auth_failures` :192); `is_imap_request_allowed` (:243, :154) | Same guard set for POP3. | POP3 session |
| `crates/managesieve/src/core/client.rs` + `op/authenticate.rs` | scan check (`client.rs:104`); `register_auth_failure` (`op/authenticate.rs:197+`) | Same guard set for ManageSieve. | ManageSieve session |
| `crates/spam-filter/src/modules/dnsbl.rs` | `check_dnsbl` (:52-128); failure cache `FAILURE_CACHE_LIFETIME = 60 s` (:35) and `MAX_NEGATIVE_TTL = 3600` (:31) — lookups **fail open** for 60 s on DNS errors (:214-227: `guard.insert(SpamLookup::None, FAILURE_CACHE_LIFETIME)`) | DNSBL scoring module with error-tolerant caching. | spam analysis pipeline (`analysis/{ip,score}.rs`) |

### 2.7 Telemetry and JMAP management

| File | Key items | Role | Used by |
|---|---|---|---|
| `crates/trc/` | Event system; `EVENT_COUNTERS: AtomicU64Array<TOTAL_EVENT_COUNT>` (`ipc/metrics.rs:20`); `SecurityEvent` enum (`event/enums.rs:698-709`); name/level/metric mappings in `event/enums_impl.rs` (levels :3634-4037, names :454-462/:1922, metric names :5820/:6264-6271 — see §5) | Structured event/telemetry substrate: in-process counters, Prometheus export, tracing spans. | every crate |
| `crates/common/src/telemetry/webhooks/mod.rs` | `MAX_PENDING_EVENTS = 1 << 16` (:34); HMAC signing (:228-233: `aws_lc_rs` HMAC-SHA256 over the JSON body, base64) → `X-Signature` header (:252); `EventWrapper { events: [...] }` payload (:238-247, serde struct :257-259) | Webhook delivery of telemetry batches with bounded pending queue and integrity signature. | webhook tracers |
| `crates/jmap/src/registry/set.rs` | `registry_object_set` (:93; dispatch arm :771-863 routes registry object types incl. `BlockedIp`) | JMAP `/set` handling for registry objects (the management write path for `BlockedIp` etc.). | webadmin/CLI |
| `crates/jmap/src/registry/mapping/action.rs` | `Action::ReloadBlockedIps` (:80-140: maps to `ObjectType::BlockedIp`, calls `reload_registry(RegistryChange::Reload(...))`, broadcasts cluster-wide `RegistryChange::Reload`) | Management action that triggers the hot reload (§2.3). | webadmin/CLI |

---

## 3. Config object reference

All settings are registry (JMAP) objects since v0.16 (`UPGRADING/v0_16.md`); `config.json` is a datastore descriptor only. Defaults below are the compiled-in `impl Default` values in `crates/registry/src/schema/structs_impl.rs` (factory seeds applied at first boot); "consumed at" names the enforcement site.

| Registry object | Property (serde name) | Default | Consumed at |
|---|---|---|---|
| **Security** (singleton) | `abuseBanRate` | 35 / day | `network/security.rs` (`is_rcpt_fail2banned` → `ban_and_reset`) |
| | `abuseBanPeriod` | `None` (falls back to `ban_period` mapping per `BlockReason`) | `network/security.rs::ban_period` |
| | `authBanRate` | 100 / day | `network/security.rs` (`is_auth_fail2banned`) |
| | `authBanPeriod` | `None` | `ban_period` |
| | `loiterBanRate` | 150 / day | `network/security.rs` (`is_loiter_fail2banned`) |
| | `loiterBanPeriod` | `None` | `ban_period` |
| | `scanBanPaths` | 12 globs: `*.php*`, `*.cgi*`, `*.asp*`, `*/wp-*`, `*/php*`, `*/cgi-bin*`, `*xmlrpc*`, `*../*`, `*/..*`, `*joomla*`, `*wordpress*`, `*drupal*` | `http/src/request.rs` (`is_http_banned_path`) |
| | `scanBanRate` | 30 / day | `network/security.rs` (`is_scanner_fail2banned`) |
| | `scanBanPeriod` | `None` | `ban_period` |
| **Http** | `rateLimitAuthenticated` | 1000 / 60 s | `auth/rate_limit.rs::is_http_authenticated_request_allowed` |
| | `rateLimitAnonymous` | 100 / 60 s | `auth/rate_limit.rs::is_http_anonymous_request_allowed` |
| | `clientIpSource` | `Peer` | `network/forwarded.rs::resolve` |
| | `forwardedHops` | 1 | `network/forwarded.rs` (clamped to ≤ 16) |
| | `allowedEndpoints` | else `"200"` (allow all) | `http/src/request.rs` routing |
| **Imap** | `rateLimit` | 2000 / 60 s | `auth/rate_limit.rs::is_imap_request_allowed` |
| | `maxConcurrentConnections` | 16 | IMAP session connect gating |
| | `maxAuthFailures` | 3 | `imap/src/op/authenticate.rs::register_auth_failure` (also POP3, ManageSieve) |
| | `allowPlainTextAuth` | `false` | IMAP capability/SASL gating |
| | `maxRequestSize` | 52 428 800 (50 MiB) | IMAP literal/command parsing |
| | `anonymousTimeout` / `authenticatedTimeout` / `idleTimeout` | 60 s / 1800 s / 1800 s | IMAP idle handling (loiter detection feed) |
| **Jmap** | `maxConcurrentRequests` | 4 | JMAP request limiter |
| | `maxConcurrentUploads` | 4 | upload limiter |
| | `maxRequestSize` | 10 000 000 (10 MB) | JMAP request parsing |
| | `maxUploadSize` | 50 000 000 (50 MB) | upload handling |
| **WebDav** | `maxRequestSize` | 26 214 400 (25 MiB) | DAV request parsing |
| **SystemSettings** (singleton) | `maxConnections` | 8192 | `listen.rs` `ConcurrencyLimiter::new(max_connections)` |
| | `proxyTrustedNetworks` | empty (no proxy hops trusted) | `listen.rs` PROXY unwrap; `forwarded.rs` |
| | `maxBufferedMemory` | unset → `available_memory()/4` (`BUFFERED_MEMORY_SHARE`), fallback 1 GiB | `config/network.rs:362-371` memory budget |
| **NetworkListener** | `tlsTimeout` | 60 s | `listen.rs::tls_accept`; PROXY header timeout |
| | `socketBacklog` | 1024 | listener bind |
| | `socketNoDelay` | `true` | `SocketOpts::apply` |
| | `socketReuseAddress` / `socketReusePort` | `true` / `true` | listener bind |
| | `socketReceiveBufferSize` / `socketSendBufferSize` / `socketTosV4` / `socketTtl` | unset (OS defaults) | `SocketOpts::apply` |
| **MtaInboundSession** | `maxDuration` | else `10m` | SMTP session watchdog |
| | `idleTimeout` | else `5m` | SMTP idle handling |
| | `transferLimit` | else `262144000` (250 MiB) | SMTP DATA size cap |
| **MtaStageAuth** | `maxFailures` | else `3` | `smtp/src/inbound/auth.rs` (`auth_errors_max`) |
| | `waitOnFail` | else `5s` | `auth.rs` (`auth_errors_wait`) |
| **MtaStageRcpt** | `maxFailures` | else `5` | `smtp/src/inbound/rcpt.rs` (`rcpt_errors_max`) |
| | `waitOnFail` | else `5s` | `rcpt.rs` (`rcpt_errors_wait`) |
| | `maxRecipients` | else `100` | `rcpt.rs` (`rcpt_max`) |
| **MtaStageData** | `maxMessages` | else `10` | SMTP DATA session cap |
| | `maxMessageSize` | else `104857600` (100 MiB) | SMTP DATA size cap |
| | `maxReceivedHeaders` | else `50` | header-count cap |
| **SpamSettings** | `enable` | `true` | SMTP DATA pipeline |
| | `scoreSpam` | 5.0 | `spam-filter/src/analysis/score.rs` |
| | `scoreReject` / `scoreDiscard` | 0.0 / 0.0 (reject/discard thresholds disabled by default) | delivery policy |
| | `greylistFor` | `None` (greylisting off) | KV_GREYLIST path |
| | `spamFilterRulesUrl` | `https://github.com/stalwartlabs/spam-filter/releases/latest/download/spam-filter-rules.json.gz` | rule download |
| **SpamDnsblSettings** | `domainLimit` / `emailLimit` / `ipLimit` / `urlLimit` | 50 / 50 / 50 / 50 | `spam-filter/src/modules/dnsbl.rs` |
| **Cache** | `spamLookup` | 6 291 456 (6 MiB) | spam lookup cache sizing |
| **DnsResolver** (`DnsResolverCommon`) | `attempts` | 2 | `config/smtp/resolver.rs` (`opts.attempts`) |
| | `timeout` | 5 s | `opts.timeout` |
| | `maxConcurrentRequests` | 2 | `opts.num_concurrent_reqs` |

**Code-fallback values:** the SMTP stage settings (`MtaStageAuth`/`Rcpt`/`Data`, `MtaInboundSession`) are stored as *expressions*; the numeric literals above are the `else_` branches of those expressions. `crates/smtp/src/core/params.rs` additionally hard-codes `unwrap_or` fallbacks (3 auth failures, 10 RCPT failures, 100 recipients, 30 s waits, 250 MiB transfer, 15 min/5 min timeouts) that apply **only when an expression evaluates empty** — a registry value, when present, always wins.

---

## 4. KV namespace table

Namespace constants in `crates/common/src/lib.rs:144-163`. Every counter/lock key is prefixed by one of these bytes.

| Constant | Value | Purpose | Reset by `resetRateLimiters`? |
|---|---|---|---|
| `KV_ACME` | 0 | ACME order/account data | n/a (not a rate namespace) |
| `KV_OAUTH` | 1 | OAuth device-flow state | n/a |
| `KV_RATE_LIMIT_RCPT` | 2 | SMTP RCPT-TO failure counters | Yes |
| `KV_RATE_LIMIT_SCAN` | 3 | Port-scanner counters | Yes |
| `KV_RATE_LIMIT_LOITER` | 4 | Loitering counters | Yes |
| `KV_RATE_LIMIT_AUTH` | 5 | Auth-failure counters | Yes |
| `KV_RATE_LIMIT_SMTP` | 6 | SMTP throttle keys (`Throttle::is_allowed`) | Yes |
| `KV_RATE_LIMIT_CONTACT` | 7 | Contact-form throttle | Yes |
| `KV_RATE_LIMIT_HTTP_AUTHENTICATED` | 8 | Authenticated HTTP request rate | Yes |
| `KV_RATE_LIMIT_HTTP_ANONYMOUS` | 9 | Anonymous HTTP request rate | Yes |
| `KV_RATE_LIMIT_IMAP` | 10 | IMAP (and POP3) request rate | Yes |
| `KV_QUOTA_BLOB` | 11 | Blob quota accounting | n/a |
| `KV_RATE_LIMIT_AVAILABILITY` | 12 | Availability/Scheduling throttles | **No** — absent from the `store_maintenance` prefix list (`maintenance.rs:386-397`) |
| `KV_GREYLIST` | 13 | Greylist triplets | n/a |
| `KV_LOCK_QUEUE_MESSAGE` | 14 | Queue-message locks | n/a |
| `KV_LOCK_TASK` | 15 | Task locks | n/a |
| `KV_LOCK_DAV` | 16 | DAV locks | n/a |
| `KV_LOCK_SEARCH_INDEX` | 17 | Search-index locks | n/a |
| `KV_OAUTH_CIMD` | 18 | OAuth CIMD (client metadata) cache | n/a |
| `KV_RATE_LIMIT_CIMD` | 19 | CIMD fetch rate limit | Yes |

**Key format** (rate-limit buckets): `prefix byte || key || window-index (u64, big-endian)` — `RateWindow::bucket` at `crates/store/src/dispatch/lookup.rs:37-43`, where `index = now / period_seconds`. Bucket TTL is the remainder of the window (`expires_in = period - now % period`).

---

## 5. Events reference

### 5.1 SecurityEvent variants

`crates/trc/src/event/enums.rs:698-709` (discriminant values are stable event IDs; string names from `enums_impl.rs:454-462`):

| Variant | ID | Event name | Raised by |
|---|---|---|---|
| `AuthenticationBan` | 28 | `security.authentication-ban` | `is_auth_fail2banned` → `ban_and_reset` |
| `AbuseBan` | 532 | `security.abuse-ban` | `is_rcpt_fail2banned` → `ban_and_reset` |
| `LoiterBan` | 533 | `security.loiter-ban` | `is_loiter_fail2banned` → `ban_and_reset` |
| `Unauthorized` | 535 | `security.unauthorized` | blocked-IP/unauthorized access paths |
| `IpBlocked` | 302 | `security.ip-blocked` | connect-time / per-request blocklist hits (`listen.rs:277`, `http/request.rs:945`) |
| `ScanBan` | 541 | `security.scan-ban` | scanner detection (SMTP :279, IMAP :77, POP3 :68, ManageSieve :104, HTTP :1038) |
| `IpBlockExpired` | 576 | `security.ip-block-expired` | TTL expiry of a `BlockedIp` |
| `IpAllowExpired` | 577 | `security.ip-allow-expired` | TTL expiry of an `AllowedIp` |
| `IpUnauthorized` | 263 | `security.ip-unauthorized` | connection from a non-allowed IP when allowlist applies |

### 5.2 Log levels

Default level mapping `EventType::level()` at `crates/trc/src/event/enums_impl.rs:3634-4037`:

- **`auth.failed` → Debug** (falls through to the `_ => Level::Debug` arm; this is why brute-force attempts are invisible at default log level).
- **`auth.too-many-attempts` → Warn** (:3970 — `AuthEvent::TooManyAttempts`, raised by IMAP/POP3/ManageSieve `register_auth_failure` once `max_auth_failures` is exceeded).
- **`security.*` → Info** (:3837-3845 — all nine `SecurityEvent` variants log at Info).

### 5.3 Prometheus metric names

`crates/trc/src/event/enums_impl.rs` (:5820, :6264-6271), counters are plain `AtomicU64`s (`EVENT_COUNTERS`, `ipc/metrics.rs:20`):

- `stalwart_auth_failed_total`
- `stalwart_security_authentication_ban_total`
- `stalwart_security_abuse_ban_total`
- `stalwart_security_scan_ban_total`
- `stalwart_security_loiter_ban_total`
- `stalwart_security_ip_blocked_total`
- `stalwart_security_unauthorized_total`

These are **dimensionless counters — no labels** (no per-IP, per-account, or per-reason dimensions), so they support only aggregate trending, not attribution.

### 5.4 Webhook payload shape

`crates/common/src/telemetry/webhooks/mod.rs`:

```json
{
  "events": [ { "id": "...", "type": "...", "spanId": "...", "keys": { "RemoteIp": "...", ... }, "level": "...", ... } ]
}
```

- Batching: up to `MAX_PENDING_EVENTS = 65536` pending events (:34); backlog also byte-bounded in the broadcast layer (8 MiB, §2.4).
- Integrity: when a signing key is configured, `X-Signature: base64(HMAC-SHA256(key, raw_body))` (:228-233, header inserted :252).

---

## 6. Tests

**Finding (verified against the snapshot): there are no in-tree `#[cfg(test)]` modules anywhere in this tree — zero occurrences of a plain `#[cfg(test)]` attribute in `crates/` at commit `23b69755`.** The workspace carries:

- **611** test-module wirings of the form
  ```rust
  #[cfg(all(test, stalwart_tests))]
  #[rustfmt::skip]
  #[path = "../../../../../stalwart-tests/unit/<path>/tests.rs"]
  mod tests;
  ```
- **196** `#[cfg(stalwart_tests)]` test hooks (fixtures/synthetic behaviors compiled only when the `stalwart_tests` cfg is set; registered in the workspace `unexpected_cfgs` lint list, root `Cargo.toml:81`).

All of these `#[path]` point into a **separate companion repository (`stalwart-tests`) that is not part of this snapshot** — the sibling checkout expected at `../../../stalwart-tests/` relative to the repo root is absent. This includes the `sieve` crate: even its tests are external-wired (e.g. `crates/sieve/src/lib.rs:242-244` → `stalwart-tests/unit/sieve/src/tests/mod.rs`), not in-tree. (Earlier notes claiming "in-tree `#[cfg(test)]` only for sieve" do not hold for this commit; the correct statement is "no in-tree unit tests at all".)

Security-relevant wired test modules (paths into `stalwart-tests/unit/...`):

| Host file | Wiring lines | Points to |
|---|---|---|
| `crates/common/src/network/security.rs` | :600-603 | `unit/common/src/network/security/tests.rs` |
| `crates/common/src/network/limiter.rs` | :441-444 | `unit/common/src/network/limiter/tests.rs` |
| `crates/common/src/network/listen.rs` | :526-529 | `unit/common/src/network/listen/tests.rs` |
| `crates/common/src/network/forwarded.rs` | :172-175 | `unit/common/src/network/forwarded/tests.rs` |
| `crates/registry/src/types/ipmask.rs` | :289-292 | `unit/registry/src/types/ipmask/tests.rs` |
| `crates/spam-filter/src/modules/dnsbl.rs` | :304-312 | `unit/spam-filter/src/modules/dnsbl/{tests,test_support}.rs` |
| `crates/spam-filter/src/analysis/mod.rs` | :271-274 | `unit/spam-filter/src/analysis/tests.rs` |

(`stalwart_tests`-gated hooks relevant to security behavior: SMTP fake recipients — `crates/smtp/src/inbound/rcpt.rs:28+`, `-debug`-suffixed instance IDs with `fail@`/forced-outcome addresses; DNSBL synthetic zones — `dnsbl.rs:310-312` `test_support.rs`.)

**Conclusion for the series:** behavioral tests for all security modules exist upstream but are **not present in this tree**. Every behavior claim in Reports A–D, F–G derives from direct implementation reads of the pinned commit, not from executing or reading tests.

---

## 7. License map

- Every security-path file listed in §2 carries the standard header
  ```c
  SPDX-FileCopyrightText: 2020 Stalwart Labs LLC <hello@stalw.art>
  SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-SEL
  ```
  (verified on e.g. `network/security.rs`, `network/limiter.rs`, `network/listen.rs`, `http/src/request.rs`, `store/src/dispatch/lookup.rs`, `trc` sources).
- **LicenseRef-SEL** (Stalwart Enterprise License) code appears only as clearly delimited **enterprise-gated stubs**, marked with `// SPDX-SnippetBegin` / `// SPDX-License-Identifier: LicenseRef-SEL` / `// SPDX-SnippetEnd` comment fences — e.g. the `Sharded` in-memory store in `store/src/backend/redis/lookup.rs`, enterprise object types in `jmap/src/registry/set.rs` (:866+ `AiModel`/`Alert`/`Enterprise`), and enterprise variants in `trc` enums.
- `LICENSES/` contains the full texts: `AGPL-3.0-only.txt`, `Apache-2.0.txt`, `LicenseRef-SEL.txt`, `MIT.txt`, `Unlicense.txt`.
- Binary-style crates declare licensing in `Cargo.toml` (`license = "AGPL-3.0-only OR LicenseRef-SEL"` in `crates/main/Cargo.toml:9`, `crates/smtp/Cargo.toml:9`, same pattern across protocol crates); library crates (`common`, `store`, `registry`, `utils`, ...) rely on per-file SPDX headers instead of a crate-level field.

For downstream analysis of this series: the security-path code examined is AGPL-3.0-only / LicenseRef-SEL dual-licensed; quotes and excerpts in these reports are used for interoperable research and independent security review purposes.

---

## 8. Sources

Primary (pinned snapshot, all line numbers):

- Local clone: `C:\Users\Ibrahim\.zcode\tmp\stalwart-analysis\stalwart`
- Commit: `23b69755cb3cd8ecd952fe0443c5ac067a506184`, tag `v1.0.1` (verified via `git log -1` / `git describe --tags`)
- Every file cited in §2–§7, as listed in the tables (paths relative to the clone root)

Project documents in this series (cross-references):

- Report A — fail2ban / blocked-IP engine analysis
- Report B — rate limiting and throttles
- Report C — HTTP/protocol front-end guards
- Report D — storage layer (rate-limit KV, local/Redis backends)
- Report F — telemetry and detection gaps
- Report G — consolidated findings

Upstream references:

- Stalwart repository: https://github.com/stalwartlabs/stalwart (tag `v1.0.1`)
- v0.16 configuration-model migration notes: `UPGRADING/v0_16.md` in the pinned snapshot
