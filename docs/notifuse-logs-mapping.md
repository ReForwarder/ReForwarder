# Notifuse Logs & Message History — Source Mapping for the ReForwarder Implementation

| | |
|---|---|
| **Reference repo** | [Notifuse/notifuse](https://github.com/Notifuse/notifuse) — pinned commit `f635aa09c1050c2df1e8f7b7a0dd6e516019b146` (2026-10-28), local clone at `C:\Users\Ibrahim\.zcode\tmp\notifuse-analysis\notifuse` |
| **Companion report** | `docs/security-research/` series (Stalwart) — same clean-room method |
| **Date** | 2026-11-02 |
| **Method** | GitHub REST API tree walk + raw source reads of ~10 key files (verification pass), full local clone for the implementation deep-dive |
| **Status** | Research deliverable for the Logs & Message History feature. Clean-room design adoption — **no Notifuse code is copied** (see license verdict). |

---

## 1. Repo and license verdict (verified)

- **Architecture**: Go backend (`internal/` — service/domain/http/repository/migrations) + React console (`console/src`, TSX/Ant Design — *not* Vue) + `pkg/`.
- **Two-database design**: one **system DB** (users, workspaces, settings, `audit_logs`) plus **one database per workspace** (contacts, templates, broadcasts, `message_history`, webhook tables, automations). Workspace DDL: `internal/database/init.go` (`InitializeWorkspaceDatabase`); migrations `internal/migrations/v4.go…v42.go`.
- **License**: root `LICENSE` = **Business Source License 1.1** for "Notifuse v40.0 and later" (`LICENSING.md`: v39.x and earlier are permanently **AGPL-3.0-or-later**; `web_analytics_sdk/` is AGPL in every version). No per-file license headers.
- **Verdict for ReForwarder (AGPL-3.0-or-later)**: current `main` is BSL — behavior may be studied, **code must not be copied**. The pre-v40 history (commit `0594cb95 "agpl3"`, present in the clone) is AGPL-3.0-or-later and is the safer reference. Note: Notifuse's **audit-log recording** is one of its BSL "Licensed Features" — ReForwarder implements its own audit trail independently.

## 2. The 8-feature mapping table (verified paths → behavior → ReForwarder equivalent)

| Feature | Notifuse source (verified) | How it works | ReForwarder equivalent |
|---|---|---|---|
| **Message history** | `internal/domain/message_history.go`, `internal/repository/message_history_postgre.go`, `internal/http/message_history_handler.go` (`/api/messages.list`), DDL `internal/database/init.go` ~197–231 | Per-workspace `message_history` row per email: id, external_id, smtp_message_id, contact_email, broadcast/automation/transactional/list/template ids + version, channel, status_info, `message_data JSONB` (encrypted at rest), attachments, **8 denormalized event timestamps** (sent/delivered/failed/opened/clicked/bounced/complained/unsubscribed), cursor pagination (base64 `RFC3339Nano~id`), limit 20–100 | **Extend the existing `emails` table** (already one row per send from every pipeline path: API `service.go:769-936`, worker `handler.go:127-490`, campaigns `campaign_processor.go:324-373`, relay `backend.go:184-232`, form replies) + add external_id and the 6 missing denormalized timestamps. List API with the same filter semantics, offset pagination per ReForwarder convention |
| **Message events** | **No `message_events` table** — events are the 8 write-once timestamp columns set via `SetStatusesIfNotSet`/`SetClicked` (repo file); raw provider payloads land separately in `inbound_webhook_events`; `clicked_links JSONB` on the row | Write-once status semantics (a later duplicate event never overwrites the first timestamp); click URLs tracked per row | **`email_events` table (new)** — full per-event history (type, occurred_at, provider, ip, ua, url, error, metadata) *plus* the same write-once denormalized timestamps on `emails` for fast filtering. Superset of Notifuse's approach: filterable like Notifuse AND inspectable like an event log |
| **Email content storage** | `message_history.message_data JSONB` = the template-compile data, **encrypted** with the workspace key; NO html/mjml column — rendered content is re-derived from `template_id`+`template_version` | Content re-rendering, not persistence | **ReForwarder already persists `html_body`/`text_body` on every email row** (verified) — strictly better for the inspection requirement. Add `template_data JSONB` snapshot at send time. Content served via a dedicated authorized endpoint, removed from list payloads |
| **HTML preview** | Console renders message content client-side (no server preview endpoint found on `messages.*`; not exhaustively verified) | — | **`EmailDetail.vue` already has the sandboxed `<iframe :srcdoc>` pattern** — reuse for the new Logs detail view; 4 tabs (HTML Preview / MJML Source / Template Data / Metadata) |
| **MJML source** | Notifuse compiles MJML at design time; `message_history` stores no MJML column | Re-render from template version | ReForwarder templates also compile to HTML at save — **no MJML source exists in the pipeline**. The MJML tab ships with an honest "not captured" empty state (per spec: never invent source) |
| **Incoming webhooks** | `internal/service/inbound_webhook_event_service.go` (`ProcessWebhook` 280–470), `internal/http/inbound_webhook_event_handler.go` (`POST /webhooks/email?provider=…`), `internal/repository/inbound_webhook_event_postgre.go`, DDL ~244–260 | ESP callbacks (ses, postmark, mailgun, sparkpost, mailjet, smtp, supabase, sendgrid) → raw payload stored in `inbound_webhook_events` (raw_payload TEXT, bounce/complaint fields, unique reply-dedup index) → parsed → `SetStatusesIfNotSet` on message_history → bounce classification → unsubscribes | **`incoming_webhook_events` table (new)** mirroring the pattern: raw payload stored before processing, dedup, process error retained. Initial sources: the existing bounce webhook (`/api/v1/webhooks/bounce`) + tracking open/click callbacks; ESP integrations are a future extension (raw SMTP has no callbacks — honest `delivered`/`complained` semantics documented below) |
| **Outgoing webhooks** | `internal/domain/webhook_subscription.go`, `internal/service/webhook_delivery_worker.go`, `internal/repository/webhook_delivery_postgres.go`, DDL ~409–445, `POST /api/webhookSubscriptions.*` | `webhook_deliveries` rows: status pending/delivering/delivered/failed, attempts, max_attempts, next_attempt_at, last_response_status/body, last_error, claimed_at lease + reclaim sweep; long backoff ladder; auto-disable after consecutive failures | **Extend the existing engine** (`internal/services/webhook/dispatcher.go` — HMAC-signed, 3 attempts, 2s×n backoff; `webhook_deliveries` table stores one final outcome). Add: **`webhook_delivery_attempts`** per-attempt rows (status code, truncated response, error) + new Logs page with subscription/status filters + attempt history. No second delivery engine |
| **Audit logs** | `internal/domain/audit_log.go`, `internal/domain/audit_actions.go`, `internal/http/middleware/audit.go` (middleware wraps every route; AuditRecord in context enriched by services), `internal/database/schema/audit_logs_tables.go`, `internal/repository/audit_log_postgres.go`, `internal/http/audit_log_handler.go` (`/api/auditLogs.list/get/actions/export` — streaming CSV/NDJSON, max 100k) | One row per control-plane action (action = RPC route name); fields: occurred_at, workspace_id, action, category, **outcome** (2xx/3xx success, 402/403 denied, else failure), status_code, actor_type/id/email/name/role, auth_method, target_type/id/name, ip_address, user_agent, request_id, changes{old,new}; stored in the SYSTEM DB (survives workspace deletion); append-only guard trigger; purge worker; filters incl. scope (workspace/deployment) | **Extend the existing `events` table** (already: category/type/workspace/actor/ip/metadata via `eventbus` + `audit` logger; ~20 audited actions already published) with `outcome`/`target_type`/`target_id`; add filters + scope + **CSV export**; publish `email.sent/failed` + campaign lifecycle to the audit trail (currently webhook-only). No append-only trigger in v1 (documented difference — ReForwarder's events are retention-cycled by `audit_log_retention_days`) |

## 3. Logs UI reference (behavior contract)

`console/src/pages/LogsPage.tsx` + `console/src/components/messages/MessageHistoryTab.tsx` + `webhooks/InboundWebhookEventsTab.tsx` + `webhooks/OutgoingWebhooksTab.tsx` + `logs/AuditLogsTab.tsx` (access-gated via `useAuditLogAccess.ts`):

- One Logs page, 4 tabs, active tab in the URL query.
- Message History: **two-line filter builder** — line 1 = the 8 status booleans (is_sent/is_delivered/is_failed/is_opened/is_clicked/is_bounced/is_complained/is_unsubscribed), line 2 = the field filters; active filters shown as chips; **"Clear All" appears only when ≥1 filter is active** and clears state + URL params; cursor-based "load more"; filters persisted in URL query; status semantics are NON-exclusive (a message can be sent+delivered+opened+clicked).
- Audit logs: filters (action, outcome, actor email, target id, IP, date range) + This workspace / Whole deployment scope toggle + Export button.

ReForwarder adaptation: collapsible **"Logs"** nav group with 4 child routes (the established Security-section pattern) instead of tabs; URL-mirrored filters per the existing `Emails.vue` search convention; offset pagination per project convention.

## 4. Honest event semantics (documented difference)

Notifuse receives delivery/complaint callbacks from 7 ESP integrations, so `delivered`/`complained` are real events there. **ReForwarder delivers via raw SMTP** (`em.sender.Send`), which provides no delivery or complaint callbacks:

- `sent` = accepted by the SMTP server (existing transitions).
- `bounced` = 5xx RCPT rejection at send time OR the existing bounce webhook (`/api/v1/webhooks/bounce`).
- `opened`/`clicked` = tracking pixel/redirect callbacks (extended to ALL email per product decision, honoring the global tracking toggle).
- `unsubscribed` = the existing unsubscribe flow.
- `delivered` / `complained` = **never fabricated**; they appear only when a future ESP-webhook integration supplies them. The UI shows Sent as the terminal state otherwise.

## 5. Gaps in Notifuse that ReForwarder's design deliberately improves

1. Notifuse re-derives content from template versions; ReForwarder **persists the actual rendered content** at send time (survives template edits/deletes — the user's explicit requirement).
2. Notifuse has no single-message GET/content endpoint on `messages.*`; ReForwarder ships an authorized `content` endpoint.
3. Notifuse's webhook deliveries keep only the final outcome summary; ReForwarder adds per-attempt rows.
4. ReForwarder's `email_events` keeps full per-event history rather than only 8 write-once timestamps.

## 6. Explicitly unverified in Notifuse (for the W1b deep-dive)

Exact `webhook_subscriptions`/`webhook_deliveries` DDL text; per-ESP webhook signature verification; the full `AuditedActions` catalogue; open/click tracking endpoint internals; whether a message-detail drawer exists beyond `MessageHistoryTab.tsx`; the v42 migration body. The W1b agent verifies these from the local clone and feeds corrections into the contracts.
