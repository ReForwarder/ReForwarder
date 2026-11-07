# Stalwart Feature Parity Audit — Master Register

**Audit:** Stalwart 115-feature parity programme · **Started:** 2026-11-07 · **Baseline HEAD:** `482d10b` ·
**Sources:** [STALWART_SOURCE_REGISTER.md](STALWART_SOURCE_REGISTER.md) · **Status vocabulary:** `COMPLETE` · `PARTIAL` · `MISSING` · `BROKEN` · `BLOCKED BY ADR-nnn (Option B Phase n)` · `PLANNED (Phase n)` · `IN PROGRESS`.

## Method and honesty rules

1. This register is the **definitive, feature-by-feature verification ledger** for every feature in the Stalwart research universe (SF-01..SF-115) plus the ReForwarder-side requirements unique to the capability audit (appendix CA records). The original inventory IDs are permanent and preserved.
2. **Pre-seed notice:** records below are seeded from the authoritative ledger (`stalwart-feature-gap-analysis.md` §4a) and the R-01..R-28 delivery evidence (2026-11-07 acceptance audit). Every record is re-verified against the actual code in verification waves before its Final status becomes final; Evidence fields cite `path:line` or a command + output. A pre-seed status marked *(verify)* is provisional.
3. Per the programme decision (2026-11-07): **no documented feature is classified N/A merely because the current relay architecture does not support it.** The mail-server surface carries `BLOCKED BY ADR-nnn` with the governing ADR and the assigned Option B phase (per `todo.md` Track A / `upcomming-convo.md` §7) and is implemented in sequence after the ADR gate. "Still dead" items from the do-not-implement table carry their documented lightweight alternative and are classified from code evidence.
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
| R-07 | SF-63 (diagnose), SF-64 (health — improves on Stalwart's negative) |
| R-08/R-09/R-14/R-21 | product-side (no SF ref) |
| R-10 | SF-69 (2FA + recovery, beyond Stalwart) |
| R-11 | SF-82 (backup/restore automation) |
| R-12 | SF-93 (families) |
| R-13 | SF-104 (cron triggers) |
| R-15 | SF-88/SF-91 (per-key limits at the lookup-store seam) |
| R-17 | SF-25 (aggregate ingest), SF-57 (read surface) |
| R-18 | SF-92 (per-key usage) |
| R-19 | SF-69 (WebAuthn — beyond Stalwart) |
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
| Final status | **BLOCKED BY ADR-001 (OB-1)** |
| Evidence | pre-seed; Phase 1 to verify the inbound receiver's command coverage as partial evidence |

### SF-02 — ESMTP extension advertisement
| Field | Value |
|---|---|
| Source | inventory §A SF-02 · ledger N/A |
| Requirement | PIPELINING/CHUNKING/8BITMIME/SMTPUTF8/DSN/REQUIRETLS/FUTURERELEASE advertisement |
| Implementation | go-smtp receiver advertises its defaults; outbound client negotiates none (relay protocol only) |
| Status fields | Backend: Missing · Frontend: — · DB: — · API: — · Security: — · Tests: Missing · Docs: Missing |
| Final status | **BLOCKED BY ADR-001 (OB-1)** |
| Evidence | pre-seed |

### SF-03 — Submission (MSA) with delivery-status writeback
| Field | Value |
|---|---|
| Source | inventory §A SF-03 · ledger N/A |
| Requirement | Authenticated submission port with status writeback |
| Implementation | HTTP API send (`POST /api/v1/emails/send|batch|send-template`) with event writeback (`email_events`) is the submission analog; SMTP MSA absent |
| Status fields | Backend: Partial (HTTP analog) · Frontend: — · DB: Verified (events) · API: Verified · Security: Verified (auth) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — SMTP MSA BLOCKED BY ADR-001 (OB-1); HTTP analog COMPLETE |
| Evidence | pre-seed; Phase 1 to cite send handlers + `email_events` recorder |

### SF-04 — Relay control / open-relay prevention
| Field | Value |
|---|---|
| Source | inventory §A SF-04 · ledger N/A |
| Requirement | Authenticated relaying only; no open relay |
| Implementation | Relay credentials bcrypt + constant-time (R-25) + IP allowlists; security engine SMTP/HTTP enforcement; inbound receiver domain-verified |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** *(verify in Phase 1)* |
| Evidence | pre-seed: `internal/services/smtprelay/`, `internal/services/security/` |

### SF-05 — Routing strategies (Local / Mx / Relay via expressions)
| Field | Value |
|---|---|
| Source | inventory §A SF-05 · ledger N/A |
| Requirement | Per-domain routing strategy selection |
| Implementation | Relay-only routing (per-domain SMTP server config); Local/Mx absent |
| Status fields | Backend: Missing · Frontend: Partial (relay config UI) · DB: Verified · API: Verified · Security: — · Tests: Partial · Docs: Partial |
| Final status | **BLOCKED BY ADR-001 (OB-1)** — relay half COMPLETE |
| Evidence | pre-seed |

### SF-06 — MX handling (preference, shuffle, null-MX, implicit MX, DNSSEC)
| Field | Value |
|---|---|
| Source | inventory §A SF-06 · ledger N/A |
| Requirement | MX resolution behaviors for direct delivery |
| Implementation | absent (no direct-to-MX) |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1)** |
| Evidence | pre-seed |

### SF-07 — Outbound relays (SASL, TLS modes, source-IP pools, pipelining)
| Field | Value |
|---|---|
| Source | inventory §A SF-07 · ledger PART |
| Requirement | Configurable outbound relays with auth, TLS modes, source-IP pools, pipelining |
| Implementation | SMTP server config + relay creds (R-25) + dial timeout/deadlines (R-22); source-IP pools absent |
| Status fields | Backend: Partial · Frontend: Verified (server config) · DB: Verified · API: Verified · Security: Verified (bcrypt) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — pools BLOCKED BY ADR-001 (OB-6 IP pools) |
| Evidence | pre-seed |

### SF-08 — Persistent multi-queue manager (per-recipient retry/notify schedules, expiry, event-locked delivery)
| Field | Value |
|---|---|
| Source | inventory §A SF-08 · ledger PART |
| Requirement | Durable queues with per-recipient schedules, expiry, event-locked delivery |
| Implementation | asynq persistent queues + Redis; atomic claims CAS + SKIP LOCKED (R-26); bounded retries (R-27); per-recipient notify schedules and message expiry absent |
| Status fields | Backend: Partial · Frontend: Partial (R-03 admin) · DB: Verified · API: Partial · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — remaining (retry/notify schedules, expiry) **PLANNED (Phase 3)** |
| Evidence | pre-seed; Phase 1 to pin asynq retention/expiry behavior |

### SF-09 — Future release (HOLDFOR/HOLDUNTIL), DELIVERBY, MT-PRIORITY, DSN params
| Field | Value |
|---|---|
| Source | inventory §A SF-09 · ledger PART |
| Requirement | Deferred delivery at message level |
| Implementation | scheduled campaigns (R-01) + API `scheduled_at` = HOLDFOR/UNTIL analog at application level |
| Status fields | Backend: Verified (app-level) · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **PARTIAL** — SMTP-protocol params BLOCKED BY ADR-001 (OB-1) |
| Evidence | pre-seed |

### SF-10 — Full DSN generation (RFC 3464), double-bounce, signed DSNs
| Field | Value |
|---|---|
| Source | inventory §A SF-10 · ledger N/I |
| Requirement | Standards-compliant delivery-status notifications |
| Implementation | provider-event bounce records + `email_events` exist; RFC 3464 DSN message generation absent (R-04 delivered status truth, not DSNs) |
| Status fields | Backend: Missing · Frontend: — · DB: Partial · API: — · Security: — · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** |
| Evidence | pre-seed |

### SF-11 — Forwarding chain (sub-addressing, catch-all, aliases, list expansion)
| Field | Value |
|---|---|
| Source | inventory §A SF-11 · ledger N/V — **resolved**: no forwarding/auto-forwarding exists in ReForwarder (confirmed by prior audits); inbound aliases/forwarding/catch-all are Option B Phase 2 scope |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-002 (OB-2)** |
| Evidence | pre-seed; Phase 1 to cite the inbound receiver's RCPT handling |

### SF-12 — Envelope rewriting (MAIL FROM / RCPT TO)
| Field | Value |
|---|---|
| Source | inventory §A SF-12 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1)** |
| Evidence | pre-seed |

### SF-13 — Traceability (Received headers, queue ids, wire logging)
| Field | Value |
|---|---|
| Source | inventory §A SF-13 · ledger PART |
| Implementation | `email_events` delivery timeline + attempt history + external message IDs; RFC 5322 Received-header injection and per-hop queue ids absent |
| Status fields | Backend: Partial · Frontend: Verified (Logs) · DB: Verified · API: Verified · Security: — · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** |
| Evidence | pre-seed |

### SF-14 — Local delivery engine (per-recipient Sieve at ingest)
| Field | Value |
|---|---|
| Source | inventory §A SF-14 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-002/003 (OB-2/3)** |
| Evidence | pre-seed |

### SF-15 — Milter client
| Field | Value |
|---|---|
| Source | inventory §A SF-15 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1)** — documented alternative: automations + HMAC webhooks (do-not-implement table) |
| Evidence | pre-seed |

### SF-16 — MTA hooks (per-SMTP-stage first-party HTTP hooks, HMAC-signed)
| Field | Value |
|---|---|
| Source | inventory §A SF-16 · ledger PART |
| Implementation | HMAC-signed outbound webhooks with retries/replay (SF-90 machinery) + automation triggers on business events; per-SMTP-stage (EHLO/MAIL/RCPT) hooks absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (HMAC) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — SMTP-stage hooks BLOCKED BY ADR-001 (OB-1/2) |
| Evidence | pre-seed |

### SF-17 — Queue quota reservations
| Field | Value |
|---|---|
| Source | inventory §A SF-17 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1)** |
| Evidence | pre-seed |

### SF-18 — Queue blob encryption [ENT]
| Field | Value |
|---|---|
| Source | inventory §A SF-18 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-003 (OB-3)** — note: credentials/keys are AES-256-GCM encrypted today (`internal/services/crypto`) |
| Evidence | pre-seed |

### SF-19 — Tenant-scoped queues [ENT]
| Field | Value |
|---|---|
| Source | inventory §A SF-19 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-008 (OB-6)** |
| Evidence | pre-seed |

### SF-20 — DKIM signing at queue time (DKIM1 RSA/Ed25519 + DKIM2 chained)
| Field | Value |
|---|---|
| Source | inventory §A SF-20 · ledger N/I |
| Implementation | R-02: go-msgauth DKIM1 signing (RSA-2048 + Ed25519-SHA256) hooked into the delivery path, encrypted keys, DNS TXT display/verify; DKIM2 chained absent |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (encrypted keys) · Tests: Verified (golden-message + independent verifier) · Docs: Verified |
| Final status | **COMPLETE** — DKIM2 recorded as a limitation under ADR-007 (OB-6) |
| Evidence | pre-seed: `internal/services/email/dkim_signer.go`, `domain_signing_keys`, R-02 tracker note + 2026-11-07 audit |

### SF-21 — MT-PRIORITY ingress + virtual-queue ordering
| Field | Value |
|---|---|
| Source | inventory §A SF-21 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1)** |
| Evidence | pre-seed |

## Category B — Email security & authentication (SF-22..SF-45)

### SF-22 — SPF verification (EHLO + MAIL FROM, strict modes, Received-SPF)
| Field | Value |
|---|---|
| Source | inventory §B SF-22 · ledger N/I |
| Requirement | Message-level SPF evaluation on inbound mail |
| Implementation | DNS **presence** checks only (domain verification + R-07 health); no message-level SPF engine |
| Status fields | Backend: Missing · Frontend: — · DB: Missing · API: — · Security: — · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** — relay-compatible: evaluate on the feature-gated inbound receiver |
| Evidence | pre-seed |

### SF-23 — DKIM verification (insecure-sig stripping, strict, ARF)
| Field | Value |
|---|---|
| Source | inventory §B SF-23 · ledger N/I |
| Implementation | absent; `emersion/go-msgauth` already in the module provides `dkim.Verify` |
| Status fields | Backend: Missing · others: — |
| Final status | **MISSING → PLANNED (Phase 3)** |
| Evidence | pre-seed |

### SF-24 — DMARC verification with policy enforcement
| Field | Value |
|---|---|
| Source | inventory §B SF-24 · ledger N/I |
| Implementation | DMARC DNS-record checks (R-07) + aggregate ingestion (R-17); message-level DMARC evaluation/enforcement absent |
| Status fields | Backend: Missing · others: — |
| Final status | **MISSING → PLANNED (Phase 3)** — depends on SF-22 + SF-23 |
| Evidence | pre-seed |

### SF-25 — DMARC aggregate reports: send AND receive/ingest
| Field | Value |
|---|---|
| Source | inventory §B SF-25 · ledger N/I |
| Implementation | **Receive/ingest: COMPLETE** (R-17: RFC 7489 parser, gzip, 16 MiB decompression cap, idempotent storage, read surface `GET /domains/{id}/dmarc` + UI); **send: MISSING** (no aggregate-report generation/dispatch for our domains) |
| Status fields | Backend: Partial · Frontend: Verified (read) · DB: Verified · API: Verified (read) · Security: Verified (size caps) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — send side **PLANNED (Phase 3)** |
| Evidence | pre-seed: `internal/services/inbound/dmarc.go`, `internal/storage/repositories/dmarc_repo.go` |

### SF-26 — SPF/DKIM auth-failure (ARF RFC 5965/6591) reports
| Field | Value |
|---|---|
| Source | inventory §B SF-26 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **MISSING → PLANNED (Phase 3)** — depends on SF-22/23 |
| Evidence | pre-seed |

### SF-27 — ARC verification (no sealing)
| Field | Value |
|---|---|
| Source | inventory §B SF-27 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-002 (OB-2)** |
| Evidence | pre-seed |

### SF-28 — DKIM2 verification (chained) + forged-DSN protection
| Field | Value |
|---|---|
| Source | inventory §B SF-28 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-007 (OB-6)** |
| Evidence | pre-seed |

### SF-29 — MTA-STS client + local policy server
| Field | Value |
|---|---|
| Source | inventory §B SF-29 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001/007 (OB-1/6)** |
| Evidence | pre-seed |

### SF-30 — DANE (TLSA) with DNSSEC
| Field | Value |
|---|---|
| Source | inventory §B SF-30 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1/6)** |
| Evidence | pre-seed |

### SF-31 — TLS-RPT (outbound build/send + inbound parse)
| Field | Value |
|---|---|
| Source | inventory §B SF-31 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-007 (OB-6)** |
| Evidence | pre-seed |

### SF-32 — Reverse DNS (iprev) validation
| Field | Value |
|---|---|
| Source | inventory §B SF-32 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-002 (OB-1 PTR guidance / OB-2)** |
| Evidence | pre-seed |

### SF-33 — TLS server stack (STARTTLS, SNI wildcard fallback)
| Field | Value |
|---|---|
| Source | inventory §B SF-33 · ledger PART |
| Implementation | inbound SMTP STARTTLS via the go-smtp receiver; relay TLS per server config; SNI wildcard fallback + automated cert management absent (SF-34) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — SNI/ACME BLOCKED BY ADR-007/009 (OB-6) |
| Evidence | pre-seed |

### SF-34 — ACME certificate automation
| Field | Value |
|---|---|
| Source | inventory §B SF-34 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-007/009 (OB-6)** — documented alternative: ingress/proxy TLS (do-not-implement table) |
| Evidence | pre-seed |

### SF-35 — SASL mechanisms (PLAIN, LOGIN, OAUTHBEARER, XOAUTH2, MFA, app passwords)
| Field | Value |
|---|---|
| Source | inventory §B SF-35 · ledger PART |
| Implementation | relay AUTH PLAIN/LOGIN with bcrypt + constant-time compare + rehash migration (R-25) COMPLETE; OAUTHBEARER/XOAUTH2/MFA tokens absent; app passwords absent |
| Status fields | Backend: Partial · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — OAUTH*/MFA BLOCKED BY ADR-001 (OB-1); app passwords **PLANNED (Phase 3)** (pairs SF-67) |
| Evidence | pre-seed |

### SF-36 — Password hashing (Argon2id/PBKDF2/scrypt/bcrypt/sha-crypt)
| Field | Value |
|---|---|
| Source | inventory §B SF-36 · ledger FULL |
| Implementation | bcrypt across user passwords, relay credentials (R-25), recovery codes; constant-time comparisons; rehash-on-success migration |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — single-algorithm (bcrypt) is the documented equivalent of Stalwart's multi-algorithm support; Phase 1 verifies no legacy unsalted rows remain |
| Evidence | pre-seed: `internal/services/smtprelay/credential.go`, `credential_repo_test.go` |

### SF-37 — OAuth 2.0/OIDC provider + directory backends (LDAP/SQL/OIDC)
| Field | Value |
|---|---|
| Source | inventory §B SF-37 · ledger FULL |
| Implementation | OAuth/OIDC as **consumer** (Google sign-in) COMPLETE; as **provider** (issuing tokens to external clients) MISSING; LDAP/SQL directory backends MISSING |
| Status fields | Backend: Partial · Frontend: Verified (login) · DB: Verified · API: Partial · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — provider + directory backends **PLANNED (Phase 3)** (control-plane feature; ledger's FULL claim to be re-derived in Phase 1) |
| Evidence | pre-seed |

### SF-38 — Abuse prevention suite (fail2ban, greylisting, rate limiters, DNSBL)
| Field | Value |
|---|---|
| Source | inventory §B SF-38 · ledger FULL |
| Implementation | security engine: ZSET sliding-window detector, weighted risk, escalation ladder, BanService + COW snapshot + Redis propagation, SMTP silent-drop/EHLO-421/HTTP-403 enforcement, allowlist; Redis rate limiters (login + per-API-key R-15); DNSBL absent |
| Status fields | Backend: Verified · Frontend: Verified (Security admin) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — DNSBL recorded as a limitation → ADR-006 (OB-2) |
| Evidence | pre-seed: `internal/services/security/`, 2026-11-02 E2E |

### SF-39 — Spam filter (~23 analyzers / ~200 tags)
| Field | Value |
|---|---|
| Source | inventory §B SF-39 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2)** |
| Evidence | pre-seed |

### SF-40 — Statistical spam classifier (FTRL-Proximal)
| Field | Value |
|---|---|
| Source | inventory §B SF-40 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2/6)** |
| Evidence | pre-seed |

### SF-41 — AI spam verdicts via LLM [ENT]
| Field | Value |
|---|---|
| Source | inventory §B SF-41 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-6)** |
| Evidence | pre-seed |

### SF-42 — Pyzor, spam traps, GTUBE, attachment blocklists, external rule feed
| Field | Value |
|---|---|
| Source | inventory §B SF-42 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2)** |
| Evidence | pre-seed |

### SF-43 — Connection/DoS budgets (memory accounting, session reservations)
| Field | Value |
|---|---|
| Source | inventory §B SF-43 · ledger PART |
| Implementation | security-engine connection-abuse detection + HTTP 403 + SMTP silent drops + bounded retry sweeps (R-27); memory accounting/session reservations absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — memory accounting **PLANNED (Phase 3)** (bounded worker pools); MTA-scale budgets BLOCKED BY ADR-001 (OB-1) |
| Evidence | pre-seed |

### SF-44 — Security event taxonomy (~40 families)
| Field | Value |
|---|---|
| Source | inventory §B SF-44 · ledger FULL |
| Implementation | `security_events` + shared `events` table + audit taxonomy across auth/security/admin actions, SSE streams, CSV export |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/services/security/`, `internal/handlers/` events |

### SF-45 — Sieve-driven policy at SMTP stages
| Field | Value |
|---|---|
| Source | inventory §B SF-45 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2)** — documented alternative: automation filter nodes |
| Evidence | pre-seed |

## Category C — Message filtering & policy (SF-46..SF-51)

### SF-46 — Sieve interpreter (RFC 5228 + extensions)
| Field | Value |
|---|---|
| Source | inventory §C SF-46 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2)** — documented alternative: automation engine + filter nodes (do-not-implement table) |
| Evidence | pre-seed |

### SF-47 — ManageSieve (RFC 5804)
| Field | Value |
|---|---|
| Source | inventory §C SF-47 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-004 (OB-4)** |
| Evidence | pre-seed |

### SF-48 — Per-account Sieve at final delivery
| Field | Value |
|---|---|
| Source | inventory §C SF-48 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2/5)** |
| Evidence | pre-seed |

### SF-49 — Header inspection/transformation (editheader, milter, hooks, A-R injection)
| Field | Value |
|---|---|
| Source | inventory §C SF-49 · ledger PART |
| Implementation | R-24 CR/LF/NUL rejection + last-guard `encodeHeader` sanitization COMPLETE; Authentication-Results injection absent (pairs SF-22/23/24) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — A-R injection **PLANNED (Phase 3)**; editheader/milter BLOCKED BY ADR-006/001 |
| Evidence | pre-seed: `internal/services/email/service.go:1507` (validateSendHeaders), `smtp_sender.go` |

### SF-50 — Conditional routing/policy engine (IfBlock expression language)
| Field | Value |
|---|---|
| Source | inventory §C SF-50 · ledger PART |
| Implementation | FilterRules + SegmentCondition SQL builder + automation filter nodes incl. `in_segment` (R-14) COMPLETE for campaign/automation conditioning; transport-policy expressions absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (guarded SQL) · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — transport-policy expressions BLOCKED BY ADR-001; evaluator unification **PLANNED (Phase 3)** (pairs SF-103) |
| Evidence | pre-seed: `internal/storage/repositories/segment_query.go` |

### SF-51 — Quarantine (spam-percentage Junk filing)
| Field | Value |
|---|---|
| Source | inventory §C SF-51 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-003/006 (OB-2/3)** |
| Evidence | pre-seed |

## Category D — Queue & delivery administration (SF-52..SF-59)

### SF-52 — Queue inspection API (list/query/patch/destroy/cancel/requeue)
| Field | Value |
|---|---|
| Source | inventory §D SF-52 · ledger PART |
| Implementation | R-03 + R-06: per-state task lists, cancel/run/archive, stats, audited replay w/ 5/hour cap, endpoint-level tests (2026-11-07); per-message patch/destroy absent |
| Status fields | Backend: Partial · Frontend: Verified (Jobs.vue) · DB: Verified (asynq) · API: Verified · Security: Verified (admin) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — patch/destroy **PLANNED (Phase 3)** |
| Evidence | pre-seed: `internal/handlers/admin_queue_handler.go`, `webhook_replay_handler.go` |

### SF-53 — SMTP response taxonomy surfaced to admins
| Field | Value |
|---|---|
| Source | inventory §D SF-53 · ledger PART |
| Implementation | R-05 14-class classifier (enhanced codes + provider reasons + diagnostics) recorded on failed events and provider events; per-message SMTP transcript/typed-error surfacing absent |
| Status fields | Backend: Partial · Frontend: Partial (classification on Logs) · DB: Verified · API: Verified · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (transcript view; pairs GAP-20) |
| Evidence | pre-seed: `internal/services/provider_events/classifier.go` |

### SF-54 — Global queue pause/resume
| Field | Value |
|---|---|
| Source | inventory §D SF-54 · ledger PART |
| Implementation | per-worker start/stop + automation pause/resume + campaign pause; **global queue pause/resume + per-domain suspension** absent |
| Status fields | Backend: Missing (global) · Frontend: Partial · DB: — · API: Missing · Security: — · Tests: Missing · Docs: Missing |
| Final status | **PARTIAL — PLANNED (Phase 3)** (pairs GAP-48) |
| Evidence | pre-seed |

### SF-55 — Live delivery diagnosis SSE [ENT]
| Field | Value |
|---|---|
| Source | inventory §D SF-55 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1)** |
| Evidence | pre-seed |

### SF-56 — Delivery tracing (trace store + live tracing SSE)
| Field | Value |
|---|---|
| Source | inventory §D SF-56 · ledger PART |
| Implementation | `email_events` timeline + `webhook_delivery_attempts` + Logs MessageDetail COMPLETE (read-only tracing); live per-message trace SSE absent |
| Status fields | Backend: Partial · Frontend: Verified (Logs) · DB: Verified · API: Partial · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (SSE pattern already exists in-repo) |
| Evidence | pre-seed |

### SF-57 — DMARC/TLS/ARF report admin objects
| Field | Value |
|---|---|
| Source | inventory §D SF-57 · ledger N/I |
| Implementation | DMARC aggregate objects + read surface COMPLETE (R-17); TLS-RPT/ARF objects absent |
| Status fields | Backend: Partial · Frontend: Verified (DMARC panel) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — TLS/ARF objects **PLANNED (Phase 3)** (with SF-26/31) |
| Evidence | pre-seed |

### SF-58 — Virtual queues, named queues, delivery-schedule objects
| Field | Value |
|---|---|
| Source | inventory §D SF-58 · ledger FULL |
| Implementation | named asynq queues (default + retention/reconcile paths), per-queue stats, R-01/R-13 delivery schedules at application level |
| Status fields | Backend: Verified · Frontend: Partial · DB: Verified · API: Verified · Security: — · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** *(verify in Phase 1)* |
| Evidence | pre-seed: `internal/worker/`, `cmd/posta/worker.go` |

### SF-59 — Operational Action objects (Reload*, InvalidateCaches, Pause/Resume)
| Field | Value |
|---|---|
| Source | inventory §D SF-59 · ledger PART |
| Implementation | queue_reconcile cron + hot settings apply (30s TTL) + admin queue actions COMPLETE; explicit Reload*/InvalidateCaches action endpoints absent |
| Status fields | Backend: Partial · Frontend: Partial · DB: — · API: Partial · Security: Verified (admin) · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** |
| Evidence | pre-seed |

## Category E — Domain management (SF-60..SF-64)

### SF-60 — Domain CRUD (aliases, catch-all, relay flag, DKIM/DNS switches)
| Field | Value |
|---|---|
| Source | inventory §E SF-60 · ledger FULL |
| Implementation | domain CRUD + ownership verification + DNS records display + DKIM signing lifecycle; alias/catch-all/relay-flag routing fields absent (inbound routing is OB-2) |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (ownership) · Tests: Verified · Docs: Verified |
| Final status | **PARTIAL** — core COMPLETE; alias/catch-all/relay routing fields BLOCKED BY ADR-002 (OB-2) |
| Evidence | pre-seed: `internal/handlers/domain_handler.go`, `Domains.vue` |

### SF-61 — Automatic DNS record publication (11 provider integrations)
| Field | Value |
|---|---|
| Source | inventory §E SF-61 · ledger PART |
| Implementation | DNS records displayed for manual publication + verification + re-check (R-07); provider integrations absent |
| Status fields | Backend: Missing (providers) · Frontend: Verified (manual flow) · DB: — · API: — · Security: — · Tests: — · Docs: Partial |
| Final status | **PARTIAL → BLOCKED BY ADR-007 (OB-6 DNS automation)** |
| Evidence | pre-seed |

### SF-62 — DKIM key lifecycle (rotation stages + scheduled rotation task)
| Field | Value |
|---|---|
| Source | inventory §E SF-62 · ledger N/I |
| Implementation | R-02 full lifecycle: generate (RSA-2048/Ed25519) → Active → Retiring → Retire → delete, verify-dns, encrypted private keys (2026-11-07 audit fixed the retire dead-end) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (encrypted keys) · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — scheduled auto-rotation task **PLANNED (Phase 3)** under SF-104 |
| Evidence | pre-seed: `internal/handlers/domain_signing_handler.go:330` (Retire), `domain_signing_key_repo.go` |

### SF-63 — DMARC/SPF/DKIM report settings + TroubleshootDmarc diagnostic
| Field | Value |
|---|---|
| Source | inventory §E SF-63 · ledger N/I |
| Implementation | R-07 health + diagnose (live DNS re-check, persisted) + R-17 aggregate read surface; report-delivery settings (rua addresses per domain, intervals) absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (settings with SF-25 send side) |
| Evidence | pre-seed |

### SF-64 — Domain health endpoint
| Field | Value |
|---|---|
| Source | inventory §E SF-64 · ledger N/I (Stalwart negative — improves on it) |
| Implementation | R-07: `GET /domains/{id}/health` (SPF/DKIM/DMARC status, signing state, bounce/complaint rates) + `POST .../health/diagnose` + Domains.vue panel |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (ownsResource) · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/handlers/domain_health_handler.go`, `internal/services/domain/health.go` |

## Category F — Identity & account management (SF-65..SF-74)

### SF-65 — Accounts (User/Group) with credentials, aliases, quotas, locale
| Field | Value |
|---|---|
| Source | inventory §F SF-65 · ledger FULL |
| Implementation | users + roles + sessions + 2FA + recovery codes + passkeys + OAuth login + language preference; **groups/aliases absent** (no mail account semantics pre-OB) |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — account aliases/groups BLOCKED BY ADR-004 (OB-4, mail-account semantics) |
| Evidence | pre-seed; Phase 1 verifies locale/plan surfaces |

### SF-66 — Granular permissions (~660) + custom Roles
| Field | Value |
|---|---|
| Source | inventory §F SF-66 · ledger PART |
| Implementation | 4-role RBAC + scoped API keys + admin middleware COMPLETE; fine-grained permission registry + custom role composition absent |
| Status fields | Backend: Partial · Frontend: Verified (role-gated UI) · DB: Partial · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (OB-6, with ADR-008 tenancy decisions)** |
| Evidence | pre-seed |

### SF-67 — API keys + app passwords (permission narrowing, IP allowlists)
| Field | Value |
|---|---|
| Source | inventory §F SF-67 · ledger FULL |
| Implementation | API keys: scopes, IP allowlists, per-key rate limits (R-15), usage analytics (R-18), query-token scoping (R-28); **app passwords absent** |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **PARTIAL** — app passwords **PLANNED (Phase 3)** (pairs SF-35) |
| Evidence | pre-seed: `internal/handlers/api_key_handler.go`, `ApiKeyDetail.vue` |

### SF-68 — OAuth grant/session administration
| Field | Value |
|---|---|
| Source | inventory §F SF-68 · ledger FULL |
| Implementation | session/device management: list, revoke, bulk revoke, new-device alerts, impersonation session model (R-20); OAuth **grant** administration absent (ReForwarder is a consumer, not a provider — SF-37) |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — grant admin **PLANNED (Phase 3)** only if the OAuth-provider decision (SF-37) lands; otherwise superseded by the provider record |
| Evidence | pre-seed; Phase 1 to reconcile the ledger's FULL claim |

### SF-69 — 2FA: TOTP (+ recovery codes, + WebAuthn beyond Stalwart)
| Field | Value |
|---|---|
| Source | inventory §F SF-69 · ledger FULL |
| Implementation | TOTP + one-time recovery codes (R-10) + WebAuthn passkeys (R-19) + `two_factor_required` wiring (R-23) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/handlers/user_handler.go`, `webauthn_handler.go`, Profile/Login UI |

### SF-70 — Admin impersonation
| Field | Value |
|---|---|
| Source | inventory §F SF-70 · ledger N/I |
| Implementation | R-20: reauth + reason, `impersonated_by` claims, session revoke + Redis blacklist + cookie clear, audit events, banner UI |
| Status fields | Backend: Verified · Frontend: Verified (banner) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/handlers/impersonation_handler.go`, `ImpersonationBanner.vue` |

### SF-71 — SCIM 2.0 provisioning [ENT]
| Field | Value |
|---|---|
| Source | inventory §F SF-71 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-008 (OB-6 revisit)** |
| Evidence | pre-seed |

### SF-72 — Tenants (object + quotas + scoped admin views) [ENT]
| Field | Value |
|---|---|
| Source | inventory §F SF-72 · ledger FULL |
| Implementation | workspaces = tenant analog: scoped views, per-workspace isolation on every endpoint, admin scope; distinct tenant objects + tenant quotas absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — tenant objects/quotas BLOCKED BY ADR-008 (OB-6) |
| Evidence | pre-seed; Phase 1 reconciles the FULL claim |

### SF-73 — Account destruction task (staged, grantee cleanup)
| Field | Value |
|---|---|
| Source | inventory §F SF-73 · ledger FULL |
| Implementation | GDPR export + deletion flows; staged destruction task with grantee cleanup to be verified |
| Status fields | Backend: Partial *(verify)* · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — Phase 1 verifies deletion coverage; residual requirements → PLANNED (Phase 3) |
| Evidence | pre-seed |

### SF-74 — Admin audit history
| Field | Value |
|---|---|
| Source | inventory §F SF-74 · ledger FULL (Stalwart negative — ReForwarder improves) |
| Implementation | shared `events` table + audit logger across admin actions, SSE + CSV export, scope=deployment admin-only |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/handlers/event_handler.go`, admin Events UI |

## Category G — Storage & message management (SF-75..SF-83)

### SF-75 — Three-plane storage (data + blob + lookup + search + registry)
| Field | Value |
|---|---|
| Source | inventory §G SF-75 · ledger PART |
| Implementation | data plane = Postgres; blob plane = `blob.Store` (FS/S3); lookup plane = Redis (limits, bans, challenges); search plane = SQL ILIKE (SF-79); registry = settings table |
| Status fields | Backend: Partial · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — search-plane depth BLOCKED BY ADR-003 (OB-3); Phase 1 verifies the plane map |
| Evidence | pre-seed |

### SF-76 — Data-store backends (RocksDB/SQLite/Postgres/MySQL/FDB/HTTP KV)
| Field | Value |
|---|---|
| Source | inventory §G SF-76 · ledger PART |
| Implementation | Postgres as the single data store (documented architecture decision — "managed Postgres HA" per the do-not-implement table) |
| Status fields | Backend: Partial (single backend by design) · others: — |
| Final status | **PARTIAL → BLOCKED BY ADR-009 (OB-6)** — alternative backends only under an approved ADR decision |
| Evidence | pre-seed |

### SF-77 — Blob stores (FS/S3; dedup, ref-count, orphan GC)
| Field | Value |
|---|---|
| Source | inventory §G SF-77 · ledger FULL |
| Implementation | `blob.Store` FS + S3; **dedup/ref-count/orphan GC absent** (GAP-45 open) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (GAP-45 usage reporting + orphan GC) |
| Evidence | pre-seed: `internal/storage/blob/` |

### SF-78 — Blob at-rest encryption (zstd + AES-256-GCM/ChaCha20)
| Field | Value |
|---|---|
| Source | inventory §G SF-78 · ledger N/I |
| Implementation | AES-256-GCM encryption for credentials/signing keys (`internal/services/crypto`); blob-level at-rest encryption absent |
| Status fields | Backend: Partial · others: — |
| Final status | **PARTIAL** — blob encryption **BLOCKED BY ADR-003 (OB-3/6)** |
| Evidence | pre-seed |

### SF-79 — Search (inverted index + Elasticsearch/Meilisearch; async reindex)
| Field | Value |
|---|---|
| Source | inventory §G SF-79 · ledger PART |
| Implementation | SQL ILIKE search across subscribers/contacts/logs (documented sufficient at current scale); inverted-index/external search engines absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — index stack **BLOCKED BY ADR-003 (OB-3 mailstore search)** |
| Evidence | pre-seed |

### SF-80 — Quotas (per-account disk + count quotas)
| Field | Value |
|---|---|
| Source | inventory §G SF-80 · ledger PART |
| Implementation | plan/workspace limits at control-plane level (verify Phase 1); per-account disk/count quota machinery absent |
| Status fields | Backend: Partial · Frontend: Partial · DB: Partial · API: Partial · Security: — · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — account quotas BLOCKED BY ADR-003 (OB-3) |
| Evidence | pre-seed |

### SF-81 — Retention/expiration (DataRetention settings, cron purges)
| Field | Value |
|---|---|
| Source | inventory §G SF-81 · ledger FULL |
| Implementation | backup retention sweep (R-11); message/event retention partial — **`security_events` retention caller (DeleteOlderThan) unwired (GAP-13)** |
| Status fields | Backend: Partial · Frontend: Partial (settings) · DB: Verified · API: Partial · Security: — · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (GAP-13 caller + retention settings surface) |
| Evidence | pre-seed |

### SF-82 — Backup/restore (stop-and-dump CLI, LZ4)
| Field | Value |
|---|---|
| Source | inventory §G SF-82 · ledger N/I |
| Implementation | R-11: pg_dump gzip CronJob into `POSTA_BACKUP_DIR` with retention sweep + restore runbook; restore test verified in the DB-tier battery |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Verified (runbook) |
| Final status | **COMPLETE** — equivalent mechanism (online dump cron) documented vs Stalwart's offline CLI |
| Evidence | pre-seed: `internal/cron/jobs/backup.go` |

### SF-83 — Compression (zstd with per-type dictionaries)
| Field | Value |
|---|---|
| Source | inventory §G SF-83 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-003 (OB-3)** — gzip used where it exists today (backups, DMARC) |
| Evidence | pre-seed |

## Category H — APIs & integrations (SF-84..SF-91)

### SF-84 — JMAP server (core/mail/submission/…)
| Field | Value |
|---|---|
| Source | inventory §H SF-84 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-004/005 (OB-4; revisit OB-6)** — REST/OpenAPI covers API needs today (RF-B126 evaluation pending) |
| Evidence | pre-seed |

### SF-85 — IMAP4rev1/rev2 server
| Field | Value |
|---|---|
| Source | inventory §H SF-85 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-004 (OB-4)** |
| Evidence | pre-seed |

### SF-86 — POP3 + ManageSieve servers
| Field | Value |
|---|---|
| Source | inventory §H SF-86 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-004 (OB-4)** |
| Evidence | pre-seed |

### SF-87 — Management API (REST/OpenAPI)
| Field | Value |
|---|---|
| Source | inventory §H SF-87 · ledger FULL |
| Implementation | REST API with OpenAPI spec + route contract test pinning 357 routes against `docs/implementation/api_routes.txt` |
| Status fields | Backend: Verified · Frontend: Verified (37 api modules) · DB: — · API: Verified · Security: Verified · Tests: Verified (contract) · Docs: Verified (OpenAPI) |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/routes/contract_test.go` |

### SF-88 — API auth (Basic/Bearer, keys w/ narrowing + IP allowlists, rate limiter)
| Field | Value |
|---|---|
| Source | inventory §H SF-88 · ledger FULL |
| Implementation | bearer/JWT + API keys (scopes, IP allowlists, R-15 rate limits, R-18 usage) + R-28 query-token scoping to SSE |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — Retry-After header absent (recorded limitation, audit 2026-11-07) |
| Evidence | pre-seed: `internal/middlewares/middleware.go` |

### SF-89 — Web Push (RFC 8030/8291/8292 VAPID) + SSE + WebSocket push
| Field | Value |
|---|---|
| Source | inventory §H SF-89 · ledger PART |
| Implementation | SSE streams COMPLETE (messages, inbound, admin events/metrics/security); Web Push + WebSocket push absent |
| Status fields | Backend: Partial · Frontend: Verified (SSE consumers) · DB: — · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL** — Web Push **PLANNED (Phase 3)**; WebSocket push **PLANNED (Phase 3)** only where a consumer exists |
| Evidence | pre-seed |

### SF-90 — Outbound telemetry webhooks (HMAC-signed event batches)
| Field | Value |
|---|---|
| Source | inventory §H SF-90 · ledger FULL |
| Implementation | HMAC-signed webhook endpoints (per-endpoint secrets, signature verification), delivery-attempt history, exponential backoff + jitter (R-06), audited SSRF-guarded replay, automation webhooks on the shared guard |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (SSRF guard) · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — batching (event-batch payloads) verified in Phase 1 |
| Evidence | pre-seed: `internal/services/webhook/` (ssrf.go, dispatcher) |

### SF-91 — Lookup stores (Redis/HTTP/memory/SQL) for Sieve/rate-limits/traps
| Field | Value |
|---|---|
| Source | inventory §H SF-91 · ledger PART |
| Implementation | Redis + in-memory + SQL stores power rate limiting (login + per-key), bans, challenges, session blacklist; Sieve/trap consumers absent (ADR-006) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — trap/Sieve consumers BLOCKED BY ADR-006; store abstraction **COMPLETE** (equivalent) |
| Evidence | pre-seed |

## Category I — Monitoring & observability (SF-92..SF-96)

### SF-92 — Event/metrics core
| Field | Value |
|---|---|
| Source | inventory §I SF-92 · ledger FULL |
| Implementation | Prometheus families for campaigns/automations/queues/relay + per-key usage analytics (R-12/R-18); registration + increment tests added 2026-11-07 |
| Status fields | Backend: Verified · Frontend: Verified (Jobs metrics card) · DB: — · API: Verified · Security: — · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/metrics/metrics.go`, `metrics_test.go` |

### SF-93 — Prometheus/OpenMetrics endpoint + OTLP push exporter
| Field | Value |
|---|---|
| Source | inventory §I SF-93 · ledger FULL |
| Implementation | `/metrics` OpenMetrics endpoint (gated by `POSTA_METRICS_ENABLED`, default-off documented); **OTLP push exporter absent** (GAP-51 adjacent) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: Verified · Security: — · Tests: Verified · Docs: Verified |
| Final status | **PARTIAL** — OTLP **PLANNED (OB-6)** (GAP-51/52 observability completion) |
| Evidence | pre-seed: `internal/routes/routes.go` metrics gate |

### SF-94 — Structured log sinks (console/files/journald/OTLP/webhooks/store)
| Field | Value |
|---|---|
| Source | inventory §I SF-94 · ledger PART |
| Implementation | structured logging (jkaninda/logger) to console with request-scoped fields; file/journald/OTLP/webhook sinks absent |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: — · Tests: — · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (configurable sinks) |
| Evidence | pre-seed |

### SF-95 — Health endpoints (/healthz/live + /healthz/ready)
| Field | Value |
|---|---|
| Source | inventory §I SF-95 · ledger FULL |
| Implementation | `/healthz` + `/readyz` (server) + worker `/healthz`; compose healthchecks use them |
| Status fields | Backend: Verified · Frontend: — · DB: — · API: Verified · Security: — · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/routes/auth_routes.go`, `internal/worker/health.go` |

### SF-96 — Metric alerts (condition→action) [ENT] + metrics/trace history
| Field | Value |
|---|---|
| Source | inventory §I SF-96 · ledger N/I |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (OB-6)** — with GAP-51/52 (error-monitoring hook, Grafana dashboard) |
| Evidence | pre-seed |

## Category J — Administration (SF-97..SF-100)

### SF-97 — Admin UI
| Field | Value |
|---|---|
| Source | inventory §J SF-97 · ledger FULL (Stalwart's is external; ReForwarder embeds) |
| Implementation | Vue 3 dashboard: full admin surface (users, workspaces, queue admin, security, settings, announcements, provider events, system info) |
| Status fields | Backend: — · Frontend: Verified · DB: — · API: Verified · Security: Verified (admin middleware) · Tests: Partial (vitest) · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `web/src/views/admin/` |

### SF-98 — Configuration = typed registry objects
| Field | Value |
|---|---|
| Source | inventory §J SF-98 · ledger FULL |
| Implementation | settings table + typed accessors + seeder + live-apply 30s TTL cache + admin Settings UI (documented equivalent of Stalwart's registry objects) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/config/`, `internal/storage/repositories/settings*` |

### SF-99 — Operational controls + task manager with scheduler-role gating
| Field | Value |
|---|---|
| Source | inventory §J SF-99 · ledger PART |
| Implementation | 8 registered cron jobs + admin queue operations + R-13 automation schedules; task-manager visibility UI (next runs, history) absent |
| Status fields | Backend: Partial · Frontend: Partial · DB: Verified · API: Partial · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (task-manager surface) |
| Evidence | pre-seed: `internal/cron/` |

### SF-100 — Configuration history/rollback
| Field | Value |
|---|---|
| Source | inventory §J SF-100 · ledger N/I (Stalwart negative) |
| Implementation | audit events record settings changes; no history/rollback surface |
| Status fields | Backend: Missing · Frontend: Missing · DB: Partial (audit exists) · API: Missing · Security: — · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** — settings history + rollback on the existing events substrate |
| Evidence | pre-seed |

## Category K — Automation & extensibility (SF-101..SF-105)

### SF-101 — Sieve system + user scripts at every SMTP stage and delivery
| Field | Value |
|---|---|
| Source | inventory §K SF-101 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2)** — documented alternative: automation engine (visual builder, 9 node types, triggers incl. cron R-13, per-subscriber journeys) |
| Evidence | pre-seed |

### SF-102 — Sieve plugin functions [ENT]
| Field | Value |
|---|---|
| Source | inventory §K SF-102 · ledger PART |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-6)** |
| Evidence | pre-seed |

### SF-103 — Expression language reused everywhere
| Field | Value |
|---|---|
| Source | inventory §K SF-103 · ledger PART |
| Implementation | three condition currencies (FilterRule, SegmentCondition SQL builder, automation conditions incl. `in_segment`) — individually COMPLETE; single unified evaluator absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (guarded SQL) · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (evaluator unification, pairs SF-50) |
| Evidence | pre-seed |

### SF-104 — Scheduled task system (ACME, DKIM rotation, reports, purges)
| Field | Value |
|---|---|
| Source | inventory §K SF-104 · ledger PART |
| Implementation | cron framework (8 jobs) + campaign scheduler (R-01) + automation schedule triggers (R-13) + backup cron (R-11) + reconcile cron; **scheduled DKIM rotation and scheduled report-generation tasks absent** |
| Status fields | Backend: Partial · Frontend: Partial · DB: Verified · API: Partial · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (DKIM rotation task; report tasks with SF-25 send) |
| Evidence | pre-seed: `internal/cron/` |

### SF-105 — Cluster broadcast fabric (Redis/NATS/Zenoh/Kafka)
| Field | Value |
|---|---|
| Source | inventory §K SF-105 · ledger N/A |
| Implementation | Redis pub/sub for ban propagation (single-fabric equivalent); multi-node fabric absent |
| Status fields | Backend: Partial · others: — |
| Final status | **BLOCKED BY ADR-009 (OB-6)** |
| Evidence | pre-seed |

## Category L — Performance & reliability (SF-106..SF-110)

### SF-106 — Store-level queue failover with distributed event locks
| Field | Value |
|---|---|
| Source | inventory §L SF-106 · ledger PART |
| Implementation | single-node: CAS status claims + `FOR UPDATE SKIP LOCKED` batch claims (R-26) + asynq task dedup COMPLETE; multi-node store failover absent |
| Status fields | Backend: Partial · Frontend: — · DB: Verified · API: — · Security: — · Tests: Verified (exactly-once) · Docs: Partial |
| Final status | **PARTIAL** — failover **BLOCKED BY ADR-009 (OB-6)** |
| Evidence | pre-seed: `internal/worker/atomic_claim_test.go`, `campaign_message_repo.go:83` |

### SF-107 — Cache layers (TTL, negative caching, invalidation, snapshots)
| Field | Value |
|---|---|
| Source | inventory §L SF-107 · ledger FULL |
| Implementation | settings 30s TTL live-apply, security-engine COW snapshot + Redis pub/sub propagation, domain/verification caches |
| Status fields | Backend: Verified · Frontend: — · DB: — · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — cluster invalidation across nodes BLOCKED BY ADR-009 |
| Evidence | pre-seed: `internal/services/security/`, settings provider |

### SF-108 — Backpressure (queue caps, memory budgets, transfer limits, throttles)
| Field | Value |
|---|---|
| Source | inventory §L SF-108 · ledger PART |
| Implementation | bounded retry sweeps + batch limits + jitter (R-27); asynq concurrency; memory budgets and transfer throttles absent |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — memory budgets **PLANNED (Phase 3)**; MTA-scale throttles BLOCKED BY ADR-001 (OB-1) |
| Evidence | pre-seed |

### SF-109 — Graceful shutdown (systemd notify, snapshot flush)
| Field | Value |
|---|---|
| Source | inventory §L SF-109 · ledger N/V — **resolving**: shutdown is owned by okapi v1.1.0 with repo `OnShutdown` hooks; worker drains queues |
| Status fields | Backend: Verified (okapi-owned) · others: — |
| Final status | **COMPLETE** — equivalent mechanism documented (okapi lifecycle vs systemd notify); Phase 1 cites the drain path |
| Evidence | pre-seed: `cmd/posta/server.go` |

### SF-110 — Hot reload of settings/certs/lookup stores/blocked IPs
| Field | Value |
|---|---|
| Source | inventory §L SF-110 · ledger FULL |
| Implementation | settings live-apply (30s TTL), security rules live-apply, ban propagation via pub/sub, provider accessors; cert reload N/A (ACME absent, ADR-007) |
| Status fields | Backend: Verified · Frontend: Verified (Rules UI) · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/services/security/` rules apply |

## Category M — Other capabilities (SF-111..SF-115)

### SF-111 — CalDAV/CardDAV/WebDAV groupware
| Field | Value |
|---|---|
| Source | inventory §M SF-111 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-005 (OB-6 revisit)** — user directive: in scope, sequenced after the ADR gate |
| Evidence | pre-seed |

### SF-112 — File storage (JMAP FileNode + text extraction)
| Field | Value |
|---|---|
| Source | inventory §M SF-112 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-003 (OB-3/6)** — email attachment handling exists in the send/inbound pipelines |
| Evidence | pre-seed |

### SF-113 — nlp crate (ML classifier, language detection, CJK tokenizers)
| Field | Value |
|---|---|
| Source | inventory §M SF-113 · ledger N/A |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-006 (OB-2/6)** |
| Evidence | pre-seed |

### SF-114 — Deployment tooling (installer, Docker bake, systemd, AppArmor)
| Field | Value |
|---|---|
| Source | inventory §M SF-114 · ledger FULL |
| Implementation | multi-stage Dockerfile (node→golang→alpine), compose + override, Makefile, multi-arch release workflow (amd64/arm64), GitHub Pages docs; systemd/AppArmor units absent (documented equivalent: Docker) |
| Status fields | Backend: — · Frontend: — · DB: — · API: — · Security: — · Tests: Verified (CI) · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `docker/Dockerfile`, `.github/workflows/release.yml` |

### SF-115 — Schema-version migration guard at boot
| Field | Value |
|---|---|
| Source | inventory §M SF-115 · ledger FULL |
| Implementation | upgrade framework: registry steps, sealed-registry vs fresh-install handling, per-release upgrade notes; migration smoke test (DSN-gated) |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | pre-seed: `internal/storage/migration/`, `18df4be` fix |

## Appendix — Capability-audit-unique requirements (CA records)

Requirements documented in `docs/research/reforwarder-capability-audit.md` that the SF universe does not carry. IDs are new and stable (CA-nn).

### CA-01 — Compose UI for transactional sending
| Field | Value |
|---|---|
| Source | capability audit §1.1 ("API-only by design — a compose screen would be a product decision") |
| Status fields | Backend: Verified (API exists) · Frontend: Missing · DB: — · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PLANNED (OB-5 webmail/compose)** |
| Evidence | pre-seed |

### CA-02 — Backend endpoints without frontend consumers
| Field | Value |
|---|---|
| Source | capability audit §2.3: `POST /campaigns/{id}/duplicate`, `GET /emails/{id}/status`, `GET /admin/api-keys`, `GET /admin/emails` (+ dead client functions flagged by RF-F103) |
| Status fields | Backend: Verified · Frontend: Missing · DB: — · API: Verified · Security: Verified · Tests: Partial · Docs: Partial |
| Final status | **PARTIAL — PLANNED (Phase 3)** (wire the UI or retire the dead client functions) |
| Evidence | pre-seed |

### CA-03 — Refresh-token rotation
| Field | Value |
|---|---|
| Source | capability audit §1.3 |
| Status fields | Backend: Missing · Frontend: — · DB: Partial (sessions exist) · API: — · Security: Missing · Tests: Missing · Docs: Missing |
| Final status | **MISSING → PLANNED (Phase 3)** (security hardening on the existing session substrate) |
| Evidence | pre-seed |

### CA-04 — SMTP client negotiation (SMTPUTF8/8BITMIME) + per-message throttling
| Field | Value |
|---|---|
| Source | capability audit §1.1 |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-001 (OB-1)** — outbound client negotiation belongs to the MTA foundation; per-message throttle PLANNED (Phase 3) |
| Evidence | pre-seed |

### CA-05 — VERP / bounce mailbox
| Field | Value |
|---|---|
| Source | capability audit §1.1; todo.md reinstates INF-020 under Option B |
| Status fields | Backend: Missing · others: — |
| Final status | **BLOCKED BY ADR-002 (OB-2 return-path processing)** |
| Evidence | pre-seed |

### CA-06 — Fallback broker in dev
| Field | Value |
|---|---|
| Source | capability audit §1.11 (HTTP send falls back to synchronous when Redis is down) |
| Status fields | Backend: Partial · others: — |
| Final status | **PLANNED (Phase 3)** — dev-experience queue fallback |
| Evidence | pre-seed |

### CA-07 — OpenTelemetry export
| Field | Value |
|---|---|
| Source | capability audit (twice noted) |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (OB-6)** with SF-96/GAP-51 |
| Evidence | pre-seed |

### CA-08 — Segment audiences skip per-list unsubscribes (by design)
| Field | Value |
|---|---|
| Source | capability audit §1.5 gap 4 |
| Status fields | Backend: Verified (documented design) · Frontend: — · DB: — · API: — · Security: Verified (global suppressions apply) · Tests: Partial · Docs: Missing |
| Final status | **COMPLETE (as designed)** — behavior documented; the doc note is added in Phase 6 |
| Evidence | pre-seed |

## Pre-seed tally (to be finalized by the Phase 1 verification waves)

| Final status | Count | IDs |
|---|---|---|
| COMPLETE | 24 | SF-04, 20, 36, 38, 44, 58, 62, 64, 69, 70, 74, 82, 87, 88, 90, 92, 95, 97, 98, 107, 109, 110, 114, 115 |
| PARTIAL | 43 | SF-03, 07, 08, 09, 13, 16, 25, 33, 35, 37, 43, 49, 50, 52, 53, 54, 56, 57, 59, 60, 63, 65, 66, 67, 68, 72, 73, 75, 77, 78, 79, 80, 81, 89, 91, 93, 94, 99, 103, 104, 106, 108 |
| MISSING → PLANNED | 7 | SF-10, 22, 23, 24, 26, 96, 100 |
| BLOCKED BY ADR | 41 | SF-01, 02, 05, 06, 11, 12, 14, 15, 17, 18, 19, 21, 27, 28, 29, 30, 31, 32, 34, 39, 40, 41, 42, 45, 46, 47, 48, 51, 55, 61, 71, 76, 83, 84, 85, 86, 105, 111, 112, 113 — plus CA-04, CA-05 |
| N/A | 0 | — (converted per the programme decision; every former N/A carries its ADR/alternative record) |

**Phase-3 actionable pool (pre-seed):** the PARTIAL remaining-requirements + MISSING→PLANNED records whose remaining scope does not depend on an ADR — approximately 20 discrete work items (inbound SPF/DKIM/DMARC verification SF-22/23/24, ARF SF-26, DSN generation SF-10, queue patch/pause/tracing SF-52/54/56, taxonomy surfacing SF-53, settings history SF-100, retention caller SF-81/GAP-13, blob GC SF-77/GAP-45, log sinks SF-94, operational actions SF-59, DKIM rotation task SF-104, header A-R injection SF-49, refresh-token rotation CA-03, dead-endpoint wiring CA-02, app passwords SF-35/67, expression unification SF-50/103, dev fallback broker CA-06, Web Push SF-89, memory budgets SF-43/108). Each is confirmed or refuted by Phase 1 before implementation.

## Verification plan (Phase 1 waves)

- **Wave V1..V10** — ~10 auditors, chunked by category, verify every record against the code (route→logic→persistence→isolation→tests), upgrade/downgrade statuses with `path:line` evidence, and resolve the two former N/V items (SF-11, SF-109) and the ledger-claim reconciliations (SF-37/68/72/73 FULL claims).
- **Wave R** — spot re-check of the 27 R-delivered records against the 2026-11-07 acceptance-audit evidence (commit `27c418a`).
- Every status change is written back here with evidence; the tally table is then frozen as the audit's official statistics.

## Resumability

This register is the programme's persistent tracker: after every wave, statuses, evidence, and the tally are updated in place. Any interrupted phase resumes from the register without loss of progress. Programme-level phase tracking lives in `todo.md` Track A and `docs/implementation/ROADMAP_IMPLEMENTATION.md`.






