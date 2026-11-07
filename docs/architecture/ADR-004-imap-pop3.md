# ADR-004 — IMAP and POP3 Access

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
