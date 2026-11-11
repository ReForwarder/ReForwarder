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

package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/jkaninda/okapi"

	"github.com/goposta/posta/internal/config"
	cronpkg "github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/cron/jobs"
	"github.com/goposta/posta/internal/logging"
	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/observability"
	"github.com/goposta/posta/internal/routes"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/services/certmanager"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/imap"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/mailauth"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/meticalert"
	"github.com/goposta/posta/internal/services/messages"
	"github.com/goposta/posta/internal/services/mta"
	"github.com/goposta/posta/internal/services/notification"
	"github.com/goposta/posta/internal/services/pop3"
	"github.com/goposta/posta/internal/services/ratelimit"
	"github.com/goposta/posta/internal/services/realtime"
	"github.com/goposta/posta/internal/services/retry"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/services/seeder"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/smtprelay"
	"github.com/goposta/posta/internal/services/tlsrpt"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/services/updatecheck"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/services/workermon"
	workspacesvc "github.com/goposta/posta/internal/services/workspace"
	"github.com/goposta/posta/internal/storage"
	"github.com/goposta/posta/internal/services/workspaceprovision"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/migration"
	"github.com/goposta/posta/internal/storage/migration/upgrade"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/goposta/posta/internal/worker"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi/okapicli"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type serverResources struct {
	producer       *worker.Producer
	cronManager    *cronpkg.Manager
	blobStore      blob.Store
	db             *gorm.DB
	redis          *redis.Client
	smtpInbound    *smtp.Server
	smtpRelay      *smtp.Server
	smtpSubmission *smtp.Server
	// pop3 is the OB-4 W3 POP3 server (ADR-004 decision 2); nil unless
	// POSTA_POP3_ENABLED=true and the listener started.
	pop3 *pop3.Server
	// imap is the OB-4 W2 IMAP4rev1 server (ADR-004 decision 1); nil unless
	// POSTA_IMAP_ENABLED=true and the listener started.
	imap *imap.Server
	// logSinks releases the SF-94 file sink (nil when stdout-only).
	logSinks io.Closer
	// certMgr is the ACME/SNI certificate manager (OB-6 W2, SF-34, ADR-007
	// decision 3); nil unless POSTA_ACME_ENABLED=true and the blob store is
	// configured — a nil manager keeps the static-certificate behavior on
	// every listener. certCancel stops the SF-110 host-allowlist hot-reload
	// loop at shutdown.
	certMgr    *certmanager.Manager
	certCancel context.CancelFunc
	// otlpStop stops the SF-93 OTLP push exporter (OB-6 W5); nil unless
	// POSTA_OTLP_ENDPOINT is set.
	otlpStop func()
}

func runServer(cli *okapicli.CLI) {
	cfg := config.New()
	// OB-6 W5 (GAP-51): error-monitoring hook, installed before anything that
	// can panic or exit fatally. The DB-backed platform-event recorder is
	// attached in OnStart once the database is up; until then the hook only
	// has its webhook side. No-op unless POSTA_ERROR_WEBHOOK_URL is set.
	observability.SetErrorHook(observability.NewErrorHook(
		cfg.ErrorWebhookURL, cfg.ErrorWebhookSecret, "posta", config.Version, nil))
	// ---- P3-3 (SF-94): multi-sink logging (POSTA_LOG_SINKS / POSTA_LOG_FILE).
	// Must run BEFORE cfg.Initialize(app): the structured logger's handler
	// captures os.Stdout at construction, so the file tee has to be in place
	// first. Fail-open: a sink error must not keep the server from starting.
	logSinks, err := logging.Init()
	if err != nil {
		logger.Error("log sink setup failed, logging to stdout only", "error", err)
		logSinks = nil
	}
	app := cli.Okapi()
	if err := cfg.Initialize(app); err != nil {
		observability.CaptureGlobalFatal("Failed to initialize Posta: " + err.Error())
		logger.Fatal("Failed to initialize Posta", "error", err)
	}
	res := &serverResources{logSinks: logSinks}
	if err := cli.RunServer(&okapicli.RunOptions{
		ShutdownTimeout: 30 * time.Second,
		OnStart: func() {
			cfg.InitStorage()
			res.db = cfg.Database.DB
			res.redis = cfg.Redis.Client

			// OB-6 W5 (GAP-51): re-install the error hook with the
			// platform-events recorder, so panics/fatals also land on the
			// admin activity timeline (best-effort) in addition to the
			// POSTA_ERROR_WEBHOOK_URL POST.
			observability.SetErrorHook(observability.NewErrorHook(
				cfg.ErrorWebhookURL, cfg.ErrorWebhookSecret, "posta", config.Version,
				repositories.NewEventRepository(res.db)))

			if err := migration.Run(res.db); err != nil {
				observability.CaptureGlobalFatal("failed to run migrations: " + err.Error())
				logger.Fatal("failed to run migrations", "error", err)
			}

			if err := upgrade.Run(context.Background(), res.db, config.Version, upgrade.Options{
				AllowDowngrade:  cfg.AllowDowngrade,
				PlanEnforcement: cfg.PlanEnforcement,
			}); err != nil {
				observability.CaptureGlobalFatal("failed to run upgrade steps: " + err.Error())
				logger.Fatal("failed to run upgrade steps", "error", err)
			}

			checkDefaultPlan(res.db, cfg)

			// Initialize SMTP password encryption
			crypto.Init(cfg.EncryptionKey)

			// Bound outgoing SMTP dials (R-22): every relay connection this
			// process opens — send worker, retry worker, notifications, relay —
			// honors the dial timeout and the session deadline derived from it.
			email.SetDialTimeout(time.Duration(cfg.SMTPDialTimeoutSecs) * time.Second)

			// Managed DKIM signing (R-02): every SMTP sender this process uses
			// signs messages whose sender domain has an Active+published key.
			// Unset (nil) keeps the bring-your-own-signer CNAME mode.
			email.SetDkimSigner(email.NewDkimSigningService(repositories.NewDomainSigningKeyRepository(res.db)))

			if err := storage.SeedAdmin(res.db, cfg.AdminEmail, cfg.AdminPassword, cfg.ValidateAdminSeedPassword); err != nil {
				observability.CaptureGlobalFatal("failed to seed admin user: " + err.Error())
				logger.Fatal("failed to seed admin user", "error", err)
			}

			seedDefaults(res.db, cfg)

			if ws, err := workspacesvc.EnsureSystem(res.db, cfg.SystemSMTP); err != nil {
				logger.Error("failed to ensure the system workspace", "error", err)
			} else {

				seedWorkspace(res.db, ws.ID, ws.OwnerID)
				if err := workspacesvc.SyncMembers(res.db); err != nil {
					logger.Error("failed to sync system workspace members", "error", err)
				}
			}

			// Initialize blob storage (S3 or filesystem) for attachments
			if cfg.BlobProvider != "" {
				bs, err := blob.New(blob.Config{
					Provider:          cfg.BlobProvider,
					S3Endpoint:        cfg.BlobS3Endpoint,
					S3Region:          cfg.BlobS3Region,
					S3Bucket:          cfg.BlobS3Bucket,
					S3AccessKeyID:     cfg.BlobS3AccessKey,
					S3SecretAccessKey: cfg.BlobS3SecretKey,
					S3UseSSL:          cfg.BlobS3UseSSL,
					S3ForcePathStyle:  cfg.BlobS3PathStyle,
					FSBasePath:        cfg.BlobFSPath,
				})
				if err != nil {
					observability.CaptureGlobalFatal("failed to initialize blob storage: " + err.Error())
					logger.Fatal("failed to initialize blob storage", "error", err)
				}
				res.blobStore = bs
				logger.Info("blob storage initialized", "provider", cfg.BlobProvider)
			}

			// ACME certificate lifecycle (OB-6 W2, SF-34, ADR-007 decision 3):
			// per-domain SNI certificates for the IMAP(993)/POP3(995)
			// implicit-TLS listeners, the submission(587) STARTTLS handshake
			// and the webmail HTTPS path, issued through autocert and cached
			// in the blob store. Off unless POSTA_ACME_ENABLED=true (validate
			// also requires the blob store, which the cache needs). A nil
			// certMgr keeps every listener on the static-certificate behavior.
			if cfg.ACME.Enabled && res.blobStore != nil {
				certCtx, certCancel := context.WithCancel(context.Background())
				cache := certmanager.NewBlobCacheWithPrefix(res.blobStore, cfg.ACME.CachePrefix)
				mgr, mgrErr := certmanager.New(certmanager.Options{
					Email:        cfg.ACME.Email,
					DirectoryURL: cfg.ACME.DirectoryURL,
					Hosts:        cfg.ACME.Domains,
					Cache:        cache,
				})
				if mgrErr != nil {
					// Fail-open to static certificates: log, keep starting.
					certCancel()
					logger.Error("ACME certificate manager unavailable; TLS listeners keep static certificates only", "error", mgrErr)
				} else {
					mgr.SetHostSource(newACMEHostSource(res.db, cfg))
					mgr.StartHostSync(certCtx, time.Duration(cfg.ACME.SyncSecs)*time.Second)
					res.certMgr = mgr
					res.certCancel = certCancel

					// HTTP-01 through the existing HTTP server: only
					// /.well-known/acme-challenge/ paths are intercepted,
					// everything else falls through to the app.
					app.UseMiddleware(func(next http.Handler) http.Handler {
						return mgr.HTTPChallenge(next)
					})

					// Webmail HTTPS path (okapi secondary TLS server — the
					// plain-HTTP listener keeps serving the API and the
					// HTTP-01 responder). Port 0 keeps the webmail path on
					// plain HTTP only.
					if cfg.ACME.HTTPSPort > 0 {
						var webBase *tls.Certificate
						if cfg.ACME.HTTPSCertFile != "" && cfg.ACME.HTTPSKeyFile != "" {
							webBase, mgrErr = certmanager.StaticCertFromFiles(cfg.ACME.HTTPSCertFile, cfg.ACME.HTTPSKeyFile)
							if mgrErr != nil {
								observability.CaptureGlobalFatal("failed to load POSTA_ACME_HTTPS certificate: " + mgrErr.Error())
								logger.Fatal("failed to load webmail HTTPS fallback certificate", "error", mgrErr)
							}
						}
						webAddr := net.JoinHostPort(cfg.ACME.HTTPSHost, strconv.Itoa(cfg.ACME.HTTPSPort))
						app.With(okapi.WithTLSServer(webAddr, mgr.WebListenerTLSConfig(webBase)))
						logger.Info("webmail HTTPS listener configured (ACME SNI map)", "addr", webAddr)
					}
				}
			}

			// Notification service
			userRepo := repositories.NewUserRepository(res.db)
			userSettingRepo := repositories.NewUserSettingRepository(res.db)
			appName := "Posta"
			notifier := notification.NewService(
				cfg.SystemSMTP,
				appName,
				cfg.AppWebURL,
				userRepo,
				userSettingRepo,
				repositories.NewWorkspaceRepository(res.db))
			notifier.SetSMTPRepo(repositories.NewSMTPRepository(res.db))
			if notifier.IsConfigured() {
				logger.Info("system notification service enabled")
			}

			// Bus is shared between the embedded worker (parse handler) and the
			// SMTP server so a single PublishSimple reaches both sets of
			// in-process subscribers.
			var inboundBus *eventbus.EventBus
			if cfg.InboundEnabled && !cfg.DevMode {
				inboundBus = eventbus.New(repositories.NewEventRepository(res.db))
			}

			if !cfg.DevMode {
				res.producer = worker.NewProducer(cfg.Redis.AsynqRedisOpt(), cfg.WorkerMaxRetries)
				if cfg.EmbeddedWorker {
					startEmbeddedWorker(res.db, cfg, res.blobStore, notifier, inboundBus)
				}
			}

			// Security engine: ban enforcement and abuse detection for the
			// HTTP API and both SMTP listeners. The bus stays nil here — the
			// login-failure feed is started by the route wiring.
			var secEng *security.Engine
			if cfg.SecurityEnabled {
				secEng = security.NewEngine(
					res.db,
					res.redis,
					cfg,
					settings.NewProvider(repositories.NewSettingRepository(res.db)),
					nil,
				)
				if err := secEng.Start(context.Background()); err != nil {
					logger.Error("security engine initial resync failed (continuing fail-open)", "error", err)
				}
			}

			if cfg.InboundEnabled && !cfg.DevMode {
				if srv, err := startInboundSMTPServer(res.db, cfg, res.blobStore, res.producer, inboundBus, secEng); err != nil {
					logger.Error("failed to start inbound SMTP server", "error", err)
				} else {
					res.smtpInbound = srv
				}
			}

			if !cfg.DevMode {
				res.cronManager = initCronManager(res.db, cfg, notifier, res.blobStore, res.producer)
			}

			emailSvc := routes.InitRoutes(app, res.db,
				res.redis,
				cfg,
				secEng,
				res.producer,
				res.cronManager,
				res.blobStore,
				context.Background(), notifier)

			// SF-31 outbound half (OB-6 W4, ADR-007 §5): the daily TLS report
			// pass over our own delivery aggregates. Inert until the operator
			// sets POSTA_MTA_STS_ORG (reports are never sent without an
			// explicit generator identity); reports go ONLY to rua addresses
			// read from each remote policy domain's live _smtp._tls TXT record.
			if res.cronManager != nil && cfg.MTASTSOrgDomain != "" {
				sender := newTLSRPTSender(emailSvc, cfg)
				res.cronManager.Register(jobs.NewTLSRPTReportJob(
					res.db,
					tlsrpt.NewAggregator(res.db),
					sender,
					"mta-sts-report@"+cfg.MTASTSOrgDomain,
					cfg.MTASTSOrgDomain,
				))
			}

			if cfg.SMTPRelayEnabled && !cfg.DevMode {
				if srv, err := startSMTPRelayServer(res.db, cfg, emailSvc, secEng); err != nil {
					observability.CaptureGlobalFatal("failed to start SMTP relay server: " + err.Error())
					logger.Fatal("failed to start SMTP relay server", "error", err)
				} else {
					res.smtpRelay = srv
				}
			}

			// Submission listener (SF-03, gated; ADR-001 §10): MSA on port
			// 587 feeding accepted messages into the same pipeline as API
			// sends. Off by default (POSTA_SUBMISSION_ENABLED).
			if cfg.SubmissionEnabled && !cfg.DevMode {
				if srv, err := startSubmissionServer(res.db, cfg, emailSvc, secEng, res.certMgr); err != nil {
					logger.Error("failed to start submission server", "error", err)
				} else {
					res.smtpSubmission = srv
				}
			}

			// POP3 server (OB-4 W3, ADR-004 decision 2): classic POP3 against
				// the mailstore INBOX, app-password-only auth, TLS enforced
				// (STLS required on the plaintext port; implicit TLS on 995 when
				// POSTA_POP3_TLS_PORT is served). Off by default
				// (POSTA_POP3_ENABLED).
				if cfg.POP3.Enabled && !cfg.DevMode {
					if srv, err := startPOP3Server(res.db, cfg, res.blobStore, secEng, res.certMgr); err != nil {
						logger.Error("failed to start POP3 server", "error", err)
					} else {
						res.pop3 = srv
					}
				}

				// IMAP4rev1 server (OB-4 W2, ADR-004 decision 1): the ADR-004
				// command subset over the mailstore, app-password-only auth,
				// TLS enforced (STARTTLS required before auth on 143; implicit
				// TLS on 993 when POSTA_IMAP_TLS_PORT is served). Off by
				// default (POSTA_IMAP_ENABLED).
				if cfg.IMAP.Enabled && !cfg.DevMode {
					if srv, err := startIMAPServer(res.db, cfg, res.blobStore, secEng, res.redis, res.certMgr); err != nil {
						logger.Error("failed to start IMAP server", "error", err)
					} else {
						res.imap = srv
					}
				}

			if res.cronManager != nil {
				res.cronManager.Start(context.Background())
			}

			// OB-6 W5 (SF-93): OTLP push exporter shipping the existing
			// Prometheus families to an OTLP/HTTP collector. Off unless
			// POSTA_OTLP_ENDPOINT is set; a misconfigured endpoint logs and
			// skips — it must not take the server down.
			if cfg.OTLPEndpoint != "" {
				headers, hdrErr := observability.ParseOTLPHeaders(cfg.OTLPHeaders)
				if hdrErr != nil {
					logger.Error("OTLP header config invalid, exporter not started", "error", hdrErr)
				} else {
					stop, startErr := observability.StartOTLPPusher(context.Background(), observability.OTLPOptions{
						Endpoint:       cfg.OTLPEndpoint,
						Interval:       time.Duration(cfg.OTLPIntervalSecs) * time.Second,
						Headers:        headers,
						ServiceName:    "posta",
						ServiceVersion: config.Version,
					})
					if startErr != nil {
						logger.Error("OTLP exporter failed to start", "error", startErr)
					} else {
						res.otlpStop = stop
						logger.Info("OTLP push exporter started", "endpoint", cfg.OTLPEndpoint, "interval_secs", cfg.OTLPIntervalSecs)
					}
				}
			}

			if !cfg.DevMode {
				startRetryWorker(res.db, cfg, res.producer)
			}

			if cfg.DevMode {
				logger.Info("running in development mode - emails will NOT be sent")
			}
		},
		OnStarted: func() {
			logger.Info("Posta Server started", "version", config.Version, "port", cfg.Port)
		},
		OnShutdown: func() {
			shutdownServer(res)
		},
	}); err != nil {
		observability.CaptureGlobalFatal("server error: " + err.Error())
		logger.Fatal("server error", "error", err)
	}
}

func checkDefaultPlan(db *gorm.DB, cfg *config.Config) {
	if !cfg.PlanEnforcement {
		return
	}
	if _, err := repositories.NewPlanRepository(db).FindDefault(); err != nil {
		logger.Warn("plan enforcement is enabled but no default plan is set — " +
			"personal-workspace migration will fail until one is configured via PATCH /admin/plans/{id}/default")
	}
}

// newSeeder builds the default-content seeder. Cheap to construct: it holds
// repositories and no state, so callers make one rather than threading it.
func newSeeder(db *gorm.DB) *seeder.Seeder {
	return seeder.New(
		repositories.NewTemplateRepository(db),
		repositories.NewStyleSheetRepository(db),
		repositories.NewTemplateVersionRepository(db),
		repositories.NewTemplateLocalizationRepository(db),
		repositories.NewLanguageRepository(db),
	)
}

func seedWorkspace(db *gorm.DB, workspaceID, ownerID uint) {
	ownerName := ""
	if owner, err := repositories.NewUserRepository(db).FindByID(ownerID); err == nil && owner != nil {
		ownerName = owner.Name
	}
	newSeeder(db).SeedWorkspaceDefaults(workspaceID, ownerID, ownerName)
}

func seedDefaults(db *gorm.DB, cfg *config.Config) {
	userRepo := repositories.NewUserRepository(db)
	admin, err := userRepo.FindByEmail(cfg.AdminEmail)
	if err != nil || admin == nil {
		return
	}
	s := newSeeder(db)
	migrator := workspaceprovision.New(cfg.PlanEnforcement)
	migrator.SetSeeder(s)
	if _, err := migrator.EnsureWorkspace(db, admin.ID); err != nil {
		logger.Error("failed to provision admin personal workspace", "error", err)
	}
}

// newWebhookDispatcher creates a configured webhook dispatcher with metrics hooks.
func newWebhookDispatcher(db *gorm.DB, cfg *config.Config) *webhook.Dispatcher {
	webhookRepo := repositories.NewWebhookRepository(db)
	whDeliveryRepo := repositories.NewWebhookDeliveryRepository(db)
	dispatcher := webhook.NewDispatcher(webhookRepo)
	dispatcher.SetDeliveryRepo(whDeliveryRepo)
	dispatcher.SetAttemptRepo(repositories.NewWebhookDeliveryAttemptRepository(db))
	dispatcher.SetConfig(webhookConfig(cfg))
	dispatcher.OnDeliverySuccess(func() { metrics.IncrementWebhookDelivery("success") })
	dispatcher.OnDeliveryFailed(func() { metrics.IncrementWebhookDelivery("failed") })
	dispatcher.OnDeliveryDone(metrics.ObserveWebhookDeliveryDuration)
	dispatcher.OnEventsDropped(metrics.IncrementWebhookEventsDropped)
	return dispatcher
}

// newAutomationEmailService builds the email service the automation engine's
// email nodes send through. It mirrors the HTTP route wiring (settings, template
// versions, domain verification, link generator, tracking, message-history
// recorder) so automation mail renders and behaves like platform mail, with its
// own instance so the HTTP path's metrics callbacks stay independent. Plan
// limits are intentionally unset: attachment limits fall back to the global
// settings and the rate limiter keys on the recipient address.
func newAutomationEmailService(
	db *gorm.DB, cfg *config.Config, producer *worker.Producer, blobStore blob.Store,
) *email.Service {
	limiter := ratelimit.NewRedisLimiter(cfg.Redis.Client, cfg.RateLimitHourly, cfg.RateLimitDaily)
	svc := email.NewService(
		repositories.NewEmailRepository(db),
		repositories.NewSMTPRepository(db),
		repositories.NewTemplateRepository(db),
		repositories.NewSuppressionRepository(db),
		limiter,
		newWebhookDispatcher(db, cfg),
		cfg.DevMode,
	)
	settingsProvider := settings.NewProvider(repositories.NewSettingRepository(db))
	svc.SetSettings(settingsProvider)
	svc.SetVersionRepos(repositories.NewTemplateVersionRepository(db),
		repositories.NewTemplateLocalizationRepository(db))
	svc.SetDomainVerification(repositories.NewDomainRepository(db),
		repositories.NewUserRepository(db), repositories.NewWorkspaceSettingRepository(db))
	svc.SetUnsubscribeListRepo(repositories.NewUnsubscribeListRepository(db))
	svc.SetContactRepo(repositories.NewContactRepository(db))
	if producer != nil {
		svc.SetEnqueuer(producer)
	}
	if blobStore != nil {
		svc.SetBlobStore(blobStore)
	}
	trackingService := tracking.NewService(repositories.NewTrackingRepository(db), cfg.AppWebURL, []byte(cfg.JWTSecret))
	svc.SetLinkGenerator(trackingService)
	svc.SetTracking(trackingService, settingsProvider.EmailTrackingEnabled)
	svc.SetRecorder(emailhistory.NewRecorder(db))
	return svc
}

func newInboundServiceForWorker(db *gorm.DB,
	cfg *config.Config,
	blobStore blob.Store,
	producer *worker.Producer,
	bus *eventbus.EventBus) *inbound.Service {
	svc := inbound.NewService(
		repositories.NewInboundEmailRepository(db),
		repositories.NewDomainRepository(db),
		repositories.NewSuppressionRepository(db),
		inbound.Config{
			MaxMessageSize:    cfg.InboundMaxMessageSize,
			MaxAttachmentSize: cfg.InboundMaxAttachSize,
		},
	)
	if blobStore != nil {
		svc.SetBlobStore(blobStore)
	}
	if producer != nil {
		svc.SetEnqueuer(producer)
	}
	if bus != nil {
		svc.SetEventBus(bus)
	}
	attachThreadRouter(svc, db, cfg, producer)
	attachDMARC(svc, db)
	attachTLSRPT(svc, db)
	attachVerification(svc, db, cfg)
	return svc
}

// attachThreadRouter routes replies sent to form thread addresses
// (msg+<token>@POSTA_MESSAGES_INBOUND_DOMAIN) onto their message instead of
// the generic inbound pipeline. Every inbound service that parses mail needs
// it; otherwise a reply parsed by that process would be handled as loose mail.
func attachThreadRouter(svc *inbound.Service, db *gorm.DB, cfg *config.Config, producer *worker.Producer) {
	if !cfg.MessagesEnabled || cfg.MessagesInboundDomain == "" {
		return
	}
	router := messages.NewThreadRouter(repositories.NewMessageRepository(db), cfg.MessagesInboundDomain)
	router.SetRealtime(realtime.NewRedis(cfg.Redis.Client))
	if producer != nil {
		router.SetEnqueuer(producer)
	}
	svc.SetThreadRouter(router)
}

// attachDMARC wires DMARC aggregate-report ingestion (R-17) into every inbound
// service that parses mail. The receiver itself stays default-off; when it
// runs, reports mailed to the tenants' rua: addresses are parsed and stored
// beside the message. The processor contains its own errors — ingestion must
// never fail because DMARC parsing did.
func attachDMARC(svc *inbound.Service, db *gorm.DB) {
	svc.EnableDMARC(db)
}

// attachTLSRPT wires SMTP TLS report ingestion (SF-31 inbound half, OB-6 W4,
// ADR-007 §5) into every inbound service that parses mail — the R-17 DMARC
// pattern with the dedicated tlsrpt@ rua allowlist. The processor contains
// its own errors — ingestion must never fail because TLS-report parsing did.
func attachTLSRPT(svc *inbound.Service, db *gorm.DB) {
	svc.EnableTLSRPT(db)
	svc.SetTLSRPTAddress(inbound.NewTlsRptAllowlist())
}

// attachVerification wires message-level SPF/DKIM/DMARC verification
// (SF-22/23/24, ADR-002 Phase-3 slice) into every inbound service that parses
// mail. Gated by POSTA_INBOUND_VERIFY (default true); verdicts persist
// asynchronously into inbound_auth_results and never block delivery.
func attachVerification(svc *inbound.Service, db *gorm.DB, cfg *config.Config) {
	svc.EnableVerification(db, cfg.InboundHostname)
}

// newInboundFilter builds the spam/malware step (OB-2, ADR-006): the rules
// baseline scorer over the persisted auth verdicts + allowlisted DNSBL zones
// (POSTA_SPAM_DNSBL_ZONES) + email_events sender reputation, the clamd
// malware client (POSTA_CLAMAV_*, disabled without POSTA_CLAMAV_HOST), and
// the optional synchronous external filter consult
// (POSTA_SPAM_EXTERNAL_FILTER_*). Score thresholds come from the admin
// platform settings (spam_score_quarantine / spam_score_reject). Returns nil
// when POSTA_SPAM_FILTER_ENABLED=false — every handler then keeps the
// pre-filter behavior.
func newInboundFilter(db *gorm.DB, cfg *config.Config, svc *inbound.Service, blobStore blob.Store, dispatcher *webhook.Dispatcher) *worker.InboundFilter {
	if !cfg.SpamFilterEnabled {
		return nil
	}
	thresholds := settings.NewProvider(repositories.NewSettingRepository(db))
	scorer := spamfilter.NewRulesScorer(
		thresholds,
		spamfilter.NewDNSBLChecker(cfg.SpamDNSBLZones, nil),
		spamfilter.NewEventReputationSource(db),
	)
	scanner := spamfilter.NewClamdClient(spamfilter.ClamdConfig{
		Host:     cfg.ClamAVHost,
		Port:     cfg.ClamAVPort,
		Timeout:  cfg.ClamAVTimeout,
		FailMode: spamfilter.ClamFailModeFromEnv(cfg.ClamAVFailMode),
	})
	return worker.NewInboundFilter(
		repositories.NewInboundEmailRepository(db),
		svc,
		blobStore,
		scorer,
		scanner,
		dispatcher,
		worker.InboundFilterOptions{
			Enabled:             true,
			ExternalHook:        cfg.SpamExternalFilter,
			ExternalHookEvent:   cfg.SpamExternalFilterEvent,
			ExternalHookTimeout: cfg.SpamExternalFilterTO,
		},
	)
}

// startInboundSMTPServer configures and launches the built-in SMTP receiver.
// Returns the server so it can be gracefully shut down on exit.
func startInboundSMTPServer(
	db *gorm.DB,
	cfg *config.Config,
	blobStore blob.Store,
	producer *worker.Producer,
	bus *eventbus.EventBus,
	secEng *security.Engine,
) (*smtp.Server, error) {
	inboundRepo := repositories.NewInboundEmailRepository(db)
	domainRepo := repositories.NewDomainRepository(db)
	suppressionRepo := repositories.NewSuppressionRepository(db)

	svc := inbound.NewService(
		inboundRepo,
		domainRepo,
		suppressionRepo,
		inbound.Config{
			MaxMessageSize:    cfg.InboundMaxMessageSize,
			MaxAttachmentSize: cfg.InboundMaxAttachSize,
		},
	)
	if blobStore != nil {
		svc.SetBlobStore(blobStore)
	}
	if producer != nil {
		svc.SetEnqueuer(producer)
	}
	svc.SetEventBus(bus)
	svc.OnReceived(func(src models.InboundSource) { metrics.IncrementInboundReceived(string(src)) })
	svc.OnRejected(metrics.IncrementInboundRejected)
	svc.OnBytes(metrics.AddInboundBytes)
	svc.OnIngestDuration(metrics.ObserveInboundIngestDuration)
	attachDMARC(svc, db)
	attachVerification(svc, db, cfg)

	// OB-2 (ADR-002): routing rules resolve at RCPT/DATA and their actions
	// run after durable ingest. The same engine instance backs the SMTP
	// backend's recipient validation and the service's ingest-time re-check.
	routeRouter := inbound.NewRouter(repositories.NewInboundRouteRepository(db))
	svc.SetRouter(routeRouter)
	svc.SetForwardQueue(repositories.NewInboundForwardTaskRepository(db))
	svc.SetWebhookDispatcher(newWebhookDispatcher(db, cfg))
	// OB-2 (ADR-002 decision 3): quarantine holds persist through the
	// quarantine store on the SMTP path too.
	svc.SetQuarantineStore(inbound.NewQuarantineService(
		repositories.NewInboundQuarantineRepository(db),
		inboundRepo,
		domainRepo,
		svc,
	))

	// OB-3 (ADR-003, flow F13): a matched route action=deliver additionally
	// files the message into the recipient mailbox's INBOX through the
	// mailstore (quota-enforced; over-quota → quarantine with a typed
	// reason). The seam needs the blob store and the at-rest encryption key —
	// the production ZstdBlobCodec refuses to store unencrypted (SF-78).
	// Without either, delivery stays archive-only: exactly the pre-OB-3
	// behavior, with a visible log line instead of a silent gap.
	if blobStore != nil && cfg.EncryptionKey != "" {
		if msSvc, err := mailstore.New(db, mailstore.ZstdBlobCodec{}, blobStore, nil); err != nil {
			logger.Error("mailstore service unavailable; inbound delivery stays archive-only", "error", err)
		} else {
			svc.SetMailboxDeliverer(inbound.NewMailboxDelivery(msSvc, domainRepo, inbound.MailboxDeliveryConfig{
				AutoProvision: cfg.MailstoreAutoProvision,
			}))
			logger.Info("hosted-mailbox delivery enabled", "auto_provision", cfg.MailstoreAutoProvision)
		}
	} else {
		logger.Warn("hosted-mailbox delivery disabled (needs blob store + POSTA_ENCRYPTION_KEY); deliver routes stay archive-only")
	}

	// OB-2 (ADR-002 decision 4): the VERP return-path feature — bounce
	// addresses are accepted through a dedicated RCPT allowlist, DSN bounces
	// are durably ingested as system messages, and RFC 3464 reports are
	// matched back to the original message. Off entirely unless
	// POSTA_RETURN_PATH_SUBDOMAIN and POSTA_RETURN_PATH_SECRET are set.
	verp := inbound.NewVerp(inbound.VerpConfig{
		Subdomain: cfg.ReturnPathSubdomain,
		Secret:    cfg.ReturnPathSecret,
	})
	svc.SetVerp(verp)
	svc.EnableDSN(db)

	backend := inbound.NewBackend(svc, domainRepo, cfg.InboundMaxMessageSize)
	backend.SetRouter(routeRouter)
	backend.SetVerp(verp)
	// SF-31 (OB-6 W4): the receiver accepts tlsrpt@<verified-domain> report
	// addresses through the dedicated allowlist (same pattern as the VERP
	// bounce host).
	backend.SetTLSRPT(inbound.NewTlsRptAllowlist())
	if cfg.InboundSMTPRateLimit > 0 {
		window := time.Duration(cfg.InboundSMTPRateWindow) * time.Second
		backend.SetRateLimiter(inbound.NewIPRateLimiter(cfg.InboundSMTPRateLimit, window))
	}
	backend.SetSecurity(secEng)
	srv, err := inbound.NewSMTPServer(backend, inbound.SMTPConfig{
		Host:           cfg.InboundSMTPHost,
		Port:           cfg.InboundSMTPPort,
		Hostname:       cfg.InboundHostname,
		MaxMessageSize: cfg.InboundMaxMessageSize,
		TLSMode:        cfg.InboundTLSMode,
		TLSCertFile:    cfg.InboundTLSCertFile,
		TLSKeyFile:     cfg.InboundTLSKeyFile,
	})
	if err != nil {
		return nil, err
	}

	go func() {
		logger.Info("inbound SMTP server listening", "addr", srv.Addr, "tls_mode", cfg.InboundTLSMode)
		// Serve on a security-wrapped listener instead of ListenAndServe:
		// the CountingListener enforces the ban snapshot at TCP accept and
		// records connect/abort signals. Inbound TLS is STARTTLS (upgraded
		// in-band by go-smtp), so the raw listener stays unwrapped.
		l, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			logger.Error("inbound SMTP server error", "error", err)
			return
		}
		listener := security.NewCountingListener(l, secEng, cfg.InboundSMTPPort, "smtp")
		err = srv.Serve(listener)
		if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, smtp.ErrServerClosed) {
			logger.Error("inbound SMTP server error", "error", err)
		}
	}()
	return srv, nil
}

// startSMTPRelayServer configures and launches the built-in SMTP Relay
// (AUTH-gated relay) listener. Returns the server so it can be
// gracefully shut down on exit.
func startSMTPRelayServer(
	db *gorm.DB, cfg *config.Config, emailSvc *email.Service, secEng *security.Engine,
) (*smtp.Server, error) {
	credRepo := repositories.NewSMTPCredentialRepository(db)
	userRepo := repositories.NewUserRepository(db)
	backend := smtprelay.NewBackend(credRepo, userRepo, emailSvc, cfg.SMTPRelayMaxMessageSize)
	if cfg.SMTPRelayRateLimit > 0 {
		window := time.Duration(cfg.SMTPRelayRateWindow) * time.Second
		backend.SetRateLimiter(inbound.NewIPRateLimiter(cfg.SMTPRelayRateLimit, window))
	}
	backend.SetSecurity(secEng)
	srv, err := smtprelay.NewSMTPServer(backend, smtprelay.SMTPConfig{
		Host:           cfg.SMTPRelayHost,
		Port:           cfg.SMTPRelayPort,
		Hostname:       cfg.SMTPRelayHostname,
		MaxMessageSize: cfg.SMTPRelayMaxMessageSize,
		TLSMode:        cfg.SMTPRelayTLSMode,
		TLSCertFile:    cfg.SMTPRelayTLSCertFile,
		TLSKeyFile:     cfg.SMTPRelayTLSKeyFile,
	})
	if err != nil {
		return nil, err
	}

	go func() {
		logger.Info("SMTP relay server listening", "addr", srv.Addr)
		// Serve on a security-wrapped listener instead of ListenAndServe:
		// the CountingListener enforces the ban snapshot at TCP accept and
		// records connect/abort signals. The relay's TLS wiring is
		// STARTTLS-only (clients connect in plaintext and upgrade in-band),
		// so no listener-level TLS wrap applies here.
		l, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			logger.Error("SMTP relay server error", "error", err)
			return
		}
		listener := security.NewCountingListener(l, secEng, cfg.SMTPRelayPort, "smtp")
		err = srv.Serve(listener)
		if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, smtp.ErrServerClosed) {
			logger.Error("SMTP relay server error", "error", err)
		}
	}()
	return srv, nil
}

// newTLSRPTSender adapts the email service's system path onto the TLS-RPT
// report sender seam (SF-31 outbound half, OB-6 W4): the report rides the
// standard outbound pipeline as system-provenance mail — queued, signed,
// retried with the standard backoff — never through the tenant send path
// (no suppression, quota or marketing limits apply to it, mirroring the
// forwarding posture). The document is attached gzipped per RFC 8460 §7.3.
func newTLSRPTSender(emailSvc *email.Service, cfg *config.Config) tlsrpt.Sender {
	return tlsrpt.SenderFunc(func(ctx context.Context, req *tlsrpt.OutboundReportRequest) error {
		filename, body, err := tlsrpt.GzipAttachment(req)
		if err != nil {
			return err
		}
		_, err = emailSvc.SendSystemForward(ctx, &email.SystemForwardRequest{
			From: req.From,
			To:   req.To,
			Subject: fmt.Sprintf("Report Domain: %s Submitter: %s Report-ID: %s",
				req.PolicyDomain, cfg.MTASTSOrgDomain, req.ReportID),
			Text: tlsrpt.ReportText(req),
			Attachments: []models.Attachment{{
				Filename:    filename,
				Content:     base64.StdEncoding.EncodeToString(body),
				ContentType: "application/tlsrpt+json",
			}},
			Metadata: map[string]any{
				"tlsrpt":        true,
				"policy_domain": req.PolicyDomain,
				"report_id":     req.ReportID,
			},
		})
		return err
	})
}

// newMTAExecutor builds the outbound MTA executor (OB-1, ADR-001): system-DNS
// MX resolver with a 30s cache, a sliding-hour per-domain budget, and the
// durable attempt-record repository. The EHLO name comes from
// POSTA_MTA_EHLO_NAME (guidance: point it at the hostname whose PTR matches
// the outbound bind IP). Domains left in the default 'relay' mode never touch
// the executor.
func newMTAExecutor(db *gorm.DB, cfg *config.Config) *mta.Executor {
	executor := mta.NewExecutor(mta.NewResolver(), mta.NewBudgetKeeper(time.Hour), repositories.NewMTADeliveryAttemptRepository(db))
	executor.SetHeloDomain(cfg.MTAEhloName)
	// SF-29 (OB-6 W4, ADR-007 §5): outbound MTA-STS enforcement for direct
	// delivery — the runner consults each recipient domain's published
	// policy through the process-wide hook (deliver.go's opts literal is
	// untouched; the hook only tightens behavior for domains that publish
	// mode: enforce, and discovery failures fall open per RFC 8461 §6.1).
	mta.SetDefaultSTS(mta.NewEnforcer(mta.NewPolicyClient()))
	return executor
}

// newACMEHostSource builds the certificate-target allowlist snapshot source
// (OB-6 W2, SF-34/ADR-007 decision 3): the ownership-verified tenant domains
// (ADR-008 decision 2 — unverified domains are refused as certificate
// targets) plus the operator's explicit POSTA_ACME_DOMAINS list (mail
// subdomains like mail.example.com are not tenant rows). The certmanager
// hot-reload loop (SF-110) calls this on its sync interval, so newly
// verified domains join the SNI map without a restart.
func newACMEHostSource(db *gorm.DB, cfg *config.Config) func(ctx context.Context) []string {
	return func(ctx context.Context) []string {
		hosts := make([]string, 0, len(cfg.ACME.Domains)+8)
		hosts = append(hosts, cfg.ACME.Domains...)
		verified := true
		domains, _, err := repositories.NewDomainRepository(db).FindAllFiltered(
			repositories.AdminDomainFilter{Verified: &verified}, 10000, 0)
		if err != nil {
			// Keep the previous allowlist on a transient read failure: the
			// refresh loop logs and retries on the next tick.
			logger.Warn("ACME host source read failed; keeping the previous allowlist", "error", err)
			return nil
		}
		for _, d := range domains {
			if d.OwnershipVerified && d.Domain != "" {
				hosts = append(hosts, d.Domain)
			}
		}
		return hosts
	}
}

// startSubmissionServer launches the gated SMTP submission listener (SF-03,
// ADR-001 §10): AUTH against SMTP relay credentials, tenant-verified sender
// domains, validated recipients — no open relay. Accepted messages enter the
// same pipeline as API sends through the email service.
//
// certMgr (nil unless POSTA_ACME_ENABLED=true, OB-6 W2/SF-34) serves the
// STARTTLS handshake through the SNI certificate map when the listener runs
// starttls mode without a static certificate pair.
func startSubmissionServer(
	db *gorm.DB, cfg *config.Config, emailSvc *email.Service, secEng *security.Engine,
	certMgr *certmanager.Manager,
) (*smtp.Server, error) {
	verifier := mta.NewRelayCredentialVerifier(
		repositories.NewSMTPCredentialRepository(db),
		repositories.NewUserRepository(db),
	)
	domains := mta.NewDomainSenderChecker(repositories.NewDomainRepository(db))
	sink := &mta.EmailServiceSink{Service: emailSvc}
	backend, err := mta.NewSubmissionBackend(verifier, domains, sink, cfg.SubmissionMaxMessageSize)
	if err != nil {
		return nil, err
	}
	if cfg.SubmissionRateLimit > 0 {
		window := time.Duration(cfg.SubmissionRateWindow) * time.Second
		backend.SetRateLimiter(inbound.NewIPRateLimiter(cfg.SubmissionRateLimit, window))
	}
	backend.SetSecurity(secEng)
	srv, err := mta.NewSubmissionServer(backend, mta.SubmissionConfig{
		Host:           cfg.SubmissionHost,
		Port:           cfg.SubmissionPort,
		Hostname:       cfg.SubmissionHostname,
		MaxMessageSize: cfg.SubmissionMaxMessageSize,
		TLSMode:        cfg.SubmissionTLSMode,
		TLSCertFile:    cfg.SubmissionTLSCertFile,
		TLSKeyFile:     cfg.SubmissionTLSKeyFile,
		// OB-6 W2 (SF-34, ADR-007 decision 3): the ACME SNI map serves the
		// STARTTLS handshake when no static certificate pair is configured.
		// Submission is STARTTLS in this tree (RFC 8314); the manager method
		// returns nil for a nil manager, keeping the static-only behavior.
		TLSGetCertificate: certMgr.SubmissionTLSGetCert(nil),
	})
	if err != nil {
		return nil, err
	}

	go func() {
		logger.Info("submission server listening", "addr", srv.Addr, "tls_mode", cfg.SubmissionTLSMode)
		// Same security-wrapped listener arrangement as the relay: the
		// CountingListener enforces the ban snapshot at TCP accept and records
		// connect/abort signals; TLS (when enabled) is STARTTLS in-band.
		l, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			logger.Error("submission server error", "error", err)
			return
		}
		listener := security.NewCountingListener(l, secEng, cfg.SubmissionPort, "smtp")
		err = srv.Serve(listener)
		if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, smtp.ErrServerClosed) {
			logger.Error("submission server error", "error", err)
		}
	}()
	return srv, nil
}

// startPOP3Server configures and launches the POP3 server (OB-4 W3,
// ADR-004 decision 2): classic POP3 against the mailstore INBOX,
// authenticated with app passwords only via the W1 mailauth seam (the
// primary account password is structurally never consulted). The plaintext
// listener (POSTA_POP3_PORT, default 110) serves STLS when a certificate is
// configured and refuses authentication before the upgrade; the implicit-TLS
// listener (POSTA_POP3_TLS_PORT, default 995) always runs TLS and is served
// only with a certificate. Returns the server so shutdown can release it.
func startPOP3Server(
	db *gorm.DB,
	cfg *config.Config,
	blobStore blob.Store,
	secEng *security.Engine,
	certMgr *certmanager.Manager,
) (*pop3.Server, error) {
	mcfg := cfg.POP3
	// Message bodies live in content-addressed encrypted blobs (ADR-003);
	// the production ZstdBlobCodec refuses to store unencrypted (SF-78), so
	// a deployment without the blob store or the at-rest key cannot serve
	// mail at all — refuse to start rather than expose empty maildrops.
	if blobStore == nil || cfg.EncryptionKey == "" {
		return nil, errors.New("POP3 requires the blob store and POSTA_ENCRYPTION_KEY (message bodies are stored encrypted)")
	}
	msSvc, err := mailstore.New(db, mailstore.ZstdBlobCodec{}, blobStore, nil)
	if err != nil {
		return nil, err
	}

	auth := mailauth.NewAuthenticator(
		repositories.NewUserRepository(db),
		repositories.NewUserAppPasswordRepository(db),
	)
	if auth == nil {
		return nil, errors.New("POP3 authenticator could not be built (nil mailauth dependency)")
	}

	// Audit events land in the events substrate (ADR-004 decision 6).
	auditSink := pop3.NewEventAuditLogger(eventbus.New(repositories.NewEventRepository(db)))

	srv, err := pop3.NewServer(pop3.Config{
		Hostname:           mcfg.Hostname,
		MaxSessionsPerUser: mcfg.MaxSessionsPerUser,
		MaxConnsPerIP:      mcfg.MaxConnsPerIP,
		CommandRateLimit:   mcfg.CommandRateLimit,
		CommandRateWindow:  time.Duration(mcfg.CommandRateWindow) * time.Second,
		// Cleartext authentication is allowed only when the plaintext
		// listener serves no TLS at all — a configuration validate()
		// confines to loopback binds; the server additionally checks the
		// peer address at runtime. With a certificate present, credentials
		// are accepted only after STLS (or on the implicit-TLS port).
		AllowCleartextAuth: mcfg.TLSCertFile == "" || mcfg.TLSKeyFile == "",
	}, msSvc, auth)
	if err != nil {
		return nil, err
	}
	srv.SetAudit(auditSink)
	srv.SetSecurity(secEng)

	// TLS wiring (OB-6 W2, SF-34/ADR-007 decision 3): with the ACME manager
	// present, the implicit-TLS listener serves the per-domain SNI map —
	// static certificate first for the names it covers, ACME-issued
	// certificates for the map's hosts, the static certificate as the
	// fallback otherwise. Without a static certificate at all, the SNI map
	// alone serves the listener (ACME-only). The plaintext/STLS listener
	// shares the same configuration.
	var tlsCfg *tls.Config
	switch {
	case mcfg.TLSCertFile != "" && mcfg.TLSKeyFile != "":
		base, certErr := tls.LoadX509KeyPair(mcfg.TLSCertFile, mcfg.TLSKeyFile)
		if certErr != nil {
			return nil, fmt.Errorf("load POP3 TLS cert: %w", certErr)
		}
		baseCert := base
		if certMgr != nil {
			// Parse the leaf so SNI coverage checks can read the SANs.
			parsed, parseErr := certmanager.StaticCertFromFiles(mcfg.TLSCertFile, mcfg.TLSKeyFile)
			if parseErr != nil {
				return nil, fmt.Errorf("load POP3 TLS cert: %w", parseErr)
			}
			baseCert = *parsed
			tlsCfg = certMgr.MailListenerTLSConfig(parsed)
		} else {
			tlsCfg = &tls.Config{
				Certificates: []tls.Certificate{baseCert},
				ServerName:   mcfg.Hostname,
				MinVersion:   tls.VersionTLS12,
			}
		}
		srv.SetTLSConfig(tlsCfg)
	case certMgr != nil && mcfg.TLSPort > 0:
		// ACME-only implicit-TLS listener: every certificate comes from the
		// SNI map; unknown SNI fails the handshake (fail-closed).
		tlsCfg = certMgr.MailListenerTLSConfig(nil)
		srv.SetTLSConfig(tlsCfg)
	case mcfg.TLSPort > 0:
		return nil, errors.New("POP3 TLS port configured without POSTA_POP3_TLS_CERT_FILE/POSTA_POP3_TLS_KEY_FILE (implicit TLS serves no cleartext)")
	}

	// Plaintext/STARTTLS listener: banned peers are dropped at accept by the
	// CountingListener; STLS upgrades happen in-session.
	if mcfg.Port > 0 {
		l, err := net.Listen("tcp", net.JoinHostPort(mcfg.Host, strconv.Itoa(mcfg.Port)))
		if err != nil {
			return nil, err
		}
		listener := security.NewCountingListener(l, secEng, mcfg.Port, "pop3")
		go func() {
			logger.Info("POP3 server listening", "addr", listener.Addr().String(),
				"tls", map[bool]string{true: "stls", false: "none"}[tlsCfg != nil])
			if err := srv.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Error("POP3 server error", "error", err)
			}
		}()
	}

	// Implicit-TLS listener (995): the eager-handshake TLSServerListener
	// hands over established *tls.Conn sessions (same stack as the SMTP
	// receivers' TLS wiring).
	if mcfg.TLSPort > 0 && tlsCfg != nil {
		l, err := net.Listen("tcp", net.JoinHostPort(mcfg.Host, strconv.Itoa(mcfg.TLSPort)))
		if err != nil {
			return nil, err
		}
		listener := security.NewCountingListener(l, secEng, mcfg.TLSPort, "pop3")
		tlsListener := security.TLSServerListener(listener, tlsCfg, secEng, mcfg.TLSPort)
		go func() {
			logger.Info("POP3 implicit-TLS server listening", "addr", listener.Addr().String())
			if err := srv.Serve(tlsListener); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Error("POP3 implicit-TLS server error", "error", err)
			}
		}()
	}

	return srv, nil
}

// startIMAPServer configures and launches the IMAP4rev1 server (OB-4 W2,
// ADR-004 decision 1): the ADR-004 command subset over the mailstore, with
// the W1 ChangeNotifier wired for IDLE and the Postgres FTS searcher wired
// for the SEARCH BODY/TEXT keys. Authentication is app-password-only through
// mailauth (ADR-004 decision 3). Returns the server for graceful shutdown.
//
// The mailstore service is built here (inbound delivery builds its own
// instance in startInboundSMTPServer); both instances share the same
// database and blob store, and IDLE notifications stay per-instance —
// sessions served by THIS process subscribe through THIS instance's
// notifier, and every mutation committed through either instance's store
// reaches subscribers via the Redis fan-out when wired (local hub otherwise).
func startIMAPServer(
	db *gorm.DB,
	cfg *config.Config,
	blobStore blob.Store,
	secEng *security.Engine,
	rdb *redis.Client,
	certMgr *certmanager.Manager,
) (*imap.Server, error) {
	mcfg := cfg.IMAP
	// Message bodies live in content-addressed encrypted blobs (ADR-003);
	// the production ZstdBlobCodec refuses unencrypted storage (SF-78), so
	// a deployment without the blob store or the at-rest key cannot serve
	// mail at all — refuse to start rather than expose empty mailboxes.
	if blobStore == nil || cfg.EncryptionKey == "" {
		return nil, errors.New("IMAP requires the blob store and POSTA_ENCRYPTION_KEY (message bodies are stored encrypted)")
	}
	msSvc, err := mailstore.New(db, mailstore.ZstdBlobCodec{}, blobStore, nil)
	if err != nil {
		return nil, err
	}
	// IDLE (ADR-004 decision 7): folder changes fan out in-process first and
	// through Redis pub/sub when this process holds a Redis handle (same
	// propagation pattern as the ban events).
	msSvc.SetChangeNotifier(mailstore.NewRedisChangeNotifier(rdb))
	// SEARCH BODY/TEXT keys run through Postgres FTS (OB-3 search wave);
	// flag/date/size/header keys work without it.
	if sr, err := mailstore.NewPostgresSearcher(db); err == nil {
		msSvc.SetSearcher(sr)
	} else {
		logger.Warn("IMAP full-text searcher unavailable; SEARCH BODY/TEXT keys will be refused", "error", err)
	}

	auth := mailauth.NewAuthenticator(
		repositories.NewUserRepository(db),
		repositories.NewUserAppPasswordRepository(db),
	)
	if auth == nil {
		return nil, errors.New("IMAP authenticator could not be built (nil mailauth dependency)")
	}

	// Audit events land in the events substrate (ADR-004 decision 6).
	auditSink := imap.NewEventAuditLogger(eventbus.New(repositories.NewEventRepository(db)))

	srv := imap.NewServer(imap.Config{
		Hostname:              mcfg.Hostname,
		MaxSessionsPerUser:    mcfg.MaxSessionsPerUser,
		MaxConnsPerIP:         mcfg.MaxConnsPerIP,
		CommandRateLimit:      mcfg.CommandRateLimit,
		CommandRateWindowSecs: mcfg.CommandRateWindow,
		// Cleartext authentication is permitted only when the plaintext
		// listener serves no TLS at all — a configuration validate()
		// confines that bind to loopback; the server additionally requires
		// the peer address to be loopback at runtime (ADR-004 decision 4).
		AllowCleartextAuth: mcfg.TLSCertFile == "" || mcfg.TLSKeyFile == "",
	}, msSvc, auth)
	srv.SetAudit(auditSink)
	srv.SetSecurity(secEng)

	// TLS wiring (OB-6 W2, SF-34/ADR-007 decision 3): same posture as POP3 —
	// the ACME SNI map serves the implicit-TLS listener when the manager is
	// present; the static certificate covers its own names first and remains
	// the fallback otherwise; without a static certificate the map alone
	// serves the listener (fail-closed for unknown SNI).
	var tlsCfg *tls.Config
	switch {
	case mcfg.TLSCertFile != "" && mcfg.TLSKeyFile != "":
		base, certErr := tls.LoadX509KeyPair(mcfg.TLSCertFile, mcfg.TLSKeyFile)
		if certErr != nil {
			return nil, fmt.Errorf("load IMAP TLS cert: %w", certErr)
		}
		baseCert := base
		if certMgr != nil {
			// Parse the leaf so SNI coverage checks can read the SANs.
			parsed, parseErr := certmanager.StaticCertFromFiles(mcfg.TLSCertFile, mcfg.TLSKeyFile)
			if parseErr != nil {
				return nil, fmt.Errorf("load IMAP TLS cert: %w", parseErr)
			}
			baseCert = *parsed
			tlsCfg = certMgr.MailListenerTLSConfig(parsed)
		} else {
			tlsCfg = &tls.Config{
				Certificates: []tls.Certificate{baseCert},
				ServerName:   mcfg.Hostname,
				MinVersion:   tls.VersionTLS12,
			}
		}
		srv.SetTLSConfig(tlsCfg)
	case certMgr != nil && mcfg.TLSPort > 0:
		// ACME-only implicit-TLS listener (993).
		tlsCfg = certMgr.MailListenerTLSConfig(nil)
		srv.SetTLSConfig(tlsCfg)
	case mcfg.TLSPort > 0:
		return nil, errors.New("IMAP TLS port configured without POSTA_IMAP_TLS_CERT_FILE/POSTA_IMAP_TLS_KEY_FILE (implicit TLS serves no cleartext)")
	}

	// Plaintext/STARTTLS listener (143): banned peers are dropped at accept
	// by the CountingListener; STARTTLS upgrades happen in-session.
	if mcfg.Port > 0 {
		l, err := net.Listen("tcp", net.JoinHostPort(mcfg.Host, strconv.Itoa(mcfg.Port)))
		if err != nil {
			return nil, err
		}
		listener := security.NewCountingListener(l, secEng, mcfg.Port, "imap")
		go func() {
			logger.Info("IMAP server listening", "addr", listener.Addr().String(),
				"tls", map[bool]string{true: "starttls", false: "none"}[tlsCfg != nil])
			if err := srv.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Error("IMAP server error", "error", err)
			}
		}()
	}

	// Implicit-TLS listener (993): the eager-handshake TLSServerListener
	// hands over established *tls.Conn sessions (same stack as the SMTP
	// receivers' TLS wiring).
	if mcfg.TLSPort > 0 && tlsCfg != nil {
		l, err := net.Listen("tcp", net.JoinHostPort(mcfg.Host, strconv.Itoa(mcfg.TLSPort)))
		if err != nil {
			return nil, err
		}
		listener := security.NewCountingListener(l, secEng, mcfg.TLSPort, "imap")
		tlsListener := security.TLSServerListener(listener, tlsCfg, secEng, mcfg.TLSPort)
		go func() {
			logger.Info("IMAP implicit-TLS server listening", "addr", listener.Addr().String())
			if err := srv.Serve(tlsListener); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Error("IMAP implicit-TLS server error", "error", err)
			}
		}()
	}

	return srv, nil
}

// startEmbeddedWorker starts an in-process asynq worker for email delivery.
// The bus argument is used by the inbound parse handler to publish
// email.inbound.received events; it may be nil when inbound is disabled.
func startEmbeddedWorker(db *gorm.DB,
	cfg *config.Config,
	blobStore blob.Store,
	notifier *notification.Service,
	bus *eventbus.EventBus) {
	dispatcher := newWebhookDispatcher(db, cfg)

	handler := worker.NewEmailSendHandler(
		repositories.NewEmailRepository(db),
		repositories.NewSMTPRepository(db),
		repositories.NewServerRepository(db),
		repositories.NewDomainRepository(db),
		repositories.NewContactRepository(db),
		dispatcher,
	)
	if blobStore != nil {
		handler.SetBlobStore(blobStore)
	}
	handler.SetCampaignMessageRepo(repositories.NewCampaignMessageRepository(db))
	handler.SetCampaignRepo(repositories.NewCampaignRepository(db))
	handler.SetSuppressionRepo(repositories.NewSuppressionRepository(db))
	handler.SetBounceRepo(repositories.NewBounceRepository(db))
	handler.SetAutoSuppress(cfg.AutoSuppressOnReject)
	handler.SetStamper(email.NewStamper("Posta", config.Version, []byte(cfg.JWTSecret)))
	// Outbound MTA executor (OB-1): domains configured 'direct' deliver
	// direct-to-MX; everything else keeps today's relay path (ADR-001).
	handler.SetMTAExecutor(newMTAExecutor(db, cfg))
	// IP pools + warm-up (OB-6 W3, ADR-007 decision 7): direct-mode domains
	// assigned to a pool rotate across its enabled member IPs and their
	// active warm-up ramp caps the hourly budget.
	handler.SetIPPoolRepo(repositories.NewIPPoolRepository(db))
	// OB-2 (ADR-002 decision 4): direct-mode MAIL FROM becomes the VERP
	// bounce address when the return-path feature is configured; relay mode
	// keeps the upstream provider's envelope. Off unless
	// POSTA_RETURN_PATH_SUBDOMAIN + POSTA_RETURN_PATH_SECRET are set.
	handler.SetVerp(inbound.NewVerp(inbound.VerpConfig{
		Subdomain: cfg.ReturnPathSubdomain,
		Secret:    cfg.ReturnPathSecret,
	}))
	// Message-history events (sent, failed, bounced, …) recorded by the worker
	// path — the HTTP send path records through the routes wiring. One recorder
	// instance backs both the send handler and the exhausted-task handler.
	logsRecorder := emailhistory.NewRecorder(db)
	handler.SetRecorder(logsRecorder)
	handler.OnSent(func() {
		metrics.IncrementEmailSent()
		metrics.DecrementEmailQueued()
	})
	handler.OnFailed(func() {
		metrics.IncrementEmailFailed()
		metrics.DecrementEmailQueued()
	})

	exhaustedHandler := worker.NewExhaustedErrorHandler(
		repositories.NewEmailRepository(db), dispatcher, func() {
			metrics.IncrementEmailFailed()
			metrics.DecrementEmailQueued()
		},
	)
	exhaustedHandler.SetRecorder(logsRecorder)

	errorHandlers := []asynq.ErrorHandler{exhaustedHandler}
	if cfg.InboundEnabled {
		inboundExhausted := worker.NewInboundExhaustedErrorHandler(
			repositories.NewInboundEmailRepository(db), metrics.IncrementInboundFailed,
		)
		errorHandlers = append(errorHandlers, inboundExhausted)
	}

	srv := asynq.NewServer(
		cfg.Redis.AsynqRedisOpt(),
		asynq.Config{
			Concurrency: cfg.WorkerConcurrency,
			Queues: map[string]int{
				worker.QueueTransactional: 6,
				worker.QueueBulk:          3,
				worker.QueueLow:           1,
			},
			ErrorHandler: worker.ChainErrorHandlers(errorHandlers...),
		},
	)

	// Campaign processor
	campaignProducer := worker.NewProducer(cfg.Redis.AsynqRedisOpt(), cfg.WorkerMaxRetries)
	trackingRepo := repositories.NewTrackingRepository(db)
	trackingService := tracking.NewService(trackingRepo, cfg.AppWebURL, []byte(cfg.JWTSecret))
	settingsProvider := settings.NewProvider(repositories.NewSettingRepository(db))
	campaignDispatcher := newWebhookDispatcher(db, cfg)
	campaignProcessor := worker.NewCampaignProcessor(
		repositories.NewCampaignRepository(db),
		repositories.NewCampaignMessageRepository(db),
		repositories.NewSubscriberListRepository(db),
		repositories.NewSegmentRepository(db),
		repositories.NewSubscriberRepository(db),
		repositories.NewEmailRepository(db),
		repositories.NewTemplateRepository(db),
		repositories.NewTemplateVersionRepository(db),
		repositories.NewTemplateLocalizationRepository(db),
		trackingService,
		settingsProvider,
		campaignProducer,
		campaignDispatcher,
	)

	// Daily report handler
	dailyReportHandler := worker.NewDailyReportHandler(
		notifier,
		repositories.NewAnalyticsRepository(db),
		repositories.NewBounceRepository(db),
	)

	mux := asynq.NewServeMux()
	mux.HandleFunc(worker.TypeEmailSend, handler.ProcessTask)
	mux.HandleFunc(worker.TypeCampaignStart, campaignProcessor.HandleCampaignStart)
	mux.HandleFunc(worker.TypeCampaignBatch, campaignProcessor.HandleCampaignBatch)
	mux.HandleFunc(jobs.TypeDailyReport, dailyReportHandler.ProcessTask)

	if cfg.InboundEnabled {
		inboundHandler := worker.NewInboundProcessHandler(
			repositories.NewInboundEmailRepository(db),
			newWebhookDispatcher(db, cfg),
			cfg.ApiBaseURL,
			[]byte(cfg.JWTSecret),
		)
		inboundHandler.OnForwarded(metrics.IncrementInboundForwarded)
		inboundHandler.OnFailed(metrics.IncrementInboundFailed)
		mux.HandleFunc(worker.TypeInboundProcess, inboundHandler.ProcessTask)

		parseProducer := worker.NewProducer(cfg.Redis.AsynqRedisOpt(), cfg.WorkerMaxRetries)
		parseSvc := newInboundServiceForWorker(db, cfg, blobStore, parseProducer, bus)
		parseHandler := worker.NewInboundParseHandler(
			repositories.NewInboundEmailRepository(db),
			parseSvc,
			parseProducer,
		)
		// OB-2 (ADR-006): spam/malware filtering on both worker stages — the
		// parse worker covers SMTP-ingested mail, the process handler covers
		// webhook-ingested mail. One shared step instance; nil (disabled)
		// keeps the pre-filter behavior on both.
		inboundFilter := newInboundFilter(db, cfg, parseSvc, blobStore, newWebhookDispatcher(db, cfg))
		inboundHandler.SetFilter(inboundFilter)
		parseHandler.SetFilter(inboundFilter)
		mux.HandleFunc(worker.TypeInboundParse, parseHandler.ProcessTask)
	}

	if cfg.MessagesEnabled {
		messageHandler := worker.NewMessageProcessHandler(
			repositories.NewMessageRepository(db),
			repositories.NewFormRepository(db),
			repositories.NewWorkspaceRepository(db),
			newWebhookDispatcher(db, cfg),
			notifier,
			cfg.AppWebURL,
		)
		messageHandler.OnNotified(metrics.IncrementMessageNotification)
		messageHandler.SetRealtime(realtime.NewRedis(cfg.Redis.Client))
		mux.HandleFunc(worker.TypeMessageProcess, messageHandler.ProcessTask)
		mux.HandleFunc(worker.TypeMessageReply, messageHandler.ProcessReplyTask)
	}

	// Automation engine: the scheduler polls for due journeys and executes
	// automation:run tasks. Running alongside a standalone worker is safe —
	// the claim query uses FOR UPDATE SKIP LOCKED and every cursor write is a
	// compare-and-swap on the journey status.
	automationProducer := worker.NewProducer(cfg.Redis.AsynqRedisOpt(), cfg.WorkerMaxRetries)
	automationEngine := automation.NewEngine(
		db,
		automationProducer,
		automation.NewEmailSenderAdapter(newAutomationEmailService(db, cfg, automationProducer, blobStore), db),
		repositories.NewSubscriberListRepository(db),
		repositories.NewSubscriberRepository(db),
	)
	mux.HandleFunc(worker.TypeAutomationRun, worker.NewAutomationRunHandler(automationEngine).ProcessTask)
	// email.bounced automation triggers for the embedded-worker path.
	handler.SetSubscriberRepo(repositories.NewSubscriberRepository(db))
	handler.SetAutomationTriggers(automationEngine)
	automationEngine.Start(context.Background())

	workermon.StartHeartbeat(context.Background(), cfg.Redis.Client, config.Version, config.CommitID)

	go func() {
		if err := srv.Run(mux); err != nil {
			logger.Error("embedded worker error", "error", err)
		}
	}()

	logger.Info("async email delivery enabled (embedded worker)")
}

// initCronManager creates and registers scheduled jobs.
func initCronManager(
	db *gorm.DB,
	cfg *config.Config,
	notifier *notification.Service,
	blobStore blob.Store,
	producer *worker.Producer,
) *cronpkg.Manager {
	cronClient := asynq.NewClient(cfg.Redis.AsynqRedisOpt())
	settingsProvider := settings.NewProvider(repositories.NewSettingRepository(db))

	manager := cronpkg.NewManager(cronClient)
	retentionJob := jobs.NewRetentionCleanupJob(
		repositories.NewEmailRepository(db),
		repositories.NewEventRepository(db),
		repositories.NewWebhookDeliveryRepository(db),
		repositories.NewTrackingRepository(db),
		settingsProvider,
	)
	retentionJob.SetEmailEventRepo(repositories.NewEmailEventRepository(db))
	retentionJob.SetIncomingWebhookEventRepo(repositories.NewIncomingWebhookEventRepository(db))
	retentionJob.SetWebhookDeliveryAttemptRepo(repositories.NewWebhookDeliveryAttemptRepository(db))
	// P3-3 (GAP-13): give SecurityEventRepository.DeleteOlderThan its first
	// caller — security events purge under security_events_retention_days
	// alongside the other retention families in the same job.
	retentionJob.SetSecurityEventRepo(repositories.NewSecurityEventRepository(db))
	if cfg.InboundEnabled {
		retentionJob.SetInboundEmailRepo(repositories.NewInboundEmailRepository(db))
		// OB-2 (ADR-002 §3): quarantine records + still-held messages purge
		// under inbound_quarantine_retention_days.
		retentionJob.SetInboundQuarantineRepo(repositories.NewInboundQuarantineRepository(db))
	}
	if blobStore != nil {
		retentionJob.SetBlobStore(blobStore)
	}
	// OB-3 (ADR-003 §7): hosted-mailbox retention runs in the same unified
	// job. The sweeper needs only db + blob store (no blob codec), so it
	// wires ahead of the full mailstore service (the OB-4 ingestion seam).
	if msSweeper, err := mailstore.NewRetentionSweeper(db, blobStore); err == nil {
		retentionJob.SetMailstoreSweeper(msSweeper)
		// OB-3 W2 (GAP-45): the same sweeper reports per-mailbox blob bytes
		// from mail_blob_refs for the GC step's audited summary event.
		retentionJob.SetMailBlobUsageReporter(msSweeper)
	}
	manager.Register(retentionJob)
	// Daily release check. A second Service instance backs the admin handler; the
	// cached verdict lives in a database row, so they need no shared state.
	updateSvc := updatecheck.NewService(db, config.Version, cfg.UpdateCheck)
	if updateSvc.Enabled() {
		manager.Register(jobs.NewUpdateCheckJob(updateSvc))
		// Seed the cache shortly after boot so a fresh install does not wait a day
		// for its first answer. Off the startup path: never block serving on GitHub.
		go func() {
			time.Sleep(30 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := updateSvc.Check(ctx); err != nil {
				logger.Warn("initial update check failed", "error", err)
			}
		}()
	}
	dailyReportJob := jobs.NewDailyReportJob(repositories.NewWorkspaceRepository(db))
	manager.Register(dailyReportJob)
	manager.Register(jobs.NewAccountCleanupJob(repositories.NewUserRepository(db)))
	manager.Register(jobs.NewAPIKeyExpiryJob(db, notifier))
	bounceAlertJob := jobs.NewBounceAlertJob(
		db, notifier,
		repositories.NewBounceRepository(db),
		repositories.NewSuppressionRepository(db),
	)
	manager.Register(bounceAlertJob)
	if producer != nil {
		manager.Register(jobs.NewCampaignRestartJob(
			repositories.NewCampaignRepository(db),
			repositories.NewCampaignMessageRepository(db),
			producer,
		))
		// Scheduled sends: claims status=scheduled campaigns whose time has come
		// and enqueues the worker task. Every minute so a scheduled send never
		// waits longer than one tick.
		manager.Register(jobs.NewCampaignSchedulerJob(
			repositories.NewCampaignRepository(db),
			producer,
		))
		// Lost-task reconcile (R-03): emails stuck in queued whose asynq task
		// no longer exists are re-enqueued; the retry worker only covers failed.
		manager.Register(jobs.NewQueueReconcileJob(
			db,
			producer,
			asynq.NewInspector(cfg.Redis.AsynqRedisOpt()),
		))
	}
	retentionJob.SetNotificationRepo(repositories.NewNotificationRepository(db))
	// Scheduled pg_dump backups with retention (R-11). Off by default — the
	// runtime image ships no postgres client, so the operator opts in once
	// POSTA_BACKUP_PG_DUMP_BIN resolves. See jobs.BackupJob for the runbook.
	if cfg.BackupEnabled {
		manager.Register(jobs.NewBackupJob(
			cfg.Database.DSN(),
			cfg.BackupDir,
			cfg.BackupRetentionDays,
			cfg.BackupPgDumpBin,
			cfg.BackupSchedule,
		))
	}
	if cfg.MessagesEnabled {
		retentionJob.SetMessageRepo(repositories.NewMessageRepository(db))
		retentionJob.SetFormRepo(repositories.NewFormRepository(db))
		manager.Register(jobs.NewMessageDigestJob(
			notifier,
			repositories.NewFormRepository(db),
			repositories.NewMessageRepository(db),
			repositories.NewWorkspaceRepository(db),
			cfg.AppWebURL,
		))
	}

	// ---- P3-3 (SF-104): scheduled DKIM retire-after-grace task. Flips a
	// `retiring` signing key to `retired` once its grace period
	// (dkim_retire_grace_days, default 14) has elapsed AND the replacement
	// key's DNS record is verified published. Rotation itself stays
	// API-driven; this only automates the retire step, making old keys
	// deletable without operator follow-up.
	manager.Register(jobs.NewDKIMRetireJob(db, settingsProvider))
	// ---- OB-6 W3 (ADR-007 decision 7): daily IP-reputation rollup pass.
	// Aggregates mta_delivery_attempts + email_events into
	// ip_reputation_rollups for the previous two UTC days (idempotent
	// upserts); the R-07 report refreshes the current day on read.
	manager.Register(jobs.NewIPReputationRollupJob(db))

	// ---- SF-96 (OB-6 W5): platform metric-alert engine. Rules live in the
	// `metric_alert_rules` platform setting (admin-gated settings API); this
	// cron evaluates them every minute and executes the fire/resolve actions
	// (event + webhook + optional email). Registered unconditionally —
	// without rules the evaluation is a cheap no-op, the same posture as the
	// bounce-alert job.
	manager.Register(jobs.NewMetricAlertJob(meticalert.NewEngine(
		settingsProvider,
		repositories.NewEventRepository(db),
		notifier,
	)))

	// ---- GAP-03 (ADR-009 §2): multi-replica cron leadership. Off by
	// default; when enabled on EVERY replica, each scheduled tick first
	// acquires/renews a Redis SET-NX-PX lease and non-holders skip the tick
	// — fail-closed on Redis errors, since mutual exclusion is the feature.
	// The same client backs the per-recipient dedup guard on the two
	// customer-facing alert jobs, covering the crash window between lease
	// acquire and send. Failure modes and recovery: docs/docs/operations/redis-ha.md.
	if cfg.CronLeaderLeaseEnabled {
		leaseClient := cfg.Redis.Client
		if leaseClient == nil {
			if client, err := storage.NewRedis(cfg.Redis.RedisOptions()); err == nil {
				leaseClient = client
			} else {
				logger.Error("cron leader lease: redis client unavailable; crons keep running on every replica (single-replica semantics)", "error", err)
			}
		}
		if leaseClient != nil {
			manager.SetLeaderLease(cronpkg.NewLeaderLease(
				cronpkg.NewRedisLeaseStore(leaseClient),
				cronpkg.DefaultLeaseKey,
				time.Duration(cfg.CronLeaseTTLSeconds)*time.Second,
				cronpkg.NewHolderID(),
			))
			claims := cronpkg.NewRedisClaimStore(leaseClient)
			bounceAlertJob.SetClaimStore(claims)
			dailyReportJob.SetClaimStore(claims)
			logger.Info("cron leader lease enabled", "ttl_seconds", cfg.CronLeaseTTLSeconds)
		}
	}

	return manager
}

// startRetryWorker starts the background worker that retries failed emails.
func startRetryWorker(db *gorm.DB, cfg *config.Config, producer *worker.Producer) {
	dispatcher := newWebhookDispatcher(db, cfg)
	retryWorker := retry.NewWorker(
		repositories.NewEmailRepository(db),
		repositories.NewSMTPRepository(db),
		dispatcher,
		5*time.Minute,
	)
	if producer != nil {
		retryWorker.SetEnqueuer(producer)
	}
	retryWorker.OnSent(metrics.IncrementEmailSent)
	retryWorker.OnFailed(metrics.IncrementEmailFailed)
	retryWorker.OnRetry(metrics.IncrementEmailRetry)
	retryWorker.Start(context.Background())
}

// shutdownServer gracefully closes all server resources.
func shutdownServer(res *serverResources) {
	logger.Info("Posta Server shutting down gracefully...")

	// OB-6 W5 (SF-93): stop the OTLP push exporter first — one final partial
	// interval is not worth delaying the drain of real listeners.
	if res.otlpStop != nil {
		res.otlpStop()
	}

	if res.smtpInbound != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := res.smtpInbound.Shutdown(ctx); err != nil {
			logger.Error("failed to shut down inbound SMTP server", "error", err)
		}
		cancel()
	}
	if res.smtpRelay != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := res.smtpRelay.Shutdown(ctx); err != nil {
			logger.Error("failed to shut down SMTP relay server", "error", err)
		}
		cancel()
	}
	if res.smtpSubmission != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := res.smtpSubmission.Shutdown(ctx); err != nil {
			logger.Error("failed to shut down submission server", "error", err)
		}
		cancel()
	}
	if res.pop3 != nil {
		// POP3 sessions own no long-lived worker state; closing the listeners
		// and live conns releases their per-user/per-IP slots.
		res.pop3.Close()
	}
	if res.imap != nil {
		// IMAP sessions hold IDLE subscriptions on the mailstore notifier;
		// Close terminates listeners and live conns, releasing the
		// per-user/per-IP slots with them.
		res.imap.Close()
	}
	if res.certCancel != nil {
		// Stop the SF-110 host-allowlist hot-reload loop (OB-6 W2/SF-34).
		res.certCancel()
	}
	if res.cronManager != nil {
		res.cronManager.Stop()
	}
	if res.producer != nil {
		if err := res.producer.Close(); err != nil {
			logger.Error("failed to close producer", "error", err)
		}
	}
	if res.redis != nil {
		if err := res.redis.Close(); err != nil {
			logger.Error("failed to close Redis", "error", err)
		}
	}
	// P3-3 (SF-94): release the file sink last so shutdown logs still tee.
	if res.logSinks != nil {
		if err := res.logSinks.Close(); err != nil {
			logger.Error("failed to close log file sink", "error", err)
		}
	}
	if res.db != nil {
		if sqlDB, err := res.db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}

	logger.Info("Posta Server shut down complete")
}

// webhookConfig builds a webhook.Config from the application configuration.
func webhookConfig(cfg *config.Config) webhook.Config {
	return webhook.Config{
		MaxRetries:     cfg.WebhookMaxRetries,
		Timeout:        time.Duration(cfg.WebhookTimeoutSecs) * time.Second,
		ProxyURL:       cfg.WebhookProxyURL,
		ProxyAckPolicy: cfg.WebhookProxyAckPolicy,

		// SF-90: batch delivery is off unless POSTA_WEBHOOK_BATCH_ENABLED
		// is set; the pending cap is inert in per-event mode.
		BatchEnabled:       cfg.WebhookBatchEnabled,
		BatchMaxEvents:     cfg.WebhookBatchMaxEvents,
		BatchFlushInterval: time.Duration(cfg.WebhookBatchFlushSecs) * time.Second,
		MaxPendingEvents:   cfg.WebhookMaxPendingEvents,
	}
}
