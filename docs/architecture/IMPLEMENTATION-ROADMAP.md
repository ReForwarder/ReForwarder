# Implementation Roadmap — Option B Execution Plan

**Status:** Accepted (2026-11-07) · Tracker of record: `todo.md` Track A + `docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md` (feature ledger) · This doc sequences the work and names each phase's exit gate.

## 0 — ADR gate (COMPLETE 2026-11-07)

Deliverables: [ADR-001…009](ADR-001-outbound-mta.md) + [OPTION-B-ARCHITECTURE](OPTION-B-ARCHITECTURE.md), [COMPONENT-MATRIX](COMPONENT-MATRIX.md), [DATA-OWNERSHIP](DATA-OWNERSITY.md), [MESSAGE-LIFECYCLE](MESSAGE-LIFECYCLE.md), [MIGRATION-PLAN](MIGRATION-PLAN.md), this doc, [RISK-REGISTER](RISK-REGISTER.md). No prod code/schema/deps (honored — docs only). Parity-register mail-server records: BLOCKED BY ADR → PLANNED.

## 1 — Outbound MTA foundation

Build order within the phase: (a) `mta_delivery_attempts` migration + attempts recorder; (b) MX resolver with cache + null-MX/implicit-MX; (c) direct-mode session runner extending `dialSMTP` (STARTTLS-enforced negotiation, per-domain budgets); (d) per-domain delivery-mode config + API + Domains UI; (e) submission listener 587 (gated) with app-password auth prep; (f) PTR-guidance surface; (g) Redis leader lease for multi-replica crons (GAP-03) + Redis HA runbook (GAP-49). **Parallel relay-track wave 1 alongside:** SF-10 DSN parser groundwork (ADR-002 dependency), SF-22/23/24 verification engines, GAP-05 idempotency-key, GAP-15 toggle symmetry.

**Exit gate:** battery green + named tests (STARTTLS enforcement, throttle deferral, exactly-once under multi-worker, fail-closed unverified sender, null-MX) + register updates + visual acceptance of the new Domains controls + commit/push.

## 2 — Inbound mail & routing

(a) `inbound_routes` + routing engine at RCPT/DATA; (b) forwarding (confirmed targets → system messages through Phase 1 pipeline); (c) quarantine store + Inbound views; (d) return-path subdomain + VERP + DSN parser (F12); (e) ARC/iprev annotations (config-gated); (f) spam baseline (rules + RBL) + clamd (ADR-006) + GTUBE self-test; (g) filter webhook hook. **Parallel wave 2:** SF-26 ARF reports, GAP-02 residual (bounce/return-path completion), GAP-18 webhook test-fire.

**Exit gate:** battery + routing matrix tests + durable-path proof + quarantine release flows + register + visuals (Inbound Routes, Quarantine) + commit/push.

## 3 — Mailstore foundation

(a) mailstore service (folders/UID/flags CAS); (b) blob content-addressing + encryption + dedup/GC (GAP-45); (c) FTS; (d) quotas; (e) unified retention sweep (GAP-13). **Parallel wave 3:** SF-52 patch/destroy, SF-54 pause/resume, SF-56 tracing SSE, SF-53 transcript surfacing, SF-100 settings history, CA-02/03/06, expression unification SF-50/103.

**Exit gate:** battery + UID/flag/quota/GC tests + register + commit/push.

## 4 — IMAP + POP3

(a) app passwords; (b) IMAP server command set per ADR-004 (IDLE included); (c) POP3; (d) TLS via ADR-007 cert lifecycle (SNI); (e) limits + audit; (f) protocol harness.

**Exit gate:** battery + command-matrix harness + interop smoke + register + commit/push.

## 5 — Webmail + mailbox administration

(a) `/api/v1/mail/*` REST; (b) Vue mail area (list/view/compose/settings); (c) admin provisioning/suspend/recover + quotas; (d) attachments via ADR-006 scan; (e) visual acceptance gate (full screenshot pass — this is the user-facing phase).

**Exit gate:** battery + handler isolation battery + vitest + visuals + register + commit/push.

## 6 — Convergence

(a) DNS automation (providers, drift); (b) ACME/SNI; (c) IP pools + warm-up + reputation panels; (d) MTA-STS + TLS-RPT; (e) DANE/DKIM2 evaluations (gated); (f) observability completion (OTLP, alerts, GAP-51/52); (g) roles/tenants/SCIM revisit (only if approved); (h) JMAP evaluation (RF-B126). **Final:** Phase 7 full acceptance battery + docs reconciliation + parity-register freeze + FINAL_VERIFICATION_REPORT.

**Exit gate:** the programme's final completeness gate (master prompt Part 9): every in-scope register record COMPLETE with evidence or explicitly PLANNED with its phase; battery + DB-tier + docs build + visual acceptance green; push.

## Cadence & resumability

Phases are sequential; parallel waves integrate between phases (never concurrent on overlapping files). After every wave: parity-register update, todo.md Track A checkbox update, battery, commit (explicit paths), push. The register + tracker make any interruption resumable.
