# FEATURE_AUDIT.md — ReForwarder feature audit: date & evidence record

**Audit date:** 2026-11-06.

**Producer:** the *audit & backlog sync* workflow run persisted at
`.zcode/workflow-runs/dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7.mjs` (untracked — `.zcode/` is
gitignored, `.gitignore:30`). This document was written by that run's *Feature audit writer* actor.
Registry statuses, gap items and the test-gate verdict below were supplied by the run; every
file-level fact was re-verified against the checkout at writing time, with the command quoted inline.

**Classification rules used by the run** (quoted from the run's embedded source, `auditRules`):

> Done = implementation exists AND is wired into the running application AND is not a stub AND
> relevant tests or equally convincing verification exist AND known limitations are documented.
> … Never mark Done merely because a handler, model, UI component, or config option exists.
> A feature may span layers; incomplete end-to-end is not Done.

---

## 1. Branch & commit

- **Branch:** `main` — verified: `git branch --show-current` → `main`.
- **Audited HEAD (per the audit inputs):** `f5f0e921455de901370b5f37b7112b634f6c2066` —
  *Add P3 platform improvements: bounded retries, scheduled triggers, segment conditions, per-key
  limits, DMARC ingestion* (2026-11-06T00:44:14+05:30).
- **Live checkout at the time this file was written:** `0f52636172e19f16a80f9fae421bb57e99ebc6d4` —
  *Fix WebAuthn nil-user abort path and duplicate impersonation stop audit* (2026-11-06 04:19:49
  +0530) — verified via `git rev-parse HEAD` and `git log -1 --format='%H %s%n%ci'`.
- Two commits landed **during** the audit run, after its git-log read: `346eb67` (04:08:28 +0530,
  48 files, +12,020/−27 — WebAuthn, impersonation, feature flags, route-contract test, the
  product-roadmap doc suite, frontend specs) and `0f52636` (04:19:49 +0530, 5 files — WebAuthn
  nil-user abort fix, impersonation stop-audit fix; verified via `git show --stat`). The registry
  evidence in this audit therefore describes the `f5f0e92` + preserved-working-tree state, while the
  checkout has moved two commits ahead. See §9 (limitations).

---

## 2. Working-tree state — 34 preserved entries

- At the audit snapshot, the run's git-status read captured **34 dirty/untracked entries**
  (staged + unstaged + untracked combined), preserved uncommitted. The programme's standing rule
  requires this: *"Existing working-tree changes preserved"* —
  `docs/product-roadmap/upcomming-convo.md:822`. The run script assembles
  `dirty = [...status.staged, ...status.unstaged, ...status.untracked]` and logs the count.
- **The itemized 34-entry list is runtime state of the run and is not persisted to disk.** Verified
  at writing time: `git status --porcelain=v1` returns **0 entries** — the preserved work has since
  been committed (below), so the exact list cannot be re-derived from the repository. Recorded as an
  open question in §9.
- **Closest on-disk reconstruction** — the 48 files of
  `git diff --name-only f5f0e921455de901370b5f37b7112b634f6c2066..0f52636`, i.e. what the preserved
  work became as committed in `346eb67` + `0f52636`. This is *not* a claim that all 48 were dirty at
  snapshot time: the snapshot predates both commits, and additional files were created/modified
  between the snapshot and the commits.

  Docs & site (16):
  `DEVELOPMENT_HISTORY.md`, `compose.override.yml`,
  `docs/implementation/ROADMAP_IMPLEMENTATION.md`, `docs/implementation/api_routes.txt`,
  `docs/product-roadmap/01-oss-feature-inventory.md`, `docs/product-roadmap/02-competitor-comparison.md`,
  `docs/product-roadmap/03-existing-feature-audit.md`, `docs/product-roadmap/04-feature-gap-analysis.md`,
  `docs/product-roadmap/05-implementation-roadmap.md`, `docs/product-roadmap/06-architecture-impact.md`,
  `docs/product-roadmap/README.md`, `docs/product-roadmap/upcomming-convo.md`,
  `docs/research/reforwarder-capability-audit.md`, `docs/research/reforwarder-reference-platform-roadmap.md`,
  `docs/research/reference-platform-feature-gap-analysis.md`, `docs/research/reference-platform-feature-inventory.md`

  Build files (2): `go.mod`, `go.sum`

  Backend Go (18):
  `internal/handlers/impersonation_handler.go`, `internal/handlers/impersonation_handler_test.go`,
  `internal/handlers/info_handler.go`, `internal/handlers/info_handler_test.go`,
  `internal/handlers/user_handler.go`, `internal/handlers/webauthn_handler.go`,
  `internal/handlers/webauthn_handler_test.go`, `internal/middlewares/middleware.go`,
  `internal/models/session.go`, `internal/models/webauthn_credential.go`,
  `internal/routes/admin_routes.go`, `internal/routes/contract_test.go`,
  `internal/routes/routes.go`, `internal/routes/user_routes.go`,
  `internal/services/webauthn/service.go`, `internal/services/webauthn/store.go`,
  `internal/services/webauthn/util.go`, `internal/storage/migration/migration.go`

  Frontend (12):
  `web/src/api/admin.spec.ts`, `web/src/api/admin.ts`, `web/src/api/info.ts`,
  `web/src/api/types.ts`, `web/src/api/webauthn.ts`,
  `web/src/layouts/DashboardLayout.spec.ts`, `web/src/layouts/DashboardLayout.vue`,
  `web/src/stores/auth.ts`,
  `web/src/views/campaigns/CampaignABWinner.spec.ts`, `web/src/views/campaigns/CampaignDetail.spec.ts`,
  `web/src/views/campaigns/CampaignSchedule.spec.ts`, `web/src/views/campaigns/Campaigns.spec.ts`

- Historical note: `todo.md` §0.3 (`todo.md:53-56`) recorded an earlier uncommitted batch
  (API-key usage tracking + retry bounding) with the instruction *"Do not discard; reconcile when
  committed"*; that batch was committed in `f5f0e92` itself (`todo.md` last committed at `06cdd97`,
  00:40:16 +0530, via `git log -- todo.md`) and is superseded by the list above.

---

## 3. Modules reviewed — the 7 audit areas

Seven parallel read-only module audits produced the raw findings; scopes below are the run's
auditor specs (run script, `auditorSpecs`). Per-area registry counts are tallied from the registry
view supplied to this writer (RF-F001…RF-F105).

| # | Area key | Label | Registry ids (count) | Done / In Prog / To-dos |
|---|---|---|---|---|
| 1 | `auth-org` | auth & organizations | RF-F001–F020 (20) | 16 / 3 / 1 |
| 2 | `domains-dns` | domains & DNS | RF-F021–F029 (9) | 4 / 3 / 2 |
| 3 | `smtp-relay` | SMTP & relay | RF-F030–F040 (11) | 10 / 0 / 1 |
| 4 | `delivery-queue` | delivery pipeline & queues | RF-F041–F057 (17) | 13 / 1 / 3 |
| 5 | `marketing-automation` | marketing & automation | RF-F058–F077 (20) | 14 / 0 / 6 |
| 6 | `platform-admin-security` | platform, admin & security | RF-F078–F100 (23) | 16 / 2 / 5 |
| 7 | `frontend-integration` | frontend integration | RF-F101–F105 (5) | 1 / 0 / 4 |
| | **Total** | | **105** | **74 / 9 / 22** (In Review 0) |

Area scopes, abbreviated from the run's auditor specs:

1. **auth-org** — users, sessions, auth, password/2FA/recovery codes, RBAC, workspaces/tenants,
   invitations, API keys (incl. per-key usage/rate limits), account lifecycle.
2. **domains-dns** — domain CRUD, verification, DNS records (SPF/DKIM/DMARC/MX), DKIM signing +
   rotation, domain health/diagnostics, domain-sending policy, tracking domains.
3. **smtp-relay** — relay listener (:2526), inbound SMTP (:2525), SMTP credentials (bcrypt,
   allowlists), STARTTLS/TLS modes, submission auth, inbound webhook ingest, inbound SSE stream,
   website form messages.
4. **delivery-queue** — send pipeline, asynq queues + deterministic TaskIDs, retry layers,
   queue_reconcile, scheduled sends, pause/cancel, queue administration, bounce handling
   (14-class classification), provider_events ingestion, suppression, webhook replay/test-fire.
5. **marketing-automation** — contacts/subscribers (dual model), campaigns (scheduler, A/B winner,
   timezone sends, UTM), templates (versions, localizations), segments (SegmentCondition SQL
   builder), lists, automation engine + visual builder, unsubscribe/suppression UI, preference
   center, signup forms, double opt-in, tags, exports.
6. **platform-admin-security** — admin platform, webhooks engine (signatures, attempt history),
   REST API surface + envelope, tracking (HMAC tokens, toggle symmetry), analytics, security engine
   (bans, fail2ban-style detectors, SSRF guard, webhook proxy), encryption at rest, retention/GDPR,
   backups, metrics/health, error-monitoring hook.
7. **frontend-integration** — cross-cutting pass over `web/src`: verify each UI capability has real
   backend behavior and vice versa; dead buttons, unwired settings, client functions with no route.

---

## 4. Tests EXECUTED

**Go test gate: PASS.** Verdict recorded from the run's gate. **This writer did not re-execute the
suite in this session** — the verdict and tail are recorded verbatim from the run's captured output;
provenance below.

- **Command** (run script, the gate world-read):
  `docker run --rm -v <repoRoot>:/app -w /app golang:1.27 go test ./... -count=1`
  reported by the run as: `PASS — go test ./... -count=1 (dockerized golang:1.27)`.
- **PASS = gate exit code 0** for the whole suite. The tail is the final portion of the gate's
  combined stdout+stderr (the run slices `t.stdout + "\n" + t.stderr` to its last ~6,000 characters
  — hence the mid-line first line, and the trailing `go: downloading` lines, which are stderr from
  module downloads). Whitespace as received in the audit inputs.
- Reading the tail: every package line visible says `ok` (passed) or `[no test files]`; packages
  outside the captured tail are covered only by the gate's overall PASS verdict.

```
b.com/goposta/posta/internal/services/tracking	0.094s
?   	github.com/goposta/posta/internal/services/twofactor	[no test files]
ok  	github.com/goposta/posta/internal/services/updatecheck	0.159s
ok  	github.com/goposta/posta/internal/services/verifier	0.112s
?   	github.com/goposta/posta/internal/services/webauthn	[no test files]
ok  	github.com/goposta/posta/internal/services/webhook	9.150s
?   	github.com/goposta/posta/internal/services/workermon	[no test files]
ok  	github.com/goposta/posta/internal/services/workspace	0.130s
ok  	github.com/goposta/posta/internal/services/workspaceprovision	0.081s
?   	github.com/goposta/posta/internal/storage	[no test files]
?   	github.com/goposta/posta/internal/storage/blob	[no test files]
ok  	github.com/goposta/posta/internal/storage/migration	0.075s
ok  	github.com/goposta/posta/internal/storage/migration/upgrade	0.112s
ok  	github.com/goposta/posta/internal/storage/repositories	0.075s
?   	github.com/goposta/posta/internal/web	[no test files]
ok  	github.com/goposta/posta/internal/worker	0.256s

go: downloading github.com/redis/go-redis/v9 v9.23.0
go: downloading github.com/hibiken/asynq v0.26.0
go: downloading gorm.io/gorm v1.31.2
go: downloading github.com/jkaninda/logger v0.0.5
go: downloading github.com/robfig/cron/v3 v3.0.1
go: downloading github.com/jkaninda/okapi v1.1.0
go: downloading github.com/prometheus/client_golang v1.25.0
go: downloading github.com/golang-jwt/jwt/v5 v5.3.1
go: downloading github.com/lib/pq v1.12.3
go: downloading github.com/emersion/go-smtp v0.25.0
go: downloading github.com/jkaninda/go-utils v0.1.4
go: downloading github.com/joho/godotenv v1.5.1
go: downloading github.com/coreos/go-oidc/v3 v3.21.0
go: downloading golang.org/x/oauth2 v0.37.0
go: downloading github.com/google/uuid v1.6.0
go: downloading gorm.io/driver/postgres v1.6.3
go: downloading golang.org/x/crypto v0.58.0
go: downloading golang.org/x/net v0.62.0
go: downloading golang.org/x/text v0.43.0
go: downloading github.com/emersion/go-msgauth v0.7.0
go: downloading github.com/vanng822/go-premailer v1.37.0
go: downloading github.com/jackc/pgx/v5 v5.11.0
go: downloading github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6
go: downloading github.com/pquerna/otp v1.5.0
go: downloading github.com/go-webauthn/webauthn v0.18.2
go: downloading golang.org/x/mod v0.42.0
go: downloading github.com/aws/aws-sdk-go-v2 v1.47.5
go: downloading github.com/aws/aws-sdk-go-v2/credentials v1.20.12
go: downloading github.com/aws/aws-sdk-go-v2/service/s3 v1.114.6
go: downloading github.com/prometheus/client_model v0.6.3
go: downloading github.com/prometheus/common v0.72.1
go: downloading github.com/beorn7/perks v1.0.1
go: downloading github.com/cespare/xxhash/v2 v2.3.0
go: downloading github.com/prometheus/procfs v0.22.0
go: downloading google.golang.org/protobuf v1.36.12
go: downloading gopkg.in/natefinch/lumberjack.v2 v2.2.1
go: downloading github.com/jinzhu/now v1.1.5
go: downloading golang.org/x/sys v0.49.0
go: downloading github.com/getkin/kin-openapi v0.151.0
go: downloading github.com/jkaninda/njia v0.0.3
go: downloading gopkg.in/yaml.v3 v3.0.1
go: downloading github.com/spf13/pflag v1.0.10
go: downloading github.com/go-jose/go-jose/v4 v4.1.6
go: downloading cloud.google.com/go/compute/metadata v0.10.0
go: downloading golang.org/x/time v0.16.0
go: downloading github.com/PuerkitoBio/goquery v1.13.0
go: downloading github.com/inbucket/html2text v1.0.0
go: downloading github.com/vanng822/css v1.0.1
go: downloading github.com/jackc/pgpassfile v1.0.0
go: downloading github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761
go: downloading github.com/go-viper/mapstructure/v2 v2.5.0
go: downloading github.com/go-webauthn/x v0.3.1
go: downloading github.com/google/go-tpm v0.9.8
go: downloading github.com/tinylib/msgp v1.6.4
go: downloading github.com/aws/smithy-go v1.28.7
go: downloading github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822
go: downloading github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.25
go: downloading github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.8
go: downloading github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.8
go: downloading github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.23
go: downloading github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.8
go: downloading github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.8
go: downloading github.com/jinzhu/inflection v1.0.0
go: downloading go.uber.org/atomic v1.12.0
go: downloading github.com/spf13/cast v1.10.0
go: downloading github.com/boombuler/barcode v1.1.0
go: downloading github.com/fxamacker/cbor/v2 v2.9.4
go: downloading github.com/olekukonko/tablewriter v1.1.5
go: downloading github.com/ssor/bom v0.0.0-20170718123548-6386211fdfcf
go: downloading github.com/andybalholm/cascadia v1.3.5
go: downloading github.com/gorilla/css v1.0.1
go: downloading github.com/go-openapi/jsonpointer v1.0.2
go: downloading github.com/philhofer/fwd v1.2.0
go: downloading github.com/oasdiff/yaml v0.1.1
go: downloading github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
go: downloading github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.8
go: downloading github.com/jackc/puddle/v2 v2.2.3
go: downloading github.com/oasdiff/yaml3 v0.0.14
go: downloading github.com/x448/float16 v0.8.4
go: downloading github.com/mattn/go-runewidth v0.0.31
go: downloading github.com/olekukonko/errors v1.3.0
go: downloading github.com/olekukonko/ll v0.1.9
go: downloading golang.org/x/sync v0.24.0
go: downloading github.com/clipperhouse/uax29/v2 v2.7.0
go: downloading github.com/clipperhouse/displaywidth v0.11.0
go: downloading github.com/fatih/color v1.19.0
go: downloading github.com/mattn/go-colorable v1.0.16
go: downloading github.com/mattn/go-isatty v0.0.24
go: downloading github.com/olekukonko/cat v0.0.0-20250911104152-50322a0618f6
go: downloading github.com/goccy/go-json v0.11.2
```

---

## 5. Tests NOT executed

| Not executed | Why |
|---|---|
| `go test ./...` on a local/native Go toolchain | **No local Go toolchain** — the run's gate log states *"the repo has no local Go toolchain"*, which is why the gate ran dockerized. |
| `-race` variant, `go vet`, golangci-lint | The gate ran `go test ./... -count=1` **only** (command in §4); no race-detector run and no lint pass were part of this audit's gate. |
| **Frontend specs (Vitest)** | **Not run — documentation-only audit** (the run's own not-covered list records the Vitest suite as not run). 8 spec files exist on disk — verified via `find web/src -name "*.spec.ts"` (count 8): `admin`, `DashboardLayout`, `SecurityEvents`, `CampaignABWinner`, `CampaignDetail`, `Campaigns`, `CampaignSchedule`, `SubscriberListPreview`; six of them are new in `346eb67`. None was executed in this audit. |
| `vue-tsc` type-check, e2e, visual gates | Not part of this audit's gate, which covered the Go suite only. |

---

## 6. Findings summary

- **Registry:** 105 features across the 7 areas — **Done 74 · In Progress 9 · In Review 0 ·
  To-dos 22** (counts line supplied by the run; per-area breakdown in §3, tallied from the registry
  view by this writer).
- **Gaps:** 76 prioritized items in the fresh gap view — **P0×6, P1×20, P2×31, P3×19** (counted
  from the gap view supplied to this writer), reconciled against the frozen register of 53 gaps
  (P0×5, P1×10, P2×22, P3×16) in `todo.md:39` / `docs/product-roadmap/04-feature-gap-analysis.md`.
- **Cross-check against the frozen registry:** the frozen 199-entry registry at baseline
  `a6f1681`/`6d2d7c0` reads 109 IMPLEMENTED / 2 NV / 46 PARTIAL / 38 MISSING / 4 N/A
  (`todo.md:38`, `docs/product-roadmap/03-existing-feature-audit.md:22`); this audit is the live
  re-classification at `f5f0e92` + preserved tree.

Headline findings (all sourced from the registry and gap views supplied by the run):

1. **The P0 cluster is trust & correctness (6 items):** transactional scheduled sends broken
   end-to-end (RF-F048, GAP-01); legacy `/webhooks/bounce` parity — suppression, bounce rows,
   complaint semantics (RF-F053, GAP-02); true delivered state — `email.delivered`, delivered
   analytics, dead delivered UI paths (RF-F099); server-side editor role gate for automation
   write/event-ingest routes (GAP-11); enforcement of the `two_factor_required` policy at login
   (RF-F006, GAP-12); multi-replica cron leader election (GAP-03).
2. **The frontend-integration area is mostly recorded debt:** 4 of its 5 rows are To-dos
   documenting dead surfaces — Contact Lists pages dead end-to-end (RF-F101), dead admin API client
   methods with no backend routes (RF-F102), backend endpoints with no UI consumer (RF-F103),
   campaign duplication backend-only / campaigns not editable from the UI (RF-F104).
3. **Landed-but-downgraded claims:** five registry rows are not Done even though their refs cite
   landed commits — RF-F014 (Passkeys/WebAuthn), RF-F015 (admin impersonation), RF-F081
   (system-info feature flags), RF-F082 (route contract gate), RF-F028 (DMARC rua ingestion) —
   i.e., work committed in `f5f0e92`/`346eb67`/`0f52636` that did not meet the Done bar
   end-to-end (detail in §7).
4. **Statuses are evidence-based, not name-based:** the run's Done bar (quoted at the top of this
   document) explicitly excludes stubs, unwired settings and UI-without-backend; the five rows in
   (3) are the visible product of that bar being applied.

---

## 7. Unverified feature list

**Confidence model** (the run's classification rules): *Verified* (you read the actual
implementation) / *Partially Verified* (evidence is indirect) / *Unverified* (insufficient
evidence — investigate later).

**Provenance caveat:** per-row confidence values were assigned in-run (registrar, then the status
verifier) and are **not persisted on disk**; the registry view supplied to this writer carries
statuses only. The lists below are therefore **labeled reconstructions**, and exact per-row
membership is an open question (§9). Note on the sibling inventory: `FEATURE_INVENTORY.md` was
written in the same run, initially landed at the repo root, and was relocated to
`docs/project/` in the correcting session; its per-row
"Conf" column uses a different, writer-assigned evidence-strength scale (**high/medium/low**) — it
does not reproduce the run's Verified / Partially Verified / Unverified field. It does corroborate
the §7.1 signature: for RF-F014 and RF-F015 it records *"Registry keeps In Progress … the
implementation tracker marks … COMPLETE — discrepancy recorded as an open question."*

### 7.1 Registry rows with the downgraded-claim signature

The run's verifier instruction is to downgrade weak Done claims *"to In Progress or To-dos with
confidence Unverified/Partially Verified"*. Rows in the final registry view whose status is not
Done **while their refs cite landed commits** therefore carry Unverified or Partially Verified
confidence:

| RF id | Feature | Status | Commit refs cited |
|---|---|---|---|
| RF-F014 | Passkeys / WebAuthn (R-19) | In Progress | `0f52636`, `346eb67` |
| RF-F015 | Admin impersonation (R-20 / GAP-46) | In Progress | `0f52636`, `346eb67` |
| RF-F081 | System-info endpoint feature flags (Section-11 gate) | In Progress | `346eb67`, `0f52636` |
| RF-F082 | API route contract gate (golden file + frontend spec) (R-21) | In Progress | `346eb67` |
| RF-F028 | DMARC aggregate report ingestion (rua) (R-17) | In Progress | `f5f0e92` |

Adjacent but **not asserted** as downgraded: RF-F005 (2FA recovery codes, In Progress, ref
`todo.md` §0.2) may be natively In Progress rather than downgraded — its refs cite todo.md rather
than a commit, so its confidence cannot be inferred either way from disk.

Note: **To-dos rows are excluded** from this list — they are planned/known-absent, not
"Unverified" in the confidence sense.

### 7.2 Register-level NEEDS_VERIFICATION items (frozen 199-entry registry)

`docs/product-roadmap/03-existing-feature-audit.md:594` names the register's NEEDS_VERIFICATION
items: **TPL-016** (template preview text/preheader — *"No evidence in audits; subject templating
exists"*, `:341`) and **TPL-022** (template media/image uploads — *"Blob store exists for
attachments; template media-library surface not audited"*, `:347`). **SEC-016** was reconciled
NEEDS_VERIFICATION → IMPLEMENTED on repo verification (`internal/services/crypto/crypto.go`), so
the effective frozen total is **2 NEEDS_VERIFICATION** (`03-existing-feature-audit.md:22` and
`:592`).

---

## 8. Technical risks

The 16 risks R1–R16 from `docs/product-roadmap/06-architecture-impact.md` §3 (frozen synthesis at
baseline `a6f1681`/`6d2d7c0`; per-row evidence at `:175`, `:176`, `:177`, `:178`, `:184`, `:185`,
`:191`, `:192`, `:193`, `:199`, `:200`, `:201`, `:202`, `:208`, `:214`, `:215`), with this audit's
registry state where the registry view supplies it:

| Risk | Sev | One-line (evidence per 06 §3) | State per this audit's registry |
|---|---|---|---|
| R1 — automation write routes lack a server-side editor gate | HIGH | route group carries `auth+workspace` only; a viewer can create/activate automations server-side (`automation_routes.go:35`) | open — P0 gap |
| R2 — `two_factor_required` is dead code | HIGH | setting seeded + getter, zero consumers; login never reads it | open — RF-F006 To-dos; P0 gap |
| R3 — no cron leader lock | HIGH | in-process mutexes only; every replica runs every cron | open — P0 gap |
| R4 — `delivered`/`complained` signals remain send-path-dead | HIGH | provider ingestion records true `delivered` timeline events (`internal/services/provider_events/service.go:210-214`; corrected 2026-11-06 — the constants are no longer fully dead), but send-path `email.delivered`/`email.complained` emission, delivered-state analytics, and UI are still open: outside ingested providers "delivery-rate" is still SMTP-accept rate, and complaint analytics remain empty | open — RF-F099 To-dos; P0 gap |
| R5 — scheduled-email durability: no cancel, no reconcile coverage | MEDIUM | reconcile scans `queued` only; Redis loss strands `scheduled` rows | open — RF-F048 To-dos; P0 gap |
| R6 — bounce webhook does not suppress or emit complained | MEDIUM | provider webhook has no automatic path to suppression | partial — RF-F053 In Progress; P0 gap |
| R7 — `security_events` grows unbounded | MEDIUM | `DeleteOlderThan` has no caller | open — RF-F091 To-dos |
| R8 — tracking toggle asymmetry | MEDIUM | campaign path injects the open pixel while tracking is off | open — RF-F100 To-dos (product-decision gate) |
| R9 — webhook signatures lack timestamp/replay protection | MEDIUM | captured payloads replayable indefinitely | open — P1 gap |
| R10 — single Redis SPOF, mixed failure semantics | MEDIUM | 9 roles on one Redis; no HA docs | open — P2 gap (Redis HA guidance) |
| R11 — weighted queues probabilistic, not strict | LOW | bulk can starve transactional under load | open — benchmark/document only |
| R12 — frontend test coverage | LOW | was 2 spec files at the synthesis baseline | spec files now **8** on disk (verified via `find web/src -name "*.spec.ts"`, count 8); still none executed in this audit |
| R13 — no backup/restore tooling | MEDIUM | Postgres is the sole system of record | **mitigated since the synthesis** — RF-F095 Done (pg_dump cron + restore runbook; `todo.md` §0.2) |
| R14 — observability blind spots | LOW | metrics default-off; no error monitoring; no per-queue latency | partial — RF-F096 Done (metrics/health); RF-F097 To-dos (error-monitoring hook) |
| R15 — provider ceiling: generic SMTP only | INFORMATIONAL | no direct-to-MX, no provider registry, no IP concept | structural — Track A Phase 1 outbound MTA (P1 gap) |
| R16 — tenant isolation | INFORMATIONAL | verified strong — enforced by construction (`middleware.go:312-341`) | keep as the pattern for all new route groups |

---

## 9. Verification limitations & open questions

1. **Gate provenance.** The PASS verdict and tail in §4 are the run's captured gate output; this
   writer did not re-execute the suite. The command and the tail-slicing behavior are quoted from
   the run script's embedded source.
2. **Tree drift during the run.** The run started with HEAD `f5f0e92` + 34 preserved entries; the
   two commits `346eb67` (04:08:28 +0530) and `0f52636` (04:19:49 +0530) landed during the run and
   the live tree is now clean at `0f52636` (verified: `git status --porcelain=v1` → 0 lines). The
   gate mounted the live working tree (`docker -v <repoRoot>:/app`), so the tested tree was the
   working tree *at gate-execution time* — its exact contents lie somewhere between `f5f0e92` and
   the two later commits and cannot be pin-pointed from disk. The registry evidence reflects the
   snapshot state.
   **Correction (2026-11-06, docs-fix session):** the "clean / 0 lines" observation above is
   wrong — the live tree carried `README.md` modified plus this doc suite's own untracked files
   (12 entries by `git status --porcelain=v1`, re-run in the correcting session); see the
   correcting entry in `STATUS_HISTORY.md`.
3. **The 34-entry list is not recoverable.** It was runtime state of the run's git-status read and
   is not persisted anywhere on disk; §2's 48-file list is a reconstruction, not the list.
4. **Per-row confidence is not persisted.** §7 is a labeled reconstruction from the verifier's
   documented downgrade rule and the rows' commit refs. The sibling `FEATURE_INVENTORY.md`
   (docs/project/) carries per-feature evidence ratings on a different high/medium/low scale — it does not
   resolve which registry rows held Unverified vs Partially Verified; that remains an open
   question recoverable only from the run's records.
5. **No local Go toolchain; dockerized run only.** The gate was the only test execution, in
   `golang:1.27` under Docker; no `-race`, no lint, no `go vet` in this gate.
6. **Frontend specs not run.** 8 spec files exist; 0 executed — documentation-only audit.
7. **GitHub Project #1 unreachable.** The issues CLI is available, but the Project #1 GraphQL API
   is **unavailable — the token lacks `read:project`/`project` scopes** (the run's probe
   `gh project view 1 --owner ReForwarder --format json` fails). Manual procedure: run
   **`gh auth refresh -s read:project,project`**, complete the browser re-authentication, then
   re-run the probe command to confirm access before attempting any board sync.
8. **Source-doc staleness.** The historical audit is pinned at 2026-11-05 / HEAD `a6f1681`
   (`docs/product-roadmap/03-existing-feature-audit.md:5-6`), and `todo.md` was generated
   2026-11-06 from 2026-11-05 research (`todo.md:3`); this audit supersedes both for feature status
   at `f5f0e92` + preserved tree.
9. **Run-script citations.** The workflow run script cited throughout lives under gitignored
   `.zcode/` (`.gitignore:30`); its quoted rules are reproducible only while that local file exists.
