import api from './client'

/** Env-gated subsystems surfaced by GET /info (Section-11 gate). */
export interface AppInfoFeatures {
  /** POSTA_MESSAGES_ENABLED — Messages / Forms / Spam Filters pages. */
  messages_enabled: boolean
  /** POSTA_INBOUND_ENABLED — Inbound Emails page. */
  inbound_enabled: boolean
  /** POSTA_SMTP_RELAY_ENABLED — SMTP Relay endpoints. */
  relay_enabled: boolean
  /** POSTA_METRICS_ENABLED — /metrics scrape endpoint. */
  metrics_enabled: boolean
}

export interface AppInfo {
  name: string
  version: string
  commit_id: string
  openapi_docs: boolean
  /** Feature flags; a backend predating the Section-11 gate omits the object. */
  features?: AppInfoFeatures
}

export const infoApi = {
  get: () => api.get<{ success: boolean; data: AppInfo }>('/info'),
}
