import api from './client'
import type { ApiResponse } from './types'

// ─── Webmail compose (OB-5 W3, ADR-005 decision 2 + ADR-006 scan gate) ──────
// Backend: the /mail routes (internal/routes/mail_compose_routes.go) —
// dashboard-JWT scoped. Attachment uploads are malware-scanned per ADR-006
// before they can be referenced; send files the Sent copy into the mailbox's
// Sent folder and enqueues the transport copy through the outbound pipeline
// with provenance mail-compose; drafts live in the Drafts folder; delivery
// status reads the sent message's email_events timeline.

/** The ref returned by POST /mail/attachments (handlers.ComposeAttachmentRefResponse). */
export interface ComposeAttachmentRef {
  blob_hash: string
  name: string
  content_type: string
  size: number
  scan_state: 'clean' | 'unavailable'
}

/** One attachment reference inside a send/draft request. */
export interface ComposeAttachmentInput {
  blob_hash: string
  name?: string
  content_type?: string
}

/** The shared to/cc/bcc + subject + body payload (ADR-005 compose shape). */
export interface ComposeMessageInput {
  mailbox_id?: number
  from: string
  to: string[]
  cc?: string[]
  bcc?: string[]
  subject: string
  text?: string
  html?: string
  attachments?: ComposeAttachmentInput[]
}

/** POST /mail/send result (mailcompose.SendResult). */
export interface ComposeSendResult {
  email_uuid: string
  status: string
  message_id: string
  mailbox_id: number
  folder_id: number
  uid: number
}

/** Draft addressing (mailcompose.DraftResult). */
export interface ComposeDraftResult {
  mailbox_id: number
  folder_id: number
  uid: number
  message_id: string
}

/** Reply/forward prefill frame (mailcompose.ReplyFrame). */
export interface ComposeReplyFrame {
  mode: 'reply' | 'reply_all' | 'forward'
  mailbox_id: number
  folder_id: number
  uid: number
  in_reply_to?: string
  references?: string
  to: string[]
  cc: string[]
  subject: string
  quote_text: string
  original_html?: string
  original: { from: string; date: string; subject: string }
}

/** One email_events timeline row of the delivery status. */
export interface ComposeDeliveryEvent {
  id: number
  email_uuid: string
  type: string
  occurred_at: string
  provider: string
  recipient: string
  error?: string
  metadata: string
}

/** GET /mail/messages/{uid}/delivery-status (mailcompose.DeliveryStatus). */
export interface ComposeDeliveryStatus {
  email_uuid: string
  status: string
  subject: string
  from: string
  recipients: string[]
  sent_at?: string
  delivered_at?: string
  error?: string
  events: ComposeDeliveryEvent[]
}

const WORKSPACE_KEY = 'posta_workspace_id'

/** The active workspace as a query parameter (browser requests can also send
 * the X-Posta-Workspace-Id header; compose is user-scoped, workspace narrows
 * the mailstore scoping). Returns "?a=b&c=d" or "". */
function wsQuery(extra?: Array<[string, string | number]>): string {
  const params = new URLSearchParams()
  const ws = localStorage.getItem(WORKSPACE_KEY)
  if (ws) params.set('workspace_id', ws)
  for (const [k, v] of extra ?? []) params.set(k, String(v))
  const q = params.toString()
  return q ? `?${q}` : ''
}

export const mailComposeApi = {
  /** Stage one file through the ADR-006 scan gate; returns its ref. */
  uploadAttachment(file: File) {
    const form = new FormData()
    form.append('file', file)
    return api.post<ApiResponse<ComposeAttachmentRef[]>>('/mail/attachments', form, {
      headers: { 'Content-Type': 'multipart/form-data' },
    })
  },

  /** Send a composed message (Sent copy + outbound pipeline). */
  send(input: ComposeMessageInput) {
    return api.post<ApiResponse<ComposeSendResult>>('/mail/send', { body: input })
  },

  /** Save a draft into the mailbox's Drafts folder. */
  saveDraft(input: ComposeMessageInput) {
    return api.post<ApiResponse<ComposeDraftResult>>('/mail/drafts', { body: input })
  },

  /** Replace a draft (Drafts-folder UID; the mailbox_id query names it). */
  updateDraft(uid: number, mailboxId: number, input: ComposeMessageInput) {
    const q = wsQuery([['mailbox_id', mailboxId]])
    return api.put<ApiResponse<ComposeDraftResult>>(`/mail/drafts/${uid}${q}`, { body: input })
  },

  /** Send the stored draft; it is deleted only after an accepted send. */
  sendDraft(uid: number, mailboxId: number) {
    const q = wsQuery([['mailbox_id', mailboxId]])
    return api.post<ApiResponse<ComposeSendResult>>(`/mail/drafts/${uid}/send${q}`)
  },

  /** Reply/reply-all/forward prefill for the composer. */
  replyFrame(uid: number, folderId: number, mailboxId: number, mode: 'reply' | 'reply_all' | 'forward' = 'reply') {
    const q = wsQuery([
      ['folder_id', folderId],
      ['mailbox_id', mailboxId],
      ['mode', mode],
    ])
    return api.get<ApiResponse<ComposeReplyFrame>>(`/mail/messages/${uid}/reply-frame${q}`)
  },

  /** The sent message's delivery timeline (email_events; provider/MX truth). */
  deliveryStatus(uid: number, folderId: number) {
    const q = wsQuery([['folder_id', folderId]])
    return api.get<ApiResponse<ComposeDeliveryStatus>>(`/mail/messages/${uid}/delivery-status${q}`)
  },
}
