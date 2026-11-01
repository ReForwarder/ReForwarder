# ReForwarder

<p align="center">
  <img src="web/public/logo.png" alt="ReForwarder" width="150" />
</p>

<p align="center">
Self-hosted email platform for sending, receiving, and relaying email through a unified API and SMTP.
</p>
<p align="center">
  <a href="#overview">Overview</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#core-features">Features</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#api-documentation">API</a> ·
  <a href="#dashboard">Dashboard</a> ·
  <a href="#sdks">SDKs</a> ·
  <a href="#license">License</a> ·
  <a href="#acknowledgments">Acknowledgments</a>
</p>

[![CI](https://github.com/ReForwarder/ReForwarder/actions/workflows/ci.yml/badge.svg)](https://github.com/ReForwarder/ReForwarder/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/ReForwarder/ReForwarder)](https://go.dev/)
[![GitHub Release](https://img.shields.io/github/v/release/ReForwarder/ReForwarder)](https://github.com/ReForwarder/ReForwarder/releases)

---

## Overview

**ReForwarder** is a self-hosted, developer-first email platform that handles **outbound**, **inbound**, and **relayed** email — driven by a single HTTP API, with SMTP available for the paths that need it.

- **Outbound** — send transactional and marketing email over your own SMTP, with templates, localization, campaigns, subscriber lists, bounce and complaint handling, and delivery analytics.
- **Inbound** — receive email at your domains, parse messages and attachments, and forward the structured payloads to your application via webhooks.
- **Relay** — point an existing SMTP client at ReForwarder and its mail flows through the same outbound pipeline as the API, so you can migrate to the HTTP API at your own pace.
- **Forms** — let a website contact form post straight to ReForwarder, scan each submission for spam, and reply to the sender from the dashboard through the same domains and suppression list as everything else.

It is designed as a fully self-hostable alternative to services like SendGrid, Mailgun, and Postmark — giving you complete ownership of your email infrastructure, data, and deliverability.

[![Website](https://img.shields.io/badge/Website-reforwarder.com-blue?style=flat-square)](https://reforwarder.com/)
[![Source Code](https://img.shields.io/badge/Source%20Code-GitHub-181717?style=flat-square&logo=github)](https://github.com/ReForwarder/ReForwarder)
[![License](https://img.shields.io/badge/License-AGPL--3.0--or--later-blue?style=flat-square)](#license)

---

## Quick Example

Send your first email:

```bash
curl -X POST http://localhost:9000/api/v1/emails/send \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "from": "Acme <hello@example.com>",
    "to": ["Jonas Kaninda <jonas@example.com>","bob@example.com"],
    "subject": "Hello from ReForwarder",
    "html": "<h1>Hello!</h1>"
  }'
```

> The `from`, `to`, and other address fields accept a plain address (`hello@example.com`)
> or RFC 5322 display-name format (`Acme <hello@example.com>`).

Response:

```json
{
  "id": "0ae4b04e-5c64-4b2f-bad6-460f8d5d98b3",
  "status": "queued"
}
```

Receive a website form submission — no JavaScript, no backend of your own:

```html
<form action="http://localhost:9000/api/v1/f/YOUR_FORM_KEY" method="POST">
  <input type="email" name="email" required>
  <textarea name="message" required></textarea>
  <button type="submit">Send</button>
</form>
```

The submission is scanned, stored, and shown in the dashboard, where a person
replies to the sender. There is no auto-responder.

---

## Core Features

### Email Delivery

* REST API for transactional, batch, and templated emails
* Attachments, custom headers, and unsubscribe support
* Web view ("view in browser") with signed, expiring links
* Scheduled sending and preview mode
* Email address verification (syntax, MX, disposable & role-account checks)
* Async processing with Redis and Asynq
* Automatic retries and priority queues

### Inbound Email

* Built-in SMTP receiver with TLS
* HTTP webhook ingest with HMAC verification
* Raw message, headers & attachment storage
* Forwarding with status tracking
* Spam scoring & retry on failure
* Real-time SSE stream for inbound notifications

### Web Form Messages

* Public ingest endpoint per form — `POST /api/v1/f/{key}`, no API key on the page
* Accepts plain HTML form posts, JSON, and multipart uploads; works without JavaScript
* Origin allowlist, honeypot field, single-use nonces, and per-IP, per-form, per-sender and per-workspace rate limits
* Spam scanning with a score, the reasons behind it, and an action ladder — allow, flag, quarantine, reject
* Workspace spam filters by keyword, phrase, regex, email, domain, or IP, with a dry-run before you commit
* Manual replies from the dashboard through the normal sending pipeline — no auto-responder
* Inbox states, assignment, attachments, and a live event stream
* Immediate, hourly, or daily digest notifications to the addresses each form nominates
* Off by default behind `POSTA_MESSAGES_ENABLED`

### Email Relay

* Point an existing SMTP client at ReForwarder — no code changes
* Relays into the same pipeline as the send API
* Domain verification, suppression & rate limits still apply
* Workspace-scoped credentials with IP allowlist
* Instant revocation, no redeploy
* Off by default — built for private networks

### Templates

* Versioned and multi-language templates
* Drag-and-drop visual email builder, or a code editor with syntax highlighting and a live preview
* Variable substitution and stylesheet inlining
* System variables for web view and one-click unsubscribe links
* Import/export, HTML file import, and desktop/mobile preview

### Campaigns

* Bulk email campaigns with scheduling and subscriber targeting
* Draft, scheduled, sending, paused, and cancelled lifecycle states
* A/B testing with multi-variant splits and per-variant performance metrics

### SMTP & Domains

* Multiple SMTP providers with TLS support
* Shared SMTP pools for teams
* Domain verification (SPF, DKIM, DMARC)
* Verified sender enforcement

### Security

* API keys with expiration, hashing, and IP allowlisting
* JWT authentication and RBAC
* Two-factor authentication (TOTP)
* OAuth / SSO login (Google, Keycloak, authentik, and more)
* Rate limiting and session management

### Contacts & Subscribers

* Auto-tracked contacts with per-recipient send/failure stats
* Subscriber management with static and dynamic (segmented) lists
* Bulk import via JSON or CSV with column mapping
* Subscriber lifecycle (active, bounced, unsubscribed)
* Hard/soft bounce and complaint handling with automatic suppression

### Unsubscribe Lists

* RFC 8058 one-click unsubscribe scoped per list
* ReForwarder-minted signed URLs and `List-Unsubscribe` header
* Scoped opt-outs so receipts and password resets keep flowing
* Management API and dashboard for CRUD and opt-out browsing
* `email.unsubscribed` webhook event
* Resubscribe individual addresses without lifting a global block

### Tracking

* Pixel-based open tracking
* Click tracking with link rewriting
* Per-email engagement metrics

### Workspaces

* Multi-tenant architecture with isolated workspaces
* Role-based access control (owner, admin, editor, viewer)
* Member invitations and scoped API keys
* Data export/import and GDPR contact/log deletion

### Webhooks & Events

* Event-driven architecture with webhook delivery
* Retry strategies and delivery tracking
* Audit logs and real-time event streaming

### Analytics & Monitoring

* Email delivery, open, and click metrics, compared against the preceding period
* Delivery and bounce trends, plus time-to-deliver percentiles
* Deliverability broken down by recipient mailbox provider — Gmail, Outlook, Yahoo, and the rest
* CSV export of any report
* Prometheus integration
* Health endpoints and daily reports

### Admin Platform

* User and API key management
* Platform dashboard, metrics, and logs
* Domains across every workspace, with a manual ownership override for the cases DNS cannot settle
* Announcements broadcast to every user's in-app inbox, retractable after the fact
* SMTP pool management
* Usage plans with quotas and per-workspace assignment
* OAuth/SSO provider configuration
* Platform configuration and retention policies

### Dashboard

* Vue-based UI for managing all resources
* Analytics, templates, SMTP, domains, contacts, campaigns, messages, and logs
* Per-user notification inbox with dismissible workspace health alerts
* Dark/light mode and user preferences

---

## Architecture

ReForwarder ships as a single binary that runs in two modes: `posta` serves the HTTP API,
dashboard, and SMTP listeners; `posta worker` consumes the job queue. Both modes are
stateless — all state lives in the shared PostgreSQL, Redis, and object storage — so you
can run as many workers as your send volume needs.

```mermaid
flowchart LR
    App["Your app<br/>REST API / SMTP client"]
    MX["Inbound mail<br/>MX delivery"]
    Form["Website form<br/>POST /api/v1/f/{key}"]

    subgraph app["ReForwarder"]
        Server["<b>posta</b><br/>HTTP API :9000 · Dashboard<br/>Inbound SMTP :2525 · Relay :2526"]
        W1["<b>worker 1</b><br/>asynq consumer"]
        W2["<b>worker 2</b><br/>asynq consumer"]
    end

    subgraph shared["Shared state"]
        Redis[("Redis<br/>Asynq queues<br/>+ scheduler")]
        PG[("PostgreSQL<br/>emails · templates<br/>contacts · logs")]
        Blob[("Object storage<br/>S3-compatible or filesystem<br/>attachments · raw inbound")]
    end

    Providers["SMTP providers<br/>outbound delivery"]
    Hooks["Your webhook<br/>endpoints"]

    App --> Server
    MX --> Server
    Form --> Server
    Server -- enqueue --> Redis
    Redis -- dequeue --> W1
    Redis -- dequeue --> W2
    Server <--> PG
    W1 <--> PG
    W2 <--> PG
    Server -.-> Blob
    W1 & W2 -.-> Blob
    W1 & W2 --> Providers
    W1 & W2 --> Hooks
```

Running the API without a dedicated worker works for evaluation, but production
deployments should split them so sending, retries, campaigns, and scheduled jobs scale
independently of request traffic — see
[`examples/docker-compose-full.yml`](examples/docker-compose-full.yml).

Object storage is optional (dashed above). With `POSTA_BLOB_PROVIDER` unset, attachments
and raw inbound messages stay in the database; point it at `s3` or `fs` to move them out.
Any multi-node deployment should use `s3` — a local `fs` path is not shared between the
server and its workers.

### Stack

* Backend: Go (Okapi framework)
* Frontend: Vue 3 + Vite
* Database: PostgreSQL
* Queue: Redis + Asynq
* Metrics: Prometheus

---

## Requirements

* Go 1.27+
* PostgreSQL
* Redis

---

## Quick Start

### Docker Compose

```bash
docker compose up -d
```

Access the dashboard:

```
http://localhost:9000
```

Default credentials:

```
Email: admin@example.com
Password: admin1234
```

---

### Local Development

```bash
git clone https://github.com/ReForwarder/ReForwarder.git
cd ReForwarder

make dev-deps
make dev
make dev-worker
```

---

## API Documentation

Every ReForwarder instance serves its own interactive API documentation:

* API Reference: `/docs` on your ReForwarder instance
* OpenAPI Spec: `/openapi.json` on your ReForwarder instance

Running locally with the default port, that is [http://localhost:9000/docs](http://localhost:9000/docs).
---

# Dashboard

ReForwarder includes a web dashboard for managing templates, SMTP servers, domains, contacts, API keys, and analytics.

<p align="center">
  <img src="docs/static/img/screenshots/dashboard.png" alt="ReForwarder Dashboard" width="900"/>
</p>

### Email Analytics

<p align="center">
  <img src="docs/static/img/screenshots/analytics.png" alt="Email Analytics" width="900"/>
</p>

### Emails

<p align="center">
  <img src="docs/static/img/screenshots/emails.png" alt="Emails" width="900"/>
</p>

### Admin Platform Metrics

<p align="center">
  <img src="docs/static/img/screenshots/admin-platform-metrics.png" alt="Admin Platform Metrics" width="900"/>
</p>

---

## SDKs

The official Posta SDKs are compatible with ReForwarder's API:

* Go: [https://github.com/goposta/posta-go](https://github.com/goposta/posta-go)
* PHP: [https://github.com/goposta/posta-php](https://github.com/goposta/posta-php)
* Java: [https://github.com/goposta/posta-java](https://github.com/goposta/posta-java)
* Node: [https://github.com/goposta/posta-node](https://github.com/goposta/posta-node)
* .NET: [https://github.com/goposta/posta-node](https://github.com/goposta/posta-node)

### Go Example

```go
package main

import (
    "fmt"
    "log"

    posta "github.com/goposta/posta-go"
)

func main() {
    client := posta.New("https://reforwarder.example.com", "your-api-key")

    resp, err := client.Emails.Send(&posta.SendEmailRequest{
        From:    "sender@example.com",
        To:      []string{"recipient@example.com"},
        Subject: "Hello from ReForwarder",
        HTML:    "<h1>Hello!</h1><p>This is a test email.</p>",
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Email sent: id=%s status=%s\n", resp.ID, resp.Status)
}
```

---

## Contributing

Contributions are welcome. Please open an issue before submitting a pull request.

---

## License

ReForwarder is free and open-source software licensed under the
GNU Affero General Public License v3.0 or later ([AGPL-3.0-or-later](LICENSE)).

## Copyright

Copyright (c) 2026 Jonas Kaninda and contributors — the original [Posta](https://github.com/goposta/posta) project.
ReForwarder changes and additions by the ReForwarder contributors.

---

## Acknowledgments

ReForwarder is a rebranded distribution built on top of the excellent work of the
Posta project and its community.

* Upstream project: [goposta/posta](https://github.com/goposta/posta)
* Author: [Jonas Kaninda](https://github.com/jkaninda)
* Support upstream: [Ko-fi](https://ko-fi.com/jkaninda)

If ReForwarder is useful to you, starring both the
[ReForwarder repository](https://github.com/ReForwarder/ReForwarder) and the
[upstream Posta repository](https://github.com/goposta/posta) is the best way to
say thanks.
