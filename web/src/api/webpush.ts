import api from './client'
import type { ApiResponse, PushSubscriptionRow } from './types'

// SF-89: browser Web Push (RFC 8030/8291/8292) for the dashboard's realtime
// events. The backend exposes the capability probe and the user-scoped
// subscription CRUD; the browser ceremony (permission, PushManager.subscribe,
// the service worker) lives in the Profile card.

export interface PushConfig {
  enabled: boolean
  public_key?: string
}

export interface SubscribePayload {
  endpoint: string
  keys: { p256dh: string; auth: string }
  user_agent?: string
}

export const webPushApi = {
  getConfig() {
    return api.get<ApiResponse<PushConfig>>('/users/me/push/config')
  },
  subscribe(payload: SubscribePayload) {
    return api.post<ApiResponse<{ message: string; count: number }>>('/users/me/push/subscriptions', payload)
  },
  list() {
    return api.get<ApiResponse<PushSubscriptionRow[]>>('/users/me/push/subscriptions')
  },
  unsubscribe(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/users/me/push/subscriptions/${id}`)
  },
}

// applicationServerKeyBytes converts the backend's base64url VAPID public key
// into the Uint8Array PushManager.subscribe expects.
export function applicationServerKeyBytes(publicKeyB64: string): Uint8Array {
  const padless = publicKeyB64.replace(/=+$/, '')
  const binary = atob(padless.replace(/-/g, '+').replace(/_/g, '/'))
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes
}

// serializeSubscription reduces the browser's PushSubscription to the payload
// the backend stores (the RFC 8291 key material, base64url as issued).
export function serializeSubscription(sub: PushSubscription): SubscribePayload {
  const json = sub.toJSON() as {
    endpoint?: string
    keys?: { p256dh?: string; auth?: string }
  }
  return {
    endpoint: json.endpoint ?? sub.endpoint,
    keys: {
      p256dh: json.keys?.p256dh ?? '',
      auth: json.keys?.auth ?? '',
    },
    user_agent: navigator.userAgent,
  }
}
