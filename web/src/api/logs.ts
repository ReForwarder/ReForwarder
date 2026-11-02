import api from './client'
import type {
  ApiResponse,
  PaginatedResponse,
  MessageRow,
  MessageDetailRow,
  MessageContent,
  EmailEventRecord,
  IncomingWebhookEventRow,
  IncomingWebhookEventDetail,
  OutgoingDeliveryRow,
  DeliveryAttemptRow,
  AuditRow,
} from './types'

/**
 * Query-string builder for the logs endpoints. Built by hand instead of
 * axios' params because the message-list status filter is repeatable and
 * non-exclusive — the server reads `status=sent&status=opened` (ANY-of), not
 * axios' default `status[]=sent` bracket form.
 */
function buildQuery<T extends object>(params: T): string {
  const q = new URLSearchParams()
  for (const [key, value] of Object.entries(params) as [string, string | number | string[] | undefined][]) {
    if (value === undefined || value === '') continue
    if (Array.isArray(value)) {
      for (const v of value) q.append(key, v)
    } else {
      q.append(key, String(value))
    }
  }
  return q.toString()
}

export interface MessageListParams {
  page?: number
  size?: number
  /** Repeatable: each entry becomes its own status= param (ANY-of match). */
  status?: string[]
  channel?: string
  contact?: string
  message_id?: string
  external_id?: string
  list_id?: string
  template_id?: string
  block_id?: string
  broadcast_id?: string
}

export interface IncomingWebhookListParams {
  page?: number
  size?: number
  source?: string
  type?: string
  recipient?: string
  email_uuid?: string
}

export interface OutgoingWebhookListParams {
  page?: number
  size?: number
  webhook_id?: string
  status?: string
}

export interface AuditListParams {
  page?: number
  size?: number
  action?: string
  outcome?: string
  actor?: string
  target_type?: string
  target_id?: string
  ip?: string
  from?: string
  to?: string
  /** workspace (default) or deployment — deployment requires an admin session. */
  scope?: string
}

const BASE = '/workspaces/current/logs'

export const logsApi = {
  listMessages(params: MessageListParams = {}) {
    return api.get<PaginatedResponse<MessageRow>>(`${BASE}/messages?${buildQuery(params)}`)
  },

  getMessage(uuid: string) {
    return api.get<ApiResponse<MessageDetailRow>>(`${BASE}/messages/${uuid}`)
  },

  getMessageContent(uuid: string) {
    return api.get<ApiResponse<MessageContent>>(`${BASE}/messages/${uuid}/content`)
  },

  getMessageEvents(uuid: string) {
    return api.get<ApiResponse<EmailEventRecord[]>>(`${BASE}/messages/${uuid}/events`)
  },

  listIncoming(params: IncomingWebhookListParams = {}) {
    return api.get<PaginatedResponse<IncomingWebhookEventRow>>(`${BASE}/incoming-webhooks?${buildQuery(params)}`)
  },

  getIncoming(id: number) {
    return api.get<ApiResponse<IncomingWebhookEventDetail>>(`${BASE}/incoming-webhooks/${id}`)
  },

  listOutgoing(params: OutgoingWebhookListParams = {}) {
    return api.get<PaginatedResponse<OutgoingDeliveryRow>>(`${BASE}/outgoing-webhooks?${buildQuery(params)}`)
  },

  getOutgoingAttempts(id: number) {
    return api.get<ApiResponse<DeliveryAttemptRow[]>>(`${BASE}/outgoing-webhooks/${id}/attempts`)
  },

  listAudit(params: AuditListParams = {}) {
    return api.get<PaginatedResponse<AuditRow>>(`${BASE}/audit?${buildQuery(params)}`)
  },

  /**
   * CSV export URL for the audit log — the same filters and authorization as
   * listAudit. Opens as a plain GET (the session cookie travels with it) and
   * streams text/csv as a download.
   */
  getAuditExportUrl(params: AuditListParams = {}) {
    const { page: _page, size: _size, ...filters } = params
    const base = (api.defaults.baseURL as string | undefined) || '/api/v1'
    return `${base}${BASE}/audit/export?${buildQuery(filters)}`
  },
}
