# Option B Architecture — Complete Email Infrastructure

**Status:** Accepted (2026-11-07) · **Programme:** `todo.md` Track A · **Decision record:** `docs/product-roadmap/upcomming-convo.md` §7 · **ADRs:** [ADR-001](ADR-001-outbound-mta.md) … [ADR-009](ADR-009-deployment-topology.md) · **Feature ledger:** `docs/audits/REFERENCE-PLATFORM_FEATURE_PARITY_AUDIT.md`

## 1. Thesis

ReForwarder expands from an email-marketing control plane with relay-based sending into **complete self-hosted email infrastructure**: outbound including direct-to-MX, inbound reception with routing, hosted mailboxes with IMAP/POP3 and webmail, plus the marketing/developer/security pillars it already has. The expansion is **architecture-first**: nine ADR decisions gate all mail-server implementation, and the existing control plane (Go monolith + worker over Postgres/Redis/S3, Vue dashboard) remains the single management surface.

## 2. The architecture in one diagram

```
                       ┌──────────────────────────────────────────────────────┐
  Dashboard (Vue) ────►│  Control plane — posta server (okapi)                │
  REST clients ───────►│  API · admin · auth · campaigns · automations        │
  IMAP/POP3 ──────────►│  + gated listeners: inbound SMTP · submission 587    │
  Mail clients         │    IMAP 143/993 · POP3 110/995 · MTA-STS path        │
                       └───────┬───────────────────────────┬──────────────────┘
                               │ asynq (Redis)             │ events bus
                       ┌───────▼───────────────────────────▼──────────────────┐
  Remote MX ◄──────────┤  Data plane — posta worker                            │
  (direct-to-MX /      │  delivery (ADR-001) · inbound routing (ADR-002)       │
   trusted relays)     │  filters (ADR-006) · mailstore sweeps · retention     │
                       └───────┬───────────────────────────┬──────────────────┘
                               │                           │
                     ┌─────────▼─────────┐        ┌────────▼────────┐
                     │ Postgres 17       │        │ Redis 7         │
                     │ data + mailstore  │        │ queues · locks  │
                     │ events · ledgers  │        │ pub/sub fabric  │
                     └─────────┬─────────┘        └─────────────────┘
                               │
                     ┌─────────▼─────────┐
                     │ blob.Store FS/S3  │  content-addressed, encrypted (ADR-003)
                     │ bodies · attachments · certs · backups
                     └───────────────────┘
```

## 3. Domain boundaries (13 logical domains — from the decision record)

`mailserver` · `mta` · `inbound` · `mailbox` · `mailstore` · `imap` · `pop3` · `mailrouting` · `reputation` · `dnsmanager` · `spamfilter` · `malware` · `webmail` — **logical package boundaries inside the monolith** (`internal/services/...` per ADR), not microservices. Ownership rules per [DATA-OWNERSHIP.md](DATA-OWNERSHIP.md).

## 4. The four non-negotiables (binding on every ADR and phase)

1. **Never an open relay.** Direct delivery only for authenticated tenants with verified sender domains; new listeners authenticate and policy-check exactly like the existing relay and inbound receivers.
2. **Never trust a client-supplied tenant ID or recipient domain.** Tenant and recipient-domain are re-derived server-side from validated records at every hop (ADR-008 §2).
3. **Never report queue acceptance as delivery.** `delivered` comes only from MX 2xx (ADR-001) or provider-event truth (R-04) — never from enqueue.
4. **Never accept inbound without a durable path.** Persistence precedes every routing/filtering action (ADR-002 §Decision 1, ADR-006 §5).

## 5. How the phases compose

| Phase | Adds | ADRs | Register effect |
|---|---|---|---|
| 0 (this) | 9 ADRs + 7 programme docs, zero code | all | BLOCKED BY ADR → PLANNED |
| 1 | Outbound MTA (direct + relay modes, attempts, policy, PTR guidance, submission 587) | 001, 009 | SF-01/02/03/05/06/12/17/21 + MTA parts → IN PROGRESS → COMPLETE |
| 2 | Inbound routing (aliases, forwarding, quarantine, VERP/DSN, ARC/iprev annotations) | 002, 006 | SF-11/14/26/27/32/45/48/51 + CA-05 |
| 3 | Mailstore (folders/UIDs/flags, FTS, dedup+GC, encryption, quotas, retention) | 003, 008 | SF-75/78/79/80/83/112 + GAP-13/45 |
| 4 | IMAP4rev1 + POP3 (app-password auth, TLS, limits, audit) | 004 | SF-85/86 |
| 5 | Webmail + mailbox administration (Vue area + REST) | 005 | CA-01 + SF-65/72/80 admin surfaces |
| 6 | Convergence (DNS automation, ACME, IP pools/warm-up/reputation, MTA-STS/TLS-RPT, roles/tenants/SCIM, JMAP + groupware/file-storage evaluations, observability completion) | 007, 008, 009 | SF-28/29/31/34/39-42/61/66/71/72/96/105/106/111/112/113 + GAP-08/49/50 |

Parallel track (not phase-gated): the **relay-architecture gaps** from the parity register's Phase-3 pool (SF-10 DSNs, SF-22/23/24 verification engines, SF-52/54/56 queue ops, SF-100 settings history, GAP-13/45, CA-02/03/06, …) — implemented alongside Phase 1 wherever file ownership allows (per the user's directive: "implement all features that can safely be completed within the existing relay architecture in parallel where dependencies permit").

## 6. What does NOT change

- The transactional `Email` model, `email_events` ledger, campaigns, automations, templates, provider events, webhooks (SSRF-guarded), security engine, and suppression/eligibility layers are untouched in their contracts; new capabilities integrate with them (ADR-002 forwarding produces system `email` rows; ADR-006 dispositions extend the taxonomy; ADR-001 records attempts).
- The API envelope, route-contract test (357+ routes), migration discipline (additive only), and AGPL-3.0 licensing carry forward unchanged.

## 7. Exit criteria per phase

Every phase exits when: components per its ADR exist and are wired (listeners/routes registered), additive migrations land, named tests pass in the full battery (`go build ./...`; `go test -count=1 -race ./...`; vet; golangci-lint v2.13.2; vue-tsc; vitest; DB-tier), the parity register's records for that phase flip to COMPLETE with evidence, `todo.md` Track A checkboxes update, and the phase's dashboard surfaces pass visual acceptance. Phase gates are sequential; the parallel relay-track integrates between phases.
