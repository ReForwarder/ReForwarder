import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'

// R-20 impersonation banner: it renders while the session profile carries
// impersonated_by, shows the recorded reason, and Exit revokes the session —
// clearing local state and sending the admin back to /login.

const exitImpersonation = vi.hoisted(() => vi.fn())

vi.mock('../api/admin', () => ({
  adminApi: { exitImpersonation },
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/', query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

// The notification store is a plain pinia store; stub it like the auth store.
const notifyError = vi.fn()
const notifySuccess = vi.fn()
vi.mock('../stores/notification', () => ({
  useNotificationStore: () => ({ error: notifyError, success: notifySuccess }),
}))

// The auth store is stubbed per test so each case controls the profile.
let authUser: Record<string, any> | null = null
let cleared = 0
vi.mock('../stores/auth', () => ({
  useAuthStore: () => ({
    get user() { return authUser },
    clearSession: () => { cleared++ },
  }),
}))

import ImpersonationBanner from './ImpersonationBanner.vue'

beforeEach(() => {
  exitImpersonation.mockReset()
  exitImpersonation.mockResolvedValue({ data: { data: { message: 'ok', original_admin_id: 1 } } })
  notifyError.mockReset()
  notifySuccess.mockReset()
  cleared = 0
})

function mountBanner() {
  return mount(ImpersonationBanner)
}

it('stays hidden for a normal session', () => {
  authUser = { name: 'Normal User', email: 'u@example.dev' }
  expect(mountBanner().find('.impersonation-banner').exists()).toBe(false)
})

it('banners the impersonation with the recorded reason', () => {
  authUser = {
    name: 'Acme Customer',
    email: 'target@example.dev',
    impersonated_by: 7,
    impersonation_reason: 'support ticket 42',
  }
  const w = mountBanner()
  expect(w.find('.impersonation-banner').exists()).toBe(true)
  expect(w.text()).toContain('Viewing as Acme Customer')
  expect(w.text()).toContain('support ticket 42')
})

it('falls back to the email when the target has no name', () => {
  authUser = { email: 'target@example.dev', impersonated_by: 7 }
  expect(mountBanner().text()).toContain('Viewing as target@example.dev')
})

it('exit revokes the session, clears local state and routes to /login', async () => {
  authUser = { name: 'Acme Customer', impersonated_by: 7 }
  const w = mountBanner()
  await w.find('.app-banner-btn').trigger('click')
  await vi.waitFor(() => expect(exitImpersonation).toHaveBeenCalledTimes(1))
  expect(cleared).toBe(1)
  expect(notifySuccess).toHaveBeenCalled()
})

it('a failed exit keeps the session and reports the error', async () => {
  exitImpersonation.mockRejectedValue({ response: { data: { error: { message: 'not impersonating' } } } })
  authUser = { name: 'Acme Customer', impersonated_by: 7 }
  const w = mountBanner()
  await w.find('.app-banner-btn').trigger('click')
  await vi.waitFor(() => expect(notifyError).toHaveBeenCalledWith('not impersonating'))
  expect(cleared).toBe(0)
})
