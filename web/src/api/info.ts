import api from './client'

export interface AppInfo {
  name: string
  version: string
  commit_id: string
  openapi_docs: boolean
}

export const infoApi = {
  get: () => api.get<{ success: boolean; data: AppInfo }>('/info'),
}
