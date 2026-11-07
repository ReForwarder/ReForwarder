# ADR-007 — DNS Automation & Deliverability (Providers, Certificates, MTA-STS/TLS-RPT/DANE)

- **Status:** Accepted (2026-11-07 — Option B programme authorization)
- **Phase:** Option B Phase 6 (with Phase 1 PTR-guidance slice) · **Covers (parity register):** SF-28 (DKIM2), SF-29 (MTA-STS), SF-31 (TLS-RPT), SF-34 (ACME), SF-61 (DNS auto-publication), SF-32 completion, GAP-08 residual

## Context

Domain deliverability today is verify-and-display: DNS records are shown for manual publication, verified on demand (R-07), and DKIM keys are managed with a manual lifecycle (R-02). The Option B Phase 6 assignment ("DNS automation + certificate lifecycle management; IP pools, warm-up controls, reputation monitoring") plus the todo.md reinstatement of DOM-007 (DNS provisioning) and DOM-010 (per-domain TLS) require an automation layer. ADR-001 deliberately did not adopt DNSSEC/DANE at Phase 1; this ADR owns that posture.

## Decision

1. **`DNSProvider` interface** (`internal/services/dnsmanager`) with Phase 6 adapters for **Cloudflare, Route53, DigitalOcean** (the three requested first integrations; the interface is the contract, adapters are additive). Provider credentials are stored encrypted (`internal/services/crypto` AES-256-GCM, same as signing keys). Functions: publish record set (SPF/DKIM/DMARC/verify/MX/return-path), read live records, delete. Publication is always followed by the existing verification flow (R-07) — auto-publish never skips verification.
2. **Credential safety**: provider tokens are write-scoped by the operator's own token choice (documented guidance), never logged, never returned by API responses (masked), and revocable per domain. A mis-scoped token fails the publish step with a typed error — no partial-zone writes.
3. **Certificate lifecycle (SF-34)**: integrate ACME via `golang.org/x/crypto/acme/autocert` for the TLS listeners that serve web/IMAP/POP3/SMTP-Submission per domain (SNI map), with the HTTP-01 challenge through the existing HTTP server and DNS-01 via the DNSProvider interface. Certificates cache to the blob store (FS/S3), reload hot (SF-110 pattern). The "ingress terminates TLS" alternative from the do-not-implement table remains supported — ACME is additive and config-gated.
4. **DKIM2 (SF-28)**: chained-signature verification support is a Phase 6 evaluation item behind a feature gate — DKIM1 (R-02) remains the signing standard; DKIM2 signing is adopted only if the specification stabilizes and inbound verification (Phase 3) shows real-world senders using it. Recorded honestly as PLANNED (evaluation), not promised.
5. **MTA-STS (SF-29) + TLS-RPT (SF-31)**: hosting the MTA-STS policy file for our domains (static text record + HTTPS policy path) and parsing inbound TLS-RPT reports (same ingest pattern as R-17 DMARC: dedicated RCPT allowlist, gzip/size caps, idempotent storage, read surface) are Phase 6; outbound MTA-STS enforcement for direct delivery (ADR-001) lands with it (mode: enforce/none per domain, cache with the MX resolver).
6. **DANE (SF-30)**: TLSA-record validation for outbound direct delivery is Phase 6, gated on DNSSEC-validating resolution being available (ADR-001 deferred it deliberately); documented as a hardening follow-up, not a Phase 6 blocker.
7. **IP pools + warm-up + reputation monitoring** (Phase 6 core): pool definitions map sending domains to source-IP sets (ADR-001 §7's binding extended), warm-up schedules cap per-domain volume ramping over configurable days, and reputation monitoring aggregates bounce/complaint/deferral rates per IP+domain from `mta_delivery_attempts` + `email_events` into the domain-health surface (R-07 extension).

## Alternatives considered and rejected

- **Bundling a DNS operator SDK per provider in the control plane without an interface**: rejected — the interface keeps adapters additive and testable with fake DNS backends.
- **Adopting certmagic/Lego**: Lego considered for ACME breadth (more challenge types, more providers); `autocert` covers HTTP-01/`TLS-ALPN` needs with zero new deps; Lego is the documented upgrade path if DNS-01-per-provider breadth demands it (decision recorded, reversible).
- **DNSSEC/DANE signing for our own zones**: rejected at any phase — ReForwarder is not authoritative DNS for tenant domains; it publishes records via provider APIs. DANE *validation* of remote hosts remains the Phase 6 evaluation above.

## Consequences

- New package `internal/services/dnsmanager` (interface, provider adapters, publication planner, drift re-check cron — closes GAP-08's drift-reverification residual alongside R-07), cert lifecycle in the listener bootstrap, MTA-STS policy hosting + TLS-RPT ingest (ADR-002 pattern), warm-up/reputation extensions to domain health.
- Tests: provider adapters against fake API servers; publication→verification flow; drift detection; ACME against a Pebble-style local ACME server in CI (optional env-gated); TLS-RPT parse/idempotency; warm-up cap math.
- Dashboard: Domains.vue gains provider connection, auto-publish toggle, warm-up schedule, reputation panel; admin gains provider token management.
