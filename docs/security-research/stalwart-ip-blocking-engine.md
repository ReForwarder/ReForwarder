# Report C — The Stalwart IP Blocking Engine: How IPs Are Tracked, Banned, Enforced, and Unblocked

| | |
|---|---|
| **Subject repository** | Stalwart Mail Server (`stalwartlabs/stalwart`), clone at `C:\Users\Ibrahim\.zcode\tmp\stalwart-analysis\stalwart` |
| **Commit analyzed** | `23b69755cb3cd8ecd952fe0443c5ac067a506184` (tag `v1.0.1`, HEAD of `main`) |
| **Date** | 2026-11-01 |
| **Method** | Static source analysis of the pinned clone: full reads of `crates/common/src/network/security.rs`, `crates/common/src/network/listen.rs`, `crates/common/src/auth/authentication.rs`, `crates/common/src/cache/{reload,invalidate}.rs`, `crates/jmap/src/registry/{set.rs, mapping/action.rs}`, `crates/services/src/broadcast/*`, `crates/store/src/{dispatch/lookup.rs, registry/*, backend/{local,redis}/*}`, plus targeted greps for negative claims (no unban function, no CLI, no BlockedIp invalidation case). Every file:line citation in this report was re-verified against the pinned commit at write time; zero discrepancies were found. |
| **Report series** | Report C of the ReForwarder security-research series (companion to Reports A, B, and F) |

## Table of Contents

1. [The Two Lists: BlockedIp and AllowedIp](#1-the-two-lists-blockedip-and-allowedip)
2. [The Ban Flow (Automatic)](#2-the-ban-flow-automatic)
3. [Ban Periods: Permanent by Default](#3-ban-periods-permanent-by-default)
4. [Enforcement Points](#4-enforcement-points)
5. [The Allowlist: How It Overrides Everything](#5-the-allowlist-how-it-overrides-everything)
6. [Unblocking and Propagation (The Footgun)](#6-unblocking-and-propagation-the-footgun)
7. [Reset and Maintenance](#7-reset-and-maintenance)
8. [Cluster Behavior and Restart](#8-cluster-behavior-and-restart)
9. [Storage Engineering (Brief)](#9-storage-engineering-brief)
10. [Summary Table, Confidence, and Verified-Absent List](#10-summary-table-confidence-and-verified-absent-list)
11. [Sources](#11-sources)

---

## 1. The Two Lists: BlockedIp and AllowedIp

**Confidence: HIGH.** Both registry schemas, the index builders, and the parsers were read line-by-line.

Stalwart tracks IPs in exactly two registry object types. There is no third list — no greylist-IP table, no per-port list, no temporary quarantine table.

### 1.1 BlockedIp (the blocklist)

`BlockedIp` registry object (`crates/registry/src/schema/structs.rs:746-755`):

```rust
pub struct BlockedIp {
    pub address: IpAddrOrMask,
    pub reason: BlockReason,
    #[serde(rename = "createdAt")]
    pub created_at: UTCDateTime,
    #[serde(rename = "expiresAt")]
    pub expires_at: Option<UTCDateTime>,
}
```

`BlockReason` is a closed enum of six values (`crates/registry/src/schema/enums.rs:259-269`; wire names in `crates/registry/src/schema/enums_impl.rs:1349-1360`):

| Variant | Value | Wire name | Set by |
|---|---|---|---|
| `RcptToFailure` | 0 | `rcptToFailure` | RCPT fail2ban (Section 2) |
| `AuthFailure` | 1 | `authFailure` | Auth fail2ban (Section 2) |
| `Loitering` | 2 | `loitering` | SMTP session-duration fail2ban (Section 2) |
| `PortScanning` | 3 | `portScanning` | Cross-protocol parse-error fail2ban + HTTP banned-path instant ban (Sections 2, 4; see also Report B) |
| `Manual` | 4 | `manual` | Admin-created blocks |
| `Other` | 5 | `other` | Anything else |

### 1.2 AllowedIp (the allowlist)

`AllowedIp` registry object (`crates/registry/src/schema/structs.rs:302-311`): same shape, but `reason` is a free-text `Option<String>` rather than an enum. Both objects carry `createdAt` and an optional `expiresAt` (absence = permanent).

### 1.3 Storage and indexing

Both object types are stored in the **registry**, a key-value store that rides on the same backend as the data store — SQLite, RocksDB, FoundationDB, PostgreSQL, or MySQL (`crates/store/src/lib.rs:369-389`; the `Store` enum contains no Redis variant). Objects are serialized as **pickle blobs** (`crates/store/src/registry/mod.rs:18,110`) with a hard payload cap of `MAX_OBJECT_PAYLOAD_SIZE = 200_000` bytes (~200 KB) enforced at write time (`crates/store/src/registry/write.rs:37,347`).

Both types declare a **unique index on `address`** in their `ObjectImpl::index` implementations — for `BlockedIp`, `i.unique(Property::Address, &self.address)` (`crates/registry/src/schema/structs_impl.rs:8321-8323`; same pattern for `AllowedIp`). The index key for a masked address is `to_index_key()`: the address octets concatenated with the mask as big-endian bytes — 8 bytes for IPv4 (4 addr + 4 mask), 24 for IPv6 (16 + 16) (`crates/registry/src/types/ipmask.rs:110-127`). This uniqueness is what makes re-bans of an already-blocked address take the conflict path described in Section 2.4.

### 1.4 CIDR parsing: what the API will actually accept

`IpAddrOrMask::from_str` (`crates/registry/src/types/ipmask.rs:131-180`):

- A bare IP parses as a /32 (IPv4) or /128 (IPv6) exact-host entry.
- With a `/mask` suffix, IPv4 masks must be in **/8../32** and IPv6 masks in **/8../128**. **/0 is not constructible through this parser** — the range checks reject it — so a "block everything" catch-all entry cannot be created via the API.
- `is_valid()` additionally rejects the IPv4 unspecified address `0.0.0.0` (`crates/registry/src/types/ipmask.rs:39-45`), enforced during object validation (`crates/registry/src/schema/structs_impl.rs:8300-8309`). The IPv6 unspecified address `::` is *not* covered by that check and is technically accepted.
- The `matches()` method handles v4/v6/mapped comparisons in both directions: a v4 entry matches a v4-mapped-v6 remote and vice versa; a v6 entry matches a v4 remote via `to_ipv4_mapped()` (`crates/registry/src/types/ipmask.rs:62-109`).

### 1.5 IP canonicalization at the edge

Every accepted connection canonicalizes v4-mapped IPv6 addresses (`::ffff:a.b.c.d`) down to plain IPv4 **before any security check runs** (`crates/common/src/network/listen.rs:266-273`). This means stored entries and live connections always compare in the same address family — a v4-mapped remote can never slip past a v4 block entry.

---

## 2. The Ban Flow (Automatic)

**Confidence: HIGH.** Every hop of the chain from trigger to registry write to cluster broadcast was read in full.

Automatic bans are fail2ban-style: a threshold counter trips, the IP is banned, and the counter is reset. The pipeline has exactly four stages.

### 2.1 Stage 1 — trigger sites

Five automatic triggers exist, all funneling into `crates/common/src/network/security.rs`:

| Trigger | Default threshold | Counter keys | Prefix | Call sites |
|---|---|---|---|---|
| **Auth failure** | 100/day (`authBanRate`, `structs_impl.rs:58380-58383`) | IP **and** blake3(login) — **either** exceeding bans | `KV_RATE_LIMIT_AUTH` = 5 (`crates/common/src/lib.rs:149`) | Central hook `Server::auth_failure` (`crates/common/src/auth/authentication.rs:96-121`), invoked from the generic auth wrapper (`authentication.rs:91`) and each protocol's authenticate op: IMAP `crates/imap/src/op/authenticate.rs:217`, ManageSieve `crates/managesieve/src/op/authenticate.rs:186`, POP3 `crates/pop3/src/op/authenticate.rs:177`, SMTP `crates/smtp/src/inbound/auth.rs:184`. JMAP OAuth calls `is_auth_fail2banned` directly at `crates/jmap/src/registry/mapping/account.rs:254` and `crates/http/src/auth/oauth/registration.rs:326`. |
| **RCPT failure** | 35/day (`abuseBanRate`, `structs_impl.rs:58375-58379`) | IP **and** blake3(rcpt) — either exceeding bans | `KV_RATE_LIMIT_RCPT` = 2 (`lib.rs:146`) | SMTP inbound `rcpt_error`, per rejected recipient (`crates/smtp/src/inbound/rcpt.rs:406-414`); emits `SecurityEvent::AbuseBan` on ban. |
| **Parse-error "scan"** | 30/day (`scanBanRate`, `structs_impl.rs:58407-58411`) | IP only | `KV_RATE_LIMIT_SCAN` = 3 (`lib.rs:147`) | Pre-auth protocol parse errors, **pooled across all protocols** into one counter per IP: SMTP `crates/smtp/src/inbound/session.rs:274-279` (unknown/invalid command while unauthenticated), IMAP `crates/imap/src/core/client.rs:67-75` (`PARSE` code while `NotAuthenticated`), POP3 `crates/pop3/src/client.rs:68`, ManageSieve `crates/managesieve/src/core/client.rs:104`, HTTP malformed-request `crates/http/src/request.rs:1042`. See Report B for the full behavioral analysis. |
| **Loitering** | 150/day (`loiterBanRate`, `structs_impl.rs:58385-58389`) | IP only | `KV_RATE_LIMIT_LOITER` = 4 (`lib.rs:148`) | SMTP session held open past `maxDuration` — the server answers `421 4.3.2 Session open for too long` then checks the limiter (`crates/smtp/src/inbound/spawn.rs:186-217`); emits `SecurityEvent::LoiterBan`. |
| **HTTP banned path** | Instant — **no counter** | n/a | n/a | `is_http_banned_path` (`crates/common/src/network/security.rs:270-280`): a request path matching any configured glob bans the source IP immediately with reason `PortScanning`, called per-request from `crates/http/src/request.rs:858`. Default globs (12): `*.php*`, `*.cgi*`, `*.asp*`, `*/wp-*`, `*/php*`, `*/cgi-bin*`, `*xmlrpc*`, `*../*`, `*/../*`, `*joomla*`, `*wordpress*`, `*drupal*` (`structs_impl.rs:58393-58406`). |

### 2.2 Stage 2 — the check IS the counter

Each `is_*_fail2banned` check calls `InMemoryStore::is_rate_allowed` (`crates/store/src/dispatch/lookup.rs:225-247`), which **increments the counter by 1 on every invocation** (`key_incr_at` with TTL = time remaining in the window) and returns "over limit" only when the new count exceeds the configured rate. Two properties follow directly from the code:

- Checking counts: every failure pushes the counter up even if it does not trigger a ban.
- For the dual-key checks (auth, rcpt), the two counters are checked with `&&` — if **either** the IP key or the blake3-hashed identity key has exceeded its threshold, the ban fires (`security.rs:346-356` for auth, `:234-248` for rcpt).

Every check first short-circuits for allowlisted IPs (`security.rs:230-232, 271-273, 294-296, 337-340`; see Section 5).

### 2.3 Stage 3 — `ban_and_reset`

When a check trips, `ban_and_reset` (`crates/common/src/network/security.rs:316-334`) runs `block_ip` (Section 2.4) and then calls `rate_reset` for **only the keys that participated in the trigger** — the IP key plus, for auth/rcpt, the blake3(login)/blake3(rcpt) key. `rate_reset` (`crates/store/src/dispatch/lookup.rs:249-251`) deletes the current-window bucket for `prefix || key` only, so counters under unrelated prefixes and unrelated keys are untouched.

### 2.4 Stage 4 — `block_ip`: four ordered effects

`block_ip` (`crates/common/src/network/security.rs:371-435`), on the node that observed the abuse:

1. **In-memory block** under the blocked-IP `RwLock` (`security.rs:377-378`): the entry is inserted or replaced, keeping the **longer** expiry — `BlockedIps::block` only replaces when the current entry expires earlier (`security.rs:507-518`).
2. **Registry write** (`security.rs:384-415`): inserts a `BlockedIp` object. On a unique-index collision (`RegistryWriteResult::PrimaryKeyConflict`), it fetches the existing row and **updates it only if the new expiry is strictly longer** — the update is skipped when `current >= new` (`security.rs:396-405`). A re-ban can extend a block but can never shorten one; once a row is permanent (`expiresAt = u64::MAX` internally), nothing automatic ever un-makes it.
3. **Cluster broadcast** (`security.rs:421-424`): `cluster_broadcast(RegistryChange::Insert(BlockedIp))` — peer nodes apply the change **incrementally** on receipt, without a full reload (`crates/common/src/cache/reload.rs:40-60`).
4. **Counter reset**: the `rate_reset` of Stage 3, deleting only the triggered bucket(s).

Automatic bans are therefore live in the blocking node's memory immediately and on every healthy cluster peer within one broadcast hop. This is the exact opposite of the admin path (Section 6).

---

## 3. Ban Periods: Permanent by Default

**Confidence: HIGH for the code behavior (read directly); MEDIUM for the weaponization assessment (analysis).**

### 3.1 The mapping

`ban_period(reason)` (`crates/common/src/network/security.rs:217-226`) maps block reason to a configured period:

| Reason | Setting | Default |
|---|---|---|
| `RcptToFailure` | `abuseBanPeriod` | `None` |
| `AuthFailure` | `authBanPeriod` | `None` |
| `Loitering` | `loiterBanPeriod` | `None` |
| `PortScanning` | `scanBanPeriod` | `None` |
| `Manual` / `Other` | — (always `None`) | — |

All four settings are `Option<Duration>` and all default to `None` (`crates/registry/src/schema/structs_impl.rs:58373-58419`). When the period is `None`, `block_ip` stores `expires_at = u64::MAX` (`security.rs:374-377`) — **the ban is permanent**. There is no finite default anywhere: out of the box, every automatic ban lasts until an administrator removes it.

`Manual` and `Other` blocks are *always* permanent unless an administrator sets `expiresAt` by hand. The patch rules for `BlockedIp` (`crates/registry/src/schema/structs_impl.rs:8404-8420`) make `address` create-only read-only (`assert_read_only`), `createdAt` server-set (`assert_server_set`), and leave `reason` and `expiresAt` admin-patchable.

### 3.2 The weaponization caveat

Permanent-by-default combined with a public-facing auth endpoint is a denial-of-service primitive in both directions:

- An attacker who wants to **lock out a NAT** (an office, a university, a VPN egress) needs only 101 failed authentications from that shared IP in one day: the counter trips at >100 (`structs_impl.rs:58380-58383`, `lookup.rs:236-247`) and the NAT's entire user base is blocked **permanently** — no unban function exists (Section 6). Each day the attacker can repeat this against new egress IPs at zero cost.
- The same math applies to the rcpt (35/day) and loiter (150/day) counters.

Report F (ReForwarder design) addresses this with finite default ban periods.

---

## 4. Enforcement Points

**Confidence: HIGH.** All enforcement sites were read and traced.

### 4.1 Accept-time drop, all protocols

The single gate for every protocol is in the shared connection-accept path: after the TCP handshake, `listen.rs` canonicalizes the remote IP and calls `server.is_ip_blocked(remote_ip)` (`crates/common/src/network/listen.rs:277`). A blocked IP gets **no protocol greeting**: the event `SecurityEvent::IpBlocked` is logged with listener, ports, and remote IP (`listen.rs:278-286`), and the accept function returns `None`, dropping the already-accepted stream. From the client's perspective the connection opens and then immediately closes — a silent, pre-auth drop. This applies uniformly to SMTP, IMAP, POP3, ManageSieve, and HTTP because they all share this accept path.

### 4.2 HTTP per-request re-check

HTTP adds a second gate: every request on an established connection re-checks `is_ip_blocked` against the **per-request** resolved remote IP, returning a `403` JSON problem response on a hit (`crates/http/src/request.rs:944-957`). This closes two gaps the accept-time check alone would leave:

- a ban that lands **mid-connection** on a keep-alive session takes effect on the next request;
- the remote IP evaluated is the one resolved from proxy/forwarded headers per request, so a client that was clean at accept time but presents a blocked forwarded IP is still refused.

### 4.3 Spam-filter tagging

The spam filter performs its own blocklist/allowlist lookups over IP addresses found in `Received` headers and message bodies: loopback, multicast, unspecified, and allowlisted IPs are skipped; blocked IPs add the `IP_BLOCKED` tag to the message's analysis result (`crates/spam-filter/src/analysis/ip.rs:87-99`). This is a scoring signal, not a refusal.

### 4.4 The in-memory structure and its cost model

Enforcement reads never touch the registry — they consult `BlockedIps` held behind a `RwLock` (`crates/common/src/network/security.rs:69-74`):

```rust
pub struct BlockedIps {
    pub blocked_ip_addresses: AHashSet<IpWithTtl<IpAddr>>,   // exact hosts: O(1)
    pub blocked_ip_networks: Vec<IpWithTtl<IpAddrOrMask>>,   // CIDR ranges: O(n) scan
    pub has_blocked_networks: bool,
}
```

- Exact-host hits are a hash-set lookup, O(1). CIDR entries require a linear scan guarded by `has_blocked_networks` (`security.rs:441-453`) — O(n) in the number of network entries. `IpWithTtl` hashes/compares on the IP alone, expiry aside (`security.rs:579-595`).
- **Expiry is lazy**: every lookup checks `is_expired()` and skips expired entries (`security.rs:446-452`), but expired entries linger in the set until replaced by a longer re-ban or until the next full re-parse (boot or `ReloadBlockedIps`), which is also the only moment expired rows are **physically deleted from the registry** (`security.rs:549-572`, emitting `SecurityEvent::IpBlockExpired`; same lifecycle for `AllowedIp` with `SecurityEvent::IpAllowExpired` at `security.rs:134-156`). There is no background sweeper task for either list.

---

## 5. The Allowlist: How It Overrides Everything

**Confidence: HIGH.**

`is_ip_allowed` (`crates/common/src/network/security.rs:455-470`) consults three sources, merged at startup by `Security::parse`:

1. **`AllowedIp` registry objects**, parsed into the same exact-host hash set + network vec structure (`security.rs:87-119`).
2. **`proxyTrustedNetworks`** from system settings, auto-added as permanent entries at parse time (`security.rs:121-132`) — the networks from which proxy-protocol forwarded IPs are trusted are unconditionally allowed.
3. **Loopback** — IPv4 `127.0.0.1` and IPv6 `::1` are always inserted (except in test builds) (`security.rs:157-168`).

The allowlist beats the blocklist structurally: `is_ip_blocked` ends with `&& !self.is_ip_allowed(ip)` (`security.rs:441-453`), so an address present in both lists is **allowed**. Beyond overriding blocks, allowlisted IPs are exempt from every enforcement input:

- every fail2ban check short-circuits before touching a counter (`security.rs:230-232, 271-273, 294-296, 337-340`);
- HTTP rate limits skip allowlisted IPs in both the authenticated and anonymous limiters (`crates/common/src/auth/rate_limit.rs:27-29, 138-139`);
- the spam filter skips allowlisted IPs in its DNSBL/tagging analysis (`crates/spam-filter/src/analysis/ip.rs:87-92`).

A **separate, adjacent control** is per-credential `allowedIps` on API keys and app passwords: `AccessToken::assert_is_valid` rejects a token whose request IP does not match the credential's configured `allowed_ips` masks, raising `SecurityEvent::IpUnauthorized` (`crates/common/src/auth/access_token.rs:623-635`). This restricts *who may use a credential*; it neither blocks nor allows network access for anyone else, and it does not interact with the two lists.

---

## 6. Unblocking and Propagation (The Footgun)

**Confidence: HIGH.** All negative claims were established by exhaustive greps across the repository.

### 6.1 There is no unban function

No `unban`, `Unban`, or un-block function exists anywhere in the codebase (case-insensitive repository-wide grep returns nothing). Removal of a block is purely a data-management operation with three manual steps:

1. **Delete the `BlockedIp` object** via the JMAP-style registry API (`x:BlockedIp/set` with `destroy`), gated by the `SysBlockedIp{Get,Create,Update,Destroy,Query}` permissions (`crates/registry/src/schema/enums_impl.rs:8413-8417`).
2. **Fire the reload action**: `x:Action/set {"type":"ReloadBlockedIps"}` (`crates/jmap/src/registry/mapping/action.rs:94-135`; wire name serialized at `crates/registry/src/schema/structs_impl.rs:720-722`). This triggers `reload_registry(RegistryChange::Reload(BlockedIp))`, which re-parses the entire list from the registry and swaps it into memory under the write lock (`crates/common/src/cache/reload.rs:88-94`), purging expired rows and emitting `SecurityEvent::IpBlockExpired` on the way (`security.rs:560-572`).
3. **Nothing else.** Without step 2 the deletion is inert.

### 6.2 Admin CRUD does not propagate — unlike automatic bans

This is the asymmetry that makes the flow a footgun:

- **Automatic bans** broadcast `RegistryChange::Insert(BlockedIp)` live (Section 2.4, step 3) and peers apply them incrementally (`reload.rs:40-60`).
- **Admin CRUD does not.** The registry object-set handler routes `BlockedIp` (and `AllowedIp`) to `registry_object_set`, which writes the registry and returns — there is **no `cluster_broadcast` call anywhere in `crates/jmap/src/registry/set.rs`** (grep-verified), and `CacheInvalidationBuilder` has no `BlockedIp` case at all (`crates/common/src/cache/invalidate.rs:92-120`, grep-verified). Only the explicit `ReloadBlockedIps` *action* broadcasts, and it broadcasts a full `RegistryChange::Reload(BlockedIp)` to peers (`action.rs:122-129`).

Consequences, both directions of staleness:

- A **created** block (admin-authored, or reason `Manual`) is invisible — on every node, including the one that accepted the write, because the in-memory list is only ever mutated by `block_ip()` or by a full `BlockedIps::parse` — until someone fires `ReloadBlockedIps` or the process restarts. Admin-created blocks are inert until then; this doubles as an accidental **grace window** against self-lockout (Section 7.3).
- A **deleted** block keeps blocking on every node — the in-memory entry and the registry row both survive — until `ReloadBlockedIps` or restart.

### 6.3 Related operational gaps

- **No CLI.** The server binary's entire CLI is `-c` (config), `-e` (export), `-i` (import), `-o` (store console), plus help/version (`crates/common/src/manager/boot.rs:44-58`). Blocklist management exists only through the HTTP/JMAP management API. Stalwart's management CLI is a **separate repository** (`stalwartlabs/cli`) that drives that same JMAP API (`CHANGELOG.md:883-889`).
- **No sweeper.** As noted in Section 4.4, expiry is evaluated lazily per lookup; nothing in the background expires either list. Registry rows for expired entries are physically deleted only at boot or on `ReloadBlockedIps`/`AllowedIp` reload.
- **Settings changes need a full reload.** Changing `*BanRate`/`*BanPeriod` (or any security setting) only takes effect through `ReloadSettings` (`action.rs:96-98`), which re-parses the full settings graph into a new `Core`; `InvalidateCaches` does not touch them.

---

## 7. Reset and Maintenance

**Confidence: HIGH.**

### 7.1 Maintenance tasks

- **`resetRateLimiters`** wipes **all** rate-limit prefixes — `2,3,4,5,6,7,8,9,10,19` (rcpt, scan, loiter, auth, smtp, contact, http-authenticated, http-anonymous, imap, cimd) — via `key_delete_prefix` (`crates/services/src/task_manager/tasks/maintenance.rs:381-410`; prefix constants at `crates/common/src/lib.rs:146-163`). Availability (prefix 12, `KV_RATE_LIMIT_AVAILABILITY`, `lib.rs:156`) is **deliberately excluded** — resetting it would enable calendar free/busy flooding. Fail2ban counters are among the wiped prefixes, so running this task also resets every in-progress abuse counter (but does **not** lift any already-issued ban, which lives in the registry).
- **`removeGreylist`** wipes `KV_GREYLIST` (prefix 13) (`maintenance.rs:382`; `lib.rs:157`).
- **`InvalidateCaches`** clears directory/caches only (`action.rs:141-144`) — it does **not** reset rate limiters.
- These are modeled as `Task` objects of type `TaskStoreMaintenance` with fields `{maintenanceType, shardIndex, status}` — **no recurrence field** (`crates/registry/src/schema/structs.rs:6184-6189`) — so an administrator can trigger one as a one-off.

### 7.2 Recovery mode

Setting `STALWART_RECOVERY_MODE=1` (`crates/store/src/registry/local.rs:46`) produces a deliberately hobbled security posture:

- the blocklist is loaded **empty** — `BlockedIps::parse` returns immediately when recovery mode is on (`security.rs:520-523`);
- all four fail2ban rates are disabled (`auth_fail_rate`/`rcpt_fail_rate`/`loiter_fail_rate`/`scanner_fail_rate` are `.filter(|_| !is_recovery_mode)`) and the HTTP banned-path list is emptied (`security.rs:180-196`);
- HTTP rate limits are disabled and permissive CORS is enabled (`crates/common/src/config/network.rs:453,495-506`);
- a recovery listener is served on port 8080 by default (`STALWART_RECOVERY_MODE_PORT`, `crates/common/src/config/server/listener.rs:37-45`).

Independently of recovery mode, `STALWART_RECOVERY_ADMIN=admin:<password>` (`crates/store/src/registry/local.rs:50`) configures a break-glass fallback admin that is checked first in basic authentication and returns a full-admin token — it works in normal operation too (`crates/common/src/auth/authentication.rs:133-175`), providing a way in even if an operator has blocked their own management address.

### 7.3 Self-lockout analysis

There is **no guard against banning your own address** — an admin, or an automated tool, can trip the counters from the management workstation like any other client. Mitigations in the design, in order of reliability:

1. The allowlist beats the blocklist (Section 5) — putting the admin station's IP in `AllowedIp` immunizes it against blocks and all fail2ban counters.
2. Loopback is always allowed (`security.rs:157-168`).
3. Admin-created (or any non-automatic) blocks do not take effect until `ReloadBlockedIps` (Section 6.2) — an accidental grace window that also means an admin who bans themselves via the API can often still reach the API to delete it, since the block never went live.
4. Blocked IPs are dropped **pre-auth** (Section 4.1), so nothing — including any admin credential — authenticates from a blocked address; once a block is live on your management IP, only the break-glass admin from an unblocked address, a registry edit from another node, or direct store surgery recovers it.

---

## 8. Cluster Behavior and Restart

**Confidence: HIGH for the code paths; MEDIUM for partition-consequence claims (derived from delivery semantics, not from a reproduced outage).**

- **The registry is the durable source of truth.** `BlockedIp`/`AllowedIp` rows live in the registry KV store and survive restarts. At boot, `BlockedIps::parse` rebuilds the in-memory list from the registry, dropping expired entries and physically deleting their rows (`security.rs:520-576`); `Security::parse` does the same for the allowlist (`security.rs:83-176`).
- **Cluster propagation** rides the coordinator pub/sub bus on topic `stwt.agora` (`crates/services/src/broadcast/mod.rs:37`), with the coordinator backed by Redis PubSub, Redis Cluster/Sentinel, NATS, Zenoh, or Kafka (`crates/registry/src/schema/structs.rs:1023-1032`). Events are batched (`MAX_BATCH_SIZE = 100`, `mod.rs:36`) with a payload cap of 512 KiB (`crates/services/src/broadcast/publisher.rs:16`), retried with exponential backoff from 100 ms capped at 5 s (`mod.rs:38-39`, `publisher.rs:59-66`).
- **Fail-open transport, stale-by-design under partition.** The bus is effectively fire-and-forget: undeliverable batches are dropped after retries (`publisher.rs:59-67`, `report_dropped`). There is no anti-entropy pass, no blocklist versioning, and no catch-up re-read — on resubscribe a peer does not re-fetch the blocklist. A partitioned node therefore keeps its **last-known blocklist** (missing new bans from the other side) until the next `RegistryChange::Insert`/`Reload(BlockedIp)` event crosses the healed partition, someone fires `ReloadBlockedIps`, or it restarts. Since permanent bans never self-heal (Section 3), a node that missed a ban broadcast blocks less than the cluster — the failure direction is under-blocking, not over-blocking, except for stale *deletions*, which over-block until reload (Section 6.2).
- **Split-brain guard for the counter store**: the local (in-process) in-memory store is hard-refused when a coordinator is configured — startup fails with *"The local in-memory store cannot be used when a cluster coordinator is configured"* (`crates/store/src/build/memory.rs:13-26`), forcing counters onto Redis (or enterprise sharded stores) so fail2ban counts are cluster-global.

---

## 9. Storage Engineering (Brief)

**Confidence: HIGH.** Full detail lives in Report A; only the IP-blocking-relevant mechanics are summarized here.

- **Counters live in the in-memory store**, chosen by configuration — `InMemoryStore` variants: `Local`, `Redis`, `Sql`, `Http`, `Static`, enterprise `Sharded` (`crates/store/src/lib.rs:425-439`).
  - **Local**: a papaya concurrent map (`crates/store/src/backend/local/mod.rs:17`) capped at `DEFAULT_MAX_ENTRIES = 1 << 20` (1,048,576) entries (`mod.rs:39`), with a lazy 30-second sweep (`SWEEP_INTERVAL = 30`, `crates/store/src/backend/local/maintenance.rs:13`). Under pressure, eviction scores entries — counters are evicted first (`COUNTER_RANK`), locks are never evicted (`crates/store/src/backend/local/entry.rs:233-238`). If the store is genuinely full, `reserve` fails with *"In-memory store is full"* (`maintenance.rs:26-30`) — the error propagates out of `is_rate_allowed`, and every fail2ban caller treats an error as "not banned" and lets the session continue, so a full counter store makes the engine **fail open**.
  - **Redis**: counter increments run as a Lua script — `INCRBY` + `EXPIRE` + `GET` (`crates/store/src/backend/redis/mod.rs:62-67`) — atomic per key, with TTL set to the window remainder. Redis unavailability likewise propagates as an error and fails open at the fail2ban call sites (e.g. `crates/smtp/src/inbound/session.rs:280-290` logs and continues).
- **Fixed-window counting**: the bucket key is `prefix || key || (now / period).to_be_bytes()` — an epoch-aligned fixed window with the counter's TTL equal to the time remaining in the window (`crates/store/src/dispatch/lookup.rs:22-44`). Window boundaries are global (epoch-aligned), so burst behavior concentrates at window edges; there is no sliding window.

---

## 10. Summary Table, Confidence, and Verified-Absent List

### 10.1 How Stalwart's IP blocking answers the four questions

| Question | Answer | Key evidence |
|---|---|---|
| **Tracked how?** | Two registry object types (`BlockedIp`, `AllowedIp`), unique-indexed on address, stored as pickle blobs (~200 KB cap) in the registry KV store on the data-store backend (SQLite/RocksDB/FDB/PostgreSQL/MySQL — never Redis), plus an in-memory `AHashSet`+`Vec` mirror behind a `RwLock` | `structs.rs:746-755, 302-311`; `structs_impl.rs:8321-8323`; `write.rs:37`; `security.rs:69-74` |
| **Banned how?** | Five automatic triggers (auth 100/day dual-key, rcpt 35/day dual-key, pooled cross-protocol parse "scan" 30/day, loiter 150/day, HTTP banned-path instant) run check-and-increment counters in the in-memory store; tripping calls `block_ip` → memory (longer-expiry merge) → registry (extend-only on conflict) → cluster broadcast → targeted counter reset | `security.rs:217-435`; `lookup.rs:225-251`; `lib.rs:146-163` |
| **Enforced where?** | Accept-time silent drop for all protocols (`IpBlocked`, no greeting); HTTP additionally re-checks per request (403 problem+json) over keep-alive and forwarded-header IPs; spam filter tags `IP_BLOCKED`; allowlist overrides blocklist and exempts all fail2ban + HTTP rate limits | `listen.rs:277-287`; `request.rs:944-957`; `ip.rs:87-99`; `security.rs:441-470`; `rate_limit.rs:27-29, 138-139` |
| **Unblocked how?** | No unban function. Admin deletes the `BlockedIp` object (JMAP registry API, `SysBlockedIp*` permissions) **and then must fire `ReloadBlockedIps`**; admin CRUD does not broadcast, so stale both ways until reload/restart. No CLI, no sweeper, no automatic unblock. Settings changes need `ReloadSettings`. | grep-verified absence; `action.rs:94-135`; `set.rs` (no broadcast); `boot.rs:44-58` |

### 10.2 Design properties worth copying vs. avoiding (for Report F)

| Property | Verdict |
|---|---|
| Accept-time single-gate enforcement shared by all protocols | Copy — one chokepoint, no per-protocol drift |
| Allowlist structurally overrides blocklist, plus exemptions from all counters | Copy |
| Longer-expiry-wins merge in memory and registry (re-ban extends, never shortens) | Copy |
| Dual-key fail2ban (IP + blake3 identity) with either-exceeds semantics | Copy with care — identity key adds targeted protection but see the poisoning corollary below |
| Check-is-count (every check increments) | Acceptable, but bounds must account for it |
| Permanent-by-default ban periods on public-facing auth | **Avoid** — NAT lockout weaponization (Section 3.2); Report F uses finite defaults |
| No unban API; admin CRUD without propagation; mandatory manual reload | **Avoid** — Report F requires propagating admin changes |
| Fire-and-forget cluster bus with no anti-entropy for the blocklist | Acceptable for coarse deltas; pair with periodic full re-sync |
| Lazy expiry with no sweeper | Acceptable; pair with boot/reload-time physical purge (as Stalwart does) |
| Fail-open counter store | Deliberate availability choice; document it |

**Observed corollary (analysis, not upstream-claimed):** because the rcpt counter's identity key is `blake3(rcpt)` and *either* key tripping bans the *sender's* IP, an attacker who drives one recipient key over its threshold can cause the next unrelated sender that touches that recipient to be banned — a cross-victim poisoning channel inherent to the dual-key design. (MEDIUM confidence — derived directly from `security.rs:229-259` semantics, not observed in a running system.)

### 10.3 Verified-absent list

Each absence below was checked by exhaustive case-insensitive grep and by reading every candidate site, on the pinned commit:

- **No per-IP connection rate limit** — the listener limiter bounds per-listener in-flight connections, not per-IP connection rates (`listen.rs:288-296`; see Report B).
- **No automatic unblock, cooldown, or progressive escalation** — no function or task reduces or expires a block early; the only exits are `expiresAt` on the object and manual deletion, and expiry is lazy (Section 4.4). No ban-count escalation ladder exists (first ban = tenth ban).
- **No ban audit trail** — `BlockedIp` records only `reason`/`createdAt`/`expiresAt`; no creator, source session, or triggering-event linkage is stored (`structs.rs:746-755`), and no audit log of bans exists beyond the ephemeral `SecurityEvent::{AbuseBan,LoiterBan,ScanBan,AuthenticationBan,IpBlocked}` trace events.
- **No CLI** for blocklist management (`boot.rs:44-58`; management CLI is a separate repo driving JMAP, `CHANGELOG.md:887-889`).
- **No cross-port scan detection** — connection topology is never observed; the `PortScanning` reason covers parse-error counting and URL-path globs only (fully analyzed in Report B).
- **No /0 catch-all, no `0.0.0.0`** entries via the API (`ipmask.rs:131-180, 39-45`).
- **No Redis in the registry path** — the registry KV store has no Redis backend (`store/src/lib.rs:369-389`); Redis only ever holds counters/leases in the in-memory store.

### 10.4 Overall confidence

**HIGH** for every mechanism described in Sections 1-9: all line references were re-verified against the pinned commit during this report's preparation, with zero discrepancies against the pre-verified finding set (two line ranges were tightened by a line or two during re-verification). MEDIUM confidence is reserved for the explicitly marked items: the weaponization assessment (Section 3.2), partition-consequence claims (Section 8), and the rcpt poisoning corollary (Section 10.2), all of which are code-derived analyses rather than reproduced incidents.

---

## 11. Sources

**Primary source** — Stalwart Mail Server, pinned clone `C:\Users\Ibrahim\.zcode\tmp\stalwart-analysis\stalwart`, commit `23b69755cb3cd8ecd952fe0443c5ac067a506184` (tag `v1.0.1`):

- `crates/registry/src/schema/structs.rs` — `BlockedIp` (746-755), `AllowedIp` (302-311), `TaskStoreMaintenance` (6184-6189), `Coordinator` (1023-1032)
- `crates/registry/src/schema/enums.rs` — `BlockReason` (259-269)
- `crates/registry/src/schema/enums_impl.rs` — wire names (1349-1360), `SysBlockedIp*` permissions (8413-8417), `Security` defaults (58373-58419), `BlockedIp` patch rules (8404-8420)
- `crates/registry/src/types/ipmask.rs` — `is_valid` (39-45), `matches` (62-109), `to_index_key` (110-127), `FromStr` (131-180)
- `crates/common/src/network/security.rs` — `BlockedIps` (69-74), `Security::parse` (83-205), `ban_period` (217-226), fail2ban checks (229-369), `ban_and_reset` (316-334), `block_ip` (371-435), `is_ip_blocked`/`is_ip_allowed` (441-470), `BlockedIps::block`/`parse` (507-576)
- `crates/common/src/network/listen.rs` — canonicalization and accept-time gate (266-287)
- `crates/common/src/auth/authentication.rs` — `auth_failure` (96-121), recovery admin (133-175)
- `crates/common/src/auth/rate_limit.rs` — HTTP rate-limit allowlist exemptions (27-29, 138-139)
- `crates/common/src/auth/access_token.rs` — per-credential `allowedIps` (623-640)
- `crates/common/src/cache/reload.rs` — incremental `Insert(BlockedIp)` apply (40-60), full `Reload(BlockedIp)` reparse (88-94)
- `crates/common/src/cache/invalidate.rs` — `CacheInvalidationBuilder` (no `BlockedIp` case; grep-verified)
- `crates/common/src/lib.rs` — `KV_RATE_LIMIT_*` prefix constants (146-163)
- `crates/common/src/manager/boot.rs` — server CLI (44-58)
- `crates/common/src/config/network.rs` — recovery-mode HTTP settings (453, 495-506); `crates/common/src/config/server/listener.rs` — recovery port (37-45)
- `crates/jmap/src/registry/mapping/action.rs` — `ReloadBlockedIps`/`ReloadSettings`/`InvalidateCaches` actions (94-144)
- `crates/jmap/src/registry/set.rs` — registry object set routing (769-863; no broadcast, grep-verified)
- Trigger sites: `crates/smtp/src/inbound/rcpt.rs:406-414`, `crates/smtp/src/inbound/spawn.rs:186-217`, `crates/smtp/src/inbound/session.rs:274-279`, `crates/smtp/src/inbound/auth.rs:184`, `crates/imap/src/op/authenticate.rs:217`, `crates/imap/src/core/client.rs:67-75`, `crates/pop3/src/op/authenticate.rs:177`, `crates/pop3/src/client.rs:68`, `crates/managesieve/src/op/authenticate.rs:186`, `crates/managesieve/src/core/client.rs:104`, `crates/http/src/request.rs:858, 1042`, `crates/jmap/src/registry/mapping/account.rs:254`, `crates/http/src/auth/oauth/registration.rs:326`
- `crates/spam-filter/src/analysis/ip.rs` — `IP_BLOCKED` tagging (87-99)
- `crates/services/src/broadcast/mod.rs` and `publisher.rs` — topic/batch/payload/backoff (36-39; 16, 59-67)
- `crates/services/src/task_manager/tasks/maintenance.rs` — `resetRateLimiters`/`removeGreylist` prefixes (381-410)
- `crates/store/src/lib.rs` — `Store` (369-389) and `InMemoryStore` (425-439) enums
- `crates/store/src/registry/write.rs` — payload cap (37, 347); `crates/store/src/registry/mod.rs` — pickle (18, 110); `crates/store/src/registry/local.rs` — recovery env vars (46-50); `crates/store/src/build/memory.rs` — coordinator refusal (13-26)
- `crates/store/src/dispatch/lookup.rs` — `RateWindow` (22-44), `is_rate_allowed` (225-247), `rate_reset` (249-251)
- `crates/store/src/backend/local/` — papaya (mod.rs:17), entry cap (mod.rs:39), sweep/full/eviction (maintenance.rs:13-30; entry.rs:233-238)
- `crates/store/src/backend/redis/mod.rs` — Lua `INCRBY`+`EXPIRE` (62-67)
- `CHANGELOG.md` — v0.16.0 management-CLI note (883-889)

**Companion reports in this series:**

- Report A — Stalwart storage architecture (counter-store details)
- Report B — `docs/security-research/stalwart-port-scanner-detection.md` (scan detection; parse-failure fail2ban analysis)
- Report F — ReForwarder design document (finite ban defaults, propagating admin unblocks)
