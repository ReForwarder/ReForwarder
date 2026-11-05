Then let's make ReForwarder a complete email infrastructure and marketing platform, combining the strongest capabilities of all 12 projects, followed by Resend-inspired developer experience.

I've reviewed the uploaded audit documents. The important thing is that we don't need to rebuild everything from scratch.

Your audit establishes:
- 199 normalized features across 10 categories.
- 108 implemented, 46 partial, 38 missing, 3 needing verification and 4 not applicable in the frozen audit.
- 53 identified gaps, with 5 P0, 10 P1, 22 P2 and 16 P3.
- 9 duplicate/redundancy findings.
- An existing Go modular-monolith architecture that we should preserve.
These numbers are the audit baseline, not necessarily the current state of the repository today. We must reconcile newer commits before implementation.

My recommendation: make all relevant OSS capabilities part of the long-term ReForwarder product scope, but preserve existing working functionality, consolidate duplicates and resolve architectural risks before adding new systems.
1. What "all features" should mean
I recommend three scope levels:
Scope	Decision
All relevant OSS capabilities	Include
Existing partial features	Complete them properly
Features already implemented	Preserve, improve and test
Duplicate implementations	Consolidate
Features incompatible with current architecture	Require an explicit architecture decision
Commercial/licence-gated features	Build independent alternatives; respect licenses

There are 15 deliberately excluded items in the existing roadmap. Examples include direct-to-MX delivery, a full mailbox/IMAP/webmail server, strict priority queues, SAML/SCIM, and IP pools under the current relay-only architecture. 
05-implementation-roadmap.md

We should not silently discard those, but we should make a conscious product decision about them.
2. Implementation strategy
I would follow the existing 52-item roadmap:
1. Phase 0: Trust, security and delivery correctness.
2. Phase 1: Delivery signals, ESP integrations and sender architecture.
3. Phase 2: Marketing, campaigns, contacts, templates and analytics.
4. Phase 3: Advanced automation, administration and scalability.
Each batch must have tests, migration checks, API compatibility checks and documented completion evidence. 
05-implementation-roadmap.md

3. One important decision before implementation
How far do you want to take ReForwarder?

Option A — Complete email platform

Keep ReForwarder relay-based. Add all applicable OSS capabilities: transactional email, campaigns, marketing, contacts, automation, templates, analytics, APIs and integrations.


Option B — Complete email infrastructure

Everything in A, plus explore a full mail-server product: direct-to-MX delivery, MTA, mailboxes, IMAP/POP3, webmail, IP pools and associated infrastructure.


Confirm direction
My recommendation is Option A first. Build an exceptionally complete email platform without destabilizing the existing delivery architecture. We can evaluate Option B as a separate major expansion.

After that, I'll prepare the implementation prompt so the coding agent works through the entire roadmap rather than stopping after a handful of features.


Choose Option B: Expand ReForwarder towards complete email infrastructure, including full mail-server capabilities. Prepare an architecture-first implementation plan using the uploaded seven audit documents.



# ReForwarder — Option B: Complete Email Infrastructure

Scope selected: Full mail-server expansion

ReForwarder will evolve from a relay-based email delivery and marketing platform into a self-hosted email infrastructure platform: transactional email, campaigns, SMTP delivery, inbound mail, hosted mailboxes, IMAP/POP access, and webmail.

The important architectural decision is that we should not turn the existing Go application into one enormous SMTP/IMAP server. ReForwarder should retain its existing Go control plane and modular-monolith architecture, while introducing dedicated mail-data-plane components for protocols and message transport.

This is a deliberate change from the original audit roadmap. The audit classified direct-to-MX, IP pools, bounce-mailbox ingestion, and full mailboxes/IMAP/webmail as out of scope under the relay-only product strategy. Option B explicitly changes that product strategy, so those exclusions become candidates for a new architecture programme—not features to bolt onto the old 52-item roadmap.&#x20;

05-implementation-roadmap.md

06-architecture-impact.md



## 1. What the seven documents establish

The audit suite gives us a useful starting point, but its baseline must be treated as historical rather than assumed to describe the current repository.

| Audit document                 | How it informs Option B                                                                      |
| ------------------------------ | -------------------------------------------------------------------------------------------- |
| `README(4).md`                 | Overall research scope, 12 competitors, 199-feature registry and navigation.                 |
| `01-oss-feature-inventory.md`  | Permanent feature IDs and the detailed capability inventory.                                 |
| `02-competitor-comparison.md`  | Which projects provide mail-server, delivery-infrastructure and mailbox capabilities.        |
| `03-existing-feature-audit.md` | Existing ReForwarder routes, services, models, protocols and verified implementation status. |
| `04-feature-gap-analysis.md`   | The original 53 gaps, duplicate findings, evidence and acceptance criteria.                  |
| `05-implementation-roadmap.md` | The original priorities, dependencies, delivered work and explicitly excluded capabilities.  |
| `06-architecture-impact.md`    | Existing system boundaries, risks, migration strategy and compatibility requirements.        |

The frozen registry contains 199 features across ten categories. Its authoritative detailed status is 108 implemented, 46 partial, 38 missing, 3 needing verification and 4 not applicable. The original roadmap contains 52 remaining items after reconciliation. Those numbers are useful for the existing platform, but they are not a complete estimate of Option B.&#x20;

README(4).md

05-implementation-roadmap.md

03-existing-feature-audit.md



Competitor lessons:

- Postal is the strongest infrastructure reference: SMTP delivery, inbound MX routing, IP pools, suppression, delivery events and multi-tenant mail-server administration.
- BillionMail is the closest all-in-one reference for Postfix, Dovecot, mailboxes, IMAP/POP and Roundcube—but the audit flags its maintenance concerns and security trade-offs.
- Listmonk is a useful reference for campaign and audience capabilities, not a full mail server.
- useSend is useful for developer experience, API design, idempotency and webhook security.

These are architectural references, not instructions to copy their implementations. The audit specifically warns about AGPL/BSL licensing and copying competitor code.&#x20;

02-competitor-comparison.md

02-competitor-comparison.md

01-oss-feature-inventory.md



## 2. Target product definition

# ReForwarder

Self-hosted email infrastructure and communication platform

Outbound email

SMTP, direct-to-MX, queues, delivery and IP pools

Inbound email

MX reception, routing, filtering and storage

Hosted mailboxes

Mailbox provisioning, IMAP, POP and webmail

Email marketing

Contacts, campaigns, automation and analytics

Developer platform

REST API, SMTP credentials, SDKs and webhooks

Infrastructure security

Abuse controls, reputation, DNS and TLS

The product should support two distinct use cases without mixing their policies:

1. Email service provider: applications and businesses send transactional and marketing email through API or SMTP.
2. Hosted email provider: organisations create domains, mailboxes and users, then send and receive ordinary mailbox email.

These use cases share domain management, authentication, delivery infrastructure and administration, but they need separate sending policies, storage rules, quotas and abuse controls.

## 3. Recommended target architecture

The existing architecture is a Go modular monolith with PostgreSQL, Redis/asynq, workers, cron jobs, blob storage and a Vue dashboard. Its existing domain boundaries and tenant isolation are valuable. Preserve them.&#x20;

06-architecture-impact.md



For Option B, I recommend a control-plane / data-plane architecture.

ReForwarder Control Plane

Existing Go application + Vue dashboard

Tenants · Users · Domains · Mailboxes · Policies · API · Billing/quotas · Monitoring

Outbound data plane

SMTP submission · MTA · delivery queues · IP pools · DKIM

Inbound data plane

MX reception · SMTP filtering · routing · mailbox delivery

Mailbox services

Mailbox storage · IMAP · POP3 · optional JMAP · webmail

Security services

Spam filtering · malware scanning · abuse detection · reputation

Shared infrastructure

PostgreSQL · Redis · blob/message storage · event bus · metrics

### Architectural boundary rules

- The Go control plane owns tenant configuration, identities, domains, mailbox metadata, credentials, policies, API and orchestration.
- Dedicated protocol services own SMTP transport, inbound SMTP reception, mailbox access and message transfer.
- PostgreSQL remains the control-plane system of record. Do not assume every message body or mailbox operation belongs in the existing relational schema.
- Use explicit service interfaces and durable events between components. Do not make protocol services depend on private Go application internals.
- Keep tenant identity and ownership consistent across all data-plane services.
- Protocol services must be independently restartable and observable without making the dashboard or API unavailable.

Key recommendation: reuse mature, independently maintained protocol software where it is safer than implementing a protocol from scratch. ReForwarder should own the product integration, configuration, lifecycle and operator experience—not necessarily every SMTP, IMAP or spam-filtering implementation.

## 4. Component strategy

The following is the proposed design, not a claim that these components already exist in ReForwarder.

| Component        | Responsibility                                         | Recommended approach                                                               |
| ---------------- | ------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| Control plane    | Workspace, domains, users, policies, API               | Extend existing Go application                                                     |
| Outbound MTA     | Queue management, SMTP delivery, retries, direct-to-MX | Integrate a mature MTA behind a ReForwarder adapter                                |
| SMTP submission  | Authenticated client submission                        | Extend existing SMTP relay integration; add proper TLS and authentication policies |
| Inbound MTA      | Receive mail for hosted domains                        | Dedicated SMTP receiver and routing pipeline                                       |
| Mailbox storage  | Store messages, folders, flags and metadata            | Dedicated mailbox storage layer with explicit quota and retention rules            |
| IMAP / POP3      | Client mailbox access                                  | Integrate a mature server with a well-defined mailbox backend                      |
| Webmail          | User-facing inbox, folders, compose, search            | Separate frontend application or isolated module                                   |
| Spam and malware | Spam scoring, reputation, attachment scanning          | Integrate established filtering and scanning engines                               |
| DNS and TLS      | MX, SPF, DKIM, DMARC, PTR guidance, certificates       | ReForwarder domain-management and verification services                            |
| Observability    | Queue, SMTP, mailbox and delivery health               | Extend existing metrics, health and admin tooling                                  |

### Important protocol decisions

SMTP / MTA

Direct-to-MX delivery introduces responsibilities that the original relay architecture intentionally avoided: outbound port 25 availability, IP reputation, PTR records, connection management, retry scheduling, remote-server throttling, feedback loops and abuse handling. The architecture must treat these as first-class operational responsibilities.

IMAP / POP3 / mailbox storage

Do not store mailbox contents as ordinary transactional `Email` records. Mailbox email has different lifecycle semantics: folders, flags, read/unread state, deletion, expunging, concurrent access, quotas and long-term storage.

Use a mailbox-oriented storage abstraction. It should support stable message identity, mailbox membership, folder membership, flags, append, copy, move, expunge and change tracking. Choose the storage backend only after a prototype proves compatibility with the selected IMAP implementation.

JMAP

Keep JMAP as a separately evaluated capability. The audit identifies IMAP/POP and webmail under INF-003; JMAP appears in the original exclusion discussion, not as a verified requirement in the frozen 199-feature inventory. Do not silently treat it as already researched or required by the original competitor evidence.&#x20;

05-implementation-roadmap.md

01-oss-feature-inventory.md



Spam filtering

The existing security engine is a verified strength, but it is not a replacement for a mail-content spam filter. Preserve the current layered security engine and add a separate content-filtering boundary. The two systems have different responsibilities.&#x20;

06-architecture-impact.md



## 5. New domain boundaries

The following packages or services should be introduced as architectural boundaries. Exact names can be adapted after the current repository is inspected.

| Domain        | Core responsibilities                                             |
| ------------- | ----------------------------------------------------------------- |
| `mailserver`  | Mail-server lifecycle, server configuration and orchestration     |
| `mta`         | Outbound transport adapter, delivery status and queue integration |
| `inbound`     | Inbound SMTP acceptance, validation and routing                   |
| `mailbox`     | Mailbox, folder, quota and message metadata                       |
| `mailstore`   | Message body and MIME storage abstraction                         |
| `imap`        | IMAP service integration and mailbox synchronization              |
| `pop3`        | POP3 service integration                                          |
| `mailrouting` | Local delivery, aliases, forwarding and routing rules             |
| `reputation`  | IP reputation, warm-up, throttling and abuse signals              |
| `dnsmanager`  | Domain verification, MX, SPF, DKIM, DMARC and PTR guidance        |
| `spamfilter`  | Spam scoring, quarantine and filtering integration                |
| `malware`     | Attachment scanning and scanning policy                           |
| `webmail`     | User-facing mailbox application integration                       |

These are logical ownership boundaries. They do not imply thirteen microservices. Start with a small number of independently deployable protocol components, while keeping the control plane modular.

### Data ownership

A clean ownership model is more important than the number of services:

- Control plane: tenant, domain, mailbox configuration, credential metadata, policy and service configuration.
- MTA: transport queue, remote delivery attempts and transport-level status.
- Mailbox store: message content, mailbox membership, folder state and IMAP synchronization metadata.
- Filtering: spam and malware verdicts, quarantine decisions and scan records.
- Event layer: durable lifecycle events consumed by analytics, webhooks and the dashboard.

Every cross-component operation should have an explicit identifier and correlation strategy. A message should be traceable from API acceptance or SMTP submission through transport, inbound routing or mailbox delivery.

## 6. Delivery and inbound flows

### Outbound delivery

\#chatgpt-mermaid-\_r_p1\_{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;fill:rgb(237, 237, 237);}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#chatgpt-mermaid-\_r_p1\_ .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_p1\_ .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_p1\_ .error-icon{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_p1\_ .error-text{fill:rgb(237, 237, 237);stroke:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ .edge-thickness-normal{stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ .edge-thickness-thick{stroke-width:3.5px;}#chatgpt-mermaid-\_r_p1\_ .edge-pattern-solid{stroke-dasharray:0;}#chatgpt-mermaid-\_r_p1\_ .edge-thickness-invisible{stroke-width:0;fill:none;}#chatgpt-mermaid-\_r_p1\_ .edge-pattern-dashed{stroke-dasharray:3;}#chatgpt-mermaid-\_r_p1\_ .edge-pattern-dotted{stroke-dasharray:2;}#chatgpt-mermaid-\_r_p1\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p1\_ .marker.cross{stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p1\_ svg{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;}#chatgpt-mermaid-\_r_p1\_ p{margin:0;}#chatgpt-mermaid-\_r_p1\_ .label{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ .cluster-label text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ .cluster-label span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ .cluster-label span p{background-color:transparent;}#chatgpt-mermaid-\_r_p1\_ .label text,#chatgpt-mermaid-\_r_p1\_ span{fill:rgb(237, 237, 237);color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ .node rect,#chatgpt-mermaid-\_r_p1\_ .node circle,#chatgpt-mermaid-\_r_p1\_ .node ellipse,#chatgpt-mermaid-\_r_p1\_ .node polygon,#chatgpt-mermaid-\_r_p1\_ .node path{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ .rough-node .label text,#chatgpt-mermaid-\_r_p1\_ .node .label text,#chatgpt-mermaid-\_r_p1\_ .image-shape .label,#chatgpt-mermaid-\_r_p1\_ .icon-shape .label{text-anchor:middle;}#chatgpt-mermaid-\_r_p1\_ .node .katex path{fill:#000;stroke:#000;stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ .rough-node .label,#chatgpt-mermaid-\_r_p1\_ .node .label,#chatgpt-mermaid-\_r_p1\_ .image-shape .label,#chatgpt-mermaid-\_r_p1\_ .icon-shape .label{text-align:center;}#chatgpt-mermaid-\_r_p1\_ .node.clickable{cursor:pointer;}#chatgpt-mermaid-\_r_p1\_ .root .anchor path{fill:rgb(175, 175, 175)!important;stroke-width:0;stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p1\_ .arrowheadPath{fill:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p1\_ .edgePath .path{stroke:rgb(175, 175, 175);stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ .flowchart-link{stroke:rgb(175, 175, 175);fill:none;}#chatgpt-mermaid-\_r_p1\_ .edgeLabel{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_p1\_ .edgeLabel p{background-color:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_p1\_ .edgeLabel rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_p1\_ .labelBkg{background-color:rgba(0, 0, 0, 0.5);}#chatgpt-mermaid-\_r_p1\_ .cluster rect{fill:rgb(48, 48, 48);stroke:rgba(255, 255, 255, 0.15);stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ .cluster text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ .cluster span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ div.mermaidTooltip{position:absolute;text-align:center;max-width:200px;padding:2px;font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:12px;background:rgb(48, 48, 48);border:1px solid rgba(255, 255, 255, 0.15);border-radius:2px;pointer-events:none;z-index:100;}#chatgpt-mermaid-\_r_p1\_ .flowchartTitleText{text-anchor:middle;font-size:18px;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p1\_ rect.text{fill:none;stroke-width:0;}#chatgpt-mermaid-\_r_p1\_ .icon-shape,#chatgpt-mermaid-\_r_p1\_ .image-shape{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_p1\_ .icon-shape p,#chatgpt-mermaid-\_r_p1\_ .image-shape p{background-color:rgb(0, 0, 0);padding:2px;}#chatgpt-mermaid-\_r_p1\_ .icon-shape .label rect,#chatgpt-mermaid-\_r_p1\_ .image-shape .label rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_p1\_ .label-icon{display:inline-block;height:1em;overflow:visible;vertical-align:-0.125em;}#chatgpt-mermaid-\_r_p1\_ .node .label-icon path{fill:currentColor;stroke:revert;stroke-width:revert;}#chatgpt-mermaid-\_r_p1\_ .node .neo-node{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].node rect,#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].cluster rect,#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].node polygon{stroke:url(#chatgpt-mermaid-\_r_p1\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].swimlane.cluster rect{filter:none;}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].node path{stroke:url(#chatgpt-mermaid-\_r_p1\_-gradient);stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].node .outer-path{filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].node .neo-line path{stroke:rgb(31, 78, 148);filter:none;}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].node circle{stroke:url(#chatgpt-mermaid-\_r_p1\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].node circle .state-start{fill:#000000;}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].icon-shape .icon{fill:url(#chatgpt-mermaid-\_r_p1\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p1\_ [data-look="neo"].icon-shape .icon-neo path{stroke:url(#chatgpt-mermaid-\_r_p1\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p1\_ .node text{font-size:14px;font-weight:600;letter-spacing:normal;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_p1\_ .edgeLabels text{font-size:13px;font-weight:600;letter-spacing:-0.08px;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_p1\_ .node tspan[font-weight="normal"],#chatgpt-mermaid-\_r_p1\_ .edgeLabels tspan[font-weight="normal"]{font-weight:600;}#chatgpt-mermaid-\_r_p1\_ .edgeLabel .label rect{opacity:1;rx:13px;ry:13px;fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ .node rect,#chatgpt-mermaid-\_r_p1\_ .node circle,#chatgpt-mermaid-\_r_p1\_ .node ellipse,#chatgpt-mermaid-\_r_p1\_ .node polygon,#chatgpt-mermaid-\_r_p1\_ .node path{fill:rgb(0, 40, 77);stroke:rgba(255, 255, 255, 0.1);stroke-width:1px;}#chatgpt-mermaid-\_r_p1\_ .node rect{rx:16px;ry:16px;}#chatgpt-mermaid-\_r_p1\_ .mermaid-decision .label-container{fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-dasharray:2,2;}#chatgpt-mermaid-\_r_p1\_ .edgePaths .flowchart-link{stroke:rgb(175, 175, 175);stroke-width:1px;stroke-linecap:round;stroke-linejoin:round;}#chatgpt-mermaid-\_r_p1\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p1\_ :root{--mermaid-font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";}REST API / SMTP Submission /CampaignReForwarder validation andpolicyTenant, domain, quota andsuppression checksDurable outbound messagerecordMTA adapterOutbound MTA queueIP pool and routing policyRemote MX via SMTPDelivery / deferral / bounceTransport event ingestionReForwarder event pipelineAnalytics, webhooks andsuppression

Requirements:

- Preserve the existing API and SMTP submission paths.
- Introduce a stable message identifier across all systems.
- Distinguish API acceptance, MTA queue acceptance, remote SMTP acceptance and final delivery evidence.
- Support idempotency, duplicate protection and durable retry.
- Add per-domain and per-IP rate limits.
- Provide held queues and manual release for operational intervention.
- Ensure campaign traffic cannot starve transactional traffic.
- Record the MTA's actual response and delivery status rather than presenting queue acceptance as successful delivery.

### Inbound mail and mailbox delivery

\#chatgpt-mermaid-\_r_p8\_{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;fill:rgb(237, 237, 237);}@keyframes edge-animation-frame{from{stroke-dashoffset:0;}}@keyframes dash{to{stroke-dashoffset:0;}}#chatgpt-mermaid-\_r_p8\_ .edge-animation-slow{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 50s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_p8\_ .edge-animation-fast{stroke-dasharray:9,5!important;stroke-dashoffset:900;animation:dash 20s linear infinite;stroke-linecap:round;}#chatgpt-mermaid-\_r_p8\_ .error-icon{fill:rgb(48, 48, 48);}#chatgpt-mermaid-\_r_p8\_ .error-text{fill:rgb(237, 237, 237);stroke:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ .edge-thickness-normal{stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ .edge-thickness-thick{stroke-width:3.5px;}#chatgpt-mermaid-\_r_p8\_ .edge-pattern-solid{stroke-dasharray:0;}#chatgpt-mermaid-\_r_p8\_ .edge-thickness-invisible{stroke-width:0;fill:none;}#chatgpt-mermaid-\_r_p8\_ .edge-pattern-dashed{stroke-dasharray:3;}#chatgpt-mermaid-\_r_p8\_ .edge-pattern-dotted{stroke-dasharray:2;}#chatgpt-mermaid-\_r_p8\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p8\_ .marker.cross{stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p8\_ svg{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:16px;}#chatgpt-mermaid-\_r_p8\_ p{margin:0;}#chatgpt-mermaid-\_r_p8\_ .label{font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ .cluster-label text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ .cluster-label span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ .cluster-label span p{background-color:transparent;}#chatgpt-mermaid-\_r_p8\_ .label text,#chatgpt-mermaid-\_r_p8\_ span{fill:rgb(237, 237, 237);color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ .node rect,#chatgpt-mermaid-\_r_p8\_ .node circle,#chatgpt-mermaid-\_r_p8\_ .node ellipse,#chatgpt-mermaid-\_r_p8\_ .node polygon,#chatgpt-mermaid-\_r_p8\_ .node path{fill:rgb(9, 23, 44);stroke:rgb(31, 78, 148);stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ .rough-node .label text,#chatgpt-mermaid-\_r_p8\_ .node .label text,#chatgpt-mermaid-\_r_p8\_ .image-shape .label,#chatgpt-mermaid-\_r_p8\_ .icon-shape .label{text-anchor:middle;}#chatgpt-mermaid-\_r_p8\_ .node .katex path{fill:#000;stroke:#000;stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ .rough-node .label,#chatgpt-mermaid-\_r_p8\_ .node .label,#chatgpt-mermaid-\_r_p8\_ .image-shape .label,#chatgpt-mermaid-\_r_p8\_ .icon-shape .label{text-align:center;}#chatgpt-mermaid-\_r_p8\_ .node.clickable{cursor:pointer;}#chatgpt-mermaid-\_r_p8\_ .root .anchor path{fill:rgb(175, 175, 175)!important;stroke-width:0;stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p8\_ .arrowheadPath{fill:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p8\_ .edgePath .path{stroke:rgb(175, 175, 175);stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ .flowchart-link{stroke:rgb(175, 175, 175);fill:none;}#chatgpt-mermaid-\_r_p8\_ .edgeLabel{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_p8\_ .edgeLabel p{background-color:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_p8\_ .edgeLabel rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_p8\_ .labelBkg{background-color:rgba(0, 0, 0, 0.5);}#chatgpt-mermaid-\_r_p8\_ .cluster rect{fill:rgb(48, 48, 48);stroke:rgba(255, 255, 255, 0.15);stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ .cluster text{fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ .cluster span{color:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ div.mermaidTooltip{position:absolute;text-align:center;max-width:200px;padding:2px;font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";font-size:12px;background:rgb(48, 48, 48);border:1px solid rgba(255, 255, 255, 0.15);border-radius:2px;pointer-events:none;z-index:100;}#chatgpt-mermaid-\_r_p8\_ .flowchartTitleText{text-anchor:middle;font-size:18px;fill:rgb(237, 237, 237);}#chatgpt-mermaid-\_r_p8\_ rect.text{fill:none;stroke-width:0;}#chatgpt-mermaid-\_r_p8\_ .icon-shape,#chatgpt-mermaid-\_r_p8\_ .image-shape{background-color:rgb(0, 0, 0);text-align:center;}#chatgpt-mermaid-\_r_p8\_ .icon-shape p,#chatgpt-mermaid-\_r_p8\_ .image-shape p{background-color:rgb(0, 0, 0);padding:2px;}#chatgpt-mermaid-\_r_p8\_ .icon-shape .label rect,#chatgpt-mermaid-\_r_p8\_ .image-shape .label rect{opacity:0.5;background-color:rgb(0, 0, 0);fill:rgb(0, 0, 0);}#chatgpt-mermaid-\_r_p8\_ .label-icon{display:inline-block;height:1em;overflow:visible;vertical-align:-0.125em;}#chatgpt-mermaid-\_r_p8\_ .node .label-icon path{fill:currentColor;stroke:revert;stroke-width:revert;}#chatgpt-mermaid-\_r_p8\_ .node .neo-node{stroke:rgb(31, 78, 148);}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].node rect,#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].cluster rect,#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].node polygon{stroke:url(#chatgpt-mermaid-\_r_p8\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].swimlane.cluster rect{filter:none;}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].node path{stroke:url(#chatgpt-mermaid-\_r_p8\_-gradient);stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].node .outer-path{filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].node .neo-line path{stroke:rgb(31, 78, 148);filter:none;}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].node circle{stroke:url(#chatgpt-mermaid-\_r_p8\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].node circle .state-start{fill:#000000;}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].icon-shape .icon{fill:url(#chatgpt-mermaid-\_r_p8\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p8\_ [data-look="neo"].icon-shape .icon-neo path{stroke:url(#chatgpt-mermaid-\_r_p8\_-gradient);filter:drop-shadow( 1px 2px 2px rgba(185,185,185,1));}#chatgpt-mermaid-\_r_p8\_ .node text{font-size:14px;font-weight:600;letter-spacing:normal;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_p8\_ .edgeLabels text{font-size:13px;font-weight:600;letter-spacing:-0.08px;fill:rgb(153, 206, 255);}#chatgpt-mermaid-\_r_p8\_ .node tspan[font-weight="normal"],#chatgpt-mermaid-\_r_p8\_ .edgeLabels tspan[font-weight="normal"]{font-weight:600;}#chatgpt-mermaid-\_r_p8\_ .edgeLabel .label rect{opacity:1;rx:13px;ry:13px;fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ .node rect,#chatgpt-mermaid-\_r_p8\_ .node circle,#chatgpt-mermaid-\_r_p8\_ .node ellipse,#chatgpt-mermaid-\_r_p8\_ .node polygon,#chatgpt-mermaid-\_r_p8\_ .node path{fill:rgb(0, 40, 77);stroke:rgba(255, 255, 255, 0.1);stroke-width:1px;}#chatgpt-mermaid-\_r_p8\_ .node rect{rx:16px;ry:16px;}#chatgpt-mermaid-\_r_p8\_ .mermaid-decision .label-container{fill:rgb(0, 14, 26);stroke:rgb(26, 62, 95);stroke-dasharray:2,2;}#chatgpt-mermaid-\_r_p8\_ .edgePaths .flowchart-link{stroke:rgb(175, 175, 175);stroke-width:1px;stroke-linecap:round;stroke-linejoin:round;}#chatgpt-mermaid-\_r_p8\_ .marker{fill:rgb(175, 175, 175);stroke:rgb(175, 175, 175);}#chatgpt-mermaid-\_r_p8\_ :root{--mermaid-font-family:-apple-system-body,ui-sans-serif,-apple-system,system-ui,"Segoe UI",Helvetica,"Apple Color Emoji",Arial,sans-serif,"Segoe UI Emoji","Segoe UI Symbol";}Remote senderPublic MX / Inbound SMTPSMTP validation and recipientlookupSpam and malware scanningRecipient policyMailbox deliveryRouting engineWebhook / application routeSMTP rejection / quarantineMailbox message storeIMAP / POP3WebmailMailbox eventsLocal mailboxAlias / forwardInbound webhookRejected

Requirements:

- Validate recipients before accepting responsibility for delivery.
- Handle unknown users, aliases, catch-all policy and forwarding explicitly.
- Make inbound delivery idempotent.
- Avoid accepting a message and then silently losing it.
- Support quarantine and operator review.
- Enforce per-mailbox and per-domain quotas.
- Protect forwarding against loops and abuse.
- Keep mailbox delivery separate from marketing subscriber state.

## 7. Phased architecture-first implementation programme

Do not start by implementing IMAP or building a webmail UI. First establish whether the target data plane, storage and operational model work.

1. Phase 0 — Repository re-audit and architecture decision record

   Must happen first
   - Inspect the current branch, working tree and latest commit.
   - Reconcile the seven audit documents with the actual current code.
   - Verify the existing SMTP relay, inbound receiver, queue, DKIM, provider-event ingestion and webhook behavior.
   - Record current database migrations, message models, event contracts and tenant boundaries.
   - Identify what can be reused and what must be isolated.
   - Write architecture decision records (ADRs) for MTA, mailbox storage, IMAP/POP, spam filtering and deployment topology.
   Exit criteria: approved architecture, component selection criteria, protocol boundaries, data ownership and migration plan. No large feature implementation yet.
2. Phase 1 — Outbound MTA foundation
   - Build the MTA adapter and lifecycle manager.
   - Establish direct-to-MX delivery and upstream-relay modes.
   - Integrate outbound queue visibility, retry, hold and release.
   - Add delivery attempt records and durable transport events.
   - Establish IP binding, per-domain sending policy and rate limits.
   - Add TLS, DKIM signing, SPF/DMARC alignment checks and PTR guidance.
   - Build test harnesses for SMTP responses, transient failures, permanent failures and remote throttling.
   Exit criteria: controlled outbound delivery works with traceable, recoverable message state. No production direct-to-MX launch until operational readiness gates pass.
3. Phase 2 — Inbound mail and routing
   - Implement inbound SMTP service integration.
   - Add recipient and domain validation.
   - Add aliases, forwarding, routing rules and catch-all policy.
   - Add spam and malware filtering interfaces.
   - Add bounce and return-path processing.
   - Add quarantine and manual review.
   - Build inbound event and audit trails.
   Exit criteria: accepted inbound messages are durably routed, rejected or quarantined with observable outcomes.
4. Phase 3 — Mailbox and storage foundation
   - Choose and prototype the mailbox storage backend.
   - Implement mailbox, folder, quota and message metadata.
   - Define MIME storage, attachment storage and retention.
   - Implement append, fetch, copy, move, flags, expunge and search.
   - Establish concurrent-access and synchronization behavior.
   - Add backup, restore and mailbox migration tooling.
   Exit criteria: mailbox data survives process restarts, supports concurrent clients and can be backed up and restored without corruption.
5. Phase 4 — IMAP and POP3
   - Integrate IMAP against the selected mailbox backend.
   - Add authentication and authorization tied to ReForwarder identities.
   - Implement folder and message synchronization.
   - Add POP3 as a separate access mode with correct deletion semantics.
   - Test standard clients and protocol compatibility.
   - Add connection limits, TLS enforcement, rate limits and audit logs.
   Exit criteria: standard mail clients can securely use hosted mailboxes with consistent state.
6. Phase 5 — Webmail and mailbox administration
   - Build mailbox provisioning and lifecycle management.
   - Add inbox, folders, compose, reply, forwarding and attachments.
   - Add search, read/unread state and mailbox settings.
   - Add password changes, sessions and device management.
   - Add mailbox quotas and usage reporting.
   - Add administrator tools for mailbox suspension and recovery.
   Exit criteria: users can manage hosted email without requiring a third-party webmail deployment.
7. Phase 6 — Full platform convergence
   - Integrate mailboxes, campaigns, transactional sending and inbound routing into one consistent control plane.
   - Complete DNS automation and certificate lifecycle management.
   - Add IP pools, warm-up controls and reputation monitoring.
   - Complete event, analytics and webhook coverage.
   - Add deployment, upgrade, backup and disaster-recovery tooling.
   - Revisit JMAP and advanced integrations based on validated demand.
   Exit criteria: one supported self-hosted platform with coherent administration, documented deployment and end-to-end operational testing.

The original audit roadmap's P0 trust and correctness work should be retained and reconciled into this programme, not discarded. In particular, its findings on queue durability, cron leadership, authorization, delivery-signal integrity and the provider `Sender` seam remain relevant.&#x20;

05-implementation-roadmap.md

06-architecture-impact.md



## 8. Critical architecture and operational requirements

Full mail infrastructure introduces risks that the relay-only platform did not have to own. These must be designed before production features.

| Area             | Required protection                                                                          |
| ---------------- | -------------------------------------------------------------------------------------------- |
| IP reputation    | IP warm-up, throttling, reputation monitoring, abuse response and blacklist alerts           |
| DNS              | Correct MX, SPF, DKIM, DMARC, PTR and forward-confirmed reverse DNS guidance                 |
| SMTP security    | STARTTLS, authentication policy, rate limits, connection limits and relay restrictions       |
| Mailbox security | Secure credential storage, session controls, per-mailbox authorization and quota enforcement |
| Spam             | Spam scoring, greylisting policy, quarantine and suppression                                 |
| Malware          | Attachment scanning, safe file handling and quarantine                                       |
| Tenant isolation | Workspace isolation across SMTP, inbound routing, mailbox access, storage and events         |
| Data durability  | Message persistence, backup, restore, replication and recovery testing                       |
| Abuse prevention | Account limits, suspicious sending detection, compromised-account response and suspension    |
| Operations       | Queue visibility, delivery tracing, service health, logs, alerts and upgrade safety          |

Four non-negotiable design rules:

1. Never expose an open relay. Authenticated submission and local inbound delivery must be separate trust paths.
2. Never trust the client-supplied tenant ID or recipient domain as authorization. Resolve ownership from server-side records.
3. Never report successful delivery just because a message entered a queue.
4. Never accept inbound mail unless the system has a durable path to deliver, route, quarantine or reject it.

The audit already identifies strengths worth preserving: layered SSRF protection, clean-room security controls, tenant isolation, encrypted credentials, queue reconciliation, webhook attempt history, retention and health tooling. New protocol integrations must build on these rather than bypass them.&#x20;

06-architecture-impact.md



## 9. Migration and backward compatibility

The expansion must not break existing ReForwarder users.

- Existing REST endpoints and SDK contracts remain supported.
- Existing SMTP submission credentials should continue working through a compatibility layer.
- Existing campaign and transactional sending must continue functioning while the MTA adapter is introduced.
- Preserve the `{success, data}` response envelope and existing webhook event contracts.
- Keep existing HMAC tracking-token formats and per-message tracking behavior.
- Keep existing workspace isolation and RBAC rules.
- Introduce new schema through additive migrations wherever possible.
- Do not migrate existing transactional email records into mailbox storage automatically.
- Provide explicit, tested migration and rollback procedures for any storage transition.

These requirements follow the original architecture document's byte-compatibility and legacy-path preservation rules.&#x20;

06-architecture-impact.md



## 10. Recommended implementation order

## Architecture priority

1\. Re-audit and ADRs

First

2\. Outbound MTA foundation

3\. Inbound SMTP and routing

4\. Mailbox storage and lifecycle

5\. IMAP and POP3

6\. Webmail

7\. DNS, reputation and operational automation

8\. Platform convergence and advanced protocols

I would not start with webmail, a visual interface for Postfix, or a custom Go implementation of SMTP/IMAP. The hardest decisions are MTA ownership, message storage, protocol compatibility, delivery semantics and operational recovery. Getting those wrong creates expensive migrations later.

## 11. What the coding agent must do before implementation

I recommend giving Codex or your coding agent a dedicated architecture programme rather than asking it to immediately implement all of Option B.

The first task should produce:

1. `docs/architecture/OPTION-B-ARCHITECTURE.md` — target architecture and system boundaries.
2. `docs/architecture/ADRs/` — individual architecture decisions for MTA, mailbox storage, IMAP/POP, filtering and deployment.
3. `docs/architecture/COMPONENT-MATRIX.md` — component candidates, licenses, maturity, maintenance, integration costs and trade-offs.
4. `docs/architecture/DATA-OWNERSHIP.md` — source of truth for every important entity and data store.
5. `docs/architecture/MESSAGE-LIFECYCLE.md` — outbound, inbound, bounce, quarantine and mailbox-delivery flows.
6. `docs/architecture/MIGRATION-PLAN.md` — existing user compatibility, database migrations and rollback.
7. `docs/architecture/IMPLEMENTATION-ROADMAP.md` — dependency-aware implementation tickets and acceptance criteria.
8. `docs/architecture/RISK-REGISTER.md` — reliability, security, deliverability, operational and licensing risks.

The agent must not modify production code during this architecture-only stage. It should inspect the latest repository state and verify every existing audit claim before finalizing its design.

## 12. Master prompt for Codex

# ReForwarder — Option B: Complete Email Infrastructure Architecture Programme

## Objective

Transform ReForwarder from a relay-based email delivery and marketing platform into a complete, self-hosted email infrastructure platform.

The target product must support:

- Transactional email through REST API and SMTP.
- Direct-to-MX outbound email delivery.
- Upstream SMTP relay delivery.
- Inbound SMTP and MX reception.
- Domain-based mail routing.
- Hosted mailboxes and mailbox provisioning.
- IMAP and POP3 access.
- Webmail.
- Aliases, forwarding and catch-all policies.
- IP pools, sending policies and reputation management.
- DKIM, SPF, DMARC, MX and PTR support.
- Spam filtering and malware scanning.
- Delivery queues, retries, held messages and operational controls.
- Existing marketing capabilities: contacts, campaigns, automation, analytics, templates and webhooks.
- Complete self-hosted installation, backup, recovery and upgrade support.

ReForwarder must become a unified email infrastructure product, not merely a dashboard that launches independent mail software.

## Research sources

Use all seven uploaded audit documents as the historical research baseline:

1. README(4).md
2. 01-oss-feature-inventory.md
3. 02-competitor-comparison.md
4. 03-existing-feature-audit.md
5. 04-feature-gap-analysis.md
6. 05-implementation-roadmap.md
7. 06-architecture-impact.md

Read all seven documents before making architecture decisions.

The audit is dated 2026-11-05 and references a historical repository baseline. Its statuses must not be assumed to describe the current branch.

Preserve permanent registry IDs, GAP-01 through GAP-53, D1-D9 and R1-R16. Never renumber existing identifiers.

The original exclusion of full mail-server capabilities is superseded by the user's explicit Option B decision. Reassess the previously excluded features, including INF-003, direct-to-MX, IP pools, PTR guidance and bounce-mailbox/return-path processing.

Do not silently rewrite the historical audit documents.

## Mandatory Stage 0: Current repository verification

Before proposing implementation:

1. Inspect the repository root, active branch, HEAD, working tree and recent commits.
2. Identify all uncommitted and untracked changes. Do not discard, reset, overwrite or reformat them.
3. Inspect existing Go packages, Vue application, migrations, models, routes, workers, queues, cron jobs, configuration and tests.
4. Verify the current implementation of SMTP submission, outbound SMTP, inbound SMTP, DKIM, provider events, webhook processing, queue reconciliation, campaign scheduling and multi-tenancy.
5. Compare the actual code with all relevant audit claims.
6. Identify work already implemented after the audit baseline.
7. Produce an evidence-backed current-state matrix.

Do not use the historical registry as proof of current implementation.

## Mandatory architecture direction

Use a control-plane/data-plane architecture.

### Control plane

Preserve the existing Go application and Vue dashboard as the central control plane.

It owns:

- Workspaces and tenants.
- Users, authentication and RBAC.
- Domain registration and verification.
- Mailbox and alias configuration.
- API keys and SMTP credentials.
- Mail-server configuration.
- Quotas and policies.
- DNS and TLS lifecycle orchestration.
- Monitoring and administration.
- Existing campaigns, contacts, templates, automation, analytics and webhooks.

Preserve the existing modular-monolith architecture unless repository evidence establishes a compelling reason to change it.

Do not split existing application domains into microservices merely for architectural fashion.

### Data plane

Design independently manageable protocol and delivery components:

- Outbound MTA.
- Inbound SMTP receiver.
- SMTP submission.
- Mailbox storage.
- IMAP.
- POP3.
- Spam filtering.
- Malware scanning.
- Mail routing and local delivery.
- Webmail integration.

Use explicit APIs, stable identifiers, durable events and documented configuration contracts.

Protocol components must be independently restartable and observable.

### Technology selection

Do not automatically implement SMTP, IMAP or POP3 from scratch in Go.

Research mature protocol implementations and compare candidates based on:

- License compatibility.
- Security history.
- Active maintenance.
- Protocol compatibility.
- Mailbox storage integration.
- Extensibility.
- Multi-tenancy.
- Performance and resource consumption.
- Operational complexity.
- Backup and recovery.
- Configuration management.
- Upgrade and migration behavior.

Evaluate integration versus native implementation.

Do not adopt BillionMail blindly. Its audit profile includes maintenance and security concerns.

Use Postal as an infrastructure reference, not as a requirement to port its Rails implementation.

Respect all licenses. Do not copy AGPL or BSL source code into ReForwarder without an explicit legal and licensing review. Behavioral research and independently designed implementations are acceptable.

## Architecture domains to design

Create explicit ownership and contracts for:

1. `mailserver`
2. `mta`
3. `inbound`
4. `mailbox`
5. `mailstore`
6. `imap`
7. `pop3`
8. `mailrouting`
9. `reputation`
10. `dnsmanager`
11. `spamfilter`
12. `malware`
13. `webmail`

These are logical domains, not a requirement for thirteen separate processes.

For every domain specify:

- Responsibility.
- Public interfaces.
- Data ownership.
- Dependencies.
- Failure behavior.
- Security boundary.
- Observability.
- Configuration.
- Tests.
- Migration impact.

## Critical architecture decisions

Produce explicit ADRs for:

### ADR-001: Outbound MTA

Compare established MTA options and recommend an integration strategy.

Cover direct-to-MX and upstream relay modes, transport queues, retries, rate limits, IP pools, DKIM signing, TLS, remote-server deferrals, delivery events and operational administration.

### ADR-002: Inbound SMTP

Define SMTP acceptance, recipient validation, domain routing, aliases, forwarding, catch-all behavior, spam filtering, malware scanning, quarantine and durable local delivery.

### ADR-003: Mailbox storage

Compare mailbox storage implementations.

Do not reuse the existing transactional email model as the mailbox data model.

Define message identity, MIME storage, folders, flags, quotas, concurrent access, deletion, expunge, search, attachment handling, retention and backup.

### ADR-004: IMAP and POP3

Choose protocol implementations and define authentication, authorization, TLS, connection limits, mailbox synchronization and client compatibility.

### ADR-005: Webmail

Define frontend boundaries, authentication integration, mailbox APIs, compose/send behavior, attachments, folders, search and session management.

### ADR-006: Spam and malware

Define integration boundaries for content filtering, attachment scanning, quarantine, abuse detection and policy enforcement.

Do not confuse ReForwarder's existing network security engine with a content-based spam filter.

### ADR-007: DNS and deliverability

Define MX, SPF, DKIM, DMARC, PTR, forward-confirmed reverse DNS, TLS certificates, domain verification, IP warm-up and reputation monitoring.

### ADR-008: Data and event ownership

Define the source of truth for tenant configuration, message content, transport attempts, mailbox state, filtering results and delivery events.

### ADR-009: Deployment topology

Define supported single-node and future multi-node deployments, component health, restart behavior, configuration, upgrades, backups and disaster recovery.

## Required message lifecycle designs

Document complete sequence diagrams for:

1. API transactional send.
2. Authenticated SMTP submission.
3. Direct-to-MX outbound delivery.
4. Upstream relay delivery.
5. Remote delivery deferral and retry.
6. Hard bounce and soft bounce.
7. Complaint ingestion and suppression.
8. Inbound SMTP acceptance.
9. Inbound mailbox delivery.
10. Alias and forwarding.
11. Spam quarantine.
12. IMAP mailbox access.
13. POP3 mailbox access.
14. Mailbox deletion and retention.

Every message must have a stable correlation identity across components.

Distinguish API acceptance, queue acceptance, remote SMTP acceptance and verified delivery evidence.

Never report a queue acceptance as final delivery.

## Security requirements

Design explicit defenses for:

- Open relay prevention.
- SMTP authentication.
- STARTTLS and certificate validation.
- SMTP connection and command abuse.
- Brute-force protection.
- Per-IP and per-account rate limits.
- Tenant isolation.
- Mailbox authorization.
- Credential encryption.
- Spam and malware.
- Attachment abuse.
- Forwarding loops.
- DNS rebinding and SSRF in integrations.
- Compromised sending accounts.
- IP reputation and blacklist events.
- Abuse reporting and account suspension.

Preserve the existing SSRF guard, security engine, workspace isolation, encrypted credential service and authorization model.

New services must not bypass these guarantees.

## Backward compatibility

Preserve existing:

- REST API behavior.
- SDK contracts.
- SMTP submission credentials where possible.
- Workspace and RBAC semantics.
- `{success, data}` response envelope.
- Webhook event contracts.
- HMAC tracking token formats.
- Transactional sending.
- Campaign processing.
- Existing queue and retry behavior.

Use additive database migrations where practical.

Do not migrate transactional email records into mailbox storage without a separately justified migration design.

Document rollback procedures.

## Implementation phases

Create a dependency-aware roadmap with these stages:

0. Repository re-audit and architecture decisions.
1. Outbound MTA foundation.
2. Inbound SMTP and routing.
3. Mailbox storage and lifecycle.
4. IMAP and POP3.
5. Webmail and mailbox administration.
6. Full platform convergence, DNS automation, reputation and operational tooling.

Reconcile the original 52-item roadmap with these phases. Retain still-relevant P0 and P1 work. Do not discard original security, queue, authorization or reliability requirements.

Identify blockers, parallelizable work, migration dependencies and measurable acceptance criteria.

## Deliverables

Create:

- `docs/architecture/OPTION-B-ARCHITECTURE.md`
- `docs/architecture/ADRs/ADR-001-outbound-mta.md`
- `docs/architecture/ADRs/ADR-002-inbound-smtp.md`
- `docs/architecture/ADRs/ADR-003-mailbox-storage.md`
- `docs/architecture/ADRs/ADR-004-imap-pop3.md`
- `docs/architecture/ADRs/ADR-005-webmail.md`
- `docs/architecture/ADRs/ADR-006-spam-malware.md`
- `docs/architecture/ADRs/ADR-007-dns-deliverability.md`
- `docs/architecture/ADRs/ADR-008-data-events.md`
- `docs/architecture/ADRs/ADR-009-deployment.md`
- `docs/architecture/COMPONENT-MATRIX.md`
- `docs/architecture/DATA-OWNERSHIP.md`
- `docs/architecture/MESSAGE-LIFECYCLE.md`
- `docs/architecture/MIGRATION-PLAN.md`
- `docs/architecture/IMPLEMENTATION-ROADMAP.md`
- `docs/architecture/RISK-REGISTER.md`

## Strict execution rules

- This is an architecture-only task.
- Do not implement features.
- Do not alter production code.
- Do not run destructive commands.
- Do not remove existing features.
- Do not change database schemas.
- Do not introduce dependencies.
- Do not commit or push.
- Do not invent repository evidence.
- Cite exact source files and symbols for current-state claims.
- Clearly label facts, recommendations, assumptions and unresolved decisions.
- Record all uncertainties instead of silently choosing an answer.

## Completion report

At completion report:

1. Repository branch and HEAD inspected.
2. Existing working-tree changes preserved.
3. Audit claims confirmed, corrected or stale.
4. Recommended architecture.
5. Selected and rejected component candidates with reasons.
6. Important ADR decisions and unresolved questions.
7. Data ownership and message lifecycle.
8. Security and operational risks.
9. Migration and backward-compatibility strategy.
10. Implementation phases and dependencies.
11. Exact documentation files created.
12. Confirmation that no production code was changed.

Stop after the architecture deliverables. Wait for explicit approval before implementation.

## Final recommendation

Build the MTA and mailbox architecture first, not the webmail interface. The first engineering milestone should be a working prototype that proves outbound direct-to-MX delivery, inbound SMTP acceptance and durable mailbox storage can coexist behind ReForwarder's control plane.

Once those boundaries are proven, IMAP/POP and webmail become much safer implementation projects.

That gives ReForwarder a realistic path toward becoming a self-hosted alternative to both an email service provider and a hosted mail server—without sacrificing the transactional, campaign and developer-platform features already built.