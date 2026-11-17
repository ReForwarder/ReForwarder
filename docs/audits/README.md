# docs/audits/ — Parity Ledgers & Audit Records

The ledgers of record for reference-platform feature parity, plus the audit reports behind them. **Live status for everything: [`docs/PROJECT_STATUS_DASHBOARD.md`](../PROJECT_STATUS_DASHBOARD.md).**

| File | What it is | Status |
|---|---|---|
| `FEATURE_PARITY_AUDIT.md` | **The live ledger of record** — 123 SF records (Categories A–M + CA appendix) with per-record implementation evidence, live tally, programme tracker | 📌 Live (owned by the running PARTIAL-completion programme — no banner, deliberately) |
| `FINAL_STALWART_FEATURE_VERIFICATION.md` | The mandated verification table — all 123 records re-graded (COMPLETE/PARTIAL/MISSING/BROKEN/BLOCKED/EXCLUDED) | 📌 Live (same programme ownership) |
| `SOURCE_REGISTER.md` | Catalog of every document feeding the parity programme; authority classes + maintenance rules | 📌 Current reference |
| `FINAL_VERIFICATION_REPORT.md` | Option B programme final report (OB-0..OB-6 narrative, 9 ADR outcomes, gate battery) | 🗄️ Historical snapshot (2026-11-12) |
| `GIT_COMMIT_PROVENANCE_AUDIT.md` | Provenance scrub verification over all reachable commit messages | 🗄️ Historical record (2026-11-12) |
| `verification/V-*.md` (13 files) | Per-category verification-wave evidence (V-A1…V-M, V-R, V-DOCS) | 🗄️ Historical evidence layer (2026-11-07) |

**Vocabulary:** the parity register grades records COMPLETE / PARTIAL / PLANNED / EXCLUDED; the FINAL_STALWART document re-grades with COMPLETE / PARTIAL / MISSING / BROKEN / BLOCKED / EXCLUDED. Where they differ, `FEATURE_PARITY_AUDIT.md` is the ledger of record.

**Rule (AGENTS.md §5):** every status in these ledgers carries `path:line` or a command actually run. A changed file alone never marks a feature Done.
