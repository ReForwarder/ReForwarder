---
sidebar_position: 7
title: Mail Protocols (IMAP & POP3)
description: Read your hosted mailbox with standard IMAP and POP3 clients over TLS, authenticated with app passwords
---

# Mail Protocols (IMAP & POP3)

ReForwarder can serve your hosted mailboxes to standard mail clients: **IMAP4rev1** (port 143 with STARTTLS, 993 with implicit TLS) and **POP3** (port 110 with STLS, 995 with implicit TLS). Both protocols read the same encrypted mailstore the dashboard and the REST API use — messages delivered through inbound routing rules land in the mailbox's INBOX and appear in every connected client.

Both listeners are **off by default**. When enabled, they follow four non-negotiable rules:

1. **App passwords only.** The mail ports never accept your primary (dashboard) account password — that keeps your WebAuthn/2FA posture intact. Mail clients authenticate with a dedicated app password, which you can revoke at any time without touching your dashboard login.
2. **TLS enforced.** A plaintext listener is only permitted on a loopback bind (tests and local development). Any public bind requires the TLS certificate files, and the implicit-TLS ports (993/995) always serve TLS.
3. **Connection limits on every session.** Concurrent sessions per user, concurrent connections per client IP, and a per-session command budget are all mandatory (defaults below; the server refuses to start with them disabled).
4. **Everything is audited.** Successful and failed logins, and the mailbox actions (SELECT, APPEND, STORE, EXPUNGE) land in the audit log with the user and client IP — visible under **Webhooks & Events → Audit Log**.

## Ports

| Protocol | Plaintext + STARTTLS | Implicit TLS | Notes |
|----------|----------------------|--------------|-------|
| IMAP4rev1 | 143 (`POSTA_IMAP_PORT`) | 993 (`POSTA_IMAP_TLS_PORT`) | Authentication requires STARTTLS first (or port 993). |
| POP3 | 110 (`POSTA_POP3_PORT`) | 995 (`POSTA_POP3_TLS_PORT`) | POP3 needs STLS before `USER`/`PASS` (or port 995). |

Set a port to `0` to disable that listener while keeping the protocol enabled on its other port.

## App-password setup

1. Sign in to the dashboard and open **Profile → App Passwords**.
2. Create a new app password. It is shown once — store it in your mail client.
3. Configure your mail client with your **account email + the app password** (client settings below).
4. Revoke the password from the same page when a device is lost or retired; the mail sessions using it stop authenticating immediately.

A wrong password, a revoked password, and an unknown account all fail identically (`AUTHENTICATIONFAILED` on IMAP, `-ERR` on POP3) so the mail ports cannot be used to enumerate identities. Failed attempts feed the abuse detection engine (the same one protecting the dashboard and the SMTP listeners), so brute-force attempts lead to automatic IP bans.

## Client settings

| Setting | IMAP | POP3 |
|---------|------|------|
| Server | your ReForwarder host | your ReForwarder host |
| Port | **993** (implicit TLS) or 143 + STARTTLS | **995** (implicit TLS) or 110 + STLS |
| Security | SSL/TLS (or STARTTLS on 143) | SSL/TLS (or STLS on 110) |
| Username | your account email | your account email |
| Password | an app password | an app password |

For IMAP clients that keep many devices connected (Thunderbird, Apple Mail, Outlook), the per-user session cap applies across all of them — the default of 10 concurrent sessions covers typical multi-device setups. Raise it with `POSTA_IMAP_MAX_SESSIONS_PER_USER` if you use more devices.

## Configuration reference

| Variable | Default | Description |
|----------|---------|--------------|
| `POSTA_IMAP_ENABLED` | `false` | Master switch for the IMAP listeners. |
| `POSTA_IMAP_HOST` | `0.0.0.0` | Bind address for both IMAP listeners. |
| `POSTA_IMAP_PORT` | `143` | Plaintext listener (STARTTLS before auth when a certificate is set). `0` disables it. |
| `POSTA_IMAP_TLS_PORT` | `993` | Implicit-TLS listener. `0` disables it. |
| `POSTA_IMAP_HOSTNAME` | `posta.local` | Hostname advertised in the IMAP greeting and used as the certificate's server name. |
| `POSTA_IMAP_TLS_CERT_FILE` / `POSTA_IMAP_TLS_KEY_FILE` | — | Certificate pair. Required for port 993 and for any non-loopback 143 bind. |
| `POSTA_IMAP_MAX_SESSIONS_PER_USER` | `10` | Concurrent authenticated IMAP sessions per user (mandatory). |
| `POSTA_IMAP_MAX_CONNS_PER_IP` | `30` | Concurrent IMAP connections per client IP, both listeners (mandatory). |
| `POSTA_IMAP_COMMAND_RATE_LIMIT` / `POSTA_IMAP_COMMAND_RATE_WINDOW` | `600` / `60` | Commands per session per window, in seconds (mandatory). A session that exceeds the budget is disconnected. |
| `POSTA_POP3_ENABLED` | `false` | Master switch for the POP3 listeners. |
| `POSTA_POP3_HOST` | `0.0.0.0` | Bind address for both POP3 listeners. |
| `POSTA_POP3_PORT` | `110` | Plaintext listener (STLS before auth when a certificate is set). `0` disables it. |
| `POSTA_POP3_TLS_PORT` | `995` | Implicit-TLS listener. `0` disables it. |
| `POSTA_POP3_HOSTNAME` | `posta.local` | Hostname advertised in the POP3 greeting. |
| `POSTA_POP3_TLS_CERT_FILE` / `POSTA_POP3_TLS_KEY_FILE` | — | Certificate pair. Required for port 995 and for any non-loopback 110 bind. |
| `POSTA_POP3_MAX_SESSIONS_PER_USER` | `10` | Concurrent authenticated POP3 sessions per user (mandatory). |
| `POSTA_POP3_MAX_CONNS_PER_IP` | `30` | Concurrent POP3 connections per client IP (mandatory). |
| `POSTA_POP3_COMMAND_RATE_LIMIT` / `POSTA_POP3_COMMAND_RATE_WINDOW` | `600` / `60` | Commands per session per window (mandatory). |

The four ports may not collide with each other or with the other listeners (dashboard, inbound SMTP, relay, submission) — the server refuses to start on a collision.

## What the mail ports can and cannot do

**IMAP** supports the full command set a desktop or mobile client needs: mailbox management (CREATE/DELETE/RENAME/SUBSCRIBE/UNSUBSCRIBE, LIST, STATUS), message access (SELECT/EXAMINE, FETCH with body-structure and section fetches, SEARCH over the standard keys, STORE with flag changes, COPY/MOVE), APPEND for client-side upload, IDLE for push updates, and UID variants of the access commands. IMAP4rev2 extensions are not adopted in this phase.

**POP3** serves the inbox folder with the classic command set (USER/PASS/AUTH, STAT, LIST, UIDL, RETR, TOP, DELE, RSET, NOOP, QUIT, CAPA, STLS). Deletion follows POP3 semantics: messages are marked for deletion and expunged when the session ends with QUIT; this does not disturb IMAP UID assignments.

Mail-protocol delivery is the same storage path as everything else: message bodies are content-addressed and encrypted at rest, per-mailbox quotas apply to APPENDs from mail clients, and every session's activity is auditable.
