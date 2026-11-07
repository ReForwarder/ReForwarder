# V-CD — Verification of SF-46..SF-59 (Categories C & D, Stalwart parity audit)

- **Auditor:** V-CD (verification pass, 2026-11-07)
- **Scope:** `docs/audits/STALWART_FEATURE_PARITY_AUDIT.md` Category C (SF-46..SF-51) and Category D (SF-52..SF-59), including pre-seed statuses.
- **Method:** read-only code verification at HEAD (no toolchain available — no builds, tests, or docker were run; all test citations are **named functions at file:line**, not executions). Requirements from `docs/research/stalwart-feature-inventory.md` §C/§D; statuses cross-checked against `docs/research/stalwart-feature-gap-analysis.md` §4a.
- **Verification limits:** anything that requires compiling or executing (test greenness, runtime behavior) is reported as "not run". Doc claims were never accepted as proof; every status below rests on `path:line` reads.

---

### SF-46 — Sieve interpreter (RFC 5228 + extensions)

**Final status: BLOCKED BY ADR-006 (OB-2) — documented alternative verified in code (automation engine + filter nodes)**

Fields: Backend Missing · Frontend — · DB — · API — · Security — · Tests — · Docs Verified (ADR + do-not-implement table present)

**Implementation (alternative):** `internal/services/automation/engine.go` (Engine + ProcessBatch, 10s ticker at engine.go:164, tick budget maxNodesPerTick=10 at engine.go:48-51); `internal/services/automation/nodes.go:85-86` (NodeTypeFilter → executeFilterNode:200-207); `internal/services/automation/graph.go:280-295` (EvaluateConditions shared evaluator).

**Evidence:**
- Zero Sieve code: `grep -rni "sieve|managesieve" internal/ cmd/ web/src` (excluding node_modules) returned **no matches** — no vendored interpreter, no parser, no script store.
- ADR-006 (`docs/architecture/ADR-006-spam-malware.md`, header "Accepted 2026-11-07") covers the filtering substrate with a SpamFilter interface + disposition pipeline and explicitly records the no-Sieve posture.
- Do-not-implement table: `docs/research/reforwarder-stalwart-roadmap.md:165` — "Sieve policy engine (SF-46/101) … ReForwarder's automation engine + filter nodes cover workflow conditioning | Automation filter nodes" — the named alternative exists exactly as documented (see SF-50 evidence for the filter-node tests).
- Ledger §4a: SF-46 = N/A, consistent.

**Notes:** Verified as claimed. The automation engine is wired at `internal/routes/routes.go:512-518` and in both processes (`cmd/posta/server.go:677`, `cmd/posta/worker.go:248`). Not run: `go test` (no toolchain) — all test names below in SF-50 are cited unexecuted.

---

### SF-47 — ManageSieve (RFC 5804)

**Final status: BLOCKED BY ADR-004 (OB-4)**

Fields: Backend Missing · Frontend — · DB — · API — · Security — · Tests — · Docs Verified

**Implementation:** none (intentionally).

**Evidence:**
- No ManageSieve code anywhere (same zero-match grep as SF-46, which included `managesieve`).
- `docs/architecture/ADR-004-imap-pop3.md:4` — covers "SF-86 (POP3 + ManageSieve), SF-47 (ManageSieve — stays with ADR-006, out of this ADR)"; ADR-004 is Option B Phase 4 and no `internal/services/imap`/`pop3` packages exist yet (`ls internal/services/` has no imap/pop3 entries), so ManageSieve is deferred twice over.
- Do-not-implement table: `docs/research/reforwarder-stalwart-roadmap.md:164` — "POP3 / ManageSieve servers (SF-86)" rejected because there are no mailboxes/Sieve users; ADR-003 (mailstore) is also unimplemented at HEAD (no `internal/services/mailstore`, no `mailboxes`/`mail_messages` migrations in `internal/storage/migration/` — grep 0 hits).
- Ledger §4a: SF-47 = N/A, consistent.

**Notes:** Verified as claimed. ManageSieve cannot precede the mailstore + IMAP stack (ADR-003 Phase 3, ADR-004 Phase 4), neither of which exists at HEAD.

---

### SF-48 — Per-account Sieve at final delivery

**Final status: BLOCKED BY ADR-006 (OB-2/5)**

Fields: Backend Missing · Frontend — · DB — · API — · Security — · Tests — · Docs Verified

**Implementation:** none for per-account scripts; the delivery hook that would host them is the inbound pipeline.

**Evidence:**
- No Sieve code (SF-46 grep). `internal/services/inbound/` contains `dmarc.go, parser.go, ratelimit.go, service.go, smtp_server.go` — no per-account script evaluation or FileInto/Vacation/Redirect semantics.
- ADR-002 (`docs/architecture/ADR-002-inbound-smtp-routing.md:18`, decision 5): "rule evaluation and quarantine disposition expose the same condition evaluator used by automations (SF-50/103 currency) — no Sieve interpreter is adopted (documented alternative stands; ADR-006 notes the Phase 6 revisit)" — the documented alternative is precisely the automation evaluator (verified: `internal/services/automation/graph.go:280`).
- Per-recipient Junk filing (the FileInto half) depends on the mailstore (ADR-003), which does not exist at HEAD (see SF-51).
- Ledger §4a: SF-48 = N/A, consistent.

**Notes:** Verified as claimed. Note ADR-002's routing/quarantine decisions themselves are designed but **not implemented** at HEAD: no `inbound_routes`/`inbound_quarantine` tables (grep over `internal/storage/migration/` = 0 hits), so even the non-Sieve delivery-hook alternative is still future work — this is what OB-2 blocks.

---

### SF-49 — Header inspection/transformation (editheader, milter, hooks, A-R injection)

**Final status: PARTIAL** — R-24 injection guards COMPLETE (verified); Authentication-Results injection **absent, PLANNED (Phase 3)**; editheader/milter BLOCKED BY ADR-006/001 (milter-analog webhook hook is ADR-006 §3 design, not implemented)

Fields: Backend Partial · Frontend — · DB — · API — · Security Verified · Tests Verified (named; not run) · Docs Partial

**Implementation:**
- `internal/services/email/service.go:1489` `containsHeaderInjection` (CR/LF/NUL), `:1496` `validateHeaderContent` (reject-not-strip), `:1507` `validateSendHeaders` (Subject + every custom header name/value; matches the audit's cited line), `:1533` `filterCustomHeaders` (reserved headers: from/to/subject/mime-version/content-type; blocked prefixes `x-posta-`, `dkim-`, `arc-`; drops CR/LF/NUL entries).
- `internal/services/email/smtp_sender.go:440` `encodeHeader` — replaces CR/LF/NUL with space as the **last guard** for internal paths (e.g. campaign subjects) that bypass validateSendHeaders; `:580` buildMessage custom-header write guard drops injected entries a final time.
- Absence verified: `grep -rni "Authentication-Results|authentication_results" internal/` = **0 hits** (no A-R injection); no milter code (no milter files in internal/).

**Evidence (tests, named):**
- `internal/services/email/crlf_test.go:30` `TestValidateSendHeadersRejectsInjection`; `:73` `TestFilterCustomHeadersDropsInjection`; `internal/services/email/attachment_mime_test.go:84` `TestBuildMessageFilenameCannotInjectHeaders`; `internal/services/email/headers_test.go:29-75` (system/reserved-header precedence suite). **Not run** (no toolchain).

**Notes:** Two findings beyond the pre-seed text: (1) the last-guard chain is three-deep (API validation → filterCustomHeaders → encodeHeader/buildMessage write guard), stronger than the audit's one-line summary implies; (2) ADR-006 §3 designs the milter-analog (HMAC webhook filter-request with synchronous await + timeout) — that too is unimplemented at HEAD, so "milter BLOCKED BY ADR-006" is accurate and the hook is not yet code.

---

### SF-50 — Conditional routing/policy engine (IfBlock expression language)

**Final status: PARTIAL** — campaign/automation conditioning COMPLETE (verified); transport-policy expressions absent (ADR-001 MTA not implemented at HEAD); evaluator unification **PLANNED (Phase 3)** (pairs SF-103)

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified (guarded SQL) · Tests Verified (named; not run) · Docs Partial

**Implementation:**
- Data model: `internal/models/subscriber_list.go:35-43` (`FilterRule`/`FilterRules` GORM Valuer); referenced by `internal/models/automation.go:178,191,212` (trigger enrollment, filter-node, journey conditions).
- Guarded SQL builder: `internal/storage/repositories/segment_query.go` — field whitelist + custom-key charset (`:52` comment, `isValidSegmentCustomKey:136`), `escapeLikePattern:152`, `ValidateSegmentCondition:237` / `validateSegmentRule:264` / `validateSegmentValue:340`, `BuildSegmentConditionSQL:408` (parameterized output).
- Automation filter nodes + in_segment (R-14): `internal/services/automation/nodes.go:200-207` (executeFilterNode); `internal/services/automation/engine.go:497-577` (`evaluateJourneyConditions`, `evaluateConditionSet`, `segmentMemberChecker` — `in_segment` resolves segments live, closes on unusable segment); shared evaluator `graph.go:280-295`; rule builder validation `domain.go:649-677` (`in_segment needs a numeric segment id`).
- Absence: transport-policy (per-message relay/queue/TLS strategy) expressions — no `internal/services/mta` package, no direct-MX delivery code (`grep LookupMX|delivery_mode|"direct"` outside tests → only `internal/services/verifier/verifier.go:265`, address verification). ADR-001 is Accepted but its MTA is **not built at HEAD**.

**Evidence (tests, named):**
- `internal/storage/repositories/segment_query_test.go:157` `TestSegmentConditionOperatorsAreParameterized`, `:447` `TestSegmentConditionRejectsInjectionInCustomKey`, `:609` `TestSegmentQueryAlwaysScopedToWorkspace`, `:660` `TestSegmentEngagementSubqueriesMatchLegacyEngine`.
- `internal/services/automation/engine_segment_test.go:42` `TestEvaluateConditionSetInSegment`, `:115` `TestSubscriberInSegmentLiveEvaluation`, `:141` `TestSubscriberInSegmentClosesOnUnusableSegment`, `:179` `TestSubscriberInSegmentScopeFiltersSubscriber`; `nodes_test.go:161` `TestFilterNodeBranchesOnConditions`; `graph_test.go:189-288` (`TestEvaluateConditionsEmptyMatchesVacuously` … `TestEvaluateConditionsANDSemantics`). **Not run.**
- Frontend builders: `web/src/views/segments/Segments.vue`, `web/src/views/segments/SegmentDrawer.vue`, `web/src/views/automations/components/ConfigPanel.vue` (condition UIs exist; render-level behavior not verified without a build).

**Notes:** Pre-seed status confirmed. The R-14 in_segment engine wiring comment is at `nodes.go:205` exactly as the audit describes.

---

### SF-51 — Quarantine (spam-percentage Junk filing)

**Final status: BLOCKED BY ADR-002/003/006 (OB-2/3)** — pre-seed said ADR-003/006; ADR-002 added as co-blocker (it designs the `inbound_quarantine` table, which does not exist at HEAD)

Fields: Backend Missing (mail path) · Frontend — · DB Missing (no quarantine table) · API — · Security — · Tests — · Docs Verified

**Implementation (non-mail analog that exists):** `internal/services/messagescan/scanner.go:37` `ActionQuarantine`; `:56-67` `Verdict.Status()` → `models.MessageStatusQuarantined`; `:178-202` `resolveAction` thresholds (flag 3.0 / quarantine 6.0 / reject 10.0, per-endpoint overridable via `Policy.ScanThresholds`); consumed by `internal/handlers/message_handler.go:337` and surfaced in `internal/handlers/dashboard_handler.go:201`. This is the **form/chat submission** scan path (models.Message), not mail delivery.

**Evidence:**
- No mail quarantine: `grep "inbound_quarantine|InboundQuarantine" internal/` = 0 hits; `grep "quarantine|mail_messages|mailboxes" internal/storage/migration/` = 0 hits; no `internal/services/mailstore`; no Junk special-use / mailstore folder code anywhere.
- ADR-002 §3 designs `inbound_quarantine` (viewable/releasable/deletable in dashboard) — designed, not built. ADR-003 designs the store side (Junk filing) — not built. ADR-006 designs the disposition pipeline feeding it — not built.
- Ledger §4a: SF-51 = N/A, consistent with a do-not-implement-tier mail feature pending the Option B phases.

**Notes:** The pre-seed "BLOCKED BY ADR-003/006 (OB-2/3)" is correct but incomplete — the quarantine table itself belongs to ADR-002 (OB-2), so I add it as co-blocker. The messagescan quarantine (RF-side message filters) must not be mistaken for the SF-51 mail feature; it is a separate pre-existing product feature with its own tests (`internal/services/messagescan/scanner_test.go:89` `TestThresholdMapping`, `:130` `TestFilterActionSetsFloorRegardlessOfScore`, `:142` `TestFilterScoreAccumulates` — named, not run).

---

### SF-52 — Queue inspection API (list/query/patch/destroy/cancel/requeue)

**Final status: PARTIAL** — list/query/cancel/run/archive/stats + audited webhook replay COMPLETE (verified); per-message patch/destroy absent, **PLANNED (Phase 3)**

Fields: Backend Partial · Frontend Verified · DB Verified (asynq) · API Verified · Security Verified (admin + scope) · Tests Verified (named; not run) · Docs Partial

**Implementation:**
- `internal/handlers/admin_queue_handler.go` — `AsynqQueueInspector` wrapper `:56-97` (GetQueueInfo, List{Pending,Active,Scheduled,Retry,Archived}Tasks, DeleteTask, RunTask, ArchiveTask), managed-queue guard `isManagedQueue:101`, stats `queueStatsFor:133`, `ListQueues:157`, `ListQueueTasks:221` (per-state paged task list with payload snippet `:305`), `CancelQueueTask:325`, `RunQueueTask:333`, `ArchiveQueueTask:341`, shared audited action path `queueTaskAction:347`.
- Routes: `internal/routes/admin_routes.go:657-721` — GET `/admin/queues`, GET `/admin/queues/{queue}/tasks`, POST `.../tasks/{task_id}/cancel|run|archive` (documented as audited).
- Webhook replay (R-06): `internal/handlers/webhook_replay_handler.go` — `Replay:86`; failed-only gate `:104`; refuses replay when the original payload was never stored `:107-111` (no fabricated payloads); ownership scope checks `:95-99,114-116`; cap `maxReplaysPerHour = 5` `:37` enforced via `CountReplaysSince(delivery.ID, delivery.Attempt, -1h)` `:122-127` (counting only attempt rows above the original count, so original attempts don't consume the budget); audit log `:135`. Wired at `internal/routes/routes.go:331` and route `internal/routes/workspace_resource_routes.go:764-768`.
- Frontend: `web/src/views/admin/Jobs.vue:103-144` (state selector incl. "Dead letter", cancel/run/archive confirmations calling `adminApi.cancelQueueTask/runQueueTask/archiveQueueTask`); API client `web/src/api/admin.ts:130-146`.
- Absence: `grep "Patch" internal/handlers/admin_queue_handler.go` = 0 hits — no per-message/per-recipient patch (schedule/expiry/status) and no whole-message destroy endpoint. asynq's Inspector supports deletion only per task; there is no patch surface.

**Evidence (tests, named):**
- `internal/handlers/admin_queue_test.go:406` `TestAdminQueuesOverview`, `:450` `TestAdminQueueTasksListByState`, `:516` `TestAdminQueueTasksOnQueueThatNeverExisted`, `:541` `TestAdminQueueTaskActions`, `:595` `TestAdminQueuesRequireAdminRole`, `:608` `TestAdminQueuesUnavailableWithoutInspector`.
- `internal/handlers/webhook_replay_handler_test.go:568` `TestWebhookReplayRecordsAuditEvent`, `:598` `TestWebhookReplayRejectsNonFailedDelivery`, `:615` `TestWebhookReplayRejectsMissingPayload`, `:631` `TestWebhookReplayScope`, `:664` `TestWebhookReplayEnforcesHourlyCap`, `:696` `TestWebhookReplayCapIgnoresOriginalAttempts`. **Not run.**

**Notes:** Pre-seed status confirmed at every point I checked. "What is missing for patch/destroy": an endpoint model for patching a queued task's schedule/expiry/payload and a destroy-by-message-id that removes all state (asynq holds one task per email:send via the deterministic ID `internal/queues` `EmailSendTaskID`, so "destroy" ≈ cancel-of-every-state; patch has no asynq primitive and would need a DB-side schedule column + requeue flow). These are correctly parked as Phase 3.

---

### SF-53 — SMTP response taxonomy surfaced to admins

**Final status: PARTIAL — PLANNED (Phase 3)** (per-message transcript/typed-error view; pairs GAP-20). New finding: the aggregate classification analytics query has **no production caller**

Fields: Backend Partial · Frontend Partial (raw metadata JSON only) · DB Verified · API Partial · Security — · Tests Verified (named; not run) · Docs Partial

**Implementation:**
- `internal/services/provider_events/classifier.go:36-51` — 14 classes (`hard_bounce, soft_bounce, mailbox_full, invalid_recipient, dns_failure, connection_failure, tls_failure, reputation, rate_limited, deferred, blocked, auth_failure, complaint, delivered`) + `ClassUnknown`; entry points `Classify:84`, `ClassifySendError:126`, `classifySMTP:148` (3-digit code + enhanced-code mapping `classifyEnhanced:206`), provider-diagnostic text matching `classifyDiagnosticText:352`, marker-based bounce classification `classifyMarker:189`. Matches the audit's "14-class" claim (14 + unknown).
- Persistence: `internal/worker/handler.go:352-387` — SendError classification lands in the email event's `metadata["classification"]` (`:378`); permanent rejections carry it on the bounce Reason (`:488-496` `handlePermanentRejection`); provider-webhook path `internal/services/provider_events/service.go:377-383` (`mergedMetadata` adds the classification to every recorded timeline row).
- Surfacing: email events are exposed with metadata via `internal/handlers/logs_handler.go:444` `GetMessageEvents` (timeline) and the metadata blob is rendered in `web/src/views/logs/MessageDetail.vue:77,287` (raw JSON block) — a developer-grade view, not a typed taxonomy UI. `grep -rn "classification|hard_bounce" web/src` = 0 hits: **no dedicated frontend taxonomy surface**.
- **Finding:** `ClassificationCounts` (`internal/services/provider_events/service.go:417-429`, JSONB aggregation over email_events) has **no handler/route caller** — `grep -rn "ClassificationCounts" internal/ cmd/` matches only the service and its tests (`provider_events_test.go:1318-1344`). The aggregation is tested but unwired; the audit's "API: Verified" overstates this specific analytics slice.

**Evidence (tests, named):** `internal/services/provider_events/provider_events_test.go:54` `TestClassifySMTPMatrix`, `:119` `TestClassifyEventTypes`, `:156` `TestClassificationPermanentSet`, `:177` `TestClassifySendError`; `internal/worker/classification_test.go:51` `TestPermanentRejectionClassificationPolicy`, `:242` `TestHandlePermanentRejectionRecordsClassification`; `internal/services/provider_events/provider_events_test.go:1321` `TestClassificationCountsQuery`. **Not run.**

**Notes:** Stalwart's typed error objects (DnsError, TlsError, MtaStsError, ConcurrencyLimited…) map onto the classes only as far as the worker's SendError carries them; per-message SMTP transcript view (the Phase 3 item, GAP-20) remains the real surfacing gap. Recommendation: either wire ClassificationCounts into an endpoint or record it as a dead-end in the ledgers (AGENTS.md §4.3 treats unwired code as not-Done).

---

### SF-54 — Global queue pause/resume

**Final status: PARTIAL — PLANNED (Phase 3)** (pairs GAP-48); global queue pause/resume and per-domain suspension verified absent

Fields: Backend Missing (global/queue-level) · Frontend Partial (campaign/automation controls) · DB — · API Missing (queue-level) · Security — · Tests Missing (queue-level) · Docs Missing

**Implementation (what exists, verified):**
- Campaign pause/resume: `internal/handlers/campaign_handler.go:440` (pause transition) and `:455-462` (resume re-enqueues); worker gate `internal/worker/handler.go:164-181` — a paused campaign releases already-queued emails back to pending so a Resume re-dispatches.
- Automation pause: `internal/handlers/automation_handler.go:546-560` (CAS pause under concurrent-status guard; delete refuses live automations `:491-503`).
- Settings-level global enqueue gate: `internal/services/email/service.go:493-494` and `:666-667` — `s.settings.MaintenanceMode()` blocks **new** sends ("maintenance: email sending is temporarily disabled"), hot-applied via `internal/services/settings/provider.go:116` (`maintenance_mode` bool) with the 30s TTL (`provider.go:42-47`). This stops enqueueing; it does **not** pause or drain queued asynq tasks.

**Absence (verified):** `grep "PauseQueue|UnpauseQueue|asynq.*Pause" internal/` = 0 hits — asynq's queue-pause feature is unused; no per-queue pause endpoint; no per-domain suspension field (`grep PerDomain|per-domain internal/models/domain.go` = 0 hits); no global queue-pause API in `internal/routes/admin_routes.go`. `internal/services/workermon` is heartbeat/read-only monitoring (`heartbeat.go:48 StartHeartbeat`, `monitor.go:60-91`), not a control plane; worker stop is process-level self-deregistration (`cmd/posta/worker.go:285` comment — stops advertising ready for the orchestrator), not an admin action.

**Evidence (tests):** none for queue-level pause (nothing exists to test); the analog gates have tests (e.g. `internal/cron/jobs/campaign_scheduler_test.go:590` `TestSchedulerIgnoresPausedAndCancelledCampaigns` — named, not run).

**Notes:** Pre-seed "Backend: Missing (global)" + "PARTIAL — PLANNED" confirmed. The MaintenanceMode gate is worth recording in the ledger as the existing partial mechanism (it is the closest thing to PauseMtaQueue and is hot-settings-driven).

---

### SF-55 — Live delivery diagnosis SSE [ENT]

**Final status: BLOCKED BY ADR-001 (OB-1)** — the MTA does not exist at HEAD, and no SMTP connect-test/diagnosis endpoint exists

Fields: Backend Missing · Frontend — · DB — · API Missing · Security — · Tests Missing · Docs Partial (ADR accepted, feature not built)

**Implementation:** none for delivery diagnosis.

**Evidence:**
- `grep "diagnose|connect.*test|mx.*test" internal/handlers internal/routes` → only `internal/handlers/domain_health_handler.go:77` `Diagnose` ("re-runs the live DNS checks on demand", route `internal/routes/paths.go:49` `/domains/{id}/health/diagnose`) — DNS/SPF/DKIM/DMARC health re-run, **not** an MX→MTA-STS→connect/EHLO/MAIL/RCPT/DATA staged SMTP test.
- No MTA at HEAD: no `internal/services/mta`; no direct-MX delivery code (see SF-50 absence evidence); ADR-001 (`docs/architecture/ADR-001-outbound-mta.md`) is Accepted with `mta_delivery_attempts` designed in its §5 — table absent (`grep mta_delivery_attempts internal/` = 0 hits).
- SSE infrastructure that would host it exists (see SF-56 evidence) — the blocker is the MTA substrate, not the streaming layer.
- Ledger §4a: SF-55 = N/I, consistent.

**Notes:** Verified as claimed. The only nuance: ADR-001 being *accepted* means the blocker is implementation status (OB-1 pending), not a decision gap — the status wording "BLOCKED BY ADR-001 (OB-1)" is the audit's convention for that and I keep it.

---

### SF-56 — Delivery tracing (trace store + live tracing SSE)

**Final status: PARTIAL — PLANNED (Phase 3)** (read-only tracing verified; live per-message trace SSE absent; the durable transport-trace store additionally awaits the ADR-001 MTA)

Fields: Backend Partial · Frontend Verified (Logs timeline) · DB Verified · API Partial · Security Verified · Tests Partial · Docs Partial

**Implementation (read-only tracing, verified):**
- `internal/handlers/logs_handler.go:444` `GetMessageEvents` (per-message `email_events` timeline, oldest first; rows embed metadata), `:173` `ListMessages`, `:356` `GetMessage`, `:393` `GetMessageContent`; webhook side `internal/models/webhook_delivery_attempt.go:27-38` (`webhook_delivery_attempts` table) with its repository wired into the logs handler (`:100` `SetWebhookDeliveryAttemptRepo`).
- Frontend: `web/src/views/logs/MessageDetail.vue:205-214` renders the timeline events; `:287` renders raw metadata.
- Absence: no live per-message trace SSE (no trace/Trace endpoint in handlers — `grep "trace|Trace" internal/handlers` = 0 relevant hits); `mta_delivery_attempts` absent (ADR-001 §5, MTA not built).

**SSE infrastructure that exists (verified):**
- Event bus: `internal/services/realtime/realtime.go:47-51` (`Publish`/`Subscribe` per topic, `WorkspaceTopic:55`), memory + Redis buses (`realtime/redis.go`).
- SSE endpoints: `internal/handlers/event_handler.go:126-153` (admin event stream), `internal/handlers/inbound_handler.go:80-126` (inbound received stream), `internal/handlers/message_handler.go:514` (workspace `/message-stream`, route `internal/routes/message_routes.go:286-288`), `internal/handlers/security_handler.go:320` (security events), `internal/handlers/admin_handler.go:651` (worker.status) and `:685` `MetricsStream`. All use `c.SSEStreamWithOptions` with 30s ping — the pattern the audit says exists in-repo.

**Evidence (tests, named):** `internal/handlers/logs_handler_test.go` (exists; named checks not enumerated here — not run); realtime `internal/services/realtime/realtime_test.go` (exists, not run).

**Notes:** Pre-seed status confirmed. One sharpening: the "trace store" half of SF-56 depends on `mta_delivery_attempts` (ADR-001), which does not exist — so today's tracing covers the provider/relay path and webhook deliveries, not direct-MX SMTP transcripts. Phase 3 sequencing for the SSE slice is unblocked by infrastructure; the SMTP-transcript slice is not (OB-1).

---

### SF-57 — DMARC/TLS/ARF report admin objects

**Final status: PARTIAL** — DMARC aggregate objects + read surface COMPLETE (R-17, verified); TLS-RPT/ARF objects absent, **PLANNED (Phase 3)** (with SF-26/31)

Fields: Backend Partial · Frontend Verified (DMARC summary on Domains) · DB Verified · API Verified · Security Verified (workspace-scoped + audited) · Tests Verified (named; not run) · Docs Partial

**Implementation:**
- Model: `internal/models/dmarc.go:31` `DMARCReport` (table `dmarc_reports`, `:59`) — RFC 7489 aggregate records (R-17), per-report-record granularity.
- Ingestion: `internal/services/inbound/dmarc.go:40-117+` (attach-on-service, gzip round-trip, refusals, alignment parsing) bound to the inbound receiver.
- Read surface: `internal/handlers/dmarc_handler.go:52` `NewDMARCReportHandler` (repo + domain repo + audit logger), `:85` `Get` (per-domain summary; foreign-domain 404 path covered).

**Absence (verified):** `grep -rni "tls.rpt|tlsrpt|tls_report|ARF|feedback-report" internal/` = **0 hits** — no TLS-RPT report objects and no ARF (abuse/failure feedback-report) objects or parsers.

**Evidence (tests, named):** `internal/handlers/dmarc_handler_test.go:731` `TestDMARCGetSummary`, `:816` `TestDMARCGetForeignDomainNotFound`, `:848` `TestDMARCGetEmptySummary`; parser side `internal/services/provider_events/dmarc_test.go:136` `TestParseDMARCGoogleFixture`, `:187` `TestParseDMARCEmptyReport`, `:204` `TestParseDMARCGzippedRoundTrip`, `:228` `TestParseDMARCRefusals`, `:250` `TestParseDMARCAlignmentCaseInsensitive`, `:276` `TestDMARCReportRowShape`. **Not run.** Frontend: DMARC summary consumed in `web/src/views/domains/Domains.vue` / `web/src/api/domains.ts` (data binding present; render not verified without build).

**Notes:** Pre-seed status confirmed at every point checked.

---

### SF-58 — Virtual queues, named queues, delivery-schedule objects

**Final status: COMPLETE** — verified: named asynq queues + weighted per-queue consumption + per-queue stats; delivery scheduling at application level (R-01 campaign windows, R-13 automation cron) replaces Stalwart's per-message MtaVirtualQueue/MtaDeliverySchedule objects

Fields: Backend Verified · Frontend Partial (queue read/actions in admin Jobs.vue; no virtual-queue management, none expected) · DB Verified (asynq/Redis) · API Verified · Security — · Tests Verified (named; not run) · Docs Partial

**Implementation:**
- Queue names: `internal/queues/` (leaf package) — `Transactional = "transactional"`, `Bulk = "bulk"`, `Low = "low"`; task type `email:send` + deterministic ID `EmailSendTaskID` (`email:send:<id>`) enabling reconcile. Aliased at `internal/worker/tasks.go:39-41`.
- Producers pin queues: `internal/worker/producer.go:140,162` (bulk), `:178,196,210,230` (transactional), `:254` (low); campaign batch → `QueueBulk` (`internal/worker/campaign_processor.go:477`); default fallback transactional (`:56,:110`).
- Per-queue worker weighting: `cmd/posta/worker.go:139-143` (`Concurrency: cfg.WorkerConcurrency`, weights transactional 6 / bulk 3 / low 1).
- Per-queue stats surfaced: `internal/handlers/admin_queue_handler.go:133-171` (`queueStatsFor`/`ListQueues` from asynq QueueInfo) + admin Jobs.vue.
- Delivery-schedule analogs: campaign scheduling R-01 — `internal/models/campaign.go:105-108` `LocalSendHour` (recipient-timezone send hour), `:119` `ScheduledAt`; scheduler cron `internal/cron/jobs/campaign_scheduler.go:76` (`* * * * *`), claim-and-enqueue path. Automation schedules R-13 — `internal/models/automation.go:169-172` `CronExpression` (5-field cron driving `AutomationEventSchedule`), `internal/services/automation/triggers.go:124-125` (recurring schedule semantics), engine cron scheduler (`engine.go:62-63` sweep cadence).

**Evidence (tests, named):** `internal/cron/jobs/campaign_scheduler_test.go:497` `TestSchedulerClaimsOnlyDueScheduledCampaigns`, `:528` `TestSchedulerClaimConflictSkipsWithoutEnqueue`, `:555` `TestSchedulerEnqueueFailureRollsBackToScheduled`, `:590` `TestSchedulerIgnoresPausedAndCancelledCampaigns`; queue-behavior tests `internal/worker/retry_bound_test.go`, `internal/worker/atomic_claim_test.go` (exist; named checks not enumerated — not run). **Not run.**

**Notes:** Ledger §4a says FULL and the audit says COMPLETE — both hold for what the platform needs. Honest caveat: Stalwart's first-class per-message `MtaVirtualQueue`/`MtaDeliverySchedule` objects (addressing scheme → queue mapping, per-schedule worker pools) are not adopted; the analogs are application-level (campaign/automation schedulers + three weighted queues). If the register wants to distinguish "named queues COMPLETE; virtual-queue objects replaced by analogs", the Notes field above is the sentence to carry.

---

### SF-59 — Operational Action objects (Reload*, InvalidateCaches, Pause/Resume)

**Final status: PARTIAL — PLANNED (Phase 3)** — cron fleet + hot settings + audited admin queue actions verified; explicit Reload*/InvalidateCaches action endpoints absent

Fields: Backend Partial · Frontend Partial (cron status read) · DB — · API Partial · Security Verified (admin) · Tests Partial · Docs Partial

**Implementation:**
- Cron fleet (11 jobs, registered on `internal/cron/manager.go:63-86` — UTC `cron.New`, `AddFunc(job.Schedule())`, `Jobs()` status): `internal/cron/jobs/` — account_cleanup (02:00 UTC, `account_cleanup.go:41`), api_key_expiry (08:00, `:46`), bounce_alert (09:00, `:61`), backup (configurable, `backup.go:138`), campaign_scheduler (every minute, `:76`), campaign_restart (*/5, `campaign_restart.go:54`), daily_report (07:00, `daily_report.go:59`), message_digest (hourly :05, `message_digest.go:66`), queue_reconcile (*/5, `queue_reconcile.go:83`), retention (03:00, `retention.go:196`), update_check (04:37, `update_check.go:44`). Read surface: `internal/handlers/cron_handler.go:36` `List`.
- Hot settings: `internal/services/settings/provider.go:42-47` (30s cache TTL), `:70` (stale reload on read) — settings take effect without a reload action; e.g. `MaintenanceMode()` (`:116`) gates sends (see SF-54), retention days gate the retention sweep (`:131,146,149`).
- Admin actions that exist: audited queue task cancel/run/archive (`internal/handlers/admin_queue_handler.go:347`), audited webhook replay (`webhook_replay_handler.go:135`), audited domain health re-diagnose (`domain_health_handler.go:104`).
- Absence (verified): no ReloadSettings/ReloadTlsCertificates/ReloadLookupStores/InvalidateCaches/Pause/ResumeMtaQueue endpoints — `grep "Invalidate|Reload" internal/handlers internal/routes/admin_routes.go` finds only internal cache invalidation (`form_handler.go:64` form-key cache, `email_handler.go:158-159` user dashboard cache) and no action-object routes. No TroubleshootDmarc/ClassifySpam analogs.

**Evidence (tests, named):** `internal/cron/jobs/queue_reconcile_test.go:465` `TestQueueReconcileReenqueuesLostTask`, `:488` `TestQueueReconcileSkipsLiveTask`, `:515` `TestQueueReconcileReenqueuesArchivedTask`, `:532` `TestQueueReconcileSkipsCampaignEmails`, `:549` `TestQueueReconcileOnlyStaleQueuedRows`; `internal/cron/jobs/retention_test.go:26,46,65` (`TestCapDays`, `TestMinDays`, `TestFormRetentionDays`); `internal/cron/jobs/backup_test.go` (exists). **Not run.**

**Notes:** Pre-seed status confirmed. The 30s hot-TTL design means ReloadSettings is structurally subsumed for the settings case (document that rather than build a redundant action); the genuinely missing pieces are explicit cache invalidation and queue pause/resume (the latter pairs with SF-54/GAP-48).

---

## Summary of deltas vs the pre-seed audit

| Record | Pre-seed | V-CD verdict | Change |
|---|---|---|---|
| SF-46 | BLOCKED BY ADR-006 | BLOCKED BY ADR-006 (alternative verified) | none |
| SF-47 | BLOCKED BY ADR-004 | BLOCKED BY ADR-004 | none |
| SF-48 | BLOCKED BY ADR-006 | BLOCKED BY ADR-006 (ADR-002 alternative also unimplemented) | sharpened |
| SF-49 | PARTIAL | PARTIAL | none (3-deep guard chain noted) |
| SF-50 | PARTIAL | PARTIAL | none |
| SF-51 | BLOCKED BY ADR-003/006 | BLOCKED BY ADR-002/003/006 | ADR-002 added as co-blocker |
| SF-52 | PARTIAL | PARTIAL | none |
| SF-53 | PARTIAL | PARTIAL | **new finding:** `ClassificationCounts` (provider_events/service.go:417) has no production caller — "API: Verified" overstated for the analytics slice |
| SF-54 | PARTIAL — PLANNED | PARTIAL — PLANNED | MaintenanceMode gate (email/service.go:493) recorded as existing partial enqueue gate |
| SF-55 | BLOCKED BY ADR-001 | BLOCKED BY ADR-001 (MTA absent at HEAD confirmed) | none |
| SF-56 | PARTIAL — PLANNED | PARTIAL — PLANNED | sharpened: SMTP-transcript slice depends on unbuilt `mta_delivery_attempts` |
| SF-57 | PARTIAL | PARTIAL | none |
| SF-58 | COMPLETE | COMPLETE (analogs noted) | caveat sentence added |
| SF-59 | PARTIAL — PLANNED | PARTIAL — PLANNED | none |

**Not verified (explicitly):** any test execution (`go test ./...` — no toolchain in this environment); frontend render behavior (no build); runtime SSE/queue behavior. All test citations are named-function existence checks at file:line, not pass/fail evidence.
