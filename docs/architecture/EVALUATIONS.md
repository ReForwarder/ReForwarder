# Evaluations — JMAP (RF-B126 / SF-84) & Groupware (SF-111 / SF-112)

- **Status:** Evaluations only (2026-11-11 — OB-6 convergence wave). **These are
  not commitments.** No feature on this page is scheduled, claimed, or ledgered
  as landed; each records the honest state of effort, dependencies, and demand
  so a future decision is cheap, and so absence of demand is *recorded* rather
  than silent (RF-B126's acceptance discipline).
- **Discipline:** demand evidence is collected — or its absence recorded —
  before any build decision (RF-B126); candidates are studied only through the
  written analyses under `docs/product-roadmap/` and `docs/research/` (AGENTS.md
  §7 clean-room rule); license verification is a prerequisite of any adoption.

## 1. JMAP server — RF-B126 / SF-84

### What it is

RFC 8620 (core) + RFC 8621 (mail): a stateless JSON API over the mailbox
store — stable object identities, per-object state strings with `/changes`,
batch requests, and push (EventSource / long-poll). It is the modern
alternative to IMAP for programmatic mail clients.

### Dependencies — all satisfied except demand

| Dependency | State | Evidence |
|---|---|---|
| Mailbox backend (RF-B120) | **Landed** — mailstore with stable message IDs, folder membership, flags, append/copy/move, FTS, quotas | OB-3 per ADR-003; `docs/audits/FEATURE_PARITY_AUDIT.md` (SF-75/77/79/80/83 → COMPLETE, 2026-11-08) |
| Protocol seam precedent (RF-B121) | **Landed** — IMAP4rev1 + POP3 consume the same store over app-password auth with TLS and limits | OB-4 per ADR-004; `internal/services/imap/`, `internal/services/pop3/`, `internal/services/mailauth/` |
| A protocol-server posture in the product | **Landed** — ADR-009 §1's gated-listener model means a JMAP listener is one more config gate, not a new deployable | `docs/architecture/ADR-009-deployment-topology.md` |
| Demand evidence | **Absent (recorded 2026-11-11)** | see below |

### Demand signals — recorded absence

- The frozen 199-feature inventory does **not** verify JMAP as a requirement;
  it "appears in the original exclusion discussion, not as a verified
  requirement" (`docs/product-roadmap/upcomming-convo.md` §4).
- The pre-Option B research classified SF-84 **NOT APPLICABLE / DO-NOT**
  ("REST/OpenAPI already covers its API needs" —
  `docs/research/infrastructure-feature-roadmap.md:163`;
  `docs/research/platform-gap-analysis.md:57`). Option B reinstated
  IMAP/POP3/webmail (INF-003) but left JMAP an explicit evaluation.
- The feature ledger's own note stands: "REST/OpenAPI covers API needs today
  (RF-B126 evaluation pending)" (`docs/audits/FEATURE_PARITY_AUDIT.md`, SF-84).
- No operator request, issue, or dashboard telemetry signal tracked in the
  backlog mentions JMAP (searched `docs/project/BACKLOG.md`, `todo.md`,
  research notes — 2026-11-11).

### Fit with the mailstore — what JMAP would need that IMAP did not

The IMAP/POP3 waves prove the hard substrate exists (identity, folders, flags,
delivery, IDLE push over the Redis fabric). JMAP's incremental demand is
different and specific:

1. **Per-collection state strings + change logs** — `/changes` requires a
   monotonic change record per mailbox/message set. IMAP needs UIDs +
   UIDVALIDITY; JMAP needs queryable change tokens. The mailstore's change
   tracking (ADR-003's abstraction list) covers flags/expunge; the *exposed*
   token API would be new.
2. **Push** — the Redis pub/sub fabric already carries mailbox IDLE
   notifications (`internal/services/mailstore/notifier.go`); JMAP
   EventSource push reuses it, but the push-state bookkeeping is new.
3. **Submission extension** (RFC 8621 §"Submission") would map onto the
   existing submission pipeline (ADR-001 §10) — the one piece with a direct
   precedent.
4. **Security surface** — a new authenticated listener inherits ADR-004's
   non-negotiables (app-password-only auth, TLS enforcement, connection caps,
   command budgets, audit events) at the same bar as IMAP/POP3.

### Effort

Multi-wave, **larger than IMAP (OB-4) was** — the state-string machinery and
batch semantics are protocol-heavy even though storage is free. Realistic
shape: a dedicated phase-sized effort (listener + state machinery + tests at
the OB-4 bar + docs), not a wave-sized slice.

### Candidate implementations — license verification outstanding

- **In-house over the mailstore** (the default shape if built): AGPL-3.0
  owned code, same layering as `internal/services/imap`; no third-party
  protocol stack. Effort as above.
- **Adopted library/server**: none verified. Any candidate requires (a)
  license verification compatible with AGPL-3.0-or-later per AGENTS.md §7 —
  AGPL/BSL-licensed sources may inform behavior analysis only, never code or
  asset copying; (b) an ADR covering the dependency. Nothing in the repo
  today pre-qualifies a candidate.

### Cost/benefit & standing decision

**Not now.** The benefit case (modern client API) serves a demand signal that
does not exist in any recorded form, while REST + IMAP + webmail cover
programmatic and human access today. Re-evaluate when a demand signal is
recorded (client ecosystem requests from actual users, a partner integration
contract, or measurable API-consumer pull); RF-B126 stays open as the tracker.

## 2. Groupware — SF-111 (CalDAV/CardDAV/WebDAV) & SF-112 (JMAP FileNode + text extraction)

### SF-111 — CalDAV/CardDAV/WebDAV groupware

- **What it is:** calendar/contact/file-sharing protocol servers — the
  collaboration-suite surface (RFC 4791/6352/4918).
- **Demand signals — recorded absence:** the research verdict is explicit:
  "Different product category" (`docs/research/infrastructure-feature-roadmap.md:166`)
  and it heads the should-not-add list ("CalDAV/CardDAV/groupware/file storage",
  `docs/research/platform-gap-analysis.md:144`). ReForwarder is an email-
  operations platform + mail host; calendars/contacts address a different
  buyer. No tracked request exists.
- **Dependencies:** none meaningful — and that is the telling part. Unlike
  JMAP (which sits on a landed mailstore), groupware needs its **own data
  domain**: calendar/contact object stores, recurrence semantics, DAV
  collection ACLs, plus new HTTP/XML protocol listeners. It would not reuse
  the Option B investment.
- **Effort:** the largest item on this page — a phase-plus effort comparable
  to the whole mailstore programme, on an unrelated data model.
- **Cost/benefit & standing decision:** **Not now, and the bar to re-evaluate
  is higher than JMAP's** — it is a product-category expansion, not an API
  surface addition. The ledger records it PLANNED under the OB-6 revisit
  (`docs/audits/FEATURE_PARITY_AUDIT.md`, SF-111); this evaluation is that
  revisit's outcome: re-open only on explicit, evidence-backed demand
  (e.g., a hosted-mail customer segment asking for calendar/contact sync).

### SF-112 — JMAP FileNode document-style storage + text extraction

- **What landed already:** mail attachments are stored as content-addressed,
  encrypted, compressed blobs with per-message metadata, byte-exact round-trip
  tested (OB-3; `internal/services/mailstore/parse.go` attachment walk,
  `internal/models/mailbox.go`, ledger SF-112 → PARTIAL).
- **What remains:** (a) *document-style* storage — arbitrary user files
  managed like JMAP FileNode objects (upload/download/list independent of any
  message); (b) text extraction from document types feeding search.
- **Demand signals — recorded absence:** no tracked request for a general
  file store. The nearest plausible signal — "search inside attachments" for
  hosted mail — is also unrecorded, but it is the one variant that composes
  with existing investment: the mailstore FTS plane (landed OB-3) and the
  malware scanning pipeline (ADR-006, ClamAV on attachments in compose) are
  the natural seams for extraction + safe parsing.
- **Dependencies:** the blob store (landed, ref-counted, GC'd); for
  extraction, parser dependencies per document type — each one is a new
  attack/untrusted-input surface and a license review (AGENTS.md §7), and
  would ride the ADR-006 scanning gate before indexing.
- **Effort:** FileNode-style storage alone: wave-sized (S/M) — the blob
  substrate does the heavy lifting. Text extraction: the real cost center —
  per-type parsers, scanning integration, and indexing glue; a phase-sized
  effort once more than one type is supported.
- **Cost/benefit & standing decision:** **Not now; narrow trigger recorded.**
  If attachment-content search is ever requested by hosted-mail users, the
  first slice is extraction-for-search behind the existing FTS + ClamAV seams
  — *not* a JMAP FileNode dependency. The ledger keeps the JMAP FileNode
  slice PLANNED (OB-6); this evaluation records that the JMAP framing is not
  the prerequisite.

## Summary table

| Item | Ledger | Demand signal | Effort if built | Standing outcome (2026-11-11) |
|---|---|---|---|---|
| JMAP server (RF-B126 / SF-84) | PLANNED, revisit OB-6 | Absent (recorded) | Phase-sized; > OB-4 IMAP | **Not now**; RF-B126 tracker stays open |
| Groupware DAV (SF-111) | PLANNED, revisit OB-6 | Absent (recorded; different product category) | Phase-plus; new data domain | **Not now**; higher re-evaluation bar |
| FileNode + text extraction (SF-112) | PARTIAL (attachments landed) | Absent (recorded); narrow "attachment search" variant plausible | S/M (FileNode) / phase (extraction) | **Not now**; extraction-for-search is the first slice if demand appears |

## Related documents

- `docs/architecture/TENANCY-DECISION.md` — the parallel SF-66/71/72
  decision (same OB-6 revisit, decided as deferral-with-prerequisites).
- `docs/architecture/ADR-008-data-event-ownership.md` §6 — tenancy gate.
- `docs/architecture/ADR-009-deployment-topology.md` — gated-listener model a
  future JMAP/DAV listener would inherit.
- `docs/project/BACKLOG.md` RF-B126 — the JMAP evaluation tracker.
- `docs/audits/FEATURE_PARITY_AUDIT.md` — SF-84/111/112 ledger entries.
