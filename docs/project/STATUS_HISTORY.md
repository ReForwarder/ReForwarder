# STATUS_HISTORY — feature-status decision ledger

Append-only ledger of the repo's feature-status decisions. One entry per producing run that re-baselines or changes registry statuses (`RF-F001`…`RF-Fnnn`). Run `dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7` produced the initial entry below.

## Ledger rules

1. **Append-only.** Never rewrite or delete an entry. A correction is a new entry that cites the entry it supersedes.
2. **Every entry carries:** date, producing run id, HEAD audited (full sha + subject), test-gate result, working-tree state, and the four status counts. An entry without a producing run id is not a valid status decision.
3. **Evidence before status.** A status may only move on test-gate evidence — `go test ./... -count=1`, PASS — recorded at the stated HEAD. A changed file alone never marks a feature Done (see `PROJECT_MAINTENANCE.md` §5).
4. **Counts reconcile.** The four counts always sum to the registry size (105 at the initial entry: `RF-F001`–`RF-F105`). If the registry grows, features get new `RF-` IDs and the entry records the new total.
5. **History is not the queue.** Work ordering lives in `PRIORITIES.md`; detailed task state lives in `BACKLOG.md`; this file records decisions and their provenance only.

## Status semantics

Defined by this ledger; `In Review` was unused (zero) at the initial entry.

- **Done** — implemented at the audited HEAD with test-gate evidence; any residual scope is tracked as its own registry item or gap entry.
- **In Progress** — partially implemented; the residual work is enumerated in `PRIORITIES.md` / `BACKLOG.md` (e.g. `RF-F005` 2FA recovery codes → the P1 completion item).
- **In Review** — implemented and awaiting review.
- **To-dos** — not implemented at the audited HEAD.

---

## Entries

### 2026-11-06 — Initial audit entry

| Field | Value |
| --- | --- |
| Producing run | `dwfrun-84c797c2-39b6-49df-9612-c3dc664a20e7` |
| HEAD audited | `f5f0e921455de901370b5f37b7112b634f6c2066` — "Add P3 platform improvements: bounded retries, scheduled triggers, segment conditions, per-key limits, DMARC ingestion" (2026-11-06T00:44:14+05:30) |
| Working tree at audit | 34 dirty/untracked entries (preserved, uncommitted) |
| Test gate | **PASS** — `go test ./... -count=1` (dockerized `golang:1.27`). Result recorded from the producing run; not re-executed for this ledger entry. |
| Done | **74** |
| In Progress | **9** — `RF-F005`, `RF-F014`, `RF-F015`, `RF-F026`, `RF-F027`, `RF-F028`, `RF-F053`, `RF-F081`, `RF-F082` |
| In Review | **0** |
| To-dos | **22** — `RF-F006`, `RF-F023`, `RF-F029`, `RF-F040`, `RF-F048`, `RF-F056`, `RF-F057`, `RF-F061`, `RF-F071`, `RF-F073`, `RF-F074`, `RF-F075`, `RF-F076`, `RF-F091`, `RF-F092`, `RF-F097`, `RF-F099`, `RF-F100`, `RF-F101`, `RF-F102`, `RF-F103`, `RF-F104` |
| Total | 105 (`RF-F001`–`RF-F105`); 74 + 9 + 0 + 22 reconciles against the producing run's registry view. |
| Delta vs previous entry | n/a — initial entry |

**Notes**

- Provenance: the counts and the HEAD are the initial status decisions handed to this ledger by the producing run's registry view (`RF-F001`–`RF-F105`). The In Progress and To-dos ID lists above were cross-checked against that view: the 9 In Progress and 22 To-dos IDs are enumerated explicitly; the remaining 74 are Done.
- **Repo state at ledger-writing time (observed 2026-11-06 in the writing session):** HEAD has advanced to `0f52636172e19f16a80f9fae421bb57e99ebc6d4` ("Fix WebAuthn nil-user abort path and duplicate impersonation stop audit", 2026-11-06T04:19:49+05:30) and the working tree is clean (`git status --porcelain | wc -l` → 0). Two commits landed after the audited HEAD — `346eb67` ("Complete remaining roadmap items: WebAuthn, impersonation, feature flags, contract test") and `0f52636` — together touching 48 files (+12,072/−27, per `git diff --stat f5f0e92..HEAD`). Whether the 34 dirty/untracked entries recorded at audit time are exactly the content these commits landed is **not verified by this entry** — an open item for the next sync run, which should re-run the test gate at the new HEAD before moving any status.

---

### 2026-11-06 — Correction entry (working-tree observation; no status change)

| Field | Value |
| --- | --- |
| Producing run | none — documentation-fix session, 2026-11-06. Per ledger rule 2 this is **not a status decision**: no registry status moves in this entry. |
| Supersedes | The **Repo state at ledger-writing time** observation in the 2026-11-06 initial entry above (its "working tree is clean (`git status --porcelain \| wc -l` → 0)" claim) and the equivalent claim in `docs/project/FEATURE_AUDIT.md` §9 item 2 ("`git status --porcelain=v1` → 0 lines"). |
| HEAD | Unchanged from the superseded observation: `0f52636172e19f16a80f9fae421bb57e99ebc6d4`. |
| Working tree (re-observed) | **Not clean** — `git status --porcelain=v1` → 12 entries: `README.md` modified + 10 untracked root docs (AGENTS.md, ARCHITECTURE_OVERVIEW.md, BACKLOG.md, FEATURE_GAP_ANALYSIS.md, FEATURE_INVENTORY.md, PRIORITIES.md, PROJECT_MAINTENANCE.md, PROJECT_OVERVIEW.md, ROADMAP.md, STATUS_HISTORY.md) + untracked `docs/project/`. Command run 2026-11-06 in this correcting session. The superseded "clean / 0 lines" claims cannot have been true while the doc suite itself sat untracked on disk. |
| Test gate | Not re-run in this session. The initial entry's gate-provenance caveat (tested tree between `f5f0e92` and `0f52636`) stands, as does the deferral of a re-run at `0f52636` to the next sync run. |
| Done / In Progress / In Review / To-dos | Unchanged: **74 / 9 / 0 / 22** (105 total). |
| Delta vs previous entry | none — observation correction only |
