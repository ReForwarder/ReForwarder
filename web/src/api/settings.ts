import api from './client'
import type {
  AdminSetting,
  AdminSettingHistoryEntry,
  AdminSettingInput,
  ApiResponse,
  PaginatedResponse,
  UserSettings,
  WorkspaceSettings,
} from './types'

export const settingsApi = {
  // Admin platform settings
  getAdminSettings() {
    return api.get<ApiResponse<AdminSetting[]>>('/admin/settings')
  },
  updateAdminSettings(settings: AdminSettingInput[]) {
    return api.put<ApiResponse<AdminSetting[]>>('/admin/settings', { settings })
  },

  // Per-key change history + rollback (SF-100). The history is paged
  // newest-first; a rollback restores a recorded prior value through the
  // normal settings setter (it produces its own history entry).
  getSettingHistory(key: string, page = 0, size = 10) {
    return api.get<PaginatedResponse<AdminSettingHistoryEntry>>('/admin/settings/history', {
      params: { key, page, size },
    })
  },
  rollbackSetting(key: string, historyId?: number) {
    return api.post<ApiResponse<AdminSetting[]>>('/admin/settings/history/rollback', {
      key,
      ...(historyId !== undefined ? { history_id: historyId } : {}),
    })
  },

  // User settings — personal notification preferences only after the
  // workspace-only migration cutover.
  getUserSettings() {
    return api.get<ApiResponse<UserSettings>>('/users/me/settings')
  },
  updateUserSettings(data: Partial<Omit<UserSettings, 'id' | 'user_id' | 'created_at' | 'updated_at'>>) {
    return api.put<ApiResponse<UserSettings>>('/users/me/settings', data)
  },

  // Workspace operational settings (timezone, sender defaults, webhook retries,
  // API-key expiry, bounce auto-suppress). Scoped via X-Posta-Workspace-Id.
  getWorkspaceSettings() {
    return api.get<ApiResponse<WorkspaceSettings>>('/workspaces/current/settings')
  },
  updateWorkspaceSettings(
    data: Partial<Omit<WorkspaceSettings, 'id' | 'workspace_id' | 'created_at' | 'updated_at'>>
  ) {
    return api.put<ApiResponse<WorkspaceSettings>>('/workspaces/current/settings', data)
  },
}
