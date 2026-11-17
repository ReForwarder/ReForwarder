# ADR-001 — Outbound MTA Foundation (Direct-to-MX Delivery)

> **📄 Status (2026-11-14):** 📌 **Accepted ADR** (2026-11-07) — binding decision record; supersedes the relay-only sending posture. Landed as Option B Phase 1 (OB-1).
> **Live status:** [docs/PROJECT_STATUS_DASHBOARD.md](../PROJECT_STATUS_DASHBOARD.md) · [docs/README.md](../README.md)

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

## Amendment (2026-11-15, SF-50): per-domain transport policy rules

The bounded transport-policy slice extends Decision 8's per-domain sending policy with condition-driven overrides, evaluated at the MTA path's two gate points and riding the unified condition evaluator (SF-103's `internal/services/conditions` package).

**Data.** One additive table `transport_policies`: workspace-owned rows bound to a sending domain NAME (`workspace_id` NOT NULL, `domain_name`), each with a `priority` (lower evaluated first), an `enabled` flag, a condition set (`models.FilterRules` JSON over the envelope-fact whitelist), and the override fields the matching policy applies (all optional, zero value = untouched): `reject` + `reject_reason` (the RCPT gate refuses the recipient), `bind_ip` (the outbound source address), `msg_rate_per_min` (pacing override), `max_message_size` (size-cap override), `hourly_budget` (budget override).

**Evaluation points.**
- RCPT (the gated submission listener): after authentication and the verified-sender-domain gate, the session's envelope facts (`mail_from`, `rcpt_to`, `helo`, `authenticated_as`) are evaluated against the sender domain's enabled policies. A matching policy with `reject` answers 550 5.7.1 with the policy's reason. An evaluator or store ERROR answers 451 4.3.1 (temporary failure) — fail-closed: the client retries rather than slipping past a gate that might have rejected it.
- Delivery (the direct-to-MX executor): the same resolution runs per recipient-domain group with the delivery-time facts (`mail_from`, `rcpt_to`, `sender_domain`, `message_size`). A matching policy's overrides merge into the resolved per-domain policy for that delivery — the override can only TIGHTEN the baseline (rate/size/budget) or pin the source IP. An evaluator or store ERROR defers the message with a transient failure (retry with the existing backoff) rather than sending unpoliced — fail-closed.

**Precedence.** Policies are ordered by `priority` ASC, then id ASC; the FIRST matching enabled policy wins and later policies do not stack. An empty condition set matches (a policy with no conditions always applies) — the operator's priority ordering is the disambiguator.

**Condition grammar.** The transport currency is the fourth currency of the unified evaluator: strict — an unknown field or operator, or a non-numeric value for a numeric comparison, is an ERROR (fail-closed), unlike the marketing currencies where unknown rules never match. Field whitelist: `mail_from`, `rcpt_to`, `helo`, `authenticated_as`, `sender_domain`, `message_size`. Operators: `equals`, `not_equals`, `contains`, `starts_with`, `ends_with` (case-insensitive on the normalized values), plus `greater_than`/`less_than` (numeric; a non-parsable side is an error).

**Out of scope (the bounded slice's boundary, recorded rather than silently dropped):** the IfBlock outcomes that need their own plumbing — smarthost rerouting (needs the smarthost credential surface), header rewriting, signer selection, TLS strategy, report addresses, spam actions. The admin surface is the workspace-scoped domain-policy REST surface (the tenant administrator's gate), not a platform-admin console.

## Amendment (2026-11-16, SF-12): sender rewrite scheme (SRS) for forwarded mail

The bounded sender-rewrite scheme is adopted for the forward-as-own-domain forwarding model (ADR-002 decision 2's system_forward copies). VERP (decision 4) remains the bounce-address mechanism for tenant-originated mail; SRS covers the forwarded path's specific requirement — the envelope sender of a forwarded copy must be an address on the forwarder's own domain that, when a bounce arrives, identifies the ORIGINAL inbound message the copy was forwarded from.

**Format** (keyed by the existing bounce secret — POSTA_RETURN_PATH_SECRET; no new key):

    srs0+<payload32hex>=<sig16hex>@<forwarder-domain>

- The payload is the INBOUND record's UUID without hyphens (32 lowercase hex chars) — the forwarded original the copy was made from.
- The signature is the first 16 hex chars (64 bits) of HMAC-SHA256(secret, "srs0+" + payload + "@" + host) — the full address is signed, so neither the payload nor the host can be swapped. Forgeries fail the HMAC.
- The local part stays within RFC 5321's 64-octet limit (6 + 32 + 1 + 16 = 55) and is all lowercase.

**Encode point**: the forwarding executor stamps the encoded return path onto the system_forward request at enqueue time (internal/services/inbound/forwarder.go — the encode is derived from the durable inbound record's UUID and the task's verified forwarder domain, never client input).

**Return path**: the bounce ingestion's return-path allowlist accepts a validly signed srs0+ address on a tenant-verified forwarder domain (same gate shape as the VERP bounce host — forged addresses fall through to ordinary recipient handling and are rejected); the RFC 3464 match-back resolves the copy through its persisted SRS return path (an additive email row column, exact match) and writes the DSN truth to the forwarded copy. An unresolvable decode is logged and dropped — the inbound bounce record remains.

**Delivery**: the delivery-time envelope seam prefers the copy's persisted SRS return path over the VERP computation (forwarded mail's MAIL FROM is the SRS address; tenant-originated mail keeps VERP unchanged).

**Out of scope (recorded)**: full RFC-compliant SRS with timestamp/nonce aging and the SRS1 hop-by-hop form — the bounded scheme covers the forwarder's own single-hop rewrite; address-aging revocation rides the existing bounce-secret rotation.

## Amendment (2026-11-17, SF-01): VRFY/EXPN disposition — no fork of the SMTP parser

**Decision**: the ESMTP receivers keep the SMTP parser dependency's own VRFY/EXPN
answers; no vendor fork or `replace` directive is adopted. VRFY stays `252 2.5.0
Cannot VRFY user, but will accept message` and EXPN stays `502 5.5.1 EXPN command
not implemented`, both unconditional and state-independent, exactly as the
dependency's command dispatcher answers them.

**Verified dependency posture (module source, this session)**: every released
version answers identically — `go-smtp@v0.25.0/conn.go:129-130` (VRFY hardcoded
`252` with no session or backend hook; `conn.go:110-112` (the
`SEND`/`SOML`/`SAML`/`EXPN`/`HELP`/`TURN` case refused `502 5.5.1` in every
state), `go-smtp@v0.25.1/conn.go:129-130` (identical), and
`go-smtp@v0.26.0/conn.go:129-130` (identical — the latest upstream release,
fetched and inspected this session; `go list -m -versions` enumerated
v0.9.0…v0.26.0). No upstream version offers a VRFY hook, so a hook is
permanently unavailable without a fork.

**Why not the fork (option (a))**: (1) the parser is the internet-facing attack
surface consumed by four receivers in this tree (inbound SMTP, LMTP, the MSA and
the outbound client) — a frozen fork stops receiving upstream security fixes
within months, the highest-maintenance posture possible for the one dependency
that must never drift; (2) a local `replace` fork drops the module's `go.sum`
provenance and permanently complicates every future dependency upgrade;
(3) the benefit is a VRFY existence oracle — and RFC 5321 §7.3 directs that a
site disabling these commands for security reasons "MUST return a 252 response,
rather than a code that could be confused with successful or unsuccessful
verification", and warns that "an implementation that 'supports' VRFY by always
returning 550 whether or not the address is valid is equally not in
conformance" — so the fork's deliverable is either an RFC-discouraged
always-550 oracle or a per-address verification oracle the anti-harvesting
posture has no use for; (4) ReForwarder's inbound model deliberately defers
address-existence answers to the RCPT/DATA route evaluation behind the route
table and the security engine's own protections, not to a pre-M_MAIL probe.

**Why 252 is the compliant answer (option (b), taken)**: RFC 5321 §3.5.3 — "A
server MUST NOT return a 250 code in response to a VRFY or EXPN command unless
it has actually verified the address" and, for the MX/relay case where an
address cannot reasonably be verified in real time, "In these situations, reply
code 252 SHOULD be returned"; RFC 5321 §7.3 makes the disabled-for-security 252
a MUST. The dependency's hardcoded answer is exactly the RFC-mandated posture
for a receiver that does not operate a directory-verification service, so the
record's remaining VRFY scope closes as a deliberate, documented, compliant
posture — not a gap.

**EXPN**: the session API offers no hook for a custom answer (same posture as
VRFY — the dispatcher handles these commands before any backend involvement),
and none is needed: the dependency's unconditional `502 5.5.1` refusal in every
state IS the disabled-by-default explicit refusal the record's scope asks for.
Pinned by a wire-level test through the production receiver
(`internal/services/inbound/vrfy_expn_test.go`), not by convention.

**Consequences**: VRFY probing returns no existence information on any of this
tree's receivers; directory-harvest resistance stands; no fork maintenance
burden; an operator wanting per-address VRFY answers must revisit this decision
record — the revisit inherits the same trade-off analysis (and RFC 5321 §3.5.3
notes implementations returning 500/502 for VRFY are not in full compliance —
252 is the compliant disabled form, which is what the receivers answer).
