# ADR-004 — IMAP and POP3 Access

> **📄 Status (2026-11-14):** 📌 **Accepted ADR** (2026-11-07) — binding decision record. Landed as Option B Phase 4 (OB-4); JMAP gated on demand (EVALUATIONS.md).
> **Live status:** [docs/PROJECT_STATUS_DASHBOARD.md](../PROJECT_STATUS_DASHBOARD.md) · [docs/README.md](../README.md)

- **Status:** Accepted (2026-11-07 — Option B programme authorization)
- **Phase:** Option B Phase 4 · **Covers (parity register):** SF-84 (JMAP revisit at OB-6), SF-85 (IMAP4rev1), SF-86 (POP3 + ManageSieve), SF-47 (ManageSieve — stays with ADR-006, out of this ADR)

## Context

Mailboxes (ADR-003) need standard protocol access. The Option B decision names IMAP and POP3 as Phase 4 deliverables with "standard-client + protocol compatibility testing", auth tied to ReForwarder identities, connection limits, TLS enforcement, rate limits, and audit logs. JMAP is **not** this phase: the decision record keeps JMAP as a Phase 6 revisit on validated demand (RF-B126); the REST API (SF-87) remains the primary machine interface until then.

## Decision

1. **In-house IMAP4rev1 server** (`internal/services/imap`) over the mailstore (ADR-003). Rationale: no maintained pure-Go IMAP *server* library meets the dependency bar; the protocol subset needed is small and testable. Command set at Phase 4: CAPABILITY, NOOP, LOGOUT, AUTHENTICATE PLAIN (+LOGIN), STARTTLS, SELECT/EXAMINE, CREATE/DELETE/RENAME/SUBSCRIBE/UNSUBSCRIBE, LIST, STATUS, APPEND, CHECK, CLOSE, EXPUNGE, SEARCH (keys: ALL/ANSWERED/BCC/BEFORE/BODY/CC/DELETED/DRAFT/FLAGGED/FROM/HEADER/KEYWORD/LARGER/NEW/NOT/OLD/ON/OR/RECENT/SEEN/SENTBEFORE/SENTON/SENTSINCE/SINCE/SMALLER/SUBJECT/TEXT/TO/UID/UNANSWERED/UNDELETED/UNFLAGGED/UNSEEN/KEYWORD), FETCH (full/macros + BODY[...] section fetch, BODY.PEEK), STORE (+FLAGS/−FLAGS), COPY/MOVE, UID variants, IDLE. IMAP4rev2 extensions are not adopted at Phase 4 (recorded limitation).
2. **POP3 server** (`internal/services/pop3`) with the classic command set (USER/PASS/AUTH, STAT, LIST, UIDL, RETR, TOP, DELE, RSET, NOOP, QUIT, CAPA, STLS) against the inbox folder; deletion semantics are POP3-correct (mark-then-expunge on QUIT) and do **not** corrupt IMAP UIDs (pop3 session works on a shadow view; expunge maps to mailstore flag sweeps).
3. **Authentication tied to ReForwarder identities**: IMAP/POP3 authenticate with the user's email + an **app password** (SF-35/67 pairing — dedicated long-lived random secrets, bcrypt-hashed, scoped to mail protocols, revocable from Profile.vue). Primary account passwords are **not** accepted (keeps WebAuthn/2FA posture intact; `two_factor_required` semantics preserved — app passwords are the deliberate mail-protocol exception, audited).
4. **TLS enforcement**: STARTTLS required by default (config can allow cleartext only on loopback for tests); implicit-TLS ports (993/995) served with per-domain certificates from ADR-007's cert lifecycle (SNI map).
5. **Connection limits + rate limits**: per-user concurrent-session cap, per-IP connection budget, command-rate limiting — implemented on the same limiter substrate as the security engine (SF-43 slice); abusive sessions feed the security engine's event taxonomy (SF-44).
6. **Audit logs**: AUTH success/failure, SELECT/EXPUNGE/APPEND/STORE actions land in the events substrate with user + IP (SF-74 continuation).
7. **IDLE**: implemented with mailstore change notifications (in-process bus; fan-out via Redis when workers are remote — same propagation pattern as ban events).

## Alternatives considered and rejected

- **JMAP instead of IMAP at Phase 4**: rejected — the decision record sequences IMAP/POP3 at Phase 4 and defers JMAP; client ecosystem reality (Thunderbird/Apple Mail/Outlook) requires IMAP.
- **Wrapping an IMAP server in a sidecar container** (e.g. Dovecot against a MySQL/PG backend): rejected — a second auth universe, config drift, and a non-Go operational surface; the mailstore service interface is small enough to serve directly.
- **CGO-backed libraries**: rejected — the build is CGO_ENABLED=0 (Dockerfile); pure-Go only.

## Consequences

- New packages `internal/services/imap`, `internal/services/pop3`; additive config (ports, TLS, limits, enabled gates); app-password model + Profile UI.
- Protocol-compatibility test harness: scripted client sessions against the in-process servers (the R-22 SMTP-harness pattern) covering the command matrix above, plus interop smoke tests against real clients where available in CI.
- compose override gains the new listeners (gated off by default; the four non-negotiables apply — no anonymous access, durable path via the mailstore).

## Amendment (2026-11-16, SF-86): the ManageSieve listener (RFC 5804)

The bounded ManageSieve server is adopted as a gated listener over the SF-14 filter-script engine (`internal/services/filterengine/` — the ADR-006 amendment already adopted the bounded engine; this amendment adopts the RFC 5804 LISTENER that exposes it, extending ADR-004's listener set: POP3/IMAP + ManageSieve):

- **Command set (RFC 5804, bounded)**: CAPABILITY, STARTTLS, AUTHENTICATE "PLAIN", HAVESPACE, PUTSCRIPT, LISTSCRIPTS, SETACTIVE, GETSCRIPT, DELETESCRIPT, CHECKSCRIPT, NOOP, UNAUTHENTICATE, LOGOUT. The server speaks the RFC 5804 wire format (quoted strings + `{n}`/`{n+}` literals) but the SCRIPT BODIES are the bounded engine's serialized documents (JSON `{"conditions": [...], "actions": [...]}` validated through the SAME strict evaluator the delivery engine runs) — not RFC 5228 text. A body outside the bounded grammar is refused at PUTSCRIPT/CHECKSCRIPT with the validation error (NO).
- **Auth**: app-password-only via the mailauth seam (ADR-004 decision 3) — the primary password is structurally never consulted. Cleartext credentials are confined exactly like the POP3 listener: accepted only when the plaintext listener serves no TLS at all (loopback-confined config + runtime peer check); with a certificate, credentials are accepted only after STARTTLS.
- **TLS**: STARTTLS on the gated port (RFC 5804's primary mode); the implicit-TLS second listener rides the ADR-004 pattern when configured.
- **Activation semantics**: PUTSCRIPT stores INACTIVE; SETACTIVE activates one script per mailbox and deactivates the others (RFC 5804's single-active-script semantics mapped onto the filter engine's enabled flag — the delivery evaluation runs enabled scripts in priority order, first match wins); SETACTIVE with an empty name deactivates all; DELETESCRIPT of the ACTIVE script is refused (NO "active script").
- **Account scoping**: a ManageSieve session operates on the authenticated user's PRIMARY mailbox (the first mailbox by id ASC) — bounded, documented.
- **Gating**: the listener registers only when `POSTA_MANAGESIEVE_ENABLED=true` (default off); port `POSTA_MANAGESIEVE_PORT` (default 4190).
