# ADR-001 — Outbound MTA Foundation (Direct-to-MX Delivery)

- **Status:** Accepted (2026-11-07 — authorized per the Option B programme decision; see `docs/product-roadmap/upcomming-convo.md` §7 and `todo.md` Track A Phase 1)
- **Phase:** Option B Phase 1 · **Supersedes:** the relay-only sending posture recorded in `docs/research/reforwarder-reference-platform-roadmap.md` (do-not-implement table) — superseded by `todo.md` §0.1
- **Covers (parity register):** SF-01, SF-02, SF-03 (SMTP MSA), SF-05, SF-06, SF-12, SF-15*, SF-17, SF-21, SF-29, SF-30 + MTA-side parts of SF-07/09/16/35/43/108; CA-04

## Context

ReForwarder sends all mail through user-configured trusted relays (`internal/services/email/smtp_sender.go`), with DKIM signing (R-02), timeouts/deadlines (R-22), atomic claims (R-26), and provider-event delivery truth (R-04). The Option B decision expands ReForwarder into complete email infrastructure, which requires **direct-to-MX delivery**: resolving the recipient domain's MX hosts and delivering over ESMTP with TLS, retries, per-domain policy, and delivery-attempt records — while the relay path remains supported (many operators will still relay through SES/Postal/etc.).

Non-negotiables (from the Option B decision, binding on this ADR):
1. Never an open relay — direct delivery is attempted only for messages created by authenticated tenants whose **sender domain is verified**; the MTA never accepts third-party submissions for arbitrary destinations.
2. Never trust a client-supplied recipient domain — the delivering worker re-derives the recipient domain from the validated recipient address at send time.
3. Never report `delivered` on queue acceptance — `delivered` is recorded only from an MX 2xx response (or provider-event truth), never from enqueueing.

## Decision

1. **The MTA is an in-process service of the existing monolith/worker split** — `internal/services/mta`. Delivery executes inside asynq worker tasks (the `email:send` path already claims rows atomically, R-26); the control plane (API + dashboard) is unchanged. No separate MTA daemon, no new process type.
2. **Delivery mode is per-domain configuration**: `relay` (today's behavior, the default) or `direct` (this ADR). A tenant flips the mode per sending domain after verifying it. Rollout is safe: default unchanged.
3. **MX resolution** with `net.LookupMX` + TTL cache: preference-ordered with shuffle within equal preference, null-MX (RFC 7505) falls back to A/AAAA, implicit MX when no MX exists. DNSSEC validation is not adopted (no validating resolver dependency; recorded limitation, revisit with ADR-007).
4. **Outbound ESMTP client** extends the existing `dialSMTP` path (R-22 dial timeout + session budget): EHLO capability negotiation (PIPELINING, 8BITMIME, SMTPUTF8 advertised per message content), STARTTLS **mandatory when offered** (with certificate verification; plain only when the remote offers no STARTTLS and the domain policy allows), AUTH only against configured smarthosts (never to MX), and per-command deadlines.
5. **Delivery attempts are recorded** in a new additive table `mta_delivery_attempts` (email_id, attempt, mx_host, ip, response_code, response_text, started_at, finished_at, outcome) — the durable transport-event substrate for SF-13 traceability, SF-56 tracing, and SF-52 diagnostics.
6. **Transient vs permanent** classification reuses the R-05 classifier on the remote server's response codes/text; transient failures re-enqueue with the existing backoff; permanent failures feed the existing bounce/suppression chain unchanged.
7. **Source-IP binding**: per-domain outbound bind address from configuration (single IP now; pools arrive in Phase 6 per ADR-007/009). PTR/rDNS **guidance and verification** for the bound IP are surfaced in domain health (SF-32 partial; full iprev validation at OB-2).
8. **Per-domain sending policy**: max connections, max messages/connection, per-hour budget, honor-421/450 throttle handling (defer + jitter). Connection reuse within one worker task's batch; no long-lived connection pool (bounded sessions per task — memory backpressure per SF-108's Phase 3 slice).
9. **Envelope rewriting (SF-12)**: the worker rewrites `MAIL FROM` to the verified return-path (domain-scoped bounce address; VERP encoding arrives with ADR-002's return-path processing) and `RCPT TO` from the validated recipients list.
10. **Submission (SF-03) stays HTTP-first**: the existing API remains the MSA; an SMTP submission listener (port 587, AUTH against user/relay credentials, policy-checked) is added as a **gated listener** in this phase for standards-compliant clients, reusing the same pipeline (claim, sign, deliver, record).

## Alternatives considered and rejected

- **Separate MTA daemon** (e.g. a standalone `postad` process): rejected — two deployables, a second config surface, and cross-process claiming for no benefit at this scale; the worker split already isolates long-running delivery from request handling.
- **Embedding/adopting an existing MTA** (the reference platform, Postal, maddy): rejected — AGPL/clean-room constraints on copying behavior into this codebase, plus a foreign config model; the go-smtp client + stdlib `net` covers the protocol surface with existing in-repo patterns (R-22 harness).
- **Replacing asynq with a bespoke MTA queue**: rejected — the queue manager (SF-08) already has persistence, claiming, backoff, and admin UI (R-03); direct delivery is a new task executor, not a new queue.

## Consequences

- New package `internal/services/mta` (resolver, session runner, policy) + additive migration for `mta_delivery_attempts` + per-domain delivery-mode config + gated submission listener.
- New tests: in-process SMTP server harness asserting EHLO negotiation, STARTTLS enforcement, 421/450 throttle deferral, permanent/permanent classification, exactly-once claims under concurrency (extends R-26), null-MX/implicit-MX resolution, and the non-relay guarantees (a direct attempt for an unverified sender domain must fail closed).
- The relay path, SSRF guard, suppression/eligibility layers, and provider-event truth are untouched (preservation list honored); `delivered` semantics unchanged (MX 2xx or provider event, never enqueue).
- Deprecates nothing; relay remains the default mode.
