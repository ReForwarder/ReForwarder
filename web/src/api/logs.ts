import api from './client'
import type {
  ApiResponse,
  PaginatedResponse,
  MessageRow,
  MessageDetailRow,
  MessageContent,
  EmailEventRecord,
  EmailStatusView,
  ClassificationBreakdown,
  IncomingWebhookEventRow,
  IncomingWebhookEventDetail,
  OutgoingDeliveryRow,
  DeliveryAttemptRow,
  AuditRow,
} from './types'

const WORKSPACE_KEY = 'posta_workspace_id'

// Browser-direct requests (EventSource) cannot set the X-Posta-Workspace-Id
// header, so the active workspace travels as a query parameter that the
// backend's WorkspaceFromQueryOrHeader middleware reads (the inbound.ts
// pattern).
function workspaceParam(): string {
  const ws = localStorage.getItem(WORKSPACE_KEY)
  return ws ? `workspace_id=${encodeURIComponent(ws)}` : ''
}

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

  /**
   * Lightweight delivery-status probe (CA-02). Wires the backend
   * GET /workspaces/current/emails/{id}/status route, which until now had no
   * frontend caller (RF-F103); used by MessageDetail.vue to poll a queued or
   * processing send until it reaches a terminal state.
   */
  getEmailStatus(uuid: string) {
    return api.get<ApiResponse<EmailStatusView>>(`/workspaces/current/emails/${encodeURIComponent(uuid)}/status`)
  },

  /**
   * Classification breakdown (SF-53): R-05 bounce/rejection classification
   * counts over the workspace's recorded timeline.
   */
  getClassifications(params: { from?: string; to?: string } = {}) {
    return api.get<ApiResponse<ClassificationBreakdown>>(`${BASE}/classifications?${buildQuery(params)}`)
  },

  /**
   * SSE stream URL of one message's live timeline (SF-56). No token in the
   * URL: EventSource sends the HttpOnly session cookie on the handshake, and
   * the active workspace travels as the workspace_id query parameter.
   */
  messageEventsStreamUrl(uuid: string) {
    const ws = workspaceParam()
    return `/api/v1${BASE}/${encodeURIComponent(uuid)}/events/stream${ws ? `?${ws}` : ''}`
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
