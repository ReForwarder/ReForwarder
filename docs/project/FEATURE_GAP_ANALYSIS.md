# ReForwarder — Feature Gap Analysis

> **Date:** 2026-11-06. **Scope:** every open gap from the RF-F registry gap view at the current tree (HEAD `0f52636`; the workflow snapshot was taken at `f5f0e92` + 34 dirty entries, which have since landed as `346eb67` "Complete remaining roadmap items: WebAuthn, impersonation, feature flags, contract test" and `0f52636` "Fix WebAuthn nil-user abort path and duplicate impersonation stop audit"). **76 gaps: P0×6, P1×20, P2×31, P3×19.**
>
> This file consolidates three sources and defers to them on conflict: the RF-F registry gap view and `FEATURE_INVENTORY.md` (current state + per-item evidence), `docs/product-roadmap/04-feature-gap-analysis.md` (the frozen gap register — GAP-01…53, D1–D9, NV register), and `todo.md` (Track A/B phasing, dependency gates). Acceptance criteria for classic `GAP-nn` items are condensed from doc 04 §2; for registry-completion and Track A items they are derived from `FEATURE_INVENTORY.md` limitations, `todo.md` phase items, and `docs/product-roadmap/upcomming-convo.md` §7/§11.

## How to read this file

- **Priority** is the gap view's class: **P0** = trust/correctness defects in live paths — fix before anything else; **P1** = high-value capability or compliance work, several fully parallel; **P2** = parity and platform hardening in batches; **P3** = long tail, conveniences, and demand-gated items.
- **Dependencies** lists what blocks the item, what it blocks, and which Option B (Track A) phase it folds into (`→ OB Phase n`).
- **Refs** use the permanent ID series: registry `RF-F###`, audit registry `INF/DOM/CON/CAM/TPL/ANA/AUT/API/SEC/OBS-nnn`, gaps `GAP-nn`, duplicates `D1–D9`, architecture risks `R1–R16`, tracker items `R-nn`, verification register `NV`. None are ever renumbered.
- Status detail (what landed vs. what remains) lives in `FEATURE_INVENTORY.md`; phasing lives in `todo.md` and `ROADMAP.md`.

## Verification performed for this analysis

Current-state claims below were spot-checked at HEAD `0f52636` this session:

- `queue_reconcile` scans `status='queued'` only — scheduled rows uncovered: `internal/cron/jobs/queue_reconcile.go:93` (grep).
- Automation write routes carry no editor role gate: `internal/routes/automation_routes.go:35` is `.Group("/workspaces/current", r.mw.auth, r.mw.workspace)` (grep — no `RequireWorkspaceRole`/`RoleEditor` in the file).
- `two_factor_required` has a getter but no consumer: `internal/services/settings/provider.go:124` is the sole reference (grep of `internal/handlers` + services found no caller in login).
- Legacy bounce webhook has no suppression/complaint handling: `internal/handlers/bounce_webhook_handler.go:83-165` (grep for `suppress|Suppression|complaint|Complaint` returns no handling sites; complaint rejected per registry).
- No cancel route exists for scheduled emails (grep of `internal/routes/*.go` for cancel/email is clean).
- No `email.delivered` emission on the SMTP worker path: `internal/worker/handler.go` mentions delivery only in comments; `EmailEventDelivered` is a defined event type (`internal/services/emailhistory/service.go:70,83`) recorded via provider-event ingestion, not by the send path.
- No cron leader lock: grep for `SET NX|SetNX|leader` in `internal/cron/manager.go` is clean.

Everything else is cited from the ask's gap view, `FEATURE_INVENTORY.md`, doc 04, doc 05, `todo.md`, or `upcomming-convo.md`, and is marked with its source. The full test suite was **not** re-run in this session; the orchestrator-reported gate (`go test ./... -count=1`, dockerized golang:1.27) was **PASS** for this snapshot.

---

## Gap counts by priority

| Priority | Gaps | Themes |
|---|---|---|
| **P0** | 6 | Trust & correctness: scheduled-send durability, delivery-signal integrity (webhook parity + delivered state), authorization and 2FA enforcement, multi-replica cron correctness |
| **P1** | 20 | Signals & sender value: ESP seam + failover, idempotency, auto-pause, consent (DOI), campaign operability, exports, auth completions (recovery codes, passkeys), and the four Option B foundation programmes (architecture, outbound MTA, inbound routing, mailbox storage, IMAP/POP3, webmail, IP pools) |
| **P2** | 31 | Parity batches: deliverability/DNS depth, audience tooling, automation lifecycle, campaign operability, platform/ops hardening, contract and authorization gates, DNS/TLS automation, deployment/DR |
| **P3** | 19 | Long tail: developer surface (CLI/SDK/MCP), marketing tail, admin tail, observability polish, consolidation/documentation batches, demand-gated evaluations |

---

## P0 — Trust & correctness (6 gaps)

### P0-1 · GAP-01 — Transactional scheduled send: cancel + reconcile coverage

**Refs:** RF-F048 · GAP-01 · INF-014 · R-26 · todo.md Track B Phase 0 · 04-feature-gap-analysis.md §2.1

**Why it matters.** `send_at` stores `status=scheduled` and enqueues via asynq `ProcessIn`, but the row cannot be cancelled (no cancel route — verified) and `queue_reconcile` scans `queued` rows only (`internal/cron/jobs/queue_reconcile.go:93` — verified). A Redis flush or worker crash strands the scheduled row forever: never sent, never failed, invisible to ops. This is silent lost/double-send behavior in the core transactional money path, and customers cannot un-send mail they scheduled by mistake. Registry verdict: "broken end-to-end" (RF-F048, To-dos).

**Dependencies.** None upstream — extends the landed reconcile/CAS patterns (R-26 atomic claims, `queue_reconcile` claim logic). The CAS transition must not race the send-side scheduler claim. Folds into OB Phase 1 (durable outbound message record).

**Acceptance criteria** (condensed from doc 04 §2.1 GAP-01):
- A scheduled email can be cancelled only while `status='scheduled'`; the row becomes `cancelled` and the asynq task is deleted.
- A `scheduled` row past its `scheduled_at` with no live Redis task is re-enqueued within one reconcile cycle (deterministic TaskIDs collapse double-enqueue without double-send).
- Both paths are covered by tests mirroring `queue_reconcile_test.go`, including the CAS-race against the scheduler claim.

### P0-2 · GAP-02 (residual a) — Finish legacy `/webhooks/bounce` parity: suppression, bounce rows, complaint semantics

**Refs:** RF-F053 · GAP-02 · ANA-003 · ANA-005 · INF-021 · API-014 · todo.md Track B Phase 0 (GAP-02 residual) · 04-feature-gap-analysis.md §2.1

**Why it matters.** The legacy generic bounce webhook only marks a subscriber/message bounced; it performs no suppression, writes no `bounces` row, and rejects complaint semantics outright (`internal/handlers/bounce_webhook_handler.go:89-92` per FEATURE_INVENTORY RF-F053; no suppression handling found in `:83-165` this session). Callers of the legacy endpoint therefore keep sending to addresses that bounced or complained — direct reputation damage — while the authenticated `POST /bounces` path does all of it correctly. Every downstream reputation, complaint-rate, and analytics feature stands on this signal path.

**Dependencies.** None upstream; the provider-events ingestion core is Done (RF-F052). Blocks GAP-06 (auto-pause consumes the signal) and GAP-07 (classification analytics surface). Folds into OB Phase 2 (bounce/return-path processing).

**Acceptance criteria:**
- A legacy webhook bounce produces the same side effects as authenticated `POST /bounces` (`bounce_handler.go:91-138` pattern): suppression row + bounce row.
- Complaint payloads are accepted (suppression `kind=complaint` + `email.complained` event) instead of rejected.
- Adapter tests accept SES-SNS / Postmark / Mailgun / SendGrid-shaped payloads (this also resolves NV G-H NV-5).
- Webhook-path suppression replicates the authenticated path's transactional guarantees without weakening the SSRF guard.

### P0-3 · GAP-02 (residual b) — Emit and surface true delivered state (`email.delivered` emission, delivered analytics, dead UI paths)

**Refs:** RF-F099 · GAP-02 residual · ANA-003 · ANA-005 · todo.md Track B Phase 0 · upcomming-convo.md §8 rule 3

**Why it matters.** `delivered`/`complained` are defined-but-dead event constants (`internal/models/email_event.go:27,32`); the SMTP path marks `sent` at accept, the webhook registry still documents `email.sent` as "accepted by the destination MTA" (`internal/dto/webhook_registry.go:32`), and delivery-rate analytics measure SMTP-accept, not delivery. Option B design rule 3 is explicit: *never report successful delivery just because a message entered a queue* (upcomming-convo.md §8). Provider ingestion (RF-F052, Done) records true `delivered` timeline events; what remains is the outbound-webhook emission, the analytics distinction, and the previously-dead delivered UI paths. Risk R4 in `06-architecture-impact.md`.

**Dependencies.** Provider-event ingestion (RF-F052) is the signal source — Done. Blocks honest GAP-06 gates and GAP-07 analytics. Folds into OB Phase 1 (durable transport events).

**Acceptance criteria:**
- `email.delivered` and `email.complained` are added to the webhook event registry and emitted on the corresponding events.
- Delivery-rate analytics distinguish accept vs delivered, and historical accept-only rows are labeled as such.
- Delivered-state UI paths render real data (no dead "delivered" states).
- Historical-data semantics for pre-delivered-event rows are documented.

### P0-4 · GAP-11 — Server-side editor role gate on automation write and event-ingest routes

**Refs:** GAP-11 · SEC-002 · AUT-003 · RF-F007 (limitation) · todo.md Track B Phase 0 · 04-feature-gap-analysis.md §2.3

**Why it matters.** Automation write gating is UI-only (commit `c0408dc` hid the buttons); the route group carries only `r.mw.auth, r.mw.workspace` (`internal/routes/automation_routes.go:35` — verified this session), so a viewer-role JWT can create, activate, pause, duplicate, and delete automations and can POST `/automations/events`. The codebase's own RBAC test documents that "any member — including a viewer —" passes. This is a real intra-tenant privilege gap (risk R1) and the smallest, highest-certainty fix in the register.

**Dependencies.** None — one middleware on the write subgroup plus the events route. API-key scope interaction must match the existing `requireWorkspaceScope` map.

**Acceptance criteria** (condensed from doc 04 §2.3 GAP-11):
- A viewer-role JWT receives 403 on `POST /automations`, activate/pause/duplicate/delete, and `POST /automations/events`; editor+ roles pass.
- API keys require an appropriate write scope on the same routes.
- Regression tests in the `workspace_rbac_test.go` pattern cover the viewer-denied cases.

### P0-5 · GAP-12 (enforcement) — Enforce the `two_factor_required` policy setting at login

**Refs:** RF-F006 · GAP-12 · SEC-005 · R-10 · todo.md Track B Phase 0 · 04-feature-gap-analysis.md §2.3

**Why it matters.** The setting is seeded (`internal/services/seeder/settings.go:52`) with a getter (`internal/services/settings/provider.go:124` — the sole reference; login `user_handler.go:459-510` never reads it), so a security control silently does nothing — the worst failure mode for a security setting, and architecture risk R2. The decision is *enforce or remove*, and it must be explicit either way. Recovery codes (RF-F005) are the designed escape hatch that makes enforcement safe.

**Dependencies.** TOTP service (RF-F004, Done); recovery codes (RF-F005, P1 in this register) as the no-lockout escape hatch; admin reset flow (exists, audited).

**Acceptance criteria** (condensed from doc 04 §2.3 GAP-12):
- With the setting on, a user without TOTP cannot reach a session without completing an enrollment challenge; recovery codes let them through exactly once per code, audited.
- Setting off restores current behavior; tests cover all three paths (no-2FA forced enrollment / recovery-code use / setting-off).
- If the remove decision is taken instead: the setting, getter, and seed row are deleted with a release note — the silent state must not survive either way.

### P0-6 · GAP-03 — Multi-replica cron leader election (Redis lease)

**Refs:** GAP-03 · OBS-019 · R3 · todo.md Track B Phase 0 · 04-feature-gap-analysis.md §2.1

**Why it matters.** All 10 `robfig/cron` jobs run on every server replica — `internal/cron/manager.go` has no leader lock (verified). On any multi-replica deployment (a documented topology), `bounce_alert` and `daily_report` double-send customer-facing alerts and digests, and scheduler jobs race. This also resolves NV items C-D NV-1 and I-J NV-2 (scheduler-under-concurrency questions).

**Dependencies.** Redis (already universal). Folds into OB Phase 1 (operational readiness for multi-node topologies).

**Acceptance criteria** (condensed from doc 04 §2.1 GAP-03):
- With 2 server replicas sharing Redis, exactly one replica executes `campaign_scheduler` / `bounce_alert` / `daily_report` per tick (integration test with two managers).
- Lease loss on the leader fails over within one interval without double execution (Redis `SET NX PX` lease; pattern precedent `form_ingest_handler.go:77,131`).
- Alert jobs additionally guard per-recipient with a Redis dedup key (crash between lease acquire and send).

---

## P1 — Signals & sender value (20 gaps)

### P1-1 · RF-F005 — Complete 2FA recovery codes (consume, regeneration, admin reset)

**Refs:** RF-F005 · R-10 · GAP-12 · todo.md §0.2

**Why it matters.** Recovery codes are the lockout escape hatch that makes enforced 2FA (P0-5) safe to turn on. Codes landed in `7f445f2` (bcrypt-hashed single-use, atomic consume, regeneration gated on current TOTP — todo.md §0.2), but the registry keeps RF-F005 In Progress: full completion including the admin-reset and policy interplay remains (FEATURE_INVENTORY RF-F005 limitation).

**Dependencies.** TOTP (RF-F004, Done). Pairs with P0-5 (`two_factor_required` enforcement — enforcement presumes a working recovery path).

**Acceptance criteria:**
- A recovery code consumes atomically exactly once and the consumption is audited.
- Regeneration requires the current TOTP code; old codes are invalidated.
- Admin reset issues a fresh code set and is audited; an exhausted-codes user can always reach the reset path.
- Tests cover the consume/regenerate/reset paths (extend `recovery_code_handler_test.go`, 409 lines at present).

### P1-2 · RF-F014 — Complete Passkeys/WebAuthn enrollment and login

**Refs:** RF-F014 · R-19 · commit `0f52636` · commit `346eb67`

**Why it matters.** Passkeys are the phishing-resistant credential layer for dashboard and (later) mailbox identities. The core landed in `346eb67` (service, credential store, handler, dashboard API/UI) with the nil-user abort-path fix in `0f52636`; the registry keeps the item In Progress. Notably the orchestrator test-gate tail shows `internal/services/webauthn [no test files]` — the service layer itself is untested even though the handler has a 1,223-line test file.

**Dependencies.** Sessions (RF-F003, Done); interplay with TOTP and recovery codes (RF-F004/005/006) for the lost-passkey path.

**Acceptance criteria:**
- Full enrollment → login ceremony works end-to-end (register credential, authenticate with it) against the credential store.
- Edge/abort paths (nil user, unknown credential, duplicate registration) are covered — the `0f52636` fix class.
- The `internal/services/webauthn` package has its own tests (no longer `[no test files]` in the suite output).
- The lost-passkey recovery path (fall back to TOTP/recovery codes) is documented and tested.

### P1-3 · Option B Phase 0 — Produce the architecture programme (re-audit, ADR-001…009, architecture docs)

**Refs:** upcomming-convo.md §7 Phase 0, §11, §12 · todo.md Track A Phase 0

**Why it matters.** Option B is architecture-first: *"do not start by implementing IMAP or building a webmail UI"* (upcomming-convo.md §7). Every Track A phase consumes approved ADRs, data-ownership rules, and a migration plan; skipping this gate is how the programme drifts into thirteen microservices or a second ad-hoc mail stack. The original relay P0 work (Track B Phase 0) is explicitly retained alongside, not discarded.

**Dependencies.** Nothing — it must happen first for Track A. Consumes a re-audit of HEAD including `7f445f2`, `f5f0e92`, `346eb67`, `0f52636` and the RF-F registry.

**Acceptance criteria** (from upcomming-convo.md §11 and todo.md Track A Phase 0):
- `docs/architecture/OPTION-B-ARCHITECTURE.md`, `ADRs/ADR-001…009` (9 files), `COMPONENT-MATRIX.md`, `DATA-OWNERSHIP.md`, `MESSAGE-LIFECYCLE.md` (all 14 lifecycle designs), `MIGRATION-PLAN.md`, `IMPLEMENTATION-ROADMAP.md`, `RISK-REGISTER.md` exist and are internally consistent.
- An evidence-backed current-state matrix reconciles the seven audit docs with actual HEAD code (migrations, message models, event contracts, tenant boundaries; reusable vs must-isolate).
- Architecture-only rules respected: no production-code changes, no schema changes, no new dependencies during this phase; claims cite exact files/symbols and label facts vs recommendations vs assumptions.
- Exit criteria met: approved architecture, component-selection criteria, protocol boundaries, data ownership, migration plan.

### P1-4 · Outbound MTA foundation (direct-to-MX + upstream relay modes)

**Refs:** upcomming-convo.md §6 outbound requirements, §7 Phase 1 · todo.md Track A Phase 1 · ADR-001 · INF-020 · DOM-013

**Why it matters.** This is the pillar that turns ReForwarder from relay-only into a complete ESP. Relay-only egress structurally excludes bounce-mailbox/VERP ownership (INF-020), IP pools (INF-025), and PTR guidance (DOM-013) — all reinstated as Track A candidates by Option B (todo.md §Not-recommended note). The existing relay path becomes one mode behind the MTA adapter rather than the ceiling.

**Dependencies.** OB Phase 0 ADR-001 approved. The `Sender` seam (P1-5 / GAP-04) is the seam the MTA adapter plugs into — extract it first. GAP-01 (durable scheduled record) and GAP-48 (queue ops) fold in here.

**Acceptance criteria** (todo.md Track A Phase 1 + upcomming-convo.md §7.2):
- MTA adapter + lifecycle manager; direct-to-MX and upstream-relay modes both deliver.
- Outbound queue visibility with retry, hold, and manual release; delivery-attempt records + durable transport events.
- IP binding, per-domain sending policy, per-domain/per-IP rate limits; TLS, DKIM signing, SPF/DMARC alignment checks, PTR guidance.
- Test harnesses for SMTP responses, transient failures, permanent failures, and remote throttling.
- Flow requirements honored: stable message identifier across systems; API acceptance / MTA queue acceptance / remote SMTP acceptance / final delivery evidence distinguished; idempotency + duplicate protection + durable retry; campaign traffic cannot starve transactional; the MTA's actual response is recorded.
- Exit criterion: controlled outbound delivery with traceable, recoverable message state — **no production direct-to-MX launch until operational readiness gates pass**.

### P1-5 · GAP-04 — Sender seam + native ESP API integrations with failover and per-send server selection

**Refs:** GAP-04 · INF-022 · API-015 · INF-023 · INF-024 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.1

**Why it matters.** The register's largest build (XL) and biggest competitive-parity item: today sending is generic SMTP only, retry re-queues against the same server, and send-time selection picks only the first enabled workspace server (`smtp_repo.go:119-125`, doc 04 §2.1). API-mode ESP integrations (the reference workflow platform ships 7 providers) unlock genuine delivery signals (pairing with the landed provider-event ingestion) and the seam is the precondition for the Option B MTA adapter (P1-4) and for GAP-44's per-key-limit value.

**Dependencies.** Credentials via the AES-256-GCM crypto service (RF-F093, Done); pairs with RF-F052 provider-event signals for testing. Blocks ESP-specific value and informs GAP-44. Recommended to start the seam abstraction early (doc 05 §8 Phase 1) even though clients complete into Phase 2.

**Acceptance criteria** (condensed from doc 04 §2.1 GAP-04):
- A `Sender` interface is extracted from `smtp_sender.go` (`Send`, `TestConnection`) and the relay path implements it.
- A workspace can configure an SES integration and send via API with credentials encrypted (`crypto.Encrypt`).
- A failing primary server fails over to the next candidate within one retry; deterministic TaskIDs prevent failover double-sends.
- Campaigns can pin a server; send-time selection uses ordered candidates + health-aware rotation (failure counters already exist at `handler.go:336-341`).

### P1-6 · GAP-05 — Caller-supplied `Idempotency-Key` on send endpoints

**Refs:** GAP-05 · INF-031 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.1

**Why it matters.** Internal deterministic TaskID dedup (landed `a6f1681`) protects the queue, but client-level retries *before* enqueue still double-send — there is no `Idempotency-Key` handling anywhere (grep clean, doc 04 §2.1). UseSend/Hyvor Relay treat caller keys as table stakes. Folds into the OB Phase 1 idempotency requirement.

**Dependencies.** None; complements the internal TaskID dedup.

**Acceptance criteria** (condensed from doc 04 §2.1 GAP-05):
- `Idempotency-Key` honored on `POST /emails/send|send-template|batch`; `(workspace_id, key) → email_id/result` persisted with a unique index.
- Identical key returns the same email_id without a second send (test with 2 concurrent requests — unique-index race winner).
- A different key creates a new email; keys expire via the existing retention cron.

### P1-7 · GAP-06 — Auto-pause circuit breaker on failure and complaint thresholds

**Refs:** GAP-06 · INF-016 · DOM-011 · ANA-010 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.2

**Why it matters.** ReForwarder has alerts only; competitors pause sending automatically at failure/complaint thresholds (NU: 5 consecutive failures; MailCheap: 2.5% bounce / 0.08% complaint — doc 04 §2.2). On shared upstream IPs a runaway campaign burns reputation for everyone because nothing stops it — alerts notify, but sending continues. Competitors treat this as table stakes.

**Dependencies.** **GAP-02 residual (P0-2/P0-3)** — gates must not fire on accept-only signals; the campaign pause/resume CAS (`campaign_handler.go:391-438`, RF-F049 Done) is the actuator. Folds into OB Phase 1 (reputation/abuse).

**Acceptance criteria** (condensed from doc 04 §2.2 GAP-06):
- A campaign auto-pauses when consecutive-failure or bounce/complaint thresholds trip (extending `bounce_alert.go` evaluation into a pause action via the existing CAS transition).
- Resume is manual and audited; thresholds are configurable per workspace.
- No pause fires on accept-only (pre-GAP-02) signals.

### P1-8 · GAP-14 — Signed timestamp replay protection on outbound webhook signatures

**Refs:** GAP-14 · API-010 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.3

**Why it matters.** Outbound webhooks are HMAC-SHA256-signed (`dispatcher.go:485`) but carry no timestamp, so a captured payload can be replayed indefinitely against customer endpoints. UseSend's timestamp + 5-minute rejection window is the exemplar. Cheap cryptographic hygiene on an otherwise strong webhook system (risk R9).

**Dependencies.** None; coordinate the SDK `constructEvent` documentation with GAP-23 (P3-11).

**Acceptance criteria** (condensed from doc 04 §2.3 GAP-14):
- `X-Posta-Timestamp` is part of the HMAC input; deliveries older than 5 minutes are rejected.
- Clock-skew tolerance is documented in `webhook_docs.go` payload examples; a registry test pins the new header; the SDK verification helper is updated in lockstep.

### P1-9 · GAP-13 — Wire `security_events` retention enforcement (DeleteOlderThan caller)

**Refs:** RF-F091 · GAP-13 · SEC-008 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.3

**Why it matters.** `security_events_retention_days` is declared (`security_handler.go:489`) and read (`security/rules.go:129`) but `SecurityEventRepository.DeleteOlderThan` (`security_event_repo.go:157-160`) has no caller — the highest-volume security table grows unbounded and degrades the ban engine's own store (risk R7). One function call away from closed.

**Dependencies.** None — the repo method and setting both exist; extend the retention cron (`retention.go:195-196` pattern).

**Acceptance criteria** (condensed from doc 04 §2.3 GAP-13):
- Rows older than the configured window are deleted daily, batched to avoid long-lived transactions.
- Setting `0` explicitly disables pruning; the setting is surfaced in the admin Settings retention category.
- Covered by a test mirroring `retention_test.go`.

### P1-10 · GAP-15 — Resolve tracking toggle asymmetry for the campaign open pixel (product-decision gate)

**Refs:** RF-F100 · GAP-15 · ANA-012 · todo.md Track B Phase 1 (product-decision gate) · 04-feature-gap-analysis.md §2.3

**Why it matters.** Campaign mail still receives the open pixel when `email_tracking_enabled` is off — the deliberate else-branch (`campaign_processor.go:392-396`, re-verified at HEAD per doc 04) contradicts the setting's documented promise (`provider.go:174-178`). The privacy promise is currently false for marketing sends (risk R8). This is a **product decision before code** (doc 05 hard gate): either the doc moves or the code moves, but the contradiction must not survive.

**Dependencies.** A recorded product-intent decision is the precondition (audit E-F NV item 1); coordinate privacy posture with GAP-40 (P3-5).

**Acceptance criteria** (condensed from doc 04 §2.3 GAP-15):
- The decision is recorded (align behavior to the doc, or codify the campaign exception in the doc).
- With the toggle off, campaign HTML contains no pixel and no rewriting (asserted on the rendered body); unsubscribe/web-view links are preserved either way.
- The setting's documentation matches shipped behavior.

### P1-11 · RF-F101 / D1 — Remove the dead Contact Lists surface end-to-end

**Refs:** RF-F101 · D1 · module audit (frontend-integration) · todo.md D1–D9 · 04-feature-gap-analysis.md §4

**Why it matters.** `contact_list_handler.go` (7 handler methods) has zero route registrations, the Vue views and API client are unrouted, and a dead in-app link pushes a nonexistent route (404 in the UI) — while GDPR export/import still round-trips the dead surface. Three meanings of "contact" confuse users and developers (doc 04 §4 D1). Subscribers are the canonical audience; Contacts stay as the read-only send ledger; ContactLists go away entirely.

**Dependencies.** D1 decision (recorded in doc 04 §4). Removal has a GDPR export/import data-migration cost (audit-C NV-5) — needs the `migration/upgrade/` versioned-step pattern and a release note for `contact_list_members.Data`.

**Acceptance criteria:**
- In one release: export/import stops carrying ContactLists; model, handler, views, and API client are dropped via a versioned upgrade step; the dead link redirects.
- A release note documents the `contact_list_members.Data` disposition (mapped or dropped).
- Post-removal grep: no route-less handler, no unrouted views, no dead client module.

### P1-12 · GAP-25 — Double opt-in (DOI) confirmation flow

**Refs:** RF-F075 · GAP-25 · CON-004 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.5

**Why it matters.** Entirely absent — subscription is immediate on `POST /subscriber-lists/subscribe` (`subscriber_list_handler.go:373-460`; grep-verified absent per FEATURE_INVENTORY RF-F075). This is the single largest EU-marketing compliance blocker, and it **blocks** signup forms (GAP-30) and the preference center (GAP-35) — the consent foundation for the whole audience funnel.

**Dependencies.** Hosted token pages (`tracking_pages.go`) as the token-page pattern; email sending. Blocks GAP-30 and GAP-35 (doc 05 hard edge).

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-25):
- Subscribe creates a `pending` member, sends a confirmation email (per-list DOI setting), and only confirmed members receive campaigns.
- The confirm endpoint uses HMAC tokens modeled on `/t/u/{token}`; expired tokens hit the hosted error path.
- Single-opt-in lists are unchanged; imports honor the list's DOI policy.
- Confirmed state fires `list.subscribed` (feeding welcome automations); E2E test: subscribe → confirm → campaign eligibility.

### P1-13 · GAP-26 — Campaign test-send and campaign-scoped preview

**Refs:** GAP-26 · CAM-007 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.5

**Why it matters.** The template send-test ignores campaign data and there is no campaign-scoped preview — the most-requested marketer affordance ("top usability gap", audit-D). Everything behind it exists; it **blocks** GAP-34 (blocks/slots must render in this preview).

**Dependencies.** Template render pipeline + `SendTestByTemplateID` (both exist). Blocks GAP-34.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-26):
- `POST /campaigns/{id}/test-send` renders campaign + template + `template_data` + localization to a real inbox, resolving content exactly like `campaign_processor.go:290-367`.
- A preview endpoint returns localized rendered HTML without sending; a UI button lands in CampaignDetail.
- The endpoint is documented.

### P1-14 · GAP-27 — CSV exports for subscribers, lists, segments, and analytics/message history

**Refs:** GAP-27 · CON-010 · ANA-011 · RF-F077 · todo.md Track B Phase 1 · 04-feature-gap-analysis.md §2.5

**Why it matters.** Only audit-log CSV + the GDPR JSON bundle exist (`logs_handler.go:853`) while import is rich — an asymmetric portability story flagged in both audits. Mostly plumbing: the streamed CSV exporter with a 100k cap (`logs_handler.go:821-892`) is directly reusable.

**Dependencies.** The segment query builder for row-parity; existing scope rules for gating.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-27):
- `GET /subscribers/export`, `/lists/{id}/export`, `/segments/{id}/export`, and an analytics/message-history export exist.
- Exports match list filters and segment definitions row-for-row; large exports stream with the cap honored; exports are audit-logged.

### P1-15 · Inbound mail routing programme (validation, aliases, spam/malware, quarantine, bounce/return-path)

**Refs:** upcomming-convo.md §6 inbound requirements, §7 Phase 2 · todo.md Track A Phase 2 · ADR-002 · ADR-006 · GAP-47 · INF-020

**Why it matters.** The hosted-provider half of Option B. The existing inbound receiver (RF-F034) is a store-first webhook feeder with recipient-domain gating — not a routing engine. Option B design rule 4: *never accept inbound mail without a durable path to deliver, route, quarantine or reject it* (upcomming-convo.md §8). Spam/malware content filtering is a **separate boundary** from the network security engine (ADR-006; upcomming-convo.md §4).

**Dependencies.** OB Phase 0 ADR-002/ADR-006. Builds on RF-F034 (inbound receiver, Done) and RF-F040 (routing rules, P1-16). Folds in GAP-02 residual (bounce/return-path, P0-2) and GAP-47 (antivirus, P3-13).

**Acceptance criteria** (todo.md Track A Phase 2 + upcomming-convo.md §7.3):
- Inbound SMTP service integration with recipient and domain validation performed **before** accepting responsibility.
- Aliases, forwarding, routing rules, and catch-all policy handled explicitly; unknown users rejected or quarantined per policy.
- Spam and malware filtering interfaces; bounce and return-path processing; quarantine with operator review; inbound event and audit trails.
- Flow requirements honored: idempotent inbound delivery, per-mailbox/per-domain quotas, forwarding loop + abuse protection, mailbox delivery kept separate from marketing subscriber state.
- Exit criterion: accepted inbound messages are durably routed, rejected, or quarantined with observable outcomes.

### P1-16 · RF-F040 — Per-domain inbound routing rules (alias/catch-all/forward targets)

**Refs:** RF-F040 · upcomming-convo.md §6 inbound requirements · module audit (smtp-relay)

**Why it matters.** Today inbound only gates on recipient-domain verification plus webhook fan-out (INF-030, FEATURE_INVENTORY RF-F040) — there are no per-domain route tables for aliases, catch-all, or forwarding. This is the concrete first slice of the inbound routing programme (P1-15) and is independently useful to self-hosters.

**Dependencies.** RF-F034 (inbound receiver); boundary per ADR-002.

**Acceptance criteria:**
- Per-domain route tables support alias targets, catch-all policy, and forward targets (including webhook/HTTP routes) applied before the store-first parse decision.
- Unknown-recipient behavior is explicit per domain (reject / catch-all / drop) — no silent loss.
- Forwarding includes loop protection and honors per-domain quotas (upcomming-convo.md §6 inbound requirements).

### P1-17 · Mailbox & storage foundation (folders, flags, quotas, MIME store)

**Refs:** upcomming-convo.md §4 mailbox storage, §7 Phase 3 · todo.md Track A Phase 3 · ADR-003 · ADR-008

**Why it matters.** Hosted mailboxes need mailbox-oriented storage — folders, flags, read/unread, expunge, concurrent access, quotas — which the transactional `Email` model does not have and must not acquire (upcomming-convo.md §4: "Do not store mailbox contents as ordinary transactional `Email` records"). The backend must be chosen only after a prototype proves compatibility with the selected IMAP implementation.

**Dependencies.** ADR-003 (mailbox storage) and ADR-008 (data/event ownership) approved in OB Phase 0. Gates OB Phase 4 (IMAP/POP3) and Phase 5 (webmail).

**Acceptance criteria** (todo.md Track A Phase 3):
- Storage backend chosen and prototyped; mailbox, folder, quota, and message metadata implemented; MIME/attachment storage with retention.
- Append, fetch, copy, move, flags, expunge, and search work; concurrent-access and synchronization behavior defined.
- Backup, restore, and mailbox-migration tooling exist.
- Exit criteria: mailbox data survives restarts, supports concurrent clients, backs up/restores without corruption. The transactional `Email` model is **not** reused and no auto-migration of transactional records occurs.

### P1-18 · IMAP and POP3 access against the mailbox backend

**Refs:** upcomming-convo.md §7 Phase 4 · todo.md Track A Phase 4 · ADR-004

**Why it matters.** Standard-client access is the product promise of hosted email; without IMAP/POP3 the mailbox foundation (P1-17) has no consumers. POP3 needs its own deletion semantics, not IMAP's.

**Dependencies.** OB Phase 3 storage backend selected; identity integration with ReForwarder auth.

**Acceptance criteria** (todo.md Track A Phase 4):
- IMAP integrated against the selected backend with authn/authz tied to ReForwarder identities; folder and message synchronization work.
- POP3 as a separate access mode with correct deletion semantics.
- Standard-client and protocol compatibility testing done; connection limits, TLS enforcement, rate limits, and audit logs in place.
- Exit criterion: standard mail clients securely use hosted mailboxes with consistent state.

### P1-19 · Webmail and mailbox administration

**Refs:** upcomming-convo.md §7 Phase 5 · todo.md Track A Phase 5 · ADR-005

**Why it matters.** The user-facing half of hosted email: inbox, compose, search, settings — plus the admin tools (provisioning, suspension, recovery) that make mailboxes operable. Option B deliberately sequences this *after* storage and protocols to avoid building UI on unproven foundations.

**Dependencies.** OB Phases 3–4; frontend boundaries per ADR-005.

**Acceptance criteria** (todo.md Track A Phase 5):
- Mailbox provisioning and lifecycle management; inbox, folders, compose, reply, forwarding, attachments; search and read/unread state; mailbox settings.
- Password changes, sessions, and device management; mailbox quotas and usage reporting; admin tools for mailbox suspension and recovery.
- Exit criterion: users manage hosted email without a third-party webmail deployment.

### P1-20 · IP pools, warm-up controls, and reputation monitoring

**Refs:** upcomming-convo.md §7 Phase 6, §8 · todo.md Track A Phase 6 · INF-025 · ADR-007

**Why it matters.** Owning egress IPs (P1-4) without warm-up, throttling, and reputation monitoring is how new ESPs burn domains. Option B reinstates IP pools (excluded under relay-only) and makes reputation a first-class operational responsibility (upcomming-convo.md §8 table: warm-up, throttling, reputation monitoring, abuse response, blacklist alerts). GAP-06 auto-pause (P1-7) is the sending-side complement.

**Dependencies.** OB Phase 1 MTA with IP binding; ADR-007 (DNS/deliverability). Folds toward OB Phase 6 convergence.

**Acceptance criteria** (todo.md Track A Phase 6 + upcomming-convo.md §7.7/§8):
- IP pool management with per-pool binding and routing policy; warm-up schedules and throttling controls.
- Reputation monitoring with blacklist alerts and abuse-response runbooks; tenant isolation holds across pools.
- Deliverability posture verified against the §8 requirements table (IP reputation row).

---

## P2 — Parity & platform hardening (31 gaps)

### Batch A — Deliverability & DNS

### P2-1 · RF-F026 — Complete domain deliverability health / diagnostics

**Refs:** RF-F026 · R-07 · GAP-08 · todo.md §0.2

**Why it matters.** The health endpoint landed in `7f445f2` (SPF/DKIM/DMARC status, DKIM key state, per-domain bounce/complaint rates, live DNS re-verification persisted — todo.md §0.2), but the registry keeps it In Progress: SPF/DMARC parsing depth and DMARC-report trend surfacing (per RF-F028) remain (FEATURE_INVENTORY RF-F026 limitation). Deliverability visibility is the operator's early-warning system for the reputation work in Track A.

**Dependencies.** RF-F022 (verification, Done); pairs with RF-F028 (DMARC rua ingestion, P2-3) for trend data; GAP-08 residual work (P2-4).

**Acceptance criteria:**
- The health page surfaces DMARC aggregate-report trends (with RF-F028 data) alongside the landed SPF/DKIM/DMARC status and bounce/complaint rates.
- SPF/DMARC depth matches the GAP-08 residual criteria (P2-4); live re-verification results persist (landed) and are verified end-to-end.
- Health checks remain advisory — never block sends.

### P2-2 · GAP-10 — Complete strict verified-domain sending policy (campaign-path parity, per-domain caps)

**Refs:** RF-F027 · R-23 · GAP-10 · DOM-012 · todo.md Track B Phase 2 Batch A

**Why it matters.** Strict mode is enforced on the transactional path (`require_domain_verification`, landed `7f445f2` — todo.md §0.2) but campaigns only *warn* on unverified sender domains (`campaign_handler.go:566-571`), and there are no per-domain volume caps on shared servers. Until the paths agree, the strict-mode promise is half-true, and one noisy campaign can degrade a shared server.

**Dependencies.** **GAP-08 (P2-4) first** — strict gating is only fair once drift re-verification keeps records healthy (doc 05 hard edge). Folds into OB Phase 1 (per-domain policy).

**Acceptance criteria** (condensed from doc 04 §2.2 GAP-10):
- With strict mode on, a campaign on an unverified sender domain is rejected pre-send (warn→block parity via `RequireVerifiedDomain`).
- Per-domain hourly caps return 429 with a clear error, consumed through the existing Redis limiter (`ratelimit/quota.go:44`).
- Warn-only remains the default so existing deployments don't change behavior.

### P2-3 · RF-F028 — Complete DMARC aggregate report (rua) ingestion

**Refs:** RF-F028 · R-17 · commit `f5f0e92` · 04-feature-gap-analysis.md §6.2 (superseded)

**Why it matters.** DMARC rua ingestion was originally excluded as verified-absent industry-wide (doc 04 §6.2) — that exclusion is **superseded**: commit `f5f0e92` landed RFC 7489 aggregate-report ingestion on the inbound pipeline (parse-driven detection, gzip/base64/blob attachments, idempotent per-record storage, 288-line test). Completion turns received reports into operator-visible insight.

**Dependencies.** Requires the inbound receiver enabled (default-off — documented posture); pairs with RF-F026 (health page surfacing).

**Acceptance criteria:**
- Reports parse and store idempotently (landed, verified by `provider_events/dmarc_test.go`); per-domain aggregate trends surface on the health page.
- Behavior for reports covering non-verifying domains is explicit (currently stored — decide keep/flag).
- Documentation states the inbound-receiver dependency and the operator enablement path.

### P2-4 · RF-F023 / GAP-08 — Deepen DNS record guidance (SPF/DMARC parsing, MX records, drift re-verification)

**Refs:** RF-F023 · GAP-08 · DOM-005 · DOM-006 · DOM-008 · todo.md Track B Phase 2 Batch A · upcomming-convo.md §8 DNS row

**Why it matters.** DNS records are verified once, on demand, and can silently rot; SPF/DMARC checks are presence-only with no lookup-count or policy parsing (`verifier.go:97-136`, doc 04 §2.2). Drift silently breaks deliverability, and Option B's DNS requirements table (§8) expects MX/SPF/DKIM/DMARC/PTR/FCrDNS guidance as first-class.

**Dependencies.** None upstream; **feeds GAP-10 (P2-2)**. Folds into OB Phase 6 (DNS automation, ADR-007).

**Acceptance criteria** (condensed from doc 04 §2.2 GAP-08):
- A daily cron re-verifies TXT/DKIM records for verified domains and raises workspace + admin alerts on drift within 24h (removing a DKIM/SPF record triggers the alert).
- SPF include/qualifier parsing with the 10-lookup count displayed; DMARC `p=`/`rua` policy parsed and shown; MX guidance present.
- Re-verification is advisory only (never blocks sends) and DNS lookups are rate-limited inside the cron.

### P2-5 · GAP-09 — Custom tracking domain

**Refs:** RF-F029 · GAP-09 · DOM-009 · todo.md Track B Phase 2 Batch A

**Why it matters.** Tracking runs on the instance domain only; a per-workspace branded tracking host is brand trust + deliverability for the marketing use case (Postal/the reference workflow platform parity, doc 04 §2.2).

**Dependencies.** Domain verification (DOM-002, Done). The HMAC token scheme must remain unchanged while the base URL varies (compat rule, FEATURE_INVENTORY RF-F029).

**Acceptance criteria** (condensed from doc 04 §2.2 GAP-09):
- A workspace-level `tracking_domain` verified via TXT (reusing `verifier.go:88`) drives all `/t/*` link/pixel/web-view URLs once verified.
- Unsigned/unverified hosts are rejected; per-message HMAC tokens unchanged; reverse-proxy host routing documented.

### Batch B — Audience

### P2-6 · GAP-29 — Subscriber tags

**Refs:** RF-F076 · GAP-29 · CON-006 · todo.md Track B Phase 2 Batch B

**Why it matters.** No `Tag` model exists anywhere (verified absent, doc 04 §2.5); SendPortal/BillionMail use tags as primary segmentation. Fits the existing guarded segment engine as a new whitelisted operator (FEATURE_INVENTORY RF-F076).

**Dependencies.** None; pairs with GAP-27 (P1-14) for CSV column round-trip.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-29):
- `subscriber_tags` join table + tag CRUD; assignable singly, in bulk, and via import/export columns.
- A segment can filter by tag via an `in_tags` operator added to the guarded builder whitelist (injection tests extended); list endpoints filter by tag.

### P2-7 · GAP-31 — Bulk contact operations

**Refs:** GAP-31 · CON-011 · todo.md Track B Phase 2 Batch B · 04-feature-gap-analysis.md §2.5

**Why it matters.** Bulk operations are import-only; delete is single-item; GDPR purge is whole-workspace (`workspace_data_handler.go:271`). Standard list-management affordance across competitors (Listmonk bulk select actions).

**Dependencies.** Segment engine (for by-segment scope).

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-31):
- `POST /subscribers/bulk` with `{action, ids|segment_id}` performs delete, status change, and list add/remove in bounded batches with per-action audit events.
- Segment-scoped bulk matches the segment preview count exactly; caps prevent unbounded transactions.

### P2-8 · GAP-19 — Contact upsert/sync API

**Refs:** GAP-19 · CON-012 · todo.md Track B Phase 2 Batch B · 04-feature-gap-analysis.md §2.4

**Why it matters.** Import dedupe + CRUD exist but there is no explicit upsert endpoint for external sync loops (UseSend/the reference workflow platform/Hyvor Post parity) — clients hand-roll read-then-write races today.

**Dependencies.** The `(workspace,email)` unique index exists (`subscriber.go:66-67`).

**Acceptance criteria** (condensed from doc 04 §2.4 GAP-19):
- Upsert semantics on `POST /subscribers` (e.g. `upsert_by=email`): creates, then updates the same row on repeat calls; concurrent upserts don't duplicate (unique-index test).
- `contact.created` fires only on true create; the import path is unchanged.

### P2-9 · GAP-32 — Contact activity timeline and unsubscribe-reason reporting

**Refs:** GAP-32 · CON-019 · CON-015 · todo.md Track B Phase 2 Batch B · 04-feature-gap-analysis.md §2.5

**Why it matters.** Events live only in the Logs/messages views; there is no per-subscriber timeline, and unsubscribe reasons are stored (`subscriber_list.go:94`) but never aggregated. Pure read-model over existing data with high support/debug value.

**Dependencies.** `email_events` / `tracking_events` (exist); needs joins indexed by subscriber email + campaign_messages.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-32):
- `GET /subscribers/{id}/activity` returns a chronological timeline (sends/opens/clicks/bounces/unsubs) linked to messages, rendered in SubscriberDetail.
- Unsubscribe reasons aggregate per list/workspace; queries are indexed and don't duplicate the Logs read model.

### Batch C — Campaign operability

### P2-10 · GAP-28 (residual) — Persist automation `ab_test` node outcomes

**Refs:** GAP-28 residual · AUT-008 · RF-F059 · todo.md Track B Phase 2 Batch C

**Why it matters.** Campaign A/B winner selection landed end-to-end in `6d2d7c0` (R-08: hold-back, CAS `MarkABWinner`, winner send, UI). The named residual is that the automation `ab_test` node's assignments/outcomes are not persisted as experiment results (RF-F059 limitation), so automation-run splits are unauditable.

**Dependencies.** Campaign winner machinery (landed); automation engine (RF-F069, Done).

**Acceptance criteria:**
- An `ab_test` node execution persists its variant assignment and outcome as an experiment result, visible in the execution drawer.
- Held/never-decided outcomes have a deterministic recorded state (tie/no-signal fallback rules).

### P2-11 · GAP-41 — Automation workflow dry-run / test tooling

**Refs:** RF-F071 · GAP-41 · AUT-018 · todo.md Track B Phase 2 Batch D

**Why it matters.** The engine has strong static validation (reachability/cycles/weights) but no test execution against a sample contact (audit-G). Standard pre-live safety for a system that sends real email to real people.

**Dependencies.** Engine execution paths (exist, ~40 tests); `SendAutomationEmail` must be stubbable.

**Acceptance criteria** (condensed from doc 04 §2.7 GAP-41):
- `POST /automations/{id}/dry-run` runs the graph against a sample contact in a side-effect-free mode (no-op node executors + synthetic `dry_run` journey) and reports branch decisions and would-be delays.
- Zero side effects verified by test: no email sends, no `automation_trigger_log` rows, no list mutations, no enrollments. UI button in the builder.

### P2-12 · GAP-42 — Re-engagement and scheduled enrollment triggers

**Refs:** GAP-42 · AUT-015 · todo.md Track B Phase 2 Batch D · 04-feature-gap-analysis.md §2.7

**Why it matters.** Triggers are purely event/schedule-reactive; there are no inactivity conditions and no cron enrollment sweep (`domain.go:632-661` condition limits) — the engine can't do "win-back subscribers inactive 30 days", the classic retention play. The engagement subqueries already exist in the segment engine (`segment_query.go:109-131`).

**Dependencies.** Segment-engine engagement subqueries (exist); automation once-dedup (`triggers.go:119-133`).

**Acceptance criteria** (condensed from doc 04 §2.7 GAP-42):
- Inactivity conditions (last engagement older than N days) and a scheduled trigger kind evaluated by a daily cron sweep that enrolls matching subscribers.
- Re-enrollment policy and the existing once-dedup are respected; the cron is visible in `/admin/crons`; sweep cost is bounded per run.

### P2-13 · GAP-43 — Stop-on-reply for automations

**Refs:** GAP-43 · AUT-016 · todo.md Track B Phase 2 Batch D · 04-feature-gap-analysis.md §2.7

**Why it matters.** `ExitOnReply` is a reserved stub the engine never reads, and no reply detection exists despite `message.replied` webhook events in the registry. the reference workflow platform detects replies via In-Reply-To/References. The inbound pipeline needed for matching already exists.

**Dependencies.** Inbound email pipeline (RF-F034+, Done); journey exit transactional path (`automation_handler.go:491-509`).

**Acceptance criteria** (condensed from doc 04 §2.7 GAP-43):
- On inbound ingest, a reply matched to a sent email (References/In-Reply-To, with bare-email fallback) exits the recipient's active journeys with reason `reply` where `ExitOnReply` is set; journeys without the flag continue.
- No false exits for unrelated inbound; relay/deployment prerequisites documented.

### P2-14 · GAP-34 — Reusable blocks/sections library and campaign content slots

**Refs:** GAP-34 · TPL-002 · TPL-012 · CAM-006 · todo.md Track B Phase 2 Batch C

**Why it matters.** The builder ships only the stock GrapesJS newsletter preset (`EmailBuilder.vue:41`) — no product block library or saved sections, and the template object is atomic so campaigns can't override blocks. The biggest editor UX lever, scoped because most competitors don't ship block libraries either.

**Dependencies.** **GAP-26 (P1-13) first** — overrides must render in the campaign preview GAP-26 builds (doc 05 hard edge). BuilderJSON persistence exists (`template_localization.go:31`).

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-34):
- A `template_blocks` entity (name, HTML, category) surfaces as GrapesJS custom blocks for all templates and round-trips through export/import.
- Campaign-level `content_overrides` JSONB replaces named blocks post-resolve in `resolveTemplateContent` (`campaign_processor.go:447-536`) without breaking localization; overrides render in preview.

### P2-15 · D3 — Consolidate segmentation onto SegmentCondition; convert and deprecate FilterRules

**Refs:** D3 · todo.md D1–D9 · 04-feature-gap-analysis.md §4

**Why it matters.** Two expression languages for one concept coexist: legacy `FilterRules` dynamic lists (kept "byte-identical") and the guarded `SegmentCondition` builder with 14 operators, injection tests, and live preview. Both are resolved by the campaign worker (`campaign_processor.go:116-152`) — double maintenance in worker and preview paths, divergent capabilities (segments can't reference list membership; dynamic lists can't use the full engagement-subquery set).

**Dependencies.** Legacy-parity translators are half-proven (`segment_query_test.go:660,697`); campaign audiences referencing dynamic lists must keep resolving during the transition.

**Acceptance criteria** (doc 04 §4 D3):
- A FilterRules→SegmentCondition converter ships with exact-semantics tests; dynamic lists present as segments on read.
- New dynamic-list creation is deprecated (UI + API); worker compatibility is kept one release; legacy evaluation is dropped only after parity is confirmed.

### P2-16 · GAP-30 — Subscriber signup forms (form-to-subscriber capture)

**Refs:** RF-F074 · GAP-30 · CON-017 · todo.md Track B Phase 2 Batch B (DOI blocks forms)

**Why it matters.** ReForwarder "forms" are website-form→inbox capture, not audience capture (audit-C explicit classification) — the audience-acquisition funnel has no front door. Keila/Listmonk parity.

**Dependencies.** **GAP-25 DOI (P1-12)** blocks this (consent first). Reuses the inbound-form protections: Redis nonce + `MinFillSeconds` time-trap (`form_ingest_handler.go:69-145`) and the spam triage ladder.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-30):
- A `signup_forms` entity (slug, target list, styling) serves public hosted/embedded forms that create (pending→confirmed) subscribers into the chosen list.
- Nonce/time-trap + IP limiter enforced; private lists are not addressable; spam submissions triage like inbound forms; embed snippet served from the instance or tracking domain (pairs with GAP-09).

### P2-17 · GAP-33 — Wire campaign duplication UI and add campaign-level failed-recipient retry

**Refs:** RF-F104 · GAP-33 · CAM-004 · CAM-018 · module audit (frontend-integration)

**Why it matters.** Duplication exists as a backend endpoint with zero UI/API-client call sites (`campaign_handler.go:466-500`; grep-clean in `web/src/api/campaigns.ts`), and campaigns cannot be edited from the UI. Per-message manual retry exists but there is no campaign-level `retryFailed` (the reference workflow platform `broadcasts.retryFailed` parity). Small wires over existing machinery with visible operator value.

**Dependencies.** Per-message retry route (`workspace_resource_routes.go:116`) and the CampaignMessage ledger (exist).

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-33):
- A Duplicate action (client function + button) calls the existing endpoint and produces a fresh editable draft copying audience/settings/variants.
- `POST /campaigns/{id}/retry-failed` re-enqueues `failed` campaign_messages since a timestamp exactly once (TaskID dedup interplay); both actions audited; snapshot analytics unaffected.

### Integration & signal-surface hygiene

### P2-18 · RF-F102 — Remove or wire dead admin API client methods (listApiKeys / revokeApiKey / listEmails)

**Refs:** RF-F102 · module audit (frontend-integration)

**Why it matters.** The admin API client ships `listApiKeys`, `revokeApiKey`, and `listEmails` with no corresponding backend routes (`web/src/api/admin.ts:106-112` — verified this session). Dead surface misleads maintainers and suggests admin capabilities that don't exist.

**Dependencies.** Decide per method: delete, or add the backend route deliberately (an admin API-key management surface would need scope design).

**Acceptance criteria:**
- Each of the three methods is either removed from `admin.ts` or backed by a registered, tested admin route — no method without a route remains; the admin API client type-checks and spec coverage reflects the decision.

### P2-19 · RF-F103 — Expose backend-only endpoints in the UI (segments contacts, message analytics & assign, automation custom-event ingest, admin provider breakdown)

**Refs:** RF-F103 · module audit (frontend-integration)

**Why it matters.** Several backend endpoints have no UI consumer (and some dead client functions): segment contacts, message analytics + assignment, automation custom-event ingest, admin provider breakdown. Built-but-unreachable surface is invisible value and untested code paths. **Caveat:** the module-audit source file is not on disk, so the exact endpoint list needs re-verification before acting (FEATURE_INVENTORY open question 3).

**Dependencies.** Re-verify the endpoint list at HEAD first (registry limitation).

**Acceptance criteria:**
- The endpoint list is re-verified at HEAD (route sweep + client grep) and recorded.
- Each listed endpoint gains a UI consumer (or is documented as intentionally API-only with the dead client function removed).

### P2-20 · GAP-18 (residual) — Webhook test-fire endpoint

**Refs:** RF-F056 · GAP-18 residual · API-013 · todo.md Track B Phase 2 Batch E

**Why it matters.** Replay of failed deliveries landed in `6d2d7c0` (R-06); the #1 integration debugging affordance still missing is `POST /webhook-endpoints/{id}/test` — dispatching a sample event so customers can verify endpoint wiring before real events flow (the reference workflow platform test events parity).

**Dependencies.** Webhook dispatcher + delivery history (both Done).

**Acceptance criteria** (condensed from doc 04 §2.4 GAP-18):
- Test-fire traverses the endpoint's real filters and the SSRF-guarded transport, is audited and rate-limited, and appears in delivery history as a distinguishable event.

### P2-21 · GAP-07 (residual) — Bounce classification analytics surface (classification counts)

**Refs:** RF-F057 · GAP-07 residual · ANA-004 · todo.md Track B Phase 2 Batch A

**Why it matters.** The 14-class bounce classifier (R-05) computes classification counts (`ClassificationCounts` over `email_events.metadata->>'classification'`) but no API endpoint or UI surfaces them (RF-F057). Retry exhaustion still records only `failed` — soft bounces are never auto-detected as rows (`handler.go:529-565`), so hard/soft trends are invisible.

**Dependencies.** Classifier (landed). Hard/soft row creation on exhaustion is the data-side half; the surface is the visibility half.

**Acceptance criteria** (condensed from doc 04 §2.2 GAP-07):
- A classification-counts endpoint + hard/soft bounce analytics surface exist; a 4xx-exhausted send produces a soft bounce row; greylist replies never suppress; suppression stays hard-only.

### Batch E — Platform & ops

### P2-22 · GAP-48 — Per-queue latency metric and pause/throttle controls

**Refs:** GAP-48 · OBS-009 · todo.md Track B Phase 2 Batch E · todo.md Track A Phase 1 (folds in)

**Why it matters.** No per-queue latency metric exists (Postal added one in 3.3.0) and per-queue pause/throttle isn't exposed — operators can see depth but not wait time, and can't stop `bulk` to let `transactional` drain during incidents. Folds directly into OB Phase 1 queue operations.

**Dependencies.** asynq Inspector wiring (exists, `admin_queue_handler.go:37-306`).

**Acceptance criteria** (condensed from doc 04 §2.9 GAP-48):
- `posta_queue_latency_seconds` (enqueue→process, from task payload timestamps) is visible per queue in Prometheus + admin.
- Pause/resume set a Redis flag checked pre-claim in producer/worker: pausing `bulk` stops new bulk claims while `transactional` flows; resume works; gauges tested.

### P2-23 · GAP-45 — Blob usage reporting and orphan GC

**Refs:** GAP-45 · OBS-013 · todo.md Track B Phase 2 Batch E

**Why it matters.** The FS/S3 blob store + retention delete blobs, but there is no bytes-per-workspace reporting (AdminInventory counts rows, not bytes) and no orphan-blob sweep — attachment-heavy deployments leak storage silently. Adjacent to OB Phase 3/6 message storage.

**Dependencies.** Retention cron (extends). GC must never delete referenced blobs — dry-run report first.

**Acceptance criteria** (condensed from doc 04 §2.8 GAP-45):
- Per-workspace blob usage surfaces in AdminInventory; orphans older than N days are deleted only after a dry-run report; retention interplay is tested.

### P2-24 · GAP-49 — Document Redis HA guidance and SPOF mitigation

**Refs:** GAP-49 · OBS-004 · R10 · todo.md Track B Phase 2 Batch E · upcomming-convo.md §7 Phase 6 (DR)

**Why it matters.** Single Redis is a SPOF across 9 verified roles (broker, cache, limits, security windows, JTI blacklist, nonces, heartbeats, pub/sub, quotas) with mixed failure semantics (security fail-open, quotas fail-closed) and no sentinel/cluster docs. The single biggest availability lever, and it is mostly writing (risk R10). Folds into OB Phase 6 DR / ADR-009.

**Dependencies.** None (docs + compose profile).

**Acceptance criteria** (condensed from doc 04 §2.9 GAP-49):
- An HA ops doc covers Sentinel/managed-Redis setup, each Redis role's failure mode citing the verified fail-open/fail-closed callsites, and recovery steps; a sentinel compose profile boots clean.

### P2-25 · GAP-16 — Admin-console IP allowlisting

**Refs:** RF-F092 · GAP-16 · SEC-013 · todo.md Track B Phase 2 Batch E

**Why it matters.** No IP restriction exists on `/admin/*` beyond JWT+role; the ban-engine allowlist is a detector exemption, not access control (audit-I). Defense-in-depth for the most privileged surface; small and opt-in.

**Dependencies.** None (a reverse proxy remains the documented alternative — `internal/config/security.go:61`).

**Acceptance criteria** (condensed from doc 04 §2.3 GAP-16):
- An `AdminIPAllowlist` setting is checked in the admin middleware chain reusing `IPAllowed` (`middleware.go:263-279`); CIDRs configured → admin endpoints 403 from other sources; unset = current behavior (fail-open); allow/deny tested.

### P2-26 · GAP-51 — Error-monitoring hook

**Refs:** RF-F097 · GAP-51 · OBS-011 · todo.md Track B Phase 2 Batch E

**Why it matters.** Errors are slog'ed only — no aggregate error signal (Sentry-compatible capture or error-rate counter). Failures are currently visible only as per-email failed states.

**Dependencies.** None.

**Acceptance criteria** (condensed from doc 04 §2.10 GAP-51):
- With a DSN set, error-level records appear in a Sentry-compatible sink with release/commit tags (sampled, scrubbed of PII/secrets); unset = zero behavior change; a `posta_errors_total` family counts errors matching logs.

### Platform gates & verification

### P2-27 · RF-F081 — Complete system-info endpoint feature flags (Section-11 gate)

**Refs:** RF-F081 · commit `346eb67` · commit `0f52636` · module audit (platform-admin-security)

**Why it matters.** `GET /info` now returns a `features` object (`messages_enabled`, `inbound_enabled`, `relay_enabled`, `metrics_enabled` — `internal/handlers/info_handler.go:35`) and sidebar navigation hides the corresponding entries (landed `346eb67`). The registry keeps the item In Progress: the "Section-11 gate" completion remainder per the roadmap — finishing the flag surface so every flag-gated area is discoverable and consistently gated. Feature-flagged surfaces (inbound, relay, messages are all default-off) otherwise present as broken UI to new operators.

**Dependencies.** The four flag sources are Done features (RF-F037/RF-F034/RF-F030/RF-F096); completion is additive.

**Acceptance criteria:**
- Every flag-gated surface is represented in the `/info` features object and gated in navigation consistently (no ungated flag, no flag without a consumer).
- Optional-field fail-open behavior on older backends is tested (`DashboardLayout.spec.ts` pattern).
- The flag set is documented for operators (which env/setting drives each flag).

### P2-28 · RF-F082 / R-21 — Complete API route contract gate (golden file + frontend spec)

**Refs:** RF-F082 · R-21 · commit `346eb67` · module audit (platform-admin-security)

**Why it matters.** The contract test pins all registered routes against the golden file `docs/implementation/api_routes.txt` (357 routes at pin time; `-update` regenerates) and a frontend admin API spec was added (`346eb67`). The registry keeps it In Progress: full frontend API-client spec coverage is the remainder. This gate is what stops route drift between the ~357-route backend and 35 typed client modules from recurring silently.

**Dependencies.** Contract test + golden file (landed).

**Acceptance criteria:**
- Registering or removing a route without updating the golden file fails CI (verified by the test's failure mode, not just its pass).
- The frontend API-client spec covers all client modules (or the uncovered set is explicitly listed and justified).
- The `-update` regeneration workflow is documented for maintainers.

### P2-29 — Add runtime authorization verification tests (admin queue scoping, API-key/admin isolation)

**Refs:** A-B NV-2 · G-H NV-4 · 04-feature-gap-analysis.md §5 · todo.md Sources (04 §5 NV register)

**Why it matters.** Two authorization properties are verified structurally but not at runtime: whether `/admin/queues/*` rejects non-admin API keys (A-B NV-2) and whether `psk_` keys truly cannot reach `/admin/*` (G-H NV-4 — "structurally verified only"). Structural reasoning about middleware chains is exactly how the GAP-11 editor-gate hole survived; these tests pin the boundary.

**Dependencies.** None — test-only; `workspace_rbac_test.go` is the pattern.

**Acceptance criteria:**
- A runtime test asserts non-admin API keys get 403 on `/admin/queues/*` (A-B NV-2's resolving evidence).
- A runtime test asserts a `psk_` key gets 401/403 on an `/admin/*` route (G-H NV-4's resolving evidence).
- Both pass in CI and are cited in the NV register as resolved.

### Batch — Track A Phase 6 platform folds

### P2-30 — Automate DNS provisioning and certificate lifecycle (ACME/SNI per-domain TLS)

**Refs:** upcomming-convo.md §7 Phase 6 · todo.md Track A Phase 6 · DOM-007 · DOM-010 · ADR-007

**Why it matters.** Both were excluded under relay-only (different product shape; meaningless without owned egress/inbound) and are **reinstated by Option B**: once ReForwarder serves MX/IMAP/webmail for many domains, per-domain TLS certificates (ACME/SNI) and automated record provisioning become operational necessities, not conveniences.

**Dependencies.** OB Phase 0 ADR-007; meaningful only once inbound (P1-15) / mailbox access (P1-18) / webmail (P1-19) surfaces exist to terminate TLS for.

**Acceptance criteria** (todo.md Track A Phase 6):
- DNS record provisioning is automated or the operator path is fully documented per domain type (sending, tracking, MX, webmail).
- Certificate lifecycle (issue, renew, SNI selection) is automated for the domains the platform terminates TLS for; expiry monitoring alerts.

### P2-31 — Complete deployment, upgrade, backup, and DR tooling

**Refs:** upcomming-convo.md §7 Phase 6, §8 data durability · todo.md Track A Phase 6 · ADR-009 · RF-F095

**Why it matters.** The pg_dump backup cron + restore runbook landed (`7f445f2`, R-11) and Redis-HA guidance is P2-24, but full-platform DR (multi-store consistency: Postgres + blobs + mailbox storage), upgrade/rollback safety, and multi-node topology tooling are Phase 6 exit requirements — "one supported self-hosted platform; documented deployment; end-to-end operational testing."

**Dependencies.** ADR-009 (deployment topology); extends RF-F095 (backups, Done) and GAP-49 (P2-24); mailbox-storage backup tooling from P1-17 folds in.

**Acceptance criteria** (todo.md Track A Phase 6 + upcomming-convo.md §7.7):
- Documented deployment covering single-node and future multi-node topology (ADR-009), health, restarts, upgrades.
- Upgrade and rollback procedures tested; backup/restore covers Postgres, blob store, and mailbox storage with recovery testing; RPO/RTO stated honestly.
- End-to-end operational testing passes as the Phase 6 exit criterion.

---

## P3 — Long tail (19 gaps)

### Developer surface

### P3-1 · GAP-20 — Deepen SMTP diagnostics (EHLO capability dump, TLS cert summary, redacted transcript)

**Refs:** GAP-20 · INF-033 · ANA-013 · todo.md Track B Phase 3 (→ OB Phase 1 ops)

**Why it matters.** Last-error text + `smtp_code`/`smtp_stage` exist, but no EHLO capability dump, TLS cert summary, or conversation transcript — operators debug relay failures blind. Folds into OB Phase 1 operations.

**Dependencies.** None.

**Acceptance criteria** (condensed from doc 04 §2.4 GAP-20):
- `TestConnection` stores EHLO capabilities + peer-certificate info; failed messages optionally retain a redacted transcript behind a settings flag; transcripts are PII/credential-redacted and retention-covered.

### P3-2 · GAP-21 — Message tagging/correlation (tags on emails)

**Refs:** GAP-21 · INF-032 · todo.md Track B Phase 3

**Why it matters.** Only caller `ExternalID` correlation exists (`service.go:343`); no free-form tag model for grouping/filtering/analytics (Postal parity).

**Dependencies.** None.

**Acceptance criteria** (condensed from doc 04 §2.4 GAP-21):
- `tags text[]` on `emails` accepted at send, filterable in logs endpoints, groupable in analytics breakdowns; array filtering stays indexed; retention unchanged.

### P3-3 · GAP-22 — Ship an operator CLI against the REST API

**Refs:** GAP-22 · API-017 · todo.md Track B Phase 3

**Why it matters.** The binary ships only `server`/`worker`/`doctor` subcommands — no operator CLI (`send`, keys, domains) against the 340-route API (Postal/BillionMail/Wraps parity).

**Dependencies.** None; keep the single-binary ethos (same image, `cmd/posta-cli`).

**Acceptance criteria** (condensed from doc 04 §2.4 GAP-22):
- Send / API-key / domain CRUD subcommands work with `psk_` key auth; documented in the SDK overview.

### P3-4 · GAP-23 — Verify SDK completeness (PyPI/NuGet packages + route coverage)

**Refs:** GAP-23 · API-008 · todo.md Track B Phase 3

**Why it matters.** Six SDK languages are documented but the Python (`posta`) and .NET (`Posta`) packages are unconfirmed on their registries, and coverage of the newest routes is unverified (audit-H §5 item 1).

**Dependencies.** Coordinate with GAP-14's signature-header change (SDK verification helpers must update in lockstep).

**Acceptance criteria** (condensed from doc 04 §2.4 GAP-23):
- PyPI/NuGet packages install (or are published); docs state the coverage level vs the current route set; a CI/docs-side coverage check exists (resolves NV G-H NV-1/NV-3).

### P3-5 · GAP-24 — Ship an MCP server over the REST API

**Refs:** GAP-24 · API-016 · todo.md Track B Phase 3

**Why it matters.** Zero MCP references (grep clean) while competitors expose AI-agent surfaces — an emerging differentiator that is cheap given the API surface.

**Dependencies.** API keys/scopes (exist).

**Acceptance criteria** (condensed from doc 04 §2.4 GAP-24):
- A thin MCP wrapper exposes list/send/query with API-key auth; write/admin scopes excluded by default; no new auth surface is created.

### Marketing tail

### P3-6 · GAP-35 — Preference center (multi-list subscription management)

**Refs:** RF-F073 · GAP-35 · CON-016 · todo.md Track B Phase 3

**Why it matters.** Only a forward-reference comment exists (`suppression.go:26`); recipients can't manage list memberships/language from a hosted page. Consent-UX maturity beyond one-click; pairs with the D8 Suppressions nav promotion.

**Dependencies.** **GAP-25 (P1-12)** — builds on DOI's consent foundation and the hosted unsubscribe pages (`tracking_pages.go`); list-scoped opt-out tables exist.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-35):
- A token-authenticated preferences view lists public lists; the recipient can opt out of specific lists and back in; changes write the existing opt-out tables and reflect in campaign eligibility.
- HMAC token scope limits toggles to that recipient; page CSP matches the `/t/v` hardening.

### P3-7 · GAP-37 — UTM auto-tagging of campaign links

**Refs:** RF-F061 · GAP-37 · CAM-015 · todo.md Track B Phase 3

**Why it matters.** No UTM rewriting in the tracking pipeline (verified absent); only the reference workflow platform ships it, so parity value is modest but the effort is small and contained.

**Dependencies.** Click-rewrite pipeline (`tracking.go:65-155`).

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-37):
- Campaign-level `utm_params` JSONB appended to outbound hrefs before tracking rewrite, skipping `/t/`, mailto, tel (same skip list); disabled by default; preview shows tagged links.

### P3-8 · GAP-36 — Complete template authoring: preview text, Markdown part, media library (verify-first gate)

**Refs:** GAP-36 · TPL-016 · TPL-003 · TPL-022 · todo.md Track B Phase 3 (verify-first gate)

**Why it matters.** Preheader support is unverified (TPL-016 NV), no Markdown authoring exists, and the template media-library surface was never audited (TPL-022 NV). **Verify before build** (doc 05 hard gate): TPL-016/TPL-022 may already partially exist.

**Dependencies.** Verification first (doc 04 §5 TPL-016/TPL-022 rows state the resolving evidence); html2text and blob store exist.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-36):
- Verification results recorded for TPL-016/TPL-022 before any build; the NV rows close.
- A `preheader` field renders as hidden preview text; optional `format: markdown` localization renders through a sanitized Go markdown pipeline with auto-generated text part; `POST /media` upload + picker in both editors with per-workspace blob keys.

### P3-9 · GAP-38 — Newsletter archive and publications

**Refs:** GAP-38 · CAM-012 · CAM-013 · todo.md Track B Phase 3

**Why it matters.** Web-view tokens are per-recipient and expiring — not a public archive — and campaigns are flat with no publication grouping. Real feature for newsletter use cases; the core design risk is subscriber-variable leakage.

**Dependencies.** Web-view rendering exists (`tracking_handler.go:345-371`) but archive pages must be recipient-independent.

**Acceptance criteria** (condensed from doc 04 §2.5 GAP-38):
- A `publication` grouping + per-campaign public flag; published campaigns render at a public URL without per-recipient tokens (subscriber variables substituted with archive metadata, never `.Subscriber`); unpublished campaigns 404; unsub links stripped; noindex + CSP as `/t/v`.

### P3-10 · GAP-40 — Device/geo breakdowns (ANA-015)

**Refs:** GAP-40 · ANA-015 · todo.md Track B Phase 3 · 04-feature-gap-analysis.md §2.6

**Why it matters.** No cohort/geo/device breakdowns in campaign analytics (explicit audit-F gap). Privacy trade-offs need explicit decisions — coordinate with GAP-15's posture.

**Dependencies.** GAP-15 decision (P1-10) for the privacy baseline.

**Acceptance criteria** (condensed from doc 04 §2.6 GAP-40):
- `ua_device`/`ua_client` parsed at record time on `tracking_events`; campaign analytics show open/click by device class and client; geo via optional local mmdb; toggle-able; no raw-IP retention beyond current windows.

### Admin tail

### P3-11 · GAP-17 — Global registration control toggle (SEC-017)

**Refs:** GAP-17 · SEC-017 · todo.md Track B Phase 3 (admin tail) · 04-feature-gap-analysis.md §2.3

**Why it matters.** No instance-wide open/closed registration toggle — only the verified-email gate on workspace create/invites/keys exists. Standard self-host control (SendPortal/Keila/MailCheap parity).

**Dependencies.** None.

**Acceptance criteria** (condensed from doc 04 §2.3 GAP-17):
- Toggle off → the registration endpoint returns 403 with a clear error; existing users, invitations, and API-key creation unaffected; default preserves current behavior; admin Settings UI exposes it.

### P3-12 · RF-F015 — Complete admin impersonation (guardrails, audit, UI flag)

**Refs:** RF-F015 · GAP-46 · SEC-015 · R-20 · commit `0f52636` · commit `346eb67`

**Why it matters.** Impersonation core landed in `346eb67` (handler, routes, dashboard affordance; 954+ line handler test) with the duplicate stop-audit fix in `0f52636`. The registry keeps it In Progress: GAP-46's guardrail scope is the completion bar — impersonation is the feature most able to weaken the tenancy story if the guardrails slip.

**Dependencies.** Sessions/JTI and audit events (exist).

**Acceptance criteria** (condensed from doc 04 §2.8 GAP-46):
- Impersonation sessions are visually flagged in the UI, audited with actor+target (start and stop, exactly once — the `0f52636` fix class), expire ≤60 minutes, cannot reach `/admin/*`, and honor per-user opt-out.

### P3-13 · GAP-47 — Add antivirus (ClamAV) hook to the inbound pipeline

**Refs:** GAP-47 · SEC-011 · todo.md Track B Phase 3 (→ OB Phase 2 malware scanning) · upcomming-convo.md §8 malware row

**Why it matters.** The inbound pipeline has no AV hook (grep clean); Postal's ClamAV integration is the exemplar. Meaningful for inbound-heavy deployments; becomes part of the OB Phase 2 malware boundary.

**Dependencies.** Inbound parse pipeline (`inbound/service.go`); folds into OB Phase 2 (ADR-006).

**Acceptance criteria** (condensed from doc 04 §2.8 GAP-47):
- Setting-gated clamd TCP scan post-parse; an EICAR test attachment is quarantined and flagged (blob retained, message flagged spam, triage UI reused); scan disabled by default; scanner outage fails open with a security event; ingest latency budgeted.

### Docs & observability tail

### P3-14 · GAP-52 — Ship a Grafana sample dashboard

**Refs:** GAP-52 · OBS-007 · todo.md Track B Phase 3

**Why it matters.** `/metrics` is the integration surface (20+ families, default-off) but no sample dashboard ships — one JSON file of real operator polish (Hyvor Relay ships one).

**Dependencies.** Metrics (exist).

**Acceptance criteria** (condensed from doc 04 §2.10 GAP-52):
- A bundled dashboard JSON imports clean against a running instance; panels map 1:1 to documented families (queue depth/latency, send/fail rates, webhook outcomes, bounces by type); docs cover enabling `POSTA_METRICS_ENABLED`.

### P3-15 · GAP-53 — Add runtime log viewer and resource history

**Refs:** GAP-53 · OBS-010 · OBS-012 · todo.md Track B Phase 3

**Why it matters.** No stdout/app-log viewer, and admin SSE `system.status` carries NumGoroutine+MemStats only — no CPU/disk/history. Convenience (deployments normally own aggregation), but cheap.

**Dependencies.** None.

**Acceptance criteria** (condensed from doc 04 §2.10 GAP-53):
- A bounded, memory-capped, level-filtered ring buffer of slog records streams via admin SSE; resource history charted for 24h; no secrets logged.

### Correctness register

### P3-16 — Close the open NEEDS_VERIFICATION register (correctness/ops items)

**Refs:** 04-feature-gap-analysis.md §5 (NV register) · todo.md Sources (04 §5)

**Why it matters.** The NV register holds the suite's unverified correctness/ops claims — e.g. DKIM signing intent on the shared-server path (A-B NV-3), queue-weight semantics under load (A-B NV-6, risk R11), gradual-send pacing accuracy (C-D NV-2), `scheduled_at` update semantics (C-D NV-3), CSV import memory profile (C-D NV-4), TaskID dedup vs retention interplay (C-D NV-6), active-version delete guard (E-F NV-2), gauge call-site (E-F NV-3), worker `/readyz` under drain (I-J NV-3), SSO enforcement depth (I-J NV-4), security-engine fail-open scope (I-J NV-5), and the provider-events/replay re-audit at current HEAD (I-J NV-6). Unverified claims eventually become silent bugs or false confidence.

**Dependencies.** Folded rows (→ GAP-nn) resolve automatically when their gap's acceptance criteria are met; open rows need the listed evidence.

**Acceptance criteria:**
- Every open row in doc 04 §5 has its listed resolving evidence produced (a test, a trace, a benchmark, or an explicit recorded decision) or is consciously retired with a note.
- I-J NV-6 closes via a route→handler→test re-audit of `provider_events`/webhook replay at the current HEAD (not the research baseline).

### Frontend consolidation

### P3-17 — Frontend consolidation batch: audit-UI scope switcher, MessageDetail merge, ChangePassword removal, Suppressions nav (D2/D5/D6/D8)

**Refs:** D2 · D5 · D6 · D8 · todo.md D1–D9 · 04-feature-gap-analysis.md §4

**Why it matters.** Four verified UI duplications: three audit-log surfaces over one `events` table (D2); two read models over `emails` with retry and timeline split across them (D5); two password-change flows (D6, the router already redirects); suppression — a first-class consent artifact — discoverable only inside the Bounces view (D8).

**Dependencies.** None beyond UI work; D5 must preserve the `/emails` API (it serves the SDK) and the DevList admin surface.

**Acceptance criteria** (doc 04 §4):
- **D2:** one audit UI with workspace/deployment/platform scope tabs enforcing all three permission gates; backend routes kept during a deprecation window; CSV export stays on the deployment surface.
- **D5:** retry + status actions merged into MessageDetail; Emails views retired after a transition release; Bounces remains a filtered lens.
- **D6:** `ChangePassword.vue` deleted with the redirect kept; one E2E test on the Profile flow.
- **D8:** Suppressions promoted to its own nav entry (or a consent section pairing with GAP-35); the Bounces tab stays.

### P3-18 — Document intentional dualities (analytics split D4, tracking/email events D7, admin SSE channels D9)

**Refs:** D4 · D7 · D9 · todo.md D1–D9 · 04-feature-gap-analysis.md §4

**Why it matters.** Three duplications are *correct by design* and must be documented so future contributors don't "fix" them: workspace vs admin analytics share one repository with scoped WHERE clauses (D4 — deliberate ops/workspace split); `tracking_events` and `email_events` have different dedup regimes answering different questions (D7 — merging would break campaign time-series and retention); three admin SSE channels have overlapping connection management but are acceptable (D9).

**Dependencies.** None — documentation only; D7 explicitly says "do not merge in the roadmap window; revisit only if analytics consolidation (GAP-40) forces it."

**Acceptance criteria** (doc 04 §4):
- The analytics split is documented in the API docs; the `tracking_events`/`email_events` dedup semantics are documented in the API docs and retention matrix; the admin SSE note exists (multiplex only if the channels are touched anyway).

### P3-19 — Evaluate JMAP and advanced integrations on validated demand

**Refs:** upcomming-convo.md §4 JMAP, §7 Phase 6 · todo.md Track A Phase 6 · INF-003

**Why it matters.** JMAP appears in the exclusion discussion, not as a verified requirement — Option B says "keep JMAP as a separately evaluated capability… Do not silently treat it as already researched or required" (upcomming-convo.md §4). Phase 6 revisits it on validated demand, alongside other advanced integrations.

**Dependencies.** OB Phases 3–5 landed (mailbox foundation to serve); a demand signal.

**Acceptance criteria:**
- A written evaluation (demand evidence, component candidates, license check, integration cost against `COMPONENT-MATRIX.md` criteria) exists before any JMAP build; no implementation without that evaluation.

---

## Cross-cutting dependency map

Structural hard edges from doc 05 §8 and todo.md, in one place:

| Edge | Meaning |
|---|---|
| P0-2 / P0-3 (GAP-02 residual) → P1-7 (GAP-06), P2-21 (GAP-07) | Signal integrity precedes auto-pause gates and bounce analytics — no pause/analytics on accept-only signals |
| P1-5 (GAP-04 `Sender` seam) → P1-4 (MTA adapter) + ESP-specific value | The seam is the precondition for the Option B MTA and for API-mode ESP features (its other historic consumer, per-key limits GAP-44, already landed in `f5f0e92`) |
| P2-4 (GAP-08 DNS health) → P2-2 (GAP-10 policy hardening) | Strict enforcement is only fair once drift re-verification keeps records healthy |
| P1-13 (GAP-26 preview) → P2-14 (GAP-34 blocks/slots) | Block/slot overrides must render in the preview GAP-26 builds |
| P1-12 (GAP-25 DOI) → P2-16 (GAP-30 forms), P3-6 (GAP-35 preference center) | Consent foundation before capture and preference management |
| P0-5 (2FA enforcement) ↔ P1-1 (recovery codes) | Enforcement presumes a working lockout escape hatch |
| P1-3 (OB Phase 0) → P1-4/P1-15/P1-17/P1-18/P1-19/P1-20/P2-30/P2-31 | Every Track A build phase consumes approved ADRs and the migration plan |
| P1-17 (mailbox storage) → P1-18 (IMAP/POP3) → P1-19 (webmail) | Storage → protocols → UI, deliberately in that order |

**Product-decision gates:** P1-10 (GAP-15 — decide tracking-toggle intent before code) and P3-8 (GAP-36 — verify TPL-016/TPL-022 before build).

## Explicitly not gaps

The exclusion lists stay governing: deliberately-not-proposed registry entries and verified-absent-everywhere features (doc 04 §6.1–6.2, doc 05 §9, todo.md "Not recommended — keep dead"). Option B reinstates exactly one set — direct-to-MX/MTA, IP pools, bounce mailbox/VERP, PTR guidance, full mail server/IMAP/JMAP/webmail, automated DNS provisioning, per-domain TLS/ACME — as Track A candidates (tracked above as P1-4, P1-15, P1-17–P1-20, P2-30); everything else stays dead, including DMARC rua's original exclusion only insofar as it was **superseded by commit `f5f0e92`** (tracked as P2-3), not resurrected by decision.
