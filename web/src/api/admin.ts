import api from './client'
import type {
  ApiResponse, PaginatedResponse, User, Event, AdminMetrics,
  UserDetailMetrics, CronJob, AdminWorkspace, UpdateInfo,
  Domain, AdminDomainRow, AdminDomainDetail, AdminDomainVerifyResult,
  AdminWorkspaceRow, AdminWorkspaceDetail,
  SecurityOverview, SecurityBan, SecurityBanInput, SecurityBanSuppressed,
  SecurityEventRecord, SecurityRule, SecurityRuleInput,
  AllowlistEntry, SecurityAllowlistInput,
  QueueStats, QueueTask, QueuesOverview, QueueTaskPage,
  QueuePauseResult, QueueActionResponse,
  ImpersonationResponse,
} from './types'
import type { Announcement, AnnouncementInput } from './inbox'

export const adminApi = {
  // ---- Announcements (platform-wide in-app notices) ----
  listAnnouncements(page = 0, size = 20) {
    return api.get<PaginatedResponse<Announcement>>('/admin/announcements', { params: { page, size } })
  },
  createAnnouncement(input: AnnouncementInput) {
    return api.post<ApiResponse<Announcement>>('/admin/announcements', input)
  },
  retractAnnouncement(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/admin/announcements/${id}`)
  },
  listUsers(page = 0, size = 20, search = '') {
    return api.get<PaginatedResponse<User>>('/admin/users', { params: { page, size, search: search || undefined } })
  },
  // ---- Impersonation (R-20) ----
  impersonateUser(id: number, reason: string) {
    return api.post<ApiResponse<ImpersonationResponse>>(`/admin/users/${id}/impersonate`, { reason })
  },
  // The exit route is reachable by the impersonated (non-admin) session, so it
  // lives outside the admin middleware — see internal/routes/user_routes.go.
  exitImpersonation() {
    return api.post<ApiResponse<{ message: string; original_admin_id: number }>>('/admin/impersonate/exit')
  },
  // ---- Domains (platform-wide) ----
  listDomains(page = 0, size = 20, search = '', status = '', workspace = 0) {
    return api.get<PaginatedResponse<AdminDomainRow>>('/admin/domains', {
      params: {
        page,
        size,
        search: search || undefined,
        status: status || undefined,
        workspace: workspace || undefined,
      },
    })
  },
  getDomain(id: number) {
    return api.get<ApiResponse<AdminDomainDetail>>(`/admin/domains/${id}`)
  },
  verifyDomain(id: number) {
    return api.post<ApiResponse<AdminDomainVerifyResult>>(`/admin/domains/${id}/verify`)
  },
  setDomainVerification(id: number, ownershipVerified: boolean, reason: string) {
    return api.put<ApiResponse<Domain>>(`/admin/domains/${id}/verification`, {
      ownership_verified: ownershipVerified,
      reason,
    })
  },

  // ---- Workspaces (platform-wide) ----
  listWorkspaces(page = 0, size = 20, search = '', plan = 0, owner = 0) {
    return api.get<PaginatedResponse<AdminWorkspaceRow>>('/admin/workspaces', {
      params: {
        page,
        size,
        search: search || undefined,
        plan: plan || undefined,
        owner: owner || undefined,
      },
    })
  },
  getWorkspace(id: number) {
    return api.get<ApiResponse<AdminWorkspaceDetail>>(`/admin/workspaces/${id}`)
  },

  createUser(name: string, email: string, password: string, role: string) {
    return api.post<ApiResponse<User>>('/admin/users', { name, email, password, role })
  },
  updateUser(id: number, data: { role?: string; active?: boolean; email_verified?: boolean }) {
    return api.put<ApiResponse<User>>(`/admin/users/${id}`, data)
  },
  deleteUser(id: number) {
    return api.delete(`/admin/users/${id}`)
  },
  forceDeleteUser(id: number) {
    return api.delete(`/admin/users/${id}/force`)
  },
  cancelUserDeletion(id: number) {
    return api.post<ApiResponse<{ message: string }>>(`/admin/users/${id}/cancel-deletion`)
  },
  getUserMetrics(id: number) {
    return api.get<ApiResponse<UserDetailMetrics>>(`/admin/users/${id}/metrics`)
  },
  disable2FA(id: number) {
    return api.delete(`/admin/users/${id}/2fa`)
  },
  revokeUserSessions(id: number) {
    return api.post<ApiResponse<{ message: string; revoked: number }>>(`/admin/users/${id}/revoke-sessions`)
  },
  getUserWorkspaces(id: number) {
    return api.get<ApiResponse<AdminWorkspace[]>>(`/admin/users/${id}/workspaces`)
  },
  // CA-02: the dead admin/api-keys and admin/emails client functions were
  // removed (2026-11-07) — no backend routes exist for them and no view ever
  // called them (RF-F103). Admin key management lives in the workspace-scoped
  // apikeys.ts surface; admin email inspection lives in logs.ts.

  getMetrics() {
    return api.get<ApiResponse<AdminMetrics>>('/admin/metrics')
  },
  listEvents(page = 0, size = 20, category?: string, search = '') {
    const params: Record<string, any> = { page, size }
    if (category) params.category = category
    if (search) params.search = search
    return api.get<PaginatedResponse<Event>>('/admin/events', { params })
  },
  getEvent(id: number) {
    return api.get<ApiResponse<Event>>(`/admin/events/${id}`)
  },
  listJobs() {
    return api.get<ApiResponse<CronJob[]>>('/admin/jobs')
  },
  // ---- Delivery queues (asynq inspection) ----
  listQueues() {
    return api.get<ApiResponse<QueuesOverview>>('/admin/queues')
  },
  listQueueTasks(queue: string, state: string, page = 0, size = 20) {
    return api.get<ApiResponse<QueueTaskPage>>(`/admin/queues/${queue}/tasks`, {
      params: { state, page, size },
    })
  },
  cancelQueueTask(queue: string, taskId: string) {
    return api.post<ApiResponse<{ message: string }>>(`/admin/queues/${queue}/tasks/${encodeURIComponent(taskId)}/cancel`)
  },
  runQueueTask(queue: string, taskId: string) {
    return api.post<ApiResponse<{ message: string }>>(`/admin/queues/${queue}/tasks/${encodeURIComponent(taskId)}/run`)
  },
  archiveQueueTask(queue: string, taskId: string) {
    return api.post<ApiResponse<{ message: string }>>(`/admin/queues/${queue}/tasks/${encodeURIComponent(taskId)}/archive`)
  },
  // ---- Global queue pause / resume (SF-54) ----
  pauseQueues() {
    return api.post<ApiResponse<QueuePauseResult>>('/admin/queue/pause')
  },
  resumeQueues() {
    return api.post<ApiResponse<QueuePauseResult>>('/admin/queue/resume')
  },
  // ---- Operational queue actions (SF-59) ----
  runQueueAction(action: 'requeue_stuck' | 'purge_archived_before', before?: string) {
    return api.post<ApiResponse<QueueActionResponse>>(`/admin/queue/actions/${action}`, before ? { before } : {})
  },
  getUpdate() {
    return api.get<ApiResponse<UpdateInfo>>('/admin/update')
  },
  dismissUpdate(version: string) {
    return api.post<ApiResponse<{ message: string }>>('/admin/update/dismiss', { version })
  },

  // ---- Security (ban list, detection events, rules, allowlist) ----
  getSecurityOverview() {
    return api.get<ApiResponse<SecurityOverview>>('/admin/security/overview')
  },
  listSecurityBans(page = 0, size = 20, search = '', filter: '' | 'active' | 'expired' | 'all' = 'active') {
    return api.get<PaginatedResponse<SecurityBan>>('/admin/security/bans', {
      params: { page, size, search: search || undefined, filter: filter || undefined },
    })
  },
  createSecurityBan(input: SecurityBanInput) {
    return api.post<ApiResponse<SecurityBan | SecurityBanSuppressed>>('/admin/security/bans', input)
  },
  unbanSecurityIp(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/admin/security/bans/${id}`)
  },
  extendSecurityBan(id: number, hours: number) {
    return api.post<ApiResponse<{ message: string }>>(`/admin/security/bans/${id}/extend`, { hours })
  },
  listSecurityEvents(page = 0, size = 20, eventType = '', severity = '') {
    return api.get<PaginatedResponse<SecurityEventRecord>>('/admin/security/events', {
      params: { page, size, event_type: eventType || undefined, severity: severity || undefined },
    })
  },
  getSecurityRules() {
    return api.get<ApiResponse<SecurityRule[]>>('/admin/security/rules')
  },
  updateSecurityRules(rules: SecurityRuleInput[]) {
    return api.put<ApiResponse<SecurityRule[]>>('/admin/security/rules', { settings: rules })
  },
  listAllowlist(page = 0, size = 20) {
    return api.get<PaginatedResponse<AllowlistEntry>>('/admin/security/allowlist', { params: { page, size } })
  },
  createAllowlistEntry(input: SecurityAllowlistInput) {
    return api.post<ApiResponse<AllowlistEntry>>('/admin/security/allowlist', input)
  },
  deleteAllowlistEntry(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/admin/security/allowlist/${id}`)
  },
}
