# docs/implementation/ — Programme Trackers & Contract Golden File

| File | What it is | Status |
|---|---|---|
| [PARTIAL_FEATURE_COMPLETION_TRACKER.md](PARTIAL_FEATURE_COMPLETION_TRACKER.md) | **The live programme queue** — the 26 PARTIAL parity records in dependency order, with per-status evidence and append-only status log | 📌 Live (owned by the running programme — no banner, deliberately) |
| [ROADMAP_IMPLEMENTATION.md](ROADMAP_IMPLEMENTATION.md) | Tracker for the 28-item relay roadmap R-01…R-28 with per-item notes and the 2026-11-07 acceptance audit | ✅ Complete — historical |
| [api_routes.txt](api_routes.txt) | Route-contract golden file pinned by `internal/routes/contract_test.go` (RF-F082); regenerate with the test's `-update` flag | 📌 Current (regenerated with route changes) |

**Start here for new work:** the PARTIAL-completion tracker's execution-order table. Current snapshot: 4 of 26 records COMPLETE (SF-08, SF-09, SF-02, SF-25), next up SF-26 — see [`docs/PROJECT_STATUS_DASHBOARD.md`](../PROJECT_STATUS_DASHBOARD.md) §3.
