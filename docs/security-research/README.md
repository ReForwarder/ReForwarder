# docs/security-research/ — Security Deep-Dive Series (historical)

Clean-room security analyses of a third-party reference platform (2026-11-01/02), plus one audit of our own code. **Historical — the capabilities they designed were delivered.** Live status: [`docs/PROJECT_STATUS_DASHBOARD.md`](../PROJECT_STATUS_DASHBOARD.md).

| File | Series | Outcome |
|---|---|---|
| [reforwarder-security-blueprint.md](reforwarder-security-blueprint.md) | Report F — the Go adoption blueprint | ✅ **Delivered** as the 2026-11-02 security engine (`internal/services/security/`; RF-F088/089/090) |
| [stalwart-security-architecture.md](stalwart-security-architecture.md) | Report A — layered abuse-prevention architecture | Analysis input to Report F |
| [stalwart-port-scanner-detection.md](stalwart-port-scanner-detection.md) | Report B — port-scan verdict + fresh-design recommendation | ✅ Implemented 2026-11-02 |
| [stalwart-ip-blocking-engine.md](stalwart-ip-blocking-engine.md) | Report C — blocklist/allowlist mechanics | ✅ Delivered as the ban service |
| [stalwart-detection-mechanisms.md](stalwart-detection-mechanisms.md) | Report D — detector catalog (fail2ban counters, SMTP guards) | ✅ Adopted P0/P1 set shipped (RF-F089) |
| [stalwart-issue-820-commit-581533b-analysis.md](stalwart-issue-820-commit-581533b-analysis.md) | Upstream issue/commit analysis | Gated our own implementation (delivered same day) |
| [stalwart-source-reference-map.md](stalwart-source-reference-map.md) | Report E — source navigation index | Navigation aid |
| [stalwart-vs-reforwarder-feature-comparison.md](stalwart-vs-reforwarder-feature-comparison.md) | Report G — feature matrix | Historical; "nothing exists" rows predate the engine |
| [webhook-ssrf-audit.md](webhook-ssrf-audit.md) | Adversarial audit of our SSRF remediation (2026-11-04) | PASS WITH LIMITATIONS — limitations remain the recorded SSRF follow-ups |

**Clean-room discipline (AGENTS.md §7):** these reports describe behavior in our own words; no source, assets, or docs were copied from AGPL/BSL projects.
