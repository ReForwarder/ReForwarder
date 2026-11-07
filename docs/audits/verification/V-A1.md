# V-A1 — Verification of Category A records SF-01..SF-11 (the reference platform parity audit)

Auditor: V-A1 · Date: 2026-11-07 · Method: static code reading only (host has no Go
toolchain; no builds/tests/docker were run — the battery runs centrally). Every claim
below cites `path:line` read in this session against the working tree. Absence claims
were checked by repo-wide `grep` over `internal/` and `cmd/` (`*.go`); a grep cannot
prove absence with certainty, but multiple independent patterns were used per record.
One library-level fact is inferred rather than read: go-smtp v0.25.0's default EHLO
advertisements (module cache absent on this host) — flagged inline where it matters.

Sources of requirements: `docs/research/reference-platform-feature-inventory.md` §A (SF-01..SF-11
rows, lines 15–27); ledger `docs/research/reference-platform-feature-gap-analysis.md` §4a; the
records under verification are `docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md` "Category
A" (SF-01 at line 54 through SF-11 at line 160). ADR scope headers read at
`docs/architecture/ADR-001-outbound-mta.md` (header line 5: covers SF-01, SF-02,
SF-03 (SMTP MSA), SF-05, SF-06, SF-12, SF-15*, SF-17, SF-21, SF-29, SF-30 + MTA-side
parts of SF-07/09/16/35/43/108) and `docs/architecture/ADR-002-inbound-smtp-routing.md`
(header line 4: covers SF-11, SF-14, SF-26, SF-27, SF-32, SF-45/48, SF-51, CA-05).

Summary of verdicts:

| ID | Audit pre-seed | V-A1 verdict |
|---|---|---|
| SF-01 | BLOCKED BY ADR-001 (OB-1) | **BLOCKED BY ADR-001 (OB-1)** — confirmed; inbound receiver command coverage now pinned as partial evidence |
| SF-02 | BLOCKED BY ADR-001 (OB-1) | **BLOCKED BY ADR-001 (OB-1)** — confirmed; advertisement set verified (SMTPUTF8/STARTTLS only, plus library defaults) |
| SF-03 | PARTIAL — SMTP MSA BLOCKED BY ADR-001 (OB-1); HTTP analog COMPLETE | **PARTIAL** — confirmed in both halves |
| SF-04 | COMPLETE (verify) | **COMPLETE** — confirmed; one field correction (API — → Verified) and one test-coverage gap recorded (allowlist + auth-gate untested) |
| SF-05 | BLOCKED BY ADR-001 (OB-1), relay half COMPLETE | **BLOCKED BY ADR-001 (OB-1)** — confirmed; relay half fully verified end-to-end |
| SF-06 | BLOCKED BY ADR-001 (OB-1) | **BLOCKED BY ADR-001 (OB-1)** — confirmed absent |
| SF-07 | PARTIAL — pools BLOCKED BY ADR-001 (OB-6 IP pools) | **PARTIAL** — confirmed; phase citation corrected OB-6 → OB-1 per ADR-001 covers line |
| SF-08 | PARTIAL — remainder PLANNED (Phase 3) | **PARTIAL** — confirmed; R-26/R-27 and admin surface verified; remainder unchanged |
| SF-09 | PARTIAL — SMTP-protocol params BLOCKED BY ADR-001 (OB-1) | **PARTIAL** — confirmed; app-level scheduling fully verified incl. cron firing |
| SF-10 | MISSING → PLANNED (Phase 3) | **PLANNED (Phase 3)** — confirmed (currently MISSING; relay-compatible per programme rule) |
| SF-11 | BLOCKED BY ADR-002 (OB-2) | **BLOCKED BY ADR-002 (OB-2)** — confirmed; ledger N/V resolved as claimed |

---

### SF-01 — Full ESMTP/LMTP state machine

Final status: BLOCKED BY ADR-001 (OB-1)

Fields: Backend Missing (MTA) · Frontend — · DB — · API — · Security — · Tests Missing · Docs Partial (ADR-001 plans the surface)

Implementation: No first-party state machine exists. Two library-driven receivers exist, both feature-gated default-off: (1) inbound receiver — `internal/services/inbound/smtp_server.go` (backend `NewBackend` :61, session implements only `Reset` :104, `Logout` :110, `Mail` :115, `Rcpt` :129, `Data` :153; STARTTLS advertised only when `cfg.TLSMode == "starttls"` :205-217), wired at `cmd/posta/server.go:205` behind `cfg.InboundEnabled` (server.go:204; `POSTA_INBOUND_ENABLED` default false, `internal/config/config.go:418`), also disabled in DevMode; (2) relay receiver — `internal/services/smtprelay/backend.go` (AUTH PLAIN only :103-139; `Mail`/`Rcpt`/`Data` return `smtp.ErrAuthRequired` without a credential :154-156, :169-171, :186-188), wired at `server.go:226` behind `cfg.SMTPRelayEnabled` (default false, config.go:441). Command state machine itself is `emersion/go-smtp` v0.25.0 (`go.mod:12`), not first-party: EHLO/HELO/MAIL/RCPT/DATA/RSET/NOOP/QUIT/STARTTLS come from the library. Absent (grep over `internal/`+`cmd/`): LMTP (zero hits), VRFY/EXPN (no handlers; library answers 502), inbound AUTH (the inbound session implements no `AuthMechanisms`/`Auth`; only the relay backend does), BDAT/CHUNKING. Outbound is a relay client only — `net/smtp` conversation at `internal/services/email/smtp_sender.go:361-395`.

Evidence: read `internal/services/inbound/smtp_server.go` (230 lines) and `internal/services/smtprelay/backend.go` (250 lines) in full; wiring `cmd/posta/server.go:176-233,408-529`; gates `internal/config/config.go:418,425,441`; go.mod:12. Grep patterns `LMTP|lmtp`, `VRFY|EXPN`, `AuthPlain|AuthSession` over `internal/` returned zero relevant hits. No tests touch the inbound SMTP backend/session (no `*_test.go` references `inbound.Backend` or its session — `internal/services/inbound/` has tests only for dmarc.go, parser.go, ratelimit.go), so Tests: Missing is accurate for the protocol surface.

Notes: The pre-seed's evidence task ("verify the inbound receiver's command coverage as partial evidence") is now done: the receiver covers the core command set through go-smtp but has no AUTH, no VRFY/EXPN, no LMTP — partial evidence, not a partial implementation of SF-01 itself. The MTA-grade state machine (first-party, LMTP, BDAT budgets, ETRN refusal semantics) stays BLOCKED BY ADR-001 (OB-1); ADR-001's covers line names SF-01 explicitly (`docs/architecture/ADR-001-outbound-mta.md` line 5).

### SF-02 — ESMTP extension advertisement

Final status: BLOCKED BY ADR-001 (OB-1)

Fields: Backend Missing · Frontend — · DB — · API — · Security — · Tests Missing · Docs Partial (ADR-001)

Implementation: Inbound advertisement: `srv.EnableSMTPUTF8 = true` (`internal/services/inbound/smtp_server.go:200`); STARTTLS advertised only when `cfg.TLSMode == "starttls"` (smtp_server.go:205-217; `POSTA_INBOUND_TLS_MODE` default `"none"`, config.go:425 — so default advertisement excludes STARTTLS); `srv.AllowInsecureAuth = true` (:199) but the inbound session implements no AUTH mechanism, so AUTH is not served on the inbound receiver. PIPELINING/8BITMIME/ENHANCEDSTATUSCODES are go-smtp library defaults (inferred from go.mod:12; library source not cached on this host — not verified by reading). Never advertised or handled (grep zero over `internal/`+`cmd/`): CHUNKING/BDAT, DSN, REQUIRETLS, FUTURERELEASE, DELIVERBY, MT-PRIORITY, NO-SOLICITING. Outbound client negotiates nothing beyond STARTTLS/AUTH: `net/smtp` with `client.Mail(from)` / `client.Rcpt(rcpt)` called without parameter options (`internal/services/email/smtp_sender.go:368-376`) — no 8BITMIME/SMTPUTF8/DSN params on the envelope.

Evidence: read smtp_server.go:189-222 (`NewSMTPServer` config), smtp_sender.go:361-395 (`sendViaClient`); config.go:425; grep patterns `PIPELINING|CHUNKING|8BITMIME|SMTPUTF8|REQUIRETLS|FUTURERELEASE|DELIVERBY|MT-PRIORITY` — only SMTPUTF8 hits (smtp_server.go:200). Tests: none cover the advertisement surface (same gap as SF-01).

Notes: Pre-seed confirmed: the receiver advertises library defaults plus repo-enabled SMTPUTF8 and config-gated STARTTLS; the outbound client negotiates none. Every missing extension is OB-1 scope (ADR-001 covers line names SF-02). The PIPELINING/8BITMIME library-default claim is marked inferred, not read.

### SF-03 — Submission (MSA) with delivery-status writeback

Final status: PARTIAL — SMTP MSA BLOCKED BY ADR-001 (OB-1); HTTP analog COMPLETE

Fields: Backend Partial (HTTP submission analog; no SMTP MSA) · Frontend — · DB Verified (`email_events`) · API Verified (standard envelope, auth + scope) · Security Verified · Tests Verified (layer-level; no end-to-end HTTP handler test) · Docs Partial

Implementation: HTTP analog route→handler→service→queue→worker→writeback chain, all traced: `POST /emails/send` `internal/routes/auth_routes.go:194` (also `/emails/send-template` :224, `/emails/batch` :239, group `apiAuth` built at :168 as `r.mw.apiKey` + `RequireScope(models.ScopeSend)`) → `EmailHandler.Send` `internal/handlers/email_handler.go:130` (`requireEdit` permission gate :131) → `Service.Send` `internal/services/email/service.go:651` (persists the email row, `EnqueueEmailSend`/`EnqueueEmailSendAt` :885-899 via the asynq producer `internal/worker/producer.go:56-101`, `asynq.ProcessIn` for `send_at` :119) → worker delivery `internal/worker/handler.go` (success `emailhistory.RecordSent` :398; failure `RecordFailed` :385, :428, :535; bounce `RecordBounced` :538) → `emailhistory.Recorder.Record` `internal/services/emailhistory/service.go:121-185` (inserts `models.EmailEvent` with `OnConflict DoNothing` dedup :183-185) → table `email_events` (`internal/models/email_event.go:62` TableName; registered in `internal/storage/migration/migration.go:72`). Sync failure paths also write events (`internal/services/email/service.go:872,889,903,971,992,1008`). No SMTP MSA listener exists: the relay listener (`smtprelay`) is for relayed mail via pre-provisioned credentials, not authenticated submission with writeback semantics beyond what it shares (it does route through the same `email.Service.Send` and therefore the same event writeback — but it is a separate feature, SF-04).

Evidence: Tests named: `internal/services/email/attachment_pipeline_test.go:96` `TestQueuedEmailKeepsRecoverableAttachment` and `:170` `TestQueuedEmailStoresAttachmentInBlobStore` (both call the real `svc.Send` queued path); `internal/services/email/smtp_sender_timeout_test.go:282` `TestSendAgainstWorkingRelay` (full SMTP conversation against a live fake relay) plus `:117`/`:212` failure-path tests; `internal/services/email/domain_verification_test.go:259-337` (send gating); `internal/middlewares/rate_limit_test.go:187` `TestIsAPIKeySendPath`, `:215` `TestAPIKeyRateLimitSubjectDistinctPerKey`; `internal/services/emailhistory/service_test.go:193-566` (12 Record/hook tests incl. dedup and panic safety); `internal/storage/repositories/email_event_repo_test.go:283-599`; `internal/worker/handler_test.go:38,69` (permanent-rejection). Gap: no `email_handler_test.go` — the HTTP handler layer itself is not directly tested.

Notes: Pre-seed confirmed in both directions. The delivery-status writeback requirement (MSA's distinguishing feature) is satisfied by the HTTP analog through `email_events`; the SMTP MSA port itself is absent and named in ADR-001's covers line ("SF-03 (SMTP MSA)") → OB-1. The pre-seed's field-level claims (DB/API/Security/Tests Verified) all check out; Tests carries the noted layer-level caveat.

### SF-04 — Relay control / open-relay prevention

Final status: COMPLETE

Fields: Backend Verified · Frontend — · DB Verified (`smtp_credentials`, migration.go:43) · API Verified (credential CRUD + revoke) · Security Verified · Tests Partial (credential layer tested; allowlist + structural gate untested) · Docs Partial

Implementation: Open-relay prevention is structural and verified at every gate: (1) relay listener — `internal/services/smtprelay/backend.go`: AUTH PLAIN only (:103-139) with credential lookup + `IsValid()` (:111-114), password verify (:116), per-credential IP allowlist `middlewares.IPAllowed(cred.AllowedIPs, ip)` (:124), and `smtp.ErrAuthRequired` returned from `Mail` (:154-156), `Rcpt` (:169-171), and `Data` (:186-188) when no credential is bound — unauthenticated peers cannot reach MAIL, so nothing can be relayed; (2) inbound receiver — `internal/services/inbound/smtp_server.go:129-146`: `Rcpt` rejects any recipient whose domain is not ownership-verified (`domainRepo.FindVerifiedByName`, 550 5.1.1 "relay not permitted"); (3) credentials — `internal/services/smtprelay/credential.go`: bcrypt for new hashes (:114-120), `subtle.ConstantTimeCompare` for legacy SHA-256 rows (:104-110), transparent rehash migration with an unchanged-hash guard (:84-97) (R-25); (4) security engine on both listeners — `security.NewSessionGuard` + `GuardNewSession` (`internal/services/security/smtp.go:63-101`, 421 on ban) at backend.go:80-84 and inbound smtp_server.go:84-88, auth-failure feeds `RecordAuthFailure` (backend.go:113,117,126); ban enforcement at TCP accept via `security.NewCountingListener` (`cmd/posta/server.go:474,522`); (5) credential management API — `internal/routes/workspace_resource_routes.go:219-267` (`POST/GET/DELETE /smtp-credentials`, `/revoke`), frontend `web/src/api/smtpRelay.ts` + `web/src/views/smtp-relay/SmtpCredentials.vue` (router `web/src/router/index.ts:59`).

Evidence: Tests named: `internal/services/smtprelay/credential_test.go:32` `TestHashPasswordBcrypt`, `:60` `TestVerifyPassword`, `:80` `TestVerifyPasswordLegacySHA256`, `:97` `TestVerifyPasswordMalformedHash`, `:108` `TestGenerateCredentialStoresBcrypt`; `credential_repo_test.go:319` `TestVerifyPasswordMigratesLegacyHash`, `:366` `TestVerifyPasswordDoesNotClobberConcurrentRotation`, `:399`, `:416`; `backend_test.go:29` `TestMapSendError`; `smtp_server_test.go:23` `TestSMTPRelaySTARTTLS`, `:64` `TestSMTPRelayTLSConfiguration`; allowlist predicate `internal/middlewares/middleware.go:423-439` read (empty allowlist permits all; exact IP + CIDR matching).

Notes: Two corrections/qualifications to the pre-seed. (a) Field correction: API is Verified, not "—" — full credential CRUD + revoke routes exist at workspace_resource_routes.go:219-267 with a wired UI. (b) Test-coverage gap: `middlewares.IPAllowed` has zero test coverage anywhere (grep over `*_test.go` for `IPAllowed|AllowedIPs` → no hits), and the structural auth-gate branches (`ErrAuthRequired`) and the inbound RCPT domain rejection have no direct unit tests; the security-critical credential verification path itself is thoroughly tested. The feature stands COMPLETE because the enforcement code is simple, fully traced, and the auth-material path is test-verified — the allowlist/gate tests are recorded here as a residual hardening item for the Phase-3 pool. Both listeners are feature-gated default-off (config.go:418,441), which does not weaken the open-relay posture (off = no listener at all).

### SF-05 — Routing strategies (Local / Mx / Relay via expressions)

Final status: BLOCKED BY ADR-001 (OB-1) — relay half COMPLETE

Fields: Backend Partial (relay-only selection) · Frontend Verified (relay server config UI) · DB Verified (`smtp_servers`) · API Verified (CRUD + connection test) · Security — · Tests Partial (config layer tested; selection policy untested) · Docs Partial

Implementation: The only routing that exists is relay-server selection: `Service.Send` resolves the per-workspace or per-user configured relay — `internal/services/email/service.go:960-965` (`smtpRepo.FindFirstByWorkspaceID(*workspaceID)` / `FindFirstByUserID(userID)`), re-resolved at retry time :1261-1265, with per-server `AllowedEmails` restriction applied :979-982 and `MaxRetries` honored :1267-1268. Config model `internal/models/smtp_server.go:44-66` (host/port/username/password/encryption/MaxRetries/AllowedEmails/Status/ValidationError). API: `internal/routes/workspace_resource_routes.go:648-716` (`POST/GET/PUT/DELETE /smtp-servers`, `POST /smtp-servers/{id}/test`). Frontend: `web/src/api/smtp.ts` (client against `/workspaces/current/smtp-servers`), router `web/src/router/index.ts:77-78`, views `web/src/views/smtp/SmtpServers.vue` + `SmtpServerDetail.vue`. DB: `&models.SMTPServer{}` registered in `internal/storage/migration/migration.go:52`. Absent: Local (mailbox ingest) and Mx (native MX) strategies, expression-driven strategy selection, per-recipient/per-(domain,route) grouping — grep for `LookupMX`/strategy selection in the delivery path returns nothing (MX code exists only in the address verifier, see SF-06).

Evidence: read service.go:960-1004 and :1261-1268; workspace_resource_routes.go:640-720; smtp_server.go model; migration.go:52; frontend client + router files. Tests: `internal/services/workspace/smtp_test.go:30` `TestNormalizeEncryption`, `:97-171` connection-drift/rotation tests cover the config layer; `internal/services/email/smtp_sender_test.go` + timeout tests cover the relay conversation; nothing pins the server-selection policy itself.

Notes: Pre-seed confirmed, with one field upgrade: Frontend is Verified (the pre-seed's "Partial (relay config UI)" understates a fully wired client+router+views surface for the relay half). The strategy engine (Local/Mx/Relay selected by expressions) is OB-1 scope per ADR-001's covers line (SF-05); the relay half being complete is exactly the pre-seed's reading.

### SF-06 — MX handling (preference, shuffle, null-MX, implicit MX, DNSSEC)

Final status: BLOCKED BY ADR-001 (OB-1)

Fields: Backend Missing · Frontend — · DB — · API — · Security — · Tests Missing (nothing to test) · Docs Partial (ADR-001 plans direct-to-MX)

Implementation: None in any delivery path. Repo-wide grep for `net.LookupMX|LookupMX|LookupNetIP` over `internal/`+`cmd/` (`*.go`, tests excluded) returns only `internal/services/verifier/verifier.go:244,256` — the address-verifiability check behind `POST /emails/verify` (route description at `internal/routes/auth_routes.go:213`), which consumes MX presence as a data point and never feeds delivery — plus the classifier's hard-fail phrase list (`internal/services/provider_events/classifier.go:319`, "no mx"/"mx record" as bounce-reason strings). No preference ordering, no shuffle, no null-MX handling, no implicit-MX fallback, no DNSSEC status — all absent by grep.

Evidence: the two grep patterns above; read verifier.go:244-270 to confirm the MX use is verification-only; ADR-001 body (header ~line 9) explicitly plans "resolving the recipient domain's MX hosts and delivering over ESMTP" as the OB-1 work.

Notes: Pre-seed confirmed (absent, blocked). The verifier's MX lookups must not be mistaken for delivery MX handling; SF-06 remains entirely OB-1 scope.

### SF-07 — Outbound relays (SASL, TLS modes, source-IP pools, pipelining)

Final status: PARTIAL — source-IP pools (and invalid-cert allowance, outbound pipelining) BLOCKED BY ADR-001 (OB-1)

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Verified · Docs Partial

Implementation: Configurable per-workspace/per-user relays exist end-to-end (see SF-05 for config/API/UI/DB citations). Protocol capabilities, all read in `internal/services/email/smtp_sender.go`: SASL PLAIN with LOGIN fallback for servers that do not advertise PLAIN (`autoAuth`/`newAuth` :250-293, selection tested); TLS modes none/STARTTLS/implicit-SSL (`dialSMTP` :210-248 — implicit TLS via `tls.DialWithDialer` :217, STARTTLS upgrade :241-246; standard certificate verification, no invalid-cert allowance option); R-22 timeouts/deadlines — dial timeout `DefaultDialTimeout` 15s (:94-96), process-wide setter (:109-116), overall session budget `dialTimeout × 4` applied post-dial (:231) and a fresh budget before DATA (:381); `TestConnection` verifies dial+AUTH (:186-203); DKIM signing hooked before send (:312-324, SF-20's R-02). Absent: per-relay source-IP pools (grep `LocalAddr|source_ip|bind.*interface` over the email/smtprelay services → zero), outbound pipelining (sequential per-recipient `client.Rcpt` loop :371-376; net/smtp does not pipeline), bearer-token SASL. Relay-server passwords are encrypted at rest (`internal/services/workspace/smtp.go:139` `crypto.Encrypt`, rotation-drift tests :136-161).

Evidence: Tests named: `smtp_sender_test.go:96` `TestAuthPrefersPlainWhenOffered`, `:112` `TestAuthUsesLoginWhenPlainNotOffered`, `:129` `TestAuthLoginRefusesUnencryptedConnection`; `smtp_sender_timeout_test.go:43` `TestDialTimeoutDefaultAndSetter`, `:117` `TestSendFailsAgainstBlackHoledRelay`, `:170` `TestSendImplicitTLSFailsAgainstBlackHoledRelay`, `:212` `TestSendFailsWhenRelayStallsMidConversation`, `:282` `TestSendAgainstWorkingRelay`; `internal/services/workspace/smtp_test.go:30-171`; `internal/services/smtprelay/smtp_server_test.go:23,64`. Absence: the two grep patterns above returned nothing.

Notes: Pre-seed PARTIAL confirmed; backend Partial, Security (bcrypt relay credentials, encrypted config passwords), Tests all check out. One correction: the pre-seed gates the missing pools with "(OB-6 IP pools)", but ADR-001's covers line assigns "MTA-side parts of SF-07" to OB-1, as does the audit's own SF↔phase crosswalk (line 43). The remaining requirements (source-IP pools, invalid-cert allowance, pipelining) are therefore BLOCKED BY ADR-001 (OB-1), not OB-6. This correction changes only the phase citation, not the status.

### SF-08 — Persistent multi-queue manager (per-recipient retry/notify schedules, expiry, event-locked delivery)

Final status: PARTIAL — remaining (per-recipient retry/notify schedules, message expiry by TTL/attempt count) PLANNED (Phase 3)

Fields: Backend Partial · Frontend Verified (R-03 queue admin) · DB Verified (asynq/Redis + claim SQL) · API Verified (queue admin + scheduled enqueue) · Security Verified · Tests Verified · Docs Partial

Implementation: Durable multi-queue: asynq over Redis (`internal/worker/producer.go:33-42` client/inspector; enqueue with `asynq.Queue(queue)`, `asynq.MaxRetry`, unique `asynq.TaskID` with `ErrTaskIDConflict` handling :56-67, :97-101, :117-126); named queues Transactional/Bulk/Low (`internal/worker/tasks.go:39-41`, `internal/queues`), plus campaign start/batch queues (:140-142). Event-locked delivery analog (R-26): batch claims with `FOR UPDATE SKIP LOCKED` (`internal/storage/repositories/campaign_message_repo.go:83`) and per-message pending→queued CAS `ClaimForSend` (:96-105, "can never double-claim"); the dedup TaskID prevents duplicate enqueue. Bounded retry sweeps (R-27): batch-limited, oldest-first sweeps with a documented unbounded escape hatch (`internal/worker/retry_bound_test.go:44-49,478,523`); per-server MaxRetries honored at retry scheduling (`internal/services/email/service.go:1261-1268`). Admin surface (R-03): `internal/handlers/admin_queue_handler.go` (per-state listings :64-80, GetQueueInfo :60, cancel/run/archive :84-92, :325-341) with routes `internal/routes/admin_routes.go:657-713` (`/admin/queues`, `/admin/queues/{queue}/tasks`, `.../cancel|run|archive`); frontend `web/src/api/admin.ts:130-146` + `web/src/views/admin/Jobs.vue`.

Evidence: Tests named: `internal/worker/atomic_claim_test.go:513` `TestClaimForProcessingAllowsExactlyOneClaim`, `:550` `TestClaimForRetryClaimsOnceAndIncrementsOnce`, `:587` `TestClaimForSendAllowsExactlyOneClaim`, `:609` `TestFindPendingByCampaignUsesSkipLockedBatchClaim`, `:654` `TestEmailSendHandlerDuplicateDeliverySendsOnce`; `internal/worker/retry_bound_test.go:478` `TestRetrySweepBoundedBatch`, `:523` `TestRetrySweepZeroLimitUnbounded`; `internal/handlers/admin_queue_test.go` (endpoint-level queue admin tests, per the audit's R-03 note); `internal/cron/jobs/queue_reconcile_test.go`. Absence: grep `asynq.Retention|Retention(` over `internal/worker` → zero (no task TTL/expiry configuration); no per-recipient retry/notify schedule code (retry policy is a single MaxRetry + sweep interval, not the reference platform's configurable interval lists).

Notes: Pre-seed PARTIAL confirmed and the two corrections it anticipated both verified: R-26 (CAS + SKIP LOCKED, exactly-once claim tests) and R-27 (bounded sweeps) are real and test-named; the admin queue surface is fully wired API+UI (pre-seed's Frontend/API "Partial" upgraded to Verified — the R-03 surface is complete; the not-yet-built per-recipient schedules/expiry naturally have no UI/API and are counted in the remainder, not as broken wiring). The remainder (retry/notify schedules, expiry) is application-level and relay-compatible → PLANNED (Phase 3), matching the pre-seed.

### SF-09 — Future release (HOLDFOR/HOLDUNTIL), DELIVERBY, MT-PRIORITY, DSN params

Final status: PARTIAL — SMTP-protocol params BLOCKED BY ADR-001 (OB-1)

Fields: Backend Verified (application-level scheduling) · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Verified · Docs Verified

Implementation: Application-level deferred delivery exists at two layers. (1) Per-message: `SendRequest.SendAt` (`internal/services/email/service.go:356`, `json:"send_at"`) → when in the future, persisted to the email row and enqueued with a delay (`service.go:881-885`: `em.ScheduledAt = req.SendAt; s.enqueuer.EnqueueEmailSendAt(...)` → `internal/worker/producer.go:109-126` `asynq.ProcessIn(delay)` with the same TaskID dedup). (2) Campaigns: `CampaignRepository.FindScheduledReady` (`internal/storage/repositories/campaign_repo.go:152-154`, `status = scheduled AND scheduled_at <= now`) driven by the scheduler cron `internal/cron/jobs/campaign_scheduler.go:82` (R-01 fixed the dead path), with claim/conflict/rollback semantics. Frontend: `scheduled_at` on the campaign create/update payloads (`web/src/api/campaigns.ts:52,75`) and email types (`web/src/api/types.ts:71,1227`). Absent (SMTP-protocol level): grep `HOLDFOR|HOLDUNTIL|DELIVERBY|MT-PRIORITY|ORCPT|ENVID|NOTIFY=` over `internal/` → zero; the outbound client (SF-02) sends no DSN/priority parameters.

Evidence: Tests named: `internal/cron/jobs/campaign_scheduler_test.go:497` `TestSchedulerClaimsOnlyDueScheduledCampaigns`, `:528` `TestSchedulerClaimConflictSkipsWithoutEnqueue`, `:555` `TestSchedulerEnqueueFailureRollsBackToScheduled`, `:590` `TestSchedulerIgnoresPausedAndCancelledCampaigns`; `internal/worker/campaign_timezone_test.go` (timezone-aware sending, R-09); the delayed-enqueue path is exercised through the producer contract (`EnqueueEmailSendAt` interface at service.go:57). Docs: user-facing scheduling documented in `docs/docs/campaigns/sending.md` and `docs/docs/campaigns/overview.md` (grep hits, read).

Notes: Pre-seed PARTIAL confirmed with all Verified fields re-verified. The SMTP-protocol parameters (HOLDFOR/HOLDUNTIL, DELIVERBY, MT-PRIORITY, DSN NOTIFY/ORCPT/RET/ENVID) require the OB-1 MTA foundation — ADR-001's covers line assigns the MTA-side part of SF-09 to OB-1; the application-level half stays as delivered.

### SF-10 — Full DSN generation (RFC 3464), double-bounce, signed DSNs

Final status: PLANNED (Phase 3) — currently MISSING

Fields: Backend Missing (RFC 3464 DSN generation; delivery-status event truth exists) · Frontend — · DB Verified (bounce substrate: `email_events` + bounce/suppression records) · API — (the bounce webhook is provider-facing ingest, not a DSN surface) · Security — · Tests Missing (no DSN tests; nothing to test yet) · Docs Missing

Implementation: DSN generation is absent — repo-wide grep for `RFC 3464|multipart/report|3464|delivery-status notification` over `internal/`+`cmd/` returns only PostgreSQL connection DSNs (`internal/storage/postgres.go:34-35`, `internal/config/config.go:246-248`) and an asynq payload-snippet field; no DSN message construction, no double-bounce handling, no DSN signing. What exists instead is delivery-truth by provider events (R-04, the documented substitute): provider-event ingestion service wired at `internal/routes/routes.go:536-545` (`provider_events.NewIngestionService` + per-provider HMAC secrets), writing normalized events into `email_events` through the shared recorder (`internal/services/provider_events/service.go:230-260` — `Record` call at :253, `recordPermanentBounce` :259+ running the full hard-bounce chain: bounce record, suppression upsert, subscriber marking, campaign-message update, bounced timeline event); dedicated bounce webhook `internal/handlers/bounce_webhook_handler.go:83` (`HandleBounce` with payload fingerprint dedup :182); 14-class bounce classifier `internal/services/provider_events/classifier.go` (R-05). Storage substrate registered: `email_events` + `incoming_webhook_events` (`internal/storage/migration/migration.go:72-73`).

Evidence: read provider_events/service.go:230-260 (write path) and bounce_webhook_handler.go:83-182; the grep patterns above; migration.go:72-73. Tests named (substrate, not DSN): `internal/services/provider_events/provider_events_test.go:980` (ingestion service with recorder), `internal/worker/classification_test.go` (classification), `internal/handlers/provider_event_handler_test.go`. No test names exist for DSN generation — Tests: Missing.

Notes: Pre-seed "MISSING → PLANNED (Phase 3)" matches the programme rule that relay-compatible work items (DSN generation explicitly named in the task) are PLANNED (Phase 3): a generated DSN would exit through the existing relay path (smtp_sender + DKIM signer `internal/services/email/dkim_signer.go` covers signing) with no ADR dependency. Remaining requirements once built: RFC 3464 multipart/report construction incl. delayed/failed DSNs, double-bounce handling, signed DSNs, DKIM2-aware DSN forwarding (the last two are OB-6-adjacent, SF-28). DB field upgraded from the pre-seed's "Partial" to Verified: the bounce substrate (timeline, bounce records, suppression) is real and migrated; a DSN feature adds message generation, not storage.

### SF-11 — Forwarding chain (sub-addressing, catch-all, aliases, list expansion)

Final status: BLOCKED BY ADR-002 (OB-2)

Fields: Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing (nothing to test) · Docs Partial (ADR-002 covers SF-11)

Implementation: No forwarding/auto-forwarding exists anywhere — the ledger's N/V resolution is confirmed by code. Repo-wide grep for "forward" over `internal/services/inbound` returns only `internal/services/inbound/service.go:128-145`: `models.InboundStatusForwarded` is an internal status meaning a thread reply was routed onto its message (via the messages thread router), not mail forwarded to a user address. RCPT handling (the pre-seed's evidence task): `internal/services/inbound/smtp_server.go:129-146` — `Rcpt` accepts a recipient only when its domain passes `domainRepo.FindVerifiedByName(domain)` and answers `550 5.1.1 "relay not permitted"` otherwise; there is no alias table, no catch-all resolution, no directory-recipient lookup, no list expansion behind it. The only sub-address-shaped mechanism is platform-internal reply addressing: `msg+<token>@POSTA_MESSAGES_INBOUND_DOMAIN` parsed by `ParseThreadAddress` (`internal/services/messages/threads.go:75-96`, router built at :66, wired `cmd/posta/server.go:387-397`) — it routes a reply to a message thread; it performs no forwarding and no user-level sub-addressing semantics. The relay backend's `Rcpt` accepts any address for authenticated submitters (`internal/services/smtprelay/backend.go:167-178`) — that is authenticated submission (SF-04), not inbound forwarding.

Evidence: the grep above; read inbound/smtp_server.go:127-146 (RCPT), service.go:120-150 (thread-forwarded status), messages/threads.go:66-96 (address parser), server.go:383-397 (router wiring). No catch-all/alias fields exist on the domain model used by the receiver (only `FindVerifiedByName` is consulted at RCPT).

Notes: Pre-seed BLOCKED BY ADR-002 (OB-2) confirmed; the record's claim "no forwarding/auto-forwarding exists (confirmed by prior audits)" re-verified directly at the RCPT layer rather than trusting prior audits. ADR-002's covers line names SF-11 (`docs/architecture/ADR-002-inbound-smtp-routing.md` line 4). Sub-addressing/catch-all/aliases/list expansion are all OB-2 inbound-routing scope.

---

## Verification limitations (honesty statement)

- No Go toolchain on this host: all verdicts rest on code reading and grep; no build/test/docker was run (per the task, the battery runs centrally). Test "evidence" means the named test functions exist at the cited `file:line` and exercise the cited behavior — not that they were executed this session.
- go-smtp v0.25.0's default EHLO advertisement set (PIPELINING/8BITMIME/ENHANCEDSTATUSCODES) is inferred from the declared dependency (`go.mod:12`) because the module source is not cached on this machine; the repo-controlled advertisement facts (SMTPUTF8, STARTTLS gating, no AUTH on inbound) are read directly.
- Repo-wide greps cover `internal/` and `cmd/` (`*.go`); they cannot prove absence with absolute certainty, but each absence claim used at least two independent patterns.
