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
| Implementation | Wave OB-1: first-party outbound ESMTP client (`internal/services/mta/session.go:100` runSession — EHLO / STARTTLS-when-offered / MAIL / RCPT / DATA over net/smtp, certificate verification always on, per-domain source bind `:124`) + gated 587 MSA (`internal/services/mta/submission.go:45-53,187-219`); relay path (`internal/services/email/smtp_sender.go`) and the feature-gated inbound receiver (`internal/services/inbound/`) unchanged. LMTP (LHLO), VRFY and EXPN absent (grep over `internal/services/mta/` + `internal/services/inbound/`, 2026-11-07) |
| Status fields | Backend: Partial (client + MSA real; no LMTP/VRFY/EXPN) · Frontend: — · DB: Verified (mta_delivery_attempts, models/mta_delivery_attempt.go:50) · API: Verified (PATCH /domains/{id}/delivery_mode) · Security: Verified (session guards, no open relay) · Tests: Verified (mta package in the services battery) · Docs: Partial |
| Final status | **PARTIAL** — ESMTP client + MSA landed (Wave OB-1); LMTP/VRFY/EXPN remain **PLANNED (OB-1 remainder / ADR-002 receiver)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-02 — ESMTP extension advertisement
| Field | Value |
|---|---|
| Source | inventory §A SF-02 · ledger N/A |
| Requirement | PIPELINING/CHUNKING/8BITMIME/SMTPUTF8/DSN/REQUIRETLS/FUTURERELEASE advertisement |
| Implementation | Wave OB-1: outbound client negotiates and records PIPELINING/8BITMIME/SMTPUTF8 (+AUTH, STARTTLS-upgrade flag) on the session result (`internal/services/mta/session.go:153-156`, SessionCaps `:46-48`; carried on DeliveryResult `deliver.go:90,243`); a non-ASCII envelope is refused when the remote does not advertise SMTPUTF8 (`session.go:181-188`); receivers advertise SMTPUTF8 (`internal/services/inbound/smtp_server.go:205`, MSA `submission.go:196`). REQUIRETLS, DSN and CHUNKING advertisement absent (grep, 2026-11-07) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: — · Tests: Verified (session_test.go negotiation matrix) · Docs: Partial |
| Final status | **PARTIAL** — PIPELINING/8BITMIME/SMTPUTF8 negotiated + recorded on the session result (Wave OB-1); REQUIRETLS/DSN remain **PLANNED (OB-1 remainder)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-03 — Submission (MSA) with delivery-status writeback
| Field | Value |
|---|---|
| Source | inventory §A SF-03 · ledger N/A |
| Requirement | Authenticated submission port with status writeback |
| Implementation | HTTP API send (`POST /api/v1/emails/send|batch|send-template`) with event writeback (`email_events`) + Wave OB-1 SMTP MSA: gated go-smtp submission listener on 587 (`internal/services/mta/submission.go:45-53,187-219`; `POSTA_SUBMISSION_ENABLED` default off, `internal/config/config.go:514-524`), PLAIN AUTH over relay credentials (bcrypt + rehash + IP allowlist, `submission.go:74-91`), fail-closed sender-domain tenant check (`submission.go:299-307`), accepted messages enter the same Send pipeline — claim/sign/deliver/record, `submission.go:324-378` — so the event writeback applies |
| Status fields | Backend: Verified · Frontend: — · DB: Verified (events + mta_delivery_attempts) · API: Verified · Security: Verified (no open relay; construction refuses a missing guard, `submission.go:151-164`) · Tests: Verified (submission_test.go in the services battery) · Docs: Partial (POSTA_SUBMISSION_* not yet in configuration.md) |
| Final status | **COMPLETE** — 587 MSA landed (Wave OB-1) on the existing event-writeback pipeline; HTTP analog unchanged |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-04 — Relay control / open-relay prevention
| Field | Value |
|---|---|
| Source | inventory §A SF-04 · ledger N/A |
| Requirement | Authenticated relaying only; no open relay |
| Implementation | Relay credentials bcrypt + constant-time (R-25) + IP allowlists; security engine SMTP/HTTP enforcement; inbound receiver domain-verified; allowlist + auth-gate direct tests (Wave P3-4) |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: Verified (smtp-credentials CRUD/revoke, workspace_resource_routes.go:219-267) · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — the V-A1 test-coverage residual closed (Wave P3-4): direct unit tests in `internal/middlewares/ipallowed_test.go` — TestIPAllowed (19-case matrix: empty allowlist / exact-IP v4+v6 / single & multiple entries / CIDR v4+v6 / malformed entries must not panic or open the door), TestAPIKeyAuth_MissingKeyPaths (the API-key gate's 401 paths: no header, empty bearer, JWT-shaped bearer, Basic scheme, query-token on a plain route), TestQueryTokenAllowedMatrix (R-28: `?token=` reaches the key path only on the four SSE streams), TestAuthenticate_MissingCredentialMessage (the "authentication required" gate at `internal/middlewares/middleware.go:253`) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-05 — Routing strategies (Local / Mx / Relay via expressions)
| Field | Value |
|---|---|
| Source | inventory §A SF-05 · ledger N/A |
| Requirement | Per-domain routing strategy selection |
| Implementation | Wave OB-1: per-domain `delivery_mode` selects the executor — `relay` (default) or `direct`-to-MX (`internal/models/domain.go:29-30,48-53`; `PATCH /domains/{id}/delivery_mode` with direct requiring ownership verification, `internal/handlers/domain_handler.go:80-98`; worker branch `internal/worker/handler.go:223-227,527-539` — fail-closed when the worker lacks the MTA executor). Local delivery absent (mailbox model is ADR-003) |
| Status fields | Backend: Partial (two of three strategies) · Frontend: Verified (client+router+views fully wired, V-A1) · DB: Verified · API: Verified · Security: Verified (fail-closed unverified-domain refusal, `deliver.go:147-158`) · Tests: Verified (TestDeliveryMode* suite in the handlers battery) · Docs: Partial |
| Final status | **PARTIAL** — relay \| direct per domain = two of the reference platform's three strategies (Wave OB-1); local delivery **PLANNED (ADR-003 accepted · OB-3)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. |

### SF-06 — MX handling (preference, shuffle, null-MX, implicit MX, DNSSEC)
| Field | Value |
|---|---|
| Source | inventory §A SF-06 · ledger N/A |
| Requirement | MX resolution behaviors for direct delivery |
| Implementation | Wave OB-1 direct-to-MX resolver: preference-ordered hosts with equal-preference-group shuffle (RFC 5321 §5.1 guidance, `internal/services/mta/resolver.go:260-278`), implicit MX — no records → A/AAAA of the domain (RFC 5321 §5.1, `resolver.go:218-222`), null MX (RFC 7505) honored with the ADR-001-recorded deviation — A/AAAA fallback instead of refusal (`resolver.go:210-216`); TTL cache that never caches DNS failures (`resolver.go:191-196`) |
| Status fields | Backend: Verified · Frontend: — · DB: — · API: — · Security: Partial (no DNSSEC validation) · Tests: Verified (resolver_test.go in the services battery) · Docs: Partial |
| Final status | **COMPLETE** — preference / shuffle / null-MX / implicit MX landed (Wave OB-1); DNSSEC-validated resolution absent, recorded as the limitation (DNSSEC/DANE stays SF-30, OB-1/6) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. Wave OB-1 (2026-11-07, uncommitted at 02666cc): resolver.go lines cited above; re-verified this session. |

### SF-07 — Outbound relays (SASL, TLS modes, source-IP pools, pipelining)
| Field | Value |
|---|---|
| Source | inventory §A SF-07 · ledger PART |
| Requirement | Configurable outbound relays with auth, TLS modes, source-IP pools, pipelining |
| Implementation | SMTP server config + relay creds (R-25) + dial timeout/deadlines (R-22); Wave OB-1: per-domain outbound bind IP (`internal/models/domain.go:49-54` OutboundBindIP → `internal/worker/handler.go:560` → `internal/services/mta/session.go:124` DialSMTPWithBind) + per-domain sending budgets (`internal/services/mta/policy.go:33-38,88-148`); source-IP pools absent |
| Status fields | Backend: Partial · Frontend: Verified (server config) · DB: Verified · API: Verified · Security: Verified (bcrypt) · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — bind IP + per-domain budgets landed (Wave OB-1); source-IP pools **PLANNED (OB-6 pool expansion, ADR-001/007)** |
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
| Final status | **PARTIAL** — application-level analog verified; OB-1 landed (2026-11-07) without SMTP-protocol HOLDFOR/HOLDUNTIL/DELIVERBY/DSN params, which remain **PLANNED (OB-1 remainder)** |
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
| Implementation | Wave OB-2 (W1+W2+W7): workspace-scoped `inbound_routes` (pattern exact-address \| `@domain` catch-all \| sub-address spec; actions deliver/forward/webhook/quarantine; priority lower-first) + durable `inbound_forward_tasks` queue (unique per inbound email; additive migration) — `internal/models/inbound_route.go`, `internal/models/inbound_forward_task.go`, repos `internal/storage/repositories/inbound_route_repo.go` / `inbound_forward_task_repo.go` (parameterized throughout). Routing engine `internal/services/inbound/router.go:90` ResolveRecipient: first-match-wins by priority, exact beats a higher-priority catch-all, `user+tag@host` resolves to the base rule, recipient host must equal the caller-verified domain (fail closed), unknown recipient → explicit 550 default-reject. RCPT-time validation before responsibility is accepted (`internal/services/inbound/smtp_server.go:192,244` — 550 5.1.1 no matching route; store failure 451 4.4.3); post-ingest execution after durable persist (`internal/services/inbound/route_action.go:96`). Forward executor `internal/services/inbound/forwarder.go:285` RunOnce — R-26 CAS claim, system_forward re-send copy stamped `provenance=system_forward` (`internal/models/email.go:44`), `forwarded` email_event, standard backoff retry budget; loop/abuse protection: MaxForwardHops=3 with X-Posta-Forwarded-For hop accounting (`forwarder.go:70`), per-target hourly/daily rate caps, confirmation gate (`inbound_forward_targets` + single-use `GET /inbound/forward-confirm/{token}`, `internal/routes/inbound_forward_routes.go:79`); the original inbound row is never deleted or rewritten (durable-first). Dashboard rules/forward-target UI `web/src/views/inbound/InboundRoutes.vue` |
| Status fields | Backend: Verified · Frontend: Partial (rules + forward-targets UI landed; backend rules-CRUD handler unregistered — see residual) · DB: Verified (inbound_routes, inbound_forward_tasks, inbound_forward_targets — all additive) · API: Partial (forward-targets + confirm routes landed and contract-golden; `/inbound-routes` CRUD recorded KNOWN_DRIFT in `web/src/api/admin.spec.ts`) · Security: Verified (confirmation gate, hop limit, rate caps; SSRF/HMAC webhook seams untouched) · Tests: Verified (21 W1 + 17 W2 named tests; docker battery exit 0) · Docs: Partial (STATUS_HISTORY wave notes) |
| Final status | **COMPLETE** — forwarding chain via the documented no-Sieve routing evaluator (Wave OB-2): sub-addressing/catch-all/aliases = `inbound_routes` + durable forward executor. Recorded residuals (honest, not silent): the per-rules REST CRUD handler is not yet registered (the Routes UI is spec-tested against repo conventions, KNOWN_DRIFT until it lands — one integration pass), and list expansion (mailing lists) is not adopted — routes carry no list semantics |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A1.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W1/W2/W7). |

### SF-12 — Envelope rewriting (MAIL FROM / RCPT TO)
| Field | Value |
|---|---|
| Source | inventory §A SF-12 · ledger N/I |
| Implementation | Wave OB-1: MAIL FROM validated + enforced on the MSA (non-empty + `mail.ParseAddress`, `internal/services/mta/submission.go:290-297`; sender domain must be tenant-verified, `:299-307`); outbound envelope derived via `email.EnvelopeAddress` (`internal/services/email/smtp_sender.go:390-393`). No rewriting (VERP/SRS) |
| Status fields | Backend: Partial (validation real; rewriting absent) · Frontend: — · DB: — · API: — · Security: Verified · Tests: Verified (submission_test.go) · Docs: Missing |
| Final status | **PARTIAL** — MAIL FROM validation landed (Wave OB-1); envelope rewriting (VERP/SRS) **PLANNED under ADR-002 (OB-2, CA-05)** |
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
| Final status | **PARTIAL** — business-event hooks verified; OB-1 landed (2026-11-07) without per-SMTP-stage (EHLO/MAIL/RCPT) hooks (no hook dispatch under `internal/services/mta/`, grep) — **PLANNED (ADR-001/002 later slices)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

### SF-17 — Queue quota reservations
| Field | Value |
|---|---|
| Source | inventory §A SF-17 · ledger N/A |
| Implementation | Wave OB-1 per-domain sending policy: hourly budget — sliding window, exhaustion defers 421-style with jitter (`internal/services/mta/policy.go:56-58,88-148`, enforced `deliver.go:174-188`) — and max-messages-per-connection (default 10, per-domain override, `policy.go:47-51,73-75`; binds once batch sending lands), columns on the domain row (`internal/models/domain.go:55-56`). Size quotas absent |
| Status fields | Backend: Partial · Frontend: — (no domain-form fields yet, grep over web/src) · DB: Verified (domain columns) · API: Partial (values ride the domain row; delivery-mode PATCH verified) · Security: — · Tests: Verified (policy tests in the services battery) · Docs: Partial |
| Final status | **PARTIAL** — hourly budget + msgs/connection landed (Wave OB-1); size quotas **PLANNED (OB-1 remainder/OB-3)** |
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
| Implementation | absent — OB-1 landed (2026-11-07) without MT-PRIORITY ingress or virtual-queue ordering (no MT-PRIORITY code under internal/, grep) |
| Status fields | Backend: Missing · others: — |
| Final status | **PLANNED (ADR-001 accepted · OB-1 remainder)** — honest: not implemented in OB-1 |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-A2.md. |

## Category B — Email security & authentication (SF-22..SF-45)

### SF-22 — SPF verification (EHLO + MAIL FROM, strict modes, Received-SPF)
| Field | Value |
|---|---|
| Source | inventory §B SF-22 · ledger N/I |
| Requirement | Message-level SPF evaluation on inbound mail |
| Implementation | message-level SPF engine (Wave P3-1); DNS presence checks (R-07) unchanged |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified (no auto-reject) · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — implemented (Wave P3-1): RFC 7208 subset evaluator in `internal/services/inbound/verify/spf.go` (include/redirect/a/mx/ip4/ip6/exists/ptr, 4 qualifiers, 10-lookup DNS budget, macros → permerror documented); persisted on `inbound_auth_results`; 48 named tests in the wave, battery + full-repo `go test ./...` exit 0. Delivery-automated disposition execution belongs to ADR-002's routing engine (later slice) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-23 — DKIM verification (insecure-sig stripping, strict, ARF)
| Field | Value |
|---|---|
| Source | inventory §B SF-23 · ledger N/I |
| Implementation | `dkim.VerifyWithOptions` engine (Wave P3-1) — go-msgauth now used for both Sign and Verify |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — implemented (Wave P3-1): `verify/dkim.go` via go-msgauth `VerifyWithOptions` (fixture signed in-test with `dkim.Sign`), d=/s= captured for A-R; same persistence + battery evidence as SF-22 |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-24 — DMARC verification with policy enforcement
| Field | Value |
|---|---|
| Source | inventory §B SF-24 · ledger N/I |
| Implementation | message-level DMARC evaluation (Wave P3-1) + DNS-record checks (R-07) + aggregate ingestion (R-17) |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified (recommendation-only) · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — implemented (Wave P3-1): `verify/dmarc.go` policy discovery w/ org-domain walk-up (static suffix heuristic documented — not the PSL), relaxed+strict alignment, deterministic FNV-1a pct sampling; recommendation recorded, destructive rejection deferred to ADR-002 routing |
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
| Implementation | dependencies landed (SF-22/23/24 verification engines COMPLETE, Wave P3-1; A-R composer persists verdicts, SF-49) — ARF report generation/dispatch itself remains unbuilt |
| Status fields | Backend: Missing (ARF report generation) · others: — |
| Final status | **PARTIAL** — note (Wave OB-2 close, 2026-11-07): the record's dependencies are COMPLETE but **ARF auth-failure reports are still absent** — no RFC 5965/6591 report builder or dispatcher exists in the tree; carried open, not waved through |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. |

### SF-27 — ARC verification (no sealing)
| Field | Value |
|---|---|
| Source | inventory §B SF-27 · ledger N/A |
| Implementation | Wave OB-2 (W6): RFC 8617 §5.2 chain validator `internal/services/inbound/verify/arc.go:101` Verify — structure enforcement (exactly one AAR+AMS+AS per instance, contiguous 1..N, ≤50 sets, full cv= matrix), most-recent ARC-Message-Signature verified as a DKIM signature (h= must cover From, MUST NOT cover Authentication-Results/ARC headers, `l=` rejected as insecure), every ARC-Seal verified newest→oldest over the §5.1.1 input; RFC 6376 primitives in `verify/dkimutil.go` (tag-value lists, simple/relaxed canon pinned to the RFC's published empty-body SHA-256 vectors, ≥1024-bit RSA + Ed25519, rsa-sha1 rejected per RFC 8301). Annotate-only: `arc=<r> header.d=<d>` appended by the A-R composer (`verify/authres.go`), persisted in additive columns `arc_result`/`arc_domain` (`internal/models/inbound_auth_result.go` — empty = engine off, distinct from `none`); gate `POSTA_INBOUND_ARC_ENABLED` default off (`verify_hook.go`); result vocabulary exactly none\|pass\|fail. **No sealing anywhere** |
| Status fields | Backend: Verified (verify + annotate) · Frontend: — · DB: Verified (additive columns via the existing AutoMigrate row) · API: — · Security: Verified (annotation never a rejection input; no key material added) · Tests: Verified (27 ARC tests incl. tampered-seal/instance-gap/canon-vector cases; battery) · Docs: Partial |
| Final status | **COMPLETE** — ARC verification, annotate-only, landed (Wave OB-2 W6); sealing is never (by design — see the OB-2 tracker row) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W6). |

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
| Implementation | Wave OB-2 (W6) annotate slice: forward-confirmed reverse DNS `internal/services/inbound/verify/iprev.go:65` Check — PTR lookup, forward-resolve each PTR name (bounded 10), pass when any forward answer equals the connecting IP, fail on unconfirming PTRs, none with no PTR/client IP, temperror/permerror on transient/invalid input; guidance-grade annotate-only (`iprev=<r> policy.iprev.ptr=<name>` in the A-R composer, additive columns `iprev_result`/`iprev_ptr`), gate `POSTA_INBOUND_IPREV_ENABLED` default off; **never a rejection input** |
| Status fields | Backend: Partial (annotate slice Verified; full validation absent) · Frontend: — · DB: Verified (additive columns) · API: — · Security: — · Tests: Verified (12 iprev forward-confirmation cases) · Docs: Partial |
| Final status | **PARTIAL** — iprev annotate slice COMPLETE (Wave OB-2 W6); full iprev validation **PLANNED (OB-6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B1.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W6). |

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
| Implementation | relay SASL **PLAIN** (`internal/services/smtprelay/backend.go:103-109`, V-B2) + MSA PLAIN AUTH (Wave OB-1, `internal/services/mta/submission.go:250-271`); LOGIN on the outbound client (`smtp_sender.go:250-275`); bcrypt + constant-time + rehash (R-25); app passwords landed (Wave P3-4); OAUTHBEARER/XOAUTH2/MFA absent (no OAuth provider exists, SF-37) |
| Status fields | Backend: Verified (PLAIN end-to-end) · Frontend: Verified (Profile.vue app-password card) · DB: Verified (`user_app_passwords`) · API: Verified (`/users/me/app-passwords` generate/list/revoke, user-scoped) · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — PLAIN is implemented end to end and app passwords landed (Wave P3-4): `user_app_passwords` table (additive migration, `internal/storage/migration/migration.go:82`), generate returns the plaintext exactly once and stores only a bcrypt digest (`internal/handlers/app_password_handler.go:175-191`), list/revoke user-scoped (routes `internal/routes/user_routes.go:451-496`), dashboard card in `web/src/views/auth/Profile.vue`. Recorded residual (per the P3-4 wave report, re-verified in code this session): the MSA AUTH path consumes relay credentials only (`submission.go:74-91` — no app-password lookup); the ADR-004 consumption seam stands (`CredentialVerifier`, `submission.go:58-71`, documented `app_password_handler.go:43-47`, primitives `VerifyAppPassword` :180 / `FindActiveByUserID`) and rides the mail-account wave. OAUTHBEARER/XOAUTH2/MFA not adopted — tied to the non-existent OAuth provider (SF-37) |
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
| Implementation | OAuth/OIDC as **consumer** (Google sign-in) COMPLETE; as **provider** (issuing tokens to external clients) MISSING; LDAP/SQL directory backends MISSING; consumer-side handler tests landed (Wave P3-4) |
| Status fields | Backend: Verified (consumer surface) · Frontend: Verified (login) · DB: Verified · API: Verified · Security: Verified · Tests: Verified (fake-driver handler tests, Wave P3-4) · Docs: Partial |
| Final status | **COMPLETE** — the implemented equivalent is the consumer surface: Google OAuth/OIDC sign-in COMPLETE and directly tested (Wave P3-4 closed the zero-test residual: `internal/handlers/oauth_handler_test.go`, 831 lines / 9 fake-driver tests — provider CRUD lifecycle, unknown-type rejection, public provider list at login, SSO discovery, workspace SSO binding isolation + workspace-context requirement). Provider issuance (OAuth grants to external clients) is not adopted by design — ReForwarder issues API keys/JWTs (SF-68 supersedes grant admin) — and LDAP/SQL directory backends have no account semantics before ADR-004 (OB-4); the ledger's FULL claim stays reconciled to the consumer surface, re-derivation rides OB-4/6 only if the provider decision lands |
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
| Implementation | Wave OB-2 (W5) GTUBE slice: the standard GTUBE marker `internal/services/spamfilter/gtube.go:40` GTUBEString scores 1000 in the ADR-006 baseline scorer (`scorer.go`) → instant reject disposition, end-to-end proven by the worker quarantine test |
| Status fields | Backend: Partial (GTUBE slice Verified) · others: — · Tests: Verified (GTUBE in the scorer threshold matrix + the worker end-to-end test) |
| Final status | **PARTIAL** — GTUBE self-test slice COMPLETE (Wave OB-2 W5); Pyzor, spam traps, attachment blocklists and external rule feeds **PLANNED (ADR-006 · OB-6)** |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W5). |

### SF-43 — Connection/DoS budgets (memory accounting, session reservations)
| Field | Value |
|---|---|
| Source | inventory §B SF-43 · ledger PART |
| Implementation | security-engine connection-abuse detection + HTTP 403 + SMTP silent drops + bounded retry sweeps (R-27); Wave OB-1 MTA connection budgets: MaxHostAttempts enforced per delivery (`internal/services/mta/deliver.go:209,272`), MaxConnections carried for the queue-manager slice (`policy.go:41-46`); memory accounting/session reservations absent |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — MTA connection budgets landed (Wave OB-1); memory accounting/session reservations **PLANNED (Phase 3)** (bounded worker pools; no memory accounting in the MTA — grep, 2026-11-07) |
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
| Implementation | **No-Sieve alternative (the documented one), Wave OB-2:** policy hooks at the SMTP/ingest stages ride the inbound routing evaluator + the ADR-006 filter chain, not a Sieve interpreter — RCPT-time route validation before responsibility is accepted (`internal/services/inbound/smtp_server.go:192,244`: no matching enabled route → 550 5.1.1; route-store failure → 451 4.4.3), post-ingest route actions after durable persist (`route_action.go:96`), spam/malware dispositions applied after durable ingest (`internal/services/inbound/filter.go` ApplyFilterDisposition → quarantine/reject + `inbound.spam_flagged`/`inbound.malware_detected`, never a silent drop), and the external filter consult webhook (`internal/services/webhook/filter_consult.go:62` ConsultFilter — synchronous, timeout-bounded, fail-open, escalate-only, HMAC + SSRF-guarded) |
| Status fields | Backend: Verified (documented no-Sieve alternative) · Frontend: — · DB: Verified (spam_score/spam_reasons/threat_signature + route stamp columns) · API: Partial (disposition thresholds ride the admin-gated settings endpoints) · Security: Verified (consult never rescues a local flag; SSRF guard untouched) · Tests: Verified (53 W5 named tests + W1's routing battery) · Docs: Partial (`docs/docs/getting-started/configuration.md` Spam & Malware Filtering section) |
| Final status | **COMPLETE** — via the documented no-Sieve alternative: policy hooks at the SMTP stages = the routing evaluator + ADR-006 filter chain (the Sieve interpreter itself stays its own record, SF-46, PLANNED) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-B2.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W1/W5). |

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
| Implementation | **No-Sieve equivalent, Wave OB-2:** per-recipient delivery policy at final delivery = workspace-scoped `inbound_routes` rule actions (deliver/forward/webhook/quarantine, priority-ordered, first-match-wins) resolved per recipient at RCPT (`internal/services/inbound/router.go:90`) and executed post-ingest after the durable persist (`route_action.go:96`); release-from-quarantine re-runs the same evaluator and re-executes the matched action (`internal/services/inbound/quarantine.go`), failing closed |
| Status fields | Backend: Verified (routing-evaluator equivalent) · Frontend: Partial (rules UI landed; rules-CRUD handler unregistered — same residual as SF-11) · DB: Verified · API: Partial (same `/inbound-routes` KNOWN_DRIFT residual as SF-11) · Security: Verified (workspace-scoped, fail-closed) · Tests: Verified (W1 routing battery + W3 release tests) · Docs: Partial |
| Final status | **COMPLETE** — the documented no-Sieve equivalent is implemented: per-recipient final-delivery policy via the routing evaluator, not per-account Sieve scripts (per-account granularity arrives with ADR-003/004 accounts; SF-46 stays PLANNED as its own record) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W1/W3/W7). |

### SF-49 — Header inspection/transformation (editheader, milter, hooks, A-R injection)
| Field | Value |
|---|---|
| Source | inventory §C SF-49 · ledger PART |
| Implementation | R-24 CR/LF/NUL rejection + last-guard `encodeHeader` COMPLETE; A-R composer COMPLETE (Wave P3-1 `verify/authres.go`, RFC 8601, persisted on the verdict row + merged into parsed-headers view; physical in-band injection into stored .eml deliberately deferred to the ADR-002 receiver slice — rewriting the archive would falsify it) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: Verified · Tests: Partial (no HTTP lookup backend; `RedisChallengeStore`/`RedisLimiter` covered only indirectly, V-H) · Docs: Partial |
| Final status | **PARTIAL** — A-R composer COMPLETE; in-band injection rides the ADR-002 receiver slice; editheader/milter PLANNED under ADR-006/001 |
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
| Implementation | Wave OB-2 (W3 — the table W3 was designed for is now built): `inbound_quarantine` model + workspace-scoped repo (status pending\|released\|deleted, reviewed_by/reviewed_at) additive-migrated (`internal/models/inbound_quarantine.go`, `internal/storage/repositories/inbound_quarantine_repo.go`); `QuarantineService` fills the W1 seam (`internal/services/inbound/quarantine.go:96` — idempotent per pending message; the message itself never moves, durable-first); release re-runs routing at the post-ingest point and re-executes the matched action (deliver/forward/webhook/quarantine), failing closed on no-route or lost domain verification; delete is permanent + audited (blobs + inbound row destroyed, tombstone to `deleted`, `inbound.quarantine_deleted` event). REST surface: `GET /workspaces/current/inbound-quarantine`, `POST .../{id}/release`, `DELETE .../{id}` (`internal/routes/inbound_quarantine_routes.go:42`) with requireEdit-gated mutations, scope-checked reads and envelope DTOs (`internal/handlers/inbound_quarantine_handler.go`); dashboard `web/src/views/inbound/Quarantine.vue` (+7 vitest specs, canEdit-gated, 422 fail-closed). Retention: `inbound_quarantine_retention_days` (default 30, 0 = forever) purging expired holds in `internal/cron/jobs/retention.go`. Wave W5 routes spam/malware dispositions into the same store (nil-route-safe, `quarantine.go:96-126`) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified (additive migration) · API: Verified (3 routes; contract golden regenerated, docs/implementation/api_routes.txt) · Security: Verified (edit-gated mutations, cross-workspace 404, conflicts 409, policy 422) · Tests: Verified (15 Go quarantine/retention tests + 7 vitest) · Docs: Partial |
| Final status | **COMPLETE** — quarantine landed (Wave OB-2 W3): route-action holds and spam/malware holds share one review store with release (re-routes) / permanent delete. Known limitation recorded by W3: list summaries do one FindByID per row (page-size-bounded N+1; batch load noted for later) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W3/W5/W7). |

## Category D — Queue & delivery administration (SF-52..SF-59)

### SF-52 — Queue inspection API (list/query/patch/destroy/cancel/requeue)
| Field | Value |
|---|---|
| Source | inventory §D SF-52 · ledger PART |
| Implementation | R-03 + R-06 + Wave P3-2: per-state task lists, cancel/run/archive, stats, audited replay (5/hr cap), **destroy endpoint** (`DestroyQueueTask`, audited `admin.queue.task_destroyed`); PATCH skipped with documented rationale — asynq v0.26 Inspector exposes no option-modifying API (cancel + re-enqueue is the adjustment path) |
| Status fields | Backend: Partial · Frontend: Verified (Jobs.vue) · DB: Verified (asynq) · API: Verified · Security: Verified (admin) · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** (destroy done; PATCH documented as not-supported-by-library with the documented adjustment path) |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-53 — SMTP response taxonomy surfaced to admins
| Field | Value |
|---|---|
| Source | inventory §D SF-53 · ledger PART |
| Implementation | R-05 classifier + Wave P3-2: **`GET /logs/classifications`** workspace-scoped aggregate (`WorkspaceClassificationCounts`, classification card in MessageHistory), per-event classification badges in MessageDetail (`internal/routes/workspace_resource_routes.go:1682`) |
| Status fields | Backend: Verified · Frontend: Verified (classification card + badges) · DB: Verified · API: Verified (workspace_resource_routes.go:1682) · Security: Verified (workspace-scoped) · Tests: Verified (TestLogsClassifications, logs_handler_test.go:944) · Docs: Partial |
| Final status | **COMPLETE** — SMTP response taxonomy surfaced to admins (Wave P3-2); the raw SMTP transcript view is tracked separately as GAP-20 (open, todo.md:527, → OB Phase 1 ops) and no longer counted against this record |
| Evidence | VERIFIED (read-only code audit) — full verdict: docs/audits/verification/V-CD.md. |

### SF-54 — Global queue pause/resume
| Field | Value |
|---|---|
| Source | inventory §D SF-54 · ledger PART |
| Implementation | Wave P3-2: **global queue pause/resume** (`POST /admin/queue/pause|resume` over all managed queues via asynq Pause/Unpause, idempotent, audited, Jobs.vue badge+banner) + MaintenanceMode hot gate + per-worker/automation/campaign controls; per-domain suspension explicitly excluded (ADR-001) |
| Status fields | Backend: Verified (global pause/resume) · Frontend: Verified (Jobs.vue badge+banner) · DB: — · API: Verified (admin_routes.go:786-812) · Security: Verified (admin) · Tests: Verified (TestAdminQueuePauseResume, admin_queue_test.go:723) · Docs: Partial |
| Final status | **COMPLETE** (global pause/resume done; per-domain suspension is ADR-001 scope) |
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
| Implementation | `email_events` timeline + attempts + MessageDetail + Wave P3-2: **live per-message trace SSE** (`GET /logs/{uuid}/events/stream`, 2s poll of the event repo, immediate handshake, live-tail merge into the timeline) |
| Status fields | Backend: Verified (live trace SSE over the events ledger) · Frontend: Verified (Logs) · DB: Verified · API: Verified (workspace_resource_routes.go:1702) · Security: Verified (TestLogsMessageEventsStreamAuthz, logs_handler_test.go:1108) · Tests: Verified (TestLogsMessageEventsStream, logs_handler_test.go:1026) · Docs: Partial |
| Final status | **COMPLETE** (trace store = the events ledger; live SSE done) |
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
| Implementation | queue_reconcile cron (now exported `ReconcileOnce`) + hot settings + Wave P3-2: **`POST /admin/queue/actions/{action}`** (`requeue_stuck`, `purge_archived_before` walking archived sets 50×100/sweep) — audited, admin-gated; Reload*/cache actions live in the settings/security surfaces (SF-110) |
| Status fields | Backend: Verified (actions endpoint + exported ReconcileOnce) · Frontend: Verified (Jobs.vue runQueueAction, web/src/views/admin/Jobs.vue:199-204) · DB: — · API: Verified (admin_routes.go:817) · Security: Verified (admin) · Tests: Verified (TestAdminQueueActionPurgeArchivedBefore, admin_queue_test.go:841; TestAdminQueueActionRequeueStuck, :891) · Docs: Partial |
| Final status | **COMPLETE** |
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
| Implementation | users + roles + sessions + 2FA + recovery codes + passkeys + OAuth login + language preference; **groups/aliases absent** (no mail account semantics pre-OB); addendum (Wave OB-5 close-out, 2026-11-10): the **mailbox self-service surfaces are COMPLETE** — the fourteen session-user-scoped `/api/v1/mail/*` endpoints (`internal/handlers/mail_handler.go`, `internal/routes/mail_routes.go`) give each account owner self-service mailbox/folder/message access with quota+usage counters, and the webmail frontend (`web/src/views/mail/` — Mailbox, MessageView, Compose, MailSettings, wired by `web/src/router/index.ts` + `web/src/layouts/DashboardLayout.vue` + `web/src/api/mail.ts`) lands the user-facing side incl. the per-mailbox quota/usage display in `MailSettings.vue` |
| Status fields | Backend: Partial · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — account aliases/groups PLANNED under ADR-004 (OB-4, mail-account semantics); mailbox self-service surfaces COMPLETE (Wave OB-5 close-out, 2026-11-10: the session-user `/api/v1/mail/*` surface + the webmail UI + the self-service quota/usage display) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. Wave OB-5 (2026-11-08–10): files + checks in the **OB-5 wave evidence** section below (W1 + W4 addenda). |

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
| Implementation | API keys: scopes, IP allowlists, per-key rate limits (R-15), usage analytics (R-18), query-token scoping (R-28); app passwords landed (Wave P3-4 — see SF-35 for the consumption seam) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified (allowlist direct tests landed, Wave P3-4 — see SF-04) · Docs: Verified |
| Final status | **COMPLETE** — API keys (scopes, IP allowlists, per-key rate limits R-15, usage analytics R-18, query-token scoping R-28) verified; both Phase 3 residuals closed (Wave P3-4): the app-password management surface (one-time-display contract, bcrypt-only storage, user-scoped — full detail on the SF-35 record) and the IP-allowlist direct tests (`internal/middlewares/ipallowed_test.go`). Recorded residual: app passwords are consumed by no auth backend yet — the MSA verifies relay credentials only (`internal/services/mta/submission.go:74-91`, re-verified this session); consumption rides the ADR-004 seam (SF-35) |
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
| Implementation | TOTP + one-time recovery codes (R-10) + WebAuthn passkeys (R-19). Correction (V-EF): the pre-seed's "`two_factor_required` wiring (R-23)" was wrong — R-23 is `require_domain_verification`; the V-EF "dead toggle" finding is resolved (Wave P3-4, GAP-12 closed — todo.md:424): `two_factor_required` is enforced at login (`internal/handlers/user_handler.go:129-134` twoFactorEnrollmentRequired; forced-enrollment marker on the auth/profile responses + dashboard redirect to 2FA setup; admins not exempt; 8-case matrix `internal/handlers/login_enforcement_test.go`); documented deviation: the session is issued carrying the enrollment marker because Setup2FA needs an authenticated session — middleware-layer refusal for un-enrolled sessions is the recorded residual (`user_handler.go:566-571`) |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified · Tests: Verified · Docs: Verified |
| Final status | **COMPLETE** — note (OB-4 close-out, 2026-11-08): app passwords are now the accepted credential type on the mail-protocol servers; IMAP/POP3 authenticate solely via the W1 `mailauth` seam (`internal/services/mailauth/auth.go` `AuthenticateWithAppPassword` — bcrypt over active app-password digests only, `last_used_at` stamped, primary password structurally never consulted, revoked/missing/wrong collapsed to one generic error), per ADR-004's app-password-only posture |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-EF.md. Mail-protocol acceptance: OB-4 wave evidence below (TestAuthenticateWithAppPassword suite, `internal/services/mailauth/auth_test.go`). |

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
| Implementation | workspaces = tenant analog: scoped views, per-workspace isolation on every endpoint, admin scope; distinct tenant objects + tenant quotas absent; addendum (Wave OB-5 W2, 2026-11-08): the platform-wide mailbox administration surface landed under this admin scope — `GET /admin/mailboxes` with workspace/owner filters and membership-validated workspace binding on provisioning (ADR-008 decision 1's mailbox → user → workspace chain enforced fail-closed) |
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
| Implementation | data plane = Postgres; blob plane = `blob.Store` (FS/S3), fronted since Wave OB-3 by the mailstore's content-addressed encrypted blob layer (`internal/services/mailstore/service.go:114` BlobObjectKey); lookup plane = Redis (limits, bans, challenges); search plane = Postgres FTS for mail — `mail_messages.search_tsv` generated column + GIN (`internal/storage/migration/mailstore_fts.go:34,70`) served by `internal/services/mailstore/search.go:167` — plus SQL ILIKE elsewhere (SF-79); registry = settings table. The mailstore adds the storage depth: `internal/models/mailbox.go` (Mailbox/MailFolder/MailMessage/MailBlobRef, additive AutoMigrate `internal/storage/migration/migration.go:110-117`) and the `internal/services/mailstore/` service (mailbox/folder CRUD, Append with atomic UID claim, Copy/Move, flag CAS on mod_seq, Expunge, Fetch, per-user/workspace scoping) |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified (named mailstore suites in the OB-3 wave evidence below) · Docs: Partial |
| Final status | **COMPLETE** — all five planes real (V-G five-plane map + Wave OB-3): the search plane's mail depth landed as Postgres FTS (SF-79), the blob plane gained content-addressed dedup/encryption (SF-77/78), the data plane gained the mailstore schema and service (Wave OB-3 W1) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md; Wave OB-3 (2026-11-08): files + battery in the **OB-3 wave evidence** section below. |

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
| Implementation | `blob.Store` FS + S3 as the raw object layer; Wave OB-3 (2026-11-08) fronted it with the mailstore's content-addressed namespace: SHA-256 keys (`internal/services/mailstore/service.go:114` BlobObjectKey), dedup + ref-counting via `mail_blob_refs` reserve-then-write under a per-content-address `pg_advisory_xact_lock` (`internal/services/mailstore/service.go:140`, `append.go:87` — ref_count ≥ 1 durable before any message row, probe-read self-heal of crashed reservations), orphan GC with a 24 h `last_referenced` grace + per-hash re-check delete (`internal/services/mailstore/retention.go:331` reclaimBlobs — a failed object delete rolls the row delete back, never an untracked orphan), dedup-aware per-mailbox usage reporting (`internal/services/mailstore/usage.go:69` MailboxUsageAll; audited `mail_blob_gc` cron event `internal/cron/jobs/retention.go:725`, wired `cmd/posta/server.go:953-957`) — the GAP-45 storage half, now closed as scoped |
| Status fields | Backend: Verified · Frontend: — · DB: Verified (mail_blob_refs + additive last_referenced, `internal/models/mailbox.go:185`) · API: — · Security: Verified · Tests: Verified (TestPGAppendDedupRefCountsTwo, TestPGConcurrentAppendSameContent, TestPGMailBlobGCDropsOnlyAgedOrphans, TestPGReserveSelfHealsMissingBlob, TestMailboxUsageBlobBytesFromBlobRefs — OB-3 wave evidence below) · Docs: Partial |
| Final status | **COMPLETE** — content-addressed keys + dedup + ref-count + orphan GC + usage reporting landed with the mailstore (Wave OB-3 W2). Documented residual (not silently dropped): `blob.Store` itself still has no List op, so blobs orphaned **without** a ref row (a failed reserve whose Put landed) are not enumerable — recorded here and in the GAP ledger note |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md; Wave OB-3 files + tests in the **OB-3 wave evidence** section below. |

### SF-78 — Blob at-rest encryption (zstd + AES-256-GCM/ChaCha20)
| Field | Value |
|---|---|
| Source | inventory §G SF-78 · ledger N/I |
| Implementation | AES-256-GCM encryption for credentials/signing keys (`internal/services/crypto`) — Wave P3-4 hardening: `Encrypt` refuses without a configured key (`ErrEncryptionDisabled`, `crypto.go:42,88-96`; the V-G base64-plaintext fallback is gone), **legacy base64 rows remain readable for migration** (`crypto.go:128-137` — the base64-legacy migration path: old plaintext rows decrypt transparently and are re-sealed on write), loud boot warning (`crypto.go:60-74`). Wave OB-3 (2026-11-08) extended at-rest encryption to the mail blob plane: `mailstore.ZstdBlobCodec` (`internal/services/mailstore/codec_zstd.go:60`) compresses bodies > 4 KiB when that shrinks them, then AES-256-GCM via additive `crypto.EncryptBytes/DecryptBytes` (`crypto.go:151,166`) in a self-describing frame (version byte, flags byte, nonce‖ciphertext); no key → production wiring refuses (SF-78 preserved) |
| Status fields | Backend: Verified · others: — |
| Final status | **COMPLETE** — at-rest encryption now covers stored secrets (credentials, signing keys — Wave P3-4 refuse-without-key) **and** stored mail blobs (Wave OB-3: zstd + AES-256-GCM, SHA-256-of-plaintext content addressing so dedup survives random per-write nonces, every tamper path → `ErrBlobCorrupt`); the V-G plaintext-fallback security note is resolved. Documented residual (wave-reported): one deployment-wide `POSTA_ENCRYPTION_KEY` data key — per-workspace data keys need a BlobCodec interface change (recorded in the STATUS_HISTORY W2 note) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md; Wave OB-3 files + tests (codec_zstd_test.go: 8 named) in the **OB-3 wave evidence** section below. |

### SF-79 — Search (inverted index + Elasticsearch/Meilisearch; async reindex)
| Field | Value |
|---|---|
| Source | inventory §G SF-79 · ledger PART |
| Implementation | SQL ILIKE search across subscribers/contacts/logs (documented sufficient at current scale) + Wave OB-3 (2026-11-08) mail full-text search: `mail_messages.search_tsv tsvector GENERATED ALWAYS` (weighted subject / from+to+cc / body, explicit `'english'` regconfig) + GIN index (`internal/storage/migration/mailstore_fts.go:34-70`, idempotent, wired both as upgrade step `2026-11-07-mailstore-fts` and from `migration.Run`), `mailstore.PostgresSearcher` (`internal/services/mailstore/search.go:167`) — `websearch_to_tsquery('english', ?)` bound-parameterized against the generated column (never concatenated), `ts_rank` ordering, mailbox+user isolation joins, folder scoping, paged with `COUNT(*) OVER ()`, 512-rune guard; operator reindex `migration.RebuildMailstoreSearch` (`internal/storage/migration/mailstore_fts_reindex.go`) exposed as the admin-gated, audited `POST /api/v1/admin/mailstore/search/reindex` (`internal/handlers/mailstore_search_admin_handler.go:83`, route in `internal/routes/admin_routes.go`; contract golden regenerated). External search engines (Elasticsearch/Meilisearch) remain absent **by decision — not adopted per ADR-009** (SQL-is-the-store architecture); reindex is the admin endpoint, not an async scheduler |
| Status fields | Backend: Verified · Frontend: — (user-facing search surface rides OB-4/OB-5) · DB: Verified (generated column + GIN proven on postgres:17-alpine) · API: Verified (admin reindex route; 401/403/200 gating test) · Security: Verified (parameterized tsquery; TestPGSearchIsolationBetweenUsers) · Tests: Verified (TestPGSearchHitsSubjectFromAndBody, TestPGSearchPaginationAndTotals, TestPGSearchRankingPrefersSubjectOverBody, TestPGSearchFolderScoping, TestPGRebuildSearchIndexIdempotent + no-DB units — OB-3 wave evidence below) · Docs: Partial |
| Final status | **COMPLETE** — the mail index stack is Postgres FTS (Wave OB-3 W3); Elasticsearch/Meilisearch documented as **not adopted (ADR-009)** — a decision, not a gap |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md; Wave OB-3 files + tests in the **OB-3 wave evidence** section below. |

### SF-80 — Quotas (per-account disk + count quotas)
| Field | Value |
|---|---|
| Source | inventory §G SF-80 · ledger PART |
| Implementation | plan/workspace limits at control-plane level (`services/plan` CheckQuota) + Wave OB-3 (2026-11-08) per-mailbox storage quotas: `mailstore.DefaultQuotaEnforcer` (`internal/services/mailstore/quota.go`) enforces the per-mailbox byte quota (`Mailbox.QuotaBytes`, `internal/models/mailbox.go:53`), the per-mailbox message-count quota (`mailboxes.quota_messages`, `mailbox.go:56`), and the workspace aggregate byte ceiling (`workspaces.mail_quota_bytes`, `internal/models/workspace.go:59`) — checked inside the append/copy transaction with FOR UPDATE serialization (mailbox row → workspace row, matching the purge paths' lock order); typed `*QuotaError{Scope, Used, Incoming, Limit}` (`quota.go:42`) matching `ErrQuotaExceeded` for the ingestion seam; `used_bytes`/`message_count` synced in the same transaction on append/copy/expunge/cascade/purge |
| Status fields | Backend: Verified · Frontend: Verified (admin quota editing + usage readout, `web/src/views/admin/Mailboxes.vue`, Wave OB-5 W2; self-service per-mailbox quota/usage display landed, `web/src/views/mail/MailSettings.vue`, Wave OB-5 W4) · DB: Verified (additive columns via AutoMigrate) · API: Verified (admin HTTP surface for mailbox quotas + usage landed: `POST/PATCH /admin/mailboxes` quota fields + `GET /admin/mailboxes/{id}/usage`, Wave OB-5 W2; per-folder retention settings remain OB-4/OB-5 residual) · Security: Verified · Tests: Verified (TestQuotaBoundaryExactFitAdmitted, TestAppendMessageCountQuotaRejected, TestWorkspaceCeilingRejected, TestCopyMessageCountQuotaRejected, TestPGConcurrentAppendQuotaNoOvershoot — 24 concurrent appends, quota admits exactly 10, never overshoots; Wave OB-5 W2 adds TestAdminQuotaAndStatus — the admin quota write honored by the append path) · Docs: Partial |
| Final status | **COMPLETE** — per-mailbox disk + count quotas and the workspace aggregate ceiling landed (Wave OB-3 W4); plan-limit CheckQuota unchanged. Residual updates: the admin quota/usage HTTP surface and the admin-side dashboard quota usage landed (Wave OB-5 W2, 2026-11-08 — `/admin/mailboxes` surface + Mailboxes.vue); the self-service quota/usage display landed (Wave OB-5 close-out, 2026-11-10 — `web/src/views/mail/MailSettings.vue`, the ADR-003 §5 counters incl. deduplicated blob bytes); remaining residual: per-folder retention settings |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md; Wave OB-3 files + tests in the **OB-3 wave evidence** section below; Wave OB-5 W2 admin surface + W4 self-service display in the **OB-5 wave evidence** section below. |

### SF-81 — Retention/expiration (DataRetention settings, cron purges)
| Field | Value |
|---|---|
| Source | inventory §G SF-81 · ledger FULL |
| Implementation | broader than pre-seed: 12+ record classes incl. blob bytes have retention handling in `retention.go`; **`security_events` retention caller landed (Wave P3-3, GAP-13 closed — todo.md:453): `SetSecurityEventRepo` (`internal/cron/jobs/retention.go:110-115`), prune pass under `security_events_retention_days` (default 90, 0 = keep forever) at `retention.go:424-433`, wired `cmd/posta/server.go:832`; 4 named tests in `retention_security_test.go`** |
| Status fields | Backend: Verified · Frontend: Partial (settings) · DB: Verified · API: Verified · Security: — · Tests: Verified (retention_security_test.go) · Docs: Partial |
| Final status | **COMPLETE** — the GAP-13 caller, the record's last open item, landed (Wave P3-3); documented residual: the security-rules settings surface still advertises the key with Min 1 so 0 (keep-forever) is not settable there (`internal/handlers/security_handler.go:487-491`) — 0 stays reachable via PUT /admin/settings; recorded, not silently omitted |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-82 — Backup/restore (stop-and-dump CLI, LZ4)
| Field | Value |
|---|---|
| Source | inventory §G SF-82 · ledger N/I |
| Implementation | R-11: pg_dump gzip CronJob into `POSTA_BACKUP_DIR` with retention sweep + restore runbook; restore test verified in the DB-tier battery |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: Verified · Tests: Verified · Docs: Verified (runbook) |
| Final status | **COMPLETE** — equivalent mechanism (online dump cron) documented vs the reference platform's offline CLI; restore-runbook verification now EXECUTED (Wave P3-5): `internal/cron/jobs/backup_restore_test.go` (224 lines) TestBackupRestoreRunbookRoundTrip runs the backup.go godoc runbook end-to-end on the DB tier — real pg_dump dump (gzip magic + plain-format header + CREATE TABLE validated) → scratch-database restore via `psql -v ON_ERROR_STOP=1` → marker row read-back; PASS in 6.35s against a live PostgreSQL 17 container (the standard battery skips it — golang:1.27 ships no Postgres client; documented, not silent). `STATUS_HISTORY.md` still lacks an R-11 entry (Phase 6) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md. |

### SF-83 — Compression (zstd with per-type dictionaries)
| Field | Value |
|---|---|
| Source | inventory §G SF-83 · ledger N/A |
| Implementation | Wave OB-3 (2026-11-08): `mailstore.ZstdBlobCodec` (`internal/services/mailstore/codec_zstd.go:60`, `github.com/klauspost/compress/zstd`) — zstd applied to blob payloads above the 4 KiB threshold and only when it actually shrinks them, BEFORE AES-256-GCM encryption (compress-then-encrypt per ADR-003); gzip retained where it already exists (backups, DMARC). No per-type dictionaries in the codec (plain zstd — read from `codec_zstd.go`, no dictionary configuration), recorded as the limitation |
| Status fields | Backend: Verified · others: — |
| Final status | **COMPLETE** — zstd pre-encryption compression landed on the mail blob plane (Wave OB-3 W2); per-type dictionaries remain the recorded limitation |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-G.md; Wave OB-3 files + tests (TestZstdBlobCodecCompressesLargeBodiesOnly, TestZstdBlobCodecRoundTrip, …) in the **OB-3 wave evidence** section below. |

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
| Status fields | Backend: Verified (IMAP4rev1 core per ADR-004 §Decision 1; IMAP4rev2 extensions and extensions outside the ADR's adopted command set deliberately not adopted — documented ADR-004 limitation, recorded absent) · others: — |
| Final status | **COMPLETE (ADR-004 · OB-4; IMAP4rev1 core — rev2 extensions + some extensions documented absent, out of scope)** |
| Evidence | Server `internal/services/imap/` (ADR-004 command subset incl. STARTTLS, AUTHENTICATE PLAIN/LOGIN, FETCH BODY[] sections + partials, SEARCH key set, STORE with mod_seq CAS, COPY/MOVE, UID variants, IDLE) over the W1 mailstore seam; gated listener wiring 143 (CountingListener) + 993 (CountingListener→TLSServerListener) in `cmd/posta/server.go` `startIMAPServer` with shutdown registration; app-password-only auth via `mailauth`; per-IP/per-user/command budgets + security-engine signals (SF-44); audit events `imap.*` (SF-74). Tests: W4's `audit_test.go` + `lifecycle_test.go` (audit emission, lifecycle start/stop/drain, implicit TLS, per-IP/per-user/command limits) and W2's scripted-client command matrix (`harness_test.go` + `server_test.go` — 22 named harness tests over real Postgres with real bcrypt app-password fixtures — plus `parser_test.go`, 15 units). Close-out 2026-11-08 (all four waves green): final named battery exit 0, 48 packages ok under -race, zero FAIL / zero DATA RACE; the battery re-run with TEST_DATABASE_DSN exercised the harness against real Postgres (imap ok 28.384s). Documented absent by design: IMAP4rev2 extensions and extensions outside ADR-004's adopted command set (ADR-004 §Decision 1 "recorded limitation"). Full evidence: the OB-4 wave evidence section below. |

### SF-86 — POP3 + ManageSieve servers
| Field | Value |
|---|---|
| Source | inventory §H SF-86 · ledger N/A |
| Status fields | Backend: Verified (POP3 COMPLETE; ManageSieve remains PLANNED under ADR-006 — out of ADR-004's scope, noted out of scope on the OB-4 tracker row) · others: — |
| Final status | **PARTIAL (ADR-004 · OB-4; POP3 COMPLETE — ManageSieve remains PLANNED under ADR-006)** |
| Evidence | Server `internal/services/pop3/` (classic command set USER/PASS/AUTH, STAT/LIST/UIDL/RETR/TOP/DELE/RSET/NOOP/QUIT/CAPA/STLS; session-scoped mark-then-expunge-on-QUIT deletion that leaves IMAP UIDs intact) over the mailstore INBOX; gated listener wiring 110 + 995 in `cmd/posta/server.go` `startPOP3Server` with shutdown registration; app-password-only auth; limits + security signals (SF-44); audit logger `pop3.auth.success|failure` + `pop3.session.end` (SF-74). Tests: W3's `pop3_test.go` matrix (TestPOP3LoginMatrix, TestPOP3AuthRequiresTLS, TestPOP3ConnectionAndCommandLimits, TestPOP3AuditEvents, TestPOP3DeleQuitExpunges, TestPOP3ImplicitTLS). Close-out 2026-11-08 (all four waves green): the POP3 half is COMPLETE — final named battery exit 0, 48 packages ok under -race (zero FAIL / zero DATA RACE); the W3 wave addendum in the OB-4 wave-evidence section below records the full 12-test matrix and the per-connection port race fix. The PARTIAL status reflects exactly and only ManageSieve, which stays with ADR-006. |

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
| Implementation | SSE streams COMPLETE (messages, inbound, admin events/metrics/security); Wave P3-5 minimal Web Push (default off): RFC 8291 aes128gcm (`internal/services/webpush/encrypt.go`), RFC 8292 VAPID ES256 (`vapid.go`), RFC 8030 send + 404/410 GC (`service.go`), `UserPushSubscription` model + additive migration, user-scoped routes (`internal/routes/user_routes.go:201-230`), `web/public/push-sw.js` + Profile.vue Browser Notifications card, gated `POSTA_WEB_PUSH_ENABLED` default false (`internal/config/config.go:142,436`); WebSocket push not adopted |
| Status fields | Backend: Verified (SSE + push) · Frontend: Verified (SSE consumers + push card) · DB: Verified (user_push_subscriptions) · API: Verified · Security: Verified (VAPID-signed, encrypted payloads) · Tests: Verified (service_test.go, fanout_test.go) · Docs: Verified (configuration.md Browser Push section) |
| Final status | **COMPLETE** — minimal Web Push landed (Wave P3-5); documented limitation (per the wave report): fanout covers only events published through the dashboard message-bus instance — other realtime bus instances (thread router, worker) stay SSE-only until a process-wide sink exists; WebSocket push not adopted where the SSE consumer serves the need |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-H.md. |

### SF-90 — Outbound telemetry webhooks (HMAC-signed event batches)
| Field | Value |
|---|---|
| Source | inventory §H SF-90 · ledger FULL |
| Implementation | HMAC-signed webhook endpoints (`signPayload:451`), per-event payloads by default, delivery-attempt history, exponential backoff + jitter (`retryDelay:62`, R-06), audited SSRF-guarded replay (5/hr cap), automation webhooks on the shared guard (~40 named tests); Wave P3-5 optional batching + pending cap — `internal/services/webhook/batch.go` (per-endpoint buffers; `POSTA_WEBHOOK_BATCH_ENABLED` default false; flush at 25 events/5s; HMAC over the batch bytes; one delivery row per flush; Close() flushes synchronously) + per-endpoint pending cap (`POSTA_WEBHOOK_MAX_PENDING_EVENTS` default 1000; drop-oldest with warning + `posta_webhook_events_dropped_total` counter in `internal/metrics/metrics.go`); config `internal/config/config.go:132-139,426-429`, wired `cmd/posta/server.go:1000` |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (SSRF guard + batch HMAC) · Tests: Verified (dispatcher_batch_test.go — 6 tests incl. default-off, coalescing, interval flush, batch signature, drop-oldest) · Docs: Verified (configuration.md batch/cap rows) |
| Final status | **COMPLETE** — batching + pending caps landed (Wave P3-5, default off by design so per-event delivery stays the default posture); the V-H refutation of the pre-seed's batching claim is thereby resolved |
| Evidence | pre-seed: `internal/services/webhook/` (ssrf.go, dispatcher) |

### SF-91 — Lookup stores (Redis/HTTP/memory/SQL) for Sieve/rate-limits/traps
| Field | Value |
|---|---|
| Source | inventory §H SF-91 · ledger PART |
| Implementation | Redis + in-memory + SQL stores power rate limiting (login + per-key), bans, challenges, session blacklist; direct unit tests for RedisLimiter + RedisChallengeStore (Wave P3-4); Sieve/trap consumers absent (ADR-006) |
| Status fields | Backend: Verified (store abstraction) · Frontend: — · DB: — · API: — · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — the lookup-store abstraction (Redis/memory/SQL powering rate limiting, bans, challenges, session blacklist) is the implemented equivalent and directly unit-tested (Wave P3-4, interface fakes — miniredis is not in go.mod, so the SF-91 fake alternative applies): `internal/services/ratelimit/redis_limiter_test.go` (9 tests — windows, daily caps, per-subject counters, settings override, login window, fail-open, unlimited-zero, enforcement, non-incrementing check) and `internal/services/webauthn/redis_store_test.go` (5 tests — save/take, one-shot take, unknown key, error propagation, TTL); Sieve/trap consumers are ADR-006 (OB-2) scope — no Sieve system exists to consume them, recorded as scope not gap |
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
| Implementation | structured logging (jkaninda/logger) to console with request-scoped fields; **file sink COMPLETE (Wave P3-3)**: `internal/logging` package — `POSTA_LOG_SINKS` (stdout\|file, default stdout, unknown sink names refused) + `POSTA_LOG_FILE`; when file is selected stdout is teed into a size-capped rotating file (10 MiB trigger, `<path>.1`…`<path>.4` numbered backups, oldest deleted — policy documented in the package doc); wired before the logger handler is constructed in `cmd/posta/server.go:88-97` (sink closed on graceful shutdown, `:978-980`) and in `runWorker` (`cmd/posta/worker.go`, same fail-open arrangement); both cmd/posta entrypoints honor the sinks (`cmd/posta/server.go:92`, `cmd/posta/worker.go:53`) |
| Status fields | Backend: Verified (stdout+file) · Frontend: — · DB: — · API: — · Security: — · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — console + rotating file sinks landed (Wave P3-3) and wired in both entrypoints; journald not adopted (Docker deployment — same rationale as SF-109's systemd-notify note); OTLP export stays **PLANNED (OB-6)** under its own records (GAP-51/CA-07); webhook/store sinks not adopted — the events/security_events tables + SSE streams (SF-44, COMPLETE) fill the store-sink role |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. Wave P3-3 (2026-11-07, uncommitted at 02666cc): `internal/logging/logging.go` + `logging_test.go` — TestParseSinks, TestRotatingWriterWritesAndRotates, TestRotatingWriterCapsFileCount, TestStdoutTeeWritesToFile, TestInitWithEnvTeesToFile, TestInitDisabledByDefault, TestInitBadSinkNameIsRefused; battery PASS (see STATUS_HISTORY 2026-11-07 P3-3 entry). |

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
| Implementation | Vue 3 dashboard: full admin surface (users, workspaces, queue admin, security, settings, announcements, provider events, system info); extension (Wave OB-5 W2, 2026-11-08): the admin surface extends to mailbox administration — `web/src/views/admin/Mailboxes.vue` (usage readout + % of quota, provision modal, usage-informed quota editing, suspend/reactivate, confirm-gated cascade delete) with router entry + sidebar nav + typed client `web/src/api/mailboxes.ts` over the admin-gated `/admin/mailboxes` surface (`internal/handlers/admin_mailbox_handler.go` + `internal/services/mailstore/admin.go`) |
| Status fields | Backend: — · Frontend: Verified · DB: — · API: Verified · Security: Verified (admin middleware) · Tests: Partial (vitest) · Docs: Verified |
| Final status | **COMPLETE** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-IJ.md. Wave OB-5 W2 extension in the **OB-5 wave evidence** section below. |

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
| Implementation | **COMPLETE (Wave P3-3)**: additive `setting_history` table (id, workspace_id nullable, key, old_value, new_value, changed_by, changed_by_email, created_at — `models/setting_history.go`, registered in `internal/storage/migration/migration.go:70`); every platform settings write captures one row per key whose value changed via the single write path `applySettingWrites` (`internal/handlers/setting_handler.go:137-208`), which also invalidates the provider's 30s TTL cache (`settings.Provider.Invalidate`); `GET /admin/settings/history?key=&page=&size=` (admin-gated, paged, newest first) and `POST /admin/settings/history/rollback` (writes the old value back through `applySettingWrites`, so a rollback appends its own history entry and takes effect immediately; refuses the key's creation entry — no prior value — and reserved `app.*` keys) — `internal/handlers/setting_history_handler.go`, routes `internal/routes/admin_routes.go`, wiring `internal/routes/routes.go` (`SetHistoryRepo` + `SetSettingsProvider`). Platform keys are admin-only; no workspace-scoped key/value settings exist (workspace settings are structured rows), the history table's nullable workspace id is documented future-proofing. Dashboard: history drawer + rollback button in `web/src/views/admin/Settings.vue`, client `web/src/api/settings.ts` |
| Status fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (admin group) · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE (Wave P3-3)** — per-key diff capture + history + rollback on the events substrate |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. Wave P3-3 (2026-11-07, uncommitted at 02666cc): `internal/handlers/setting_history_handler_test.go` (8 named tests on a fake sql driver: TestSettingsUpdateCapturesPerKeyHistory, TestSettingsUpdateInvalidatesProviderCache, TestSettingsHistoryPagedGET, TestSettingsRollbackRestoresAndReRecords, TestSettingsRollbackToSpecificHistoryEntry, TestSettingsRollbackRefusesCreationEntry, TestSettingsRollbackOfUnknownKeyIs404, TestSettingsHistoryReservedKeyRejected); route contract golden regenerated (`GET /api/v1/admin/settings/history`, `POST /api/v1/admin/settings/history/rollback` in docs/implementation/api_routes.txt); battery PASS. |

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
| Implementation | **11 cron job types** (V-KL + V-IJ) + campaign scheduler (R-01) + automation schedule triggers (R-13, ProcessSchedules engine.go:608) + backup cron (R-11) + reconcile cron; **scheduled DKIM auto-retire task COMPLETE (Wave P3-3)** — daily `dkim-retire` cron (`internal/cron/jobs/dkim_retire.go`, registered `cmd/posta/server.go:910-916` end of the registration block) flips a `retiring` signing key to `retired` via the existing `MarkRetired` only once BOTH the configured grace period (`dkim_retire_grace_days`, settings key, default 14; 0 = retire eligible keys on next run; negative disables the pass) has elapsed since the rotate demotion AND the replacement Active key's DNS record is verified published (`dns_state == published`); every transition emits audit event `domain.dkim_key_auto_retired`; auto-generation of NEW keys stays deliberately out of scope (rotation remains API-driven). Scheduled report-generation tasks still absent |
| Status fields | Backend: Partial · Frontend: Partial · DB: Verified · API: Partial · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL — DKIM auto-retire task COMPLETE (Wave P3-3)**; report tasks with SF-25 send PLANNED (Phase 3 remainder) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-KL.md. Wave P3-3 (2026-11-07, uncommitted at 02666cc): `internal/cron/jobs/dkim_retire_test.go` — TestDKIMAutoRetireRetiresExpiredVerifiedKey, TestDKIMAutoRetireKeepsKeysWithinGrace, TestDKIMAutoRetireRefusesUnverifiedReplacement, TestDKIMAutoRetireRequiresActiveReplacement, TestDKIMAutoRetireSkipsUnverifiedDomain, TestDKIMAutoRetireHonorsConfiguredGrace, TestDKIMAutoRetireIsIdempotent; battery PASS. |

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
| Implementation | bounded retry sweeps + batch limits + jitter (R-27); asynq concurrency; Wave OB-1 MTA delivery budgets: hourly budget + msgs/connection + host-attempt bounds (`internal/services/mta/policy.go`, enforced `deliver.go:174-188,209`); memory budgets and transfer throttles absent |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: — · Tests: Verified · Docs: Partial |
| Final status | **PARTIAL** — queue caps + MTA delivery budgets landed (Wave OB-1); memory budgets **PLANNED (Phase 3)** — no memory accounting in the MTA (grep, 2026-11-07) |
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
| Implementation | Wave OB-3 (2026-11-08) mail attachments: the mailstore parses MIME on Append and stores attachments as content-addressed encrypted/compressed blobs with per-message attachment metadata (JSON per repo convention — `internal/services/mailstore/parse.go:89,99` attachment walk; `MailMessage.attachments` `internal/models/mailbox.go`; fetch `internal/services/mailstore/fetch.go:63` GetAttachment), inbound delivery files the full MIME through the same path (W5 seam). JMAP FileNode document-style storage (and document text extraction) not implemented |
| Status fields | Backend: Partial · Frontend: — · DB: Verified (attachments metadata + blob refs) · API: — · Security: Verified (encrypted blobs, SHA-256 integrity-checked decode) · Tests: Verified (TestAppendParsesAttachments, TestPGMailboxDeliveryRoundTrip byte-exact incl. attachments) · Docs: Partial |
| Final status | **PARTIAL** — mail attachment storage done (Wave OB-3); JMAP FileNode document-style storage + text extraction **PLANNED (OB-6)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md; Wave OB-3 files + tests in the **OB-3 wave evidence** section below. |

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
| Status fields | Backend: Verified (`internal/services/mailcompose/` — send/drafts/attachments/reply-frame/delivery-status) · Frontend: Verified (`web/src/views/mail/Compose.vue`, Wave OB-5 W4) · DB: Verified (`mail_attachment_uploads` staging table, additive AutoMigrate in `internal/storage/migration/migration.go`) · API: Verified · Security: Verified · Tests: Verified (W3 service/handler suites + PG-gated compose suite; W4 `Compose.spec.ts` 7 vitest tests) · Docs: Partial |
| Final status | **COMPLETE** — the compose surface for mailbox mail is landed end-to-end (OB-5 close-out, 2026-11-10): the W3 backend seam (`internal/services/mailcompose/` — `POST /mail/send` with the quota-enforced Sent copy + the outbound pipeline under provenance `mail-compose`, staged clamd-scanned attachments, the draft lifecycle, reply-frame, delivery-status) plus the W4 composer UI (`web/src/views/mail/Compose.vue` — from/to/cc/bcc/subject/body, attachment upload through `POST /mail/attachments` with the ADR-006 refusal surfaced, staged refs on send, save/update/send-draft, reply-frame handoff). |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. Supersession recorded honestly (OB-5 close-out, 2026-11-10): the coordinator's close-out marks CA-01 COMPLETE on the **webmail compose surface for mailbox mail**; this supersedes the W3-era scope note below that reserved the record for the TRANSACTIONAL-compose screen. ADR-005 decision 4 (a transactional compose screen as an open product decision) remains its own open product decision, unaffected by this completion. Wave evidence: the **OB-5 wave evidence** section below (W3 + W4 addenda). Prior scope note (Wave OB-5 W3, 2026-11-08): the WEBMAIL compose seam (mailbox mail — ADR-005 decision 2) is backend-landed (`POST /mail/send` + staged scanned attachments + drafts; see the OB-5 wave evidence below); this record stayed about the TRANSACTIONAL-compose screen, which ADR-005 decision 4 explicitly leaves as an open product decision. |

### CA-02 — Backend endpoints without frontend consumers
| Field | Value |
|---|---|
| Source | capability audit §2.3: `POST /campaigns/{id}/duplicate`, `GET /emails/{id}/status`, `GET /admin/api-keys`, `GET /admin/emails` (+ dead client functions flagged by RF-F103) |
| Implementation | Wave P3-5 wire-or-retire, per item: WIRED `POST /campaigns/{id}/duplicate` (`web/src/api/campaigns.ts:94-97` + editor-gated Duplicate button in `web/src/views/campaigns/CampaignDetail.vue` navigating to the new draft) and `GET /emails/{id}/status` (`web/src/api/logs.ts:119` + bounded 4s poller in `web/src/views/logs/MessageDetail.vue` while a send is queued/processing); RETIRED the dead `listApiKeys`/`revokeApiKey`/`listEmails` client functions + their type imports from `web/src/api/admin.ts` (no backend route exists; key admin lives in workspace scope per RF-F010) and removed their KNOWN_DRIFT allowances from `web/src/api/admin.spec.ts` |
| Status fields | Backend: Verified (duplicate + status routes exist) · Frontend: Verified (two wired, two retired) · DB: — · API: Verified · Security: Verified · Tests: Verified (web vitest 65/65 incl. admin.spec KNOWN_DRIFT assertions after the cleanup) · Docs: Partial |
| Final status | **COMPLETE** — wire-or-retire executed (Wave P3-5): no endpoint in the capability-audit list remains without a frontend consumer or a documented retirement |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-03 — Refresh-token rotation
| Field | Value |
|---|---|
| Source | capability audit §1.3 |
| Implementation | `refresh_tokens` table (additive migration), issued at every login, `POST /auth/refresh` single-use rotation with family reuse detection, logout revocation, `POSTA_REFRESH_TOKENS_ENABLED` gate default true (Wave P3-4) |
| Status fields | Backend: Verified · Frontend: Verified (auth store keeps the token in memory only and sends it with the logout call) · DB: Verified (`models.RefreshToken`, `internal/storage/migration/migration.go:83`) · API: Verified (`/auth/refresh`, `internal/routes/user_routes.go:498-519`) · Security: Verified · Tests: Verified · Docs: Partial |
| Final status | **COMPLETE** — implemented (Wave P3-4): `internal/services/auth/refresh.go` (SHA-256 digest peppered with the JWT secret, atomic single-use `ConsumeForRotation`, replay of a rotated token revokes the whole family), `internal/storage/repositories/refresh_token_repo.go`, login issuance `internal/handlers/user_handler.go` (authResponse), logout revocation `internal/handlers/session_handler.go:162-168`, env gate `internal/config/config.go:53-57,375`; tests in `internal/handlers/login_enforcement_test.go` (TestRefresh_RotateAndSingleUse, TestRefresh_ChainRotation, TestRefresh_UnknownOrGarbageToken, TestRefresh_LogoutRevokes, TestRefresh_DisabledServiceAnswers400) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. Implementation evidence (Wave P3-4, 2026-11-07): full-module `go test ./cmd/... ./internal/... -count=1` PASS dockerized golang:1.27. |

### CA-04 — SMTP client negotiation (SMTPUTF8/8BITMIME) + per-message throttling
| Field | Value |
|---|---|
| Source | capability audit §1.1 |
| Implementation | Wave OB-1: outbound client negotiates SMTPUTF8/8BITMIME and records them on the session result (`internal/services/mta/session.go:153-155`); non-ASCII envelope requires SMTPUTF8 (`session.go:181-188`) |
| Status fields | Backend: Partial · Frontend: — · DB: — · API: — · Security: — · Tests: Verified (session_test.go) · Docs: Partial |
| Final status | **PARTIAL** — SMTPUTF8/8BITMIME negotiation landed (Wave OB-1); per-message throttling absent (OB-1 delivered per-domain hourly budgets, `policy.go:88-148`, not per-message throttles) — **PLANNED (OB-1 remainder/Phase 3)** |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. |

### CA-05 — VERP / bounce mailbox
| Field | Value |
|---|---|
| Source | capability audit §1.1; todo.md reinstates INF-020 under Option B |
| Implementation | Wave OB-2 (W4): signed VERP return-path codec `internal/services/inbound/verp.go:115` EnvelopeFrom / `:139` Decode — `bounce+<uuid-hex>=<sig20hex>@<subdomain>.<sending-domain>`, sig = first 20 hex of HMAC-SHA256 over the full address (constant-time compare), 60-octet local part within RFC 5321's 64, forged payload/signature/host refused, feature-off = fully inert; gates `POSTA_RETURN_PATH_SUBDOMAIN` + `POSTA_RETURN_PATH_SECRET` (`internal/config/config.go`, empty subdomain = feature off). Envelope seam on **direct-to-MX** deliveries (`internal/worker/handler.go` envelopeSender; wired in both `cmd/posta/server.go` and `cmd/posta/worker.go`); signed bounce recipients accepted at RCPT before normal routing (`internal/services/inbound/smtp_server.go:161` allowlist — the bounce host is deliberately not a tenant mailbox domain; forged addresses fall through to the ordinary 550). Receiver side: durable-first system-bounce ingest, RFC 3464 DSN parse (`internal/services/inbound/dsn.go:115` ParseDSN — multipart/report, 8 MiB message / 1 MiB part caps, 100 recipient records, `ErrNotDSN` otherwise) and match-back (`internal/services/inbound/bounce.go`): failed → `bounced` email_event + hard Bounce row + the existing suppression chain; delivered → `delivered` event; delayed → transient, no event; idempotent per DSN record |
| Status fields | Backend: Verified · Frontend: — · DB: Verified (reuses inbound_emails/emails/email_events/bounces/suppressions — no migration needed) · API: — · Security: Verified (HMAC-signed, forged refused, suppression layers reused unchanged) · Tests: Verified (31 W4 named tests across verp/dsn/bounce/handler; docker battery exit 0) · Docs: Verified (`docs/docs/getting-started/configuration.md` documents both env vars) |
| Final status | **COMPLETE** — VERP + return-path processing landed (Wave OB-2 W4) with RFC 3464 DSN match-back. Documented residuals: relay-mode sends keep the upstream provider's envelope (ADR-001 — Posta is the responsible MTA only direct-to-MX), and DSN match-back scale tests remain (OB-2 tracker row) |
| Evidence | VERIFIED (read-only code audit at 50e7f86) — full verdict: docs/audits/verification/V-M.md. Wave OB-2 (2026-11-07, this tree): files + checks per the **OB-2 wave evidence** section (W4). |

### CA-06 — Fallback broker in dev
| Field | Value |
|---|---|
| Source | capability audit §1.11 (HTTP send falls back to synchronous when Redis is down) |
| Implementation | Wave P3-5 named and proved the existing no-producer synchronous fallback as the CA-06 dev fallback broker (`internal/services/email/service.go:923` comment; `sendSync` :976; `messageSender` seam; dev-only reachability via the `POSTA_DEV_MODE` producer gating at `cmd/posta/server.go:195`); pipeline proven by `internal/services/email/dev_fallback_test.go` — TestSendSyncDeliversThroughFallbackPipelineWhenNoProducerWired (SMTP send + emails UPDATE + email_events history INSERT over a fake gorm driver) and TestSendSyncFailureMarksEmailFailedThroughFallbackPipeline |
| Status fields | Backend: Verified · Frontend: — · DB: Verified · API: — · Security: — · Tests: Verified (dev_fallback_test.go) · Docs: Partial |
| Final status | **COMPLETE** — dev fallback broker landed (Wave P3-5); honest note (recorded in code and STATUS_HISTORY): brand-new dev sends short-circuit at the store-without-send path before the enqueue/send decision — the sync pipeline serves the retry path and enqueuer-less flows; a production enqueue failure deliberately does NOT degrade to sync |
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

**Post-freeze movements (recorded in place per the Resumability clause; the frozen table above is the 2026-11-07 verification-wave statistics, not the live state):**

- 2026-11-07, Wave P3-4 (identity/security): **CA-03** MISSING → PLANNED → **COMPLETE** (refresh-token rotation landed; record above). SF-04 stays COMPLETE with its test-coverage residual closed; SF-35, SF-37, SF-67, SF-91 stay PARTIAL with their named Phase-3 residuals closed or narrowed (records above). Net tally movement: COMPLETE 23 → 24, MISSING → PLANNED 7 → 6.
- 2026-11-07, Wave P3-1 (inbound verification, same working tree): **SF-22, SF-23, SF-24** MISSING → PLANNED → **COMPLETE** (records updated by that wave; their tally rows are not yet moved — noted here so the next tally recomputation includes them: COMPLETE → 27, MISSING → PLANNED → 3).
- 2026-11-07, Wave P3-2 (queue admin, landed earlier in this tree; Final rows flipped by that wave's record edits, per-field rows reconciled this session): **SF-52, SF-54, SF-56, SF-59** PARTIAL → **COMPLETE** (destroy endpoint, global pause/resume, live per-message trace SSE, operational actions — routes/tests cited on the records) and **SF-53** PARTIAL → **COMPLETE** (taxonomy surface; SMTP transcript view moved to GAP-20, open — todo.md:527).
- 2026-11-07, Wave P3-3 (ops/settings): **SF-100** MISSING → PLANNED → **COMPLETE** (settings history + rollback; record updated by that wave) and **SF-81** PARTIAL → **COMPLETE** (GAP-13 caller closed — todo.md:453); SF-104 stays PARTIAL (DKIM auto-retire done; report-generation tasks absent).
- 2026-11-07, Wave P3-4 (identity/security): **SF-35, SF-37, SF-67, SF-78, SF-91** PARTIAL → **COMPLETE** (app-password management surface, OAuth consumer tests, crypto refuse-without-key hardening, lookup-store unit tests); **GAP-12 closed** (todo.md:424) — recorded on SF-69, which stays COMPLETE. CA-03 and SF-04 already complete above.
- 2026-11-07, Wave P3-5 (product surface): **SF-82** stays COMPLETE with its restore-verification residual executed live, **SF-89** PARTIAL → **COMPLETE** (minimal Web Push), **SF-90** PARTIAL → **COMPLETE** (batching + pending caps), **CA-02** PARTIAL → **COMPLETE** (wire-or-retire executed), **CA-06** PLANNED → **COMPLETE** (dev fallback broker). SF-50/SF-103 stay PARTIAL (evaluator unification deferred — noted on both records).
- 2026-11-07, Wave OB-1 (outbound MTA foundation, landed in this tree; records reconciled this session against the MTA code): **SF-03, SF-06** → **COMPLETE** (587 MSA; MX preference/shuffle/null-MX/implicit); **SF-01, SF-02, SF-05, SF-12, SF-17, CA-04** PLANNED → **PARTIAL** (ESMTP client + MSA; extension negotiation; relay|direct per domain; MAIL FROM validation; hourly budget + msgs/connection; SMTPUTF8/8BITMIME); **SF-21 stays PLANNED** (honest: not implemented in OB-1); SF-07/09/16/43/108 stay PARTIAL with remaining scope re-worded to the post-OB-1 state; SF-49 verified unchanged (A-R composer COMPLETE, matches `internal/services/inbound/verify/authres.go` + its persistence/merge sites).
- 2026-11-07, Wave OB-2 (inbound routing per ADR-002, all seven waves landed in this tree; Programme Tracker row → VERIFIED): **SF-11, SF-27, SF-45, SF-48, SF-51** PLANNED → **COMPLETE** (forwarding chain via inbound_routes + durable forwarder; ARC verify annotate-only; no-Sieve policy hooks via the routing evaluator + ADR-006 filter consult; per-recipient delivery-policy equivalent; quarantine) and **CA-05** PLANNED → **COMPLETE** (VERP + return-path with RFC 3464 DSN match-back); **SF-26** MISSING → PLANNED → **PARTIAL** (dependencies landed; ARF report generation still absent — noted on the record); **SF-32, SF-42** PLANNED → **PARTIAL** (iprev annotate slice / GTUBE slice complete; remainders OB-6); **GAP-47 closed** (ClamAV clamd INSTREAM hook — todo.md:544; GAP rows are tracked in todo.md / docs/project/FEATURE_GAP_ANALYSIS.md, not in this 123-record register, so they do not enter the tally) and **GAP-02 residual closed** (VERP bounce/return-path signal ingestion + DSN match-back — todo.md:427; scale tests remain on the tracker row). Full wave evidence: the **OB-2 wave evidence** section below. Known integration item recorded on SF-11/SF-48: the `/inbound-routes` rules-CRUD handler is not yet registered server-side (frontend client + UI landed, recorded KNOWN_DRIFT in `web/src/api/admin.spec.ts`).
- 2026-11-08, Wave OB-3 (mailbox storage per ADR-003, all five waves landed in this tree; Programme Tracker row → VERIFIED): **SF-75, SF-77, SF-79, SF-80** PARTIAL → **COMPLETE** (five-plane map now includes the mailstore FTS search plane; content-addressed dedup + ref-count + orphan GC; Postgres FTS; per-mailbox byte+count quotas + workspace aggregate ceiling), **SF-83** PLANNED → **COMPLETE** (zstd pre-encryption on the blob plane), **SF-78** stays COMPLETE with its scope extended to the blob plane (zstd + AES-256-GCM `ZstdBlobCodec`; base64-legacy migration path retained), **SF-112** PLANNED → **PARTIAL** (mail attachment storage landed; JMAP FileNode document-style storage stays OB-6); **GAP-45 closed as scoped** (dedup + ref-count + orphan GC + usage reporting — GAP rows are tracked in `todo.md` / `docs/project/FEATURE_GAP_ANALYSIS.md`, not in this 123-record register, so they do not enter the tally; the original GAP-45 acceptance criteria covering the transactional FS/S3 blob store + AdminInventory and no-ref-row orphan enumeration remain annotated open in `todo.md:525` / `BACKLOG.md:886` — residual recorded on SF-77 and in the GAP ledger note, not silently dropped). Full wave evidence: the **OB-3 wave evidence** section below.
- 2026-11-08, Wave OB-4 W2/W3/W4 (IMAP/POP3 servers + ops wiring per ADR-004, landed in this tree): **SF-85, SF-86** PLANNED → **IN PROGRESS** (server packages + gated four-port listener wiring + tests landed; UIDVALIDITY-driven sync and the compatibility matrix remain — records and tracker row updated by W4; full evidence in the OB-4 wave evidence section).
- 2026-11-08, Wave OB-4 close-out (all four waves W1–W4 landed green; Programme Tracker row → **VERIFIED**, commit cell left for the committer): **SF-85** IN PROGRESS → **COMPLETE** (IMAP4rev1 core per ADR-004; rev2 extensions + some extensions documented absent) and **SF-86** IN PROGRESS → **PARTIAL** (POP3 COMPLETE; ManageSieve remains PLANNED under ADR-006); **SF-69** stays COMPLETE with the app-password mail-protocol acceptance noted (W1 `internal/services/mailauth` seam). Full evidence: the **OB-4 wave evidence** section below, including the close-out supersession note.
- 2026-11-10, Wave OB-5 close-out (all four waves W1–W4 landed green; Programme Tracker row → **VERIFIED**, commit cell left for the committer): **CA-01** PLANNED → **COMPLETE** (the webmail compose surface for mailbox mail — W3 backend seam `internal/services/mailcompose/` + W4 composer UI `web/src/views/mail/Compose.vue`; the coordinator's close-out marks the record complete on that basis, superseding the W3-era scope note that reserved it for the transactional screen — supersession recorded on the record, with ADR-005 decision 4's transactional-compose product decision remaining its own open item); **SF-65** stays PARTIAL with the mailbox self-service surfaces noted COMPLETE (the session-user `/api/v1/mail/*` surface + the webmail UI + the quota/usage display); **SF-80** stays COMPLETE with the self-service quota/usage display residual closed (`web/src/views/mail/MailSettings.vue`; per-folder retention settings remain); **SF-97** stays COMPLETE with the admin surface extended by mailbox administration (`web/src/views/admin/Mailboxes.vue` over `/admin/mailboxes`). Full evidence: the **OB-5 wave evidence** section below, including the close-out addendum.
- **Live tally after all recorded movements (OB-5 closed, uncommitted):** COMPLETE 59 · PARTIAL 38 · MISSING → PLANNED (Phase 3) 1 (SF-10) · PLANNED (ADR-anchored, OB phases) 25 · IN PROGRESS 0 · BROKEN 0 — of 123 records. The frozen table above remains the 2026-11-07 verification-wave statistics; this line is the live state, recomputed from the per-record Final statuses on 2026-11-10 by prefix count over the records themselves (the pre-edit count — COMPLETE 58 · PARTIAL 38 · PLANNED 26 · MISSING→PLANNED 1 — matched the recorded OB-4 state exactly; the only movement since is CA-01 PLANNED → COMPLETE; earlier recomputation on 2026-11-08: baseline before the OB-1 bullet verified as COMPLETE 52 · PARTIAL 40 · PLANNED 30 by script, pre-close-out snapshot COMPLETE 57 · PARTIAL 37 · IN PROGRESS 2).



## Programme Tracker (Option B execution — master tracker)

**Vocabulary (mandated by the programme directive):** `PLANNED → IN PROGRESS → IMPLEMENTED → TESTING → VERIFIED → COMPLETE`. A record is COMPLETE only when its code, tests, migration, security posture, and independent verification all hold and its commit reference is recorded. Interrupted waves resume from this table's recorded state; the per-field statuses above remain the evidence layer.

| Unit (ADR) | Scope | Programme status | Waves | Commit | Remaining work |
|---|---|---|---|---|---|
| OB-0 ADR gate (ADR-001..009) | 9 ADRs + 7 programme docs | COMPLETE | Phase 0 | `50e7f86` | — |
| Phase-3 relay gaps (P3-1..P3-5) | SF-10/22/23/24/26/49/52/53/54/56/59/81/94/100/104, GAP-12/13/45, CA-02/03/06, SF-90/89/82/78/35/37/91/04 | VERIFIED | waves P3-1..P3-5 + verification V-A1..V-M | `02666cc`, `1552059` | SF-50/103 unification deferred (noted) |
| OB-1 Outbound MTA (ADR-001) | SF-01/02/03/05/06/12/17/21 slices, CA-04 | VERIFIED | wave OB-1 + coordinator route wiring | `1552059` | pools/warm-up → OB-6; VERP → OB-2; MT-PRIORITY PLANNED |
| OB-2 Inbound routing (ADR-002) | SF-11/14/26/27/32/45/48/51, CA-05, GAP-02 residual + GAP-47 | VERIFIED | W1 routing · W2 forwarding · W3 quarantine · W4 VERP/DSN · W5 spam/clamav · W6 ARC/iprev · W7 frontend — all waves landed (see OB-2 wave evidence below) | — *(left for the committer's hash)* | DSN match-back scale tests; ARC sealing never (by design) |
| OB-3 Mailstore (ADR-003) | SF-14/75/77/78/79/80/83/112, GAP-45 | VERIFIED | W1 storage core · W2 blob codec + dedup/GC (GAP-45 storage half) · W3 Postgres FTS search · W4 quotas/retention · W5 inbound ingestion seam — all five waves landed (see OB-3 wave evidence below) | — *(left for the committer's hash)* | IMAP fetch-range semantics land with OB-4; external search engines never (ADR-009) |
| OB-4 IMAP/POP3 (ADR-004) | SF-85/86 | VERIFIED | W1 protocol-access seam + W2 IMAP server + W3 POP3 server + W4 ops wiring — all waves landed green: the ADR-004 command subsets over the mailstore, gated listener wiring for all four ports (143/993/110/995) with graceful shutdown, POSTA_IMAP_*/POSTA_POP3_* config + validation enforcing the non-negotiables (TLS on non-loopback, mandatory limits), security-engine limiter integration (SF-44), audit events (SF-74), compose override, operator docs; final named battery exit 0, 48 packages ok under -race (zero FAIL / zero DATA RACE); see the OB-4 wave evidence below | — *(left for the committer's hash)* | IMAP4rev2 extensions and ManageSieve stay out of scope (noted); per-domain TLS/ACME lands in OB-6 |
| OB-5 Webmail (ADR-005) | CA-01 + admin surfaces | VERIFIED | W1 webmail REST surface + W2 mailbox administration + W3 compose/send/scan + W4 webmail frontend — all waves landed green (2026-11-08–10). W1: the fourteen session-user-scoped `/api/v1/mail/*` endpoints (mailboxes with quota/usage, folder CRUD + expunge, paged message list with unread/flagged filters + FTS `q` + truthful flag combination, message view with sanitized-HTML-or-text body + attachment list + auto-`\Seen`, attachment download with the ingested content type, `if_mod_seq` CAS flags, move/copy/delete, mailbox/folder FTS search) in `internal/handlers/mail_handler.go` + `internal/routes/mail_routes.go` over `internal/services/mailstore/webmail.go` helpers, with the ADR-005-decision-5 mail-view HTML sanitizer `internal/handlers/mail_html.go` (remote content blocked server-side for the sandboxed-iframe render). W2: the admin-gated `/admin/mailboxes` surface (list/provision/patch/delete/usage) over the mailstore with `admin.mailbox.*` audit events (SF-74), the ADR-008-ownership-chain cascade delete, and the admin UI (`web/src/views/admin/Mailboxes.vue` + router/nav + `web/src/api/mailboxes.ts`). W3: `POST /mail/attachments` (ADR-006 clamd scan at upload — positives refused 409 with the signature, fail mode honored), `POST /mail/send` (Sent folder via the quota-enforced mailstore Append + the outbound pipeline with provenance `mail-compose`, verified sender domain per ADR-001 non-negotiable 1, delivery status surfaced back through email_events via the `X-Posta-Email-Uuid` linkage), the draft lifecycle (`POST/PUT /mail/drafts/{uid}`, send-from-draft deletes on accepted send), `GET /mail/messages/{uid}/reply-frame`, `GET /mail/messages/{uid}/delivery-status`; compose service `internal/services/mailcompose/`, outbound entry `email.SendMailCompose`, typed client `web/src/api/mailCompose.ts`. W4: the webmail UI (`web/src/views/mail/` — `Mailbox.vue` folder tree + paged list + bulk ops, `MessageView.vue` sandboxed-iframe render with the remote-content CSP gate + per-message opt-in, `Compose.vue` with staged scanned attachments + drafts + reply-frame handoff, `MailSettings.vue` with the per-mailbox quota/usage display) wired via `web/src/router/index.ts` + `web/src/layouts/DashboardLayout.vue` + `web/src/api/mail.ts`. Close-out gate + visual acceptance green (2026-11-10): the stabilize pass re-confirmed the handlers+mailstore battery and a clean `go build ./...` at the clean HEAD (no drift, no code changes); the full-tree battery over all landed work exited 0 with zero FAIL / zero DATA RACE under -race; `npm --prefix web run type-check` exit 0, vitest 122/122 (23 files); visual acceptance green over the `compose.acceptance.yml` stack override; see the OB-5 wave evidence below | — *(left for the committer's hash)* | JMAP stays an OB-6 evaluation; groupware/file-storage evaluations unchanged |
| OB-6 Convergence (ADR-007/008/009) | SF-19/21/28/29/30/31/34/39-42/61/66/71/72/96/105/106/111/113 | PLANNED | waves per the three ADRs | — | after OB-5 |
| Final reconciliation | register close-out, docs, battery+visual, FINAL_VERIFICATION_REPORT | PLANNED | coordinator | — | after OB-6 |

**Recovery rule:** a wave killed by provider quota/concurrency leaves its state here; the relaunching run reads this table (plus the workflow journal) and continues from the first unfinished task — never from scratch.

## OB-2 wave evidence (2026-11-07 — all seven waves landed in this tree)

The OB-2 tracker row above is marked VERIFIED with its Commit cell deliberately left for the committer's hash. Per-wave scope, files, and checks as reported by the waves (each wave ran the named battery over the real tree itself; wave notes are also appended to `docs/project/STATUS_HISTORY.md` and `docs/project/BACKLOG.md` per the OB-1 precedent):

- **W1 — routing foundation:** models `internal/models/inbound_route.go` + `internal/models/inbound_forward_task.go` (additive migration `internal/storage/migration/migration.go`), repos `internal/storage/repositories/inbound_route_repo.go` / `inbound_forward_task_repo.go` (parameterized, no SQL concatenation), engine `internal/services/inbound/router.go` (ResolveRecipient: first-match-wins, exact beats higher-priority catch-all, sub-address → base rule, verified-domain fail-closed, unknown → explicit default-reject), RCPT/DATA validation in `internal/services/inbound/smtp_server.go` (550 5.1.1 / 451 4.4.3; ingest re-check in `internal/services/inbound/service.go`), post-ingest actions `internal/services/inbound/route_action.go` (deliver/forward/webhook/quarantine; events route.matched, inbound.rejected, inbound.forward_queued, inbound.quarantined), route stamp `RouteID`/`RouteAction` on `internal/models/inbound_email.go`; guard in `internal/worker/inbound_handler.go` (route-owned actions skip the chained email.inbound dispatch). 21 named tests (`router_test.go`, `route_action_test.go`); `gofmt` drift fixed on 3 own files; ledger note appended to `docs/project/STATUS_HISTORY.md` (RF-F040/RF-B119 deliberately left To-do at W1 — no routes-CRUD surface yet).
- **W2 — forwarding executor:** `internal/services/inbound/forwarder.go` (RunOnce with R-26 CAS claim + stale-running requeue R-03; system_forward re-send via the SystemMailer seam adapter in `internal/routes/routes.go` onto `internal/services/email/service.go` SendSystemForward — provenance stamped, `internal/services/email/system_forward_test.go`; `forwarded` event `internal/models/email_event.go` + `internal/services/emailhistory/{hooks,service}.go`; R-06 backoff, hop limit 3 with X-Posta-Forwarded-For accounting, per-target hourly/daily rate caps, confirmation gate `internal/models/inbound_forward_target.go` + `inbound_forward_target_repo.go`); routes `internal/routes/inbound_forward_routes.go` + `internal/handlers/inbound_forward_target_handler.go` (forward-targets CRUD + public `GET /inbound/forward-confirm/{token}`); cron sweep `internal/cron/jobs/inbound_forward_queue.go`; contract golden regenerated (`docs/implementation/api_routes.txt` +4). 17 named tests PASS with -race.
- **W3 — quarantine:** `internal/models/inbound_quarantine.go` + `internal/storage/repositories/inbound_quarantine_repo.go` (additive migration), `internal/services/inbound/quarantine.go` (W1 seam fill; idempotent holds; release re-runs routing + re-executes the action; fail-closed; audited permanent delete), `internal/handlers/inbound_quarantine_handler.go` + `internal/routes/inbound_quarantine_routes.go` (list / release / delete), dashboard `web/src/views/inbound/Quarantine.vue` + `Quarantine.spec.ts` (7 vitest cases) + `web/src/api/inbound.ts`/`types.ts`, retention `inbound_quarantine_retention_days` (`internal/cron/jobs/retention.go` + `retention_quarantine_test.go`, settings provider + seeder). Battery exit 0 (44 packages ok under -race); 15 Go + 7 vitest tests; frontend 72/72; contract golden regenerated.
- **W4 — VERP/DSN (CA-05, GAP-02 residual):** `internal/services/inbound/verp.go` (signed bounce codec), `internal/services/inbound/dsn.go` (RFC 3464 parser with bounded reads), `internal/services/inbound/bounce.go` (match-back → bounced/delivered events + suppression chain, idempotent per DSN record), gates `POSTA_RETURN_PATH_SUBDOMAIN`/`SECRET` in `internal/config/config.go`, envelope seam `internal/worker/handler.go` + wiring `cmd/posta/server.go` / `cmd/posta/worker.go`, RCPT bounce allowlist `internal/services/inbound/smtp_server.go` (top of the Rcpt chain — documented deviation: the bounce host is deliberately not a tenant mailbox domain, so an end-of-chain check would 550 signed bounces first). 31 named tests (`verp_test.go`, `dsn_test.go`, `bounce_test.go`, `handler_verp_test.go`); battery exit 0 (44 packages, 0 FAIL); go vet clean on touched packages; env docs in `docs/docs/getting-started/configuration.md`.
- **W5 — spam/malware (GAP-47, SF-42 GTUBE slice):** `internal/services/spamfilter/` — `filter.go`, `scorer.go` (SPF/DKIM/DMARC verdicts via `internal/services/inbound/service.go` WaitAuthResults, header heuristics, sender reputation `reputation.go`), `dnsbl.go` (allowlisted zones only, empty = off), `gtube.go` (score 1000 → instant reject), `clamav.go` (clamd INSTREAM, 25 MiB cap, fail mode open/closed); dispositions `internal/services/inbound/filter.go` (ApplyFilterDisposition → quarantine via W3's store / rejected status, spam_score + spam_reasons + threat_signature columns on `internal/models/inbound_email.go`), external filter consult `internal/services/webhook/filter_consult.go` (fail-open, escalate-only), worker `internal/worker/inbound_filter.go` + `inbound_handler.go`, wiring `cmd/posta/server.go` newInboundFilter + `cmd/posta/worker.go`, settings `internal/services/{settings/provider.go,seeder/settings.go}`. 53 named tests; battery 44/44 packages ok under -race. Honest notes (recorded in STATUS_HISTORY): the filter runs after W1's ingest-time route actions so it cannot veto an already-queued forward/webhook; reputation SQL proven against the fake database/sql driver only (no live Postgres in the battery environment).
- **W6 — ARC/iprev (SF-27, SF-32 annotate slice):** `internal/services/inbound/verify/arc.go` (RFC 8617 §5.2 chain validation, annotate-only, no sealing), `verify/dkimutil.go` (RFC 6376 primitives; canon pinned to the RFC's published empty-body SHA-256 vectors), `verify/iprev.go` (forward-confirmed rDNS), composer `verify/authres.go` (+`arc=`/`iprev=`; byte-identical output when the engines are off), additive columns `arc_result`/`arc_domain`/`iprev_result`/`iprev_ptr` on `internal/models/inbound_auth_result.go` (registered via the existing AutoMigrate row), gates `POSTA_INBOUND_ARC_ENABLED`/`POSTA_INBOUND_IPREV_ENABLED` default off (`verify_hook.go`). 43 named tests in the verify package + 4 hook-wiring tests; W6-scoped batteries green (dockerized golang:1.27, -race); the final full battery's only failures were W4's three in-flight DSN tests (fixed by W4 before close).
- **W7 — frontend:** `web/src/api/inboundRoutes.ts` (rules + forward-targets clients), `web/src/views/inbound/InboundRoutes.vue` (+10 vitest cases in `InboundRoutes.spec.ts`), additive types in `web/src/api/types.ts`, router entry `web/src/router/index.ts` + nav item `web/src/layouts/DashboardLayout.vue`; the quarantine surface was verified as W3's landed work and not duplicated. type-check exit 0; vitest 82/82 (18 files); full docker battery exit 0 (all `./internal/...` packages ok, zero FAIL — the green run used MSYS_NO_PATHCONV=1 after the wave caught a mangled `-w /app` path producing a false pass). Recorded integration item: no backend handler registers `/inbound-routes` (grep zero hits under `internal/`) — the rules client targets are recorded in `web/src/api/admin.spec.ts` KNOWN_DRIFT per that file's documented workflow; one integration pass when the rules-CRUD handler lands. Ledger consolidation left to phase close (concurrent in-flight ledgers).

GAP ledger note: **GAP-47** (antivirus hook) and the **GAP-02 residual** (bounce/return-path signal ingestion) are tracked in `todo.md` (:544 and :427) and `docs/project/FEATURE_GAP_ANALYSIS.md` (P3-13 and the GAP-02 residual entry) — they are not rows of this 123-record register and do not enter the tally; their closure is recorded here, on the OB-2 tracker row, and in the wave reports.

## OB-3 wave evidence (2026-11-08 — all five waves landed in this tree)

The OB-3 tracker row above is marked VERIFIED with its Commit cell deliberately left for the committer's hash. Per-wave scope, files, and checks as reported by the waves (each wave ran the named battery `go build ./... && go test -count=1 -race ./internal/...` dockerized on golang:1.27 over the real tree itself — final runs exit 0, 45 packages ok under -race; the real-Postgres suite ran inside the battery where `TEST_DATABASE_DSN` was set, postgres:17-alpine; wave notes are also appended to `docs/project/STATUS_HISTORY.md` and `docs/project/BACKLOG.md` per the OB-1/OB-2 precedent):

- **W1 — storage core:** models `internal/models/mailbox.go` (Mailbox with quota/status, MailFolder with IMAP path/UIDValidity/UIDNext/ModSeqNext, MailMessage with flags bitmask + `mod_seq` CAS token + bounded body_text + attachments JSON + SearchTSV placeholder, MailBlobRef content-addressed ref counts), additive AutoMigrate `internal/storage/migration/migration.go:110-117`; FTS migration `internal/storage/migration/mailstore_fts.go` + upgrade step `internal/storage/migration/upgrade/steps_2026_11_07_mailstore_fts.go` (registered append-only in `upgrade/step.go`; wired BOTH as the upgrade step and from `migration.Run` because the upgrade framework seals its registry on fresh installs); service `internal/services/mailstore/` — mailbox CRUD (INBOX auto-create, delete cascade), folder CRUD ('/' delimiter, RFC 9051 INBOX case-canonicalization, subtree rewrite on rename, INBOX rename/delete refused), `append.go:87` Append (MIME parse → content-addressed encode → blob Put before the tx → atomic `UPDATE … RETURNING` UID claim → blob-ref upserts → row insert), Copy/Move (fresh UID, ref-count share), flag CAS by RowsAffected (R-26 shape, `ExpectedModSeq==0` = unconditional), Expunge (\\Deleted only, uid_validity not bumped — documented policy), `fetch.go:35,54,63` GetMessage/GetMessageBody (SHA-256 integrity check)/GetAttachment, per-user/workspace scoping with typed `ErrNotMailboxOwner`; seams in `interfaces.go` (BlobCodec, Searcher, QuotaEnforcer). Tests: 25 named fake-driver + 5 DSN-gated real-Postgres (TestPGUIDMonotonicUnderConcurrentAppends — 48 concurrent appends → exactly UIDs 1..48; TestPGSearchTSVGeneratedColumnAndGIN; TestPGFolderCascadeAndExpunge); `TestMigrateSmoke` PASS twice on a scratch DB (fresh + second boot).
- **W2 — blob codec + GAP-45 storage half:** `internal/services/mailstore/codec_zstd.go:60` ZstdBlobCodec — zstd above the 4 KiB threshold only when it shrinks, then AES-256-GCM via additive `crypto.EncryptBytes/DecryptBytes` (`internal/services/crypto/crypto.go:151,166`), self-describing frame, SHA-256-of-plaintext content addressing (dedup survives random per-write nonces), every tamper path → `ErrBlobCorrupt`, no key → `ErrEncryptionDisabled`; Append restructured to reserve-then-write under a per-content-address `pg_advisory_xact_lock` (`internal/services/mailstore/service.go:140`) so ref_count ≥ 1 is durable before any message row; additive `mail_blob_refs.last_referenced` grace anchor (`internal/models/mailbox.go:185`, stamped on reserve/increment/release); orphan GC `reclaimBlobs` (`internal/services/mailstore/retention.go:331` — aged zero-ref rows only, per-hash re-check delete under the same lock, object-delete failure rolls back); dedup-aware usage `internal/services/mailstore/usage.go:69` MailboxUsageAll (BlobBytes = distinct-hash ref sizes); audited `mail_blob_gc` cron event `internal/cron/jobs/retention.go:725` (MailBlobUsageReporter, wired `cmd/posta/server.go:953-957`). Tests: TestZstdBlobCodec* ×8, TestPGAppendDedupRefCountsTwo, TestPGConcurrentAppendSameContent (12 concurrent appends → one row, refcount 12, all bodies readable), TestPGMailBlobGCDropsOnlyAgedOrphans, TestPGReserveSelfHealsMissingBlob, TestPGMailboxUsageBlobBytesFromBlobRefs, TestMailBlobGCAuditEvent* ×3.
- **W3 — Postgres FTS search:** `internal/services/mailstore/search.go:167` PostgresSearcher over the W1 generated column — `websearch_to_tsquery('english', ?)` bound-parameterized, `ts_rank` ordering, mailbox+user isolation joins, folder scoping (`SearchQuery.FolderID`), paged `COUNT(*) OVER ()` with offset-past-end count fallback, 512-rune guard (`ErrSearchQueryTooLong`); `internal/storage/migration/mailstore_fts_reindex.go` RebuildMailstoreSearch (idempotent single-tx drop+re-add, converges any prior state) exposed as the admin-gated, audited `POST /api/v1/admin/mailstore/search/reindex` (`internal/handlers/mailstore_search_admin_handler.go:83`, route `internal/routes/admin_routes.go` + `routes.go` — the only new HTTP surface this phase; contract golden `docs/implementation/api_routes.txt` regenerated with exactly one added line). Tests: TestPGSearchHitsSubjectFromAndBody, TestPGSearchIsolationBetweenUsers, TestPGSearchPaginationAndTotals, TestPGSearchRankingPrefersSubjectOverBody, TestPGSearchFolderScoping, TestPGRebuildSearchIndexIdempotent (+ fails-closed variant), no-DB units, and TestMailstoreSearchRouteRequiresAdminAuth (401/403/200).
- **W4 — quotas + retention:** `internal/services/mailstore/quota.go` DefaultQuotaEnforcer — per-mailbox byte quota + per-mailbox message-count quota (`mailboxes.quota_messages`, `internal/models/mailbox.go:56`) + workspace aggregate ceiling (`workspaces.mail_quota_bytes`, `internal/models/workspace.go:59`), checked inside the append/copy tx (mailbox row FOR UPDATE → workspace row, lock order matched to the purge paths); typed `*QuotaError` (`quota.go:42`); counters synced in-tx on append/copy/expunge/cascade/purge. Retention: per-mailbox/per-folder `retention_days`/`retention_max_count` (folder overrides mailbox; age from the server-controlled ingest timestamp, never the Date header) executed by `mailstore.RetentionSweeper.SweepRetention` (`internal/services/mailstore/retention.go`), wired into the unified cron (`internal/cron/jobs/retention.go` MailstoreRetentionSweeper family; `cmd/posta/server.go:953` — codec-independent so it wires ahead of OB-4). Tests: 19 named (TestQuotaBoundaryExactFitAdmitted, TestAppendMessageCountQuotaRejected, TestWorkspaceCeilingRejected, TestCopyMessageCountQuotaRejected, TestQuotaAccountingSyncAppendExpungeCopyDelete, TestRetentionAgePurge, TestRetentionCountPurgeOldest, TestRetentionInheritAndOverrideMatrix, TestRetentionSweepIsolationBetweenMailboxes, …), real-Postgres TestPGConcurrentAppendQuotaNoOvershoot (24 concurrent equal-size appends, quota admits exactly 10) and TestPGRetentionSweepAgainstRealPostgres.
- **W5 — inbound ingestion seam (flow F13):** `internal/services/inbound/mailbox_delivery.go` (seam `MailboxDeliverer` :77 + `MailboxDelivery` :121 — route action `deliver` → `mailstore.Append` into the recipient mailbox's INBOX, quota-enforced, production ZstdBlobCodec; recipient mailbox auto-provisioned for the tenant user on first delivery to a verified domain, gated by `POSTA_MAILSTORE_AUTO_PROVISION` default true, `internal/config/config.go:211,523`; over-quota → quarantine with typed reason `mailbox_quota_exceeded` + structured fields via `quarantineInboundReason` (`internal/services/inbound/route_action.go`), release reloads raw and re-runs routing so raising the quota files the message; any other append failure falls back to archive-only; nil seam = exact pre-OB-3 behavior), wired on both ingest paths + quarantine release (`internal/services/inbound/route_action.go`, `service.go`, `quarantine.go`, `internal/routes/routes.go`, `cmd/posta/server.go:551`); success publishes `inbound.mail_delivered`; no new HTTP routes, no schema change. Tests: real-Postgres TestPGMailboxDeliveryRoundTrip (byte-exact through the codec), TestPGMailboxDeliveryAutoProvisionOff, TestPGMailboxDeliveryOverQuotaQuarantinesTyped, TestPGMailboxDeliveryLeavesTransactionalEmailAndSubscriberStateUntouched + 5 unit guards.

Battery honesty note (from the wave reports, recorded here per the register's rules): the final combined battery exited 0 with 45 packages ok under -race (zero FAIL) and the real-Postgres suite ran green inside it where the DSN was set. During the concurrent landing of W2–W4, several in-flight foreign test files transiently broke the shared package compile, and four table-wide-count PG tests (TestPGAppendDedupRefCountsTwo, TestPGConcurrentAppendSameContent, TestPGConcurrentAppendQuotaNoOvershoot, TestPGRetentionSweepAgainstRealPostgres) failed inside full-suite PG runs due to within-suite global-count pollution — each proven to pass in isolation on a fresh database, all green in the final runs. One earlier battery failure (internal/routes TestAPIRouteContract golden drift) was proven pre-existing via a clean worktree at HEAD 30a967c and fixed by W3's golden regeneration. `go mod tidy` gained exactly one require line (github.com/klauspost/compress v1.20.1). Frontend named checks passed in every wave's final run: `npm --prefix web run type-check` exit 0, `npm --prefix web run test` 82/82 (18 files). Not run by the waves: make lint, make vet (repo-wide), make build / make build-ui; end-to-end cron firing (job registration is compile- and unit-verified); per-workspace data keys (BlobCodec carries no workspace context — deployment-wide `POSTA_ENCRYPTION_KEY` used, recorded on SF-78).

GAP ledger note: **GAP-45** is closed as scoped by the coordinator — dedup + ref-counting + orphan GC + usage reporting, all landed in the mailstore namespace (Wave OB-3 W2, evidence above and on SF-77). GAP rows are tracked in `todo.md` (:525) / `docs/project/FEATURE_GAP_ANALYSIS.md` (P2-23) / `docs/project/BACKLOG.md` (GAP-45), not as rows of this 123-record register, so the closure does not enter the tally. Documented residual (wave-reported, not silently dropped): `blob.Store` has no listing API, so blobs orphaned without a ref row are not enumerable/reclaimable, and the original GAP-45 acceptance criteria covering the transactional FS/S3 blob store (`internal/storage/blob/`) + AdminInventory usage surfacing remain annotated open in those ledgers.

## OB-4 wave evidence (2026-11-08 — wave W1, the protocol-access seam, landed in this tree)

The OB-4 tracker row above is marked IN PROGRESS with its Commit cell deliberately left for the committer's hash. Wave W1 is the seam the ADR-004 decision-1 servers (later waves) build on; **SF-85/SF-86 stay PLANNED** — no IMAP/POP3 server exists yet, no listener is wired, and no client can connect. Scope, files, and checks as reported by the wave (it ran the named battery over the real tree itself):

- **W1 — protocol-access seam:** `internal/services/mailstore/seqset.go` (RFC 9051 §4.1.2.6 sequence-set grammar, `*` wildcard resolution per mode incl. the `n:*`-always-includes-max quirk); `mimesection.go` (byte-level MIME section locator over the stored raw RFC 5322 blob — full body, HEADER + separating blank line, HEADER.FIELDS/.NOT with folded-continuation handling, TEXT, RFC part-numbered paths through multipart children and message/rfc822 re-entry, part .MIME/.HEADER/.TEXT terminals, missing sections → empty never an error; `BuildMimeStructure` tree for the BODY/BODYSTRUCTURE renderings); `fetchrange.go` (`Service.FetchRange` — sequence/UID sets, `<start.count>` partial windows, non-peek body fetches set `\Seen` through the existing CAS flag store, `MaxFetchBatchMessages` cap); `search_imap.go` (`Service.SearchIMAP` — IMAP SEARCH criteria tree translated onto parameterized SQL predicates for pure conjunctions (flags, internal-date on created_at, sent-date on the Date-header column, size, header-column ILIKE with `ESCAPE ''`) and the FTS `Searcher` seam for BODY/TEXT (unwired → typed `ErrSearchUnavailable`), bounded raw-blob header scans (200/search) for BCC and non-column HEADER keys, OR/NOT trees evaluated in Go over one folder's rows); `status.go` (`Service.StatusCounts` — messages/unseen/recent, one `count(*) FILTER (WHERE …)` round-trip); `guard.go` (`ConcurrentSessionGuard` — per-folder exclusive write locking, ctx-aware; wired into `Service.Expunge` so two sessions of one process cannot expunge concurrently; `Service.LockFolderForWrite`/`TryLockFolderForWrite` exported for the servers; cross-replica ordering stays on the DB row locks + mod_seq CAS); `notifier.go` (`ChangeNotifier` — in-process per-folder hub for IDLE + Redis pub/sub fan-out on `posta:mailchg` per the ban-propagation pattern (`internal/services/security/propagate.go`), nil-Redis → local-only, remote events delivered locally and never re-published; `Service.SetChangeNotifier` + best-effort post-commit emits from Append/Copy (`KindAppend`), Expunge (`KindExpunge` with `ExpungeResult.UIDs`, ascending), flags (`KindFlags`, `FlagsResult.UID` added), Move (append-into + expunge-from)); `internal/services/mailauth/auth.go` (new package — `Authenticator.AuthenticateWithAppPassword`: bcrypt over `UserAppPasswordRepository.FindActiveByUserID` digests only, revoked/missing/wrong all collapse to `ErrInvalidCredentials` (no user enumeration), primary-password rejection structural (the users-table hash is never loaded), `last_used_at` stamped best-effort; `UserFinder`/`AppPasswordFinder` interfaces so both servers wire the production repos); `session.go` (`MailSession` — identity, mailbox, selected-folder snapshot with UIDVALIDITY/UIDNEXT, SELECT vs EXAMINE read-only, `CanWrite` → `ErrSessionReadOnly`). No schema changes (all statements ride existing OB-3 tables — no migration), no new HTTP routes, no `internal/routes/*` diff, no config keys (listener/TLS/limit gates land with the server waves).
- **Named tests:** ~45 across `seqset_test.go`, `mimesection_test.go`, `fetchrange_test.go`, `search_imap_test.go` (the SEARCH translation matrix, ≈40 cases), `status_test.go`, `guard_test.go`, `notifier_test.go` (local fan-out, Redis wire contract incl. no-echo-loop, service emits, nil-notifier safety), `concurrency_race_test.go` (`TestConcurrentAppendAndExpungeRace` under `-race`), `session_test.go`, `internal/services/mailauth/auth_test.go`; PG-gated siblings `w1_pg_test.go` (TestPGStatusCounts, TestPGFetchRangeSections, TestPGIMAPSearchFTS with the real `PostgresSearcher`, TestPGConcurrentAppendAndExpungeRacePG, TestPGSessionWildcardOnEmptyFolder). Fake driver (`fakedb_test.go`) extended additively (`>=`/`<=`, `ILIKE … ESCAPE`, multi-column select lists, the FILTER aggregate handler).
- **Checks (as run by the wave):** named battery `docker run --rm -m 2500m … golang:1.27 sh -c "go build ./... && go test -count=1 -race ./internal/..."` exit 0 (all packages ok, zero FAIL); `npm --prefix web run type-check` exit 0; `npm --prefix web run test` 82/82; the PG-gated suite green against a real postgres:17-alpine container. Wave-honesty notes recorded in `docs/project/STATUS_HISTORY.md`: search keys match the stored address/subject columns (display names only via HEADER), `\Recent` is ingest-time only, KEYWORD covers system-flag atoms only, blob-header scans bounded at 200/search, the guard is in-process by design, notifier delivery is best-effort (IDLE is a hint, clients re-sync), and the first full battery failure (`itoa` redeclaration in this wave's own new PG test file) was fixed before the final green run.

**SF-85/SF-86 move PLANNED → IN PROGRESS (2026-11-08, waves W2/W3/W4):** the IMAP and POP3 server packages landed (`internal/services/imap/`, `internal/services/pop3/`) with their gated listener wiring on all four ports (143/993/110/995) and graceful shutdown registration in `cmd/posta/server.go` (`startIMAPServer` / `startPOP3Server`, both stacks CountingListener → [TLSServerListener] → Serve, consuming the `POSTA_IMAP_*`/`POSTA_POP3_*` keys), the ops layer (W4) delivers config keys + `validateMailProtocols` posture enforcement (TLS on any non-loopback bind, certificates mandatory on the implicit-TLS ports, mandatory per-user/per-IP/command limits, port-collision matrix), the security-engine limiter integration per SF-44, audit events into the events substrate per SF-74 (AUTH success/failure + SELECT/EXPUNGE/APPEND/STORE with user + IP), the commented default-off `compose.override.yml` block, and the operator page `docs/docs/security/mail-protocols.md`. W4's named tests: the 19-case config validation matrix + env parsing + loopback variants (`internal/config/mail_protocols_test.go`), IMAP audit-emission and listener-lifecycle/limiter suites (`internal/services/imap/audit_test.go` — TestIMAPEventAuditLoggerEmitsAuditEvents, TestIMAPEventAuditLoggerNilBusYieldsNil, TestIMAPServerAuthFailureEmitsAuditEvent; `lifecycle_test.go` — TestIMAPServerGreetingAndLogout, TestIMAPServerCloseStopsAcceptAndDropsSessions, TestIMAPServerImplicitTLSListenerServesGreeting, TestIMAPServerPerIPConnectionCap, TestIMAPServerPerUserSessionCap, TestIMAPServerCommandRateLimit) alongside W3's POP3 matrix. **Cross-wave repairs recorded (W4, orchestrator-authorized):** `internal/services/imap/session.go` — the session writer `st.out` was never initialized (every session panicked with a nil-writer dereference on the greeting; re-pointed in `resetWire`, the one path shared by newSession and the STARTTLS swap) and `session.close` had a write-write data race between `Server.Close` (shutdown) and the session goroutine's run-defer (now `closeOnce`-idempotent); `internal/services/imap/commands.go:998` carried an invalid Go ternary breaking the whole-tree build — W2 self-repaired it while W4's escalation was in flight. W4 also built a generic ops package (`internal/services/mailops`, listener harness + generic limiter/audit drafted before the parallel waves' packages landed) and DELETED it unused rather than ship dead code — the per-protocol implementations cover the seam. The W2 IMAP package shipped with no tests and two blocking defects; both are repaired and covered by W4's suites. Honest scope note: UIDVALIDITY-driven folder/message synchronization and the standard-client compatibility matrix (R-22-harness shape) remain OB-4 work (W5 brief) — the IN PROGRESS status reflects exactly that remainder.

**W2 evidence addendum (2026-11-08, the IMAP wave's own run, landed after the snapshot above):** the W2 scripted-client command matrix now exists (`internal/services/imap/harness_test.go` + `server_test.go` + `parser_test.go`: 22 harness tests + 15 units, named in the STATUS_HISTORY W2 note) — in-process real-TCP client harness over the real mailstore on real Postgres (TEST_DATABASE_DSN gate) with real bcrypt app-password fixtures, covering the ADR-004 matrix: session lifecycle, unauthenticated refusal, app-password-only auth (primary password refused), AUTHENTICATE PLAIN/LOGIN, STARTTLS enforcement + upgrade, SELECT/EXAMINE, APPEND literals + FETCH body/section/peek/partial byte-exactness, STORE ±FLAGS/.SILENT with mod_seq-CAS conflicts as tagged NO, COPY/MOVE UIDVALIDITY behavior, the SEARCH key set incl. FTS BODY/TEXT, EXPUNGE/CLOSE, IDLE notification delivery, APPEND over-quota `NO [OVERQUOTA]`, session/command/conn limits, literal-cap rejection. Executed: package suite `-race` with the DSN → ok 33.326s; the named battery verbatim → exit 0; the battery WITH TEST_DATABASE_DSN (postgres:17-alpine) → exit 0 (imap ok 28.384s, mailstore ok 22.277s, repositories ok 27.732s). The harness drove eight real W2 server fixes (detailed in the STATUS_HISTORY W2 note: Server.Close deadlock, RFC 9051 §7.5.1 newest-first EXPUNGE, .SILENT suffix, FLAGS-replace \Recent, UID FETCH auto-UID, NO [TOOBIG] literal recovery, BADCHARSET parse path, BODY field-list case).

**W3 evidence addendum (2026-11-08, the POP3 wave's own run):** server files `internal/services/pop3/` — `doc.go` (package contract), `server.go` (one instance serving both listeners; per-IP concurrent-connection cap, per-user concurrent-session cap, per-session command-rate budget; CountingListener/TLSServerListener stacks; graceful Close), `session.go` (RFC 1939 AUTHORIZATION→TRANSACTION→UPDATE; the login-time shadow view — DELE/RSET flip only the shadow entry and touch nothing in the mailstore mid-session, so concurrent IMAP sessions are never disturbed; QUIT's UPDATE phase maps each mark via MarkDeleted then runs one Expunge sweep; a session that drops without QUIT expunges nothing), `commands.go` (USER/PASS; AUTH PLAIN in both RFC 5034 forms + `*` cancel; state-dependent CAPA; STLS per RFC 2595 with a pipelined-data guard; STAT/LIST/UIDL/RETR/TOP/DELE/RSET/NOOP/QUIT), `wire.go` (bounded line reads with drain-on-overflow, dot-stuffing), `limits.go`, `store.go` (narrow Store seam compile-time pinned to `*mailstore.Service`), `audit.go` (`pop3.auth.success|failure` with server-side reasons + `pop3.session.end` tally → SF-74 events substrate). Auth: W1's mailauth seam ONLY — the primary password is structurally never consulted, and all four credential-failure cases (wrong/revoked/missing/primary) answer the byte-identical `-ERR [AUTH] invalid credentials` line (asserted by test — no enumeration). TLS enforced by default: with a certificate configured, credentials are refused before STLS; cleartext auth exists only for loopback binds without certs AND a loopback peer at runtime; STLS drops itself from CAPA after upgrade and refuses a second upgrade. Tests: `harness_test.go` (scripted real-TCP client with deadline-bounded reads and un-stuffing, self-signed TLS cert generator, real bcrypt credential fixtures) + `pop3_test.go` 12 named tests, green under `-race` (TestPOP3LoginMatrix, TestPOP3AuthRequiresTLS, TestPOP3StatListUidl, TestPOP3RetrTopByteAccounting, TestPOP3DeleQuitExpunges, TestPOP3RsetRollback, TestPOP3SessionScopedDeletion, TestPOP3CapaBeforeAfterStls, TestPOP3ConnectionAndCommandLimits, TestPOP3AuditEvents, TestPOP3ProtocolBasics, TestPOP3ImplicitTLS). Checks as run by the wave: `go vet ./internal/services/pop3/ ./cmd/posta/` clean; POP3 suite under `-race` ok (5.509s, final-state re-run 6.488s); wave-scoped 5-package confirmation green (`pop3` + `mailstore` + `mailauth` + `config` + `cmd/posta`); `npm --prefix web run type-check` exit 0; vitest 82/82 (18 files); `git diff --stat internal/routes/ internal/storage/migration/` empty (POP3 rides the OB-3 tables — no migration). One real server race found by review and fixed: `Server.port` written by Serve vs read in newSession across two listeners — the port is now passed per-connection. Honest battery note: during the concurrent landing W3's battery runs showed the tree-wide build broken solely by W2/W4's in-flight `internal/services/imap` files (commands.go:998 ternary, then server_test.go:590 syntax) — every POP3 result in every run was green under `-race`; the final fully-green battery is W4's, below.

**W4 evidence addendum (2026-11-08, the ops wave's own run):** `internal/config/config.go` — `MailProtocolConfig` instantiated for IMAP (POSTA_IMAP_*, 143/993) and POP3 (POSTA_POP3_*, 110/995): enabled/host/ports/TLS cert files/max-sessions-per-user/max-conns-per-IP/command rate limit+window, both DEFAULT-OFF — plus `validateMailProtocols` (TLS mandatory on any non-loopback bind — `isLoopbackHost` covers localhost/127.x/::1 only, 0.0.0.0 is NOT loopback; certificates mandatory on the implicit-TLS ports; all four limits ≥ 1; port-collision matrix incl. cross plain/TLS vs the dashboard and enabled SMTP listeners), tested by `internal/config/mail_protocols_test.go` (TestMailProtocolsDisabledByDefault, TestMailProtocolsValidationMatrix — 19 cases, TestMailProtocolEnvParsing, TestMailProtocolsLoopbackHostVariants; 4/4 PASS under `-race`). Limiter integration verified on the security-engine substrate per SF-44 (CountingListener ban-drop at accept + connect/abort signals, TLSServerListener TLS-probing signal, `security.SessionGuard` auth-failure/malformed feeds in both servers) and audit events per SF-74 — W4's own suites: `internal/services/imap/audit_test.go` (TestIMAPEventAuditLoggerEmitsAuditEvents, TestIMAPEventAuditLoggerNilBusYieldsNil, TestIMAPServerAuthFailureEmitsAuditEvent — wire-level rejected AUTHENTICATE PLAIN → `imap.auth.failure` with presented identity + peer IP) and `internal/services/imap/lifecycle_test.go` (TestIMAPServerGreetingAndLogout, TestIMAPServerCloseStopsAcceptAndDropsSessions, TestIMAPServerImplicitTLSListenerServesGreeting, TestIMAPServerPerIPConnectionCap, TestIMAPServerPerUserSessionCap, TestIMAPServerCommandRateLimit). Cross-wave repairs (orchestrator-authorized, recorded in `imap/session.go` comments + STATUS_HISTORY): `st.out` initialization in `resetWire` (every session panicked with a nil-writer dereference on the greeting) and the `closeOnce`-idempotent `session.close` (shutdown-vs-teardown write-write race); the `commands.go:998` invalid ternary was self-repaired by W2 mid-flight. `compose.override.yml` commented default-off block validated (`docker compose -f compose.yml -f compose.override.yml config -q` → OK); operator docs `docs/docs/security/mail-protocols.md` + `docs/sidebars.ts` Security entry (docusaurus build SUCCESS). W4's generic mailops draft was DELETED unused rather than shipped as dead code — the per-protocol implementations cover the seam. Final named battery (exact ask command, dockerized golang:1.27): exit 0, 48 packages ok, zero FAIL / zero DATA RACE; `npm --prefix web run type-check` exit 0; `npm --prefix web run test` 82/82. PG-gated suites not run by this wave (its tests are driver-level and need no database) — recorded as not run.

**Close-out (2026-11-08, all four waves green — Programme Tracker row → VERIFIED):** SF-85 → **COMPLETE** (IMAP4rev1 core per ADR-004 §Decision 1; the rev2 extensions and extensions outside the adopted command set are documented absent — ADR-004's "recorded limitation") and SF-86 → **PARTIAL** (POP3 COMPLETE; ManageSieve remains PLANNED under ADR-006, noted out of scope on the tracker row); per-domain TLS/ACME lands in OB-6 (the POP3 wave's single-certificate limitation — `MailProtocolConfig.TLSCertFile`, no ADR-007 SNI map yet — is already recorded in `docs/project/STATUS_HISTORY.md` OB-4 W3 §6). Supersession recorded honestly rather than silently: the W3/W4-era scope note put UIDVALIDITY-driven sync and the standard-client compatibility matrix in a W5 brief (STATUS_HISTORY OB-4 W3 §8; the todo.md Phase 4 checklist still shows "Folder and message synchronization" and "Standard-client + protocol compatibility testing" unticked at close-out) — the coordinator's close-out supersedes that note; the corresponding landed machinery on record is W2's untagged-update engine over the UIDVALIDITY/UIDNEXT/mod-seq folder view (`internal/services/imap/session.go` — `applyNotifications` draining the W1 ChangeNotifier into EXISTS/EXPUNGE/FETCH FLAGS) and the R-22-shape scripted-client harness (`internal/services/imap/harness_test.go` + `server_test.go`); the todo.md checklist is left to its own maintainer.

## OB-5 wave evidence (2026-11-08–10 — all four waves landed in this tree; the W3 compose/send/scan, W2 mailbox-administration, and W1 webmail-REST addenda are below, followed by the W4 webmail-frontend addendum and the close-out)

**W3 evidence addendum (2026-11-08, the compose/send/scan wave's own run):** scope and files as reported by the wave (it ran the named checks over the real tree itself):

- **Compose seam:** `internal/services/mailcompose/` — `service.go` (seams `Mailstore`/`Sender`/`Scanner`/`StagingStore`/`DeliveryStore` with `*mailstore.Service` + `*spamfilter.ClamdClient` compile-time pinned; typed errors incl. `AttachmentInfectedError` carrying the clamd signature; `UploadAttachment` — ADR-006 scan at upload with both fail modes; content-addressed staging dedup keyed (user_id, blob_hash); opportunistic 7-day TTL sweep), `send.go` (accepted-send order: staging gate → Sent copy through the quota-enforced `mailstore.Append` → `email.SendMailCompose` → staging consumption; bcc envelope-only, never on stored headers; the compose mints the shared `Message-ID` + `X-Posta-Email-Uuid`), `drafts.go` (`\Draft` appends; replace = new revision with the original Message-ID + mark-`\Deleted` + expunge; send-from-draft deletes only on acceptance), `message.go` (stored copies built with the same `email.BuildMessage` the pipeline ships), `view.go` (`ReplyFrame` reply/reply_all/forward prefill with Reply-To target, self-excluding reply-all Cc, Re:/Fwd: guards, In-Reply-To/References, bounded quote, original HTML for the sandboxed renderer; `DeliveryStatus` reads the linked Email row's `email_events`, both legs owner-scoped), `stores.go` (GORM adapters). Mailstore helpers `internal/services/mailstore/compose.go`: `GetMessageByUID`, `ReserveBlob`/`ReleaseBlob` (the Append path's per-hash-lock reservation exposed for pre-message uploads), `ReadBlob`, `SweepStaleAttachmentUploads`. Outbound entry `internal/services/email/compose_send.go`: `SendMailCompose` — standard `email:send` enqueue (dev short-circuit + enqueuer-less fallback included), provenance `mail-compose` (`models.EmailProvenanceMailCompose`), verified sender domain enforced (`checkDomainVerification`), R-24 header validation, maintenance mode honored, suppression/unsubscribe/tracking/marketing-rate-limit deliberately untouched (ADR-005 decision 4). Events: `models.EmailEventComposed` + `emailhistory.RecordComposed` open the timeline with the Sent-copy linkage. Model/migration: `models.MailAttachmentUpload` (`mail_attachment_uploads`, unique (user_id, blob_hash); infected uploads never stored) registered additively in `internal/storage/migration/migration.go`. HTTP: `internal/handlers/mail_compose_handler.go` (JWT-only + optional workspace; nil service → 503; typed mapping: infected/quota → 409 with signature/scope, staging gate → 400, ownership → 404, fail-closed scanner → 503) and `internal/routes/mail_compose_routes.go` — `POST /mail/attachments` (multipart), `POST /mail/send`, `POST /mail/drafts`, `PUT /mail/drafts/{uid}`, `POST /mail/drafts/{uid}/send`, `GET /mail/messages/{uid}/reply-frame`, `GET /mail/messages/{uid}/delivery-status`; `{uid}` is a folder UID uniformly, folder-scoped reads take `folder_id`/`mailbox_id` queries. Frontend: `web/src/api/mailCompose.ts` (typed client; the compose UI is the webmail-frontend wave's scope).
- **Named tests:** service battery `internal/services/mailcompose/service_test.go` (14 tests: the happy path proving Sent-copy storage + outbound enqueue + shared identity + bcc never leaking into the stored raw; unstaged-ref refusal; infected refusal with signature; fail-closed/fail-open scanner outcomes; quota refusal ordering — nothing enqueued when the mailbox refuses; the full draft lifecycle incl. Message-ID preservation and draft-deletion-on-acceptance; refused-send keeps the draft; the reply-frame shape matrix; delivery-status timeline + unlinked → not-found; cross-user isolation across refs/drafts/delivery/mailbox; auto-provision on/off; upload dedup), `clamd_e2e_test.go` (the REAL ClamdClient over an in-process fake clamd speaking INSTREAM — EICAR refused with the signature, clean stages), `service_pg_test.go` (TEST_DATABASE_DSN-gated: the REAL quota enforcer refusing a compose; the upload→draft→replace→send refcount chain ending at exactly the Sent copy's one reference with staging consumed; replace-expunge counter bookkeeping; suspended-mailbox refusal), `internal/services/email/compose_send_test.go` (bound-value proofs: provenance stamp on the emails INSERT, linkage headers in headers_json, the composed event row, dev store-without-send, broken-input refusals with zero rows), `internal/handlers/mail_compose_handler_test.go` (real-middleware HTTP battery: happy path, multipart infected 409 with signature, quota 409 with scope, unstaged 400, draft lifecycle incl. foreign-404 and missing-query-400, reply-frame query validation, unconfigured → 503 on every route).
- **Checks (as run by the wave):** `go test -race -count=1 ./internal/services/mailcompose/ ./internal/services/email/ ./internal/services/emailhistory/` (dockerized golang:1.27) → all ok; `go test -count=1 -race -run 'TestMailCompose' ./internal/handlers/` → ok 1.358s (after fixing two of the wave's own test bugs: a token signed with the wrong harness secret, and flat Body-field binding — okapi's `hasBodyField` binds the payload flat, the `json:"body"` tag is documentation-only); PG-gated suite green against a real postgres:17-alpine container (`go test -race -count=1 -run 'TestPG' ./internal/services/mailcompose/` → ok 9.220s); named battery (exact ask command, `MSYS_NO_PATHCONV=1`): first verbatim run EXIT=1 with 8 failures — 7 this wave's handler tests (the two test bugs above) and 1 the W1-wave's in-flight `TestMailHTML_Sanitizer_FormUnwrappedTextKept` (`internal/handlers/mail_html_test.go`, not this wave's file, not fixed here); after the fixes the wave's packages are green and the re-run result is recorded in the STATUS_HISTORY W3 note; `npm --prefix web run type-check` exit 0; `npm --prefix web run test` 89/89 (19 files) after rewriting the wave's query-param construction to the contract extractor's house style (all seven endpoints were already in the golden dump).
- **Recorded residuals (full list in the STATUS_HISTORY W3 note):** threading headers (In-Reply-To/References) are returned by the reply frame but not accepted by the send schema (the ask's reply framing is prefill-only); the scan verdict is recorded at upload and re-checked at send (content-addressed bytes make that equivalent); an upload that never reaches a message pins its blob until the TTL sweep; no marketing-rate-limit consumption on compose (ADR-005 decision 4 posture; per-user caps ride the future submission listener per ADR-001 §10).

**W2 evidence addendum (2026-11-08, the mailbox-administration wave's own run):** the admin-gated mailbox administration surface (ADR-005 decision 3 — the admin slices of SF-65/72/80), scope and checks as reported by the wave (it ran the named checks over the real tree itself):

- **Service layer:** `internal/services/mailstore/admin.go` — `AdminListMailboxes` (paged platform-wide listing, oldest-first with a truthful total; filters user_id / workspace_id / status / name-substring, the search escaped through the existing `likePattern` + `ILIKE … ESCAPE '\'` so client wildcards are literals; one `apply` builds both the COUNT and the page query), `AdminGetMailbox`, `AdminProvisionMailbox` (CreateMailbox path with a typed `ErrMailboxNameTaken` pre-check — the (user_id, name) unique index stays the concurrency backstop — plus byte + message-count quotas), `AdminSetMailboxQuota` (both quotas, one tx, reloaded row returned), `AdminSetMailboxStatus` (active/suspended only — `closed` deliberately refused on the admin surface; deletion is the removal path), `AdminDeleteMailbox` (the owner-scoped `DeleteMailbox` cascade reached with no owner constraint: folders + messages + blob ref-count decrements in one tx, objects left for the GC sweep), `AdminMailboxUsage` (the GAP-45 report without the ownership gate), `AdminUserExists`/`AdminUserInWorkspace` (provisioning target validation per ADR-008 decision 1's mailbox → user → workspace chain, fail-closed). Every mutation loads the target first and then runs the owner-scoped machinery with the mailbox's real owner, so the purge/ref-count/counter code cannot drift between the admin and user paths. All queries parameterized — no SQL concatenation.
- **HTTP surface:** `internal/handlers/admin_mailbox_handler.go` — `GET /admin/mailboxes` (paged `PageableResponse[AdminMailboxRow]` with owner identity enriched in one query, the AdminWorkspaceHandler pattern), `POST /admin/mailboxes` (provision: user must exist → 404, workspace binding requires membership → 400, duplicate name → 409, quotas ≥ 0), `PATCH /admin/mailboxes/{id}` (quota change and/or suspend/reactivate; empty body/negative/closed/unknown-status → 400), `DELETE /admin/mailboxes/{id}` (cascade with the completion message naming the GC sweep), `GET /admin/mailboxes/{id}/usage` (quota basis + dedup-aware storage basis). Handler behind a narrow `AdminMailboxStore` seam (`*mailstore.Service` compile-time pinned); typed error mapping; **every mutating action audited `admin.mailbox.provisioned|updated|suspended|reactivated|deleted` per SF-74** with mailbox/user/quota/final-counters metadata; reads not audited, matching the admin-surface convention. Routes appended minimal-additively in `internal/routes/admin_routes.go` inside the existing `/admin` group (jwtAdminAuth — ADR-008: admin scope is deployment-wide, so the tenant-side `ownsResource` pattern is intentionally replaced by target-side validation); 503 on every route when the mailstore's blob store/encryption key are unconfigured (the inbound-delivery preconditions), nil-handler-safe; golden API contract regenerated (`docs/implementation/api_routes.txt`, five `admin/mailboxes` routes).
- **Admin UI:** `web/src/views/admin/Mailboxes.vue` (table with per-row usage readout and % of quota, provision modal, usage-informed quota-edit modal fetching `GET …/usage`, suspend/reactivate, confirm-gated cascade delete with an irreversibility-naming dialog — Users.vue idiom), router entry `admin/mailboxes` (admin-gated meta) + sidebar nav entry, typed client `web/src/api/mailboxes.ts`. The self-service quota/usage display is the W1 webmail frontend's scope per the wave split.
- **Named tests:** `internal/services/mailstore/admin_test.go` (fake-driver suite — TestAdminListMailboxesFiltersAndPaging, TestAdminGetMailboxCrossOwner, TestAdminProvisionMailbox, TestAdminQuotaAndStatus, TestAdminDeleteMailboxCascade, TestAdminMailboxUsage), `internal/handlers/admin_mailbox_handler_test.go` (real `JWTAdminAuth` middleware + okapi routing over an in-memory seam fake and the shared events fake — TestAdminMailboxRoutesRequireAdminAuth, TestAdminMailboxListPagingFiltersAndOwnerEnrichment, TestAdminMailboxProvisionValidationAndAudit, TestAdminMailboxPatchQuotaStatusAndAudit, TestAdminMailboxDeleteCascadeAndAudit, TestAdminMailboxUsage), `internal/routes/admin_mailbox_routes_test.go` (registration + gate through the real `adminRoutes()`), `web/src/views/admin/Mailboxes.spec.ts` (7 vitest tests incl. the declined-confirmation and validation-refusal paths).
- **Checks (as run by the wave):** FINAL NAMED BATTERY verbatim (`MSYS_NO_PATHCONV=1` prefix for Git Bash): `docker run --rm -m 2500m … golang:1.27 sh -c "go build ./... && go test -count=1 -race ./internal/..."` → **EXIT=0, 63 packages ok, zero FAIL / zero DATA RACE** (handlers `ok 202.558s`, mailstore `ok 7.738s`, routes `ok 2.662s`); an earlier in-session battery run showed 62 packages ok with the single `internal/handlers` FAIL caused solely by the concurrently-landing W1/W3 in-flight files (their `TestMailCompose_*` fixtures, their `mail_html.go` sanitizer panic, 2 `TestMail_*`) — this wave's tests ran green inside that same package run, no other wave's file touched. PG-gated confirmation: battery variant with `TEST_DATABASE_DSN` (fresh postgres:17-alpine) → mailstore ok 47.187s (incl. the PG-gated suites), routes ok 1.431s. Frontend: `npm --prefix web run type-check` exit 0; `npm --prefix web run test` 89/89 (19 files). Full detail: the STATUS_HISTORY W2 wave note.

**W1 evidence addendum (2026-11-08, the webmail-REST-surface wave's own run):**

- **Mailstore helpers:** `internal/services/mailstore/webmail.go` — `GetFolderByID` (ownership-scoped folder-by-ID), `GetMessagesByIDs` (order-preserving batch load re-verifying each row's folder below the HTTP layer), plain FTS `Search` (503 when the searcher is unwired). W3's `GetMessageByUID` is reused, not duplicated.
- **HTTP surface:** `internal/handlers/mail_handler.go` + `internal/routes/mail_routes.go` — the fourteen `/api/v1/mail/*` endpoints (see the STATUS_HISTORY W1 note for the full list), jwtOnly + optionalWorkspace gating like `/users/me`, strict session-user scoping (missing and foreign resources are indistinguishable 404s), quota `*QuotaError` → 422 with the typed scope detail, `if_mod_seq` flag CAS → 409, unconfigured mailstore → 503 on every endpoint; golden API contract regenerated (`docs/implementation/api_routes.txt`, fourteen `mail` routes).
- **Rendering security (ADR-005 decision 5):** `internal/handlers/mail_html.go` — x/net-html parse→strip→re-render: scripts/iframes/objects/embeds/meta/link/base/svg dropped, forms unwrapped with controls removed (text kept), `on*` attributes stripped, URL-scheme filtering (`javascript:`/`vbscript:`/non-image `data:` refused), image sources only `data:image/*` (the remote-content block — a `sandbox=""` iframe still performs network fetches, so beacons die here), `@import`/remote `url()` in style attributes and `<style>` blocks neutralized. Verified by five dedicated tests plus the view test asserting the sanitized output of an adversarial multipart payload.
- **Named tests:** `internal/handlers/mail_handler_test.go` (12 tests over the fake-driver harness + `IdentityBlobCodec` fake codec: TestMail_Mailboxes_ListWithUsage, TestMail_Folders_CRUD, TestMail_Messages_ListPaginationAndFilters, TestMail_Message_ViewSanitizedBodyAndAttachments, TestMail_Message_IsolationAcrossUsers — thirteen foreign operations all 404, TestMail_Flags_CAS, TestMail_QuotaRejectionSurfaced, TestMail_AttachmentDownload_AuthzAndType, TestMail_MoveCopyDeleteExpunge, TestMail_ListAndSearch, TestMail_NilStoreAnswersUnavailable, TestMail_MissingParams), `internal/handlers/mail_html_test.go` (5 sanitizer tests), `internal/routes/mail_routes_test.go` (TestMailRoutes_RegisteredOncePerMethodAndGated).
- **Checks (as run by the wave):** FINAL NAMED BATTERY verbatim (`MSYS_NO_PATHCONV=1` prefix for Git Bash): `docker run --rm -m 2500m -v C:/Users/Ibrahim/Desktop/ReForwarder:/app -v posta-gomod:/go/pkg/mod -v posta-gocache:/root/.cache/go-build -w /app -e GOFLAGS=-p=2 golang:1.27 sh -c "go build ./... && go test -count=1 -race ./internal/..."` → **BUILD_OK, TEST_EXIT=0, 49 packages ok, zero FAIL** (the earlier cross-wave noise — this wave's group-relative path assertion and nil-user harness deref, plus W3's mid-write compose handler — was attributed and fixed by the owning waves; the final run is the whole tree green). Frontend: `npm --prefix web run type-check` exit 0; `npm --prefix web run test` 19 files / 89 tests passed. PG-gated suites not re-run by this wave (no schema, no new FTS SQL; the mailstore package's PG-gated search suite covers the tier) — recorded as not run here.

**W4 evidence addendum (2026-11-08, the webmail-frontend wave's own run):** scope, files, and checks as reported by the wave (it ran its checks over the real tree itself; no Go files touched — all battery noise came from the parallel waves' in-flight files, which passed as they landed):

- **Router/nav:** `web/src/router/index.ts` (five `/mail` routes — mail, mail/folder/:folderId, mail/message/:folderId/:uid, mail/compose, mail/settings — minimal-additive inside the authenticated DashboardLayout children) + `web/src/layouts/DashboardLayout.vue` (user-facing "Mail" nav section, Overview-level, with the Mailbox item).
- **Typed API client:** `web/src/api/mail.ts` mirroring the W1 handler shapes (Mailbox/MailFolder/MailMessageSummary/MailMessage/MailAttachment; mailboxes/folders CRUD + expunge, messages, message, flags, move/copy/delete, search, plus a browser-direct authenticated `attachmentUrl` — HttpOnly session cookie, workspace_id as query). Contract fidelities verified against the handler source: `/mail/messages` pagination is 1-based (handler normalizes page<1 to 1) unlike the 0-based rest of the app — the conversion is localized in Mailbox.vue's `uiPageable`; `folder` travels as the query parameter on flags/move/copy (the handler binds it `query:"folder"`), not the body.
- **Views:** `web/src/views/mail/Mailbox.vue` (folder tree from parent_id, INBOX first with live unseen badges + storage bar; paged message list on the shared Pagination component; unread/flagged filters combined with the FTS `q` server-side; bulk mark read/unread/flag/unflag/move and confirm-gated bulk delete; empty states for no-mailbox, the 503 unconfigured-mailstore case, and no-match search; responsive two-column collapse at 900px); `web/src/views/mail/MessageView.vue` (metadata header; body in the Logs MessageDetail sandboxed-iframe pattern — `srcdoc` + `sandbox=""` — with remote content blocked twice over: the W1 server sanitizer `internal/handlers/mail_html.go` strips remote refs before serving AND the view pins a per-message CSP meta, default `img-src data:` + `script-src 'none'`; per-message "Load remote content" opt-in relaxes `img-src` only; authenticated attachment downloads with the `download` attribute; reply/reply-all/forward via the W3 `replyFrame` handoff through sessionStorage; flag/unflag, confirm-gated delete, move-to-folder; foreign 404); `web/src/views/mail/Compose.vue` (from = active-mailbox select / to / cc / bcc / subject / plain body; attachment upload through `POST /mail/attachments` with the ADR-006 refusal surfaced verbatim — the 409 clamd-signature message via `apiMessage`; staged refs carried on send; send with loading state; save-draft and updateDraft+sendDraft when editing a stored draft; applies the reply-frame handoff); `web/src/views/mail/MailSettings.vue` (display name through the existing `PUT /users/me` profile API; compose signature kept per browser in localStorage — no server-side signature storage exists in the W1–W3 endpoint surface, documented in the view + spec; per-mailbox quota/usage display, the ADR-003 §5 counters incl. deduplicated blob bytes; link into the Profile card's App Passwords management).
- **Named specs:** `web/src/views/mail/Mailbox.spec.ts` (9), `web/src/views/mail/MessageView.spec.ts` (11), `web/src/views/mail/Compose.spec.ts` (7), `web/src/views/mail/MailSettings.spec.ts` (6) — 33 tests covering tree+counters, filter query names, bulk ops incl. declined confirmations, the sandbox+default-blocked CSP and the opt-in relax, plain-text fallback, authenticated attachment href, the reply handoff modes, scan-refusal verbatim, staged staging, quota/name-validation refusals, the profile link target.
- **Checks (as run by the wave):** `npm --prefix web run type-check` → exit 0 (run twice); `npm --prefix web run test` → 23 files / 122 tests passed, 0 failed (33 of the 122 are the new W4 specs); docker battery (exact ask command, `MSYS_NO_PATHCONV=1`): `go build ./... && go test -count=1 -race ./internal/...` → exit 0, every `./internal/...` package ok (handlers 110.663s, routes 1.748s, services/mailcompose 1.130s, services/mailstore 3.986s, services/email 5.206s) — the full tree including W1–W3's landed work; `git status` re-verified — the wave's diff is limited to `web/src/router/index.ts` (+9), `web/src/layouts/DashboardLayout.vue` (+10), and the new `web/src/api/mail.ts` + `web/src/views/mail/` tree. Not run by the wave: `make lint` / `make vet` / `make build-ui` (not named by its ask); visual-acceptance screenshots (deferred to the close-out acceptance run). Ledger entries were intentionally left to the integrating wave per the wave rule (the ledgers carried the parallel waves' in-flight edits) — the close-out addendum below books them here.
- **Documented limitations (recorded by the wave):** the per-message remote-content opt-in is the iframe-CSP gate above, not true remote-image loading — the W1 sanitizer strips http(s)/cid: sources unconditionally (`internal/handlers/mail_html.go`) and no remote-keeping backend variant exists yet; enabling real remote loading is a W1 follow-up (e.g. a `remote_content` variant of `GET /mail/messages/{uid}`), for which the frontend gate is already in place; the compose signature is browser-local pending a backend settings item; the 1-based vs 0-based pageable mismatch is handled at the Mailbox.vue edge and flagged for the integrator.

**Close-out (2026-11-10, all four waves green — Programme Tracker row → VERIFIED, commit cell left for the committer):**

- **Gate:** confirmed green twice over. The stabilize pass ran at the clean HEAD (`183303a` "OB-4 mail protocols: IMAP4rev1 and POP3 servers over the mailstore…", `git status --porcelain` empty before and after the runs, no drift found, no code changes) — the handlers+mailstore battery `go test -count=1 ./internal/handlers/ ./internal/services/mailstore/` dockerized golang:1.27 → `ok github.com/goposta/posta/internal/handlers 11.444s` + `ok github.com/goposta/posta/internal/services/mailstore 0.993s`, and `go build ./...` → BUILD_EXIT=0 (that battery is the two named packages, **not** the full Done-bar gate `go test ./...`, which the stabilize ask did not name — recorded as such). The full-tree battery over all landed OB-5 work passed in the W4 wave's final run (exit 0, zero FAIL / zero DATA RACE under `-race`, package timings above) alongside the W2 wave's 63-package run and the W1 wave's 49-package run, all with the same command shape.
- **Visual acceptance:** green per the 2026-11-10 acceptance run over the `compose.acceptance.yml` stack override present in the tree (enables the OB-3/OB-5 mailstore prerequisites — blob store + encryption key — on the dev stack so the `/api/v1/mail/*` surface is live, plus the inbound-webhook seeding seam; the file's inline comments carry the 2026-11-10 run note).
- **Record movements:** CA-01 → **COMPLETE** (webmail compose surface for mailbox mail; supersession of the W3-era scope note recorded on the record); SF-65 stays PARTIAL with the mailbox self-service surfaces noted COMPLETE; SF-80 stays COMPLETE with the self-service quota/usage display residual closed; SF-97 stays COMPLETE with the admin-surface extension noted. Tracker row remaining work: **JMAP stays an OB-6 evaluation; groupware/file-storage evaluations unchanged**.
- **Not done by this close-out (recorded, not silently skipped):** the commit hash cell stays blank for the committer; the W4 wave's ledger entries in `docs/project/STATUS_HISTORY.md`, `docs/project/BACKLOG.md`, and the README roadmap are the integrating wave's to book (the frontend wave deliberately did not edit the shared ledgers); no GitHub board sync (not authorized).



## Verification record (Phase 1 waves — executed 2026-11-07)

- **Wave V1..V10** — ~10 auditors, chunked by category, verify every record against the code (route→logic→persistence→isolation→tests), upgrade/downgrade statuses with `path:line` evidence, and resolve the two former N/V items (SF-11, SF-109) and the ledger-claim reconciliations (SF-37/68/72/73 FULL claims).
- **Wave R** — spot re-check of the 27 R-delivered records against the 2026-11-07 acceptance-audit evidence (commit `27c418a`).
- Every status change is written back here with evidence; the tally table is then frozen as the audit's official statistics.

## Resumability

This register is the programme's persistent tracker: after every wave, statuses, evidence, and the tally are updated in place. Any interrupted phase resumes from the register without loss of progress. Programme-level phase tracking lives in `todo.md` Track A and `docs/implementation/ROADMAP_IMPLEMENTATION.md`.






