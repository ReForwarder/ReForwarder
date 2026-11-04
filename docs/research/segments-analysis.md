# the reference segmentation product Segments — Source Analysis & ReForwarder Adaptation Decisions

> Research basis for the ReForwarder Segments feature. the reference segmentation product was studied at **source level** from the
> [usereference-segmentation/reference-segmentation](https://github.com/usereference-segmentation/reference-segmentation) repository, **`next` branch** (the default branch — the `main`
> branch is a legacy codebase whose Prisma schema has **no Segment model at all**). Every claim below was verified
> against actual source files; the reference segmentation product's own file paths are cited so future maintainers can re-check.
>
> **License verdict: the reference segmentation product is AGPL-3.0** (`LICENSE`, both branches). ReForwarder is also AGPL-3.0-or-later, but this
> adaptation was performed **clean-room by design**: concepts and behavior were studied and reimplemented in Go/Vue;
> no the reference segmentation product source was copied. Concepts are not copyrightable; verbatim borrowing would create a derived work.

---

## 1. the reference segmentation product's segment architecture (source findings)

### 1.1 Data model — `packages/db/prisma/schema.prisma`

```prisma
model Segment {
  id              String      @id @default(uuid())
  name            String
  description     String?
  type            SegmentType @default(DYNAMIC)   // DYNAMIC | STATIC
  condition       Json?                           // filter tree, null for STATIC
  trackMembership Boolean     @default(false)     // enables membership persistence + entry/exit events
  memberCount     Int         @default(0)         // computed stat
  projectId       String                          // the reference segmentation product's workspace equivalent
  memberships     SegmentMembership[]
  campaigns       Campaign[]
}
model SegmentMembership {
  contactId String
  segmentId String
  enteredAt DateTime @default(now())
  exitedAt  DateTime?                    // null = currently a member
  @@id([contactId, segmentId])
}
```

- **Hybrid membership model**: DYNAMIC segments are computed at query/send time (`SegmentService.buildWhereClause`);
  STATIC segments are purely membership-table driven; a DYNAMIC segment with `trackMembership: true` also persists
  memberships via `computeMembership` (cursor-paginated diffing, 1000/batch scan, 500/batch writes, re-entry resets
  `exitedAt = null`).
- A BullMQ repeatable job (`apps/api/src/jobs/segment-count-processor.ts`, scheduled `*/5 * * * *`, concurrency 1)
  recomputes tracked memberships (emitting `segment.<slug>.entry` / `.exit` events) and refreshes untracked counts.
- Member counts are computed synchronously at create/update and otherwise maintained by the job.

### 1.2 Condition tree — `packages/types/src/segments/index.ts`

```ts
interface FilterCondition { logic: 'AND' | 'OR'; groups: FilterGroup[] }
interface FilterGroup { filters: SegmentFilter[]; conditions?: FilterCondition } // recursion supported
interface SegmentFilter { field: string; operator: SegmentFilterOperator; value?: any; unit?: 'days'|'hours'|'minutes' }
```

19 operators: `equals, notEquals, contains, notContains, greaterThan, lessThan, greaterThanOrEqual, lessThanOrEqual,
exists, notExists, within, olderThan, triggered, triggeredWithin, triggeredOlderThan, notTriggered, notTriggeredWithin,
memberOfSegment, notMemberOfSegment` (time-family operators take `unit`).

Field namespaces dispatch in `SegmentService.buildFilterCondition`:
- plain columns: `email`, `subscribed`, `createdAt`, `updatedAt`
- `data.<path>` — custom attributes live in a single JSON column (there are **no** first/last-name columns; those are
  conventional keys inside `data`)
- `event.<eventName>` — behavioral events (`some`/`none` + time bounds)
- `email.<activity>` — opened/clicked/bounced/complained/sent/delivered
- `segment.<segmentId>` — nested segments, with a `visitedSegments` cycle-detection set, and static/tracked segments
  resolved **through the membership table** (a comment explains: inlining conditions breaks past 32,767 bind variables)

Validation (`SegmentService.validateCondition`): logic must be AND|OR, groups non-empty, field/operator required,
operator whitelisted, value required for value-operators, unit required for time operators. Self-reference is rejected
at update (`getReferencedSegmentIds`); delete returns **409** when campaigns in DRAFT/SCHEDULED/SENDING reference the
segment. Every query pins `projectId` first (`buildWhereClause`).

### 1.3 API — `apps/api/src/controllers/Segments.ts`

`GET/POST /segments`, `GET/PATCH/DELETE /segments/:id`, `GET /segments/:id/contacts` (paginated preview, cap 100),
`POST/DELETE /segments/:id/members` (static), `POST /segments/:id/compute`, `POST /segments/:id/refresh`. All behind
auth + email-verification middleware; workspace scoping via `auth.projectId` on every call.

### 1.4 Campaign integration — `apps/api/src/services/CampaignService.ts`

`Campaign.audienceType: ALL | FILTERED | SEGMENT` (+ nullable `segmentId`, optional inline `audienceCondition`).
Recipient resolution: ALL → `subscribed: true`; SEGMENT → static members from the membership table or dynamic
`buildConditionClause`; FILTERED → inline condition. Sends batch 500 contacts through a BullMQ queue.

### 1.5 Automation integration

**Indirect only**: workflows never reference segments directly. A tracked segment's `computeMembership` emits
`segment.<slug>.entry`/`.exit` events, which workflow EVENT triggers can consume. Workflow CONDITION steps reuse a
subset of the operator vocabulary in an in-memory evaluator, but there is no shared evaluation engine with segments.

### 1.6 UI — `apps/web/src`

`pages/segments/index.tsx` (list), `pages/segments/new.tsx` (create: name, description, DYNAMIC/STATIC toggle,
`trackMembership` checkbox, `SegmentFilterBuilder`, optional initial static members), `pages/segments/[id].tsx`
(detail: filter card for dynamic; member management for static; "Matching Contacts" paginated preview; Recompute
button for tracked segments). `components/SegmentFilterBuilder.tsx` is the builder: recursive
condition→group→row with a grouped searchable field picker (Contact fields / Custom data / Events / Email activity /
Segments), operators filtered by field type, value editor varying by type (boolean select, number, date, text, unit
select for time operators), AND/OR pills between groups, last-group protection, self-reference exclusion.
`components/SegmentRuleSummary.tsx` renders a human-readable condition summary.

### 1.7 Tests

`apps/api/src/services/__tests__/SegmentService.test.ts`, `SegmentService.operators.test.ts` (~90 operator/boundary
cases against real Postgres), `SegmentService.segmentFilters.test.ts` (nested segment filters). No controller/job tests.

---

## 2. ReForwarder architecture findings (what we adapted into)

| Fact | Source | Consequence |
| --- | --- | --- |
| A segments-like feature already exists for **subscribers**: `SubscriberList.type static\|dynamic` + `FilterRules` JSON + `applyFilterRules` SQL engine + `preview-segment` count endpoint | `internal/models/subscriber_list.go:66-77`, `internal/storage/repositories/subscriber_repo.go:120-232`, `internal/routes/workspace_resource_routes.go:1091-1100` | Build **on** this machinery, not beside it |
| The `contacts` table is a send-history ledger: email/name/sent/fail counts only — no status, no custom attributes, no CRUD | `internal/models/contact.go:24-34` | Segments filter the **subscriber audience** (user decision) |
| Campaigns already resolve dynamic lists at send time (`FindByFilterRules`) with eligibility = `status==subscribed` + per-list unsubscribes | `internal/worker/campaign_processor.go:107-156` | Segment-as-audience slots beside the dynamic-list branch |
| The existing filter engine is **flat AND only**, 8 operators (`eq, neq, contains, starts_with, ends_with, gt, lt, in`), whitelisted columns + `custom_fields.<key>` JSON + `engagement.*` subqueries | `subscriber_repo.go:144-232` | Groups/OR + richer operators are the actual gap Segments fills |
| Automations reuse `models.FilterRule` for trigger + filter-node conditions (in-memory AND-only evaluator) | `internal/services/automation/graph.go:280-366` | Left untouched in this task |
| API-key scoping is automatic per method for `/workspaces/current/*` routes; `requireEdit` = Editor+ | `internal/middlewares/middleware.go:307-324` | No new auth mechanism |
| Naming hazard: path substrings in `tenantAdminPaths` escalate API-key scope to `admin` | `middleware.go:295-305` | The path segment `segments` is safe |
| Sidebar icons are hand-inlined Lucide-style SVGs via `getIcon()` — no lucide package | `web/src/layouts/DashboardLayout.vue:428-464` | Icon changes follow that convention |

## 3. Adaptation decisions

| the reference segmentation product concept | ReForwarder decision | Rationale |
| --- | --- | --- |
| Segments over a Contact model with `data` JSON | Segments over **subscribers** (`subscribers` table: status, timezone, language, subscribed_at, `custom_fields` JSON) | The subscriber audience is what campaigns/automations target; the contacts ledger has no attributes; user-confirmed |
| DYNAMIC + STATIC + tracked hybrid | **Dynamic only** (condition evaluated at read/preview/send time) | Static membership + tracking requires the membership table + 5-minute compute job + entry/exit event bus — a subsystem with no consumer yet (automation triggers don't fire on it). ReForwarder's static lists already exist as Subscriber Lists. Documented deferral |
| Recursive condition tree | **One nesting level**: `SegmentCondition{logic, groups[]}`, `SegmentRuleGroup{logic, rules[]}`, rules = `models.FilterRule` | Covers all use cases in the feature brief (`A AND B`, `A OR B`, mixed); recursion adds UI + validation complexity with no v1 consumer; the guard against 32k-bind-variable blowups doesn't apply at this depth |
| 19 operators incl. event/trigger/segment-member | **12 core operators** (`equals, not_equals, contains, not_contains, starts_with, ends_with, is_empty, is_not_empty, greater_than, less_than, one_of, not_one_of`) + `exists/not_exists` for custom fields; `engagement.*` virtual fields reuse the existing subquery vocabulary | Operators beyond the text/number/date/existence family depend on the reference segmentation product's event-tracking subsystem, which ReForwarder models differently; engagement covers the behavioral need |
| `event.*` / `segment.*` / `email.*` field namespaces | Not adapted (v1) | Event ingestion and nested segments are separate features; engagement subqueries cover the "who engaged" case |
| Campaign `audienceType ALL/FILTERED/SEGMENT` | **Nullable `Campaign.SegmentID`** XOR `ListID`, validated at create/update; processor branch beside the dynamic-list branch | Minimal schema change; existing list campaigns untouched; per-list unsubscribes don't apply to segment audiences (they are keyed to a list id) — global suppressions still enforced in the send path |
| Automation via membership events | **Deferred, documented** | Requires the tracking subsystem above; the existing FilterRule conditions are untouched |
| Name uniqueness | **Not unique** per workspace | Matches SubscriberList convention |
| Live member counts on list read | Kept (same as dynamic lists) | Consistency over caching in v1; revisit if expensive |

## 4. Segment condition shape (ReForwarder)

```json
{
  "logic": "and",
  "groups": [
    { "logic": "and", "rules": [ { "field": "email", "operator": "contains", "value": "example.com" } ] },
    { "logic": "or",  "rules": [ { "field": "status", "operator": "one_of", "value": "subscribed,unsubscribed" } ] }
  ]
}
```

Groups are joined by `condition.logic`; rules within a group by `group.logic`. Fields:
`email, name, status, timezone, language, created_at, subscribed_at, unsubscribed_at, custom_fields.<key>
(alphanumeric+underscore sanitized), engagement.opened_campaign, engagement.clicked_campaign,
engagement.not_opened_campaign, engagement.received_campaign, engagement.inactive_days, engagement.active_days,
engagement.total_opens_gte, engagement.total_clicks_gte`.
