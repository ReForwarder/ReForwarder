import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// The auth store: the GAP-12 forced-enrollment handling and the CA-03
// refresh-token bookkeeping ride login/logout here.

const authApi = vi.hoisted(() => ({
  login: vi.fn(),
  logout: vi.fn().mockResolvedValue({ data: { success: true } }),
  me: vi.fn(),
}))
vi.mock('../api/auth', () => ({ authApi }))

vi.mock('../api/webauthn', () => ({
  webauthnApi: { finishLogin: vi.fn() },
}))

import { useAuthStore } from './auth'

beforeEach(() => {
  setActivePinia(createPinia())
  localStorage.clear()
  vi.clearAllMocks()
  // Default profile fetch: a plain user WITHOUT the enrollment marker, so the
  // no-policy tests observe the store's real behavior. Tests that need the
  // marker override this (login() calls fetchUser() before it decides).
  authApi.me.mockResolvedValue({
    data: { success: true, data: { id: 0, name: 'Plain', email: 'plain@example.com', role: 'user' } },
  })
})

describe('auth store — GAP-12 + CA-03', () => {
  it('stores the session, keeps the refresh token, and throws the enrollment marker', async () => {
    // The profile fetch the store makes right after login carries the marker
    // too (the backend recomputes it in Me), so enrollmentRequired stays true.
    authApi.me.mockResolvedValue({
      data: { success: true, data: { id: 7, role: 'user', two_factor_enrollment_required: true } },
    })
    authApi.login.mockResolvedValue({
      data: {
        success: true,
        data: {
          token: 'jwt-1',
          refresh_token: 'rt-1',
          two_factor_enrollment_required: true,
          user: { id: 7, name: 'U', email: 'u@example.com', role: 'user' },
        },
      },
    })

    const store = useAuthStore()
    await expect(store.login('u@example.com', 'pw')).rejects.toMatchObject({ requires2FASetup: true })

    // The session IS live (the redirect target is an authenticated page).
    expect(store.isAuthenticated).toBe(true)
    expect(store.enrollmentRequired).toBe(true)
  })

  it('does not throw the marker when the policy does not apply', async () => {
    authApi.login.mockResolvedValue({
      data: {
        success: true,
        data: {
          token: 'jwt-2',
          refresh_token: 'rt-2',
          user: { id: 8, name: 'V', email: 'v@example.com', role: 'user' },
        },
      },
    })

    const store = useAuthStore()
    const res = await store.login('v@example.com', 'pw')
    expect(res.token).toBe('jwt-2')
    expect(store.enrollmentRequired).toBe(false)
  })

  it('still surfaces the requires_2fa pending marker for enrolled accounts', async () => {
    authApi.login.mockResolvedValue({
      data: { success: false, data: { requires_2fa: true, message: '2FA code required' } },
    })

    const store = useAuthStore()
    await expect(store.login('v@example.com', 'pw')).rejects.toMatchObject({ requires2FA: true })
    expect(store.isAuthenticated).toBe(false)
  })

  it('sends the held refresh token with the logout call (CA-03)', async () => {
    authApi.login.mockResolvedValue({
      data: {
        success: true,
        data: {
          token: 'jwt-3',
          refresh_token: 'rt-3',
          user: { id: 9, name: 'W', email: 'w@example.com', role: 'user' },
        },
      },
    })

    const store = useAuthStore()
    await store.login('w@example.com', 'pw')
    await store.logout()

    expect(authApi.logout).toHaveBeenCalledWith('rt-3')
    expect(store.isAuthenticated).toBe(false)
  })

  it('logs out without a refresh token when none was issued', async () => {
    authApi.login.mockResolvedValue({
      data: {
        success: true,
        data: { token: 'jwt-4', user: { id: 10, name: 'X', email: 'x@example.com', role: 'user' } },
      },
    })

    const store = useAuthStore()
    await store.login('x@example.com', 'pw')
    await store.logout()

    expect(authApi.logout).toHaveBeenCalledWith(undefined)
  })
})
