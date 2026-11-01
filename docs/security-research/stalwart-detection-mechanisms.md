# Report D — The Complete Catalog of Abnormal-Behavior Detection Mechanisms in the reference platform: Every Detector, Every Trigger, Every Default

| | |
|---|---|
| **Subject repository** | the reference platform Mail Server (`reference-platformlabs/reference-platform`), clone at `C:\Users\Ibrahim\.zcode\tmp\reference-platform-analysis\reference-platform` |
| **Commit analyzed** | `23b69755cb3cd8ecd952fe0443c5ac067a506184` (tag `v1.0.1`, HEAD of `main`) |
| **Date** | 2026-11-01 |
| **Method** | Line-by-line reads of the network security module, the central authentication funnel, all five protocol server paths, the SMTP inbound stage handlers, the rate limiter, the DNSBL/spam-filter modules, and the registry object defaults that seed every threshold. Every rate, default, response code, and call site cited below was re-verified against the pinned commit at write time. Companion to Report A (security architecture) and Report B (port-scanner detection). |
| **Report series** | Report D of the ReForwarder security-research series |

## Table of Contents

1. [The Detection Model in One Page](#1-the-detection-model-in-one-page)
2. [The Fail2ban Detection Engine](#2-the-fail2ban-detection-engine)
   - 2.1 [Shared mechanics: fixed-window counters, increment-on-check, ban-and-reset](#21-shared-mechanics-fixed-window-counters-increment-on-check-ban-and-reset)
   - 2.2 [Detector 1 — `auth`: authentication failures](#22-detector-1--auth-authentication-failures)
   - 2.3 [Detector 2 — `rcpt`: RCPT TO failures](#23-detector-2--rcpt-rcpt-to-failures)
   - 2.4 [Detector 3 — `scan`: pre-auth protocol parse errors](#24-detector-3--scan-pre-auth-protocol-parse-errors)
   - 2.5 [Detector 4 — `loiter`: SMTP session loitering](#25-detector-4--loiter-smtp-session-loitering)
   - 2.6 [Instant ban without a counter — HTTP banned paths](#26-instant-ban-without-a-counter--http-banned-paths)
3. [SMTP Session-Level Guards: The 23-Row Table](#3-smtp-session-level-guards-the-23-row-table)
4. [Connection and Timeout Hardening](#4-connection-and-timeout-hardening)
5. [Rate Limiting as Detection](#5-rate-limiting-as-detection)
6. [SMTP Throttles and Greylisting](#6-smtp-throttles-and-greylisting)
7. [Spam and DNSBL Detection (Post-DATA, Message-Scoped)](#7-spam-and-dnsbl-detection-post-data-message-scoped)
8. [Mail-Auth Verification and the Expression Engine as Policy Layer](#8-mail-auth-verification-and-the-expression-engine-as-policy-layer)
9. [OAuth and Auth-Hygiene Detection Surface](#9-oauth-and-auth-hygiene-detection-surface)
10. [What Is NOT Detection (Explicit Absences)](#10-what-is-not-detection-explicit-absences)
11. [Priority Classification and Confidence Table](#11-priority-classification-and-confidence-table)
12. [Sources](#12-sources)

---

## 1. The Detection Model in One Page

the reference platform v1.0.1 detects abnormal behavior through **four fail2ban counters, one instant-ban path list, a stack of SMTP session-level guards, a layer of protocol-agnostic rate limiters, expression-gated throttles, and a post-DATA spam/DNSBL pipeline** — and nothing else. There is no port-scan detector (Report B), no account lockout, no progressive/escalating bans, no cross-port correlation, no per-IP connection-rate limiter, and no behavioral anomaly detection.

The architecture has three distinct "detection tiers" with very different consequences:

| Tier | Examples | Consequence of tripping |
|---|---|---|
| **Ban tier** (fail2ban + banned paths) | auth failures, RCPT failures, pre-auth parse errors, loitering, banned-path hits | IP added to the blocked list (`block_ip`), refused at accept time on **every** listener; counter reset; default **permanent** |
| **Limit tier** (rate limiters, throttles, session guards) | HTTP/IMAP request rates, concurrency caps, message/recipient/byte ceilings | Request rejected with `429` / tagged `NO` / transient SMTP code; **no ban, no counter toward any ban** |
| **Classify tier** (spam filter, DNSBL, mail-auth) | spam score, DNSBL hits, SPF/DKIM/DMARC results | Message tagged, scored, deferred, or rejected per-message; **never touches the IP ban list** |

Everything in the ban tier lives in one file: `crates/common/src/network/security.rs`. Everything else is scattered through the protocol crates. This report walks all of it.

## 2. The Fail2ban Detection Engine

All four detectors plus the banned-path check are methods on `Server` in `crates/common/src/network/security.rs`:

| Function | Lines | Kind |
|---|---|---|
| `is_rcpt_fail2banned` | `security.rs:229-259` | counter, dual-keyed |
| `is_scanner_fail2banned` | `security.rs:261-268` | counter, IP-keyed |
| `is_http_banned_path` | `security.rs:270-280` | instant ban, no counter |
| `is_loiter_fail2banned` | `security.rs:282-289` | counter, IP-keyed |
| `is_ip_fail2banned` (shared helper) | `security.rs:291-314` | counter mechanics |
| `ban_and_reset` (shared helper) | `security.rs:316-334` | ban + counter reset |
| `is_auth_fail2banned` | `security.rs:336-369` | counter, dual-keyed |
| `block_ip` | `security.rs:371+` | ban writer |
| `ban_period` | `security.rs:217-228` | per-reason ban duration lookup |

### 2.1 Shared mechanics: fixed-window counters, increment-on-check, ban-and-reset

Every detector shares the same machinery (`is_ip_fail2banned`, `security.rs:291-314`):

- **Store**: the in-memory KV store only. Counters are keyed `prefix_byte || key_bytes`, where the prefix bytes are `KV_RATE_LIMIT_AUTH` / `KV_RATE_LIMIT_RCPT` / `KV_RATE_LIMIT_SCAN` / `KV_RATE_LIMIT_LOITER` (`crates/common/src/lib.rs`, constants block around `:147-152`).
- **Window**: a fixed window (`RateWindow::new(rate, now)`), **not** a sliding window or token bucket.
- **Increment-on-check**: every call to `is_rate_allowed` increments the counter by 1 before comparing (`key_incr_at(..., 1)`; `crates/store/src/dispatch/lookup.rs:225-247`). The check *is* the increment — there is no separate "record failure" step, so a failure counted at check time counts even if the session dies immediately after.
- **On breach**: `ban_and_reset` (`security.rs:316-334`) calls `block_ip(ip, reason)` and then `rate_reset` on every key that fed the breach (both the IP key and the credential key for the dual-keyed detectors). The counter restarts from zero after every ban.
- **Ban duration**: `ban_period(reason)` (`security.rs:217-228`) maps `BlockReason::AuthFailure → auth_ban_period`, `RcptToFailure → abuse_ban_period`, `Loitering → loiter_ban_period`, `PortScanning → scan_ban_period`. All four default to `Default::default()` = `None` (`crates/registry/src/schema/structs_impl.rs:58375-58414`, the `Default for Security` impl) → `expires_at = None` → `expiry = u64::MAX` → **permanent ban**, persisted as a `BlockedIp` object in the registry (`block_ip`, `security.rs:371+`). A temporary ban requires an admin-configured `banPeriod` per detector.
- **Enforcement**: the listener accept path checks `is_ip_blocked(remote_ip)` before any protocol work and refuses with `SecurityEvent::IpBlocked` (`crates/common/src/network/listen.rs:277-286`; see Report B §2.1).
- **Exemptions**: `is_ip_allowed(ip)` short-circuits before any counting — config allowlist, loopback (always), and proxy-trusted networks (`security.rs:298-300`).
- **Recovery mode**: when the server is in recovery mode all four fail rates are filtered to `None` and banned paths are emptied — the entire ban tier switches off (`security.rs:181-193`).

**Default rates** (`Default for Security`, `crates/registry/src/schema/structs_impl.rs:58373-58414`): auth **100/day**, rcpt **35/day**, scan **30/day**, loiter **150/day**, all periods 86,400,000 ms (1 day), all ban periods `None` (permanent).

### 2.2 Detector 1 — `auth`: authentication failures

**Function**: `is_auth_fail2banned(ip, login)` (`security.rs:336-369`).

**Trigger condition**: an error classified as `AuthEvent::Failed` or `SecurityEvent::IpUnauthorized` reaching `Server::auth_failure` (`crates/common/src/auth/authentication.rs:90-121`). The guard:

```rust
if matches!(
    err.as_ref(),
    trc::EventType::Auth(trc::AuthEvent::Failed)
        | trc::EventType::Security(trc::SecurityEvent::IpUnauthorized)
) && self.has_auth_fail2ban()
{
    match self.is_auth_fail2banned(remote_ip, username).await { ... }
}
```

**The central funnel.** Every authentication attempt on every protocol converges on `Server::authenticate` (`authentication.rs:52-88`) and, on failure, `Server::auth_failure`:

| Protocol | Failure entry point | Location |
|---|---|---|
| SMTP | `malformed_credentials` / SASL failure paths call `self.server.auth_failure(...)` | `crates/smtp/src/inbound/auth.rs:184` |
| IMAP | `malformed_credentials` calls `self.server.auth_failure(...)` | `crates/imap/src/op/authenticate.rs:217` |
| POP3 | `register_auth_failure` calls `self.server.auth_failure(...)` | `crates/pop3/src/op/authenticate.rs:177` |
| ManageSieve | `register_auth_failure` calls `self.server.auth_failure(...)` | `crates/managesieve/src/op/authenticate.rs:186` |
| HTTP (OAuth/Bearer/Basic) | all credential flows route through `Server::authenticate` | `crates/http/src/auth/*` via `authentication.rs:52` |
| HTTP (client registration) | direct call with `login = None` | `crates/http/src/auth/oauth/registration.rs:326` |
| JMAP (account/credential changes) | password and OTP changes re-verify the current secret and funnel failures into the same check | `crates/jmap/src/registry/mapping/account.rs:254` |

**Keys**: `ip_to_bytes(ip)` **and** `blake3::hash(login)` (`security.rs:344-349`). Both counters are checked; **either** exceeding the rate bans (`security.rs:350-357`). An empty/absent login skips the credential key (`security.rs:345-346`). On breach both keys are reset (`security.rs:358-365` → `ban_and_reset`). The dual-key design means credential-stuffing against one account from many IPs is caught by the login-key counter, and one IP spraying many accounts is caught by the IP-key counter.

**Default rate**: 100 failures/day per key (`auth_ban_rate`, `structs_impl.rs:58381-58384`); permanent ban by default.

**Anti-enumeration**: before an authentication failure is returned, `Server::authenticate` sleeps a uniformly random **50–500 ms** (`authentication.rs:73-82`, `#[cfg(not(reference-platform_tests))]`), so failed and successful logins are not distinguishable by timing.

**Confidence: HIGH** — engine, funnel call sites, keys, default, and delay all read directly.

### 2.3 Detector 2 — `rcpt`: RCPT TO failures

**Function**: `is_rcpt_fail2banned(ip, rcpt)` (`security.rs:229-259`).

**Trigger condition**: a **recipient-level** SMTP failure — specifically an unknown local mailbox (`550 5.1.1`) or relay-denied (`550 5.7.1`) response to `RCPT TO`. Every such error flows through `rcpt_error` (`crates/smtp/src/inbound/rcpt.rs:407-443`):

```rust
async fn rcpt_error(&mut self, response: &[u8], rcpt: String) -> Result<(), ()> {
    tokio::time::sleep(self.params.rcpt_errors_wait).await;   // default 5s
    self.data.rcpt_errors += 1;
    let has_too_many_errors = self.data.rcpt_errors >= self.params.rcpt_errors_max;
    match self.server.is_rcpt_fail2banned(self.data.remote_ip, &rcpt).await { ... }
```

Two consequences per failure: the **session** counter (`rcpt_errors`, covered in §3 row 7) and the **fail2ban** counter (called at `rcpt.rs:414`, logging `SecurityEvent::AbuseBan` on breach at `rcpt.rs:416-423`).

**Keys**: `ip_to_bytes(ip)` **and** `blake3::hash(rcpt)` (`security.rs:235-239`), either exceeding bans; both reset on breach.

**What is NOT counted**: syntax-level rejections (`501 5.1.3` invalid recipient address, `501 5.5.4` bad parameters) and over-quota `455` responses do not reach `rcpt_error`'s fail2ban path — only mailbox-unknown and relay-denied class failures do (branch analysis in Report B §2.2).

**Default rate**: 35 failures/day per key (`abuse_ban_rate`, `structs_impl.rs:58376-58379`); permanent ban by default. `BlockReason::RcptToFailure`.

**Confidence: HIGH.**

### 2.4 Detector 3 — `scan`: pre-auth protocol parse errors

**Function**: `is_scanner_fail2banned(ip)` (`security.rs:261-268`) → shared `is_ip_fail2banned` with `KV_RATE_LIMIT_SCAN`.

**Trigger condition**: a protocol parse error **before authentication**, pooled across all five protocol servers:

| # | Protocol | Condition | Location |
|---|---|---|---|
| 1 | SMTP | `Error::UnknownCommand \| Error::InvalidResponse` pre-auth (`!self.is_authenticated()` gate at `:276`) | `crates/smtp/src/inbound/session.rs:272-308` |
| 2 | IMAP | `PARSE` response code while `State::NotAuthenticated` | `crates/imap/src/core/client.rs:67-94` |
| 3 | POP3 | `Error::Parse(_)` while `State::NotAuthenticated` | `crates/pop3/src/client.rs:66-87` |
| 4 | ManageSieve | `PARSE` response code while `State::NotAuthenticated` | `crates/managesieve/src/core/client.rs:96-123` |
| 5 | HTTP | hyper `http_err.is_parse()` on connection error, only when the client IP is the direct TCP peer | `crates/http/src/request.rs:1036-1060` |

**Keys**: IP only (`security.rs:291-314`) — the counter is **pooled across all five protocols**, so malformed traffic against IMAP, POP3, Sieve, HTTP, and SMTP accumulates in one bucket per client IP. Authenticated SMTP clients are exempt (gate at `session.rs:276`). Full trigger analysis, including what counts and what does not, is Report B §2.2–2.3.

**Default rate**: 30 failures/day per IP (`scan_ban_rate`, `structs_impl.rs:58405-58408`); permanent ban with `BlockReason::PortScanning` — the label is a misnomer; nothing about ports is inspected (Report B §1).

**Confidence: HIGH.**

### 2.5 Detector 4 — `loiter`: SMTP session loitering

**Function**: `is_loiter_fail2banned(ip)` (`security.rs:282-289`) → shared `is_ip_fail2banned` with `KV_RATE_LIMIT_LOITER`.

**Trigger condition**: an SMTP session still open past its `valid_until` deadline. The session read loop (`crates/smtp/src/inbound/spawn.rs:147-217`) checks on every read: if `Instant::now() >= self.data.valid_until`, it writes `421 4.3.2 ... Session open for too long.` (`spawn.rs:188`) and calls `is_loiter_fail2banned` (`spawn.rs:192`), logging `SecurityEvent::LoiterBan` on breach. `valid_until` is initialized at session start plus the `maxDuration` expression — default **10 minutes** (`MtaInboundSession.maxDuration = "10m"`, `structs_impl.rs:45432-45446`; applied in `crates/smtp/src/core/params.rs:22-25`).

This is the only detector whose "failure" is passive: a client that simply stays connected past 10 minutes and then speaks commits a countable offense. Ranged against the 150/day default (`loiter_ban_rate`, `structs_impl.rs:58386-58389`), an IP can idle out and reconnect about 6 times per hour for a day before a permanent `BlockReason::Loitering` ban.

**Confidence: HIGH.**

### 2.6 Instant ban without a counter — HTTP banned paths

**Function**: `is_http_banned_path(path, ip)` (`security.rs:270-280`). If the request path matches **any** of the compiled glob patterns and the IP is not allowlisted, `block_ip(ip, BlockReason::PortScanning)` fires immediately — no rate, no window, no first-offense tolerance. One hit = permanent ban.

**Invocation**: `ban_scanned_path` (`crates/http/src/request.rs:853-868`), called from the 404 fallback handler at `request.rs:846-847` — i.e., only when a request matched no route. (It is also called on one additional internal fallback at `request.rs:798`.)

**Default patterns — exactly 12**, case-insensitive (`scan_ban_paths`, `structs_impl.rs:58391-58403`; compiled at `security.rs:190`):

```
*.php*    *.cgi*    *.asp*    */wp-*    */php*    */cgi-bin*
*xmlrpc*  *../*     */..*     *joomla*  *wordpress*  *drupal*
```

A signature list for HTTP exploit scanners (PHP/CMS/traversal probes) — and the only mechanism in the reference platform that behaves the way its `PortScanning` name suggests.

**Confidence: HIGH.**

## 3. SMTP Session-Level Guards: The 23-Row Table

The SMTP inbound path (`crates/smtp/src/inbound/*`) enforces a layered set of per-session guards. Session parameters are resolved as `IfBlock` expressions against registry objects (`crates/smtp/src/core/params.rs:19-86`); the values below are the **registry defaults** (the code-level `unwrap_or` fallbacks in `params.rs` — 10 rcpt errors, 30 s waits, 15 min duration, 25 MiB message — apply only if an expression evaluates to `None`).

| # | Guard | Default threshold | Response on breach | Counted toward a ban? |
|---|---|---|---|---|
| 1 | Unknown/invalid command, pre-auth | first occurrence | `500`-class error + `SecurityEvent::ScanBan` if counter trips | **Yes — scan counter** (30/day pooled; `session.rs:272-308`) |
| 2 | Command line > 4096 bytes | 4096 (`MAX_LINE_LENGTH`) | `554 5.3.4 Line is too long.` (`session.rs:495-507`) | No |
| 3 | Syntax/parameter errors (`501`/`504`) | n/a | `501 5.5.2` / `501 5.5.4` / `504 5.5.4` | No (Report B §2.2) |
| 4 | EHLO not sent / non-FQND HELO | `require = true` always; `rejectNonFqdn = true` **only on `local_port == 25`** (`MtaStageEhlo` default, `structs_impl.rs:48239-48252`) | rejection at EHLO | No |
| 5 | AUTH failures in one session | 3 errors, 5 s sleep each (`MtaStageAuth`: `maxFailures = "3"`, `waitOnFail = "5s"`, `structs_impl.rs:47295-47304`) | after 3rd: `455 4.3.0 Too many authentication errors, disconnecting.` (`auth.rs:324-342`, response at `:336`) | **Yes — auth fail2ban** (each failure funnels through `server.auth_failure`, `auth.rs:184`) |
| 6 | SASL exchange line > 4096 | 4096 | `500 5.5.6 Authentication Exchange line is too long.` (`session.rs:453-467`) | **Yes — session auth-error counter** (`auth_error` increments; 3 of these in one session = disconnect). Not directly the fail2ban scan counter |
| 7 | RCPT-level errors in one session | 5 errors, 5 s sleep each (`MtaStageRcpt`: `maxFailures = "5"`, `waitOnFail = "5s"`, `structs_impl.rs:48663-48672`) | after 5th: `451 4.3.0 Too many errors, disconnecting.` (`rcpt.rs:441-449`) | **Yes — rcpt fail2ban** (every error also counted at `rcpt.rs:414`) |
| 8 | Recipients per message | 100 (`maxRecipients = "100"`, `structs_impl.rs:48673-48675`) | `455 4.5.3 Too many recipients.` (`rcpt.rs:57`) | No |
| 9 | Messages per session | 10 (`maxMessages = "10"`, `MtaStageData` default, `structs_impl.rs:47996-47998`) | `452 4.4.5 Maximum number of messages per session exceeded.` (`data.rs:1092-1095`) | No |
| 10 | Message size | 100 MiB (`maxMessageSize = "104857600"`, `structs_impl.rs:48002-48004`) | `552 5.3.4 Message too big for system.` (`session.rs:118-122`, `mail.rs:345-348`) | No |
| 11 | BDAT chunk overflow | chunk crossing the message limit | `552 5.3.4` and the chunk is **discarded** (message buffer reset; `session.rs:390-415`, `State::SkipData` at `session.rs:469-489`) | No |
| 12 | Session transfer quota | 250 MiB (`transferLimit = "262144000"`, `structs_impl.rs:45441-45443`) | `452 4.7.28 ... Session exceeded transfer quota.` (`spawn.rs:172-181`) | No |
| 13 | Session wall-clock duration | 10 min (`maxDuration = "10m"`, `structs_impl.rs:45433-45437`) | `421 4.3.2 ... Session open for too long.` (`spawn.rs:184-215`) | **Yes — loiter fail2ban** (`spawn.rs:192`) |
| 14 | Session idle time | 5 min (`idleTimeout = "5m"`, `structs_impl.rs:45438-45440`) | `221 2.0.0 ... Disconnecting inactive client.` (`spawn.rs:236-241`) | No — disconnect only |
| 15 | Inbound message memory budget | RAM/4, fallback 1 GiB (`BUFFERED_MEMORY_SHARE = 4`, `FALLBACK_BUFFERED_MEMORY = 1 GiB`, `crates/common/src/config/network.rs:32-33,362-371`) | `452 4.3.1` (`session.rs:32,48`; `data.rs:905`) | No — availability guard |
| 16 | VRFY / EXPN | disabled by default (`vrfy`/`expn` expressions: `false`, enabled only for authenticated senders; `MtaExtensions` default, `structs_impl.rs:44983-45040`) | `252 2.5.1 VRFY is disabled.` / `252 2.5.1 EXPN is disabled.` (`vrfy.rs:67,128`) | No |
| 17 | Received headers (loop detection) | 50 (`maxReceivedHeaders = "50"`, `structs_impl.rs:47999-48001`) | `450 4.4.6 Too many Received headers. Possible loop detected.` (`data.rs:141-152`) | No |
| 18 | Header section size / header count | 512 KiB / 5000 (`max_header_size: 524288`, `max_header_count: 5000`, `structs_impl.rs:34856-34857`; enforced by `validate_header_section`, `crates/common/src/config/mailstore/limits.rs:129-145`) | `552 5.3.4 Message header too large.` (`data.rs:119-130`) | No |
| 19 | Pipelining | advertised by default (`pipelining = "true"` unconditionally, `MtaExtensions` default, `structs_impl.rs:45020-45024`) | — | **Abuse surface**: the read loop ingests and processes the entire buffered read per iteration with **no per-read command cap** — a pipelining client can queue unbounded work per TCP segment, bounded only by the 4096-byte line limit and session quotas (rows 8-12) |
| 20 | Relay authorization | `allowRelaying = "!is_empty(authenticated_as)"` — authenticated-only (`structs_impl.rs:48676-48678`), plus `mustMatchSender = "true"` (`structs_impl.rs:47315-47317`) | `550 5.7.1` relay denied (feeds row 7) | Via rcpt counter |
| 21 | Inbound throttles (MAIL/RCPT) | expression-gated, default objects seeded (see §6) | `452 4.4.5 Rate limit exceeded, try again later.` (`mail.rs:505-508`, `rcpt.rs:342-345`) | No |
| 22 | Greylisting (RCPT) | disabled by default (see §6) | `452 4.2.2 Greylisted, please try again in a few moments.` (`rcpt.rs:300-308`) | No |
| 23 | Spam classification (DATA) | enabled for unauthenticated senders only (see §7) | `550 5.7.1` at reject threshold (`data.rs:620-623`) | No — reply-only |

**Reading the table**: rows 1, 5, 7, 13 are the only guards that feed the IP ban tier. Rows 2-4, 6, 8-18, 21-23 are **availability and hygiene guards** — they end the request or the session but leave no trace in any ban counter. Row 19 is the one guard-shaped gap.

**Confidence: HIGH** — every row verified against the cited handler and default.

## 4. Connection and Timeout Hardening

All of the following are **disconnect-only** mechanisms: they close the connection but never increment any counter, log no security event beyond tracing, and never ban.

| Surface | Setting | Default | Evidence |
|---|---|---|---|
| SMTP idle | `idleTimeout` | 5 min → `221` disconnect | `structs_impl.rs:45438-45440`; `spawn.rs:236-241` |
| SMTP absolute | `maxDuration` | 10 min → `421` + loiter check | `structs_impl.rs:45433-45437`; `spawn.rs:184-215` |
| SMTP bytes | `transferLimit` | 250 MiB → `452 4.7.28` | `structs_impl.rs:45441-45443`; `spawn.rs:172-181` |
| IMAP unauthenticated | `anonymousTimeout` | 60 s | `structs_impl.rs:38435` |
| IMAP authenticated | `authenticatedTimeout` | 30 min | `structs_impl.rs:38436` |
| IMAP IDLE cycle | `idleTimeout` | 30 min | `structs_impl.rs:38437` |
| POP3 timeouts | shares `core.imap` timeouts | same 60 s / 30 min | `crates/pop3/src/session.rs:73-75` |
| ManageSieve timeouts | shares `core.imap` timeouts | same 60 s / 30 min | `crates/managesieve/src/core/session.rs:69-73` |
| HTTP (hyper) | header read | hyper's built-in ~30 s header-read timeout applies because a timer is installed on the connection builder; **no body/read timeout is configured** | `crates/http/src/request.rs:873-879` (`http1::Builder::new().timer(...).keep_alive(true)`) |
| TLS handshake | `tlsTimeout` (listener) | 60 s, includes the PROXY-header read window before TLS | `structs_impl.rs:50262` (`tls_timeout: Some(Duration::from_millis(60000))` in `Default for NetworkListener`); applied in `crates/common/src/network/mod.rs:128-141` |
| Socket options | `TCP_NODELAY` | on, per listener config | `listen.rs:350-355` |
| TCP keepalive | idle 300 s / interval 30 s / 4 retries | always applied | `listen.rs:320-345` |
| Listen backlog | `backlog` | 1024 | `listen.rs:447` |

**Slowloris verdict.** IMAP, POP3, ManageSieve, and the HTTP body have **no hard wall-clock ceiling on total connection lifetime** — a dribbling client that sends a byte before each idle timeout can hold its connection (and its memory) indefinitely. This is memory-safe (all reads are budgeted; see row 15 of §3) but time-unbounded. **SMTP is the only protocol with hard wall-clock + byte ceilings** (10 min / 250 MiB), enforced by the `valid_until` / `bytes_left` check in the read loop (`spawn.rs:155-215`) — and SMTP is also the only protocol whose time abuse feeds a ban counter (loiter). TLS handshake and HTTP headers are individually time-bounded, but an established IMAP/HTTP-body connection is not.

**Confidence: HIGH** for the table; MEDIUM for the exact hyper header-read figure (it is hyper's documented default enabled by the timer installation, not a value in the reference platform's code).

## 5. Rate Limiting as Detection

The rate limiter (`crates/common/src/auth/rate_limit.rs`, 171 lines) is a second, separate detection layer: it **rejects** but never bans. Every breach returns `429 Too Many Requests` (HTTP) or a tagged `NO`/`BYE` (IMAP-family) with `LimitEvent::TooManyRequests` / `LimitEvent::ConcurrentRequest` — there is no path from any rate limiter to `block_ip`.

| Limiter | Scope | Default | Response | Evidence |
|---|---|---|---|---|
| HTTP authenticated requests | per **account**, 60 s window | 1000/60 s | `429` | `rate_limit.rs:25-48`; default `structs_impl.rs:36988-36991` |
| HTTP concurrent requests | per account | 4 in flight | `429` (concurrent) | `rate_limit.rs:33-60`; `account_limits()` maps `jmap.request_max_concurrent = 4` (`rate_limit.rs:117-131`, `structs_impl.rs:39460`) |
| HTTP anonymous requests | per **IP**, 60 s window | 100/60 s | `429` | `rate_limit.rs:133-151`; default `structs_impl.rs:36992-36995` |
| IMAP request rate | per **account**, post-auth only | 2000/60 s | tagged `NO` | `rate_limit.rs:63-83`; default `structs_impl.rs:38430-38433` |
| IMAP concurrent connections | per account (POP3 + ManageSieve draw from the same pool via `core.imap.rate_concurrent`) | 16 | connection refused | `structs_impl.rs:38429`; `rate_limit.rs:117-131` |
| JMAP concurrent requests | per account | 4 | `429` | `structs_impl.rs:39460` |
| JMAP concurrent uploads | per account | 4 | `429` | `structs_impl.rs:39466` |
| IMAP per-session operations | per session semaphore (read permits) | 5 concurrent ops (`MAX_CONCURRENT_OPS`) | permit waits/none | `crates/imap/src/core/mod.rs:51`; `crates/imap/src/core/client.rs:331-339` |

The anonymous HTTP limiter guards the public route set — roughly two dozen call sites (`request.rs` ×21, `api/mod.rs` ×4, `auth/authenticate.rs` ×3) covering `/.well-known/oauth-authorization-server`, `/auth/token`, `/auth/device`, `/auth/revoke`, autoconfig, MTA-STS, and similar (~20 routes; verified by direct call-site reading).

**Verified bypass — Basic-auth attempts**: routes that authenticate via `Authorization: Basic` (and the `introspect` route) call `authenticate_headers` **without** the anonymous per-IP limiter (e.g. `request.rs:330-338` DAV branch; `request.rs:477-483` introspect). A Basic-auth brute force therefore never touches the 100/60 s per-IP counter; its only backstop is the **auth fail2ban** (100/day → permanent ban) via the central funnel. The reverse also holds: `is_http_authenticated_request_allowed` only engages after successful authentication, so the 1000/60 s authenticated budget is likewise not a brute-force control. The division of labor is deliberate — but it means the *only* thing standing between an attacker and unlimited Basic-auth guesses per connection is the fail2ban counter.

**Confidence: HIGH** (all defaults and call-site structure read directly; route count approximate).

## 6. SMTP Throttles and Greylisting

### 6.1 Throttles: admin-created registry objects, factory-seeded examples

Throttles are `MtaInboundThrottle` (pre-queue) and `MtaQueueThrottle` (post-queue) registry objects (`structs_impl.rs:45505+`, `MtaQueueQuota` at `:46463`). Each object carries:

- an **enable** flag,
- a **match** expression (the gate),
- a **key** map selecting the throttle dimensions — any combination of `remote_ip`, `sender_domain`, `sender`, `rcpt_domain`, `rcpt`, `authenticated_as`, `listener`, and queue dimensions,
- a **rate** (`count`/`period`).

Evaluation happens pre-greeting/pre-`MAIL`/pre-`RCPT` against the session variables; a tripped throttle returns `452 4.4.5 Rate limit exceeded, try again later.` (`mail.rs:505-508` for MAIL, `rcpt.rs:342-345` for RCPT).

**Default state**: the schema default is empty (`Default for MtaQueue`-family objects carry no throttles), i.e. **no throttling out of the box except what the factory seeds**. The factory (`crates/common/src/manager/defaults.rs:112-141`) inserts two **enabled** example objects when the registry contains zero `MtaInboundThrottle` objects:

| Seeded object | Keys | Rate |
|---|---|---|
| "Sender IP throttle" | `remote_ip` | 5/sec per remote IP |
| "Sender address to recipient throttle" | `sender_domain` + `rcpt` | 25/hour per pair |

Both are inserted with `enable: true` (`defaults.rs:114-140`). So the accurate statement is: **the default throttle configuration is these two factory-seeded, enabled objects**; everything beyond them is admin-created. The factory seeding was verified as live code (not commented-out example config).

### 6.2 Greylisting: disabled by default, fail-open

Greylisting is a property of the spam settings object: `greylist_for` defaults to `None` (`SpamSettings` default, `structs_impl.rs:65672`) — **disabled by default**. When an admin sets it (a TTL):

- Gate: only for **unauthenticated** sessions (`rcpt.rs:256-259`: `grey_list_expiry.filter(|_| self.data.authenticated_as.is_none())`).
- Key: `KV_GREYLIST` prefix byte ‖ `blake3(len(from) ‖ from ‖ to)` over the lowercased envelope addresses (`rcpt.rs:262-277`).
- First attempt: key absent → key set with the TTL → recipient popped → `452 4.2.2 Greylisted, please try again in a few moments.` (`rcpt.rs:279-308`). A legitimate MTA retries and passes; the key exists → delivery proceeds.
- TTL expiry: the key expires, so a much-later retry is greylisted again.
- **Fail-open**: both store errors (check failure and set failure) log a tracing error and fall through to normal delivery (`rcpt.rs:310-327`). A broken in-memory store degrades greylisting to no-op, never to a mail outage.

**Confidence: HIGH.**

## 7. Spam and DNSBL Detection (Post-DATA, Message-Scoped)

The spam pipeline is message-scoped: it runs **after DATA**, classifies the message, and acts on the message. It never sees the connection layer, and it never feeds the IP ban list (verified exhaustively: the only `block_ip` call sites in the entire repository are `security.rs:274` (banned paths) and `security.rs:324` (`ban_and_reset`, used by the four fail2ban detectors)).

### 7.1 Gating: unauthenticated mail only

```rust
if !self.is_authenticated() {
    server.spam_filter_classify(&mut ctx).await
} else {
    SpamFilterAction::Disabled
}
```

(`crates/smtp/src/inbound/spam.rs:41-47`.) The same rule is expressed at the config layer: `enableSpamFilter = "is_empty(authenticated_as)"` (`MtaStageData` default, `structs_impl.rs:48009-48011`). **Authenticated sessions are never classified.**

### 7.2 DNSBL: distributed zones, A-record-only, fail-open

DNSBL (`crates/spam-filter/src/modules/dnsbl.rs`) checks IPs, domains, emails, and URLs found in the message against zone objects:

- **Zones are admin-configured** (`SpamDnsblServer` registry objects, `crates/common/src/config/mailstore/spamfilter.rs:490-556`). There are **no hardcoded zones** in the binary. The default rule set — including the stock DNSBL servers — is **distributed at runtime as a remote rules JSON** fetched from `https://github.com/reference-platformlabs/spam-filter/releases/latest/download/spam-filter-rules.json.gz` (`spam_filter_rules_url`, `SpamSettings` default, `structs_impl.rs:65676-65677`).
- **A-record-only lookups**: `resolve_zone` performs `ipv4_lookup` on the reversed query name (`dnsbl.rs:250-260`). No TXT-query DNSBLs.
- **Per-message limits per scope**: 50 checks each for ip/domain/email/url scopes (`dnsbl.ip_limit` etc., defaults `50`, `structs_impl.rs:63793-63796`; enforced by the `checks < max_checks` loop in `check_dnsbl`, `dnsbl.rs:44-76`).
- **Fail-open**: a resolver error caches `SpamLookup::None` for **60 s** (`FAILURE_CACHE_LIFETIME`, `dnsbl.rs:39`; error branch `dnsbl.rs:215-232`) and yields "not listed". A DNSBL outage can never reject mail.
- **Result caching**: listings and negatives are cached in the in-memory `spam_lookup` cache, default capacity **6 MiB** (`Cache` default, `structs_impl.rs:8987-9010`: `spam_lookup: 6291456`), with TTL-spread expiry to avoid thundering re-queries (`dnsbl.rs:34-37,234-246`).

### 7.3 Hits → tags → score → action

- Each DNSBL hit adds a **tag** (`ctx.result.add_tag`, `dnsbl.rs:72-77`); tags and other signals feed the score.
- Thresholds (defaults, `structs_impl.rs:65671-65675`): `score_spam = 5.0` (tag `spam`), `score_reject = 0.0` (**off**), `score_discard = 0.0` (**off**). So out of the box the pipeline **tags but never rejects**; rejection requires an admin to raise `score_reject`.
- At the reject threshold the message is refused with `550 5.7.1 Message rejected due to excessive spam score.` (`data.rs:620-623`) — which, because it is a DATA-stage rejection, counts nothing toward any IP ban.
- Companion machinery in the same pipeline: a Pyzor client (**disabled by default**, `SpamPyzor` default `enable: false`, `structs_impl.rs:64085`), an FTRL statistical classifier (the `nlp` crate — content classification only), and URL redirect chasing capped at **10 URLs with a 5 s per-redirect timeout** (`max_redirect_urls: 10`, `redirect_timeout: 5000 ms`, `structs_impl.rs:65680-65683`).

### 7.4 Verified absent

- **Connect-stage DNSBL rejection**: nothing queries a DNSBL at connect, EHLO, MAIL, or RCPT; the first DNSBL touch is post-DATA.
- **URIBL as a first-class module**: URL checks exist only through the generic DNSBL scope machinery above.
- **Rspamd/SpamAssassin integration**: none built in; external filters attach via milter or HTTP hook endpoints only (stage-selectable external rejection paths, §8).

**Confidence: HIGH** for all of §7.

## 8. Mail-Auth Verification and the Expression Engine as Policy Layer

### 8.1 Stage-by-stage mail-auth

| Check | Stage | Default policy | Reject behavior |
|---|---|---|---|
| SPF (EHLO) | MAIL pre-stage | `VerifyStrategy::Relaxed` fallback (`params.rs:31-34`) | `550 5.7.23 SPF validation failed, status: {result}.` (`mail.rs:521-524`); softfail/temp yields `451`-class defer |
| SPF (MAIL FROM) | MAIL | `VerifyStrategy::Relaxed` (`params.rs:35-38`) | same |
| iprev | MAIL | `VerifyStrategy::Relaxed` (`params.rs:39-42`) | policy-driven |
| DKIM / ARC / DMARC | DATA | DMARC policy-driven | `550 5.7.1 Email rejected per DMARC policy.` (`data.rs:505-509`) |
| Multiple author domains | DATA, unauthenticated mail only | reject | `550 5.7.1 Multiple author domains in From header are not allowed.` (`data.rs:457-461`) |

All of these are **message-stage detections**: they reject the message, never the connection.

### 8.2 The expression engine as the universal policy layer

Every stage parameter cited in this report is an `IfBlock` expression (`crates/common/src/expr`) evaluated against live session variables — `remote_ip`, `helo`, `sender`, `authenticated_as`, `local_port`, `is_tls`, `asn`, `country`, and so on. The defaults shown throughout (e.g. `enableSpamFilter = "is_empty(authenticated_as)"`, `allowRelaying = "!is_empty(authenticated_as)"`, `rejectNonFqdn` on `local_port == 25`) are themselves one-line expressions, which is what makes per-listener, per-network, per-country tuning possible without code changes.

- **Store-backed lists**: rules can call `key_exists('list-name', key)` to test membership in a store-backed list (`crates/common/src/expr/functions/asynch.rs:139`). Lists are **string-keyed** (no CIDR expansion at the list layer — IP keys are exact strings), **fail-open** when the store is misconfigured, and **SQL stores are unsupported** for list lookups.
- **Capability boundary**: expressions control *decisions at the current stage* — reject, defer, tag, allow — but there is **no side-effecting block action**. An expression can never add an IP to the block list; `block_ip` is reachable only from the fail2ban and banned-path paths (§7.1 verification). Detection that "escapes" the expression engine into a ban happens only through the four counters.

### 8.3 External rejection paths

Three more message/connection gates are stage-selectable and external-policy-driven: **trusted Sieve scripts** (each stage object carries a `script` property — Connect, EHLO, MAIL, RCPT, DATA — and a Sieve `reject`/`discard` acts at that stage), **milters**, and **HTTP MTA hooks**. Like everything in this section, they produce per-message/per-stage responses, not bans.

**Confidence: HIGH.**

## 9. OAuth and Auth-Hygiene Detection Surface

These are not "detectors" in the counter sense; they are hardening defaults that shrink what a detector ever has to see. They are cataloged here because each one produces a security event (`IpUnauthorized`, `AuthEvent::Failed`) that feeds the auth fail2ban when violated.

| Control | Default | Evidence |
|---|---|---|
| PKCE | `S256` code challenge **mandatory** — other methods rejected with `"The code_challenge_method must be S256."` | `crates/http/src/auth/oauth/authorize.rs:255-262` |
| ROPC ("password grant") | not implemented — no `grant_type=password` code path exists in the token endpoint | token handler, `crates/http/src/auth/oauth/token.rs` |
| Bearer tokens over IMAP/POP3/Sieve | accepted only when `is_tls \|\| bootstrap \|\| recovery` (`accepts_bearer_tokens`) | `crates/common/src/auth/sasl.rs:83-85` |
| CIMD client-id metadata fetch | rate-limited to **30/min per domain**, 5 KiB document cap, 5 s fetch timeout | `crates/common/src/auth/oauth/cimd.rs:31-40` |
| Plaintext auth (IMAP/POP3/Sieve) | disabled by default (`allow_plain_text_auth: false`, `structs_impl.rs:38427`); SASL mechanisms offered only when `local_port != 25 && is_tls` (`MtaStageAuth` default, `structs_impl.rs:47306-47312`) → `LOGINDISABLED` advertised without TLS | same |
| Per-credential IP allowlists | credentials with `allowedIps` from a non-allowed source produce `SecurityEvent::IpUnauthorized`, which is **auth-fail2ban-eligible** (`authentication.rs:95-105` matches `IpUnauthorized`) | credential model + funnel |
| Auth failure timing | 50-500 ms random delay before any auth failure response | `authentication.rs:73-82` |

**Confidence: HIGH.**

## 10. What Is NOT Detection (Explicit Absences)

Each absence below was verified by exhaustive term sweep and/or structural reads at commit `23b69755`:

1. **Port scanning** — no cross-port or connection-topology detection of any kind. The `PortScanning` block reason covers pre-auth parse failures and banned HTTP paths only. (Full argument: Report B.)
2. **Per-IP connection rate** — no limiter counts connections per IP. Concurrency caps are per-account (IMAP 16, HTTP 4, JMAP 4/4); the anonymous HTTP limiter is per-request, not per-connection.
3. **Account lockout** — none. No directory-level or application-level lockout exists anywhere in the repository (sweep for lockout/locked/lock terms: only unrelated hits — `BlockedIps`, IPC lock states). Repeated bad passwords produce only: auth fail2ban counts, the 3-error SMTP session limit, and IMAP-family tagged failures. A distributed slow brute force under 100 failures/day/key never locks or bans anything.
4. **Progressive / escalating bans** — ban durations are a fixed per-reason setting; there is no escalation logic, no repeat-offender tracking, no ban-history aggregation. Every ban is "reset counter, apply fixed period (default: permanent)".
5. **Cross-port correlation** — no data structure records which listener/port a failure came from; keys contain no port. The scan counter pools across protocols but says nothing about which ports were touched.
6. **IPv6 /64 (or any prefix) aggregation** — all ban/counter keys are the exact address bytes (`ip_to_bytes`, `crates/common/src/network/mod.rs:290-297`: raw V4/V6 octets). A /128 for IPv6, a /32 for IPv4. An attacker with a /64 can trivially out-run every IP-keyed detector by rotating addresses.
7. **Statistical behavioral anomaly detection** — the `nlp` crate is content classification (spam/phishing text models) only. A repository-wide case-insensitive sweep for `anomaly` returns **zero hits**. There is no baseline-learning, no outlier scoring, no per-account behavioral profiling.

**Confidence: HIGH** for items 1, 3, 4, 6, 7 (direct sweeps/reads); HIGH for 2 and 5 (limiter and key-structure reads).

## 11. Priority Classification and Confidence Table

The table ranks every mechanism by **detection signal quality** (how reliably a trip indicates hostile intent), classifies it (security guard vs availability guard), and records its default state.

| Rank | Mechanism | Signal quality | Class | Default | Rationale |
|---|---|---|---|---|---|
| 1 | **auth fail2ban** (100/day, IP+blake3(login), permanent) | **Highest** | Security | **ON** | Trips only on failed credentials or unauthorized-source use; dual keys catch both stuffing and spraying; permanent ban is proportionate; central funnel means no protocol can bypass it |
| 2 | **HTTP banned paths** (12 globs, instant permanent) | **Highest** | Security | **ON** | Zero false-positive risk in practice (no legitimate route serves `*.php` on a the reference platform origin); one hit = scanner confirmed |
| 3 | **rcpt fail2ban** (35/day, IP+blake3(rcpt), permanent) | High | Security | **ON** | Directory-harvest and relay-probing are unambiguous hostilities; the blake3(rcpt) key also catches per-recipient enumeration from distributed IPs; 5 s sleep per error already throttles the attempt itself |
| 4 | **scan fail2ban** (30/day pooled, IP-only, permanent) | Medium-high | Security | **ON** | Pre-auth parse garbage is hostile or badly broken software; IP-only key makes it noisy for CGNAT users, but 30/day is conservative |
| 5 | **loiter fail2ban** (150/day, IP-only, permanent) | Medium | Security | **ON** | Idling is weak evidence (a stuck client also trips it), but 150/day headroom keeps accidental trips well under the ban line |
| 6 | **Spam filter + DNSBL** (score 5.0 tag; reject off) | High (per-message) | Classify | **ON** (tag only) | Strong content signal, message-scoped; as shipped it informs rather than blocks |
| 7 | **Mail-auth (SPF/iprev/DKIM/DMARC/multi-author)** | High (per-message) | Classify | ON (DMARC policy-driven) | Spoofing detection; reject actions are policy-configured, per-message |
| 8 | **Rate limiters** (HTTP/IMAP/JMAP + concurrency) | Medium | Availability | **ON** | Trips on load, scripts, and sync storms as often as attacks; correct as availability guards, not detection |
| 9 | **SMTP session guards** (rows 2-4, 6, 8-12, 14-18, 21-22) | Low-medium | Availability/hygiene | **ON** | Ceilings and hygiene; mostly uncounted by design |
| 10 | **Throttles** (2 seeded objects, 5/s per IP, 25/h sender→rcpt) | Medium | Availability | **ON** (seeded) | Legit high-volume senders can trip these; 452 is retryable, so impact is defer-not-drop |
| 11 | **Greylisting** | Medium | Availability | **OFF** | Deliberate admin opt-in; burdens all first-time senders |
| 12 | **Spam reject/discard** | High (if tuned) | Classify | **OFF** (0.0 = off) | Admin must opt into rejection |
| 13 | **Timeouts/limits hardening** (§4) | — | Availability | **ON** | Not detection at all; disconnect-only |

**Default-state summary**: the ban tier ships fully armed — fail2ban **ON** for all four detectors with **permanent** bans; banned paths **ON**; spam classification **ON** (tag-only, unauthenticated mail); mail-auth **ON**; rate limiters **ON**; both seeded throttles **ON**. Opt-in only: greylisting (**OFF**), spam reject/discard (**OFF** at 0.0), Pyzor (**OFF**), temporary ban durations (**OFF** — permanent unless set), recovery-mode disables the ban tier entirely.

**Design lessons for ReForwarder** (cross-reference Report F):

1. The **central auth funnel** (one `authenticate` + one `auth_failure` on the server object, all five protocols calling it) is what makes the auth fail2ban airtight — replicate this topology rather than per-protocol counters.
2. **Dual-key counters** (IP + blake3(credential)) catch distributed credential stuffing that IP-only counters structurally cannot; the cost is two counter increments per failure, which is trivial.
3. **Increment-on-check** keeps failure recording and breach detection atomic in one call site — no forgotten "record" step.
4. **Separate the tiers**: bans (rare, high-signal) vs limits (common, low-signal) vs classification (message-scoped). the reference platform never lets a limiter or a spam rule create a ban, and that boundary is load-bearing.
5. **Fail-open everywhere failure detection depends on infrastructure** (greylist store errors, DNSBL resolver errors): a broken dependency degrades detection to no-op, never to outage — but note the corollary: an attacker who can induce store errors can disable greylisting.
6. **Watch the gaps** the reference platform leaves open, because they are the likely next decade of abuse: no /64 aggregation (IPv6 rotation defeats all IP keys), no lockout (slow brute force is invisible below 100/day), no per-IP connection rate, no body timeout on HTTP, and pipelining without a per-read command cap.

## 12. Sources

**Primary repository** (all file:line citations, verified at commit `23b69755cb3cd8ecd952fe0443c5ac067a506184`, tag `v1.0.1`):

- `crates/common/src/network/security.rs` — fail2ban engine, banned paths, `block_ip`, `ban_period`
- `crates/common/src/auth/authentication.rs` — central `authenticate` / `auth_failure` funnel, 50-500 ms delay
- `crates/common/src/auth/rate_limit.rs` — HTTP/IMAP rate limiters, `account_limits`
- `crates/common/src/auth/sasl.rs` — `accepts_bearer_tokens`
- `crates/common/src/auth/oauth/cimd.rs` — CIMD fetch rate/limits
- `crates/common/src/auth/access_token.rs` — per-account concurrency limiters
- `crates/common/src/network/mod.rs` — `ip_to_bytes`, TLS handshake timeout application
- `crates/common/src/network/listen.rs` — accept-time IP-block check, socket options, backlog
- `crates/common/src/network/memory.rs` — memory budget classes
- `crates/common/src/config/network.rs` — `max_buffered_memory` (RAM/4, 1 GiB fallback)
- `crates/common/src/config/smtp/session.rs` — SMTP session IfBlock wiring
- `crates/common/src/config/mailstore/spamfilter.rs` — spam/DNSBL config parsing
- `crates/common/src/config/mailstore/limits.rs` — `validate_header_section`
- `crates/common/src/manager/defaults.rs` — factory-seeded throttle objects, queue quota
- `crates/common/src/expr/functions/asynch.rs` — `key_exists` list function
- `crates/smtp/src/core/mod.rs` — `SessionData` (incl. `dnsbl_error` vestige), `SessionParameters`
- `crates/smtp/src/core/params.rs` — session parameter resolution and fallbacks
- `crates/smtp/src/inbound/session.rs` — line limits, SASL limit, DATA limits, memory guard
- `crates/smtp/src/inbound/rcpt.rs` — rcpt error counting, fail2ban call, greylisting, throttle, 455/451
- `crates/smtp/src/inbound/auth.rs` — `auth_error` (3×5 s → 455), `auth_failure` funnel
- `crates/smtp/src/inbound/mail.rs` — message size, throttle, SPF reject
- `crates/smtp/src/inbound/data.rs` — header limits, loop detection, DMARC/multi-author/spam rejects, memory, messages/session
- `crates/smtp/src/inbound/spam.rs` — authenticated-session classification gate
- `crates/smtp/src/inbound/vrfy.rs` — VRFY/EXPN 252 responses
- `crates/smtp/src/inbound/spawn.rs` — session loop: transfer quota, max duration, idle, loiter call
- `crates/imap/src/op/authenticate.rs`, `crates/imap/src/core/mod.rs`, `crates/imap/src/core/client.rs` — IMAP auth funnel, `MAX_CONCURRENT_OPS`, PARSE scan trigger
- `crates/pop3/src/op/authenticate.rs`, `crates/pop3/src/session.rs` — POP3 funnel, shared timeouts
- `crates/managesieve/src/op/authenticate.rs`, `crates/managesieve/src/core/session.rs` — Sieve funnel, shared timeouts
- `crates/http/src/request.rs` — routing, anonymous limiter sites, `ban_scanned_path`, hyper connection builder, parse-error scan trigger
- `crates/http/src/auth/oauth/registration.rs`, `authorize.rs`, `token.rs` — registration fail2ban call, PKCE S256, no ROPC
- `crates/jmap/src/registry/mapping/account.rs` — JMAP credential re-verification funnel
- `crates/spam-filter/src/modules/dnsbl.rs` — DNSBL lookups, caching, fail-open, per-scope limits
- `crates/store/src/dispatch/lookup.rs` — fixed-window counter implementation (increment-on-check)
- `crates/registry/src/schema/structs_impl.rs` — all defaults: `Security` (:58373-58414), `Http` (:36984-36995), `Imap` (:38424-38446), `Jmap` (:39452+), `MtaInboundSession` (:45432-45446), `MtaStageAuth` (:47295+), `MtaStageData` (:47943+), `MtaStageRcpt` (:48663+), `MtaExtensions` (:44983+), `MtaStageEhlo` (:48239+), `SpamSettings` (:65668-65686), `DnsBlSettings` (:63793-63796), `SpamPyzor` (:64085), `Cache` (:8987-9010), email header limits (:34856-34857), listener `tls_timeout` (:50262)

**Companion reports in this series:**

- Report A — `docs/security-research/reference-platform-security-architecture.md`
- Report B — `docs/security-research/reference-platform-port-scanner-detection.md` (scan-counter trigger exhaustiveness, `PortScanning` naming analysis)
