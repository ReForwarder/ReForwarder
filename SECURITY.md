## Security Policy

### Reporting a Vulnerability

If you discover a security vulnerability, please **do not open a public issue**.

Instead, report it privately by emailing: **[me@jkaninda.dev](mailto:me@jkaninda.dev)**

Please include as much detail as possible to help assess and reproduce the issue:

* Description of the vulnerability
* Steps to reproduce
* Potential impact
* Suggested fix (if available)

### Webhook egress security

Webhook destinations are user-supplied, so outbound webhook delivery is SSRF-hardened:

* **Direct mode (default)** — every connection is pinned at dial level to a pre-validated public IP (validated per address, mixed DNS answers fail closed), redirects are never followed, and recorded/user-facing delivery errors are sanitized: they never carry resolved addresses, ports or resolver detail.
* **Proxy mode** — `POSTA_WEBHOOK_PROXY_URL` routes deliveries through an HTTP/HTTPS/SOCKS5 proxy. The destination is still pinned: the proxy is asked to connect to the validated public IP per connection/request (never the hostname), so DNS rebinding between validation and connection cannot redirect the tunnel. TLS SNI and certificate verification stay hostname-based.
* **Operator acknowledgement** — proxy mode additionally requires `POSTA_WEBHOOK_PROXY_ACK_POLICY=enforced`; without it the proxy is ignored and delivery stays direct. The acknowledgement asserts the operator accepts the residual proxy-side assumptions: the proxy's own egress ACL behavior, split-horizon DNS reachability (the proxy must be able to reach the pinned public IP), rare proxies that refuse IP-form targets, and — for plain-HTTP (non-TLS) destinations — an IP-form `Host` header reaching the endpoint.

### Responsible Disclosure

We follow a responsible disclosure process:

* Your report will be acknowledged as soon as possible
* We will investigate and work on a fix promptly
* You will be notified once the issue is resolved
* Public disclosure will only happen after a fix is available

We appreciate your efforts to help keep this project secure.
