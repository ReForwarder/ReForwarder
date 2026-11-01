# the reference platform Issue #820 & Commit 581533b — Source-Verified Analysis for the ReForwarder Security Engine

| | |
|---|---|
| **Series** | Companion to Reports A–G (`docs/security-research/`) — this report satisfies the "Mandatory the reference platform Source-Code Reference Research" deliverable that gates the ReForwarder port-scan-detection implementation. |
| **Upstream refs** | [Issue #820](https://github.com/reference-platformlabs/reference-platform/issues/820) · [Commit 581533b](https://github.com/reference-platformlabs/reference-platform/commit/581533b09c0b1ca4faadaa0eb9d521de8553f3fa) |
| **Sources inspected** | Issue page (GitHub), commit page (GitHub), **and the full commit diff from the locally pinned clone** (`git show 581533b09c0b1ca4faadaa0eb9d521de8553f3fa`, 14 files, +303/−62) at `C:\Users\Ibrahim\.zcode\tmp\reference-platform-analysis\reference-platform`, plus the v1.0.1 evolution (`23b69755`) from Reports A–G. |
| **Date** | 2026-11-02 |
| **Confidence** | HIGH — every claim below was read directly from the commit diff and/or the pinned v1.0.1 source. |

---

## Contents

1. [What issue #820 actually discusses](#1-what-issue-820-actually-discusses)
2. [What commit 581533b actually changes](#2-what-commit-581533b-actually-changes)
3. [Relationship between issue and commit](#3-relationship-between-issue-and-commit)
4. [Exact the reference platform source files and functions](#4-exact-reference-platform-source-files-and-functions)
5. [The detection-to-ban execution flow](#5-the-detection-to-ban-execution-flow)
6. [the reference platform → ReForwarder mapping table](#6-reference-platform--reforwarder-mapping-table)
7. [What ReForwarder must implement independently](#7-what-reforwarder-must-implement-independently)
8. [License note](#8-license-note)

---

## 1. What issue #820 actually discusses

**Title:** `[enhancement]: Let Fail2Ban ban port scanners` — opened by **nomadturk**, Sep 29, 2024; closed as completed by maintainer **mdecimus** on Oct 8, 2024.

The reporter's real-world scenario: a single IP (`15.204.47.3`) was probing multiple the reference platform services — HTTPS, POP3S, ManageSieve — with malformed requests. Debug logs showed the server correctly parsing and rejecting the garbage per protocol, but nothing aggregated the failures, so the IP could probe forever. The request: since the reference platform already parses these protocol errors, **automatically add such IPs to the blocked list** (or make them bannable via Fail2Ban across a cluster).

Discussion points (verbatim themes):

- Contributor **williamdes** suggested an external CrowdSec integration pushing banned-IP lists via an API.
- **nomadturk** described their existing host-level setup: a bash script aggregating blocklists (Emerging Threats, FireHOL, Blocklist.de, Tor exit nodes) into kernel blackhole routes — and its drawbacks: high CPU updating ~30k entries one-by-one, silent drops with no logs, false positives.
- A nice-to-have floated: importing external IP lists into the reference platform directly.

**What the issue is NOT:** it does not request TCP-level scan detection (SYN sweeps, port-touch tracking, connect-and-abort counting). The "port scanners" in the title means *clients whose protocol behavior is abusive* — malformed requests against live services. That framing matters: the eventual implementation addresses exactly this scenario, nothing broader.

## 2. What commit 581533b actually changes

**Message:** `Detect and ban port scanners as well as other forms of abuse (closes #820)` — author mdecimus, Tue Oct 8 2024 11:48:03 +0200, parent `1561a60`. **14 files changed, +303/−62**, all modifications (no new files).

| File | Change |
|---|---|
| `crates/common/src/listener/blocked.rs` | Core: adds `http_banned_paths: Vec<MatchType>` (defaults: `*.php*`, `*.cgi*`, `*.asp*`, `*/wp-*`, `*/php*`, `*/cgi-bin*`, `*xmlrpc*`, `*../*`, `*/..*`, `*joomla*`, `*wordpress*`, `*drupal*`) and `scanner_fail_rate: Option<Rate>` (config `server.fail2ban.scanner`, **default 30/1d**) to the `Security` struct. New `Server` methods: `is_scanner_fail2banned(ip)` (counter key `h:{ip}`), `is_http_banned_path(path, ip)` (glob match → immediate `block_ip`), and `is_rcpt_fail2banned` **extended to dual keys** (`r:{ip}` + `r:{rcpt}` — either exceeding bans). |
| `crates/common/src/manager/config.rs` | `MatchType` made public/`Clone`, plus public `parse()`/`matches()` (glob support reused for banned paths). |
| `crates/imap/src/core/client.rs` (+29) | On a PARSE error while `State::NotAuthenticated` → `is_scanner_fail2banned(remote_addr)`; on ban: emit `SecurityEvent::ScanBan` (reason `"Invalid IMAP command"`), `SessionResult::Close`. |
| `crates/smtp/src/inbound/session.rs` (+28) | On `Error::UnknownCommand \| Error::InvalidResponse` **pre-auth**: `is_scanner_fail2banned(self.data.remote_ip)`; on ban: `ScanBan` event (reason `"Invalid SMTP command"`) and tear the session down with no response. |
| `crates/pop3/src/client.rs` (+23), `crates/managesieve/src/core/client.rs` (+30) | Analogous pre-auth parse-error scanner checks + `ScanBan`. |
| `crates/jmap/src/api/http.rs` (+72/−20) | HTTP: "dangerous URLs" blocked via `is_http_banned_path`; hyper **parse errors** trigger a scanner ban check (gated on the peer being the real client IP); ban errors map to a too-many-auth-attempts (429) response. |
| `crates/smtp/src/inbound/rcpt.rs` (+31/−8) | `rcpt_error` now also calls the dual-keyed `is_rcpt_fail2banned(ip, rcpt)` → `AbuseBan`. |
| `crates/trc/*` (+23/−4) | New `SecurityEvent::{ScanBan, AbuseBan}` variants + descriptions; `BruteForceBan` metric replaced by the two new ban metrics in `telemetry/metrics/store.rs`. |
| `crates/common/src/manager/config.rs`, `tests/src/jmap/enterprise.rs` | Config plumbing + test updates. |

Verified directly from the diff (SMTP trigger, quoted):

```rust
Error::UnknownCommand | Error::InvalidResponse { .. } => {
    // Check for port scanners
    if !self.is_authenticated() {
        match self.server.is_scanner_fail2banned(self.data.remote_ip).await {
            Ok(true) => {
                trc::event!(Security(SecurityEvent::ScanBan),
                    SpanId = self.data.session_id,
                    RemoteIp = self.data.remote_ip,
                    Reason = "Invalid SMTP command");
                return Err(());
            }
            ...
```

## 3. Relationship between issue and commit — verified

**They are the same feature, and it is not what the commit title implies.** The issue's scenario (one IP, malformed requests across multiple services) is implemented as a **per-IP fail2ban counter over pre-auth protocol parse failures** — with these properties verified in the diff and still true at v1.0.1 (`crates/common/src/network/security.rs:261-268`):

- The counter key is **IP-only** (`h:{ip}` at 581533b; `KV_RATE_LIMIT_SCAN ‖ ip_to_bytes(ip)` at v1.0.1). There is **no port dimension** — but because every protocol server calls the same check, failure counts **pool across services**, which is precisely what the issue asked for.
- **No TCP-level scan detection exists anywhere in the commit**: no port-touch tracking, no per-IP connection-rate, no connect-and-abort counting, no observation of unbound-port probes. Report B's exhaustive sweep at v1.0.1 confirmed none was ever added later.
- `is_http_banned_path` bans **instantly** (no counter) — the only "single-strike" path.
- Bans default to permanent (`block_ip` with no configured ban period) and are enforced at accept time for all protocols (v1.0.1: `listen.rs:277`) plus per-request for HTTP (`request.rs:945`).

**Consequence for ReForwarder (per the implementation prompt):** the actual TCP port-scan detection (multi-port sweeps, connect bursts, incomplete handshakes, connect-aborts) is **new engineering** — ReForwarder builds it fresh. What the reference platform contributes is the proven surrounding architecture: pooled pre-auth parse-error fail2ban, banned-path glob banning, allowlist-wins, ban persistence + propagation, and the event model.

## 4. Exact the reference platform source files and functions

| Mechanism | @ 581533b (Oct 2024) | @ v1.0.1 (pinned 23b69755) |
|---|---|---|
| Scanner fail2ban | `crates/common/src/listener/blocked.rs` — `Server::is_scanner_fail2banned` (new) | `crates/common/src/network/security.rs:261-268`; key `KV_RATE_LIMIT_SCAN` (`common/src/lib.rs:147`) |
| HTTP banned paths | `blocked.rs` — `Server::is_http_banned_path` (new) + 12 glob defaults | `security.rs:270-280`; trigger `crates/http/src/request.rs:846-868` |
| RCPT fail2ban (dual-key) | `blocked.rs` — `is_rcpt_fail2banned(ip, rcpt)` (extended) | `security.rs:229-259`; trigger `crates/smtp/src/inbound/rcpt.rs:414` |
| Auth fail2ban | pre-existing (`blocked.rs` `is_auth_fail2banned`) | `security.rs:336-369` (IP + blake3(login), 100/day) |
| Ban write | `block_ip` (pre-existing) | `security.rs:371-435` — in-memory `RwLock<BlockedIps>` + persistent `BlockedIp` registry object + cluster broadcast |
| Enforcement | per-protocol session checks (this commit) + accept-time list check | `crates/common/src/network/listen.rs:277-285` (accept, all protocols), `http/src/request.rs:945-957` (per-request) |
| Events | `trc::SecurityEvent::{ScanBan, AbuseBan}` (new in this commit) | `enums.rs` — 9 `SecurityEvent` variants; Prometheus `reference-platform_security_scan_ban_total` etc. |

## 5. The detection-to-ban execution flow (verified, v1.0.1 numbering)

```
TCP accept (listen.rs:135/189) → PROXY unwrap if trusted → canonicalize v4-mapped-v6 (listen.rs:266-273)
  → is_ip_blocked (security.rs:441: blocked && !allowed)  ──hit──► SecurityEvent::IpBlocked → silent drop
  → ConcurrencyLimiter (listen.rs:286)                    ──full─► drop
  → per-protocol session
SMTP:  greeting → command loop → UnknownCommand pre-auth (session.rs:272-308)
         → is_scanner_fail2banned(ip)   [INCR h/ip, 30/1d] ──breach──► ban_and_reset (security.rs:316-334)
              → block_ip (memory + BlockedIp registry + cluster broadcast) → counters reset → session torn down
IMAP/POP3/Sieve: PARSE error pre-auth (client.rs:67-94 etc.) → same path
HTTP:  request (request.rs:885+) → resolve client IP → is_ip_blocked per request → 403
         → 404 fallback: is_http_banned_path (globs)      ──match─► block_ip immediately
         → hyper parse error (peer-IP source) → scanner counter
Any auth failure (all protocols → authentication.rs:96-121) → is_auth_fail2banned (IP + login keys, 100/day)
```

## 6. the reference platform → ReForwarder mapping table

| the reference platform reference | What it does (verified) | ReForwarder equivalent | Recommended adaptation |
|---|---|---|---|
| `is_scanner_fail2banned` (`security.rs:261-268`, origin 581533b) | Fixed-window per-IP counter on pre-auth parse failures, pooled cross-protocol, 30/day → ban | New `internal/services/security` detector, `MALFORMED_PROTOCOL_ABUSE` signal (Redis sliding window) | **Adopt** as one weighted signal (weight 15), pooled across SMTP+HTTP; threshold configurable |
| `is_http_banned_path` (`security.rs:270-280`) | 12 glob patterns on HTTP paths → instant ban | HTTP middleware path-recon recorder → `HTTP_RECONNAISSANCE` signal | **Adopt** globs + add traversal/CMS patterns; instant-ban only on high-confidence hits |
| `is_auth_fail2banned` (`security.rs:336-369`) | Dual-key (IP + hash(login)) auth-failure fail2ban, 100/day, all protocols via one funnel | `feed.go` consumer of existing `user.login_failed` events + new relay AUTH failure events → `AUTH_BRUTE_FORCE` | **Adopt dual-key design verbatim** (IP key + SHA-256(login) key), mapped onto the existing event bus |
| `block_ip` (`security.rs:371-435`) | Ban write: in-memory list + persistent object + cluster broadcast; upsert keeps longer expiry | `bansvc.go` + `snapshot.go` + `propagate.go` (Postgres + COW snapshot + Redis pub/sub) | **Adopt** the three-layer model; **fix** the admin-CRUD propagation gap (single-writer service, live propagation) |
| `BlockedIps` list (`security.rs:69-74`) | Exact-IP set + CIDR vec, lazy TTL expiry, allowlist-wins | `snapshot.go` (`netip` map + `[]netip.Prefix`) | **Adopt** shape; `netip.Addr.Unmap()` for v4-mapped canonicalization |
| Accept-time + per-request enforcement (`listen.rs:277`, `request.rs:945`) | Block check before greeting (all protocols) + per-request HTTP re-check (403) | SMTP `NewSession` hook → 421; HTTP global middleware → 403 | **Adopt** both points; 421 for SMTP pre-greeting, 403 problem JSON for API paths |
| `SecurityEvent::{ScanBan, AbuseBan, AuthenticationBan, IpBlocked}` | Typed security events → logs/metrics/webhooks | `security_events` table + `EventBus` + admin SSE stream | **Adopt** taxonomy (ReForwarder reason codes) with structured evidence JSON the reference platform lacks |
| the reference platform's permanent-by-default ban periods | `*BanPeriod: None → u64::MAX` | Escalation ladder 1h→24h→7d→30d, permanent = manual only | **Correct** — documented self-DoS risk |
| **(absent)** TCP scan detection | — | `detector.go` signals: distinct ports, connection bursts, connect-aborts, incomplete handshakes, TLS probing | **Design fresh** (spec §2A–C) |
| **(absent)** Admin UI for bans/evidence | webadmin lists BlockedIp objects only | Vue Security section (5 views) | **Design fresh** (spec §7) |

## 7. What ReForwarder must implement independently

1. **TCP port-scan detection** (the prompt's core requirement): per-IP distinct-port tracking, connection-rate anomalies, connect-and-abort counting, incomplete-handshake/TLS-probe counting — none of this exists in any the reference platform version.
2. **Weighted risk scoring** combining multiple signals before banning (the reference platform bans on single counters).
3. **Evidence model** (structured JSON: ports touched, counters, window, score) persisted with each ban — the reference platform stores only reason + timestamps.
4. **Escalation ladders** (persistent offense counts surviving ban expiry) — the reference platform resets counters on ban and has no escalation.
5. **Finite default ban TTLs** — the reference platform defaults to permanent.
6. **Admin CRUD with live propagation** — fixes the reference platform's `ReloadBlockedIps` footgun (admin writes don't propagate without an explicit action).
7. **Full security admin UI** — the reference platform's OSS webadmin has no dedicated security dashboard with evidence/detection-event views.
8. **Audit/history persistence** — the reference platform OSS is streams-only.

## 8. License note

Commit 581533b and its v1.0.1 descendants carry per-file SPDX `AGPL-3.0-only OR LicenseRef-SEL` (verified in Report E, §7). The AGPL side is compatible with ReForwarder's AGPL-3.0-or-later, but this implementation is a **clean-room Go design adoption** (Rust→Go transcription is impractical and copyleft-attracting); the reference platform is cited as design inspiration. No the reference platform code is copied.
