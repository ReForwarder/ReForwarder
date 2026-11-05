import api from './client'
import type { ApiResponse, PaginatedResponse, ApiKey, ApiKeyCreateResponse, ApiKeyUsage } from './types'

export const apiKeysApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<ApiKey>>('/workspaces/current/api-keys', { params: { page, size } })
  },
  get(id: number) {
    return api.get<ApiResponse<ApiKey>>(`/workspaces/current/api-keys/${id}`)
  },
  create(name: string, allowedIPs?: string[], scopes?: string[], expiresInDays?: number, rateLimitHourly?: number, rateLimitDaily?: number) {
    const body: Record<string, any> = { name }
    if (allowedIPs && allowedIPs.length > 0) body.allowed_ips = allowedIPs
    if (scopes && scopes.length > 0) body.scopes = scopes
    if (expiresInDays !== undefined) body.expires_in_days = expiresInDays
    // R-15: optional per-key request caps (0/undefined = unlimited).
    if (rateLimitHourly !== undefined && rateLimitHourly > 0) body.rate_limit_hourly = rateLimitHourly
    if (rateLimitDaily !== undefined && rateLimitDaily > 0) body.rate_limit_daily = rateLimitDaily
    return api.post<ApiResponse<ApiKeyCreateResponse>>('/workspaces/current/api-keys', body)
  },
  revoke(id: number) {
    return api.put(`/workspaces/current/api-keys/${id}/revoke`)
  },
  delete(id: number) {
    return api.delete(`/workspaces/current/api-keys/${id}`)
  },
  /** Trailing daily usage series + totals for one key (R-18). */
  usage(id: number, days = 30) {
    return api.get<ApiResponse<ApiKeyUsage>>(`/workspaces/current/api-keys/${id}/usage`, { params: { days } })
  },
}
