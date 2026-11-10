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

package routes

import (
	"context"
	"net/url"
	"time"

	"github.com/goposta/posta/internal/config"
	cronpkg "github.com/goposta/posta/internal/cron"
	"github.com/goposta/posta/internal/cron/jobs"
	"github.com/goposta/posta/internal/handlers"
	"github.com/goposta/posta/internal/metrics"
	"github.com/goposta/posta/internal/middlewares"
	"github.com/goposta/posta/internal/models"
	"github.com/goposta/posta/internal/services/audit"
	"github.com/goposta/posta/internal/services/auth"
	"github.com/goposta/posta/internal/services/automation"
	"github.com/goposta/posta/internal/services/cache"
	domainpkg "github.com/goposta/posta/internal/services/domain"
	"github.com/goposta/posta/internal/services/email"
	"github.com/goposta/posta/internal/services/emailhistory"
	"github.com/goposta/posta/internal/services/emailverify"
	"github.com/goposta/posta/internal/services/eventbus"
	"github.com/goposta/posta/internal/services/inbound"
	"github.com/goposta/posta/internal/services/inbox"
	"github.com/goposta/posta/internal/services/mailcompose"
	"github.com/goposta/posta/internal/services/mailstore"
	"github.com/goposta/posta/internal/services/messages"
	"github.com/goposta/posta/internal/services/notification"
	"github.com/goposta/posta/internal/services/passwordreset"
	planpkg "github.com/goposta/posta/internal/services/plan"
	"github.com/goposta/posta/internal/services/provider_events"
	"github.com/goposta/posta/internal/services/ratelimit"
	"github.com/goposta/posta/internal/services/realtime"
	"github.com/goposta/posta/internal/services/security"
	"github.com/goposta/posta/internal/services/seeder"
	sessionpkg "github.com/goposta/posta/internal/services/session"
	"github.com/goposta/posta/internal/services/spamfilter"
	"github.com/goposta/posta/internal/services/settings"
	"github.com/goposta/posta/internal/services/smtprelay"
	"github.com/goposta/posta/internal/services/tracking"
	"github.com/goposta/posta/internal/services/updatecheck"
	"github.com/goposta/posta/internal/services/verifier"
	"github.com/goposta/posta/internal/services/webauthn"
	"github.com/goposta/posta/internal/services/webhook"
	"github.com/goposta/posta/internal/services/webpush"
	"github.com/goposta/posta/internal/services/workermon"
	"github.com/goposta/posta/internal/services/workspaceprovision"
	"github.com/goposta/posta/internal/storage/blob"
	"github.com/goposta/posta/internal/storage/repositories"
	"github.com/goposta/posta/internal/web"
	"github.com/goposta/posta/internal/worker"
	"github.com/hibiken/asynq"
	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Router struct {
	app *okapi.Okapi
	cfg *config.Config
	// eng is the security engine, nil when it is disabled or not wired in.
	// Its ban middleware runs after RequestID and before every route group.
	eng *security.Engine
	v1  *okapi.Group
	mw  routerMiddleware
	h   routerHandlers
}

type routerMiddleware struct {
	// jwtAdminAuth guards platform administration. Only an admin user session
	// satisfies it — /admin/* is dashboard-only and an API key is never a valid
	// credential there, whatever scopes it carries.
	jwtAdminAuth okapi.JWTAuth
	// auth accepts a user JWT or an API key; it guards workspace-scoped routes.
	auth okapi.Middleware
	// jwtOnly accepts only a user JWT; it guards account-level routes (profile,
	// 2FA, API key management), which a machine key must not reach.
	jwtOnly okapi.Middleware
	// apiKey accepts only an API key; it guards the public machine-facing API.
	apiKey            okapi.Middleware
	loginLimiter      okapi.Middleware
	workspace         okapi.Middleware
	optionalWorkspace okapi.Middleware
	workspaceQuery    okapi.Middleware
	verifiedEmail     okapi.Middleware
}

type routerHandlers struct {
	health               *handlers.HealthHandler
	update               *handlers.UpdateHandler
	user                 *handlers.UserHandler
	email                *handlers.EmailHandler
	apiKey               *handlers.APIKeyHandler
	apiKeyUsage          *handlers.APIKeyUsageHandler
	template             *handlers.TemplateHandler
	version              *handlers.TemplateVersionHandler
	localization         *handlers.TemplateLocalizationHandler
	language             *handlers.LanguageHandler
	stylesheet           *handlers.StyleSheetHandler
	smtp                 *handlers.SMTPHandler
	webhook              *handlers.WebhookHandler
	webhookDelivery      *handlers.WebhookDeliveryHandler
	webhookReplay        *handlers.WebhookReplayHandler
	dashboard            *handlers.DashboardHandler
	domain               *handlers.DomainHandler
	domainSigning        *handlers.DomainSigningHandler
	domainHealth         *handlers.DomainHealthHandler
	dmarc                *handlers.DMARCReportHandler
	bounce               *handlers.BounceHandler
	suppression          *handlers.SuppressionHandler
	unsubscribeList      *handlers.UnsubscribeListHandler
	contact              *handlers.ContactHandler
	admin                *handlers.AdminHandler
	workspace            *handlers.WorkspaceHandler
	server               *handlers.ServerHandler
	event                *handlers.EventHandler
	analytics            *handlers.AnalyticsHandler
	adminDomain          *handlers.AdminDomainHandler
	adminWorkspace       *handlers.AdminWorkspaceHandler
	notification         *handlers.NotificationHandler
	announcement         *handlers.AdminAnnouncementHandler
	setting              *handlers.SettingHandler
	userSetting          *handlers.UserSettingHandler
	workspaceSetting     *handlers.WorkspaceSettingHandler
	security             *handlers.SecurityHandler
	session              *handlers.SessionHandler
	appPassword          *handlers.AppPasswordHandler
	webauthn             *handlers.WebAuthnHandler
	impersonation        *handlers.ImpersonationHandler
	cron                 *handlers.CronHandler
	oauth                *handlers.OAuthHandler
	oauthAdmin           *handlers.OAuthAdminHandler
	subscriber           *handlers.SubscriberHandler
	subscriberList       *handlers.SubscriberListHandler
	segment              *handlers.SegmentHandler
	automation           *handlers.AutomationHandler
	campaign             *handlers.CampaignHandler
	tracking             *handlers.TrackingHandler
	bounceWebhook        *handlers.BounceWebhookHandler
	providerEvent        *handlers.ProviderEventHandler
	workspaceData        *handlers.WorkspaceDataHandler
	plan                 *handlers.PlanHandler
	inbound              *handlers.InboundHandler
	inboundQuarantine    *handlers.InboundQuarantineHandler
	inboundForwardTarget *handlers.InboundForwardTargetHandler
	smtpCredential       *handlers.SMTPCredentialHandler
	verify               *handlers.VerifyHandler
	form                 *handlers.FormHandler
	message              *handlers.MessageHandler
	messageFilter        *handlers.MessageFilterHandler
	formIngest           *handlers.FormIngestHandler
	logs                 *handlers.LogsHandler
	queueOps             *handlers.QueueOpsHandler
	mailstoreSearch      *handlers.MailstoreSearchAdminHandler
	adminMailbox         *handlers.AdminMailboxHandler
	mailCompose          *handlers.MailComposeHandler
	// mail is the OB-5 W1 webmail REST surface (ADR-005 decision 1) for the
	// session user's own mailboxes.
	mail      *handlers.MailHandler
	webpush   *handlers.WebPushHandler
}

func InitRoutes(app *okapi.Okapi, db *gorm.DB, redisClient *redis.Client, cfg *config.Config, eng *security.Engine, producer *worker.Producer, cronManager *cronpkg.Manager, blobStore blob.Store, ctx context.Context, notifier ...*notification.Service) *email.Service {

	// Repositories
	userRepo := repositories.NewUserRepository(db)
	apiKeyRepo := repositories.NewAPIKeyRepository(db)
	emailRepo := repositories.NewEmailRepository(db)
	templateRepo := repositories.NewTemplateRepository(db)
	smtpRepo := repositories.NewSMTPRepository(db)
	serverRepo := repositories.NewServerRepository(db)
	webhookRepo := repositories.NewWebhookRepository(db)
	domainRepo := repositories.NewDomainRepository(db)
	notificationRepo := repositories.NewNotificationRepository(db)
	bounceRepo := repositories.NewBounceRepository(db)
	suppressionRepo := repositories.NewSuppressionRepository(db)
	unsubListRepo := repositories.NewUnsubscribeListRepository(db)
	stylesheetRepo := repositories.NewStyleSheetRepository(db)
	versionRepo := repositories.NewTemplateVersionRepository(db)
	localizationRepo := repositories.NewTemplateLocalizationRepository(db)
	languageRepo := repositories.NewLanguageRepository(db)
	contactRepo := repositories.NewContactRepository(db)
	contactListRepo := repositories.NewContactListRepository(db)
	eventRepo := repositories.NewEventRepository(db)
	settingRepo := repositories.NewSettingRepository(db)
	userSettingRepo := repositories.NewUserSettingRepository(db)
	workspaceSettingRepo := repositories.NewWorkspaceSettingRepository(db)
	sessionRepo := repositories.NewSessionRepository(db)
	workspaceRepo := repositories.NewWorkspaceRepository(db)
	emailVerifyRepo := repositories.NewUserEmailVerificationRepository(db)

	// Session store (Redis-backed blacklist)
	sessionStore := sessionpkg.NewStore(redisClient)

	// Services
	bus := eventbus.New(eventRepo)
	auditLogger := audit.NewLogger(bus)

	// Login-failure feed: the event bus is created here, inside InitRoutes,
	// so the feed consuming it starts here too (the engine itself is built in
	// cmd/posta without a bus). It turns failed dashboard logins into
	// auth-failure signals so brute force against the UI is scored by the
	// same engine as SMTP abuse. Start blocks; run it as a background loop.
	if eng != nil {
		feed := security.NewFeed(bus, eng.Detect)
		go func() {
			if err := feed.Start(ctx); err != nil {
				logger.Error("security feed stopped", "error", err)
			}
		}()
	}

	inboxService := inbox.NewService(db, notificationRepo)
	apiKeyService := auth.NewAPIKeyService(apiKeyRepo)
	settingsProvider := settings.NewProvider(settingRepo)
	planRepo := repositories.NewPlanRepository(db)
	planService := planpkg.NewService(planRepo, settingsProvider)
	limiter := ratelimit.NewRedisLimiter(redisClient, cfg.RateLimitHourly, cfg.RateLimitDaily)
	limiter.SetSettings(settingsProvider)
	// Per-API-key services (R-15 per-key rate limits, R-18 usage analytics),
	// consulted by the auth middlewares after a key validates.
	apiKeyUsageRepo := repositories.NewAPIKeyUsageRepository(db)
	apiKeyDeps := &middlewares.APIKeyDeps{Limiter: limiter, Usage: apiKeyUsageRepo}
	webhookDeliveryRepo := repositories.NewWebhookDeliveryRepository(db)
	dispatcher := webhook.NewDispatcher(webhookRepo)
	dispatcher.SetDeliveryRepo(webhookDeliveryRepo)
	dispatcher.SetAttemptRepo(repositories.NewWebhookDeliveryAttemptRepository(db))
	dispatcher.SetConfig(webhook.Config{
		MaxRetries:     cfg.WebhookMaxRetries,
		Timeout:        time.Duration(cfg.WebhookTimeoutSecs) * time.Second,
		ProxyURL:       cfg.WebhookProxyURL,
		ProxyAckPolicy: cfg.WebhookProxyAckPolicy,

		// SF-90: batch delivery is off unless POSTA_WEBHOOK_BATCH_ENABLED is
		// set; the pending cap is inert in per-event mode.
		BatchEnabled:       cfg.WebhookBatchEnabled,
		BatchMaxEvents:     cfg.WebhookBatchMaxEvents,
		BatchFlushInterval: time.Duration(cfg.WebhookBatchFlushSecs) * time.Second,
		MaxPendingEvents:   cfg.WebhookMaxPendingEvents,
	})
	dispatcher.OnDeliverySuccess(func() { metrics.IncrementWebhookDelivery("success") })
	dispatcher.OnDeliveryFailed(func() { metrics.IncrementWebhookDelivery("failed") })
	dispatcher.OnDeliveryDone(metrics.ObserveWebhookDeliveryDuration)
	dispatcher.OnEventsDropped(metrics.IncrementWebhookEventsDropped)
	emailService := email.NewService(emailRepo, smtpRepo, templateRepo, suppressionRepo, limiter, dispatcher, cfg.DevMode)
	emailService.SetSettings(settingsProvider)
	emailService.SetPlanLimits(&emailPlanAdapter{planService})
	emailService.SetVersionRepos(versionRepo, localizationRepo)
	emailService.SetContactRepo(contactRepo)
	emailService.SetUnsubscribeListRepo(unsubListRepo)
	emailService.SetDomainVerification(domainRepo, userRepo, workspaceSettingRepo)
	if producer != nil {
		emailService.SetEnqueuer(producer)
	}
	if blobStore != nil {
		emailService.SetBlobStore(blobStore)
	}
	emailService.OnSent(metrics.IncrementEmailSent)
	emailService.OnFailed(metrics.IncrementEmailFailed)
	emailService.OnQueued(metrics.IncrementEmailQueued)

	// Message-history recorder: every email lifecycle event (sent, failed,
	// opened, clicked, bounced, …) flows into email_events through it. One
	// recorder instance backs the HTTP send path, the tracking handlers and the
	// bounce webhook here; the worker builds its own in cmd/posta/server.go.
	logsRecorder := emailhistory.NewRecorder(db)
	emailService.SetRecorder(logsRecorder)

	// Cache
	statsCache := cache.New(redisClient)

	// Email verification service (Redis-cached)
	verifierSvc := verifier.NewService(redisClient, suppressionRepo, bounceRepo, verifier.Options{
		Enabled:    cfg.EmailVerifyEnabled,
		AddrTTL:    time.Duration(cfg.EmailVerifyCacheTTLHours) * time.Hour,
		MXTTL:      time.Duration(cfg.EmailVerifyMXCacheTTLHours) * time.Hour,
		RateHourly: cfg.EmailVerifyRateHourly,
	})

	// Handlers
	userSeeder := seeder.New(templateRepo, stylesheetRepo, versionRepo, localizationRepo, languageRepo)

	migrator := workspaceprovision.New(cfg.PlanEnforcement)
	migrator.SetSeeder(userSeeder)
	userHandler := handlers.NewUserHandler(userRepo, cfg.JWTSecret, userSeeder, bus)
	userHandler.SetSettings(settingsProvider)
	userHandler.SetMigrator(db, migrator)
	inspector := asynq.NewInspector(cfg.Redis.AsynqRedisOpt())

	// Seed default platform settings
	go seeder.SeedDefaultSettings(settingRepo)

	// Start worker connection monitor
	wm := workermon.New(inspector, bus, 15*time.Second)
	wm.OnCount(metrics.SetActiveWorkers)
	wm.Start(ctx)

	jwtAuth := middlewares.JWTAuth(cfg, sessionStore)
	jwtAdminAuth := middlewares.JWTAdminAuth(cfg, sessionStore)

	r := &Router{
		app: app,
		cfg: cfg,
		eng: eng,
		v1:  app.Group("/api/v1"),
		mw: routerMiddleware{
			jwtAdminAuth:      jwtAdminAuth,
			auth:              middlewares.Authenticate(jwtAuth, apiKeyService, userRepo, apiKeyRepo, apiKeyDeps),
			jwtOnly:           jwtAuth.Middleware,
			apiKey:            middlewares.APIKeyAuth(apiKeyService, userRepo, apiKeyRepo, apiKeyDeps),
			loginLimiter:      loginLimiterMiddleware(cfg, limiter),
			workspace:         middlewares.RequireWorkspaceMiddleware(workspaceRepo, userRepo),
			optionalWorkspace: middlewares.OptionalWorkspaceMiddleware(workspaceRepo, userRepo),
			workspaceQuery:    middlewares.WorkspaceFromQueryOrHeader(workspaceRepo, userRepo),
		},
		h: routerHandlers{
			health: handlers.NewHealthHandler(db, redisClient),
			// A second Service instance also exists for the cron job. They share no
			// in-memory state — the cached verdict is a single database row — so
			// constructing one per consumer is equivalent to threading one through.
			update:           handlers.NewUpdateHandler(updatecheck.NewService(db, config.Version, cfg.UpdateCheck)),
			user:             userHandler,
			email:            handlers.NewEmailHandler(emailService, emailRepo, bus, statsCache),
			apiKey:           handlers.NewAPIKeyHandler(apiKeyService, apiKeyRepo, userSettingRepo, auditLogger),
			apiKeyUsage:      handlers.NewAPIKeyUsageHandler(apiKeyUsageRepo, apiKeyRepo),
			template:         handlers.NewTemplateHandler(templateRepo, stylesheetRepo, versionRepo, localizationRepo, languageRepo, emailService),
			version:          handlers.NewTemplateVersionHandler(templateRepo, versionRepo, stylesheetRepo),
			localization:     handlers.NewTemplateLocalizationHandler(templateRepo, versionRepo, localizationRepo, stylesheetRepo),
			language:         handlers.NewLanguageHandler(languageRepo),
			stylesheet:       handlers.NewStyleSheetHandler(stylesheetRepo),
			smtp:             handlers.NewSMTPHandler(smtpRepo, domainRepo, auditLogger),
			webhook:          handlers.NewWebhookHandler(webhookRepo, auditLogger),
			webhookDelivery:  handlers.NewWebhookDeliveryHandler(webhookDeliveryRepo),
			webhookReplay:    handlers.NewWebhookReplayHandler(webhookDeliveryRepo, repositories.NewWebhookDeliveryAttemptRepository(db), webhookRepo, dispatcher, auditLogger),
			dashboard:        handlers.NewDashboardHandler(db, statsCache, webhookDeliveryRepo),
			domain:           handlers.NewDomainHandler(domainRepo),
			domainSigning:    handlers.NewDomainSigningHandler(repositories.NewDomainSigningKeyRepository(db), domainRepo, auditLogger),
			domainHealth:     handlers.NewDomainHealthHandler(domainpkg.NewHealthService(db, repositories.NewDomainSigningKeyRepository(db)), domainRepo, auditLogger),
			dmarc:            handlers.NewDMARCReportHandler(repositories.NewDMARCReportRepository(db), domainRepo, auditLogger),
			adminDomain:      handlers.NewAdminDomainHandler(db, domainRepo, auditLogger),
			adminWorkspace:   handlers.NewAdminWorkspaceHandler(db, workspaceRepo),
			notification:     handlers.NewNotificationHandler(notificationRepo, inboxService),
			announcement:     handlers.NewAdminAnnouncementHandler(db, inboxService, auditLogger),
			bounce:           handlers.NewBounceHandler(bounceRepo, suppressionRepo, emailRepo, dispatcher),
			suppression:      handlers.NewSuppressionHandler(suppressionRepo, unsubListRepo),
			unsubscribeList:  handlers.NewUnsubscribeListHandler(unsubListRepo),
			contact:          handlers.NewContactHandler(contactRepo, suppressionRepo),
			admin:            handlers.NewAdminHandler(db, statsCache, userRepo, apiKeyRepo, emailRepo, webhookDeliveryRepo, inspector, redisClient, bus, userSeeder, cfg.EmbeddedWorker),
			security:         handlers.NewSecurityHandler(db, eng, bus),
			workspace:        handlers.NewWorkspaceHandler(workspaceRepo, userRepo, db),
			server:           handlers.NewServerHandler(serverRepo, auditLogger),
			event:            handlers.NewEventHandler(eventRepo, bus),
			analytics:        handlers.NewAnalyticsHandler(repositories.NewAnalyticsRepository(db), statsCache),
			setting:          handlers.NewSettingHandler(settingRepo, auditLogger),
			userSetting:      handlers.NewUserSettingHandler(userSettingRepo),
			workspaceSetting: handlers.NewWorkspaceSettingHandler(workspaceSettingRepo),
			session:          handlers.NewSessionHandler(sessionRepo, sessionStore),
			appPassword:      handlers.NewAppPasswordHandler(repositories.NewUserAppPasswordRepository(db)),
			verify:           handlers.NewVerifyHandler(verifierSvc),
		},
	}

	// Session management
	r.h.user.SetSessionRepo(sessionRepo)
	r.h.admin.SetSessionRepo(sessionRepo, sessionStore)
	r.h.admin.SetMigrator(migrator)
	// Queue operations (cancel/run/archive/destroy, pause/resume) are audited
	// on the deployment audit trail.
	r.h.admin.SetAuditLogger(auditLogger)
	// Recovery-code logins (R-10) land on the user audit trail.
	r.h.user.SetAuditLogger(auditLogger)
	// Delivery-mode switches (OB-1) are audited on the deployment trail.
	r.h.domain.SetAuditLogger(auditLogger)

	// SF-100: platform settings writes record a per-key change history
	// (setting_history) and invalidate the shared provider's 30s TTL cache
	// through the one write path (applySettingWrites) — update and rollback
	// alike. The history repository rides the same DB; the provider is the
	// instance every consumer (rate limiter, retention jobs, plan service)
	// already reads through.
	r.h.setting.SetHistoryRepo(repositories.NewSettingHistoryRepository(db))
	r.h.setting.SetSettingsProvider(settingsProvider)

	// SF-35/SF-67: app passwords for the future mail-client surfaces (ADR-004).
	// The management endpoints and the verification seam share this repository.
	r.h.user.SetAppPasswordRepo(repositories.NewUserAppPasswordRepository(db))

	// CA-03: rotating refresh tokens ride every login unless the deployment
	// opted out with POSTA_REFRESH_TOKENS_ENABLED=false.
	refreshSvc := auth.NewRefreshTokenService(repositories.NewRefreshTokenRepository(db), cfg.JWTSecret)
	if cfg.RefreshTokensEnabled {
		r.h.user.SetRefreshTokens(refreshSvc)
		r.h.session.SetRefreshTokens(refreshSvc)
	}

	// Operational queue actions (SF-59): requeue_stuck needs the send
	// producer, which only exists when the deployment runs a worker, and
	// purge_archived_before rides the same asynq inspector.
	if producer != nil {
		r.h.queueOps = handlers.NewQueueOpsHandler(
			db, producer, inspector, handlers.NewAsynqQueueInspector(inspector))
		r.h.queueOps.SetAuditLogger(auditLogger)
	}

	// OB-3 mailstore search maintenance (ADR-003 decision 3): the operator
	// recovery path for the mail_messages.search_tsv index — needs only the
	// primary database, so it is wired unconditionally.
	r.h.mailstoreSearch = handlers.NewMailstoreSearchAdminHandler(db)
	r.h.mailstoreSearch.SetAuditLogger(auditLogger)

	// OB-5 W2 mailbox administration (ADR-005 decision 3): provision/quota/
	// suspend/delete + usage over the mailstore service. The service needs
	// the same preconditions as inbound mailbox delivery (blob store + the
	// at-rest encryption key — the production ZstdBlobCodec refuses to store
	// unencrypted, SF-78); without either, the routes answer 503. The admin
	// surface gets its own Service instance: the Service carries only
	// in-process state (the per-folder write guard), cross-instance ordering
	// is the database's (atomic UID claim, row locks), and this keeps the
	// inbound wiring block untouched.
	if blobStore != nil && cfg.EncryptionKey != "" {
		if msSvc, err := mailstore.New(db, mailstore.ZstdBlobCodec{}, blobStore, nil); err != nil {
			logger.Error("mailbox administration unavailable", "error", err)
		} else {
			r.h.adminMailbox = handlers.NewAdminMailboxHandler(msSvc, db)
		}
	}
	if r.h.adminMailbox == nil {
		// Unconfigured mailstore still gets a handler: it answers 503 on
		// every endpoint instead of the route table dereferencing a nil
		// handler (same pattern as the disabled-passkeys webauthn handler).
		r.h.adminMailbox = handlers.NewAdminMailboxHandler(nil, db)
	}
	r.h.adminMailbox.SetAuditLogger(auditLogger)

	// OB-5 W3 webmail compose seam (ADR-005 decision 2 + ADR-006 scan hook):
	// attachment staging over the blob layer, send into the Sent folder +
	// outbound pipeline, drafts, reply frame, delivery status. Same
	// preconditions as the W2 block above (blob store + at-rest encryption
	// key — the production ZstdBlobCodec refuses unencrypted storage, SF-78);
	// without either, the compose routes answer 503. The compose surface gets
	// its own mailstore Service instance for the same reason W2's does: the
	// service carries only the in-process per-folder write guard, and this
	// keeps the inbound and admin wiring blocks untouched. The spam scan runs
	// through the deployment's clamd config (POSTA_CLAMAV_*); with no clamd
	// configured the client reports disabled and uploads stage unscanned
	// (ADR-006's config-gated fail-open posture).
	if blobStore != nil && cfg.EncryptionKey != "" {
		if msSvc, err := mailstore.New(db, mailstore.ZstdBlobCodec{}, blobStore, nil); err != nil {
			logger.Error("webmail compose unavailable", "error", err)
		} else {
			composeSvc := mailcompose.New(
				msSvc,
				emailService,
				spamfilter.NewClamdClient(spamfilter.ClamdConfig{
					Host:     cfg.ClamAVHost,
					Port:     cfg.ClamAVPort,
					Timeout:  cfg.ClamAVTimeout,
					FailMode: spamfilter.ClamFailModeFromEnv(cfg.ClamAVFailMode),
				}),
				mailcompose.NewGormStagingStore(db),
				mailcompose.NewGormDeliveryStore(repositories.NewEmailRepository(db), repositories.NewEmailEventRepository(db)),
				mailcompose.Config{AutoProvision: cfg.MailstoreAutoProvision},
			)
			r.h.mailCompose = handlers.NewMailComposeHandler(composeSvc)
		}
	}
	if r.h.mailCompose == nil {
		// Unconfigured mailstore still gets a handler: it answers 503 on
		// every endpoint instead of the route table dereferencing a nil
		// handler (same pattern as the disabled-passkeys webauthn handler).
		r.h.mailCompose = handlers.NewMailComposeHandler(nil)
	}

	// OB-5 W1 webmail REST surface (ADR-005 decision 1): /api/v1/mail/* for
	// the session user's own mailboxes — folders, message list/view, flags,
	// attachments, ADR-003 FTS search. Same preconditions and construction
	// posture as the W2/W3 blocks above; own Service instance (only
	// in-process state, cross-instance ordering is the database's). The FTS
	// searcher rides the same instance (POST /mail/search and the list
	// endpoint's q parameter refuse search with 503 when it is unavailable).
	if blobStore != nil && cfg.EncryptionKey != "" {
		if msSvc, err := mailstore.New(db, mailstore.ZstdBlobCodec{}, blobStore, nil); err != nil {
			logger.Error("webmail unavailable", "error", err)
		} else {
			if sr, serr := mailstore.NewPostgresSearcher(db); serr == nil {
				msSvc.SetSearcher(sr)
			}
			r.h.mail = handlers.NewMailHandler(msSvc)
		}
	}
	if r.h.mail == nil {
		// Unconfigured mailstore still gets a handler: it answers 503 on
		// every endpoint instead of the route table dereferencing a nil
		// handler (same pattern as the disabled-passkeys webauthn handler).
		r.h.mail = handlers.NewMailHandler(nil)
	}

	// Passkeys (R-19). The WebAuthn Relying Party identity is derived from the
	// dashboard origin: RPID is the origin's bare host (no scheme, no port),
	// which the browser requires the ceremony to be scoped to. Without an app
	// URL there is no valid identity, so passkeys stay disabled — every
	// endpoint answers 503 and the dashboard hides the flow.
	if cfg.AppWebURL != "" {
		waCfg := webauthn.Config{
			RPID:          webauthnRPID(cfg.AppWebURL),
			RPDisplayName: "Posta",
			RPOrigins:     []string{cfg.AppWebURL},
		}
		// Ceremony state rides Redis when available so challenges survive a
		// restart and replicas share it; the in-process store covers the
		// Redis-less single-instance case.
		var challengeStore webauthn.ChallengeStore
		if redisClient != nil {
			challengeStore = webauthn.NewRedisChallengeStore(redisClient)
		} else {
			challengeStore = webauthn.NewMemoryChallengeStore()
		}
		if svc, err := webauthn.New(waCfg, webauthn.NewGormCredentialStore(db), challengeStore); err != nil {
			logger.Error("passkeys disabled: invalid WebAuthn configuration", "err", err)
		} else {
			r.h.webauthn = handlers.NewWebAuthnHandler(svc, userRepo, userHandler)
			r.h.webauthn.SetAuditLogger(auditLogger)
		}
	}
	if r.h.webauthn == nil {
		// Disabled passkeys still get a handler: it answers 503 on every
		// endpoint instead of the route table dereferencing a nil handler.
		r.h.webauthn = handlers.NewWebAuthnHandler(nil, userRepo, userHandler)
	}

	// SF-89 Web Push: off unless POSTA_WEB_PUSH_ENABLED=true with a valid
	// VAPID identity. When off, the handler still answers the capability
	// probe with enabled=false and refuses the subscription endpoints.
	pushRepo := repositories.NewUserPushSubscriptionRepository(db)
	var pushService *webpush.Service
	if cfg.WebPushEnabled {
		if svc, err := webpush.New(webpush.Config{
			PublicKey:  cfg.WebPushVapidPublic,
			PrivateKey: cfg.WebPushVapidPriv,
			Subject:    cfg.WebPushSubject,
		}); err != nil {
			logger.Error("web push disabled: invalid VAPID configuration", "error", err)
			r.h.webpush = handlers.NewWebPushHandler(nil, pushRepo)
		} else {
			pushService = svc
			r.h.webpush = handlers.NewWebPushHandler(svc, pushRepo)
		}
	} else {
		r.h.webpush = handlers.NewWebPushHandler(nil, pushRepo)
	}

	// Administrative impersonation (R-20): endpoint handlers plus wiring; the
	// context marker lives in the auth middleware.
	r.h.impersonation = handlers.NewImpersonationHandler(userRepo, sessionRepo, sessionStore, userHandler, auditLogger)

	// Security rules read/write through the settings repository
	r.h.security.SetSettingRepo(settingRepo)

	// Plans
	r.h.plan = handlers.NewPlanHandler(planRepo, workspaceRepo, userRepo, planService, auditLogger)
	r.h.admin.SetWorkspaceRepo(workspaceRepo, planRepo)
	r.h.workspace.SetSeeder(userSeeder)
	r.h.workspace.SetPlanService(planService)
	r.h.workspace.SetAuditLogger(auditLogger)
	r.h.apiKey.SetQuota(planService, db)
	r.h.domain.SetQuota(planService, db)
	r.h.smtp.SetQuota(planService, db)

	// Notifications
	var notif *notification.Service
	if len(notifier) > 0 && notifier[0] != nil {
		notif = notifier[0]
		r.h.user.SetNotifier(notif)
		r.h.workspace.SetNotifier(notif, cfg.AppWebURL)
		r.h.apiKey.SetNotifier(notif)
	}

	// Email verification service (coordinates token issuance, delivery, redemption)
	emailVerifySvc := emailverify.NewService(userRepo, emailVerifyRepo, notif, cfg.AppWebURL, cfg.EmailVerificationRequired)
	r.h.user.SetEmailVerifier(emailVerifySvc)
	r.mw.verifiedEmail = middlewares.RequireVerifiedEmail(emailVerifySvc, userRepo)

	// Self-service password reset (gated by the password_reset_enabled setting)
	passwordResetSvc := passwordreset.NewService(userRepo, repositories.NewPasswordResetRepository(db), notif, cfg.AppWebURL)
	r.h.user.SetPasswordReset(passwordResetSvc)

	r.h.dashboard.SetFeatures(handlers.DashboardFeatures{
		Messages: cfg.MessagesEnabled,
		Inbound:  cfg.InboundEnabled,
		Relay:    cfg.SMTPRelayEnabled,
	})
	r.h.dashboard.SetInbox(inboxService)

	// Email content privacy
	r.h.email.SetSettings(settingsProvider)
	r.h.admin.SetEmailSettings(settingsProvider)

	if cronManager != nil {
		r.h.cron = handlers.NewCronHandler(cronManager)
	}

	// OAuth
	oauthProviderRepo := repositories.NewOAuthProviderRepository(db)
	oauthAccountRepo := repositories.NewOAuthAccountRepository(db)
	ssoRepo := repositories.NewWorkspaceSSORepository(db)
	oauthService := auth.NewOAuthService(oauthProviderRepo, oauthAccountRepo, userRepo)

	callbackBase := cfg.OAuthCallbackBaseURL
	if callbackBase == "" {
		callbackBase = cfg.AppWebURL
	}

	r.h.oauth = handlers.NewOAuthHandler(
		oauthService, oauthProviderRepo, oauthAccountRepo,
		userRepo, sessionRepo, cfg.JWTSecret,
		userSeeder, bus, redisClient, callbackBase, cfg.AppWebURL,
	)
	r.h.oauth.SetMigrator(db, migrator)
	r.h.oauthAdmin = handlers.NewOAuthAdminHandler(oauthProviderRepo, ssoRepo)

	// Subscribers
	subscriberRepo := repositories.NewSubscriberRepository(db)
	subscriberListRepo := repositories.NewSubscriberListRepository(db)
	segmentRepo := repositories.NewSegmentRepository(db)
	r.h.subscriber = handlers.NewSubscriberHandler(subscriberRepo)
	r.h.subscriberList = handlers.NewSubscriberListHandler(subscriberListRepo, subscriberRepo)
	r.h.segment = handlers.NewSegmentHandler(segmentRepo, subscriberRepo)

	// Automations (workflow builder + executions + custom-event ingest). The
	// enrollment engine is attached separately — see AttachAutomationTrigger.
	r.h.automation = handlers.NewAutomationHandler(db)

	// Automation engine (HTTP side): enrollment only — it matches trigger
	// events against live automations and enqueues automation:run tasks. The
	// scheduler that executes those tasks runs in the worker (embedded worker
	// in cmd/posta/server.go and the standalone worker binary), so the HTTP
	// side never calls Start. Without a worker producer there is nowhere to
	// enqueue, and every hook stays nil-guarded and inert. Wired AFTER the
	// tracking handler is constructed below — AttachAutomationTrigger fans out
	// to r.h.tracking, which must not be nil.
	// Campaigns
	campaignRepo := repositories.NewCampaignRepository(db)
	campaignMessageRepo := repositories.NewCampaignMessageRepository(db)
	r.h.campaign = handlers.NewCampaignHandler(
		campaignRepo, campaignMessageRepo,
		subscriberListRepo, segmentRepo, subscriberRepo, templateRepo, domainRepo, producer,
	)

	// Tracking
	trackingRepo := repositories.NewTrackingRepository(db)
	trackingService := tracking.NewService(trackingRepo, cfg.AppWebURL, []byte(cfg.JWTSecret))
	r.h.tracking = handlers.NewTrackingHandler(trackingRepo, campaignMessageRepo, campaignRepo, subscriberRepo, subscriberListRepo, emailRepo, suppressionRepo, trackingService, dispatcher)

	// Wire the generator for Posta-injected public links (one-click unsubscribe +
	// hosted "view in browser"), used to resolve reserved {{ posta_* }} template
	// variables once an email's identity is known.
	emailService.SetLinkGenerator(trackingService)

	// Open/click tracking for all email: the send path rewrites links through the
	// tracking service when the email_tracking_enabled setting allows it, and the
	// tracking handlers turn callbacks into email_events.
	emailService.SetTracking(trackingService, settingsProvider.EmailTrackingEnabled)
	r.h.tracking.SetRecorder(logsRecorder)

	// Automation engine: enrolls contacts into live automations from the
	// trigger hooks (list.subscribed, contact.created/unsubscribed,
	// email.opened/clicked/bounced, custom-event ingest). Constructed after
	// every handler it fans out to exists. The scheduler itself runs in the
	// worker/embedded-worker binary; the HTTP side only enrolls.
	if producer != nil {
		automationEngine := automation.NewEngine(
			db,
			producer,
			automation.NewEmailSenderAdapter(emailService, db),
			subscriberListRepo,
			subscriberRepo,
		)
		r.AttachAutomationTrigger(automationEngine)
	}

	// Bounce webhook
	r.h.bounceWebhook = handlers.NewBounceWebhookHandler(subscriberRepo, emailRepo, campaignMessageRepo)
	r.h.bounceWebhook.SetRecorder(logsRecorder)

	// Provider event ingestion (R-04): one service + handler beside the
	// bounce webhook. Per-provider signing secrets come from POSTA_PROVIDER_*
	// env (documented choice over workspace_settings — see the package doc);
	// a provider without a configured secret answers 501.
	providerEventSvc := provider_events.NewIngestionService(emailRepo, logsRecorder)
	providerEventSvc.SetDB(db)
	providerEventSvc.SetSubscriberRepo(subscriberRepo)
	providerEventSvc.SetSuppressionRepo(suppressionRepo)
	providerEventSvc.SetBounceRepo(bounceRepo)
	providerEventSvc.SetCampaignMessageRepo(campaignMessageRepo)
	providerEventSvc.SetIncomingWebhookRepo(repositories.NewIncomingWebhookEventRepository(db))
	providerEventSvc.SetAutoSuppress(cfg.AutoSuppressOnReject)
	r.h.providerEvent = handlers.NewProviderEventHandler(providerEventSvc)
	r.h.providerEvent.SetProviderSecrets(map[string]provider_events.ProviderConfig{
		"ses":      {Secret: cfg.ProviderSESSecret},
		"sendgrid": {Secret: cfg.ProviderSendGridKey},
		"postmark": {Secret: cfg.ProviderPostmarkSecret},
		"mailgun":  {Secret: cfg.ProviderMailgunKey},
	})

	// Logs (message history, webhook stores, audit)
	r.h.logs = handlers.NewLogsHandler(db, logsRecorder, settingsProvider)
	// Classification breakdown (SF-53): the ingestion service's workspace-
	// scoped aggregate over email_events.metadata backs the endpoint.
	r.h.logs.SetClassificationCounter(providerEventSvc)

	// Inbound email
	if cfg.InboundEnabled {
		inboundEmailRepo := repositories.NewInboundEmailRepository(db)
		inboundSvc := inbound.NewService(
			inboundEmailRepo,
			domainRepo,
			suppressionRepo,
			inbound.Config{
				MaxMessageSize:    cfg.InboundMaxMessageSize,
				MaxAttachmentSize: cfg.InboundMaxAttachSize,
			},
		)
		if blobStore != nil {
			inboundSvc.SetBlobStore(blobStore)
		}
		if producer != nil {
			inboundSvc.SetEnqueuer(producer)
		}
		inboundSvc.SetEventBus(bus)
		// OB-2 (ADR-002): route webhook-ingested mail through the same
		// routing rules as the SMTP receiver (additive wiring).
		inboundSvc.SetRouter(inbound.NewRouter(repositories.NewInboundRouteRepository(db)))
		inboundSvc.SetForwardQueue(repositories.NewInboundForwardTaskRepository(db))
		inboundSvc.SetWebhookDispatcher(dispatcher)
		// OB-2 (ADR-002 decision 3): quarantine holds persist through the
		// quarantine store; the review surface releases/deletes via HTTP.
		quarantineSvc := inbound.NewQuarantineService(
			repositories.NewInboundQuarantineRepository(db),
			inboundEmailRepo,
			domainRepo,
			inboundSvc,
		)
		inboundSvc.SetQuarantineStore(quarantineSvc)
		r.h.inboundQuarantine = handlers.NewInboundQuarantineHandler(quarantineSvc)
		// OB-3 (ADR-003, flow F13): webhook-ingested deliver-routed mail also
		// files into the recipient mailbox's INBOX through the mailstore —
		// same seam as the SMTP receiver. Needs the blob store and the
		// at-rest encryption key (the production ZstdBlobCodec refuses to
		// store unencrypted, SF-78); without either, archive-only as before.
		if blobStore != nil && cfg.EncryptionKey != "" {
			if msSvc, err := mailstore.New(db, mailstore.ZstdBlobCodec{}, blobStore, nil); err != nil {
				logger.Error("mailstore service unavailable; inbound delivery stays archive-only", "error", err)
			} else {
				inboundSvc.SetMailboxDeliverer(inbound.NewMailboxDelivery(msSvc, domainRepo, inbound.MailboxDeliveryConfig{
					AutoProvision: cfg.MailstoreAutoProvision,
				}))
				logger.Info("hosted-mailbox delivery enabled (webhook path)", "auto_provision", cfg.MailstoreAutoProvision)
			}
		}
		// OB-2 (ADR-002 decision 2): the forwarding executor consumes the
		// durable inbound_forward_tasks rows (queued above) and re-sends the
		// stored originals as system_forward copies through the email
		// service's system path; confirmed targets gate every forward. The
		// mailer seam adapts inbound's request shape onto the email service
		// (a direct import would close a test-only cycle).
		forwardTargetRepo := repositories.NewInboundForwardTargetRepository(db)
		forwardTargetSvc := inbound.NewForwardTargetService(forwardTargetRepo)
		systemMailer := inbound.SystemMailerFunc(func(ctx context.Context, req *inbound.ForwardSendRequest) (*inbound.ForwardSendResponse, error) {
			resp, err := emailService.SendSystemForward(ctx, &email.SystemForwardRequest{
				UserID:      req.UserID,
				WorkspaceID: req.WorkspaceID,
				From:        req.From,
				To:          req.To,
				Subject:     req.Subject,
				Text:        req.Text,
				HTML:        req.HTML,
				Headers:     req.Headers,
				Attachments: req.Attachments,
				Metadata:    req.Metadata,
			})
			if err != nil {
				return nil, err
			}
			return &inbound.ForwardSendResponse{EmailUUID: resp.ID, Status: resp.Status}, nil
		})
		forwarder := inbound.NewForwarder(
			repositories.NewInboundForwardTaskRepository(db),
			inboundEmailRepo,
			inboundSvc,
			domainRepo,
			systemMailer,
		)
		forwarder.SetTargetStore(forwardTargetRepo)
		if redisClient != nil {
			forwarder.SetTargetLimiter(inbound.NewRedisTargetLimiter(redisClient,
				inbound.DefaultForwardTargetHourly, inbound.DefaultForwardTargetDaily))
		}
		forwarder.SetEventBus(bus)
		r.h.inboundForwardTarget = handlers.NewInboundForwardTargetHandler(forwardTargetSvc, cfg.ApiBaseURL)
		// One-minute executor cadence: the task rows carry their own
		// retry/backoff state, so a periodic claim sweep is all the driving
		// the durable queue needs (dev mode has no cron manager by design).
		if cronManager != nil {
			cronManager.Register(jobs.NewInboundForwardQueueJob(forwarder))
		}
		if cfg.MessagesEnabled && cfg.MessagesInboundDomain != "" {
			router := messages.NewThreadRouter(repositories.NewMessageRepository(db), cfg.MessagesInboundDomain)
			router.SetRealtime(realtime.NewRedis(redisClient))
			if producer != nil {
				router.SetEnqueuer(producer)
			}
			inboundSvc.SetThreadRouter(router)
		}
		inboundSvc.OnReceived(func(src models.InboundSource) { metrics.IncrementInboundReceived(string(src)) })
		inboundSvc.OnRejected(metrics.IncrementInboundRejected)
		inboundSvc.OnBytes(metrics.AddInboundBytes)

		r.h.inbound = handlers.NewInboundHandler(
			inboundSvc,
			inboundEmailRepo,
			blobStore,
			cfg.InboundWebhookSecret,
			[]byte(cfg.JWTSecret),
		)
		if producer != nil {
			r.h.inbound.SetEnqueuer(producer)
		}
		r.h.inbound.SetEventBus(bus)
	}

	// Web form messages
	if cfg.MessagesEnabled {
		formRepo := repositories.NewFormRepository(db)
		messageRepo := repositories.NewMessageRepository(db)
		messageFilterRepo := repositories.NewMessageFilterRepository(db)

		messageSvc := messages.NewService(
			formRepo, messageRepo, messageFilterRepo, suppressionRepo, redisClient,
			messages.Config{
				PerIPHourly:       cfg.MessagesIPRateLimit,
				PerFormHourly:     cfg.MessagesPerFormHourly,
				PerEmailHourly:    cfg.MessagesPerEmailHourly,
				PerWorkspaceDaily: cfg.MessagesPerWorkspaceDaily,
				MaxBodyBytes:      cfg.MessagesMaxBodyBytes,
				MaxAttachmentSize: cfg.MessagesMaxAttachSize,
				InboundDomain:     cfg.MessagesInboundDomain,
				AppWebURL:         cfg.AppWebURL,
			},
			[]byte(cfg.JWTSecret),
		)
		messageSvc.SetEmailService(emailService)
		if producer != nil {
			messageSvc.SetEnqueuer(producer)
		}
		if blobStore != nil {
			messageSvc.SetBlobStore(blobStore)
		}
		messageSvc.OnReceived(func(status models.MessageStatus) {
			metrics.IncrementMessageReceived(string(status))
		})

		r.h.form = handlers.NewFormHandler(formRepo, messageRepo, domainRepo, auditLogger, cfg.ApiBaseURL)
		r.h.form.SetKeyCache(messageSvc)
		r.h.message = handlers.NewMessageHandler(messageRepo, formRepo, messageFilterRepo, messageSvc, auditLogger)
		r.h.message.SetEventBus(bus)
		// SF-89: when Web Push is enabled, published dashboard message events
		// also fan out to workspace members' browsers; otherwise the plain
		// Redis bus serves the SSE stream only.
		messageBus := realtime.Bus(realtime.NewRedis(redisClient))
		if pushService != nil {
			messageBus = webpush.NewFanout(messageBus, pushService, pushRepo)
		}
		r.h.message.SetRealtime(messageBus)
		r.h.messageFilter = handlers.NewMessageFilterHandler(messageFilterRepo, messageRepo, formRepo, messageSvc.Scanner())
		r.h.formIngest = handlers.NewFormIngestHandler(
			messageSvc,
			inbound.NewIPRateLimiter(cfg.MessagesIPRateLimit, time.Duration(cfg.MessagesIPRateWindow)*time.Second),
			cfg.MessagesMaxAttachSize,
		)
		if blobStore != nil {
			r.h.message.SetBlobStore(blobStore)
			r.h.formIngest.SetBlobStore(blobStore)
		}
	}

	// SMTP Relay
	if cfg.SMTPRelayEnabled {
		smtpCredRepo := repositories.NewSMTPCredentialRepository(db)
		credService := smtprelay.NewCredentialService(smtpCredRepo)
		r.h.smtpCredential = handlers.NewSMTPCredentialHandler(credService, smtpCredRepo, cfg, auditLogger)
	}

	// Workspace data export/import
	r.h.workspaceData = handlers.NewWorkspaceDataHandler(
		db, workspaceRepo, templateRepo, versionRepo, localizationRepo,
		stylesheetRepo, languageRepo, contactRepo, contactListRepo,
		webhookRepo, suppressionRepo, smtpRepo, domainRepo,
		subscriberRepo, subscriberListRepo,
	)
	r.h.workspaceData.SetAuditLogger(auditLogger)
	if blobStore != nil {
		r.h.workspaceData.SetBlobStore(blobStore)
	}

	// Auto-seed Google provider if configured
	if cfg.GoogleOAuthClientID != "" {
		if _, err := oauthProviderRepo.FindBySlug("google"); err != nil {
			_ = oauthProviderRepo.Create(&models.OAuthProvider{
				Name:         "Google",
				Slug:         "google",
				Type:         models.OAuthProviderGoogle,
				ClientID:     cfg.GoogleOAuthClientID,
				ClientSecret: cfg.GoogleOAuthClientSecret,
				Scopes:       "openid email profile",
				Enabled:      true,
				AutoRegister: true,
			})
		}
	}

	r.registerRoutes()

	return emailService
}

// emailPlanAdapter adapts planpkg.Service to the email.PlanLimitsProvider interface.
type emailPlanAdapter struct {
	svc *planpkg.Service
}

// AttachAutomationTrigger wires one AutomationTrigger — the automation engine —
// into the automation handler's custom-event ingest and the trigger hooks of
// the subscriber, subscriber-list and tracking handlers. Passing nil leaves
// every hook nil-guarded and inert; calling it again replaces the trigger.
func (r *Router) AttachAutomationTrigger(at automation.AutomationTrigger) {
	r.h.automation.SetAutomationTrigger(at)
	r.h.subscriber.SetAutomationTrigger(at)
	r.h.subscriberList.SetAutomationTrigger(at)
	r.h.tracking.SetAutomationTrigger(at)
}

func (a *emailPlanAdapter) EffectiveLimits(workspaceID *uint) *email.PlanLimits {
	l := a.svc.EffectiveLimits(workspaceID)
	return &email.PlanLimits{
		HourlyRateLimit:     l.HourlyRateLimit,
		DailyRateLimit:      l.DailyRateLimit,
		MaxAttachmentSizeMB: l.MaxAttachmentSizeMB,
		MaxBatchSize:        l.MaxBatchSize,
	}
}

// workspaceHeaderRequired is a reusable route option documenting the required workspace header.
var workspaceHeaderRequired = okapi.DocHeader("X-Posta-Workspace-Id", "integer", "Workspace ID (required for workspace-scoped endpoints)", true)

// webauthnRPID derives the WebAuthn Relying Party ID from the dashboard
// origin: the bare host, without scheme or port, since the browser scopes
// ceremonies to a registrable domain. A URL that will not parse falls back to
// the raw string, which keeps a hand-configured bare hostname working.
func webauthnRPID(appWebURL string) string {
	u, err := url.Parse(appWebURL)
	if err != nil || u.Hostname() == "" {
		return appWebURL
	}
	return u.Hostname()
}

// loginLimiterMiddleware returns the login rate limit middleware when enabled,
// or a pass-through middleware when disabled.
func loginLimiterMiddleware(cfg *config.Config, limiter *ratelimit.RedisLimiter) okapi.Middleware {
	if !cfg.AuthRateLimitEnabled {
		return func(c *okapi.Context) error {
			return c.Next()
		}
	}
	return middlewares.LoginRateLimitMiddleware(limiter)
}

func (r *Router) registerRoutes() {
	// Request ID middleware
	r.app.Use(okapi.RequestID())

	// Security engine ban enforcement: allowlisted IPs pass, banned IPs are
	// refused with a 403 before reaching any group, everything else is counted
	// as a new connection. Sits after RequestID and before all route groups.
	if r.eng != nil {
		r.app.Use(r.eng.Middleware())
	}

	// Prometheus metrics
	if r.cfg.MetricsEnabled {
		r.app.Use(metrics.PrometheusMiddleware())
		r.app.Get("/metrics", metrics.MetricsHandler(), okapi.DocHide())
	}

	// Register all route definitions
	r.app.Register(r.healthRoutes()...)
	r.app.Register(r.infoRoute())
	r.app.Register(r.authRoutes()...)
	r.app.Register(r.apiAuthRoutes()...)
	r.app.Register(r.userRoutes()...)
	// OB-5 (ADR-005 decision 1): the webmail REST surface under /mail, gated
	// like /users/me (dashboard session, optional workspace header).
	r.app.Register(r.mailRoutes()...)
	r.app.Register(r.mailComposeRoutes()...)
	r.app.Register(r.workspaceRoutes()...)
	r.app.Register(r.workspaceResourceRoutes()...)
	r.app.Register(r.automationRoutes()...)
	r.app.Register(r.logsRoutes()...)
	r.app.Register(r.oauthRoutes()...)
	r.app.Register(r.trackingRoutes()...)
	r.app.Register(r.trackingAnalyticsRoutes()...)
	r.app.Register(r.bounceWebhookRoutes()...)
	r.app.Register(r.providerWebhookRoutes()...)
	if r.cfg.InboundEnabled && r.h.inbound != nil {
		r.app.Register(r.inboundWebhookRoutes()...)
		r.app.Register(r.inboundWorkspaceRoutes()...)
		if r.h.inboundQuarantine != nil {
			r.app.Register(r.inboundQuarantineRoutes()...)
		}
		if r.h.inboundForwardTarget != nil {
			r.app.Register(r.inboundForwardTargetRoutes()...)
			r.app.Register(r.inboundForwardConfirmRoutes()...)
		}
	}
	if r.cfg.MessagesEnabled && r.h.formIngest != nil {
		r.app.Register(r.formIngestRoutes()...)
		r.app.Register(r.messageWorkspaceRoutes()...)
	}
	r.app.Register(r.adminSSERoutes()...)
	r.app.Register(r.adminRoutes()...)

	// Security admin routes. The SSE stream is registered before the plain
	// security routes so a later param route can never shadow
	// /admin/security/events/stream (see admin_sse_route_order_test.go for
	// the convention).
	r.app.Register(r.securitySSERoutes()...)
	r.app.Register(r.securityRoutes()...)

	// Documentation-only OpenAPI webhooks (events Posta POSTs to subscribers).
	r.registerWebhookDocs()

	// Dashboard UI (static files + SPA fallback). Registered last so API routes
	// win; Okapi auto-excludes their top-level segments (/api, /healthz, /metrics)
	// from the index fallback, so those keep returning JSON rather than index.html.
	//
	// The UI is embedded in the binary (internal/web), so a stock `posta`
	// executable serves it with no static files to ship. POSTA_WEB_DIR overrides
	// this to serve from disk — useful for frontend development against live
	// rebuilds, or to swap in a customized build without recompiling.
	if r.cfg.WebDir != "" {
		r.app.Web("/", r.cfg.WebDir, okapi.WebConfig{MaxAge: time.Hour})
	} else {
		r.app.WebFS("/", web.Assets, okapi.WebConfig{Root: "dist", MaxAge: time.Hour})
	}
}
