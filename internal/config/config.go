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

package config

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	errorhandlers "github.com/goposta/posta/internal/error_handlers"
	"github.com/goposta/posta/internal/storage"
	"github.com/hibiken/asynq"
	goutils "github.com/jkaninda/go-utils"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Config struct {
	Database             DatabaseConfig
	Redis                RedisConfig
	JWTSecret            string
	Env                  string
	Port                 int
	DevMode              bool
	RateLimitHourly      int
	RateLimitDaily       int
	AuthRateLimitEnabled bool

	// RefreshTokensEnabled gates the CA-03 rotating refresh tokens. Default
	// true: logins additionally receive a single-use refresh token and
	// POST /auth/refresh rotates it. Setting POSTA_REFRESH_TOKENS_ENABLED=false
	// restores the pre-CA-03 behavior (access JWT only).
	RefreshTokensEnabled bool

	// SecurityEnabled turns the abuse-detection engine on: connection and
	// protocol signals are scored and offending IPs are banned automatically.
	SecurityEnabled bool
	// SecurityFailOpen decides what happens when the detector cannot reach
	// Redis. True (the default) lets the connection through; false rejects it
	// until detection works again.
	SecurityFailOpen bool

	// Email verification (POST /api/v1/emails/verify). Results are cached in
	// Redis to avoid re-checking the same address/domain on every call.
	EmailVerifyEnabled         bool
	EmailVerifyCacheTTLHours   int
	EmailVerifyMXCacheTTLHours int
	EmailVerifyRateHourly      int // per-user hourly cap; 0 disables
	AdminEmail                 string
	AdminPassword              string
	OpenAPIDocs                bool
	// AllowDowngrade lets the server boot even when the binary's version is
	// older than the version recorded in the database. Off by default.
	AllowDowngrade  bool
	securitySchemes okapi.SecuritySchemes

	MetricsEnabled bool

	// ---- OB-6 W5 observability completion (SF-93 OTLP slice, SF-96, GAP-51).
	// All three surfaces default OFF: an unset endpoint/URL ships nothing.

	// OTLPEndpoint is the OTLP/HTTP receiver base URL
	// (POSTA_OTLP_ENDPOINT, e.g. http://otel-collector:4318). Empty
	// disables the push exporter — the existing Prometheus families are
	// shipped via OTLP only when an operator points it at a collector.
	OTLPEndpoint string
	// OTLPIntervalSecs is the push period (POSTA_OTLP_INTERVAL_SECS,
	// default 60).
	OTLPIntervalSecs int
	// OTLPHeaders carries collector authentication headers
	// (POSTA_OTLP_HEADERS, comma-separated Name=Value).
	OTLPHeaders string

	// ErrorWebhookURL ships runtime panics/fatals as events
	// (POSTA_ERROR_WEBHOOK_URL, GAP-51). Empty disables the hook.
	ErrorWebhookURL string
	// ErrorWebhookSecret optionally HMAC-signs the error-hook payload
	// (POSTA_ERROR_WEBHOOK_SECRET) with the same scheme as delivery
	// webhooks (X-Posta-Signature: sha256=<hex>).
	ErrorWebhookSecret string

	// UpdateCheck enables the daily "is there a newer release?" query to GitHub.
	// It only ever reads a public release list and reports the answer to platform
	// admins; it never upgrades anything and sends nothing about this install.
	UpdateCheck bool

	PlanEnforcement bool

	// WebDir overrides where the dashboard is served from. The UI is normally
	// embedded in the binary (internal/web); setting POSTA_WEB_DIR serves it from
	// this directory instead, for frontend development or a customized build.
	WebDir     string
	AppWebURL  string
	ApiBaseURL string
	// CORSOrigins is POSTA_CORS_ORIGINS as given. When empty, the allowed
	// origins are derived from AppWebURL and ApiBaseURL; see AllowedCORSOrigins.
	CORSOrigins string

	// TrustedProxies lists the CIDR blocks or addresses of the reverse proxies
	// in front of Posta. X-Forwarded-For and X-Real-IP are honoured only on
	// connections from one of them. Left empty, the headers are trusted from
	// any peer, so a client can choose the IP that rate limits, API key IP
	// allow-lists and the audit log see.
	TrustedProxies []string

	// Worker settings
	EmbeddedWorker    bool
	WorkerConcurrency int
	WorkerMaxRetries  int
	// WorkerHealthEnabled and WorkerHealthPort control the probe and metrics
	// listener a dedicated worker exposes. It is on by default: a worker with no
	// probes cannot be health-checked by an orchestrator, which is the reason
	// the listener exists.
	//
	// The port defaults to POSTA_PORT, the same one the server uses. Server and
	// worker run as separate containers, so there is nothing to collide with,
	// and one port means one health check command and one EXPOSE for both.
	WorkerHealthEnabled bool
	WorkerHealthPort    int

	// DSNEnabled (POSTA_DSN_ENABLED, default false) gates SF-10: RFC 3464
	// delivery-status notifications generated for permanently failed
	// locally-originated mail (worker terminal failures + auto-suppressed
	// rejections). System rows never notify; the NULL reverse path and
	// Auto-Submitted: auto-replied stop bounce loops.
	DSNEnabled bool

	// AutoSuppressOnReject adds a recipient to the suppression list when an
	// outbound send is permanently rejected (5xx at RCPT TO, e.g. 550 user
	// unknown), and stops retrying that message.
	AutoSuppressOnReject bool

	// Webhook settings
	WebhookMaxRetries  int
	WebhookTimeoutSecs int
	WebhookProxyURL    string

	// SF-90 webhook batching (default off: per-event delivery). When enabled,
	// events group per endpoint up to WebhookBatchMaxEvents, flushed at least
	// every WebhookBatchFlushSecs. WebhookMaxPendingEvents caps the per-endpoint
	// pending buffer (drop-oldest + posta_webhook_events_dropped_total).
	WebhookBatchEnabled     bool
	WebhookBatchMaxEvents   int
	WebhookBatchFlushSecs   int
	WebhookMaxPendingEvents int

	// SF-89 Web Push (default off). See the env block for key formats.
	WebPushEnabled     bool
	WebPushVapidPublic string
	WebPushVapidPriv   string
	WebPushSubject     string

	// WebhookProxyAckPolicy gates WebhookProxyURL: proxy mode activates only
	// with "enforced" (POSTA_WEBHOOK_PROXY_ACK_POLICY=enforced); any other
	// value keeps webhook delivery in guarded direct mode.
	WebhookProxyAckPolicy string

	// OAuth settings
	GoogleOAuthClientID     string
	GoogleOAuthClientSecret string
	OAuthCallbackBaseURL    string

	// System SMTP for platform notifications (daily reports, invitations, etc.)
	SystemSMTP SystemSMTPConfig

	// Backup automation (R-11, POSTA_BACKUP_*). When enabled, the server's cron
	// manager runs pg_dump on a schedule, gzips the plain-format dump into
	// BackupDir, and prunes files older than BackupRetentionDays. Off by
	// default; the runtime image does not ship the postgres client, so enable
	// it only where pg_dump is installed (or point BackupPgDumpBin at a
	// mounted binary). See internal/cron/jobs/backup.go for the restore
	// runbook.
	BackupEnabled       bool
	BackupSchedule      string
	BackupDir           string
	BackupRetentionDays int
	BackupPgDumpBin     string

	// Multi-replica cron leader lease (GAP-03, ADR-009 §2). Off by default
	// so single-replica behavior is unchanged; enable on EVERY replica of a
	// multi-replica deployment so exactly one replica executes each
	// scheduled cron tick. Failure semantics and recovery are documented in
	// docs/docs/operations/redis-ha.md.
	CronLeaderLeaseEnabled bool
	// Lease TTL in seconds. A holder that stops renewing (crash, lost
	// Redis) is displaced at most this late — the failover bound is one
	// cron interval; the default matches the smallest schedule (1 minute).
	CronLeaseTTLSeconds int

	EmailVerificationRequired bool

	// Encryption key for SMTP password encryption (if empty, base64 encoding is used)
	EncryptionKey string

	// Blob storage settings (S3-compatible or filesystem)
	BlobProvider    string
	BlobS3Endpoint  string
	BlobS3Region    string
	BlobS3Bucket    string
	BlobS3AccessKey string
	BlobS3SecretKey string
	BlobS3UseSSL    bool
	BlobS3PathStyle bool
	BlobFSPath      string

	// Inbound email settings
	InboundEnabled        bool
	InboundSMTPHost       string
	InboundSMTPPort       int
	InboundMaxMessageSize int64
	InboundMaxAttachSize  int64
	InboundWebhookSecret  string
	InboundHostname       string
	InboundTLSMode        string
	InboundTLSCertFile    string
	InboundTLSKeyFile     string
	InboundSMTPRateLimit  int // per-IP max sessions per window; 0 disables
	InboundSMTPRateWindow int // rate-limit window in seconds

	// LMTP listener (SF-01, RFC 2033): local delivery agents hand mail for
	// our recipients over LHLO with per-recipient DATA status, through the
	// same ingest pipeline as the SMTP receiver. Off by default.
	LMTPEnabled bool
	LMTPPort    int

	// Hosted-mailbox delivery (OB-3, ADR-003, flow F13): a matched route
	// action=deliver files the message into the recipient mailbox's INBOX
	// via the mailstore. Auto-provision creates the recipient mailbox on
	// first delivery to a verified domain bound to the tenant user; off means
	// only already-provisioned mailboxes receive (archive-only otherwise).
	// Delivery itself also requires the blob store and POSTA_ENCRYPTION_KEY
	// (the stored form is encrypted at rest).
	MailstoreAutoProvision bool // POSTA_MAILSTORE_AUTO_PROVISION, default true

	// Spam & malware filtering (OB-2, ADR-006). Thresholds are
	// admin-configurable platform settings (spam_score_quarantine /
	// spam_score_reject); these environment keys gate the machinery itself.
	SpamFilterEnabled       bool          // POSTA_SPAM_FILTER_ENABLED, default true
	SpamDNSBLZones          string        // POSTA_SPAM_DNSBL_ZONES, allowlisted zones, empty disables
	SpamExternalFilter      bool          // POSTA_SPAM_EXTERNAL_FILTER_ENABLED, default false
	SpamExternalFilterEvent string        // POSTA_SPAM_EXTERNAL_FILTER_EVENT, default email.inbound.filter
	SpamExternalFilterTO    time.Duration // POSTA_SPAM_EXTERNAL_FILTER_TIMEOUT seconds, default 5
	ClamAVHost              string        // POSTA_CLAMAV_HOST, empty disables scanning
	ClamAVPort              int           // POSTA_CLAMAV_PORT, default 3310
	ClamAVTimeout           time.Duration // POSTA_CLAMAV_TIMEOUT seconds, default 30
	ClamAVFailMode          string        // POSTA_CLAMAV_FAIL_MODE: open (default) | closed
	// SF-42 attachment blocklists: extensions (".exe,.scr") and SHA-256
	// content digests quarantined on sight in the inbound filter. Empty
	// (the default) disables both lists.
	SpamBlockedExt   string // POSTA_SPAM_ATTACHMENT_BLOCK_EXT
	SpamBlockedHashes string // POSTA_SPAM_ATTACHMENT_BLOCK_SHA256

	// Web form message settings
	MessagesEnabled           bool
	MessagesMaxBodyBytes      int64
	MessagesMaxAttachSize     int64
	MessagesIPRateLimit       int
	MessagesIPRateWindow      int
	MessagesPerFormHourly     int
	MessagesPerEmailHourly    int
	MessagesPerWorkspaceDaily int
	MessagesInboundDomain     string

	// SMTP Relay settings.
	SMTPRelayEnabled        bool
	SMTPRelayHost           string
	SMTPRelayPort           int
	SMTPRelayHostname       string
	SMTPRelayTLSMode        string
	SMTPRelayTLSCertFile    string
	SMTPRelayTLSKeyFile     string
	SMTPRelayMaxMessageSize int64
	SMTPRelayRateLimit      int
	SMTPRelayRateWindow     int

	// SMTPDialTimeoutSecs bounds the TCP/TLS dial to an outgoing relay and
	// scales the overall send-session deadline (see email.SetDialTimeout).
	// A stalled relay must fail within this budget so the worker slot frees up
	// and asynq retries the task instead of wedging forever.
	SMTPDialTimeoutSecs int

	// Outbound MTA (OB-1, ADR-001). MTAEhloName is the EHLO identifier the
	// direct-to-MX sessions present; point it at the hostname whose PTR
	// record matches the outbound bind IP (domain health surfaces the
	// guidance). Delivery mode itself is per-domain configuration.
	MTAEhloName string

	// Transfer throttle (SF-108): DeliveryThrottleKBPS
	// (POSTA_DELIVERY_THROTTLE_KBPS, default 0 = unlimited) paces every byte
	// a direct-delivery connection sends through a token bucket.
	DeliveryThrottleKBPS int

	// Memory budget (SF-108/SF-43): MemoryBudgetMB
	// (POSTA_MEMORY_BUDGET_MB, default 0 = off) arms the process memory
	// watchdog; while over budget, SMTP listeners refuse new sessions with
	// 451 and posta_mta_memory_pressure_total counts the episodes.
	MemoryBudgetMB int

	// Per-message send-rate pacing (CA-04): MTAMsgRatePerMin
	// (POSTA_MTA_MSG_RATE_PER_MIN, default 0 = disabled) paces outbound
	// MAIL FROM commands to n messages per minute per sending domain
	// through the executor's MessageRateKeeper; 0 keeps today's unpaced
	// behavior. Sustained waits (>= 5s) are recorded as throttle_wait
	// attempt rows and observed on posta_mta_throttle_wait_seconds.
	MTAMsgRatePerMin int

	// MTA-STS policy hosting (SF-29, OB-6 W4, ADR-007 §5, RFC 8461).
	// MTASTSEnabled (POSTA_MTA_STS_ENABLED, default false) gates the
	// /.well-known/mta-sts.txt policy path on the existing HTTP server
	// (ADR-009 §1: gated path). When enabled, a verified domain whose
	// mta_sts_mode is set is served its policy on the mta-sts.<domain>
	// vhost; the operator fronts that vhost with the standard reverse proxy
	// (TLS terminates there). The outbound enforcement half (checking
	// recipient-domain policies during direct delivery) is always active —
	// it only tightens behavior for remote domains that publish an enforce
	// policy themselves, and falls open on discovery failure per RFC 8461.
	MTASTSEnabled bool

	// MTASTSEhloDomain is the generator identity for outbound TLS reports
	// (SF-31 outbound half): organization-name / contact-info in the report
	// document and the local domain of the mta-sts-report@ sender address.
	// Empty (POSTA_MTA_STS_ORG empty) keeps the outbound report pass inert —
	// reports are never sent without an explicit identity.
	MTASTSOrgDomain string

	// DMARCReportOrgDomain is the generator identity for outbound DMARC
	// aggregate reports (SF-25 send side): the RFC 7489 <org_name> in the
	// report document and the local domain of the dmarc-report@ sender
	// address. Empty (POSTA_DMARC_REPORT_ORG empty) keeps the outbound report
	// pass inert — reports are never sent without an explicit identity, and
	// then only to rua endpoints read from each reported domain's live
	// _dmarc DNS record.
	DMARCReportOrgDomain string

	// DNS automation (OB-6, ADR-007 decision 1): the provider publication
	// layer. DNSAutomationEnabled (POSTA_DNS_AUTOMATION_ENABLED, default
	// false) gates the connect/publish/drift routes and the drift cron —
	// off-by-default like every new surface (ADR-009 §8). DNSMailHost
	// (POSTA_DNS_MAIL_HOST) is the FQDN the MX + return-path records point
	// at; empty falls back to InboundHostname. DNSMailRouting
	// (POSTA_DNS_MAIL_ROUTING, default false) opts the publication planner
	// into writing MX/return-path records — publishing an MX redirects a
	// domain's mail RECEIPT, so it stays an explicit operator choice.
	DNSAutomationEnabled bool
	DNSMailHost          string
	DNSMailRouting       bool

	// Return-path / VERP (OB-2, ADR-002 decision 4, CA-05). ReturnPathSubdomain
	// is the subdomain label prepended to each sending domain to form the
	// bounce host — e.g. "bounce" turns sender domain mail.example.com into
	// bounce.mail.example.com. Empty (the default) keeps the feature fully
	// off: outbound keeps today's envelope and the inbound receiver accepts
	// no bounce addresses. ReturnPathSecret keys the HMAC that authenticates
	// bounce addresses (they cannot be forged without it); the feature only
	// activates when both values are set.
	ReturnPathSubdomain string
	ReturnPathSecret    string

	// Submission listener (SF-03, gated): a standards-compliant MSA on port
	// 587 that authenticates against SMTP relay credentials and feeds
	// accepted messages into the same pipeline as API sends. Off by default.
	SubmissionEnabled        bool
	SubmissionHost           string
	SubmissionPort           int
	SubmissionHostname       string
	SubmissionTLSMode        string
	SubmissionTLSCertFile    string
	SubmissionTLSKeyFile     string
	SubmissionMaxMessageSize int64
	SubmissionRateLimit      int
	SubmissionRateWindow     int

	// Mail-protocol listeners (OB-4, ADR-004): IMAP4rev1 (143 STARTTLS +
	// 993 implicit TLS) and POP3 (110 STARTTLS + 995 implicit TLS) served
	// over the mailstore, authenticated with app passwords only. Both are
	// OFF by default (POSTA_IMAP_ENABLED / POSTA_POP3_ENABLED); the
	// non-negotiables are enforced in validate(): TLS on any non-loopback
	// bind, a connection cap per user and per IP, and a per-session
	// command-rate budget.
	IMAP MailProtocolConfig
	POP3 MailProtocolConfig

	// ACME certificate lifecycle (SF-34, ADR-007 decision 3, OB-6 wave W2):
	// per-domain SNI certificates for the IMAP(993)/POP3(995) implicit-TLS
	// listeners, the submission(587) STARTTLS handshake and the webmail
	// HTTPS path, issued through golang.org/x/crypto/acme/autocert with the
	// certificate cache in the blob store. The whole feature is OFF by
	// default; see internal/services/certmanager (package doc) for the
	// challenge and listener posture, and the CertSolver DNS-01 seam.
	ACME AcmeConfig

	// Provider event webhook secrets (R-04). Each upstream provider that
	// posts delivery events to /api/v1/webhooks/provider/{provider} is
	// authenticated by its own signing secret; a provider whose secret is
	// empty answers 501 — the endpoint exists but the integration is not
	// configured. See internal/services/provider_events (package doc) for the
	// scheme each secret keys.
	ProviderSESSecret      string // POSTA_PROVIDER_SES_SECRET — SNS topic HMAC secret (SignatureVersion 1)
	ProviderSendGridKey    string // POSTA_PROVIDER_SENDGRID_KEY — HMAC verification key for X-SendGrid-Signature
	ProviderPostmarkSecret string // POSTA_PROVIDER_POSTMARK_SECRET — webhook token (header or Basic auth)
	ProviderMailgunKey     string // POSTA_PROVIDER_MAILGUN_KEY — webhook signing key
}

// MailProtocolConfig carries the settings of one mail-protocol listener pair
// (OB-4, ADR-004). IMAP uses ports 143 (STARTTLS) + 993 (implicit TLS); POP3
// uses 110 + 995. Port 0 disables that listener while the protocol stays
// enabled on the other. Authentication is app-password-only everywhere
// (mailauth.Authenticator); the limits below are the ADR-004 decision-5
// non-negotiables and are required to be positive when the protocol is on.
type MailProtocolConfig struct {
	Enabled  bool   // POSTA_IMAP_ENABLED / POSTA_POP3_ENABLED, default false
	Host     string // bind address for both listeners
	Port     int    // plaintext/STARTTLS listener; 0 disables it
	TLSPort  int    // implicit-TLS listener; 0 disables it
	Hostname string // advertised hostname (greeting, capability, cert ServerName)

	TLSCertFile string // certificate for the implicit-TLS listener and STARTTLS
	TLSKeyFile  string

	MaxSessionsPerUser int // concurrent authenticated sessions per user
	MaxConnsPerIP      int // concurrent connections per client IP, both listeners
	CommandRateLimit   int // commands per session per CommandRateWindow; required
	CommandRateWindow  int // command-rate window in seconds
}

// AcmeConfig carries the ACME certificate-lifecycle settings (SF-34,
// ADR-007 decision 3). Every listener posture the certificates serve is
// described in internal/services/certmanager; the HTTPS listener here is
// the webmail HTTPS path (okapi secondary TLS server — the plain-HTTP
// listener keeps serving the API and the HTTP-01 challenge responder).
type AcmeConfig struct {
	Enabled      bool     // POSTA_ACME_ENABLED, default false
	DirectoryURL string   // POSTA_ACME_DIRECTORY_URL; empty = Let's Encrypt production
	Email        string   // POSTA_ACME_EMAIL — ACME account contact
	Domains      []string // POSTA_ACME_DOMAINS — explicit certificate targets (comma-separated)

	// Webmail HTTPS listener (okapi WithTLSServer). Port 0 (the default)
	// keeps the webmail/API on plain HTTP only; a non-zero port serves the
	// same app over TLS with the SNI map, so mapping 443 to it also enables
	// TLS-ALPN-01 challenges. The optional static certificate pair is the
	// fallback for SNI names the ACME map does not cover.
	HTTPSHost     string // POSTA_ACME_HTTPS_HOST, default 0.0.0.0
	HTTPSPort     int    // POSTA_ACME_HTTPS_PORT, default 0 (off)
	HTTPSCertFile string // POSTA_ACME_HTTPS_CERT_FILE
	HTTPSKeyFile  string // POSTA_ACME_HTTPS_KEY_FILE

	// Blob-store cache namespace for the issued certificates, and the
	// interval (seconds) of the SF-110 host-allowlist hot reload
	// (verified tenant domains re-read and applied without restart).
	CachePrefix string // POSTA_ACME_CACHE_PREFIX, default certs/acme
	SyncSecs    int    // POSTA_ACME_SYNC_SECS, default 300
}

type SystemSMTPConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	Encryption string // none, ssl, starttls
}

func (s SystemSMTPConfig) IsConfigured() bool {
	return s.Host != "" && s.From != ""
}

type DatabaseConfig struct {
	DB       *gorm.DB
	host     string
	user     string
	password string
	name     string
	port     int
	sslMode  string
	url      string
}

// DSN returns the postgres connection string in the form external tools such
// as pg_dump (R-11 backup automation) accept, honoring POSTA_DB_URL overrides.
func (c *DatabaseConfig) DSN() string {
	if c.url != "" {
		return c.url
	}
	return fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s",
		c.host, c.user, c.password, c.name, c.port, c.sslMode)
}

type RedisConfig struct {
	Client   *redis.Client
	Addr     string
	Username string
	Password string
	DB       int
	// URL, when set, overrides the discrete fields above (e.g. redis://user:pass@host:6379/2).
	URL string
	// set for rediss:// URLs
	TLSConfig *tls.Config
}

// newRedisConfig reads Redis settings from the env; POSTA_REDIS_URL, if set,
// is parsed and overrides the discrete POSTA_REDIS_* vars (mirrors POSTA_DB_URL).
func newRedisConfig() RedisConfig {
	rc := RedisConfig{
		Addr:     goutils.Env("POSTA_REDIS_ADDR", "localhost:6379"),
		Username: goutils.Env("POSTA_REDIS_USERNAME", ""),
		Password: goutils.Env("POSTA_REDIS_PASSWORD", ""),
		DB:       goutils.EnvInt("POSTA_REDIS_DB", 0),
		URL:      goutils.Env("POSTA_REDIS_URL", ""),
	}
	if rc.URL != "" {
		opt, err := redis.ParseURL(rc.URL)
		if err != nil {
			logger.Fatal("invalid POSTA_REDIS_URL", "error", err)
		}
		rc.Addr = opt.Addr
		rc.Username = opt.Username
		rc.Password = opt.Password
		rc.DB = opt.DB
		rc.TLSConfig = opt.TLSConfig
	}
	return rc
}

// RedisOptions returns the go-redis client options.
func (r RedisConfig) RedisOptions() *redis.Options {
	return &redis.Options{
		Addr:      r.Addr,
		Username:  r.Username,
		Password:  r.Password,
		DB:        r.DB,
		TLSConfig: r.TLSConfig,
	}
}

// AsynqRedisOpt returns the Asynq Redis connection options.
func (r RedisConfig) AsynqRedisOpt() asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:      r.Addr,
		Username:  r.Username,
		Password:  r.Password,
		DB:        r.DB,
		TLSConfig: r.TLSConfig,
	}
}

// newMailProtocolConfig reads one mail-protocol listener pair (OB-4, ADR-004)
// from the environment; proto is the "IMAP" / "POP3" key fragment. Limits
// default to conservative on-values (never zero): the ADR-004 non-negotiables
// make connection caps, per-session command budgets and audit events part of
// the feature, not tunable opt-outs.
func newMailProtocolConfig(proto string, plainPort, tlsPort int) MailProtocolConfig {
	return MailProtocolConfig{
		Enabled:            goutils.EnvBool("POSTA_"+proto+"_ENABLED", false),
		Host:               goutils.Env("POSTA_"+proto+"_HOST", "0.0.0.0"),
		Port:               goutils.EnvInt("POSTA_"+proto+"_PORT", plainPort),
		TLSPort:            goutils.EnvInt("POSTA_"+proto+"_TLS_PORT", tlsPort),
		Hostname:           goutils.Env("POSTA_"+proto+"_HOSTNAME", "posta.local"),
		TLSCertFile:        goutils.Env("POSTA_"+proto+"_TLS_CERT_FILE", ""),
		TLSKeyFile:         goutils.Env("POSTA_"+proto+"_TLS_KEY_FILE", ""),
		MaxSessionsPerUser: goutils.EnvInt("POSTA_"+proto+"_MAX_SESSIONS_PER_USER", 10),
		MaxConnsPerIP:      goutils.EnvInt("POSTA_"+proto+"_MAX_CONNS_PER_IP", 30),
		CommandRateLimit:   goutils.EnvInt("POSTA_"+proto+"_COMMAND_RATE_LIMIT", 600),
		CommandRateWindow:  goutils.EnvInt("POSTA_"+proto+"_COMMAND_RATE_WINDOW", 60),
	}
}

type JWTConfig struct {
	Secret   string
	Issuer   string
	Audience string
}

type LogConfig struct {
	Level string
}

// newAcmeConfig reads the ACME certificate-lifecycle settings (SF-34,
// ADR-007 decision 3). Everything defaults OFF/empty: the feature exists
// only when POSTA_ACME_ENABLED=true (the listener wiring treats that as the
// single gate).
func newAcmeConfig() AcmeConfig {
	return AcmeConfig{
		Enabled:       goutils.EnvBool("POSTA_ACME_ENABLED", false),
		DirectoryURL:  goutils.Env("POSTA_ACME_DIRECTORY_URL", ""),
		Email:         goutils.Env("POSTA_ACME_EMAIL", ""),
		Domains:       splitList(goutils.Env("POSTA_ACME_DOMAINS", "")),
		HTTPSHost:     goutils.Env("POSTA_ACME_HTTPS_HOST", "0.0.0.0"),
		HTTPSPort:     goutils.EnvInt("POSTA_ACME_HTTPS_PORT", 0),
		HTTPSCertFile: goutils.Env("POSTA_ACME_HTTPS_CERT_FILE", ""),
		HTTPSKeyFile:  goutils.Env("POSTA_ACME_HTTPS_KEY_FILE", ""),
		CachePrefix:   goutils.Env("POSTA_ACME_CACHE_PREFIX", "certs/acme"),
		SyncSecs:      goutils.EnvInt("POSTA_ACME_SYNC_SECS", 300),
	}
}

func New() *Config {
	if err := godotenv.Load(); err != nil {
		logger.Debug("no .env file found, using environment variables")
	}
	// Resolved first so the worker's probe listener can default to it.
	port := goutils.EnvInt("POSTA_PORT", 9000)

	return &Config{
		Database: DatabaseConfig{
			host:     goutils.Env("POSTA_DB_HOST", "localhost"),
			user:     goutils.Env("POSTA_DB_USER", "posta"),
			password: goutils.Env("POSTA_DB_PASSWORD", "posta"),
			name:     goutils.Env("POSTA_DB_NAME", "posta"),
			port:     goutils.EnvInt("POSTA_DB_PORT", 5432),
			sslMode:  goutils.Env("POSTA_DB_SSL_MODE", "disable"),
			url:      goutils.Env("POSTA_DB_URL", ""),
		},
		Redis:                newRedisConfig(),
		Port:                 port,
		Env:                  goutils.Env("POSTA_ENV", "production"),
		JWTSecret:            goutils.Env("POSTA_JWT_SECRET", "change-me-in-production"),
		DevMode:              goutils.EnvBool("POSTA_DEV_MODE", false),
		RateLimitHourly:      goutils.EnvInt("POSTA_RATE_LIMIT_HOURLY", 100),
		RateLimitDaily:       goutils.EnvInt("POSTA_RATE_LIMIT_DAILY", 1000),
		AuthRateLimitEnabled: goutils.EnvBool("POSTA_AUTH_RATE_LIMIT_ENABLED", true),
		RefreshTokensEnabled: goutils.EnvBool("POSTA_REFRESH_TOKENS_ENABLED", true),
		SecurityEnabled:      goutils.EnvBool("POSTA_SECURITY_ENABLED", true),
		SecurityFailOpen:     goutils.EnvBool("POSTA_SECURITY_FAIL_OPEN", true),

		EmailVerifyEnabled:         goutils.EnvBool("POSTA_EMAIL_VERIFY_ENABLED", true),
		EmailVerifyCacheTTLHours:   goutils.EnvInt("POSTA_EMAIL_VERIFY_CACHE_TTL_HOURS", 168),
		EmailVerifyMXCacheTTLHours: goutils.EnvInt("POSTA_EMAIL_VERIFY_MX_CACHE_TTL_HOURS", 24),
		EmailVerifyRateHourly:      goutils.EnvInt("POSTA_EMAIL_VERIFY_RATE_HOURLY", 1000),
		AdminEmail:                 goutils.Env("POSTA_ADMIN_EMAIL", "admin@example.com"),
		AdminPassword:              goutils.Env("POSTA_ADMIN_PASSWORD", "admin1234"),
		OpenAPIDocs:                goutils.EnvBool("POSTA_OPENAPI_DOCS", true),
		AllowDowngrade:             goutils.EnvBool("POSTA_ALLOW_DOWNGRADE", false),
		securitySchemes:            okapi.SecuritySchemes{},

		MetricsEnabled: goutils.EnvBool("POSTA_METRICS_ENABLED", false),

		// OB-6 W5 observability: OTLP push exporter (SF-93) and the
		// error-monitoring hook (GAP-51) — both default-off.
		OTLPEndpoint:       goutils.Env("POSTA_OTLP_ENDPOINT", ""),
		OTLPIntervalSecs:   goutils.EnvInt("POSTA_OTLP_INTERVAL_SECS", 60),
		OTLPHeaders:        goutils.Env("POSTA_OTLP_HEADERS", ""),
		ErrorWebhookURL:    goutils.Env("POSTA_ERROR_WEBHOOK_URL", ""),
		ErrorWebhookSecret: goutils.Env("POSTA_ERROR_WEBHOOK_SECRET", ""),

		UpdateCheck:     goutils.EnvBool("POSTA_UPDATE_CHECK", true),
		PlanEnforcement: goutils.EnvBool("POSTA_PLAN_ENFORCEMENT", false),
		WebDir:          goutils.Env("POSTA_WEB_DIR", ""),
		AppWebURL:       goutils.Env("POSTA_WEB_URL", ""),
		ApiBaseURL:      goutils.Env("POSTA_API_URL", ""),

		CORSOrigins:    goutils.Env("POSTA_CORS_ORIGINS", ""),
		TrustedProxies: splitList(goutils.Env("POSTA_TRUSTED_PROXIES", "")),

		EmbeddedWorker:       goutils.EnvBool("POSTA_EMBEDDED_WORKER", false),
		WorkerConcurrency:    goutils.EnvInt("POSTA_WORKER_CONCURRENCY", 10),
		WorkerMaxRetries:     goutils.EnvInt("POSTA_WORKER_MAX_RETRIES", 5),
		WorkerHealthEnabled:  goutils.EnvBool("POSTA_WORKER_HEALTH_ENABLED", true),
		WorkerHealthPort:     goutils.EnvInt("POSTA_WORKER_HEALTH_PORT", port),
		AutoSuppressOnReject: goutils.EnvBool("POSTA_AUTO_SUPPRESS_ON_REJECT", true),
		// SF-10: RFC 3464 delivery-status notifications for permanently failed
		// locally-originated mail. Off by default (backscatter risk is the
		// operator's call); the NULL reverse path + Auto-Submitted headers stop
		// bounce loops regardless.
		DSNEnabled: goutils.EnvBool("POSTA_DSN_ENABLED", false),

		WebhookMaxRetries:     goutils.EnvInt("POSTA_WEBHOOK_MAX_RETRIES", 3),
		WebhookTimeoutSecs:    goutils.EnvInt("POSTA_WEBHOOK_TIMEOUT_SECS", 10),
		WebhookProxyURL:       goutils.Env("POSTA_WEBHOOK_PROXY_URL", ""),
		WebhookProxyAckPolicy: goutils.Env("POSTA_WEBHOOK_PROXY_ACK_POLICY", ""),

		WebhookBatchEnabled:     goutils.EnvBool("POSTA_WEBHOOK_BATCH_ENABLED", false),
		WebhookBatchMaxEvents:   goutils.EnvInt("POSTA_WEBHOOK_BATCH_MAX_EVENTS", 25),
		WebhookBatchFlushSecs:   goutils.EnvInt("POSTA_WEBHOOK_BATCH_FLUSH_SECS", 5),
		WebhookMaxPendingEvents: goutils.EnvInt("POSTA_WEBHOOK_MAX_PENDING_EVENTS", 1000),

		// SF-89 Web Push: off by default. When enabled, the VAPID identity is
		// required — base64url keys generated by webpush.GenerateKeys (the
		// private key is the 32-byte scalar, the public key the 65-byte
		// uncompressed P-256 point). Subject is the operator contact in the
		// VAPID JWT (mailto: or https:).
		WebPushEnabled:     goutils.EnvBool("POSTA_WEB_PUSH_ENABLED", false),
		WebPushVapidPublic: goutils.Env("POSTA_WEB_PUSH_VAPID_PUBLIC_KEY", ""),
		WebPushVapidPriv:   goutils.Env("POSTA_WEB_PUSH_VAPID_PRIVATE_KEY", ""),
		WebPushSubject:     goutils.Env("POSTA_WEB_PUSH_SUBJECT", ""),

		GoogleOAuthClientID:     goutils.Env("POSTA_GOOGLE_OAUTH_CLIENT_ID", ""),
		GoogleOAuthClientSecret: goutils.Env("POSTA_GOOGLE_OAUTH_CLIENT_SECRET", ""),
		OAuthCallbackBaseURL:    goutils.Env("POSTA_OAUTH_CALLBACK_URL", ""),

		SystemSMTP: SystemSMTPConfig{
			Host:       goutils.Env("POSTA_SYSTEM_SMTP_HOST", ""),
			Port:       goutils.EnvInt("POSTA_SYSTEM_SMTP_PORT", 587),
			Username:   goutils.Env("POSTA_SYSTEM_SMTP_USERNAME", ""),
			Password:   goutils.Env("POSTA_SYSTEM_SMTP_PASSWORD", ""),
			From:       goutils.Env("POSTA_SYSTEM_SMTP_FROM", ""),
			Encryption: goutils.Env("POSTA_SYSTEM_SMTP_ENCRYPTION", "starttls"),
		},

		EmailVerificationRequired: goutils.EnvBool("POSTA_EMAIL_VERIFICATION_REQUIRED", false),

		// Backup automation (R-11): off by default, daily at 03:00 UTC when on.
		BackupEnabled:       goutils.EnvBool("POSTA_BACKUP_ENABLED", false),
		BackupSchedule:      goutils.Env("POSTA_BACKUP_SCHEDULE", "0 3 * * *"),
		BackupDir:           goutils.Env("POSTA_BACKUP_DIR", "backups"),
		BackupRetentionDays: goutils.EnvInt("POSTA_BACKUP_RETENTION_DAYS", 14),
		BackupPgDumpBin:     goutils.Env("POSTA_BACKUP_PG_DUMP_BIN", "pg_dump"),

		// GAP-03 cron leader lease: off by default — a single replica must
		// not start depending on Redis liveness for its cron schedule. When
		// on, the TTL bounds failover to one cron interval.
		CronLeaderLeaseEnabled: goutils.EnvBool("POSTA_CRON_LEADER_LEASE_ENABLED", false),
		CronLeaseTTLSeconds:    goutils.EnvInt("POSTA_CRON_LEASE_TTL_SECONDS", 60),

		EncryptionKey: goutils.Env("POSTA_ENCRYPTION_KEY", ""),

		BlobProvider:    goutils.Env("POSTA_BLOB_PROVIDER", ""),
		BlobS3Endpoint:  goutils.Env("POSTA_BLOB_S3_ENDPOINT", ""),
		BlobS3Region:    goutils.Env("POSTA_BLOB_S3_REGION", "us-east-1"),
		BlobS3Bucket:    goutils.Env("POSTA_BLOB_S3_BUCKET", ""),
		BlobS3AccessKey: goutils.Env("POSTA_BLOB_S3_ACCESS_KEY", ""),
		BlobS3SecretKey: goutils.Env("POSTA_BLOB_S3_SECRET_KEY", ""),
		BlobS3UseSSL:    goutils.EnvBool("POSTA_BLOB_S3_USE_SSL", true),
		BlobS3PathStyle: goutils.EnvBool("POSTA_BLOB_S3_PATH_STYLE", false),
		BlobFSPath:      goutils.Env("POSTA_BLOB_FS_PATH", "data/attachments"),

		InboundEnabled:        goutils.EnvBool("POSTA_INBOUND_ENABLED", false),
		InboundSMTPHost:       goutils.Env("POSTA_INBOUND_SMTP_HOST", "0.0.0.0"),
		InboundSMTPPort:       goutils.EnvInt("POSTA_INBOUND_SMTP_PORT", 2525),
		InboundMaxMessageSize: int64(goutils.EnvInt("POSTA_INBOUND_MAX_MESSAGE_SIZE", 26214400)),
		InboundMaxAttachSize:  int64(goutils.EnvInt("POSTA_INBOUND_MAX_ATTACH_SIZE", 10485760)),
		InboundWebhookSecret:  goutils.Env("POSTA_INBOUND_WEBHOOK_SECRET", ""),
		InboundHostname:       goutils.Env("POSTA_INBOUND_HOSTNAME", "posta.local"),
		InboundTLSMode:        goutils.Env("POSTA_INBOUND_TLS_MODE", "none"),
		InboundTLSCertFile:    goutils.Env("POSTA_INBOUND_TLS_CERT_FILE", ""),
		InboundTLSKeyFile:     goutils.Env("POSTA_INBOUND_TLS_KEY_FILE", ""),
		InboundSMTPRateLimit:  goutils.EnvInt("POSTA_INBOUND_SMTP_RATE_LIMIT", 60),
		InboundSMTPRateWindow: goutils.EnvInt("POSTA_INBOUND_SMTP_RATE_WINDOW", 60),
		LMTPEnabled: goutils.EnvBool("POSTA_LMTP_ENABLED", false),
		LMTPPort:    goutils.EnvInt("POSTA_LMTP_PORT", 24),

		// Hosted-mailbox auto-provisioning (OB-3, ADR-003, flow F13).
		MailstoreAutoProvision: goutils.EnvBool("POSTA_MAILSTORE_AUTO_PROVISION", true),

		// Spam & malware filtering (OB-2, ADR-006).
		SpamFilterEnabled:       goutils.EnvBool("POSTA_SPAM_FILTER_ENABLED", true),
		SpamDNSBLZones:          goutils.Env("POSTA_SPAM_DNSBL_ZONES", ""),
		SpamExternalFilter:      goutils.EnvBool("POSTA_SPAM_EXTERNAL_FILTER_ENABLED", false),
		SpamExternalFilterEvent: goutils.Env("POSTA_SPAM_EXTERNAL_FILTER_EVENT", "email.inbound.filter"),
		SpamExternalFilterTO:    time.Duration(goutils.EnvInt("POSTA_SPAM_EXTERNAL_FILTER_TIMEOUT", 5)) * time.Second,
		ClamAVHost:              goutils.Env("POSTA_CLAMAV_HOST", ""),
		ClamAVPort:              goutils.EnvInt("POSTA_CLAMAV_PORT", 3310),
		ClamAVTimeout:           time.Duration(goutils.EnvInt("POSTA_CLAMAV_TIMEOUT", 30)) * time.Second,
		ClamAVFailMode:          goutils.Env("POSTA_CLAMAV_FAIL_MODE", "open"),
		SpamBlockedExt:    goutils.Env("POSTA_SPAM_ATTACHMENT_BLOCK_EXT", ""),
		SpamBlockedHashes: goutils.Env("POSTA_SPAM_ATTACHMENT_BLOCK_SHA256", ""),

		MessagesEnabled:           goutils.EnvBool("POSTA_MESSAGES_ENABLED", false),
		MessagesMaxBodyBytes:      int64(goutils.EnvInt("POSTA_MESSAGES_MAX_BODY_BYTES", 65536)),
		MessagesMaxAttachSize:     int64(goutils.EnvInt("POSTA_MESSAGES_MAX_ATTACH_SIZE", 5242880)),
		MessagesIPRateLimit:       goutils.EnvInt("POSTA_MESSAGES_IP_RATE_LIMIT", 20),
		MessagesIPRateWindow:      goutils.EnvInt("POSTA_MESSAGES_IP_RATE_WINDOW", 3600),
		MessagesPerFormHourly:     goutils.EnvInt("POSTA_MESSAGES_PER_FORM_HOURLY", 200),
		MessagesPerEmailHourly:    goutils.EnvInt("POSTA_MESSAGES_PER_EMAIL_HOURLY", 5),
		MessagesPerWorkspaceDaily: goutils.EnvInt("POSTA_MESSAGES_PER_WORKSPACE_DAILY", 1000),
		MessagesInboundDomain:     goutils.Env("POSTA_MESSAGES_INBOUND_DOMAIN", ""),

		SMTPRelayEnabled:        goutils.EnvBool("POSTA_SMTP_RELAY_ENABLED", false),
		SMTPRelayHost:           goutils.Env("POSTA_SMTP_RELAY_HOST", "0.0.0.0"),
		SMTPRelayPort:           goutils.EnvInt("POSTA_SMTP_RELAY_PORT", 2526),
		SMTPRelayHostname:       goutils.Env("POSTA_SMTP_RELAY_HOSTNAME", "posta.local"),
		SMTPRelayTLSMode:        goutils.Env("POSTA_SMTP_RELAY_TLS_MODE", "none"),
		SMTPRelayTLSCertFile:    goutils.Env("POSTA_SMTP_RELAY_TLS_CERT_FILE", ""),
		SMTPRelayTLSKeyFile:     goutils.Env("POSTA_SMTP_RELAY_TLS_KEY_FILE", ""),
		SMTPRelayMaxMessageSize: int64(goutils.EnvInt("POSTA_SMTP_RELAY_MAX_MESSAGE_SIZE", 26214400)),
		SMTPRelayRateLimit:      goutils.EnvInt("POSTA_SMTP_RELAY_RATE_LIMIT", 60),
		SMTPRelayRateWindow:     goutils.EnvInt("POSTA_SMTP_RELAY_RATE_WINDOW", 60),

		SMTPDialTimeoutSecs: goutils.EnvInt("POSTA_SMTP_DIAL_TIMEOUT_SECS", 15),

		// Outbound MTA (OB-1): EHLO name for direct-to-MX sessions.
		MTAEhloName:          goutils.Env("POSTA_MTA_EHLO_NAME", ""),
		MTAMsgRatePerMin:     goutils.EnvInt("POSTA_MTA_MSG_RATE_PER_MIN", 0),
		DeliveryThrottleKBPS: goutils.EnvInt("POSTA_DELIVERY_THROTTLE_KBPS", 0),
		MemoryBudgetMB:       goutils.EnvInt("POSTA_MEMORY_BUDGET_MB", 0),

		// MTA-STS policy hosting (SF-29, OB-6 W4): gated well-known path;
		// see the field comments for the gate semantics.
		MTASTSEnabled:   goutils.EnvBool("POSTA_MTA_STS_ENABLED", false),
		MTASTSOrgDomain: strings.ToLower(strings.TrimSpace(goutils.Env("POSTA_MTA_STS_ORG", ""))),

		// DMARC aggregate reports (SF-25 send side): gated by the generator
		// identity; see the field comment for the gate semantics.
		DMARCReportOrgDomain: strings.ToLower(strings.TrimSpace(goutils.Env("POSTA_DMARC_REPORT_ORG", ""))),

		// DNS automation (OB-6, ADR-007 decision 1): off by default; see the
		// field comments for the gate semantics.
		DNSAutomationEnabled: goutils.EnvBool("POSTA_DNS_AUTOMATION_ENABLED", false),
		DNSMailHost:          goutils.Env("POSTA_DNS_MAIL_HOST", ""),
		DNSMailRouting:       goutils.EnvBool("POSTA_DNS_MAIL_ROUTING", false),

		// Return-path / VERP (OB-2, ADR-002 decision 4): empty subdomain =
		// feature off (the default; zero behavior change).
		ReturnPathSubdomain: strings.ToLower(strings.TrimSpace(goutils.Env("POSTA_RETURN_PATH_SUBDOMAIN", ""))),
		ReturnPathSecret:    goutils.Env("POSTA_RETURN_PATH_SECRET", ""),

		// Submission listener (SF-03): gated MSA on 587, off by default.
		SubmissionEnabled:        goutils.EnvBool("POSTA_SUBMISSION_ENABLED", false),
		SubmissionHost:           goutils.Env("POSTA_SUBMISSION_HOST", "0.0.0.0"),
		SubmissionPort:           goutils.EnvInt("POSTA_SUBMISSION_PORT", 587),
		SubmissionHostname:       goutils.Env("POSTA_SUBMISSION_HOSTNAME", "posta.local"),
		SubmissionTLSMode:        goutils.Env("POSTA_SUBMISSION_TLS_MODE", "none"),
		SubmissionTLSCertFile:    goutils.Env("POSTA_SUBMISSION_TLS_CERT_FILE", ""),
		SubmissionTLSKeyFile:     goutils.Env("POSTA_SUBMISSION_TLS_KEY_FILE", ""),
		SubmissionMaxMessageSize: int64(goutils.EnvInt("POSTA_SUBMISSION_MAX_MESSAGE_SIZE", 26214400)),
		SubmissionRateLimit:      goutils.EnvInt("POSTA_SUBMISSION_RATE_LIMIT", 60),
		SubmissionRateWindow:     goutils.EnvInt("POSTA_SUBMISSION_RATE_WINDOW", 60),

		// Mail-protocol listeners (OB-4, ADR-004): off by default; TLS and
		// the limits are required when on (validate()).
		IMAP: newMailProtocolConfig("IMAP", 143, 993),
		POP3: newMailProtocolConfig("POP3", 110, 995),
		ACME: newAcmeConfig(),

		ProviderSESSecret:      goutils.Env("POSTA_PROVIDER_SES_SECRET", ""),
		ProviderSendGridKey:    goutils.Env("POSTA_PROVIDER_SENDGRID_KEY", ""),
		ProviderPostmarkSecret: goutils.Env("POSTA_PROVIDER_POSTMARK_SECRET", ""),
		ProviderMailgunKey:     goutils.Env("POSTA_PROVIDER_MAILGUN_KEY", ""),
	}
}

// removedEnvVars are settings that no longer do anything. Setting one is not an
// error, but it is worth telling the operator so they can clean up.
var removedEnvVars = map[string]string{
	"POSTA_WORKSPACE_ONLY_MODE": "every resource is workspace-scoped unconditionally; the flag no longer does anything",
}

func warnRemovedEnvVars() {
	for name, why := range removedEnvVars {
		if _, ok := os.LookupEnv(name); ok {
			logger.Warn("ignoring removed setting", "variable", name, "reason", why)
		}
	}
}

func (c *Config) validate() error {
	warnRemovedEnvVars()
	if c.MessagesEnabled && c.MessagesInboundDomain != "" && !c.InboundEnabled {
		return fmt.Errorf("POSTA_MESSAGES_INBOUND_DOMAIN requires POSTA_INBOUND_ENABLED=true")
	}
	if c.InboundEnabled && c.InboundTLSMode != "" && c.InboundTLSMode != "none" {
		if c.InboundTLSMode != "starttls" {
			return fmt.Errorf("unsupported POSTA_INBOUND_TLS_MODE %q (use none or starttls)", c.InboundTLSMode)
		}
		if c.InboundTLSCertFile == "" || c.InboundTLSKeyFile == "" {
			return fmt.Errorf("POSTA_INBOUND_TLS_MODE=%s requires POSTA_INBOUND_TLS_CERT_FILE and POSTA_INBOUND_TLS_KEY_FILE", c.InboundTLSMode)
		}
	}
	if c.SMTPRelayEnabled {
		switch c.SMTPRelayTLSMode {
		case "", "none":
		case "starttls":
			if c.SMTPRelayTLSCertFile == "" || c.SMTPRelayTLSKeyFile == "" {
				return fmt.Errorf("POSTA_SMTP_RELAY_TLS_MODE=starttls requires POSTA_SMTP_RELAY_TLS_CERT_FILE and POSTA_SMTP_RELAY_TLS_KEY_FILE")
			}
		default:
			return fmt.Errorf("unsupported POSTA_SMTP_RELAY_TLS_MODE %q (use none or starttls)", c.SMTPRelayTLSMode)
		}
	}
	if c.SubmissionEnabled {
		switch c.SubmissionTLSMode {
		case "", "none":
		case "starttls":
			// A static certificate pair OR the ACME SNI map (SF-34, ADR-007
			// decision 3) can serve the STARTTLS handshake; without either,
			// starttls mode has no certificate to offer.
			if (c.SubmissionTLSCertFile == "" || c.SubmissionTLSKeyFile == "") && !c.ACME.Enabled {
				return fmt.Errorf("POSTA_SUBMISSION_TLS_MODE=starttls requires POSTA_SUBMISSION_TLS_CERT_FILE and POSTA_SUBMISSION_TLS_KEY_FILE (or POSTA_ACME_ENABLED=true with an ACME certificate map)")
			}
		default:
			return fmt.Errorf("unsupported POSTA_SUBMISSION_TLS_MODE %q (use none or starttls)", c.SubmissionTLSMode)
		}
	}
	// okapi drops an invalid list and trusts no proxy, which would quietly
	// collapse every client onto the proxy's address. Refuse to start instead.
	for _, entry := range c.TrustedProxies {
		if !validProxyEntry(entry) {
			return fmt.Errorf("invalid POSTA_TRUSTED_PROXIES entry %q (expected a CIDR block or IP address)", entry)
		}
	}
	if err := c.validateACME(); err != nil {
		return err
	}
	if err := c.validateMailProtocols(); err != nil {
		return err
	}
	return c.ValidateSecurity()
}

// validateACME enforces the ADR-007 certificate-lifecycle posture (SF-34):
// ACME needs the blob store for the certificate cache (the cache is
// structural — see certmanager.New), the webmail HTTPS listener needs a
// sane bind, and the static fallback pair (when given) must be complete.
func (c *Config) validateACME() error {
	a := c.ACME
	if !a.Enabled {
		return nil
	}
	if c.BlobProvider == "" {
		return fmt.Errorf("POSTA_ACME_ENABLED=true requires the blob store (POSTA_BLOB_PROVIDER=fs|s3): issued certificates are cached there and the hot reload re-reads them (ADR-007 decision 3)")
	}
	if a.HTTPSPort < 0 || a.HTTPSPort > 65535 {
		return fmt.Errorf("invalid POSTA_ACME_HTTPS_PORT %d (use 0 to keep the webmail path on plain HTTP, or 1-65535)", a.HTTPSPort)
	}
	if (a.HTTPSCertFile == "") != (a.HTTPSKeyFile == "") {
		return fmt.Errorf("POSTA_ACME_HTTPS_CERT_FILE and POSTA_ACME_HTTPS_KEY_FILE must be set together (static fallback certificate for the webmail HTTPS listener)")
	}
	if a.SyncSecs <= 0 {
		return fmt.Errorf("POSTA_ACME_SYNC_SECS must be positive (the SF-110 host-allowlist hot reload interval)")
	}
	return nil
}

// isLoopbackHost reports whether host binds only the loopback interface — the
// one place ADR-004 decision 4 permits a cleartext mail listener (tests and
// local development).
func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// validateMailProtocols enforces the ADR-004 posture for the IMAP/POP3
// listeners: enabled means TLS-protected (implicit-TLS ports always need a
// certificate; a plaintext listener is only accepted on a loopback bind), the
// connection and command limits are on, and the four ports do not collide
// with each other or with the other listeners this process may open.
func (c *Config) validateMailProtocols() error {
	for name, m := range map[string]MailProtocolConfig{"POSTA_IMAP": c.IMAP, "POSTA_POP3": c.POP3} {
		if !m.Enabled {
			continue
		}
		if m.Port < 0 || m.TLSPort < 0 {
			return fmt.Errorf("%s_PORT and %s_TLS_PORT cannot be negative", name, name)
		}
		if m.Port == 0 && m.TLSPort == 0 {
			return fmt.Errorf("%s_ENABLED=true needs %s_PORT or %s_TLS_PORT above 0 (both are 0)", name, name, name)
		}
		if m.Port > 65535 || m.TLSPort > 65535 {
			return fmt.Errorf("%s_PORT and %s_TLS_PORT must be at most 65535", name, name)
		}
		// An implicit-TLS port needs a certificate: the static pair, or the
		// ACME SNI map (OB-6 W2, SF-34/ADR-007 decision 3) when enabled.
		if m.TLSPort > 0 && (m.TLSCertFile == "" || m.TLSKeyFile == "") && !c.ACME.Enabled {
			return fmt.Errorf("%s_TLS_PORT=%d requires %s_TLS_CERT_FILE and %s_TLS_KEY_FILE (or POSTA_ACME_ENABLED=true with an ACME certificate map; implicit TLS serves no cleartext)", name, m.TLSPort, name, name)
		}
		if m.Port > 0 && (m.TLSCertFile == "" || m.TLSKeyFile == "") && !isLoopbackHost(m.Host) {
			return fmt.Errorf("%s_PORT=%d without %s_TLS_CERT_FILE/%s_TLS_KEY_FILE is only permitted on a loopback bind (%s_HOST=%q); set the certificate files to serve a public address", name, m.Port, name, name, name, m.Host)
		}
		if m.MaxSessionsPerUser < 1 {
			return fmt.Errorf("%s_MAX_SESSIONS_PER_USER must be at least 1 when %s_ENABLED=true (connection limits are part of the mail-protocol posture)", name, name)
		}
		if m.MaxConnsPerIP < 1 {
			return fmt.Errorf("%s_MAX_CONNS_PER_IP must be at least 1 when %s_ENABLED=true (connection limits are part of the mail-protocol posture)", name, name)
		}
		if m.CommandRateLimit < 1 || m.CommandRateWindow < 1 {
			return fmt.Errorf("%s_COMMAND_RATE_LIMIT and %s_COMMAND_RATE_WINDOW must be at least 1 when %s_ENABLED=true (command rate limits are part of the mail-protocol posture)", name, name, name)
		}
		if m.Host == "" {
			return fmt.Errorf("%s_HOST cannot be empty", name)
		}
	}

	if c.IMAP.Enabled && c.POP3.Enabled {
		if c.IMAP.Port != 0 && c.IMAP.Port == c.POP3.Port {
			return fmt.Errorf("POSTA_IMAP_PORT=%d and POSTA_POP3_PORT=%d collide; one process cannot listen on the same port twice", c.IMAP.Port, c.POP3.Port)
		}
		if c.IMAP.TLSPort != 0 && c.IMAP.TLSPort == c.POP3.TLSPort {
			return fmt.Errorf("POSTA_IMAP_TLS_PORT=%d and POSTA_POP3_TLS_PORT=%d collide; one process cannot listen on the same port twice", c.IMAP.TLSPort, c.POP3.TLSPort)
		}
		for _, pair := range [][2]int{{c.IMAP.Port, c.POP3.TLSPort}, {c.IMAP.TLSPort, c.POP3.Port}} {
			if pair[0] != 0 && pair[0] == pair[1] {
				return fmt.Errorf("POSTA_IMAP/POSTA_POP3 port %d is configured twice; one process cannot listen on the same port twice", pair[0])
			}
		}
	}

	// Same-process listeners the mail ports must not collide with. The HTTP
	// port is always taken; the SMTP listeners only when their feature is on.
	// (The worker's health/metrics port is a different process and not
	// checked here.)
	type gatedPort struct {
		envVar string
		on     bool
		port   int
	}
	taken := []gatedPort{
		{"POSTA_PORT", true, c.Port},
		{"POSTA_INBOUND_SMTP_PORT", c.InboundEnabled, c.InboundSMTPPort},
		{"POSTA_SMTP_RELAY_PORT", c.SMTPRelayEnabled, c.SMTPRelayPort},
		{"POSTA_SUBMISSION_PORT", c.SubmissionEnabled, c.SubmissionPort},
	}
	check := func(label string, port int) error {
		if port == 0 {
			return nil
		}
		for _, tp := range taken {
			if tp.on && tp.port != 0 && tp.port == port {
				return fmt.Errorf("%s=%d collides with %s=%d; one process cannot listen on the same port twice", label, port, tp.envVar, tp.port)
			}
		}
		return nil
	}
	if c.IMAP.Enabled {
		if err := check("POSTA_IMAP_PORT", c.IMAP.Port); err != nil {
			return err
		}
		if err := check("POSTA_IMAP_TLS_PORT", c.IMAP.TLSPort); err != nil {
			return err
		}
	}
	if c.POP3.Enabled {
		if err := check("POSTA_POP3_PORT", c.POP3.Port); err != nil {
			return err
		}
		if err := check("POSTA_POP3_TLS_PORT", c.POP3.TLSPort); err != nil {
			return err
		}
	}
	return nil
}
func (c *Config) validateWorker() error {
	warnRemovedEnvVars()
	return c.ValidateWorker()
}
func (c *Config) Initialize(app *okapi.Okapi) error {
	if err := c.validate(); err != nil {
		return err
	}
	// Initialize global logger
	l := c.initLogger()
	c.WarnInsecureConfig()
	// Dev mode
	if c.DevMode {
		app.WithDebug()
	}
	// Set Port
	app.WithPort(c.Port)
	app.WithLogger(l.Logger)
	app.WithTrustedProxies(c.TrustedProxies...)
	app.WithMaxRequestBody(maxRequestBody)
	_ = goutils.SetEnv("ENV", c.Env)
	apiServers := okapi.Servers{}
	if c.AppWebURL != "" {
		apiServers = append(apiServers, okapi.Server{URL: c.AppWebURL})
	}
	if c.ApiBaseURL != "" {
		apiServers = append(apiServers, okapi.Server{URL: c.ApiBaseURL})
	}
	app.WithCORS(okapi.Cors{
		AllowedOrigins:   c.AllowedCORSOrigins(),
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID", "X-Posta-Workspace-Id"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowCredentials: true,
	})

	if c.OpenAPIDocs {
		// Dashboard / JWT-authenticated endpoints.
		c.securitySchemes = append(c.securitySchemes, okapi.SecurityScheme{
			Name:         "BearerAuth",
			Description:  "Bearer token issued by /auth/login. Send as: `Authorization: Bearer <JWT>`.",
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
		})
		// API-key authenticated endpoints.
		c.securitySchemes = append(c.securitySchemes, okapi.SecurityScheme{
			Name:         "ApiKeyAuth",
			Description:  "Long-lived API key. Send as: `Authorization: Bearer <API_KEY>`. Manage keys under Settings → API Keys.",
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "Posta API Key",
		})
		app.WithOpenAPIDocs(okapi.OpenAPI{
			Title:       "Posta API",
			Version:     Version,
			Description: "Self-hosted email delivery platform for developers and teams.",
			Favicon:     "/favicon.png",
			License: okapi.License{
				Name: "AGPL-3.0-or-later",
				URL:  "https://www.gnu.org/licenses/agpl-3.0.html",
			},
			Contact: okapi.Contact{
				Name:  "Support",
				URL:   "https://goposta.dev/",
				Email: "jonas@goposta.dev",
			},
			Servers:         apiServers,
			SecuritySchemes: c.securitySchemes,
			UI:              okapi.ScalarUI,
			StrictDocUI:     true,
		})
	}
	app.WithErrorHandler(errorhandlers.CustomErrorHandler())
	return nil
}
func (c *Config) InitWorker() error {
	// Initialize global logger
	c.initLogger()
	c.WarnInsecureWorkerConfig()
	return c.validateWorker()
}
func (c *Config) initLogger() *logger.Logger {
	if c.DevMode {
		return logger.New(logger.WithDebugLevel())
	}
	return logger.New(logger.WithJSONFormat(), logger.WithInfoLevel())
}

func (c *Config) InitStorage() {
	var dsn string
	if c.Database.url != "" {
		dsn = c.Database.url
	} else {
		dsn = fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s", c.Database.host, c.Database.user, c.Database.password, c.Database.name, c.Database.port, c.Database.sslMode)
	}
	dbConn, err := storage.ConnectPostgres(dsn)
	if err != nil {
		logger.Fatal("failed to connect to database", "error", err)
	}
	c.Database.DB = dbConn

	redisClient, err := storage.NewRedis(c.Redis.RedisOptions())
	if err != nil {
		logger.Fatal("failed to connect to redis", "error", err)
	}
	c.Redis.Client = redisClient

}

// AllowedCORSOrigins returns POSTA_CORS_ORIGINS when set, otherwise the origins
// of POSTA_WEB_URL and POSTA_API_URL. With none of them set the list is empty
// and only same-origin requests (the embedded dashboard) are served.
func (c *Config) AllowedCORSOrigins() []string {
	if origins := splitList(c.CORSOrigins); len(origins) > 0 {
		return origins
	}
	var origins []string
	for _, raw := range []string{c.AppWebURL, c.ApiBaseURL} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	return origins
}

// maxRequestBody caps JSON request bodies. okapi's 8 MB default is below what
// the send API accepts: email.DefaultMaxTotalSize allows 25 MB of attachments,
// which arrive base64-encoded (~33 MB), plus the message bodies themselves.
const maxRequestBody = 40 << 20

// splitList parses a comma-separated env value, dropping blank entries.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// validProxyEntry mirrors what okapi.WithTrustedProxies accepts.
func validProxyEntry(entry string) bool {
	if _, _, err := net.ParseCIDR(entry); err == nil {
		return true
	}
	return net.ParseIP(entry) != nil
}
