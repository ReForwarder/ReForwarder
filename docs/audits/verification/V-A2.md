# V-A2 — Verification of Category A records SF-12..SF-21 (the reference platform parity audit)

Auditor: V-A2 · Date: 2026-11-07 · Method: static code reading only (host has no Go
toolchain; no builds/tests/docker were run). Every claim below cites `path:line` read in
this session against the working tree at HEAD. Absence claims were checked by
repo-wide `grep` over `internal/` and `cmd/` (`*.go`); a grep cannot prove absence with
certainty, but multiple independent patterns were used per record.

Sources of requirements: `docs/research/reference-platform-feature-inventory.md` §A (SF-12..SF-21
rows, lines 28–37); ledger `docs/research/reference-platform-feature-gap-analysis.md` §4a; the
records under verification are `docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md`
"Category A" (SF-12 at line 162 through SF-21 at line 237); ADR governing scopes read at
`docs/architecture/ADR-001-outbound-mta.md` (header line 9: covers SF-12, SF-15, SF-17,
SF-21), `ADR-002-inbound-smtp-routing.md` / `ADR-003-mailbox-storage.md` (header: covers
SF-18, SF-78 at Phase 3), `ADR-008-data-event-ownership.md` (header line 4: covers SF-19).

Summary of verdicts (unchanged from the audit in all ten records):

| ID | Audit said | V-A2 verdict |
|---|---|---|
| SF-12 | BLOCKED BY ADR-001 (OB-1) | **BLOCKED BY ADR-001 (OB-1)** — confirmed absent |
| SF-13 | PARTIAL — PLANNED (Phase 3) | **PARTIAL — PLANNED (Phase 3, ADR-001 attempt ledger)** — confirmed |
| SF-14 | BLOCKED BY ADR-002/003 (OB-2/3) | **BLOCKED BY ADR-002/003 (OB-2/3)** — confirmed absent |
| SF-15 | BLOCKED BY ADR-001 (OB-1) | **BLOCKED BY ADR-001 (OB-1)** — confirmed absent |
| SF-16 | PARTIAL — SMTP-stage hooks BLOCKED BY ADR-001 (OB-1/2) | **PARTIAL** — confirmed |
| SF-17 | BLOCKED BY ADR-001 (OB-1) | **BLOCKED BY ADR-001 (OB-1)** — confirmed absent |
| SF-18 | BLOCKED BY ADR-003 (OB-3) | **BLOCKED BY ADR-003 (OB-3)** — confirmed absent (crypto note accurate) |
| SF-19 | BLOCKED BY ADR-008 (OB-6) | **BLOCKED BY ADR-008 (OB-6)** — confirmed absent |
| SF-20 | COMPLETE (DKIM2 limitation under ADR-007/OB-6) | **COMPLETE** — fully verified, incl. golden-message tests |
| SF-21 | BLOCKED BY ADR-001 (OB-1) | **BLOCKED BY ADR-001 (OB-1)** — confirmed absent |

---

### SF-12 — Envelope rewriting (MAIL FROM / RCPT TO)

| Field | Verdict |
|---|---|
| Final status | **BLOCKED BY ADR-001 (OB-1)** (audit record: line 167 — confirmed) |
| Fields | Backend: Missing · Frontend: — · DB: Missing · API: Missing · Security: Missing · Tests: Missing (nothing to test) · Docs: Partial (ADR-001 Decision §9 plans it) |
| Implementation | None. Absence verified. |
| Evidence | Repo-wide grep for `SetEnvelope`, `RewriteEnvelope`, `envelope rewrite`, `rewrite.*MAIL FROM` over `internal/` + `cmd/` (`*.go`) returned zero hits. The only envelope manipulation on the outbound path is `envelopeAddress` (`internal/services/email/smtp_sender.go:267-341`, used at `:308` to extract the bare MAIL FROM from the RFC 5322 From) — address extraction, not rewriting. Envelope today = sender's own verified identity; no VERP, no domain-scoped return-path rewrite. |
| Notes | ADR-001 Decision §9 (`docs/architecture/ADR-001-outbound-mta.md`) explicitly plans the rewrite of MAIL FROM to the verified return-path within the OB-1 MTA work, so "BLOCKED BY ADR-001" is the correct gating, not "do-not-implement". Cannot be verified by running tests (no toolchain); verdict rests on code reading + grep. |

### SF-13 — Traceability (Received headers, queue ids, wire logging)

| Field | Verdict |
|---|---|
| Final status | **PARTIAL — PLANNED (Phase 3)** (audit record: line 176 — confirmed) |
| Fields | Backend: Partial · Frontend: Partial (Logs timeline exists; no Received/queue-id display because data does not exist) · DB: Verified (`email_events`) · API: Verified (logs endpoints) · Security: — · Tests: Partial (tests cover what exists; nothing tests Received/queue ids) · Docs: Partial |
| Implementation | What EXISTS: (1) `email_events` timeline — `internal/models/email_event.go:34-58` (table `email_events`, dedup index on `(email_uuid,type,provider_event_id)`); recorder `internal/services/emailhistory/service.go:121` (`Record`) with hooks `internal/services/emailhistory/hooks.go:23-63` (`RecordSent`/`RecordFailed`/`RecordBounced`/`RecordOpened`/`RecordClicked`/`RecordUnsubscribed`), wired into the real send pipeline at `internal/services/email/service.go:872,889,903,971,992,1008` and `internal/worker/handler.go:385,398,428,535,538`, and delivery-truth from provider webhooks `internal/services/provider_events/service.go:212-213` (`EmailEventDelivered`). (2) Attempt history: `retry_count` + `smtp_hostname` metadata on each event — `internal/worker/handler.go:447-461` (`eventMetadata`), consumed at `:376,398`. (3) External message reference: outbound messages carry `X-Posta-ID` = email UUID — `internal/services/email/stamper.go:63` — which provider webhooks resolve back (provider_events `resolveEmail`, `service.go:143-152`). (4) API + UI: `internal/handlers/logs_handler.go` (routes `internal/routes/workspace_resource_routes.go:1583-1617`), frontend timeline `web/src/views/logs/MessageDetail.vue:13,31,205-210` + `MessageHistory.vue`. What is ABSENT: RFC 5322 `Received:` header injection — grep for `Received:` over `internal/`+`cmd/` hits only unrelated identifiers (`InboundStatusReceived`, `MessageStatusReceived`), no header construction; per-hop queue ids — no `queue_id`/`QueueID` in the mail path (asynq task IDs are scheduler-internal, `internal/queues/queues.go:50`); SMTP wire/trc logging of session I/O — grep for `wire log`, `protocol trace`, `trc` found nothing in `internal/services/email/` or `internal/services/smtprelay/`; only structured stage errors exist (`SendError.Stage`, `internal/worker/handler.go:473-479`). |
| Evidence | Tests for the existing substrate: `internal/handlers/logs_handler_test.go:569` (`TestLogsMessageEvents`), `:534` (`TestLogsMessageDetailAuthzAndSlim`); `internal/services/emailhistory/service_test.go:193` (`TestRecordInsertsEventAndStampsWriteOnceTimestamp`), `:265,301` (dedup); `internal/storage/repositories/email_event_repo_test.go:283,327,396,444` (create/filters/ordering). |
| Notes | The audit's PARTIAL split is exactly right: business-level delivery timeline (relay acceptance, provider truth, retries, bounces, opens/clicks) is real and tested; MTA-grade transport traceability (Received headers with HELO/TLS detail, per-hop queue ids, wire logs) does not exist. ADR-001 Decision §5 plans `mta_delivery_attempts` (mx_host, ip, response_code, response_text) as the SF-13 transport substrate at OB-1 — so the residual is PLANNED (Phase 3 / OB-1), matching the audit. |

### SF-14 — Local delivery engine (per-recipient Sieve at ingest)

| Field | Verdict |
|---|---|
| Final status | **BLOCKED BY ADR-002/003 (OB-2/3)** (audit record: line 184 — confirmed) |
| Fields | Backend: Missing · Frontend: — · DB: Missing · API: Missing · Security: Missing · Tests: Missing (nothing to test) · Docs: Partial (ADR-002/ADR-003 scope the mailbox substrate) |
| Implementation | None. Absence verified. |
| Evidence | Repo-wide grep for `sieve` (case-insensitive) over `internal/` + `cmd/` (`*.go`) returned zero hits. No per-recipient script engine, no local-delivery dispatch at ingest; the only "delivery" is the outbound relay path (`internal/services/email/smtp_sender.go`) and the inbound SMTP relay server (`internal/services/smtprelay/smtp_server.go`) which archives, not delivers. |
| Notes | OB-2 (ADR-002, inbound mail) and OB-3 (ADR-003, mailstore) are the governing phases per the audit build-order table (parity audit line 46); ADR-003 Decision §1 plans `mail_messages`/folders but script filtering itself is the SF-45/SF-48 Sieve tier explicitly classified do-not-implement in gap-analysis §4a corrections ("reclassifies ... SF-101 (Sieve) ... to N/A"). BLOCKED is the right status. |

### SF-15 — Milter client

| Field | Verdict |
|---|---|
| Final status | **BLOCKED BY ADR-001 (OB-1)** (audit record: line 192 — confirmed) |
| Fields | Backend: Missing · Frontend: — · DB: Missing · API: Missing · Security: Missing · Tests: Missing (nothing to test) · Docs: Verified (documented alternative) |
| Implementation | None. Absence verified. |
| Evidence | Repo-wide grep for `milter` (case-insensitive) over `internal/` + `cmd/` (`*.go`) returned zero hits — no Connect/Ehlo/Mail/Rcpt/Data/Eom client, no protocol code. The documented alternative exists in code: business-event HMAC webhooks (SF-16, below) and the automation webhook node (`internal/services/automation/webhook_client.go:30-90`, SSRF-guarded HTTP node). |
| Notes | The do-not-implement row is on the record: `docs/research/reforwarder-reference-platform-roadmap.md:171` ("Milter client (SF-15) ... No SMTP-stage pipeline to hook ... MTA hooks concept already covered by automations + webhooks"). ADR-001 (accepted 2026-11-07) supersedes the blanket do-not-implement posture for the MTA foundation and lists SF-15 in its covers (ADR-001 header line 9): an SMTP-stage pipeline to hook arrives with OB-1, so gating SF-15 on ADR-001 is correct and the audit's parenthetical alternative note matches the roadmap. |

### SF-16 — MTA hooks (per-SMTP-stage first-party HTTP hooks, HMAC-signed)

| Field | Verdict |
|---|---|
| Final status | **PARTIAL** — SMTP-stage hooks BLOCKED BY ADR-001 (OB-1/2) (audit record: line 201 — confirmed) |
| Fields | Backend: Partial (event webhooks real; SMTP-stage hooks absent) · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (HMAC-SHA256 + SSRF guard) · Tests: Verified (dispatcher suite) · Docs: Partial |
| Implementation | What EXISTS (the "SF-90 machinery" the audit cites): `internal/services/webhook/dispatcher.go` — HMAC-SHA256 signing `signPayload` `:450-455`, header `X-Posta-Signature: sha256=...` `:485`; exponential retry backoff `retryDelay` `:56-62` + `deliverWithRetry` `:367`; per-attempt history `SetAttemptRepo` `:237` / `recordAttempts` `:543`; operator replay `ReplayDelivery` `:572`; SSRF policy enforced pre-request `:466-477` (+ `ssrf.go`, `proxydial.go`). Business-event dispatch call sites: `email.sent`/`email.failed` — `internal/services/email/service.go:972,993,1009,1024`, `internal/worker/handler.go:405`, `internal/services/retry/worker.go:256`; `campaign.started`/`campaign.completed` — `internal/worker/campaign_processor.go:267,520`; `email.complained` — `internal/handlers/bounce_handler.go:138`; `email.unsubscribed` — `internal/handlers/tracking_handler.go:341`. Automation webhook node: `internal/services/automation/webhook_client.go:68-90`. API + UI: webhook CRUD/delivery/replay routes `internal/routes/workspace_resource_routes.go:742,755,764`, webhook-log routes `:1607,1617`; frontend `web/src/views/logs/OutgoingWebhooks.vue` exists. What is ABSENT: per-SMTP-stage (EHLO/MAIL/RCPT/DATA) first-party HTTP hooks — repo grep for stage-hook patterns found nothing; SMTP-stage awareness exists only as outbound-client error classification (`SendError.Stage`, `internal/worker/handler.go:473-479`) and relay-session security (`internal/services/security/listener.go:129`), neither of which issues HTTP hooks; and the reference platform's response-`modifications` contract (header/rcpt rewrites fed back into the session) has no analog. |
| Evidence | `internal/services/webhook/dispatcher_test.go:36,52,61` (`TestSignPayload*`), `:153,193` (`TestDispatcher_SignatureHeader`, `TestDispatcher_NoSignatureWhenNoSecret`); `internal/services/webhook/dispatcher_replay_test.go:48,78,171` (backoff curve/cap/seam), `:226,287,333` (replay paths); `internal/services/webhook/dispatcher_attempts_test.go` (attempt history). |
| Notes | Matches the audit precisely. The webhook path signs with the same HMAC scheme the reference platform-style hooks would need, so the residual is additive: gate per-stage hooks on the OB-1 SMTP pipeline (an SMTP-stage surface to hook must exist first). No discrepancy found. |

### SF-17 — Queue quota reservations

| Field | Verdict |
|---|---|
| Final status | **BLOCKED BY ADR-001 (OB-1)** (audit record: line 209 — confirmed) |
| Fields | Backend: Missing · Frontend: — · DB: Missing · API: Missing · Security: Missing · Tests: Missing (nothing to test) · Docs: — |
| Implementation | None. Absence verified. |
| Evidence | Repo-wide grep for `queue.*quota`, `quota.*queue`, `452 4.3.1`, `quota reservation` over `internal/` + `cmd/` (`*.go`) returned zero hits. The quota hits that do exist are plan/message-rate quotas, not sender/recipient queue-count+size reservations: `internal/services/plan/service.go`, `internal/services/ratelimit/quota_test.go`, `internal/services/messages/service.go` + `quota_cache_test.go`, and per-object limit checks in `internal/handlers/api_key_handler.go`, `domain_handler.go`, `smtp_handler.go`, `workspace_handler.go`. None enforce ingest-time queue reservations with a 452 4.3.1 rejection. |
| Notes | ADR-001 covers SF-17 (header line 9); queue quota enforcement at ingest belongs to the OB-1 MTA ingest surface. Ingest today (API submission, `internal/services/email/service.go`) has plan/rate limits but nothing of the reference platform's queue-quota semantics (sender/rcpt/rcpt-domain count+size). Status confirmed. |

### SF-18 — Queue blob encryption [ENT]

| Field | Verdict |
|---|---|
| Final status | **BLOCKED BY ADR-003 (OB-3)** (audit record: line 217 — confirmed) |
| Fields | Backend: Missing · Frontend: — · DB: Missing · API: — · Security: Partial (credential/key encryption exists — the audit's note) · Tests: Verified for the crypto service itself · Docs: Partial (ADR-003 §4 plans it) |
| Implementation | Queue blob encryption: absent. The audit's counter-note is accurate: credentials/keys ARE AES-256-GCM encrypted today — `internal/services/crypto/crypto.go:41-44` (`Init` derives 32-byte AES-256 key), `:62-65` (`Encrypt`, AES-256-GCM), `:83-98` (`Decrypt`); callers: SMTP relay credentials `internal/models/smtp_server.go:82`, SMTP server passwords `internal/models/server.go:74`, DKIM private keys `internal/services/email/dkim_signer.go:139-142,207`. What does NOT exist: ECDH per-recipient-group queue-blob encryption (the reference platform `queue/encrypt.rs`) — repo grep found no `cipher`/`Encrypt` in the blob store `internal/storage/blob/` (`blob.go`, `fs.go`, `s3.go` are plaintext); the asynq queue payload carries only row IDs (`EmailSendPayload{EmailID}` — `internal/worker/tasks.go:40-56`), so no message blob transits Redis, and message bodies at rest (Postgres `emails` rows, blob store) are plaintext. |
| Evidence | Grep `Encrypt|aes|cipher` over `internal/storage/blob/*.go`: zero hits (excludes tests). Crypto service tests: `internal/services/crypto/crypto_test.go` exists (round-trip coverage read at file level; not executed — no toolchain). |
| Notes | Two distinct things must not be conflated, and the audit keeps them separate: (a) SF-78 at-rest blob encryption and (b) SF-18 queue-time ECDH blob encryption. ADR-003 Decision §4 plans (a) with the existing crypto service at Phase 3; (b) is the ENT-tier queue feature covered by OB-3 per the build-order table. The BLOCKED BY ADR-003 status with the crypto note is accurate as written. |

### SF-19 — Tenant-scoped queues [ENT]

| Field | Verdict |
|---|---|
| Final status | **BLOCKED BY ADR-008 (OB-6)** (audit record: line 225 — confirmed) |
| Fields | Backend: Missing · Frontend: — · DB: Missing · API: Missing · Security: Missing (workspace isolation exists, but not queue partitioning) · Tests: Missing (nothing to test) · Docs: Partial (ADR-008 §1) |
| Implementation | None. Absence verified. |
| Evidence | Repo-wide grep for `tenant.*queue`, `queue.*tenant`, `TenantQueue` over `internal/` + `cmd/` (`*.go`) returned zero hits. The queue layer (`internal/queues/queues.go:20-50`) names exactly three asynq queues (`Transactional`, `Bulk`, `Low` — workload classes, not tenant partitions); no per-tenant routing, no per-tenant queue isolation or per-tenant queue admin. Tenancy today is workspace-scoped rows per ADR-008 Decision §1 (workspace_id on every table), which is data isolation, not queue partitioning. |
| Notes | ADR-008 header line 4 covers SF-19; ADR-008's rejected-alternative note ("Tenant as a separate object above workspaces now: rejected at Phases 1–5 ... Phase 6 revisit is the decision point") is exactly the OB-6 gating the audit records. Status confirmed. |

### SF-20 — DKIM signing at queue time (DKIM1 RSA/Ed25519 + DKIM2 chained)

| Field | Verdict |
|---|---|
| Final status | **COMPLETE** (audit record: line 234 — confirmed; DKIM2 remains a recorded limitation under ADR-007/OB-6) |
| Fields | Backend: Verified · Frontend: Verified · DB: Verified · API: Verified · Security: Verified (encrypted keys; no private material over API) · Tests: Verified (golden-message + independent verifier + sender-hook tests) · Docs: Verified (README roadmap item; ADR context) |
| Implementation | Signing service: `internal/services/email/dkim_signer.go` — `SignForDomain` `:91-136` (go-msgauth `dkim.Sign`, relaxed/relaxed, SHA-256, RFC 6376 §5.4.1 header set incl. List-Unsubscribe `:124-128`; unsigned-passthrough when no Active+published key, hard failure on lookup/sign errors per the documented policy `:43-52`); key parsing `ParseSigningKeyPem` `:139-178` (RSA PKCS#1/PKCS#8, Ed25519 PKCS#8); key generation `GenerateSigningKeyPem` `:182-208` (RSA-2048 `:187`, Ed25519 `:193-198`, private PEM encrypted via `postacrypto.Encrypt` `:207`); DNS TXT builder `DnsTXTRecord` `:213-256` (RFC 8463 raw 32-octet Ed25519 `p=` `:245-249`); selector validation `ValidSelector` `:265-278`; DNS publication check `VerifyDNSPublication` `:290-319`. Send-path hook: `DkimSigner` interface `internal/services/email/smtp_sender.go:136-140`, process-wide setter `:151-165`, per-sender override `:179-181`, invocation inside `Send` before the SMTP conversation `:312-326` (signs for the envelope sender's domain; signing error fails the send). Production wiring: `cmd/posta/server.go:118` and `cmd/posta/worker.go:70` both call `email.SetDkimSigner(email.NewDkimSigningService(repositories.NewDomainSigningKeyRepository(...)))`. Model/DB: `internal/models/domain_signing_key.go:55-77` — `domain_signing_keys` table, `PrivateKeyPem` has `json:"-"` (never serialized), lifecycle constants `:40-50` (active/retiring/retired, pending-retry/published, rsa-sha256/ed25519-sha256), `CanSign()` `:74-76`; created by AutoMigrate `internal/storage/migration/migration.go:56` (additive) + FK `internal/storage/migration/constraints.go:175-176`. Repository: `FindActiveByDomain` `internal/storage/repositories/domain_signing_key_repo.go:73-85` (JOIN domains, filters status=active AND dns_state=published). API: `internal/handlers/domain_signing_handler.go` — Create `:145`, List `:200`, Rotate `:226`, VerifyDNS `:284`, Retire `:330`, Delete `:371`; routes `internal/routes/workspace_resource_routes.go:842-903` (`POST/GET /domains/{id}/signing-keys`, `/rotate`, `/{keyId}/verify-dns`, `/retire`, `DELETE`). Frontend: `web/src/api/domains.ts:25-46` (create/list/rotate/verify-dns/retire/delete) and `web/src/views/domains/Domains.vue:175-192` (key list) + `:503-547` (DNS TXT record display + copy + verify). DNS display/verify loop: handler `VerifyDNS` (`domain_signing_handler.go:284`) calls `VerifyDNSPublication` and records pending-retry on failure — matching the gap-analysis correction that the reference platform's `Pending` stage is a DNS-publication retry state. |
| Evidence | Golden-message tests: `internal/services/email/dkim_signer_test.go` — fixed test keys `:44-76`, golden message `:80-88` (CRLF per RFC 5322), `TestDkimGoldenRSASignature` `:149-179` (signs, asserts DKIM-Signature prepended, then re-verifies with go-msgauth's own `dkim.VerifyWithOptions` against the DNS TXT the service would publish `:129-147`), `TestDkimGoldenEd25519Signature` `:181-201`, `TestDkimSignatureCatchesTampering` `:204-218` (tampered body must fail), `TestDkimNoKeyUnsignedPassthrough` `:220-229`, `TestDkimUnpublishedKeyDoesNotSign` `:234-245` (defense in depth on DNSState), `TestDkimResolverErrorFails` `:249-255`, `TestDkimUnusableStoredKeyFails` `:257-268`, `TestDnsTXTRecordShapes` `:270-305` (RSA-2048 bit length `:288-290`, 32-octet Ed25519 `p=` `:301-304`), `TestGenerateSigningKeyPemRoundTrip` `:307-335`, `TestValidSelector` `:337-350`, `TestVerifyDNSPublication` `:352-385`. Sender-hook integration: `TestSMTPSenderSignsWhenSignerPinned` `:408-434` (real SMTP session against a loopback relay, signer called once with the envelope domain), `TestSMTPSenderPackageLevelSigner` `:438-461` (the server.go/worker.go wiring path), `TestSMTPSenderSigningErrorFailsSend` `:465-475` (signing failure fails the send). API tests: `internal/handlers/domain_signing_handler_test.go:671,715,732,767,814,878,921,946,966,997,1013` — notably `TestSigningKeyListHasNoPrivateMaterial` `:732` (no private material over API) and `TestSigningKeyVerifyDNS` `:814`. |
| Notes | The audit's COMPLETE claim is accurate on all four requested axes: (1) signing hook — real, in `Send`, wired in both server and worker mains; (2) key encryption — `postacrypto.Encrypt` (AES-256-GCM, `internal/services/crypto/crypto.go:62-65`) on every stored key, `json:"-"` on the model field, and a test asserting no private material in list responses; (3) DNS display + verify — TXT record built server-side and shown/copied in the dashboard, verify-dns endpoint + pending-retry state machine; (4) golden-message tests — fixed keys + fixed message verified with the library's independent verifier, for both RSA-2048 and Ed25519. DKIM2 (chained signatures) is absent — repo-wide grep for `dkim2`/`chained signature`/`chainverification` over `internal/`+`cmd/`: zero hits — and the audit correctly records it as a limitation rather than counting it against DKIM1 completeness (DKIM2 spec work is ADR-007/OB-6 territory). Caveat: no Go toolchain on this host, so tests were read, not executed; the test gate remains the programme's dockerized `go test ./... -count=1`. |

### SF-21 — MT-PRIORITY ingress + virtual-queue ordering

| Field | Verdict |
|---|---|
| Final status | **BLOCKED BY ADR-001 (OB-1)** (audit record: line 242 — confirmed) |
| Fields | Backend: Missing · Frontend: — · DB: Missing · API: Missing · Security: Missing · Tests: Missing (nothing to test) · Docs: — |
| Implementation | None. Absence verified. |
| Evidence | Repo-wide grep for `MT-PRIORITY`, `MT_PRIORITY`, `MTPriority`, `RFC 6710`, `virtual.queue` over `internal/` + `cmd/` (`*.go`) returned zero relevant hits — the single match is marketing copy in the seeder (`internal/services/seeder/seeder.go:216`: "Async processing with automatic retries and priority queues"), which describes the asynq workload queues (`internal/queues/queues.go:26-40`: Transactional/Bulk/Low), not RFC 6710 MT-PRIORITY parameter parsing at ingress or virtual-queue service ordering. There is no SMTP ingress parameter surface at all yet (submission listener is ADR-001 Decision §10, gated). |
| Notes | ADR-001 covers SF-21 (header line 9; inventory cites the reference platform `inbound/mail.rs:303-318` + `queue/manager.rs`). Ingress parsing can only exist once an SMTP submission/ingest listener lands (OB-1), and ordering belongs to the queue manager — BLOCKED BY ADR-001 is the correct status. |

---

## V-A2 sign-off

- All ten records verified by static reading; none of the audit's Final statuses for
  SF-12..SF-21 needed correction.
- The only COMPLETE claim in scope (SF-20 via R-02) was verified end-to-end and holds:
  signer + send-path hook + encrypted keys + DNS display/verify + golden-message and
  sender-hook tests all exist at the cited paths; DKIM2 is absent and correctly recorded
  as a limitation.
- SF-13 and SF-16 PARTIAL splits match the code exactly (timeline/webhooks exist and are
  tested; Received headers, queue ids, wire logs, SMTP-stage hooks do not).
- Not verifiable on this host: test execution (`go test ./...`), builds, lint — verdicts
  rest on code reading and repo-wide greps only; this limitation is stated per record
  above.
- Files modified by V-A2: only this file (`docs/audits/verification/V-A2.md`). No other
  file was read-modified or written.
