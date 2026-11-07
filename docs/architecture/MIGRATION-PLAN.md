# Migration Plan — Option B

**Status:** Accepted (2026-11-07) · Discipline: **additive migrations only** (`internal/storage/migration` + `upgrade`), AutoMigrate-registered per existing convention; deployed instances upgrade in place; no drops or rewrites (AGENTS.md §7).

## 1. Migration sequence by phase

### Phase 1 — Outbound MTA
| Migration | Kind | Notes |
|---|---|---|
| `mta_delivery_attempts` (email_id, attempt, mx_host, ip, response_code, response_text, outcome, started_at, finished_at + idx email_id/attempt) | new table | transport ledger; tenant via email join |
| `domains.delivery_mode` (`relay` default), `domains.outbound_bind_ip`, `domains.max_msgs_per_connection`, `domains.hourly_budget` | additive columns | direct mode off by default |
| `settings` seeds: submission listener gate/ports, per-domain policy defaults, PTR-guidance display toggle | seeder rows | live-apply |

### Phase 2 — Inbound routing
| Migration | Kind | Notes |
|---|---|---|
| `inbound_routes` (domain_id, pattern, action, target, priority, enabled, confirmed_at + idx domain_id) | new table | forward targets carry confirmation tokens |
| `inbound_quarantine` (domain_id, message ref, reason, score, status, received_at + idx) | new table | retention-family: quarantine |
| `domains.return_path_subdomain` (+ DNS record display), VERP secret column | additive | receiver RCPT allowlist reads it |
| `email_events` extension: DSN-derived types in the known-event-types gate | code + seed | no schema change (existing JSONB) |

### Phase 3 — Mailstore (+ parallel relay-track)
| Migration | Kind | Notes |
|---|---|---|
| `mailboxes`, `mail_folders` (uid_validity/uid_next), `mail_messages` (flags, tsvector + GIN, folder+uid unique) | new tables | tenant via user |
| `blob_refs` (content hash, refcount, owner) | new table | GAP-45 dedup/GC |
| `mailboxes` quotas columns; workspace aggregate ceiling setting | additive | ADR-008 §4 |
| `api_key_usage_daily` retention policy row; `security_events` retention wiring | settings | GAP-13 |
| provider-event/DSN raw-payload retention rows | settings | unified sweep |

### Phase 4 — IMAP/POP3
| Migration | Kind | Notes |
|---|---|---|
| `user_app_passwords` (user_id, name, bcrypt hash, scopes[mail], last_used, revoked) | new table | Profile UI + audit events |
| listener config seeds (ports/TLS/limits, default-off) | settings | ADR-009 gates |

### Phase 5 — Webmail
| Migration | Kind | Notes |
|---|---|---|
| mailbox settings (signature, display name) on `mailboxes` | additive columns | no new table |

### Phase 6 — Convergence
| Migration | Kind | Notes |
|---|---|---|
| `dns_provider_credentials` (domain_id, provider, encrypted token, scopes) | new table | masked reads |
| `ip_pools` + `ip_pool_members` + `ip_warmup_schedules` | new tables | ADR-001 §7 extension |
| `tls_rpt_reports`, `mta_sts_policies` (per-domain) | new tables | ingest per ADR-002 pattern |
| roles/permissions registry + tenant objects | **gated**: only after the ADR-008 Phase 6 revisit approves | migration plan written then |

## 2. Compatibility guarantees

- Additive-only: no column drops, no type rewrites, no data migrations that lock hot tables (backfills run as bounded upgrade steps with progress logging, the `upgrade` framework's pattern).
- Existing data: `domains.delivery_mode` defaults to `relay` so upgrade = zero behavior change; inbound routing defaults to today's archive behavior when no rules exist; mailstore is empty on upgrade; listener gates default off.
- Restart persistence: every new table's contents survive restart (AutoMigrate idempotence verified by the existing dev-boot path + the migration smoke test `MIGRATION_SMOKE_DSN` on a clean DB and against an upgraded DB).
- Rollback: schema-level rollback is not supported (additive), but feature-level rollback = config gates off (listeners, direct mode, routing actions) — documented per phase in the runbooks.

## 3. Verification per phase

Clean-DB migration run (`MIGRATION_SMOKE_DSN` against an empty postgres:17), upgrade-from-previous-tag run, restart-persistence check (compose down/up with data), and the DB-tier integration battery — all recorded in `docs/audits/FINAL_VERIFICATION_REPORT.md`.
