# Data Ownership Map — Option B

**Status:** Accepted (2026-11-07) · Normative source: [ADR-008](ADR-008-data-event-ownership.md). This doc is the generated ownership map every Phase 1–6 migration is checked against.

## 1. Ownership rules (binding)

1. Every row belongs to a tenant through `workspace_id`, either directly or via a strict parent chain (mailbox → user → workspace; domain → workspace; inbound_route → domain → workspace).
2. Every repository query that can cross tenants filters by the tenant column; every handler test asserts cross-tenant 404/empty.
3. Mail-domain features (direct delivery, auto-publish, return-path, forwarding rules) require a **verified** domain (existing TXT-ownership flow) — fail-closed otherwise.
4. Event records are write-once, tenant-stamped, and never mutated (append-only ledgers).
5. Deletion is cascade-designed and retention-owned: every family has a retention policy owner (the unified retention cron, ADR-008 §5) and every hard-delete path is tenant-scoped.

## 2. Object ownership table (new objects, Phases 1–6)

| Object | Table | Tenant via | Parent | Events written to | Retention owner |
|---|---|---|---|---|---|
| Delivery attempt | `mta_delivery_attempts` | workspace via email | email | `email_events` (attempt outcomes) | retention cron (family: transport) |
| Inbound route | `inbound_routes` | workspace via domain | domain | `events` (rule changes, audited) | owner-deleted (no auto-retention) |
| Quarantined message | `inbound_quarantine` | workspace via domain | domain | `events` (quarantined/released) | retention cron (family: quarantine) |
| Mailbox | `mailboxes` | workspace via user | user | `mail_events` (lifecycle) | owner/admin delete |
| Folder | `mail_folders` | via mailbox | mailbox | `mail_events` (create/rename/delete) | owner delete (cascade) |
| Message | `mail_messages` | via mailbox | folder | `mail_events` (deliver/flag/expunge) | retention cron (family: mailbox) |
| Blob ref | `blob_refs` | via content owner | referencing object | — (GC is silent by design; GC runs logged) | GC sweep (GAP-45) |
| DNS provider credential | `dns_provider_credentials` | workspace via domain | domain | `events` (audited, masked) | owner-deleted |
| Warm-up schedule | `ip_warmup_schedules` | workspace via domain | domain | `events` (changes) | owner-deleted |
| App password | `user_app_passwords` | user → workspace | user | `events` (created/used/revoked) | owner-deleted |
| TLS-RPT report | `tls_rpt_reports` | workspace via domain | domain | `events` (received) | retention cron |
| Settings history | `events` (existing substrate) | workspace | — | is the audit record | retention cron (existing) |

Existing families (emails, email_events, subscribers, campaigns, automations, security_bans/events, inbound_email, dmarc_reports, webhook deliveries/attempts, api_keys/usage) keep their current ownership — unchanged by Option B except where an ADR extends a family (ADR-002 receiver, ADR-006 taxonomy).

## 3. Event substrate map

| Ledger | Lifecycles | Dedup key | Write-once timestamps |
|---|---|---|---|
| `email_events` | transactional/campaign delivery truth (sent, delivered, bounced, complained, deferred, opened, clicked + DSN/VERP-derived) | (email_uuid, type, provider_event_id) | per-event-type |
| `mail_events` | mailbox/folder/message lifecycle (deliver, flag, move, expunge, quota) | (mailbox_id, message_uid, type, seq) | per-event-type |
| `events` (shared audit) | admin actions, auth, security rules, impersonation, filter dispositions, DNS/provider changes | type+actor+target fingerprint (existing) | created_at |
| `security_events` | abuse/detection signals | existing | existing |
| `mta_delivery_attempts` | per-MX-attempt transport record | (email_id, attempt) | started/finished |

Rule (gap-analysis §7): one **ingestion pattern** (the `emailhistory.Recorder` discipline: fail-open, write-once timestamps, known-event-types gate, dedup) applied to every ledger above — new ledgers copy the pattern, they do not invent one.

## 4. Cross-store joins

- Bounce truth: outbound message → VERP-encoded return-path (`bounce+<uuid>@<rpath domain>`) → inbound receiver (ADR-002) → DSN parse → `email_events` row matched on the encoded UUID. No free-text matching.
- Webmail compose → outbound pipeline: mailstore message refs the created `email`/`email_events` records by UUID for delivery status display (one direction; no backfill into mailstore).
- Reputation aggregation: `mta_delivery_attempts` + `email_events` aggregated per (ip, domain) into the R-07 health surface — read-only rollups, never written back into event ledgers.

## 5. Migration checklist (per new table)

`workspace_id` present + indexed · repository filters tenant in every read/write · handler tests assert cross-tenant isolation · events written through the Recorder pattern · retention policy declared in the unified sweep config · GDPR export path extended where user-identifying data is introduced (mailboxes/messages: export included) · delete path cascade-complete.
