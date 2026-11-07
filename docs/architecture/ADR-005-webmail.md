# ADR-005 — Webmail & Mailbox Administration

- **Status:** Accepted (2026-11-07 — Option B programme authorization)
- **Phase:** Option B Phase 5 · **Covers (parity register):** SF-84 (JMAP revisit deferred), CA-01 (compose UI), plus mailbox administration (SF-65/72/80 surfaces)

## Context

Phases 3–4 deliver the mailstore and protocol access. Phase 5 is the user-facing surface: a webmail client and mailbox administration, per the decision record ("mailbox provisioning & lifecycle; inbox/folders/compose/reply/forwarding/attachments; search, read/unread, mailbox settings; password changes, sessions, device management; mailbox quotas & usage reporting; admin suspension/recovery tools").

## Decision

1. **Webmail is a new area of the existing Vue 3 dashboard** — routes under `/mail` in the same SPA, same auth (JWT + sessions + 2FA + passkeys), same design system. No new frontend framework, no embedded third-party webmail. The mailstore is exposed to it over **REST endpoints** (`/api/v1/mail/...`) — the dashboard already talks only through the REST API (AGENTS.md §7), and JMAP is deferred (OB-6 revisit).
2. **Feature set at Phase 5**: folder tree + message list (pagination, unread/flagged filters, search via ADR-003 FTS), message view (MIME rendering with attachment download, sandboxed HTML display reusing the existing Logs content-iframe pattern), compose/reply/forward with attachments (submission through the outbound pipeline: ADR-001 for delivery, ADR-002 forwarding rules honored), read/unread/flag/draft handling (drafts stored in a Drafts folder via APPEND-equivalent mailstore calls), mailbox settings (signature, display name), quota/usage display (ADR-003 §5).
3. **Mailbox administration (admin + self-service)**: admin can provision/suspend/recover mailboxes and adjust quotas; users manage their own password (existing flows), sessions/devices (existing), app passwords (ADR-004), and mailbox settings. All admin actions audited (SF-74 substrate).
4. **Compose parity with CA-01**: transactional send continues through the existing HTTP API; the webmail composer is for mailbox mail. A campaign/transactional compose surface is explicitly **not** part of this ADR (product decision remains open; the capability-audit CA-01 record tracks it).
5. **Rendering security**: remote-content blocking by default (no automatic external image loads), HTML sanitized for display (the existing tracking/sanitization pipeline), attachments scanned per ADR-006 before download-offer.

## Alternatives considered and rejected

- **Embedding an existing open-source webmail** (Roundcube/SnappyMail): rejected — PHP stack, separate auth universe, styling/branding drift, and a second security surface to audit; the dashboard already has the design system, session model, and API conventions.
- **JMAP-first client**: rejected — JMAP is a Phase 6 revisit (RF-B126); REST matches the existing architecture.
- **Standalone SPA**: rejected — splits auth and doubles maintenance for no user benefit.

## Consequences

- New REST surface `internal/handlers/mail_handler.go` (+ routes, workspace/user-scoped) over the mailstore service; new Vue area `web/src/views/mail/` (Mailbox, MessageView, Compose, MailboxSettings); mailstore gains a small set of session-level helpers (drafts, search ranking).
- Tests: REST handler battery (fake-driver harness, ownership isolation), vitest specs for list/view/compose flows, attachment-download authz; visual acceptance screenshots are a Phase 7 gate for this surface.
- Non-negotiables hold: compose attachments are scanned (ADR-006) before send; sending honors suppression/eligibility rules for campaign-type flows — personal mail is out of that scope by definition and is still DKIM-signed and policy-checked (ADR-001).
