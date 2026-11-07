# V-KL — Verification of Category K and L records (SF-101..SF-110)

Auditor: V-KL · Date: 2026-11-07 · Method: code reading only (no local Go toolchain —
no builds/tests/docker per the audit brief). Every claim below was verified against
this checkout by reading the cited files; greps are quoted as run. Docs were treated
as claims, not proof. Test functions are named `file:line`; no test was executed —
"covered" means the test exists in the tree and names the behavior, not that it
passes in this session. Dependency-internal behavior (okapi, asynq) is flagged where
it cannot be verified from this tree because the modules are not vendored.

---

### SF-101 — Sieve system + user scripts at every SMTP stage and delivery

**Final status: PLANNED (ADR-006 accepted · OB-2) — confirmed; zero Sieve code in the tree; the documented alternative (automation engine) is real and wired**

Fields: Backend Missing (Sieve) / Verified (automation alternative) · Frontend Verified (automation builder views exist, not opened) · DB Verified (automation tables via models) · API Verified (automation routes) · Security — · Tests Covered · Docs Partial

**Implementation:** none for Sieve — verified absent. Alternative: `internal/services/automation/` (engine.go, nodes.go, triggers.go, domain.go, graph.go).

**Evidence:**
- Zero Sieve code: `grep -rli "sieve" . --include="*.go" --include="*.vue" --include="*.ts" --include="*.js"` → **0 files** (case-insensitive, tests included). A second run excluding nothing in `web/` also returned nothing. The only "Sieve" occurrences in the repo are prose in `docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md` and ADR references.
- Alternative verified — the automation engine exists and is wired end to end:
  - Node types — 9 total, `internal/services/automation/domain.go:47-55` (constants aliased to `internal/models`) and `validNodeTypes` map `domain.go:111-121`: `trigger`, `delay`, `email`, `filter`, `ab_test`, `list_status`, `add_to_list`, `remove_from_list`, `webhook`. Dispatch in `internal/services/automation/nodes.go:77-99` (`executeNode` switch) with per-type executors (`executeDelayNode` :106, `executeEmailNode` :143, `executeFilterNode` :200, `executeABTestNode` :221 — FNV-1a deterministic variant roll, `executeListStatusNode` :257, `executeAddToListNode` :280, `executeRemoveFromListNode` :300, `executeWebhookNode` :320 with SSRF-validated URL and capped response capture).
  - Trigger kinds — 8 event kinds, `internal/services/automation/domain.go:79-88` aliased to `internal/models/automation.go:79-86`: `list.subscribed`, `contact.created`, `contact.unsubscribed`, `email.opened`, `email.clicked`, `email.bounced`, `custom`, and **`schedule` (the R-13 cron trigger)**. `email.sent`/`email.delivered` are deliberately not trigger kinds (loop guard, comment `domain.go:76-78`).
  - R-13 schedule trigger: validation requires a parsable cron expression plus an audience (list or segment) at `domain.go:574-584`; firing at `internal/services/automation/engine.go:604-649` (`ProcessSchedules` :608, `cron.ParseStandard` :624, per-automation persisted checkpoint `stats.LastFiredAt` :630-649, `maxScheduleCatchup = 1440` :582 bounding backlog chase).
  - Enrollment: `internal/services/automation/triggers.go:73-147` (`Enroll` recovers panics and never propagates errors into the triggering action; once-frequency dedup via `triggerLogRepo.Insert` :136-144); worker-side bounce hook `RecordBouncedTrigger` :278-288.
  - Wiring: asynq handler `mux.HandleFunc(worker.TypeAutomationRun, …)` at `cmd/posta/worker.go:255` and `automationEngine.Start` :260 (also started in the embedded server worker); REST routes registered at `internal/routes/automation_routes.go:34-47` (`/workspaces/current/automations`).
- Tests (named, not run): `internal/services/automation/engine_test.go`, `nodes_test.go`, `triggers_test.go`, `graph_test.go`, `domain_test.go`, `engine_schedule_test.go:86-243` (8 schedule-trigger tests), `engine_segment_test.go:42-179`, `internal/worker/message_process_test.go` adjacency. Workflow validation covered by `domain.go:388-552` (`Validate` with BFS orphans and cycle detection).

**Notes:** Pre-seed status stands: Sieve itself is do-not-implement under ADR-006/OB-2, and the engine is a genuine per-contact workflow interpreter, not a Sieve interpreter — no user-supplied script surface at SMTP stages exists or is claimed. The audit's "9 node types" matches the code exactly. Cannot verify: nothing material; the builder UI files were located (`web/src/views/automations/`) but not read in depth — the API/wiring evidence above does not depend on them.

---

### SF-102 — Sieve plugin functions [ENT]

**Final status: PLANNED (ADR-006 accepted · OB-6) — confirmed; zero Sieve plugin code; narrow workflow-node analogs exist**

Fields: Backend Missing · Frontend — · DB — · API — · Security — · Tests — · Docs Partial

**Implementation:** none — verified absent.

**Evidence:**
- Same greps as SF-101: `grep -rli "sieve" …` → 0 files; no plugin registry, no external-function table, no `exec`/`dns`/`http-fetch`/`llm_prompt` function surface.
- Narrow analogs that exist inside the automation engine (not Sieve functions): webhook HTTP POST with static SSRF policy at save time and runtime re-check (`internal/services/automation/nodes.go:320-342`; `webhook.ValidateWebhookURL` invoked at `internal/services/automation/domain.go:515-517`), and `{{ var }}` placeholder rendering for webhook bodies (`nodes.go:368-383`). No KV-lookup, DNS, external-program, or LLM plugin equivalent.

**Notes:** Pre-seed status stands (OB-6 enterprise tier). Nothing here can be mistaken for partial implementation — the analogs are automation nodes, not a plugin mechanism.

---

### SF-103 — Expression language reused everywhere

**Final status: PARTIAL — PLANNED (Phase 3) (evaluator unification, pairs SF-50) — confirmed; three condition currencies verified, each complete in itself; no unified evaluator**

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified (parameterized SQL) · Tests Covered · Docs Partial

**Implementation:**
1. **FilterRule currency** — model `internal/models/subscriber_list.go:35-39` (`FilterRule{Field,Operator,Value}`); two evaluators over it: in-memory `EvaluateConditions` at `internal/services/automation/graph.go:295` (used by triggers and filter nodes) and the SQL translator `applyFilterRules` at `internal/storage/repositories/subscriber_repo.go:145` (dynamic subscriber lists; engagement subqueries, `engagement.*` fields :149-175).
2. **SegmentCondition currency** — model `internal/models/segment.go:53`; guarded SQL builder `internal/storage/repositories/segment_query.go`: `BuildSegmentConditionSQL` :408-438, `ValidateSegmentCondition` :237, whitelist + operator tables :57-71. Doc block :46-55 states the invariant: placeholders are `?` bindvars, values returned separately, only `[A-Za-z0-9_]` custom-field keys ever interpolated. Consumers: `internal/storage/repositories/segment_repo.go:76,92` and the automation engine's `in_segment` resolution `internal/services/automation/engine.go:556`.
3. **Automation condition currency** — `evaluateConditionSet` at `internal/services/automation/engine.go:515-531`: AND-combines `FilterRule`s, hoists `in_segment` rules (R-14, field const `domain.go:93`) to a live segment checker (`segmentMemberChecker` :535-539, `subscriberInSegment` :545-573 — scope-checked via `repositories.OwnsWorkspaceResource` :550), delegates the rest to `EvaluateConditions`. Save-time validation mirrors operator support (`filterRuleProblem`, `domain.go:651-688`).
- **Unification absence verified:** `grep -rn "package expr" internal/` → 0 hits; the three currencies share no evaluator, no IR, and no operator table. No regex-capture/SIMD-style expression kernel exists anywhere in `internal/`.

**Evidence (tests, not run):** `internal/services/automation/engine_segment_test.go:42` `TestEvaluateConditionSetInSegment`, :115 `TestSubscriberInSegmentLiveEvaluation`, :141 `TestSubscriberInSegmentClosesOnUnusableSegment`, :179 `TestSubscriberInSegmentScopeFiltersSubscriber`; `graph_test.go`, `domain_test.go` cover `EvaluateConditions`/`Validate`; segment builder covered by `internal/storage/repositories/segment_query`-related tests in `segment_repo`/`engine_segment_test.go`.

**Notes:** Pre-seed status stands. The "individually COMPLETE; single unified evaluator absent" claim is exactly what the code shows. The three currencies do interoperate at one point — `in_segment` lets the automation currency call the SegmentCondition SQL — which is composition, not unification.

---

### SF-104 — Scheduled task system (ACME, DKIM rotation, reports, purges)

**Final status: PARTIAL — PLANNED (Phase 3) (DKIM rotation task; report tasks with SF-25 send) — confirmed; cron framework + 11 jobs + campaign scheduler + automation schedule triggers verified; DKIM rotation and scheduled report generation absent**

Fields: Backend Partial · Frontend Partial · DB Verified · API Partial · Security — · Tests Covered · Docs Partial

**Implementation:** cron framework `internal/cron/cronjob.go` (interface `Name/Schedule/Run` :23-31) + `internal/cron/manager.go` (robfig cron in UTC :63, `AddFunc` registration :86-103, `Stop` :112, `Jobs() []JobStatus` :124). All jobs registered in `cmd/posta/server.go` `initCronManager` (:703-798).

**Evidence — full enumeration of `internal/cron/jobs/` (11 job files, name @ schedule, all verified in the files):**
| File | Job | Schedule | Purpose (from the file itself) |
|---|---|---|---|
| account_cleanup.go:40-41 | account-cleanup | `0 2 * * *` | daily user-account cleanup |
| api_key_expiry.go:45-46 | api-key-expiry | `0 8 * * *` | notify/expire expiring API keys |
| backup.go:137-138 | backup | configurable (`j.schedule`) | pg_dump backups + dump retention (R-11), opt-in `cfg.BackupEnabled` (server.go:780-786) |
| bounce_alert.go:60-61 | bounce-alert | `0 9 * * *` | daily bounce-rate alerts via notifier |
| campaign_restart.go:53-54 | campaign-restart | `*/5 * * * *` | re-enqueue stuck campaigns (`stuckFor` 10 min, :56) |
| campaign_scheduler.go:75-76 | campaign-scheduler | `* * * * *` | fires scheduled campaigns (R-01): atomic claim `scheduled → sending` via `TransitionStatus`, enqueue roll-back on failure (:52-77 doc) |
| daily_report.go:58-59 | daily-report | `0 7 * * *` | enqueues per-workspace usage report tasks to owners/admins (:37-40) |
| message_digest.go:65-66 | message-digest | `5 * * * *` | hourly form-message digest (opt-in `cfg.MessagesEnabled`, server.go:791-795) |
| queue_reconcile.go:82-83 | queue-reconcile | `*/5 * * * *` | lost-task reconcile (R-03): re-enqueues queued emails whose asynq task vanished |
| retention.go:195-196 | retention-cleanup | `0 3 * * *` | content/event retention sweeps across repos (GAP-13 caller) |
| update_check.go:40-44 | update-check | `37 4 * * *` | daily release check (opt-in `updateSvc.Enabled()`, server.go:734) |

Conditional registration verified at `cmd/posta/server.go`: unconditional retention/daily-report/account-cleanup/api-key-expiry/bounce-alert (:718, :746-752); `producer != nil` gates campaign-restart/campaign-scheduler/queue-reconcile (:755, :763, :769); backup :780; message-digest :791.
- **R-01** campaign scheduler: `campaign_scheduler.go` as above; tests `campaign_scheduler_test.go:497` `TestSchedulerClaimsOnlyDueScheduledCampaigns`, :528 `TestSchedulerClaimConflictSkipsWithoutEnqueue`, :555 `TestSchedulerEnqueueFailureRollsBackToScheduled`, :590 `TestSchedulerIgnoresPausedAndCancelledCampaigns`.
- **R-13** automation schedule triggers: `ProcessSchedules` called from the engine loop every tick (`internal/services/automation/engine.go:176`); tests `engine_schedule_test.go:86,109,132,148,165,184,206,243`.
- **Reconciliation** analogs: queue-reconcile (R-03, test `queue_reconcile_test.go:465` `TestQueueReconcileReenqueuesLostTask`), engine `Sweep` (engine.go:178-184), schedule checkpoints (engine.go:630-649).
- **Absent, verified:** DKIM rotation — `grep -rin "rotat" internal/cron/ internal/services/` shows only prose guidance (`internal/services/domain/health.go:215`) and backup-dump rotation; no scheduled key-rotation task. Scheduled report generation — no DMARC aggregate report *sending* job; DMARC exists only on the inbound parse path (`parseSvc.EnableDMARC(db)`, `cmd/posta/worker.go:219`; R-17). The daily-report job is a per-workspace usage digest, not a the reference platform-style system report. ACME: no task and no ACME client at all (`grep -rin "acme" go.mod internal/ cmd/` → only "Acme" in address-format doc examples).
- **Correction to the audit row:** it says "cron framework (8 jobs)"; the tree defines **11** job files. The remaining claims in the row (R-01, R-13, R-11, reconcile, absent rotation/report tasks) all verified.

**Notes:** Pre-seed status stands. Frontend "Partial" is consistent with `web/src/views/admin/Jobs.vue` existing and `Manager.Jobs()`/`JobStatus` (`internal/cron/manager.go:124-135`) exposing run status; I located both but did not trace the endpoint route — that trace remains open.

---

### SF-105 — Cluster broadcast fabric (Redis/NATS/Zenoh/Kafka)

**Final status: PLANNED (ADR-009 accepted · OB-6) — confirmed; single-fabric equivalent (Redis pub/sub ban propagation) verified; no multi-protocol cluster fabric**

Fields: Backend Partial · Frontend — · DB — · API — · Security Verified · Tests Covered · Docs Partial

**Implementation:** `internal/services/security/propagate.go` — Redis pub/sub channel `posta:sec:bans` (:40); `Publish` :94-106 (`p.rdb.Publish` :106); subscribe loop :153-172 (`p.rdb.Subscribe` :154) with 5 s reconnect backoff and a 5-minute full-resync reconcile ticker (:124-129) so pub/sub gaps self-heal; message application `apply` :182-218 (`ban` upsert :190-196, `unban` remove :197-210, `allow`/`disallow`/`reload` → full `ResyncFromDB` :211-214); source-of-truth rebuild `ResyncFromDB` :223-244. Publish call site: `internal/services/security/bansvc.go:543`.

**Evidence:**
- Second (independent) Redis pub/sub usage: `internal/services/realtime/redis.go:58` (`Publish` on workspace topics) and :84 (`Subscribe`) — a per-workspace realtime fan-out, not a cluster coordination fabric.
- No alternative fabric: `grep -in "nats|kafka|zenoh" go.mod go.sum` → 0 hits; the only broker is Redis via asynq/go-redis. No registry-change broadcast, no cross-node cache invalidation, no queue-status/rescan broadcast exists (settings changes propagate only through the 30 s TTL — see SF-107/110).

**Notes:** Pre-seed status stands: what exists is exactly the audit's "single-fabric equivalent" — ban/allowlist propagation between replicas of one deployment, with reconnect resync. Cluster-wide invalidation (settings, caches) is the ADR-009/OB-6 gap. Tests: `bansvc_test.go` (:158-475) and `snapshot_test.go` cover the local apply side; the pub/sub loop itself has no integration test in-tree.

---

### SF-106 — Store-level queue failover with distributed event locks

**Final status: PARTIAL — failover PLANNED (ADR-009 accepted · OB-6) — confirmed; single-node claim substrate (SKIP LOCKED + CAS + asynq dedup) verified with exactly-once tests; multi-node store failover absent**

Fields: Backend Partial · Frontend — · DB Verified · API — · Security — · Tests Covered (exactly-once) · Docs Partial

**Implementation:**
- `FOR UPDATE SKIP LOCKED` batch claim: `internal/storage/repositories/campaign_message_repo.go:80-88` — `FindPendingByCampaign` runs `tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})` inside a short transaction over `campaign_id AND status = pending` (lock clause at :83; R-26 rationale :72-79).
- Per-message CAS: `ClaimForSend` :103-110 (single `UPDATE … WHERE id = ? AND status = pending`, `RowsAffected > 0` wins).
- Email-row CAS claims: `internal/storage/repositories/email_repo.go:57-64` `ClaimForProcessing` (queued/processing → processing), :70-80 `ClaimForRetry` (failed → queued + `retry_count + 1` in one UPDATE), :84-88 `ClaimFailedForProcessing` (failed → processing).
- asynq task dedup: `internal/worker/producer.go:47-50` deterministic `EmailSendTaskID`, :61/:67/:101 enqueue with `asynq.TaskID(...)` and `ErrTaskIDConflict` tolerance — a re-enqueue of the same email collapses onto one task.
- Automation journeys claim under the same pattern: `ClaimDueBatchAt` + CAS cursor writes (`internal/services/automation/engine.go:157-159` doc; `ProcessBatch` :187-205).

**Evidence (tests, not run):** `internal/worker/atomic_claim_test.go:513` `TestClaimForProcessingAllowsExactlyOneClaim`, :550 `TestClaimForRetryClaimsOnceAndIncrementsOnce`, :587 `TestClaimForSendAllowsExactlyOneClaim`, :609 `TestFindPendingByCampaignUsesSkipLockedBatchClaim`, :654 `TestEmailSendHandlerDuplicateDeliverySendsOnce`.

**Multi-node failover absence, verified:** `grep -rn "failover|lease|coordinator" internal/` → only unrelated comments; there is no node-registry, no distributed event lock beyond row locks, no store-level failover or queue replication. If the process dies mid-batch, recovery is re-claim via CAS + asynq redelivery (email_repo.go:50-56 doc), not coordinated handover.

**Notes:** Pre-seed status stands. The audit's evidence citation `campaign_message_repo.go:83` for the lock clause checks out exactly.

---

### SF-107 — Cache layers (TTL, negative caching, invalidation, snapshots)

**Final status: COMPLETE — cluster invalidation across nodes BLOCKED BY ADR-009 — confirmed for the single-node scope the record claims**

Fields: Backend Verified · Frontend — · DB — · API — · Security Verified · Tests Covered · Docs Partial

**Implementation:**
- **Settings TTL cache (the provider):** `internal/services/settings/provider.go` — `cacheTTL: 30 * time.Second` (:47), whole-map refresh (:54-66), `ensureFresh` staleness check (:68-75). Consumers incl. security engine, campaign processor, cron jobs (constructed at `cmd/posta/server.go:706`, `cmd/posta/worker.go:153`).
- **Security COW snapshot:** `internal/services/security/snapshot.go:38-57` — `BanList` holds `atomic.Pointer[banSnapshot]` (:57); readers `Blocked`/`Allowed` are lock-free snapshot loads (:328-357, :363-384); writers clone-then-store under a mutex: `clone` :111-124, `Replace` :277-290, `UpsertBan` :296-303, `RemoveBan` :308-321 (with slot re-claim to the longest-blocking ban :230-254).
- **Invalidation:** Redis pub/sub actions trigger full resync (`propagate.go:211-214`) + 5-minute reconcile ticker (:126-129) + resync after every reconnect (:145-147). Local (in-process) only — no cross-node targeted invalidation, which is the recorded ADR-009 caveat.
- **Other lookup/verdict caches (verified):** Redis analytics/dashboard cache with fixed TTLs `internal/services/cache/cache.go:22-35`; email verifier Redis caches with per-result TTLs and **negative caching** — MX miss cached under the sentinel `"none"` (`internal/services/verifier/verifier.go:232-251`), intrinsic results cached (:298-321); public form lookups with miss TTL `internal/services/messages/formcache.go:31-48`. No large-object swap cache with persisted snapshots (the reference platform's specific mechanism) — absent, and nothing needs it in this architecture.

**Evidence (tests, not run):** `internal/services/security/snapshot_test.go:67-333` (9 BanList tests incl. canonicalization and most-specific-CIDR), `bansvc_test.go:158-475`, `internal/services/settings/provider_test.go:33-60` (TTL-provider behavior incl. invalid-value fallback), `internal/services/security/rules_test.go:53-169`.

**Notes:** The ledger's FULL claim is supported for what the record actually claims (settings TTL + security COW + pub/sub invalidation + lookup caches). "COMPLETE" is fair with the recorded ADR-009 caveat: cross-node invalidation is broadcast resync (full rebuild) rather than targeted eviction, and only for bans/allowlists.

---

### SF-108 — Backpressure (queue caps, memory budgets, transfer limits, throttles)

**Final status: PARTIAL — memory budgets PLANNED (Phase 3); MTA-scale throttles BLOCKED BY ADR-001 (OB-1) — confirmed**

Fields: Backend Partial · Frontend — · DB — · API — · Security — · Tests Covered · Docs Partial

**Implementation:**
- **Bounded retry sweep (R-27):** `internal/services/retry/worker.go` — `DefaultBatchLimit = 200` (:59, per-server per-tick cap on re-enqueued retries), `batchLimit` field :47-50, `SetBatchLimit` :83-87 (0 = unbounded, negative ignored), pre-sweep **jitter** `interval/10` (:74-76) applied before every tick (:143-149) to de-synchronize concurrent processes.
- **asynq concurrency:** `cmd/posta/worker.go:136-147` and the embedded copy `cmd/posta/server.go:588-596` — `Concurrency: cfg.WorkerConcurrency` with weighted queues (transactional 6 / bulk 3 / low 1, worker.go:140-144). Default 10: `internal/config/config.go:373` (`POSTA_WORKER_CONCURRENCY`); mis-config guarded at `internal/config/security.go:188-191`.
- **Rate limits (plan quotas):** `internal/services/email/service.go:67-68` `HourlyRateLimit`/`DailyRateLimit`, limiter :96, enforcement :670-673 (`AllowWithLimits` via `ratelimit.RedisLimiter`) — per-user send quotas, not MTA transport throttles.
- **Absence, verified:** `grep -rin "memory budget|MemoryBudget|SetMemoryLimit|in-flight cap|virtual queue" internal/` → 0 hits — no session memory budgets, no virtual-queue in-flight caps. Inbound has message/attachment size caps (`MaxMessageSize`/`MaxAttachmentSize` into `inbound.Config`, `cmd/posta/worker.go:208-211`) — size limits, not transfer throttles. No per-session bandwidth throttling exists.

**Evidence (tests, not run):** `internal/worker/retry_bound_test.go:478` `TestRetrySweepBoundedBatch`, :526 `TestRetrySweepZeroLimitUnbounded`, :552 `TestRetrySweepExhaustedRetriesSkipped`, :579 `TestRetrySweepJitteredLoop`, :620 `TestRetryDefaultJitterBounds`.

**Notes:** Pre-seed status stands. Note the audit row already excludes SMTP-level throttles to ADR-001; the in-tree evidence matches that split exactly.

---

### SF-109 — Graceful shutdown (systemd notify, snapshot flush)

**Final status: PARTIAL — resolving the former N/V: shutdown wiring is real and drains server-owned resources; queue drain exists only for the standalone worker via asynq's own signal handling (library behavior, not verifiable from this tree), and the embedded worker is NOT drained; no systemd notify**

Fields: Backend Partial (downgraded from audit's "Verified (okapi-owned)") · Frontend — · DB Verified (clean close) · API — · Security — · Tests — (no shutdown test in-tree) · Docs Partial

**Implementation:** okapi lifecycle owns the process: `cmd/posta/main.go:33-49` (`okapi.New` + server/worker commands); `cmd/posta/server.go:87-89` `cli.RunServer(&okapicli.RunOptions{ShutdownTimeout: 30 * time.Second, OnStart …, OnShutdown …})`; **`OnShutdown` hook at `server.go:248-250`** calling `shutdownServer(res)`.

**Evidence:**
- `shutdownServer` (`cmd/posta/server.go:821-855`) in order: inbound SMTP `Shutdown(30 s ctx)` :826, relay SMTP `Shutdown(30 s ctx)` :833, `cronManager.Stop()` :839 (`Manager.Stop` at `internal/cron/manager.go:112`), `producer.Close()` :842, `redis.Close()` :847, `sqlDB.Close()` :853. This is real, ordered, timeout-bounded shutdown of every resource the server registers — the audit's core claim **verified**.
- Worker drain, standalone process: `cmd/posta/worker.go:292` `return srv.Run(mux)` — drain depends on asynq's `Server.Run` installing SIGTERM/SIGINT handlers and finishing in-flight tasks. **Cannot verify from this tree:** asynq (`github.com/hibiken/asynq`) and okapi (`github.com/jkaninda/okapi v1.1.0`, `go.mod:20`) are external modules, not vendored; no module cache is available to the auditor. Their signal→graceful-drain behavior is library-documented, not checkout-verified — stated explicitly per the evidence rules.
- Readiness flip around the drain: `healthSrv.SetProcessing(true)` before `Run` and `SetProcessing(false)` on return (`cmd/posta/worker.go:287-290`; `internal/worker/health.go:82-83`, `/readyz` :115-125 returns 503 when not processing) — an orchestrator stops routing to a draining worker.
- **Gap found (basis for the downgrade):** the embedded worker's asynq server is never captured or stopped — `startEmbeddedWorker(...)` return discarded at `cmd/posta/server.go:183` (function returns nothing, :534-536), and `shutdownServer` has no asynq-server drain step. Likewise `retry.Worker.Stop()` (`internal/services/retry/worker.go:125-127`) is never wired to shutdown, and the automation engine loop (started with `context.Background()`, `cmd/posta/worker.go:260`) has no stop wiring. In `EmbeddedWorker` server mode, in-flight asynq tasks are abandoned at process exit (recovered later by CAS re-claim + asynq redelivery, per SF-106), not drained.
- **No systemd notify:** `grep -rin "sd_notify|systemd|NOTIFY_SOCKET" internal/ cmd/` → 0 hits. No snapshot flush: no persisted cache snapshots exist to flush (see SF-107).

**Notes:** This resolves the ledger's N/V (gap-analysis §4a: `SF-109 | N/V`) and **downgrades** the parity audit's COMPLETE: "equivalent mechanism documented (okapi lifecycle)" is half-true — the OnShutdown wiring is real and verified, but the "worker drains queues" half rests on unverified dependency behavior for the standalone worker and is false for the embedded worker. `ShutdownTimeout`/signal handling inside okapi v1.1.0 likewise could not be verified from this checkout. Recommended follow-up: wire the embedded asynq server + retry worker + engine stop into `shutdownServer`, and cite the asynq drain from the module source once available.

---

### SF-110 — Hot reload of settings/certs/lookup stores/blocked IPs

**Final status: COMPLETE — confirmed (cert reload N/A: no ACME, per ADR-007; cluster broadcast of reloads is the ADR-009 gap already recorded at SF-105/107)**

Fields: Backend Verified · Frontend Verified (Rules UI) · DB Verified · API Verified · Security Verified · Tests Covered · Docs Partial

**Implementation:**
- **Settings live-apply (TTL):** `internal/services/settings/provider.go:42-47,68-75` — every consumer re-reads through the 30 s TTL cache, so admin edits take effect without restart. The provider doc (:31-35) states this contract.
- **Security rules live-apply:** `internal/services/security/rules.go:103-128` `LoadRules` reads every `security_*` key with clamped minimums; `RulesLoader` (:130-146) re-reads on **every** `Load()` call, deliberately cheap on top of the TTL cache. Wired into the hot path: `internal/services/security/detector.go:77,92-99` holds a `*RulesLoader` and calls `d.rules.Load()` per evaluation (:205, :263, :289, :315); ban-tier TTLs resolve per call too (`internal/services/security/engine.go:101-110`).
- **Blocked-IP reload (the `ReloadBlockedIps`-equivalent):** `internal/services/security/propagate.go` — full rebuild `ResyncFromDB` :223-244, triggered by `allow`/`disallow`/`reload` pub/sub actions :211-214, after every reconnect :145-147, and on the 5-minute reconcile ticker :126-129; ban/unban fan out incrementally :190-210.
- **Certs:** N/A — no ACME client anywhere (`grep -rin "acme" go.mod internal/ cmd/` → only "Acme" examples in address-format docs); ADR-007 governs. Nothing to hot-reload.
- **Frontend + API wiring (verified):** `web/src/views/admin/security/SecurityRules.vue` loads via `adminApi.getSecurityRules` (:46-52) and persists per-rule via `adminApi.updateSecurityRules` (:96, debounce/toggle save :80-121); API client `web/src/api/admin.ts:178-181`; route registered with `UpdateSecurityRulesRequest` at `internal/routes/admin_routes.go:866`; handler `internal/handlers/security_handler.go:585` `UpdateRules`. Rules are DB rows in the settings store, so DB Verified.

**Evidence (tests, not run):** `internal/services/security/rules_test.go:53` `TestDefaultRulesMatchDocumentedTable`, :99 `TestLoadRulesWithoutSourceReturnsDefaults`, :114 `TestLoadRulesUsesCustomValues`, :169 `TestLoadRulesClampsZeroAndNegativeValues`; snapshot/ban tests as cited at SF-107; provider tests as cited at SF-107.

**Notes:** Pre-seed status stands. One nuance vs the the reference platform original: rule edits propagate by TTL expiry (≤30 s) or next evaluation, and blocked-IP/allowlist changes by pub/sub resync — there is no instant push for settings changes, only for bans. That is within what this record claims and does not change the verdict.

---

## Summary of deltas vs the parity audit

- **SF-109 downgraded COMPLETE → PARTIAL:** OnShutdown/shutdownServer verified (server.go:248-250, 821-855), but the standalone worker's drain rests on asynq library behavior unverifiable from this tree, the embedded worker is never drained (server.go:183 discards it), and no systemd-notify equivalent exists. This resolves the ledger N/V.
- **SF-104 correction:** the cron framework runs **11** job definitions, not 8 (all enumerated above); the PARTIAL status and its absent items (DKIM rotation, scheduled report generation) are confirmed.
- **SF-101/102, SF-103, SF-105..108, SF-110:** pre-seed statuses confirmed as written, with exact path:line evidence recorded above.

Nothing in this file was verified by execution: no tests, builds, or runs were performed (no local Go toolchain). All greps quoted were run in this session.
