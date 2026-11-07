import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { authApi } from '../api/auth'
import { webauthnApi, type WebAuthnAssertionPayload } from '../api/webauthn'
import type { AuthResponse, User, UserProfile } from '../api/types'

// The session lives in an HttpOnly cookie that the browser attaches to every
// same-origin request, so there is no token here to hold. `posta_user` caches the
// profile only so a reload can render before /users/me resolves — it is not a
// credential. If the cookie is missing or expired the first API call 401s and the
// response interceptor in api/client.ts sends us to /login.
const USER_KEY = 'posta_user'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | UserProfile | null>(JSON.parse(localStorage.getItem(USER_KEY) || 'null'))
  // CA-03: the single-use refresh token rides the login response. It lives in
  // memory only — never localStorage — and dies with the logout call.
  const refresh = ref('')

  const isAuthenticated = computed(() => !!user.value)
  const isAdmin = computed(() => user.value?.role === 'admin')
  // GAP-12: true while the account owes its forced 2FA enrollment.
  const enrollmentRequired = computed(() => !!(user.value as UserProfile | null)?.two_factor_enrollment_required)

  async function login(email: string, password: string, twoFactorCode?: string) {
    const res = await authApi.login(email, password, twoFactorCode)
    // Check if 2FA is required
    if (!res.data.success && (res.data.data as any)?.requires_2fa) {
      throw { requires2FA: true }
    }
    setUser(res.data.data.user)
    refresh.value = res.data.data.refresh_token || ''
    // Refresh to pick up extended profile fields (email_verified_at, etc).
    await fetchUser()
    // GAP-12: the session is live, but the two_factor_required policy demands
    // enrollment first — the Login page redirects to the 2FA setup card.
    if (res.data.data.two_factor_enrollment_required) {
      throw { requires2FASetup: true }
    }
    return res.data.data
  }

  // passkeyLogin completes the R-19 passkey sign-in: the finish call issues
  // the same session as a password login. A 401 with requires_2fa means the
  // passkey verified but the account owes the second factor — the Login page
  // resubmits with the pending token and the TOTP code.
  async function passkeyLogin(payload: WebAuthnAssertionPayload) {
    const res = await webauthnApi.finishLogin(payload)
    setUser(res.data.data.user)
    refresh.value = (res.data.data as AuthResponse).refresh_token || ''
    // Refresh to pick up extended profile fields (impersonation flag included).
    await fetchUser()
    if ((res.data.data as AuthResponse).two_factor_enrollment_required) {
      throw { requires2FASetup: true }
    }
    return res.data.data
  }

  function setUser(u: User | UserProfile) {
    user.value = u
    localStorage.setItem(USER_KEY, JSON.stringify(u))
  }

  // clearSession drops client state only. The 401 interceptor uses this: calling
  // logout() there would fire another request that 401s in turn.
  function clearSession() {
    user.value = null
    refresh.value = ''
    localStorage.removeItem(USER_KEY)
    // Clear workspace state
    localStorage.removeItem('posta_workspace_id')
  }

  // logout revokes the session server-side (blacklists its jti and expires the
  // cookie, and revokes the CA-03 refresh token when one is held), then clears
  // client state. Best-effort: an offline or already-expired session still
  // logs out locally.
  async function logout() {
    try {
      await authApi.logout(refresh.value || undefined)
    } catch {
      /* the session is gone either way */
    }
    clearSession()
  }

  async function fetchUser() {
    try {
      const res = await authApi.me()
      setUser(res.data.data)
    } catch {
      clearSession()
    }
  }

  return { user, isAuthenticated, isAdmin, enrollmentRequired, login, passkeyLogin, logout, clearSession, fetchUser, setUser }
})
