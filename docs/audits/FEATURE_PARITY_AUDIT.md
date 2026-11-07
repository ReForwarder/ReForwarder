# the reference platform Feature Parity Audit — Master Register

**Audit:** the reference platform 115-feature parity programme · **Started:** 2026-11-07 · **Baseline HEAD:** `482d10b` ·
**Sources:** [REFERENCE-PLATFORM_SOURCE_REGISTER.md](REFERENCE-PLATFORM_SOURCE_REGISTER.md) · **Status vocabulary:** `COMPLETE` · `PARTIAL` · `MISSING` · `BROKEN` · `PLANNED (ADR-nnn accepted · Option B Phase n)` — the ADR gate passed 2026-11-07; no record remains merely blocked · `IN PROGRESS`.

## Method and honesty rules

1. This register is the **definitive, feature-by-feature verification ledger** for every feature in the the reference platform research universe (SF-01..SF-115) plus the ReForwarder-side requirements unique to the capability audit (appendix CA records). The original inventory IDs are permanent and preserved.
2. **Pre-seed notice:** records below are seeded from the authoritative ledger (`reference-platform-feature-gap-analysis.md` §4a) and the R-01..R-28 delivery evidence (2026-11-07 acceptance audit). Every record is re-verified against the actual code in verification waves before its Final status becomes final; Evidence fields cite `path:line` or a command + output. A pre-seed status marked *(verify)* is provisional.
3. Per the programme decision (2026-11-07): **no documented feature is classified N/A merely because the current relay architecture does not support it.** The mail-server surface carries `BLOCKED BY ADR-nnn` with the governing ADR and the assigned Option B phase (per `todo.md` Track A / `upcomming-convo.md` §7) and is implemented in sequence after the ADR gate (the gate passed 2026-11-07: all nine ADRs are Accepted, so these records read PLANNED with their ADR + phase). "Still dead" items from the do-not-implement table carry their documented lightweight alternative and are classified from code evidence.
4. Status fields per record: Backend / Frontend / Database / API / Security / Tests / Documentation, then the Final status. `—` = not applicable to that feature's shape.
5. preserved protections: the SSRF guard (`internal/services/webhook/ssrf.go`, `proxydial.go`), suppression/consent/eligibility layers, webhook authorization gates, and the security engine are never weakened by any implementation under this register.

## Cross-walks

### SF ↔ R (delivered)

| R-ID | Delivers (SF refs) |
|---|---|
| R-01 | SF-09 (scheduled send), SF-104 (scheduler analog) |
| R-02 | SF-20 (DKIM1 signing), SF-62 (key lifecycle incl. retire) |
| R-03 | SF-52 (inspection/cancel/run/archive), SF-54 (partial), SF-59 (partial) |
| R-04 | SF-10 (partial: delivery-status events, not RFC 3464 DSNs), SF-25 (receive-side truth) |
| R-05 | SF-53 (14-class taxonomy) |
| R-06 | SF-52 (replay) |
| R-07 | SF-63 (diagnose), SF-64 (health — improves on the reference platform's negative) |
| R-08/R-09/R-14/R-21 | product-side (no SF ref) |
| R-10 | SF-69 (2FA + recovery, beyond the reference platform) |
| R-11 | SF-82 (backup/restore automation) |
| R-12 | SF-93 (families) |
| R-13 | SF-104 (cron triggers) |
| R-15 | SF-88/SF-91 (per-key limits at the lookup-store seam) |
| R-17 | SF-25 (aggregate ingest), SF-57 (read surface) |
| R-18 | SF-92 (per-key usage) |
| R-19 | SF-69 (WebAuthn — beyond the reference platform) |
| R-20 | SF-70 (impersonation) |
| R-22/R-23/R-24/R-25/R-26/R-27/R-28 | ReForwarder-side hardening (no SF ref; R-26 ≈ SF-106 event-lock analog, R-27 ≈ SF-108 analog) |

### SF ↔ Option B phase (the ADR-blocked surface)

| Phase | SF features | Governing ADRs |
|---|---|---|
| OB-1 Outbound MTA foundation | SF-01, 02, 03, 05, 06, 12, 15, 17, 21, 29, 30 + MTA-side parts of SF-07/09/16/35/43/108 | ADR-001 |
| OB-2 Inbound mail & routing | SF-11, 14 + SF-45/48 (Sieve), SF-51 (quarantine), SF-32 (iprev), SF-27 (ARC) | ADR-002 |
| OB-3 Mailbox & storage | SF-18, 51?, 75/79/80/83 (storage/search/quotas/compression depth), SF-112 | ADR-003 |
| OB-4 IMAP/POP3 | SF-84, 85, 86, 47 | ADR-004 |
| OB-5 Webmail | SF-84 (JMAP revisit), CA-01 (compose) | ADR-005 |
| OB-6 Convergence | SF-19, 28, 31, 34, 39–42, 61, 66, 71, 72, 96, 105, 106, 113 | ADR-006/007/008/009 |

---

## Category A — Email protocols & mail delivery (SF-01..SF-21)

### SF-01 — Full ESMTP/LMTP state machine
| Field | Value |
|---|---|
| Source | inventory §A SF-01 · ledger N/A |
| Requirement | First-party ESMTP/LMTP command state machine (EHLO/MAIL/RCPT/DATA/RSET/NOOP/VRFY/EXPNS/STARTTLS/AUTH) |
| Implementation | Relay model: outbound via trusted relays (`internal/services/email/smtp_sender.go`); an inbound SMTP receiver exists feature-gated (`internal/services/inbound/`) |
| Status fields | Backend: Missing (MTA) · Frontend: — · DB: — · API: — · Security: — · Tests: Missing · Docs: Missing |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-02 — ESMTP extension advertisement
| Field | Value |
|---|---|
| Source | inventory §A SF-02 · ledger N/A |
| Requirement | PIPELINING/CHUNKING/8BITMIME/SMTPUTF8/DSN/REQUIRETLS/FUTURERELEASE advertisement |
| Implementation | go-smtp receiver advertises its defaults; outbound client negotiates none (relay protocol only) |
| Status fields | Backend: Missing · Frontend: — · DB: — · API: — · Security: — · Tests: Missing · Docs: Missing |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-03 — Submission (MSA) with delivery-status writeback
| Field | Value |
|---|---|
| Source | inventory §A SF-03 · ledger N/A |
| Requirement | Authenticated submission port with status writeback |
| Implementation | HTTP API send (`POST /api/v1/emails/send|batch|send-template`) with event writeback (`email_events`) is the submission analog; SMTP MSA absent |
| Status fields | Backend: Partial (HTTP analog) · Frontend: — · DB: Verified (events) · API: Verified · Security: Verified (auth) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — SMTP MSA deferred to ADR-001 (OB-1); HTTP analog COMPLETE |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-04 — Relay control / open-relay prevention
| Field | Value |
|---|---|
| Source | inventory §A SF-04 · ledger N/A |
| Requirement | Authenticated relaying only; no open relay |
| Implementation | Relay credentials bcrypt + constant-time (R-25) + IP allowlists; security engine SMTP/HTTP enforcement; inbound receiver domain-verified |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: Verified (smtp-credentials CRUD/revoke, workspace_resource_routes.go:219-267) · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — residual hardening recorded: `middlewares.IPAllowed` and the `ErrAuthRequired` auth gate have zero direct test coverage (V-A1); **PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-05 — Routing strategies (Local / Mx / Relay via expressions)
| Field | Value |
|---|---|
| Source | inventory §A SF-05 · ledger N/A |
| Requirement | Per-domain routing strategy selection |
| Implementation | Relay-only routing (per-domain SMTP server config); Local/Mx absent |
| Status fields | Backend: Missing · Frontend: Verified (client+router+views fully wired, V-A1) · DB: Verified · API: Verified · Security: — · Tests: Partial · Docs: Partial |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** — relay half COMPLETE |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-06 — MX handling (preference, shuffle, null-MX, implicit MX, DNSSEC)
| Field | Value |
|---|---|
| Source | inventory §A SF-06 · ledger N/A |
| Requirement | MX resolution behaviors for direct delivery |
| Implementation | absent (no direct-to-MX) |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-07 — Outbound relays (SASL, TLS modes, source-IP pools, pipelining)
| Field | Value |
|---|---|
| Source | inventory §A SF-07 · ledger PART |
| Requirement | Configurable outbound relays with auth, TLS modes, source-IP pools, pipelining |
| Implementation | SMTP server config + relay creds (R-25) + dial timeout/deadlines (R-22); source-IP pools absent |
| Status fields | Backend: Partial · Frontend: Verified (server config) · DB: Verified · API: Verified · Security: Verified (bcrypt) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — pools PLANNED under ADR-001 (per ADR-001 covers: source-IP pools land with OB-1 binding + OB-6 pool expansion; V-A1 re-gated the remainder to OB-1) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-08 — Persistent multi-queue manager (per-recipient retry/notify schedules, expiry, event-locked delivery)
| Field | Value |
|---|---|
| Source | inventory §A SF-08 · ledger PART |
| Requirement | Durable queues with per-recipient schedules, expiry, event-locked delivery |
| Implementation | asynq persistent queues + Redis; atomic claims CAS + SKIP LOCKED (R-26); bounded retries (R-27); per-recipient notify schedules and message expiry absent |
| Status fields | Backend: Partial · Frontend: Partial (R-03 admin) · DB: Verified · API: Partial · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — remaining (retry/notify schedules, expiry) **PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-09 — Future release (HOLDFOR/HOLDUNTIL), DELIVERBY, MT-PRIORITY, DSN params
| Field | Value |
|---|---|
| Source | inventory §A SF-09 · ledger PART |
| Requirement | Deferred delivery at message level |
| Implementation | scheduled campaigns (R-01) + API `scheduled_at` = HOLDFOR/UNTIL analog at application level |
| Status fields | Backend: Verified (app-level) · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **PARTIAL** — SMTP-protocol params deferred to ADR-001 (OB-1) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-10 — Full DSN generation (RFC 3464), double-bounce, signed DSNs
| Field | Value |
|---|---|
| Source | inventory §A SF-10 · ledger N/I |
| Requirement | Standards-compliant delivery-status notifications |
| Implementation | provider-event bounce records + `email_events` exist; RFC 3464 DSN message generation absent (R-04 delivered status truth, not DSNs) |
| Status fields | Backend: Missing · Frontend: — · DB: Partial · API: — · Security: — · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-11 — Forwarding chain (sub-addressing, catch-all, aliases, list expansion)
| Field | Value |
|---|---|
| Source | inventory §A SF-11 · ledger N/V — **resolved**: no forwarding/auto-forwarding exists in ReForwarder (confirmed by prior audits); inbound aliases/forwarding/catch-all are Option B Phase 2 scope |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-002 accepted · OB-2)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-12 — Envelope rewriting (MAIL FROM / RCPT TO)
| Field | Value |
|---|---|
| Source | inventory §A SF-12 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-13 — Traceability (Received headers, queue ids, wire logging)
| Field | Value |
|---|---|
| Source | inventory §A SF-13 · ledger PART |
| Implementation | `email_events` delivery timeline + attempt history + external message IDs; RFC 5322 Received-header injection and per-hop queue ids absent |
| Status fields | Backend: Partial · Frontend: Verified (Logs) · DB: Verified · API: Verified · Security: — · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-14 — Local delivery engine (per-recipient Sieve at ingest)
| Field | Value |
|---|---|
| Source | inventory §A SF-14 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-002/003 accepted · OB-2/3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-15 — Milter client
| Field | Value |
|---|---|
| Source | inventory §A SF-15 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** — documented alternative: automations + HMAC webhooks (do-not-implement table) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-16 — MTA hooks (per-SMTP-stage first-party HTTP hooks, HMAC-signed)
| Field | Value |
|---|---|
| Source | inventory §A SF-16 · ledger PART |
| Implementation | HMAC-signed outbound webhooks with retries/replay (SF-90 machinery) + automation triggers on business events; per-SMTP-stage (EHLO/MAIL/RCPT) hooks absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (HMAC) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — SMTP-stage hooks deferred to ADR-001 (OB-1/2) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-17 — Queue quota reservations
| Field | Value |
|---|---|
| Source | inventory §A SF-17 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-18 — Queue blob encryption [ENT]
| Field | Value |
|---|---|
| Source | inventory §A SF-18 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-003 accepted · OB-3)** — note: credentials/keys are AES-256-GCM encrypted today (`internal/services/crypto`) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-19 — Tenant-scoped queues [ENT]
| Field | Value |
|---|---|
| Source | inventory §A SF-19 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-008 accepted · OB-6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-20 — DKIM signing at queue time (DKIM1 RSA/Ed25519 + DKIM2 chained)
| Field | Value |
|---|---|
| Source | inventory §A SF-20 · ledger N/I |
| Implementation | R-02: go-msgauth DKIM1 signing (RSA-2048 + Ed25519-SHA256) hooked into the delivery path, encrypted keys, DNS TXT display/verify; DKIM2 chained absent |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (encrypted keys) · Tests: Verified (golden-message + independent verifier) · Docs: Verified |
| Final status | **COMPLETE** — DKIM2 recorded as a limitation under ADR-007 (OB-6) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-21 — MT-PRIORITY ingress + virtual-queue ordering
| Field | Value |
|---|---|
| Source | inventory §A SF-21 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

## Category B — Email security & authentication (SF-22..SF-45)

### SF-22 — SPF verification (EHLO + MAIL FROM, strict modes, Received-SPF)
| Field | Value |
|---|---|
| Source | inventory §B SF-22 · ledger N/I |
| Requirement | Message-level SPF evaluation on inbound mail |
| Implementation | DNS **presence** checks only (domain verification + R-07 health); no message-level SPF engine |
| Status fields | Backend: Missing · Frontend: — · DB: Missing · API: — · Security: — · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** — relay-compatible: evaluate on the feature-gated inbound receiver |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-23 — DKIM verification (insecure-sig stripping, strict, ARF)
| Field | Value |
|---|---|
| Source | inventory §B SF-23 · ledger N/I |
| Implementation | absent; `emersion/go-msgauth` already in the module provides `dkim.Verify` |
| Status fields | Backend: Missing · others: — |
| Final status | **MISSING → PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-24 — DMARC verification with policy enforcement
| Field | Value |
|---|---|
| Source | inventory §B SF-24 · ledger N/I |
| Implementation | DMARC DNS-record checks (R-07) + aggregate ingestion (R-17); message-level DMARC evaluation/enforcement absent |
| Status fields | Backend: Missing · others: — |
| Final status | **MISSING → PLANNED (Phase 3)** — depends on SF-22 + SF-23 |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-25 — DMARC aggregate reports: send AND receive/ingest
| Field | Value |
|---|---|
| Source | inventory §B SF-25 · ledger N/I |
| Implementation | **Receive/ingest: COMPLETE** (R-17: RFC 7489 parser, gzip, 16 MiB decompression cap, idempotent storage, read surface `GET /domains/{id}/dmarc` + UI); **send: MISSING** (no aggregate-report generation/dispatch for our domains) |
| Status fields | Backend: Partial · Frontend: Verified (read) · DB: Verified · API: Verified (read) · Security: Verified (size caps) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — send side **PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-26 — SPF/DKIM auth-failure (ARF RFC 5965/6591) reports
| Field | Value |
|---|---|
| Source | inventory §B SF-26 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **MISSING → PLANNED (Phase 3)** — depends on SF-22/23 |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-27 — ARC verification (no sealing)
| Field | Value |
|---|---|
| Source | inventory §B SF-27 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-002 accepted · OB-2)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-28 — DKIM2 verification (chained) + forged-DSN protection
| Field | Value |
|---|---|
| Source | inventory §B SF-28 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-007 accepted · OB-6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-29 — MTA-STS client + local policy server
| Field | Value |
|---|---|
| Source | inventory §B SF-29 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001/007 accepted · OB-1/6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-30 — DANE (TLSA) with DNSSEC
| Field | Value |
|---|---|
| Source | inventory §B SF-30 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1/6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-31 — TLS-RPT (outbound build/send + inbound parse)
| Field | Value |
|---|---|
| Source | inventory §B SF-31 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-007 accepted · OB-6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-32 — Reverse DNS (iprev) validation
| Field | Value |
|---|---|
| Source | inventory §B SF-32 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-002 accepted · OB-1 PTR guidance, OB-2 iprev)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-33 — TLS server stack (STARTTLS, SNI wildcard fallback)
| Field | Value |
|---|---|
| Source | inventory §B SF-33 · ledger PART |
| Implementation | inbound SMTP STARTTLS via the go-smtp receiver; relay TLS per server config; SNI wildcard fallback + automated cert management absent (SF-34) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — SNI/ACME deferred to ADR-007/009 (OB-6) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-34 — ACME certificate automation
| Field | Value |
|---|---|
| Source | inventory §B SF-34 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-007/009 accepted · OB-6)** — documented alternative: ingress/proxy TLS (do-not-implement table) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-35 — SASL mechanisms (PLAIN, LOGIN, OAUTHBEARER, XOAUTH2, MFA, app passwords)
| Field | Value |
|---|---|
| Source | inventory §B SF-35 · ledger PART |
| Implementation | relay SASL is **PLAIN-only** (`internal/services/smtprelay/backend.go:103-109`, V-B2); LOGIN exists only as the outbound client mechanism (`smtp_sender.go:250-275`); bcrypt + constant-time + rehash (R-25) COMPLETE; OAUTHBEARER/XOAUTH2/MFA/app-passwords absent |
| Status fields | Backend: Partial · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — OAUTH*/MFA deferred to ADR-001 (OB-1); app passwords **PLANNED (Phase 3)** (pairs SF-67) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-36 — Password hashing (Argon2id/PBKDF2/scrypt/bcrypt/sha-crypt)
| Field | Value |
|---|---|
| Source | inventory §B SF-36 · ledger FULL |
| Implementation | bcrypt across user passwords, relay credentials (R-25), recovery codes; constant-time comparisons; rehash-on-success migration |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — single-algorithm (bcrypt) is the documented equivalent of the reference platform's multi-algorithm support; Phase 1 verifies no legacy unsalted rows remain |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-37 — OAuth 2.0/OIDC provider + directory backends (LDAP/SQL/OIDC)
| Field | Value |
|---|---|
| Source | inventory §B SF-37 · ledger FULL |
| Implementation | OAuth/OIDC as **consumer** (Google sign-in) COMPLETE; as **provider** (issuing tokens to external clients) MISSING; LDAP/SQL directory backends MISSING |
| Status fields | Backend: Partial · Frontend: Verified (login) · DB: Verified · API: Partial · Security: Verified · Tests: Missing (zero OAuth tests found, V-B2) · Docs: Partial |
| Final status | **PARTIAL** — provider + directory backends **PLANNED (Phase 3)** (control-plane feature; ledger's FULL claim to be re-derived in Phase 1) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-38 — Abuse prevention suite (fail2ban, greylisting, rate limiters, DNSBL)
| Field | Value |
|---|---|
| Source | inventory §B SF-38 · ledger FULL |
| Implementation | security engine: ZSET sliding-window detector, weighted risk, escalation ladder, BanService + COW snapshot + Redis propagation, SMTP silent-drop/EHLO-421/HTTP-403 enforcement, allowlist; Redis rate limiters (login + per-API-key R-15); DNSBL absent |
| Status fields | Backend: Verified · Frontend: Verified (Security admin) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — DNSBL recorded as a limitation → ADR-006 (OB-2) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-39 — Spam filter (~23 analyzers / ~200 tags)
| Field | Value |
|---|---|
| Source | inventory §B SF-39 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-40 — Statistical spam classifier (FTRL-Proximal)
| Field | Value |
|---|---|
| Source | inventory §B SF-40 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2/6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-41 — AI spam verdicts via LLM [ENT]
| Field | Value |
|---|---|
| Source | inventory §B SF-41 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-42 — Pyzor, spam traps, GTUBE, attachment blocklists, external rule feed
| Field | Value |
|---|---|
| Source | inventory §B SF-42 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-43 — Connection/DoS budgets (memory accounting, session reservations)
| Field | Value |
|---|---|
| Source | inventory §B SF-43 · ledger PART |
| Implementation | security-engine connection-abuse detection + HTTP 403 + SMTP silent drops + bounded retry sweeps (R-27); memory accounting/session reservations absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — memory accounting **PLANNED (Phase 3)** (bounded worker pools); MTA-scale budgets deferred to ADR-001 (OB-1) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-44 — Security event taxonomy (~40 families)
| Field | Value |
|---|---|
| Source | inventory §B SF-44 · ledger FULL |
| Implementation | `security_events` + shared `events` table + audit taxonomy across auth/security/admin actions, SSE streams, CSV export |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

### SF-45 — Sieve-driven policy at SMTP stages
| Field | Value |
|---|---|
| Source | inventory §B SF-45 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2)** — documented alternative: automation filter nodes |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. |

## Category C — Message filtering & policy (SF-46..SF-51)

### SF-46 — Sieve interpreter (RFC 5228 + extensions)
| Field | Value |
|---|---|
| Source | inventory §C SF-46 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2)** — documented alternative: automation engine + filter nodes (do-not-implement table) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-47 — ManageSieve (RFC 5804)
| Field | Value |
|---|---|
| Source | inventory §C SF-47 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-004 accepted · OB-4)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-48 — Per-account Sieve at final delivery
| Field | Value |
|---|---|
| Source | inventory §C SF-48 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2/5)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-49 — Header inspection/transformation (editheader, milter, hooks, A-R injection)
| Field | Value |
|---|---|
| Source | inventory §C SF-49 · ledger PART |
| Implementation | R-24 CR/LF/NUL rejection + last-guard `encodeHeader` sanitization COMPLETE; Authentication-Results injection absent (pairs SF-22/23/24) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Partial (no HTTP lookup backend; `RedisChallengeStore`/`RedisLimiter` covered only indirectly, V-H) · Docs: Partial |
| Final status | **PARTIAL** — A-R injection **PLANNED (Phase 3)**; editheader/milter PLANNED under ADR-006/001 |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-50 — Conditional routing/policy engine (IfBlock expression language)
| Field | Value |
|---|---|
| Source | inventory §C SF-50 · ledger PART |
| Implementation | FilterRules + SegmentCondition SQL builder + automation filter nodes incl. `in_segment` (R-14) COMPLETE for campaign/automation conditioning; transport-policy expressions absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (guarded SQL) · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — transport-policy expressions PLANNED under ADR-001; evaluator unification **PLANNED (Phase 3)** (pairs SF-103) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-51 — Quarantine (spam-percentage Junk filing)
| Field | Value |
|---|---|
| Source | inventory §C SF-51 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-002/003/006 accepted · OB-2/3)** — quarantine design co-owned by ADR-002 (`inbound_quarantine` designed, table not yet built; V-CD) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

## Category D — Queue & delivery administration (SF-52..SF-59)

### SF-52 — Queue inspection API (list/query/patch/destroy/cancel/requeue)
| Field | Value |
|---|---|
| Source | inventory §D SF-52 · ledger PART |
| Implementation | R-03 + R-06: per-state task lists, cancel/run/archive, stats, audited replay w/ 5/hour cap, endpoint-level tests (2026-11-07); per-message patch/destroy absent |
| Status fields | Backend: Partial · Frontend: Verified (Jobs.vue) · DB: Verified (asynq) · API: Verified · Security: Verified (admin) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — patch/destroy **PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-53 — SMTP response taxonomy surfaced to admins
| Field | Value |
|---|---|
| Source | inventory §D SF-53 · ledger PART |
| Implementation | R-05 14-class classifier (enhanced codes + provider reasons + diagnostics) recorded on failed events and provider events; per-message SMTP transcript/typed-error surfacing absent |
| Status fields | Backend: Partial · Frontend: Partial (classification via metadata JSON in Logs) · DB: Verified · API: Partial (`ClassificationCounts` tested but unwired — no production caller, service.go:417-429, V-CD) · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (transcript view; pairs GAP-20) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-54 — Global queue pause/resume
| Field | Value |
|---|---|
| Source | inventory §D SF-54 · ledger PART |
| Implementation | per-worker start/stop + automation pause/resume + campaign pause + a MaintenanceMode hot gate (`email/service.go:493`, V-CD); **global queue pause/resume + per-domain suspension** absent |
| Status fields | Backend: Missing (global) · Frontend: Partial · DB: — · API: Missing · Security: — · Tests: Missing · Docs: Missing |
| Final status | **PARTIAL — PLANNED (Phase 3)** (pairs GAP-48) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-55 — Live delivery diagnosis SSE [ENT]
| Field | Value |
|---|---|
| Source | inventory §D SF-55 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-56 — Delivery tracing (trace store + live tracing SSE)
| Field | Value |
|---|---|
| Source | inventory §D SF-56 · ledger PART |
| Implementation | `email_events` timeline + `webhook_delivery_attempts` + Logs MessageDetail COMPLETE (read-only tracing); live per-message trace SSE absent |
| Status fields | Backend: Partial · Frontend: Verified (Logs) · DB: Verified · API: Partial · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (SSE pattern already exists in-repo) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-57 — DMARC/TLS/ARF report admin objects
| Field | Value |
|---|---|
| Source | inventory §D SF-57 · ledger N/I |
| Implementation | DMARC aggregate objects + read surface COMPLETE (R-17); TLS-RPT/ARF objects absent |
| Status fields | Backend: Partial · Frontend: Verified (DMARC panel) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — TLS/ARF objects **PLANNED (Phase 3)** (with SF-26/31) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-58 — Virtual queues, named queues, delivery-schedule objects
| Field | Value |
|---|---|
| Source | inventory §D SF-58 · ledger FULL |
| Implementation | named asynq queues (default + retention/reconcile paths), per-queue stats, R-01/R-13 delivery schedules at application level |
| Status fields | Backend: Verified · Frontend: Partial · DB: Verified · API: Verified · Security: — · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-59 — Operational Action objects (Reload*, InvalidateCaches, Pause/Resume)
| Field | Value |
|---|---|
| Source | inventory §D SF-59 · ledger PART |
| Implementation | queue_reconcile cron + hot settings apply (30s TTL) + admin queue actions COMPLETE; explicit Reload*/InvalidateCaches action endpoints absent |
| Status fields | Backend: Partial · Frontend: Partial · DB: — · API: Partial · Security: Verified (admin) · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

## Category E — Domain management (SF-60..SF-64)

### SF-60 — Domain CRUD (aliases, catch-all, relay flag, DKIM/DNS switches)
| Field | Value |
|---|---|
| Source | inventory §E SF-60 · ledger FULL |
| Implementation | domain CRUD + ownership verification + DNS records display + DKIM signing lifecycle; alias/catch-all/relay-flag routing fields absent (inbound routing is OB-2) |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (ownership) · Tests: Partial (domain CRUD has no dedicated tests, V-EF) · Docs: Verified |
| Final status | **PARTIAL** — core COMPLETE; alias/catch-all/relay routing fields deferred to ADR-002 (OB-2) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-61 — Automatic DNS record publication (11 provider integrations)
| Field | Value |
|---|---|
| Source | inventory §E SF-61 · ledger PART |
| Implementation | DNS records displayed for manual publication + verification + re-check (R-07); provider integrations absent |
| Status fields | Backend: Missing (providers) · Frontend: Verified (manual flow) · DB: — · API: — · Security: — · Tests: — · Docs: Partial |
| Final status | **PARTIAL → PLANNED under ADR-007 (OB-6 DNS automation)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-62 — DKIM key lifecycle (rotation stages + scheduled rotation task)
| Field | Value |
|---|---|
| Source | inventory §E SF-62 · ledger N/I |
| Implementation | R-02 full lifecycle: generate (RSA-2048/Ed25519) → Active → Retiring → Retire → delete, verify-dns, encrypted private keys (2026-11-07 audit fixed the retire dead-end) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (encrypted keys) · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — scheduled auto-rotation task **PLANNED (Phase 3)** under SF-104 |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-63 — DMARC/SPF/DKIM report settings + TroubleshootDmarc diagnostic
| Field | Value |
|---|---|
| Source | inventory §E SF-63 · ledger N/I |
| Implementation | R-07 health + diagnose (live DNS re-check, persisted) + R-17 aggregate read surface; report-delivery settings (rua addresses per domain, intervals) absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Partial (session endpoints lack dedicated tests, V-EF) · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (settings with SF-25 send side) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-64 — Domain health endpoint
| Field | Value |
|---|---|
| Source | inventory §E SF-64 · ledger N/I (the reference platform negative — improves on it) |
| Implementation | R-07: `GET /domains/{id}/health` (SPF/DKIM/DMARC status, signing state, bounce/complaint rates) + `POST .../health/diagnose` + Domains.vue panel |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (ownsResource) · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

## Category F — Identity & account management (SF-65..SF-74)

### SF-65 — Accounts (User/Group) with credentials, aliases, quotas, locale
| Field | Value |
|---|---|
| Source | inventory §F SF-65 · ledger FULL |
| Implementation | users + roles + sessions + 2FA + recovery codes + passkeys + OAuth login + language preference; **groups/aliases absent** (no mail account semantics pre-OB) |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — account aliases/groups PLANNED under ADR-004 (OB-4, mail-account semantics) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-66 — Granular permissions (~660) + custom Roles
| Field | Value |
|---|---|
| Source | inventory §F SF-66 · ledger PART |
| Implementation | 4-role RBAC + scoped API keys + admin middleware COMPLETE; fine-grained permission registry + custom role composition absent |
| Status fields | Backend: Partial · Frontend: Verified (role-gated UI) · DB: Partial · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (OB-6, with ADR-008 tenancy decisions)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-67 — API keys + app passwords (permission narrowing, IP allowlists)
| Field | Value |
|---|---|
| Source | inventory §F SF-67 · ledger FULL |
| Implementation | API keys: scopes, IP allowlists, per-key rate limits (R-15), usage analytics (R-18), query-token scoping (R-28); **app passwords absent** |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Partial (IP allowlists untested, V-EF) · Docs: Verified |
| Final status | **PARTIAL** — app passwords **PLANNED (Phase 3)** (pairs SF-35); allowlist tests **PLANNED (Phase 3)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-68 — OAuth grant/session administration
| Field | Value |
|---|---|
| Source | inventory §F SF-68 · ledger FULL |
| Implementation | session/device management: list, revoke, bulk revoke, new-device alerts, impersonation session model (R-20); OAuth **grant** administration absent (ReForwarder is a consumer, not a provider — SF-37) |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — grant admin **PLANNED (Phase 3)** only if the OAuth-provider decision (SF-37) lands; otherwise superseded by the provider record |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-69 — 2FA: TOTP (+ recovery codes, + WebAuthn beyond the reference platform)
| Field | Value |
|---|---|
| Source | inventory §F SF-69 · ledger FULL |
| Implementation | TOTP + one-time recovery codes (R-10) + WebAuthn passkeys (R-19). Correction (V-EF): the pre-seed's "`two_factor_required` wiring (R-23)" was wrong — R-23 is `require_domain_verification`; **`two_factor_required` is a dead toggle** (seeded + admin UI, zero enforcement consumers) = GAP-12's other half |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-70 — Admin impersonation
| Field | Value |
|---|---|
| Source | inventory §F SF-70 · ledger N/I |
| Implementation | R-20: reauth + reason, `impersonated_by` claims, session revoke + Redis blacklist + cookie clear, audit events, banner UI |
| Status fields | Backend: Verified · Frontend: Verified (banner) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-71 — SCIM 2.0 provisioning [ENT]
| Field | Value |
|---|---|
| Source | inventory §F SF-71 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED under ADR-008 (OB-6 revisit)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-72 — Tenants (object + quotas + scoped admin views) [ENT]
| Field | Value |
|---|---|
| Source | inventory §F SF-72 · ledger FULL |
| Implementation | workspaces = tenant analog: scoped views, per-workspace isolation on every endpoint, admin scope; distinct tenant objects + tenant quotas absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — tenant objects/quotas deferred to ADR-008 (OB-6) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-73 — Account destruction task (staged, grantee cleanup)
| Field | Value |
|---|---|
| Source | inventory §F SF-73 · ledger FULL |
| Implementation | GDPR export + deletion flows; staged destruction task with grantee cleanup to be verified |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Missing (staged-destruction chain `internal/cron/jobs/account_cleanup.go` real+wired but zero tests, V-EF) · Docs: Partial |
| Final status | **PARTIAL** — Phase 1 verifies deletion coverage; residual requirements → PLANNED (Phase 3) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

### SF-74 — Admin audit history
| Field | Value |
|---|---|
| Source | inventory §F SF-74 · ledger FULL (the reference platform negative — ReForwarder improves) |
| Implementation | shared `events` table + audit logger across admin actions, SSE + CSV export, scope=deployment admin-only |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Partial (event endpoints lack dedicated tests, V-EF) · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. |

## Category G — Storage & message management (SF-75..SF-83)

### SF-75 — Three-plane storage (data + blob + lookup + search + registry)
| Field | Value |
|---|---|
| Source | inventory §G SF-75 · ledger PART |
| Implementation | data plane = Postgres; blob plane = `blob.Store` (FS/S3); lookup plane = Redis (limits, bans, challenges); search plane = SQL ILIKE (SF-79); registry = settings table |
| Status fields | Backend: Partial · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — five-plane map verified real (Postgres/blob FS+S3/Redis/ILIKE/settings, V-G); search depth **PLANNED (ADR-003 accepted · OB-3)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-76 — Data-store backends (RocksDB/SQLite/Postgres/MySQL/FDB/HTTP KV)
| Field | Value |
|---|---|
| Source | inventory §G SF-76 · ledger PART |
| Implementation | Postgres as the single data store (documented architecture decision — "managed Postgres HA" per the do-not-implement table); depth slices re-graded PLANNED now that ADR-009 is accepted (V-G) |
| Status fields | Backend: Partial (single backend by design) · others: — |
| Final status | **PARTIAL → PLANNED (ADR-009 accepted · OB-6)** — alternative backends under a future ADR decision |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-77 — Blob stores (FS/S3; dedup, ref-count, orphan GC)
| Field | Value |
|---|---|
| Source | inventory §G SF-77 · ledger FULL |
| Implementation | `blob.Store` FS + S3 with UUID-pathed keys (not content-addressed), no List op, **zero dedup/ref-count/orphan GC** (GAP-45 open; ledger's SF-77=FULL corrected to PARTIAL by V-G) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (GAP-45 usage reporting + orphan GC) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-78 — Blob at-rest encryption (zstd + AES-256-GCM/ChaCha20)
| Field | Value |
|---|---|
| Source | inventory §G SF-78 · ledger N/I |
| Implementation | AES-256-GCM encryption for credentials/signing keys (`internal/services/crypto`); blob-level at-rest encryption absent |
| Status fields | Backend: Partial (no-worker sync fallback exists at service.go:916, but enqueue failure does NOT fall back, V-M) · others: — |
| Final status | **PARTIAL** — blob encryption **PLANNED (ADR-003 accepted · OB-3/6)**; security note recorded: the crypto service falls back to base64 plaintext when `EncryptionKey` is unset (V-G) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-79 — Search (inverted index + Elasticsearch/Meilisearch; async reindex)
| Field | Value |
|---|---|
| Source | inventory §G SF-79 · ledger PART |
| Implementation | SQL ILIKE search across subscribers/contacts/logs (documented sufficient at current scale); inverted-index/external search engines absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — index stack **PLANNED under ADR-003 (OB-3 mailstore search)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-80 — Quotas (per-account disk + count quotas)
| Field | Value |
|---|---|
| Source | inventory §G SF-80 · ledger PART |
| Implementation | plan/workspace limits at control-plane level (verify Phase 1); per-account disk/count quota machinery absent |
| Status fields | Backend: Partial · Frontend: Partial · DB: Partial · API: Partial · Security: — · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — plan limits enforced via `services/plan` CheckQuota; per-account disk/count quotas **PLANNED (ADR-003 accepted · OB-3)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-81 — Retention/expiration (DataRetention settings, cron purges)
| Field | Value |
|---|---|
| Source | inventory §G SF-81 · ledger FULL |
| Implementation | broader than pre-seed: 12+ record classes incl. blob bytes have retention handling in `retention.go`; **`security_events` retention caller (DeleteOlderThan, security_event_repo.go:157) has zero callers incl. tests (GAP-13, V-G)** |
| Status fields | Backend: Partial · Frontend: Partial (settings) · DB: Verified · API: Partial · Security: — · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (GAP-13 caller + retention settings surface) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-82 — Backup/restore (stop-and-dump CLI, LZ4)
| Field | Value |
|---|---|
| Source | inventory §G SF-82 · ledger N/I |
| Implementation | R-11: pg_dump gzip CronJob into `POSTA_BACKUP_DIR` with retention sweep + restore runbook; restore test verified in the DB-tier battery |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Verified (runbook) |
| Final status | **COMPLETE** — equivalent mechanism (online dump cron) documented vs the reference platform's offline CLI; correction: the pre-seed's "restore test in DB-tier battery" claim was not verifiable (no such artifact found, V-G) — restore-runbook verification **PLANNED (Phase 3)**; `STATUS_HISTORY.md` lacks an R-11 entry (Phase 6) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-83 — Compression (zstd with per-type dictionaries)
| Field | Value |
|---|---|
| Source | inventory §G SF-83 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-003 accepted · OB-3)** — gzip used where it exists today (backups, DMARC) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

## Category H — APIs & integrations (SF-84..SF-91)

### SF-84 — JMAP server (core/mail/submission/…)
| Field | Value |
|---|---|
| Source | inventory §H SF-84 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED under ADR-004/005 (OB-4; revisit OB-6)** — REST/OpenAPI covers API needs today (RF-B126 evaluation pending) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

### SF-85 — IMAP4rev1/rev2 server
| Field | Value |
|---|---|
| Source | inventory §H SF-85 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-004 accepted · OB-4)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

### SF-86 — POP3 + ManageSieve servers
| Field | Value |
|---|---|
| Source | inventory §H SF-86 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-004 accepted · OB-4)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

### SF-87 — Management API (REST/OpenAPI)
| Field | Value |
|---|---|
| Source | inventory §H SF-87 · ledger FULL |
| Implementation | REST API with runtime-generated OpenAPI (okapi, config.go:560 — not a static spec) + route contract test pinning **359** routes against `docs/implementation/api_routes.txt` (grew from 357 with the DMARC read surface; audit's 357 figure was stale, V-H) |
| Status fields | Backend: Verified · Frontend: Verified (37 api modules) · DB: — · API: Verified · Security: Verified · Tests: Verified (contract) · Docs: Verified (OpenAPI) |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

### SF-88 — API auth (Basic/Bearer, keys w/ narrowing + IP allowlists, rate limiter)
| Field | Value |
|---|---|
| Source | inventory §H SF-88 · ledger FULL |
| Implementation | bearer/JWT + API keys (scopes, IP allowlists, R-15 rate limits, R-18 usage) + R-28 query-token scoping to SSE |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — recorded limitations: no Basic auth (Bearer/cookie only; OAuth2 is login-identity, not API auth) and no Retry-After header (V-H + 2026-11-07 audit) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

### SF-89 — Web Push (RFC 8030/8291/8292 VAPID) + SSE + WebSocket push
| Field | Value |
|---|---|
| Source | inventory §H SF-89 · ledger PART |
| Implementation | SSE streams COMPLETE (messages, inbound, admin events/metrics/security); Web Push + WebSocket push absent |
| Status fields | Backend: Partial · Frontend: Verified (SSE consumers) · DB: — · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — Web Push **PLANNED (Phase 3)**; WebSocket push **PLANNED (Phase 3)** only where a consumer exists |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

### SF-90 — Outbound telemetry webhooks (HMAC-signed event batches)
| Field | Value |
|---|---|
| Source | inventory §H SF-90 · ledger FULL |
| Implementation | HMAC-signed webhook endpoints (`signPayload:451`), per-event payloads (`Payload`/`CampaignPayload` — all 7 call sites single-event, **no event batching, no throttle/max-pending**), delivery-attempt history, exponential backoff + jitter (`retryDelay:62`, R-06), audited SSRF-guarded replay (5/hr cap), automation webhooks on the shared guard (~40 named tests) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (SSRF guard) · Tests: Verified · Docs: Verified |
| Final status | **PARTIAL** — pre-seed's "batching" note refuted (per-event payloads only, V-H); batching + pending-limits **PLANNED (Phase 3)**; all other machinery verified |
| Evidence | pre-seed: `internal/services/webhook/` (ssrf.go, dispatcher) |

### SF-91 — Lookup stores (Redis/HTTP/memory/SQL) for Sieve/rate-limits/traps
| Field | Value |
|---|---|
| Source | inventory §H SF-91 · ledger PART |
| Implementation | Redis + in-memory + SQL stores power rate limiting (login + per-key), bans, challenges, session blacklist; Sieve/trap consumers absent (ADR-006) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — trap/Sieve consumers PLANNED under ADR-006; store abstraction **COMPLETE** (equivalent) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

## Category I — Monitoring & observability (SF-92..SF-96)

### SF-92 — Event/metrics core
| Field | Value |
|---|---|
| Source | inventory §I SF-92 · ledger FULL |
| Implementation | Prometheus families for campaigns/automations/queues/relay + per-key usage analytics (R-12/R-18); registration + increment tests added 2026-11-07 |
| Status fields | Backend: Verified · Frontend: Verified (Jobs metrics card) · DB: — · API: Verified · Security: — · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

### SF-93 — Prometheus/OpenMetrics endpoint + OTLP push exporter
| Field | Value |
|---|---|
| Source | inventory §I SF-93 · ledger FULL |
| Implementation | `/metrics` OpenMetrics endpoint (gated by `POSTA_METRICS_ENABLED`, default-off documented); **OTLP push exporter absent** (GAP-51 adjacent) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: Verified · Security: — · Tests: Verified · Docs: Verified |
| Final status | **PARTIAL** — OTLP **PLANNED (OB-6)** (GAP-51/52); gate asymmetry noted: server `/metrics` gated by POSTA_METRICS_ENABLED (default-off) while worker health/metrics ride POSTA_WORKER_HEALTH_ENABLED (default-on) (V-IJ) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

### SF-94 — Structured log sinks (console/files/journald/OTLP/webhooks/store)
| Field | Value |
|---|---|
| Source | inventory §I SF-94 · ledger PART |
| Implementation | structured logging (jkaninda/logger) to console with request-scoped fields; file/journald/OTLP/webhook sinks absent |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: — · Tests: — · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (configurable sinks) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

### SF-95 — Health endpoints (/healthz/live + /healthz/ready)
| Field | Value |
|---|---|
| Source | inventory §I SF-95 · ledger FULL |
| Implementation | `/healthz` + `/readyz` (server) + worker `/healthz`; compose healthchecks use them |
| Status fields | Backend: Verified · Frontend: — · DB: — · API: Verified · Security: — · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — gap noted: compose lacks a worker healthcheck (V-IJ); add in Phase 3 ops sweep |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

### SF-96 — Metric alerts (condition→action) [ENT] + metrics/trace history
| Field | Value |
|---|---|
| Source | inventory §I SF-96 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (OB-6)** — with GAP-51/52 (error-monitoring hook, Grafana dashboard) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

## Category J — Administration (SF-97..SF-100)

### SF-97 — Admin UI
| Field | Value |
|---|---|
| Source | inventory §J SF-97 · ledger FULL (the reference platform's is external; ReForwarder embeds) |
| Implementation | Vue 3 dashboard: full admin surface (users, workspaces, queue admin, security, settings, announcements, provider events, system info) |
| Status fields | Backend: — · Frontend: Verified · DB: — · API: Verified · Security: Verified (admin middleware) · Tests: Partial (vitest) · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

### SF-98 — Configuration = typed registry objects
| Field | Value |
|---|---|
| Source | inventory §J SF-98 · ledger FULL |
| Implementation | settings table + typed accessors + seeder + live-apply 30s TTL cache + admin Settings UI (documented equivalent of the reference platform's registry objects) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

### SF-99 — Operational controls + task manager with scheduler-role gating
| Field | Value |
|---|---|
| Source | inventory §J SF-99 · ledger PART |
| Implementation | **11 cron job types** (cmd/posta/server.go:702-790, V-IJ — pre-seed said 8) + admin queue operations + R-13 automation schedules; task-manager visibility EXISTS (`GET /admin/jobs`, cron_handler.go:36 + Jobs.vue:305-340 next-run/status); missing only run history, manual trigger, scheduler-role gating |
| Status fields | Backend: Partial · Frontend: Partial · DB: Verified · API: Partial · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (run history + manual trigger + scheduler-role gating) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. |

### SF-100 — Configuration history/rollback
| Field | Value |
|---|---|
| Source | inventory §J SF-100 · ledger N/I (the reference platform negative) |
| Implementation | settings writes emit exactly ONE `settings.updated` audit event with nil metadata (setting_handler.go:101, V-IJ); no per-key diff history, no rollback surface |
| Status fields | Backend: Missing · Frontend: Missing · DB: Partial (audit exists) · API: Missing · Security: — · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** — per-key diff capture first, then history/rollback on the events substrate |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

## Category K — Automation & extensibility (SF-101..SF-105)

### SF-101 — Sieve system + user scripts at every SMTP stage and delivery
| Field | Value |
|---|---|
| Source | inventory §K SF-101 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2)** — documented alternative: automation engine (visual builder, 9 node types, triggers incl. cron R-13, per-subscriber journeys) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-102 — Sieve plugin functions [ENT]
| Field | Value |
|---|---|
| Source | inventory §K SF-102 · ledger PART |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-6)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-103 — Expression language reused everywhere
| Field | Value |
|---|---|
| Source | inventory §K SF-103 · ledger PART |
| Implementation | three condition currencies (FilterRule, SegmentCondition SQL builder, automation conditions incl. `in_segment`) — individually COMPLETE; single unified evaluator absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (guarded SQL) · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (evaluator unification, pairs SF-50) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-104 — Scheduled task system (ACME, DKIM rotation, reports, purges)
| Field | Value |
|---|---|
| Source | inventory §K SF-104 · ledger PART |
| Implementation | **11 cron job types** (V-KL + V-IJ) + campaign scheduler (R-01) + automation schedule triggers (R-13, ProcessSchedules engine.go:608) + backup cron (R-11) + reconcile cron; **scheduled DKIM rotation and scheduled report-generation tasks absent** |
| Status fields | Backend: Partial · Frontend: Partial · DB: Verified · API: Partial · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (DKIM rotation task; report tasks with SF-25 send) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-105 — Cluster broadcast fabric (Redis/NATS/Zenoh/Kafka)
| Field | Value |
|---|---|
| Source | inventory §K SF-105 · ledger N/A |
| Implementation | Redis pub/sub for ban propagation (single-fabric equivalent); multi-node fabric absent |
| Status fields | Backend: Partial · others: — |
| Final status | **PLANNED (ADR-009 accepted · OB-6)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

## Category L — Performance & reliability (SF-106..SF-110)

### SF-106 — Store-level queue failover with distributed event locks
| Field | Value |
|---|---|
| Source | inventory §L SF-106 · ledger PART |
| Implementation | single-node: CAS status claims + `FOR UPDATE SKIP LOCKED` batch claims (R-26) + asynq task dedup COMPLETE; multi-node store failover absent |
| Status fields | Backend: Partial · Frontend: — · DB: Verified · API: — · Security: — · Tests: Verified (exactly-once) · Docs: Partial |
| Final status | **PARTIAL** — failover **PLANNED (ADR-009 accepted · OB-6)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-107 — Cache layers (TTL, negative caching, invalidation, snapshots)
| Field | Value |
|---|---|
| Source | inventory §L SF-107 · ledger FULL |
| Implementation | settings 30s TTL live-apply, security-engine COW snapshot + Redis pub/sub propagation, domain/verification caches |
| Status fields | Backend: Verified · Frontend: — · DB: — · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — cluster invalidation across nodes PLANNED under ADR-009 |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-108 — Backpressure (queue caps, memory budgets, transfer limits, throttles)
| Field | Value |
|---|---|
| Source | inventory §L SF-108 · ledger PART |
| Implementation | bounded retry sweeps + batch limits + jitter (R-27); asynq concurrency; memory budgets and transfer throttles absent |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — memory budgets **PLANNED (Phase 3)**; MTA-scale throttles deferred to ADR-001 (OB-1) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-109 — Graceful shutdown (systemd notify, snapshot flush)
| Field | Value |
|---|---|
| Source | inventory §L SF-109 · ledger N/V — **resolving**: shutdown is owned by okapi v1.1.0 with repo `OnShutdown` hooks; worker drains queues |
| Status fields | Backend: Partial · others: — |
| Final status | **PARTIAL** — resolves the ledger N/V (V-KL): `shutdownServer` (server.go:821-855) drains SMTP/cron/redis/db for real, **but the embedded worker is never drained** (startEmbeddedWorker return discarded, server.go:183) and the standalone worker's drain rests on asynq/okapi library behavior; worker-drain fix **PLANNED (Phase 3)**; systemd notify documented as not-adopted (Docker deployment) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

### SF-110 — Hot reload of settings/certs/lookup stores/blocked IPs
| Field | Value |
|---|---|
| Source | inventory §L SF-110 · ledger FULL |
| Implementation | settings live-apply (30s TTL), security rules live-apply, ban propagation via pub/sub, provider accessors; cert reload N/A (ACME absent, ADR-007) |
| Status fields | Backend: Verified · Frontend: Verified (Rules UI) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. |

## Category M — Other capabilities (SF-111..SF-115)

### SF-111 — CalDAV/CardDAV/WebDAV groupware
| Field | Value |
|---|---|
| Source | inventory §M SF-111 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-009 accepted · OB-6 convergence revisit)** — user directive: in scope; anchor corrected (V-M: no ADR-005 groupware coverage; OB-6 owns advanced-integration revisits) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### SF-112 — File storage (JMAP FileNode + text extraction)
| Field | Value |
|---|---|
| Source | inventory §M SF-112 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-003 accepted · OB-3/6)** — email attachment handling exists in the send/inbound pipelines |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### SF-113 — nlp crate (ML classifier, language detection, CJK tokenizers)
| Field | Value |
|---|---|
| Source | inventory §M SF-113 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-006 accepted · OB-2/6)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### SF-114 — Deployment tooling (installer, Docker bake, systemd, AppArmor)
| Field | Value |
|---|---|
| Source | inventory §M SF-114 · ledger FULL |
| Implementation | multi-stage Dockerfile (node→golang→alpine), compose + override, Makefile, multi-arch release workflow (amd64/arm64), GitHub Pages docs; systemd/AppArmor units absent (documented equivalent: Docker) |
| Status fields | Backend: — · Frontend: — · DB: — · API: — · Security: — · Tests: Verified (CI) · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### SF-115 — Schema-version migration guard at boot
| Field | Value |
|---|---|
| Source | inventory §M SF-115 · ledger FULL |
| Implementation | upgrade framework: registry steps, sealed-registry vs fresh-install handling, per-release upgrade notes; migration smoke test (DSN-gated) |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial (no per-release UPGRADING notes found, V-M) |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

## Appendix — Capability-audit-unique requirements (CA records)

Requirements documented in `docs/research/reforwarder-capability-audit.md` that the SF universe does not carry. IDs are new and stable (CA-nn).

### CA-01 — Compose UI for transactional sending
| Field | Value |
|---|---|
| Source | capability audit §1.1 ("API-only by design — a compose screen would be a product decision") |
| Status fields | Backend: Verified (API exists) · Frontend: Missing · DB: — · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PLANNED (OB-5 webmail/compose)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-02 — Backend endpoints without frontend consumers
| Field | Value |
|---|---|
| Source | capability audit §2.3: `POST /campaigns/{id}/duplicate`, `GET /emails/{id}/status`, `GET /admin/api-keys`, `GET /admin/emails` (+ dead client functions flagged by RF-F103) |
| Status fields | Backend: Partial (only `POST /campaigns/{id}/duplicate` + `GET /emails/{id}/status` have routes; the two admin endpoints have NO backend route — dead client functions only, V-M) · Frontend: Missing · DB: — · API: Partial · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (wire the UI or retire the dead client functions) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-03 — Refresh-token rotation
| Field | Value |
|---|---|
| Source | capability audit §1.3 |
| Status fields | Backend: Missing · Frontend: — · DB: Partial (sessions exist) · API: — · Security: Missing · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** (security hardening on the existing session substrate) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-04 — SMTP client negotiation (SMTPUTF8/8BITMIME) + per-message throttling
| Field | Value |
|---|---|
| Source | capability audit §1.1 |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1)** — outbound client negotiation belongs to the MTA foundation; per-message throttle PLANNED (Phase 3) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-05 — VERP / bounce mailbox
| Field | Value |
|---|---|
| Source | capability audit §1.1; todo.md reinstates INF-020 under Option B |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED under ADR-002 (OB-2 return-path processing)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-06 — Fallback broker in dev
| Field | Value |
|---|---|
| Source | capability audit §1.11 (HTTP send falls back to synchronous when Redis is down) |
| Status fields | Backend: Partial · others: — |
| Final status | **PLANNED (Phase 3)** — dev-experience queue fallback |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-07 — OpenTelemetry export
| Field | Value |
|---|---|
| Source | capability audit (twice noted) |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (OB-6)** with SF-96/GAP-51 |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-08 — Segment audiences skip per-list unsubscribes (by design)
| Field | Value |
|---|---|
| Source | capability audit §1.5 gap 4 |
| Status fields | Backend: Verified (documented design; campaign_processor.go:132-186 + service.go:716) · Frontend: — · DB: — · API: — · Security: Verified (global suppressions apply) · Tests: Partial (the list-skip behavior itself untested, V-M) · Docs: Missing |
| Final status | **COMPLETE (as designed)** — behavior documented; the doc note is added in Phase 6 |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

## Verification progress

Verified chunks (read-only code audits, verdict files under `docs/audits/verification/`): **V-A1** (SF-01..11), **V-A2** (SF-12..21), **V-B1** (SF-22..33), **V-B2** (SF-34..45), **V-CD** (SF-46..59), **V-EF** (SF-60..74), **V-G** (SF-75..83), **V-H** (SF-84..91), **V-IJ** (SF-92..100), **V-KL** (SF-101..110), **V-R** (R-01..28 spot re-check: **27/27 CONFIRMED**, ~60 exact test/mechanism spot checks, zero code drift; locator note: `evaluateConditionSet` is at `engine.go:515`, not triggers.go). Pending: V-M (SF-111..115 + CA-01..08). Statuses were corrected where evidence demanded (SF-90 →PARTIAL, SF-109 →PARTIAL, SF-77 ledger FULL →PARTIAL, SF-35 PLAIN-only, SF-37 no tests, SF-69 `two_factor_required` dead-toggle finding, SF-99 task-manager partially exists, 11 cron jobs not 8); the BLOCKED BY ADR vocabulary was retired after the ADR gate passed (all records now PLANNED with their ADR + phase).

## Final tally (frozen 2026-11-07 after verification waves V-A1…V-M + V-R; 123 records)

| Final status | Count | Records |
|---|---|---|
| COMPLETE | 23 | SF-04, SF-20, SF-36, SF-38, SF-44, SF-58, SF-62, SF-64, SF-69, SF-70, SF-74, SF-82, SF-87, SF-88, SF-92, SF-95, SF-97, SF-98, SF-107, SF-110, SF-114, SF-115, CA-08 |
| PARTIAL (verified remaining scope; each carries its PLANNED slices) | 47 | SF-03, SF-07, SF-08, SF-09, SF-13, SF-16, SF-25, SF-33, SF-35, SF-37, SF-43, SF-49, SF-50, SF-52, SF-53, SF-54, SF-56, SF-57, SF-59, SF-60, SF-61, SF-63, SF-65, SF-66, SF-67, SF-68, SF-72, SF-73, SF-75, SF-76, SF-77, SF-78, SF-79, SF-80, SF-81, SF-89, SF-90, SF-91, SF-93, SF-94, SF-99, SF-103, SF-104, SF-106, SF-108, SF-109, CA-02 |
| MISSING → PLANNED (Phase 3) | 7 | SF-10, SF-22, SF-23, SF-24, SF-26, SF-100, CA-03 |
| PLANNED (ADR-anchored, OB phases) | 46 | SF-01, SF-02, SF-05, SF-06, SF-11, SF-12, SF-14, SF-15, SF-17, SF-18, SF-19, SF-21, SF-27, SF-28, SF-29, SF-30, SF-31, SF-32, SF-34, SF-39, SF-40, SF-41, SF-42, SF-45, SF-46, SF-47, SF-48, SF-51, SF-55, SF-71, SF-83, SF-84, SF-85, SF-86, SF-96, SF-101, SF-102, SF-105, SF-111, SF-112, SF-113, CA-01, CA-04, CA-05, CA-06, CA-07 |
| BROKEN | 0 | — |


## Verification record (Phase 1 waves — executed 2026-11-07)

- **Wave V1..V10** — ~10 auditors, chunked by category, verify every record against the code (route→logic→persistence→isolation→tests), upgrade/downgrade statuses with `path:line` evidence, and resolve the two former N/V items (SF-11, SF-109) and the ledger-claim reconciliations (SF-37/68/72/73 FULL claims).
- **Wave R** — spot re-check of the 27 R-delivered records against the 2026-11-07 acceptance-audit evidence (commit `27c418a`).
- Every status change is written back here with evidence; the tally table is then frozen as the audit's official statistics.

## Resumability

This register is the programme's persistent tracker: after every wave, statuses, evidence, and the tally are updated in place. Any interrupted phase resumes from the register without loss of progress. Programme-level phase tracking lives in `todo.md` Track A and `docs/implementation/ROADMAP_IMPLEMENTATION.md`.






