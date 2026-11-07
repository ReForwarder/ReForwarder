# ADR-008 — Data & Event Ownership (Tenancy, Quotas, Event Substrate)

- **Status:** Accepted (2026-11-07 — Option B programme authorization)
- **Phase:** Cross-cutting (binding on Phases 1–6; tenant objects/SCIM revisited at Phase 6) · **Covers (parity register):** SF-19, SF-71, SF-72, SF-66 (tenancy/roles), SF-80 (quotas), SF-81 (retention ownership), the event substrate (gap-analysis §7 architecture requirement)

## Context

ReForwarder's tenancy model is the **workspace**: every row carries `workspace_id`, every endpoint scopes through `ownsResource`/workspace filters, admin scope is deployment-wide. Mail infrastructure adds new object kinds (domains-as-mail-zones, mailboxes, inbound routes, queues, delivery attempts) and new event families (transport, filtering, mailbox). The Option B decision requires an explicit ownership map so that no data is orphaned, no tenant boundary is bypassed, and the event substrate stays the single delivery-timeline currency (gap-analysis §7: "`email_events` as the single delivery-timeline/ingestion substrate").

## Decision

1. **Workspace remains the tenant boundary for all new objects.** Every new table in Phases 1–6 (`mta_delivery_attempts`, `inbound_routes`, `inbound_quarantine`, `mailboxes`, `mail_folders`, `mail_messages`, `blob_refs`, DNS provider credentials, warm-up schedules) carries `workspace_id` (or resolves to one through a strict parent chain: mailbox → user → workspace; domain → workspace) and every query path enforces it — the existing `ownsResource`/`OwnsWorkspaceResource` pattern, no exceptions. Mailboxes additionally bind to a **user** (IMAP/POP3/webmail identity), and user→workspace membership follows the existing model.
2. **Mail-domain ownership gates mail features.** A domain becomes a mail zone only through the existing verified-domain ownership flow (TXT verification); MX/return-path/auto-publish actions are refused for unverified domains (fail-closed, ADR-001 §Context non-negotiable 2).
3. **Event substrate**: `email_events` remains the delivery-timeline ledger for transactional/campaign mail. Mailbox operations (delivery, flag changes, folder ops) get a parallel **`mail_events`** table with the same shape discipline (write-once timestamps, dedup keys) — the §7 requirement is honored by keeping ONE ingestion pattern, not one table for unrelated lifecycles. Filtering dispositions (ADR-006) land in the shared `events`/`security_events` taxonomy. Cross-store joins (campaign email ↔ DSN bounce) go through message-id + VERP-encoded return-path matching (ADR-002 §4), never through ad-hoc string coupling.
4. **Quotas (SF-80)**: ownership is two-tier — control-plane send quotas live on the workspace/plan (existing), storage quotas live on the mailbox (ADR-003 §5) with a workspace aggregate ceiling; all quota mutations audited; quota breaches are typed errors surfaced to both API and UI.
5. **Retention ownership (SF-81)**: one retention cron owns all deletion schedules (backups R-11, events, quarantine ADR-002, mailstore folders ADR-003, provider raw payloads) — policy per object family in settings, execution centralized, GAP-13's `security_events` caller closes with it. Retention never deletes across tenant boundaries: every sweep is workspace-scoped.
6. **Tenant objects + SCIM (SF-72/71) and granular roles (SF-66)**: Phase 6 evaluation items — the workspace model already provides isolation; distinct tenant objects, SCIM provisioning, and a ~660-granular permission registry are adopted only under this ADR's Phase 6 revisit with a migration plan for existing workspaces. Recorded as PLANNED (OB-6) with prerequisites, not dropped.

## Alternatives considered and rejected

- **Tenant as a separate object above workspaces now**: rejected at Phases 1–5 — no current consumer; the workspace boundary satisfies isolation; the Phase 6 revisit is the decision point (with real B2B demand evidence required).
- **Mailbox events folded into `email_events`**: rejected — different lifecycles (transactional delivery vs mailbox state) would pollute the delivery-timeline substrate and break its dedup keys; one *pattern*, two tables (the gap-analysis requirement is about the ingestion discipline, satisfied).
- **Per-feature retention crons**: rejected — N retention jobs drift; one sweep with per-family policies is auditable (matches SF-104's scheduled-task system).

## Consequences

- Migration review checklist for every Phase 1–6 table: workspace scoping column present, index includes it, repository queries filter by it, handler tests assert cross-tenant 404s (the existing fake-driver harness pattern).
- Event-taxonomy registry extended in one place (`internal/services/emailhistory` known-event-types pattern + security taxonomy) to prevent taxonomy drift.
- Documentation: `DATA-OWNERSHIP.md` (programme doc) is generated from this ADR's ownership map and kept in sync.
