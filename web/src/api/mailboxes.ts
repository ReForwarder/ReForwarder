import api from './client'
import type { ApiResponse, PaginatedResponse } from './types'

// ─── Admin mailbox administration (OB-5 W2, ADR-005 decision 3) ────────────
// The platform-wide hosted-mailbox surface: provision, quota changes,
// suspend/reactivate, cascade delete and usage. Backend: the /admin/mailboxes
// routes (internal/routes/admin_routes.go) — admin-gated and audited as
// admin.mailbox.* (SF-74).

/** One row of the admin mailbox table (handlers.AdminMailboxRow). */
export interface AdminMailbox {
  id: number
  user_id: number
  owner_name: string
  owner_email: string
  workspace_id?: number | null
  name: string
  quota_bytes: number
  quota_messages: number
  used_bytes: number
  message_count: number
  status: 'active' | 'suspended' | 'closed'
  created_at: string
}

/** The per-mailbox usage report (mailstore.MailboxUsageReport). */
export interface AdminMailboxUsage {
  mailbox_id: number
  user_id: number
  name: string
  quota_bytes: number
  raw_bytes: number
  message_count: number
  blob_bytes: number
  distinct_blobs: number
}

export interface MailboxProvisionInput {
  user_id: number
  name: string
  quota_bytes?: number
  quota_messages?: number
  workspace_id?: number
}

export interface MailboxUpdateInput {
  status?: 'active' | 'suspended'
  quota_bytes?: number
  quota_messages?: number
}

export const adminMailboxesApi = {
  list(page = 0, size = 20, search = '', user = 0, workspace = 0, status: '' | AdminMailbox['status'] = '') {
    return api.get<PaginatedResponse<AdminMailbox>>('/admin/mailboxes', {
      params: {
        page,
        size,
        search: search || undefined,
        user: user || undefined,
        workspace: workspace || undefined,
        status: status || undefined,
      },
    })
  },
  provision(input: MailboxProvisionInput) {
    return api.post<ApiResponse<AdminMailbox>>('/admin/mailboxes', input)
  },
  update(id: number, input: MailboxUpdateInput) {
    return api.patch<ApiResponse<AdminMailbox>>(`/admin/mailboxes/${id}`, input)
  },
  // Irreversible: removes the mailbox with all folders and messages (the
  // dashboard confirms before calling).
  remove(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/admin/mailboxes/${id}`)
  },
  usage(id: number) {
    return api.get<ApiResponse<AdminMailboxUsage>>(`/admin/mailboxes/${id}/usage`)
  },
}
