import api from './client'
import type {
  ApiResponse,
  PaginatedResponse,
  Campaign,
  CampaignABTestMetric,
  CampaignABTestStats,
  CampaignABTestVariant,
  CampaignMessage,
  CampaignTimezones,
  CampaignAnalyticsData,
} from './types'

/** A/B test configuration fields (R-08). */
export interface CampaignABTestConfig {
  ab_test_enabled: boolean
  ab_test_variants?: CampaignABTestVariant[]
  ab_test_metric?: CampaignABTestMetric
  ab_test_window_hours?: number
  ab_test_min_sample?: number
}

export const campaignsApi = {
  list(page = 0, size = 20, status?: string) {
    return api.get<PaginatedResponse<Campaign>>('/workspaces/current/campaigns', { params: { page, size, status } })
  },
  get(id: number) {
    return api.get<ApiResponse<Campaign>>(`/workspaces/current/campaigns/${id}`)
  },
  // Audience is a XOR: send either list_id or segment_id, never both.
  create(data: {
    name: string
    subject: string
    from_email: string
    from_name?: string
    template_id: number
    template_version_id?: number
    language?: string
    template_data?: Record<string, any>
    list_id?: number
    segment_id?: number
    send_rate?: number
    // R-09: timezone-aware sending
    send_at_local_time?: boolean
    local_send_hour?: number | null
    // R-08: A/B test
    ab_test_enabled?: boolean
    ab_test_variants?: CampaignABTestVariant[]
    ab_test_metric?: CampaignABTestMetric
    ab_test_window_hours?: number
    ab_test_min_sample?: number
    scheduled_at?: string
  }) {
    return api.post<ApiResponse<Campaign>>('/workspaces/current/campaigns', data)
  },
  update(id: number, data: Partial<{
    name: string
    subject: string
    from_email: string
    from_name: string
    template_id: number
    template_version_id: number
    language: string
    template_data: Record<string, any>
    list_id: number
    segment_id: number
    send_rate: number
    send_at_local_time: boolean
    local_send_hour: number | null
    ab_test_enabled: boolean
    ab_test_variants: CampaignABTestVariant[]
    ab_test_metric: CampaignABTestMetric
    ab_test_window_hours: number
    ab_test_min_sample: number
    scheduled_at: string
  }>) {
    return api.put<ApiResponse<Campaign>>(`/workspaces/current/campaigns/${id}`, data)
  },
  delete(id: number) {
    return api.delete(`/workspaces/current/campaigns/${id}`)
  },
  send(id: number) {
    return api.post<ApiResponse<Campaign>>(`/workspaces/current/campaigns/${id}/send`)
  },
  pause(id: number) {
    return api.post<ApiResponse<Campaign>>(`/workspaces/current/campaigns/${id}/pause`)
  },
  resume(id: number) {
    return api.post<ApiResponse<Campaign>>(`/workspaces/current/campaigns/${id}/resume`)
  },
  cancel(id: number) {
    return api.post<ApiResponse<Campaign>>(`/workspaces/current/campaigns/${id}/cancel`)
  },
  listMessages(id: number, page = 0, size = 20, status?: string) {
    return api.get<PaginatedResponse<CampaignMessage>>(`/workspaces/current/campaigns/${id}/messages`, { params: { page, size, status } })
  },
  analytics(id: number) {
    return api.get<ApiResponse<CampaignAnalyticsData>>(`/workspaces/current/campaigns/${id}/analytics`)
  },
  // R-08: per-variant stats + winner decision for the campaign detail.
  abTest(id: number) {
    return api.get<ApiResponse<CampaignABTestStats>>(`/workspaces/current/campaigns/${id}/ab-test`)
  },
  // R-09: subscriber timezone distribution + timezone-aware sending config.
  timezones(id: number) {
    return api.get<ApiResponse<CampaignTimezones>>(`/workspaces/current/campaigns/${id}/timezones`)
  },
}
