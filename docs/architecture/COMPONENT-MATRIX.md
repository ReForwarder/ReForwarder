# Component Matrix — Option B

**Status:** Accepted (2026-11-07) · Companion to [OPTION-B-ARCHITECTURE.md](OPTION-B-ARCHITECTURE.md). Maps every new/extended component to its ADR, phase, package, storage, and the parity-register features it delivers.

## 1. Components

| Component | Package | Phase | ADR | Storage | Delivers (SF) | New listeners/APIs |
|---|---|---|---|---|---|---|
| Outbound MTA (direct-to-MX) | `internal/services/mta` | 1 | 001 | `mta_delivery_attempts` (PG) | SF-01/02/05/06/12/17/21, MTA parts of 07/09/35/43/108 | direct delivery in worker tasks |
| Submission listener (MSA) | `internal/services/mta` | 1 | 001 | — | SF-03 | 587 gated listener |
| Delivery attempts ledger | `internal/services/mta` | 1 | 001 | PG | SF-13 (per-hop ids), SF-52 (diagnostics), SF-56 | — |
| PTR guidance | `internal/services/dnsmanager` | 1 | 001/007 | — | SF-32 (guidance slice) | domain-health extension |
| Inbound routing engine | `internal/services/inbound` (extend) | 2 | 002 | `inbound_routes` (PG) | SF-11 (aliases/catch-all/forward), SF-45/48 (policy hooks) | RCPT/DATA evaluation |
| Forwarding (system messages) | `internal/services/email` (extend) | 2 | 002 | `email` + `email_events` | SF-11 | — |
| Quarantine | `internal/services/inbound` (extend) | 2 | 002 | `inbound_quarantine` (PG) | SF-51 | dashboard Inbound views |
| Return-path / VERP + DSN parser | `internal/services/inbound` (extend) | 2 | 002 | `email_events` | SF-10, CA-05 | bounce RCPT allowlist |
| SPF/DKIM/DMARC verification engines | `internal/services/inbound` (new subpackage) | Phase-3 parallel | 002 | annotations + A-R header | SF-22/23/24, SF-49 A-R | — |
| ARF failure reports | `internal/services/inbound` (extend) | Phase-3 parallel | 002 | PG | SF-26 | — |
| Mailstore | `internal/services/mailstore` | 3 | 003 | `mailboxes`/`mail_folders`/`mail_messages` + `blob_refs` (PG) + blob.Store | SF-14 storage, SF-75/80/112 | — |
| Mailstore FTS | `internal/services/mailstore` | 3 | 003 | tsvector + GIN (PG) | SF-79 (depth) | — |
| Blob dedup/GC + usage | `internal/services/mailstore` | 3 | 003 | `blob_refs` | SF-77 (GAP-45) | — |
| Blob at-rest encryption | `internal/storage/blob` (extend) | 3 | 003 | blob.Store | SF-78, SF-18 | — |
| Retention sweep (unified) | `internal/cron/jobs` | 3 | 003/008 | settings-driven | SF-81 (GAP-13) | — |
| IMAP server | `internal/services/imap` | 4 | 004 | mailstore | SF-85 | 143/993 gated listeners |
| POP3 server | `internal/services/pop3` | 4 | 004 | mailstore | SF-86 | 110/995 gated listeners |
| App passwords | `internal/handlers` + model | 4 | 004 | PG (bcrypt) | SF-35/67 slice | Profile UI |
| Webmail SPA + REST | `web/src/views/mail/`, `internal/handlers/mail_handler.go` | 5 | 005 | mailstore | CA-01, SF-65/80 admin slices | `/mail` routes, `/api/v1/mail/*` |
| Mailbox administration | dashboard + handlers | 5 | 005 | PG | SF-72/80 surfaces | admin + self-service |
| Spam filter (rules + RBL) | `internal/services/spamfilter` | 2 | 006 | scores → events | SF-39 baseline, SF-42 (GTUBE), DNSBL | — |
| Malware (clamd) | `internal/services/spamfilter` | 2 | 006 | quarantine refs | GAP-47, SF-42 | clamd client |
| External filter hook | webhook machinery (extend) | 2 | 006 | — | SF-16 (filter stage) | synchronous webhook await |
| DNS automation | `internal/services/dnsmanager` | 6 | 007 | encrypted provider creds (PG) | SF-61 (GAP-08 drift) | provider adapters |
| Certificate lifecycle (ACME) | listener bootstrap + `dnsmanager` | 6 | 007 | blob cache | SF-34, SF-33 (SNI) | HTTP-01/DNS-01 |
| IP pools / warm-up / reputation | `internal/services/mta` + dnsmanager | 6 | 001/007 | PG + attempts data | SF-07 (pools), R-07 extension | — |
| MTA-STS + TLS-RPT | `internal/services/mta` + inbound | 6 | 007 | PG (reports) | SF-29/31 | policy path + ingest |
| DANE validation (evaluation) | `internal/services/mta` | 6 | 007 | — | SF-30 | — |
| DKIM2 (evaluation) | `internal/services/email` | 6 | 007 | — | SF-28 | — |
| Queue ops completion | `internal/handlers` + worker | Phase-3 parallel | 001/009 | asynq | SF-52 (patch/destroy), SF-54 (pause), SF-59 (actions) | admin API + UI |
| Settings history/rollback | handlers + repos | Phase-3 parallel | 008 | events substrate | SF-100 | admin API + UI |
| Metrics/observability completion | `internal/metrics` | 6 | 009 | — | SF-93 (OTLP), SF-96, CA-07 (GAP-51/52) | — |
| Roles/tenants/SCIM (evaluation) | various | 6 | 008 | PG | SF-66/71/72 | — |
| Redis HA runbook + leader lease | cron + docs | 1 | 009 | Redis | GAP-03/49 | — |
| Track B relay residuals | various | Phase-3 parallel | — | — | GAP-02/07/18/28 residuals, GAP-05 idempotency-key, GAP-06 circuit breaker, GAP-15 toggle symmetry | — |

## 2. Untouched components (contracts preserved)

`internal/services/webhook/ssrf.go` + `proxydial.go` (SSRF guard), `internal/services/security/` (engine), suppression/consent/eligibility layers, `internal/services/emailhistory` (Recorder), provider-event ingestion, automation engine, campaign processor, tracking, auth/session/WebAuthn/impersonation. New code integrates with these; none are restructured by Phases 1–6 except where an ADR explicitly extends them (inbound, email, metrics, cron).

## 3. Dependency order (implementation)

1 (MTA) → 2 (routing needs 1's outbound path for forwarding) → 3 (mailstore needs 2's durable ingest) → 4 (IMAP/POP3 need 3) → 5 (webmail needs 3/4) → 6 (convergence needs all). Parallel relay-track waves slot between phases per file-ownership; the Phase-3 verification engines (SF-22/23/24) are pulled earlier than OB-2 where the ADR-002 receiver extension allows, per the user's parallel directive.
