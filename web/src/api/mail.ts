import api from './client'
import type { ApiResponse, PaginatedResponse } from './types'

// ─── Webmail (OB-5 W4, ADR-005 decision 1) ──────────────────────────────────
// The /mail surface over the hosted mailbox store: mailboxes with quota and
// usage (ADR-003 §5), the folder tree, paged message listings filterable by
// unread/flagged and the ADR-003 full-text query, the message view (sanitized
// HTML — remote content blocked — plus the attachment list), flag CAS, move,
// delete and mailbox-wide search. Backend: internal/routes/mail_routes.go —
// strictly scoped to the session user's own mailboxes (a foreign mailbox,
// folder or message reads as 404 exactly like a missing one).

/** GET /mail/mailboxes row (handlers.MailboxResponse). */
export interface Mailbox {
  id: number
  user_id: number
  workspace_id?: number | null
  name: string
  quota_bytes: number
  quota_messages: number
  used_bytes: number
  message_count: number
  blob_bytes: number
  distinct_blobs: number
  status: 'active' | 'suspended' | 'closed'
  created_at: string
}

/** GET /mail/folders row (handlers.MailFolderResponse). */
export interface MailFolder {
  id: number
  mailbox_id: number
  parent_id?: number | null
  name: string
  imap_path: string
  uid_validity: number
  uid_next: number
  subscribed: boolean
  total_count: number
  unseen_count: number
  created_at: string
}

export interface MailFolderCreateInput {
  mailbox: number
  name: string
  parent_path?: string
}

export interface MailFolderPatchInput {
  name?: string
  subscribed?: boolean
}

/** One attachment entry of the message view (handlers.MailAttachmentMeta). */
export interface MailAttachment {
  index: number
  name: string
  content_type: string
  size: number
}

/** GET /mail/messages list row (handlers.MailMessageSummary). */
export interface MailMessageSummary {
  id: number
  uid: number
  folder_id: number
  message_id: string
  subject: string
  from: string
  to: string
  cc?: string
  date: string
  size: number
  flags: number
  flags_list: string[]
  mod_seq: number
  snippet?: string
  has_attachments: boolean
  created_at: string
}

/** The renderable body: sanitized HTML (remote content blocked) or plain text
 * (handlers.MailMessageBody). HTML goes into a sandboxed iframe. */
export interface MailMessageBody {
  text: string
  html?: string
  has_html: boolean
}

/** GET /mail/messages/:uid row (handlers.MailMessageResponse). */
export interface MailMessage {
  id: number
  uid: number
  folder_id: number
  message_id: string
  subject: string
  from: string
  to: string
  cc?: string
  date: string
  size: number
  flags: number
  flags_list: string[]
  mod_seq: number
  body: MailMessageBody
  attachments: MailAttachment[]
  created_at: string
}

export interface MailMessageListParams {
  folder: number
  /** 1-based page — the handler's contract (page < 1 clamps to 1). */
  page?: number
  size?: number
  unread?: boolean
  flagged?: boolean
  /** ADR-003 full-text query; combines with the flag filters. */
  q?: string
}

export interface MailFlagUpdateInput {
  seen?: boolean
  flagged?: boolean
  answered?: boolean
  draft?: boolean
  /** CONDSTORE-style precondition: the mod_seq last observed. A lost race
   * answers 409. */
  if_mod_seq?: number
}

export interface MailFlagUpdateResult {
  uid: number
  flags: number
  mod_seq: number
}

export interface MailMoveResult {
  message_id: number
  folder_id: number
  uid: number
  mod_seq: number
}

export interface MailExpungeResult {
  expunged: number
  byte_delta: number
  uids: number[]
}

export interface MailSearchInput {
  mailbox?: number
  folder?: number
  q: string
  /** 1-based page. */
  page?: number
  size?: number
}

export interface MailMessageListResult {
  messages: MailMessageSummary[]
  pageable: PaginatedResponse<MailMessageSummary>['pageable']
}

const WORKSPACE_KEY = 'posta_workspace_id'

// Browser-direct requests (download links) cannot set the
// X-Posta-Workspace-Id header, so the active workspace travels as a query
// parameter that the backend's WorkspaceFromQueryOrHeader middleware reads
// (same idiom as inbound.ts).
function workspaceParam(): string {
  const ws = localStorage.getItem(WORKSPACE_KEY)
  return ws ? `workspace_id=${encodeURIComponent(ws)}` : ''
}

export const mailApi = {
  /** The session user's mailboxes with their quota and usage counters. */
  mailboxes() {
    return api.get<ApiResponse<Mailbox[]>>('/mail/mailboxes')
  },

  /** One mailbox's folder tree with live total/unseen counters. */
  folders(mailboxId: number) {
    return api.get<ApiResponse<MailFolder[]>>('/mail/folders', { params: { mailbox: mailboxId } })
  },

  createFolder(input: MailFolderCreateInput) {
    return api.post<ApiResponse<MailFolder>>('/mail/folders', input)
  },

  /** Rename and/or (un)subscribe a folder; renaming INBOX is refused. */
  patchFolder(id: number, input: MailFolderPatchInput) {
    return api.patch<ApiResponse<MailFolder>>(`/mail/folders/${id}`, input)
  },

  /** Deletes the folder and its whole subtree with all messages. */
  deleteFolder(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/mail/folders/${id}`)
  },

  /** Permanently removes the folder's \Deleted-marked messages. */
  expungeFolder(id: number) {
    return api.post<ApiResponse<MailExpungeResult>>(`/mail/folders/${id}/expunge`)
  },

  /** Paged listing of one folder (UID ascending), filterable by unread,
   * flagged and the full-text query `q`. */
  async messages(params: MailMessageListParams): Promise<MailMessageListResult> {
    const res = await api.get<PaginatedResponse<MailMessageSummary>>('/mail/messages', {
      params: {
        folder: params.folder,
        page: params.page ?? 1,
        size: params.size ?? 25,
        unread: params.unread || undefined,
        flagged: params.flagged || undefined,
        q: params.q?.trim() || undefined,
      },
    })
    return { messages: res.data.data ?? [], pageable: res.data.pageable }
  },

  /** One message's metadata + sanitized body + attachment list. Marks the
   * message \Seen like an IMAP BODY[] fetch. */
  message(uid: number, folderId: number) {
    return api.get<ApiResponse<MailMessage>>(`/mail/messages/${uid}`, { params: { folder: folderId } })
  },

  /** Browser-direct download link for one attachment (Content-Disposition:
   * attachment; the HttpOnly session cookie authenticates the navigation). */
  attachmentUrl(uid: number, index: number, folderId: number): string {
    const ws = workspaceParam()
    return `/api/v1/mail/messages/${uid}/attachments/${index}?folder=${folderId}${ws ? `&${ws}` : ''}`
  },

  /** Sets/clears seen/flagged/answered/draft; with if_mod_seq a lost CAS race
   * answers 409. The folder travels as the handler's query parameter
   * (handlers.MailMessageFlagsRequest.Folder `query:"folder"`). */
  updateFlags(uid: number, folderId: number, input: MailFlagUpdateInput) {
    return api.post<ApiResponse<MailFlagUpdateResult>>(`/mail/messages/${uid}/flags`, input, {
      params: { folder: folderId },
    })
  },

  /** Relocates the message to another folder of the same mailbox (target in
   * the body, source folder as the query parameter — the handler's contract). */
  moveMessage(uid: number, folderId: number, targetFolderId: number) {
    return api.post<ApiResponse<MailMoveResult>>(`/mail/messages/${uid}/move`, { target: targetFolderId }, {
      params: { folder: folderId },
    })
  },

  /** Duplicates the message into another folder (blobs are shared). */
  copyMessage(uid: number, folderId: number, targetFolderId: number) {
    return api.post<ApiResponse<MailMoveResult>>(`/mail/messages/${uid}/copy`, { target: targetFolderId }, {
      params: { folder: folderId },
    })
  },

  /** Marks the message \Deleted; rows leave permanently at folder expunge. */
  deleteMessage(uid: number, folderId: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/mail/messages/${uid}`, {
      params: { folder: folderId },
    })
  },

  /** ADR-003 full-text search across one mailbox, optionally narrowed to a
   * folder; ranked, paged results (1-based page, same pageable shape). */
  async search(input: MailSearchInput): Promise<MailMessageListResult> {
    const res = await api.post<PaginatedResponse<MailMessageSummary>>('/mail/search', {
      mailbox: input.mailbox,
      folder: input.folder,
      q: input.q,
      page: input.page ?? 1,
      size: input.size ?? 50,
    })
    return { messages: res.data.data ?? [], pageable: res.data.pageable }
  },
}

// ─── Mailbox settings (ADR-005 decision 3, self-service) ────────────────────
// The settings view reuses the existing profile endpoint for the display name
// (authApi.updateProfile, PUT /users/me — internal/handlers/user_handler.go
// UpdateProfile) and the existing Profile card for app-password management.
// The signature has no server-side storage in this wave's endpoint surface and
// is kept per browser (see MailSettings.vue for the documented limitation).

// ─── Client-side display helpers ────────────────────────────────────────────

/** True when the message's flag list (handlers.flagNames order: seen,
 * answered, flagged, draft, deleted, recent) carries the named system flag. */
export function hasFlag(message: { flags_list: string[] | undefined }, flag: string): boolean {
  return (message.flags_list ?? []).includes(flag)
}

/** "3.4 MiB / 5 GiB" — the quota display convention the admin Mailboxes view
 * uses (bytesToGiB there); here kept binary-precise with MiB for smaller
 * values. 0 reads as "no limit". */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '0 B'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  const digits = value >= 100 || unit === 0 ? 0 : value >= 10 ? 1 : 2
  return `${value.toFixed(digits)} ${units[unit]}`
}
