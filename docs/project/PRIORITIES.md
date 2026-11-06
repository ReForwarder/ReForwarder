# PRIORITIES — priority tiers and current ordering

Priority ordering of the top work, produced by run `dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7` from the audit gap view at HEAD `f5f0e921455de901370b5f37b7112b634f6c2066`. P0 first, always. Re-ordering happens by a new producing run appending to `STATUS_HISTORY.md` and replacing §3 below — not by ad-hoc edits.

## 1. Priority definitions

Tier definitions, verbatim from the priority legend at `docs/product-roadmap/05-implementation-roadmap.md:25`:

> **P0** critical trust/correctness hardening · **P1** high — signals and sender value · **P2** medium — competitor parity · **P3** future long tail.

| Tier | Definition | Phase mapping (todo.md) |
| --- | --- | --- |
| **P0** | Critical trust/correctness hardening | Phase 0 — trust & correctness hardening (`todo.md:405`) |
| **P1** | High — signals and sender value | Phase 1 — signals & sender value (`todo.md:436`) |
| **P2** | Medium — competitor parity | Phase 2 — parity batches (`todo.md:456`) |
| **P3** | Future long tail | Phase 3 — long tail (`todo.md:521`) |

**Assignment method** (`docs/product-roadmap/05-implementation-roadmap.md` §2): weighted scoring across six 1–5 dimensions — UV 0.20, II 0.25, SR 0.30, CA 0.10, CX −0.10, AD −0.05; formula at lines 47–54 — with the **security/reliability override rule**: an item drafted P0 stays P0 even if its numeric score would sort below P1 items (line 56; e.g. GAP-11 at score 2.85 sits in Phase 0 beside score-3.40 GAP-01, while GAP-17 at 1.50 stays P3 despite a P2-level score). Phase sequence is P0 → P1 → P2 → P3 (`05-implementation-roadmap.md:929`).

**Scope note:** the definitions were originally assigned to the 53-gap Track B register (P0×5, P1×10, P2×22, P3×16 — `todo.md:39`). The ordering in §3 is the broader consolidated gap view of run `dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7` — **76 items: 6 P0 / 20 P1 / 31 P2 / 19 P3** — which additionally folds in Track A (Option B) programme items, module-audit items (frontend-integration, smtp-relay, platform-admin-security), and residual completions of In Progress features. In Progress and To-dos features are represented; Done features are not.

## 2. Ordering rules

- P0 items are always worked first; no P1 starts while a P0 is open.
- Within a tier, the order in §3 is the gap view's order of record.
- Priority assignment changes only via a new producing run (ledger entry + replacement of §3).

## 3. Current ordering of the top work

**Note on ADR refs:** `ADR-001`…`ADR-009` cited below are the Option B Phase 0 deliverables (target path `docs/architecture/ADRs/`, one backlog item per ADR: ADR-001→RF-B102, ADR-002→RF-B103, ADR-003→RF-B104, ADR-004→RF-B105, ADR-005→RF-B106, ADR-006→RF-B107, ADR-007→RF-B108, ADR-008→RF-B109, ADR-009→RF-B110). They do **not** exist on disk yet — those refs name future documents until the Phase 0 programme completes.

### P0 — do first (6 items)

| # | Work item | Refs |
| --- | --- | --- |
| 1 | Fix transactional scheduled sends end-to-end: cancel endpoint + reconcile coverage | RF-F048, GAP-01, INF-014, R-26, todo.md Track B Phase 0, 04-feature-gap-analysis.md §2.1 |
| 2 | Finish legacy `/webhooks/bounce` parity: suppression, bounce rows, complaint semantics | RF-F053, GAP-02, ANA-003, ANA-005, INF-021, API-014, todo.md Track B Phase 0 (GAP-02 residual), 04-feature-gap-analysis.md §2.1 |
| 3 | Emit and surface true delivered state (`email.delivered` emission, delivered analytics, dead UI paths) | RF-F099, GAP-02 residual, ANA-003, ANA-005, todo.md Track B Phase 0, upcomming-convo.md §8 rule 3 |
| 4 | Add a server-side editor role gate to automation write and event-ingest routes | GAP-11, SEC-002, AUT-003, todo.md Track B Phase 0, 04-feature-gap-analysis.md §2.3 |
| 5 | Enforce the `two_factor_required` policy setting at login | RF-F006, GAP-12, SEC-005, R-10, todo.md Track B Phase 0, 04-feature-gap-analysis.md §2.3 |
| 6 | Add multi-replica cron leader election (Redis lease) | GAP-03, OBS-019, R3, todo.md Track B Phase 0, 04-feature-gap-analysis.md §2.1 |

### P1 — next (20 items)

| # | Work item | Refs |
| --- | --- | --- |
| 1 | Complete 2FA recovery codes (consume, regeneration, admin reset) | RF-F005, R-10, GAP-12, todo.md §0.2 |
| 2 | Complete Passkeys/WebAuthn enrollment and login | RF-F014, R-19, commit 0f52636, commit 346eb67 |
| 3 | Produce the Option B architecture programme (re-audit, ADR-001..009, architecture docs) | upcomming-convo.md §7 Phase 0, §11, §12, todo.md Track A Phase 0 |
| 4 | Build the outbound MTA foundation (direct-to-MX + upstream relay modes) | upcomming-convo.md §6 outbound requirements, §7 Phase 1, todo.md Track A Phase 1, ADR-001, INF-020, DOM-013 |
| 5 | Extract the Sender seam and add ESP API integrations with failover and per-send server selection | GAP-04, INF-022, API-015, INF-023, INF-024, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.1 |
| 6 | Support caller-supplied Idempotency-Key on send endpoints | GAP-05, INF-031, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.1 |
| 7 | Add auto-pause circuit breaker on failure and complaint thresholds | GAP-06, INF-016, DOM-011, ANA-010, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.2 |
| 8 | Add signed timestamp replay protection to outbound webhook signatures | GAP-14, API-010, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.3 |
| 9 | Wire security_events retention enforcement (DeleteOlderThan caller) | RF-F091, GAP-13, SEC-008, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.3 |
| 10 | Resolve tracking toggle asymmetry for the campaign open pixel (product decision gate) | RF-F100, GAP-15, ANA-012, todo.md Track B Phase 1 (product-decision gate), 04-feature-gap-analysis.md §2.3 |
| 11 | Remove the dead Contact Lists surface end-to-end (D1 consolidation) | RF-F101, D1, module audit (frontend-integration), todo.md D1–D9, 04-feature-gap-analysis.md §4 |
| 12 | Implement double opt-in (DOI) confirmation flow | RF-F075, GAP-25, CON-004, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.5 |
| 13 | Add campaign test-send and campaign-scoped preview | GAP-26, CAM-007, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.5 |
| 14 | Add CSV exports for subscribers, lists, segments, and analytics/message history | GAP-27, CON-010, ANA-011, RF-F077, todo.md Track B Phase 1, 04-feature-gap-analysis.md §2.5 |
| 15 | Build the inbound mail routing programme (validation, aliases, spam/malware, quarantine, bounce/return-path) | upcomming-convo.md §6 inbound requirements, §7 Phase 2, todo.md Track A Phase 2, ADR-002, ADR-006, GAP-47, INF-020 |
| 16 | Add per-domain inbound routing rules (alias/catch-all/forward targets) | RF-F040, upcomming-convo.md §6 inbound requirements, module audit (smtp-relay) |
| 17 | Build the mailbox & storage foundation (folders, flags, quotas, MIME store) | upcomming-convo.md §4 mailbox storage, §7 Phase 3, todo.md Track A Phase 3, ADR-003, ADR-008 |
| 18 | Deliver IMAP and POP3 access against the mailbox backend | upcomming-convo.md §7 Phase 4, todo.md Track A Phase 4, ADR-004 |
| 19 | Build webmail and mailbox administration | upcomming-convo.md §7 Phase 5, todo.md Track A Phase 5, ADR-005 |
| 20 | Add IP pools, warm-up controls, and reputation monitoring | upcomming-convo.md §7 Phase 6, §8, todo.md Track A Phase 6, INF-025, ADR-007 |

### P2 — then (31 items)

| # | Work item | Refs |
| --- | --- | --- |
| 1 | Complete domain deliverability health / diagnostics | RF-F026, R-07, GAP-08, todo.md §0.2 |
| 2 | Complete strict verified-domain sending policy (campaign-path parity, per-domain caps) | RF-F027, R-23, GAP-10, DOM-012, todo.md Track B Phase 2 Batch A |
| 3 | Complete DMARC aggregate report (rua) ingestion | RF-F028, R-17, commit f5f0e92, 04-feature-gap-analysis.md §6.2 (superseded) |
| 4 | Deepen DNS record guidance (SPF/DMARC parsing, MX records, drift re-verification) | RF-F023, GAP-08, DOM-005, DOM-006, DOM-008, todo.md Track B Phase 2 Batch A, upcomming-convo.md §8 DNS row |
| 5 | Add custom tracking domain | RF-F029, GAP-09, DOM-009, todo.md Track B Phase 2 Batch A |
| 6 | Add subscriber tags | RF-F076, GAP-29, CON-006, todo.md Track B Phase 2 Batch B |
| 7 | Add bulk contact operations (delete/status/list add-remove by IDs or segment) | GAP-31, CON-011, todo.md Track B Phase 2 Batch B, 04-feature-gap-analysis.md §2.5 |
| 8 | Add contact upsert/sync API | GAP-19, CON-012, todo.md Track B Phase 2 Batch B, 04-feature-gap-analysis.md §2.4 |
| 9 | Add contact activity timeline and unsubscribe-reason reporting | GAP-32, CON-019, CON-015, todo.md Track B Phase 2 Batch B, 04-feature-gap-analysis.md §2.5 |
| 10 | Persist automation ab_test node outcomes (GAP-28 residual) | GAP-28 residual, AUT-008, RF-F059, todo.md Track B Phase 2 Batch C |
| 11 | Add automation workflow dry-run / test tooling | RF-F071, GAP-41, AUT-018, todo.md Track B Phase 2 Batch D |
| 12 | Add re-engagement and scheduled enrollment triggers | GAP-42, AUT-015, todo.md Track B Phase 2 Batch D, 04-feature-gap-analysis.md §2.7 |
| 13 | Implement stop-on-reply for automations | GAP-43, AUT-016, todo.md Track B Phase 2 Batch D, 04-feature-gap-analysis.md §2.7 |
| 14 | Add reusable blocks/sections library and campaign content slots | GAP-34, TPL-002, TPL-012, CAM-006, todo.md Track B Phase 2 Batch C |
| 15 | Consolidate segmentation onto SegmentCondition; convert and deprecate FilterRules (D3) | D3, todo.md D1–D9, 04-feature-gap-analysis.md §4 |
| 16 | Add subscriber signup forms (form-to-subscriber capture) | RF-F074, GAP-30, CON-017, todo.md Track B Phase 2 Batch B (DOI blocks forms) |
| 17 | Wire campaign duplication UI and add campaign-level failed-recipient retry | RF-F104, GAP-33, CAM-004, CAM-018, module audit (frontend-integration) |
| 18 | Remove or wire dead admin API client methods (listApiKeys / revokeApiKey / listEmails) | RF-F102, module audit (frontend-integration) |
| 19 | Expose backend-only endpoints in the UI (segments contacts, message analytics & assign, automation custom-event ingest, admin provider breakdown) | RF-F103, module audit (frontend-integration) |
| 20 | Add webhook test-fire endpoint | RF-F056, GAP-18 residual, API-013, todo.md Track B Phase 2 Batch E |
| 21 | Add bounce classification analytics surface (classification counts) | RF-F057, GAP-07 residual, ANA-004, todo.md Track B Phase 2 Batch A |
| 22 | Add per-queue latency metric and pause/throttle controls | GAP-48, OBS-009, todo.md Track B Phase 2 Batch E, todo.md Track A Phase 1 (folds in) |
| 23 | Add blob usage reporting and orphan GC | GAP-45, OBS-013, todo.md Track B Phase 2 Batch E |
| 24 | Document Redis HA guidance and SPOF mitigation | GAP-49, OBS-004, R10, todo.md Track B Phase 2 Batch E, upcomming-convo.md §7 Phase 6 (DR) |
| 25 | Add admin-console IP allowlisting | RF-F092, GAP-16, SEC-013, todo.md Track B Phase 2 Batch E |
| 26 | Add error-monitoring hook | RF-F097, GAP-51, OBS-011, todo.md Track B Phase 2 Batch E |
| 27 | Complete system-info endpoint feature flags (Section-11 gate) | RF-F081, commit 346eb67, commit 0f52636, module audit (platform-admin-security) |
| 28 | Complete API route contract gate (golden file + frontend spec) | RF-F082, R-21, commit 346eb67, module audit (platform-admin-security) |
| 29 | Add runtime authorization verification tests (admin queue scoping, API-key/admin isolation) | A-B NV-2, G-H NV-4, 04-feature-gap-analysis.md §5, todo.md Sources (04 §5 NV register) |
| 30 | Automate DNS provisioning and certificate lifecycle (ACME/SNI per-domain TLS) | upcomming-convo.md §7 Phase 6, todo.md Track A Phase 6, DOM-007, DOM-010, ADR-007 |
| 31 | Complete deployment, upgrade, backup, and DR tooling | upcomming-convo.md §7 Phase 6, §8 data durability, todo.md Track A Phase 6, ADR-009, RF-F095 |

### P3 — later (19 items)

| # | Work item | Refs |
| --- | --- | --- |
| 1 | Build preference center (multi-list subscription management) | RF-F073, GAP-35, CON-016, todo.md Track B Phase 3 |
| 2 | Add UTM auto-tagging of campaign links | RF-F061, GAP-37, CAM-015, todo.md Track B Phase 3 |
| 3 | Complete template authoring: preview text, Markdown part, media library (verify-first gate) | GAP-36, TPL-016, TPL-003, TPL-022, todo.md Track B Phase 3 (verify-first gate) |
| 4 | Add newsletter archive and publications | GAP-38, CAM-012, CAM-013, todo.md Track B Phase 3 |
| 5 | Add device/geo breakdowns (ANA-015) | GAP-40, ANA-015, todo.md Track B Phase 3, 04-feature-gap-analysis.md §2.6 |
| 6 | Add global registration control toggle (SEC-017) | GAP-17, SEC-017, todo.md Track B Phase 3 (admin tail), 04-feature-gap-analysis.md §2.3 |
| 7 | Complete admin impersonation (guardrails, audit, UI flag) | RF-F015, GAP-46, SEC-015, R-20, commit 0f52636, commit 346eb67 |
| 8 | Deepen SMTP diagnostics (EHLO capability dump, TLS cert summary, redacted transcript) | GAP-20, INF-033, ANA-013, todo.md Track B Phase 3 (→ OB Phase 1 ops) |
| 9 | Add message tagging/correlation (tags on emails) | GAP-21, INF-032, todo.md Track B Phase 3 |
| 10 | Ship an operator CLI against the REST API | GAP-22, API-017, todo.md Track B Phase 3 |
| 11 | Verify SDK completeness (PyPI/NuGet packages + route coverage) | GAP-23, API-008, todo.md Track B Phase 3 |
| 12 | Ship an MCP server over the REST API | GAP-24, API-016, todo.md Track B Phase 3 |
| 13 | Add antivirus (ClamAV) hook to the inbound pipeline | GAP-47, SEC-011, todo.md Track B Phase 3 (→ OB Phase 2 malware scanning), upcomming-convo.md §8 malware row |
| 14 | Ship a Grafana sample dashboard | GAP-52, OBS-007, todo.md Track B Phase 3 |
| 15 | Add runtime log viewer and resource history | GAP-53, OBS-010, OBS-012, todo.md Track B Phase 3 |
| 16 | Close the open NEEDS_VERIFICATION register (correctness/ops items) | 04-feature-gap-analysis.md §5 (NV register), todo.md Sources (04 §5) |
| 17 | Frontend consolidation batch: audit-UI scope switcher, MessageDetail merge, ChangePassword removal, Suppressions nav (D2/D5/D6/D8) | D2, D5, D6, D8, todo.md D1–D9, 04-feature-gap-analysis.md §4 |
| 18 | Document intentional dualities (analytics split D4, tracking/email events D7, admin SSE channels D9) | D4, D7, D9, todo.md D1–D9, 04-feature-gap-analysis.md §4 |
| 19 | Evaluate JMAP and advanced integrations on validated demand | upcomming-convo.md §4 JMAP, §7 Phase 6, todo.md Track A Phase 6, INF-003 |
