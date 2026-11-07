# Message Lifecycle — the 14 Flows

**Status:** Accepted (2026-11-07) · Companion to [OPTION-B-ARCHITECTURE.md](OPTION-B-ARCHITECTURE.md). Each flow names its trigger, path, durable artifacts, and truth events. Non-negotiable #3 (delivered = remote 2xx / provider truth only) and #4 (durable path before any action) hold in every flow.

## Outbound flows

**F1 — API transactional send (relay mode, existing)**
API → validate → eligibility/suppression → claim CAS (R-26) → build + sign (R-02) → relay session (R-22) → `email_events{sent, delivered|bounced}`. Truth: relay 2xx per recipient / provider events (R-04).

**F2 — API transactional send (direct mode, Phase 1)**
API → …claim → mode lookup (verified domain + direct) → MX resolve (cache) → per-MX ESMTP attempt (STARTTLS-enforced, budgeted) → `mta_delivery_attempts` row per attempt → 2xx = `delivered`; 4xx transient → re-enqueue (R-27 bounds); 5xx permanent → R-05 classify → suppression chain. Truth: MX 2xx only.

**F3 — Campaign batch send**
Campaign start claim → recipient batches (timezone-aware R-09) → per-recipient F1/F2 path (A/B winner logic R-08 gates variant release). Truth: per-recipient as F1/F2.

**F4 — Scheduled send**
`scheduled_at` row → campaign-scheduler cron (R-01) atomic claim → F3; per-message `send_at` = same at message granularity. Restart-safe (claim + rollback verified by R-01 tests).

**F5 — SMTP submission (587, Phase 1)**
Client EHLO/AUTH (app-password/relay creds; STARTTLS enforced) → MAIL/RCPT policy-checked (authenticated tenant, verified sender domain, recipient eligibility) → DATA → durable ingest → same pipeline as F1/F2. Truth: identical to F1/F2; acceptance response = queue entry (never "delivered").

**F6 — Automation email node**
Trigger/condition (R-13/R-14) → enroll → email node → F1 path with automation provenance stamp. Truth: F1.

**F7 — Webhook notification (existing, unchanged)**
Delivery-status transitions → HMAC-signed webhook endpoints → attempts recorded → replay (R-06) available. SSRF guard applies at delivery.

## Inbound flows

**F8 — Inbound reception (archive)**
Remote MTA → listener (STARTTLS; security engine gates) → durable ingest → domain/route check → SPF/DKIM/DMARC verify (Phase-3 engines) → spam/malware disposition (ADR-006) → store/archive + thread route. Truth: `email_events`/inbound records; no outbound side effects.

**F9 — Inbound → forward**
F8 → route action=forward (confirmed target) → system `email` row (system_forward provenance) → F1/F2 delivery → forwarding result annotated on the inbound record; original never deleted on forward failure.

**F10 — Inbound → webhook**
F8 → route action=webhook → HMAC POST (parsed metadata/records; raw content optional) → retries via webhook machinery.

**F11 — Inbound → quarantine → release/delete**
F8 → disposition quarantine → `inbound_quarantine` + event → admin/user review → release (re-run routing from F8's post-ingest point) or delete (permanent, audited).

**F12 — Bounce (DSN) processing**
Remote MTA → receiver → VERP local-part decode → durable ingest → RFC 3464 DSN parse → match original message by encoded UUID → `email_events{bounced}` (class from R-05) → suppression chain for permanent classes. Idempotent per DSN record.

## Mailbox flows

**F13 — Mailbox delivery**
F8/F9 → mailbox routing (per-user inbox) → mailstore ingest (MIME parse → blobs content-addressed + encrypted ADR-003) → UID assign → `mail_events{deliver}` → IMAP IDLE notify (ADR-004) → spam disposition may pre-empt to Junk/Quarantine per ADR-006.

**F14 — Mailbox access & mutation**
IMAP/POP3/webmail session (app-password auth, TLS, limits) → mailstore ops (fetch/search/store/copy/move/expunge/append) → `mail_events` per mutation → webmail REST mirrors the same ops; webmail compose exits via F1/F2 with delivery-status displayed from `email_events`.

## Invariants asserted by tests (per flow, named in the parity register)

- No flow reports `delivered` on enqueue (F1–F7).
- No flow acts before persistence (F8–F13).
- No flow skips tenant re-derivation (all).
- Every flow's failure path leaves the durable artifacts consistent (attempts/records written, claims released, nothing half-mutated).
