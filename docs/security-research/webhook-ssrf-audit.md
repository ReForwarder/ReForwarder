# Independent Security Audit — Webhook SSRF Remediation (commit `5c2678a`)

> **Date:** 2026-11-04 · **Auditors:** three independent review agents (cold review, no implementation-brief contamination) · **Reference:** upstream goposta/posta#282
>
> **Scope:** every change in `5c2678a` (block SSRF through webhook URLs) + callers, workers, retry paths, config; proxy-mode assessment; automation-subsystem assessment (read-only). Adversarial tests were added and executed independently.

## Verdict

**PASS WITH LIMITATIONS.**

- The audited commit's own webhook delivery path held up under independent adversarial testing: **no exploitable bypass found** in `5c2678a`'s guarded path.
- The limitations, in priority order:
  1. **Automation webhook client** (`internal/services/automation/webhook_client.go`, shipped *after* the audited commit in `e9ac335`/`1565242`/`bb6968c`) — a second, weaker webhook egress path: redirects followed (≤10 hops, unvalidated), DNS-rebinding TOCTOU (no dial-time enforcement), 10 IP ranges missing, raw errors + ≤2KB response bodies persisted and served to all workspace members. Authoring is gated only by `auth`+`workspace` (no role/scope gate), so a viewer or send-only API key can author → trigger → read back internal responses. **High.** → follow-up issue *Security: Migrate Automation Webhooks to Shared SSRF Guard*.
  2. **Proxy mode** (`POSTA_WEBHOOK_PROXY_URL`, default off, admin opt-in) — pre-flight (server-side resolver) and proxy-side resolution are two sequential lookups with no shared state; a rebinding attacker crosses the gap (reproduced against production code with a throwaway test). With a proxy positioned inside the network, this is a **readable** SSRF (≤2KB bodies persisted per attempt, not redacted for hostname URLs). Redirects are empirically **not** reintroduced in proxy mode. **High-when-enabled / Low-Moderate overall.** → follow-up issue *Security: Harden Webhook Proxy Mode Against SSRF*.
  3. Pinned hardening gaps (Low): translation prefixes `2002::/16` and `64:ff9b::/96` not in `blockedV6`; `LegacyAttemptBodyUnsafe` keeps legacy hostname-URL bodies (forward deliveries cannot create such rows).

## 1. Verified protections (audited commit — all evidence from independent execution)

| Protection | Evidence |
| --- | --- |
| Static validation at create + every delivery attempt | `webhook_handler.go:62-67`, `dispatcher.go:427` |
| All DNS answers validated; mixed answers fail wholesale | `ssrf.go:198-217`; `TestAuditResolvePublicHost_AdversarialAnswers` PASS |
| IPv4/IPv6; mapped-v6 unmapped; zone literals blocked | `ssrf.go:153-177`; adversarial tests PASS |
| Adversarial IP-literal encodings (hex/decimal/octal/trailing-dot) fail closed at resolution | `TestAuditStaticGate_AdversarialIPEncodings`, `TestAuditDeliveryHexEncodedLoopbackBlocked` PASS (1 attempt, 0 server hits) |
| Pinned dial + `Control` pre-connect re-validation, socket-level | `TestAuditGuardedDialer_DialsValidatedLiteralOnly` (Control recorded exactly the validated literal; resolver called once per connection) |
| TOCTOU atomicity; fresh resolution per connection (rebinding dead) | same test (`calls == 1` per connection) + `TestGuardedDialerRebindBetweenAttempts` |
| TLS SNI/cert verification intact under literal dialing; no `InsecureSkipVerify` anywhere | `TestAuditTLSVerificationSurvivesLiteralDial`, `TestAuditGuardedTransportBlocksLiveLocalTLSServer`, grep |
| Redirects: 301/302/303/307/308 all terminate (3xx = final response) | `TestAuditAllRedirectCodesTerminateDelivery` + `TestAuditDispatcherClientCheckRedirectIsProduction` |
| Pooling safe; no ambient proxy; invalid proxy URL fails safe | `TestAuditDispatcherClientIsGuardedByConstruction` |
| Fail-fast on policy blocks (1 attempt, no backoff) | `TestDeliverPolicyBlockFailsFast`, hex-encoding e2e |
| Single delivery path: `FindByScopeAndEvent` has exactly one caller | git grep at `5c2678a` |
| Write-time + read-time sanitization; legacy rows leak-free; fail-closed body redaction | leak-scanner tests over hostile legacy rows: no IPs/ports/resolver detail; `HTTP <code>` kept verbatim |

Independent suite results: clean `5c2678a` worktree + audit tests → `go build ./...` OK, `go test -race` ok for both packages; full suite 34 packages, 0 FAIL. At pushed HEAD `bb6968c` (includes the automation feature): full `go test -count=1 -race ./...` green; golangci-lint: audited packages clean (1 residual `unparam` in the automation code, owned by that workstream).

## 2. Findings

| # | Severity | Finding |
| --- | --- | --- |
| F1 | High (post-commit path) | Automation webhook client gaps — see verdict item 1 / follow-up issue 2 |
| F2 | High-when-enabled | Proxy-mode TOCTOU — see verdict item 2 / follow-up issue 1 |
| F3 | Low | Translation prefixes (6to4, NAT64) not blocked — add to `blockedV6` |
| F4 | Low | Legacy hostname-URL attempt bodies assumed safe (forward-safe; legacy-only) |
| F5 | Low | Proxy-mode pre-flight resolver hardcoded `nil` — untestable without real DNS; inject it |
| F6 | Bug, not hole | `internal/routes/routes.go:216-219` builds the API-process dispatcher config without `ProxyURL` — API-path deliveries silently ignore proxy mode (path-dependent egress). Fix in follow-up issue 1 |
| F7 | Informational | 3xx recorded as *successful* delivery (semantics); audit-log `webhook.created/deleted` includes the URL (user-supplied, validated-public); `Webhook.Secret` returned at create (needed for HMAC); raw errors in server logs only |

## 3. Committed-test weaknesses (closed by the new audit tests)

1. `TestGuardedDialerRebindBetweenAttempts` dials a real external IP (network dependence).
2. `TestRedirectsNeverFollowed` covered only 302 and re-implemented `CheckRedirect` inline.
3. `TestDeliverWithRetryRecordsConnectionErrors` checked non-emptiness, not sanitization.
4. No adversarial IP-encoding, socket-level pinning, transport-construction, or proxy-mode gate tests.

The two audit test files (`internal/services/webhook/ssrf_audit_test.go`, `internal/handlers/ssrf_audit_test.go` — 16 test functions, all passing) are now part of the tree (landed in `bb6968c`).

## 4. Follow-up issues

1. **Security: Harden Webhook Proxy Mode Against SSRF** — remediation ranking: (a) fix `routes.go` wiring omission (S); (b) explicit trusted-proxy contract + operator docs in configuration.md and SECURITY.md (S); (c) IP-pinned proxy dialing — SOCKS5 first (atyp=1, ~30 lines), HTTP CONNECT second (custom RoundTripper rewriting CONNECT authority to the validated IP while preserving `req.Host` and `TLSClientConfig.ServerName`, M); (d) fail closed (remove proxy mode) as the documented fallback; operator-side egress allowlisting remains best practice. Also: inject the pre-flight resolver; consider `POSTA_WEBHOOK_EGRESS_IP` (LocalAddr binding) as a proxy-free alternative for the "control egress identity" use case.
2. **Security: Migrate Automation Webhooks to Shared SSRF Guard** — size M; ordered steps: guarded client constructor → replace ad-hoc checks with `ValidateWebhookURL` + keep `ResolvePublicURL` pre-flight (delete `validateWebhookIP`) → sanitize node-run errors at the engine write boundary → stop embedding response bodies in non-2xx errors (`HTTP %d` notation) → save-time static gate in `WorkflowDoc.Validate` → `urlPolicy` test seam mirroring the dispatcher; test plan ports the reject matrix, redirect table, rebinding, sanitization, and save-time cases; documented semantic changes (3xx terminal, CGNAT/TEST-NET rejected, ambient proxy ignored).

## 5. Outbound-HTTP inventory (bypass-sweep surface, verified)

Exactly five outbound-HTTP paths in the Go backend: (1) webhook dispatcher — the only `webhooks`-table delivery path, single caller; (2) automation webhook node (follow-up issue 2); (3) OAuth/OIDC (admin-supplied URLs, platform-admin only; users trigger but never choose destinations; no IP pinning — adjacent note); (4) update checker (hardcoded GitHub URL); (5) AWS S3 SDK (config endpoints). Adjacent non-HTTP outbound: DNS TXT verification (workspace domains), recipient MX/Host lookups, SMTP to workspace-configured hosts.
