import api from './client'
import type { ApiResponse, PaginatedResponse, Webhook, WebhookDelivery, WebhookInput } from './types'

export const webhooksApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<Webhook>>('/workspaces/current/webhooks', { params: { page, size } })
  },
  create(data: WebhookInput) {
    return api.post<ApiResponse<Webhook>>('/workspaces/current/webhooks', data)
  },
  delete(id: number) {
    return api.delete(`/workspaces/current/webhooks/${id}`)
  },
}

/** Result of one operator-initiated replay of a failed delivery (R-06). */
export interface WebhookReplayResult {
  id: number
  webhook_id: number
  status: 'success' | 'failed'
  http_status_code: number
  attempt: number
  error_message?: string
  replay: { attempt: number; status_code: number; status: 'success' | 'failed'; error?: string } | null
}

export const webhookDeliveriesApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<WebhookDelivery>>('/workspaces/current/webhook-deliveries', { params: { page, size } })
  },
  // R-06: re-deliver a failed delivery with its original stored payload
  // through the same SSRF-guarded transport. Limited to 5 replays per
  // delivery per hour.
  replay(id: number) {
    return api.post<ApiResponse<WebhookReplayResult>>(`/workspaces/current/webhook-deliveries/${id}/replay`)
  },
}
