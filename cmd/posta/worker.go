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
	"fmt"
	"time"

	"github.com/goposta/posta/internal/config"
	"github.com/goposta/posta/internal/cron/jobs"
	"github.com/goposta/posta/internal/logging"
	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/observability"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/services/crypto"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/notification"
	"github.com/goposta/posta/internal/services/realtime"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/services/workermon"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/goposta/posta/internal/worker"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
)

func runWorker() error {
	// SF-94: multi-sink logging (POSTA_LOG_SINKS / POSTA_LOG_FILE) — the same
	// fail-open arrangement as runServer, and it must run before
	// cfg.InitWorker(): the structured logger's handler captures os.Stdout at
	// construction, so the file tee has to be in place first.
	logSinks, err := logging.Init()
	if err != nil {
		logger.Error("log sink setup failed, logging to stdout only", "error", err)
		logSinks = nil
	}
	defer func() {
		if logSinks != nil {
			_ = logSinks.Close()
		}
	}()

	cfg := config.New()
	// OB-6 W5 (GAP-51): error-monitoring hook before anything that can exit
	// fatally; the DB-backed recorder attaches below once the database is
	// open. No-op unless POSTA_ERROR_WEBHOOK_URL is set.
	observability.SetErrorHook(observability.NewErrorHook(
		cfg.ErrorWebhookURL, cfg.ErrorWebhookSecret, "posta", config.Version, nil))
	if err := cfg.InitWorker(); err != nil {
		observability.CaptureGlobalFatal("worker configuration is not usable: " + err.Error())
		logger.Fatal("worker configuration is not usable", "error", err)
	}
	cfg.InitStorage()

	db := cfg.Database.DB

	// OB-6 W5 (GAP-51): attach the platform-events recorder now that the
	// database is available, so panics/fatals also land on the admin
	// activity timeline (best-effort).
	observability.SetErrorHook(observability.NewErrorHook(
		cfg.ErrorWebhookURL, cfg.ErrorWebhookSecret, "posta", config.Version,
		repositories.NewEventRepository(db)))
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	// Initialize SMTP password encryption
	crypto.Init(cfg.EncryptionKey)

	// Bound outgoing SMTP dials (R-22): a stalled relay must fail within the
	// dial timeout so the concurrency slot frees up and asynq retries the task.
	email.SetDialTimeout(time.Duration(cfg.SMTPDialTimeoutSecs) * time.Second)

	// Managed DKIM signing (R-02): sign messages whose sender domain has an
	// Active+published key; unsigned otherwise (upstream CNAME signer mode).
	email.SetDkimSigner(email.NewDkimSigningService(repositories.NewDomainSigningKeyRepository(db)))

	// Initialize blob storage for attachment retrieval
	var blobStore blob.Store
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
		blobStore = bs
		logger.Info("blob storage initialized", "provider", cfg.BlobProvider)
	}

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
	// Outbound MTA executor (OB-1, ADR-001): domains configured 'direct'
	// deliver direct-to-MX; everything else keeps today's relay path.
	handler.SetMTAExecutor(newMTAExecutor(db, cfg))
	// IP pools + warm-up (OB-6 W3, ADR-007 decision 7): direct-mode domains
	// assigned to a pool rotate across its enabled member IPs and their
	// active warm-up ramp caps the hourly budget.
	handler.SetIPPoolRepo(repositories.NewIPPoolRepository(db))
	// OB-2 (ADR-002 decision 4): direct-mode MAIL FROM becomes the VERP
	// bounce address when the return-path feature is configured (off unless
	// POSTA_RETURN_PATH_SUBDOMAIN + POSTA_RETURN_PATH_SECRET are set).
	handler.SetVerp(inbound.NewVerp(inbound.VerpConfig{
		Subdomain: cfg.ReturnPathSubdomain,
		Secret:    cfg.ReturnPathSecret,
	}))
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

	// Notification service + daily report handler
	notifier := notification.NewService(
		cfg.SystemSMTP, "Posta", cfg.AppWebURL,
		repositories.NewUserRepository(db),
		repositories.NewUserSettingRepository(db),
		repositories.NewWorkspaceRepository(db),
	)
	notifier.SetSMTPRepo(repositories.NewSMTPRepository(db))
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
		parseBus := eventbus.New(repositories.NewEventRepository(db))
		parseSvc := inbound.NewService(
			repositories.NewInboundEmailRepository(db),
			repositories.NewDomainRepository(db),
			repositories.NewSuppressionRepository(db),
			inbound.Config{
				MaxMessageSize:    cfg.InboundMaxMessageSize,
				MaxAttachmentSize: cfg.InboundMaxAttachSize,
			},
		)
		if blobStore != nil {
			parseSvc.SetBlobStore(blobStore)
		}
		parseSvc.SetEnqueuer(parseProducer)
		parseSvc.SetEventBus(parseBus)
		attachThreadRouter(parseSvc, db, cfg, parseProducer)
		parseSvc.EnableDMARC(db) // R-17: parse DMARC aggregate reports on the parse path
		parseSvc.EnableTLSRPT(db) // SF-31 (OB-6 W4): parse TLS reports on the parse path
		parseSvc.EnableVerification(db, cfg.InboundHostname) // SF-22/23/24: message-level auth verification
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
	// automation:run tasks. Running alongside the server's embedded worker is
	// safe — the claim query uses FOR UPDATE SKIP LOCKED and every cursor
	// write is a compare-and-swap on the journey status.
	automationProducer := worker.NewProducer(cfg.Redis.AsynqRedisOpt(), cfg.WorkerMaxRetries)
	automationEngine := automation.NewEngine(
		db,
		automationProducer,
		automation.NewEmailSenderAdapter(newAutomationEmailService(db, cfg, automationProducer, blobStore), db),
		repositories.NewSubscriberListRepository(db),
		repositories.NewSubscriberRepository(db),
	)
	mux.HandleFunc(worker.TypeAutomationRun, worker.NewAutomationRunHandler(automationEngine).ProcessTask)
	// email.bounced automation triggers: the send handler resolves the
	// subscriber behind a bounced address so the engine can enroll.
	handler.SetSubscriberRepo(repositories.NewSubscriberRepository(db))
	handler.SetAutomationTriggers(automationEngine)
	automationEngine.Start(context.Background())

	// Publish worker's build info
	workermon.StartHeartbeat(context.Background(), cfg.Redis.Client, config.Version, config.CommitID)

	var healthSrv *worker.HealthServer
	if cfg.WorkerHealthEnabled {
		addr := fmt.Sprintf(":%d", cfg.WorkerHealthPort)
		healthSrv = worker.NewHealthServer(addr, db, cfg.Redis.Client)
		stop := healthSrv.Start()
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stop(ctx)
		}()
		logger.Info("worker health endpoints listening",
			"addr", addr, "paths", "/healthz /readyz /metrics")
	}

	// OB-6 W5 (SF-93): OTLP push exporter off the worker's registry too —
	// delivery-side families (sends, failures, queue size) move here. Off
	// unless POSTA_OTLP_ENDPOINT is set; failures log and skip.
	otlpStop := func() {}
	if cfg.OTLPEndpoint != "" {
		headers, hdrErr := observability.ParseOTLPHeaders(cfg.OTLPHeaders)
		if hdrErr != nil {
			logger.Error("OTLP header config invalid, exporter not started", "error", hdrErr)
		} else {
			pushStop, startErr := observability.StartOTLPPusher(context.Background(), observability.OTLPOptions{
				Endpoint:       cfg.OTLPEndpoint,
				Interval:       time.Duration(cfg.OTLPIntervalSecs) * time.Second,
				Headers:        headers,
				ServiceName:    "posta",
				ServiceVersion: config.Version,
			})
			if startErr != nil {
				logger.Error("OTLP exporter failed to start", "error", startErr)
			} else {
				otlpStop = pushStop
				logger.Info("OTLP push exporter started", "endpoint", cfg.OTLPEndpoint, "interval_secs", cfg.OTLPIntervalSecs)
			}
		}
	}
	defer otlpStop()

	logger.Info("Posta worker started",
		"version", config.Version,
		"concurrency", cfg.WorkerConcurrency,
	)

	// Readiness flips before Run blocks and back once it returns, so a draining
	// or stopped worker stops advertising itself as ready and an orchestrator
	// routes nothing new at it.
	if healthSrv != nil {
		healthSrv.SetProcessing(true)
		defer healthSrv.SetProcessing(false)
	}

	return srv.Run(mux)
}
