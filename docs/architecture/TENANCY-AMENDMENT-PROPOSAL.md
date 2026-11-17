# Proposal — ADR-008 Amendment: Tenant Objects (SF-72) — DRAFT, NOT ADOPTED

> **📄 Status (2026-11-17):** 📝 **PROPOSAL** — produced as the SF-72 PARTIAL
> record's completion artifact per the programme rules. This document is NOT a
> decision and adopts nothing: `docs/architecture/TENANCY-DECISION.md` (2026-11-11)
> remains the binding decision record, and its prerequisite 1 — **named demand
> evidence** — is the gate this proposal cannot satisfy for itself. SF-72 is
> therefore recorded **BLOCKED**, not COMPLETE: the workspace boundary remains
> the complete tenant implementation today, and **no half-tenancy may merge**
> (the decision record's own rule, §"What ships now").
> **Live status:** [docs/PROJECT_STATUS_DASHBOARD.md](../PROJECT_STATUS_DASHBOARD.md) · [docs/README.md](../README.md)

- **Type:** ADR-008 amendment proposal (the migration-plan prerequisite of
  `TENANCY-DECISION.md` §"What a migration plan would have to contain", items
  2, 3, 5, 6 — itemized concretely against the current tree)
- **Covers:** SF-72 (tenants: object + quotas + tenant-scoped admin views +
  queue/report tagging **[ENT]**); touches SF-71's SCIM prerequisite 4 and
  SF-66's role-registry prerequisite 5 only by reference
- **Gates:** `ADR-008 §6` ("adopted only … with a migration plan for existing
  workspaces") + `TENANCY-DECISION.md` prerequisites 1–7
- **What this proposal deliberately does not do:** no code, no migration, no
  schema change ships with it. It is the plan the decision record asked to
  exist and be reviewed — its production remains gated on demand evidence
  (prerequisite 1), which is why the register records SF-72 as **BLOCKED**.

## 1. The tenant boundary today (verified, why deferral is safe)

The **workspace** is the tenant boundary and is complete at every layer:

- 37 model files carry a `WorkspaceID` field (62 `WorkspaceID`-tagged columns
  across the registered fleet — emails, domains, contacts, inbound_emails,
  mailstore objects, campaigns, templates, workspaces' own resource tables).
- Every endpoint scopes through the `ownsResource(userID, workspaceID)` chain
  (`internal/handlers/helper.go:96-98` →
  `repositories.OwnsResource`/`OwnsWorkspaceResource`); mailboxes additionally
  bind **user → workspace** fail-closed on provisioning
  (`internal/handlers/admin_mailbox_handler.go:197-207`, ADR-008 decision 1's
  mailbox → user → workspace rule).
- Roles: platform `admin`/`user` (`internal/models/user.go:27-28`) + four
  workspace roles owner/admin/editor/viewer (`internal/models/workspace.go:27-30`),
  scoped API keys, app passwords.
- Admin scope is deployment-wide (`internal/middlewares/middleware.go:152`).
- Isolation is pinned by suites, not convention: the workspace RBAC battery
  (`internal/routes/workspace_rbac_test.go`), the per-surface cross-workspace
  404 suites (e.g. the DMARC record isolation in
  `internal/handlers/dmarc_handler_test.go`, the SF-60 domain CRUD isolation
  battery in `internal/handlers/domain_crud_handler_test.go`, the SF-68 OAuth
  identity isolation in `internal/handlers/oauth_link_test.go`), and the
  admin-mailbox membership validation tests.

## 2. What distinct tenant objects would require

### 2.1 Schema plan (additive-only; every step registered)

New tables and columns, in dependency order. Every step is a dated file under
`internal/storage/migration/upgrade/` (the machinery that already gates steps
on binary version and plan enforcement — `upgrade.Run`, `Options.PlanEnforcement`)
plus the AutoMigrate registration in `internal/storage/migration/migration.go`.
No drops, no rewrites — the AGENTS.md §7 rule.

| # | Step | Shape | Notes |
|---|------|-------|-------|
| S1 | `tenants` table | `id` PK, `name` (unique), `status` (`active`/`suspended`), `plan_id` FK (nullable — the tenant-level plan tier), `quota_overrides` (JSON: the third-tier caps), `created_at`/`updated_at` | The tenant object itself. No backfill needed — a fresh table. |
| S2 | `workspaces.tenant_id` | nullable `*uint` FK + index | Nullable first (zero-risk additive step). |
| S3 | Backfill | every existing workspace → a deployment's **default tenant** row (created in S1 with a well-known slug, e.g. `default`) | One `upgrade` step: create-the-default-tenant + `UPDATE workspaces SET tenant_id = ?` inside one transaction; idempotent (re-runnable), rehearsed on a production-shape dump. |
| S4 | `workspaces.tenant_id` → `NOT NULL` | constraint tighten AFTER backfill is verified on every deployed shape | The only constraint-tightening step; gated on the S3 backfill audit query returning zero NULLs across deployments. |
| S5 | Tenant memberships (optional, demand-shaped) | `tenant_memberships` (`tenant_id`, `user_id`, role) — ONLY if the named consumer needs users spanning tenants; otherwise tenant admins are derived from workspace ownership | Deliberately NOT built speculatively (the decision record's half-tenancy rule). |
| S6 | Queue/report tagging | `emails.tenant_id`, `mta_delivery_attempts.tenant_id` (denormalized, backfilled from the workspace chain) — the **[ENT]** queue/report tagging half | Additive columns + backfill steps; read paths unchanged until the tenant queries exist. |

### 2.2 Query-path re-audit (the whole cost)

Every isolation seam gains a hop, and each hop must be proven by tests BEFORE
the step that needs it merges:

1. **Resource ownership** — `repositories.OwnsResource`/`OwnsWorkspaceResource`
   (`internal/storage/repositories/scope.go`) gains the tenant comparison:
   a request's tenant (derived from the bound workspace) must match the
   resource chain's tenant. The `ResourceScope` struct gains `TenantID`.
2. **Repository scoping** — `ApplyScope` (`scope.go:36-42`, currently
   `workspace_id = ?`) becomes a two-level scope builder
   (`tenant_id = ? AND workspace_id = ?`); every repository call site is
   enumerated and re-audited — the register's 62 `WorkspaceID` columns are the
   checklist's backbone.
3. **Admin scope model** — `middlewares/middleware.go:152`'s deployment-wide
   admin gate splits: a **platform-admin** superuser (deployment operators)
   vs **tenant admins** (the consumer's organization administrators). This is
   a product decision the named consumer must make — the proposal does not
   pre-select it.
4. **Quota model** — ADR-008 §4's two tiers (send quotas on the workspace/plan;
   storage quotas on the mailbox + workspace aggregate ceiling) grow the third
   tenant tier: tenant-level send/storage ceilings checked BEFORE the
   workspace tiers, with quota mutations audited (the existing audit posture).
5. **Billing/plan joins** — `plans`/workspace plan assignment gains the
   tenant-level plan precedence (S1's `plan_id`).

### 2.3 Isolation changes and the test matrix (merge gates, not afterthoughts)

The existing isolation suites are the template; the tenant harness extends
each of them to assert cross-**tenant** 404s, not just cross-workspace:

- the RBAC battery (`internal/routes/workspace_rbac_test.go`) gains tenant-role
  cases;
- every per-surface isolation suite (the DMARC/domain/OAuth examples above)
  gains a second-workspace-same-tenant row (must stay visible) and a
  second-tenant row (must 404);
- the admin surface tests split platform-admin vs tenant-admin expectations;
- the quota tests gain the third-tier breach paths (tenant cap before
  workspace cap, both audited).

A migration step that needs a tenant query may not merge while its matching
isolation case is absent — the same fail-closed discipline the SF-68 router
finding demonstrated at the route layer.

### 2.4 Rollout & rollback

- Feature-flagged enablement, **off by default** (`POSTA_TENANTS_ENABLED` —
  the ADR-009 off-by-default discipline); the flag gates the tenant-aware
  scoping builders, not the schema (the additive columns are inert until
  scoped).
- Per-deployment migration runbook in `docs/docs/operations/` (the GAP-49
  precedent: a rehearsed drill, not a document).
- Rollback: every step through S4 is reversible by the additive-migration
  contract (drop the new column/table — no existing data rewritten); after S4
  the rollback path is the S3 backfill's inverse (NULL-out + drop), rehearsed
  on the dump before production.

## 3. What stays excluded (unchanged by this proposal)

- The grant-registry/SCIM/permission-registry halves of SF-66/SF-71 keep their
  own decision records; this proposal only names their prerequisite linkage.
- No `tenant_id` lands on any table until S2; nothing in the working tree
  changes with this document.

## 4. The gate this proposal cannot satisfy for itself

`TENANCY-DECISION.md` prerequisite 1: a named deployment needing *multiple
isolated organizations under one administration*, with hierarchy/quota
requirements in writing. Until that evidence exists, the proposal stays a
reviewed draft, the decision record stands, and SF-72's register entry records
**BLOCKED** with this document as the produced artifact. The revisit triggers
remain the decision record's (`TENANCY-DECISION.md` §"Revisit triggers": a
signed design-partner requirement; a second deployment whose admins must not
see each other; an enterprise IdP contract requiring SCIM).

## Related documents

- `docs/architecture/TENANCY-DECISION.md` — the binding decision record (this proposal satisfies its prerequisite-2/3/5/6 shape; prerequisite 1 remains the gate).
- `docs/architecture/ADR-008-data-event-ownership.md` §6 — the gate's origin.
- `docs/audits/FEATURE_PARITY_AUDIT.md` SF-72 — the register entry (BLOCKED, this document as evidence).
- `docs/architecture/EVALUATIONS.md` — the parallel evaluation discipline (evaluation ≠ commitment).
