import api from './client'
import type { ApiResponse, AuthResponse, UserProfile, Setup2FAResponse, Plan, AppPassword, AppPasswordCreated } from './types'

export const authApi = {
  login(email: string, password: string, twoFactorCode?: string) {
    const body: Record<string, string> = { email, password }
    if (twoFactorCode) body.two_factor_code = twoFactorCode
    return api.post<ApiResponse<AuthResponse>>('/auth/login', body)
  },
  // CA-03: redeem a single-use refresh token for a fresh token pair.
  refresh(refreshToken: string) {
    return api.post<ApiResponse<AuthResponse>>('/auth/refresh', { refresh_token: refreshToken })
  },
  me() {
    return api.get<ApiResponse<UserProfile>>('/users/me')
  },
  // Revokes the current session server-side and expires the session cookie.
  // A refresh token in the body dies with it (CA-03).
  logout(refreshToken?: string) {
    return api.post<ApiResponse<{ message: string }>>('/users/me/sessions/logout',
      refreshToken ? { refresh_token: refreshToken } : undefined)
  },
  updateProfile(data: { name: string; require_verified_domain?: boolean }) {
    return api.put<ApiResponse<UserProfile>>('/users/me', data)
  },
  // The workspace a request that sends no X-Posta-Workspace-Id header operates
  // on. The dashboard always sends the header, so this only affects API keys,
  // SDKs, and curl.
  setDefaultWorkspace(workspaceId: number) {
    return api.put<ApiResponse<{ message: string }>>('/users/me/default-workspace', {
      workspace_id: workspaceId,
    })
  },
  changePassword(currentPassword: string, newPassword: string) {
    return api.put<ApiResponse<{ message: string }>>('/users/me/password', {
      current_password: currentPassword,
      new_password: newPassword,
    })
  },
  setup2FA() {
    return api.post<ApiResponse<Setup2FAResponse>>('/users/me/2fa/setup')
  },
  // Enabling 2FA also issues the one-time recovery codes (R-10): they ride the
  // verify response and are shown exactly once.
  verify2FA(code: string) {
    return api.post<ApiResponse<{ message: string; recovery_codes?: string[] }>>('/users/me/2fa/verify', { code })
  },
  disable2FA(code: string) {
    return api.post<ApiResponse<{ message: string }>>('/users/me/2fa/disable', { code })
  },
  // Recovery codes (R-10): the remaining count backs the profile card; the
  // regeneration issues a fresh single-use set (requires the current TOTP).
  recoveryCodeCount() {
    return api.get<ApiResponse<{ remaining: number }>>('/users/me/2fa/recovery-codes/count')
  },
  regenerateRecoveryCodes(code: string) {
    return api.post<ApiResponse<{ codes: string[] }>>('/users/me/2fa/recovery-codes', { code })
  },
  requestAccountDeletion() {
    return api.post<ApiResponse<{ message: string; scheduled_deletion_at: string }>>('/users/me/delete')
  },
  cancelAccountDeletion() {
    return api.post<ApiResponse<{ message: string }>>('/users/me/cancel-deletion')
  },
  register(name: string, email: string, password: string) {
    return api.post<ApiResponse<AuthResponse>>('/auth/register', { name, email, password })
  },
  registrationStatus() {
    return api.get<ApiResponse<{ registration_enabled: boolean; password_reset_enabled: boolean }>>('/auth/registration-status')
  },
  forgotPassword(email: string) {
    return api.post<ApiResponse<{ message: string }>>('/auth/forgot-password', { email })
  },
  resetPassword(token: string, newPassword: string) {
    return api.post<ApiResponse<{ message: string }>>('/auth/reset-password', { token, new_password: newPassword })
  },
  verifyEmail(token: string) {
    return api.get<ApiResponse<{ message: string }>>(`/auth/verify-email?token=${encodeURIComponent(token)}`)
  },
  resendVerificationEmail() {
    return api.post<ApiResponse<{ message: string }>>('/users/me/verify-email/resend')
  },
  getMyPlan() {
    return api.get<ApiResponse<Plan | null>>('/users/me/plan')
  },
  // SF-35/SF-67: app passwords for mail clients. Generation returns the
  // plaintext exactly once; the backend stores only a bcrypt digest.
  listAppPasswords() {
    return api.get<ApiResponse<AppPassword[]>>('/users/me/app-passwords')
  },
  createAppPassword(name: string) {
    return api.post<ApiResponse<AppPasswordCreated>>('/users/me/app-passwords', { name })
  },
  revokeAppPassword(id: number) {
    return api.delete<ApiResponse<{ message: string }>>(`/users/me/app-passwords/${id}`)
  },
}
