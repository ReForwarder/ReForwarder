import api from "./client";
import type { ApiResponse } from "./types";

export type NotificationSeverity = "info" | "warning" | "critical";
export type NotificationKind = "alert" | "announcement";
export type NotificationCategory =
  | "domains"
  | "deliverability"
  | "security"
  | "messages"
  | "platform";

export interface InboxNotification {
  id: number;
  user_id: number;
  workspace_id?: number | null;
  kind: NotificationKind;
  category: NotificationCategory;
  severity: NotificationSeverity;
  title: string;
  body: string;
  link?: string;
  action_text?: string;
  dedup_key: string;
  read_at?: string | null;
  dismissed_at?: string | null;
  resolved_at?: string | null;
  created_at: string;
}

export interface NotificationCounts {
  unread: number;
  open: number;
}

export interface InboxListParams {
  unread?: boolean;
  open?: boolean;
  category?: NotificationCategory;
  before?: number;
  limit?: number;
  scoped?: boolean;
}

const base = "/users/me/notifications";

export const inboxApi = {
  list: (params: InboxListParams = {}) =>
    api.get<ApiResponse<InboxNotification[]>>(base, {
      params: {
        ...(params.unread ? { unread: true } : {}),
        ...(params.open ? { open: true } : {}),
        ...(params.category ? { category: params.category } : {}),
        ...(params.before ? { before: params.before } : {}),
        ...(params.limit ? { limit: params.limit } : {}),
        ...(params.scoped ? { scoped: true } : {}),
      },
    }),
  counts: () => api.get<ApiResponse<NotificationCounts>>(`${base}/counts`),
  banner: () => api.get<ApiResponse<InboxNotification[]>>(`${base}/banner`),
  markRead: (ids: number[]) => api.post<ApiResponse<{ message: string }>>(`${base}/read`, { ids }),
  markAllRead: () => api.post<ApiResponse<{ message: string }>>(`${base}/read-all`),
  dismiss: (ids: number[]) => api.post<ApiResponse<{ message: string }>>(`${base}/dismiss`, { ids }),
  dismissAll: () => api.post<ApiResponse<{ message: string }>>(`${base}/dismiss-all`),
};

export interface Announcement {
  id: number;
  title: string;
  message: string;
  link?: string;
  severity: NotificationSeverity;
  created_by: number;
  author_name: string;
  recipients: number;
  sent_at?: string | null;
  created_at: string;
}

export interface AnnouncementInput {
  title: string;
  message: string;
  link?: string;
  severity: NotificationSeverity;
}
