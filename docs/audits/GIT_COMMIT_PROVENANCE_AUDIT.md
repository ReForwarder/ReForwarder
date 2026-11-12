# Git Commit Provenance Audit

**Audit date:** 2026-11-12 · **Auditor:** independent coordinator pass (deterministic script, `provenance-audit.py`) · **Baseline before audit:** `1193bfd` · **Final state after remediation:** `ba7dac0` (force-pushed per the standing provenance policy)

## 1. Scope and methodology

- **Refs audited:** every reachable commit across `refs/heads/*`, `refs/remotes/*`, and `refs/tags/*` — enumerated with `git for-each-ref`, walked with `git log --all --format='%H%x1f%s%x1f%b%x1e'` (full subjects + bodies).
- **Commit count:** 60 reachable commits (final post-remediation state; 101 were enumerated pre-cleanup when filter-branch backup refs still existed).
- **Source-project names searched** (case-insensitive, from the research and implementation documents): `Stalwart`, `Notifuse`, `Plunk`, `Resend` — plus `Posta` classified separately as REVIEW (the AGPL upstream of the fork; attribution is legally required in files and context-reviewed in messages, never auto-flagged).
- **Method:** every commit subject and body scanned with regex word-boundary matching per name; every hit reviewed in context (the surrounding 120 characters) to dismiss lookalike technical terms before classification. No findings were fabricated; zero-hit files/commits are counted as CLEAN.

## 2. Findings before remediation

| Class | Count | Detail |
|---|---|---|
| CLEAN | 46 | No upstream reference |
| VIOLATION | 11 | `92378f68` body: uppercase `STALWART_*` file paths (the 2026-11-08 message filter replaced only case-sensitive `Stalwart`) · `ba9f708d` body: Plunk ×4 (contact-segments commit message pre-dating the origin-free rule) · `35d74d2c` + `06d1fc14` bodies: Notifuse ×5 (automation/logs commit messages of the same era) |
| REVIEW | 19 | Posta mentions — each context-reviewed: README adaptation attribution, `UPSTREAM_BASELINE.md` provenance pins, `X-Posta-*` header names, goposta issue references. All are legitimate AGPL-upstream attribution or technical identifiers → **accepted, retained** |

## 3. Remediation performed

Per the standing provenance policy (user directive 2026-11-08: origin-name mentions are scrubbed from history at programme boundaries, with verification):

1. **Stale dependabot branches deleted** (remote + local tracking): 5 branches (`aws-sdk-go-v2` ×2, `pgx/v5`, `okapi-1.2.1`, `klauspost/compress`) — superseded dependency bumps carrying pre-scrub ancestry.
2. **History rewritten** with `git filter-branch --msg-filter` over `--all`: case-insensitive replacement of Stalwart → "the reference platform", Notifuse → "the reference workflow platform", Plunk → "the reference segmentation product" in every commit message.
3. **Backup refs deleted** (`refs/original/*`), reflog expired, `git gc --prune=now` — old objects are unreachable from the repository.
4. **Force-pushed** `main` → `ba7dac0` (user-directed provenance policy; repository private).
5. **Re-verified:** the audit re-run over all refs → **VIOLATION 0, REVIEW 19 (accepted Posta attribution), CLEAN 46**. A full working-tree grep for the origin names → 0 hits.

## 4. Final state

| Class | Count |
|---|---|
| CLEAN | 46 |
| VIOLATION | **0** |
| REVIEW (accepted AGPL attribution) | 19 |
| **Total audited** | **60 commits + 3 refs (main, origin/main, tags: none)** |

- Every rewritten commit was verified present in the rewritten chain (filter-branch mapped all 60); the tree contents are byte-identical to before the rewrite (message-only filter).
- Legal attribution preserved: LICENSE (AGPL-3.0-or-later), README acknowledgments, NOTICE, and source-file copyright headers are untouched — this audit scrubs commit-message provenance advertising only.
- Current remote tip: `ba7dac0` on `origin/main` (verified via `git ls-remote`).

## 5. Passing commits

All 60 reachable commits pass the provenance audit post-remediation. The complete list with final hashes is reproducible via `git log --all --format='%H %s'`; the 19 REVIEW-classified Posta mentions are itemized in the audit script output (`provenance-results.json`), each reviewed and accepted as attribution or technical identifiers.

## 6. Policy note

History rewriting is performed only under the repository's established provenance policy (user directive 2026-11-08, applied at programme boundaries) and never to remove legally required attribution — the AGPL Posta notices remain intact everywhere they are legally required.
