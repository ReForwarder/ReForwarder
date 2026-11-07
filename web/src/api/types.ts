// API Response types
export interface ApiResponse<T> {
  success: boolean
  data: T
  error?: ApiError
}

export interface ApiError {
  code: string
  message: string
  error: string
}

export interface Pageable {
  current_page: number
  size: number
  total_pages: number
  total_elements: number
  empty: boolean
}

export interface PaginatedResponse<T> {
  success: boolean
  data: T[]
  pageable: Pageable
}

// Models
export interface User {
  id: number
  name: string
  email: string
  role: 'admin' | 'user'
  active: boolean
  two_factor_enabled: boolean
  auth_method: string
  plan_id: number | null
  scheduled_deletion_at: string | null
  email_verified_at: string | null
  created_at: string
  last_login_at: string | null
}

export interface AuthResponse {
  token: string
  user: User
  // CA-03: the single-use refresh token. Empty when refresh tokens are
  // disabled for the deployment.
  refresh_token?: string
  // GAP-12: the two_factor_required policy applies and this account has no
  // second factor yet — the dashboard must land the user on the 2FA setup card.
  two_factor_enrollment_required?: boolean
}

// SF-35/SF-67: one stored app password (metadata only — the plaintext is
// returned exactly once at generation and never again).
export interface AppPassword {
  id: number
  name: string
  created_at: string
  last_used_at?: string | null
  revoked_at?: string | null
}

// The one-time view of a freshly generated app password.
export interface AppPasswordCreated {
  app_password: AppPassword
  password: string
  notice: string
}

export interface Email {
  id: number
  uuid: string
  user_id: number
  api_key_id: number
  api_key_name?: string
  smtp_hostname: string | null
  sender: string
  recipients: string[]
  subject: string
  template_name?: string
  html_body: string
  text_body: string
  attachments_json: string
  headers_json: string
  list_unsubscribe_url: string
  list_unsubscribe_post: boolean
  status: 'pending' | 'queued' | 'processing' | 'sent' | 'failed' | 'suppressed' | 'scheduled'
  error_message: string
  retry_count: number
  created_at: string
  sent_at: string | null
  scheduled_at: string | null
  provider?: string
}

// CA-02: lightweight delivery-status view returned by
// GET /workspaces/current/emails/{id}/status (handlers.EmailStatusResponse).
// MessageDetail.vue polls it while a send is queued or processing.
export interface EmailStatusView {
  id: string
  status: Email['status']
  error_message?: string
  retry_count: number
  created_at: string
  sent_at?: string
}

// SF-89: one browser Web Push subscription as the backend stores it.
export interface PushSubscriptionRow {
  id: number
  user_id: number
  endpoint: string
  p256dh: string
  auth: string
  user_agent?: string
  created_at: string
}

export type InboundEmailStatus = 'received' | 'forwarded' | 'failed' | 'rejected' | 'quarantined'
export type InboundSource = 'smtp' | 'webhook'

export interface InboundAttachmentMeta {
  filename: string
  content_type: string
  size: number
  storage_key?: string
  content?: string
}

export interface InboundEmail {
  id: number
  uuid: string
  user_id: number
  workspace_id?: number | null
  domain_id: number
  message_id: string
  sender: string
  recipients: string[]
  subject: string
  text_body: string
  html_body: string
  attachments_json: string
  headers_json: string
  raw_storage_key?: string
  size: number
  spam_score?: number | null
  status: InboundEmailStatus
  source: InboundSource
  error_message?: string
  retry_count: number
  received_at: string
  forwarded_at?: string | null
  created_at: string
}

export interface ApiKey {
  id: number
  user_id: number
  name: string
  key_prefix: string
  created_at: string
  expires_at: string | null
  last_used_at: string | null
  revoked: boolean
  allowed_ips: string[] | null
  scopes: string[] | null
  /** Per-key request caps (R-15); 0 = unlimited. */
  rate_limit_hourly?: number
  rate_limit_daily?: number
  created_by?: ActorRef | null
}

/** One day of a key's usage (R-18); zero-count days are gap-free. */
export interface ApiKeyUsageDay {
  day: string
  requests: number
  sends: number
}

export interface ApiKeyUsageTotals {
  requests: number
  sends: number
}

export interface ApiKeyUsage {
  days: ApiKeyUsageDay[]
  days_trailing: number
  totals: ApiKeyUsageTotals
}

export interface Contact {
  id: number
  user_id: number
  email: string
  name: string
  sent_count: number
  fail_count: number
  suppressed: boolean
  last_sent_at: string | null
  created_at: string
}

export interface ApiKeyCreateResponse {
  key: string
  id: number
  name: string
  prefix: string
  scopes: string[] | null
  expires_at: string | null
  message: string
}

export interface SMTPCredential {
  id: number
  workspace_id: number
  user_id: number
  name: string
  username: string
  revoked: boolean
  allowed_ips: string[] | null
  created_at: string
  last_used_at: string | null
}

export interface SMTPCredentialCreateResponse {
  id: number
  name: string
  username: string
  password: string
  host: string
  port: number
  encryption: 'none' | 'starttls'
  created_at: string
  message: string
}

/** A template as the list returns it: the record plus the languages it is translated into. */
export interface TemplateListItem extends Template {
  languages: string[]
}

export interface ActorRef {
  id: number
  name: string
}

export interface Template {
  id: number
  user_id: number
  name: string
  default_language: string
  active_version_id?: number | null
  description: string
  active_version?: TemplateVersion | null
  sample_data: string
  last_edited_by_id?: number | null
  created_by?: ActorRef | null
  last_edited_by?: ActorRef | null
  created_at: string
  updated_at?: string
}

export interface TemplateInput {
  name: string
  sample_data?: string
  default_language?: string
  description?: string
}

export interface TemplateExportLocalization {
  language: string
  subject_template: string
  html_template: string
  text_template: string
}

export interface TemplateExportVersion {
  version: number
  sample_data: string
  is_active: boolean
  stylesheet?: ExportStyleSheet | null
  localizations: TemplateExportLocalization[]
}

export interface TemplateExport {
  posta_version?: string
  exported_at?: string
  name: string
  description: string
  default_language: string
  sample_data: string
  versions: TemplateExportVersion[]
}

export interface TemplatePreview {
  subject: string
  html: string
  text: string
}

export interface SendTestInput {
  to: string[]
  from?: string
  language?: string
  template_data?: Record<string, any>
}

export interface SendTestResponse {
  id: string
  status: string
}

export interface TemplateVersion {
  id: number
  template_id: number
  version: number
  stylesheet_id?: number | null
  stylesheet?: StyleSheet | null
  localizations?: TemplateLocalization[] | null
  sample_data: string
  created_at: string
}

export interface TemplateVersionInput {
  stylesheet_id?: number | null
  sample_data?: string
}

export interface TemplateLocalization {
  id: number
  version_id: number
  language: string
  subject_template: string
  html_template: string
  text_template: string
  builder_json?: string
  created_at: string
  updated_at?: string
}

export interface TemplateLocalizationInput {
  language: string
  subject_template: string
  html_template: string
  text_template: string
  builder_json?: string
}

export interface Language {
  id: number
  user_id: number
  code: string
  name: string
  is_default: boolean
  created_at: string
}

export interface LanguageInput {
  code: string
  name: string
  is_default?: boolean
}

export interface UnsubscribeListItem {
  id: number
  uuid: string
  user_id: number
  workspace_id?: number | null
  name: string
  public_name?: string
  description?: string
  active: boolean
  created_at: string
  updated_at?: string | null
}

export interface UnsubscribeListInput {
  name: string
  public_name?: string
  description?: string
  active?: boolean
}

export interface StyleSheet {
  id: number
  user_id: number
  name: string
  css: string
  created_at: string
  updated_at?: string
}

export interface StyleSheetInput {
  name: string
  css: string
}

export type ServerStatus = 'enabled' | 'disabled' | 'invalid'

export interface SharedServer {
  id: number
  name: string
  host: string
  port: number
  username: string
  encryption: 'none' | 'starttls' | 'ssl'
  max_retries: number
  status: ServerStatus
  allowed_domains: string[]
  security_mode: 'permissive' | 'strict'
  sent_count: number
  failed_count: number
  validation_error: string
  validated_at: string | null
  created_at: string
  updated_at: string
}

export interface SharedServerInput {
  name: string
  host: string
  port: number
  username?: string
  password?: string
  encryption?: 'none' | 'starttls' | 'ssl'
  max_retries?: number
  status?: ServerStatus
  allowed_domains?: string[]
  security_mode?: 'permissive' | 'strict'
}

export interface SmtpServer {
  id: number
  user_id: number
  host: string
  port: number
  username: string
  password: string
  encryption: 'none' | 'starttls' | 'ssl'
  max_retries: number
  allowed_emails: string[]
  status: ServerStatus
  validation_error: string
  validated_at: string | null
  created_at: string
}

export interface SmtpServerInput {
  host: string
  port: number
  username: string
  password: string
  encryption: 'none' | 'starttls' | 'ssl'
  max_retries?: number
  allowed_emails?: string[]
  status?: ServerStatus
}

export interface Domain {
  id: number
  user_id: number
  domain: string
  ownership_verified: boolean
  spf_verified: boolean
  dkim_verified: boolean
  dmarc_verified: boolean
  verification_token: string
  created_at: string
  dns_records?: DnsRecords
}

export interface DnsRecords {
  verification: DnsRecord
  spf: DnsRecord
  dkim: DnsRecord
  dmarc: DnsRecord
}

export interface DnsRecord {
  type: string
  host: string
  value: string
}

// ---- Admin domains (platform-wide) ----

export interface AdminDomainRow {
  id: number
  domain: string
  workspace_id?: number | null
  workspace_name: string
  owner_id: number
  owner_email: string
  ownership_verified: boolean
  spf_verified: boolean
  dkim_verified: boolean
  dmarc_verified: boolean
  fully_verified: boolean
  created_at: string
}

export interface AdminDomainDetail extends AdminDomainRow {
  verification_token: string
  records: DnsRecords | null
  /** Set when a different workspace already holds this name verified. */
  conflict_workspace_id?: number | null
  conflict_workspace_name?: string
}

export interface AdminWorkspaceRow {
  id: number
  name: string
  slug: string
  system: boolean
  owner_id: number
  owner_name: string
  owner_email: string
  plan_id: number | null
  plan_name: string
  member_count: number
  created_at: string
}

export interface AdminWorkspaceMember {
  user_id: number
  name: string
  email: string
  active: boolean
  role: WorkspaceRole
  joined_at: string
}

export interface AdminWorkspaceDetail extends AdminWorkspaceRow {
  description: string
  default_language: string
  updated_at: string
  members: AdminWorkspaceMember[]
  pending_invitations: number
  usage: {
    emails: number
    templates: number
    contacts: number
    subscribers: number
    campaigns: number
    domains: number
    smtp_servers: number
    api_keys: number
    webhooks: number
  }
}

export interface AdminDomainVerifyResult {
  domain: Domain
  verification: {
    ownership_verified: boolean
    spf_verified: boolean
    dkim_verified: boolean
    dmarc_verified: boolean
    spf_record?: string
    dkim_record?: string
    dmarc_record?: string
  }
  fully_verified: boolean
  /** Set when DNS passed but another workspace already holds the name verified. */
  conflict_workspace_id?: number | null
  conflict_workspace_name?: string
}

export interface Webhook {
  id: number
  user_id: number
  url: string
  events: string[]
  filters: string[] | null
  secret?: string
  created_at: string
}

export interface WebhookInput {
  url: string
  events: string[]
  filters: string[]
}

export interface Bounce {
  id: number
  user_id: number
  email_id: number
  recipient: string
  type: 'hard' | 'soft' | 'complaint'
  reason: string
  created_at: string
}

export interface Suppression {
  id: number
  user_id: number
  email: string
  list_id?: number | null
  kind?: string
  reason: string
  created_at: string
}

export interface WebhookDelivery {
  id: number
  webhook_id: number
  user_id: number
  event: string
  status: 'success' | 'failed'
  http_status_code: number
  error_message?: string
  attempt: number
  created_at: string
}

export interface WebhookDeliveryStats {
  total_deliveries: number
  success_deliveries: number
  failed_deliveries: number
  success_rate: number
}

export interface DashboardStats {
  total_emails: number
  queued_emails: number
  processing_emails: number
  sent_emails: number
  failed_emails: number
  suppressed_emails: number
  failure_rate: number
  total_domains: number
  total_smtp_servers: number
  total_api_keys: number
  active_api_keys: number
  total_contacts: number
  total_bounces: number
  total_suppressions: number
  total_webhooks: number
  total_contact_lists: number
  total_inbound: number
  forwarded_inbound: number
  failed_inbound: number
  daily_volume: DailyVolume[]
  webhook_deliveries: WebhookDeliveryStats | null
  unverified_domains: number
  expiring_api_keys: number
  bounce_rate: number
  total_forms: number
  total_messages: number
  unread_messages: number
  spam_messages: number
  total_templates: number
  total_campaigns: number
  total_subscribers: number
  features: DashboardFeatures
}

export interface DashboardFeatures {
  messages: boolean
  inbound: boolean
  relay: boolean
}

export interface DailyVolume {
  date: string
  sent: number
  failed: number
}

export interface AdminMetrics {
  total_users: number
  total_emails: number
  queued_emails: number
  processing_emails: number
  sent_emails: number
  failed_emails: number
  suppressed_emails: number
  failure_rate: number
  total_api_keys: number
  active_api_keys: number
  total_bounces: number
  total_suppressions: number
  active_workers: number
  shared_smtp_servers: number
  total_domains: number
  total_workspaces: number
  // Always emitted by the platform metrics endpoint; zero when inbound is off.
  total_inbound: number
  forwarded_inbound: number
  failed_inbound: number
  received_inbound: number
  rejected_inbound: number
  webhook_deliveries: WebhookDeliveryStats | null
  server_uptime_seconds: number
  current_goroutines: number
  current_memory_usage: number
  active_sessions: number
  failed_logins_last_24h: number
  two_factor_adoption_rate: number
  two_factor_users: number
  users_without_workspace: number
}

export interface WorkerStatus {
  active_workers: number
  workers: WorkerDetail[]
  server_version?: string
  version_mismatch?: boolean
}

export interface SystemStatus {
  server_uptime_seconds: number
  current_goroutines: number
  current_memory_usage: number
}

export interface WorkerDetail {
  host: string
  pid: number
  queues: Record<string, number>
  type: 'embedded' | 'standalone'
  version?: string
  outdated?: boolean
}

export interface Event {
  id: number
  category: 'user' | 'email' | 'system' | 'audit'
  type: string
  workspace_id?: number | null
  actor_id: number | null
  actor_name: string
  client_ip?: string
  message: string
  metadata: string
  created_at: string
}

export interface UserDetailMetrics {
  user: User
  total_emails: number
  sent_emails: number
  failed_emails: number
  suppressed_emails: number
  failure_rate: number
  total_api_keys: number
  active_api_keys: number
  total_contacts: number
  total_bounces: number
  total_suppressions: number
  total_domains: number
  total_smtp_servers: number
  total_inbound?: number
  forwarded_inbound?: number
  failed_inbound?: number
  webhook_deliveries: WebhookDeliveryStats | null
}

// Analytics
export interface DailyCount {
  date: string
  count: number
}

export interface StatusBreakdown {
  status: string
  count: number
}

export interface AnalyticsResponse {
  daily_counts: DailyCount[]
  status_breakdown: StatusBreakdown[]
}

export interface DeliveryRatePoint {
  date: string
  sent: number
  failed: number
  total: number
  delivery_rate: number
}

export interface BounceRatePoint {
  date: string
  hard: number
  soft: number
  complaint: number
  total: number
}

export interface LatencyPercentiles {
  p50: number
  p75: number
  p90: number
  p99: number
  avg: number
}

export interface DashboardAnalyticsResponse {
  delivery_rate_trends: DeliveryRatePoint[]
  bounce_rate_trends: BounceRatePoint[]
  latency_percentiles: LatencyPercentiles
}

export interface ProviderBreakdownPoint {
  provider: string
  sent: number
  failed: number
  /** Blocked before sending because every recipient was suppressed. Not a bounce. */
  suppressed: number
  total: number
  delivery_rate: number
}

export interface ProviderBreakdownResponse {
  providers: ProviderBreakdownPoint[]
}

// Contact Lists
export interface ContactList {
  id: number
  user_id: number
  name: string
  description: string
  created_at: string
  updated_at: string
}

export interface ContactListWithCount extends ContactList {
  member_count: number
}

export interface ContactListMember {
  id: number
  list_id: number
  email: string
  name: string
  created_at: string
}

// Settings
export interface AdminSetting {
  id: number
  key: string
  value: string
  type: 'string' | 'int' | 'bool'
  created_at: string
  updated_at: string
}

export interface AdminSettingInput {
  key: string
  value: string
  type: string
}

// One per-key entry of the platform settings change history (SF-100). A null
// old_value marks the key's creation — such an entry cannot be rolled back.
export interface AdminSettingHistoryEntry {
  id: number
  workspace_id?: number | null
  key: string
  old_value?: string | null
  new_value: string
  changed_by: number
  changed_by_email: string
  created_at: string
}

export interface UserSettings {
  id: number
  user_id: number
  timezone: string
  default_sender_name: string
  default_sender_email: string
  email_notifications: boolean
  notification_email: string
  webhook_retry_count: number
  default_template_id: number | null
  api_key_expiry_days: number
  bounce_auto_suppress: boolean
  daily_report: boolean
  notify_bounce_alerts: boolean
  notify_api_key_expiry: boolean
  notify_workspace_activity: boolean
  notify_new_message: boolean
  created_at: string
  updated_at: string
}

// Cron Jobs
export interface CronJob {
  name: string
  schedule: string
  running: boolean
  last_run_at: string | null
  last_error?: string
  next_run_at: string | null
}

// Result of the daily release check. Admin-only.
export interface UpdateInfo {
  current_version: string
  latest_version?: string
  release_url?: string
  published_at?: string
  /** False when up to date, when checks are disabled, and when this exact version was dismissed. */
  update_available: boolean
  enabled: boolean
  checked_at?: string
  /** Set when the checker could not reach GitHub, so a silent failure is visible. */
  last_error?: string
}

// 2FA
export interface Setup2FAResponse {
  secret: string
  url: string
}

// User Profile (extended)
export interface UserProfile extends User {
  require_verified_domain: boolean
  scheduled_deletion_at: string | null
  email_verification_required: boolean
  // GAP-12: true while the two_factor_required policy applies and the account
  // has no second factor configured yet.
  two_factor_enrollment_required?: boolean
  default_workspace_id: number | null
  // R-20 impersonation: set while the session acts as another user via the
  // admin impersonation flow. The dashboard banners "Viewing as …" off it.
  impersonated_by?: number
  impersonation_reason?: string
}

// R-19 passkey: one registered WebAuthn credential of the current account.
export interface WebAuthnCredential {
  id: string
  user_id: number
  name: string
  attestation_type: string
  transport: string[]
  sign_count: number
  last_used_at: string | null
  created_at: string
}

// R-20 impersonation: the response of POST /admin/users/{id}/impersonate. The
// token replaces the admin's browser session (cookie set server-side); the
// dashboard switches to the target user and banners the impersonation.
export interface ImpersonationResponse {
  token: string
  impersonated: boolean
  target: {
    id: number
    name: string
    email: string
    role: 'admin' | 'user'
  }
  original_admin: string
}

// User Data Export/Import
export interface ExportStyleSheet {
  name: string
  css: string
}

export interface ExportLanguage {
  code: string
  name: string
}

export interface ExportContact {
  email: string
  name: string
  sent_count: number
  fail_count: number
}

export interface ExportContactList {
  name: string
  description: string
  members: ExportContactMember[]
}

export interface ExportContactMember {
  email: string
  name: string
  data?: string
}

export interface ExportSuppression {
  email: string
  reason: string
}

export interface ExportWebhook {
  url: string
  events: string[]
  filters?: string[]
}

export interface ExportUserSettings {
  timezone: string
  default_sender_name: string
  default_sender_email: string
  email_notifications: boolean
  notification_email: string
  webhook_retry_count: number
  api_key_expiry_days: number
  bounce_auto_suppress: boolean
  daily_report: boolean
}

export interface GDPRDeleteResult {
  deleted: number
  message: string
}

// Workspace Data Export/Import
export interface WorkspaceDataExport {
  posta_version?: string
  exported_at?: string
  workspace_settings: ExportWorkspaceSettings
  templates: TemplateExport[]
  stylesheets: ExportStyleSheet[]
  languages: ExportLanguage[]
  contacts: ExportContact[]
  contact_lists: ExportContactList[]
  suppressions: ExportSuppression[]
  webhooks: ExportWebhook[]
  smtp_servers: ExportSMTPServer[]
  domains: ExportDomain[]
  subscribers: ExportSubscriber[]
  subscriber_lists: ExportSubscriberList[]
}

export interface ExportWorkspaceSettings {
  name: string
  description: string
  default_language: string
}

export interface ExportSMTPServer {
  host: string
  port: number
  username: string
  encryption: string
  max_retries: number
  allowed_emails?: string[]
}

export interface ExportDomain {
  domain: string
}

export interface ExportSubscriber {
  email: string
  name: string
  status: string
  custom_fields?: Record<string, unknown>
  timezone?: string
  language?: string
}

export interface ExportSubscriberList {
  name: string
  description: string
  type: string
  filter_rules?: { field: string; operator: string; value: unknown }[]
}

// Workspaces
export type WorkspaceRole = 'owner' | 'admin' | 'editor' | 'viewer'

export interface Workspace {
  id: number
  name: string
  slug: string
  description: string
  owner_id: number
  role: WorkspaceRole
  system: boolean
  created_at: string
}

// Operational settings that live on the workspace (not the user). Personal
// notification preferences stay on UserSettings.
export interface WorkspaceSettings {
  id: number
  workspace_id: number
  timezone: string
  default_sender_name: string
  default_sender_email: string
  webhook_retry_count: number
  api_key_expiry_days: number
  bounce_auto_suppress: boolean
  require_verified_domain: boolean
  created_at: string
  updated_at: string
}

export interface WorkspaceInput {
  name: string
  slug?: string
  description?: string
  /** Starter templates, stylesheet and languages. Omitted means true. */
  seed_defaults?: boolean
}

export interface WorkspaceMember {
  id: number
  user_id: number
  name: string
  email: string
  role: WorkspaceRole
  created_at: string
}

export interface WorkspaceInvitation {
  id: number
  workspace_id: number
  workspace?: string
  email: string
  role: WorkspaceRole
  status: 'pending' | 'accepted' | 'declined'
  expires_at: string
  created_at: string
}

export interface InviteMemberInput {
  email: string
  role: WorkspaceRole
}

// OAuth
export interface OAuthProviderInfo {
  id: number
  slug: string
  name: string
  type: 'google' | 'oidc'
}

export interface OAuthLinkedAccount {
  id: number
  provider_id: number
  provider_name: string
  provider_type: string
  email: string
  created_at: string
}

export interface OAuthProviderAdmin {
  id: number
  name: string
  slug: string
  type: string
  issuer: string
  scopes: string
  enabled: boolean
  hidden: boolean
  auto_register: boolean
  allowed_domains: string
  created_at: string
}

export interface OAuthProviderInput {
  name: string
  slug: string
  type: string
  client_id: string
  client_secret: string
  issuer?: string
  auth_url?: string
  token_url?: string
  userinfo_url?: string
  scopes?: string
  auto_register?: boolean
  hidden?: boolean
  allowed_domains?: string
}

export interface SSODiscovery {
  slug: string
  name: string
  type: string
}

export interface WorkspaceSSOConfig {
  provider_id: number
  provider_name: string
  enforce_sso: boolean
  auto_provision: boolean
  allowed_domains: string
}

// Subscribers
export type SubscriberStatus = 'subscribed' | 'unsubscribed' | 'bounced' | 'complained'

export interface Subscriber {
  id: number
  email: string
  name: string
  status: SubscriberStatus
  custom_fields: Record<string, any>
  subscribed_at: string | null
  unsubscribed_at: string | null
  created_at: string
  updated_at: string | null
}

export type SubscriberListType = 'static' | 'dynamic'

export interface FilterRule {
  field: string
  operator: 'eq' | 'neq' | 'contains' | 'starts_with' | 'ends_with' | 'gt' | 'lt' | 'in'
  value: any
}

export interface SubscriberListItem {
  id: number
  name: string
  description: string
  type: SubscriberListType
  filter_rules?: FilterRule[]
  member_count: number
  created_at: string
  updated_at: string | null
}

export interface BulkImportResult {
  created: number
  skipped: number
  total: number
  // Imported anyway, with no timezone.
  invalid_timezones?: number
}

// Campaigns
export type CampaignStatus = 'draft' | 'scheduled' | 'sending' | 'sent' | 'paused' | 'cancelled'

/** One weighted A/B branch of a campaign (R-08). */
export interface CampaignABTestVariant {
  name: string
  subject: string
  template_id?: number | null
  split_percentage: number
}

export type CampaignABTestMetric = 'open_rate' | 'click_rate'

export interface CampaignStats {
  total: number
  pending: number
  queued: number
  sent: number
  failed: number
  skipped: number
}

export interface Campaign {
  id: number
  name: string
  subject: string
  from_email: string
  from_name: string
  template_id: number
  template_version_id?: number
  language: string
  template_data?: Record<string, any>
  status: CampaignStatus
  list_id: number
  send_rate: number
  // R-09: timezone-aware sending
  send_at_local_time?: boolean
  local_send_hour?: number | null
  // R-08: A/B test
  ab_test_enabled?: boolean
  ab_test_variants?: CampaignABTestVariant[]
  ab_test_metric?: CampaignABTestMetric
  ab_test_window_hours?: number
  ab_test_min_sample?: number
  ab_test_winner?: string
  ab_test_winner_sent?: boolean
  scheduled_at?: string
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at?: string
  stats?: CampaignStats
}

/** Per-variant engagement for the campaign detail's A/B table (R-08). */
export interface CampaignABVariantStat {
  variant: string
  sent: number
  opened: number
  clicked: number
  open_rate: number
  click_rate: number
  winner: boolean
}

export interface CampaignABTestStats {
  enabled: boolean
  metric: CampaignABTestMetric
  window_hours: number
  min_sample: number
  winner?: string
  winner_sent: boolean
  window_ends_at?: string
  decision?: Record<string, any>
  variants: CampaignABVariantStat[]
}

/** One timezone row of the campaign detail's distribution list (R-09). */
export interface CampaignTimezoneCount {
  timezone: string
  count: number
}

export interface CampaignTimezones {
  send_at_local_time: boolean
  local_send_hour?: number | null
  timezones: CampaignTimezoneCount[]
}

export type CampaignMessageStatus = 'pending' | 'queued' | 'sent' | 'failed' | 'skipped'

export interface CampaignMessage {
  id: number
  campaign_id: number
  subscriber_id: number
  email_id?: number
  status: CampaignMessageStatus
  error_message?: string
  sent_at?: string
  created_at: string
  subscriber?: Subscriber
}

// Plans
export interface Plan {
  id: number
  name: string
  description: string
  is_default: boolean
  is_active: boolean
  daily_rate_limit: number
  hourly_rate_limit: number
  max_attachment_size_mb: number
  max_batch_size: number
  max_api_keys: number
  max_domains: number
  max_smtp_servers: number
  max_workspaces: number
  email_log_retention_days: number
  created_at: string
  updated_at: string
}

export interface AdminWorkspace {
  id: number
  name: string
  slug: string
  owner_id: number
  plan_id: number | null
  plan_name: string
  created_at: string
  updated_at: string
}

export interface PlanInput {
  name: string
  description: string
  is_default?: boolean
  daily_rate_limit: number
  hourly_rate_limit: number
  max_attachment_size_mb: number
  max_batch_size: number
  max_api_keys: number
  max_domains: number
  max_smtp_servers: number
  max_workspaces: number
  email_log_retention_days: number
}

export interface CampaignAnalyticsData {
  analytics: {
    total_messages: number; sent_messages: number; failed_messages: number
    opened_messages: number; clicked_messages: number; bounced_messages: number; unsubscribed: number
    delivery_rate: number; open_rate: number; click_rate: number; bounce_rate: number; unsubscribe_rate: number
  }
  links: Array<{ id: number; original_url: string; hash: string; click_count: number }>
  open_series: Array<{ time: string; count: number }>
  click_series: Array<{ time: string; count: number }>
}

export type FormStatus = 'active' | 'paused' | 'archived'
export type NotifyMode = 'immediate' | 'hourly' | 'daily' | 'off'
export type MessageStatus = 'received' | 'flagged' | 'quarantined' | 'rejected'
export type MessageState = 'new' | 'open' | 'replied' | 'closed' | 'spam'
export type MessageReplyKind = 'operator' | 'inbound'
export type FilterKind = 'keyword' | 'phrase' | 'regex' | 'email' | 'domain' | 'ip'
export type FilterAction = 'score' | 'flag' | 'quarantine' | 'reject' | 'allowlist'

export interface Form {
  id: number
  uuid: string
  workspace_id?: number | null
  name: string
  slug: string
  description: string
  public_key: string
  status: FormStatus
  allowed_origins: string[]
  strict_origin: boolean
  redirect_url: string
  max_body_bytes: number
  max_fields: number
  allow_attachments: boolean
  honeypot_field: string
  require_nonce: boolean
  min_fill_seconds: number
  scan_enabled: boolean
  flag_threshold: number
  quarantine_threshold: number
  reject_threshold: number
  notify_enabled: boolean
  notify_emails: string[]
  notify_mode: NotifyMode
  notify_on_flagged: boolean
  reply_from: string
  reply_from_name: string
  retention_days: number
  message_count: number
  spam_count: number
  last_message_at?: string | null
  created_at: string
  updated_at?: string | null
}

export interface MessageField {
  key: string
  value: string
}

export interface MessageReply {
  id: number
  uuid: string
  message_id: number
  kind: MessageReplyKind
  author_id?: number | null
  from_addr: string
  to_addr: string
  subject: string
  html_body: string
  text_body: string
  email_uuid?: string
  sender_mismatch?: boolean
  created_at: string
  author?: { id: number; name: string; email: string } | null
}

export interface Message {
  id: number
  uuid: string
  workspace_id?: number | null
  form_id: number
  sender_email: string
  sender_name: string
  sender_phone: string
  subject: string
  body: string
  client_ip?: string
  user_agent?: string
  referer?: string
  origin?: string
  status: MessageStatus
  state: MessageState
  spam_score: number
  scan_reasons: string[] | null
  notified_at?: string | null
  assigned_to_id?: number | null
  read_at?: string | null
  replied_at?: string | null
  reply_count: number
  created_at: string
  updated_at?: string | null
  fields?: MessageField[]
  attachments?: InboundAttachmentMeta[]
  form?: Form | null
  assigned_to?: { id: number; name: string; email: string } | null
  replies?: MessageReply[]
}

export interface MessageFilterRule {
  id: number
  workspace_id?: number | null
  form_id?: number | null
  kind: FilterKind
  pattern: string
  action: FilterAction
  score: number
  fields: string[] | null
  case_sensitive: boolean
  enabled: boolean
  hit_count: number
  last_hit_at?: string | null
  note: string
  created_at: string
  updated_at?: string | null
}

export interface MessageStats {
  total: number
  unread: number
  spam: number
  forms: number
}

export interface FormSnippet {
  endpoint: string
  public_key: string
  html: string
  fetch: string
}

// ---- Security (admin: bans, detection events, rules, allowlist) ----

/** One ban row. `evidence` is a JSON string — parse before pretty-printing. */
export interface SecurityBan {
  id: number
  ip_address: string
  is_cidr: boolean
  reason_code: string
  reason_description: string
  /** JSON string, e.g. {"ports":[...],"counts":{...},"score":40} */
  evidence: string
  risk_score: number
  ban_type: 'automatic' | 'manual'
  offense_count: number
  /** "system" for automatic bans, the acting admin's email for manual ones. */
  created_by: string
  created_at: string
  updated_at: string
  /** null = permanent. */
  expires_at: string | null
}

/** One detection event row (signal crossing or ban lifecycle record). */
export interface SecurityEventRecord {
  id: number
  source_ip: string
  /** Signal name, or ban.created / ban.lifted / ban.expired_notice. */
  event_type: string
  severity: 'info' | 'warning' | 'critical'
  protocol: string
  destination_port?: number | null
  /** JSON string. */
  evidence: string
  risk_score: number
  action_taken: 'recorded' | 'banned' | 'rejected'
  created_at: string
}

export interface AllowlistEntry {
  id: number
  /** A single IP address or a CIDR block ("10.0.0.0/8"). */
  ip_cidr: string
  description: string
  created_by: string
  created_at: string
}

/** One day of the 14-day security trend; days without activity carry zeroes. */
export interface SecurityDailyCount {
  date: string
  events: number
  bans: number
}

export interface SecurityOverview {
  active_bans: number
  automatic_bans: number
  manual_bans: number
  bans_last_24h: number
  events_last_24h: number
  port_scans_detected: number
  auth_attacks_detected: number
  trend: SecurityDailyCount[]
}

/** One tunable security_* setting as the rules endpoints present it. */
export interface SecurityRule {
  key: string
  label: string
  description: string
  /** boolean for type "bool", number for type "int". */
  value: any
  type: 'int' | 'bool'
  /** Absent when the rule has no lower/upper bound. */
  min?: number
  max?: number
  group: string
}

/** One rule update for the batch PUT; the value travels as its stored string. */
export interface SecurityRuleInput {
  key: string
  value: string
  type: string
}

export interface SecurityBanInput {
  ip_address: string
  description?: string
  permanent: boolean
  /** Required when permanent is false. */
  duration_hours?: number
  reason_code?: string
}

/** Returned instead of a ban when the address is allowlisted and placement was suppressed. */
export interface SecurityBanSuppressed {
  message: string
  suppressed: boolean
}

export interface SecurityAllowlistInput {
  ip_cidr: string
  description?: string
}

// ---- Logs & message history (/workspaces/current/logs/*) ----

/** One timeline entry attached to a message-history row. */
export interface MessageEventRow {
  type: string
  occurred_at: string
}

/**
 * Slim message-history list row (handlers.MessageRow): every metadata column
 * of an email, never its bodies. The event timeline is embedded so the list
 * can render state icons without one request per row.
 */
export interface MessageRow {
  uuid: string
  external_id?: string
  sender: string
  recipient: string
  subject: string
  status: Email['status']
  template_name?: string
  broadcast_id?: number
  broadcast_name?: string
  list_id?: number
  events: MessageEventRow[]
  error?: string
  provider?: string
  smtp_hostname?: string
  api_key_id?: number
  created_at: string
  sent_at?: string
  delivered_at?: string
  opened_at?: string
  clicked_at?: string
  bounced_at?: string
  complained_at?: string
  unsubscribed_at?: string
}

/**
 * One message without its bodies (GET /logs/messages/{uuid} serves the slim
 * Email projection): the same metadata columns as the list rows, plus the
 * unsubscribe header fields. Body fields are empty strings in this projection —
 * content is only served by the dedicated content endpoint.
 */
export interface MessageDetailRow {
  id: number
  uuid: string
  external_id?: string
  user_id: number
  workspace_id?: number
  api_key_id?: number | null
  api_key_name?: string
  sender: string
  recipients: string[] | null
  subject: string
  template_name?: string
  html_body: string
  text_body: string
  attachments_json?: string
  headers_json?: string
  template_data?: string
  list_unsubscribe_url?: string
  list_unsubscribe_mailto?: string
  list_unsubscribe_post?: boolean
  unsubscribe_list_id?: number | null
  status: Email['status']
  error_message?: string
  retry_count: number
  created_at: string
  sent_at: string | null
  scheduled_at: string | null
  provider?: string
  delivered_at?: string
  opened_at?: string
  clicked_at?: string
  bounced_at?: string
  complained_at?: string
  unsubscribed_at?: string
  smtp_hostname?: string
}

/** One attachment of a message, without its content. */
export interface MessageAttachmentInfo {
  filename: string
  content_type?: string
  storage_key?: string
}

/**
 * Content payload of the message-content endpoint. Answers
 * {redacted: true} — and nothing else — when the platform's
 * email_content_visibility setting hides content.
 */
export interface MessageContent {
  redacted?: boolean
  html_body?: string
  text_body?: string
  template_data?: Record<string, any>
  headers?: Record<string, any>
  attachments?: MessageAttachmentInfo[]
  template_name?: string
}

/** A full email_events timeline row (GET /logs/messages/{uuid}/events). */
export interface EmailEventRecord {
  id: number
  email_uuid: string
  workspace_id?: number
  type: string
  occurred_at: string
  provider: string
  provider_event_id?: string
  recipient: string
  ip?: string
  user_agent?: string
  url?: string
  error?: string
  metadata: string
  created_at: string
}

/** GET /workspaces/current/logs/classifications payload (SF-53). */
export interface ClassificationBreakdown {
  /** R-05 classification → recorded email_event count ("(none)" included). */
  counts: Record<string, number>
  total: number
  from?: string
  to?: string
}

/**
 * List row of a stored incoming webhook payload — the raw body itself is only
 * served by the detail endpoint.
 */
export interface IncomingWebhookEventRow {
  id: number
  source: string
  type: string
  email_uuid?: string
  recipient: string
  message_id?: string
  process_error?: string
  created_at: string
}

/** Detail of one incoming webhook event: the list row plus the raw payload. */
export interface IncomingWebhookEventDetail extends IncomingWebhookEventRow {
  raw_payload: string
}

/** Outgoing webhook delivery row with its recorded attempt count. */
export interface OutgoingDeliveryRow {
  id: number
  webhook_id: number
  user_id: number
  workspace_id?: number
  event: string
  status: 'success' | 'failed'
  http_status_code: number
  error_message?: string
  attempt: number
  created_at: string
  attempt_count: number
}

/** One recorded HTTP attempt of a webhook delivery. */
export interface DeliveryAttemptRow {
  id: number
  delivery_id: number
  attempt: number
  status_code: number
  response_body: string
  error?: string
  created_at: string
}

/** One audit event row: what happened, who did it, to what, and how it ended. */
export interface AuditRow {
  id: number
  category: Event['category']
  type: string
  workspace_id?: number | null
  actor_id: number | null
  actor_name: string
  client_ip?: string
  message: string
  metadata: string
  outcome?: 'success' | 'failure' | 'denied'
  target_type?: string
  target_id?: string
  created_at: string
}

// Automations
// The shapes mirror internal/handlers/automation_handler.go: the JSONB columns
// (trigger_config, nodes, stats) arrive inlined as parsed JSON, not strings.

export type AutomationStatus = 'draft' | 'live' | 'paused'
export type AutomationJourneyStatus = 'active' | 'completed' | 'exited' | 'failed'
export type AutomationNodeRunAction = 'entered' | 'processing' | 'completed' | 'failed'
export type AutomationNodeType =
  | 'trigger'
  | 'delay'
  | 'email'
  | 'filter'
  | 'ab_test'
  | 'list_status'
  | 'add_to_list'
  | 'remove_from_list'
  | 'webhook'

/**
 * One condition row of the automation condition builders (trigger + filter
 * nodes). Wider than the subscriber-list FilterRule: the automation engine
 * spells its operators out (equals/not_equals/contains, and exists/not_exists
 * on custom fields) and supports the in_list membership field.
 */
export interface AutomationFilterRule {
  field: string
  operator: string
  value: any
}

/** Trigger matching document, inlined from Automation.TriggerConfig. */
export interface AutomationTriggerConfig {
  event_kind: string
  list_id?: number | null
  custom_event_name?: string
  /** Schedule triggers (R-13): standard 5-field cron, optional CRON_TZ=. */
  cron_expression?: string
  /** Schedule-trigger audience when it targets a saved segment (R-13). */
  segment_id?: number | null
  /** 'once' (deduped per contact) or 'every_time'. */
  frequency: string
  conditions?: AutomationFilterRule[]
}

/** One workflow node, inlined from Automation.Nodes. */
export interface AutomationNodeSpec {
  id: string
  type: string
  config?: Record<string, any>
  /** Linear successor; branch nodes carry targets inside config instead. */
  next_node_id?: string | null
  position: { x: number; y: number }
}

/** One weighted A/B branch of an ab_test node config. */
export interface AutomationABVariant {
  id: string
  name: string
  weight: number
  next_node_id?: string
}

/** Aggregate counters, inlined from Automation.Stats. */
export interface WorkflowStats {
  enrolled: number
  completed: number
  exited: number
  failed: number
}

/** One automation row as returned by the list/get/create/update endpoints. */
export interface AutomationRow {
  id: number
  uuid: string
  user_id?: number
  workspace_id?: number | null
  name: string
  status: AutomationStatus
  list_id?: number | null
  exit_on_reply: boolean
  trigger_config: AutomationTriggerConfig
  nodes: AutomationNodeSpec[]
  root_node_id: string
  stats: WorkflowStats
  version: number
  created_at: string
  updated_at: string
}

/** Builder document behind POST/PUT /automations. */
export interface SaveAutomationPayload {
  name: string
  list_id?: number | null
  exit_on_reply?: boolean
  /** Derived from the trigger node by the builder; the server re-derives it when absent. */
  trigger_config?: AutomationTriggerConfig
  nodes: AutomationNodeSpec[]
  root_node_id: string
}

/** One contact's run through an automation (a journey). */
export interface JourneyRow {
  id: number
  uuid: string
  automation_id: number
  workspace_id?: number | null
  subscriber_id: number
  email: string
  current_node_id: string
  status: AutomationJourneyStatus
  exit_reason?: string
  scheduled_at?: string | null
  retry_count: number
  last_error?: string
  context_json?: string
  entered_at: string
  last_advanced_at?: string | null
  completed_at?: string | null
}

/** One node execution on a journey's timeline. */
export interface NodeRunRow {
  id: number
  journey_id: number
  automation_id: number
  node_id: string
  node_type: string
  action: AutomationNodeRunAction
  /** JSON object string: email_uuid, status_code, chosen branch, ... */
  output: string
  error?: string
  duration_ms: number
  entered_at: string
  completed_at?: string | null
}

/** GET /automations/{id}/executions/{journeyId}: journey + node-run timeline. */
export interface AutomationExecutionDetail {
  journey: JourneyRow
  timeline: NodeRunRow[]
}

/** One node's aggregate over all journeys of an automation. */
export interface NodeStatRow {
  node_id: string
  node_type: string
  entered: number
  completed: number
  failed: number
}

/** GET /automations/{id}/stats response. */
export interface AutomationStatsResponse {
  enrolled: number
  completed: number
  exited: number
  failed: number
  node_stats: NodeStatRow[]
}

/** POST /automations/events response: accepted is always true. */
export interface AutomationEventResponse {
  accepted: boolean
  enrolled: boolean
  subscriber_id?: number
  email?: string
  /** Why the event did not enroll: unknown_subscriber, trigger_unavailable or enroll_failed. */
  reason?: string
}

// ─── Segments ───────────────────────────────────────────────────────────────

/** How sibling nodes are combined: groups by condition.logic, rules by group.logic. */
export type SegmentLogic = 'and' | 'or'

/** One condition row. `value` is always a string (multi-values comma-separated). */
export interface SegmentRule {
  field: string
  operator: string
  value: string
}

export interface SegmentRuleGroup {
  logic: SegmentLogic
  rules: SegmentRule[]
}

export interface SegmentCondition {
  logic: SegmentLogic
  groups: SegmentRuleGroup[]
}

export interface Segment {
  id: number
  user_id: number
  workspace_id: number
  name: string
  description: string
  condition: SegmentCondition
  created_at: string
  updated_at: string
}

/** List rows carry the live subscriber count next to the saved segment. */
export interface SegmentRow extends Segment {
  matching_count: number
}

export interface SegmentInput {
  name: string
  description?: string
  condition: SegmentCondition
}

/** One sampled subscriber row of a preview / saved-segment contacts response. */
export interface SegmentPreviewContact {
  id: number
  email: string
  name: string
  status: string
  timezone: string
  language: string
  created_at: string
  subscribed_at: string
}

/** POST /segments/preview and GET /segments/{id}/contacts payload. */
export interface SegmentPreview {
  count: number
  page: number
  size: number
  total_pages: number
  contacts: SegmentPreviewContact[]
}

// ---- Delivery queues (admin, asynq inspection) ----

/** Per-queue stats of one send-pipeline queue. */
export interface QueueStats {
  queue: string
  size: number
  pending: number
  active: number
  scheduled: number
  retry: number
  /** Dead-letter set: exhausted tasks awaiting operator action. */
  archived: number
  processed: number
  failed: number
  latency_ms: number
  /** True while the queue is paused (no tasks handed to workers). */
  paused?: boolean
}

/** GET /admin/queues payload. */
export interface QueuesOverview {
  queues: QueueStats[]
  active_workers: number
}

/** One asynq task in an admin queue listing. */
export interface QueueTask {
  id: string
  type: string
  queue: string
  state: string
  payload_snippet: string
  max_retry: number
  retried: number
  last_error?: string
  last_failed_at?: string | null
  next_process_at?: string | null
}

/** GET /admin/queues/{queue}/tasks payload (page-local count). */
export interface QueueTaskPage {
  queue: string
  state: string
  tasks: QueueTask[]
  page: number
  size: number
  total: number
}

/** Per-queue outcome of POST /admin/queue/pause or /admin/queue/resume. */
export interface QueuePauseState {
  queue: string
  paused: boolean
  /** False when the queue was already in the requested state. */
  changed: boolean
}

/** POST /admin/queue/pause and /admin/queue/resume payload. */
export interface QueuePauseResult {
  paused: boolean
  queues: QueuePauseState[]
}

/** POST /admin/queue/actions/{action} payload. */
export interface QueueActionResponse {
  action: 'requeue_stuck' | 'purge_archived_before'
  /** Emails the reconcile sweep re-enqueued (requeue_stuck). */
  requeued: number
  /** Dead-lettered tasks deleted (purge_archived_before). */
  purged: number
  queues: string[]
}

// ---- Managed DKIM signing keys (workspace domains) ----

/** The public DNS record an operator must publish for a signing key. */
export interface DnsRecordView {
  host: string
  type: string
  value: string
  ttl: number
}

/**
 * One managed DKIM signing key. The private key never leaves the server —
 * responses carry only the public DNS record.
 */
export interface SigningKey {
  id: number
  domain_id: number
  domain?: string
  selector: string
  algorithm: 'rsa-sha256' | 'ed25519-sha256' | string
  status: 'active' | 'retiring' | 'retired' | string
  /** pending-retry = TXT record not confirmed in DNS yet; published = confirmed. */
  dns_state: 'published' | 'pending-retry' | string
  dns_published_at?: string | null
  created_at: string
  dns_record?: DnsRecordView | null
}

// ---- Domain deliverability health (R-07) ----

/** One auth check (SPF / DKIM / DMARC) with the operator guidance when it fails. */
export interface HealthCheckResult {
  verified: boolean
  record?: string
  guidance?: string
}

/** State of the managed DKIM signing key (R-02) for this domain. */
export interface HealthSigningStatus {
  enabled: boolean
  selector?: string
  algorithm?: string
  dns_state?: string
  guidance?: string
}

/** Recent deliverability record of mail from this domain; rates are percent of sent. */
export interface HealthSendingHealth {
  sent: number
  bounced: number
  complained: number
  bounce_rate: number
  complaint_rate: number
  since: string
}

/** Payload of GET/POST /workspaces/current/domains/{id}/health. */
export interface DomainHealthReport {
  domain_id: number
  domain: string
  ownership_verified: boolean
  spf: HealthCheckResult
  dkim: HealthCheckResult
  dmarc: HealthCheckResult
  signing: HealthSigningStatus
  sending: HealthSendingHealth
  records?: DnsRecords
  checked_at: string
}

/** Roll-up of the RFC 7489 aggregate reports ingested for one domain (R-17). */
export interface DmarcSummary {
  /** Number of stored report records. */
  record_count: number
  /** Total message volume the records carried. */
  message_count: number
  dkim_aligned_messages: number
  spf_aligned_messages: number
  /** Message-weighted alignment ratios (0..1; 0 when nothing is stored). */
  dkim_alignment: number
  spf_alignment: number
  /** Policy disposition receiving providers reported applying. */
  disposition_none: number
  disposition_quarantine: number
  disposition_reject: number
  /** End of the newest stored report window; null when nothing is stored. */
  latest_window_end: string | null
}

/** One stored DMARC aggregate-report record (models.DMARCReport). */
export interface DmarcReportRecord {
  id: number
  user_id: number
  workspace_id?: number
  domain_id?: number
  inbound_id: number
  domain: string
  reporter: string
  reporter_email: string
  report_id: string
  source_ip: string
  window_start: string
  window_end: string
  count: number
  dkim_aligned: boolean
  spf_aligned: boolean
  /** none | quarantine | reject */
  disposition: 'none' | 'quarantine' | 'reject' | string
  header_from: string
  created_at: string
}

/** Payload of GET /workspaces/current/domains/{id}/dmarc. */
export interface DomainDmarcResponse {
  summary: DmarcSummary
  records: DmarcReportRecord[]
}
