# ADR-003 — Mailbox & Storage Foundation

- **Status:** Accepted (2026-11-07 — Option B programme authorization)
- **Phase:** Option B Phase 3 · **Covers (parity register):** SF-14 (storage side), SF-18, SF-51 (store side), SF-75 (plane completion), SF-78 (blob encryption), SF-79 (search depth), SF-80 (quotas), SF-83 (compression), SF-112 (files/attachments)

## Context

Option B Phase 3 introduces **mailboxes** — a persistent, per-user message store that is explicitly *not* the transactional `Email` model (`todo.md` Track A Phase 3: "explicitly not the transactional Email model"). Mailboxes back IMAP/POP3 (Phase 4, ADR-004) and webmail (Phase 5, ADR-005), and give inbound mail (ADR-002) a durable per-user home alongside the existing workspace-scoped inbound archive. ReForwarder's infrastructure today: Postgres 17 (data), Redis 7 (lookup), `blob.Store` (FS + S3, `internal/storage/blob/`).

## Decision

1. **Mailstore is a Postgres-backed service** (`internal/services/mailstore`) with three new core tables (all additive): `mailboxes` (owner user, name, quota bytes, created), `mail_folders` (mailbox, parent, name/IMAP path, uid_validity, uid_next, subscribed), `mail_messages` (folder, uid, message_id, subject, from/to/cc, date, size, flags bitmask, seen/answered/flagged/draft/deleted/recent, snippet, blob refs). Body and attachments are **content-addressed blobs in `blob.Store`** (SHA-256 refs, MIME-parsed at ingest) — Postgres holds metadata only.
2. **Blob dedup + ref-count + orphan GC** (SF-77 residual, GAP-45): a `blob_refs` table maps content hashes to referencing objects; a periodic GC sweep deletes unreferenced blobs. This closes the storage half of GAP-45 (usage reporting lands with it: per-mailbox blob bytes counted from `blob_refs`).
3. **Search (SF-79)**: Postgres **full-text search** (`tsvector` column + GIN index over subject/from/to/body-text) — in-Postgres, no new infrastructure. ILIKE remains for control-plane registers; external engines (Elasticsearch/Meilisearch) are explicitly not adopted (ADR-009 topology: no new stateful services).
4. **At-rest encryption (SF-78)**: blob payloads are encrypted at rest with the existing `internal/services/crypto` AES-256-GCM service (per-workspace data keys), extending the credential-encryption pattern to stored content. Zstd compression (SF-83) is applied before encryption for bodies > 4 KiB (dictionary training not adopted — single standard dictionary, documented).
5. **Quotas (SF-80)**: per-mailbox byte quota enforced at append/ingest (reject over-quota with a typed error), per-mailbox message-count quota, workspace-level aggregate ceiling; usage surfaced in the dashboard.
6. **Concurrency & durability**: IMAP-correct UID semantics live in the mailstore service (monotonic per-folder UID assignment under transactional claim, `uid_validity` bump on folder rebuild); concurrent flag/store operations use row-level CAS like the R-26 claims pattern.
7. **Retention (SF-81)**: per-folder and per-mailbox retention policies (age/size/count) executed by a cron sweep — the same sweep gains the GAP-13 `security_events` retention caller so one job owns all retention.
8. **Files/attachments for webmail (SF-112)**: outbound compose attachments and inbound attachment extraction reuse the same content-addressed blob store; document-style file storage beyond mail is out of scope until Phase 6 (JMAP FileNode evaluation, RF-B126).

## Alternatives considered and rejected

- **Adopting an existing Go mailstore** (e.g. embedding a third-party IMAP-backed store): rejected — none meet the license/dependency bar; the mailstore's actual surface (folders/UIDs/flags/search) is small compared to the integration cost.
- **Storing full raw messages in Postgres TOAST**: rejected — body blobs belong in `blob.Store` (already S3-capable, already encrypted), keeping Postgres rows small and IMAP FETCH ranges cheap.
- **External search engine**: rejected per ADR-009 (no new stateful services); Postgres FTS covers mailstore-scale search; revisit only under a convergence ADR.

## Consequences

- Additive migrations: `mailboxes`, `mail_folders`, `mail_messages`, `blob_refs` (+ indexes: folder+uid unique, tsvector GIN, message-id).
- New tests: UID monotonicity under concurrency; flag CAS; quota rejection; dedup ref-count + GC correctness (no live blob ever collected); encryption round-trip; retention sweep; FTS relevance basics.
- The transactional `Email` model, `email_events`, logs, and campaigns are untouched — mailstore is additive; the two stores meet only at ADR-002's forwarding (system messages) and webmail compose (ADR-005).
