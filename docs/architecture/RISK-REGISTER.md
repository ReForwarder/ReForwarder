# Risk Register — Option B Programme

**Status:** Accepted (2026-11-07) · Living doc; reviewed at every phase gate. Severity = impact × likelihood (H/M/L). Every risk names its mitigation and its detection signal.

| ID | Risk | Sev | Mitigation | Detection |
|---|---|---|---|---|
| RSK-1 | **IP reputation damage from direct-to-MX** — new senders on fresh IPs get junked; a misconfigured tenant spams and burns the IP | H | Direct mode is per-domain opt-in after verification; per-domain budgets + warm-up schedules (Phase 6) gate volume ramp; PTR-guidance surface; relay remains default; per-domain IP binding isolates blast radius | `mta_delivery_attempts` deferral/bounce rates per IP+domain; R-07 health panels; provider-event complaint rates |
| RSK-2 | **Open-relay regression** on any new listener (submission/IMAP/POP3) | H | All listeners auth-gated + policy-checked at the listener boundary; non-negotiable #1 asserted by named fail-closed tests per listener; security engine covers every port (connection budgets, bans) | listener test matrix (unauthenticated RCPT must fail); security events; CI test gate |
| RSK-3 | **Tenant-isolation hole in new tables** | H | ADR-008 migration checklist (workspace column + index + query filter + cross-tenant test) enforced per PR; reviewer checklist in the register | handler isolation battery; grep gate for unfiltered repo queries |
| RSK-4 | **Delivered-truth regression** (reporting queue acceptance as delivered) | H | Non-negotiable #3 encoded in the delivery runner (2xx-only) + provider events; tests assert `delivered` never precedes MX 2xx/provider event | F1/F2 test harness; email_events audit queries |
| RSK-5 | **Storage growth / mailstore bloat** (blobs + events unbounded) | M | Unified retention cron per family (ADR-008 §5); blob GC (GAP-45); per-mailbox quotas; usage reporting | disk metrics; quota-rejection events; GC sweep logs |
| RSK-6 | **Protocol-compatibility breakage** (IMAP/POP3 clients) | M | Command-matrix harness + real-client interop smoke per ADR-004; conservative subset first (IMAP4rev1 core); UID semantics tested under concurrency | harness failures in CI; user reports |
| RSK-7 | **ClamAV latency/stability** blocks ingest | M | Scans run in ingest worker (not SMTP session); fail-open/fail-closed config; timeout + health check in `doctor` | scan-duration metrics; quarantine queue depth |
| RSK-8 | **DNS provider token abuse** | M | Encrypted at rest, masked in API, write-scoped guidance, per-domain revocation, audited changes | audit events; publish failures |
| RSK-9 | **Redis SPOF** (queues, locks, fabric) | M | GAP-49 runbook (managed Redis HA, replication); fail-open semantics documented per limiter; Postgres backstop durability for queued work | `doctor` Redis probe; delivery lag alarms (Phase 6) |
| RSK-10 | **Multi-replica race** (crons double-firing, claim contention) | M | Redis leader lease for crons (GAP-03); CAS/SKIP-LOCKED claims (R-26) already replica-safe; concurrency battery extended per phase | exactly-once tests; leader-lease tests |
| RSK-11 | **Scope/security drift under parallel waves** (two agents touching overlapping files; a protection weakened for convenience) | M | Strict file-ownership per wave; preservation list asserted by existing tests (SSRF guard, suppression, webhook authz) re-run in every battery; standing rule: never weaken to make implementation easier | battery; code review against preservation list |
| RSK-12 | **TLS/ACME mis-issuance or SNI mismatch** | M | autocert with per-domain SNI map; HTTP-01 through the existing server; certs cached in blob store; staging ACME first (env-gated Pebble tests) | cert-reload logs; `doctor` TLS probe |
| RSK-13 | **Webmail XSS / MIME attacks** | H | Sanitization pipeline reuse; remote-content blocked by default; sandboxed iframe rendering (Logs pattern); attachments scanned (ADR-006) before download; CSP review at Phase 5 visual gate | security review at phase gate; vitest + handler tests |
| RSK-14 | **Documentation drift** (register vs reality) | L | Parity register updated after every wave; single-source-of-truth rule (source register §5); Phase 6 reconciliation sweep + fresh-eyes reviewer | Phase 6 reconciliation; register Evidence fields |
| RSK-15 | **Provider quota / model failures kill long waves** | L | Waves are resumable (verdict files + register are on disk); retries are cheap; battery reruns are cache-warmed | wave completion notifications |

## Standing review rule

At every phase exit gate, this register is re-read; any new risk discovered during the phase is appended with its mitigation before the phase's commit. Risks that materialize become parity-register findings (fixed, not documented away).
