# Report B — Port-Scanner Detection in Stalwart: What Exists, What Doesn't, and What ReForwarder Should Learn

| | |
|---|---|
| **Subject repository** | Stalwart Mail Server (`stalwartlabs/stalwart`), clone at `C:\Users\Ibrahim\.zcode\tmp\stalwart-analysis\stalwart` |
| **Commit analyzed** | `23b69755cb3cd8ecd952fe0443c5ac067a506184` (tag `v1.0.1`, HEAD of `main`) |
| **Date** | 2026-11-01 |
| **Method** | Full-file reads of the network security, listener, limiter, and all five protocol server paths; exhaustive case-insensitive term sweep across all 1,717 repository files (excluding `.git/`, `target/`, `.cargo/`); every hit classified. All file:line citations in this report were re-verified against the pinned commit at write time. |
| **Report series** | Report B of the ReForwarder security-research series (companion to Report F, the ReForwarder design document) |

## Table of Contents

1. [The Verdict](#1-the-verdict)
2. [What Actually Exists](#2-what-actually-exists)
   - 2.1 [The fail2ban counter: `is_scanner_fail2banned`](#21-the-fail2ban-counter-is_scanner_fail2banned)
   - 2.2 [The five trigger sites](#22-the-five-trigger-sites)
   - 2.3 [Cross-protocol pooling](#23-cross-protocol-pooling)
   - 2.4 [The HTTP banned-path globs](#24-the-http-banned-path-globs)
3. [Verdict Table: What Each Classic Scan Behavior Actually Produces](#3-verdict-table-what-each-classic-scan-behavior-actually-produces)
4. [Closest Analogs in Stalwart](#4-closest-analogs-in-stalwart)
5. [Implications for ReForwarder](#5-implications-for-reforwarder)
6. [Confidence and Verified-Absent Statements](#6-confidence-and-verified-absent-statements)
7. [Sources](#7-sources)

---

## 1. The Verdict

> **Stalwart v1.0.1 has NO dedicated port-scanner detection.** There is no module, function, configuration setting, or cross-port tracker anywhere in the codebase that detects an IP probing multiple service ports. A TCP-level scan — a sweep across ports 25/143/993/587/465/110/995/4190/443, rapid repeated connects to one port, or connect-and-abort probes — produces **no security event, no counter increment, and no ban of any kind**.

The naming is a misnomer, and it is the single most misleading element of this subsystem:

- The name **`PortScanning`** (`BlockReason::PortScanning = 3`, `crates/registry/src/schema/enums.rs:259-269`) is the block reason attached to a **fail2ban counter over pre-auth protocol parse failures** and to the **HTTP banned-path instant-ban list**. Neither mechanism ever looks at ports or connection topology.
- The comment **"Check for port scanners"** appears at 4 of the 5 call sites of `is_scanner_fail2banned` (`crates/smtp/src/inbound/session.rs:275`, `crates/imap/src/core/client.rs:67`, `crates/pop3/src/client.rs:66`, `crates/managesieve/src/core/client.rs:96`; the fifth call site, `crates/http/src/request.rs:1042`, carries no comment). Every one of those checks fires on a **protocol parse error on an already-accepted connection**, never on connection-level behavior.
- The CHANGELOG line for the feature — *"Detect and ban port scanners as well as other forms of abuse (#820)"* (`CHANGELOG.md:1549`) — refers to this parse-failure fail2ban plus banned-URL feature, **not** to a TCP-level scanner detector. (Confidence: HIGH — CHANGELOG line read directly; feature traced end-to-end.)

**Confidence: HIGH.** Established by full-file reads of every network-entry path plus an exhaustive term sweep (Section 6).

## 2. What Actually Exists

Two mechanisms carry the `PortScanning` label: a pooled fail2ban counter (Section 2.1–2.3) and an instant-ban HTTP path-glob list (Section 2.4). Both ban by IP; neither observes ports.

### 2.1 The fail2ban counter: `is_scanner_fail2banned`

**Function** (`crates/common/src/network/security.rs:261-268`):

```rust
pub async fn is_scanner_fail2banned(&self, ip: IpAddr) -> trc::Result<bool> {
    if let Some(rate) = &self.core.network.security.scanner_fail_rate {
        self.is_ip_fail2banned(ip, KV_RATE_LIMIT_SCAN, rate, BlockReason::PortScanning)
            .await
    } else {
        Ok(false)
    }
}
```

Mechanics, all verified line-by-line:

| Property | Value | Evidence |
|---|---|---|
| Counter key | `KV_RATE_LIMIT_SCAN` prefix byte (`= 3`, `crates/common/src/lib.rs:147`) concatenated with `ip_to_bytes(ip)` — **IP-only: no port, no protocol, no listener dimension** | `crates/common/src/network/security.rs:291-314` (`is_ip_fail2banned`), `crates/store/src/dispatch/lookup.rs:225-247` (`is_rate_allowed` → `window.bucket(prefix, key)`) |
| Window | Fixed window via `RateWindow::new(rate, now)`; every call to `is_rate_allowed` **increments** the counter by 1 before comparing (`key_incr_at(..., 1)`) — the check *is* the increment | `crates/store/src/dispatch/lookup.rs:225-247` |
| Threshold | Default **30 failures / 86,400,000 ms (1 day)** (`scan_ban_rate: Some(Rate { count: 30, period: Duration::from_millis(86400000) })`) | `crates/registry/src/schema/structs_impl.rs:58405-58408` |
| On breach | `ban_and_reset` → `block_ip(ip, BlockReason::PortScanning)` + `rate_reset` of the counter | `crates/common/src/network/security.rs:316-334` |
| Ban duration | `ban_period(BlockReason::PortScanning)` = `scan_ban_period`, default `Default::default()` = `None` → `expires_at = None` → `expiry = u64::MAX` → **permanent ban** (persisted as a `BlockedIp` object in the registry) | `crates/common/src/network/security.rs:217-225`, `371-391`; default at `crates/registry/src/schema/structs_impl.rs:58409` |
| Exemptions | `is_ip_allowed(ip)` short-circuits before any counting: config allowlist plus **loopback always** (127.0.0.1 and ::1 inserted unconditionally), plus proxy-trusted networks | `crates/common/src/network/security.rs:298-300`, `157-168`, `455` |

Enforcement of the ban happens at accept time, before any protocol work: the listener's accept path checks `server.is_ip_blocked(remote_ip)` and rejects with a `SecurityEvent::IpBlocked` event (`crates/common/src/network/listen.rs:277-286`).

**Recovery mode:** when the server is in recovery mode, `scanner_fail_rate` is filtered out (`None`) and `http_banned_paths` is emptied — **scan banning is disabled entirely** — while the other fail2ban rates are likewise disabled (`crates/common/src/network/security.rs:181-193`, specifically `scanner_fail_rate: security.scan_ban_rate.filter(|_| !is_recovery_mode)` at line 193 and the `http_banned_paths` branch at lines 184-192).

**Confidence: HIGH** (direct reads of the full function, the shared `is_ip_fail2banned`, the counter implementation, and the defaults).

### 2.2 The five trigger sites

All five call sites of `is_scanner_fail2banned` (exhaustive — this is every call site in the repository, confirmed by grep):

| # | Protocol | Condition | Location |
|---|---|---|---|
| 1 | SMTP | `Error::UnknownCommand \| Error::InvalidResponse` on pre-auth command parse, gated `if !self.is_authenticated()` | `crates/smtp/src/inbound/session.rs:272-308` (gate at 276, call at 279) |
| 2 | IMAP | response code `PARSE` while `State::NotAuthenticated` | `crates/imap/src/core/client.rs:67-94` (call at 75) |
| 3 | POP3 | `Error::Parse(_)` while `State::NotAuthenticated` | `crates/pop3/src/client.rs:66-87` (call at 68) |
| 4 | ManageSieve | response code `PARSE` while `State::NotAuthenticated` | `crates/managesieve/src/core/client.rs:96-123` (call at 104) |
| 5 | HTTP | hyper `http_err.is_parse()` on the connection error, **only when** `remote_ip_source.is_peer()` (i.e. the client IP is the direct TCP peer, not recovered from a proxy header) | `crates/http/src/request.rs:1036-1060` (call at 1042) |

SMTP is the narrowest of the five: **authenticated clients are exempt** (the `!self.is_authenticated()` gate at `crates/smtp/src/inbound/session.rs:276`), and of the pre-auth error branches only `UnknownCommand | InvalidResponse` reach the scanner check (line 274). The neighboring branches are **not counted** — syntax errors (`Error::SyntaxError { syntax }` → `501 5.5.2`, line 341; `Error::InvalidParameter` → `501 5.5.4`, line 355; `Error::InvalidSenderAddress` → `501 5.1.8`, line 315; `Error::InvalidRecipientAddress` → `501 5.1.3`, line 325), unsupported parameters (`Error::UnsupportedParameter` → `504 5.5.4`, line 368), and overlong lines (`Error::ResponseTooLong` → `554 5.3.4`, line 503) all emit their responses without touching the scan counter.

Each trigger, when the counter trips, logs `SecurityEvent::ScanBan` and closes the session (`SessionResult::Close` / `return Err(())` / `return`) — e.g. `crates/smtp/src/inbound/session.rs:282-291`.

**Confidence: HIGH** (all five sites read in full; the call-site list is exhaustive by grep).

### 2.3 Cross-protocol pooling

Because the counter key is **IP-only** — prefix byte `3` + IP bytes, with no protocol, port, or listener component (`crates/common/src/network/security.rs:291-314`) — the five protocol servers all feed **one shared counter per client IP**. Parse-failure counts pool across services:

> Example (arithmetic, not a code path): an IP sending 15 malformed IMAP commands (`crates/imap/src/core/client.rs:75`) and 16 malformed SMTP commands (`crates/smtp/src/inbound/session.rs:279`) accumulates 31 increments of the same key → 31 > 30/day → `block_ip(PortScanning)` on the 31st failure.

This is **cross-protocol error pooling** — a real and deliberate design — but it must not be confused with cross-port connection tracking. Nothing anywhere correlates connections across *ports*: the key contains no port, and no data structure records which listener a failure came from.

**Confidence: HIGH** (key construction read directly; pooling follows necessarily from the shared prefix+IP key).

### 2.4 The HTTP banned-path globs

The second `PortScanning` mechanism is instant and counterless:

- **Match function** `is_http_banned_path(path, ip)` (`crates/common/src/network/security.rs:270-280`): if any compiled glob matches the request path and the IP is not allowlisted → `block_ip(ip, BlockReason::PortScanning)` immediately. No rate, no counter, no first-offense tolerance.
- **Invocation** `ban_scanned_path` (`crates/http/src/request.rs:853-868`), called from the 404 fallback handler at `crates/http/src/request.rs:846-847` — i.e., the check runs when a request has matched no route and is about to be answered "Not Found".
- **Default globs — exactly 12** (`crates/registry/src/schema/structs_impl.rs:58391-58404`):

  ```
  *.php*    *.cgi*    *.asp*    */wp-*    */php*    */cgi-bin*
  *xmlrpc*  *../*     */..*     *joomla*  *wordpress*  *drupal*
  ```

  (Globs compiled with `GlobPattern::compile(pattern, true)` — case-insensitive — at `crates/common/src/network/security.rs:190`.)

This is a signature list for HTTP exploit scanners (PHP/CMS/WordPress/Joomla/Drupal probes and traversal attempts), and it is the only "scanner" detection in Stalwart that behaves the way its name suggests: one hit → instant, permanent `PortScanning` ban.

**Confidence: HIGH** (all three components read in full).

## 3. Verdict Table: What Each Classic Scan Behavior Actually Produces

| Scan behavior | What Stalwart v1.0.1 does | Evidence |
|---|---|---|
| **Multi-port sweep across services** (probe 25, then 143, then 993, …) | **NOTHING.** No cross-port correlation exists anywhere. The SCAN counter is fed by protocol parse errors, not port touches. Each connect runs the same stateless path: TCP accept → blocklist check (`is_ip_blocked`, `crates/common/src/network/listen.rs:277`) → global concurrency check (same file, line 288) → protocol session; on disconnect, `NetworkEvent::Closed` is logged and all session state is dropped. The accept path maintains **no per-IP, per-port, or per-history state**. | `crates/common/src/network/listen.rs:270-316`; disconnect handling per protocol cited in rows below |
| **Rapid repeated same-port connects, no data** | **NOTHING per-IP.** There is no per-IP connection rate or per-IP concurrency limit at accept. The only connection-count control is `ConcurrencyLimiter`, a single global `AtomicU64` per listener (`crates/common/src/network/limiter.rs:97-126`; constructed once per listener at `crates/common/src/network/listen.rs:45` with `max_connections`, default **8192** — `crates/registry/src/schema/structs_impl.rs:67663`). The only IP-keyed accept-time state is the blocklist. TLS ports: a failed/expired handshake logs `TlsEvent::HandshakeError` and drops the stream — **no counter** (`crates/common/src/network/listen.rs:503-512`). | `crates/common/src/network/limiter.rs:97-126`; `crates/common/src/network/listen.rs:45, 503-512` |
| **Connect + abort** (open socket, send nothing or a few bytes, close) | **NOTHING.** In every protocol server, a read of zero bytes before a request logs `NetworkEvent::Closed` and breaks the session loop — no fail2ban call exists in any of these paths: SMTP (`crates/smtp/src/inbound/spawn.rs:218-226`), IMAP (`crates/imap/src/core/session.rs:74-81`), POP3 (`crates/pop3/src/session.rs:90-97`). Greeting/idle timeouts — SMTP default **5 minutes** (`Duration::from_secs(5 * 60)`, `crates/smtp/src/core/params.rs:22-24`; timeout branch at `crates/smtp/src/inbound/spawn.rs:231-245` sends "221 Disconnecting inactive client" and breaks) and IMAP/POP3/ManageSieve unauthenticated **60 seconds** (`timeout_unauth`, default `Duration::from_millis(60000)` at `crates/registry/src/schema/structs_impl.rs:38435`; selected at `crates/imap/src/core/session.rs:55-59`, `crates/pop3/src/session.rs:71-75`, `crates/managesieve/src/core/session.rs:69-73`) — are strictly **disconnect-only**: no `is_scanner_fail2banned` call exists in any timeout branch (the exhaustive call-site list in Section 2.2 contains none). HTTP: hyper timeouts are explicitly excluded from the scan check (`!http_err.is_timeout()`, `crates/http/src/request.rs:1037`). | As cited per row |
| **Unbound-port probes** (hit a port with no listener) | **UNOBSERVABLE.** The kernel refuses the connection; the application never sees it. Even application-side bind failures are merely logged (`NetworkEvent::ListenError`) and skipped (`crates/common/src/network/listen.rs:66-90`). | `crates/common/src/network/listen.rs:66-90` |
| **Pre-auth garbage on one port** (malformed SMTP/IMAP/POP3/Sieve commands, malformed HTTP) | **THE one signal.** Each parse failure increments the pooled per-IP counter (30/day default → permanent ban on breach). See Section 2.1–2.3 for mechanics and the SMTP caveats (authenticated exempt; 501/504/554 branches not counted). | `crates/smtp/src/inbound/session.rs:272-308`; `crates/imap/src/core/client.rs:67-94`; `crates/pop3/src/client.rs:66-87`; `crates/managesieve/src/core/client.rs:96-123`; `crates/http/src/request.rs:1036-1060` |
| **HTTP exploit-path scanner** (`/wp-login.php`, `/cgi-bin/…`, `*../*` traversal, etc.) | **Instant permanent ban.** First 404 whose path matches any of the 12 default globs → `block_ip(PortScanning)`, no counter, no grace. | `crates/http/src/request.rs:846-868`; `crates/common/src/network/security.rs:270-280`; `crates/registry/src/schema/structs_impl.rs:58391-58404` |

**Confidence: HIGH for every row** (each row rests on full-file reads of the named paths; the "NOTHING" verdicts additionally rest on the exhaustive term sweep and the exhaustive call-site enumeration in Section 2.2 and Section 6).

## 4. Closest Analogs in Stalwart

For ReForwarder's design work, the neighboring mechanisms that come closest to connection-level controls:

- **`MtaInboundThrottle` (SMTP)** — the only inbound rate-limiting primitive that can be keyed on `remote_ip`: its key enum is `MtaInboundThrottleKey { Listener = 0, RemoteIp = 1, LocalIp = 2, AuthenticatedAs = 3, HeloDomain = 4, Sender = 5, SenderDomain = 6, Rcpt = 7, RcptDomain = 8 }` (`crates/registry/src/schema/enums.rs:1521-1531`). It is message/transaction-oriented (throttles across SMTP transactions), is configured per-object with **no default instances** (`MtaInboundThrottle` default is empty; loaded at `crates/common/src/config/smtp/queue.rs:451-469`), and does not count connection events.
- **Global listener concurrency** — one `AtomicU64` per listener, default 8192 (`crates/common/src/network/limiter.rs:97-126`, `crates/common/src/network/listen.rs:45`, default at `crates/registry/src/schema/structs_impl.rs:67663`). Protects the server from overload, not the network from reconnaissance.
- **Recovery mode** — disables scan banning entirely (rate filtered to `None`, banned-path list emptied) alongside the other fail2ban rates (`crates/common/src/network/security.rs:181-193`).
- **Anti-lockout exemptions** — allowlisted IPs and always-loopback are exempt from every scanner check (`crates/common/src/network/security.rs:157-168, 298-300`); the blocklist check at accept and the fail2ban paths both honor `is_ip_allowed` (`crates/common/src/network/security.rs:455`).

## 5. Implications for ReForwarder

(The full design lives in Report F; this section records only what this study establishes.)

1. **A port-scan detector must be designed fresh.** There is no Stalwart mechanism to imitate — not the `PortScanning` name, not the fail2ban counter (it counts parse errors, not connections), not the concurrency limiter (global, not per-IP). Adopting "what Stalwart does" for port scans would adopt nothing.
2. **Uncounted signals Stalwart leaves on the table** — all directly implementable at the accept path ReForwarder will control:
   - a **per-IP connection-rate counter** at accept (Stalwart's accept path touches the IP for the blocklist check anyway, `crates/common/src/network/listen.rs:277`, but records nothing else);
   - **distinct-destination-port tracking** per source IP over a sliding window;
   - **connect-without-protocol-abort counting** (zero/short-byte reads before any valid protocol data — the exact branches Stalwart closes silently: `crates/smtp/src/inbound/spawn.rs:218-226`, `crates/imap/src/core/session.rs:74-81`, `crates/pop3/src/session.rs:90-97`, and TLS handshake errors, `crates/common/src/network/listen.rs:503-512`).
3. **Worth adopting from Stalwart:** the proven pieces are (a) **pooled parse-error fail2ban** — one IP-keyed counter fed by all protocol servers, fixed window, ban-and-reset (`crates/common/src/network/security.rs:291-334`), and (b) the **banned-path glob list** with instant ban on match (`crates/http/src/request.rs:846-868`, `crates/common/src/network/security.rs:270-280`). Both are simple, cheap, and battle-tested.
4. **Stalwart's design trade-off is deliberate and should be a conscious choice for ReForwarder too:** by banning only on *protocol violations*, the false-positive surface is very low (no legitimate mail client sends malformed commands), at the cost of **zero TCP-reconnaissance visibility** — a scanner can map every port Stalwart exposes, on every service, indefinitely, and never trip anything. ReForwarder's threat model must decide where it wants to sit on that trade-off.
5. **Naming lesson:** Stalwart's `BlockReason::PortScanning` label on a parse-error counter shows how a misleading name propagates — the CHANGELOG advertises "detect port scanners," the enum says `PortScanning`, and the code counts malformed commands. ReForwarder should name detection mechanisms by their signal, not by the threat they hope to address.

## 6. Confidence and Verified-Absent Statements

**Overall: HIGH.** Every file:line citation in this report was re-read against commit `23b69755` immediately before writing. The absence claims rest on two independent methods:

**Method 1 — exhaustive term sweep.** Case-insensitive search across **all 1,717 files** in the repository (excluding `.git/`, `target/`, `.cargo/`; run both with and without file-type filters). Terms and classification of every hit:

| Term | Hits | Classification |
|---|---|---|
| `port_scan` | 0 | Verified absent |
| `portscan` | Only `PortScanning` enum/serialize strings (`crates/registry/src/schema/enums.rs:266`, `crates/registry/src/schema/enums_impl.rs:1357, 1369`, `crates/common/src/network/security.rs:222, 263, 274`) | The misnomer itself — no scanner logic |
| `scan_detect` | 0 | Verified absent |
| `multi_port` | 0 | Verified absent |
| `ports_touched` | 0 | Verified absent |
| `handshake_fail` | 0 (the TLS path uses `TlsEvent::HandshakeError`, `crates/common/src/network/listen.rs:505` — a log event, no counter) | Verified absent |
| `nmap` | Substring false positives only (`unmapped`/`unmap` in `crates/calcard/src/jscontact/export/params.rs:148` et al., `crates/common/src/expr/tokenizer.rs`, script plugin files) | No port-scanner references |
| `honeypot` | Unrelated: HTTP form anti-spam honeypot field (`crates/http/src/form/mod.rs:68`) and JMAP `fieldHoneyPot` property (`crates/registry/src/schema/properties.rs:815`) | No honeypot-port mechanism |
| `probe` | Unrelated: cache-swap liveness probe file (`crates/common/src/cache/swap/file.rs:32-36`), test perf module (`crates/common/src/cache/swap/messages.rs:335-336`), enterprise AI admission `Probe` (`crates/common/src/enterprise/ai/limits.rs:39, 140, 163`) | No network-probe detection |
| `recon` | Substring false positives only (`preconnect`, `unmapped`) | Verified absent |

**Method 2 — exhaustive call-site enumeration.** `grep -rn "is_scanner_fail2banned"` returns exactly 6 lines: the definition (`crates/common/src/network/security.rs:261`) and the five call sites listed in Section 2.2. There is no sixth entry point into scan banning, and no other function references `KV_RATE_LIMIT_SCAN` outside the definition, the fail2ban check, and counter maintenance (`crates/services/src/task_manager/tasks/maintenance.rs:21, 388` — scheduled counter cleanup).

**Method 3 — entry-path reads.** The TCP accept path (`crates/common/src/network/listen.rs:270-316`), the listener bind path (lines 66-90), the concurrency limiter (`crates/common/src/network/limiter.rs:97-126`), the TLS handshake path (lines 503-512), and the read/timeout/disconnect loops of all five protocol servers were read in full; none contains per-IP connection state, port tracking, or scanner logic beyond the sites documented above.

**Residual uncertainty (LOW risk):** none identified for v1.0.1. The verdict table's "NOTHING" entries are statements about code that was read in full, not about code that was merely not found.

## 7. Sources

Repository (all paths relative to `C:\Users\Ibrahim\.zcode\tmp\stalwart-analysis\stalwart`, commit `23b69755cb3cd8ecd952fe0443c5ac067a506184`, tag `v1.0.1`):

- `CHANGELOG.md:1549`
- `crates/common/src/lib.rs:147`
- `crates/common/src/config/smtp/queue.rs:451-469`
- `crates/common/src/config/mailstore/imap.rs:18, 37-38`
- `crates/common/src/network/security.rs:157-168, 181-193, 217-225, 228-334, 371-391, 455`
- `crates/common/src/network/limiter.rs:97-126`
- `crates/common/src/network/listen.rs:45, 66-90, 270-316, 503-512`
- `crates/common/src/config/server/listener.rs:216`
- `crates/smtp/src/inbound/session.rs:272-308, 309-373, 503`
- `crates/smtp/src/inbound/spawn.rs:144-145, 218-226, 231-245`
- `crates/smtp/src/core/params.rs:22-24`
- `crates/imap/src/core/client.rs:67-94`
- `crates/imap/src/core/session.rs:55-81`
- `crates/pop3/src/client.rs:66-87`
- `crates/pop3/src/session.rs:71-97`
- `crates/managesieve/src/core/client.rs:96-123`
- `crates/managesieve/src/core/session.rs:69-73`
- `crates/http/src/request.rs:846-868, 1036-1060`
- `crates/http/src/form/mod.rs:68`
- `crates/store/src/dispatch/lookup.rs:225-252`
- `crates/registry/src/schema/enums.rs:259-269, 1521-1531`
- `crates/registry/src/schema/structs_impl.rs:58373-58418, 67663, 38435`
- `crates/services/src/task_manager/tasks/maintenance.rs:21, 388`

Methodology: full-file reads of every cited path; `grep -rn` sweeps for `is_scanner_fail2banned`, `KV_RATE_LIMIT_SCAN`, `BlockReason::PortScanning`, `scan_ban_rate`, `scan_ban_period`, `max_connections`, `timeout_unauth`, `HandshakeError`, and the ten absence-sweep terms listed in Section 6, executed across all 1,717 repository files at commit `23b69755`.
