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
	"net"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/goposta/posta/internal/config"
	cronpkg "github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/cron/jobs"
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
	"github.com/goposta/posta/internal/services/notification"
	"github.com/goposta/posta/internal/services/ratelimit"
	"github.com/goposta/posta/internal/services/realtime"
	"github.com/goposta/posta/internal/services/retry"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/services/seeder"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/smtprelay"
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
	producer    *worker.Producer
	cronManager *cronpkg.Manager
	blobStore   blob.Store
	db          *gorm.DB
	redis       *redis.Client
	smtpInbound *smtp.Server
	smtpRelay   *smtp.Server
}

func runServer(cli *okapicli.CLI) {
	cfg := config.New()
	app := cli.Okapi()
	if err := cfg.Initialize(app); err != nil {
		logger.Fatal("Failed to initialize Posta", "error", err)
	}
	res := &serverResources{}
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

	backend := inbound.NewBackend(svc, domainRepo, cfg.InboundMaxMessageSize)
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
	if cfg.InboundEnabled {
		retentionJob.SetInboundEmailRepo(repositories.NewInboundEmailRepository(db))
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
	}
	retentionJob.SetNotificationRepo(repositories.NewNotificationRepository(db))
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
	}
}
