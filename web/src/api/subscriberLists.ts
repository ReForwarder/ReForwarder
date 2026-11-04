import api from './client'
import type { ApiResponse, PaginatedResponse, SubscriberListItem, Subscriber, FilterRule } from './types'

// Segment preview response: the match count plus a small sample of the
// matching subscribers (first page, server-capped).
export interface SegmentPreviewSubscriber {
  id: number
  email: string
  name: string
  status: string
}

export interface SegmentPreview {
  count: number
  subscribers: SegmentPreviewSubscriber[]
}

export const subscriberListsApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<SubscriberListItem>>('/workspaces/current/subscriber-lists', { params: { page, size } })
  },
  get(id: number) {
    return api.get<ApiResponse<SubscriberListItem>>(`/workspaces/current/subscriber-lists/${id}`)
  },
  create(data: { name: string; description: string; type: string; filter_rules?: FilterRule[] }) {
    return api.post<ApiResponse<SubscriberListItem>>('/workspaces/current/subscriber-lists', data)
  },
  update(id: number, data: { name?: string; description?: string; filter_rules?: FilterRule[] }) {
    return api.put<ApiResponse<SubscriberListItem>>(`/workspaces/current/subscriber-lists/${id}`, data)
  },
  delete(id: number) {
    return api.delete(`/workspaces/current/subscriber-lists/${id}`)
  },
  listMembers(id: number, page = 0, size = 20) {
    return api.get<PaginatedResponse<Subscriber>>(`/workspaces/current/subscriber-lists/${id}/members`, { params: { page, size } })
  },
  addMember(id: number, subscriberId: number) {
    return api.post<ApiResponse<null>>(`/workspaces/current/subscriber-lists/${id}/members`, { subscriber_id: subscriberId })
  },
  removeMember(id: number, subscriberId: number) {
    return api.delete(`/workspaces/current/subscriber-lists/${id}/members`, { data: { subscriber_id: subscriberId } })
  },
  previewSegment(filterRules: FilterRule[]) {
    return api.post<ApiResponse<SegmentPreview>>('/workspaces/current/subscriber-lists/preview-segment', { filter_rules: filterRules })
  },
}
