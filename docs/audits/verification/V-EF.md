# V-EF — Verification of SF-60..SF-74 (Categories E & F, Stalwart parity audit)

- **Auditor:** V-EF (verification pass, 2026-11-07)
- **Scope:** `docs/audits/STALWART_FEATURE_PARITY_AUDIT.md` Category E (SF-60..SF-64) and Category F (SF-65..SF-74), including pre-seed statuses.
- **Method:** read-only code verification at HEAD (no toolchain — no builds, tests, or docker were run; all test citations are **named functions at file:line**, never executions). Requirements from `docs/research/stalwart-feature-inventory.md` §E/§F; statuses cross-checked against `docs/research/stalwart-feature-gap-analysis.md` §4a.
- **Verification limits:** anything requiring compiling or executing (test greenness, runtime behavior, DNS checks) is reported as "not run". Doc claims were not accepted as proof; every status below rests on `path:line` reads at HEAD.

---

## Category E — Domain management (SF-60..SF-64)

### SF-60 — Domain CRUD (aliases, catch-all, relay flag, DKIM/DNS switches)

**Final status: PARTIAL** — core CRUD COMPLETE (verified); alias/catch-all/relay routing fields BLOCKED BY ADR-002 (OB-2), confirmed absent; one pre-seed correction: **domain CRUD handlers have no dedicated tests** (Tests: Verified → Partial).

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Partial · Docs Verified

**Implementation:** `internal/handlers/domain_handler.go:67-168` — Create (:67, quota-gated :73-77, verification token :79), List (:101), Get (:112, `ownsResource` :114), Delete (:124, `ownsResource` :129), Verify (:140, persists SPF/DKIM/DMARC results :154-157). Routes registered `internal/routes/workspace_resource_routes.go:780-830` (POST/GET/DELETE `/domains`, POST `/domains/{id}/verify`). Admin surfaces: `internal/handlers/admin_domain_handler.go:91-225` (List/Get/Verify/SetVerification with conflict guard :202-207 and audit :310-324). Frontend `web/src/views/domains/Domains.vue` (CRUD + DNS records + signing + health panels), spec `web/src/views/domains/Domains.spec.ts:182-277` (health panel only).

**Evidence:**
- Routing fields absent — confirmed at the model: `internal/models/domain.go:24-37` carries only ID/UserID/WorkspaceID/Domain/4 verification booleans/VerificationToken/CreatedAt. No alias, catch-all, relay flag, report address, or per-domain DKIM/DNS/certificate switches anywhere: `grep -rni "catch.all|catchall" internal/ web/src` = **0 hits**; "relay" appears only in an unrelated protocol field (`internal/models/security.go:54`).
- There is **no Update/PATCH endpoint** for domains at all (only Create/List/Get/Delete/Verify in the handler and in `workspace_resource_routes.go`) — a Stalwart `validate_domain` analog is further away than "fields absent" suggests.
- Blocker verified: ADR-002 (`docs/architecture/ADR-002-inbound-smtp-routing.md:14`) designs aliases/catch-all/sub-address as `inbound_routes` rows, and no `inbound_routes` migration exists (no such file in `internal/storage/migration/`), so OB-2 still gates every routing field.
- **Tests (named):** none for the CRUD handlers — `internal/handlers/` has `domain_health_handler_test.go`, `domain_signing_handler_test.go`, `dmarc_handler_test.go` but **no `domain_handler_test.go`**; `internal/services/domain/` has `health_test.go` only — `verifier.go` (the DNS verification logic the Verify endpoint calls) has **no unit tests**. The pre-seed "Tests: Verified" is overstated; cross-tenant 404 behavior is only exercised transitively via the dmarc/health handler tests (`TestDMARCGetForeignDomainNotFound`, `dmarc_handler_test.go:816`; `TestDomainHealthForeignDomainNotFound`, `domain_health_handler_test.go:153`). **Not run.**

**Notes:** Verified as claimed, with two corrections: (1) routing fields not merely "absent from the API" but absent from the model and DB; (2) test coverage of the CRUD/verify core is nil. The PARTIAL + OB-2 verdict stands.

---

### SF-61 — Automatic DNS record publication (11 provider integrations)

**Final status: PARTIAL → BLOCKED BY ADR-007 (OB-6)** — provider integrations confirmed absent; manual publication + verification flow verified.

Fields: Backend Missing (providers) · Frontend Verified (manual flow) · DB — · API — · Security — · Tests — · Docs Verified (ADR-007)

**Implementation (manual analog):** record generation `internal/services/domain/verifier.go` (`RequiredRecords`, DMARC suggestion with `rua=mailto:dmarc@<domain>` at verifier.go:78); verification `domain.Verify` (domain_handler.go:149) and live re-check `POST /domains/{id}/health/diagnose`.

**Evidence:**
- **No provider integrations** — `grep -rni "cloudflare|route53|powerdns|infoblox|cpanel|plesk|gcloud|tsig|rfc 2136|dynect|godaddy|namecheap|digitalocean" internal/ cmd/` returned **0 provider hits** (only unrelated `heaviestSignal` matches in `internal/services/security/detector.go`). No `internal/services/dnsmanager` package exists.
- ADR-007 (`docs/architecture/ADR-007-dns-deliverability.md`, header line 4 "Covers … SF-61 (DNS auto-publication)") records the decision: a `DNSProvider` interface (`internal/services/dnsmanager`) with Phase 6 adapters for Cloudflare/Route53/DigitalOcean, publication always followed by the R-07 verification flow — i.e., the blocker is deliberate and the parity register already routes SF-61 there.
- Ledger §4a: SF-61 = PART, consistent.

**Notes:** Verified as claimed. Manual publication surface (records + verify + diagnose) is the R-07 flow verified under SF-64. Nothing to add.

---

### SF-62 — DKIM key lifecycle (rotation stages + scheduled rotation task)

**Final status: COMPLETE** — full lifecycle verified in code; scheduled auto-rotation PLANNED (Phase 3) under SF-104, confirmed not yet built.

Fields: Backend Verified · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Verified (named; not run) · Docs Verified

**Implementation:** `internal/handlers/domain_signing_handler.go` — Create :145 (RSA-2048/Ed25519 via `email.GenerateSigningKeyPem` :169, refuses second Active key :165-167), List :200 (view omits private key; `SigningKeyView` :90-101 re-states public shape), Rotate :226 (new Active + demote via `keyRepo.RotateActive` :268), VerifyDNS :284 (`email.VerifyDNSPublication` :297, records DNS state :299-311), **Retire :330** (`MarkRetired` CAS :349-357; Active cannot retire :342-344, no double-retire :345-347), Delete :371 (only Retired deletable :383-385). Statuses `active|retiring|retired` (`internal/models/domain_signing_key.go:41-43`), DNS states `pending-retry|published` (:45-46). Repo: `internal/storage/repositories/domain_signing_key_repo.go` — `RotateActive` :107 (transaction demotes Active→Retiring then inserts), `MarkRetired` :124 (UPDATE…WHERE status='retiring' CAS, returns RowsAffected>0). Encryption: `internal/services/email/dkim_signer.go:182` `GenerateSigningKeyPem` → `postacrypto.Encrypt`; TXT record builder :213 decrypts in-process only. Signing is real: `DkimSigningService.SignForDomain` (dkim_signer.go:88-136) resolves the Active+published key (`FindActiveByDomain` :97) and produces the DKIM-Signature header, gated by `key.CanSign()` (:103-110). Routes: `workspace_resource_routes.go:843-933`.

**Evidence (tests, named):**
- `internal/handlers/domain_signing_handler_test.go` — `TestSigningKeyGenerate` :671, `TestSigningKeyGenerateConflictWhileActiveExists` :715, `TestSigningKeyListHasNoPrivateMaterial` :732, `TestSigningKeyRotation` :767, `TestSigningKeyVerifyDNS` :814, `TestSigningKeyRetireLifecycle` :878, `TestSigningKeyDeleteLifecycle` :966.
- `internal/services/email/dkim_signer_test.go` — `TestDnsTXTRecordShapes` :270, `TestGenerateSigningKeyPemRoundTrip` :307, `TestValidSelector` :337, `TestVerifyDNSPublication` :352. **Not run** (no toolchain).
- No scheduled auto-rotation: `grep -rni "rotation" internal/worker/ cmd/` = 0 hits — the operator-decided retire (no cron) matches the handler comment (domain_signing_handler.go:222-225) and the PLANNED Phase 3 claim.

**Notes:** Verified as claimed. One nuance: the Rotate comment "the retiring key keeps verifying old mail in transit" (domain_signing_handler.go:224-225) has **no implementing code** — nothing in `internal/` uses a Retiring key for inbound verification (grep `DomainSigningStatusRetiring` outside the repo/status constants = 0); Retiring is effectively only a not-yet-deletable state. This does not affect the lifecycle parity claim.

---

### SF-63 — DMARC/SPF/DKIM report settings + TroubleshootDmarc diagnostic

**Final status: PARTIAL — PLANNED (Phase 3)** — report-delivery settings confirmed absent; R-07 diagnose and R-17 aggregate read surface verified; message-paste diagnostic (Stalwart's TroubleshootDmarc) absent.

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Verified (named; not run) · Docs Partial

**Implementation:**
- R-07 live re-check: `internal/services/domain/health.go:129-135` `Diagnose` (re-runs `verify`, reports fresh) + handler `internal/handlers/domain_health_handler.go:80-114` (persists results :95-101, audits :103-112).
- R-17 aggregate read surface: `internal/handlers/dmarc_handler.go:85` `Get` (resolveDomain scope check :72-83) at route `workspace_resource_routes.go:960-973` (`GET /domains/{id}/dmarc`); inbound RFC 7489 report ingestion at `internal/services/inbound/dmarc.go` (receiver hook, :42 comment).

**Evidence:**
- **Report-delivery settings absent** — `grep -rni "rua|ruv|report_address|dmarc.*interval" internal/` finds only the *suggested DNS record string* (verifier.go:78) and the inbound report receiver; there is no per-domain rua-addresses or report-interval settings object, no model, no endpoint. Confirmed.
- **No message-paste diagnostic** — Stalwart's TroubleshootDmarc re-checks a pasted message (SPF/iprev/DKIM/ARC/DMARC); ReForwarder's diagnose re-checks the *domain's* DNS only. No ARC code exists (no `authentication_results`/ARC hits — checked in the V-CD pass and re-confirmed by absence of any ARC file in `internal/`). The analog is partial, not equivalent.
- **Tests (named):** `internal/handlers/dmarc_handler_test.go` — `TestDMARCGetSummary` :731, `TestDMARCGetForeignDomainNotFound` :816, `TestDMARCGetEmptySummary` :848; health-side diagnose tests under SF-64. **Not run.**

**Notes:** Verified as claimed; PARTIAL with Phase 3 planning for the settings half is accurate (the audit pairs it with SF-25 send side).

---

### SF-64 — Domain health endpoint

**Final status: COMPLETE** — end-to-end verified (route → handler → service → DB → UI → tests).

Fields: Backend Verified · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Verified (named; not run) · Docs Verified

**Implementation:** routes `internal/routes/workspace_resource_routes.go:935-957` (`GET /domains/{id}/health`, `POST .../health/diagnose`); handler `internal/handlers/domain_health_handler.go` — Get :65 (stored state, no DNS traffic), Diagnose :80 (requireEdit :81, persists fresh booleans :95-101, audit event :103-112), scope guard `resolveDomain` :55-61 (`ownsResource`, foreign domain = 404); service `internal/services/domain/health.go` — `Report` :122, `Diagnose` :129, per-check guidance :226-239, signing status :186-217 (`keyRepo.FindByDomainID` + `CanSign`), bounce/complaint rates :267-289 (`gormSendingStats`, sender-domain SQL expression :258, workspace-scoped joins :293-305), 30-day window :63. Frontend: `Domains.vue:294-321` lazy-loaded health cache + Diagnose; `Domains.spec.ts:182-277`.

**Evidence (tests, named):**
- `internal/handlers/domain_health_handler_test.go` — `TestDomainHealthGetStoredReport` :106, `TestDomainHealthForeignDomainNotFound` :153, `TestDomainHealthDiagnoseRequiresEdit` :167, `TestDomainHealthDiagnosePersistsAndAudits` :188.
- `internal/services/domain/health_test.go` — 7 tests: `TestHealthReportFromStoredState` :79, `TestHealthReportUnverifiedDomain` :125, `TestHealthDiagnoseUsesLiveChecks` :148, `TestHealthDiagnoseVerifyError` :184, `TestSigningStatusOf` :195, `TestRateOf` :221, `TestGormSendingStatsSenderDomainSQL` :250. **Not run.**

**Notes:** Verified as claimed. Stalwart's negative (no health endpoint) is genuinely improved upon. The diagnose path's persistence mirrors the verify endpoint, so stored booleans cannot drift — as documented.

---

## Category F — Identity & account management (SF-65..SF-74)

### SF-65 — Accounts (User/Group) with credentials, aliases, quotas, locale

**Final status: PARTIAL** — user-side account surface verified broad; **groups/aliases confirmed absent** (BLOCKED BY ADR-004, OB-4, mail-account semantics).

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Partial · Docs Partial

**Implementation:** user model `internal/models/user.go:30-56` (password hash, role, 2FA fields, Active, PlanID, `ScheduledDeletionAt` :44, `EmailVerifiedAt`, `DefaultWorkspaceID`); auth flows `internal/handlers/user_handler.go` — Register :213, ForgotPassword :322, ResetPassword :350, VerifyEmail :380, UpdateProfile :427, ChangePassword :446, Login :488 (2FA + recovery fallback :574), new-device detection :543-546/:721, login alerts :739; 2FA setup/verify/disable :619-689 (routes `user_routes.go:93-116`); recovery codes `internal/handlers/recovery_code_handler.go` (Generate :70, Remaining :107, DeleteAll :116, Consume :122; routes `user_routes.go:121-134`); passkeys `internal/handlers/webauthn_handler.go` (registration/login ceremonies :110-199, credential CRUD :308-335; routes `user_routes.go:332-413`); sessions `internal/handlers/session_handler.go` (see SF-68); locale/timezone: `internal/models/user_setting.go:28` `Timezone`, `:37` `DefaultLanguage` + `internal/handlers/user_setting_handler.go:57-103` (validation tests `TestSettingsTimezoneValidation`, `internal/handlers/setting_timezone_test.go:33`). Frontend `web/src/views/auth/Profile.vue` wires sessions + passkeys + 2FA + recovery codes (:6-7, :21, :50-55, :192-199).

**Evidence:**
- **Groups absent** — no mail-group/account-group model anywhere (`grep` over `internal/models/` for group structs = only `SegmentConditionGroup`, unrelated). **Aliases absent** — no mail alias / address alias model or migration (0 hits).
- Quotas: user Plan (`PlanID` :43) + workspace resource quotas via `internal/services/plan/service.go:138` `CheckQuota` (consumed by domain/api-key creation).
- **Tests (named):** login/2FA/recovery flows covered by `internal/handlers/recovery_code_handler_api_test.go` — `TestGenerateRecoveryCodesTOTPGate` :469, `TestLoginRecoveryCodeFallback` :522, `TestLoginRejectsBadTwoFactorCode` :579; WebAuthn suite under SF-69. **No `user_handler_test.go`** — Register/Profile/password-reset handlers have no dedicated tests. **Not run.**
- Locale nuance: `DefaultLanguage` is consumed as a *content/template* default (`web/src/components/TemplateModal.vue`, `EmailPreview.vue`, `WorkspaceDetail.vue`), not a full dashboard UI locale switch — "locale" parity is shallower than the inventory's account-locale framing.

**Notes:** Verified as claimed. The PARTIAL + ADR-004 blocker is correct: groups/aliases are mail-account semantics and ADR-004 (`docs/architecture/ADR-004-imap-pop3.md`) is the Phase 4 home for them.

---

### SF-66 — Granular permissions (~660) + custom Roles

**Final status: PARTIAL — PLANNED (OB-6, with ADR-008 tenancy decisions)** — role model counted: 2 platform roles + 4 workspace roles + 5 API-key scopes; no permission registry, no custom roles.

Fields: Backend Partial · Frontend Verified (role-gated UI) · DB Partial · API Verified · Security Verified · Tests Partial · Docs Partial

**Implementation:** platform roles `internal/models/user.go:27-28` (`admin`, `user`; `IsAdmin` :58-60); workspace roles `internal/models/workspace.go:27-30` (`owner|admin|editor|viewer`) on `WorkspaceMember.Role` (:65) with capability helpers `CanManageMembers` :91, `CanEdit` :96, `CanView` :101, `IsOwner` :106; enforcement `internal/handlers/helper.go:116` `requireEdit` and `internal/middlewares/` workspace-role middlewares (e.g., `RequireWorkspaceRole(models.WorkspaceRoleAdmin)` on data export/import, `internal/routes/workspace_routes.go:274-291`); API-key scopes `internal/models/api_key.go:32-47` (`send|read|webhooks|write|admin|*`).

**Evidence:**
- Role count verified: **6 named roles** (2 platform + 4 workspace) + 5 valid API-key scopes + `"*"`. Nothing else: no permission-registry table, no `Role` object model, no permission composition — `grep -rni "permission" internal/models/` finds only scope constants; ADR-008 (`docs/architecture/ADR-008-data-event-ownership.md:17`) records the ~660-granular registry + custom roles as a Phase 6 evaluation (PLANNED/OB-6), matching the pre-seed.
- **Tests (named):** `internal/routes/workspace_rbac_test.go` — `TestWorkspaceAdminRoutesRejectNonAdmins` :103, `TestDeleteWorkspaceIsOwnerOnly` :140, `TestWorkspaceMemberRoutesStayOpenToMembers` :159; middleware scope tests `internal/middlewares/scope_test.go:40` `TestRequireScope`, `workspace_test.go:146` `TestWorkspaceScopeEnforcement`. **Not run.**
- DB Partial: roles are plain string columns; no permission/role tables to compose.

**Notes:** Verified as claimed. The pre-seed's "4-role RBAC" refers to the workspace tier; the full count including the platform tier is 6. No discrepancy that changes the verdict.

---

### SF-67 — API keys + app passwords (permission narrowing, IP allowlists)

**Final status: PARTIAL** — API keys verified complete for the claimed surface; **app passwords confirmed absent**, PLANNED (Phase 3) with the ADR-004 pairing verified; one correction: **IP allowlists are enforced but untested**.

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Partial · Docs Verified

**Implementation:** `internal/handlers/api_key_handler.go` — Create :83 (scopes, `AllowedIPs` :52, expiry), List :167, Get :182, Revoke :190, Delete :208; model `internal/models/api_key.go:50-69` — `ExpiresAt` :58, `Revoked`, `AllowedIPs` :61, `Scopes` :62, **R-15** per-key `RateLimitHourly/Daily` :66-67; enforcement `internal/middlewares/middleware.go:327` (`IPAllowed(apiKey.AllowedIPs, c.RealIP())`, implementation :421-423) and per-key rate limiting (R-15); **R-18** usage analytics `internal/handlers/api_key_usage_handler.go:54` `Get` over `APIKeyUsageDaily` (`api_key.go:79-89`, upsert-keyed counters); **R-28** query-token scoping `middleware.go:226-272` (`?token=` restricted to SSE endpoints that cannot set headers, documented at :29, :87). Frontend `ApiKeyDetail.vue` (per pre-seed; usage chart surface).

**Evidence:**
- **App passwords absent** — `grep -rni "app.password|application.password" internal/ web/src` = **0 hits**. The nearest analog is SMTP *relay* credentials (`internal/handlers/smtp_credential_handler.go:49-74`, with their own `AllowedIPs` :49) — those are sending credentials, not mail-reading app passwords.
- ADR-004 pairing verified: `docs/architecture/ADR-004-imap-pop3.md:14` — app passwords (SF-35/67 pairing) are specified as the IMAP/POP3 auth credential, Phase 4 — so "PLANNED (Phase 3)" in the audit is consistent with the ADR's home for them.
- **Tests (named):** `internal/handlers/api_key_usage_handler_test.go` — `TestAPIKeyUsageHandlerSeriesAndClamp` :299, `TestAPIKeyUsageHandlerScope` :341; `internal/services/auth/api_key_test.go:29` `TestNormalizeScopes`; `internal/middlewares/rate_limit_test.go` — `TestAPIKeyRateLimitCheck` :143, `TestIsAPIKeySendPath` :187, `TestQueryTokenAllowed` :41 (R-28); `internal/middlewares/authenticate_test.go:164` `TestAuthenticate_RoutesAPIKeyPrefixToKeyPath`, `workspace_test.go:62` `TestRequireWorkspace_BoundAPIKey`. **Gap: `grep -rln "AllowedIPs|allowed_ips" internal/ --include="*_test.go"` = 0 files — the IP allowlist path (`IPAllowed`, middleware.go:421) has no test coverage.** **Not run.**

**Notes:** Verified as claimed, with the allowlist test gap recorded. Per-credential *permission narrowing* (Stalwart's Inherit/Disable/Replace per credential) exists only as the coarse scope list — narrowing granularity is part of the same OB-6 roles discussion, consistent with the PARTIAL verdict.

---

### SF-68 — OAuth grant/session administration

**Final status: PARTIAL** — session/device management verified; **OAuth grant administration confirmed absent** (consumer-not-provider posture, SF-37); grant admin PLANNED (Phase 3) only if SF-37 lands. One correction: **the session endpoints have no dedicated tests**.

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Partial · Docs Partial

**Implementation:** `internal/handlers/session_handler.go` — List :59 (device metadata via `clientinfo.Parse`: Browser/OS/Device/Label :66-80, current-session flag :78), Revoke :89, RevokeOthers ("bulk" revoke of other sessions) :112, Logout :143; routes `internal/routes/user_routes.go:161-194`; frontend API `web/src/api/sessions.ts:17-28` + Profile.vue. New-device alerts: `internal/handlers/user_handler.go:543-546` (detected before session record), `isNewDevice` :721, `sendLoginAlert` :739. Impersonation session model: `internal/models/session.go:38` `ImpersonatedBy *uint` (indexed).

**Evidence:**
- **OAuth grants absent** — `grep -rni "oauth.*grant|grant.*oauth|SysOAuth" internal/` = **0 hits**. What does exist is OAuth *provider/SSO configuration* (`internal/handlers/oauth_admin_handler.go` — CreateProvider :111, ListProviders :150, UpdateProvider :163, DeleteProvider :214, Get/Set/DeleteWorkspaceSSO :221/:241/:262) — that is consumer-side IdP config, not grant (user-consent artifact) administration. The pre-seed's distinction is accurate.
- **Tests (named):** none for the session endpoints — no `session_handler_test.go`, and no test references `RevokeOthers`/`revoke-others` (grep over `*_test.go` = 0). The impersonation half of the session model is tested (`impersonation_handler_test.go`, see SF-70). Pre-seed "Tests: Verified" is overstated → Partial. **Not run.**

**Notes:** Verified as claimed, with the session-endpoint test gap recorded. The ledger's FULL claim (gap-analysis §4a: SF-68 FULL) remains overstated exactly as the parity audit predicted ("Phase 1 to reconcile"): sessions yes, grants no.

---

### SF-69 — 2FA: TOTP (+ recovery codes, + WebAuthn beyond Stalwart)

**Final status: COMPLETE** — TOTP + one-time recovery codes (R-10) + WebAuthn passkeys (R-19) verified with named tests. **Two corrections to the record text:** the "`two_factor_required` wiring (R-23)" claim is wrong — R-23 concerns `require_domain_verification` (a different setting), and `two_factor_required` itself is a **dead toggle with zero consumers**.

Fields: Backend Verified · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Verified (named; TOTP helper untested directly) · Docs Verified (with the R-23 miscite corrected here)

**Implementation:** TOTP `internal/services/twofactor/totp.go` + `internal/handlers/user_handler.go` Setup2FA :619, Verify2FA :647, Disable2FA :689 (routes `user_routes.go:93-116`); recovery codes R-10 `internal/handlers/recovery_code_handler.go` — Generate :70 (hashes, one-time), Consume :122, Remaining :107, DeleteAll :116, plus login fallback `user_handler.go:574` `consumeRecoveryCode` and routes `user_routes.go:121-134`; Profile UI surfaces the one-time codes (`Profile.vue:197-199`). WebAuthn R-19 `internal/handlers/webauthn_handler.go` — BeginRegistration :110, FinishRegistration :135, BeginLogin :162, FinishLogin :199, second-factor path `finishSecondFactor` :264 + `secondFactorVerified` :287, credential list/rename/delete :308-335 (routes `user_routes.go:332-413`).

**Evidence (tests, named):**
- `internal/handlers/webauthn_handler_test.go` — `TestWebAuthnDisabledAnswersServiceUnavailable` :839, `TestWebAuthnRegistrationCeremony` :865, `TestWebAuthnRegistrationRejectsForeignCeremony` :938, `TestWebAuthnLoginBeginRejectsUnknownAndCredlessAccounts` :962, `TestWebAuthnLoginCeremony` :987, `TestWebAuthnLoginFinishInputValidation` :1061, `TestWebAuthnLoginSecondFactor` :1089, `TestWebAuthnLoginSecondFactorRejectsBadCodeAndTokens` :1144, `TestWebAuthnCredentialManagement` :1176, `TestWebAuthnVanishedUserAbortsNotPanics` :1223.
- `internal/handlers/recovery_code_handler_test.go` — `TestRecoveryCodeGenerateAndConsume` :279, `TestRecoveryCodeGenerateInvalidatesOutstanding` :346, `TestRecoveryCodeDeleteAll` :370; API-level `TestLoginRecoveryCodeFallback` :522, `TestLoginRejectsBadTwoFactorCode` :579. **Not run.**

**Evidence (corrections):**
- `two_factor_required` is seeded (`internal/services/seeder/settings.go:52`), exposed by the settings provider (`internal/services/settings/provider.go:124` `TwoFactorRequired()`) and rendered as an admin toggle (`web/src/views/admin/Settings.vue:21,208`) — but `grep -rn "\.TwoFactorRequired()|GetBool(\"two_factor" internal/ cmd/` finds **no consumer**: nothing in the login flow or middleware enforces it. It is a dead security toggle.
- R-23 actually reads: "Wire up (or remove) the dead `require_domain_verification` platform setting" (`docs/research/reforwarder-stalwart-roadmap.md:27,142`) — a different setting. The parity audit conflated the two.
- `internal/services/twofactor/` contains only `totp.go` — **no unit test file** for the TOTP helper itself; its behavior is exercised only through the handler tests above.

**Notes:** Core parity claim (TOTP + recovery + WebAuthn, all beyond Stalwart's TOTP-only) fully verified — COMPLETE stands. The dead org-wide 2FA toggle should be tracked as a separate item (it is not part of Stalwart SF-69), and the record text's R-23 citation should be dropped or corrected.

---

### SF-70 — Admin impersonation

**Final status: COMPLETE** — verified end-to-end (handler, markers, session model, Redis blacklist, audit, banner, tests).

Fields: Backend Verified · Frontend Verified (banner) · DB Verified · API Verified · Security Verified · Tests Verified (named; not run) · Docs Verified

**Implementation:** `internal/handlers/impersonation_handler.go` — Impersonate :99 (self-guard :107, admin-target guard :114, disabled-account guard :117, reason ≥ 10 chars :123-125, issues target session with `impersonated_by` + `impersonation_reason` claims :128-132, cookie swap, audit `admin.impersonation.started` :137-145); ExitImpersonation :165 (refuses non-impersonated sessions :167-169, revokes + Redis-blacklists + clears cookie, audit `admin.impersonation.stopped` fired once :198-205); admin's own session row untouched (:135-139 comment). Routes: `internal/routes/admin_routes.go:161-162` (`POST /admin/users/{id}/impersonate`, admin-gated group :34 `jwtAdminAuth`) and `internal/routes/user_routes.go:440` (exit). Middleware claim propagation `internal/middlewares/middleware.go:68-72,161` + `IsImpersonated` :196-198. Session model `internal/models/session.go:38` `ImpersonatedBy`. Redis JTI blacklist `internal/services/session/session.go:42-51` (`MarkRevoked`/`IsRevoked`). Frontend `web/src/components/ImpersonationBanner.vue` (+ `ImpersonationBanner.spec.ts`).

**Evidence (tests, named):** `internal/handlers/impersonation_handler_test.go` — `TestImpersonateIssuesMarkedSession` :647, `TestImpersonateResponseCarriesTargetProfile` :729, `TestImpersonateGuards` :758, `TestExitImpersonationRevokesAndAudits` :852, `TestExitImpersonationRefusesNonImpersonatedSession` :928. **Not run.**

**Notes:** Verified exactly as claimed.

---

### SF-71 — SCIM 2.0 provisioning [ENT]

**Final status: BLOCKED BY ADR-008 (OB-6)** — absence confirmed; ADR home verified.

Fields: Backend Missing · Frontend — · DB — · API — · Security — · Tests — · Docs Verified (ADR-008)

**Implementation:** none (intentionally).

**Evidence:**
- `grep -rni "scim" internal/ cmd/ web/src --include="*.go" --include="*.vue" --include="*.ts"` = **0 hits** — no SCIM server, filter language, bulk endpoints, or ETag support anywhere.
- ADR-008 (`docs/architecture/ADR-008-data-event-ownership.md:4,17`) explicitly covers SF-71 and records "SCIM provisioning … adopted only under this ADR's Phase 6 revisit with a migration plan for existing workspaces. Recorded as PLANNED (OB-6) with prerequisites, not dropped." `docs/architecture/IMPLEMENTATION-ROADMAP.md:41` lists the same revisit ("(g) roles/tenants/SCIM revisit (only if approved)").
- Ledger §4a: SF-71 = N/A, consistent with the do-not-implement tier.

**Notes:** Verified as claimed.

---

### SF-72 — Tenants (object + quotas + scoped admin views) [ENT]

**Final status: PARTIAL** — workspace tenant analog verified deep (isolation confirmed by spot-check); distinct tenant objects and tenant-level quotas confirmed absent, BLOCKED BY ADR-008 (OB-6).

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Verified (named; not run) · Docs Partial

**Implementation:** workspaces `internal/models/workspace.go` (Workspace + `WorkspaceMember.Role` :65 + `WorkspaceInvitation` :76); isolation: `ownsResource` (`internal/handlers/helper.go:96`) is used **84 times across 15+ handler files** (spot-check: domain_handler.go:114/:129/:145, domain_signing_handler.go:131, domain_health_handler.go:57, dmarc resolveDomain, api_key_handler.go, template/segment/subscriber/contact/log/inbound handlers — every resource read funnels through it, foreign resources answer 404); list-level scoping `ApplyScope` (`internal/storage/repositories/scope.go:36`) used in **21 repository files**; admin scoped views `internal/handlers/admin_workspace_handler.go` — List :102, Get :131, usage roll-up :191 (deployment-wide admin scope per ADR-008:8); quotas = plan-based per-workspace resource checks `internal/services/plan/service.go:138` `CheckQuota` (consumed e.g. domain_handler.go:73-77); member/invitation lifecycle `internal/handlers/workspace_handler.go` (Create :161 … AcceptInvitation :642, role update :401, remove :456).

**Evidence:**
- **No tenant objects** — `grep -rni "type Tenant\b|tenant_id" internal/models/` = **0 hits**; quotas are plan-scoped (per workspace resource counts), not Stalwart-style tenant quota objects.
- **Tests (named):** `internal/routes/workspace_rbac_test.go:103/140/159` (admin-route rejection, owner-only delete, member access); cross-tenant 404s asserted in resource tests (`TestDMARCGetForeignDomainNotFound` dmarc_handler_test.go:816, `TestDomainHealthForeignDomainNotFound` domain_health_handler_test.go:153, `TestWorkspaceScopeForMessageRoutes` middlewares/message_scope_test.go:29); `TestShouldSeedWorkspace` workspace_seed_test.go:26. **Not run.**
- ADR-008 verified: workspace is the mandated tenant boundary for all new tables (:12, with the `ownsResource`/`OwnsWorkspaceResource` pattern named), tenant objects recorded as Phase 6 evaluation (:17).

**Notes:** Verified as claimed. The ledger's FULL claim remains overstated as the parity audit predicted; PARTIAL + ADR-008 blocker is the truthful state.

---

### SF-73 — Account destruction task (staged, grantee cleanup)

**Final status: PARTIAL** — the staged destruction chain **exists and is wired** (schedule → daily cron sweep → transactional purge), stronger than the pre-seed "to be verified" hedge; but the **entire chain has zero tests**, and account-level (as opposed to workspace-level) GDPR export is absent. Residuals → PLANNED (Phase 3). The ledger's FULL claim is overstated.

Fields: Backend Partial · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Missing (for this chain) · Docs Partial

**Implementation:**
- Self-service staging: `internal/handlers/user_handler.go:908` `RequestAccountDeletion` (admins excluded :915-917, 7-day delay :923, `Active=false` :925, event `user.deletion_requested` :931-935, confirmation email :937-946) and `:955` `CancelAccountDeletion` (re-activates :966-967).
- Admin staging: `internal/handlers/admin_handler.go:306` `DeleteUser` (schedule + disable + audit event :317-338), `:344` `ForceDeleteUser` (requires disabled account :355-357 → `DeleteAllUserData` :359), `:374` `CancelUserDeletion`.
- **The staged destruction task exists:** `internal/cron/jobs/account_cleanup.go` — `AccountCleanupJob` (:32), schedule `0 2 * * *` daily 02:00 UTC (:41), `FindScheduledForDeletion` (:44, repo query `user_repo.go:286` picks up past-due schedules) → `DeleteAllUserData` (:56); **registered** `cmd/posta/server.go:747` (`manager.Register(jobs.NewAccountCleanupJob(...))`).
- Purge coverage: `internal/storage/repositories/user_repo.go:170-283` — one transaction: cascade deletes via campaigns/lists/templates (:172-201), events by actor (:218-220), contact-list members (:222-235), webhook deliveries (:237-241), 21 direct `user_id` tables (:207-229 incl. sessions, api_keys, domains, emails, inbound_emails, o_auth_accounts), workspace memberships (:260-263), **owned workspaces + their invitations/members** (:265-279 — the grantee-cleanup analog), then the user row (:281).
- GDPR workspace flows (separate, admin-gated): `internal/routes/workspace_routes.go:272-337` — `GET /data/export` (:272-285, RequireWorkspaceRole Admin + ScopeAdmin), `POST /data/import` (:286-298), `POST /gdpr/delete-contacts` (:300), `/gdpr/delete-email-logs` (:312), `/gdpr/delete-messages` (:324, erases a person's form messages, replies, inbound emails, attachments — `internal/handlers/gdpr_messages.go:21-25`).

**Evidence:**
- **No tests for the destruction chain** — `grep -rln "RequestAccountDeletion|account_cleanup|DeleteAllUserData|ForceDeleteUser" internal/ --include="*_test.go"` = **0 files**; there is no `account_cleanup_test.go` (the `internal/cron/jobs/` dir has tests for backup/queue_reconcile/retention/campaign_scheduler only) and no `workspace_data_handler_test.go`. The pre-seed "Tests: Partial" is, for this chain, closer to Missing. **Not run.**
- No automatic execution gap: the cron closes the loop the pre-seed feared — `ScheduledDeletionAt` is consumed by `FindScheduledForDeletion` and the sweep is registered in the server process.
- Stalwart deltas: no "suspended" state intermediate (matching the inventory's noted Stalwart gap too); grantee cleanup is membership/ownership cascade, not share-grantee reassignment (no share objects exist to clean).

**Notes:** The pre-seed record asked Phase 1 to "verify deletion coverage" — verified: coverage is broad and the task is real. What keeps this PARTIAL is (1) zero test coverage on the whole destruction chain (a Done-bar item), and (2) the GDPR export surface is workspace-scoped content, not a person's cross-workspace account export. Both are recorded as Phase 3 residuals.

---

### SF-74 — Admin audit history

**Final status: COMPLETE** — events table, audit logger across admin actions, admin Events UI, SSE, and CSV export all verified. One correction: the event endpoints themselves have **no dedicated tests** (audit logging is asserted transitively in other suites).

Fields: Backend Verified · Frontend Verified · DB Verified · API Verified · Security Verified · Tests Partial · Docs Verified

**Implementation:** events model `internal/models/event.go:33-53` (Category/Type/WorkspaceID/ActorID/ActorName/ClientIP/Metadata + audit filters `Outcome` success|failure|denied, `TargetType`, `TargetID` :47-50); logger `internal/services/audit/audit.go` — `Log` :38, `LogScoped` :42, `LogCtx` :46, `LogCtxScoped` :50 (used across admin actions: domain admin handler :310-324, signing handler :137-141, impersonation :137/:200, health diagnose :103-112, user lifecycle events); handlers `internal/handlers/event_handler.go` — List :61, Get :52, UserAuditLog :81, WorkspaceAuditLog :93, detail :110, SSE `Stream` :127; routes: admin `GET /admin/events` (`internal/routes/admin_routes.go:221`), `/admin/events/{id}` (:231), `/admin/events/stream` (:742) — all inside the `jwtAdminAuth` group (:34, deployment-admin scope as the pre-seed states); workspace audit `GET /logs/audit` (`workspace_resource_routes.go:1631`) + **CSV export** `GET /logs/audit/export` (:1641-1645, "Streams the filtered audit log as text/csv (capped at 100000 rows). Same filters and authorization as the audit list."); admin UI `web/src/views/admin/Events.vue` + `EventDetail.vue`.

**Evidence:**
- No `event_handler_test.go` and no `internal/services/audit/*_test.go` (the audit package is a single file). Audit assertions are transitive: `TestDomainHealthDiagnosePersistsAndAudits` (domain_health_handler_test.go:188), `TestExitImpersonationRevokesAndAudits` (impersonation_handler_test.go:852), `TestImpersonateGuards` (:758). **Not run.**
- Stalwart's negative (no audit-log feature; only store-level changelogs) is genuinely improved upon, as claimed.

**Notes:** Verified as claimed; the completion claim rests on implementation/wiring evidence, and the missing dedicated event-endpoint tests are recorded here as the residual gap.

---

## Summary of corrections to the pre-seed records

| Record | Pre-seed said | V-EF found |
|---|---|---|
| SF-60 | Tests: Verified | No dedicated domain-CRUD tests; `services/domain/verifier.go` untested → Tests Partial |
| SF-62 | — | "Retiring keys keep verifying mail" comment has no implementing code (inbound DKIM verification of Retiring keys does not exist); lifecycle itself fully verified |
| SF-67 | Tests: Verified | IP allowlist (`IPAllowed`, middleware.go:421) has zero test references → Tests Partial |
| SF-68 | Tests: Verified | Session list/revoke/revoke-others endpoints untested → Tests Partial |
| SF-69 | "two_factor_required wiring (R-23)" | Wrong twice: R-23 is `require_domain_verification`; `two_factor_required` is a dead toggle (seeder + getter + admin UI, zero enforcement consumers). Core TOTP/recovery/WebAuthn claim unaffected — COMPLETE stands |
| SF-73 | Backend Partial (verify) | Staged destruction task verified real and registered (`cron/jobs/account_cleanup.go`, cmd/posta/server.go:747); but zero tests on the chain and no account-level export → stays PARTIAL |
| SF-74 | Tests: Verified | Event endpoints untested (audit asserted transitively only) → Tests Partial; surface itself complete |

Nothing else in the pre-seed texts for SF-60..SF-74 was contradicted by the code.
