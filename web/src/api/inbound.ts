import api from './client'
import type { ApiResponse, PaginatedResponse, InboundEmail, InboundQuarantine } from './types'

export interface InboundListParams {
  page?: number
  size?: number
  status?: string
  source?: string
  sender?: string
  q?: string
}

export interface InboundRetryResponse {
  id: string
  status: string
}

const WORKSPACE_KEY = 'posta_workspace_id'

// Browser-direct requests (download links, EventSource) cannot set the
// X-Posta-Workspace-Id header, so the active workspace travels as a query
// parameter that the backend's WorkspaceFromQueryOrHeader middleware reads.
function workspaceParam(): string {
  const ws = localStorage.getItem(WORKSPACE_KEY)
  return ws ? `workspace_id=${encodeURIComponent(ws)}` : ''
}

export const inboundApi = {
  list(params: InboundListParams = {}) {
    return api.get<PaginatedResponse<InboundEmail>>('/workspaces/current/inbound-emails', {
      params: { page: 0, size: 20, ...params },
    })
  },
  get(uuid: string) {
    return api.get<ApiResponse<InboundEmail>>(`/workspaces/current/inbound-emails/${uuid}`)
  },
  delete(uuid: string) {
    return api.delete<ApiResponse<void>>(`/workspaces/current/inbound-emails/${uuid}`)
  },
  retry(uuid: string) {
    return api.post<ApiResponse<InboundRetryResponse>>(`/workspaces/current/inbound-emails/${uuid}/retry`)
  },
  rawUrl(uuid: string) {
    const ws = workspaceParam()
    return `/api/v1/workspaces/current/inbound-emails/${uuid}/raw${ws ? `?${ws}` : ''}`
  },
  attachmentUrl(uuid: string, idx: number) {
    const ws = workspaceParam()
    return `/api/v1/workspaces/current/inbound-emails/${uuid}/attachments/${idx}${ws ? `?${ws}` : ''}`
  },
  // No token in the URL: EventSource sends the HttpOnly session cookie on the
  // handshake, same as the download links above.
  streamUrl() {
    const ws = workspaceParam()
    return `/api/v1/workspaces/current/inbound-stream${ws ? `?${ws}` : ''}`
  },
}

export interface QuarantineListParams {
  page?: number
  size?: number
  status?: string
  domain_id?: number
}

// Quarantine review surface (OB-2, ADR-002 decision 3): held messages,
// release (re-runs routing), permanent delete.
export const inboundQuarantineApi = {
  list(params: QuarantineListParams = {}) {
    return api.get<PaginatedResponse<InboundQuarantine>>('/workspaces/current/inbound-quarantine', {
      params: { page: 0, size: 20, ...params },
    })
  },
  release(id: number) {
    return api.post<ApiResponse<InboundQuarantine>>(`/workspaces/current/inbound-quarantine/${id}/release`)
  },
  delete(id: number) {
    return api.delete<ApiResponse<InboundQuarantine>>(`/workspaces/current/inbound-quarantine/${id}`)
  },
}
