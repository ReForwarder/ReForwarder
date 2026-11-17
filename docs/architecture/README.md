# docs/architecture/ — ADRs & Programme Governance

Decision records and governance documents for the Option B mail-infrastructure architecture. All Accepted 2026-11-07 unless noted. **Live status: [`docs/PROJECT_STATUS_DASHBOARD.md`](../PROJECT_STATUS_DASHBOARD.md).**

## ADRs (binding decision records — 📌 current)

| ADR | Decision (one line) | Landed |
|---|---|---|
| [ADR-001](ADR-001-outbound-mta.md) | In-process outbound MTA; per-domain `relay` (default) / `direct`; STARTTLS mandatory-when-offered | OB-1 |
| [ADR-002](ADR-002-inbound-smtp-routing.md) | Rules-as-data inbound routing (deliver/forward/webhook/quarantine), VERP + DSN match-back | OB-2 |
| [ADR-003](ADR-003-mailbox-storage.md) | Postgres mailstore, content-addressed blobs, FTS in-Postgres, AES-256-GCM at rest | OB-3 |
| [ADR-004](ADR-004-imap-pop3.md) | In-house IMAP4rev1 + POP3; app-password-only auth; JMAP gated on demand | OB-4 |
| [ADR-005](ADR-005-webmail.md) | Webmail as a `/mail` area of the existing Vue SPA over REST; no embeds, no second SPA | OB-5 |
| [ADR-006](ADR-006-spam-malware.md) | `SpamFilter` interface + rules baseline + ClamAV; Sieve-subset scripts behind amendment gate | OB-2 |
| [ADR-007](ADR-007-dns-deliverability.md) | `DNSProvider` interface, ACME/SNI, IP pools/warm-up/reputation, MTA-STS + TLS-RPT | OB-6 |
| [ADR-008](ADR-008-data-event-ownership.md) | Workspace stays the tenant boundary; one event pattern per store; two-tier quotas | cross-cutting |
| [ADR-009](ADR-009-deployment-topology.md) | One binary, gated listeners; single Postgres + Redis; multi-node deferred | cross-cutting |

## Governance & decision documents (📌 current)

| File | Role |
|---|---|
| [OPTION-B-ARCHITECTURE.md](OPTION-B-ARCHITECTURE.md) | The binding thesis + four non-negotiables |
| [COMPONENT-MATRIX.md](COMPONENT-MATRIX.md) | Component → package/phase/ADR/storage map |
| [DATA-OWNERSHIP.md](DATA-OWNERSHIP.md) | Normative ownership rules per table/store (normative source: ADR-008) |
| [MESSAGE-LIFECYCLE.md](MESSAGE-LIFECYCLE.md) | The 14 message flows F1–F14 + invariants |
| [MIGRATION-PLAN.md](MIGRATION-PLAN.md) | Additive-migrations-only policy; rollback posture |
| [RISK-REGISTER.md](RISK-REGISTER.md) | RSK-1..RSK-15, reviewed at every phase gate |
| [IMPLEMENTATION-ROADMAP.md](IMPLEMENTATION-ROADMAP.md) | Phase plan (§0 ADR gate complete; execution tracked in root `todo.md`) |
| [TENANCY-DECISION.md](TENANCY-DECISION.md) | 2026-11-11: nothing ships without prerequisites; gates SF-66/71/72 |
| [EVALUATIONS.md](EVALUATIONS.md) | 2026-11-11: JMAP / groupware / FileNode — "Not now", with revisit triggers |

Every file in this folder carries a status banner at the top stating its currency.
