# STATUS_HISTORY — feature-status decision ledger

Append-only ledger of the repo's feature-status decisions. One entry per producing run that re-baselines or changes registry statuses (`RF-F001`…`RF-Fnnn`). Run `dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7` produced the initial entry below.

## Ledger rules

1. **Append-only.** Never rewrite or delete an entry. A correction is a new entry that cites the entry it supersedes.
2. **Every entry carries:** date, producing run id, HEAD audited (full sha + subject), test-gate result, working-tree state, and the four status counts. An entry without a producing run id is not a valid status decision.
3. **Evidence before status.** A status may only move on test-gate evidence — `go test ./... -count=1`, PASS — recorded at the stated HEAD. A changed file alone never marks a feature Done (see `PROJECT_MAINTENANCE.md` §5).
4. **Counts reconcile.** The four counts always sum to the registry size (105 at the initial entry: `RF-F001`–`RF-F105`). If the registry grows, features get new `RF-` IDs and the entry records the new total.
5. **History is not the queue.** Work ordering lives in `PRIORITIES.md`; detailed task state lives in `BACKLOG.md`; this file records decisions and their provenance only.

## Status semantics

Defined by this ledger; `In Review` was unused (zero) at the initial entry.

- **Done** — implemented at the audited HEAD with test-gate evidence; any residual scope is tracked as its own registry item or gap entry.
- **In Progress** — partially implemented; the residual work is enumerated in `PRIORITIES.md` / `BACKLOG.md` (e.g. `RF-F005` 2FA recovery codes → the P1 completion item).
- **In Review** — implemented and awaiting review.
- **To-dos** — not implemented at the audited HEAD.

---

## Entries

### 2026-11-06 — Initial audit entry

| Field | Value |
| --- | --- |
| Producing run | `dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7` |
| HEAD audited | `f5f0e921455de901370b5f37b7112b634f6c2066` — "Add P3 platform improvements: bounded retries, scheduled triggers, segment conditions, per-key limits, DMARC ingestion" (2026-11-06T00:44:14+05:30) |
| Working tree at audit | 34 dirty/untracked entries (preserved, uncommitted) |
| Test gate | **PASS** — `go test ./... -count=1` (dockerized `golang:1.27`). Result recorded from the producing run; not re-executed for this ledger entry. |
| Done | **74** |
| In Progress | **9** — `RF-F005`, `RF-F014`, `RF-F015`, `RF-F026`, `RF-F027`, `RF-F028`, `RF-F053`, `RF-F081`, `RF-F082` |
| In Review | **0** |
| To-dos | **22** — `RF-F006`, `RF-F023`, `RF-F029`, `RF-F040`, `RF-F048`, `RF-F056`, `RF-F057`, `RF-F061`, `RF-F071`, `RF-F073`, `RF-F074`, `RF-F075`, `RF-F076`, `RF-F091`, `RF-F092`, `RF-F097`, `RF-F099`, `RF-F100`, `RF-F101`, `RF-F102`, `RF-F103`, `RF-F104` |
| Total | 105 (`RF-F001`–`RF-F105`); 74 + 9 + 0 + 22 reconciles against the producing run's registry view. |
| Delta vs previous entry | n/a — initial entry |

**Notes**

- Provenance: the counts and the HEAD are the initial status decisions handed to this ledger by the producing run's registry view (`RF-F001`–`RF-F105`). The In Progress and To-dos ID lists above were cross-checked against that view: the 9 In Progress and 22 To-dos IDs are enumerated explicitly; the remaining 74 are Done.
- **Repo state at ledger-writing time (observed 2026-11-06 in the writing session):** HEAD has advanced to `0f52636172e19f16a80f9fae421bb57e99ebc6d4` ("Fix WebAuthn nil-user abort path and duplicate impersonation stop audit", 2026-11-06T04:19:49+05:30) and the working tree is clean (`git status --porcelain | wc -l` → 0). Two commits landed after the audited HEAD — `346eb67` ("Complete remaining roadmap items: WebAuthn, impersonation, feature flags, contract test") and `0f52636` — together touching 48 files (+12,072/−27, per `git diff --stat f5f0e92..HEAD`). Whether the 34 dirty/untracked entries recorded at audit time are exactly the content these commits landed is **not verified by this entry** — an open item for the next sync run, which should re-run the test gate at the new HEAD before moving any status.

---

### 2026-11-06 — Correction entry (working-tree observation; no status change)

| Field | Value |
| --- | --- |
| Producing run | none — documentation-fix session, 2026-11-06. Per ledger rule 2 this is **not a status decision**: no registry status moves in this entry. |
| Supersedes | The **Repo state at ledger-writing time** observation in the 2026-11-06 initial entry above (its "working tree is clean (`git status --porcelain \| wc -l` → 0)" claim) and the equivalent claim in `docs/project/FEATURE_AUDIT.md` §9 item 2 ("`git status --porcelain=v1` → 0 lines"). |
| HEAD | Unchanged from the superseded observation: `0f52636172e19f16a80f9fae421bb57e99ebc6d4`. |
| Working tree (re-observed) | **Not clean** — `git status --porcelain=v1` → 12 entries: `README.md` modified + 10 untracked root docs (AGENTS.md, ARCHITECTURE_OVERVIEW.md, BACKLOG.md, FEATURE_GAP_ANALYSIS.md, FEATURE_INVENTORY.md, PRIORITIES.md, PROJECT_MAINTENANCE.md, PROJECT_OVERVIEW.md, ROADMAP.md, STATUS_HISTORY.md) + untracked `docs/project/`. Command run 2026-11-06 in this correcting session. The superseded "clean / 0 lines" claims cannot have been true while the doc suite itself sat untracked on disk. |
| Test gate | Not re-run in this session. The initial entry's gate-provenance caveat (tested tree between `f5f0e92` and `0f52636`) stands, as does the deferral of a re-run at `0f52636` to the next sync run. |
| Done / In Progress / In Review / To-dos | Unchanged: **74 / 9 / 0 / 22** (105 total). |
| Delta vs previous entry | none — observation correction only |

---

### 2026-11-07 — Wave progress note: Option B wave OB-1 (outbound MTA foundation; NOT a status decision)

| Field | Value |
| --- | --- |
| Producing run | none — implementation wave OB-1 per `docs/architecture/ADR-001-outbound-mta.md` (coordinator-directed; route registration and the delivery-mode PATCH are explicitly deferred to the coordinator wave, so **no registry status moves here**) |
| Scope landed | New package `internal/services/mta` (resolver with 30s TTL cache + null-MX/implicit-MX fallback per ADR-001 §3, ESMTP session runner with mandatory STARTTLS-when-offered and AUTH-never-to-MX, per-domain policy + hourly budget, delivery executor writing `mta_delivery_attempts`); additive migration (`models.MTADeliveryAttempt` registered in `internal/storage/migration/migration.go`; `domains` gains `delivery_mode` (default `'relay'`), `outbound_bind_ip`, `max_msgs_per_connection`, `hourly_budget`); worker branch in `internal/worker/handler.go` (`relay` = unchanged path; `direct` = MTA executor, delivered = MX 2xx only, fail-closed on unverified sender domain); gated submission listener `internal/services/mta/submission.go` (`POSTA_SUBMISSION_ENABLED`, default false, port 587, AUTH against relay credentials via `smtprelay.VerifyPassword`, sender-domain + recipient validation); PTR guidance slice in `internal/services/domain/health.go` |
| Test evidence (run 2026-11-07, dockerized `golang:1.27`) | `go build ./...` exit 0; `go test -count=1 -race ./internal/services/mta/... ./internal/worker/... ./internal/services/domain/...` exit 0 (mta 2.8s, worker 1.4s, domain 1.0s). Named tests: TestResolveOrdersByPreference, TestResolveShufflesEqualPreference, TestResolveCachesWithTTL, TestInvalidateDomainForcesFreshLookup, TestNullMXFallsBackToA, TestImplicitMXWhenNoMXRecords, TestResolveErrorIsNotCached, TestResolveRejectsEmptyDomain, TestBudgetKeeperHourlyWindow, TestBudgetKeeperUnlimitedWhenBudgetZero, TestBudgetKeeperIsPerDomain, TestPolicyForDefaultsAndOverrides, TestDeferDelayJitterBounded, TestSessionHappyPath, TestSessionSTARTTLSEnforcedWhenOffered, TestSessionPlainWhenSTARTTLSNotOffered, TestSessionTransient450AtMailFrom, TestSessionTransient421AtMailFrom, TestSessionPermanent550AtRcptTo, TestSessionRefusesAUTHOnMXSession, TestSessionSMTPUTF8RequiredForUTF8Envelope, TestSessionSMTPUTF8Negotiated, TestSessionStallMidConversationFailsWithinBudget, TestDirectDeliveryHappyPath, TestDirectDeliverySTARTTLSWhenOffered, TestDirectDeliveryPlaintextWhenNoSTARTTLSOffered, TestDirectDeliveryTransient421And450, TestDirectDeliveryPermanent550AtRcpt, TestDirectDeliveryFailsClosedForUnverifiedSenderDomain, TestDirectDeliveryInvalidRecipientFailsPermanent, TestDirectDeliveryHourlyBudgetExhaustedDefers, TestDirectDeliveryGroupsRecipientsByDerivedDomain, TestDirectDeliveryDNSFailureIsTransientAndRecorded, TestDirectDeliveryAttemptBoundByPolicy, TestDirectDeliveryAttemptRowsForEveryAttemptIncludingFailures, TestDirectDeliveryNeverDeliveredOnNonDATA2xx, TestSubmissionAuthenticatedSendAccepted, TestSubmissionUnauthenticatedCommandsRefused, TestSubmissionUnverifiedSenderDomainRefused, TestSubmissionInvalidRecipientRefused, TestSubmissionBadCredentialsRefused, TestSubmissionBackendRefusesIncompleteWiring, TestDirectModeDeliveryHappyPathViaWorker, TestDirectModeDuplicateDeliveryDeliversOnce, TestDirectModeTransientThrottleReenqueues, TestDirectModePermanent550SuppressesWithoutRetry, TestRelayModeUnchangedForRelayDomains, TestDirectModeFailsClosedWithoutExecutor, TestPTRGuidanceRelayModeOmitted, TestPTRGuidanceDirectModeWithoutBindIP, TestPTRGuidanceMatchAndMismatch, TestPTRGuidanceMissingRecord, TestPTRGuidanceLookupErrorDoesNotFailReport |
| Battery caveat | In the same session, `go test ./internal/services/email/...` fails 6 DKIM tests (`crypto: no encryption key configured`) — caused by the **concurrent uncommitted SF-78 change to `internal/services/crypto/crypto.go`** (not by OB-1 files; proven: the email package passes at HEAD `02666cc` in a clean worktree, exit 0). OB-1 does not touch crypto, DKIM signing, handlers, or the relay sender path. |
| Done / In Progress / In Review / To-dos | Unchanged — the registry statuses are untouched by this note. OB-1 as a whole remains **In Progress** until the coordinator wires the delivery-mode PATCH route (the Done bar's wiring criterion is not met without it). |
| Recorded deviations | (1) ADR-001 §3 null-MX → A/AAAA fallback conflicts with RFC 7505 §3 ("no mail service"); implemented per ADR as instructed, flagged for ADR-007 revisit. (2) `MaxConnections`/`MaxMsgsPerConnection` policy fields are carried but only bite once batch connection reuse lands (OB-2); `MaxHostAttempts` bounds per-domain MX attempts now. (3) Budget keeper is per-process (documented in code); shared budgeting is deferred. (4) Honor-421/450 re-enqueue uses the existing asynq backoff; the jittered defer delay is computed and recorded but full delay-driven re-enqueue waits for the queue-manager slice. |

---

### 2026-11-07 — P3-4 identity/security wave: RF-F006 To-dos → Done

| Field | Value |
| --- | --- |
| Producing run | `dwfrun-65ae252f-6257-4524-a0a2-b3cb715a2689` (P3-4 identity/security completer wave) |
| HEAD audited | `02666cc` — "Stalwart parity register: verification complete (123/123 records, tally frozen)" — unchanged during the wave; all wave output is uncommitted working-tree content on top of it |
| Working tree at writing | Heavily dirty: concurrent sibling waves (P3-1 inbound verification, OB-1 MTA, SF-78 crypto hardening, SF-89 web push, SF-100 setting history) landed uncommitted files during the same session — see "Concurrent-wave noise" below |
| Test gate | **PASS** — `go test ./cmd/... ./internal/... -count=1`, dockerized `golang:1.27`, 2026-11-07: 43 packages `ok`, zero FAIL (`internal/handlers` 16.3s incl. the wave's suites; `internal/middlewares`, `internal/services/auth`, `internal/services/ratelimit`, `internal/services/webauthn`, `internal/routes`, `internal/worker`, `internal/services/workspace` all ok). **Scope caveat, recorded precisely:** the literal `go test ./...` / `go build ./...` currently fails tree-wide on the gitignored `docs/node_modules` artifact that the docs-site work installed mid-session ("no Go files in /app/docs/node_modules/@jsonjoy.com/util/lib/sort"); `./cmd/... ./internal/...` was verified to cover every Go package in the module (`find` for `*.go` outside those trees and outside `docs/`, `web/`, `.git/` returns nothing; `examples/` has no Go code) |
| Done | **75** (+1: `RF-F006`) |
| In Progress | **9** — unchanged (`RF-F005`, `RF-F014`, `RF-F015`, `RF-F026`, `RF-F027`, `RF-F028`, `RF-F053`, `RF-F081`, `RF-F082`) |
| In Review | **0** |
| To-dos | **21** (−1: `RF-F006`) |
| Total | 105 (`RF-F001`–`RF-F105`); 75 + 9 + 0 + 21 reconciles. Concurrent sibling waves are moving their own statuses in the same tree; this entry re-baselines only the single move above and does not claim a fresh registry view. |
| Delta vs previous entry | `RF-F006` To-dos → Done |

**Scope landed by this wave (all uncommitted at `02666cc`)**

- **GAP-12 / RF-F006** — `two_factor_required` enforced at login: policy check `internal/handlers/user_handler.go:129-134`; forced-enrollment marker `two_factor_enrollment_required` on AuthResponse (`:199-210`) and profile (`:709-725`); login flow `:572-648`; passkey path `:953-981`; dashboard redirect `web/src/views/auth/Login.vue` + `web/src/stores/auth.ts` → `/profile?setup=2fa`; setup card `web/src/views/auth/Profile.vue`. Admins not exempt. Documented deviation from the BACKLOG criterion's letter (session IS issued with the marker — Setup2FA needs a session; refusing would lock out) and residual (middleware layer does not refuse un-enrolled sessions; `user_handler.go:566-571`).
- **SF-35/SF-67** — app passwords: `models/user_app_password.go`, additive migration (`internal/storage/migration/migration.go:82`), `internal/storage/repositories/user_app_password_repo.go`, one-time-display generate + list + revoke (`internal/handlers/app_password_handler.go`), user-scoped routes (`internal/routes/user_routes.go:451-496`), Profile.vue card, `web/src/api/auth.ts` client. SMTP-submission consumption is a **documented seam** (`CredentialVerifier` at `internal/services/mta/submission.go:52-56` + `VerifyAppPassword`/`FindActiveByUserID`); wiring into `internal/services/mta/` was out of this wave's bounds.
- **CA-03** — refresh-token rotation: `models/refresh_token.go` (migration `migration.go:83`), `internal/storage/repositories/refresh_token_repo.go`, `internal/services/auth/refresh.go` (SHA-256+pepper digests, atomic single-use rotation, replay revokes the family), issued at login (`authResponse`), `POST /auth/refresh` (`user_routes.go:498-519`), logout revocation (`internal/handlers/session_handler.go:162-168`), `POSTA_REFRESH_TOKENS_ENABLED` default true (`internal/config/config.go:53-57,375`); auth store keeps the token in memory and sends it with logout.
- **SF-04 residual** — `internal/middlewares/ipallowed_test.go`: TestIPAllowed (19-case matrix), TestAPIKeyAuth_IPAllowlistGate, TestAPIKeyAuth_MissingKeyPaths, TestQueryTokenAllowedMatrix, TestAuthenticate_MissingCredentialMessage (`middleware.go:253` gate).
- **SF-37 residual** — `internal/handlers/oauth_handler_test.go`: 9 fake-driver tests (provider CRUD, unknown-type rejection, public list/discovery, workspace SSO binding isolation + context requirement).
- **SF-91 residual** — `internal/services/ratelimit/redis_limiter_test.go` (9 tests) + `internal/services/webauthn/redis_store_test.go` (5 tests) on interface fakes; miniredis is **not** in go.mod, so the fake alternative was used.

**Test evidence (this wave's own suites, run 2026-11-07 dockerized `golang:1.27`)**

- `internal/handlers/login_enforcement_test.go`: TestLogin_TwoFactorRequired_Matrix (8 cases), TestLogin_EnrollmentMarkerInProfile, TestRefresh_RotateAndSingleUse, TestRefresh_ChainRotation, TestRefresh_UnknownOrGarbageToken, TestRefresh_LogoutRevokes, TestRefresh_DisabledServiceAnswers400.
- `internal/handlers/app_password_test.go`: TestAppPassword_CreateReturnsPlaintextOnce, TestAppPassword_ListHasNoCredentialMaterial, TestAppPassword_RevokeLifecycle, TestAppPassword_CrossUserIsolation, TestAppPassword_CreateRequiresName.
- Named battery template (`go build ./... && go test -count=1 -race ./internal/handlers/ ./internal/services/... ./internal/storage/... ./internal/cron/...`) was executed 3× this session; the build half is currently impossible as written (see `docs/node_modules` above), so the final run used `go build ./cmd/... ./internal/...` — every Go package — plus the test list exactly as named.
- Frontend: `npm --prefix web run type-check` PASS (exit 0); `npm --prefix web run test` 64/65 tests + 15/16 files pass.

**One in-scope fallout fix:** `internal/services/workspace/smtp_test.go` TestConnectionDriftPerField failed after the (already-landed, kept) SF-78 crypto hardening — the test never initializes crypto, so `Encrypt` refuses and the password column silently drops from the drift map (`internal/services/workspace/smtp.go:138-146`). Fixed by adding `crypto.Init(...)` to the test, the same repair pattern the crypto wave itself used in `internal/services/email/dkim_signer_test.go:46`. The test file was clean at HEAD; no one else owned it.

**Concurrent-wave noise (reported, not fixed):**

1. `internal/storage/migration/migration.go:79` referenced `models.UserPushSubscription` before the SF-89 wave's model file existed → transient build failure of `internal/storage/migration` + `internal/storage/repositories` during battery run 2; resolved itself when the sibling landed `internal/models/user_push_subscription.go` (13:22).
2. `internal/services/webpush` TestFanoutPublishesWorkspaceEventsToPushSubscriptions failed during battery run 4 (package created mid-run); `ok` on isolated re-run minutes later — sibling was mid-edit.
3. `web/src/api/admin.spec.ts` "every known-drift allowance is still drifted" FAILS as of this session's end: `/admin/settings/history` is registered in `admin_routes.go` (SF-100 sibling's in-flight work) but its KNOWN_DRIFT allowance has not been deleted. That spec file is the SF-100 wave's in-flight file — left untouched per the wave rules; **the frontend suite is not green through no fault of this wave's files**.

**Parity register:** records SF-04, SF-35, SF-37, SF-67, SF-91, CA-03 updated in place; post-freeze tally note appended (CA-03 → COMPLETE; SF-22/23/24 movement belongs to the P3-1 wave's records). The frozen tally table itself was not rewritten.

---

### 2026-11-07 — P3-3 ops/settings wave: RF-F091 To-dos → Done

| Field | Value |
| --- | --- |
| Producing run | `dwfrun-65ae252f-6257-4524-a0a2-b3cb715a2689` (P3-3 ops/settings completer wave, continuing the interrupted wave's in-flight files) |
| HEAD audited | `02666cc` — "Stalwart parity register: verification complete (123/123 records, tally frozen)" — unchanged during the wave; all wave output is uncommitted working-tree content on top of it |
| Working tree at writing | Heavily dirty: concurrent sibling waves (P3-1 inbound verification, OB-1 MTA, P3-4 identity/security, SF-78 crypto, SF-89 web push, docs site) land uncommitted files in the same session; `docs/node_modules` appeared mid-session (05:48) from the docs wave's npm install |
| Test gate | **PASS** — the literal named battery `go build ./... && go test -count=1 -race ./internal/handlers/ ./internal/services/... ./internal/storage/... ./internal/cron/...`, dockerized `golang:1.27` (`-m 2500m`, `GOFLAGS=-p=2`), 2026-11-07: build half exit 0, test half exit 0 with 36 packages `ok` and zero FAIL (`internal/handlers` 179.7s under `-race`). This is the full battery at the scale named — the earlier same-session runs that tripped over `docs/node_modules` mid-install ("no Go files in /app/docs/node_modules/@docusaurus/...", then "…/web/node_modules/grapesjs/...") were concurrent-wave npm churn, and the settled tree passes as written. Also run: route-contract `go test ./internal/routes/ -run TestAPIRouteContract -count=1` ok (after `-update` regeneration); `npm --prefix web run type-check` exit 0; `npm --prefix web run test` 16/16 files, 65/65 tests pass |
| Done | **76** (+1: `RF-F091`) |
| In Progress | **9** — unchanged (`RF-F005`, `RF-F014`, `RF-F015`, `RF-F026`, `RF-F027`, `RF-F028`, `RF-F053`, `RF-F081`, `RF-F082`) |
| In Review | **0** |
| To-dos | **20** (−1: `RF-F091`) |
| Total | 105 (`RF-F001`–`RF-F105`); 76 + 9 + 0 + 20 reconciles against the last reconciled view (P3-4 entry) plus this entry's single move. |
| Delta vs previous entry | `RF-F091` To-dos → Done |

**Scope landed by this wave (all uncommitted at `02666cc`; the interrupted wave's partial files were completed, not rewritten)**

- **GAP-13 / RF-F091** — security-events retention: `RetentionCleanupJob.SetSecurityEventRepo` + purge pass under `security_events_retention_days` (provider default 90, 0 = keep forever) at `internal/cron/jobs/retention.go:424-433`, wired `cmd/posta/server.go:829-832`; seeder default aligned 30 → 90 (`internal/services/seeder/settings.go:98`); tests `internal/cron/jobs/retention_security_test.go` (TestRetentionPurgesSecurityEventsPastDefaultWindow, TestRetentionSecurityEventsZeroKeepsForever, TestRetentionSecurityEventsCustomWindow, TestRetentionSecurityEventsDeleteIsParameterized). Residual (documented, not silently omitted): the security-rules settings surface (`internal/handlers/security_handler.go:489`) still displays default 30 with Min 1, so `0 = keep forever` is reachable via PUT `/admin/settings` or the DB but not via the security-rules upsert; the enforcement side (provider + cron) fully implements the spec.
- **SF-100** — settings history + rollback: additive `setting_history` table (`internal/models/setting_history.go`, migration registration `internal/storage/migration/migration.go:70`), `internal/storage/repositories/setting_history_repo.go` (append-only), per-key diff capture + provider-cache invalidation through the single write path `applySettingWrites` (`internal/handlers/setting_handler.go:137-208`, `settings.Provider.Invalidate` at `internal/services/settings/provider.go:77-86`); `GET /admin/settings/history` (paged, admin-gated) + `POST /admin/settings/history/rollback` (restores through the normal setter → new history entry + cache invalidation; refuses creation entries and reserved keys) in `internal/handlers/setting_history_handler.go`; wired `internal/routes/routes.go` (`SetHistoryRepo` + `SetSettingsProvider`); routes registered `internal/routes/admin_routes.go` (rollback path `/settings/history/rollback` matches the existing frontend client `web/src/api/settings.ts:24-34` and the already-built Settings.vue history UI); golden `docs/implementation/api_routes.txt` regenerated; the two matching `KNOWN_DRIFT` allowances in `web/src/api/admin.spec.ts` deleted per that spec's own stale-entry rule (resolving the drift the P3-4 entry reported). Tests: `internal/handlers/setting_history_handler_test.go` (8 named tests).
- **SF-104** — DKIM auto-retire cron: `internal/cron/jobs/dkim_retire.go` (grace `dkim_retire_grace_days`, default 14, 0 = next run, negative disables; requires replacement Active key with `dns_state == published`; audit `domain.dkim_key_auto_retired`; auto-generation of new keys out of scope), registered `cmd/posta/server.go:910-916`; seeder `internal/services/seeder/settings.go:103`; tests `internal/cron/jobs/dkim_retire_test.go` (7 named tests).
- **SF-94** — log sinks: `internal/logging/logging.go` (`POSTA_LOG_SINKS` stdout\|file, `POSTA_LOG_FILE`; stdout tee → rotating file, 10 MiB × 4 backups, policy in the package doc) wired in `cmd/posta/server.go:88-97` (pre-logger, sink closed on shutdown `:978-980`) and now also in `runWorker` (`cmd/posta/worker.go` — both `cmd/posta` entrypoints); tests `internal/logging/logging_test.go` (7 named tests).

**Reported, not fixed (other waves' in-flight files):** during this wave's session `go build ./...` transiently failed twice on gitignored `node_modules` subtrees being written by concurrent npm installs (`docs/node_modules` created 05:48 mid-session; earlier `web/node_modules/grapesjs/...`); the final battery run on the settled tree passes as written. No other wave's file was modified except the one in-scope surface this wave owns: the `admin.spec.ts` KNOWN_DRIFT pair this wave's routes made stale.

**Parity register:** SF-94, SF-100, SF-104 records updated in place (wave-tagged); the frozen tally table itself was not rewritten.
---

### 2026-11-07 — P3-5 misc wave: RF-F102 To-dos → Done, RF-F104 To-dos → In Progress (duplication half landed)

| Field | Value |
| --- | --- |
| Producing run | `dwfrun-65ae252f-6257-4524-a0a2-b3cb715a2689` (actor 4 — P3-5 misc wave: CA-02, CA-06, SF-82, SF-90, SF-89) |
| HEAD audited | `02666cc` — "Stalwart parity register: verification complete (123/123 records, tally frozen)" — unchanged during the wave; all wave output is uncommitted working-tree content on top of it |
| Working tree at writing | Heavily dirty with concurrent sibling waves' in-flight files (P3-4 identity wave, SF-100, P3-1, OB-1) — this wave's diff is limited to the files enumerated below |
| Test gate | **PASS** — the named battery `go build ./... && go test -count=1 -race ./internal/handlers/ ./internal/services/... ./internal/storage/... ./internal/cron/...`, dockerized `golang:1.27` (2026-11-07): build succeeded; every package `ok` (`internal/handlers` 132.7s; `internal/services/webpush` 1.15s; `internal/services/webhook` 10.4s; `internal/services/email` 5.4s; `internal/cron/jobs` 1.9s; zero FAIL). Deviation recorded precisely: the container OOM'd on the first attempt under the 2500m cap, so the passing run added `-e GOMEMLIMIT=1800MiB -e GOGC=50` (GC budgeting only; command and test scope otherwise exactly as named). `go test ./internal/routes/ -count=1` (contract test, outside the battery's named trees) also PASS after the golden regeneration. Frontend: `npm --prefix web run type-check` PASS; `npm --prefix web run test` **16/16 files, 65/65 tests PASS**. |
| Done | **76** (+1: `RF-F102`) |
| In Progress | **10** (+1: `RF-F104` — duplication half landed, failed-recipient retry residual) |
| In Review | **0** |
| To-dos | **19** (−2) |
| Total | 105 (`RF-F001`–`RF-F105`); 76 + 10 + 0 + 19 reconciles. Sibling waves are moving their own statuses in the same tree; this entry records only this wave's two moves. |
| Delta vs previous entry | `RF-F102` To-dos → Done; `RF-F104` To-dos → In Progress |

**Scope landed by this wave (all uncommitted at `02666cc`)**

- **CA-02** — dead surfaces wired or retired, per endpoint the option that serves the product: (1) `POST /campaigns/{id}/duplicate` **wired** — `campaignsApi.duplicate()` (`web/src/api/campaigns.ts`) + Duplicate action in `web/src/views/campaigns/CampaignDetail.vue` (`duplicateCampaign()`, editor-gated, navigates to the new draft). (2) `GET /emails/{id}/status` **wired** — `logsApi.getEmailStatus()` + `EmailStatusView` type, and MessageDetail.vue polls it every 4s (bounded to ~75 polls) while a send is queued/processing, updating the badge and refetching the timeline on a terminal status. (3) `GET /admin/api-keys`, `DELETE /admin/api-keys/{id}`, `GET /admin/emails` — **retired**: the dead client functions `listApiKeys`/`revokeApiKey`/`listEmails` and their now-unused type imports removed from `web/src/api/admin.ts` (no backend route exists; key admin lives in workspace scope per RF-F010); the three matching KNOWN_DRIFT allowances removed from `web/src/api/admin.spec.ts` (the spec's own fix rule). Choice rationale recorded in the `web/src/api/admin.ts` comment and BACKLOG.md.
- **CA-06** — dev fallback broker: the fallback exists (`internal/services/email/service.go` `sendSync` from the no-enqueuer branch, `:916` area); this wave named it in code (CA-06 comment documenting the dev-only reachability and why a production enqueue failure deliberately does NOT degrade to sync) and proved the pipeline with tests: `internal/services/email/dev_fallback_test.go` — `TestSendSyncDeliversThroughFallbackPipelineWhenNoProducerWired` (SMTP send + status UPDATE + email_events history INSERT over a fake gorm driver, via a new `messageSender` seam on `Service`) and `TestSendSyncFailureMarksEmailFailedThroughFallbackPipeline`. Note recorded honestly: for brand-new sends in dev the store-without-send short-circuit (`:867`) returns first; the sync pipeline is what the retry path and enqueuer-less flows use.
- **SF-82** — restore verification: `internal/cron/jobs/backup_restore_test.go` `TestBackupRestoreRunbookRoundTrip` executes the runbook end to end on the DB-tier pattern (real Postgres via `TEST_DATABASE_DSN`, skips honestly when the DB or pg_dump/psql are absent): seeds a probe table, runs the REAL pg_dump-backed `BackupJob.Run`, validates dump structure (gzip magic, plain-format header, CREATE TABLE), restores into a scratch database via `psql -v ON_ERROR_STOP=1`, and reads the marker row back. **Executed for real 2026-11-07 against a live PostgreSQL 17 container (postgres:17-alpine, pg_dump 17.11): PASS (6.35s)** — one fix during the run (pg_dump schema-qualifies identifiers; assertion now checks `CREATE TABLE` + probe name). The battery run skips it (golang:1.27 has no Postgres client) — documented, not silent.
- **SF-90** — webhook batching + pending cap (default off): `internal/services/webhook/batch.go` — optional per-endpoint batch buffers (`Config.BatchEnabled` from `POSTA_WEBHOOK_BATCH_ENABLED` default false; flush at `BatchMaxEvents` default 25 or `BatchFlushInterval` default 5s; batch body = JSON array of the per-event payloads, HMAC-signed over the batch bytes; one `event=batch` delivery row per flush) and a per-endpoint pending cap (`MaxPendingEvents` default 1000, drop-oldest with a warning event + `OnEventsDropped` → new `posta_webhook_events_dropped_total` metric in `internal/metrics/metrics.go`). `Close()` flushes pending synchronously. Per-event behavior untouched when off (pinned by `TestBatchingDisabledByDefaultDeliversPerEvent`). Tests: `internal/services/webhook/dispatcher_batch_test.go` (6 tests: default-off, coalescing, interval flush, batch signature, cap drop-oldest + callback, per-endpoint independence). Wired in `cmd/posta/server.go` (`webhookConfig` + `OnEventsDropped`) and `internal/routes/routes.go`; env vars documented in `docs/docs/getting-started/configuration.md`.
- **SF-89** — Web Push (RFC 8030/8291/8292 VAPID), minimal: `internal/services/webpush/` (stdlib crypto only — ECDH P-256, hand-rolled HKDF per RFC 5869, AES-128-GCM, ECDSA ES256 VAPID JWT): `encrypt.go` (RFC 8291 aes128gcm), `vapid.go` (RFC 8292 + `GenerateKeys` for operators + public/private cross-check), `service.go` (RFC 8030 POST with TTL/aes128gcm/vapid headers, 404/410 → `ErrGone`, plus the `Fanout` realtime.Bus decorator). Model `models/user_push_subscription.go` + additive migration (`internal/storage/migration/migration.go`, `UserPushSubscription` row) + `user_push_subscription_repo.go` (user-scoped CRUD; `FindForWorkspace` joins workspace_members). Handler `internal/handlers/webpush_handler.go`: `GET /users/me/push/config` (capability probe), `POST/GET /users/me/push/subscriptions`, `DELETE /users/me/push/subscriptions/{id}` (user_id-scoped delete) — registered in `internal/routes/user_routes.go`, golden regenerated (`docs/implementation/api_routes.txt` lines 32, 120-121, 288). Config `POSTA_WEB_PUSH_ENABLED` default false + VAPID keys/subject (`internal/config/config.go`). Fanout wired around the dashboard message bus in `internal/routes/routes.go` (message.* events only — documented minimal scope; other bus instances stay SSE-only). Frontend: `web/src/api/webpush.ts`, `web/public/push-sw.js` (service worker), Profile.vue "Browser Notifications" card (permission + PushManager.subscribe + server upsert + remove). Tests: `service_test.go` (encrypt/decrypt round trip from the subscription side, ephemeral uniqueness, VAPID JWT verification against the public key, key-pair mismatch rejection, end-to-end Send over httptest incl. 410 gone) and `fanout_test.go` (message event pushed, email event not, gone subscription deleted).
- **Docs** — `docs/docs/getting-started/configuration.md` gained the SF-90 batch/cap rows and a Browser Push section; CA-02/CA-06/SF-82/SF-90/SF-89 recorded in README roadmap, BACKLOG.md (RF-F102 closed, RF-F104 residual moved), FEATURE_INVENTORY.md.

**Honest limitations (not silently omitted)**

1. SF-89 push fan-out covers only events published through the dashboard message bus instance (the `message.*` stream). Events published via other realtime bus instances (thread router, worker process) do not trigger push — the realtime package has no process-wide sink; widening this is future scope.
2. SF-89 push bodies are refetch hints (`{type, workspace_id, message}`), not rich notification payloads; the service worker renders the type as the body.
3. SF-82's live-run evidence came from an ad-hoc side Postgres (postgres:17-alpine); the test ships in the tree and skips in the standard battery because golang:1.27 ships neither Postgres nor its client.
4. The parity register (`docs/audits/STALWART_FEATURE_PARITY_AUDIT.md`) is frozen; this wave did NOT rewrite its records. The register's CA-02/CA-06/SF-82/SF-89/SF-90 rows remain as frozen at `02666cc`; this entry and the RF-F registry view are the status of record for the landed work.
