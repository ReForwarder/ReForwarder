# ReForwarder — Operational Backlog

> Generated **2026-11-06** from `todo.md` (master todo, Track A/B), the `docs/product-roadmap/` suite (research dated 2026-11-05), and `upcomming-convo.md` (Option B decision). Workflow statuses reconciled against HEAD `0f52636` ("Fix WebAuthn nil-user abort path and duplicate impersonation stop audit"). Test gate: the audit run's `go test ./... -count=1` PASS was captured on a working tree between `f5f0e92` and `0f52636` (`docs/project/FEATURE_AUDIT.md` §9) — the gate has **not** been re-run at `0f52636` itself (deferred to the next sync run per `STATUS_HISTORY.md`). Every open item from todo.md Track B (GAP-01…53, residuals and opens) and the Track A near-term programme (Phase 0 re-audit, ADR-001…009, the `docs/architecture/` deliverables, Phase 1–6 milestones) appears **exactly once** below.

## Conventions

- **Stable IDs.** Tasks anchored to a registry feature reuse its permanent `RF-Fnnn` id. Tasks of the Option B architecture programme use the `RF-Bnnn` series (RF-B101…, defined here, never renumbered); this series is backlog-local — it is **not** part of the audited `RF-F` registry (`RF-F001`–`RF-F105`) and does not consume or alter `RF-F` numbering (per `AGENTS.md` §2). Tasks anchored only to a roadmap-suite id carry that id verbatim (`GAP-nn`, `Dn`, `A-B NV-nn` / `G-H NV-nn` — all permanent per `todo.md` §header). `GAP-nn`, `D1–D9`, registry IDs, architecture risks `R1–R16` and repo tracker IDs `R-01…R-28` are never renumbered; note `R4` (delivery-signal risk) ≠ `R-04` (tracker: provider-event ingestion).
- **Workflow status.** `To-do` · `In Progress` · `In Review` — matching the registry view. To-do tasks live in the priority sections (P0–P3, priority per todo.md Track B phases and the Option B milestone priorities); **In Progress and In Review tasks appear only in the final section** (with their priority recorded per task) so no task is listed twice.
- **Re-verification rule.** Audit statuses are historical (research baseline `a6f1681` → `6d2d7c0`, 2026-11-05). Before starting any item, re-verify its state at HEAD — `todo.md` §0. Where this file says *verified at HEAD*, the check was run on 2026-11-06 at `0f52636`; file:line evidence marked *audit* is quoted from the roadmap suite as of its stated baseline.
- **Decision gates.** GAP-15 is a **product-decision gate** (decide intent before code); GAP-36 is **verify-first** (resolve its NV items before building); the Option B Phase 0 programme is **architecture-only** (no production code, schema, dependency or destructive changes; cite exact files/symbols; label facts vs recommendations vs assumptions).

**Counts:** P0 × 5 · P1 × 34 · P2 × 24 · P3 × 18 · In Progress × 12 · In Review × 0 — 91 tasks. (2026-11-07 P3-5 misc wave: RF-F102 closed — its dead client methods were removed, see the closed list; RF-F104's duplication half landed and the residual moved to In Progress. 2026-11-07 OB-3 wave W1: RF-B120 To-do → In Progress — the entry stays in its P1 section per the standing OB-2 precedent of annotating the phase entry in place. 2026-11-08 OB-4 wave W1: RF-B121 To-do → In Progress — seam wave landed, servers pending; annotated in place per the same precedent. 2026-11-08 OB-4 waves W2/W3/W4: RF-B121 stays In Progress — the IMAP/POP3 server packages, the four-port gated listener wiring with graceful shutdown, the POSTA_IMAP_*/POSTA_POP3_* config + validation, security-engine limiter integration and audit-event emission landed (W4 also repaired two W2 defects and added the missing IMAP test suites); UIDVALIDITY-driven sync and the compatibility matrix remain.)

**Closed items intentionally absent (do not re-create):** RF-F102 dead admin API client methods (removed 2026-11-07, CA-02 wave — `listApiKeys`/`revokeApiKey`/`listEmails` deleted from `web/src/api/admin.ts` with their type imports; KNOWN_DRIFT allowances removed from `web/src/api/admin.spec.ts`; no backend routes exist and none are wanted — key admin lives in workspace scope per RF-F010) · GAP-39 timezone sending (done, `6d2d7c0`) · GAP-44 per-API-key rate limits (RF-F011 Done, landed `f5f0e92`) · GAP-50 backup/restore (RF-F095 Done, `pg_dump` CronJob + restore runbook, `7f445f2`) · GAP-02 core / GAP-07 core / GAP-18 replay / GAP-28 campaign half (landed `6d2d7c0`; only residuals tracked) · GAP-01 atomic-claims half (landed `7f445f2`; only cancel + reconcile coverage tracked). Already-delivered record: `todo.md` §"Already delivered", `05-implementation-roadmap.md` §7.

---

## P0 — Trust & correctness hardening (Phase 0)

Five items, fully parallel, no intra-phase edges. Source: `todo.md` Track B Phase 0, `05-implementation-roadmap.md` §3, §8.

### RF-F048 · Fix transactional scheduled sends end-to-end: cancel endpoint + reconcile coverage (GAP-01)

- **Priority:** P0
- **Category:** delivery-queue
- **Workflow status:** To-do
- **Refs:** GAP-01 · INF-014 · tracker R-26 · architecture risk R5 · `04-feature-gap-analysis.md` §2.1 · `05-implementation-roadmap.md` §3 · folds into Option B Phase 1 (RF-B118)
- **Description:** Scheduled *transactional* emails are neither cancellable nor covered by reconciliation: `queue_reconcile` claims `status='queued'` rows only (verified at HEAD — `internal/cron/jobs/queue_reconcile.go:56`, comment and CAS claim both `queued`-scoped), and no cancel endpoint exists for a scheduled email (verified at HEAD — no `DELETE /emails/{id}` or `/cancel` route). A Redis loss strands `scheduled` rows forever, and senders cannot unsend future mail in the core money path. Atomic send/campaign claims (CAS transitions, TaskID dedup, `FOR UPDATE SKIP LOCKED` batch claims) already landed in `7f445f2` (tracker R-26); this task closes the remaining cancel + reconcile-coverage half.
- **Acceptance criteria:**
  - [ ] `DELETE /emails/{id}` (or `/cancel`) cancels an email only while `status='scheduled'`, using the CAS transition pattern from `campaign_handler.go:391-464` (audit); the asynq task is deleted on cancel; the row ends `cancelled`
  - [ ] Cancel is workspace-scoped (existing scope enforcement) and audited
  - [ ] `queue_reconcile` additionally claims `scheduled` rows past `scheduled_at` with deterministic-TaskID re-enqueue (extends the existing queued-row pattern)
  - [ ] A scheduled row whose Redis task is lost re-enqueues within one reconcile cycle — tested, mirroring `queue_reconcile_test.go`
  - [ ] CAS race coverage between cancel and the scheduler claim; cancel-after-claim is a no-op, not a double send
  - [ ] Cancel affordance on scheduled emails in the dashboard emails views
  - [ ] `go test ./...` passes; no migration required (status column exists)
- **Dependencies:** none (extends the `a6f1681` reconcile pattern, tracker R-03; the `7f445f2` atomic-claims half already landed).
- **Evidence / relevant files:** `internal/cron/jobs/queue_reconcile.go` (verified at HEAD: `queued`-only claim) · `internal/services/queue/producer.go:47-133` (audit: deterministic TaskIDs, `EnqueueEmailSendAt`) · `internal/handlers/campaign_handler.go:391-464` (audit: CAS cancel pattern) · `docs/docs/email-sending/single-email.md` (status lifecycle).

### RF-F099 · Emit and surface true delivered state: `email.delivered` emission, delivered analytics, dead UI paths (GAP-02 residual)

- **Priority:** P0
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-02 (residual) · ANA-003 · ANA-005 · `todo.md` Track B Phase 0 · `upcomming-convo.md` §8 rule 3 ("never report successful delivery just because a message entered a queue") · architecture risk R4
- **Description:** Provider-event ingestion (`6d2d7c0`) made a provider `delivered` event produce `EmailEventDelivered` with write-once `delivered_at`, but the signal is not plumbed everywhere: the emailhistory recorder does not fire the outbound webhook dispatcher, so `email.delivered` / `email.complained` never reach customer endpoints; delivery-rate analytics still measure the SMTP-accept proxy; MessageHistory ships a `delivered` filter "that will simply never match" (audit-F). This task finishes the read/emission half of the delivered/complained signal (the legacy-webhook parity half is RF-F053, In Progress).
- **Acceptance criteria:**
  - [ ] Outbound webhooks `email.delivered` and `email.complained` are emitted for provider-sourced events, following the 11-event registry pattern (`emailhistory` recorder wired to the dispatcher)
  - [ ] Delivery-rate analytics distinguish SMTP-accept from true delivered (a delivered webhook moves the delivery rate off the accept proxy — acceptance test pins it)
  - [ ] Automation triggers can consume delivered/complained events (integration test)
  - [ ] Dead delivered UI paths fixed: the MessageHistory `delivered` filter matches real events; delivered state renders where the UI expects it
  - [ ] Signature verification / replay posture of the provider-event path unchanged (verify with existing `provider_events_test.go` suite)
  - [ ] `go test ./...` passes
- **Dependencies:** none upstream (provider-event core landed `6d2d7c0`). Blocks honest delivery-rate reporting (risk R4) and feeds RF-F057; together with RF-F053 it unblocks GAP-06.
- **Evidence / relevant files:** `internal/services/provider_events/` (audit: service.go:213 `EmailEventDelivered`) · `internal/services/webhook/dispatcher.go` (audit: dispatch path; the recorder does not call it) · `internal/handlers/webhook_docs.go` (11-event registry) · `05-implementation-roadmap.md` §3 GAP-02 (residual items 3–4).

### GAP-11 · Add a server-side editor role gate to automation write and event-ingest routes

- **Priority:** P0
- **Category:** auth-org
- **Workflow status:** To-do
- **Refs:** GAP-11 · SEC-002 · AUT-003 · architecture risk R1 · `04-feature-gap-analysis.md` §2.3 · `05-implementation-roadmap.md` §3
- **Description:** The automation route group carries only `auth + workspace` middleware — verified at HEAD (`internal/routes/automation_routes.go:35`); the repo's own `workspace_rbac_test.go:101` notes "any member — including a viewer —" passes, and commit `c0408dc` gated the UI only. A viewer-role session can create/activate automations and ingest events server-side (intra-tenant privilege escalation, no cross-tenant reach). One-middleware fix on the automation write subgroup and `POST /automations/events`, aligned with the existing `requireWorkspaceScope` map for API keys.
- **Acceptance criteria:**
  - [ ] `RequireWorkspaceRole(models.RoleEditor)` added to the automation write subgroup and `POST /automations/events`; no automation route bypasses the subgroup (verified by route inventory)
  - [ ] API-key scope requirements aligned with the existing `requireWorkspaceScope` map
  - [ ] Viewer JWT gets 403 on `POST /automations`, activate/pause/duplicate/delete, and `/automations/events`; editor+ passes — regression tests pinned in the `workspace_rbac_test.go` pattern (`:103,140,159`)
  - [ ] No API shape change (403 is the only observable difference for viewers)
  - [ ] Release note: viewers/scripted viewer tokens lose automation write access (deliberate breaking change)
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/routes/automation_routes.go:35-37,186-201` (verified at HEAD: `r.mw.auth, r.mw.workspace` only) · `internal/storage/repositories/workspace_rbac_test.go:101` (audit) · `internal/middlewares/middleware.go` (RequireWorkspaceRole).

### RF-F006 · Enforce the `two_factor_required` policy setting at login (GAP-12 enforcement half)

- **Priority:** P0
- **Category:** auth-org
- **Workflow status:** Feature Done (2026-11-07, Wave P3-4)
- **Refs:** RF-F006 · GAP-12 · SEC-005 · tracker R-10 · architecture risk R2 · `04-feature-gap-analysis.md` §2.3 · `05-implementation-roadmap.md` §3
- **Description:** The seeded `two_factor_required` setting was dead code (seeded, exposed by a getter, zero consumers at the audit baseline). Landed (Wave P3-4): at login, after password verification, an account WITHOUT any second factor under an enabled policy completes sign-in carrying a forced-enrollment marker `two_factor_enrollment_required` and the dashboard routes it to the 2FA setup card (`/profile?setup=2fa`); accounts WITH 2FA keep the unchanged `requires_2fa` TOTP step; admins are NOT exempt; the existing 2FA, recovery-code (RF-F005), WebAuthn (R-19) and impersonation flows are preserved (all pre-existing auth tests pass unchanged).
- **Acceptance criteria:**
  - [x] With `two_factor_required` on, an un-enrolled account is forced into enrollment after password success. **Design deviation, documented:** the session IS issued together with the marker (the criterion's letter said "no session is issued") — Setup2FA requires an authenticated session, so refusing it would dead-end enrollment into a lockout (the Done bar counts dead-end flows as not Done). Evidence: `internal/handlers/user_handler.go:129-134,572-648,654-687`; marker on AuthResponse `:199-210`; profile marker `:709-725`; passkey path `:953-981`; dashboard `web/src/views/auth/Login.vue` (`requires2FASetup` → `/profile?setup=2fa`) + `web/src/stores/auth.ts`.
  - [x] With the setting off, behavior is identical to today (regression cases in the matrix). Evidence: `internal/handlers/login_enforcement_test.go` TestLogin_TwoFactorRequired_Matrix, policy-off cases.
  - [x] Enrollment flow cannot lock users out: the account can always reach Setup2FA (session live) and un-enroll + re-enroll freely; recovery codes apply to the 2FA login step, which is unchanged. Evidence: `internal/handlers/login_enforcement_test.go` (recovery-code login path covered by the pre-existing suite, still passing).
  - [x] Admin reset flow keeps its audit trail — the admin reset path is untouched by the wave; enforced-2FA admins are treated like any other account by the policy (no exemption). Evidence: TestLogin_TwoFactorRequired_Matrix "policy on, admin without 2FA: no exemption, marker present".
  - [x] Behavioral change release-noted + tests for the paths. Evidence: `internal/handlers/login_enforcement_test.go` (8-case matrix: enforcement marker, admin no-exemption, policy-off regression, requires_2fa unchanged, valid-code login) + TestLogin_EnrollmentMarkerInProfile; full-module `go test ./cmd/... ./internal/... -count=1` PASS (dockerized golang:1.27, 2026-11-07).
- **Residual (tracked):** the middleware layer does not yet refuse API calls from a session whose account owes enrollment — documented at `internal/handlers/user_handler.go:566-571`; the dashboard holds the enrollment banner via the profile marker (`two_factor_enrollment_required`) until 2FA is enabled.
- **Dependencies:** RF-F005 (In Progress — recovery codes) as the lockout path; TOTP service exists (`internal/services/twofactor/totp.go:27-42`, audit).
- **Evidence / relevant files:** `internal/handlers/user_handler.go` (policy check, login, authResponse, profile, passkey) · `internal/handlers/login_enforcement_test.go` · `web/src/views/auth/Login.vue`, `web/src/stores/auth.ts`, `web/src/views/auth/Profile.vue` · `internal/services/seeder/settings.go:52`, `internal/services/settings/provider.go:134` (the setting's seed and getter are unchanged; they now have a consumer).

### GAP-03 · Add multi-replica cron leader election (Redis lease)

- **Priority:** P0
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-03 · OBS-019 · architecture risk R3 · `04-feature-gap-analysis.md` §2.1 · `05-implementation-roadmap.md` §3 · resolves audit C-D NV-1 / I-J NV-2
- **Description:** All 10 cron jobs run on every server replica — `internal/cron/manager.go` has only in-process mutexes; a repo-wide grep at HEAD finds no `SetNX`/leader-lease anywhere in `internal/cron/`. Duplicate customer-facing sends (`bounce_alert`, `daily_report`, `message_digest`) are silent correctness bugs in the documented multi-replica topology (`compose.yml` worker split). Fix: a Redis `SET NX PX` leader lease around the cron manager tick (pattern precedent: form nonces, `form_ingest_handler.go:77,131`, audit), keeping idempotent job design, plus a Redis dedup key guarding `bounce_alert`/`daily_report` per recipient.
- **Acceptance criteria:**
  - [ ] Redis `SET NX PX` leader lease wraps the cron manager tick; lease key instance-scoped; no new auth surface
  - [ ] Integration test with two managers sharing Redis: exactly one replica executes `campaign_scheduler` / `bounce_alert` / `daily_report` per tick
  - [ ] Lease-loss failover completes within one interval (tested)
  - [ ] `bounce_alert`/`daily_report` per-recipient dedup key guards against lease-edge double-sends
  - [ ] Single-replica behavior unchanged; no migration; `go test ./...` passes
- **Dependencies:** none (Redis already universal).
- **Evidence / relevant files:** `internal/cron/manager.go` (verified at HEAD: in-process mutexes only, no lease) · `internal/handlers/form_ingest_handler.go:77,131` (audit: nonce/lease pattern precedent) · `compose.yml` (multi-replica topology).

---

## P1 — Signals, sender value & the Option B architecture programme (Phase 1)

Track B P1 items (6 parallel tracks) plus the Track A near-term programme. The Phase 0 architecture programme (RF-B101…RF-B117) is architecture-only and must precede the Track A build milestones (RF-B118…).

### RF-B101 · Option B Phase 0 — repository re-audit (evidence-backed current-state matrix)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §7 Phase 0, §12 (Mandatory Stage 0) · `todo.md` Track A Phase 0 · resolves audit I-J NV-6 (re-audit request)
- **Description:** Inspect the current branch, HEAD and working tree, and reconcile all seven audit documents with the actual code at HEAD — an evidence-backed current-state matrix covering everything that landed since the research baseline (at minimum `7f445f2`, `f5f0e92`, `346eb67`, `0f52636`, plus any uncommitted work preserved per todo.md §0.3). Verify existing SMTP relay, inbound receiver, queue, DKIM, provider-event ingestion and webhook behavior at HEAD. Record current migrations, message models, event contracts and tenant boundaries. Identify what is reusable versus what must be isolated. The registry must not be used as proof of current implementation. This is the mandatory first step of the Option B programme; every other RF-B task consumes its matrix.
- **Acceptance criteria:**
  - [ ] Evidence-backed current-state matrix produced, reconciling all seven `docs/product-roadmap/` documents with HEAD, including post-audit commits and uncommitted work
  - [ ] SMTP submission, outbound SMTP, inbound SMTP, DKIM, provider events, webhook processing, queue reconciliation, campaign scheduling and multi-tenancy verified at HEAD with file:symbol citations
  - [ ] Current migrations, message models, event contracts and tenant boundaries recorded
  - [ ] Reusable vs must-be-isolated components identified
  - [ ] Facts, recommendations and assumptions labeled; audit claims marked confirmed, corrected or stale
  - [ ] Architecture-only rules honored: no production-code changes, no schema changes, no new dependencies, no destructive commands, no commits/pushes of code
- **Dependencies:** none. Blocks RF-B102…RF-B117 (all ADRs and architecture docs cite its matrix).
- **Evidence / relevant files:** `docs/product-roadmap/03-existing-feature-audit.md` (baseline audit) · `docs/product-roadmap/04-feature-gap-analysis.md` §5 (I-J NV-6) · `todo.md` §0 (reconciliation context) · `docs/implementation/ROADMAP_IMPLEMENTATION.md` (tracker).

### RF-B102 · ADR-001 — Outbound MTA (direct-to-MX + upstream relay modes)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-001) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-001-outbound-mta.md`
- **Description:** Compare established MTA options and recommend an integration strategy for outbound transport. Must cover: direct-to-MX and upstream relay modes, transport queues, retries, rate limits, IP pools, DKIM signing, TLS, remote-server deferrals, delivery events, and operational administration. Must build on the existing relay path (SMTPSender, deterministic TaskIDs, retry layers) rather than bypass it; the GAP-04 `Sender` seam (below) is the in-monolith predecessor of this adapter.
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-001-outbound-mta.md` written with selected and rejected candidates plus reasons
  - [ ] Covers all ten scopes listed in the description (modes, queues, retries, rate limits, IP pools, DKIM, TLS, deferrals, delivery events, ops admin)
  - [ ] Integration strategy states how the existing relay path and `Sender` seam are preserved
  - [ ] Facts, recommendations, assumptions and unresolved decisions labeled; licenses of candidates verified (no AGPL/BSL code copying)
- **Dependencies:** RF-B101.
- **Evidence / relevant files:** `docs/product-roadmap/06-architecture-impact.md` §3.5 (risk R15 provider ceiling), §4.1 · `internal/services/queue/smtp_sender.go` (audit: relay mode, dial timeouts) · `upcomming-convo.md` §4 component table (Outbound MTA row).

### RF-B103 · ADR-002 — Inbound SMTP (acceptance, validation, routing, quarantine)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-002) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-002-inbound-smtp.md`
- **Description:** Define SMTP acceptance, recipient validation, domain routing, aliases, forwarding, catch-all behavior, spam filtering, malware scanning, quarantine, and durable local delivery. Extends the existing inbound go-smtp receiver (`internal/services/inbound/`, webhook ingest RF-F035) toward full MX reception with routing (RF-F040 per-domain routing rules is the control-plane surface); mailbox delivery must stay a separate trust path from authenticated submission (never an open relay).
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-002-inbound-smtp.md` written with the acceptance/validation/routing/quarantine decision
  - [ ] Covers recipient validation before accepting responsibility; explicit unknown-user/alias/catch-all/forward handling; durable local delivery (never accept-then-lose)
  - [ ] Spam/malware filter boundary references ADR-006; distinct from the network security engine
  - [ ] Candidate selection with license verification; facts vs recommendations labeled
- **Dependencies:** RF-B101.
- **Evidence / relevant files:** `internal/services/inbound/service.go`, `internal/routes/inbound_routes.go` (existing receiver) · `upcomming-convo.md` §6 inbound flow requirements · `todo.md` Track A Phase 2.

### RF-B104 · ADR-003 — Mailbox storage (not the transactional Email model)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-003) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-003-mailbox-storage.md`
- **Description:** Compare mailbox storage implementations and decide the backend. Explicitly: do **not** reuse the existing transactional `Email` model as the mailbox data model, and do not auto-migrate transactional records into mailbox storage. Define message identity, MIME storage, folders, flags, quotas, concurrent access, deletion, expunge, search, attachment handling, retention, and backup. Storage backend choice is finalized only after a prototype proves compatibility with the selected IMAP implementation (ADR-004).
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-003-mailbox-storage.md` written with the storage decision and rejected alternatives
  - [ ] All eleven scopes covered (identity, MIME, folders, flags, quotas, concurrent access, expunge, search, attachments, retention, backup)
  - [ ] Explicit statement of the boundary from the transactional `Email` model and the no-auto-migration rule
  - [ ] License verification of candidates; unresolved questions recorded
- **Dependencies:** RF-B101.
- **Evidence / relevant files:** `upcomming-convo.md` §4 protocol decisions (mailbox storage) · `todo.md` Track A Phase 3 · `internal/models/email.go` (the transactional model NOT to reuse).

### RF-B105 · ADR-004 — IMAP and POP3 (implementations, auth, sync, compatibility)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-004) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-004-imap-pop3.md`
- **Description:** Choose IMAP and POP3 protocol implementations and define authentication/authorization tied to ReForwarder identities, TLS, connection limits, mailbox synchronization, and client compatibility. Prefer integrating a mature server with a well-defined mailbox backend (ADR-003) over implementing the protocols from scratch in Go.
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-004-imap-pop3.md` written; implementations selected with reasons and rejected candidates listed
  - [ ] Auth/authorization model tied to ReForwarder identities (sessions, credentials, per-mailbox authorization)
  - [ ] TLS, connection limits, sync semantics and standard-client compatibility plan defined
  - [ ] License verification; facts vs recommendations labeled
- **Dependencies:** RF-B101; pairs with RF-B104 (backend choice).
- **Evidence / relevant files:** `upcomming-convo.md` §4 (IMAP/POP3 row) · `todo.md` Track A Phase 4.

### RF-B106 · ADR-005 — Webmail (frontend boundaries, auth, compose, sessions)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-005) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-005-webmail.md`
- **Description:** Define the webmail frontend boundaries: separate frontend application vs isolated module in the existing Vue dashboard; authentication integration; mailbox APIs; compose/send behavior; attachments; folders; search; session management. Decision only — no webmail UI is built in this programme phase.
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-005-webmail.md` written; frontend boundary decision with rationale
  - [ ] Auth integration, mailbox API surface, compose/send, attachments, folders, search and sessions each addressed
  - [ ] Relationship to the existing Vue dashboard and control-plane auth stated
- **Dependencies:** RF-B101; informed by RF-B104/RF-B105.
- **Evidence / relevant files:** `upcomming-convo.md` §4 (Webmail row) · `todo.md` Track A Phase 5.

### RF-B107 · ADR-006 — Spam and malware (content-filter boundary, distinct from the security engine)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-006) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-006-spam-malware.md`
- **Description:** Define integration boundaries for content filtering, attachment scanning, quarantine, abuse detection, and policy enforcement. Do **not** confuse ReForwarder's existing network security engine (fail2ban-style ban service, detectors — a verified strength) with a mail-content spam filter: preserve the security engine and add a separate content-filtering boundary with different responsibilities.
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-006-spam-malware.md` written; filter/scan engine candidates with license verification
  - [ ] Explicit boundary statement: existing security engine preserved, content filtering separate
  - [ ] Quarantine, scan-record data ownership, and policy enforcement points defined (feeds DATA-OWNERSHIP)
  - [ ] Facts, recommendations, assumptions labeled
- **Dependencies:** RF-B101.
- **Evidence / relevant files:** `internal/services/security/` (existing network security engine to preserve) · `upcomming-convo.md` §4 (Spam filtering decision) · GAP-47 (ClamAV hook — future integration point).

### RF-B108 · ADR-007 — DNS and deliverability (MX, SPF, DKIM, DMARC, PTR, FCrDNS, TLS, warm-up, reputation)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-007) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-007-dns-deliverability.md`
- **Description:** Define MX, SPF, DKIM, DMARC, PTR and forward-confirmed reverse DNS guidance, TLS certificates, domain verification, IP warm-up, and reputation monitoring for the Option B platform (direct-to-MX changes what DNS the platform must own). Builds on the existing verification service (`internal/services/verifier/`) and DKIM lifecycle (RF-F024/RF-F025); PTR guidance and IP pools are reinstated from the relay-only exclusion list by the Option B decision.
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-007-dns-deliverability.md` written covering MX, SPF, DKIM, DMARC, PTR, FCrDNS, TLS certificates, verification, warm-up, reputation
  - [ ] Extension plan for the existing verifier/DKIM services stated (reuse vs extend decisions)
  - [ ] Warm-up and reputation model defined (feeds RF-B123)
  - [ ] Facts, recommendations, assumptions labeled
- **Dependencies:** RF-B101.
- **Evidence / relevant files:** `internal/services/verifier/verifier.go` (audit: presence checks at `:97-101,126-136`) · `todo.md` "Not recommended — keep dead" (reinstated-by-Option-B list) · RF-F023/RF-B124 (guidance depth and automation consumers).

### RF-B109 · ADR-008 — Data and event ownership (source of truth per entity/store)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-008) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-008-data-events.md`
- **Description:** Define the source of truth for tenant configuration, message content, transport attempts, mailbox state, filtering results, and delivery events; plus the durable event layer consumed by analytics, webhooks and the dashboard. Establishes the data-ownership model sketched in upcomming-convo.md §5 (control plane / MTA / mailbox store / filtering / event layer) and the correlation-identity requirement: every cross-component operation has an explicit identifier, and a message is traceable from API acceptance or SMTP submission through transport, inbound routing or mailbox delivery.
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-008-data-events.md` written; source-of-truth table for every entity and store
  - [ ] Event layer contract defined (durable lifecycle events, consumers, correlation identity)
  - [ ] PostgreSQL-remains-control-plane-of-record rule stated; message bodies not assumed into the relational schema
  - [ ] Consistency with the existing `email_events` / `tracking_events` duality (D7) addressed
- **Dependencies:** RF-B101; informed by RF-B102/RF-B104/RF-B107.
- **Evidence / relevant files:** `upcomming-convo.md` §5 (data ownership) · `todo.md` §2.1 (13 domain boundaries + data ownership paragraph) · `04-feature-gap-analysis.md` §4 D7.

### RF-B110 · ADR-009 — Deployment topology (single-node → multi-node, health, upgrades, backup, DR)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §12 (ADR-009) · `todo.md` Track A Phase 0 · delivers to `docs/architecture/ADRs/ADR-009-deployment.md`
- **Description:** Define supported single-node and future multi-node deployments: component health, restart behavior, configuration, upgrades, backups and disaster recovery for the expanded platform (control plane + protocol components). Protocol components must be independently restartable and observable without taking down the dashboard/API. Consumes the Redis HA analysis (GAP-49) and the existing compose topology, backup CronJob (RF-F095) and health probes (RF-F096).
- **Acceptance criteria:**
  - [ ] `docs/architecture/ADRs/ADR-009-deployment.md` written; single-node topology specified and multi-node evolution path defined
  - [ ] Health, restart, upgrade, backup and DR semantics per component defined
  - [ ] Independent restartability/observability requirement for protocol components stated
  - [ ] Facts, recommendations, assumptions labeled
- **Dependencies:** RF-B101; consumes GAP-49 analysis.
- **Evidence / relevant files:** `compose.yml`, `docker/` (current topology) · `internal/services/backup/` + restore runbook (RF-F095) · `upcomming-convo.md` §8 (data durability row).

### RF-B111 · Create `docs/architecture/OPTION-B-ARCHITECTURE.md` (target architecture and system boundaries)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §11 item 1, §12 (Deliverables) · `todo.md` Track A Phase 0
- **Description:** Write the Option B target-architecture document: control plane / data plane split (outbound, inbound, mailbox, security planes over shared infrastructure), boundary rules (control plane owns tenant config/identities/domains/mailbox metadata/credentials/policies/API; protocol services own transport; explicit interfaces + durable events between components; protocol services never depend on private Go app internals; tenant identity consistent across all data-plane services), and the 13 logical domain boundaries (`mailserver`, `mta`, `inbound`, `mailbox`, `mailstore`, `imap`, `pop3`, `mailrouting`, `reputation`, `dnsmanager`, `spamfilter`, `malware`, `webmail`) — logical ownership, not thirteen microservices.
- **Acceptance criteria:**
  - [ ] `docs/architecture/OPTION-B-ARCHITECTURE.md` created with diagrams and the boundary rules verbatim from the decision
  - [ ] All 13 domain boundaries listed with core responsibilities
  - [ ] Preserved strengths (SSRF guard, security engine, tenant isolation, encrypted credentials, queue reconciliation, webhook attempt history, retention/health tooling) stated as build-on, never bypass
  - [ ] Architecture-only rules honored (no production code changes)
- **Dependencies:** RF-B101; synthesizes RF-B102…RF-B110.
- **Evidence / relevant files:** `upcomming-convo.md` §3, §5 · `todo.md` §2 (target architecture + boundary rules + §2.1 domain table).

### RF-B112 · Create `docs/architecture/COMPONENT-MATRIX.md` (candidates, licenses, maturity, trade-offs)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §11 item 3, §12 · `todo.md` Track A Phase 0
- **Description:** Produce the component-selection matrix for every protocol/store component under consideration (MTA, inbound SMTP, mailbox storage, IMAP/POP3, webmail, spam, malware): candidates, licenses, maturity, maintenance activity, security history, integration costs, and trade-offs. Evaluate integration versus native implementation per component. Do not adopt BillionMail blindly (audit flags maintenance/security concerns); use Postal as infrastructure reference, not a port; respect all licenses (no AGPL/BSL source copying — clean-room behavioral research only).
- **Acceptance criteria:**
  - [ ] `docs/architecture/COMPONENT-MATRIX.md` created with a row per component × candidate, license (SPDX, verified), maturity, maintenance, integration cost, trade-offs
  - [ ] Integration-vs-native recommendation per component
  - [ ] License-risk call-outs (Listmonk AGPL, the reference workflow platform BSL v40+, BillionMail caution) recorded
- **Dependencies:** RF-B101; feeds RF-B102…RF-B107.
- **Evidence / relevant files:** `docs/product-roadmap/02-competitor-comparison.md` §2.11 (license families) · `docs/product-roadmap/01-oss-feature-inventory.md` §4.4/§5 · `upcomming-convo.md` §12 (technology selection criteria list).

### RF-B113 · Create `docs/architecture/DATA-OWNERSHIP.md` (source of truth for every entity and store)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §11 item 4, §12 · `todo.md` Track A Phase 0
- **Description:** Write the data-ownership document expanding ADR-008 into the authoritative reference: for every important entity and data store, its owning component, system of record, replication/backup story, and cross-component correlation identity. Covers tenant/domain/mailbox config, credential metadata, policy, transport queue, remote attempts, transport status, message content, mailbox membership, folder state, IMAP sync metadata, spam/malware verdicts, quarantine decisions, scan records, and lifecycle events.
- **Acceptance criteria:**
  - [ ] `docs/architecture/DATA-OWNERSHIP.md` created; every entity/store listed with owner + system of record
  - [ ] Correlation-identity strategy for every cross-component operation stated
  - [ ] Retention/backup owner per store stated
- **Dependencies:** RF-B109 (ADR-008).
- **Evidence / relevant files:** `upcomming-convo.md` §5 (data ownership) · `todo.md` §2.1 (data ownership paragraph).

### RF-B114 · Create `docs/architecture/MESSAGE-LIFECYCLE.md` (14 flow designs)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §11 item 5, §12 (required lifecycle designs) · `todo.md` Track A Phase 0
- **Description:** Document complete sequence diagrams for all 14 message-lifecycle flows: API transactional send · authenticated SMTP submission · direct-to-MX outbound delivery · upstream relay delivery · remote delivery deferral and retry · hard bounce and soft bounce · complaint ingestion and suppression · inbound SMTP acceptance · inbound mailbox delivery · alias and forwarding · spam quarantine · IMAP mailbox access · POP3 mailbox access · mailbox deletion and retention. Every message carries a stable correlation identity across components; every design distinguishes API acceptance, queue acceptance, remote SMTP acceptance and verified delivery evidence; no design reports queue acceptance as final delivery.
- **Acceptance criteria:**
  - [ ] `docs/architecture/MESSAGE-LIFECYCLE.md` created with all 14 sequence diagrams
  - [ ] Each diagram shows the four acceptance/delivery states distinctly
  - [ ] Correlation identity visible in every flow; bounce/quarantine/mailbox-deletion flows include retention and suppression effects
- **Dependencies:** RF-B109/RF-B113 (ownership + identity); RF-B102/RF-B103 (transport decisions).
- **Evidence / relevant files:** `upcomming-convo.md` §12 (required message lifecycle designs, 14 items) · `todo.md` "Message-lifecycle designs to document".

### RF-B115 · Create `docs/architecture/MIGRATION-PLAN.md` (compatibility, migrations, rollback)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §11 item 6, §9 (backward compatibility), §12 · `todo.md` Track A Phase 0
- **Description:** Write the migration and backward-compatibility plan: existing REST endpoints and SDK contracts remain supported; existing SMTP submission credentials keep working through a compatibility layer; campaign and transactional sending keep functioning while the MTA adapter is introduced; preserve the `{success, data}` envelope, webhook event contracts, HMAC tracking-token formats, workspace isolation and RBAC rules; additive migrations wherever practical; no automatic migration of transactional email records into mailbox storage; explicit tested migration and rollback procedures for any storage transition.
- **Acceptance criteria:**
  - [ ] `docs/architecture/MIGRATION-PLAN.md` created covering compatibility, schema migration and rollback
  - [ ] The eight preservation requirements (API/SDK, SMTP credentials, sending, envelope, webhooks, tracking tokens, RBAC, additive migrations) each addressed
  - [ ] Rollback procedure per migration phase defined
- **Dependencies:** RF-B109/RF-B113; informed by RF-B102/RF-B104.
- **Evidence / relevant files:** `upcomming-convo.md` §9 (migration and backward compatibility list) · `internal/storage/migration/upgrade/` (versioned-step pattern).

### RF-B116 · Create `docs/architecture/IMPLEMENTATION-ROADMAP.md` (dependency-aware tickets + acceptance criteria)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §11 item 7, §12 · `todo.md` Track A Phase 0
- **Description:** Produce the Option B implementation roadmap as dependency-aware tickets with measurable acceptance criteria, reconciling the original 52-item Track B roadmap with the Option B phases (0–6): retain still-relevant P0/P1 trust, queue, authorization, signal-integrity and `Sender`-seam work; fold `→ OB Phase n` items (GAP-01→P1, GAP-02 residual/GAP-47→P2, GAP-04/05/48→P1, GAP-08/49/50→P6, GAP-20→P1 ops) into their phases; identify blockers, parallelizable work and migration dependencies.
- **Acceptance criteria:**
  - [ ] `docs/architecture/IMPLEMENTATION-ROADMAP.md` created with dependency-aware tickets + acceptance criteria per Option B phase
  - [ ] Track B reconciliation table present (every GAP-nn mapped: retained, folded to a phase, or closed)
  - [ ] Blockers and parallelizable tracks identified
- **Dependencies:** RF-B101…RF-B115 (needs the architecture decisions to sequence).
- **Evidence / relevant files:** `todo.md` Track A phases + "Folds in from Track B" notes · `05-implementation-roadmap.md` §8 (sequence + hard blockers).

### RF-B117 · Create `docs/architecture/RISK-REGISTER.md` (reliability, security, deliverability, operational, licensing)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §11 item 8, §12 · `todo.md` Track A Phase 0
- **Description:** Write the Option B risk register covering reliability, security, deliverability, operational and licensing risks of the expanded platform — seeded from the AS-IS risk table (R1–R16 in `06-architecture-impact.md` §3, including the four HIGH risks R1–R4), the critical operational requirements table (IP reputation, DNS, SMTP security, mailbox security, spam, malware, tenant isolation, data durability, abuse prevention, operations), and the four non-negotiable design rules (never an open relay; never trust client-supplied tenant ID/recipient domain; never report queue acceptance as delivery; never accept inbound mail without a durable path).
- **Acceptance criteria:**
  - [ ] `docs/architecture/RISK-REGISTER.md` created with risk, severity, evidence, mitigation, owner surface per entry
  - [ ] All four non-negotiable design rules and the ten operational-requirement rows covered
  - [ ] R1–R16 dispositions stated (carried forward / mitigated / superseded)
  - [ ] Licensing risks (AGPL/BSL exposure of candidates) included
- **Dependencies:** RF-B101; consumes RF-B102…RF-B110 outputs.
- **Evidence / relevant files:** `docs/product-roadmap/06-architecture-impact.md` §3 (R1–R16), §10 · `upcomming-convo.md` §8 (critical operational requirements + design rules).

### RF-B118 · Option B Phase 1 — Outbound MTA foundation (adapter, direct-to-MX + relay modes, durable transport events)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §7 Phase 1, §6 outbound flow · `todo.md` Track A Phase 1 · ADR-001 (RF-B102) · folds in GAP-01 (RF-F048), GAP-04, GAP-05, GAP-48 · risk R15
- **Description:** Build the outbound MTA foundation behind the ADR-001 decision: MTA adapter and lifecycle manager; direct-to-MX delivery and upstream-relay modes; outbound queue visibility, retry, hold and release; delivery-attempt records and durable transport events; IP binding, per-domain sending policy and rate limits; TLS, DKIM signing, SPF/DMARC alignment checks and PTR guidance; test harnesses for SMTP responses, transient failures, permanent failures and remote throttling. Flow requirements: stable message identifier across systems; distinguish API acceptance / MTA queue acceptance / remote SMTP acceptance / final delivery evidence; idempotency + duplicate protection + durable retry; per-domain and per-IP rate limits; held queues with manual release; campaign traffic must not starve transactional traffic; record the MTA's **actual** response (queue acceptance ≠ delivery).
- **Acceptance criteria:**
  - [ ] MTA adapter + lifecycle manager implemented per the approved ADR-001; existing API/SMTP submission paths preserved (RF-B115 compatibility rules)
  - [ ] Direct-to-MX and upstream-relay modes both deliver; mode selection is configuration-driven
  - [ ] Delivery-attempt records and durable transport events land; analytics/webhooks/suppression consume them via the event pipeline
  - [ ] Outbound queue has visibility, retry, hold and manual release; campaign traffic cannot starve transactional traffic
  - [ ] Per-domain and per-IP rate limits enforced; TLS + DKIM signing + SPF/DMARC alignment checks + PTR guidance in place
  - [ ] Test harnesses cover: SMTP responses, transient failures, permanent failures, remote throttling
  - [ ] No production direct-to-MX launch until the operational readiness gates from ADR-001 pass
- **Dependencies:** RF-B101, RF-B102, RF-B111 (approved architecture); builds on GAP-04's `Sender` seam; folds RF-F048, GAP-05, GAP-48 when their phases arrive.
- **Evidence / relevant files:** `upcomming-convo.md` §6 outbound flow + requirements · `todo.md` Track A Phase 1 (incl. AS-IS pipeline diagram, `06-architecture-impact.md` §2.3) · `internal/services/queue/` (existing pipeline to extend).

### RF-B119 · Option B Phase 2 — Inbound mail routing programme (validation, aliases, spam/malware, quarantine, bounce/return-path)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** In Progress (routing foundation landed in wave W1, 2026-11-07; forwarding executor + confirmed targets landed in wave W2, 2026-11-07; quarantine + review UI landed in wave W3, 2026-11-07; spam & malware baseline per ADR-006 landed in wave W5, 2026-11-07; bounce/return-path + DSN landed in wave W4, 2026-11-07; ARC + iprev annotations landed in wave W6, 2026-11-07; the routes management API/UI is the remaining wave — see STATUS_HISTORY)
- **Refs:** `upcomming-convo.md` §7 Phase 2, §6 inbound flow · `todo.md` Track A Phase 2 · ADR-002 (RF-B103), ADR-006 (RF-B107) · folds in GAP-02 residual (bounce/return-path) and GAP-47 (antivirus)
- **Description:** Implement the inbound mail programme behind ADR-002: inbound SMTP service integration; recipient and domain validation; aliases, forwarding, routing rules and catch-all policy; spam and malware filtering interfaces; bounce and return-path processing; quarantine and manual review; inbound event and audit trails. Flow requirements: validate recipients **before** accepting responsibility; handle unknown users/aliases/catch-all/forwarding explicitly; inbound delivery idempotent; never accept then silently lose a message; quarantine + operator review; per-mailbox and per-domain quotas; forwarding loop + abuse protection; mailbox delivery kept separate from marketing subscriber state.
- **Acceptance criteria:**
  - [ ] Inbound acceptance validates recipients before accepting responsibility; unknown users/aliases/catch-all/forward outcomes explicit (deliver, route, quarantine or reject — never silently drop)
  - [x] Routing engine executes alias/forward/catch-all policy (extends RF-F040's per-domain rules); forwarding loops and abuse protected — wave W2 (2026-11-07): the executor consumes the durable `inbound_forward_tasks` rows (`internal/services/inbound/forwarder.go` — R-26 CAS claim, retry budget + standard backoff, stale-run requeue), re-sends the stored original as a `system_forward` copy through `email.Service.SendSystemForward` (provenance stamp + `forwarded` `email_events` row; never touches suppression/unsubscribe/consent — `internal/services/email/service.go`), marks the inbound row forwarded on success (original stays durable on every outcome), X-Forwarded-For-style hop accounting (`X-Posta-Forwarded-For`, limit 3, fail-closed against spoofed chains) and a per-target hourly/daily Redis rate cap; forward targets require single-use token confirmation (`inbound_forward_targets`, `internal/services/inbound/forwarder.go` `ForwardTargetService`, endpoints `internal/routes/inbound_forward_routes.go`, create/revoke workspace-edit-gated, public plain-text confirm URL); cron sweep `internal/cron/jobs/inbound_forward_queue.go`; 16 named tests in `internal/services/inbound/forwarder_test.go` + `internal/services/email/system_forward_test.go`. Rule management CRUD/UI remains RF-F040's surface.
  - [x] Spam/malware filtering interfaces per ADR-006 wired (GAP-47 ClamAV hook integrates here); quarantine + operator review UI path exists — wave W5 (2026-11-07): `internal/services/spamfilter/` — `SpamFilter` interface (`Score(msg) → (score, reasons)` + `Disposition(score) → deliver|quarantine|reject`) and the rules baseline scorer (`scorer.go`: SPF/DKIM/DMARC verdicts read from the persisted `inbound_auth_results` row via `inbound.Service.WaitAuthResults`, config-gated allowlisted DNSBL lookups for the connecting IP (`dnsbl.go`, `POSTA_SPAM_DNSBL_ZONES`, empty=off, only `DNSBLAllowlist` zones honored), header heuristics (missing Message-ID, missing/unparseable/skewed Date >24h, URL counts >10/>25), sender reputation from `email_events` bounce/complaint rates per tenant (`reputation.go`, ≥20-message sample floor), and the GTUBE self-test marker (`gtube.go`, score 1000)). Disposition thresholds admin-configurable via platform settings `spam_score_quarantine` (default 5) / `spam_score_reject` (default 10) (`internal/services/settings/provider.go`, seeded in `internal/services/seeder/settings.go`), with a clamp so reject<quarantine can only err toward quarantine. ClamAV clamd INSTREAM client (`clamav.go`, GAP-47): `POSTA_CLAMAV_HOST/PORT/TIMEOUT/FAIL_MODE` (open default), attachments scanned in the ingest WORKER (parse worker for SMTP mail, process handler for webhook-ingested mail — never the SMTP session; `internal/worker/inbound_filter.go`), positives → quarantine with the signature recorded on the durable row (`inbound_emails.threat_signature`, additive column) + `inbound.malware_detected` event; scanner outage follows the fail mode (open logs-and-continues, closed quarantines as `clamd_unavailable`). External filter hook (ADR-006 decision 3): optional synchronous consult of webhooks subscribed to `email.inbound.filter` (`POSTA_SPAM_EXTERNAL_FILTER_*`; `internal/services/webhook/filter_consult.go` — reuses the HMAC `X-Posta-Signature` scheme + guarded transport + SSRF policy; metadata-only payload, no raw content; timeout + fail-open; a reply can only escalate, never rescue). Dispositions applied through `internal/services/inbound/filter.go` `ApplyFilterDisposition` after durable ingest: quarantine → `quarantined` status + quarantine row via the W3 `QuarantineStore` seam (nil route) + `inbound.quarantined`; reject → `rejected` status (never a silent drop) + `inbound.spam_flagged`; every scored message stamps `spam_score`/`spam_reasons`. Tests: `scorer_test.go` (per-signal + threshold matrix + clamped misconfig), `clamav_test.go` (in-process fake clamd: clean/infected/unexpected/unreachable/stuck + fail-mode env parsing), `reputation_test.go` (fake-driver aggregation + bound-parameter assertion), `filter_test.go` (dispositions + event taxonomy + auth-verdict wait), `internal/worker/inbound_filter_test.go` (Run end-to-end: GTUBE quarantine, malware positive, fail-open/closed, hook escalation/fail-open), `internal/services/webhook/filter_consult_test.go` (signed reply, error fail-open, sender filter, timeout).
  - [x] Bounce and return-path processing durable (GAP-02 residual folds in); inbound events and audit trails recorded — wave W4 (2026-11-07): VERP return-path config `POSTA_RETURN_PATH_SUBDOMAIN`/`POSTA_RETURN_PATH_SECRET` (`internal/config/config.go`), signed VERP envelope on direct-MTA delivery (`internal/services/inbound/verp.go`, `internal/worker/handler.go` `envelopeSender`), RCPT allowlist + durable system-message ingest (`internal/services/inbound/smtp_server.go`, `service.go` `ingestSystemBounce`), RFC 3464 parser (`internal/services/inbound/dsn.go`) and match-back into `email_events` + the existing suppression chain (`internal/services/inbound/bounce.go`); 31 named tests in `verp_test.go`/`dsn_test.go`/`bounce_test.go`/`handler_verp_test.go`
  - [ ] Per-mailbox and per-domain quotas enforced; mailbox delivery isolated from marketing subscriber state
  - [ ] Inbound delivery idempotency tested
  - [x] ARC (SF-27) and iprev (SF-32 partial) validation annotate Authentication-Results and the verdict row, config-gated, never rejecting — wave W6 (2026-11-07, ADR-002 decision 6, annotate-only): RFC 8617 §5.2 chain validator over the ARC set headers (most recent ARC-Message-Signature verified as a DKIM signature over the message; every ARC-Seal newest→oldest over the §5.1.1 input; cv= claims + set structure checked; result none/pass/fail with all failures permanent per §5.2.1) in `internal/services/inbound/verify/arc.go` on in-repo RFC 6376 primitives (`internal/services/inbound/verify/dkimutil.go` — go-msgauth's canonicalizers are unexported and its verifier only recognizes DKIM-Signature fields); forward-confirmed reverse DNS (`internal/services/inbound/verify/iprev.go` — PTR → forward resolve each name → connecting-IP equality; pass/fail/none/temperror/permerror); composer appends `; arc=` / `; iprev=` groups only when the engine ran — a disabled deployment composes the byte-identical pre-feature value (`internal/services/inbound/verify/authres.go`); additive `inbound_auth_results` columns `arc_result`/`arc_domain`/`iprev_result`/`iprev_ptr` (empty = engine off, distinct from "none" = evaluated and nothing found), via the existing AutoMigrate registration (`internal/storage/migration/migration.go:46`); config `POSTA_INBOUND_ARC_ENABLED` / `POSTA_INBOUND_IPREV_ENABLED` both default false (`internal/services/inbound/verify_hook.go`); no sealing anywhere (forwarding re-signs DKIM per ADR-001); no `internal/routes/*` diff. Tests: 27 ARC + 12 iprev + 4 annotation-shape/gating tests in `internal/services/inbound/verify/{arc,iprev,authres}_test.go` (verify package green under `go test -count=1 -race ./internal/services/inbound/verify/`); 4 hook-wiring tests in `internal/services/inbound/verify_hook_test.go` (green under `-race -run 'TestInboundVerify|TestEnableVerification'`; they caught and fixed a gorm column-name derivation issue — see the W6 STATUS_HISTORY note)
- **Dependencies:** RF-B101, RF-B103, RF-B107, RF-B111; RF-F040 (routing-rule surface); mailbox delivery path lands with RF-B120.
- **Evidence / relevant files:** `upcomming-convo.md` §6 inbound flow + requirements · `todo.md` Track A Phase 2 · `internal/services/inbound/`, `internal/routes/inbound_routes.go` (existing receiver).

### RF-B120 · Option B Phase 3 — Mailbox & storage foundation (folders, flags, quotas, MIME store)

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** In Progress (storage core landed in wave W1, 2026-11-07 — models, additive migration incl. the search_tsv generated column + GIN via the upgrade framework AND `migration.Run`, the mailstore service (mailbox/folder CRUD, Append with pre-commit blob writes + atomic UID claim, Copy/Move, flag CAS on mod_seq, Expunge, Fetch/List), the `mail_blob_refs` ref-count seam for GAP-45, and the wave interfaces (BlobCodec stub, Searcher stub, QuotaEnforcer byte-quota baseline) with fake-driver + real-Postgres test suites — see STATUS_HISTORY. Wave W4, 2026-11-07: quotas + retention landed — the full ADR-003 §5 quota policy (per-mailbox byte quota + new `quota_messages` count quota + workspace aggregate ceiling via `workspaces.mail_quota_bytes`, decided under a mailbox-row `FOR UPDATE` lock inside the append/copy transaction, typed `*QuotaError` matching `ErrQuotaExceeded`) and the ADR-003 §7 retention sweep (`mailstore.RetentionSweeper` driven by the unified retention cron, family wired in `cmd/posta/server.go` initCronManager; per-mailbox/per-folder age+count policies with folder-over-mailbox precedence, counters synced transactionally, `mail_blob_refs` rows at ref_count ≤ 0 reclaimed together with their stored objects). Wave W3, 2026-11-07: Postgres FTS search landed — `mailstore.PostgresSearcher` implements W1's `Searcher` (`internal/services/mailstore/search.go`: `websearch_to_tsquery('english', ?)` over `mail_messages.search_tsv`, mailbox+user isolation in the WHERE, optional folder scoping, ranked paged hits with true total), plus the operator reindex path `migration.RebuildMailstoreSearch` (`internal/storage/migration/mailstore_fts_reindex.go`, raw-SQL drop+re-add of the generated column + GIN in one transaction) exposed as the admin-gated audited `POST /admin/mailstore/search/reindex` (`internal/handlers/mailstore_search_admin_handler.go`, `internal/routes/admin_routes.go`). Remaining: backup/restore/migration tooling, orphan-object blob enumeration (GAP-45 residual), size-based retention policy, dashboard quota-usage surfacing (OB-4/OB-5). Wave W2, 2026-11-07: the content-addressed encrypted blob layer (GAP-45 storage half) landed — `mailstore.ZstdBlobCodec` implements W1's `BlobCodec` (`internal/services/mailstore/codec_zstd.go`: zstd above the 4 KiB threshold only when it shrinks the payload, AES-256-GCM via new `crypto.EncryptBytes`/`DecryptBytes`, self-describing frame, SHA-256-of-plaintext content addressing, every tamper path → `ErrBlobCorrupt`); Append restructured to reserve-then-write (per-content-address `pg_advisory_xact_lock` around the ref upsert + blob Put, `ref_count ≥ 1` durable before any message row; dedup via the returned post-increment count with a probe-read self-heal); `mail_blob_refs.last_referenced` grace anchor added (additive column, stamped on reserve/increment/release); `RetentionSweeper.reclaimBlobs` now grace-periods dead refs (default 24 h, `ReclaimGrace` overridable), re-checks `ref_count <= 0` under the same per-hash lock (live references can never be collected), rolls the row delete back when the object delete fails, and reports reclaimed bytes; the unified cron writes the audited `mail_blob_gc` summary event (`internal/cron/jobs/retention.go` `auditMailBlobGC`, usage reporter wired additively in `cmd/posta/server.go`); GAP-45 per-mailbox usage reporting via `mailstore.MailboxUsage`/`MailboxBlobUsageAll` (`internal/services/mailstore/usage.go`, dedup-aware blob bytes from `mail_blob_refs` vs the dedup-blind `used_bytes` quota basis). Named tests: `codec_zstd_test.go` (8 tests), `TestExpungeDecrementsBlobRefs`, `TestMailboxUsageBlobBytesFromBlobRefs`, `TestMailboxUsageOwnershipScoped`, `TestPGAppendDedupRefCountsTwo`, `TestPGMailBlobGCDropsOnlyAgedOrphans`, `TestPGConcurrentAppendSameContent`, `TestPGMailboxUsageBlobBytesFromBlobRefs`, `TestPGReserveSelfHealsMissingBlob`; named battery green (45 packages ok under `-race`, zero FAIL, PG-gated suite included). Documented residual: per-workspace data keys (the `BlobCodec` interface carries no workspace context; the deployment-wide `POSTA_ENCRYPTION_KEY` data key is used — see the STATUS_HISTORY W2 note). Wave W5, 2026-11-08: the ingestion seam (flow F13) landed — inbound messages whose matched route action is `deliver` file into the recipient's hosted INBOX via `mailstore.Append`: seam interface `inbound.MailboxDeliverer` + implementation `inbound.MailboxDelivery` (`internal/services/inbound/mailbox_delivery.go`), executed by the route-action executor `applyRouteActions` on both ingest paths (SMTP `IngestRaw`, webhook `Ingest`) and on quarantine release (raw reloaded via `LoadRaw`); wired in `cmd/posta/server.go` (SMTP receiver) and `internal/routes/routes.go` (webhook path) behind the blob store + `POSTA_ENCRYPTION_KEY` presence (the production `ZstdBlobCodec` refuses unencrypted storage, SF-78) — without either, delivery stays archive-only with a visible log line. Auto-provision: the recipient mailbox (named by the full lowercased address, unique (user_id, name)) is created for the tenant user on first delivery to a verified domain bound to that user, gated by new config `POSTA_MAILSTORE_AUTO_PROVISION` (default true, `internal/config/config.go`); off → archive-only exactly as before. Quota-enforced: an append refused with the typed `*mailstore.QuotaError` quarantines the message (ADR-002 §3) with the stable typed reason prefix `mailbox_quota_exceeded` + structured quota fields (scope/used/incoming/limit); release after the quota is raised re-runs routing and files the message (never lost — the durable archive copy exists throughout). Any other append failure logs and falls back to archive-only. A held (quota-quarantined) record also skips the webhook path's generic process dispatch, matching route-quarantine semantics. Named tests (all passing): `TestPGMailboxDeliveryRoundTrip`, `TestPGMailboxDeliveryAutoProvisionOff`, `TestPGMailboxDeliveryOverQuotaQuarantinesTyped`, `TestPGMailboxDeliveryLeavesTransactionalEmailAndSubscriberStateUntouched` (real Postgres, `TEST_DATABASE_DSN`), plus `TestDeliverActionWithoutSeamKeepsArchiveOnly`, `TestMailboxDeliveryNilGuards`, `TestDeliverActionUnknownErrorFallsBackToArchive`, `TestDeliverActionOverQuotaQuarantinesWithoutStore` (fake-driver/units).)
- **Refs:** `upcomming-convo.md` §7 Phase 3 · `todo.md` Track A Phase 3 · ADR-003 (RF-B104)
- **Description:** Choose and prototype the mailbox storage backend per ADR-003, then implement the mailbox/folder/quota/message-metadata model, MIME and attachment storage with retention, and the operation set: append, fetch, copy, move, flags, expunge, search. Establish concurrent-access and synchronization behavior, plus backup, restore and mailbox-migration tooling. Do **not** reuse the transactional `Email` model; do not auto-migrate transactional records into mailbox storage.
- **Acceptance criteria:**
  - [ ] Storage backend selected per ADR-003 and prototyped against the selected IMAP implementation (RF-B105) — backend selected (Postgres mailstore per ADR-003); IMAP-side prototype pending OB-4
  - [ ] Mailbox, folder, quota and message-metadata model implemented; MIME/attachment storage with retention rules — wave W1 (2026-11-07): models `internal/models/mailbox.go` (`Mailbox`/`MailFolder`/`MailMessage`/`MailBlobRef`, additive AutoMigrate registration `internal/storage/migration/migration.go`), MIME + attachment ingest into content-addressed blobs with ref counts (`internal/services/mailstore/append.go`, `parse.go`). Wave W4 (2026-11-07): retention rules landed — `retention_days`/`retention_max_count` on `mailboxes` + `mail_folders` (folder overrides mailbox; age from the server-side ingest timestamp), executed by the unified retention cron via `internal/services/mailstore/retention.go` (`RetentionSweeper.SweepRetention`, wired in `cmd/posta/server.go` initCronManager); quota enforcement likewise landed (`internal/services/mailstore/quota.go`: `DefaultQuotaEnforcer` bytes + `quota_messages` + workspace ceiling via `workspaces.mail_quota_bytes`, `*QuotaError` typed rejection surfaced at append/copy). Size-based retention and orphan-object blob enumeration remain open residuals; quota usage surfaced in the dashboard lands with OB-4/OB-5
  - [ ] Append, fetch, copy, move, flags, expunge and search all work — wave W1 (2026-11-07): append/fetch/copy/move/flags-CAS/expunge/list live in `internal/services/mailstore/` (uid claim via atomic `UPDATE ... RETURNING`, flag CAS on mod_seq, Expunge keeps uid_validity + never rewinds uid_next). Wave W3 (2026-11-07): search landed — `PostgresSearcher` (`internal/services/mailstore/search.go`) replaces the `SearcherStub`: websearch-syntax queries parsed by `websearch_to_tsquery('english', ?)` against the weighted `search_tsv` column (A subject / B from+to+cc / C body), user isolation + mailbox scoping in the statement, optional `FolderID` narrowing, relevance-ranked pages (`ts_rank` + `COUNT(*) OVER ()`) with clamped page sizes; real-Postgres tests cover subject/from/body hits, cross-user isolation, pagination+totals, ranking (subject outranks body), folder scoping and reindex idempotence (`search_pg_test.go`, `TEST_DATABASE_DSN`-gated); the operator rebuild helper + admin route close the maintenance loop (`RebuildMailstoreSearch`, `POST /admin/mailstore/search/reindex`). HTTP search surfaces for end users are OB-4 (IMAP SEARCH) / OB-5 (webmail) by design
  - [ ] Mailbox data survives restarts, supports concurrent clients, and backs up/restores without corruption (tested) — concurrent access proven in W1 (UID monotonicity under 48-way concurrent appends + flag CAS against real Postgres, `mailstore_pg_test.go`; migration smoke passes twice on a scratch DB); backup/restore still open
  - [ ] Backup/restore/migration tooling delivered; transactional `Email` records untouched — `Email` untouched (mailstore is fully additive, separate tables); tooling still open
- **Dependencies:** RF-B104 (ADR-003), RF-B105 (backend/IMAP compatibility); blocks RF-B121.
- **Evidence / relevant files:** `upcomming-convo.md` §7 Phase 3 · `todo.md` Track A Phase 3 (exit criteria) · `internal/storage/blob/` (existing blob store — reference for attachment storage).

### RF-B121 · Option B Phase 4 — IMAP and POP3 access against the mailbox backend

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** In Progress (2026-11-08, OB-4 waves W1–W4 — the seam (W1) and the `internal/services/imap` / `internal/services/pop3` server packages with gated four-port listener wiring, TLS/limits/audit gates (W2/W3/W4) landed; UIDVALIDITY-driven folder/message sync and the standard-client compatibility matrix are the remaining waves)
Wave W2 evidence addendum (2026-11-08, the IMAP wave's own scripted-client matrix, landed after W4's snapshot above was written): `internal/services/imap/` carries the R-22-shape scripted-client harness (`harness_test.go`: in-process TCP listener, real mailstore over real Postgres via the TEST_DATABASE_DSN gate, real bcrypt app-password fixtures) and the named command-matrix suites (`server_test.go` + `parser_test.go`, 22 harness tests + 15 units — names in the STATUS_HISTORY W2 note): session lifecycle + capability gating, unauthenticated command refusal, app-password-only auth (primary password refused), AUTHENTICATE PLAIN/LOGIN flows, STARTTLS enforcement + in-place upgrade, SELECT/EXAMINE semantics, APPEND literal + FETCH body/section/peek/partial byte-exactness, STORE ±FLAGS/.SILENT with the mod_seq CAS conflict surfacing as tagged NO, COPY/MOVE UIDVALIDITY behavior, the ADR-004 SEARCH key set incl. FTS BODY/TEXT, EXPUNGE/CLOSE semantics, IDLE notification delivery, APPEND over-quota `NO [OVERQUOTA]`, per-user session cap, command-rate budget, literal-cap rejection. Checks: package suite under `-race` with the DSN → `ok 33.326s`; the named battery verbatim → exit 0; the battery re-run WITH TEST_DATABASE_DSN against postgres:17-alpine → exit 0 (imap `ok 28.384s` — the harness runs against real Postgres inside the battery; mailstore `ok 22.277s`, repositories `ok 27.732s`). The harness drove eight real server fixes, recorded in the STATUS_HISTORY W2 note (Server.Close registry deadlock; RFC 9051 §7.5.1 newest-first EXPUNGE; STORE .SILENT suffix parse; FLAGS-replace keeping server-managed \Recent; UID FETCH auto-UID; tagged `NO [TOOBIG]` literal-cap recovery; SEARCH BADCHARSET on the parse path; BODY field-list case preservation). "W2 shipped with no tests" in the W4 snapshot above was true at that snapshot and is superseded by this addendum.
- **Refs:** `upcomming-convo.md` §7 Phase 4 · `todo.md` Track A Phase 4 · ADR-004 (RF-B105)
- **Description:** Integrate IMAP against the selected mailbox backend; authentication/authorization tied to ReForwarder identities; folder and message synchronization; POP3 as a separate access mode with correct deletion semantics; standard-client and protocol compatibility testing; connection limits, TLS enforcement, rate limits and audit logs.
- **Acceptance criteria:**
  - [ ] IMAP works against the mailbox backend with folder/message sync; state consistent across concurrent clients
  - [ ] Auth/authorization tied to ReForwarder identities; TLS enforced; connection limits and rate limits applied; audit logs recorded
  - [x] POP3 operates as a separate access mode with correct deletion semantics (2026-11-08, OB-4 wave W3: `internal/services/pop3/` classic command set over the INBOX with the per-session shadow view — DELE marks in-session, QUIT's UPDATE phase maps the marks onto the mailstore \Deleted sweep, RSET rolls back, a session that drops without QUIT expunges nothing, concurrent sessions never see each other's marks, and survivors keep their UIDs with uid_validity unchanged; proven by the scripted-client matrix `pop3_test.go` — TestPOP3DeleQuitExpunges, TestPOP3RsetRollback, TestPOP3SessionScopedDeletion — green under `-race` on golang:1.27)
  - [ ] Standard mail clients (compatibility test matrix) securely use hosted mailboxes
- **Dependencies:** RF-B105, RF-B120 (mailbox backend is the precondition).
- **Evidence / relevant files:** `upcomming-convo.md` §7 Phase 4 · `todo.md` Track A Phase 4 (exit criteria). Wave W1 (2026-11-08, seam): `mailstore.FetchRange` sequence/UID sets + BODY sections incl. partials (`internal/services/mailstore/fetchrange.go`, `seqset.go`, `mimesection.go`), `mailstore.SearchIMAP` IMAP SEARCH translation to SQL + FTS (`search_imap.go`), `mailstore.StatusCounts` (`status.go`), `mailstore.ConcurrentSessionGuard` per-folder write locking wired into Expunge (`guard.go`), `mailstore.ChangeNotifier` in-process + Redis fan-out on `posta:mailchg` emitting from Append/Expunge/flags/Move/Copy (`notifier.go`), `mailauth.Authenticator.AuthenticateWithAppPassword` app-password-only bcrypt verification (`internal/services/mailauth/auth.go`), `mailstore.MailSession` SELECT/EXAMINE state (`session.go`); ~45 named tests incl. the `-race` concurrent append+expunge suite and real-Postgres PG-gated siblings (`w1_pg_test.go`); ledger note in `docs/project/STATUS_HISTORY.md`. Waves W2/W3/W4 (2026-11-08, servers + ops wiring): `internal/services/imap/` (ADR-004 command subset, STARTTLS, IDLE via the W1 ChangeNotifier, audit sink `audit.go`), `internal/services/pop3/` (classic command set, STLS, mark-then-expunge-on-QUIT deletion semantics, audit logger, its own protocol test matrix `pop3_test.go`), gated listener wiring for all four ports (143/993/110/995) with graceful shutdown in `cmd/posta/server.go` (`startIMAPServer`/`startPOP3Server`, both behind security.NewCountingListener + TLSServerListener per SF-44), `POSTA_IMAP_*`/`POSTA_POP3_*` config keys + `validateMailProtocols` posture enforcement (`internal/config/config.go`), audit events into the events substrate (SF-74 continuation), commented default-off `compose.override.yml` block, operator page `docs/docs/security/mail-protocols.md`; W4 tests: `internal/config/mail_protocols_test.go` (validation matrix), `internal/services/imap/audit_test.go` + `lifecycle_test.go` (audit emission, limiter, lifecycle — W2 shipped no tests), W3's `pop3_test.go`; two W2 defects repaired and recorded (uninitialized `session.out` writer — every session panicked on the greeting — and a shutdown/teardown data race in `session.close`), W2 self-repaired an invalid ternary in `commands.go:998` that broke the tree build.

### RF-B122 · Option B Phase 5 — Webmail and mailbox administration

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §7 Phase 5 · `todo.md` Track A Phase 5 · ADR-005 (RF-B106)
- **Description:** Build webmail and mailbox administration per ADR-005: mailbox provisioning and lifecycle management; inbox, folders, compose, reply, forwarding, attachments; search, read/unread state, mailbox settings; password changes, sessions, device management; mailbox quotas and usage reporting; admin tools for mailbox suspension and recovery.
- **Acceptance criteria:**
  - [ ] Users manage hosted email (inbox, folders, compose/reply/forward, attachments, search, read state) without a third-party webmail deployment
  - [ ] Mailbox provisioning and lifecycle managed from the control plane; quotas and usage reporting visible
  - [ ] Password changes, sessions and device management work for mailbox identities; admin suspension/recovery tools exist
- **Dependencies:** RF-B106, RF-B120, RF-B121.
- **Evidence / relevant files:** `upcomming-convo.md` §7 Phase 5 · `todo.md` Track A Phase 5 (exit criteria).

### RF-B123 · Option B Phase 6 — IP pools, warm-up controls, and reputation monitoring

- **Priority:** P1
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §7 Phase 6, §8 (IP reputation row) · `todo.md` Track A Phase 6 · INF-025 · ADR-007 (RF-B108) · reinstated by the Option B decision (was excluded under relay-only, `05-implementation-roadmap.md` §9)
- **Description:** Add IP pools (multiple sending IPs), warm-up controls, throttling, reputation monitoring, abuse response and blacklist alerts on the outbound data plane — meaningful only once the platform owns egress (RF-B118). Per-domain/IP routing policy selection rides the RF-B118 queue policy layer.
- **Acceptance criteria:**
  - [ ] IP pool model implemented (pool membership, per-pool routing policy on the MTA adapter)
  - [ ] Warm-up controls (ramped volume per IP) and throttling enforced
  - [ ] Reputation monitoring with blacklist alerts and abuse response runbook
  - [ ] Pool/warm-up/reputation signals visible in metrics and admin surface
- **Dependencies:** RF-B118 (outbound MTA foundation), RF-B108 (ADR-007 reputation model).
- **Evidence / relevant files:** `upcomming-convo.md` §7 Phase 6, §8 (IP reputation required protection) · `05-implementation-roadmap.md` §9 entry 12 (exclusion superseded by Option B).

### GAP-04 · Extract the Sender seam and add ESP API integrations with failover and per-send server selection

- **Priority:** P1
- **Category:** delivery-queue
- **Workflow status:** To-do
- **Refs:** GAP-04 · INF-022 · API-015 · INF-023 · INF-024 · complexity XL (score 2.55; the register's biggest build) · `05-implementation-roadmap.md` §4 · feeds RF-B118 (the seam is the MTA-adapter predecessor)
- **Description:** Put a provider abstraction behind `Send`: extract the `Sender` interface implicit in `smtp_sender.go` (`Send`, `TestConnection`), add API-mode ESP clients (SES, Postmark, Mailgun first), a failover chain, and per-message/per-campaign server override. Today: generic SMTP only; retry re-queues against the same server (`handler.go:334-356`, audit); send-time selection picks the first enabled server (`smtp_repo.go:119-125`, audit). The `6d2d7c0` adapters cover event *ingestion*, not sending. Deterministic TaskIDs must prevent failover double-sends. Start the abstraction early in Phase 1 (synthesis §5); ESP-client surface runs long into Phase 2.
- **Acceptance criteria:**
  - [ ] `Sender` interface extracted; SMTP relay remains the default path behind it (no behavior change when unconfigured)
  - [ ] SES/Postmark/Mailgun API clients implemented with per-provider contract tests
  - [ ] A failing primary fails over to the next candidate within one retry — tested; no double-send under failover (deterministic-TaskID interplay pinned)
  - [ ] Server selection extended to ordered candidates + health-aware rotation (failure counters at `handler.go:336-341`, audit, reused)
  - [ ] Workspace integration entity with credentials stored via `crypto.Encrypt` (AES-256-GCM; production requires `POSTA_ENCRYPTION_KEY` — base64 fallback caveat documented)
  - [ ] Integration CRUD + per-campaign server pin + per-provider test-connection endpoints; ESP-config UI and campaign pin selector shipped
  - [ ] SSRF posture applied to API-mode calls; scope-gated CRUD
- **Dependencies:** none hard (pairs with the landed provider-event webhooks for test signals); the seam is the precondition for failover semantics and future IP/reputation work (RF-B123).
- **Evidence / relevant files:** `internal/services/queue/smtp_sender.go:186-203` (audit: `Send`/`TestConnection`), `internal/worker/handler.go:197-250,334-356` (audit) · `internal/storage/repositories/smtp_repo.go:119-125` (audit) · `05-implementation-roadmap.md` §4 GAP-04.

### GAP-05 · Support caller-supplied Idempotency-Key on send endpoints

- **Priority:** P1
- **Category:** delivery-queue
- **Workflow status:** To-do
- **Refs:** GAP-05 · INF-031 · complexity M (2.85) · `05-implementation-roadmap.md` §4 · → OB Phase 1 (folds into RF-B118)
- **Description:** `Idempotency-Key` header on send/batch endpoints with a keyed dedup store and result replay, so client-level retries before enqueue cannot double-send. Internal deterministic-TaskID dedup landed `a6f1681` (`producer.go:47-101`) but does not protect caller-level retries before enqueue (grep clean for `Idempotency-Key`, audit-H).
- **Acceptance criteria:**
  - [ ] `Idempotency-Key` accepted on `POST /emails/send`, `/emails/send-template`, `/emails/batch`
  - [ ] `(workspace_id, key) → email_id/result` persisted with a unique index; a conflicting replay returns the stored result
  - [ ] Two concurrent identical-key requests return the same `email_id` (test); a different key creates a new email
  - [ ] Keys expire via the existing retention cron; keys are workspace-scoped (no cross-tenant replay)
  - [ ] Interplay with deterministic-TaskID dedup tested; opt-in header, no breaking change; replay semantics documented
- **Dependencies:** none; complements the internal TaskID dedup.
- **Evidence / relevant files:** `internal/services/queue/producer.go:47-101` (audit: TaskID dedup) · `05-implementation-roadmap.md` §4 GAP-05.

### GAP-06 · Add auto-pause circuit breaker on failure and complaint thresholds

- **Priority:** P1
- **Category:** delivery-queue
- **Workflow status:** To-do
- **Refs:** GAP-06 · INF-016 · DOM-011 · ANA-010 · complexity M (3.15) · `05-implementation-roadmap.md` §4 · → OB Phase 1 (reputation) · blocked by GAP-02 residual (RF-F053 / RF-F099)
- **Description:** Auto-pause campaigns on consecutive-failure or bounce/complaint-rate thresholds instead of alerting only. Today `INF-016` is MISSING and `DOM-011`/`ANA-010` are alert-only (`bounce_alert.go`, audit). Extend the `bounce_alert.go` evaluation into a pause action: on threshold breach, call the existing pause CAS transition (`campaign_handler.go:391-438`), emit an audit event and workspace notification; per-workspace thresholds via settings. Exemplar thresholds (MailCheap, audit): pause at 2.5% bounce / 0.08% complaint, block at 5% / 0.3%.
- **Acceptance criteria:**
  - [ ] Auto-pause trips on configured consecutive-failure and rate thresholds; complaint-rate gates consume the provider-event path (never accept-only data)
  - [ ] Resume is manual-only and audited; paused-by-circuit-breaker indicator shown on campaigns
  - [ ] Thresholds configurable per workspace (settings mechanism); default off until configured
  - [ ] No pause when signals are accept-only (guard test)
  - [ ] Threshold-trip and resume-audit tests pass
- **Dependencies:** RF-F053 + RF-F099 (honest complaint/delivered signals); campaign pause CAS exists.
- **Evidence / relevant files:** `internal/cron/jobs/bounce_alert.go` (audit: alert-only) · `internal/handlers/campaign_handler.go:391-438` (audit: pause CAS) · `05-implementation-roadmap.md` §4 GAP-06.

### GAP-14 · Add signed timestamp replay protection to outbound webhook signatures

- **Priority:** P1
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-14 · API-010 · complexity S (2.75) · `05-implementation-roadmap.md` §4 · risk R9 · coordinates with GAP-23 (SDK docs)
- **Description:** Signed timestamp + tolerance window on outbound webhook signatures so captured payloads cannot be replayed indefinitely. Distinct from the landed inbound provider-webhook replay windows (`6d2d7c0`): this is the outbound `X-Posta-Signature` path (`dispatcher.go:485`, audit — no timestamp today). Add `X-Posta-Timestamp` into the HMAC input; reject deliveries older than 5 minutes against the dispatcher clock; document clock-skew tolerance and the header in `webhook_docs.go` payload examples.
- **Acceptance criteria:**
  - [ ] `X-Posta-Timestamp` signed into the HMAC input; deliveries older than the 5-minute tolerance are rejected
  - [ ] Captured-valid-signature replayed >5 min later is rejected (test); registry test pins the new header (11-event registry test pattern)
  - [ ] Payload examples and signature-verification docs updated; version note for existing receivers with an advised grace window
- **Dependencies:** none; coordinate the SDK `constructEvent` helper update with GAP-23.
- **Evidence / relevant files:** `internal/services/webhook/dispatcher.go:485` (audit: signature without timestamp) · `internal/handlers/webhook_docs.go` · `05-implementation-roadmap.md` §4 GAP-14.

### RF-F091 · Wire security_events retention enforcement (`DeleteOlderThan` caller) (GAP-13)

- **Priority:** P1
- **Category:** platform-admin-security
- **Workflow status:** Done (2026-11-07, uncommitted working tree — P3-3 ops/settings wave, dwfrun-65ae252f; test gate PASS)
- **Refs:** RF-F091 · GAP-13 · SEC-008 · complexity S (2.50) · `05-implementation-roadmap.md` §4 · risk R7
- **Description:** `security_events` grows unbounded: the retention setting is declared (`security_handler.go:489`) and read (`security/rules.go:129`), but `SecurityEventRepository.DeleteOlderThan` (`security_event_repo.go:157-160`) has no caller (re-verified in the audit suite). Wire a daily job in the retention cron (`retention.go:195-196` pattern) that reads the setting and prunes in batches; expose the setting in the admin Settings UI category that already lists retention windows.
- **Acceptance criteria:**
  - [x] Daily retention job calls `DeleteOlderThan` using the `security_events_retention_days` setting; deletes are batched (no long transactions) — `internal/cron/jobs/retention.go:424-433` (single parameterized DELETE per pass, same shape as the other retention families); wired `cmd/posta/server.go:829-832`; provider `internal/services/settings/provider.go:170-172` (default 90)
  - [x] Setting `0` explicitly disables retention (tested) — `TestRetentionSecurityEventsZeroKeepsForever` (`internal/cron/jobs/retention_security_test.go:268`); seeder value aligned 30 → 90 (`internal/services/seeder/settings.go:98`) with the enforcement default
  - [x] Setting exposed in the admin Settings UI retention category — pre-existing exposure via the security rules settings surface (`internal/handlers/security_handler.go:489`, group `securityRuleGroupRetention`, PUT `/admin/security/rules`); residual: that UI rule still displays a default of 30 with Min 1, so `0 = keep forever` is reachable only through PUT `/admin/settings` or the DB, not the security-rules upsert
  - [x] Tests mirror `retention_test.go`; `go test ./...` passes — `internal/cron/jobs/retention_security_test.go`: TestRetentionPurgesSecurityEventsPastDefaultWindow, TestRetentionSecurityEventsZeroKeepsForever, TestRetentionSecurityEventsCustomWindow, TestRetentionSecurityEventsDeleteIsParameterized; named battery `go build ./... && go test -count=1 -race ./internal/handlers/ ./internal/services/... ./internal/storage/... ./internal/cron/...` dockerized `golang:1.27` exit 0, 36 packages ok, 2026-11-07
- **Dependencies:** none (repo method and setting both exist).
- **Evidence / relevant files:** `internal/storage/repositories/security_event_repo.go:157-160` (audit: no caller) · `internal/services/security/rules.go:129`, `internal/handlers/security_handler.go:489` (audit) · `internal/cron/` retention job pattern.

### RF-F100 · Resolve tracking toggle asymmetry for the campaign open pixel (product-decision gate) (GAP-15)

- **Priority:** P1
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** RF-F100 · GAP-15 · ANA-012 · complexity S (2.00) · `05-implementation-roadmap.md` §4 · risk R8 · **product-decision gate: decide intent before code**
- **Description:** Campaign mail still receives the open pixel when `email_tracking_enabled` is off (`campaign_processor.go:392-396` — deliberate else-branch with comment `:319-321`; re-verified at HEAD per synthesis §6.5), while the setting's documentation promises no rewriting + no pixel. This is a false privacy promise with GDPR/ePrivacy exposure. **First decide product intent** (audit E-F NV item 1): align behavior to the documented promise, or codify the exception in the doc and/or add a per-campaign `tracking_enabled` override. Then implement: skip rewrite + pixel in the campaign branch when off (or the scoped decision), preserving unsubscribe/web-view links either way.
- **Acceptance criteria:**
  - [ ] Product decision recorded (align vs scope the exception) before code lands
  - [ ] With tracking off, rendered campaign bodies contain no pixel and no rewritten links — rendered-body assertion test
  - [ ] Unsubscribe and web-view links remain functional regardless of the toggle
  - [ ] Setting documentation matches implemented behavior; release note for the rendered-output change
- **Dependencies:** product decision (no code dependency).
- **Evidence / relevant files:** `internal/services/campaign/campaign_processor.go:319-321,392-396` (audit: else-branch) · `05-implementation-roadmap.md` §4 GAP-15.

### RF-F101 · Remove the dead Contact Lists surface end-to-end (D1 consolidation)

- **Priority:** P1
- **Category:** frontend-integration
- **Workflow status:** To-do
- **Refs:** RF-F101 · D1 · C-D NV-5 · `04-feature-gap-analysis.md` §4 D1 · `todo.md` D1–D9
- **Description:** ContactLists is a fully-built but completely unrouted legacy surface: 7 handler methods with zero route registrations, a dead link (`ContactListMembers.vue:33` pushes nonexistent route `contact-lists`), and the surface still round-trips in GDPR export/import (audit-C). Consolidate the dual audience model: Subscribers stay the canonical CRUD audience; Contacts stay as the read-only send ledger; ContactLists is deleted entirely (model + handler + views + API client + export/import paths) in a versioned upgrade step (the `migration/upgrade/` pattern exists).
- **Acceptance criteria:**
  - [ ] One release stops exporting ContactLists from GDPR export/import; removal has a release note (data-migration cost of `contact_list_members.Data` documented)
  - [ ] Model, handlers, Vue views, API-client methods and route registrations removed in a versioned upgrade step
  - [ ] Dead `contact-lists` link replaced with a redirect
  - [ ] Subscribers (CRUD audience) and Contacts (read-only send ledger) roles documented; no regression in subscriber/list flows
- **Dependencies:** none; versioned-migration step required.
- **Evidence / relevant files:** `internal/handlers/contact_list_handler.go` (unrouted methods) · `web/src/views/ContactListMembers.vue:33` (audit) · `internal/handlers/workspace_data_handler.go` (export/import paths) · `04-feature-gap-analysis.md` §4 D1.

### RF-F075 · Implement double opt-in (DOI) confirmation flow (GAP-25)

- **Priority:** P1
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** RF-F075 · GAP-25 · CON-004 · complexity L (2.95) · `05-implementation-roadmap.md` §4 · blocks RF-F074 (signup forms) and RF-F073 (preference center)
- **Description:** Per-list double opt-in — the single largest EU-marketing compliance blocker (audit-C top gap). Today `POST /subscriber-lists/subscribe` subscribes immediately (`subscriber_list_handler.go:373-460`, audit). Add: `pending` subscriber status + per-list DOI setting; tokenized confirmation endpoint modeled on the `/t/u/{token}` HMAC tokens (`tracking.go:273-355`); confirm → `list.subscribed` trigger (feeds welcome automations); DOI-aware imports. Exemplars: Listmonk per-list DOI toggle with `subscriber-optin` system template; Keila editable DOI + welcome emails (audit).
- **Acceptance criteria:**
  - [ ] Subscribe with DOI on creates a `pending` member and sends a confirmation; only confirmed members receive campaigns
  - [ ] Hosted confirmation page with HMAC-token scope, expiry error path, no enumeration; expired-token page tested
  - [ ] Confirmation fires the `list.subscribed` trigger (welcome automation E2E test)
  - [ ] Imports honor the list DOI policy (DOI-aware import test); existing subscribers stay as-is (imported confirmed) — no breaking change when DOI is off (default)
  - [ ] Per-list DOI toggle + pending-state display + confirmation/resend flows in the UI
- **Dependencies:** none (hosted tracking pages + email sending exist). Blocks RF-F074 and RF-F073.
- **Evidence / relevant files:** `internal/handlers/subscriber_list_handler.go:373-460` (audit: immediate subscribe) · `internal/services/tracking/tracking.go:273-355` (audit: HMAC token pattern) · `05-implementation-roadmap.md` §4 GAP-25.

### GAP-26 · Add campaign test-send and campaign-scoped preview

- **Priority:** P1
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-26 · CAM-007 · complexity M (1.95) · `05-implementation-roadmap.md` §4 · blocks GAP-34 · feeds GAP-37 (UTM preview)
- **Description:** `POST /campaigns/{id}/test-send` rendering campaign + template + `template_data` + localization to a real inbox, plus a campaign-scoped preview endpoint/UI. Today the template send-test ignores campaign data (`template_handler.go:273`, audit) and no campaign test-send endpoint exists (verified against `6d2d7c0` routes). Resolve content exactly like `campaign_processor.go:290-367` (per-recipient data omitted, sample subscriber injected), reusing `SendTestByTemplateID` (`service.go:1186-1215`).
- **Acceptance criteria:**
  - [ ] `POST /campaigns/{id}/test-send` sends a test email reflecting campaign subject, `template_data` and language
  - [ ] Campaign-scoped preview endpoint returns rendered HTML matching the send pipeline render
  - [ ] Test-send dialog + preview pane in CampaignDetail; editor-gated; preview renders escaped HTML
  - [ ] Endpoints documented; tests cover the render-parity and UI paths
- **Dependencies:** none (template render pipeline + send-test exist). Blocks GAP-34.
- **Evidence / relevant files:** `internal/handlers/template_handler.go:273` (audit: campaign data ignored) · `internal/services/campaign/campaign_processor.go:290-367` (audit: content resolution) · `internal/services/template/service.go:1186-1215` (audit: `SendTestByTemplateID`).

### GAP-27 · Add CSV exports for subscribers, lists, segments, and analytics/message history

- **Priority:** P1
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-27 · CON-010 · ANA-011 · complexity M (2.00) · `05-implementation-roadmap.md` §4 · distinct from RF-F077 (workspace GDPR JSON export, Done)
- **Description:** Close the import/export asymmetry: CSV export for subscribers, lists, segments, and analytics/message history. Only audit-log CSV + GDPR JSON bundle exist today (`logs_handler.go:853,821`, audit). Reuse the existing streamed CSV exporter with the 100k cap (`logs_handler.go:821-892`) directly.
- **Acceptance criteria:**
  - [ ] `GET /subscribers/export`, `/lists/{id}/export`, `/segments/{id}/export` (reusing the segment query builder), `GET /analytics/export?from=&to=` implemented
  - [ ] Exports match list filters and segment definitions row-for-row (tests); cap/streaming honored
  - [ ] All exports scope-gated by existing rules and audit-logged
  - [ ] Export buttons on the respective list views
- **Dependencies:** none (exporter helper exists).
- **Evidence / relevant files:** `internal/handlers/logs_handler.go:821-892` (audit: streamed CSV exporter) · `05-implementation-roadmap.md` §4 GAP-27.

### RF-F040 · Add per-domain inbound routing rules (alias/catch-all/forward targets)

- **Priority:** P1
- **Category:** smtp-relay
- **Workflow status:** To-do
- **Refs:** RF-F040 · `upcomming-convo.md` §6 inbound requirements · module audit (smtp-relay) · control-plane surface for RF-B119
- **Description:** The inbound receiver (RF-F034) and webhook ingest (RF-F035) exist, but routing is not configurable per domain: add per-domain inbound routing rules — alias targets, catch-all policy, and forward targets — managed per workspace domain, with explicit outcomes for unknown users. This is the control-plane configuration the Option B inbound programme (RF-B119) executes.
- **Acceptance criteria:**
  - [ ] Per-domain routing-rule model (alias, catch-all target, forward target) with workspace-scoped CRUD API + UI in Domains
  - [ ] Inbound ingest consults the rules: alias/forward targets receive; catch-all honored; unknown-user outcome explicit (reject/quarantine per policy)
  - [x] Forwarding loop protection (depth/loop detection) and rate limits on forwards — wave W2 of RF-B119 (2026-11-07): hop accounting via `X-Posta-Forwarded-For` on the forwarded copy, derived from the stored original's raw header at execution time (`internal/services/inbound/forwarder.go` `resolveHops`), limit 3 with permanent refusal beyond (spoofed deep chains fail closed); per-target hourly (60)/daily (500) Redis counters (`redisTargetLimiter`, R-06/ratelimit key shape) — wired in `internal/routes/routes.go`; tests `TestForwarderRejectsBeyondMaxForwardHops`, `TestForwarderAllowsForwardAtMaxHopsBoundary`, `TestForwardHopsSpoofedHeaderFailsClosed`, `TestForwarderPerTargetRateCapRetries` in `internal/services/inbound/forwarder_test.go`
  - [ ] Routing decisions recorded in the inbound trail; tests cover alias, catch-all, forward, unknown-user paths
- **Dependencies:** none for the rule surface; full execution guarantees land with RF-B119.
- **Evidence / relevant files:** `internal/services/inbound/`, `internal/handlers/` inbound ingest path · `upcomming-convo.md` §6 (alias/forward/catch-all requirements).

---

## P2 — Competitor parity (Phase 2 batches)

Source: `todo.md` Track B Phase 2 (batches A–E) plus the Option B Phase 6 operations milestones. Serial edges inside P2: RF-F023/RF-F026 (DNS health) → RF-F027 (policy hardening); GAP-26 → GAP-34; RF-F075 → RF-F074.

### RF-F023 · Deepen DNS record guidance (SPF/DMARC parsing, MX records, drift re-verification) (GAP-08 depth)

- **Priority:** P2
- **Category:** domains-dns
- **Workflow status:** To-do
- **Refs:** RF-F023 · GAP-08 · DOM-005 · DOM-006 · DOM-008 · `todo.md` Track B Phase 2 Batch A · `upcomming-convo.md` §8 DNS row · feeds RF-F027
- **Description:** DNS records are verified once (presence checks only — `verifier.go:97-101,126-136`, audit) and can silently rot. Deepen domain health: daily re-verification of DNS records for verified domains with drift alerts, SPF include/qualifier + 10-lookup validation, DMARC policy parsing, and MX record guidance surfaced in the UI. The domain deliverability-health endpoint landed (`7f445f2`, R-07 — see In Progress RF-F026); this task is the remaining depth per GAP-08.
- **Acceptance criteria:**
  - [ ] Daily cron re-runs verifier checks on verified domains, stores last-checked status, raises workspace + admin alerts on drift (removal of a DKIM/SPF record triggers an alert within 24h — tested)
  - [ ] SPF parsing surfaces include/qualifier detail and the 10-lookup count; DMARC policy parsed and displayed; MX records guided
  - [ ] Re-verification stays advisory — never blocks sends (consistent with `IsFullyVerified`)
  - [ ] Last-checked status + SPF/DMARC depth visible in the Domains UI; DNS lookups inside the cron rate-limited
- **Dependencies:** none; feeds RF-F027 (healthy records make strict gating fair); complements In Progress RF-F026.
- **Evidence / relevant files:** `internal/services/verifier/verifier.go:97-101,126-136` (audit: presence checks only) · `internal/cron/jobs/` (10-job fleet) · `05-implementation-roadmap.md` §5 GAP-08.

### RF-F029 · Add custom tracking domain (GAP-09)

- **Priority:** P2
- **Category:** domains-dns
- **Workflow status:** To-do
- **Refs:** RF-F029 · GAP-09 · DOM-009 · complexity M (1.40) · `05-implementation-roadmap.md` §5 · serves RF-F074 embeds
- **Description:** Per-workspace branded tracking hostname (verified) used for all `/t/*` link/pixel/web-view URLs. Tracking runs on the instance domain only today (registry; audit-B). Add a workspace-level `tracking_domain` verified via TXT (reusing `verifier.go:88`); parameterize link-rewrite and pixel base URL in `tracking.go:65-227`; document reverse-proxy host routing. Exemplar: Postal per-workspace tracking domains (audit).
- **Acceptance criteria:**
  - [ ] Workspace can set a `tracking_domain`; verification via TXT with the existing verifier; unverified hosts rejected
  - [ ] Links, pixels and web-view URLs use the workspace domain once verified (token scheme regression-pinned — per-message HMAC tokens unchanged while the base URL varies)
  - [ ] Configuration + verification state in workspace settings UI; reverse-proxy host routing documented
- **Dependencies:** none (domain verification exists).
- **Evidence / relevant files:** `internal/services/tracking/tracking.go:65-227` (audit: URL generation) · `internal/services/verifier/verifier.go:88` (audit: TXT verification) · `05-implementation-roadmap.md` §5 GAP-09.

### RF-F076 · Add subscriber tags (GAP-29)

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** RF-F076 · GAP-29 · CON-006 · complexity M (1.50) · `todo.md` Track B Phase 2 Batch B · pairs with GAP-27 (CSV columns)
- **Description:** Tag taxonomy on subscribers — no `Tag` model exists anywhere (audit-C). Add tag CRUD, bulk tagging, tag filters, and an `in_tags` segment operator added to the guarded segment builder's whitelist; CSV import/export columns. Note: segments cannot reference list membership today (`segment_query.go` operates over subscribers only) — tags must not change that boundary.
- **Acceptance criteria:**
  - [ ] `tags` + `subscriber_tags` join tables (migration); tag CRUD API + management UI in subscriber/list views
  - [ ] Tags assignable singly, in bulk, and via import/export columns
  - [ ] `in_tags` segment operator added to the guarded builder whitelist; passes the injection tests and workspace scoping
  - [ ] Segment-by-tag filtering works end-to-end (subscriber filters + segment drawer tag picker)
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/services/segments/segment_query.go` (guarded builder whitelist) · `05-implementation-roadmap.md` §5 GAP-29.

### GAP-31 · Add bulk contact operations (delete/status/list add-remove by IDs or segment)

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-31 · CON-011 · complexity M (1.80) · `todo.md` Track B Phase 2 Batch B
- **Description:** Bulk delete, status change, and list add/remove by selected IDs or segment. Today: import-only bulk, single-item delete, GDPR purge is whole-workspace (`workspace_data_handler.go:271`, audit). Implement `POST /subscribers/bulk` with `{action, ids|segment_id}` executed in batches with per-action audit events; reuse the `SuppressMember`/`UnsuppressMember` tx patterns (`subscriber_list_repo.go:146-160`, audit).
- **Acceptance criteria:**
  - [ ] Bulk endpoint supports delete, status change, list add/remove, targeted by IDs or `segment_id`
  - [ ] Segment-scoped bulk matches the segment preview count (`347ebb4`'s preview); batch caps prevent unbounded single transactions
  - [ ] Per-action audit events recorded; bulk-selection actions in the subscribers view
- **Dependencies:** segment engine (by-segment targeting).
- **Evidence / relevant files:** `internal/storage/repositories/subscriber_list_repo.go:146-160` (audit) · `05-implementation-roadmap.md` §5 GAP-31.

### GAP-19 · Add contact upsert/sync API

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-19 · CON-012 · complexity S (1.30) · `todo.md` Track B Phase 2 Batch B
- **Description:** Upsert semantics (create-or-update keyed on email) for external sync loops — removes a class of client-side workarounds. Import dedupe + CRUD exist; no explicit upsert endpoint. Extend `POST /subscribers` with an `upsert` flag; on conflict update fields/status per policy; fire `contact.created` only on true create (hook exists at `subscriber_handler.go:162-166`, audit). The subscriber unique index per (workspace,email) exists (`subscriber.go:66-67`, audit).
- **Acceptance criteria:**
  - [ ] `upsert` flag on `POST /subscribers` creates-then-updates the same row; concurrent upserts don't duplicate (unique-index test)
  - [ ] `contact.created` fires only on true create; import path unchanged
  - [ ] Workspace-scoped as existing routes; no migration (index exists)
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/models/subscriber.go:66-67` (audit: unique index) · `internal/handlers/subscriber_handler.go:162-166` (audit: create hook) · `05-implementation-roadmap.md` §5 GAP-19.

### GAP-32 · Add contact activity timeline and unsubscribe-reason reporting

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-32 · CON-019 · CON-015 · complexity M (1.50) · `todo.md` Track B Phase 2 Batch B
- **Description:** Per-subscriber timeline (sends/opens/clicks/bounces/unsubs) in the detail view + unsubscribe-reason breakdowns — a pure read model over existing data. Events live in Logs/messages; the reason column exists (`subscriber_list.go:94`) with no analytics UI. Implement `GET /subscribers/{id}/activity` joining `email_events` by subscriber email + `campaign_messages`; reason histogram aggregation; supporting indexes for the join.
- **Acceptance criteria:**
  - [ ] Activity endpoint returns chronological events with message links; timeline rendered in SubscriberDetail
  - [ ] Unsubscribe-reason aggregation per list/workspace; reason breakdown surfaced
  - [ ] Supporting indexes added; queries workspace-scoped; performance acceptable on indexed paths (tested)
- **Dependencies:** none (`email_events`/`tracking_events` exist).
- **Evidence / relevant files:** `internal/models/subscriber_list.go:94` (audit: reason column) · `05-implementation-roadmap.md` §5 GAP-32.

### GAP-28 · Persist automation `ab_test` node outcomes (GAP-28 residual, AUT-008)

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-28 (residual) · AUT-008 · CAM-008 · complexity S (residual; 2.05) · `todo.md` Track B Phase 2 Batch C · campaign half landed `6d2d7c0` (tracker R-08)
- **Description:** The campaign A/B half (hold-back model, winner selection, exactly-once CAS distribution, stats, UI) landed in `6d2d7c0`. The remaining residual is the automation-engine piece: persist the automation `ab_test` node assignment as an experiment outcome so automation split-tests have durable, inspectable results (today only the campaign scope persists outcomes).
- **Acceptance criteria:**
  - [ ] Automation `ab_test` node persists an experiment outcome per assignment/decision using the existing automation-journey storage patterns
  - [ ] Outcome persisted exactly once per assignment; no duplication on re-runs; stable across retries (tests)
  - [ ] Outcome visible in the automation-builder execution drawer (if surfaced)
- **Dependencies:** per-variant analytics (exists).
- **Evidence / relevant files:** `internal/services/automations/` (journey/node executors) · `05-implementation-roadmap.md` §5 GAP-28.

### RF-F071 · Add automation workflow dry-run / test tooling (GAP-41)

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** RF-F071 · GAP-41 · AUT-018 · complexity M (1.80) · `todo.md` Track B Phase 2 Batch D
- **Description:** `POST /automations/{id}/dry-run` executing the graph against a sample contact with **zero side effects** — the standard pre-live safety complement to the existing static validation (~40 engine tests; no test execution today, audit-G). Run the engine in a side-effect-free mode: no-op node executors + a synthetic journey row flagged `dry_run`; return the traversed path + per-node decisions; `SendAutomationEmail` must be stubbed.
- **Acceptance criteria:**
  - [ ] Dry-run endpoint returns traversed path + per-node decisions for a sample contact
  - [ ] Hard guarantee of no sends, no trigger-log rows, no list mutations, no enrollment — side-effect-free assertions across all node types
  - [ ] Dry-run button + decision trace in the visual builder; synthetic journey rows don't pollute execution history
- **Dependencies:** none (engine execution paths exist).
- **Evidence / relevant files:** `internal/services/automations/` (engine + executors) · `05-implementation-roadmap.md` §5 GAP-41.

### GAP-42 · Add re-engagement and scheduled enrollment triggers

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-42 · AUT-015 · complexity M (1.65) · `todo.md` Track B Phase 2 Batch D
- **Description:** Inactivity-based trigger conditions (last engagement older than N days) evaluated by a cron enrollment sweep — converts the reactive automation engine into a proactive retention tool. Triggers are purely event-reactive today; conditions limited to email/name/status/in_list/custom_fields (`domain.go:632-661`, audit). The engagement subqueries already exist in the segment engine (`segment_query.go:109-131`) — reuse as the condition source; call `Enroll` with the existing once-dedup (`triggers.go:119-133`).
- **Acceptance criteria:**
  - [ ] `schedule` trigger kind evaluated by a daily cron scanning subscribers matching the automation's condition set (segment-engine reuse)
  - [ ] Enrollment honors the once-dedup and the automation's re-enrollment policy; subscribed-elsewhere contacts excluded
  - [ ] Sweep cost bounded per run; the cron is visible in `/admin/jobs`
  - [ ] Schedule-trigger configuration in the builder; inactivity automation enrolls matching subscribers on schedule (tested)
- **Dependencies:** none (engagement subqueries exist). Note: bounded-retries/scheduled-trigger groundwork landed `f5f0e92` — re-verify trigger coverage at HEAD before starting.
- **Evidence / relevant files:** `internal/services/automations/domain.go:632-661`, `triggers.go:119-133` (audit) · `internal/services/segments/segment_query.go:109-131` (audit) · `05-implementation-roadmap.md` §5 GAP-42.

### GAP-43 · Implement stop-on-reply for automations

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-43 · AUT-016 · complexity M (1.15) · `todo.md` Track B Phase 2 Batch D
- **Description:** `ExitOnReply` is a reserved stub the engine never reads; no reply detection exists (audit-G). Implement reply detection — inbound message matched to a sent email via In-Reply-To/References headers or recipient email — and a journey exit hook honoring `ExitOnReply`: on inbound ingest, match the sender email to recent recipients, read `ExitOnReply` on their active journeys, exit transactionally (exit path exists at `automation_handler.go:491-509`, audit). Document relay/deployment prerequisites.
- **Acceptance criteria:**
  - [ ] A reply exits an active journey with reason `reply` (transactional exit path used)
  - [ ] References-header matching with bare-email fallback; non-flagged journeys continue (no false exits on unrelated inbound)
  - [ ] `ExitOnReply` toggle activated in the builder (stub UI exists); deployment prerequisites documented
- **Dependencies:** inbound email pipeline (exists).
- **Evidence / relevant files:** `internal/handlers/automation_handler.go:491-509` (audit: exit path) · `internal/routes/inbound_routes.go` · `05-implementation-roadmap.md` §5 GAP-43.

### GAP-34 · Add reusable blocks/sections library and campaign content slots

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-34 · TPL-002 · TPL-012 · CAM-006 · complexity L (1.15) · `todo.md` Track B Phase 2 Batch C · **after GAP-26** (preview reuse)
- **Description:** Saved block/section entities surfaced as GrapesJS custom blocks, plus campaign-level overrides of template blocks/slots — the biggest editor UX lever, scoped because block libraries are unshipped even by most competitors (TPL-012 absent industry-wide, audit). Today: stock GrapesJS newsletter preset only (`EmailBuilder.vue:41`); no block model/route; template object is atomic. Add a `template_blocks` entity (name, HTML, category) rendered into the GrapesJS block manager on load; campaign-level `content_overrides` JSONB applied post-resolve in `resolveTemplateContent` (`campaign_processor.go:447-536`, audit).
- **Acceptance criteria:**
  - [ ] `template_blocks` entity + CRUD; block-library UI + GrapesJS block-manager integration
  - [ ] A saved block appears for all templates and round-trips template export/import
  - [ ] Campaign `content_overrides` replace the named block at send; overrides render in the campaign preview (GAP-26)
  - [ ] Block HTML sanitized/stored server-side; overrides don't break localization
- **Dependencies:** GAP-26 (campaign preview to render overrides).
- **Evidence / relevant files:** `web/src/components/EmailBuilder.vue:41` (audit) · `internal/services/campaign/campaign_processor.go:447-536` (audit: `resolveTemplateContent`) · `05-implementation-roadmap.md` §5 GAP-34.

### D3 · Consolidate segmentation onto SegmentCondition; convert and deprecate FilterRules

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** D3 · `04-feature-gap-analysis.md` §4 D3 · `todo.md` D1–D9
- **Description:** Two segmentation mechanisms coexist: legacy dynamic lists (`FilterRules`, kept byte-identical, resolved by the campaign worker at `campaign_processor.go:116-152`) and segments (`SegmentCondition` — guarded SQL builder, 14 operators, injection tests, live preview). Consolidate directionally onto segments: add a FilterRules→SegmentCondition converter; present dynamic lists as segments on read; deprecate new dynamic-list creation; keep worker compatibility one release; drop legacy evaluation after parity is confirmed.
- **Acceptance criteria:**
  - [ ] FilterRules→SegmentCondition converter implemented; legacy-parity tests at `segment_query_test.go:660,697` (audit — the translators are half-proven) extended to full coverage
  - [ ] Campaign audiences referencing dynamic lists keep resolving during transition (worker compatibility one release)
  - [ ] New dynamic-list creation deprecated in the UI/API; dynamic lists presented as segments on read
  - [ ] Per-list unsubscribe caveat difference between the paths (`campaign_processor.go:117-121`, audit) documented and behavior-preserved
  - [ ] Legacy evaluation dropped in a versioned step after parity confirmation
- **Dependencies:** none; versioned migration discipline.
- **Evidence / relevant files:** `internal/services/campaign/campaign_processor.go:116-152` (audit: dual resolution) · `internal/services/segments/segment_query_test.go:660,697` (audit) · `04-feature-gap-analysis.md` §4 D3.

### RF-F074 · Add subscriber signup forms (form-to-subscriber capture) (GAP-30)

- **Priority:** P2
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** RF-F074 · GAP-30 · CON-017 · complexity L (1.85) · `todo.md` Track B Phase 2 Batch B (DOI blocks forms) · **after RF-F075**
- **Description:** Hosted/embedded signup forms creating subscribers into a chosen list — success states, DOI hook, bot protection. RF's existing "forms" are website-form→inbox, not audience capture (audit-C classification). Add a `signup_forms` entity (slug, target list, styling) + public route reusing the existing Redis nonce + `MinFillSeconds` time-trap + IP limiter (`form_ingest_handler.go:69-145`, audit) as bot protection; hook the RF-F075 DOI policy. Embed serving via instance or tracking domain (pairs with RF-F029).
- **Acceptance criteria:**
  - [ ] Public form endpoint creates subscribers (pending→confirmed under DOI) into its configured list; private lists not addressable
  - [ ] Nonce + time-trap + IP limiter enforced on submissions; spam submissions triaged like inbound forms
  - [ ] Form builder/list UI + embed snippet; CRUD API for `signup_forms`
  - [ ] Success/error states hosted; E2E form→subscriber→campaign-eligibility test
- **Dependencies:** RF-F075 (DOI hook); embeds pair with RF-F029.
- **Evidence / relevant files:** `internal/handlers/form_ingest_handler.go:69-145` (audit: nonce/time-trap machinery) · `05-implementation-roadmap.md` §5 GAP-30.

### RF-F103 · Expose backend-only endpoints in the UI (segments contacts, message analytics & assign, automation custom-event ingest, admin provider breakdown)

- **Priority:** P2
- **Category:** frontend-integration
- **Workflow status:** To-do
- **Refs:** RF-F103 · module audit (frontend-integration)
- **Description:** Four backend endpoints have no UI consumer and correspondingly dead client functions: segment contacts listing, message analytics + assign, automation custom-event ingest (dashboard side), and the admin provider-breakdown analytics. Expose each in the dashboard or remove the dead client code — every API surface should be reachable or explicitly internal.
- **Acceptance criteria:**
  - [ ] Per-endpoint decision recorded (build UI vs mark internal and drop dead client code)
  - [ ] UI surfaces shipped for the kept endpoints (segment contacts view, message assign action, custom-event ingest docs/trigger UI, provider-breakdown admin panel)
  - [ ] Dead client functions removed for internal-only endpoints; specs updated
- **Dependencies:** none.
- **Evidence / relevant files:** module audit (frontend-integration) endpoint list · `web/src/api/` client modules · `internal/routes/admin_routes.go` (provider breakdown).

### RF-F056 · Add webhook test-fire endpoint (GAP-18 residual)

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** RF-F056 · GAP-18 (residual) · API-013 · complexity S (1.40) · `todo.md` Track B Phase 2 Batch E · replay half landed `6d2d7c0` (tracker R-06)
- **Description:** `POST /webhook-endpoints/{id}/test` dispatching a sample event through the real dispatcher (`dispatcher.go`) with the endpoint's actual filters, appearing in delivery history as a distinguishable synthetic event; audited. Replay landed `6d2d7c0` (SSRF-guarded transport, 5/h per-delivery limit, Replay UI); test-fire is the remaining half — the #1 integration debugging affordance.
- **Acceptance criteria:**
  - [ ] Test-fire endpoint dispatches a sample event through `dispatcher.go` honoring the endpoint's filters; traverses the SSRF guard
  - [ ] The attempt appears in delivery history as a distinguishable synthetic event; audited
  - [ ] Test-fire button on webhook endpoints in the UI
  - [ ] Filters-honored and history-visible tests pass
- **Dependencies:** none (dispatcher + delivery history exist).
- **Evidence / relevant files:** `internal/services/webhook/dispatcher.go` · `05-implementation-roadmap.md` §5 GAP-18 (residual scope).

### RF-F057 · Add bounce classification analytics surface (classification counts) (GAP-07 residual)

- **Priority:** P2
- **Category:** delivery-queue
- **Workflow status:** To-do
- **Refs:** RF-F057 · GAP-07 (residual) · ANA-004 · INF-034 · complexity S (residual; 2.30) · `todo.md` Track B Phase 2 Batch A
- **Description:** The 14-class bounce classifier landed `6d2d7c0` (authority-ordered classification, greylist detection, suppress-only-{hard_bounce, invalid_recipient} policy — greylist never suppresses, pinned by test). The counts live in `email_events` metadata but are "not yet surfaced through any API endpoint" (tracker R-05 limitation). Expose the classification-counts endpoint (extend `IngestionService.ClassificationCounts` exposure) + bounce-analytics hard/soft trend queries; display the split in the UI.
- **Acceptance criteria:**
  - [ ] Classification-counts analytics endpoint live over the `email_events` metadata JSONB aggregation (query shape already pinned in `provider_events_test.go`)
  - [ ] Hard/soft bounce trend over time queryable; greylist regression stays pinned (never suppresses)
  - [ ] Bounce analytics split displayed in the analytics views
  - [ ] Analytics shape tests pass
- **Dependencies:** signal accuracy (GAP-02 core landed; residual parity RF-F053/RF-F099 improves it).
- **Evidence / relevant files:** `internal/services/provider_events/` (audit: `ClassificationCounts`) · `05-implementation-roadmap.md` §5 GAP-07 (residual scope).

### GAP-48 · Add per-queue latency metric and pause/throttle controls

- **Priority:** P2
- **Category:** delivery-queue
- **Workflow status:** To-do
- **Refs:** GAP-48 · OBS-009 · INF-010 (residual note) · complexity M (2.40) · `todo.md` Track B Phase 2 Batch E · folds into RF-B118 (Track A Phase 1)
- **Description:** Per-queue latency histogram (enqueue→process) and pause/throttle controls in the admin UI — rounds out the landed queue-admin story (R-03, `a6f1681`). Emit `posta_queue_latency_seconds` from task payload timestamps; pause/resume actions set a Redis flag checked in producer/worker pre-claim; latency display + pause/resume in the admin Jobs/queues section. Exemplar: Postal per-queue latency (audit).
- **Acceptance criteria:**
  - [ ] `posta_queue_latency_seconds` emitted per queue (transactional/bulk/low); visible in Prometheus + admin
  - [ ] Pausing `bulk` stops bulk claims while `transactional` flows; resume works (tested)
  - [ ] Pause/resume actions admin-gated and audited (R-03 pattern)
- **Dependencies:** asynq Inspector wiring (exists, `admin_queue_handler.go:37-306`, audit).
- **Evidence / relevant files:** `internal/handlers/admin_queue_handler.go:37-306` (audit) · `05-implementation-roadmap.md` §5 GAP-48.

### GAP-45 · Add blob usage reporting and orphan GC

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** To-do — **storage half landed 2026-11-07 in OB-3 wave W2** (ADR-003 decision 2 scope): the mailstore's content-addressed blob layer now has dedup + ref-counting + a grace-perioded orphan GC (dead `mail_blob_refs` rows only after `COALESCE(last_referenced, created_at)` ages past the window; per-hash `pg_advisory_xact_lock` re-check makes a live reference uncollectable; audited `mail_blob_gc` cron summary event) and dedup-aware per-mailbox blob-byte reporting (`mailstore.MailboxUsage`/`MailboxBlobUsageAll` from `mail_blob_refs`) — proven by `TestPGMailBlobGCDropsOnlyAgedOrphans`, `TestPGConcurrentAppendSameContent`, `TestPGAppendDedupRefCountsTwo`, `TestPGReserveSelfHealsMissingBlob`, `TestPGMailboxUsageBlobBytesFromBlobRefs` against real Postgres plus the fake-driver codec/usage suite. The original GAP-45 halves below stay open: they cover the TRANSACTIONAL FS/S3 blob store (`internal/storage/blob/`, Email/InboundEmail attachments), not the mailstore content-addressed namespace.
- **Refs:** GAP-45 · OBS-013 · complexity M (1.45) · `todo.md` Track B Phase 2 Batch E · adjacent to RF-B120 (message storage)
- **Description:** Bytes-per-workspace reporting and an orphan-blob GC (blobs without DB rows) with a dry-run report first. FS/S3 blob store exists and retention deletes blobs; no usage reporting or orphan GC (audit-J); AdminInventory counts rows, not bytes. Nightly job computing per-workspace blob-prefix sizes into a cache surfaced in AdminInventory; GC pass deleting blobs older than a threshold with no referencing row.
- **Acceptance criteria:**
  - [ ] Per-workspace blob usage reported in AdminInventory (bytes, cached nightly)
  - [ ] Orphan GC deletes only blobs older than the threshold with no referencing row — **dry-run report first**, never deletes referenced blobs (tested)
  - [ ] Retention interplay tested (retention deletes and GC don't fight)
- **Dependencies:** retention cron (extends).
- **Evidence / relevant files:** `internal/storage/blob/` · `internal/cron/` retention job · `05-implementation-roadmap.md` §5 GAP-45.

### GAP-49 · Document Redis HA guidance and SPOF mitigation

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-49 · OBS-004 · R10 (tracker) · complexity M (2.40) · `todo.md` Track B Phase 2 Batch E · → OB Phase 6 / ADR-009 (RF-B110) · pairs with audit I-J NV-5 (fault-injection test)
- **Description:** Single Redis is a SPOF across 9 verified roles (broker, cache, rate limits, security windows (fail-open), quotas (fail-closed), JTI blacklist, nonces, heartbeats, pub/sub) with no sentinel/cluster docs (audit-J; risk R10). Deliver documented Sentinel/managed-Redis configuration, a failover behavior matrix per subsystem (citing the verified fail-open/fail-closed callsites), env examples, and a sentinel compose profile. Mostly writing — the single biggest availability lever.
- **Acceptance criteria:**
  - [ ] Redis HA doc covers Sentinel setup, managed-Redis option, each Redis role's failure mode, and recovery steps
  - [ ] Failure-mode matrix cites the verified fail-open (security) / fail-closed (quotas) callsites accurately
  - [ ] Sentinel compose profile boots clean (verified)
  - [ ] Feeds ADR-009 (RF-B110) deployment topology
- **Dependencies:** none.
- **Evidence / relevant files:** `compose.yml` (single Redis) · `06-architecture-impact.md` §6 (Redis considerations), §3 risk R10 · `05-implementation-roadmap.md` §5 GAP-49.

### RF-F092 · Add admin-console IP allowlisting (GAP-16)

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** RF-F092 · GAP-16 · SEC-013 · complexity S (1.80) · `todo.md` Track B Phase 2 Batch E
- **Description:** Optional CIDR allowlist middleware on `/admin/*` (env/setting-driven), fail-open when unset — defense-in-depth for the most privileged surface. The ban-engine allowlist is a detector exemption, not access control; no middleware beyond JWT+role exists (audit-I). Implement an `AdminIPAllowlist` setting checked in the admin middleware chain (`middleware.go:159-161`), reusing `IPAllowed` (`middleware.go:263-279`); settings field in the UI.
- **Acceptance criteria:**
  - [ ] Allow/deny enforced including CIDR ranges; 403 from non-allowlisted sources when configured
  - [ ] Unset = current behavior (fail-open; tested); trusted-proxy header handling matches existing semantics
  - [ ] Settings field shipped; documented as opt-in (reverse proxy remains an alternative)
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/middlewares/middleware.go:159-161,263-279` (audit: admin chain + `IPAllowed`) · `05-implementation-roadmap.md` §5 GAP-16.

### RF-F097 · Add error-monitoring hook (GAP-51)

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** RF-F097 · GAP-51 · OBS-011 · complexity S (2.05) · `todo.md` Track B Phase 2 Batch E · risk R14
- **Description:** Errors are slog'ed only (grep clean, audit) — failures are visible only as per-email failed states. Add optional Sentry-compatible error capture (DSN env): a minimal `slog` handler forwarding error-level records (sampled, scrubbed of PII/secrets) to a Sentry DSN when configured; plus a `posta_errors_total` Prometheus family so aggregate error visibility exists even without a DSN.
- **Acceptance criteria:**
  - [ ] With DSN set, errors appear in Sentry with release/commit tags; sampled and scrubbed (no PII/secrets — test)
  - [ ] `posta_errors_total` metric counts match error logs (metrics test)
  - [ ] Unset DSN = zero behavior change (no-op test)
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/observability/metrics.go` (metric families) · `05-implementation-roadmap.md` §5 GAP-51.

### A-B NV-2 / G-H NV-4 · Add runtime authorization verification tests (admin queue scoping, API-key/admin isolation)

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** A-B NV-2 · G-H NV-4 · `04-feature-gap-analysis.md` §5 (NV register) · `todo.md` Sources (04 §5 NV register)
- **Description:** Two Open items in the NEEDS_VERIFICATION register concern runtime authorization guarantees that are only structurally verified: (1) A-B NV-2 — do `/admin/queues/*` routes reject non-admin API keys? (2) G-H NV-4 — "API keys can never reach `/admin/*`" is structurally verified but not runtime-tested. Write the runtime tests in the `workspace_rbac_test.go` style: a scope test asserting non-admin API keys get 403 on `/admin/queues/*`, and a runtime test asserting a `psk_` key gets 401/403 on an `/admin/*` route.
- **Acceptance criteria:**
  - [ ] Runtime test: non-admin-scoped API key receives 403 on `/admin/queues/*` actions (A-B NV-2 resolved)
  - [ ] Runtime test: `psk_` API key receives 401/403 on an `/admin/*` route (G-H NV-4 resolved)
  - [ ] Both tests pinned in CI (`go test ./...`); NV register entries updated to Resolved with the test as evidence
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/routes/contract_test.go` (route-contract test pattern) · `internal/storage/repositories/workspace_rbac_test.go` (RBAC test style) · `04-feature-gap-analysis.md` §5 rows A-B NV-2, G-H NV-4.

### RF-B124 · Option B Phase 6 — Automate DNS provisioning and certificate lifecycle (ACME/SNI per-domain TLS)

- **Priority:** P2
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §7 Phase 6 · `todo.md` Track A Phase 6 · DOM-007 · DOM-010 · ADR-007 (RF-B108) · reinstated by the Option B decision
- **Description:** Automated DNS provisioning and per-domain TLS certificate lifecycle for the mail platform: ACME issuance/renewal with SNI for inbound/multi-domain TLS, and (where the deployment shape allows) DNS automation for required records. Reinstated from the relay-only exclusion list by Option B (DOM-007/DOM-010 were excluded because relay-only sending uses the upstream relay's cert; owning inbound/MTA changes that).
- **Acceptance criteria:**
  - [ ] Certificate lifecycle automated (ACME) with SNI serving per domain for the inbound/MTA listeners per ADR-007
  - [ ] Renewal, revocation and failure handling defined and tested; expiry monitoring/alerting
  - [ ] DNS provisioning automation scoped per ADR-007 (or the copy-paste `RequiredRecords` path explicitly kept, with the decision recorded)
- **Dependencies:** RF-B108 (ADR-007), RF-B118/RF-B119 (listeners that terminate TLS).
- **Evidence / relevant files:** `upcomming-convo.md` §7 Phase 6 · `05-implementation-roadmap.md` §9 entry 13 (exclusion superseded) · `internal/services/verifier/` (record guidance today).

### RF-B125 · Option B Phase 6 — Complete deployment, upgrade, backup, and DR tooling

- **Priority:** P2
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §7 Phase 6, §8 (data durability row) · `todo.md` Track A Phase 6 · ADR-009 (RF-B110) · builds on RF-F095 (pg_dump backup, Done) · folds GAP-49 outcome
- **Description:** Complete the operational tooling for the converged platform: deployment (single-node per ADR-009), upgrade safety (versioned migration steps + rollback), backup beyond Postgres (blob/message stores, mailbox storage), disaster-recovery tooling with recovery testing, replication guidance. The `pg_dump` CronJob + restore runbook (RF-F095, `7f445f2`) is the Postgres foundation; this task extends coverage to the new data planes and closes the loop with end-to-end operational testing.
- **Acceptance criteria:**
  - [ ] Upgrade procedure with versioned migration steps + tested rollback; upgrade safety documented
  - [ ] Backup coverage extended to blob/message and mailbox stores per ADR-009; restore drills reproduce a working instance
  - [ ] DR runbook with RPO/RTO expectations and a tested recovery path; Redis HA guidance (GAP-49) incorporated
  - [ ] Deployment docs match the supported topologies in ADR-009
- **Dependencies:** RF-B110 (ADR-009), GAP-49; builds on RF-F095.
- **Evidence / relevant files:** `internal/services/backup/` + restore runbook (RF-F095) · `upcomming-convo.md` §8 (data durability) · `todo.md` Track A Phase 6.

---

## P3 — Long tail (Phase 3)

Source: `todo.md` Track B Phase 3 (4 groups: developer surface, marketing tail, admin tail, docs & observability tail) plus the register/consolidation housekeeping and the Option B Phase 6 evaluation item. No item blocks another; pull forward opportunistically.

### RF-F073 · Build preference center (multi-list subscription management) (GAP-35)

- **Priority:** P3
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** RF-F073 · GAP-35 · CON-016 · complexity M (0.80) · `todo.md` Track B Phase 3 · **builds on RF-F075** (hosted pages)
- **Description:** Hosted page where a recipient toggles list memberships/language, linked from unsubscribe pages — consent UX maturity beyond one-click. Only a forward-reference comment exists today (`suppression.go:26`, audit). Token-authenticated preferences view listing public lists; toggles write `SubscriberListUnsubscribe` via the existing `SuppressMember`/`UnsuppressMember`; page CSP matches the `/t/v` hardening.
- **Acceptance criteria:**
  - [ ] Hosted preferences page reachable from unsubscribe pages; HMAC token scope limits toggles to that recipient
  - [ ] Recipient can opt out of specific lists and back in; changes reflect in campaign eligibility (tested)
  - [ ] Language preference togglable; page CSP hardened as `/t/v`
- **Dependencies:** RF-F075 (DOI/pages foundation).
- **Evidence / relevant files:** `internal/services/suppression/suppression.go:26` (audit: forward reference) · `internal/services/tracking/tracking_pages.go` (hosted-page pattern) · `05-implementation-roadmap.md` §6 GAP-35.

### RF-F061 · Add UTM auto-tagging of campaign links (GAP-37)

- **Priority:** P3
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** RF-F061 · GAP-37 · CAM-015 · complexity S (1.05) · `todo.md` Track B Phase 3 · preview via GAP-26
- **Description:** Per-campaign UTM template appended to external links before the tracking rewrite (the reference workflow platform is the only peer, audit). Campaign-level `utm_params` JSONB; in `ProcessHTML`, append to outbound hrefs skipping `/t/`, mailto, tel (same skip list as the rewrite) before rewrite; disabled by default.
- **Acceptance criteria:**
  - [ ] Enabled campaign links carry utm_source/medium/campaign; skip list respected (never tag internal/tracked/mailto/tel — tested)
  - [ ] Default off; UTM fields on campaigns; tagged links visible in the campaign preview (GAP-26)
- **Dependencies:** none hard (pairs with GAP-26 preview).
- **Evidence / relevant files:** `internal/services/tracking/tracking.go:65-155` (audit: click-rewrite pipeline) · `05-implementation-roadmap.md` §6 GAP-37.

### GAP-36 · Complete template authoring: preview text, Markdown part, media library (verify-first gate)

- **Priority:** P3
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-36 · TPL-016 · TPL-003 · TPL-022 · complexity M (0.95) · `todo.md` Track B Phase 3 · **verify-first: resolve the TPL-016/TPL-022 NV items before any build**
- **Description:** Three authoring conveniences: preheader field injected into rendered HTML (TPL-016 NEEDS_VERIFICATION — unverified), Markdown→HTML authoring option (TPL-003 MISSING), media library over the blob store (TPL-022 NEEDS_VERIFICATION — media surface not audited). Dependencies exist (html2text; FS/S3 blob store), but verification comes first: render-audit the preheader path and audit the `EmailBuilder.vue` image pickers before building.
- **Acceptance criteria:**
  - [ ] TPL-016 and TPL-022 NV items resolved with evidence (grep/render audit of the render pipeline and editor pickers); scope adjusted to findings
  - [ ] `preheader` field rendered as hidden preview text after `<body>`; Markdown localization option through a small Go markdown lib **with mandatory sanitizer**, auto-generating the text part
  - [ ] `POST /media` upload with per-workspace blob keys; media list/delete with blob-backed URLs; media picker in both editors
  - [ ] All behind the existing render pipeline; per-workspace media scoping enforced
- **Dependencies:** NV-REGISTER items TPL-016/TPL-022 (verify-first gate).
- **Evidence / relevant files:** `04-feature-gap-analysis.md` §5 (TPL-016, TPL-022 rows) · `web/src/components/EmailBuilder.vue` (audit: editor surface) · `05-implementation-roadmap.md` §6 GAP-36.

### GAP-38 · Add newsletter archive and publications

- **Priority:** P3
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-38 · CAM-012 · CAM-013 · complexity L (0.75) · `todo.md` Track B Phase 3 · design risk: recipient-variable leakage
- **Description:** Opt-in public archive of sent campaigns (recipient-independent) + publication grouping entity. Web-view tokens are per-recipient + expiring today; campaigns are flat. Add `publication` grouping + per-campaign `public` flag; archive page rendering with archive metadata substituting a metadata object for `.Subscriber` (Listmonk pattern, audit); unsub links stripped from archive copies. **Recipient-variable leakage is the design risk — leakage review mandatory.**
- **Acceptance criteria:**
  - [ ] Published campaigns visible without tokens; unpublished 404; publications group/filter campaigns in the UI
  - [ ] No subscriber-variable rendering on archive pages; unsub links absent (leakage review checklist passed and documented)
  - [ ] Archive URLs carry no per-recipient tokens; noindex default; CSP as `/t/v`
- **Dependencies:** web-view rendering (exists).
- **Evidence / relevant files:** `internal/handlers/tracking_handler.go:345-371` (audit: web-view rendering) · `05-implementation-roadmap.md` §6 GAP-38.

### GAP-40 · Add device/geo breakdowns (ANA-015)

- **Priority:** P3
- **Category:** marketing-automation
- **Workflow status:** To-do
- **Refs:** GAP-40 · ANA-015 · complexity M (0.75) · `todo.md` Track B Phase 3 · privacy trade-off needs explicit decisions · coordinate with RF-F100
- **Description:** UA parsing (device/client) and IP-geo enrichment on tracking events with breakdown endpoints (the reference workflow platform is the only shipped peer, audit). Store parsed `ua_device`/`ua_client` on `tracking_events` at record time (no retro-parse); aggregate in the analytics-repo pattern (`analytics_repo.go:49-330`); geo via an optional local mmdb. Privacy: no raw-IP retention beyond current windows; IP truncation by default considered; toggle-able — decisions recorded explicitly.
- **Acceptance criteria:**
  - [ ] Open/click breakdowns by device class and client available via API + campaign-analytics panels
  - [ ] Privacy decisions recorded (IP truncation, retention) and enforced; no raw-IP retention beyond current windows
  - [ ] Provider breakdown unaffected (regression); additive tracking-event columns only
- **Dependencies:** none; privacy posture coordinated with RF-F100.
- **Evidence / relevant files:** `internal/storage/repositories/analytics_repo.go:49-330` (audit) · `05-implementation-roadmap.md` §6 GAP-40.

### GAP-17 · Add global registration control toggle (SEC-017)

- **Priority:** P3
- **Category:** auth-org
- **Workflow status:** To-do
- **Refs:** GAP-17 · SEC-017 · complexity S (1.50) · `todo.md` Track B Phase 3 (admin tail) · kept P3 by class override despite P2-level score
- **Description:** Instance-wide open/closed registration toggle in admin settings. Today only the verified-email gate exists on workspace create/invites/keys (audit). Settings-driven gate in the registration route (`routes/routes.go` signup path) reusing the settings-provider pattern; 403 when closed with a clear error; admin Settings toggle.
- **Acceptance criteria:**
  - [ ] Toggle off → registration returns 403 with a clear error; default preserves behavior
  - [ ] Existing users, invitations, and API-key creation unaffected (tested)
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/routes/routes.go` (signup path) · `internal/services/settings/provider.go` (settings pattern) · `05-implementation-roadmap.md` §6 GAP-17.

### GAP-20 · Deepen SMTP diagnostics (EHLO capability dump, TLS cert summary, redacted transcript)

- **Priority:** P3
- **Category:** smtp-relay
- **Workflow status:** To-do
- **Refs:** GAP-20 · INF-033 · ANA-013 · complexity M (1.30) · `todo.md` Track B Phase 3 (→ OB Phase 1 ops) · complements the landed dial timeouts (R-22, `a6f1681`)
- **Description:** Extend `TestConnection` (`smtp_sender.go:186-203`) to store EHLO lines + peer certificate info; optional last-dialogue capture behind a settings flag; richer server-test response display. Last-error text + `smtp_code`/`smtp_stage` metadata exist; no transcript today (audit).
- **Acceptance criteria:**
  - [ ] Test response includes EHLO capabilities + TLS version/cert subject (tested)
  - [ ] Optional conversation capture setting-gated; transcripts redacted (credentials/PII), retention-covered, size-bounded
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/services/queue/smtp_sender.go:186-203` (audit) · `05-implementation-roadmap.md` §6 GAP-20.

### GAP-21 · Add message tagging/correlation (tags on emails)

- **Priority:** P3
- **Category:** delivery-queue
- **Workflow status:** To-do
- **Refs:** GAP-21 · INF-032 · complexity M (0.85) · `todo.md` Track B Phase 3
- **Description:** Free-form tags on emails accepted at send, filterable in logs, exposed in analytics breakdowns — a correlation layer beyond `ExternalID` (`service.go:343`, audit). `tags text[]` on `emails` accepted at send; filterable in logs endpoints (ILIKE search exists in `logs_handler.go`); analytics grouping; tag display/filter in message views.
- **Acceptance criteria:**
  - [ ] Send payloads accept `tags`; stored, returned, filterable (tested)
  - [ ] Logs filtering indexed/scoped (GIN index on the array column); analytics group-by tags
  - [ ] Retention unchanged; additive migration only
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/services/email/service.go:343` (audit: ExternalID) · `internal/handlers/logs_handler.go` (audit: ILIKE search) · `05-implementation-roadmap.md` §6 GAP-21.

### GAP-22 · Ship an operator CLI against the REST API

- **Priority:** P3
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-22 · API-017 · complexity M (0.65) · `todo.md` Track B Phase 3
- **Description:** `posta` client subcommands (send, keys, domains) against the REST API, shipped in the same image — only server/worker/doctor subcommands exist today (audit). `cmd/posta-cli` using the documented SDK envelope; single-binary ethos preserved; `psk_` key auth only, no token storage beyond the operator's environment.
- **Acceptance criteria:**
  - [ ] Send / api-key / domain CRUD subcommands usable with `psk_` auth (tested against a running instance)
  - [ ] Documented in the SDK overview; ships in the same image
- **Dependencies:** none.
- **Evidence / relevant files:** `cmd/posta/` (existing subcommands) · `05-implementation-roadmap.md` §6 GAP-22.

### GAP-23 · Verify SDK completeness (PyPI/NuGet packages + route coverage)

- **Priority:** P3
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-23 · API-008 · G-H NV-1 · G-H NV-3 · complexity S (0.85) · `todo.md` Track B Phase 3 · coordinate with GAP-14's signature-timestamp change
- **Description:** Confirm (or publish) the Python (`posta`, PyPI) and .NET (`Posta`, NuGet) packages and state SDK coverage of the current route set. Go/Node/PHP/Java confirmed; Python/.NET unconfirmed (audit-H §5 item 1). Out-of-tree repos cannot be verified from this codebase — this is an install-check + coverage-statement task. Coordinate the `constructEvent` signature-timestamp helper update with GAP-14 in lockstep.
- **Acceptance criteria:**
  - [ ] PyPI/NuGet install checks run (or packages published); G-H NV-1 resolved with evidence
  - [ ] Coverage check vs the current route set (or documented lag); G-H NV-3 resolved; docs note the coverage level
  - [ ] Signature-verification helpers updated for the GAP-14 timestamp header (grace window documented)
- **Dependencies:** GAP-14 (header change to reflect).
- **Evidence / relevant files:** `04-feature-gap-analysis.md` §5 (G-H NV-1, G-H NV-3) · `internal/handlers/webhook_docs.go` · `05-implementation-roadmap.md` §6 GAP-23.

### GAP-24 · Ship an MCP server over the REST API

- **Priority:** P3
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-24 · API-016 · complexity M (0.85) · `todo.md` Track B Phase 3 · CA speculative (synthesis §4 note)
- **Description:** A Model-Context-Protocol server exposing read + send actions over the existing REST API — thin wrapper, zero MCP references today (grep clean, audit). API-key auth only; read + send scopes only; no new auth surface; write/admin scopes excluded by default.
- **Acceptance criteria:**
  - [ ] An MCP client can list/send/query using an API key
  - [ ] Scope exclusion (no write/admin tools by default) pinned by test
- **Dependencies:** none (API keys/scopes exist).
- **Evidence / relevant files:** `internal/routes/` (REST surface consumed) · `05-implementation-roadmap.md` §6 GAP-24.

### GAP-47 · Add antivirus (ClamAV) hook to the inbound pipeline

- **Priority:** P3
- **Category:** smtp-relay
- **Workflow status:** Done (2026-11-07 — wave W5 of the OB-2 programme; the implementation went through `internal/services/spamfilter` + the ingest worker per ADR-006 rather than a pre-blob-storage hook — durable-first ordering kept, see the RF-B119 spam/malware criterion and STATUS_HISTORY for evidence)
- **Refs:** GAP-47 · SEC-011 · complexity M (1.25) · `todo.md` Track B Phase 3 (→ OB Phase 2 malware scanning) · folds into RF-B119
- **Description:** Optional ClamAV (clamd) scan of inbound attachments before blob storage/notification (Postal is the only peer, audit). Setting-gated clamd TCP scan post-parse; infected attachments quarantined (blob retained, message flagged spam) reusing the message triage UI. Scan disabled by default; scanner-outage fails open with a security event; latency budgeted.
- **Acceptance criteria:**
  - [x] EICAR test attachment quarantined and flagged (tested); disabled by default — wave W5 (2026-11-07): scanning off unless `POSTA_CLAMAV_HOST` is set (`spamfilter.ClamdClient.Enabled`); INSTREAM client + in-process fake clamd (`internal/services/spamfilter/clamav_test.go` — `TestClamdScanInfected` with the `Eicar-Test-Signature` signature, `TestClamdScanClean`, parser table); end-to-end positive → quarantine with signature recorded: `internal/worker/inbound_filter_test.go` `TestInboundFilterMalwarePositiveQuarantines` (status quarantined, `threat_signature` stamped, `inbound.malware_detected` + `inbound.quarantined` events)
  - [x] Scanner-outage path fails open with a security event; latency budget documented — fail-open AND fail-closed both implemented and tested (`POSTA_CLAMAV_FAIL_MODE`, default open): `TestInboundFilterFailOpenProceedsOnOutage` / `TestInboundFilterFailClosedQuarantinesOnOutage` (closed mode quarantines as `clamd_unavailable` — the conservative reading of "with a security event" for an ingest worker, where the event substrate is the delivery record); latency budget: scans run in the ingest worker, never the SMTP session (ADR-006 performance note; durable ingest precedes any scan), per-scan timeout `POSTA_CLAMAV_TIMEOUT` (default 30s) and a 25 MiB stream cap mirroring clamd's `StreamMaxLength` (oversized samples skip the scan, logged — `TestClamdOversizedSampleSkipped`, `TestClamdDeadlineTerminatesStuckServer`)
  - [x] Quarantine/triage display reuses the existing message UI — positives land in the same `inbound_quarantine` store W3 built (via the `QuarantineStore` seam with a nil route and a malware reason string) and surface through its list/release/delete review surface; the dashboard quarantine view (`web/src/views/inbound/Quarantine.spec.ts`) lists holds regardless of reason
- **Dependencies:** inbound parse pipeline (exists); integrates into RF-B119 when that phase lands.
- **Evidence / relevant files:** `internal/services/inbound/service.go` · `05-implementation-roadmap.md` §6 GAP-47.

### GAP-52 · Ship a Grafana sample dashboard

- **Priority:** P3
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-52 · OBS-007 · complexity S (1.20) · `todo.md` Track B Phase 3
- **Description:** One bundled Grafana dashboard JSON covering the 20 metric families + docs enabling `POSTA_METRICS_ENABLED` — one file, real operator polish (Hyvor Relay is the only peer, audit). Metrics exist but are default-off and not surfaced in the UI (risk R14).
- **Acceptance criteria:**
  - [ ] Dashboard JSON imports clean against a running instance; panels map 1:1 to the documented metric families
  - [ ] Docs cover enabling `POSTA_METRICS_ENABLED` and importing the dashboard
- **Dependencies:** metrics (exist, default-off).
- **Evidence / relevant files:** `internal/observability/metrics.go` · `05-implementation-roadmap.md` §6 GAP-52.

### GAP-53 · Add runtime log viewer and resource history

- **Priority:** P3
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** GAP-53 · OBS-010 · OBS-012 · complexity M (0.90) · `todo.md` Track B Phase 3
- **Description:** In-product tail of recent app logs (bounded ring buffer) + historical resource stats (CPU/disk, 24h samples). Today the SSE `system.status` carries NumGoroutine+MemStats only (audit). Bounded in-memory ring buffer of slog records (level-filtered) exposed via admin SSE alongside `system.status`; optional 24h sample persistence; log viewer + resource-history chart in admin.
- **Acceptance criteria:**
  - [ ] Admin can stream/tail level-filtered app logs; ring buffer memory-capped (bounds respected — tested)
  - [ ] Resource history charted for 24h; no secrets logged (scrubbing verified)
- **Dependencies:** none.
- **Evidence / relevant files:** admin SSE `system.status` channel (`internal/routes/admin_routes.go:735` area) · `05-implementation-roadmap.md` §6 GAP-53.

### NV-REGISTER · Close the open NEEDS_VERIFICATION register (correctness/ops items)

- **Priority:** P3
- **Category:** platform-admin-security
- **Workflow status:** To-do
- **Refs:** `04-feature-gap-analysis.md` §5 (NV register) · `todo.md` Sources (04 §5)
- **Description:** Sweep the §5 NEEDS_VERIFICATION register: 30 items at freeze (27 Open/folded, 2 Resolved, 1 landed-pending-re-audit). Folded items (→ GAP-nn, → D1, → GAP-03) resolve automatically when their gap's acceptance criteria are met — this task covers the **Open** items not owned by another backlog entry: A-B NV-3 (DKIM signing on the shared-server path — maintainer decision + test), A-B NV-6 (queue-weight benchmark/load test under sustained campaign load, then document weighted semantics), C-D NV-2 (gradual-send pacing accuracy), C-D NV-3 (`scheduled_at` update semantics post-scheduling — rejection test), C-D NV-4 (100k-row CSV import memory benchmark; import streaming), C-D NV-6 (TaskID dedup window vs `emails` row deletion/retention), E-F NV-2 (delete-active-version guard test), E-F NV-3 (`emails_queue_size` gauge call-site test), E-F NV-4 (segment preview response shape vs `347ebb4` claim), E-F NV-5 (uncommitted migration edits purpose), I-J NV-1 (`user.login` retention quality decision), I-J NV-3 (worker `/readyz` under graceful shutdown), I-J NV-4 (workspace SSO enforcement E2E), I-J NV-5 (security-engine fail-open scope fault-injection test), I-J NV-6 (`provider_events`/replay re-audit at HEAD). A-B NV-2 / G-H NV-4 are handled separately (see the P2 task).
- **Acceptance criteria:**
  - [ ] Every Open NV row dispositioned: resolved with recorded evidence, folded into a backlog task (noted), or explicitly accepted-as-documented
  - [ ] Maintainer-decision items (A-B NV-3, I-J NV-1) decided and the decision recorded in the repo docs
  - [ ] Register table in `04-feature-gap-analysis.md` §5 (or a successor doc) updated with per-row outcomes; no row left Open without an owner
- **Dependencies:** none (sweep task; individual items may cite it).
- **Evidence / relevant files:** `docs/product-roadmap/04-feature-gap-analysis.md` §5 (the register) · `todo.md` Sources table.

### D2/D5/D6/D8 · Frontend consolidation batch (audit-UI scope switcher, MessageDetail merge, ChangePassword removal, Suppressions nav)

- **Priority:** P3
- **Category:** frontend-integration
- **Workflow status:** To-do
- **Refs:** D2 · D5 · D6 · D8 · `04-feature-gap-analysis.md` §4 · `todo.md` D1–D9
- **Description:** Four UI consolidations, each small and independent: **D2** — one audit UI with a scope switcher (workspace/deployment/platform) over the existing `events` table, replacing `AuditLog.vue`/`AuditLogs.vue`/`Events.vue` (enforce all three permission gates in the switcher; CSV export stays on the deployment surface); **D5** — merge retry + status into MessageDetail, retire the Emails views after a transition release (keep the `/emails` API for the SDK; preserve the dev-mode listing admin-only; Bounces stays a filtered lens); **D6** — delete `ChangePassword.vue` + its route entry, keep the existing redirect to Profile (`web/src/router/index.ts:112`); **D8** — promote Suppressions to its own nav entry (keep the Bounces tab).
- **Acceptance criteria:**
  - [ ] D2: single audit view with scope tabs; all three permission gates enforced; deep links updated; CSV export retained
  - [ ] D5: retry + status actions available in MessageDetail; Emails views retired in a transition release; `/emails` API unchanged
  - [ ] D6: `ChangePassword.vue` removed, redirect kept, one E2E test on the Profile password flow
  - [ ] D8: Suppressions is a first-class nav entry
- **Dependencies:** none (independent of each other and of the P0–P2 tasks).
- **Evidence / relevant files:** `04-feature-gap-analysis.md` §4 D2, D5, D6, D8 (canonical implementations + migration risks) · `web/src/views/` (audit surfaces).

### D4/D7/D9 · Document intentional dualities (analytics split, tracking/email events, admin SSE channels)

- **Priority:** P3
- **Category:** frontend-integration
- **Workflow status:** To-do
- **Refs:** D4 · D7 · D9 · `04-feature-gap-analysis.md` §4 · `todo.md` D1–D9
- **Description:** Three verified dualities are deliberate — document, don't merge: **D4** — workspace vs admin analytics endpoints (same `AnalyticsRepository`, scoped WHERE clauses; deliberate ops/workspace split) → document the split in the API docs; **D7** — `tracking_events` (campaign time-series, first-per-link dedup) vs `email_events` (message-history timeline, day-bucket) → document both in the API docs and retention matrix; do **not** merge in the roadmap window; revisit only if GAP-40 forces it; **D9** — three admin SSE channels (`/admin/events/stream`, `/admin/security/events`, `/admin/metrics/stream`) acceptable as-is → note only; if touched, multiplex one connection with typed channels.
- **Acceptance criteria:**
  - [ ] D4 split documented in the API docs (identical metric definitions via the shared repo stated)
  - [ ] D7 dedup-semantics split documented in the API docs + retention matrix; no merge
  - [ ] D9 noted in the admin docs (with the multiplexing option recorded for future work)
- **Dependencies:** none.
- **Evidence / relevant files:** `04-feature-gap-analysis.md` §4 D4, D7, D9 · `internal/storage/repositories/analytics_repo.go`, `internal/storage/repositories/tracking_repo.go:98-105` (audit), `internal/cron/retention.go:401-437` (audit).

### RF-B126 · Evaluate JMAP and advanced integrations on validated demand

- **Priority:** P3
- **Category:** architecture (Option B programme)
- **Workflow status:** To-do
- **Refs:** `upcomming-convo.md` §4 (JMAP decision), §7 Phase 6 · `todo.md` Track A Phase 6 · INF-003
- **Description:** Keep JMAP as a separately evaluated capability: the audit identifies IMAP/POP and webmail under INF-003, but JMAP appears in the exclusion discussion, not as a verified requirement in the frozen 199-feature inventory — do not silently treat it as already researched or required. Evaluate on validated demand after the mailbox backend (RF-B120) and IMAP/POP3 (RF-B121) exist, alongside any other advanced-integration candidates surfaced by then.
- **Acceptance criteria:**
  - [ ] Demand evidence collected (or absence recorded) before any build decision
  - [ ] Evaluation memo: fit with the mailbox backend, candidate implementations with license verification, cost/benefit — or an explicit not-now decision recorded
- **Dependencies:** RF-B120, RF-B121 (evaluation is meaningful only against a real mailbox backend).
- **Evidence / relevant files:** `upcomming-convo.md` §4 (JMAP paragraph) · `05-implementation-roadmap.md` §9 entry 2 (exclusion superseded by Option B).

---

## In Progress

Ten tasks with registry status **In Progress** (listed here only — their priority is recorded per task; do not duplicate them into the priority sections). Each carries what remains to close it out.

### RF-F104 · Campaign-level failed-recipient retry (GAP-33 residual — duplication UI landed)

- **Priority:** P2
- **Category:** frontend-integration
- **Workflow status:** In Progress
- **Refs:** RF-F104 · GAP-33 · CAM-004 · CAM-018 · module audit (frontend-integration) · `todo.md` Track B Phase 2 Batch C
- **Description:** The duplication half landed 2026-11-07 (P3-5 misc wave, CA-02): `campaignsApi.duplicate` + a Duplicate action in CampaignDetail now call the existing `POST /campaigns/{id}/duplicate`. What remains is campaign-level failed-recipient retry: `POST /campaigns/{id}/retry-failed` selecting `failed` `campaign_messages` since a timestamp, re-enqueued via the batch producer (deterministic-TaskID dedup interplay). Exemplar: the reference workflow platform `broadcasts.retryFailed` holding failures 7 days (audit).
- **Acceptance criteria:**
  - [x] Duplicate creates a fresh editable draft copying audience/settings; Duplicate action wired in CampaignDetail + API client (landed 2026-11-07 — `web/src/api/campaigns.ts` `duplicate()`, `web/src/views/campaigns/CampaignDetail.vue` `duplicateCampaign()`)
  - [ ] `POST /campaigns/{id}/retry-failed` re-sends only failed messages once (dedup-safe, tested); optional since-timestamp filter
  - [ ] Both actions editor-gated and audited; snapshot analytics unaffected (duplicate is editor-gated; retry-failed still open)
- **Dependencies:** per-message retry + CampaignMessage ledger (exist).
- **Evidence / relevant files:** `internal/handlers/campaign_handler.go:466` (audit: backend-only duplicate — wired now) · `internal/routes/workspace_resource_routes.go:116` (audit: per-message retry) · `05-implementation-roadmap.md` §5 GAP-33.

### RF-F053 · Finish legacy `/webhooks/bounce` parity: suppression, bounce rows, complaint semantics (GAP-02 residual)

- **Priority:** P0
- **Category:** delivery-queue
- **Workflow status:** In Progress
- **Refs:** RF-F053 · GAP-02 (residual) · ANA-003 · ANA-005 · INF-021 · API-014 · architecture risk R6 · `todo.md` Track B Phase 0 (GAP-02 residual) · `04-feature-gap-analysis.md` §2.1 · → OB Phase 2 (bounce/return-path, RF-B119)
- **Description:** The provider-event core landed (`6d2d7c0`), but the legacy `/webhooks/bounce` path still neither suppresses nor records a `bounces` row, and rejects complaint semantics (hard/soft only, `bounce_webhook_handler.go:89-92`, audit). Verified at HEAD: the handler only marks the subscriber bounced for hard bounces (`internal/handlers/bounce_webhook_handler.go:127`). Make `/webhooks/bounce` reuse the authenticated `POST /bounces` side-effect chain (`bounce_handler.go:91-138`, audit): suppression upsert, hard-bounce row, complaint acceptance with the complaint kind (reuse `SuppressionKindComplaint` — the landed provider-event complaint chain also marks subscribers without upserting a suppression row; close that too). Preserve the landed security posture (signature verification, constant-time compares, 2 MiB cap — nothing unverifiable persisted). Remaining GAP-02 residual sub-items ride here: retention verification of `incoming_webhook_events` raw payloads, optional per-workspace provider-config UI, ARF/FBL parsing (stretch).
- **Acceptance criteria:**
  - [ ] Legacy `/webhooks/bounce` suppresses exactly like the authenticated `POST /bounces` (parity test) and records the bounce row
  - [ ] Complaint payloads accepted: suppression with kind=complaint + `complained` email_event + `email.complained` webhook (with RF-F099's emission)
  - [ ] ESP-specific payload parity for the legacy path (real-world SES/Postmark/Mailgun/SendGrid fixtures — resolves audit G-H NV-5)
  - [ ] Retention coverage of `incoming_webhook_events` raw payloads verified against the retention matrix
  - [ ] Optional: per-workspace provider-config UI (landed config is install-wide env secrets; empty secret answers 501); stretch: ARF/FBL parsing
  - [ ] Release note: `/webhooks/bounce` gains suppression behavior
- **Dependencies:** none upstream. **Blocks GAP-06 and feeds RF-F057** (signal integrity precedes auto-pause and soft-bounce analytics).
- **Evidence / relevant files:** `internal/handlers/bounce_webhook_handler.go:89-92,127` (rejection + hard-bounce-only; verified at HEAD) · `internal/handlers/bounce_handler.go:91-138` (audit: authenticated chain to reuse) · `internal/services/provider_events/` (landed core + complaint chain) · `05-implementation-roadmap.md` §3 GAP-02 (residual items 1–2, 5–6).

### RF-F005 · Complete 2FA recovery codes (consume, regeneration, admin reset)

- **Priority:** P1
- **Category:** auth-org
- **Workflow status:** In Progress
- **Refs:** RF-F005 · R-10 · GAP-12 · `todo.md` §0.2 · consumed by RF-F006 (enforcement)
- **Description:** TOTP recovery codes landed in `7f445f2` (tracker R-10): bcrypt-hashed single-use codes, atomic consume, regeneration gated on current TOTP. Verified at HEAD: `internal/handlers/recovery_code_handler.go`, `internal/models/user_recovery_code.go`, routes in `internal/routes/user_routes.go` all exist. The registry keeps RF-F005 In Progress — complete and pin the remaining surface so the lockout path is fully trustworthy before RF-F006 turns enforcement on.
- **Acceptance criteria:**
  - [ ] Consume path verified end-to-end: a code works exactly once (burn + audit verified)
  - [ ] Regeneration gated on current TOTP verified; regeneration invalidates prior codes
  - [ ] Admin reset path (`admin_handler.go:601-606` extension) keeps the audit trail
  - [ ] Recovery-code display at enrollment (once) + recovery input in the 2FA challenge present in the UI
  - [ ] Tests cover: enforcement-off behavior unchanged; codes hashed at rest; no reuse after burn
- **Dependencies:** none; RF-F006 (enforcement) composes with this.
- **Evidence / relevant files:** `internal/handlers/recovery_code_handler.go`, `internal/models/user_recovery_code.go`, `internal/routes/user_routes.go` (verified at HEAD) · `todo.md` §0.2 (R-10 reconciliation row) · `05-implementation-roadmap.md` §3 GAP-12.

### RF-F014 · Complete Passkeys/WebAuthn enrollment and login

- **Priority:** P1
- **Category:** auth-org
- **Workflow status:** In Progress
- **Refs:** RF-F014 · R-19 · commit `346eb67` (implementation), commit `0f52636` (nil-user abort fix)
- **Description:** WebAuthn landed in `346eb67`: `internal/services/webauthn/` (service, store, util), `internal/handlers/webauthn_handler.go` (+1,223-line test), `internal/models/webauthn_credential.go`, `web/src/api/webauthn.ts`, dashboard wiring; the nil-user abort path was fixed in `0f52636`. The registry keeps RF-F014 In Progress — finish the enrollment + login flows end-to-end and pin the behavior.
- **Acceptance criteria:**
  - [ ] Enrollment flow completes end-to-end (credential registered, stored, listed, deletable) — verified in the dashboard UI
  - [ ] Login with a passkey issues a session equivalent to password+2FA posture; orphaned/unknown credential paths handled (nil-user abort fix pinned by test)
  - [ ] Sessions/audit integrate: WebAuthn events audited; credentials encrypted at rest per the crypto service
  - [ ] `go test ./...` passes including `webauthn_handler_test.go`
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/services/webauthn/service.go`, `store.go`, `util.go`; `internal/handlers/webauthn_handler.go` + `_test.go`; `internal/models/webauthn_credential.go`; `web/src/api/webauthn.ts` (all verified at HEAD) · commits `346eb67`, `0f52636`.

### RF-F026 · Complete domain deliverability health / diagnostics

- **Priority:** P2
- **Category:** domains-dns
- **Workflow status:** In Progress
- **Refs:** RF-F026 · R-07 · GAP-08 · `todo.md` §0.2
- **Description:** The domain deliverability health endpoint landed in `7f445f2` (tracker R-07): GET + diagnose with SPF/DKIM/DMARC status, DKIM key state, per-domain bounce/complaint rates, and live DNS re-verification persisted. Registry keeps RF-F026 In Progress — verify the remaining depth against GAP-08 (drift alerting and daily re-verification live in RF-F023; this task closes the diagnostics surface itself).
- **Acceptance criteria:**
  - [ ] Health + diagnose endpoints return the full picture: SPF/DKIM/DMARC status, DKIM key state, per-domain bounce/complaint rates
  - [ ] Live DNS re-verification persists (last-checked state visible)
  - [ ] Bounce/complaint rates consume provider-event signals (honest data — consistent with RF-F099)
  - [ ] Any depth gaps vs GAP-08 dispositioned into RF-F023 or closed here
- **Dependencies:** none; pairs with RF-F023 (drift/re-verification depth).
- **Evidence / relevant files:** `todo.md` §0.2 (R-07 row) · domain health handler/service in `internal/handlers/` + `internal/services/` (landed `7f445f2`) · `05-implementation-roadmap.md` §5 GAP-08.

### RF-F027 · Complete strict verified-domain sending policy (campaign-path parity, per-domain caps)

- **Priority:** P2
- **Category:** domains-dns
- **Workflow status:** In Progress
- **Refs:** RF-F027 · R-23 · GAP-10 · DOM-012 · `todo.md` Track B Phase 2 Batch A · after RF-F023/RF-F026 (DNS health)
- **Description:** `require_domain_verification` enforcement landed `7f445f2` (R-23) on the transactional path (blocks when strict is enabled — `service.go:1531-1570`, audit). Remaining GAP-10 work: campaign-path parity — campaigns only **warn** on unverified today (`campaign_handler.go:566-571`, audit) — route `warnSenderDeliverable` through the same `RequireVerifiedDomain` setting; and per-domain hourly volume caps on shared servers consumed by the existing Redis limiter (`ratelimit/quota.go:44`).
- **Acceptance criteria:**
  - [ ] Strict-mode campaigns are rejected pre-send (block, not warn); warn-only remains the default behavior
  - [ ] Per-domain hourly caps enforced with a clear 429; caps consumed via the existing Redis limiter
  - [ ] Settings surface + 429 error display in the UI; cap breach tested
- **Dependencies:** RF-F023 / RF-F026 (healthy records make strict gating fair — hard blocker GAP-08 → GAP-10).
- **Evidence / relevant files:** `internal/handlers/campaign_handler.go:566-571` (audit: warn-only) · `internal/services/email/service.go:1531-1570` (audit: tx strict block) · `internal/services/ratelimit/quota.go:44` (audit: Redis limiter).

### RF-F028 · Complete DMARC aggregate report (rua) ingestion

- **Priority:** P2
- **Category:** domains-dns
- **Workflow status:** In Progress
- **Refs:** RF-F028 · R-17 · commit `f5f0e92` · `04-feature-gap-analysis.md` §6.2 (superseded by this feature's implementation)
- **Description:** DMARC rua ingestion was implemented in `f5f0e92` ("DMARC ingestion") — superseding the roadmap-suite position that rua ingestion was excluded as peer-unvalidated (04 §6.2). Registry keeps RF-F028 In Progress — complete the ingestion surface: report parsing, per-domain aggregate statistics, and retention of parsed reports.
- **Acceptance criteria:**
  - [ ] rua report ingestion end-to-end: parse, store, and surface per-domain aggregate stats (pass/fail alignment counts)
  - [ ] Reports retention-covered; ingestion rate-limited/authenticated as appropriate
  - [ ] Domain health UI shows DMARC aggregate insights; tests pin the parser
- **Dependencies:** none.
- **Evidence / relevant files:** commit `f5f0e92` (DMARC ingestion) · `04-feature-gap-analysis.md` §6.2 (row superseded) · tracker R-17 in `docs/implementation/ROADMAP_IMPLEMENTATION.md`.

### RF-F081 · Complete system-info endpoint feature flags (Section-11 gate)

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** In Progress
- **Refs:** RF-F081 · commit `346eb67`, commit `0f52636` · module audit (platform-admin-security)
- **Description:** The system-info endpoint landed in `346eb67`: `internal/handlers/info_handler.go` exposes `AppFeatures` (`messages_enabled`, `inbound_enabled`, `relay_enabled`, `metrics_enabled`) mirroring the env-gated subsystems, and `DashboardLayout.vue` hides the nav entries whose pages would only error when the subsystem is disabled (Section-11 gate). Complete the gate: every env-gated subsystem flagged, every dashboard nav entry honoring the flags, behavior pinned by tests.
- **Acceptance criteria:**
  - [ ] Every env-gated subsystem is represented in `AppFeatures`; no dashboard nav entry can reach a page that errors when its subsystem is disabled
  - [ ] Flags default false and are sourced from config only (no client trust); endpoint is public-safe (no sensitive data beyond enablement bits — verified)
  - [ ] `info_handler_test.go` pins the flag matrix; dashboard tests cover the hidden-nav behavior
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/handlers/info_handler.go` + `info_handler_test.go` (verified at HEAD) · `web/src/layouts/DashboardLayout.vue`, `web/src/api/info.ts` (landed `346eb67`).

### RF-F082 · Complete API route contract gate (golden file + frontend spec)

- **Priority:** P2
- **Category:** platform-admin-security
- **Workflow status:** In Progress
- **Refs:** RF-F082 · R-21 · commit `346eb67`
- **Description:** The route contract gate landed in `346eb67`: `internal/routes/contract_test.go` (golden file test, 174 lines) + `docs/implementation/api_routes.txt` (the 340+-route golden inventory). Complete the gate so route drift is caught in CI on both sides: the golden file fully regenerated/verified against HEAD, and the frontend API-client spec (`web/src/api/admin.spec.ts` and siblings) asserting client/route alignment.
- **Acceptance criteria:**
  - [ ] Golden file verified against the full route set at HEAD; contract test fails on any undocumented route addition/removal
  - [ ] Frontend API-client spec asserts client methods ↔ registered routes alignment (no dead methods like RF-F102's, no missing routes)
  - [ ] Process documented: how to regenerate the golden file when routes change intentionally
  - [ ] `go test ./...` + web specs pass in CI
- **Dependencies:** none; RF-F102/RF-F103 outcomes must keep this gate green.
- **Evidence / relevant files:** `internal/routes/contract_test.go`, `docs/implementation/api_routes.txt` (verified at HEAD) · `web/src/api/admin.spec.ts` (landed `346eb67`).

### RF-F015 · Complete admin impersonation (guardrails, audit, UI flag)

- **Priority:** P3
- **Category:** auth-org
- **Workflow status:** In Progress
- **Refs:** RF-F015 · GAP-46 · SEC-015 · R-20 · commits `346eb67`, `0f52636`
- **Description:** Impersonation landed in `346eb67`: `POST /admin/users/{id}/impersonate` (admin-only, requires a reason of ≥10 chars, audited, issues a new JWT + session carrying `impersonated_by`/`impersonation_reason`; administrators and disabled accounts cannot be impersonated; the browser session cookie is replaced, so exiting requires re-authentication — `internal/routes/admin_routes.go:161-165`), with a 954-line handler test; `0f52636` fixed a duplicate stop-impersonation audit event. Complete the remaining guardrail/UI surface the registry still lists as In Progress.
- **Acceptance criteria:**
  - [ ] All guardrails pinned by test: expire ≤60 min; impersonated session cannot touch `/admin/*`; actor+target on every audit event; per-user opt-out honored
  - [ ] Impersonation banner + exit control in the dashboard (visually flagged at all times)
  - [ ] Stop-impersonation audited exactly once (duplicate-audit fix pinned)
  - [ ] `impersonation_handler_test.go` green in CI
- **Dependencies:** none.
- **Evidence / relevant files:** `internal/routes/admin_routes.go:161-165`, `internal/handlers/impersonation_handler.go` + `_test.go` (verified at HEAD) · `internal/models/session.go` (impersonation fields, `346eb67`).

---

## In Review

No items are currently in review (registry view: In Review × 0). When a task moves to review, move its entry here from In Progress with the same field set, adding the review artifact (PR link, test evidence) to **Evidence / relevant files**.

---

*Sources: `todo.md` (master todo, Tracks A/B, generated 2026-11-06) · `docs/product-roadmap/README.md`, `03-existing-feature-audit.md`, `04-feature-gap-analysis.md`, `05-implementation-roadmap.md`, `06-architecture-impact.md` (research 2026-11-05, baseline `a6f1681`→`6d2d7c0`) · `docs/product-roadmap/upcomming-convo.md` (Option B) · registry/gap views and repo state at HEAD `0f52636` (2026-11-06). Registry IDs, GAP-01…53, D1–D9, R1–R16 are permanent — never renumber. Per `todo.md` §"How to use": re-verify an item's current state at HEAD before starting it; where a sibling doc disagrees with this file, the sibling governs.*
