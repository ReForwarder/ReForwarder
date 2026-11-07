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
	"errors"
	"io"
	"net"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/goposta/posta/internal/config"
	cronpkg "github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/cron/jobs"
	"github.com/goposta/posta/internal/logging"
	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/routes"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/messages"
	"github.com/goposta/posta/internal/services/mta"
	"github.com/goposta/posta/internal/services/notification"
	"github.com/goposta/posta/internal/services/ratelimit"
	"github.com/goposta/posta/internal/services/realtime"
	"github.com/goposta/posta/internal/services/retry"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/services/seeder"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/smtprelay"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/services/updatecheck"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/services/workermon"
	workspacesvc "github.com/goposta/posta/internal/services/workspace"
	"github.com/goposta/posta/internal/services/workspaceprovision"
	"github.com/goposta/posta/internal/storage"
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
	// logSinks releases the SF-94 file sink (nil when stdout-only).
	logSinks io.Closer
}

func runServer(cli *okapicli.CLI) {
	cfg := config.New()
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
		logger.Fatal("Failed to initialize Posta", "error", err)
	}
	res := &serverResources{logSinks: logSinks}
	if err := cli.RunServer(&okapicli.RunOptions{
		ShutdownTimeout: 30 * time.Second,
		OnStart: func() {
			cfg.InitStorage()
			res.db = cfg.Database.DB
			res.redis = cfg.Redis.Client

			if err := migration.Run(res.db); err != nil {
				logger.Fatal("failed to run migrations", "error", err)
			}

			if err := upgrade.Run(context.Background(), res.db, config.Version, upgrade.Options{
				AllowDowngrade:  cfg.AllowDowngrade,
				PlanEnforcement: cfg.PlanEnforcement,
			}); err != nil {
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
					logger.Fatal("failed to initialize blob storage", "error", err)
				}
				res.blobStore = bs
				logger.Info("blob storage initialized", "provider", cfg.BlobProvider)
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

			if cfg.SMTPRelayEnabled && !cfg.DevMode {
				if srv, err := startSMTPRelayServer(res.db, cfg, emailSvc, secEng); err != nil {
					logger.Fatal("failed to start SMTP relay server", "error", err)
				} else {
					res.smtpRelay = srv
				}
			}

			// Submission listener (SF-03, gated; ADR-001 §10): MSA on port
			// 587 feeding accepted messages into the same pipeline as API
			// sends. Off by default (POSTA_SUBMISSION_ENABLED).
			if cfg.SubmissionEnabled && !cfg.DevMode {
				if srv, err := startSubmissionServer(res.db, cfg, emailSvc, secEng); err != nil {
					logger.Error("failed to start submission server", "error", err)
				} else {
					res.smtpSubmission = srv
				}
			}

			if res.cronManager != nil {
				res.cronManager.Start(context.Background())
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

// newMTAExecutor builds the outbound MTA executor (OB-1, ADR-001): system-DNS
// MX resolver with a 30s cache, a sliding-hour per-domain budget, and the
// durable attempt-record repository. The EHLO name comes from
// POSTA_MTA_EHLO_NAME (guidance: point it at the hostname whose PTR matches
// the outbound bind IP). Domains left in the default 'relay' mode never touch
// the executor.
func newMTAExecutor(db *gorm.DB, cfg *config.Config) *mta.Executor {
	executor := mta.NewExecutor(mta.NewResolver(), mta.NewBudgetKeeper(time.Hour), repositories.NewMTADeliveryAttemptRepository(db))
	executor.SetHeloDomain(cfg.MTAEhloName)
	return executor
}

// startSubmissionServer launches the gated SMTP submission listener (SF-03,
// ADR-001 §10): AUTH against SMTP relay credentials, tenant-verified sender
// domains, validated recipients — no open relay. Accepted messages enter the
// same pipeline as API sends through the email service.
func startSubmissionServer(
	db *gorm.DB, cfg *config.Config, emailSvc *email.Service, secEng *security.Engine,
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
	manager.Register(jobs.NewDailyReportJob(repositories.NewWorkspaceRepository(db)))
	manager.Register(jobs.NewAccountCleanupJob(repositories.NewUserRepository(db)))
	manager.Register(jobs.NewAPIKeyExpiryJob(db, notifier))
	manager.Register(jobs.NewBounceAlertJob(
		db, notifier,
		repositories.NewBounceRepository(db),
		repositories.NewSuppressionRepository(db),
	))
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
