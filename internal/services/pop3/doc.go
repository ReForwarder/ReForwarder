// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Posta.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pop3 is the OB-4 wave W3 POP3 server (ADR-004 decision 2, SF-86):
// the classic POP3 command set — USER/PASS, AUTH PLAIN (RFC 5034), CAPA,
// STAT, LIST, UIDL, RETR, TOP, DELE, RSET, NOOP, QUIT and STLS (RFC 2595) —
// served against the user's INBOX folder through the mailstore (ADR-003).
//
// # Auth
//
// Clients authenticate with the user's email address plus an APP PASSWORD via
// the W1 mailauth seam (ADR-004 decision 3). The primary account password is
// structurally never accepted — mailauth compares the presented secret only
// against app-password digests, and this server never touches the users
// table's password hash. A wrong secret, a revoked secret, a missing user and
// a missing mailbox are indistinguishable on the wire (-ERR [AUTH] invalid
// credentials); the audit sink records the distinguishing reason server-side.
//
// # Deletion semantics — the shadow view
//
// A POP3 session works on a SHADOW VIEW of the INBOX, snapshotted once at
// login: message numbers 1..n mapped to (mailstore row ID, UID, octets).
// POP3-correct deletion is mark-then-expunge (RFC 1939 §6):
//
//   - DELE marks the message deleted IN THE SHADOW ONLY — the mailstore is
//     untouched, so IMAP sessions and other POP3 sessions keep seeing the
//     message and their UIDs are never disturbed mid-session;
//   - RSET clears every in-session mark (pure shadow rollback);
//   - QUIT enters the UPDATE phase: each shadow-marked message is flagged
//     \Deleted through the mailstore's CAS flag store, then the mailstore
//     expunge sweep (ADR-004 decision 2: "expunge maps to mailstore flag
//     sweeps") removes the flagged rows transactionally. Surviving messages
//     keep their UIDs and uid_validity never changes (mailstore expunge
//     contract), so UIDL values remain stable across sessions;
//   - a session that drops without QUIT expunges NOTHING — marks live only
//     inside the session.
//
// The mailstore's expunge sweeps every \Deleted-flagged row of the folder,
// which matches IMAP EXPUNGE semantics (an IMAP client that itself set
// \Deleted consented to the removal; the mailstore expunge contract is
// covered by its own tests).
//
// # Listing semantics
//
// STAT/LIST/UIDL exclude shadow-deleted messages and report the snapshot's
// octet counts (the mailstore's stored plaintext size). Message bodies are
// served byte-exact from the content-addressed blobs (no reassembly), so a
// RETR's byte count equals the advertised octets. RETR and TOP have NO
// side effects on the store — POP3 never mutates flags mid-session (no \Seen
// write), keeping the shadow-view guarantee absolute; new mail delivered
// after the login snapshot is not visible to the session (RFC 1939 permits).
//
// # TLS and limits (non-negotiables)
//
// TLS is enforced by default: the default TLS mode is "starttls" (STLS
// required before any authentication; cleartext auth refused), and the
// config's only cleartext escape hatch additionally requires the listener to
// be bound to a loopback address (validated in internal/config). Port 995 is
// the implicit-TLS variant. Connections run through the security substrate:
// accept-level ban enforcement and connect/abort signals via
// security.CountingListener (wired by the caller), auth-failure detection via
// the security engine, a per-IP connect budget, a per-user concurrent-session
// cap, and a per-session command-rate limit. Every session publishes audit
// events (auth success/failure with reason, session end with the expunge
// tally) through the audit sink wired at startup.
package pop3
