# Decision — Tenancy, Roles, SCIM (SF-66 / SF-71 / SF-72) at the OB-6 Revisit

- **Status:** Decided (2026-11-11 — OB-6 convergence wave, per ADR-008 §6's Phase 6 revisit)
- **Decision in one line:** **Nothing ships now.** No tenant objects, no SCIM, no granular-permission registry — each is deferred until a named, reviewed migration plan exists *and* a real consumer demands it. This document is the decision record ADR-008 §6 required; it implements no half-tenantancy.
- **Supersedes:** the residual ambiguity between `todo.md:599` (SCIM listed "Still dead") and ADR-008 §6 / `docs/audits/FEATURE_PARITY_AUDIT.md` (SF-71 "PLANNED under ADR-008 (OB-6 revisit)") — the ADR governs: these are **deferral-with-prerequisites decisions, not rejections**.

## What ships now

**Nothing on the tenancy/SCIM/roles axis.** Concretely, the OB-6 wave adds:

- no `tenants` table, no `tenant_id` column anywhere;
- no SCIM endpoints (RFC 7644), no provisioning bearer tokens;
- no permission registry, no custom-role composition UI/API;
- no changes to `ownsResource` or the admin middleware chain.

This is deliberate. The ask-level rule ("nothing without a migration plan is
the honest default") matches ADR-008 §6's own gating: these items are "adopted
only … with a migration plan for existing workspaces". Half-tenantancy — a
tenant object without re-scoped admin views, SCIM without tenant objects, a
partial permission registry — is worse than none: it doubles the eventual
migration surface and creates authorization seams while shipping no isolation
value.

## What already exists (the tenant boundary today)

The **workspace** is the tenant boundary, and it is complete at every layer
(ADR-008 §1; this is why deferral is safe):

- Every domain row carries `workspace_id` — e.g. `internal/models/mailbox.go:51`
  (`WorkspaceID *uint`, indexed), plus the whole registered model fleet
  (`emails`, `domains`, `contacts`, `inbound_emails`, mailstore objects, …).
- Every endpoint scopes through the `ownsResource(userID, workspaceID)` chain
  — `internal/handlers/api_key_usage_handler.go:56`,
  `internal/handlers/dmarc_handler.go:74`, `internal/handlers/automation_handler.go:346`,
  and mailboxes additionally bind **user → workspace**
  (`internal/handlers/admin_mailbox_handler.go:42` documents where the chain
  is validated fail-closed for provisioning, ADR-008 decision 1's
  mailbox → user → workspace rule).
- Roles today are two-level: platform `admin`/`user`
  (`internal/models/user.go:27-28`) and workspace owner/admin/editor/viewer
  (`internal/models/workspace.go:27-30`) — 4 workspace roles + scoped API keys
  + app passwords. This is the "4-role RBAC" the feature ledger records for
  SF-66 as COMPLETE-before-registry.
- Admin scope is deployment-wide (`internal/middlewares/middleware.go:152`).
- Audit verdict: SF-72's ledger entry calls workspaces the "tenant analog"
  with scoped views and per-workspace isolation verified on every endpoint.

## Why defer (the explicit rationale)

1. **No consumer for the migration.** Distinct tenant objects pay off when a
   deployment hosts multiple organizations that must not see each other's
   *workspaces* — a B2B reseller/enterprise posture. ADR-008 §Alternatives
   rejected the tenant object "at Phases 1–5 — no current consumer". Nothing
   in the demand record since (feature audits, backlog, research notes)
   supplies that consumer. Deferral costs nothing the current user base needs.
2. **The migration is the whole cost, and it is unbounded without a plan.**
   Adding a level above workspaces touches *every* query path (the
   `ownsResource` chain gains a hop), the admin scope model, quotas
   (ADR-008 §4's two-tier model grows a third tier), billing/plan joins, and
   the audit checklist (workspace-scoping column → now tenant-scoping of
   *admins*). Doing this speculatively risks the one thing the programme has
   protected everywhere: additive, verifiable migrations (AGENTS.md §7) and
   cross-tenant 404 tests (ADR-008 §Consequences). Without a real deployment
   to migrate, the plan cannot even be validated against its own data shape.
3. **SCIM without tenant objects is a stub.** SCIM provisions identities into
   *some* boundary. With only workspaces, SCIM would map IdP groups onto
   workspace memberships — a design that no requesting enterprise has
   constrained, so it would likely be wrong on arrival. SF-71 is marked
   [ENT] in the ledger: it exists for enterprise IdP integration, which is the
   same absent consumer.
4. **The ~660-granular permission registry (SF-66) is negative-value now.**
   Four workspace roles + scoped API keys cover every current UI/API surface.
   A 660-permission registry multiplies test surface and authorization review
   cost for roles nobody holds yet. The ledger already records the honest
   PARTIAL; inventing the registry to flip a label would be status theater,
   which §4 of AGENTS.md forbids.
5. **Mailplane priorities outrank it.** The Option B programme (OB-1…OB-6)
   landed delivery, mailstore, protocols, webmail, and is closing
   deliverability/DNS/observability — the capabilities the actual mail-sending
   user base consumes. Tenancy re-plumbing would compete for the same
   additive-migration review bandwidth (every Phase 1–6 table was audited for
   workspace scoping; a tenant level re-opens that whole checklist).

## What a migration plan would have to contain (the prerequisites)

Adoption happens only when **all** of the following exist and are reviewed.
Until then this decision stands; no partial step may merge.

1. **Demand evidence** — a named deployment (design partner, self-host
   operator) that needs *multiple isolated organizations under one
   administration*, with their hierarchy/quota requirements in writing. This
   mirrors ADR-008 §Alternatives ("real B2B demand evidence required") and
   RF-B126's discipline for JMAP: demand is collected or its absence is
   recorded, before any build decision.
2. **Schema plan (additive-only)** — `tenants` table; `workspaces.tenant_id`
   nullable + backfill (default tenant or per-workspace assignment), then
   `NOT NULL` only after backfill; tenant-scoped admin views; tenant quotas
   (third tier over ADR-008 §4's workspace/storage tiers); every migration
   registered under `internal/storage/migration` + `upgrade`, no drops/rewrites.
3. **Query-path re-audit** — every `ownsResource`-style check and every
   repository query gains the tenant hop; admin endpoints re-scope from
   deployment-wide to tenant-scoped (or gain explicit platform-admin
   superuser); the cross-tenant-404 test harness extended to assert
   cross-*tenant* 404s, not just cross-workspace.
4. **Identity/provisioning design** — SCIM 2.0 (RFC 7644) Users (and
   Groups→workspace membership) mapping decided against the requesting IdP's
   actual manifest: token model, de-provisioning semantics (GDPR-adjacent:
   SF-73 destruction interplay), 2FA/app-password interplay (SF-69/SF-67),
   audit events per RFC 7644 §3.9 events we adopt.
5. **Role registry scope** — SF-66 only if demand shows the 4 workspace roles
   are insufficient *in practice*; a complete inventory of the ~660
   permissions derived from actual routes (`internal/routes/*`), a
   composition model, and a migration for existing role assignments — not a
   number-first registry.
6. **Rollout & rollback** — feature-flagged enablement (off by default, per
   the ADR-009 off-by-default discipline), a rehearsed rollback path, and a
   documented per-deployment migration runbook in `docs/docs/operations/`.
7. **Status bookkeeping** — SF-66/71/72 move from PARTIAL/PLANNED to
   In Progress only when a plan meeting 1–6 is merged; the ledger entries
   point at the plan, not at this document.

## Revisit triggers

Re-open this decision when any of the following lands in the backlog with
evidence attached:

- a signed design partner / operator requirement for organization-level
  isolation above workspaces (trigger 1);
- a second deployment on one install whose *admins must not see each other* —
  the concrete capability gap SF-72's tenant objects close;
- an enterprise IdP integration requiring SCIM provisioning as a contract
  (trigger 4).

## Related documents

- `docs/architecture/ADR-008-data-event-ownership.md` §6 — the gate this decision closes.
- `docs/audits/FEATURE_PARITY_AUDIT.md` — SF-66 (PARTIAL, registry PLANNED),
  SF-71 (Missing, PLANNED under ADR-008 OB-6 revisit), SF-72 (PARTIAL, tenant
  objects deferred).
- `docs/project/BACKLOG.md` — the standing backlog entries for the three items.
- `docs/architecture/EVALUATIONS.md` — the parallel JMAP/groupware evaluations
  (same discipline: evaluation ≠ commitment).
