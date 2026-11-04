# ROADMAP IMPLEMENTATION TRACKER

> Implements `docs/research/reforwarder-stalwart-roadmap.md` (28 recommendations, R-16 tombstoned \u2014 session management already exists).
> **Baseline:** `main` @ `7a84e46`, working tree clean except `DEVELOPMENT_HISTORY.md` + `compose.override.yml` (pre-existing untracked).
> **Policy:** tracker-driven, phase-ordered, per-phase verification battery (build + `go test -race ./...` + lint + `vue-tsc` where frontend changed + targeted E2E/visual) then per-phase commit + push. R-16 is NOT re-implemented.

## Status legend

NOT STARTED \u00b7 IN PROGRESS \u00b7 BLOCKED \u00b7 PARTIALLY COMPLETE \u00b7 COMPLETE \u00b7 NEEDS VERIFICATION

## Tracker

| ID | Feature | Priority | Initial status | Implementation status | Backend | Frontend | Tests |
| --- | ------- | -------- | -------------- | --------------------- | ------- | -------- | ----- |
| R-01 | Scheduled campaigns execute | P0 | Broken (scheduler dead) | COMPLETE | COMPLETE | COMPLETE | COMPLETE |
| R-21 | Subscriber-list segment preview fix | P0 | Broken (URL + shape) | COMPLETE | COMPLETE | COMPLETE | COMPLETE |
| R-22 | SMTP client timeouts/deadlines | P1 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | n/a | NOT STARTED |
| R-02 | DKIM signing + key lifecycle | P1 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-03 | Delivery queue administration | P1 | PARTIALLY IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-04 | Provider event ingestion | P2 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-05 | Bounce/failure classification | P2 | PARTIALLY IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-06 | Webhook delivery replay (+backoff fix) | P2 | PARTIALLY IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-07 | Domain deliverability health | P2 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-08 | Campaign A/B winner logic | P2 | PARTIALLY IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-09 | Timezone-aware sending | P2 | STUB | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-10 | 2FA recovery codes | P2 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-11 | Backup & restore automation | P2 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-23 | require_domain_verification wiring | P2 | DEAD SETTING | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-24 | Header/Subject CRLF hardening | P2 | MISSING | NOT STARTED | NOT STARTED | n/a | NOT STARTED |
| R-25 | Relay credential hashing | P2 | UNSALTED SHA-256 | NOT STARTED | NOT STARTED | n/a | NOT STARTED |
| R-26 | Atomic send/campaign claims | P2 | MISSING | NOT STARTED | NOT STARTED | n/a | NOT STARTED |
| R-12 | Prometheus improvements | P3 | PARTIALLY IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-13 | Scheduled automation triggers | P3 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-14 | Segment-membership conditions | P3 | NOT IMPLEMENTED (deferred) | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-15 | Per-API-key rate limits | P3 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-27 | Bound retry sweep query | P3 | UNBOUNDED | NOT STARTED | NOT STARTED | n/a | NOT STARTED |
| R-28 | Scope ?token= auth to SSE | P3 | OVER-BROAD | NOT STARTED | NOT STARTED | n/a | NOT STARTED |
| R-17 | DMARC aggregate report ingestion | P3 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-18 | Per-API-key usage analytics | P3 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-19 | WebAuthn/passkeys | P4 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-20 | Admin impersonation | P4 | NOT IMPLEMENTED | NOT STARTED | NOT STARTED | NOT STARTED | NOT STARTED |
| R-16 | ~~Session/device management~~ | \u2014 | ALREADY IMPLEMENTED | EXCLUDED | existing | existing | existing |

## Phase log

- **Phase 0 (2026-11-05):** baseline verified at `7a84e46`; 27 active items confirmed outstanding; tracker created. Section-11 pre-classification: IMPLEMENT = feature-flag surfacing, API contract test, frontend specs; DEFER = SF-61 DNS auto-publication, SF-78 at-rest encryption, SF-79 FTS, SF-96 metric alerts, SF-100 settings history (partial via audit), SF-49/50 expression unification; RESOLVED/N-A = SF-11 (no auto-forwarding exists), SF-106/109 (okapi-owned shutdown, documented), SF-108 (inventory in audit doc).

## Per-item notes

### R-01 — Scheduled campaigns execute (Phase 1, 2026-11-05)

- **What existed:** `CampaignRepository.FindScheduledReady()` (`campaign_repo.go:152`) had zero callers. `Send` with a future `scheduled_at` atomically transitioned draft→scheduled (`campaign_handler.go:351`) and nothing ever fired it; the stuck-campaign restart cron only revived status=`sending`.
- **What changed:** new `campaign-scheduler` cron job (`internal/cron/jobs/campaign_scheduler.go`, schedule `* * * * *` — scheduled sends never wait longer than a tick) registered in `initCronManager` (`cmd/posta/server.go`) beside campaign-restart under the existing `producer != nil` guard. Each tick: `FindScheduledReady()` → atomic claim scheduled→sending via `TransitionStatus` (RowsAffected==0 → `ErrStatusConflict` → skip, another worker won) → `EnqueueCampaignStart(id)`. Enqueue failure rolls the status back to `scheduled` so the next tick retries (same rollback pattern as the Send handler). Pause/cancel during `sending` stays enforced downstream (`HandleCampaignStart` refuses non-sending rows); paused/cancelled campaigns never match the claim's `status = 'scheduled'` filter. Restart-safety is inherent: state is DB-backed (status + scheduled_at) and the every-minute poll re-finds anything left in `scheduled`; the producer TaskID `campaign:start:<id>` collapses double-fires.
- **Frontend:** Create-campaign modal (`web/src/views/campaigns/Campaigns.vue`) gained an optional "Schedule send" datetime-local field (`formScheduledAt`); sent as RFC3339 `scheduled_at` only when set — empty = send immediately. CampaignDetail's existing scheduled-time display unchanged.
- **DB/API changes:** none (existing columns and endpoint reused; no scheduler-interval setting added — the interval is the cron schedule string).
- **Security notes:** none — no new inputs; the scheduler is system-side and audience scoping happens in the worker as before.
- **Tests added:** `internal/cron/jobs/campaign_scheduler_test.go` — fake `database/sql` driver harness (the `segment_handler_test.go` pattern) driving the real `CampaignRepository`: due→claimed+enqueued; future→untouched; already-sending→untouched; paused/cancelled→untouched; claim conflict (RowsAffected 0)→no enqueue; enqueue failure→rolled back to `scheduled`.
- **Results:** `go build ./...` + `go test -count=1 -race ./internal/...` green; gofmt clean.
- **Limitations:** a hard crash in the narrow window between a successful claim and the enqueue leaves the campaign in `sending` with zero pending messages, which the campaign-restart sweep deliberately does not revive (pending==0) — the window is a single Redis call. Rows with status=`scheduled` but NULL `scheduled_at` are never picked up (Send always sets it when scheduling).

### R-21 — Subscriber-list segment preview fix (Phase 1, 2026-11-05)

- **What existed:** triple mismatch — the API posted to `/workspaces/current/subscriber-lists/preview` while the backend registers `/subscriber-lists/preview-segment` (`workspace_resource_routes.go:1092`), and the view parsed a pageable envelope (`res.data.data ?? []` + `res.data.pageable.total_elements`) while the handler returned `{"count": N}`. The Preview button on every dynamic list always failed.
- **What changed (handler-upgrade option, documented choice):** `PreviewSegment` (`internal/handlers/subscriber_list_handler.go`) now returns `{count, subscribers}` — subscribers is the first page (limit 20, `previewSegmentSampleSize`) of matches via the existing `subscriberRepo.FindByFilterRules`, whose total doubles as the count (one call); entries are shaped minimally (id, email, name, status) in the handler. `web/src/api/subscriberLists.ts` posts to `/preview-segment` and types the response (`SegmentPreview`, `SegmentPreviewSubscriber`). `web/src/views/subscriber-lists/SubscriberListDetail.vue` parses `res.data.data.count`/`.subscribers`, renders the count plus a first-20 sample table (Created column dropped — the sample shape is minimal), a "Showing first N of M" note when the count exceeds the sample, and a "No subscribers match these filters" zero-match state; API errors keep the existing toast and the button/confirmation UX is unchanged.
- **DB/API changes:** response body of `POST /workspaces/current/subscriber-lists/preview-segment` gained a `subscribers` array (additive; `count` semantics unchanged). No DB change.
- **Security notes:** the sample is scope-filtered through the same `ApplyScope` path as the count (`FindByFilterRules` applies the workspace scope to both queries) — no cross-workspace leak; the projection omits custom_fields/user_id.
- **Tests added:** `web/src/views/subscriber-lists/SubscriberListPreview.spec.ts` (vitest, happy-dom) — mocks the axios client one level below the api module so the real `subscriberLists.ts` runs, and asserts the exact `/preview-segment` URL with the `filter_rules` payload, count + sample rendering, the zero-match state, the "showing first N" cap note, and the error toast on rejection.
- **Results:** `vue-tsc --noEmit` clean; vitest 4/4 green.
- **Limitations:** the sample is the first 20 by `created_at DESC` (same ordering as the members listing); the preview has no pagination — the count is authoritative.
