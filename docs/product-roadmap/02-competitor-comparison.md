# 02 — Competitor Comparison: 12 OSS Email Platforms

- **Purpose:** cross-project comparison of the 12 audited OSS email platforms across every capability domain (feature matrices, infrastructure, licensing, architecture, strengths/limitations, best use cases) and a relevance verdict for ReForwarder.
- **Research date:** 2026-11-05. All verdicts are transcribed from `registry.md` (Canonical Feature-ID Registry v1.0.0, frozen 2026-11-05) and the 12 evidence dossiers (`dossier-01-listmonk.md` … `dossier-12-hyvorpost.md`, in `C:\Users\Ibrahim\.zcode\tmp\oss-audit-2026-11\`). **No verdict was re-researched or changed for this document.**
- **ID scheme:** feature IDs (`INF-001`, `CAM-008`, …) are permanent registry IDs. Per-project detail lives in **`01-oss-feature-inventory.md`** — this document compares; it does not re-profile.
- **Companion docs:** ReForwarder-side evidence: `03-existing-feature-audit.md` (working-tree audit at HEAD `a6f1681`), `04-feature-gap-analysis.md` (gap derivation).

## 0. Project codes and status-flag legend

| Code | Project | Code | Project |
|---|---|---|---|
| LM | Listmonk | KE | Keila |
| BM | BillionMail | MC | MailCheap |
| PO | Postal | SE | Sessy |
| US | useSend | HR | Hyvor Relay |
| NU | Notifuse | WR | Wraps |
| SP | SendPortal | HP | Hyvor Post |

Status flags (per registry): **✓** implemented · **◐** partial · **▲** planned/unreleased · **E** enterprise/paid/licence-gated · **H** hosted-only · **✗** verified absent/non-feature. A **·** (blank-equivalent) means the registry has no verified entry for that project — unverified items are *omitted rather than guessed*, so · never implies absence. Rows marked "RF-specific" have no competitor evidence in the registry (feature exists only in ReForwarder, or was checked and found nowhere).

---

## 1. Master comparison matrices (categories A–J)

Rows = registry entries of each category (short names + IDs). Columns = the 12 projects. Cell values copied verbatim from `registry.md`.

### Category A — Email infrastructure (INF-001…036)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| INF-001 | External SMTP sending | ✓ | ✓ | · | · | ✓ | ✓ | ✓ | · | · | · | · | · |
| INF-002 | Built-in SMTP server (submission) | · | ✓ | ✓ | ◐ | ✓ | · | · | · | · | ✓ | · | · |
| INF-003 | Full mail server (mailboxes, IMAP/POP, webmail) | · | ✓ | · | · | · | · | · | · | · | · | · | · |
| INF-004 | Transactional send API | ✓ | ✓ | ✓ | ✓ | ✓ | · | ✓ | ✓ | · | ✓ | ✓ | · |
| INF-005 | Batch send API (per-recipient variables) | ✓ | · | · | ✓ | · | · | · | · | · | · | ✓ | · |
| INF-006 | Campaign/newsletter bulk sending | ✓ | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | ◐ | ✓ | ✓ |
| INF-007 | Outbound attachments | ◐ | · | ◐ | ✓ | ✓ | · | · | · | · | ✓ | · | · |
| INF-008 | Custom headers control | ✓ | · | · | ✓ | · | · | · | ✓ | · | ✓ | ◐ | · |
| INF-009 | Background queue pipeline | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| INF-010 | Queue administration & dead-letter management | · | ✓ | ◐ | · | · | · | · | · | · | · | ◐ | · |
| INF-011 | Priority/weighted queues (tx vs bulk) | ✗ | ✗ | ✗ | · | · | · | · | · | · | ✓ | · | · |
| INF-012 | Send rate limiting & quotas | ✓ | ✗ | ✓ | ◐ | ✓ | · | ✓ | ✓ | · | ✓ | ✓ | ✓ |
| INF-013 | Retry policies | ✓ | · | ✓ | · | ✓ | · | · | ✓ | · | ✓ | · | · |
| INF-014 | Scheduled (future) sends | ✓ | ✓ | ◐ | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | ✓ |
| INF-015 | Gradual/throttled campaign sending | ◐ | ✗ | · | ✓ | ✓ | ✗ | ✓ | ✓ | · | · | · | ✓ |
| INF-016 | Circuit breaker / auto-pause on failure | · | · | · | · | ✓ | · | · | ✓ | · | · | · | · |
| INF-017 | Message & attachment retention/purge | · | ✓ | ✓ | ✓ | ◐ | · | ✓ | ◐ | ✓ | ✓ | ✓ | ✓ |
| INF-018 | Suppression lists (send-time enforcement) | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | · | ✓ | ✓ | ✓ |
| INF-019 | Bounce ingestion & auto-suppression | ✓ | ✓ | ✓ | ✓ | ✓ | · | ◐ | ✓ | ✓ | ✓ | ✓ | ✓ |
| INF-020 | Bounce mailbox / VERP ingestion | ✓ | · | · | · | · | · | · | · | · | · | · | · |
| INF-021 | Complaint/FBL ingestion | ◐ | · | · | ✓ | ✓ | ◐ | ◐ | ✓ | ✓ | ✓ | ✓ | ✓ |
| INF-022 | Multiple ESP integrations (native APIs) | · | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | · |
| INF-023 | ESP/provider failover | ◐ | · | · | · | · | ◐ | · | · | · | · | · | · |
| INF-024 | Per-message/per-campaign server selection | ✓ | · | · | · | · | · | · | · | · | · | · | · |
| INF-025 | IP pools / multiple sending IPs | · | ◐ | ✓ | · | · | · | · | · | · | ▲ | · | · |
| INF-026 | Development/sandbox hold mode | · | · | ✓ | · | · | · | · | ◐ | · | · | · | · |
| INF-027 | Dry-run send validation | · | · | · | · | · | · | · | · | · | · | · | · |
| INF-028 | Recipient address verification | · | ✓ | · | · | · | · | · | ✓ | · | · | ◐ | · |
| INF-029 | Inbound email receiving & parsing | · | ◐ | ✓ | ▲ | · | · | · | · | · | ▲ | · | · |
| INF-030 | Inbound routing rules | · | ◐ | ✓ | · | · | · | · | · | · | · | · | · |
| INF-031 | Idempotent sends | · | · | · | ✓ | ◐ | · | · | · | · | ✓ | · | · |
| INF-032 | Mail/message tagging | · | ◐ | ✓ | · | · | · | · | · | · | · | · | · |
| INF-033 | SMTP connection diagnostics | · | ◐ | · | · | · | · | · | · | · | · | · | · |
| INF-034 | Greylisting & transient-failure handling | · | · | · | · | · | · | · | · | · | ✓ | · | · |
| INF-035 | Maintenance-mode send gate | · | · | · | · | · | · | · | · | · | · | · | · |
| INF-036 | Multi-channel messengers (SMS/push adapters) | ✓ | · | · | · | · | · | · | · | · | · | · | · |

### Category B — Domains & DNS (DOM-001…013)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| DOM-001 | Custom sending domains (multi-domain CRUD) | ✗ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | · | ✓ | ✓ | ✓ |
| DOM-002 | Domain ownership verification | · | ✓ | ✓ | ✓ | · | · | · | ✓ | · | ✓ | ✓ | · |
| DOM-003 | Managed DKIM signing | · | ✓ | ◐ | ✓ | · | · | · | · | · | ✓ | ✓ | · |
| DOM-004 | DKIM key rotation & DNS publication verification | · | · | · | · | · | · | · | · | · | ◐ | · | · |
| DOM-005 | SPF checking & guidance | · | ✓ | ✓ | ✓ | · | · | · | ✓ | · | · | ✓ | · |
| DOM-006 | DMARC checking & guidance | · | ◐ | · | ✓ | · | · | · | ✓ | · | · | ✓ | · |
| DOM-007 | Automated DNS provisioning | · | · | · | · | · | · | · | ◐ | · | ✓ | · | · |
| DOM-008 | DNS re-verification & drift monitoring | · | · | ✓ | · | · | · | · | ✓ | · | ✓ | · | · |
| DOM-009 | Custom tracking domain | · | · | ✓ | · | ✓ | · | · | · | · | · | · | · |
| DOM-010 | TLS certificate management (ACME/SNI) | · | ✓ | ◐ | · | · | · | · | · | · | · | · | · |
| DOM-011 | Domain reputation/blacklist monitoring | · | ✓ | · | · | · | · | · | ✓ | · | · | ✓ | · |
| DOM-012 | Domain-sending policy gates | · | · | ◐ | · | · | · | · | ✓ | · | ✓ | · | · |
| DOM-013 | rDNS/PTR guidance | · | · | · | · | · | · | · | · | · | ◐ | · | · |

### Category C — Contacts & audience (CON-001…020)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| CON-001 | Subscriber/contact records | ✓ | ✓ | ✗ | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | ✓ |
| CON-002 | Mailing lists | ✓ | ✓ | · | ✓ | ✓ | ✗ | · | ✓ | · | · | ✓ | ✓ |
| CON-003 | Subscription status management | ✓ | · | · | ✓ | ✓ | ◐ | · | ✓ | · | · | ✓ | ✓ |
| CON-004 | Double opt-in | ✓ | ✓ | · | ✓ | ✓ | ✗ | ✓ | ✓ | · | · | ✓ | ✓ |
| CON-005 | Custom attributes/fields | ✓ | ✓ | · | ✓ | ◐ | ✗ | ✓ | ✓ | · | · | ✓ | ◐ |
| CON-006 | Subscriber tags | ✗ | ✓ | · | ✗ | · | ✓ | · | · | · | · | ✗ | · |
| CON-007 | Dynamic segmentation (condition builder) | ✓ | ◐ | · | · | ✓ | ◐ | ✓ | · | · | · | E | · |
| CON-008 | Engagement-based segmentation | · | · | · | · | ✓ | · | ✓ | · | · | · | · | · |
| CON-009 | CSV import (column mapping) | ✓ | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | ✓ |
| CON-010 | CSV export | ◐ | ✓ | · | · | · | ✓ | ✓ | ✓ | · | · | ✓ | ✓ |
| CON-011 | Bulk contact operations | ✓ | · | · | ✓ | ✓ | · | · | · | · | · | · | ✓ |
| CON-012 | Contact upsert/sync API | · | · | · | ✓ | ✓ | · | · | · | · | · | · | ✓ |
| CON-013 | Suppression management (UI/API) | · | · | · | ✓ | · | · | · | ✓ | · | ✓ | ✓ | · |
| CON-014 | Unsubscribe management (RFC 8058, hosted pages) | ✓ | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | ✓ |
| CON-015 | Unsubscribe reasons | · | · | · | ✓ | · | · | · | · | · | · | · | · |
| CON-016 | Preference center | ◐ | · | · | · | ✓ | · | ◐ | · | · | · | ✓ | ◐ |
| CON-017 | Signup forms (hosted/embedded) | ✓ | ✗ | · | ✗ | ✓ | ✗ | ✓ | ✓ | · | · | · | ✓ |
| CON-018 | Form bot protection (captcha/honeypot) | · | · | · | · | · | · | ✓ | ✓ | · | · | · | · |
| CON-019 | Contact activity history | · | ✓ | · | ◐ | ✓ | · | ✓ | · | · | · | · | · |
| CON-020 | Import cleaning & validation | · | ✓ | · | · | · | · | · | ✓ | · | · | ◐ | · |

### Category D — Campaigns (CAM-001…018)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| CAM-001 | Campaign creation & drafts | ✓ | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | ✓ |
| CAM-002 | Campaign scheduling | ✓ | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | ✓ |
| CAM-003 | Recurring campaigns | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| CAM-004 | Campaign duplication | ✓ | ✓ | · | · | · | · | · | · | · | · | ✓ | · |
| CAM-005 | Reusable campaign templates | ✓ | ✓ | · | ◐ | · | ✓ | ✓ | · | · | · | · | ✓ |
| CAM-006 | Template content slots / campaign overrides | · | · | · | · | · | · | ✓ | · | · | · | · | · |
| CAM-007 | Campaign preview & test send | ✓ | · | · | · | ✓ | ✓ | ◐ | ✓ | · | · | · | · |
| CAM-008 | A/B testing | · | · | · | · | ✓ | · | ▲ | · | · | · | · | · |
| CAM-009 | Audience targeting (list/segment) | ✓ | · | · | · | ✓ | ◐ | ✓ | · | · | · | ✓ | ✓ |
| CAM-010 | Pause/resume/cancel lifecycle | ✓ | · | · | ✓ | ✓ | · | · | · | · | · | ✓ | · |
| CAM-011 | Campaign reports | ✓ | ✓ | · | ✓ | ✓ | ◐ | ◐ | ✓ | · | · | ✓ | ✓ |
| CAM-012 | Public newsletter archive | ✓ | · | · | · | · | · | ✓ | ✓ | · | · | · | ✓ |
| CAM-013 | Multiple newsletters/publications | ✓ | · | · | · | · | · | ✓ | ✓ | · | · | · | ✓ |
| CAM-014 | Web view of message | · | · | · | · | · | ✓ | · | · | · | · | · | · |
| CAM-015 | UTM auto-tagging | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| CAM-016 | Timezone-aware sending | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| CAM-017 | Stuck-campaign watchdog recovery | · | · | · | · | · | · | · | · | · | · | · | · |
| CAM-018 | Failed-recipient retry | · | · | · | · | ✓ | · | · | · | · | · | · | · |

### Category E — Templates & editors (TPL-001…022)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| TPL-001 | Raw HTML editor | ✓ | ✓ | · | · | ✓ | ✓ | ✓ | ✓ | · | · | · | ✓ |
| TPL-002 | WYSIWYG / drag-and-drop builder | ✓ | ✓ | · | ✓ | ✓ | ✗ | ✓ | ◐ | · | · | ✗ | ✗ |
| TPL-003 | Markdown authoring | ✓ | · | · | · | · | · | ✓ | ✓ | · | · | · | · |
| TPL-004 | Plain-text part & auto-generation | ✓ | · | · | ✓ | · | · | ✓ | · | · | · | · | · |
| TPL-005 | Template variables / merge fields | ✓ | ✓ | · | ✓ | ✓ | ◐ | ✓ | · | · | · | ✓ | ✓ |
| TPL-006 | Advanced templating engines (Liquid/Handlebars-style) | ◐ | · | · | · | ✓ | · | ✓ | · | · | · | · | · |
| TPL-007 | MJML support | · | · | · | · | ✓ | · | ✓ | · | · | · | · | · |
| TPL-008 | React Email | · | · | · | ✓ | · | · | · | · | · | · | ✓ | · |
| TPL-009 | Template versioning & activation | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| TPL-010 | Template localization (multi-language) | · | · | · | · | E | · | · | · | · | · | · | · |
| TPL-011 | CSS inlining | · | · | · | · | · | ✓ | · | · | · | · | · | ✓ |
| TPL-012 | Reusable blocks/sections library | · | · | · | · | · | · | · | · | · | · | · | · |
| TPL-013 | Brand kit / shared stylesheets | · | · | · | · | · | · | · | · | · | · | ✓ | · |
| TPL-014 | Template preview with sample data | ✓ | · | · | · | ✓ | · | ◐ | · | · | · | · | · |
| TPL-015 | Editor device previews | · | · | · | · | · | · | · | ◐ | · | · | · | · |
| TPL-016 | Preview text (preheader) | · | · | · | ✓ | · | · | ✓ | · | · | · | · | · |
| TPL-017 | Send-test to inbox | ✓ | · | · | · | ✓ | ✓ | · | · | · | · | · | · |
| TPL-018 | Template import/export (JSON bundle) | · | · | · | · | ◐ | · | · | · | · | · | · | · |
| TPL-019 | HTML import (style extraction) | · | · | · | · | · | · | · | · | · | · | · | · |
| TPL-020 | AI-assisted creation | · | ✓ | · | · | ✓ | · | · | ◐ | · | · | E | · |
| TPL-021 | Spintax variation | · | ✓ | · | · | · | · | · | · | · | · | · | · |
| TPL-022 | Media/image uploads & storage | ✓ | · | · | · | · | · | ✓ | ✓ | · | · | · | ◐ |

### Category F — Analytics & tracking (ANA-001…016)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ANA-001 | Open tracking | ✓ | ✓ | ✓ | ✓ | ✓ | ◐ | ✓ | ✓ | ✓ | ✗ | ✓ | ✗ |
| ANA-002 | Click tracking | ✓ | ✓ | ✓ | ✓ | ✓ | ◐ | ✓ | ✓ | ✓ | ✗ | ✓ | ✗ |
| ANA-003 | Delivery status tracking | · | · | ✓ | ✓ | ✓ | · | · | ✓ | ✓ | ◐ | ✓ | · |
| ANA-004 | Bounce categorization (hard/soft) | ◐ | · | ◐ | ✓ | · | · | · | ✓ | ✓ | · | · | · |
| ANA-005 | Complaint tracking | ◐ | · | · | ✓ | ✓ | ◐ | · | ✓ | · | ✓ | ✓ | · |
| ANA-006 | Campaign analytics (rates, links, series) | ✓ | ✓ | · | ✓ | ✓ | ◐ | ◐ | ✓ | · | · | ✓ | ✓ |
| ANA-007 | Historical trend analytics (date ranges) | ✓ | ✓ | · | ✓ | ✓ | · | · | · | ✓ | · | ✓ | · |
| ANA-008 | Per-message event timeline & searchable logs | · | ✓ | ✓ | · | ✓ | · | · | ✓ | ✓ | ✓ | ✓ | · |
| ANA-009 | Provider/inbox breakdown analytics | · | · | · | · | · | · | · | ✓ | · | · | · | · |
| ANA-010 | Bounce-rate alerting | ◐ | · | · | · | · | · | · | ✓ | · | · | · | · |
| ANA-011 | Exportable reports | ◐ | · | · | · | ◐ | · | · | · | ✓ | · | · | · |
| ANA-012 | Tracking privacy controls (opt-out scoping) | ✓ | · | ◐ | ◐ | · | ◐ | ✓ | · | · | · | · | · |
| ANA-013 | SMTP response/conversation inspection | · | · | ◐ | · | · | · | · | · | · | ✓ | · | · |
| ANA-014 | Dashboard KPIs | · | ✓ | · | · | · | · | · | ✓ | · | · | · | · |
| ANA-015 | Device/geo breakdowns | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| ANA-016 | Web analytics (site traffic) | · | · | · | · | ✓ | · | · | · | · | · | · | · |

### Category G — Automation (AUT-001…018)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| AUT-001 | Visual workflow builder | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-002 | Event triggers | · | · | · | · | ✓ | · | · | · | · | · | ✓ | · |
| AUT-003 | Custom event ingest API | · | · | · | · | ✓ | · | · | · | · | · | ✓ | · |
| AUT-004 | Email action node | · | · | · | · | ✓ | · | · | · | · | · | ✓ | · |
| AUT-005 | Delay/wait steps | · | · | · | · | ✓ | · | · | · | · | · | ✓ | · |
| AUT-006 | Conditional branching | · | · | · | · | ✓ | · | · | · | · | · | ✓ | · |
| AUT-007 | List-management action nodes | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-008 | A/B split node | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-009 | Webhook action node (SSRF-guarded) | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-010 | Durable journey engine | · | · | · | · | ✓ | · | · | · | · | · | ✓ | · |
| AUT-011 | Re-enrollment policy (once/every-time) | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-012 | Execution stats & timeline observability | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-013 | Lifecycle (activate/pause/duplicate/delete-safe) | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-014 | Welcome/onboarding sequences | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-015 | Re-engagement / scheduled enrollment | · | · | · | · | · | · | · | · | · | · | ✓ | · |
| AUT-016 | Stop-on-reply | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| AUT-017 | Automated suppression on bounce/complaint | ✓ | ✓ | ✓ | ✓ | ✓ | · | ◐ | ✓ | · | ✓ | ✓ | · |
| AUT-018 | Workflow testing / dry-run | · | · | · | · | · | · | · | · | · | · | ◐ | · |

### Category H — APIs & integrations (API-001…019)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| API-001 | REST API (full product coverage) | ✓ | · | ◐ | ✓ | ✓ | ✓ | ✓ | ✓ | ◐ | ✓ | ✓ | ✓ |
| API-002 | OpenAPI/Swagger docs | ✓ | · | · | ✓ | ✓ | · | ✓ | ✓ | · | · | ✓ | ✓ |
| API-003 | API keys (hashed, workspace-bound) | ✓ | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | ✓ | ✓ | ✓ |
| API-004 | Scoped API keys / permissions | ✓ | · | · | ◐ | ✓ | · | · | ◐ | · | ✓ | · | ✓ |
| API-005 | Per-key IP/CIDR allowlists | · | ✓ | · | · | · | · | · | · | · | · | · | · |
| API-006 | API key expiry & rotation | · | · | · | · | · | · | · | · | · | · | · | ✓ |
| API-007 | API rate limiting | ◐ | · | · | ◐ | · | ✓ | ✓ | ✓ | · | ✓ | ✓ | ✓ |
| API-008 | Official SDKs | · | · | ✓ | ✓ | · | · | · | · | · | · | ✓ | · |
| API-009 | Outbound webhooks (event push) | ✗ | ✗ | ✓ | ✓ | ✓ | ✗ | ✗ | · | · | ✓ | ✓ | · |
| API-010 | Webhook signatures & replay protection | · | · | · | ✓ | ✓ | · | · | · | · | ✓ | · | · |
| API-011 | Webhook retries with backoff | · | · | · | ✓ | ✓ | · | · | · | · | ✓ | · | · |
| API-012 | Webhook delivery history & inspection | · | · | · | ◐ | ✓ | · | · | · | · | ✓ | · | · |
| API-013 | Webhook test-fire / replay | · | · | · | · | ✓ | · | · | · | · | · | · | · |
| API-014 | Inbound ESP event webhooks (adapters) | ✓ | ✗ | · | ✓ | ✓ | ✓ | ◐ | ✓ | ✓ | · | ✓ | ◐ |
| API-015 | Native ESP sending integrations | · | ✓ | · | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | ✓ | · |
| API-016 | MCP server | ◐ | · | · | · | · | · | · | ✓ | ✓ | · | ✓ | · |
| API-017 | CLI client | · | ✓ | ✓ | · | · | · | · | · | · | · | ✓ | · |
| API-018 | Zapier/platform integrations | · | · | · | · | ✓ | · | ◐ | · | · | · | · | · |
| API-019 | Embeddable core package | · | · | · | · | · | ✓ | · | · | · | · | · | · |

### Category I — Security & administration (SEC-001…018)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| SEC-001 | Multi-tenancy (workspaces) | ✗ | · | ✓ | ✓ | E | ✓ | ◐ | ✓ | ◐ | ✓ | ◐ | ✓ |
| SEC-002 | RBAC | ✓ | · | ◐ | ◐ | E | ◐ | ✓ | ◐ | · | · | · | · |
| SEC-003 | Team management & invitations | ◐ | · | ◐ | ✓ | ✓ | ✓ | · | ✓ | · | · | · | ✓ |
| SEC-004 | Audit logs | ✗ | ◐ | · | ✗ | E | · | · | · | · | · | ◐ | · |
| SEC-005 | Two-factor auth (TOTP) | ✗ | · | · | · | · | · | · | · | · | · | · | · |
| SEC-006 | SSO/OIDC (+ workspace SSO enforcement) | ✓ | · | ✓ | ◐ | E | · | · | · | · | · | · | · |
| SEC-007 | Session management & revocation | · | · | · | · | · | · | ◐ | · | · | · | · | · |
| SEC-008 | Configurable retention framework | · | · | ✓ | · | ✓ | · | · | · | ✓ | · | ✓ | · |
| SEC-009 | GDPR deletion & export | ✓ | · | ◐ | ◐ | ◐ | · | · | · | · | · | · | · |
| SEC-010 | Inbound spam scanning & filtering | · | ✓ | ✓ | · | · | · | · | · | · | · | · | · |
| SEC-011 | Antivirus integration | · | · | ✓ | · | · | · | · | · | · | · | · | · |
| SEC-012 | Ban engine / brute-force protection | · | ✓ | · | · | · | · | · | · | · | · | · | · |
| SEC-013 | Admin-console IP allowlisting | · | ◐ | · | · | · | · | · | · | · | · | · | · |
| SEC-014 | Plans & quota enforcement | · | · | · | · | · | · | ✓ | · | · | · | · | · |
| SEC-015 | Admin impersonation | · | · | · | · | · | · | ◐ | · | · | · | · | ✓ |
| SEC-016 | Credential encryption at rest | · | · | · | · | · | ✓ | · | ✓ | · | · | · | · |
| SEC-017 | Registration control / invite-only mode | · | · | · | · | · | ✓ | ✓ | ✓ | · | · | · | · |
| SEC-018 | SSRF hardening on outbound HTTP | · | · | · | · | ✓ | · | · | · | · | · | · | · |

### Category J — Infrastructure & observability (OBS-001…019)

| ID | Feature | LM | BM | PO | US | NU | SP | KE | MC | SE | HR | WR | HP |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| OBS-001 | Horizontal scaling (server/worker split) | · | · | ✓ | · | ◐ | ◐ | · | · | · | ✓ | · | · |
| OBS-002 | Multiple worker processes | · | · | ✓ | · | · | · | · | · | · | · | · | · |
| OBS-003 | Multiple sending servers / shared upstream pool | · | ◐ | · | · | · | · | · | · | · | · | · | · |
| OBS-004 | Redis-backed queues/cache | · | · | · | ✓ | · | ◐ | · | ✓ | · | ✓ | · | · |
| OBS-005 | PostgreSQL system of record | ✓ | ✓ | ✗ | ✓ | ✓ | ◐ | ✓ | ✗ | ◐ | ✓ | ✓ | ✓ |
| OBS-006 | Prometheus metrics | · | · | ✓ | · | · | · | · | · | · | ✓ | · | · |
| OBS-007 | Grafana dashboards | · | · | ✗ | · | · | · | · | · | · | ✓ | · | · |
| OBS-008 | Health check endpoints | · | · | ✓ | · | · | · | · | ✓ | ✓ | ◐ | · | · |
| OBS-009 | Queue metrics & latency observability | · | · | ✓ | · | · | ✓ | ✓ | · | ✓ | · | · | · |
| OBS-010 | Runtime log viewer | ◐ | ◐ | · | · | · | · | · | · | · | · | · | · |
| OBS-011 | Error monitoring (Sentry) | · | · | ◐ | · | ◐ | · | · | · | · | · | · | ◐ |
| OBS-012 | Runtime resource monitoring | · | · | · | · | · | · | · | · | · | · | · | · |
| OBS-013 | Blob storage abstraction (FS/S3) | ✓ | · | · | ✓ | ✓ | · | · | · | · | · | · | · |
| OBS-014 | Cron job visibility | · | · | · | · | · | ◐ | · | ◐ | · | · | · | · |
| OBS-015 | Worker heartbeats & version detection | · | · | · | · | · | · | · | · | · | · | · | · |
| OBS-016 | Deployment tooling (Docker/compose/CI) | ✓ | ✓ | ✓ | ✓ | ✓ | · | · | · | ✓ | ✓ | · | · |
| OBS-017 | Backup/restore tooling | · | · | · | · | · | · | · | · | · | · | · | · |
| OBS-018 | Update check & version banner | · | ✓ | · | · | · | · | ✓ | · | ✓ | · | · | · |
| OBS-019 | Multi-replica cron leader election | · | · | · | · | · | · | · | · | · | · | · | · |

RF-only rows (no competitor evidence in the registry): INF-027 (dry-run validation), INF-035 (maintenance gate), CAM-017 (watchdog), TPL-012 (reusable blocks — absent everywhere), TPL-019 (HTML import), OBS-012, OBS-015, OBS-017, OBS-019. CAM-003 (recurring campaigns) is verified absent in **all 12** projects (registry "verified absent" list).

---

## 2. Domain comparison

### 2.1 Email infrastructure & SMTP

Three projects operate their own mail-transfer machinery: **BM** (Postfix + Dovecot + Rspamd + Roundcube — the only full mail server with mailboxes/webmail), **PO** (native Rails SMTP server on port 25, IP pools, inbound MX with routing rules and a held queue), and **HR** (Go worker binary containing an SMTP submission server *and* a built-in DNS server with enforced DKIM). Everyone else is an SMTP *client* or delegates entirely: US ships an optional SMTP proxy that converts SMTP to API calls; NU runs an inbound SMTP bridge (587/465) fronting its API; SP warns that SMTP sends cannot be tracked at all. SE sends nothing. The registry shows built-in SMTP submission (INF-002) in only 4 projects plus US◐ — and no competitor implements RF's maintenance-mode send gate (INF-035) or dry-run validation (INF-027).

| Project | Delivery path | Own SMTP server | Inbound email | Notable infrastructure (registry IDs) |
|---|---|---|---|---|
| LM | External SMTP / HTTP messengers | ✗ (client only) | ✗ (POP3 bounces only, INF-020) | Per-campaign SMTP selection (INF-024), per-domain routing, VERP/mailbox bounce ingestion |
| BM | Native Postfix MTA (+ optional SES/Mailgun relay-out) | ✓ | ◐ (hosted-domain receiving, BCC/forward rules) | Full mail server (INF-003), visual Postfix queue manager (INF-010), IP binding + warmup (INF-025◐) |
| PO | Native SMTP server + HTTP API | ✓ (port 25) | ✓ (MX + wildcard routes, INF-029/030) | IP pools (INF-025✓), held queue, rolling send limits (INF-012), dev hold mode (INF-026) |
| US | AWS SES only (SMTP proxy → API) | ◐ (proxy app) | ▲ planned | Idempotency keys (INF-031✓), batch API (INF-005), per-campaign pacing (INF-015) |
| NU | 7 ESP integrations + SMTP bridge | ✓ (bridge) | ◐ (reply detection for automations) | Circuit breaker (INF-016✓), retry ladder, per-integration concurrency |
| SP | 6 ESP APIs + SMTP | ✗ | ✗ | Queue workers + Horizon; no transactional API (INF-004·) |
| KE | SMTP / SES / Sendgrid / Mailgun / Postmark adapters | ✗ | ✗ | Per-sender rate limiting (INF-012✓), Oban PG-backed queues |
| MC | SES tenants + PostShiba SMTP | ✗ | ✗ | Gradual warm-up pacing (INF-015✓), auto-pause thresholds (INF-016✓) |
| SE | Observes SES only | ✗ | ✗ | No send path at all (product scope) |
| HR | Native Go SMTP + API, own DNS | ✓ | ▲ planned | Tx/bulk queue isolation (INF-011✓), greylisting/retries (INF-034✓), idempotency (INF-031✓) |
| WR | AWS SES direct (IaC-deployed) | ✗ | ✓ (SES receipt rules → S3 inbox) | SQS + DLQ in customer account, DynamoDB TTL retention |
| HP | 100% delegated to Hyvor Relay API | ✗ | ◐ (via Relay) | MAX_EMAILS_PER_SECOND throttle; retention via Relay (30-day logs) |

### 2.2 Transactional APIs & developer experience

Nine projects expose a transactional send API (INF-004), but depth differs sharply. **US** is the DX reference: `Idempotency-Key` header with 24h window and 409 semantics (INF-031), official TS/Python/Go SDKs (API-008), OpenAPI spec, natural-language scheduling. **HR** matches on idempotency + scoped keys + rate-limit headers. **PO** has official client libraries and a `postal` CLI but its HTTP API is v1 send/read only — management API v2 is "planned, no ETA". **MC**, **SE** and **WR** are MCP-first (API-016): every dashboard action is an MCP tool (MC parity-tests REST/MCP/OpenAPI against each other in CI). LM's MCP is community-built (API-016 ◐). BM and WR ship management CLIs (API-017); LM exposes a documented direct-DB integration pattern instead.

| Capability | Leaders | Laggards (registry verdicts) |
|---|---|---|
| Idempotent sends (INF-031) | US✓ HR✓ NU◐ | No other project ships client idempotency keys |
| Official SDKs (API-008) | PO✓ (Ruby/PHP/Node official + community) US✓ (TS/Py/Go) WR✓ (TS/Py) | LM/KE/SP/HP: none official (community only or absent) |
| CLI (API-017) | PO✓ BM✓ WR✓ | Absent elsewhere (LM has install flags only) |
| MCP server (API-016) | MC✓ SE✓ WR✓ LM◐ (community) | Absent in 8 projects |
| Per-key hardening (API-005/006) | BM (IP/CIDR allowlists), HP (expiry + 7-day notification cron) | No other project documents either |
| Scoped keys (API-004) | LM✓ NU✓ HR✓ HP✓ + US◐ MC◐ | PO/KE/SP: keys are instance/workspace-wide |

### 2.3 Marketing & campaigns

Campaign basics (drafts, scheduling, reports — CAM-001/002/011) are near-universal across the nine marketing-capable projects; Postal and Sessy have no campaign concept at all. Differentiation sits in the advanced tier: **NU** is the only project with shipped A/B testing including winner selection (CAM-008 ✓; KE lists A/B on its roadmap — ▲ — despite marketing copy). Public newsletter archives (CAM-012) exist in LM, KE, MC, HP; multiple newsletters/publications (CAM-013) in the same four. SP is unique in per-message web views (CAM-014). NU alone has UTM auto-tagging (CAM-015) and timezone-aware sending (CAM-016 — itself a near-industry-wide gap; RF's implementation is a stub). Recurring campaigns (CAM-003) are verified absent in all 12.

| Capability | Who has it | Notes |
|---|---|---|
| Scheduling (CAM-002) | LM BM US NU SP KE MC WR HP (9✓) | US accepts natural-language dates ("tomorrow 9am") |
| A/B testing (CAM-008) | NU✓ KE▲ | NU: variant split + auto/manual winner; RF has subject-split API only |
| Pause/resume/cancel (CAM-010) | LM US NU WR | RF parity (atomic CAS lifecycle) |
| Public archive (CAM-012) | LM KE MC HP | RF has per-recipient expiring web views, not an archive |
| Multiple newsletters (CAM-013) | LM KE MC HP | SP's lists/tags and WR's topics are adjacent, not publications |
| Duplication (CAM-004) | LM BM WR | RF backend-only ◐ |
| Gradual sending (INF-015) | US NU MC KE HP + LM◐ | BM deliberately ships no speed limits (positioning) |
| Failed-recipient retry (CAM-018) | NU✓ (`broadcasts.retryFailed`) | RF per-message retry only ◐ |

### 2.4 Templates & editors

Editor sophistication clusters into three camps. **Rich visual+code platforms:** NU (visual MJML/Liquid builder, versioning TPL-009, localization TPL-010 — licence-gated, import/export TPL-018) and KE (block editor, MJML, Liquid, Markdown, content slots CAM-006). **Developer-first code templates:** US (jsx-email/Tiptap builder + React Email), WR (React Email TSX as code, brand kit TPL-013, CLI preview/push), HP (Twig + ProseMirror rich text + cssinliner). **Basic substitution:** SP (`{{placeholder}}` only, no logic) and BM (HTML/DnD + AI + spintax TPL-021). RF ships GrapesJS preset-only (TPL-002 ◐) — no competitor that ships a builder treats it as deeply as NU/KE. MJML (TPL-007) exists only in NU and KE; React Email (TPL-008) only in US and WR; template versioning (TPL-009) only in NU; reusable blocks (TPL-012) nowhere.

| Capability | Who | Registry |
|---|---|---|
| DnD/WYSIWYG builder (TPL-002) | LM BM US NU KE + MC◐ | SP✗ WR✗ HP✗ explicit |
| MJML (TPL-007) | NU KE | RF missing (audit-E) |
| Liquid-class engine (TPL-006) | NU KE LM◐ | RF: Go templates + Mustache-style normalization ◐ |
| Versioning (TPL-009) | NU only | RF IMPLEMENTED |
| Localization (TPL-010) | NU only (E — translations licence-gated) | RF IMPLEMENTED |
| CSS inlining (TPL-011) | SP HP | RF go-premailer IMPLEMENTED |
| Brand kit (TPL-013) | WR only | RF stylesheet-entity IMPLEMENTED |
| AI-assisted (TPL-020) | BM NU + WR(E) MC◐ | RF deliberately absent (self-host stance) |

### 2.5 Analytics & tracking

Open/click tracking (ANA-001/002) is near-universal with two principled exceptions: **HR excludes open/click tracking by design** ("wouldn't be too hard at the app-level") and **HP excludes it as product policy** ("no hidden switch"). Delivery-status tracking beyond SMTP accept (ANA-003) requires ESP callbacks — PO, US, NU, MC, SE, WR have it; RF currently defines a `delivered` event it never produces. Bounce categorization (ANA-004) is strongest in SE (SES bounce type + subtype with backfill diagnostics), US, MC; LM and PO are heuristic/partial. Per-message event timelines (ANA-008) are a strength of PO, BM, NU, SE, HR, MC, WR — the registry credits RF with the schema plus a 6-event timeline. MC is alone in provider/inbox-provider breakdowns (ANA-009) and pairs them with Gmail Postmaster integration; NU alone has device/geo breakdowns (ANA-015) and bundled web analytics (ANA-016). Privacy kill-switches (ANA-012) exist in LM/KE with global toggles; RF's global toggle has a campaign-pixel leak (◐).

| Depth dimension | Strongest | Weakest |
|---|---|---|
| Observability depth (ANA-008/013) | PO, HR (SMTP conversation traces), SE (per-message diagnostics) | SP (ESP-webhook counts only), LM (aggregate only) |
| Bounce intelligence (ANA-004/010) | SE, MC, US | SP, KE◐, HP (no opens/clicks at all by policy) |
| Privacy modes (ANA-012) | LM✓ KE✓ (global + per-campaign opt-outs) | HP✗ HR✗ (policy non-features), MC (no mode documented) |
| Trend analytics (ANA-007) | LM BM US NU SE WR | SP, KE◐, MC (dashboards not range APIs) |

### 2.6 Automation

The automation domain is a near-monopoly: **NU implements 15 of 18 registry automation entries** (visual builder, durable crash-safe engine, 9 node types incl. A/B split and SSRF-guarded webhook nodes, re-enrollment policy, execution stats, welcome sequences, stop-on-reply). **WR** is the only other journey engine, expressed as TypeScript code (`defineWorkflow` with `delay`, `condition`, `waitForEvent`) plus the only re-engagement/scheduled-enrollment trigger (AUT-015) and workflow validation (AUT-018 ◐). Everyone else tops out at automated suppression (AUT-017 — 9 projects), double opt-in flows, and welcome/confirmation emails. LM has no engine at all and leans on community n8n/RSS integrations; RF's registry profile (14 implemented / 2 partial) mirrors the NU donor set minus A/B-node winner logic and stop-on-reply.

### 2.7 Webhooks

Outbound event webhooks (API-009) exist in only 5 of 12: PO, US, NU, HR, WR (LM✗ BM✗ SP✗ KE✗ explicitly). The reference security stack is US/NU/HR — all three sign payloads (API-010), retry with backoff (API-011), and persist delivery history (API-012). Concrete deltas: **US** adds a 5-minute replay-rejection window, timing-safe comparison, and auto-disable after 30 consecutive failures; **NU** implements the Standard Webhooks spec (`whsec_`, webhook-id/timestamp/signature), a 30s→24h retry ladder over ~34h, 410 retirement, and auto-disable after 20 failures >2h; **HR** uses HMAC-SHA256 `X-Signature` with a 1m→24h six-retry ladder. PO provides 7 days of webhook request history for debugging but documents neither signing nor retries. Test-fire/replay (API-013) exists only in NU — manual delivery replay is verified absent industry-wide (registry "verified absent" list item 4). RF has signatures + exponential backoff + delivery history but no timestamp/replay window and (pre-a6f1681) no replay.

### 2.8 Security & administration

Nobody in the set ships SAML SSO or SCIM (registry "verified absent" items 7–8) — every SSO implementation is OIDC (SEC-006: LM, PO, NU(E), US◐ via GitHub OAuth). 2FA (SEC-005) is effectively absent: LM✗ is the only explicit verdict, and RF's own TOTP enforcement is dead code (registry P0 note). RBAC depth ranges from PO's deliberate two-level model to NU's 14 permission areas (licence-gated to save non-full-access sets) and KE's role/group schema. Audit logs (SEC-004) exist in NU(E) (append-only, DB-trigger enforced, before/after diffs), BM◐ (operation logs) and WR◐ (plan-gated retention); RF ships three overlapping surfaces with CSV export. Outliers: BM's ban engine with CIDR pub/sub propagation (SEC-012 — an RF differentiator benchmark), NU's SSRF guard with pinned-dial transport (SEC-018), KE's session revocation (SEC-007) and plans/quotas (SEC-014).

### 2.9 Multi-tenancy models

| Project | Tenancy model | Isolation mechanism | Gating |
|---|---|---|---|
| BM | Single-admin instance | None documented | — |
| LM | Single-tenant instance | ✗ no workspaces (SEC-001) | — |
| PO | Organizations → mail servers | Isolated credentials/domains/retention per server; per-server message DB | Minimal RBAC |
| US | Teams | Row-level `team_id` scoping (Prisma) | — |
| NU | Workspaces | **One PostgreSQL database per workspace** (system DB + per-workspace DBs) | >3 workspaces = paid licence (E) |
| SP | Workspaces | Scoped subscribers/tags/campaigns/messages; also embeddable as package | — |
| KE | Projects-as-workspaces + shared senders | PG schema; quotas/credits (SEC-014) | ◐ |
| MC | Accounts (≤3 newsletters each) + teams (≤5) | Mongoose accountId guard plugin, isolation test suite | — |
| SE | Accounts → sources (per-environment streams) | Per-source webhook token, retention | ◐ |
| HR | Tenants → projects | Scoped access, project-bound keys (max 10) | — |
| WR | Org slugs (unlimited) | org-scoped dashboard/API | Org/AI surface = proprietary EE code |
| HP | Hyvor org accounts + collaborators | Platform-level (Hyvor Core) | Self-host OIDC pending |

### 2.10 Self-hosting & deployment story

All 12 publish source and describe self-hosting, but operational weight varies by two orders of magnitude. Easiest: **LM** (single Go binary + PostgreSQL, nothing else), **KE** (single container + PostgreSQL), **US** (compose bundling PG+Redis+RustFS) and **NU** (docker run + your PostgreSQL, setup wizard). Hardest/conditional: **PO** and **HR** expect operators to run public SMTP (and for HR, DNS) on host networking — deliverability and DDoS protection are the operator's job; **MC** requires Upstash QStash (a third-party SaaS) in the critical path plus MongoDB; **WR** requires a vendor-issued `wraps_lic_` key and deploys into your AWS account; **HP** is officially *not* self-hostable yet per the vendor's own README/FAQ (contradicted by its privacy-first page and hosting guide — dossier-12 §5). BM is compose-only, single-node, no HA story. Deployment tooling with real CI (OBS-016) is documented in LM, PO, US, NU, BM, SE, HR — RF's compose+multi-arch CI matches this cluster.

### 2.11 Licensing & maintenance

License families (from dossier §2 of each dossier):

| Family | Projects | Practical implications |
|---|---|---|
| **MIT** | PO, SP | No copyleft; safest to study and re-implement patterns from. |
| **AGPL-3.0 (clean)** | LM, BM, US, MC, HR*, HP | §13 network copyleft: modified versions offered over a network must offer source. Fine for internal reference/inspection; copying code into ReForwarder would put ReForwarder under AGPL obligations. *HR also sells an AGPL escape (enterprise from €10,000/yr, same features). |
| **AGPL-3.0 with proprietary carve-out** | KE (`extra/` cloud code excluded from license), WR (EE directories — org dashboard/API/AI — proprietary; production use requires a commercial agreement), MC (cloud marketing site not in repo; an early commit footnote claims MIT historically) | Core is inspectable; the carved-out code may not be used/modified/redistributed. WR's EE carve-out covers precisely the self-host control plane. |
| **BUSL-1.1 (source-available)** | NU (v40+; ≤v39 permanently AGPL; Change License AGPL-3.0 at +4 years; 6 "Licensed Features": >3 workspaces, granular permission sets, SES tenant provisioning, OIDC SSO, template translations, audit-log recording; offline key; PRs not accepted) | Research-reference only — no production use of current versions outside the license grant; do not clone its gating boundaries or code. The v39 AGPL history is the safe open reference. |
| **Proprietary source-available (anti-SaaS)** | SE ("O'Saasy License", NOASSERTION; no competing hosted product; attribution required; Claude/Cursor plugin dirs MIT) | Self-host/internal use OK; "open-source" branding overstates it. |

Maintenance status (dossier §4; all dates as of research date 2026-11-05):

| Project | Latest release | Cadence & risk |
|---|---|---|
| LM | v6.2.0 stable (2026-06-26) + v7.0.0-beta.1 (2026-10-20) | 2–4 releases/yr; pushed 2026-11-04; healthy, 24.1k stars |
| BM | v4.9 (2025-12-11) | **Stall risk: no releases ~11 months, no pushes ~5 months**, 252 open issues; repo moved aaPanel→Billionmail org |
| PO | v3.3.7 (2026-06-03) | ~20-month dormancy 2024→2026; 2026 releases security-only (SSRF/SQLi/XSS); two named maintainers + Krystal backing |
| US | v1.10.0 (2026-10-26) | Monthly-ish; responsive security batch; beta label |
| NU | v42.1 (2026-10-28) | ~monthly, very active; single-vendor (sole trader licensor); PRs not accepted |
| SP | v4.0.0.1 (2026-10-22) | One major line per 12–18 months; small team; AI-co-authored commits noted |
| KE | 0.40.2 (2026-11-04) | Resumed after ~9.5-month 2025 gap; **bus factor 1** (single maintainer) |
| MC | none (git-only delivery) | Created 2026-10-10; very active but **issues disabled, PRs may not be reviewed**; single primary author |
| SE | none (rolling `:main` image) | Very active single maintainer; self-described "very early stage" |
| HR | 0.0.48 (2026-10-21) | Monthly-ish; pre-1.0; cloud tier announced, unshipped |
| WR | CLI v3.20.1 (2026-11-04) | Weekly cadence; very low adoption (66 stars); EE license dependency |
| HP | 0.0.63 (2026-10-21) | Rapid 0.0.x trains without release notes; 4 recurring contributors; 34 stars |

---

## 3. Architectural differences

| Project | Language | Database | Queue/worker model | Delivery engine |
|---|---|---|---|---|
| LM | Go (Echo), Vue/Buefy UI | PostgreSQL only (no Redis, no broker) | In-process sender pool + sliding windows, PG-backed | **Delegated** — SMTP client + generic HTTP messengers; hybrid (messengers can carry SMS/push) |
| BM | Go core container | PostgreSQL 17 + Redis 7 | No separate worker service; Postfix delivers | **Native MTA** — Postfix/Dovecot/Rspamd/Roundcube compose; optional relay-out to SES/Mailgun |
| PO | Ruby on Rails 7.1 | MariaDB only (+ per-server message DB) | DB-backed job queue in v3 (no broker gems; exact mechanism NEEDS_VERIFICATION in dossier) | **Native MTA** — own SMTP server process, worker processes scale horizontally |
| US | TypeScript (Next.js 15, Hono API) | PostgreSQL (Prisma) + Redis (BullMQ) | BullMQ workers | **Delegated** — AWS SES/SNS only; SMTP proxy app converts SMTP→API |
| NU | Go (clean architecture) + React/Ant Design console | PostgreSQL — system DB + one DB per workspace | In-app rounds engine (≤10 concurrent per integration, ~6k/min practical) | **Delegated** — 7 provider integrations (SES, Mailgun, Postmark, Mailjet, SparkPost, SendGrid, SMTP); SMTP bridge for submission |
| SP | PHP 8.3 (Laravel 13 + Livewire) | MySQL or PostgreSQL | Laravel queues (sync/database/redis) + Horizon; 2 dedicated queues | **Delegated** — SES/Postmark/SendGrid/Mailgun/Mailjet/Postal APIs + SMTP |
| KE | Elixir (Phoenix 1.7 LiveView) | PostgreSQL only | Oban (PG-backed workers: render, delivery, scheduler, pruner) | **Delegated** — sender adapters: gen_smtp, SES, Sendgrid, Mailgun, Postmark |
| MC | JavaScript (Next.js 16, React 19) | MongoDB (Mongoose) | **Upstash QStash (mandatory third-party SaaS)** + Vercel Cron | **Delegated** — SES tenants (SESv2 + SNS) or PostShiba per-tenant SMTP |
| SE | Ruby on Rails | SQLite default; PostgreSQL via `DATABASE_URL` | Solid Queue + MissionControl jobs UI | **Observes only** — SNS push webhook ingest; no send path |
| HR | PHP Symfony (API) + Go single binary (workers, SMTP, DNS, webhooks) + SvelteKit console | PostgreSQL (DB *and* queue) | PG-backed queues; tx vs bulk isolation | **Native MTA + DNS** — own SMTP server, built-in DNS server, enforced DKIM |
| WR | TypeScript (SST/Pulumi IaC, pnpm/Turbo monorepo) | PostgreSQL (Drizzle; BYO Neon/Supabase/self-hosted) | SQS + DLQ + Lambda in the customer's AWS account; DynamoDB event history | **Delegated to AWS directly** — "Wraps is never in the sending path"; SES receipt rules for inbound |
| HP | PHP 8.4 (Symfony 8, FrankenPHP) + SvelteKit console/embed/archive | PostgreSQL | Symfony Messenger workers (`WORKERS` env) | **Delegated entirely to Hyvor Relay** (separate AGPL product, native MTA) |

**Delivery-path grouping.** Native-MTA projects (own SMTP egress): **PO, BM, HR** — these are ReForwarder's true structural peers; they own IP reputation, bounce detection at the SMTP layer, DKIM/DNS, and (PO/HR) inbound MX. Delegating projects: **US, MC, KE, NU, SP, WR, HP** — they treat an ESP API (or, for HP, a sibling product) as the delivery engine and build value in the application layer; their bounce/delivery data arrives via provider webhooks. Two nuance cases: **US** and **NU** accept inbound SMTP (proxy/bridge) but still delegate egress, and **WR** is delegation taken to the extreme — the IaC itself provisions the AWS pipeline in the customer's account. **SE** belongs to neither camp: it only observes an SES event stream. **LM** is the hybrid: SMTP-client egress like the delegators, but with an operator-configurable sender-pool abstraction (per-campaign/per-domain SMTP selection, VERP mailbox ingestion, HTTP messengers) that behaves like a mini-MTA router without owning any transport.

---

## 4. Unique vs common capabilities

### Capabilities found in exactly one project (owner)

| Registry ID | Capability | Owner |
|---|---|---|
| INF-003 | Full mail server (mailboxes, IMAP/POP, webmail) | BM |
| INF-020 | Bounce mailbox / VERP ingestion | LM |
| INF-024 | Per-message/per-campaign server selection | LM |
| INF-033 | SMTP connection diagnostics | BM |
| INF-034 | Greylisting & transient-failure handling | HR |
| INF-036 | Multi-channel messengers (SMS/push adapters) | LM |
| DOM-004 | DKIM key rotation & DNS publication verification | HR ◐ |
| DOM-013 | rDNS/PTR guidance | HR ◐ |
| CON-015 | Unsubscribe reasons | US |
| CAM-006 | Template content slots / campaign overrides | KE |
| CAM-014 | Web view of message | SP |
| CAM-015 | UTM auto-tagging | NU |
| CAM-016 | Timezone-aware sending (shipped) | NU |
| CAM-018 | Failed-recipient retry (`retryFailed`) | NU |
| TPL-009 | Template versioning & activation | NU |
| TPL-010 | Template localization (multi-language) | NU (E) |
| TPL-013 | Brand kit / shared stylesheets | WR |
| TPL-015 | Editor device previews | MC ◐ |
| TPL-021 | Spintax variation | BM |
| ANA-009 | Provider/inbox breakdown analytics | MC |
| ANA-016 | Bundled web analytics (site traffic) | NU |
| AUT-001/007/008/009/011/012/013/014/016 | Automation-engine depth (visual builder, list nodes, A/B split node, webhook node, re-enrollment, exec stats, lifecycle, welcome sequences, stop-on-reply) | NU (all ✓) |
| AUT-015 | Re-engagement / scheduled enrollment | WR |
| API-005 | Per-key IP/CIDR allowlists | BM |
| API-006 | API key expiry & rotation | HP |
| API-013 | Webhook test-fire | NU |
| API-019 | Embeddable core package | SP |
| SEC-007 | Session management & revocation | KE ◐ |
| SEC-012 | Ban engine / brute-force protection | BM |
| SEC-014 | Plans & quota enforcement | KE |

### Capabilities ≥6 projects have (any positive flag)

| Registry ID | Capability | Count (✓/◐/▲/E) |
|---|---|---|
| INF-009 | Background queue pipeline | 12 ✓ |
| API-001 | Full-coverage REST API | 11 (9✓+2◐) |
| INF-019 | Bounce ingestion & auto-suppression | 11 (10✓+1◐) |
| ANA-001 / ANA-002 | Open / click tracking | 10 each (9✓+1◐; HP✗ HR✗) |
| INF-006 | Bulk campaign sending | 10 (9✓+1◐) |
| INF-014 | Scheduled sends | 10 (9✓+1◐) |
| INF-017 | Retention/purge | 10 (8✓+2◐) |
| INF-021 | Complaint/FBL ingestion | 10 (7✓+3◐) |
| API-003 | API keys | 10 ✓ |
| OBS-005 | PostgreSQL system of record | 10 (8✓+2◐, PO/MC✗) |
| SEC-001 | Multi-tenancy | 10 (6✓+E+3◐; LM✗) |
| ANA-006 / CAM-011 | Campaign analytics/reports | 9 (7✓+2◐) |
| AUT-017 | Automated suppression | 9 (8✓+1◐) |
| API-014 | Inbound ESP event webhooks | 9 (7✓+2◐+1✗) |
| CAM-001 / CAM-002 | Campaign drafts / scheduling | 9 ✓ each |
| CON-001 / CON-009 / CON-014 | Subscriber records / CSV import / RFC 8058 unsubscribe | 9 ✓ each |
| INF-004 / INF-012 / INF-018 | Transactional API / rate limits / suppression lists | 9 each |
| DOM-001 | Custom sending domains | 8 (incl. LM✗) |
| CON-004 / CON-005 | Double opt-in / custom attributes | 8 each |
| TPL-005 | Template variables / merge fields | 8 (7✓+1◐) |
| API-007 | API rate limiting | 8 (6✓+2◐) |
| INF-022 / API-015 | ESP integrations (shared sending-side entry) | 7 ✓ |
| TPL-001 / CON-002 / CON-010 / CON-003 / SEC-003 / SEC-002 / API-002 / OBS-016 / ANA-008 | Raw HTML editor; mailing lists; CSV export; subscription statuses; teams; RBAC; OpenAPI; deployment tooling; per-message timelines | 7 each |
| INF-015 / TPL-002 / CAM-009 / API-004 / ANA-007 | Gradual sending; DnD builder; audience targeting; scoped keys; historical trends | 6 each |

**Near-miss clusters worth noting (2–5 projects):** built-in SMTP submission (5), outbound webhooks (5), attachments (5), custom headers (5), idempotent sends (3: US/HR/NU◐), circuit breakers (2: NU/MC), automated DNS (2: HR/MC◐), engagement segmentation (2: NU/KE), template import/export (1◐: NU) — these are differentiators, not table stakes.

---

## 5. Strengths, limitations & best use cases per project

Condensed from each dossier's §7 (explicit non-features) and §8 (limitations & security notes); full detail in `01-oss-feature-inventory.md`.

**LM — Listmonk** (AGPL-3.0)
- Mature and radically simple: single Go binary + PostgreSQL only; 24.1k stars; strong release hygiene.
- Best-in-class sending ergonomics for an SMTP client: per-campaign SMTP selection, per-domain routing, sliding-window rate limiting, HTTP messengers.
- No automation, no A/B, no multi-tenancy, no inbound email, no built-in MTA; bounce handling is heuristic and mailbox-destructive (POP3 download-and-delete).
- Security sharp edges: Turing-complete Sprig templates (DoS vector), powerful `subscribers:sql_query` permission; recurring RBAC permission patches.
- Best use: self-hosted newsletter/mailing-list manager for technical operators; pattern donor for sender-pool and segmentation design.

**BM — BillionMail** (AGPL-3.0)
- Only project combining a full mail server (mailboxes, IMAP/POP, Roundcube) with a marketing stack; DKIM/Let's Encrypt/SNI, blacklist monitoring, visual Postfix queue manager, AI templates + spintax.
- **Maintenance stall: no release since 2025-12-11, no pushes since 2026-06-11** (as of 2026-11-05); repo moved owners; development whereabouts unverified.
- Single-tenant, single-admin; no RBAC/2FA evidence; core container mounts docker.sock (read-only) — escape surface; "no speed limits" positioning is a deliverability trap.
- Best use: all-in-one self-hosted mail server + marketing for panel-style operators — adopt only with eyes open on stall risk; treat as cautionary architecture reference.

**PO — Postal** (MIT)
- Purest self-hosted delivery-infrastructure analogue: org→mail-server tenancy, IP pools, inbound MX + routing rules, held queue, automatic suppression, DKIM + custom return-path, per-message logs, 7-day webhook request history.
- MIT license — no copyleft constraints on studying/ports.
- MariaDB-only; 1024-bit DKIM keys; minimal two-level RBAC; no 2FA; no management API (v2 "no ETA"); webhook signing/retries undocumented; 2024–2025 dormancy followed by security-only 2026 releases.
- Best use: self-hosted Sendgrid/Mailgun replacement run by operators; closest prior art for RF's relay+admin architecture (registry A/B matrices align closely).

**US — useSend** (AGPL-3.0)
- Developer-first DX: idempotency keys, official TS/Python/Go SDKs, OpenAPI, natural-language scheduling, strongest webhook security stack (HMAC + timestamp + replay window + backoff + auto-disable).
- Hard AWS dependency (SES + SNS; broad IAM policy documented); GitHub-OAuth-only auth (no local accounts); no segmentation, A/B, automation, template API; beta label.
- Best use: transactional + light-marketing platform for AWS-native teams; primary donor for idempotency/webhook-hardening patterns.

**NU — Notifuse** (BUSL-1.1 v40+; ≤v39 AGPL)
- Deepest application layer in the set: MJML+Liquid+versioning+localization, canvas automations with A/B node and stop-on-reply, campaign A/B with winner selection, per-workspace DB isolation, Standard-Webhooks-spec outbound webhooks, circuit breaker, 7 ESP integrations, per-workspace branded endpoints.
- License-risky: 6 licence-gated features (workspaces >3, granular RBAC, SES tenants, OIDC SSO, translations, audit recording); PRs not accepted; no SDKs, no 2FA; telemetry flag default-on contradicts "no telemetry" claim (dossier-flagged).
- Best use: feature-rich Mailchimp/Klaviyo-class self-host **if** the BSL model is acceptable; for ReForwarder, research-reference only (see §6).

**SP — SendPortal** (MIT)
- MIT + unique embeddable `sendportal-core` Laravel package; 7 ESP adapters with encrypted credentials; multi-workspace; automatic CSS inlining; Horizon queue observability.
- No transactional API, no lists (tags only), no custom fields (explicit non-feature), no DOI evidence, no visual editor, tracking impossible over SMTP, mandatory cron + two queue workers (silent breakage if forgotten), slow release cadence.
- Best use: PHP/Laravel shops embedding campaign management into an existing app; donor for the package-extraction architecture pattern.

**KE — Keila** (AGPL-3.0; `extra/` cloud code excluded)
- Privacy-respecting newsletter tool: tracking opt-outs, disableable phone-home, signed links, captcha'd forms, DOI; strong authoring (block editor, MJML, Liquid, Markdown, content slots); KQL + interaction-based segments; quotas/credits and shared senders for multi-tenant self-hosts.
- **Bus factor 1** with a demonstrated 2025 stall; no outbound webhooks; A/B and automations are roadmap-only (homepage claims are not shipped); no 2FA/SSO; PG-only; sample compose not production-ready.
- Best use: privacy-focused newsletter for small orgs; donor for segment-query-language and quota design.

**MC — MailCheap** (AGPL-3.0)
- AI-agent-first: MCP server + OAuth for ChatGPT/Claude with parity-tested REST/MCP/OpenAPI surface; excellent import hygiene (spam-trap/misspell cleaning), inbox-provider analytics, Gmail Postmaster integration, bounce/complaint auto-pause thresholds, RFC 8058 + Feedback-ID headers.
- Upstash QStash is mandatory SaaS in the critical path (not air-gappable as documented); MongoDB; no Docker app image; issues disabled and PRs unreviewed (bus factor); no A/B, no outbound webhooks, no TOTP.
- Best use: agent-operated newsletters on Vercel-style stacks; donor for MCP/API-parity engineering and deliverability guardrails.

**SE — Sessy** (source-available "O'Saasy", anti-SaaS)
- Unmatched SES observability: 10 event types, bounce type/subtype diagnostics with backfill, per-message timelines, per-source retention, exportable logs, read-only MCP; honest docs and clean SaaS-code separation.
- Sends nothing (observability only); SES-only; no versioned releases (rolling `:main`); thin webhook auth (token-in-URL; SNS signature validation unverified); license forbids offering it as a competing hosted product.
- Best use: teams on raw AWS SES wanting a console; donor for event taxonomy, bounce-categorization depth, and per-source retention models.

**HR — Hyvor Relay** (AGPL-3.0; enterprise from €10k/yr)
- Modern pure-infrastructure design: idempotent sends, HMAC-signed webhooks with a 1m→24h retry ladder, tx/bulk queue isolation, built-in DNS server with enforced DKIM and domain lifecycle states, scoped keys, Prometheus/Grafana/Alertmanager, 30-day SMTP-conversation logs.
- Pre-1.0; OIDC-only auth (mandatory external IdP); no open/click tracking and no templates by explicit decision; public SMTP+DNS make L3/L4 DDoS protection the operator's job; cloud tier unshipped.
- Best use: self-hosted SES/Mailgun alternative; **the closest modern architectural sibling to ReForwarder's delivery core** (compare INF/DOM matrices row-by-row).

**WR — Wraps** (AGPL-3.0 core + proprietary EE)
- Code-first DX: React Email templates as code with CLI preview/push, TypeScript workflows (`waitForEvent`), brand kit, CLI + CDK + Pulumi + MCP + docs-server, inbound email, account-health sweeps — all deployed into the customer's AWS account via IaC.
- **License asymmetry: org dashboard/API/AI (EE directories) are proprietary** — production self-host requires a commercial agreement; AWS-only; plan-gated segments/history; SaaS mode centralizes data on vendor-hosted Neon; no DnD/A-B.
- Best use: AWS-native teams wanting an IaC email stack; license-risky for emulation — reference for workflow-as-code and brand-kit patterns only.

**HP — Hyvor Post** (AGPL-3.0; composer.json metadata conflict flagged)
- Strongest privacy stance in the set: no open/click tracking ever ("no hidden switch"), no profiling, DOI by default, embeddable form web component, auto-generated archive website, scoped API keys, fast CVE response (fixed 2 CVEs within days).
- **Self-hosting officially unsupported** (vendor README/FAQ) with contradictory marketing pages; OIDC sign-in is the named blocker; hard coupling to a Hyvor Relay deployment; no transactional API, no outgoing user webhooks, image-only media (no attachments); tiny community (34 stars), pre-1.0.
- Best use: privacy-first newsletters inside the Hyvor ecosystem; donor for no-tracking product policy, DOI defaults, and archive-site design.

---

## 6. Relevance summary for ReForwarder

| Class | Projects | Rationale (registry/dossier-sourced) |
|---|---|---|
| **Closest architectural analogues** (self-hosted, own delivery path) | **PO, HR, BM** (+ LM as hybrid) | Native MTA / own SMTP: PO (Rails MTA, IP pools, inbound MX, held queue), HR (Go SMTP+DNS, queue isolation, idempotency, signed webhooks), BM (Postfix stack + full mail server). LM shares the operator-configured-server model plus VERP/messengers without owning transport. Their INF/DOM/OBS matrices are RF's direct parity targets. |
| **Feature-donors only** (delegate delivery; rich app layer) | **NU, US, MC, KE, WR, SP, LM, HP, SE** | Value lives above the send path: NU (automation, A/B, templates, analytics), US (idempotency, webhook hardening, SDKs), MC (MCP parity, provider analytics, auto-pause), KE (segments, quotas, privacy), WR (workflow-as-code, brand kit, CLI/IaC), SP (package embedding, web views), LM (messengers, sender pools), HP (no-tracking policy, key expiry), SE (event taxonomy, bounce diagnostics). |
| **License-risky to emulate** (research-reference only) | **NU (BUSL-1.1), WR (proprietary EE), SE (anti-SaaS source-available)** | No production reuse of gated code or gating boundaries; no cloning of NU's Licensed-Feature set, WR's EE directories, or SE's hosted functionality. Inspect concepts, re-implement independently. PO/SP (MIT) carry no such constraint; AGPL projects constrain *code copying*, not pattern study. |
| **Cautionary references** (maintenance) | **BM** (stall), **KE** (bus factor 1), **MC/SE** (single-maintainer, no releases/issues-off), **HP** (unsupported self-host) | Useful for design, not for dependency; re-verify before citing as active prior art. |

Next steps live in **`03-existing-feature-audit.md`** (what ReForwarder already implements per registry ID — the audit-A–J evidence base) and **`04-feature-gap-analysis.md`** (which of the donor capabilities above become roadmap items, with the "verified absent across all 12" list marking where RF could differentiate rather than catch up).

---

## 7. Research limitations

- **Date of research:** all evidence was collected 2026-11-05 from repos, docs, and release feeds (web research only). Release counts, cadences, and stall measurements (BM's ~11-month release gap, KE's 2025 gap) are snapshots as of that date and will drift.
- **NEEDS_VERIFICATION carryover:** each dossier ends with an unverified-items list (e.g., PO's v3 queue mechanism and webhook signing; KE's attachment support and webhook coverage beyond SES; MC's segment model usage; HP's DOI-disable and form captcha; NU's telemetry payload). The registry deliberately *omits* unverified cells rather than guessing — blank cells in the matrices above must not be read as "absent". See the per-dossier §9 lists before relying on any · cell.
- **Maintenance-stall risks:** BM shows the hardest stall signal (no release since 2025-12-11, no push since 2026-06-11, repo ownership moved, 252 open issues accruing). KE is actively releasing but effectively bus-factor-1 with a demonstrated 9.5-month 2025 pause. MC disables issues and may not review PRs; SE ships no versioned releases (rolling images complicate pinning/auditing); SP's cadence is one major line per 12–18 months with AI-co-authored commits.
- **License caveats:** SE is source-available with an anti-SaaS clause (not OSI open source despite marketing), WR's EE directories are proprietary and cover exactly the self-host control plane, and NU's BUSL-1.1 boundary is versioned (≤v39 AGPL) with six gated features. License families are summarized in §2.11; do not re-derive them from README badges.
- **Vendor-claim caution:** marketing claims that contradict fetched documentation (NU "no telemetry" vs default-on flag; KE homepage A/B/automation roadmap items; HP privacy-page "yes you can self-host" vs README "not yet ready") are flagged in the dossiers and treated here as unverified claims, not features.
