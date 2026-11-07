# ADR-002 — Inbound Mail & Routing (Aliases, Forwarding, Quarantine, Return-Path)

- **Status:** Accepted (2026-11-07 — Option B programme authorization)
- **Phase:** Option B Phase 2 · **Covers (parity register):** SF-11, SF-14 (local delivery), SF-26, SF-27, SF-32, SF-45/48 (policy hooks), SF-51 (quarantine), CA-05 (VERP/return-path)

## Context

ReForwarder already runs a feature-gated inbound SMTP receiver (`internal/services/inbound/`, go-smtp server): durable ingest, domain-verified RCPT validation, thread routing, DMARC aggregate-report detection, attachment pipeline, and the security engine's SMTP enforcement (silent drops, EHLO 421). What it lacks is a **routing engine**: aliases, catch-all, forwarding to external/internal destinations, quarantine with review, and bounce/return-path processing. ADR-001 adds outbound direct delivery, which makes **return-path (bounce) handling** a first-class requirement — bounces must be accepted, parsed (RFC 3464 DSNs), and matched back to the original message for delivery truth.

Non-negotiables binding this ADR: never accept inbound without a durable path (the message is persisted before any routing action); never trust a client-supplied recipient domain (routing rules resolve only through tenant-verified domains); suppression/unsubscribe/consent layers remain untouched — forwarding is not a marketing-send path.

## Decision

1. **Routing rules are data, evaluated at RCPT/DATA**: new additive table `inbound_routes` (id, workspace_id, domain_id, pattern [exact address | `@domain` catch-all | sub-address spec], action, target, priority, enabled). Actions: `deliver` (store for the domain's inbound archive/thread view — today's behavior), `forward` (re-send to a target address through the outbound pipeline), `webhook` (POST the parsed message to a registered endpoint — the existing HMAC webhook machinery), `quarantine` (hold for review). First-match-wins by priority; unmatched recipients on a verified domain follow the domain's catch-all rule or default-reject.
2. **Forwarding re-enters the outbound pipeline as a system message**: the forwarded copy is enqueued as an `email` row with `system_forward` provenance, signed per ADR-001/`domain_signing_keys`, recorded in `email_events`, and retried with the standard backoff. The original stays durably stored regardless of forward outcome. Forward targets must be confirmed (single-use confirmation token) before a rule using them activates — no open forwarding to arbitrary addresses.
3. **Quarantine (SF-51)**: new `inbound_quarantine` table (message ref, reason, score, received_at, status). Quarantined messages are viewable/releasable/deletable in the dashboard's Inbound area. Release re-runs routing; deletion is permanent. Retention per ADR-008.
4. **Return-path & VERP (CA-05)**: outbound messages gain a per-domain bounce address (`bounce@<return-path subdomain>` with VERP-encoded local part `bounce+<message-uuid>@…`), accepted by the receiver via a dedicated RCPT allowlist (no global inbound default change), parsed as RFC 3464 DSNs (SF-10), and matched back to the original message → `email_events` gets a real DSN-derived bounce truth; permanent classes feed suppression as today.
5. **Policy hooks (SF-45/48 partial)**: rule evaluation and quarantine disposition expose the same condition evaluator used by automations (SF-50/103 currency) — no Sieve interpreter is adopted (documented alternative stands; ADR-006 notes the Phase 6 revisit).
6. **ARC verification (SF-27)** and **iprev validation (SF-32)** are added at this phase as optional, config-gated evaluation steps that annotate (never reject by default) — results land in Authentication-Results (SF-49's Phase 3 slice) and the trace record.

## Alternatives considered and rejected

- **Separate routing daemon**: rejected — the receiver is already an in-process listener with durable ingest; a second process would duplicate the persistence boundary the non-negotiable requires.
- **Forwarding as pure proxy** (RCPT-time relay without storage): rejected — violates "never accept inbound without a durable path".
- **Full mailing-list expansion engine (SF-11 list expansion)**: deferred to Phase 6 convergence (marketing lists already exist as a separate feature); this ADR covers address-level forwarding only.

## Consequences

- Additive migrations: `inbound_routes`, `inbound_quarantine`; return-path subdomain config; VERP parsing on the receiver; DSN parser (`internal/services/inbound/dsn.go`).
- Tests: routing priority/catch-all/sub-address matrix; forward-confirm gate; quarantine release/deletion; DSN parse→event match (idempotent); durable-path proof (forward never happens before persistence); unverified-domain fail-closed.
- Dashboard: Inbound area gains Routes + Quarantine views (Phase 2 frontend).
