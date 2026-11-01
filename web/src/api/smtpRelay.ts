import api from './client'
import type { ApiResponse, PaginatedResponse, SMTPCredential, SMTPCredentialCreateResponse } from './types'

export const smtpRelayApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<SMTPCredential>>('/workspaces/current/smtp-credentials', { params: { page, size } })
  },
  create(name: string, allowedIPs?: string[]) {
    const body: Record<string, any> = { name }
    if (allowedIPs && allowedIPs.length > 0) body.allowed_ips = allowedIPs
    return api.post<ApiResponse<SMTPCredentialCreateResponse>>('/workspaces/current/smtp-credentials', body)
  },
  revoke(id: number) {
    return api.post(`/workspaces/current/smtp-credentials/${id}/revoke`)
  },
  delete(id: number) {
    return api.delete(`/workspaces/current/smtp-credentials/${id}`)
  },
}
