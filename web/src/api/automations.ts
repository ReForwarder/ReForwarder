import api from './client'
import type {
  ApiResponse,
  PaginatedResponse,
  AutomationRow,
  AutomationStatsResponse,
  AutomationExecutionDetail,
  AutomationEventResponse,
  JourneyRow,
  SaveAutomationPayload,
} from './types'

const BASE = '/workspaces/current/automations'

export interface AutomationListParams {
  page?: number
  size?: number
  /** Filter by status: draft, live or paused. */
  status?: string
}

export interface AutomationExecutionListParams {
  page?: number
  size?: number
  /** Filter by status: active, completed, exited or failed. */
  status?: string
  /** Filter by contact email substring. */
  contact?: string
}

export interface AutomationEventInput {
  event: string
  contact_email: string
  properties?: Record<string, any>
}

/**
 * Error extracted from an automation API failure. Validation 400s come back as
 * a flat body — { error: "automation validation failed", validation_errors:
 * [...] } — unlike the envelope errors ({ error: { message } }), so both shapes
 * are handled here and callers surface them verbatim.
 */
export interface AutomationApiError {
  message: string
  validationErrors: string[]
}

export function automationApiError(e: any, fallback = 'Request failed'): AutomationApiError {
  const data = e?.response?.data
  const validationErrors: string[] = Array.isArray(data?.validation_errors)
    ? data.validation_errors.filter((v: unknown) => typeof v === 'string')
    : []
  let message = ''
  if (typeof data?.error === 'string') message = data.error
  else if (data?.error?.message) message = data.error.message
  else if (data?.error?.error) message = data.error.error
  else if (data?.message) message = data.message
  return { message: message || fallback, validationErrors }
}

export const automationsApi = {
  list(params: AutomationListParams = {}) {
    return api.get<PaginatedResponse<AutomationRow>>(BASE, { params })
  },

  get(id: number) {
    return api.get<ApiResponse<AutomationRow>>(`${BASE}/${id}`)
  },

  create(payload: SaveAutomationPayload) {
    return api.post<ApiResponse<AutomationRow>>(BASE, payload)
  },

  update(id: number, payload: SaveAutomationPayload) {
    return api.put<ApiResponse<AutomationRow>>(`${BASE}/${id}`, payload)
  },

  remove(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`${BASE}/${id}`)
  },

  activate(id: number) {
    return api.post<ApiResponse<AutomationRow>>(`${BASE}/${id}/activate`)
  },

  pause(id: number) {
    return api.post<ApiResponse<AutomationRow>>(`${BASE}/${id}/pause`)
  },

  duplicate(id: number) {
    return api.post<ApiResponse<AutomationRow>>(`${BASE}/${id}/duplicate`)
  },

  executions(id: number, params: AutomationExecutionListParams = {}) {
    return api.get<PaginatedResponse<JourneyRow>>(`${BASE}/${id}/executions`, { params })
  },

  execution(id: number, journeyId: number) {
    return api.get<ApiResponse<AutomationExecutionDetail>>(`${BASE}/${id}/executions/${journeyId}`)
  },

  stats(id: number) {
    return api.get<ApiResponse<AutomationStatsResponse>>(`${BASE}/${id}/stats`)
  },

  /** Always answers 202; enrolled says whether a live automation matched. */
  sendCustomEvent(body: AutomationEventInput) {
    return api.post<ApiResponse<AutomationEventResponse>>(`${BASE}/events`, body)
  },
}
