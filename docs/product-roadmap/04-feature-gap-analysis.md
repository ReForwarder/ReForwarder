# 04 — Feature Gap Analysis: What ReForwarder Lacks or Needs to Improve

> **Doc 4 of 7** in the ReForwarder product-roadmap documentation suite. This document is the **gap register**: everything ReForwarder is missing, has only partially implemented, needs to refactor (expressed as architectural/duplication findings), cannot yet verify, or ships twice. Each gap carries competitor evidence, file-level proof, and a recommended approach that reuses existing ReForwarder patterns. The scored and sequenced version of this register is doc 05 (roadmap); architecture consequences live in `06-architecture-impact.md`.

- **Date:** 2026-11-05.
- **Baseline:** HEAD `a6f1681` ("Add SMTP timeouts, DKIM signing, and delivery-queue administration"); scheduler at parent `347ebb4`; **plus in-flight working-tree items pending commit** (provider-event ingestion, A/B winner selection, timezone-aware sending, webhook replay — §4). Drift warning: a post-baseline spot-check on 2026-11-05 found those four streams committed as `6d2d7c0`; per suite convention this register stays frozen at the synthesis baseline and the affected gaps keep their "in progress, pending commit" status (§4, §8).
- **Source of truth:** `synthesis.md` §1 (53 gaps across 10 classes; priorities P0×5, P1×10, P2×22, P3×16; 15 deliberately-not-proposed entries) and §2 (9 duplicates). Registry IDs only (INF/DOM/CON/CAM/TPL/ANA/AUT/API/SEC/OBS-###), permanent per `01-oss-feature-inventory.md` § "ID scheme".
- **Method:** the 199-entry registry (`registry.md` v1.0.0, frozen 2026-11-05) classifies 108 IMPLEMENTED / 46 PARTIAL / 38 MISSING / 3 NEEDS_VERIFICATION / 4 NOT_APPLICABLE. Of the 87 non-OK entries, **1 resolved at synthesis** (SEC-016 credential encryption → IMPLEMENTED, verified 2026-11-05), **15 deliberately not proposed** (§7), and the remaining **86 fold into the 53 gaps** recorded here. Evidence pointers are quoted from the five category audits (audit-A/B and audit-C/D drafted at `347ebb4`+WIP; audit-E/F, G/H, I/J at `a6f1681`); claims re-verified at HEAD are marked. Competitor flags (✓ ◐ ▲ E H ✗) are transcribed from `02-competitor-comparison.md` (legend §0, matrices §1); per-feature current-state detail is `03-existing-feature-audit.md`.
- **Status legend (gap status values):** **MISSING** — verified absent (grep + route/handler/model sweep) · **PARTIAL** — core present with a named, verified functional gap · **INCOMPLETE** — implemented at baseline but with a named correctness hole · **[in progress, pending commit]** — partially or fully covered by uncommitted working-tree code (§4); remaining requirements are still listed.
- **Gap-ID note:** every gap carries its registry feature ID(s) **and** a stable `GAP-nn` identifier taken verbatim from synthesis §1. GAP IDs (GAP-01…GAP-53), duplicate IDs (D1–D9), and risk IDs (R1–R16) are already consumed by `06-architecture-impact.md` and will be consumed by doc 05 — **do not renumber**.
- **Priority note:** the priority shown per gap is this register's **draft priority with rationale**. Numeric scoring (UV/II/SR/CA/CX/AD), phase sequencing, and effort estimates belong to **doc 05** and are deliberately not duplicated here.
- **Reading order:** `01-oss-feature-inventory.md` (ID scheme, project profiles) → `02-competitor-comparison.md` (competitor evidence, §0 legend) → `03-existing-feature-audit.md` (current state, file:line) → **this document** → doc 05 (roadmap) → `06-architecture-impact.md` (architecture impact).

---

## 1. Gap summary

### 1.1 Counts by class and priority

| Class | Gaps | GAP IDs | P0 | P1 | P2 | P3 |
|---|---|---|---|---|---|---|
| Critical infrastructure | 5 | GAP-01…05 | 3 | 2 | 0 | 0 |
| Deliverability | 5 | GAP-06…10 | 0 | 1 | 4 | 0 |
| Security | 7 | GAP-11…17 | 2 | 3 | 1 | 1 |
| Developer experience | 7 | GAP-18…24 | 0 | 0 | 2 | 5 |
| Marketing | 15 | GAP-25…39 | 0 | 3 | 7 | 5 |
| Analytics | 1 | GAP-40 | 0 | 0 | 0 | 1 |
| Automation | 3 | GAP-41…43 | 0 | 0 | 3 | 0 |
| Administration | 4 | GAP-44…47 | 0 | 0 | 2 | 2 |
| Scalability | 2 | GAP-48…49 | 0 | 0 | 2 | 0 |
| Documentation | 4 | GAP-50…53 | 0 | 1 | 1 | 2 |
| **Total** | **53** | | **5** | **10** | **22** | **16** |

### 1.2 The five P0s (named up front)

| GAP | One line |
|---|---|
| GAP-01 | Scheduled-email cancel + reconcile coverage — un-cancellable future mail; scheduled rows invisible to `queue_reconcile` → silent lost/double sends in the core money path. |
| GAP-12 | Enforce or remove `two_factor_required` — the seeded setting has zero consumers; a security control that silently does nothing; add recovery codes. |
| GAP-03 | Cron leader lock — all 10 cron jobs run on every replica; alert/report jobs double-send on multi-replica deployments. |
| GAP-02 | Delivery/complaint signal ingestion — `delivered`/`complained` are dead constants; webhook path does not suppress; every downstream analytics/reputation feature stands on an accept-only signal. |
| GAP-11 | Automation editor gate — automation write + event-ingest routes are authenticated but not role-gated server-side; a viewer JWT can create/activate automations. |

---

## 2. Gap detail (by class)

Format per gap: registry feature ID(s) → description → competitor reference (project codes per `02-competitor-comparison.md` §0) → current status + evidence (file:line from the audit files) → missing requirements → dependencies → technical challenges → recommended approach (reusing existing ReForwarder patterns) → acceptance criteria → draft priority + rationale (scoring: doc 05).

### 2.1 Critical infrastructure (GAP-01…05)

**GAP-01 · Scheduled transactional email: cancel + reconcile coverage** — `INF-014` — P0
- **Description:** scheduled transactional emails cannot be cancelled by users, and `scheduled` rows sit outside queue-reconcile coverage, so an asynq task lost to a Redis flush or crash strands the row forever (never sent, never failed, invisible to ops).
- **Competitor reference:** INF-014 LM✓ BM✓ US✓ NU✓ SP✓ KE✓ MC✓ WR✓ HP✓ PO◐; exemplar Listmonk "Unschedule button; scheduled campaigns revert to draft" (`02-competitor-comparison.md` §1.A).
- **Current status:** INCOMPLETE (send side shipped at `a6f1681`). **Evidence:** audit-A: "No user-facing cancel of a scheduled email … scheduled rows are not covered by queue-reconcile (it scans `status='queued'` only, `queue_reconcile.go:93`)" (also audit-A NV item 1).
- **Missing requirements:** cancel endpoint valid only while `status='scheduled'`; `queue_reconcile` sweep of `scheduled` rows past their `scheduled_at` whose task was lost.
- **Dependencies:** none — extends the `a6f1681` reconcile pattern.
- **Technical challenges:** CAS transition must not race the every-minute scheduler claim; deterministic TaskIDs must collapse double-enqueue without double-send; cancel must propagate to asynq task deletion.
- **Recommended approach:** add `DELETE /emails/{id}` (or `/cancel`) valid only while `status='scheduled'`, reusing the CAS transition pattern from `campaign_handler.go:391-464`; extend `queue_reconcile` to also claim `scheduled` rows past `scheduled_at` with the same deterministic-TaskID re-enqueue.
- **Acceptance criteria:** cancelling a scheduled email leaves it `cancelled` and deletes the asynq task; a scheduled row with no Redis task is re-enqueued within one reconcile cycle; both paths covered by tests mirroring `queue_reconcile_test.go`.
- **Priority rationale:** P0 — silent double/lost sends and un-cancellable future mail are correctness defects in the core transactional path (doc 05 score 3.40, top of Phase 0).

**GAP-02 · Delivery & complaint signal ingestion (true `delivered`/`complained` plumbing)** — `ANA-003`, `ANA-005`, `INF-021`, `API-014` — P0
- **Description:** `delivered` is defined but never produced (SMTP-accept only), no `complained` email_events row is ever written, the bounce webhook neither suppresses nor records a bounce, and ESP-format webhook adapters are absent.
- **Competitor reference:** API-014 LM✓ SP✓ US✓ NU✓ MC✓ SE✓ WR✓; INF-021 broad ◐; exemplar the reference workflow platform: provider webhooks set bounced/complained statuses that "always suppress from broadcasts"; UseSend tracks 21 event types (`02-competitor-comparison.md` §1.A/F/H).
- **Current status:** PARTIAL/MISSING mix; **[in progress, pending commit]** for the ingestion core (§4). **Evidence:** audit-F delivery-tracking and complaints rows; audit-G: "`/webhooks/bounce` does NOT suppress or record a `bounces` row (only the authenticated API does)" and "rejects complaint semantics (hard/soft only, `bounce_webhook_handler.go:89-92`)"; ESP adapters absent (`bounce_webhook_handler.go:83` expects Posta schema only).
- **Missing requirements:** ESP-shaped event receivers (SES SNS / Postmark / Mailgun / SendGrid); `delivered` event production; `complained` email_events rows; suppression + subscriber-status updates from the webhook path; ARF/FBL parsing (stretch).
- **Dependencies:** none upstream; **blocks** GAP-06 (auto-pause), GAP-07 (soft-bounce trends), complaint-rate analytics, and honest delivery-rate reporting (risk R4 in `06-architecture-impact.md`).
- **Technical challenges:** normalizing heterogeneous provider payloads into the Posta schema without weakening the SSRF guard; webhook-path suppression must replicate the authenticated path's transactional guarantees; analytics must distinguish accept vs delivered for historical rows.
- **Recommended approach:** commit and finish the in-flight `provider_events` ingestion (untracked `internal/services/provider_events/` already records `EmailEventDelivered` at `provider_events/service.go:213`); add per-ESP payload adapters normalizing into the existing Posta schema; make `/webhooks/bounce` perform the same suppression + `bounces` row + `email.complained` emission as authenticated `POST /bounces` (`bounce_handler.go:91-138`), reusing `SuppressionKindComplaint`.
- **Acceptance criteria:** a delivered ESP webhook produces a `delivered` email_event and sets `delivered_at`; a complaint webhook produces suppression(kind=complaint) + `complained` email_event + `email.complained` webhook; an SES-SNS-shaped payload is accepted by an adapter test; delivery-rate analytics distinguish accept vs delivered.
- **Priority rationale:** P0 — every reputation, complaint, and analytics feature downstream is currently built on an accept-only signal (doc 05 score 2.85; largest unlock; de-risked by in-flight code).

**GAP-03 · Multi-replica cron leader election** — `OBS-019` — P0
- **Description:** all 10 `robfig/cron` jobs run on every server replica; there is no leader election, so alert and report jobs double-fire on any multi-replica deployment.
- **Competitor reference:** RF-specific — no competitor flag in the registry (`02-competitor-comparison.md` §1.J "RF-specific" row).
- **Current status:** PARTIAL. **Evidence:** audit-J: "No leader lock: all replicas run all 10 crons; alert jobs (bounce_alert, daily_report) risk double-send"; re-verified at HEAD: `internal/cron/manager.go` has only in-process mutexes, no Redis lock.
- **Missing requirements:** a distributed leader lock (or a dedicated cron-replica flag) guarding the 10 jobs.
- **Dependencies:** Redis (already universal).
- **Technical challenges:** lease loss must fail over within one interval without double execution; even with a lease, alert jobs need per-recipient dedup (crash between lease acquire and send).
- **Recommended approach:** Redis `SET NX PX` leader lease around the cron manager tick — pattern precedent is the form-nonce Redis usage (`form_ingest_handler.go:77,131`); keep the jobs' idempotent design; additionally guard `bounce_alert`/`daily_report` per-recipient with a Redis dedup key.
- **Acceptance criteria:** with 2 server replicas sharing Redis, exactly one replica executes `campaign_scheduler`/`bounce_alert`/`daily_report` per tick (integration test with two managers); lease loss on the leader fails over within one interval.
- **Priority rationale:** P0 — multi-replica is a documented topology (`compose.yml` worker split) and duplicate customer-facing alerts/digests are a silent correctness bug (risk R3 in `06-architecture-impact.md`).

**GAP-04 · Native ESP sending integrations, failover, per-send server selection** — `INF-022`, `API-015`, `INF-023`, `INF-024` — P1
- **Description:** sending is generic SMTP only; retry re-queues against the same server with no rotation or circuit breaker; send-time selection picks only the first enabled workspace server.
- **Competitor reference:** INF-022 NU✓ SP✓ KE✓ BM✓ US✓ MC✓ WR✓; exemplar the reference workflow platform: 7 providers (SES, Mailgun, Postmark, Mailjet, SparkPost, SendGrid, SMTP) with per-integration rate limits; Listmonk per-campaign SMTP selection (`02-competitor-comparison.md` §1.A/H).
- **Current status:** PARTIAL/MISSING. **Evidence:** audit-H: "Generic SMTP only; no native ESP API clients (go.mod: s3 only)"; audit-A: "Retry re-queues against the same server (`handler.go:334-356`); no rotation/circuit breaker"; "send-time selection picks only the first enabled workspace server (`smtp_repo.go:119-125`)".
- **Missing requirements:** provider abstraction behind `Send` (API-mode ESP clients); failover chain (alternate server/ESP on transient failure); per-message/per-campaign server override.
- **Dependencies:** pairs with GAP-02 — ESP event webhooks supply the delivery signals that make API-mode sending worthwhile.
- **Technical challenges:** largest build in the register; credential handling must reuse the AES-256-GCM crypto service; failover must not double-send (deterministic TaskIDs); health-aware rotation needs failure counters per candidate.
- **Recommended approach:** extract the `Sender` interface already implicit in `smtp_sender.go` (`Send`, `TestConnection`) and add SES/Postmark/Mailgun clients; extend server selection (`handler.go:197-250`) to ordered candidates + health-aware rotation (failure counters already exist at `handler.go:336-341`); encrypt provider credentials with `crypto.Encrypt`.
- **Acceptance criteria:** a workspace can configure an SES integration and send via API with credentials encrypted; a failing primary server fails over to the next candidate within one retry; campaigns can pin a server.
- **Priority rationale:** P1 — the largest competitive-parity item in category A; unblocks genuine delivery signals and future IP/reputation work (doc 05: start abstraction early in Phase 1 if capacity allows).

**GAP-05 · Caller-supplied idempotency keys** — `INF-031` — P1
- **Description:** internal deterministic TaskID dedup shipped at `a6f1681`, but there is no caller-facing `Idempotency-Key` API, so client-level retries before enqueue still double-send.
- **Competitor reference:** INF-031 US✓ HR✓ NU◐; exemplar Hyvor Relay/UseSend client key dedup (`02-competitor-comparison.md` §1.A).
- **Current status:** PARTIAL. **Evidence:** registry + audit-H: "No `Idempotency-Key` header handling anywhere (grep clean)"; internal dedup committed at `producer.go:47-101`.
- **Missing requirements:** `Idempotency-Key` header on send/batch endpoints; keyed dedup store with result replay.
- **Dependencies:** none; complements the internal TaskID dedup.
- **Technical challenges:** concurrent duplicate requests need a unique-index race winner; result replay must survive retention windows; scope interaction with batch endpoints (one key per batch).
- **Recommended approach:** on `POST /emails/send|send-template|batch`, persist `(workspace_id, key) → email_id/result` with a unique index; replay the stored response on conflict; expire keys via the existing retention cron.
- **Acceptance criteria:** identical key returns the same email_id without a second send (test with 2 concurrent requests); a different key creates a new email; keys expire per retention.
- **Priority rationale:** P1 — client retries currently double-send; the internal dedup does not protect against caller-level retries before enqueue.

### 2.2 Deliverability (GAP-06…10)

**GAP-06 · Circuit breaker / auto-pause on failure & complaint thresholds** — `INF-016`, `DOM-011`, `ANA-010` — P1
- **Description:** ReForwarder has alerts only; competitors pause sending automatically on failure or complaint-rate thresholds.
- **Competitor reference:** INF-016 NU✓ MC✓; exemplar MailCheap: pause at 2.5% bounce / 0.08% complaint, block at 5%/0.3%, Postmaster Gmail spam-rate monitoring (`02-competitor-comparison.md` §1.A/B/F).
- **Current status:** PARTIAL (INF-016 MISSING, DOM-011/ANA-010 alert-only). **Evidence:** "NU pauses after 5 consecutive failures; MC pauses at 2.5%/0.08%. RF has alerts only" (audit-A/F: `bounce_alert.go`, provider-classified analytics; no blacklist/Postmaster).
- **Missing requirements:** auto-pause state machine for campaigns (consecutive-failure and rate thresholds); complaint-rate gates once GAP-02 lands.
- **Dependencies:** **GAP-02** (real complaint/bounce signal); campaign pause/resume CAS (`campaign_handler.go:391-438`).
- **Technical challenges:** thresholds must be per-workspace configurable; pause must be auditable and manual-resume-only; must not fire on accept-only signals (pre-GAP-02 data).
- **Recommended approach:** extend `bounce_alert.go` evaluation into a pause action: on threshold breach, call the existing `POST /campaigns/{id}/pause` CAS transition and emit an audit event + workspace notification; per-workspace thresholds via settings.
- **Acceptance criteria:** a campaign auto-pauses when consecutive-failure or bounce/complaint thresholds trip; resume is manual and audited; thresholds configurable per workspace; no pause when signals are accept-only.
- **Priority rationale:** P1 — protects shared-IP upstream reputation; competitors treat this as table stakes.

**GAP-07 · Soft-bounce classification & greylist/transient refinement** — `ANA-004`, `INF-034` — P2
- **Description:** retry exhaustion records only `failed` — soft bounces are never auto-detected; timeout→transient classification exists but greylist replies are not distinguished.
- **Competitor reference:** ANA-004 US✓ MC✓ SE✓; exemplar Sessy hard/soft/complaint trend analytics; INF-034 WR✓ (`02-competitor-comparison.md` §1.A/F).
- **Current status:** PARTIAL. **Evidence:** "soft never auto-detected — exhaustion records only `failed` (`handler.go:538-574`)"; "Timeout→transient classification committed a6f1681 (`smtp_sender.go:64-67`); no explicit greylist detection".
- **Missing requirements:** 4xx/deferral detection → `soft` bounce row on retry exhaustion; greylist signature (4xx + known greylist messages) → longer retry horizon, never suppress.
- **Dependencies:** benefits from GAP-02 for accuracy; stands alone for SMTP-level signals.
- **Technical challenges:** classification must key off the structured `SendError` without misclassifying permanent 5xx; suppression must stay hard-only.
- **Recommended approach:** in the asynq exhaustion path (`handler.go:529-565`) record `Bounce{Type: soft}` when the last error is 4xx/timeout (reuse `SendError{Stage,Code}` from `smtp_sender.go:48-62`); keep suppression hard-only.
- **Acceptance criteria:** a 4xx-exhausted send produces a soft bounce row + trend analytics; greylist replies never suppress; hard/soft split visible in bounce analytics.
- **Priority rationale:** P2 — improves list hygiene and analytics fidelity without changing suppression behavior.

**GAP-08 · DNS record health: drift re-verification + SPF/DMARC depth** — `DOM-008`, `DOM-005`, `DOM-006` — P2
- **Description:** DNS records are verified once, on demand, and can silently rot; SPF/DMARC checks are presence-only with no lookup-count or policy parsing.
- **Competitor reference:** DOM-008 PO✓ MC✓ HR✓ — exemplar Postal periodic re-checks; DOM-005/006 PO✓ US✓ BM✓ MC✓ WR✓ (`02-competitor-comparison.md` §1.B).
- **Current status:** DOM-008 MISSING; DOM-005/006 PARTIAL. **Evidence:** "Verification is on-demand only; no cron re-check"; presence checks only (`verifier.go:97-101,126-136`); no 10-lookup validation, no policy parsing (audit-B top gap: "records verified once and can silently rot").
- **Missing requirements:** cron re-checking TXT records for verified domains with drift alerts; SPF include/qualifier parsing + lookup-count check; DMARC p=/rua policy parse surfaced in UI.
- **Dependencies:** none; feeds DOM-012 enforcement (GAP-10).
- **Technical challenges:** re-verification must stay advisory (never block sends, consistent with `IsFullyVerified` semantics); DNS lookups need rate limiting inside the cron.
- **Recommended approach:** add a daily cron job to the 10-job fleet (`internal/cron/jobs/`) that re-runs `verifier.go` checks on verified domains, stores last-checked status, and raises workspace + admin alerts on drift; deepen SPF/DMARC parsing in the verifier.
- **Acceptance criteria:** removing a DKIM/SPF record triggers a drift alert within 24h; SPF 10-lookup count displayed; DMARC policy shown; re-verification never blocks sends.
- **Priority rationale:** P2 — top category-B gap and cheap to close.

**GAP-09 · Custom tracking domain** — `DOM-009` — P2
- **Description:** tracking runs on the instance domain only; no per-workspace branded tracking host for `/t/*` link/pixel/web-view URLs.
- **Competitor reference:** PO✓ NU✓ — exemplar Postal per-workspace tracking domains (`02-competitor-comparison.md` §1.B).
- **Current status:** MISSING. **Evidence:** registry + audit-B: "Tracking runs on instance domain only; no per-workspace branded tracking host".
- **Missing requirements:** per-workspace tracking hostname (CNAME verified like sending domains) used for all `/t/*` URLs.
- **Dependencies:** domain verification (DOM-002 exists).
- **Technical challenges:** touches every `/t/` URL generator; host must route to the instance (reverse-proxy doc requirement); HMAC token scheme must remain unchanged while the base URL varies.
- **Recommended approach:** add a workspace-level `tracking_domain` verified via TXT (reusing `verifier.go:88`); parameterize link-rewrite and pixel base URL in `tracking.go:65-227`; document the reverse-proxy host routing.
- **Acceptance criteria:** tracked links/pixels use the workspace domain once verified; unsigned/unverified hosts are rejected; per-message HMAC tokens unchanged.
- **Priority rationale:** P2 — brand trust + deliverability for the marketing use case; moderate surface.

**GAP-10 · Domain-sending policy enforcement hardening** — `DOM-012` — P2
- **Description:** the campaign path only warns on unverified sender domains while transactional strict mode blocks; there are no per-domain volume quotas.
- **Competitor reference:** MC✓ HR✓ PO◐ — exemplar Hyvor Post per-server allowed domains (`02-competitor-comparison.md` §1.B).
- **Current status:** PARTIAL. **Evidence:** "campaigns only warn on unverified (`campaign_handler.go:566-571`)" while transactional strict mode blocks (`service.go:1531-1570`); no per-domain volume quotas (audit-A).
- **Missing requirements:** campaign-path parity with strict mode (block, not warn, when strict is enabled); per-domain rate caps on shared servers.
- **Dependencies:** GAP-08 (healthy records make strict gating fair).
- **Technical challenges:** warn→block flips behavior for existing campaigns (needs the setting default preserved); per-domain caps must integrate the existing Redis limiter without a new storage surface.
- **Recommended approach:** route `warnSenderDeliverable` through the same `RequireVerifiedDomain` setting the transactional path uses; add per-domain hourly caps consumed by the existing Redis limiter (`ratelimit/quota.go:44`).
- **Acceptance criteria:** with strict mode on, a campaign on an unverified domain is rejected pre-send; per-domain caps return 429 with a clear error; warn-only remains the default.
- **Priority rationale:** P2 — shared-server reputation protection; small delta on existing gates.

### 2.3 Security (GAP-11…17)

**GAP-11 · Server-side editor gate on automation write & event-ingest routes** — `SEC-002` (note), `AUT-003` (note) — P0
- **Description:** automation write gating is UI-only; all mutating `/automations*` routes (including event ingest) accept any workspace member role server-side.
- **Competitor reference:** SEC-002 LM✓ KE✓ — server-side role checks everywhere (`02-competitor-comparison.md` §1.I).
- **Current status:** PARTIAL (gate missing). **Evidence:** "automation write gating is UI-only … Commit `c0408dc` hid the buttons in 3 Vue files only" (audit-I); "route sits in the `auth + workspace` group (`automation_routes.go:35-37,186-201`) … no editor role gate"; re-verified at HEAD: group middleware is only `r.mw.auth, r.mw.workspace`; `workspace_rbac_test.go:101` itself documents "any member — including a viewer —" passing. Reconciled verdict per synthesis §6.3: authenticated and tenant-scoped; server-side editor gate pending.
- **Missing requirements:** `RequireWorkspaceRole(Editor)` (or write-scope requirement) on all mutating `/automations*` routes including `POST /automations/events`.
- **Dependencies:** none — one middleware per route group.
- **Technical challenges:** none beyond test coverage; scope interaction for API keys must match the existing `requireWorkspaceScope` map.
- **Recommended approach:** add `r.mw.RequireWorkspaceRole(models.RoleEditor)` to the automation write subgroup and the events route; extend `workspace_rbac_test.go` with viewer-denied cases (pattern exists at `:103,140,159`).
- **Acceptance criteria:** a viewer-role JWT receives 403 on `POST /automations`, activate/pause/duplicate/delete, and `/automations/events`; editor+ roles pass; API keys require an appropriate scope; regression test added.
- **Priority rationale:** P0 — a ten-minute fix closing a real intra-tenant privilege gap the codebase's own test comments acknowledge (risk R1 in `06-architecture-impact.md`).

**GAP-12 · Enforce or remove `two_factor_required`; add recovery codes** — `SEC-005` — P0
- **Description:** the `two_factor_required` setting is seeded and has a getter but zero consumers — login never reads it; no recovery codes exist. A security control that silently does nothing.
- **Competitor reference:** SEC-005 — LM✗ (not even TOTP), so RF's TOTP already leads; enforced-2FA is RF-specific hygiene (`02-competitor-comparison.md` §1.I).
- **Current status:** PARTIAL (dead setting). **Evidence:** "`two_factor_required` setting is seeded (`seeder/settings.go:52`) with getter `settings/provider.go:124`, but has zero consumers — login (`user_handler.go:459-510`) never reads it. No recovery codes" (audit-I; re-verified: sole reference is the getter).
- **Missing requirements:** login-time enforcement of the setting; one-time recovery codes with single-use burn; admin reset flow extension.
- **Dependencies:** TOTP service (exists, `twofactor/totp.go:27-42`).
- **Technical challenges:** enrollment must be forced without locking users out of their accounts (recovery codes as the escape hatch); admin reset must keep its existing audit trail.
- **Recommended approach:** in the login handler, after password success, if `TwoFactorRequired()` and the user lacks TOTP → force an enrollment challenge; add a `user_recovery_codes` table (hashed, single-use) consumed in the 2FA challenge step; keep admin reset (`admin_handler.go:601-606`), which already audits.
- **Acceptance criteria:** with the setting on, a user without 2FA cannot reach a session without enrolling; a recovery code works exactly once and is audited; setting off restores current behavior; tests cover all three paths.
- **Priority rationale:** P0 — "a control that silently does nothing" (audit-I) is the worst failure mode for a security setting; the fix is small (risk R2 in `06-architecture-impact.md`).

**GAP-13 · `security_events` retention caller** — `SEC-008` (note) — P1
- **Description:** `security_events_retention_days` is declared and read but `SecurityEventRepository.DeleteOlderThan` has no caller — the highest-volume security table grows unbounded.
- **Competitor reference:** SEC-008 PO✓ NU✓ SE✓ WR✓ (`02-competitor-comparison.md` §1.I/J).
- **Current status:** INCOMPLETE. **Evidence:** audit-I: "`security_events_retention_days` is a dead setting — declared `security_handler.go:489`, read `security/rules.go:129`, but `SecurityEventRepository.DeleteOlderThan` (`security_event_repo.go:157-160`) has no caller"; re-verified: no caller outside the repo file.
- **Missing requirements:** a cron/hook that calls `DeleteOlderThan` with the configured window.
- **Dependencies:** none — the repo method and the setting both exist.
- **Technical challenges:** pruning must batch to avoid long-lived transactions on the largest table; setting=0 semantics must be explicit (disable).
- **Recommended approach:** register a daily job in the retention cron (`retention.go:195-196` pattern) that reads the setting and prunes; wire the setting into the admin Settings UI category that already lists retention windows.
- **Acceptance criteria:** rows older than the configured window are deleted daily; setting=0 disables pruning explicitly; covered by a test mirroring `retention_test.go`.
- **Priority rationale:** P1 — unbounded growth degrades the ban engine's own store; one function call away (risk R7 in `06-architecture-impact.md`).

**GAP-14 · Webhook signature replay protection** — `API-010` — P1
- **Description:** outbound webhooks are HMAC-SHA256-signed but carry no timestamp, so captured payloads can be replayed indefinitely against customer endpoints.
- **Competitor reference:** API-010 US✓ NU✓ HR✓ — exemplar UseSend: timestamp header + 5-minute replay rejection (`02-competitor-comparison.md` §1.H).
- **Current status:** PARTIAL. **Evidence:** "HMAC-SHA256 `X-Posta-Signature` (`dispatcher.go:485`); no timestamp/replay window" (registry; audit-H signatures row).
- **Missing requirements:** signed timestamp header, tolerance window, replay rejection; SDK `constructEvent` helper note.
- **Dependencies:** none; coordinate with SDK docs (GAP-23).
- **Technical challenges:** clock skew between dispatcher and customer receivers needs a documented tolerance; the verification helper shipped in SDK docs must be updated in lockstep.
- **Recommended approach:** add `X-Posta-Timestamp` into the HMAC input; reject deliveries older than 5 minutes (computed against the dispatcher clock); document in `webhook_docs.go` payload examples.
- **Acceptance criteria:** a captured valid signature replayed >5 min later is rejected; clock-skew tolerance documented; registry test pins the new header.
- **Priority rationale:** P1 — cheap cryptographic hygiene on an otherwise strong webhook system (risk R9 in `06-architecture-impact.md`).

**GAP-15 · Tracking toggle symmetry (privacy)** — `ANA-012` — P1
- **Description:** campaign mail still receives the open pixel when `email_tracking_enabled` is off — deliberate code contradicting the setting's documented semantics.
- **Competitor reference:** ANA-012 LM✓ KE✓ PO◐ US◐ SP◐ — exemplar Keila per-campaign opt-out (`02-competitor-comparison.md` §1.F).
- **Current status:** PARTIAL. **Evidence:** "Campaign mail still gets the open pixel when the toggle is off (`campaign_processor.go:392-396` … comment :319-321 says open tracking 'unaffected')" (audit-F; re-verified at HEAD, incl. else-branch `InjectOpenPixel`); the setting doc comment (`provider.go:174-178`) promises rewriting **and** pixel.
- **Missing requirements:** campaign path honoring `email_tracking_enabled` for the open pixel; (optional) per-campaign/per-workscope scoping.
- **Dependencies:** a product-intent decision first (audit-E-F NV item 1) — the else-branch is deliberate code contradicting the setting's doc.
- **Technical challenges:** changing behavior alters campaign-render output for existing deployments; either the doc or the code must move, and unsubscribe/web-view links must be preserved either way.
- **Recommended approach:** make the campaign branch skip both rewrite and pixel when the toggle is off (matching the setting's documented semantics), and either update the doc comment to codify the exception or add `tracking_enabled` on campaigns for scoped control.
- **Acceptance criteria:** with the toggle off, no pixel and no rewriting appear in campaign HTML (assert on rendered body); unsubscribe/web-view links still present; setting documented to match behavior.
- **Priority rationale:** P1 — the privacy promise is currently false for marketing sends; a two-line behavioral fix plus doc alignment (risk R8 in `06-architecture-impact.md`).

**GAP-16 · Admin-console IP allowlisting** — `SEC-013` — P2
- **Description:** the ban-engine allowlist (detector exemption) is not an access control; no IP restriction exists on `/admin/*` beyond JWT+role.
- **Competitor reference:** SEC-013 BM◐ — partial peer (`02-competitor-comparison.md` §1.I).
- **Current status:** PARTIAL. **Evidence:** audit-I: "Ban-engine allowlist (detector exemption) ≠ admin-access control … No admin-console/dashboard access IP restriction (no middleware on `/admin/*` beyond JWT+role)".
- **Missing requirements:** optional CIDR allowlist middleware on `/admin/*` (env/setting-driven).
- **Dependencies:** none (a reverse proxy remains an alternative).
- **Technical challenges:** must fail-open when unset (no behavior change for existing deployments); trusted-proxy header handling must match the existing `IPAllowed` semantics.
- **Recommended approach:** add an `AdminIPAllowlist` setting checked in the admin middleware chain (`middleware.go:159-161`), reusing `IPAllowed` (`middleware.go:263-279`); fail-open when unset.
- **Acceptance criteria:** with CIDRs configured, admin endpoints 403 from other sources; unset = current behavior; allowlisted ranges support CIDR; test covers allow/deny.
- **Priority rationale:** P2 — defense-in-depth for the most privileged surface; small, opt-in.

**GAP-17 · Global registration control** — `SEC-017` — P3
- **Description:** no instance-wide open/closed registration toggle; only the verified-email gate on workspace create/invites/keys exists.
- **Competitor reference:** SEC-017 SP✓ KE✓ MC✓ (`02-competitor-comparison.md` §1.I).
- **Current status:** PARTIAL. **Evidence:** "Verified-email gate on workspace create/invites/keys; global registration toggle not documented in audits" (registry).
- **Missing requirements:** instance-wide open/closed registration toggle surfaced in admin settings.
- **Dependencies:** none.
- **Technical challenges:** must not affect existing users, invitations, or API-key creation; discoverability in admin settings UI.
- **Recommended approach:** settings-driven gate in the registration route (`routes/routes.go` signup path) reusing the settings provider pattern; admin UI toggle in the existing Settings view.
- **Acceptance criteria:** toggle off → registration endpoint returns 403 with a clear error; existing users/invites unaffected; default preserves current behavior.
- **Priority rationale:** P3 — modest but standard self-host control.

### 2.4 Developer experience (GAP-18…24)

**GAP-18 · Webhook test-fire endpoint (and commit the in-flight replay)** — `API-013` — P2 — **[in progress, pending commit]** (replay half; §4)
- **Description:** no test endpoint and no user-facing replay of failed webhook deliveries; the replay exists only as uncommitted working-tree code excluded from the registry.
- **Competitor reference:** API-013 NU✓ — the reference workflow platform test events; PO 7-day request history with debugging (`02-competitor-comparison.md` §1.H).
- **Current status:** MISSING (test-fire); replay in flight. **Evidence:** "No test endpoint or replay (replay exists only as post-a6f1681 untracked code — excluded)" (registry; audit-H webhook testing row); untracked `webhook_replay_handler.go`.
- **Missing requirements:** `POST /webhook-endpoints/{id}/test` dispatching a sample event; user-facing replay of failed deliveries.
- **Dependencies:** webhook dispatcher + delivery history (both IMPLEMENTED).
- **Technical challenges:** test-fire must traverse the endpoint's real filters so results are meaningful; replay must reuse the SSRF-guarded transport and be audited and rate-limited.
- **Recommended approach:** commit the in-flight replay path (§4) after review; add a test-fire action that enqueues a synthetic event through `dispatcher.go` with the endpoint's real filters.
- **Acceptance criteria:** test-fire appears in delivery history with a distinguishable event; replay re-delivers a failed attempt and records a new attempt; both audited.
- **Priority rationale:** P2 — the #1 integration debugging affordance; history exists, the trigger is missing.

**GAP-19 · Contact upsert/sync API** — `CON-012` — P2
- **Description:** import dedupe + CRUD exist but there is no explicit upsert endpoint for external sync loops.
- **Competitor reference:** CON-012 US✓ NU✓ HP✓ — exemplar create-or-update keyed on email (`02-competitor-comparison.md` §1.C).
- **Current status:** PARTIAL. **Evidence:** "Import dedupe + CRUD exist; no explicit upsert endpoint" (registry; audit-C).
- **Missing requirements:** `PUT /subscribers?upsert_by=email` semantics for external sync.
- **Dependencies:** subscriber unique index per (workspace,email) exists (`subscriber.go:66-67`).
- **Technical challenges:** concurrent upserts must rely on the unique index to avoid duplicates; webhook `contact.created` must fire only on true create.
- **Recommended approach:** extend `POST /subscribers` with an `upsert` flag; on conflict update fields/status per policy; fire `contact.created` only on true create (hook exists at `subscriber_handler.go:162-166`).
- **Acceptance criteria:** upsert creates then updates the same row on repeat calls; import path unchanged; concurrent upserts don't duplicate (unique-index test).
- **Priority rationale:** P2 — removes a whole class of client-side sync workarounds.

**GAP-20 · SMTP diagnostics deepening** — `INF-033`, `ANA-013` — P3
- **Description:** last-error text plus `smtp_code`/`smtp_stage` metadata exist, but no EHLO capability dump, TLS cert summary, or conversation transcript.
- **Competitor reference:** ANA-013 HR✓ PO◐ (`02-competitor-comparison.md` §1.A/F).
- **Current status:** PARTIAL. **Evidence:** "no EHLO/TLS transcript" (registry); "Last-error text + `smtp_code`/`smtp_stage` metadata; no conversation transcript" (audit-F).
- **Missing requirements:** EHLO capability dump + TLS cert summary persisted on `TestConnection`; optional per-message conversation capture (setting-gated).
- **Dependencies:** none.
- **Technical challenges:** transcripts can contain credentials/PII — must be redacted and retention-covered; storage size bounded.
- **Recommended approach:** extend `TestConnection` (`smtp_sender.go:186-203`) to store EHLO lines + peer certificate info in `validation_error`/metadata; optionally capture the last SMTP dialogue per failed send behind a settings flag.
- **Acceptance criteria:** server test response includes EHLO capabilities + TLS version/cert subject; failed messages optionally retain a redacted transcript; retention covers it.
- **Priority rationale:** P3 — diagnostics depth, not correctness.

**GAP-21 · Message tagging/correlation** — `INF-032` — P3
- **Description:** only caller `ExternalID` correlation exists; no free-form tag model for grouping/filtering/analytics.
- **Competitor reference:** INF-032 PO✓ BM◐ (`02-competitor-comparison.md` §1.A).
- **Current status:** PARTIAL. **Evidence:** "`ExternalID` caller correlation only (`service.go:343`); no tag model" (registry).
- **Missing requirements:** free-form tags on emails accepted at send, filterable in logs, exposed in analytics breakdowns.
- **Dependencies:** none.
- **Technical challenges:** array-column filtering must stay indexed; retention unaffected.
- **Recommended approach:** `tags text[]` on `emails` accepted at send, filterable in logs endpoints (ILIKE search exists in `logs_handler.go`); expose in analytics breakdowns.
- **Acceptance criteria:** send with `tags` → stored, returned, filterable; analytics can group by tag; retention unchanged.
- **Priority rationale:** P3 — nice-to-have correlation layer.

**GAP-22 · CLI client** — `API-017` — P3
- **Description:** the binary ships only server/worker/doctor subcommands; there is no operator CLI against the REST API.
- **Competitor reference:** API-017 PO✓ BM✓ WR✓ (`02-competitor-comparison.md` §1.H).
- **Current status:** MISSING. **Evidence:** "Only server/worker/doctor subcommands … `doctor` is an upgrade-readiness checker" (audit-H).
- **Missing requirements:** `posta` client subcommands (send, keys, domains) against the REST API.
- **Dependencies:** none.
- **Technical challenges:** keeping the single-binary ethos (same image) without bloating the server entrypoint; auth via `psk_` keys only.
- **Recommended approach:** add a `cmd/posta-cli` using the documented SDK envelope; ship in the same image.
- **Acceptance criteria:** send/api-key/domain CRUD usable with `psk_` key auth; documented in the SDK overview.
- **Priority rationale:** P3 — operator convenience; API completeness already high (340 routes).

**GAP-23 · SDK completeness verification** — `API-008` — P3
- **Description:** six SDK languages are documented, but the Python and .NET packages are not confirmed to exist on their registries; coverage of newest endpoints unverified.
- **Competitor reference:** API-008 PO✓ US✓ WR✓ (`02-competitor-comparison.md` §1.H).
- **Current status:** PARTIAL. **Evidence:** "Python and .NET packages not confirmed to exist on their registries (audit-H §5 item 1)".
- **Missing requirements:** registry confirmation (or publishing) of `posta` (PyPI) and `Posta` (NuGet); SDK coverage statement for the current route set.
- **Dependencies:** none.
- **Technical challenges:** out-of-tree repos cannot be verified from this codebase; needs a CI or docs-side coverage check.
- **Recommended approach:** verify/publish the two packages; add a CI check that SDK repos cover the current route set (or document lag).
- **Acceptance criteria:** PyPI/NuGet packages installable; docs note coverage level; Go/Node/PHP/Java already confirmed.
- **Priority rationale:** P3 — documentation-hygiene item.

**GAP-24 · MCP server** — `API-016` — P3
- **Description:** zero Model-Context-Protocol references; competitors increasingly expose AI-agent surfaces.
- **Competitor reference:** API-016 SE✓ MC✓ WR✓ LM◐ (`02-competitor-comparison.md` §1.H).
- **Current status:** MISSING. **Evidence:** "Zero MCP references (grep clean)" (audit-H).
- **Missing requirements:** an MCP server exposing read + send actions over the existing API.
- **Dependencies:** API keys/scopes (exists).
- **Technical challenges:** must not create a new auth surface; write/admin scopes excluded by default.
- **Recommended approach:** thin MCP wrapper over the REST API with read + send scopes only.
- **Acceptance criteria:** an MCP client can list/send/query with an API key; write/admin scopes excluded by default.
- **Priority rationale:** P3 — emerging differentiator; low effort given the API surface.

### 2.5 Marketing (GAP-25…39)

**GAP-25 · Double opt-in (DOI)** — `CON-004` — P1
- **Description:** entirely absent — subscription is immediate on subscribe; the single largest EU-marketing compliance blocker.
- **Competitor reference:** CON-004 LM✓ BM✓ US✓ NU✓ KE✓ MC✓ WR✓ HP✓ — exemplars: Listmonk per-list DOI toggle with `subscriber-optin` system template; Keila editable DOI + welcome emails 0.40.0 (`02-competitor-comparison.md` §1.C).
- **Current status:** MISSING. **Evidence:** "Entirely absent (grep clean); EU-marketing compliance blocker (audit-C top gap)"; `POST /subscriber-lists/subscribe` subscribes immediately (`subscriber_list_handler.go:373-460`).
- **Missing requirements:** pending status, confirmation token flow, confirmation email, expiry/resend, DOI-aware imports.
- **Dependencies:** hosted unsubscribe/tracking pages (`tracking_pages.go`) as the token-page pattern; email sending.
- **Technical challenges:** imports must honor per-list DOI policy; token expiry needs a hosted error path; confirmed state must drive campaign eligibility without breaking single-opt-in lists.
- **Recommended approach:** add a `pending` subscriber status + per-list DOI setting; tokenized confirm endpoint modeled on the `/t/u/{token}` HMAC tokens (`tracking.go:273-355`); confirm → `list.subscribed` trigger (feeds welcome automations); imports honor list DOI policy.
- **Acceptance criteria:** subscribe creates `pending`, sends confirmation, and only confirmed members receive campaigns; expired tokens show the hosted page error path; single-opt-in lists unchanged; E2E test from subscribe → confirm → campaign eligibility.
- **Priority rationale:** P1 — the single largest compliance blocker for EU marketing use.

**GAP-26 · Campaign test-send + campaign-scoped preview** — `CAM-007` — P1
- **Description:** the template send-test ignores campaign data; no campaign-scoped preview endpoint or UI — the most-requested marketer affordance.
- **Competitor reference:** CAM-007 LM✓ SP✓ MC✓ NU✓ — exemplar Listmonk preview + test email with campaign data (`02-competitor-comparison.md` §1.D).
- **Current status:** PARTIAL. **Evidence:** "Template send-test ignores campaign data; no campaign-scoped preview endpoint/UI (`template_handler.go:273`) — top usability gap" (registry); "Campaigns have no test-send endpoint … Top usability gap" (audit-D).
- **Missing requirements:** `POST /campaigns/{id}/test-send` (render campaign + template + `template_data` + localization to a real inbox); a preview endpoint/UI in CampaignDetail.
- **Dependencies:** template render pipeline + send-test (both exist).
- **Technical challenges:** content resolution must match the campaign processor exactly (per-recipient data omitted, sample subscriber injected) or tests mislead.
- **Recommended approach:** add a campaign test-send handler that resolves content exactly like `campaign_processor.go:290-367` and reuses `SendTestByTemplateID` (`service.go:1186-1215`); preview endpoint returns rendered HTML.
- **Acceptance criteria:** the test email reflects campaign subject/`template_data`/language; preview shows localized HTML without sending; UI button in CampaignDetail; endpoint documented.
- **Priority rationale:** P1 — the most-requested marketer affordance; everything behind it exists.

**GAP-27 · Data export completeness** — `CON-010`, `ANA-011` — P1
- **Description:** only audit-log CSV + GDPR JSON bundle exist; no export for subscribers, lists, segments, analytics, or message history — an asymmetric portability story.
- **Competitor reference:** CON-010 BM✓ SP✓ KE✓ MC✓ WR✓ HP✓; ANA-011 SE✓ (`02-competitor-comparison.md` §1.C/F).
- **Current status:** CON-010 MISSING; ANA-011 PARTIAL. **Evidence:** "Only audit-log CSV + GDPR JSON bundle exist (`logs_handler.go:853`); asymmetric portability"; "Audit-log CSV only (`logs_handler.go:821`); no analytics/message export" (registry; audits C/F).
- **Missing requirements:** CSV export for subscribers/lists/segments and for analytics/message history.
- **Dependencies:** the existing streamed CSV exporter with 100k cap (`logs_handler.go:821-892`) — reuse directly.
- **Technical challenges:** exports must match list filters and segment definitions row-for-row; streaming must hold at scale; scope gating consistent with existing rules.
- **Recommended approach:** add `GET /subscribers/export`, `/lists/{id}/export`, `/segments/{id}/export` (reuse the segment query builder) and `GET /analytics/export?from=&to=`; stream via the same helper; admin-gated by existing scope rules.
- **Acceptance criteria:** exports match list filters and segment definitions row-for-row; large exports stream (cap honored); audit-logged.
- **Priority rationale:** P1 — closes the import/export asymmetry flagged in both audits; mostly plumbing.

**GAP-28 · A/B winner selection, committed end-to-end** — `CAM-008`, `AUT-008` — P2 — **[in progress, pending commit]** (§4)
- **Description:** split assignment exists but no winner selection/auto-rollout shipped at HEAD — `ABTestWinner` had zero writers repo-wide, variant `TemplateID` was dead, no UI; a winner implementation sits uncommitted in the working tree.
- **Competitor reference:** CAM-008 NU✓ KE▲; AUT-008 NU✓ — the reference workflow platform is the only shipped split+winner (`02-competitor-comparison.md` §1.D/G).
- **Current status:** PARTIAL at HEAD; winner flow uncommitted. **Evidence:** "No winner selection/auto-rollout (`campaign.go:88 ABTestWinner` never written — zero writers repo-wide)" (registry; audit-D correction 4; re-verified at HEAD via `git show HEAD:internal/worker/campaign_processor.go`); working tree: `campaign.go:109-118` (`ABTestWinner`, `ABTestWinnerSent`, `ABTestWinnerStats`), winner hold/selection in `campaign_processor.go` (`isHeldForWinner`), untracked `campaign_ab_repo.go` + `campaign_winner_test.go`, winner UI in `CampaignDetail.vue:30,317-327`.
- **Missing requirements:** commit the in-flight winner flow (hold, pick by open/click after window, send winner to remainder, persist `ABTestWinnerStats`); campaign-create UI for variants; automation node outcome persistence.
- **Dependencies:** per-variant analytics (exists, `tracking_repo.go:122+`).
- **Technical challenges:** held messages must never send before the decision; tie/no-signal fallback needs deterministic rules; automation ab_test assignment must persist as an experiment outcome.
- **Recommended approach:** land the working-tree winner implementation after test review (R-08 series in `CampaignDetail.vue`, `campaign_winner_test.go`); add create-form variant fields; persist automation ab_test assignment as an experiment outcome.
- **Acceptance criteria:** a split campaign auto-selects a winner after the window and mails the remainder; winner + stats visible in UI; held messages never send before decision; tests cover tie/no-signal fallback.
- **Priority rationale:** P2 — high-value, mostly already written; needs commit discipline + UI.

**GAP-29 · Subscriber tags** — `CON-006` — P2
- **Description:** no `Tag` model anywhere; SendPortal uses tags as its primary segmentation mechanism.
- **Competitor reference:** BM✓ SP✓ (LM✗ US✗ WR✗) — exemplar SendPortal tags-as-segmentation (`02-competitor-comparison.md` §1.C).
- **Current status:** MISSING. **Evidence:** "No `Tag` model anywhere … SP uses tags as its primary segmentation (competitor-parity gap, audit-C)".
- **Missing requirements:** tag taxonomy on subscribers with CRUD, bulk tagging, tag filters, segmentation integration.
- **Dependencies:** none; note segments cannot reference list membership today (`segment_query.go` operates over subscribers only).
- **Technical challenges:** the new `in_tags` segment operator must pass the guarded builder's whitelist and injection tests; CSV import/export columns must round-trip.
- **Recommended approach:** `subscriber_tags` join table + tag CRUD; add an `in_tags` segment operator to the guarded builder (`segment_query.go` whitelist pattern); CSV import/export column support (pairs with GAP-27).
- **Acceptance criteria:** tags assignable singly/bulk/import; a segment can filter by tag; list endpoints filter by tag; injection tests extended.
- **Priority rationale:** P2 — parity with the two competitors that lean on tags; fits the existing segment engine.

**GAP-30 · Signup forms** — `CON-017` — P2
- **Description:** RF "forms" are website-form→inbox capture, not audience signup; no hosted/embedded forms that create subscribers.
- **Competitor reference:** LM✓ NU✓ KE✓ MC✓ HP✓ — exemplars: Keila drag-and-drop form fields + captcha; Listmonk public subscription pages with private-list UUID blocking (`02-competitor-comparison.md` §1.C).
- **Current status:** MISSING. **Evidence:** "RF 'forms' = website-form→inbox, not audience capture (audit-C explicit classification)"; CON-018 N/A because no signup forms exist.
- **Missing requirements:** hosted/embedded forms creating subscribers into a chosen list, success states, DOI hook, bot protection.
- **Dependencies:** GAP-25 (DOI); form nonce/time-trap machinery (exists for inbound forms, `form_ingest_handler.go:69-145` — reuse as bot protection).
- **Technical challenges:** private lists must not be addressable; spam triage must reuse the inbound-form pipeline; embed snippet serving (instance or tracking domain, GAP-09).
- **Recommended approach:** new `signup_forms` entity (slug, target list, styling) + public route with the existing Redis nonce + `MinFillSeconds` time-trap; DOI hook from GAP-25; embed snippet served from the instance or tracking domain.
- **Acceptance criteria:** a public form creates (pending→confirmed) subscribers into its list; nonce/time-trap + IP limiter enforced; private lists not addressable; spam submissions triaged like inbound forms.
- **Priority rationale:** P2 — closes the audience-acquisition funnel end; reuses two existing protections.

**GAP-31 · Bulk contact operations** — `CON-011` — P2
- **Description:** bulk operations are import-only; single-item delete; GDPR purge is whole-workspace only.
- **Competitor reference:** LM✓ US✓ NU✓ HP✓ — exemplar Listmonk bulk select actions (delete/blocklist/list ops) (`02-competitor-comparison.md` §1.C).
- **Current status:** PARTIAL. **Evidence:** "Import-only bulk; single-item delete; GDPR purge is whole-workspace (`workspace_data_handler.go:271`)" (registry).
- **Missing requirements:** bulk delete, bulk status change, bulk list add/remove by selected IDs or segment.
- **Dependencies:** segment engine (for by-segment bulk), member routes.
- **Technical challenges:** segment-scoped bulk must match the segment preview count exactly; caps prevent unbounded single transactions; per-action audit events.
- **Recommended approach:** `POST /subscribers/bulk` with `{action, ids|segment_id}` executed in batches with per-action audit events; reuse `SuppressMember`/`UnsuppressMember` tx patterns (`subscriber_list_repo.go:146-160`).
- **Acceptance criteria:** bulk delete/status/list-add apply to all matched subscribers; segment-scoped bulk matches the segment preview count; audit events recorded; caps prevent unbounded transactions.
- **Priority rationale:** P2 — standard list-management affordance; medium surface, high marketer value.

**GAP-32 · Contact activity timeline (+ unsubscribe-reason reporting)** — `CON-019`, `CON-015` — P2
- **Description:** events live in the Logs/messages views; no per-subscriber timeline in the detail view; unsubscribe reasons are stored but never aggregated.
- **Competitor reference:** CON-019 BM✓ KE✓ NU✓ US◐ (`02-competitor-comparison.md` §1.C).
- **Current status:** PARTIAL (both). **Evidence:** "Events live in Logs/messages; no per-subscriber timeline in detail view"; "Reason column exists (`subscriber_list.go:94`); no analytics UI" (registry; audit-C).
- **Missing requirements:** per-subscriber timeline (sends/opens/clicks/bounces/unsubs); unsubscribe-reason breakdown.
- **Dependencies:** `email_events`/`tracking_events` (exist); aggregator queries.
- **Technical challenges:** joining events by subscriber email + campaign_messages needs indexes; timeline must link back to messages without duplicating the Logs read model.
- **Recommended approach:** `GET /subscribers/{id}/activity` joining email_events by subscriber email + campaign_messages; render in SubscriberDetail; reason histogram from the existing column.
- **Acceptance criteria:** detail view shows chronological events with links to messages; reasons aggregated per list/workspace; queries indexed.
- **Priority rationale:** P2 — pure read-model over existing data; high support/debug value.

**GAP-33 · Campaign operability: duplication UI + failed-recipient retry** — `CAM-004`, `CAM-018` — P2
- **Description:** campaign duplication exists as a backend endpoint with zero UI/API-client call sites; per-message manual retry exists but no campaign-level retryFailed.
- **Competitor reference:** CAM-018 NU✓ — the reference workflow platform `broadcasts.retryFailed` holding failures 7 days (`02-competitor-comparison.md` §1.D).
- **Current status:** PARTIAL (both). **Evidence:** "Backend endpoint only — zero UI/API-client call sites (`campaign_handler.go:466`)"; "Per-message manual retry API only … no campaign-level retryFailed" (registry; audit-D).
- **Missing requirements:** Duplicate button wired to the existing endpoint; `POST /campaigns/{id}/retry-failed` re-enqueueing failed campaign messages.
- **Dependencies:** per-message retry (`workspace_resource_routes.go:116`) and the CampaignMessage ledger (exist).
- **Technical challenges:** retry-failed must select `failed` campaign_messages since a timestamp and re-enqueue exactly once (TaskID dedup interplay); snapshot analytics must stay unaffected.
- **Recommended approach:** add the UI action (client function + button) for duplication; add campaign-level retry that selects `failed` campaign_messages since a timestamp and re-enqueues via the batch producer.
- **Acceptance criteria:** duplicate creates a fresh editable draft copying audience/settings; retry-failed re-sends only failed messages once; both audited; snapshot analytics unaffected.
- **Priority rationale:** P2 — small wires over existing machinery; visible operator value.

**GAP-34 · Reusable blocks/sections library + campaign content slots** — `TPL-002`, `TPL-012`, `CAM-006` — P2
- **Description:** stock GrapesJS newsletter preset only — no product block library or saved sections; no block-level override (template object is atomic).
- **Competitor reference:** TPL-012 absent industry-wide (registry lists "—"), which lowers urgency; TPL-002 LM✓ BM✓ US✓ NU✓ KE✓ MC◐ (`02-competitor-comparison.md` §1.D/E).
- **Current status:** PARTIAL (TPL-002/012); CAM-006 MISSING. **Evidence:** "Stock GrapesJS newsletter preset only — no product block library or saved sections (`EmailBuilder.vue:41`)"; "No block model/route (audit-E)"; "No block-level override; template object is atomic" (registry; audit-E).
- **Missing requirements:** saved block/section entities surfaced as GrapesJS custom blocks; campaign-level overrides of template blocks/slots.
- **Dependencies:** BuilderJSON persistence (exists, `template_localization.go:31`); preview integration pairs with GAP-26.
- **Technical challenges:** blocks must round-trip through export/import; overrides must apply post-resolve without breaking localization; GrapesJS block-manager integration.
- **Recommended approach:** `template_blocks` entity (name, HTML, category) rendered into the GrapesJS block manager on load; campaign-level `content_overrides` JSONB applied post-resolve in `resolveTemplateContent` (`campaign_processor.go:447-536`).
- **Acceptance criteria:** a saved block appears in the builder for all templates and round-trips through export/import; a campaign override replaces the named block at send; overrides render in preview.
- **Priority rationale:** P2 — biggest editor UX lever; scoped because block libraries are unshipped even by most competitors.

**GAP-35 · Preference center** — `CON-016` — P3
- **Description:** only a forward-reference comment exists; no hosted page where a recipient toggles list memberships/language.
- **Competitor reference:** NU✓ WR✓ LM◐ KE◐ HP◐ — exemplar Listmonk `?manage=true` subscription-management page (`02-competitor-comparison.md` §1.C).
- **Current status:** MISSING. **Evidence:** "Only a forward-reference comment (`suppression.go:26`)" (registry).
- **Missing requirements:** hosted preference page linked from unsubscribe pages; list-scope toggles writing the existing opt-out tables.
- **Dependencies:** hosted unsubscribe pages (foundation exists, `tracking_pages.go`); list-scoped opt-out tables (exist).
- **Technical challenges:** HMAC token scope must limit toggles to that recipient; page CSP must match the `/t/v` hardening; changes must reflect in campaign eligibility.
- **Recommended approach:** extend the campaign unsubscribe page with a token-authenticated preferences view listing public lists; toggles write `SubscriberListUnsubscribe` via existing `SuppressMember`/`UnsuppressMember`.
- **Acceptance criteria:** recipient can opt out of specific lists and back in; changes reflect in campaign eligibility; HMAC token scope limits to that recipient; page CSP matches `/t/v` hardening.
- **Priority rationale:** P3 — consent UX maturity beyond one-click; foundation already built.

**GAP-36 · Template authoring completeness: preview text, Markdown part, media library** — `TPL-016`, `TPL-003`, `TPL-022` — P3 — **verify-first** (§6)
- **Description:** preheader support is unverified (NV), no Markdown authoring option exists, and the template media-library surface was never audited.
- **Competitor reference:** TPL-016 KE✓ US✓; TPL-003 LM✓ KE✓ MC✓ (`02-competitor-comparison.md` §1.E).
- **Current status:** TPL-016 NEEDS_VERIFICATION; TPL-003 MISSING; TPL-022 NEEDS_VERIFICATION. **Evidence:** "No evidence in audits; preheader unverified" (registry); "No Markdown in pipeline" (audit-E); "Blob store exists for attachments; template media-library surface not audited" (registry).
- **Missing requirements:** preheader field injected into rendered HTML; Markdown→HTML authoring option; media library over the blob store for template images.
- **Dependencies:** html2text (exists); blob store FS/S3 (exists); §6 verification first.
- **Technical challenges:** Markdown rendering needs a sanitizer; media uploads need per-workspace blob keys and picker integration in both editors.
- **Recommended approach:** verify current behavior first (§6); add a `preheader` field rendered after `<body>`; optional `format: markdown` localization rendered through a small Go markdown lib with sanitizer; `POST /media` upload + picker in editors with per-workspace blob keys.
- **Acceptance criteria:** preheader appears as hidden preview text; Markdown templates render + auto-generate text part; media uploads list/delete with blob-backed URLs; all behind the existing render pipeline.
- **Priority rationale:** P3 — three small authoring conveniences; verification first.

**GAP-37 · UTM auto-tagging** — `CAM-015` — P3
- **Description:** no UTM rewriting in the tracking pipeline; only one competitor ships it, so parity value is modest.
- **Competitor reference:** NU✓ (only) — the reference workflow platform auto-appends utm params (`02-competitor-comparison.md` §1.D).
- **Current status:** MISSING. **Evidence:** "No UTM rewriting in tracking pipeline (audit-D/E)".
- **Missing requirements:** per-campaign UTM template appended to external links before rewrite.
- **Dependencies:** click-rewrite pipeline (`tracking.go:65-155`).
- **Technical challenges:** skip-list correctness (internal `/t/`, mailto, tel) must match the existing rewrite skip list; default-off.
- **Recommended approach:** campaign-level `utm_params` JSONB; in `ProcessHTML`, append to outbound hrefs (skipping `/t/`, mailto, tel — same skip list) before tracking rewrite.
- **Acceptance criteria:** enabled campaign links carry utm_source/medium/campaign; skips internal/tracked/mailto/tel; disabled by default; preview shows tagged links.
- **Priority rationale:** P3 — small, contained; single-competitor feature.

**GAP-38 · Newsletter archive & publications** — `CAM-012`, `CAM-013` — P3
- **Description:** web-view tokens are per-recipient and expiring — not a public archive; campaigns are a flat entity with no publication grouping.
- **Competitor reference:** LM✓ KE✓ MC✓ HP✓ — exemplar Listmonk public archive with per-campaign publish (`02-competitor-comparison.md` §1.D).
- **Current status:** MISSING (both). **Evidence:** "Web-view tokens are per-recipient + expiring, not an archive"; "Flat `campaigns` entity; no publication grouping" (registry; audit-D).
- **Missing requirements:** opt-in public archive of sent campaigns; publication grouping entity.
- **Dependencies:** web-view rendering (`tracking_handler.go:345-371`) — but archive pages must be recipient-independent.
- **Technical challenges:** subscriber-variable leakage is the core design risk (Listmonk substitutes a metadata object for `.Subscriber`); unsub links must be stripped from archive copies; noindex + CSP as `/t/v`.
- **Recommended approach:** `publication` grouping + per-campaign `public` flag; archive page renders the campaign's template with archive metadata; noindex default, CSP as `/t/v`.
- **Acceptance criteria:** published campaigns visible at a public URL without per-recipient tokens; unpublished campaigns 404; unsub links removed from archive copies; publications group/filter campaigns in UI.
- **Priority rationale:** P3 — real feature for newsletter use cases; design carefully around subscriber-var leakage.

**GAP-39 · Timezone-aware sending: commit in-flight work** — `CAM-016` — P3 — **[in progress, pending commit]** (§4)
- **Description:** shipped behavior is a stub (`SendAtLocalTime` field logs intent and sends one batch); per-subscriber local-time splitting sits uncommitted.
- **Competitor reference:** NU✓ (workspace-timezone only, not per-subscriber); absent industry-wide otherwise (`02-competitor-comparison.md` §1.D).
- **Current status:** PARTIAL (stub at HEAD); implementation in flight. **Evidence:** "Stub: `SendAtLocalTime` field exists, logs intent, sends one batch (`campaign_processor.go:230`)" (registry); untracked `campaign_timezone_test.go` in the working tree.
- **Missing requirements:** commit + finish the per-subscriber local-time batch split using the subscriber `timezone` column (IANA-validated, exists).
- **Dependencies:** scheduler (committed `347ebb4`).
- **Technical challenges:** DST edges; deferred batches must survive restart; flag default-off to avoid behavior change.
- **Recommended approach:** land the in-flight implementation after review; batch messages by UTC-offset hour, enqueue deferred batches like `enqueueDeferredBatch` already does for winner rechecks.
- **Acceptance criteria:** recipients in distinct timezones receive at their local hour; deferred batches survive restart; tests cover DST edges; feature flag default-off.
- **Priority rationale:** P3 — differentiator with no peer pressure; finish what is started rather than expand scope.

### 2.6 Analytics (GAP-40)

**GAP-40 · Device/geo breakdowns** — `ANA-015` — P3
- **Description:** no cohort/geo/device breakdowns in campaign analytics.
- **Competitor reference:** NU✓ — the reference workflow platform device/client breakdowns (`02-competitor-comparison.md` §1.F).
- **Current status:** MISSING. **Evidence:** "Explicit audit-F gap: no cohort/geo/device breakdowns" (registry; audit-F campaign analytics row).
- **Missing requirements:** UA parsing (device/client) and IP-geo enrichment on tracking events, with breakdown endpoints.
- **Dependencies:** privacy posture — coordinate with GAP-15; consider IP truncation by default.
- **Technical challenges:** privacy trade-offs need explicit decisions; parse at record time (no retro-parse); optional local mmdb for geo.
- **Recommended approach:** store parsed `ua_device`/`ua_client` on `tracking_events` at record time; aggregate in the analytics repo pattern (`analytics_repo.go:49-330`); geo via an optional local mmdb.
- **Acceptance criteria:** campaign analytics show open/click by device class and client; no raw-IP retention beyond current windows; toggle-able; provider breakdown unaffected.
- **Priority rationale:** P3 — nice-to-have; privacy trade-offs need explicit decisions.

### 2.7 Automation (GAP-41…43)

**GAP-41 · Workflow testing / dry-run** — `AUT-018` — P2
- **Description:** static validation only (reachability/cycles/weights); no test execution against a sample contact.
- **Competitor reference:** WR◐ — partial peer; NU ships testing implicitly via executions (`02-competitor-comparison.md` §1.G).
- **Current status:** MISSING. **Evidence:** "Static validation only; no test execution" (registry); audit-G workflow testing row: "No test-contact run, no payload simulator".
- **Missing requirements:** `POST /automations/{id}/dry-run` executing the graph against a sample contact without side effects.
- **Dependencies:** engine execution paths (exist, ~40 tests); `SendAutomationEmail` must be stubbed.
- **Technical challenges:** guaranteeing zero side effects (no sends, no trigger-log rows, no list mutations, no enrollment) requires no-op node executors + a synthetic journey flagged `dry_run`.
- **Recommended approach:** run the engine in a side-effect-free mode (inject no-op node executors + a synthetic journey row flagged `dry_run`), returning the traversed path + per-node decisions.
- **Acceptance criteria:** dry-run reports branch decisions and would-be delays; no email sends, no automation_trigger_log rows, no list mutations; UI button in the builder.
- **Priority rationale:** P2 — complements the strong static validation; standard pre-live safety.

**GAP-42 · Re-engagement & scheduled enrollment** — `AUT-015` — P2
- **Description:** triggers are purely event-reactive; no inactivity conditions and no cron enrollment sweep; conditions limited to email/name/status/in_list/custom_fields.
- **Competitor reference:** WR✓ — Wraps is the only peer with scheduled enrollment (`02-competitor-comparison.md` §1.G).
- **Current status:** PARTIAL. **Evidence:** "Triggers purely event-reactive; no inactivity conditions, no cron enrollment (audit-G gap)"; conditions limited (`domain.go:632-661`).
- **Missing requirements:** inactivity-based conditions (last engagement older than N days) and a cron enrollment sweep.
- **Dependencies:** engagement subqueries already exist in the segment engine (`segment_query.go:109-131`) — reuse as the condition source.
- **Technical challenges:** scheduled enrollment must respect re-enrollment policy and the existing once-dedup; sweep cost bounded per run.
- **Recommended approach:** add a `schedule` trigger kind evaluated by a daily cron that scans subscribers matching the automation's condition set (segment-engine reuse) and calls `Enroll` with the existing once-dedup (`triggers.go:119-133`).
- **Acceptance criteria:** an inactivity automation enrolls matching subscribers on schedule, respects re-enrollment policy, and excludes subscribed-elsewhere per current trigger rules; cron visible in `/admin/crons`.
- **Priority rationale:** P2 — converts the reactive engine into a proactive retention tool with machinery that already exists.

**GAP-43 · Stop-on-reply** — `AUT-016` — P2
- **Description:** `ExitOnReply` is a reserved stub the engine never reads; no reply detection exists despite `message.replied` webhook events in the registry.
- **Competitor reference:** NU✓ — the reference workflow platform reply detection via In-Reply-To/References (Mailgun/SES inbound) (`02-competitor-comparison.md` §1.G).
- **Current status:** MISSING. **Evidence:** "`ExitOnReply` reserved stub the engine never reads; no reply detection (audit-G)".
- **Missing requirements:** reply detection (inbound message matched to sent email via In-Reply-To/References or recipient email) + journey exit hook.
- **Dependencies:** inbound email pipeline (IMPLEMENTED, `inbound_routes.go`); journey exit transactional path (`automation_handler.go:491-509`).
- **Technical challenges:** matching must work against References headers and bare-email fallback without false exits; relay/deployment prerequisites must be documented.
- **Recommended approach:** on inbound ingest, match the sender email to recent recipients, read `ExitOnReply` on their active journeys, and exit transactionally; document relay/deployment prerequisites.
- **Acceptance criteria:** a reply exits an active journey with reason `reply`; journeys without the flag continue; matching tested against References and bare-email fallback; no exits for unrelated inbound.
- **Priority rationale:** P2 — completes the automation lifecycle story; plumbing mostly present.

### 2.8 Administration (GAP-44…47)

**GAP-44 · Per-API-key rate limits** — `API-007` — P2
- **Description:** hourly/daily send quotas only; no per-API-key or per-endpoint limits; quota-store failure fails closed.
- **Competitor reference:** API-007 SP✓ KE✓ MC✓ HR✓ WR✓ HP✓ — broad peer coverage (`02-competitor-comparison.md` §1.H).
- **Current status:** PARTIAL. **Evidence:** "Hourly/daily send quotas only; no per-API-key or per-endpoint limits (`config.go:311`); fails closed on store error" (registry; audit-H).
- **Missing requirements:** per-key request/throughput limits with 429 semantics.
- **Dependencies:** Redis limiter (`ratelimit/quota.go:44`) — extend rather than replace.
- **Technical challenges:** fail-closed store-outage behavior must be preserved and documented; plan-default inheritance; metrics for limit rejections.
- **Recommended approach:** optional per-key limits stored on `api_keys`, enforced in `RequireScope`-adjacent middleware with the same fixed-window counter keyed by key ID; plan defaults inheritable.
- **Acceptance criteria:** a limited key receives 429 past its quota with `Retry-After`; unlimited keys unaffected; store outage behavior documented (fail-closed preserved); metrics emit limit rejections.
- **Priority rationale:** P2 — protects multi-tenant fairness once ESP integrations (GAP-04) raise per-key value.

**GAP-45 · Blob usage reporting & orphan GC** — `OBS-013` — P2
- **Description:** FS/S3 blob store + retention delete blobs, but there is no usage reporting and no orphan-blob GC.
- **Competitor reference:** OBS-013 LM✓ US✓ NU✓ (`02-competitor-comparison.md` §1.J).
- **Current status:** PARTIAL. **Evidence:** "FS/S3 blob store + retention deletes blobs; no usage reporting or orphan GC (audit-J)"; AdminInventory counts rows, not bytes.
- **Missing requirements:** bytes-per-workspace reporting; orphan-blob sweep (blobs without DB rows).
- **Dependencies:** retention cron (extends).
- **Technical challenges:** GC must never delete referenced blobs — dry-run report first; interplay with retention windows must be tested.
- **Recommended approach:** nightly job computes per-workspace blob-prefix sizes into a cache surfaced in AdminInventory; GC pass deletes blobs older than a threshold with no referencing row.
- **Acceptance criteria:** admin sees storage usage per workspace; orphans older than N days are deleted with a dry-run report first; retention interplay tested.
- **Priority rationale:** P2 — operational hygiene for attachment-heavy deployments.

**GAP-46 · Admin impersonation** — `SEC-015` — P3
- **Description:** no admin "login as" capability for support.
- **Competitor reference:** SEC-015 HP✓ KE◐ (`02-competitor-comparison.md` §1.I).
- **Current status:** MISSING. **Evidence:** "No impersonation (audit-I confirmed-unchanged list)".
- **Missing requirements:** admin impersonation with heavy audit trail and scope guardrails.
- **Dependencies:** sessions/JTI (exists); audit events (exist).
- **Technical challenges:** must not weaken the strong tenancy story — no admin routes reachable, visually flagged, time-boxed, opt-out honored.
- **Recommended approach:** admin-only endpoint issuing a time-boxed impersonation session tagged in every audit event; no admin routes reachable; banner in UI.
- **Acceptance criteria:** impersonation sessions are visually flagged, audited with actor+target, expire ≤60 min, and cannot touch `/admin/*`; opt-out per user honored.
- **Priority rationale:** P3 — support convenience; guardrails are the design constraint.

**GAP-47 · Antivirus hook** — `SEC-011` — P3
- **Description:** the inbound pipeline has no antivirus hook.
- **Competitor reference:** SEC-011 PO✓ — Postal ClamAV integration (`02-competitor-comparison.md` §1.I).
- **Current status:** MISSING. **Evidence:** "inbound pipeline has no AV hook (audit-I)"; grep clean.
- **Missing requirements:** optional ClamAV (clamd) scan of inbound attachments before blob storage/notification.
- **Dependencies:** inbound parse pipeline (`inbound/service.go`).
- **Technical challenges:** latency budget on ingest; fail-open on scanner outage with a security event; quarantine flow reuse.
- **Recommended approach:** setting-gated clamd TCP scan post-parse; infected attachments quarantined (blob retained, message flagged spam) reusing the message triage UI.
- **Acceptance criteria:** an EICAR test attachment is quarantined and flagged; scan disabled by default; failures fail-open with a security event; latency budgeted.
- **Priority rationale:** P3 — meaningful only for inbound-heavy deployments.

### 2.9 Scalability (GAP-48…49)

**GAP-48 · Queue operations: per-queue latency metric + pause/throttle** — `OBS-009` (+ INF-010 residual note) — P2
- **Description:** no per-queue latency metric (Postal added one in 3.3.0); per-queue pause/throttle not exposed.
- **Competitor reference:** OBS-009 PO✓ SP✓ KE✓ SE✓ — exemplar Postal per-queue latency (`02-competitor-comparison.md` §1.J).
- **Current status:** PARTIAL. **Evidence:** audit-A residual: "Per-queue pause/throttle not exposed"; no per-queue latency metric (registry).
- **Missing requirements:** per-queue latency histogram (enqueue→process); per-queue pause/throttle controls in the admin UI.
- **Dependencies:** asynq Inspector wiring (exists, `admin_queue_handler.go:37-306`).
- **Technical challenges:** latency requires task payload timestamps; pause must be checked pre-claim in producer/worker without leaking state.
- **Recommended approach:** emit `posta_queue_latency_seconds` from task payload timestamps; add pause/resume actions that set a Redis flag checked in the producer/worker pre-claim.
- **Acceptance criteria:** latency visible per queue in Prometheus + admin; pausing `bulk` stops new bulk claims while `transactional` flows; resume works; gauges tested.
- **Priority rationale:** P2 — rounds out the already-strong queue admin story.

**GAP-49 · Redis HA guidance & SPOF mitigation docs** — `OBS-004` (note) — P2
- **Description:** single Redis is a SPOF across 9 verified roles (broker, cache, limits, security windows, jti blacklist, nonces, heartbeats, pub/sub, quotas); no sentinel/cluster config docs; mixed failure semantics (security fail-open, quotas fail-closed).
- **Competitor reference:** RF-specific topology guidance (no registry flag).
- **Current status:** MISSING (docs). **Evidence:** "Single Redis is a SPOF; no sentinel/cluster config docs" (audit-J Redis row).
- **Missing requirements:** documented Sentinel/managed-Redis configuration, failover behavior matrix per subsystem, env examples.
- **Dependencies:** none (documentation + compose profile).
- **Technical challenges:** the failure-mode matrix must cite the verified fail-open/fail-closed callsites accurately; a sentinel compose profile must boot clean.
- **Recommended approach:** add an HA ops doc + optional compose profile with sentinel; enumerate failure behaviors per subsystem (citing the verified callsites).
- **Acceptance criteria:** doc covers sentinel setup, each Redis role's failure mode, and recovery steps; a sentinel compose profile boots clean.
- **Priority rationale:** P2 — the single biggest availability lever and it is mostly writing (risk R10 in `06-architecture-impact.md`).

### 2.10 Documentation (GAP-50…53)

**GAP-50 · Backup/restore tooling & guidance** — `OBS-017` — P1
- **Description:** no in-repo backup/restore tooling or runbook; no Helm/k8s manifests either.
- **Competitor reference:** self-host hygiene; LM ships Helm via community (no registry flag).
- **Current status:** MISSING. **Evidence:** "Absent (seed R-11 still true, audit-J); no Helm/k8s manifests either".
- **Missing requirements:** documented backup/restore runbook (Postgres dump, Redis expendability, blob store sync), optional scripts, restore verification steps.
- **Dependencies:** none.
- **Technical challenges:** stating RPO/RTO honestly — Redis is ephemeral (queues), Postgres is the sole system of record; blob restore consistency.
- **Recommended approach:** ops doc + `make backup/restore` scripts against compose; state clearly that Redis is expendable and Postgres is the system of record.
- **Acceptance criteria:** a restore from backup reproduces a working instance via the documented procedure; blob restore covered; doc lists RPO/RTO expectations.
- **Priority rationale:** P1 — trivial effort, existential value for self-hosted data (risk R13 in `06-architecture-impact.md`).

**GAP-51 · Error-monitoring hook** — `OBS-011` — P2
- **Description:** errors are slog'ed only; no aggregate error signal (Sentry-compatible capture or error-rate counter).
- **Competitor reference:** OBS-011 PO◐ HP◐ NU◐ (`02-competitor-comparison.md` §1.J).
- **Current status:** MISSING. **Evidence:** "errors slog'ed only (audit-J); grep clean".
- **Missing requirements:** optional Sentry-compatible error capture (DSN env) or at minimum an error-rate Prometheus counter.
- **Dependencies:** none.
- **Technical challenges:** payloads must be sampled and scrubbed of PII/secrets; zero behavior change when unset.
- **Recommended approach:** minimal `slog` handler that forwards error-level records (sampled, scrubbed) to a Sentry DSN when configured; add a `posta_errors_total` family.
- **Acceptance criteria:** with DSN set, errors appear in Sentry with release/commit tags; unset = zero behavior change; no PII in payloads; metric counts match logs.
- **Priority rationale:** P2 — closes the "failures only visible as per-email failed states" blind spot cheaply.

**GAP-52 · Grafana sample dashboard** — `OBS-007` — P3
- **Description:** `/metrics` is the integration surface; no sample dashboard ships.
- **Competitor reference:** OBS-007 HR✓ (`02-competitor-comparison.md` §1.J).
- **Current status:** MISSING. **Evidence:** "`/metrics` is the integration surface; a sample dashboard JSON would close it cheaply (audit-J)".
- **Missing requirements:** bundled Grafana JSON covering the 20 metric families.
- **Dependencies:** metrics (exist; default-off).
- **Technical challenges:** panels must map 1:1 to documented families and stay in sync as families evolve.
- **Recommended approach:** author one dashboard JSON (queue depth/latency, send/fail rates, webhook outcomes, bounces by type) + docs enabling `POSTA_METRICS_ENABLED`.
- **Acceptance criteria:** dashboard imports clean against a running instance; panels map 1:1 to documented families.
- **Priority rationale:** P3 — one file, real operator polish.

**GAP-53 · Runtime log viewer & resource history** — `OBS-010`, `OBS-012` — P3
- **Description:** no stdout/app-log viewer; admin SSE `system.status` carries NumGoroutine+MemStats only — no CPU/disk/history.
- **Competitor reference:** OBS-010 LM◐ BM◐ (`02-competitor-comparison.md` §1.J).
- **Current status:** PARTIAL (both). **Evidence:** "no stdout/app-log viewer"; "SSE `system.status` with NumGoroutine+MemStats; no CPU/disk/history" (registry; audit-J).
- **Missing requirements:** in-product tail of recent app logs (ring buffer); historical resource stats.
- **Dependencies:** none.
- **Technical challenges:** ring buffer must be memory-capped and level-filtered; no secrets logged; deployments normally own log aggregation.
- **Recommended approach:** bounded in-memory ring buffer of slog records (level-filtered) exposed via admin SSE alongside `system.status`; optionally persist 24h samples.
- **Acceptance criteria:** admin can stream/tail filtered app logs; ring buffer bounded (memory-capped); resource history charted for 24h; no secrets logged.
- **Priority rationale:** P3 — convenience; deployments normally own log aggregation.

---

## 3. In-flight coverage note (uncommitted working tree at baseline)

Four working-tree streams cover parts of the register. Per suite convention (03 §"Commit cutoff", synthesis §6.2) these are **in progress, pending commit** — never counted as IMPLEMENTED; the gaps they touch keep their remaining requirements. A post-baseline spot-check on 2026-11-05 found all four **committed as `6d2d7c0`** ("Add provider event ingestion, A/B winner logic, timezone sending, and webhook replay", 36 new tests) — see §7 (limitations) and the re-verification note below the table.

| In-flight item | Working-tree files (as cited by synthesis §6.2) | Gaps covered | Status in this register | What remains after they land |
|---|---|---|---|---|
| `provider_events` ESP event ingestion | untracked `internal/services/provider_events/` + `provider_event_handler.go`; records `EmailEventDelivered` (`provider_events/service.go:213`) | GAP-02 (partial) | in progress, pending commit | Webhook-path suppression parity (`/webhooks/bounce` → suppression + `bounces` row + `email.complained`); per-ESP payload adapters normalized into the Posta schema; delivery-rate analytics distinguishing accept vs delivered; ARF/FBL parse (stretch) |
| A/B winner selection | `campaign.go:109-118` (`ABTestWinner`/`Sent`/`Stats`), winner hold in `campaign_processor.go` (`isHeldForWinner`), untracked `campaign_ab_repo.go` + `campaign_winner_test.go`, `CampaignDetail.vue:30,317-327` | GAP-28 (partial) | in progress, pending commit | Commit after test review; campaign-create variant UI; automation ab_test node outcome persistence |
| Timezone-aware sending | untracked `campaign_timezone_test.go` | GAP-39 | in progress, pending commit | Per-subscriber local-time batch split completion; deferred batches surviving restart; DST-edge tests; feature flag default-off |
| Webhook replay/redrive | untracked `webhook_replay_handler.go`, `webhook_replay_repo.go`, `dispatcher_replay_test.go` | GAP-18 (partial) | in progress, pending commit | Commit after review; user-facing test-fire endpoint (`POST /webhook-endpoints/{id}/test`) is still net-new |

**Re-verification note (2026-11-05 spot-check):** commit `6d2d7c0` lands all four streams and appears to go beyond the synthesis snapshot — per-ESP adapters with signature verification (SES SNS, SendGrid, Postmark, Mailgun fixtures), a bounce/failure classification service (14 classes) in the worker error path, replay through the SSRF-guarded transport with per-delivery rate limiting, CAS-gated exactly-once winner distribution, and DST-safe timezone batching. This register stays frozen at the synthesis baseline; the "what remains" cells above govern until each item is re-audited at `6d2d7c0` (audit I-J NV item 6 already requested exactly this re-audit). Nothing in §2 has been re-statused on the strength of the un-re-audited commit.

---

## 4. Duplicate & redundancy analysis (synthesis §2, D1–D9)

Nine duplications verified at synthesis (D1–D7 required + D8–D9 additional). Format: where (evidence) → why a problem → consolidate? → canonical implementation → migration risks → recommended resolution. Architecture costs are `06-architecture-impact.md`'s input.

**D1 — Dual audience model: `contacts`+`contact_lists` vs `subscribers`+`subscriber_lists`** (covers both the dual model and the orphaned contact-lists surface)
- **Where (evidence):** Contacts = per-workspace send-ledger, auto-populated by sends, read-only (`contact.go:24-34`); Subscribers = the real audience (CRUD, CustomFields, lists); ContactLists = "fully-built but completely unrouted legacy surface" — 7 handler methods, **zero route registrations**; dead link `ContactListMembers.vue:33` pushes nonexistent route `contact-lists`; the surface still round-trips in GDPR export/import (audit-C).
- **Why a problem:** three meanings of "contact" confuse users and developers; dead routes/link 404 in the UI; export/import carries dead data.
- **Consolidate?** Yes. **Canonical:** Subscribers as the CRUD audience; Contacts kept strictly as the read-only send ledger (genuinely useful as send history); ContactLists **deleted entirely** (model + handler + views + API client + export/import paths).
- **Migration risks:** GDPR export/import bundle compatibility (audit-C NV item 5 — removal has a data-migration cost); tenant data in `contact_list_members.Data` must be mapped or dropped with a release note.
- **Recommended resolution:** one release: stop exporting ContactLists, drop routes/models in a versioned upgrade step (the `migration/upgrade/` pattern exists), delete the Vue views, add a redirect for the dead link.

**D2 — Three audit-log surfaces (workspace `/audit-log`, deployment `/logs/audit`, admin `/admin/events` + SSE)**
- **Where (evidence):** all three read the same `events` table — duplication is by scope, not schema (audit-I); three UIs (`AuditLog.vue`, `AuditLogs.vue`, `Events.vue`), two SSE feeds.
- **Why a problem:** UI triplication, inconsistent filters, unclear which surface is authoritative for which question.
- **Consolidate?** Yes (UI; endpoints optional). **Canonical:** one audit UI with a scope switcher (workspace / deployment / platform), backed by the existing `events` table; keep `/admin/events` SSE as the live feed.
- **Migration risks:** permission differences (workspace audit is admin/owner-only; deployment scope requires an admin session; admin is a separate credential class) — the switcher must enforce all three gates; deep links change.
- **Recommended resolution:** single `AuditView` with scope tabs; keep backend routes during a deprecation window; CSV export stays on the deployment surface (only one with export).

**D3 — Two segmentation mechanisms: dynamic lists (`FilterRules`) vs segments (`SegmentCondition`)**
- **Where (evidence):** dual engine "coexists intentionally" (audit-C); legacy `applyFilterRules` kept "byte-identical"; campaign worker resolves both (`campaign_processor.go:116-152`); segments have the guarded SQL builder, 14 operators, injection tests, live preview UI.
- **Why a problem:** two expression languages for one concept; divergent capabilities (segments can't reference list membership; dynamic lists can't use the full engagement-subquery set); double maintenance in worker and preview paths.
- **Consolidate?** Yes, directionally. **Canonical:** segments (`SegmentCondition`) — better guarded, better tested, better UI.
- **Migration risks:** `FilterRules` semantics must map exactly (legacy-parity tests at `segment_query_test.go:660,697` — the translators are half-proven); campaign audiences referencing dynamic lists must keep resolving during transition; the per-list unsubscribe caveat differs by path (`campaign_processor.go:117-121`).
- **Recommended resolution:** add a FilterRules→SegmentCondition converter; present dynamic lists as segments on read; deprecate new dynamic-list creation; keep worker compatibility one release; drop legacy evaluation after parity is confirmed.

**D4 — Workspace analytics vs admin analytics endpoints**
- **Where (evidence):** same `AnalyticsRepository` with scoped WHERE clauses; UI split (`Analytics.vue` vs admin `Dashboard.vue`); "Deliberate ops/workspace split, not drift" (audit-E-F F.3).
- **Why a problem:** none functionally — the split mirrors the authorization domains (tenant self-service vs operator oversight, as with SEC-002 structure).
- **Consolidate?** **No.** **Canonical:** keep both; shared repo already guarantees identical metric definitions.
- **Migration risks:** none.
- **Recommended resolution:** document the split in the API docs; no action.

**D5 — Emails view vs Logs/Messages view**
- **Where (evidence):** same `emails` table, two read models: `web/src/views/emails/*` over `/emails*` (list/retry/status) vs `web/src/views/logs/*` over `/logs/messages*` (detail/content/events, privacy-redactable); "Bounces view is a third partial lens" (audit-E-F F.3).
- **Why a problem:** two navigational homes for "where is my message"; retry lives in one, timeline in the other; redaction only on one.
- **Consolidate?** Yes (UI). **Canonical:** the Logs/Messages surface (events, content, redaction) with retry/status actions merged in (thin API calls, `Emails.vue:198`).
- **Migration risks:** bookmarks/routes; the Emails view powers the dev-mode listing (`DevList`) which must stay somewhere (admin-only).
- **Recommended resolution:** merge retry + status into MessageDetail; keep the `/emails` API (it serves the SDK); retire the Emails views after a transition release; Bounces remains a filtered lens (cross-message by design).

**D6 — ChangePassword vs Profile password UI**
- **Where (evidence):** both `web/src/views/auth/ChangePassword.vue:28` and `web/src/views/auth/Profile.vue:142` call `authApi.changePassword(...)`; the router already redirects `{ path: 'change-password', redirect: '/profile' }` (`web/src/router/index.ts:112`), yet the ChangePassword view remains reachable/wired (repo-verified).
- **Why a problem:** two password-change flows to maintain and test; inconsistent post-change session behavior risk.
- **Consolidate?** Yes. **Canonical:** Profile.vue (the redirect already points there).
- **Migration risks:** none beyond removing a route; verify any nav links to `/change-password`.
- **Recommended resolution:** delete ChangePassword.vue + its route entry; keep the redirect; one E2E test on the Profile flow.

**D7 — `tracking_events` vs `email_events` dual tracking stores**
- **Where (evidence):** two dedup regimes (`tracking_repo.go:98-105` first-per-link vs `emailhistory` day-bucket), documented in code (audit-E-F F.3).
- **Why a problem:** conceptual dual surface; the same interaction can appear in both with different dedup semantics; retention handles both separately (`retention.go:401-437`).
- **Consolidate?** **No (yet).** **Canonical:** keep — `tracking_events` powers campaign time-series (campaign-scoped, raw); `email_events` is the message-history timeline (cross-scope, mirrored). They answer different questions.
- **Migration risks:** a merge would break campaign time-series semantics and retention windows.
- **Recommended resolution:** document the split in the API docs and retention matrix; do not merge in the roadmap window; revisit only if analytics consolidation (GAP-40) forces it.

**D8 — Suppression + Bounces UI entanglement**
- **Where (evidence):** "UI: Suppressions tab in the Bounces view (`Bounces.vue:14,38-45`)" (audit-C); suppression is its own resource with CRUD API (`suppression_handler.go`).
- **Why a problem:** suppression — a first-class consent artifact — is discoverable only inside a bounce-centric view.
- **Consolidate?** UI promotion only; no data change. **Canonical:** Suppressions as its own nav entry (or a consent section, pairing with GAP-35's preference center).
- **Migration risks:** none (navigation only).
- **Recommended resolution:** promote Suppressions to its own nav entry; keep the Bounces tab.

**D9 — Admin SSE feed duplication**
- **Where (evidence):** separate `/admin/events/stream` (`admin_routes.go:726`), `/admin/security/events` SSE (`:909`), plus `/admin/metrics/stream` (`:735`) (audit-I).
- **Why a problem:** three admin SSE channels with overlapping connection management (not data duplication per se).
- **Consolidate?** Acceptable as-is. **Canonical:** if touched, multiplex one admin SSE connection with typed channels.
- **Migration risks:** connection-handling changes can silently drop live admin feeds.
- **Recommended resolution:** low priority, note only.

---

## 5. NEEDS_VERIFICATION register

Every verification item open at synthesis, plus the registry's NV entries. Status: **Open** (needs evidence), **→ GAP-nn** (folded into a gap's requirements/acceptance), **Resolved** (closed at synthesis or by a later audit). Evidence column states what would resolve each.

| Ref | Item | Source | Status | Evidence that would resolve it |
|---|---|---|---|---|
| TPL-016 | Preview text (preheader) — subject templating exists, preheader unverified | registry | → GAP-36 (verify-first) | Render a template with a preheader field and assert hidden preview text in the rendered HTML; or grep the render pipeline for preview-text handling |
| TPL-022 | Media/image uploads for templates — blob store exists for attachments; media-library surface not audited | registry | → GAP-36 (verify-first) | Audit template-editor image pickers (`EmailBuilder.vue`) for blob-backed upload/list/delete endpoints |
| SEC-016 | Credential encryption at rest | registry | **Resolved** (synthesis §6.4) | Done: AES-256-GCM via `internal/services/crypto` verified 2026-11-05; caveat — base64 fallback when `POSTA_ENCRYPTION_KEY` unset (production docs must require the key) |
| A-B NV-1 | Scheduled-email durability (scheduled rows outside reconcile) | audit-A-B | → GAP-01 | The GAP-01 acceptance tests (cancel + scheduled-row reconcile) are themselves the resolving evidence |
| A-B NV-2 | Admin queue action scoping (`/admin/queues/*` reject non-admin keys?) | audit-A-B | Open | A scope test in the `workspace_rbac_test.go` style asserting non-admin API keys get 403 on `/admin/queues/*` |
| A-B NV-3 | DKIM signing on the shared-server path (operator intent for platform domain) | audit-A-B | Open | Maintainer decision + test pinning whether a `POSTA_SYSTEM_SMTP_*` platform domain gets a managed key |
| A-B NV-4 | Jobs.vue / Domains.vue wiring (WIP views) | audit-A-B | **Resolved** (synthesis §6.7) | Later audits confirmed views are wired (`web/src/api/admin.ts:120-136`) |
| A-B NV-5 | `email.complained` lifecycle (no other FBL ingestion) | audit-A-B | → GAP-02 | Grep already found none; the GAP-02 FBL/ARF stretch requirement closes it either way |
| A-B NV-6 | Queue weight semantics under load (bulk starving transactional) | audit-A-B | Open (risk R11) | A benchmark/load test under sustained campaign load; until then document weighted (probabilistic) semantics; INF-011 stays not-proposed |
| C-D NV-1 | Scheduler under concurrent workers (claim/rollback vs real Redis + 2 replicas) | audit-C-D | → GAP-03 | The GAP-03 two-manager integration test covers the concurrent-scheduler question |
| C-D NV-2 | Gradual-send pacing accuracy under bulk-queue concurrency | audit-C-D | Open | Throughput measurement of `delay = batch/rate` (`campaign_processor.go:419-426`) with worker concurrency w=3 |
| C-D NV-3 | `scheduled_at` update semantics post-scheduling (PUT rejection for `scheduled`?) | audit-C-D | Open | Trace `PUT /campaigns/{id}` (`campaign_handler.go:107`) for a `scheduled` campaign; add a rejection test |
| C-D NV-4 | CSV import memory profile at 100k+ rows (`BulkImportCSV` builds the whole slice) | audit-C-D | Open | A 100k-row import benchmark; note GAP-27's export path streams — import streaming can follow |
| C-D NV-5 | Contact-lists deletion intent | audit-C-D | → D1 | The D1 resolution (delete ContactLists in a versioned upgrade) is the decision record |
| C-D NV-6 | TaskID dedup window surviving `emails` row deletion/retention | audit-C-D | Open | A test that a replayed batch after `emails` row deletion does not re-send (dedup store vs retention interplay) |
| E-F NV-1 | Campaign open pixel vs toggle intent (deliberate else-branch) | audit-E-F | → GAP-15 | The GAP-15 product-intent decision (align doc or code) resolves it |
| E-F NV-2 | Delete-active-version guard (`ActiveVersionID` dangling after version delete?) | audit-E-F | Open | Trace `TemplateVersionHandler.Delete` (`template_version_handler.go:194+`); test that delete blocks or clears the active pointer |
| E-F NV-3 | `emails_queue_size` gauge update call-site frequency | audit-E-F | Open | Verify the update site of the metric defined at `metrics.go:68-74`; pin with a metrics test |
| E-F NV-4 | Seed R-21 list segment preview fix shape (pageable vs `{count}`) | audit-E-F | Open | Check the preview endpoint response shape against `347ebb4`'s claim |
| E-F NV-5 | Uncommitted migration edits (`constraints.go`, `migration.go`) purpose unknown | audit-E-F | Open | Read the working-tree diff of `internal/storage/migration/`; note line numbers in those files may shift (synthesis §6.6) |
| G-H NV-1 | Python (`posta`) and .NET (`Posta`) SDK registries unconfirmed | audit-G-H | → GAP-23 | PyPI/NuGet install check (or publishing) |
| G-H NV-2 | `/automations/events` authorization breadth (viewer sessions intended?) | audit-G-H | → GAP-11 | The GAP-11 editor gate + RBAC regression test is the resolution |
| G-H NV-3 | SDK repos track this API version (340 endpoints)? | audit-G-H | → GAP-23 | CI/docs coverage check of SDK repos vs current route set |
| G-H NV-4 | "API keys can never reach `/admin/*`" runtime-tested (structurally verified only) | audit-G-H | Open | A runtime test asserting a `psk_` key gets 401/403 on an `/admin/*` route |
| G-H NV-5 | `/webhooks/bounce` behavior under ESP-specific payloads | audit-G-H | → GAP-02 | Real-world SES/Postmark/Mailgun/SendGrid payload fixtures (the GAP-02 adapter tests) |
| I-J NV-1 | `user.login` events retention quality (compliance expectation?) | audit-I-J | Open | A compliance-user requirement check; confirm the 90-day `audit_log_retention_days` default is acceptable or add a window |
| I-J NV-2 | Cron behavior with >1 server replica (runtime) | audit-I-J | → GAP-03 | The GAP-03 integration test is the resolving evidence |
| I-J NV-3 | Worker `/readyz` semantics under graceful shutdown | audit-I-J | Open | An integration run observing `SetProcessing` flip vs drain completion (`worker.go:286-288`) |
| I-J NV-4 | Workspace SSO enforcement depth (login-time rejection end-to-end) | audit-I-J | Open | Trace `EnforceSSO` through `oauth_handler.go` login/discover to rejection; add an E2E test |
| I-J NV-5 | Security-engine fail-open scope (all HTTP protections vs signal recording only) | audit-I-J | Open (pairs with R10/GAP-49) | A fault-injection test: Redis outage with the ban engine, observing which protections persist |
| I-J NV-6 | `provider_events` / webhook replay re-audit after commit | audit-I-J | **Landed, re-audit pending** | Spot-check found commit `6d2d7c0` (§3); re-run the route→handler→test sweep at the new HEAD |

---

## 6. Explicitly NOT proposed

Nothing in this section is a roadmap candidate; each entry documents why it is excluded so later docs do not resurrect it. Sources: synthesis §1.0 (15 registry entries), registry §"verified ABSENT" list, synthesis §5 not-recommended list.

### 6.1 Deliberately not proposed (15 registry entries)

| ID | Feature | One-line reason |
|---|---|---|
| INF-011 | Strict priority send queues | No competitor ships strict priority (LM✗ PO✗ explicit); weighted fair-share is adequate — document semantics instead of building a scheduler. |
| INF-020 | Bounce mailbox / VERP ingestion | Relay-only architecture has no return-path ownership (audit-G); revisit only if direct-to-MX is ever decided. |
| INF-025 | IP pools / multiple sending IPs | Structurally impossible under relay-only egress (no IP concept in code, audit-A); blocked on an MTA decision, not a coding gap. |
| INF-036 | Multi-channel messengers (SMS/push) | RF is an email product; LM-style messenger adapters are scope creep with zero RF evidence. |
| DOM-007 | Automated DNS provisioning | HR does it by delegating NS to a built-in DNS server — a different product shape; copy-paste `RequiredRecords` is acceptable for self-host operators. |
| DOM-010 | Per-domain TLS (ACME/SNI) | Sending TLS is the upstream relay's cert; inbound multi-domain TLS only matters if inbound is promoted — deferred. |
| DOM-013 | rDNS/PTR guidance | Meaningless without owning egress IPs (audit-B: zero code, correctly absent). |
| CON-020 | Import cleaning heuristics (spam-trap/misspell) | `verifier.go` syntax/disposable/role/MX + import dedupe cover the core; MC-scale cleaning is marginal at self-host volumes. |
| CAM-003 | Recurring campaigns | Verified absent in all 12 projects (registry §absent #2); defer until user demand — scheduler cron could host it later. |
| TPL-006 | Liquid/Handlebars-class engine | Go templates + Mustache normalization (`template_renderer.go:85-149`) already cover loops/conditionals; a full filter pipeline is a large port with low differentiation. |
| TPL-007 | MJML | Needs an MJML compiler dependency (NU maintains its own gomjml fork); heavy, marginal for a Go/Vue product. |
| TPL-008 | React Email | Vue product; feature exists only in US/WR TSX ecosystems. |
| TPL-020 | AI-assisted creation | Self-hosted no-cloud stance (audit-E); zero LLM infra in repo — a deliberate product decision, not a gap. |
| TPL-021 | Spintax variation | Cold-email niche (BM-only); variation features cut against deliverability/consent posture. |
| API-018 | Zapier/platform connectors | Requires marketplace presence and hosting ecosystem RF does not have; community paths exist for competitors (KE◐). |

### 6.2 Verified absent across all 12 competitors — do not invent as gaps

Registry §absent entries not already covered by §6.1 (manual webhook replay is in-flight/GAP-18, not an exclusion; strict priority and recurring campaigns are §6.1): DMARC rua ingestion, per-subscriber local-time optimization as a shipped feature (RF stub → GAP-39 finishes the in-flight work only), retention legal-hold, ML send-time optimization, SAML SSO, SCIM, SPF flattening tooling, MTA-STS/DANE — each checked and found in no dossier; building any would be differentiation without peer-validated demand. (SEC-016 is resolved, not excluded — §5.)

### 6.3 Not-recommended strategies (synthesis §5)

| Strategy | Reason |
|---|---|
| Building an MTA / direct-to-MX delivery (would unlock INF-025/DOM-013/INF-020) | Owning egress is a different product (IP reputation ops, PTR contracts, feedback loops); the relay-only design is a defensible identity, and API-mode ESPs (GAP-04) deliver most of the value without it. |
| Full mail server / IMAP / JMAP / webmail (INF-003, N/A) | BillionMail-only; out of RF scope permanently unless direction changes. |
| License/strategy-gated copies (NU's gomjml fork; NU feature mimicry; Listmonk code) | License risk: NU moved to BSL at v40.0 — treat NU code as license-risk; Listmonk is AGPL-3.0 — study behavior, never copy code. |
| Strict priority scheduler, SAML/SCIM, DMARC rua ingestion, ML send-time, recurring campaigns (CAM-003), SPF flattening | Verified absent across all 12 competitors; build only on demonstrated demand. |
| Liquid engine port, MJML, React Email, spintax, AI-assisted creation, Zapier connectors (TPL-006/007/008/021/020, API-018) | Reasons in §6.1. |
| Web2-style adjacent products (website analytics ANA-016, embeddable core package API-019) | Product fit: out of email-platform scope; both N/A in the registry. |

---

## 7. Research limitations

- **Date:** all statuses, evidence, and repo verifications are as of 2026-11-05.
- **Baseline drift (material):** the repo is under active parallel development. This register is frozen at the synthesis baseline (HEAD `a6f1681` + in-flight WIP), but a post-baseline spot-check found the four in-flight streams committed as `6d2d7c0` (provider-event ingestion with per-ESP adapters, A/B winner, timezone sending, webhook replay; §3). The register's "in progress, pending commit" statuses and remaining requirements stand until a re-audit at `6d2d7c0` lands; nothing here has been re-statused on the un-re-audited commit. Audit evidence was captured at mixed baselines (audit-A/B and C/D at `347ebb4`+WIP; E/F, G/H, I/J at `a6f1681`), and the working tree since added ~800 lines to campaign files — cite audit-file pointers, not working-tree greps, for HEAD claims (synthesis §6.6).
- **Registry freeze:** the 108/46/38/3/4 status split is the frozen v1.0.0 reference; SEC-016's synthesis-time resolution to IMPLEMENTED makes the live count 109 implicit. This document consumes the registry as-is.
- **NEEDS_VERIFICATION carryover:** 30 items remain in the §5 register (27 Open / folded into gaps, 2 Resolved, 1 landed-pending-re-audit). Folded items resolve automatically when their gap's acceptance criteria are met; Open items need the evidence listed in the table.
- **Scoring and sequencing out of scope:** numeric prioritization, phase sequencing, and effort sizing belong to doc 05; where this register's draft priority and doc 05's numeric rank disagree, the priority class wins and the discrepancy is noted there (synthesis §4).
- **Unverified competitor claims:** competitor flags are transcribed from `02-competitor-comparison.md` without re-research; NV items there carry through unchanged.

---

*End of doc 04. Every claim traces to `registry.md`, `audit-A/B/C/D/E/F/G/H/I-J.md`, `synthesis.md`, or a repo spot-check at the stated baseline; GAP-nn / D-nn identifiers are verbatim from synthesis §1–§2 and are consumed unchanged by doc 05 and `06-architecture-impact.md`.*
