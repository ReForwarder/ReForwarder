# V-B1 — Verification verdicts: the reference platform parity audit Category B (SF-22..SF-33)

Auditor: V-B1 · Date: 2026-11-07 · Scope: `docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md` Category B records SF-22..SF-33 (pre-seed statuses included) · Requirements: `docs/research/reference-platform-feature-inventory.md` §B · Ledger: `docs/research/reference-platform-feature-gap-analysis.md` §4a.

**Method and limits.** Read-only code verification at HEAD `482d10b` (tree carries uncommitted changes not touched by this audit). **No Go toolchain available — no builds, no tests, no docker were run.** Every test named below was located by reading its source (`file:line`), not executed; "Tests: Verified" means the test source exists, is substantive, and targets the verified behavior — not that a run passed. Frontend verified by reading sources/specs, not by running a browser. Absence claims rest on exhaustive greps over `internal/` + `cmd/` (`*.go`, tests excluded) for: `spf`, `dkim.Verify`, `msgauth`, `Received-SPF`, `Authentication-Results`, `mta-sts|mtasts`, `tlsa|dane`, `tls-rpt|tlsrpt`, `arc`, `iprev`, `LookupAddr`, `arf|5965|6591`, `dkim2`, `GetCertificate|sni|acme|autocert`, `aggregate|rua`. Only this file was written.

**Headline correction to the pre-seed ledger:** none of the records changed status; SF-22/23/24 are confirmed MISSING (DNS presence checks verified as the only SPF/DKIM/DMARC surface — no message-level engine exists), SF-25 receive side confirmed COMPLETE with tests and wiring, SF-33's implemented TLS slice is better documented than the pre-seed "Docs: Partial" assumed. One systemic caveat: ADR-007 and ADR-009 do not exist in the tree yet (`todo.md:187-197` lists them as open; `docs/architecture/` holds only ADR-001/002, untracked, dated today) — the four records blocked on them are confirmed code-absent but their governing ADRs are *pending*, not yet written.

---

### SF-22 — SPF verification (EHLO + MAIL FROM, strict modes, Received-SPF)
| Field | Value |
|---|---|
| Final status | **MISSING → PLANNED (Phase 3)** — pre-seed CONFIRMED |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None (message-level). DNS presence checks only: `internal/services/domain/verifier.go:97-100` (SPF = any TXT starting `v=spf1`), `internal/services/domain/health.go:144-166` (health report consumes the presence flags), `internal/services/domain/verifier.go:65-69` (required-record template) |
| Evidence | Greps for `spf` across `internal/`+`cmd/` return only the presence checks above and unrelated `spf13` CLI deps (`go.mod:87-88`); no SPF library; no `Received-SPF` / `Authentication-Results` generation anywhere. Inbound ingest (`internal/services/inbound/service.go:188-340` `Ingest`, `:342-413` `IngestRaw`) performs size limit, domain resolution, suppression, dedup — no authentication evaluation. |
| Notes | The R-07 domain-health SPF check (`docs/project/FEATURE_INVENTORY.md:61,66`, tests `internal/services/domain/health_test.go:148` `TestHealthDiagnoseUsesLiveChecks`) is record *presence*, not RFC 7208 identity evaluation of received mail — correctly kept distinct from SF-22. Relay-compatible Phase-3 placement per the parity audit stands. Cannot verify: runtime behavior (no toolchain). |

### SF-23 — DKIM verification (insecure-sig stripping, strict, ARF)
| Field | Value |
|---|---|
| Final status | **MISSING → PLANNED (Phase 3)** — pre-seed CONFIRMED |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None (verification). The only DKIM code is send-side signing: `internal/services/email/dkim_signer.go:37` imports `github.com/emersion/go-msgauth/dkim`; `:116-132` builds `dkim.SignOptions` and calls `dkim.Sign` (relaxed/relaxed). `:286-290` `VerifyDNSPublication` is a DNS TXT presence check for the selector. |
| Evidence | `dkim.Verify` is never called in the tree (grep zero hits); `go-msgauth v0.7.0` present (`go.mod:10`) so the verifier is one call away, exactly as the pre-seed noted. No insecure-signature stripping, no strict mode, no ARF auth-failure reporting. `internal/services/email/dkim_signer_test.go` exercises signing/publication, not verification. |
| Notes | Signing (R-02) must not be credited as verification. Phase-3 plan stands. |

### SF-24 — DMARC verification with policy enforcement
| Field | Value |
|---|---|
| Final status | **MISSING → PLANNED (Phase 3)** — pre-seed CONFIRMED (depends on SF-22 + SF-23, both verified missing) |
| Fields | Backend Missing · Frontend — · DB Missing (for this feature) · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None (message-level). DMARC surfaces that exist are out of scope for this record: presence check `internal/services/domain/verifier.go:126-136` (TXT prefix `v=DMARC1`); aggregate-report *ingestion* `internal/services/inbound/dmarc.go` (R-17, see SF-25) parses reports **about** the tenant's domains — it never evaluates inbound mail. |
| Evidence | No policy-record fetch at message time, no p/sp/adkim/aspf evaluation, no alignment computation from live SPF/DKIM results, no Reject/Quarantine/None enforcement on inbound messages (greps zero; ingest path re-read line by line). `dmarc_reports` storage is per-provider-report rows, not per-message verdicts. |
| Notes | The alignment booleans stored by R-17 (`DKIMAligned`/`SPFAligned`) are values *reported by the sender's provider*, not our own evaluation. Dependency chain intact; Phase-3 plan stands. |

### SF-25 — DMARC aggregate reports: send AND receive/ingest
| Field | Value |
|---|---|
| Final status | **PARTIAL** — receive/ingest side **COMPLETE (verified)**; send side MISSING → PLANNED (Phase 3) |
| Fields | Backend Verified (receive) / Missing (send) · Frontend Verified (read) · DB Verified · API Verified (read) · Security Verified (size caps + scoping) · Tests Verified · Docs Partial |
| Implementation | Receive: parser `internal/services/inbound/dmarc.go:185-243` (`ParseDMARCAggregateReport`, RFC 7489 XML; refuses reports without `report_id` :216-220 or `policy_published/domain` :221-223); ingest `dmarc.go:251-279`; idempotent store `dmarc.go:350-381` (`OnConflict DoNothing`, keyed user+report_id+source_ip); decompression cap `dmarc.go:326-345` (`maxDecompressedReportBytes = 16 << 20` at :330, `readBounded` :336-345, `maybeGunzip` :314-324); hook points `internal/services/inbound/service.go:304` (Ingest) and `:486` (ApplyParsed); wiring `cmd/posta/server.go:404-406` (`attachDMARC` → `EnableDMARC`) and `cmd/posta/worker.go:219` (parse path), both gated on `cfg.InboundEnabled` (`cmd/posta/worker.go:193`). DB: `internal/models/dmarc.go:59` (`dmarc_reports`), AutoMigrate `internal/storage/migration/migration.go:46`. API: `GET /domains/{id}/dmarc` registered `internal/routes/workspace_resource_routes.go:959-968`, handler `internal/handlers/dmarc_handler.go:85-119` (workspace-scoped 404 `:72-79`, audit-logged `:110-117`). Frontend: `web/src/api/domains.ts:61-62`, `web/src/views/domains/Domains.vue:341-368` (loader), `:678+` (render), spec `web/src/views/domains/Domains.spec.ts:251` (renders summary+records) and `:277` (empty state). Send: no aggregate-report generation/dispatch exists (grep `aggregate|rua` over `internal/`+`cmd/`: only ingest/read-view hits; no DMARC task type in `internal/worker/tasks.go`). |
| Evidence | Tests named from source (not executed): `internal/services/inbound/dmarc_test.go` — `TestMaybeGunzipBoundsDecompressedSize` :98, `TestMaybeGunzipRoundTrip` :117, `TestAttachmentContentBoundsBlobRead` :137, `TestAttachmentContentBlobGzipHappyPath` :147, `TestDMARCStoreIsIdempotent` :427, `TestDMARCStoreKeepsDistinctSourceIPs` :467, `TestDMARCProcessIngestsGzipAndSkipsOversize` :497; `internal/services/provider_events/dmarc_test.go` — `TestParseDMARCGoogleFixture` :136, `TestParseDMARCEmptyReport` :187, `TestParseDMARCGzippedRoundTrip` :204, `TestParseDMARCRefusals` :228, `TestParseDMARCAlignmentCaseInsensitive` :250, `TestDMARCReportRowShape` :276; `internal/handlers/dmarc_handler_test.go` — `TestDMARCGetSummary` :731, `TestDMARCGetForeignDomainNotFound` :816, `TestDMARCGetEmptySummary` :848. Ledger: `docs/project/FEATURE_INVENTORY.md:67` (RF-F028, R-17, In Progress), `docs/project/STATUS_HISTORY.md:31` (commit `f5f0e92`). |
| Notes | Two documented limitations verified in code: (1) ingestion runs only when the inbound receiver is enabled (`cfg.InboundEnabled`, default-off — FEATURE_INVENTORY.md:67 says the same); (2) reports for non-verifying domains are still stored (no domain match gate in `dmarc.go:350-381`). Pre-seed status fields and the PARTIAL split are accurate. The 16 MiB cap and workspace-scoped read surface match the pre-seed Security/DB/API claims. |

### SF-26 — SPF/DKIM auth-failure (ARF, RFC 5965/6591) reports
| Field | Value |
|---|---|
| Final status | **MISSING → PLANNED (Phase 3)** — pre-seed CONFIRMED |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None. No ARF/RFC 5965/6591 code in the tree (grep `arf|5965|6591`: zero relevant hits — the only "failure reports" string is DNS guidance text at `internal/services/domain/health.go:238`; the security engine's "auth-failure" naming is unrelated). |
| Evidence | Depends on SF-22/23 (verified missing); no rate limiting or loop-suppression machinery for reports exists. |
| Notes | The only report format parsed anywhere is the RFC 7489 aggregate (SF-25 receive). |

### SF-27 — ARC verification (no sealing)
| Field | Value |
|---|---|
| Final status | **BLOCKED BY ADR-002 (OB-2)** — pre-seed CONFIRMED |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None. Grep `arc`: the only hit is a blocked-header prefix on the send path (`internal/services/email/service.go:1547`) — header hygiene, not ARC. |
| Evidence | Blocking record now has a real artifact: `docs/architecture/ADR-002-inbound-smtp-routing.md:19` explicitly places **ARC verification (SF-27)** and iprev (SF-32) in the OB-2 phase as config-gated annotation steps (never reject by default). Ledger consistency: gap-analysis §2 row "SF-27–32 … NOT APPLICABLE … DO-NOT" and §4a SF-27 N/A; OB-2 row lists SF-27 (`REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md:44`). |
| Notes | ADR-002 is untracked in git (created during this audit window); treat its presence as current-tree state, not committed history. |

### SF-28 — DKIM2 verification (chained, envelope-bound) + forged-DSN protection
| Field | Value |
|---|---|
| Final status | **BLOCKED BY ADR-007 (OB-6)** — pre-seed CONFIRMED, with caveat |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None. Grep `dkim2`: zero hits. |
| Evidence | Code absence verified. The governing ADR-007 does **not exist** in the tree yet (`todo.md:193,197` lists it as open; `docs/architecture/` contains only ADR-001/002). The OB-6 phase row maps SF-28 to ADR-006/007/008/009 (`REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md:47`). |
| Notes | The block is real (draft-spec feature, no code, no owner ADR) but "BLOCKED BY ADR-007" should be read as "blocked pending ADR-007", not "decided by an existing ADR-007". Same caveat applies to SF-31. |

### SF-29 — MTA-STS client + local policy server
| Field | Value |
|---|---|
| Final status | **BLOCKED BY ADR-001/007 (OB-1/6)** — pre-seed CONFIRMED, with caveat |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None. Grep `mta-sts|mtasts`: zero hits in Go code. |
| Evidence | ADR-001 exists (`docs/architecture/ADR-001-outbound-mta.md`) and covers the direct-to-MX surface an MTA-STS client would ride; it records at `:20` that "DNSSEC validation is not adopted … revisit with ADR-007". ADR-007 itself is pending (`todo.md:193`). OB-1 row maps SF-29 to ADR-001 (`REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md:42`). |
| Notes | The MTA-STS client (outbound) would land with OB-1; the local policy server is inbound (OB-2 surface) and MX-record guidance is tracked separately (RF-F023, To-dos — `docs/project/FEATURE_INVENTORY.md:62`). |

### SF-30 — DANE (TLSA) with DNSSEC
| Field | Value |
|---|---|
| Final status | **BLOCKED BY ADR-001 (OB-1/6)** — pre-seed CONFIRMED |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None. Grep `tlsa|dane`: zero hits. |
| Evidence | Strongest ADR anchor of the group: `docs/architecture/ADR-001-outbound-mta.md:20` — "DNSSEC validation is not adopted (no validating resolver dependency; recorded limitation, **revisit with ADR-007**)." DANE is impossible without DNSSEC validation, so the record's block is directly supported by the existing ADR-001 text even though the depth-governing ADR-007 is pending. |
| Notes | No validating-resolver dependency in `go.mod` (no dnssec/dns libraries beyond defaults), consistent. |

### SF-31 — TLS-RPT (outbound build/send + inbound parse)
| Field | Value |
|---|---|
| Final status | **BLOCKED BY ADR-007 (OB-6)** — pre-seed CONFIRMED, with caveat |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None. Grep `tls-rpt|tlsrpt`: zero hits. |
| Evidence | Code absence verified. ADR-007 does not exist yet (`todo.md:193`); OB-6 row maps SF-31 to ADR-006/007/008/009 (`REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md:47`). Outbound TLS-RPT reporting would also presuppose delivery-truth data (R-04 events) in the direct-to-MX world. |
| Notes | Same "blocked pending ADR-007" reading as SF-28. The only SMTP report machinery anywhere is the DMARC aggregate receive path. |

### SF-32 — Reverse DNS (iprev) validation
| Field | Value |
|---|---|
| Final status | **BLOCKED BY ADR-002 (OB-1 PTR guidance / OB-2)** — pre-seed CONFIRMED |
| Fields | Backend Missing · Frontend — · DB Missing · API Missing · Security Missing · Tests Missing · Docs Missing |
| Implementation | None. No `net.LookupAddr` / reverse-DNS code in the tree (grep zero). |
| Evidence | `docs/architecture/ADR-002-inbound-smtp-routing.md:19` explicitly includes **iprev validation (SF-32)** in the OB-2 phase (annotation-only, never reject by default). OB-2 row lists SF-32 (`REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md:44`); gap-analysis §4a SF-32 N/A / DO-NOT group. PTR ownership guidance is deferred per OB-1, matching the record's "OB-1 PTR guidance / OB-2" split. |
| Notes | No partial iprev surface (the security engine keys signals by IP but never resolves PTRs). |

### SF-33 — TLS server stack (STARTTLS, SNI wildcard fallback)
| Field | Value |
|---|---|
| Final status | **PARTIAL** — implemented slice verified; SNI wildcard fallback + automated cert management absent (SF-34 territory, **BLOCKED BY ADR-007/009 (OB-6)**, ADR-009 pending per `todo.md:195`) |
| Fields | Backend Partial · Frontend — · DB — · API — (env-config only) · Security Verified · Tests Partial · Docs Verified (implemented slice) |
| Implementation | Inbound receiver: `internal/services/inbound/smtp_server.go:191-222` — TLSMode `none|starttls`; `starttls` loads one static keypair (`:209`), sets `MinVersion: tls.VersionTLS12` (`:216`); go-smtp advertises STARTTLS once `TLSConfig` is set. Relay listener: `internal/services/smtprelay/smtp_server.go:54-69` — same shape; `starttls` mode never enables `AllowInsecureAuth` (default false → AUTH withheld until TLS), `none` preserves plaintext private-network AUTH (`:56-57`). Outbound relay client: `internal/services/email/smtp_sender.go:188` (`tls.Config{ServerName: server.Host}`), `:210-244` (`dialSMTP`: implicit TLS vs STARTTLS upgrade), `:265-268` (LOGIN refused on unencrypted non-localhost connections). Config: `internal/config/config.go:425-427,445-447` (env vars) with validation `:480-497`; both listeners started at `cmd/posta/server.go:450` and `:497`. |
| Evidence | Absence verified: no `GetCertificate`/SNI multi-cert fallback, no ACME/autocert/certmagic anywhere (grep hits for "sni" were only `Snippet`/`nosniff` false positives). Tests named from source: `internal/services/smtprelay/smtp_server_test.go` — `TestSMTPRelaySTARTTLS` :23 (asserts STARTTLS advertised, AUTH withheld pre-TLS, AUTH PLAIN offered post-TLS, TLS 1.2 floor) and `TestSMTPRelayTLSConfiguration` :64 (plaintext config unchanged; missing cert and invalid mode rejected); outbound `internal/services/email/smtp_sender_test.go` — `TestAuthLoginRefusesUnencryptedConnection` :129. **No TLS test exists for the inbound receiver** (no `smtp_server_test.go` in `internal/services/inbound/`) — the pre-seed "Tests: Partial" is accurate for that reason. Docs: `docs/docs/getting-started/configuration.md:165-167` documents the inbound TLS vars; `docs/docs/security/smtp-relay.md:23-25` documents the relay TLS vars plus a transport-security warning. |
| Notes | Two caveats against "Security: Verified" being read too broadly: (1) inbound receiver sets `AllowInsecureAuth = true` (`smtp_server.go:199`) — AUTH-over-plaintext is possible in `none` mode by design (private-network posture, matching the relay doc warning); (2) STARTTLS on both listeners is opportunistic config, not enforcement, and a single static certificate means no SNI wildcard fallback — the exact residual the record assigns to SF-34. Docs upgraded from pre-seed "Partial" to "Verified (implemented slice)" on the strength of the two doc hits above; cert provisioning/rotation guidance beyond env references does not exist. |

---

## Cross-record conclusions

1. **SF-22/23/24 (message-level authentication) — all three MISSING, confirmed by exhaustive grep.** The only SPF/DKIM/DMARC code in the tree is (a) DNS *presence* checking for domain verification and R-07 health (`internal/services/domain/verifier.go:85-139`, `internal/services/domain/health.go`), and (b) DKIM *signing* on send (`internal/services/email/dkim_signer.go:116-132`). No `dkim.Verify` call, no SPF policy evaluation, no Received-SPF/Authentication-Results generation, no DMARC policy enforcement on received mail. The pre-seed records drew exactly this distinction and it holds.
2. **SF-25 receive side is genuinely COMPLETE as claimed** — parser, 16 MiB decompression cap (`dmarc.go:330`), idempotent per-record storage, both server (`server.go:405`) and worker (`worker.go:219`) wiring, read endpoint (`workspace_resource_routes.go:959-968`), dashboard view + spec, and 16 named test functions across three packages. Send side absent. Registry (RF-F028) keeps "In Progress" for the trend-surfacing residual — consistent with PARTIAL.
3. **ADR-blocked records (SF-27..32) are code-absent as recorded.** ADR-002 exists (untracked) and explicitly defers SF-27/SF-32; ADR-001 exists and defers DNSSEC/DANE to ADR-007. **ADR-007 and ADR-009 do not exist yet** (`todo.md:187-197` open) — for SF-28/SF-31 (and the SF-33 SNI/ACME residual) the accurate reading is "blocked pending ADR-007/009". Their code-absence and OB-phase mapping are verified regardless.
4. **Not verifiable in this session (no toolchain):** execution of `go test ./... -count=1`, `go vet`, golangci-lint, dashboard build; runtime behavior of both SMTP listeners and the STARTTLS handshake paths; frontend runtime rendering. All tests cited were located and read, not run.
5. **Tree hygiene:** only `docs/audits/verification/V-B1.md` was written by this audit. Pre-existing dirty/untracked state observed and untouched (`docs/README.md` modified; `docs/architecture/ADR-001-outbound-mta.md`, `docs/architecture/ADR-002-inbound-smtp-routing.md`, `docs/audits/` untracked; `docs/audits/verification/V-DOCS.md` created by another auditor).
