# ReForwarder — Stalwart-Inspired Implementation Roadmap (P0–P4)

> Derived from `stalwart-feature-inventory.md` (Stalwart v1.0.3 @ `8b326865`) and `reforwarder-capability-audit.md` (ReForwarder @ `7a84e46`). This roadmap follows the governing distinction: **email-infrastructure management capabilities in; mail-server capabilities out.** ReForwarder sends through trusted relays by design — the roadmap hardens and observes that model rather than replacing it with an MTA.
>
> Priorities: **P0** critical gap (broken/essential) · **P1** very high · **P2** high · **P3** medium · **P4** low. Complexity: XS/S/M/L/XL. Risk: Low/Medium/High. "Stalwart ref" cites the `SF-nn` feature that inspired the item.

## Phase 1 — Critical improvements (P0)

> IDs are stable identifiers, not ordering. R-01 was found by the backend campaign audit, R-21 by the frontend audit.

### R-01 · Make scheduled campaigns actually send — **P0, S, Low risk**
`CampaignRepository.FindScheduledReady()` (`campaign_repo.go:152`) has zero callers; campaigns placed in `scheduled` sit forever unless manually re-triggered. The status model and API exist (scheduling is API-only — the create modal has no schedule field; include one), only the scheduler is missing.
**Implementation:** a cron/loop in the worker that polls `FindScheduledReady()` (like the existing stuck-campaign watchdog), claims each via the atomic `TransitionStatus`, and enqueues. Add a scheduler-interval setting.
**Stalwart ref:** SF-09/SF-104 (scheduled delivery + task system). **Acceptance:** a campaign with `scheduled_at` in the past + `scheduled` status transitions to `sending` automatically; integration test; no double-fire (atomic claim already exists).

### R-21 · Fix the broken subscriber-list segment preview — **P0, XS, Low risk** (frontend-audit find)
`web/src/api/subscriberLists.ts:30` calls `POST /subscriber-lists/preview`, but the backend registers `POST /subscriber-lists/preview-segment`; the view also expects a pageable response while the handler returns `{"count": N}`. The "Preview segment" button on every dynamic subscriber list always fails.
**Implementation:** the control is **doubly broken** — the URL is wrong (`/preview` vs `/preview-segment`) *and* the view parses a pageable shape (`res.data.data`, `res.data.pageable.total_elements`) while the handler returns `{"count": N}`. Fix both: point the call at `/preview-segment` with `{"filter_rules": …}` and read `data.count`; optionally return a small matches sample by reusing the new segments preview shape. Backend endpoint verified working live (`{"success":true,"data":{"count":7}}`).
**Acceptance:** the Preview button on a dynamic list shows the live matching count.

## Phase 2 — Core platform improvements (P1)

### R-22 · SMTP client timeouts & deadlines — **P1, M, Low risk** (verification find)
`smtp_sender.go` dials with bare `tls.Dial`/`smtp.Dial` (no dial timeout) and never sets connection deadlines; no `asynq.Timeout` on send tasks. A stalled relay wedges worker concurrency slots and stalls the whole bulk pipeline.
**Implementation:** dial with a timeout (e.g. 15s), per-command read/write deadlines via `conn.SetDeadline`, optional `asynq.Timeout` on send tasks. **Stalwart ref:** the relay-model analogue of Stalwart's queue/session hardening. **Acceptance:** a black-holed relay cannot occupy a worker slot beyond the deadline; the task fails and retries normally.

### R-23 · Wire up (or remove) the dead `require_domain_verification` platform setting — **P2, S, Low risk** (verification find)
The admin UI toggles it, the seeder seeds it true, `Provider.RequireDomainVerification()` exists — and has **zero callers**. Actual enforcement reads per-workspace settings that default to false, so out of the box shared-server sending requires no domain ownership and per-user SMTP servers are never domain-checked at all.
**Implementation:** make the platform setting the default for workspaces without an explicit override (the three-layer settings already support this), or remove the toggle; document the strict-mode shared-server behavior. **Acceptance:** flipping the admin toggle changes send-time enforcement observably.

### R-02 · DKIM signing of outbound mail — **P1, M, Medium risk**
ReForwarder verifies DKIM **presence** but never signs, silently assuming an upstream signer (selector CNAME `posta._domainkey.posta`). For a sending platform this is the largest deliverability gap: every workspace without an external signer ships unsigned mail, and DMARC fails for them.
**Implementation:** per-domain DKIM key management (RSA-SHA256 + Ed25519-SHA256) — key generation UI, private-key storage (encrypted settings/registry, mirroring `models.SMTPServer` handling), selector configuration per verified domain, signing at `buildMessage`/SMTP-send time, DNS record display (the existing verifier already reads `posta._domainkey`). Keep the CNAME path as a valid "bring your own signer" mode.
**Stalwart ref:** SF-20/SF-62 (signing + rotation lifecycle). **Design note from verification:** Stalwart’s rotation stages are **not** a linear Active→Pending→Retiring→Retired schedule — new keys are created directly Active; `Pending` is a transient DNS-publication retry state; the real flow is Active → Retiring (once a replacement Active key exists) → Retired → deleted. Model ReForwarder’s lifecycle accordingly. **Dependencies:** key storage; domain verification flow (exists). **Acceptance:** mail-signature verification (e.g. against a local verifier or mail-tester) passes for signed domains; rotation supported; unsigned domains still deliverable when an upstream signer is configured. **Risk note:** mis-signing is worse than not signing — golden-message tests required.

### R-03 · Delivery queue administration — **P1, M, Low risk**
Stalwart exposes full queue control (SF-52/54/59: inspect, patch schedules, cancel, requeue, pause). ReForwarder's asynq queue has server visibility + per-email retry only — no cancel, no dead-letter view, no requeue of exhausted jobs.
**Implementation:** admin API + UI over `asynq.Inspector`: list queued/scheduled/failed tasks per queue, cancel/delete, re-run dead-lettered tasks, campaign-message-level cancel (worker already honors live campaign state); surface the existing worker SSE in a queue view. **Verification notes:** exhausted asynq tasks already land in the per-queue **archived set** (no `Retention` is configured), so the dead-letter data exists today — surface the archive count in admin metrics even before the UI ships; also add a lost-task reconcile for `emails` stuck in `queued` whose task was lost (the retry worker only covers `failed`, and the user retry endpoint only accepts `failed`). **Stalwart design note:** Stalwart implements queue "cancel" as a patch omission (`PermanentFailure("Delivery canceled.")`) rather than a dedicated verb — an explicit cancel endpoint is cleaner in ReForwarder’s REST style.
**Acceptance:** an admin can find a stuck/failed send, cancel it, and re-run a dead-lettered task from the UI.

## Phase 3 — Product enhancements (P2)

### R-24 · Header/Subject CRLF injection hardening — **P2, S, Low risk** (verification find)
`filterCustomHeaders` filters header *names* but never validates **values**, and `Subject` passes through `encodeHeader` unvalidated — a CR/LF in either injects arbitrary headers (e.g. `Bcc:`). Recipients/From are safe (`mail.ParseAddress`).
**Implementation:** reject or strip CR/LF/NUL in custom header names+values and Subject before `buildMessage`; add injection tests. **Acceptance:** crafted malicious payloads render as literal text or are rejected with 400.

### R-25 · Hash relay credentials properly — **P2, S, Low risk** (verification find)
`smtprelay/credential.go` stores `hex(sha256(password))` — unsalted, no KDF — and verifies with `==`. A DB leak yields instant recovery of credentials that relay through a user's upstream server.
**Implementation:** bcrypt/argon2 like user passwords + `subtle.ConstantTimeCompare`; migrate existing rows (re-hash on next successful login or force reset). **Acceptance:** stored hashes are salted KDF outputs; verification is constant-time.

### R-26 · Atomic send/campaign claims (duplicate-send races) — **P2, M, Medium risk** (verification find)
Three interleaving defects: (1) `EnqueueEmailSend` has **no TaskID dedup** (only campaign:start/message tasks do); (2) `EmailSendHandler` flips `queued→processing` with an unconditional update, so two delivered tasks both send; (3) `RetryEmail` and the retry worker both read-then-flip `failed→queued` without CAS, so a user retry and the 5-min poller can create two tasks. Additionally `FindPendingByCampaign` is a plain SELECT with post-hoc status flips, so the stuck-campaign restart cron and asynq redelivery can double-claim batches.
**Implementation:** CAS everywhere (`UPDATE ... WHERE status='queued'` / `RETURNING`), TaskID dedup for sends, `FOR UPDATE SKIP LOCKED` batch claims (the pattern `automation_journey_repo.go` already uses). **Acceptance:** concurrent duplicate tasks/retries produce exactly one send; batch re-claim produces no duplicate `emails` rows.

### R-04 · Provider event ingestion → true delivery status — **P2, M, Low risk**
The `delivered` email-event exists but nothing produces it; with relay-based sending, "sent" ends at the relay's 250. True engagement/delivery signals come from the upstream provider.
**Implementation:** pluggable event-webhook receivers (SES/SendGrid/Postmark/Mailgun-style: delivered, bounce type hard/soft, complaint, delayed) mapping into the existing `email_events` + bounce/suppression machinery; per-provider signature verification; reuse the bounce-webhook pattern (`bounce_webhook_handler.go`). Bounce classification (hard/soft/full/reputation) rides on this.
**Stalwart ref:** SF-10/SF-25 (report ingestion philosophy — consume upstream reports rather than parse raw DSNs). **Acceptance:** a delivered event recorded from a provider webhook shows in message history + analytics; soft bounces retry via the existing retry worker.

### R-05 · Bounce/failure classification — **P2, M, Low risk** (depends on R-04)
Classify failures (hard bounce, soft/mailbox-full, DNS, TLS, reputation, rate-limited) from provider events + RCPT 5xx texts; expose per-class analytics; drive auto-suppression policy per class (currently only hard-bounce auto-suppresses).
**Stalwart ref:** SF-53 (typed SMTP response taxonomy).

### R-06 · Webhook delivery replay — **P2, S, Low risk**
Failed webhook deliveries + attempts are already persisted and viewable; add an authorized "re-deliver" action (re-enqueue with the original payload; per-endpoint abuse cap).
**Stalwart ref:** SF-52 (requeue/cancel semantics). **Scope note:** also fix the dispatcher’s backoff comment/code mismatch (`defaultRetryBaseWait * attempt` is linear, the comment claims exponential) while touching this surface. **Acceptance:** replay from the Logs → Outgoing Webhooks UI with a confirmation + audit event.

### R-07 · Domain deliverability health page — **P2, S–M, Low risk**
One page per verified domain: current SPF/DKIM/DMARC check results (the verifier already performs them), DKIM signing status (from R-02), recent bounce/ complaint rates (analytics exist), record-setting guidance. A "diagnose" button re-runs checks on demand.
**Stalwart ref:** SF-63 (TroubleshootDmarc) + SF-64 (negative: Stalwart has no domain health page either — this is a product improvement Stalwart lacks).

### R-08 · Campaign A/B winner logic — **P2, M, Medium risk**
Campaigns and the automation `ab_test` node currently only assign variants (subject override). Add: send window → per-variant open/click stats → optional automatic winner send to the remainder, with the existing `campaign_messages`/`email_events` data.
**Acceptance:** an A/B campaign completes its test window, picks the winner by configurable metric, and mails the remainder once.

### R-09 · Timezone-aware campaign sending — **P2, M, Low risk**
The stub at `campaign_processor.go:230-235` (logs intent, single batch). Group recipients by the existing subscriber `timezone` field and schedule per-timezone batches against the campaign send window.
**Dependency:** none beyond batching care; pairs naturally with R-01.

### R-10 · 2FA recovery codes — **P2, S, Low risk** (ReForwarder self-improvement; Stalwart has no recovery codes either — SF-69)
TOTP lockout currently requires admin intervention. Generate single-use codes at 2FA enable, hash-verify at login (pattern exists in `twofactor/totp.go` + user handler).

### R-11 · Backup & restore automation — **P2, S, Low risk**
No documented/tested backup story. Document + automate: scheduled `pg_dump` (cron job or compose sidecar) with retention, restore runbook, and the existing workspace export as tenant-level backup. (Stalwart ref SF-82 — stop-and-dump CLI is the same class of solution.)

## Phase 4 — Advanced features (P3)

### R-12 · Prometheus metrics enabled by default + dashboard docs — **P3, XS, Low risk**
`POSTA_METRICS_ENABLED` defaults false and the endpoint is undocumented in the admin UI; Stalwart ships a Prometheus endpoint + OTLP push (SF-93). Flip the default for new installs (compat note) or add a setup card; document the 19 existing metric families **and add the missing campaign/automation families** (campaigns started/completed, messages sent per campaign; journeys executed, node failures) — nothing currently exports either subsystem.

### R-13 · Scheduled (cron) automation triggers — **P3, M, Low risk**
Automations are purely event-triggered; Stalwart's task scheduler (SF-104) shows the value of cron-shaped workflows (digests, cleanups). Add a `schedule` trigger type to the existing engine (worker already runs the engine loop).

### R-14 · Segment-membership automation conditions — **P3, M, Low risk** (documented deferral from the segments task)
Add a `in_segment` condition evaluator to the filter node (backend check via `segment_query`), deferring membership *tracking* (entry/exit events) until a consumer exists.

### R-15 · Per-API-key rate limits — **P3, S, Low risk**
API keys carry scopes + IP allowlists but no rate limits (Stalwart: SF-88's per-account request/concurrency limiter + SF-91 lookup stores). Extend `models.APIKey` with an optional limit honored by the existing Redis limiter.

### R-16 · ~~Session/device management~~ — **DELETED (verification finding: already implemented)**
The verification audit found this is **already built end-to-end**: `GET /users/me/sessions`, `DELETE /users/me/sessions/{id}`, `POST /users/me/sessions/revoke-others` with device/IP/browser/OS metadata (`internal/routes/user_routes.go:136-175`, `internal/handlers/session_handler.go:40-52`, rendered in `web/src/views/auth/Profile.vue` via `web/src/api/sessions.ts`), admin-side bulk revoke, and new-device login alerts. ID retained as a tombstone so cross-references stay stable.

### R-27 · Bound the retry sweep query — **P3, XS, Low risk** (verification find)
`FindFailedForRetry` has no LIMIT (`email_repo.go:332-350`); every 5-min tick loads **all** failed emails for a server and re-enqueues them — one bad hour becomes a huge requeue storm.
**Implementation:** LIMIT + jittered spread across ticks. **Acceptance:** a 50k-row failed backlog re-enqueues in bounded batches.

### R-28 · Scope `?token=` API-key auth to SSE routes — **P3, S, Low risk** (verification find)
API keys are accepted via `?token=psk_…` on **every** authenticated route (`middleware.go:28,172`) — URL-borne credentials land in proxy/access logs. JWT already excludes query tokens for exactly this reason.
**Implementation:** restrict the query-param path to the SSE routes that genuinely cannot set headers (the EventSource consumers), or drop it. **Acceptance:** `?token=` no longer authenticates plain JSON routes.

### R-17 · DMARC aggregate report ingestion — **P3, L, Medium risk** (depends on inbound receiver default-on)
Use the (already built, default-off) inbound pipeline to receive `mailto:` DMARC reports for the user's sending domains, parse XML, and surface per-domain auth-summary trends in the domain health page (R-07). Stalwart ref SF-25/SF-57.

### R-18 · API usage analytics per API key — **P3, S, Low risk**
Count requests/sends per key (middleware counter → Redis/Postgres); surface in the API-key UI. Stalwart ref SF-92 (event taxonomy feeds usage views).

## Phase 5 — Optional capabilities (P4)

### R-19 · WebAuthn/passkeys — **P4, L, Medium risk**
Neither platform has it (Stalwart is TOTP-only, SF-69). Worth doing eventually; not differentiating.

### R-20 · Admin impersonation ("act as workspace") — **P4, S, Low risk**
Stalwart SF-70. Support-mode convenience; the current admin views + audit log cover most needs.

## Corrected roadmap summary (post-verification)

| ID | Feature | Current Status | Priority | Complexity | Risk | Dependencies | Reason |
| --- | ------- | -------------- | -------- | ---------- | ---- | ------------ | ------ |
| R-01 | Scheduled-campaign scheduler | Broken (dead code) | P0 | S | Low | — | Visible feature silently does nothing |
| R-21 | Subscriber-list segment preview | Broken (URL + shape) | P0 | XS | Low | — | Visible control always fails |
| R-02 | DKIM signing + key lifecycle | NOT IMPLEMENTED | P1 | M | Medium | key storage; domain verify | Biggest deliverability gap |
| R-22 | SMTP client timeouts/deadlines | NOT IMPLEMENTED | P1 | M | Low | — | Stalled relay wedges workers |
| R-03 | Queue administration (cancel/DLQ/requeue + reconcile) | PARTIALLY IMPLEMENTED | P1 | M | Low | asynq Inspector (already used) | Operational parity with Stalwart SF-52 |
| R-04 | Provider event ingestion (true `delivered`) | NOT IMPLEMENTED | P2 | M | Low | `email_events` substrate | Relay 250 ≠ delivery |
| R-05 | Bounce/failure classification | NOT IMPLEMENTED | P2 | M | Low | R-04 | Drives suppression policy |
| R-06 | Webhook replay (+ backoff fix) | PARTIALLY IMPLEMENTED | P2 | S | Low | existing deliveries store | Operator relief for failed webhooks |
| R-07 | Domain deliverability health page | NOT IMPLEMENTED (improves on Stalwart) | P2 | S–M | Low | R-02, R-04, verifier | One view of auth status + bounce rates |
| R-08 | Campaign A/B winner logic | PARTIALLY IMPLEMENTED | P2 | M | Medium | `email_events` stats | Feature exists in model, no logic |
| R-09 | Timezone-aware sending | STUB | P2 | M | Low | R-01 | Subscriber `timezone` unused |
| R-10 | 2FA recovery codes | NOT IMPLEMENTED (self-improvement) | P2 | S | Low | — | TOTP lockout needs admin today |
| R-11 | Backup & restore automation | NOT IMPLEMENTED | P2 | S | Low | — | No backup story at all |
| R-23 | Wire/remove dead `require_domain_verification` | DEAD SETTING | P2 | S | Low | settings layer | Security toggle does nothing |
| R-24 | Header/Subject CRLF hardening | MISSING | P2 | S | Low | — | Header injection via values/Subject |
| R-25 | Relay credential hashing (bcrypt + constant-time) | UNSALTED SHA-256 | P2 | S | Low | — | DB leak → instant relay creds |
| R-26 | Atomic send/campaign claims | MISSING | P2 | M | Medium | SKIP LOCKED pattern exists | Duplicate-send races |
| R-12 | Prometheus default-on + new campaign/automation families | PARTIALLY IMPLEMENTED | P3 | XS | Low | — | Observability parity |
| R-13 | Scheduled (cron) automation triggers | NOT IMPLEMENTED | P3 | M | Low | engine loop exists | Digest/cleanup workflows |
| R-14 | Segment-membership automation conditions | NOT IMPLEMENTED (deferred) | P3 | M | Low | segment_query | Completes segments story |
| R-15 | Per-API-key rate limits | NOT IMPLEMENTED | P3 | S | Low | Redis limiter | Abuse control granularity |
| R-27 | Bound retry sweep query | UNBOUNDED | P3 | XS | Low | — | Requeue-storm prevention |
| R-28 | Scope `?token=` auth to SSE | OVER-BROAD | P3 | S | Low | — | URL-borne credentials |
| R-17 | DMARC aggregate report ingestion | NOT IMPLEMENTED | P3 | L | Medium | inbound receiver default-on | Long-term domain health |
| R-18 | Per-API-key usage analytics | NOT IMPLEMENTED | P3 | S | Low | middleware counter | Developer visibility |
| R-19 | WebAuthn/passkeys | NOT IMPLEMENTED (Stalwart: also absent) | P4 | L | Medium | — | Future auth parity |
| R-20 | Admin impersonation | NOT IMPLEMENTED | P4 | S | Low | audit log | Support convenience |
| R-16 | ~~Session/device management~~ | ALREADY IMPLEMENTED | — | — | — | — | DELETED — tombstone for stable IDs |
## Not recommended (DO NOT IMPLEMENT) — mandatory review

| Stalwart capability | Why Stalwart needs it | Why ReForwarder doesn't | Lightweight alternative |
|---|---|---|---|
| **MX delivery engine / full MTA** (SF-01–07, 12–19, 21: direct-to-MX, routing strategies, relays with source-IP pools, queue quotas/encryption, milter — excluding SF-09/10/20, which the roadmap implements via scheduler, provider events and DKIM signing) | It *is* an MTA | ReForwarder's product sends via trusted relays; an in-process MTA brings IP reputation, TLS policy, abuse exposure and the entire SF-01–SF-21 surface for near-zero product value | The existing relay model + R-02 DKIM signing; Stalwart itself can serve as the customer's relay |
| **IMAP4rev1/rev2 server** (SF-85) | Mailbox access for stored mail | No mailboxes exist; mail is not stored post-send (attachments/logs only) | None needed |
| **JMAP server for end-user mail** (SF-84) | Same | Same; ReForwarder's REST/OpenAPI already covers its API needs | — |
| **POP3 / ManageSieve servers** (SF-86) | Mailbox + user scripts | No mailboxes, no Sieve users | — |
| **Sieve policy engine** (SF-46/101) | Per-user mail filtering at delivery | No mail delivery into stores; ReForwarder's automation engine + filter nodes cover workflow conditioning | Automation filter nodes |
| **CalDAV/CardDAV/groupware/file storage** (SF-111/112) | Collaboration product surface | Different product category | — |
| **Mailbox storage cluster (FDB, sharded stores, read replicas)** (SF-76/77) | Petabyte mailboxes | Postgres + Redis + S3 at current scale | Postgres/Redis/S3 managed HA |
| **Multi-node clustering fabric** (SF-105/106) | Distributed MTA delivery | Single app + worker scale horizontally via asynq already; Postgres/Redis HA is the ops concern | Managed Postgres/Redis HA |
| **Built-in ACME/HTTP certificate automation** (SF-34) | Terminates TLS for SMTP/HTTP itself | TLS terminates at the customer's relay/ingress proxy | Ingress/proxy TLS |
| **Spam filter / ML classifier / DNSBL for inbound** (SF-39–42) | Accepts untrusted inbound mail at scale | Inbound receiver is feature-gated and domain-verified; abuse prevention already ported (security engine) | Existing security engine; nlp classifier only if inbound becomes a hosted product |
| **Milter client** (SF-15) | SMTP-stage filtering hooks | No SMTP-stage pipeline to hook | MTA hooks concept already covered by automations + webhooks |
| **Masked email / AI spam / SCIM / tenants / traces stores** (enterprise SEL features) | Stalwart's commercial tier | Different scope; SCIM/tenant features can be revisited if B2B reseller demand appears | — |

## Dependency map (implementation order rationale)

- **R-01** standalone (do first — P0, smallest).
- **R-02** (DKIM signing) unlocks the deliverability story; **R-07** (health page) displays its state; **R-17** (DMARC ingestion) completes it long-term and depends on the inbound receiver's default-on story.
- **R-04 → R-05** (provider events → classification) unlocks true `delivered` status and upgrades bounce analytics; both feed **R-07**.
- **R-03** standalone; pairs with R-06 (replay) on the same admin surface.
- **R-08/R-09** both touch `campaign_processor` — sequence together to avoid churn.
- **R-10/R-15/R-16** are independent security polish.
- Frontend for R-03/R-06/R-07 rides the existing admin/logs patterns (tables, SSE, ConfirmDialog).

## Explicitly preserved (do not weaken)

The shared SSRF guard (`internal/services/webhook/ssrf.go` + `proxydial.go`), the automation webhook authorization gates, the security engine's enforcement points, and the suppression/eligibility layers (status, list-scoped unsubscribes, global suppressions) must remain intact in every item above — several roadmap items (R-04, R-06) explicitly build *on top of* them rather than around them.
