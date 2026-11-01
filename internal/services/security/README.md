# ReForwarder Security Engine

Automatic threat detection and IP blocking for ReForwarder's exposed services
(HTTP API, inbound SMTP receiver, SMTP relay). A source is detected by
behavioral signals, risk-scored, and automatically banned with a persisted,
explainable evidence record; every subsequent connection from a banned source
is rejected at the earliest possible point.

Design adopted (clean-room, Go) from Stalwart Mail Server's fail2ban/blocked-IP
architecture — see `docs/security-research/` (Reports A–G and the
issue-820/commit-581533b analysis) for the full source-verified investigation.

## Layout

| File | Responsibility |
|---|---|
| `engine.go` | `Engine` wiring: snapshot + propagator + BanService + EventRecorder + Detector + Feed. Constructed in `cmd/posta/server.go` when `POSTA_SECURITY_ENABLED=true`; nil (fully inert) otherwise. |
| `signals.go` | Signal + reason-code vocabulary (e.g. `PORT_SCAN_DETECTED`, `AUTH_BRUTE_FORCE`). |
| `rules.go` | Thresholds, windows, weights, escalation tiers — all admin-mutable, read through `settings.Provider` (30 s cache, changes apply without restart). |
| `detector.go` | Redis sliding-window counters (`sec:sig:<signal>:<ip>`) + weighted risk scoring + ban trigger. Fail-open on Redis errors by default. |
| `bansvc.go` | Single writer for bans/allowlist: Postgres persistence, in-memory snapshot update, Redis pub/sub propagation, offense escalation. |
| `snapshot.go` | Lock-free in-memory blocklist/allowlist (exact + CIDR, v4-mapped-IPv6 canonicalized). The enforcement hot path never touches Redis or Postgres. |
| `propagate.go` | Redis pub/sub (`posta:sec:bans`) + periodic reconciliation; all replicas converge even after message loss or reconnects. |
| `feed.go` | Consumes `user.login_failed` events from the event bus into the `AUTH_BRUTE_FORCE` signal. |
| `events.go` | Structured `security_events` rows (evidence JSON, severity, action). |
| `listener.go` | `CountingListener` (accept-time allow/block/connect-count + zero-byte abort detection) and the eager-TLS handshake listener for future implicit-TLS modes. |
| `middleware.go` | Global HTTP middleware: ban check (403), connect + suspicious-path recording, per-request re-check on keep-alive connections. |
| `smtp.go` | `SessionGuard`: secondary ban check at first EHLO (421), command tracking, zero-command abort detection, relay AUTH-failure feed. |

## Detection signals (defaults — all configurable in Admin → Security → Rules)

| Signal | Threshold | Window | Weight |
|---|---|---|---|
| Distinct exposed ports touched | 5 | 60 s | 35 |
| New connections | 30 | 60 s | 20 |
| Incomplete/TLS handshake failures | 10 | 60 s | 20 |
| Connect-and-abort (closed with zero bytes) | 12 | 60 s | 15 |
| Malformed pre-auth protocol activity | 15 | 5 min | 15 |
| Suspicious HTTP paths (probe globs + 404s) | 5 | 60 s | 25 |
| Auth failures (per source IP **and** per targeted login) | 10 | 5 min | 20 |

A ban triggers when the summed weight of exceeded signals reaches the risk
threshold (default **40**), or when any single signal reaches
**2×** its threshold (configurable). Escalation by offense count:
1 h → 24 h → 7 d → 30 d. Automatic bans are never permanent; permanent bans
are an explicit admin action. Allowlisted sources (loopback is always
allowlisted) bypass every check.

## Enforcement points

1. SMTP accept (`CountingListener`): allowlist pass → ban = immediate silent
   close → connect counted. Zero-byte closes feed the abort signal.
2. SMTP first EHLO (`SessionGuard`): ban → `421 4.7.1`.
3. HTTP every request (`Engine.Middleware`): ban → 403; suspicious-path and
   404 probing recorded.
4. Relay AUTH failures and dashboard login failures feed the auth signal
   (dual-keyed: source IP and targeted login are tracked independently).

## Configuration

Environment (master switches, `internal/config`):

| Variable | Default | Meaning |
|---|---|---|
| `POSTA_SECURITY_ENABLED` | `true` | Construct the engine and enforce everywhere. |
| `POSTA_SECURITY_FAIL_OPEN` | `true` | On Redis errors, detection pauses instead of failing requests. Ban enforcement is unaffected (in-memory snapshot). |

Everything else is admin-mutable (seeded into the `settings` table, editable
under **Admin → Security → Rules**): `security_risk_threshold`,
`security_instant_ban_ratio`, per-signal `security_sig_<name>_threshold` /
`_window_seconds`, per-signal `security_weight_<name>`,
`security_ban_tier_1_hours` … `_tier_4_hours`,
`security_events_retention_days` (cron-cleans `security_events`).

## Known limitations (honest scope)

- **Application-level only.** A scan of a host port with no ReForwarder
  listener is refused by the OS before this engine can observe it. Host-level
  coverage requires an OS firewall integration (deliberately out of scope).
- HTTP keep-alive connections count as one connect per request, not one per
  TCP connection (okapi does not expose the accept path).
- STARTTLS handshake failures on the SMTP listeners are not observable in
  go-smtp v0.25.0 (no hook); only implicit-TLS handshake failures count.
- `TLSServerListener` (eager handshake, TLS-fail counting) is implemented but
  only wired when an implicit-TLS listener mode is used; both current SMTP
  TLS modes are `none|starttls`.
