# Screenshots — visual acceptance evidence

Captured browser screenshots from the programme's visual acceptance passes (OB-2 through the final reconciliation). Each surface was driven in a real browser against a live compose stack + dev server and independently judged before acceptance.

| File | Surface |
|---|---|
| `domains-health.png` | Domain deliverability health panel (SPF/DKIM/DMARC, signing, sending rates) |
| `domains-dmarc.png` | DMARC aggregate reports subsection |
| `domains-dkim.png` | DKIM signing panel with a key listed |
| `domain-pools-reputation.png` | IP pools, warm-up, and reputation panel (OB-6) |
| `inbound-routes.png` | Inbound routing rules CRUD (OB-2) |
| `inbound-quarantine.png` | Quarantine review with release/delete (OB-2) |
| `mailboxes-admin.png` | Admin mailbox provisioning/suspend (OB-5) |
| `webmail-mailbox.png` | Webmail folder sidebar + message list (OB-5) |
| `webmail-message.png` | Webmail message view + attachments (OB-5) |
| `webmail-compose.png` | Webmail compose with attachment (OB-5) |
| `admin-queue.png` | Admin queue administration (R-03) |
| `apikey-usage.png` / `apikey-usage-limits.png` | API-key usage analytics + rate limits (R-15/R-18) |
| `profile-passkeys.png` | Passkey management card (R-19) |
| `impersonation-active.png` / `impersonation-exit.png` | Admin impersonation banner + exit flow (R-20) |
| `campaigns-schedule-ab.png` | Campaign schedule picker + A/B configuration (R-01/R-08) |
| `automation-config.png` | Automation builder with schedule trigger + segment condition (R-13/R-14) |
| `automation-config-save-validation-bug.png` | Bug capture found during visual acceptance (repaired; clean shot above) |

Related: [`docs/audits/FINAL_VERIFICATION_REPORT.md`](../audits/FINAL_VERIFICATION_REPORT.md) (the close-out report these screenshots are evidence for).
