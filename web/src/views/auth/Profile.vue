<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { authApi } from '../../api/auth'
import { oauthApi } from '../../api/oauth'
import { sessionsApi, type Session } from '../../api/sessions'
import { webauthnApi, passkeysSupported, serializeCreation } from '../../api/webauthn'
import { webPushApi, applicationServerKeyBytes, serializeSubscription } from '../../api/webpush'
import { useAuthStore } from '../../stores/auth'
import { useNotificationStore } from '../../stores/notification'
import { useConfirm } from '../../composables/useConfirm'
import type { Plan, OAuthLinkedAccount, OAuthProviderInfo, WebAuthnCredential, AppPassword, PushSubscriptionRow } from '../../api/types'
const auth = useAuthStore()
const notify = useNotificationStore()
const { confirm } = useConfirm()
const route = useRoute()
const router = useRouter()

// Profile
const name = ref('')
const email = ref('')
const profileLoading = ref(false)
const twoFactorEnabled = ref(false)
// GAP-12: set while the two_factor_required policy applies and the account
// still owes its enrollment — the banner points at the setup card.
const enrollmentRequired = ref(false)
const scheduledDeletionAt = ref<string | null>(null)
const deletionLoading = ref(false)

// Plan
const userPlan = ref<Plan | null>(null)
const planLoading = ref(false)

// Linked OAuth Accounts
const linkedAccounts = ref<OAuthLinkedAccount[]>([])
const oauthProviders = ref<OAuthProviderInfo[]>([])
const oauthLoading = ref(false)
const unlinkingAccount = ref<number | null>(null)

const availableProviders = computed(() => {
  const linkedNames = new Set(linkedAccounts.value.map(a => a.provider_name))
  return oauthProviders.value.filter(p => !linkedNames.has(p.name))
})

function formatLimit(val: number): string {
  return val === 0 ? 'Unlimited' : String(val)
}

onMounted(async () => {
  name.value = auth.user?.name || ''
  email.value = auth.user?.email || ''
  // Fetch fresh profile to get 2FA status and deletion status
  try {
    const res = await authApi.me()
    twoFactorEnabled.value = res.data.data.two_factor_enabled
    enrollmentRequired.value = !!res.data.data.two_factor_enrollment_required
    scheduledDeletionAt.value = res.data.data.scheduled_deletion_at
    if (twoFactorEnabled.value) await loadRecoveryCount()
  } catch { /* ignore */ }

  // GAP-12 deep link: the login flow redirects an un-enrolled account here
  // (?setup=2fa). Scroll the 2FA card into view and say why.
  if (route.query.setup === '2fa' && !twoFactorEnabled.value) {
    enrollmentRequired.value = true
    await nextTick()
    document.getElementById('two-factor-card')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  await loadAppPasswords()

  if (passkeysSupportedHere.value) await loadPasskeys()

  // Fetch user plan
  planLoading.value = true
  try {
    const res = await authApi.getMyPlan()
    userPlan.value = res.data.data
  } catch { /* ignore */ }
  finally { planLoading.value = false }

  // Fetch linked OAuth accounts and available providers
  oauthLoading.value = true
  try {
    const [accountsRes, providersRes] = await Promise.all([
      oauthApi.linkedAccounts(),
      oauthApi.providers(),
    ])
    linkedAccounts.value = accountsRes.data.data || []
    oauthProviders.value = providersRes.data.data?.providers || []
  } catch { /* ignore */ }
  finally { oauthLoading.value = false }
})

async function handleProfileUpdate() {
  if (!name.value.trim()) {
    notify.error('Name is required')
    return
  }
  profileLoading.value = true
  try {
    const res = await authApi.updateProfile({ name: name.value.trim() })
    auth.user = res.data.data
    localStorage.setItem('posta_user', JSON.stringify(res.data.data))
    twoFactorEnabled.value = res.data.data.two_factor_enabled
    notify.success('Profile updated successfully')
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to update profile'
    notify.error(message)
  } finally {
    profileLoading.value = false
  }
}

// OAuth account management
async function unlinkOAuthAccount(account: OAuthLinkedAccount) {
  const confirmed = await confirm({
    title: 'Unlink Account',
    message: `Unlink your ${account.provider_name} account (${account.email})?`,
    confirmText: 'Unlink',
    variant: 'danger',
  })
  if (!confirmed) return

  unlinkingAccount.value = account.id
  try {
    await oauthApi.unlink(account.provider_id)
    linkedAccounts.value = linkedAccounts.value.filter(a => a.id !== account.id)
    notify.success(`${account.provider_name} account unlinked`)
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to unlink account'
    notify.error(message)
  } finally {
    unlinkingAccount.value = null
  }
}

function linkOAuthProvider(slug: string) {
  window.location.href = `/api/v1/auth/oauth/${slug}/authorize`
}

// Password
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const passwordLoading = ref(false)

async function handlePasswordChange() {
  if (!currentPassword.value || !newPassword.value || !confirmPassword.value) {
    notify.error('Please fill in all fields')
    return
  }
  if (newPassword.value.length < 8) {
    notify.error('New password must be at least 8 characters')
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    notify.error('New passwords do not match')
    return
  }
  passwordLoading.value = true
  try {
    await authApi.changePassword(currentPassword.value, newPassword.value)
    notify.success('Password changed successfully')
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to change password'
    notify.error(message)
  } finally {
    passwordLoading.value = false
  }
}

// 2FA
const show2FASetup = ref(false)
const tfaSecret = ref('')
const tfaURL = ref('')
const tfaCode = ref('')
const tfaLoading = ref(false)
const tfaDisableCode = ref('')
const show2FADisable = ref(false)
const tfaDisableLoading = ref(false)

async function startSetup2FA() {
  tfaLoading.value = true
  try {
    const res = await authApi.setup2FA()
    tfaSecret.value = res.data.data.secret
    tfaURL.value = res.data.data.url
    show2FASetup.value = true
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to setup 2FA'
    notify.error(message)
  } finally {
    tfaLoading.value = false
  }
}

async function verify2FA() {
  if (!tfaCode.value || tfaCode.value.length !== 6) {
    notify.error('Please enter a valid 6-digit code')
    return
  }
  tfaLoading.value = true
  try {
    const res = await authApi.verify2FA(tfaCode.value)
    twoFactorEnabled.value = true
    show2FASetup.value = false
    tfaCode.value = ''
    tfaSecret.value = ''
    tfaURL.value = ''
    // R-10: the enable response carries the one-time recovery codes — surface
    // them immediately, they are never retrievable again.
    recoveryCodes.value = res.data.data.recovery_codes || []
    recoveryRemaining.value = recoveryCodes.value.length
    notify.success('Two-factor authentication enabled — save your recovery codes')
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Invalid code. Please try again.'
    notify.error(message)
  } finally {
    tfaLoading.value = false
  }
}

// Recovery codes (R-10): the profile card keeps the count fresh, regenerates
// the single-use set behind a TOTP confirmation, and shows the fresh codes
// exactly once with a local copy/download.
const recoveryCodes = ref<string[]>([])
const recoveryRemaining = ref<number | null>(null)
const showRecoveryRegen = ref(false)
const recoveryRegenCode = ref('')
const recoveryRegenLoading = ref(false)

async function loadRecoveryCount() {
  try {
    const res = await authApi.recoveryCodeCount()
    recoveryRemaining.value = res.data.data.remaining
  } catch { /* the count is advisory; a failure leaves the card quiet */ }
}

async function regenerateRecoveryCodes() {
  if (!recoveryRegenCode.value || recoveryRegenCode.value.length !== 6) {
    notify.error('Please enter a valid 6-digit code')
    return
  }
  recoveryRegenLoading.value = true
  try {
    const res = await authApi.regenerateRecoveryCodes(recoveryRegenCode.value)
    recoveryCodes.value = res.data.data.codes || []
    recoveryRemaining.value = recoveryCodes.value.length
    showRecoveryRegen.value = false
    recoveryRegenCode.value = ''
    notify.success('New recovery codes generated — the previous set is no longer valid')
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Invalid code. Please try again.'
    notify.error(message)
  } finally {
    recoveryRegenLoading.value = false
  }
}

function copyRecoveryCodes() {
  const text = recoveryCodes.value.join('\n')
  navigator.clipboard?.writeText(text).then(
    () => notify.success('Recovery codes copied to clipboard'),
    () => notify.error('Copy failed — select the codes manually'),
  )
}

function downloadRecoveryCodes() {
  const header = `Posta recovery codes for ${email.value} — each code works once.\n`
  const blob = new Blob([header + recoveryCodes.value.join('\n') + '\n'], { type: 'text/plain' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'posta-recovery-codes.txt'
  a.click()
  URL.revokeObjectURL(url)
}

// SF-35/SF-67: app passwords for mail clients (IMAP/POP3/submission). The
// plaintext rides the create response exactly once — same contract as the
// recovery codes; the backend keeps only a bcrypt digest.
const appPasswords = ref<AppPassword[]>([])
const appPasswordsLoading = ref(false)
const appPasswordName = ref('')
const appPasswordCreating = ref(false)
const appPasswordSecret = ref('') // one-time display
const appPasswordSecretName = ref('')
const appPasswordCopied = ref(false)

async function loadAppPasswords() {
  appPasswordsLoading.value = true
  try {
    const res = await authApi.listAppPasswords()
    appPasswords.value = res.data.data || []
  } catch { /* the card stays empty on failure */ }
  finally { appPasswordsLoading.value = false }
}

async function createAppPassword() {
  const label = appPasswordName.value.trim()
  if (!label) {
    notify.error('Give the app password a name first')
    return
  }
  appPasswordCreating.value = true
  try {
    const res = await authApi.createAppPassword(label)
    appPasswordSecret.value = res.data.data.password
    appPasswordSecretName.value = res.data.data.app_password.name
    appPasswordCopied.value = false
    appPasswordName.value = ''
    await loadAppPasswords()
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to create app password'
    notify.error(message)
  } finally {
    appPasswordCreating.value = false
  }
}

function copyAppPassword() {
  navigator.clipboard?.writeText(appPasswordSecret.value).then(
    () => {
      appPasswordCopied.value = true
      notify.success('App password copied to clipboard')
    },
    () => notify.error('Copy failed — select the password manually'),
  )
}

function dismissAppPasswordSecret() {
  // Closing the one-time view is final: the value cannot be retrieved again.
  appPasswordSecret.value = ''
  appPasswordSecretName.value = ''
  appPasswordCopied.value = false
}

async function revokeAppPassword(p: AppPassword) {
  const confirmed = await confirm({
    title: 'Revoke App Password',
    message: `Revoke "${p.name}"? Any mail client using it will stop working immediately.`,
    confirmText: 'Revoke',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await authApi.revokeAppPassword(p.id)
    await loadAppPasswords()
    notify.success('App password revoked')
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to revoke app password'
    notify.error(message)
  }
}

function formatAppPasswordDate(val: string | null): string {
  if (!val) return '—'
  return new Date(val).toLocaleString()
}

async function disable2FA() {
  if (!tfaDisableCode.value || tfaDisableCode.value.length !== 6) {
    notify.error('Please enter a valid 6-digit code')
    return
  }
  tfaDisableLoading.value = true
  try {
    await authApi.disable2FA(tfaDisableCode.value)
    twoFactorEnabled.value = false
    show2FADisable.value = false
    tfaDisableCode.value = ''
    // The backend deletes every recovery code with 2FA.
    recoveryCodes.value = []
    recoveryRemaining.value = null
    notify.success('Two-factor authentication disabled')
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Invalid code. Please try again.'
    notify.error(message)
  } finally {
    tfaDisableLoading.value = false
  }
}

// Passkeys (R-19): list, register, rename and delete this account's WebAuthn
// credentials. The browser runs the ceremonies; the private key never leaves
// the authenticator.
const passkeysSupportedHere = computed(() => passkeysSupported())
const passkeyCredentials = ref<WebAuthnCredential[]>([])
const passkeyLoading = ref(false)
const showPasskeyAdd = ref(false)
const newPasskeyName = ref('')
const renamingPasskeyId = ref<string | null>(null)
const renamePasskeyValue = ref('')

async function loadPasskeys() {
  passkeyLoading.value = true
  try {
    const res = await webauthnApi.listCredentials()
    passkeyCredentials.value = res.data.data || []
  } catch { /* the card shows the empty state */ }
  finally { passkeyLoading.value = false }
}

async function registerPasskey() {
  passkeyLoading.value = true
  try {
    const begin = await webauthnApi.beginRegistration()
    const options = begin.data.data as unknown as PublicKeyCredentialCreationOptions
    const credential = await navigator.credentials.create({ publicKey: options })
    if (!credential) throw new Error('The passkey ceremony returned no credential')
    const saved = await webauthnApi.finishRegistration(serializeCreation(credential as PublicKeyCredential))
    const name = newPasskeyName.value.trim()
    if (name && saved.data.data.name !== name) {
      try { await webauthnApi.renameCredential(saved.data.data.id, name) } catch { /* cosmetic */ }
    }
    showPasskeyAdd.value = false
    newPasskeyName.value = ''
    notify.success('Passkey registered — you can now sign in with it')
    await loadPasskeys()
  } catch (e: any) {
    if (e?.name === 'NotAllowedError') {
      notify.error('Passkey registration was cancelled or timed out')
    } else {
      const message = e?.response?.data?.error?.message || e?.message || 'Failed to register the passkey'
      notify.error(message)
    }
  } finally {
    passkeyLoading.value = false
  }
}

async function renamePasskey(cred: WebAuthnCredential) {
  const name = renamePasskeyValue.value.trim()
  if (!name || name === cred.name) {
    renamingPasskeyId.value = null
    return
  }
  try {
    await webauthnApi.renameCredential(cred.id, name)
    cred.name = name
    notify.success('Passkey renamed')
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to rename the passkey')
  } finally {
    renamingPasskeyId.value = null
    renamePasskeyValue.value = ''
  }
}

async function deletePasskey(cred: WebAuthnCredential) {
  const confirmed = await confirm({
    title: 'Remove passkey',
    message: `Remove the passkey "${cred.name}"? You will no longer be able to sign in with it.`,
    confirmText: 'Remove',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await webauthnApi.deleteCredential(cred.id)
    passkeyCredentials.value = passkeyCredentials.value.filter(c => c.id !== cred.id)
    notify.success('Passkey removed')
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to remove the passkey')
  }
}

function formatPasskeyDate(dateStr: string | null): string {
  if (!dateStr) return 'never'
  return new Date(dateStr).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

function cancel2FASetup() {
  show2FASetup.value = false
  tfaCode.value = ''
  tfaSecret.value = ''
  tfaURL.value = ''
}

// Sessions
const sessions = ref<Session[]>([])
const sessionsLoading = ref(false)
const revokingSession = ref<number | null>(null)
const revokingOthers = ref(false)

async function loadSessions() {
  sessionsLoading.value = true
  try {
    const res = await sessionsApi.list()
    sessions.value = res.data.data || []
  } catch {
    // silently fail — sessions card still shows
  } finally {
    sessionsLoading.value = false
  }
}

async function revokeSession(s: Session) {
  const confirmed = await confirm({
    title: 'Revoke Session',
    message: `Force logout the session from ${s.ip_address}?`,
    confirmText: 'Revoke',
    variant: 'danger',
  })
  if (!confirmed) return

  revokingSession.value = s.id
  try {
    await sessionsApi.revoke(s.id)
    sessions.value = sessions.value.filter(x => x.id !== s.id)
    notify.success('Session revoked')
  } catch (e: any) {
    notify.error(e.response?.data?.error?.message || 'Failed to revoke session')
  } finally {
    revokingSession.value = null
  }
}

async function revokeOtherSessions() {
  const confirmed = await confirm({
    title: 'Revoke All Other Sessions',
    message: 'This will force logout all other devices and browsers. Continue?',
    confirmText: 'Revoke All Others',
    variant: 'danger',
  })
  if (!confirmed) return

  revokingOthers.value = true
  try {
    const res = await sessionsApi.revokeOthers()
    notify.success(res.data.data.message)
    await loadSessions()
  } catch (e: any) {
    notify.error(e.response?.data?.error?.message || 'Failed to revoke sessions')
  } finally {
    revokingOthers.value = false
  }
}

function parseUserAgent(ua: string): string {
  if (!ua) return 'Unknown'
  // Extract browser name
  if (ua.includes('Firefox')) return 'Firefox'
  if (ua.includes('Edg/')) return 'Edge'
  if (ua.includes('Chrome')) return 'Chrome'
  if (ua.includes('Safari')) return 'Safari'
  if (ua.includes('curl')) return 'curl'
  return ua.slice(0, 30) + (ua.length > 30 ? '...' : '')
}

// sessionLabel prefers the server-parsed label, falling back to the local
// User-Agent parser for sessions created before the server enriched them.
function sessionLabel(s: Session): string {
  if (s.label && s.label !== 'Unknown device') return s.label
  return parseUserAgent(s.user_agent)
}

// sessionDevice returns a short form-factor hint (e.g. "Mobile") when known.
function sessionDevice(s: Session): string {
  return s.device && s.device !== 'Unknown' ? s.device : ''
}

function formatSessionDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

// Account Deletion
async function requestAccountDeletion() {
  const confirmed = await confirm({
    title: 'Delete Account',
    message: 'Are you sure you want to delete your account? Your account will be deactivated immediately and permanently deleted after 7 days. You can cancel this during the 7-day period.',
    confirmText: 'Delete My Account',
    variant: 'danger',
  })
  if (!confirmed) return

  deletionLoading.value = true
  try {
    const res = await authApi.requestAccountDeletion()
    scheduledDeletionAt.value = res.data.data.scheduled_deletion_at
    notify.success('Account scheduled for deletion in 7 days')
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to schedule account deletion'
    notify.error(message)
  } finally {
    deletionLoading.value = false
  }
}

async function cancelAccountDeletion() {
  const confirmed = await confirm({
    title: 'Cancel Account Deletion',
    message: 'Are you sure you want to cancel the scheduled deletion and reactivate your account?',
    confirmText: 'Keep My Account',
    variant: 'danger',
  })
  if (!confirmed) return

  deletionLoading.value = true
  try {
    await authApi.cancelAccountDeletion()
    scheduledDeletionAt.value = null
    notify.success('Account deletion cancelled')
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to cancel account deletion'
    notify.error(message)
  } finally {
    deletionLoading.value = false
  }
}

function formatDeletionDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, {
    year: 'numeric', month: 'long', day: 'numeric',
  })
}

// ---- Browser push notifications (SF-89) ----
// The deployment advertises Web Push (VAPID public key) via /users/me/push;
// subscribing runs the browser ceremony — service worker registration,
// Notification.requestPermission, PushManager.subscribe — then hands the
// PushSubscription to the backend for user-scoped storage.
const pushSupported = typeof window !== 'undefined' && 'PushManager' in window && 'serviceWorker' in window.navigator
const pushState = ref<'loading' | 'off' | 'unsupported' | 'denied' | 'ready'>('loading')
const pushSubs = ref<PushSubscriptionRow[]>([])
const pushBusy = ref(false)

async function loadPush() {
  if (!pushSupported) {
    pushState.value = 'unsupported'
    return
  }
  try {
    const cfg = await webPushApi.getConfig()
    if (!cfg.data.data.enabled) {
      pushState.value = 'off'
      return
    }
    if (typeof Notification !== 'undefined' && Notification.permission === 'denied') {
      pushState.value = 'denied'
      return
    }
    const listed = await webPushApi.list()
    pushSubs.value = listed.data.data || []
    pushState.value = 'ready'
  } catch {
    pushState.value = 'off' // probe failure reads as "not offered here"
  }
}

async function enablePush() {
  pushBusy.value = true
  try {
    const cfg = await webPushApi.getConfig()
    const key = cfg.data.data.public_key
    if (!key) throw new Error('push is not configured')
    const permission = await Notification.requestPermission()
    if (permission !== 'granted') {
      pushState.value = 'denied'
      return
    }
    const reg = await navigator.serviceWorker.register('/push-sw.js')
    await navigator.serviceWorker.ready
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: applicationServerKeyBytes(key) as BufferSource,
    })
    await webPushApi.subscribe(serializeSubscription(sub))
    notify.success('Browser notifications enabled')
    await loadPush()
  } catch (e: any) {
    notify.error(e?.message || 'Could not enable browser notifications')
  } finally {
    pushBusy.value = false
  }
}

// pushSubscriptionLabel names a stored subscription after the push service
// host and, when recognizable, the browser that registered it.
function pushSubscriptionLabel(s: PushSubscriptionRow): string {
  let host = 'push service'
  try {
    host = new URL(s.endpoint).host
  } catch { /* keep the fallback */ }
  const ua = s.user_agent || ''
  const browser =
    ua.includes('Firefox') ? 'Firefox' :
    ua.includes('Edg/') ? 'Edge' :
    ua.includes('Chrome') ? 'Chrome' :
    ua.includes('Safari') ? 'Safari' : ''
  return browser ? `${browser} via ${host}` : host
}

async function removePush(id: number) {
  pushBusy.value = true
  try {
    await webPushApi.unsubscribe(id)
    // Best effort: drop this browser's local subscription so the next enable
    // starts clean. A different browser's row is removed server-side only.
    try {
      const reg = await navigator.serviceWorker.getRegistration()
      const local = await reg?.pushManager.getSubscription()
      if (local && pushSubs.value.find((s) => s.id === id)?.endpoint === local.endpoint) {
        await local.unsubscribe()
      }
    } catch { /* the server row is gone either way */ }
    await loadPush()
  } catch {
    notify.error('Could not remove the notification subscription')
  } finally {
    pushBusy.value = false
  }
}

// Load sessions on mount
onMounted(() => {
  loadSessions()
  loadPush()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Profile</h1>
    </div>

    <div class="profile-grid">
      <!-- My Profile -->
      <div class="card">
        <div class="card-header"><h2>My Profile</h2></div>
        <div class="card-body">
          <form @submit.prevent="handleProfileUpdate" class="profile-form">
            <div class="form-group">
              <label class="form-label" for="profile-name">Name</label>
              <input id="profile-name" v-model="name" type="text" class="form-input" placeholder="Your name" required />
            </div>
            <div class="form-group">
              <label class="form-label" for="profile-email">Email</label>
              <input id="profile-email" :value="email" type="email" class="form-input" disabled />
              <small class="form-hint">Email cannot be changed</small>
            </div>
            <button type="submit" class="btn btn-primary" :disabled="profileLoading">
              {{ profileLoading ? 'Saving...' : 'Save Changes' }}
            </button>
          </form>
        </div>
      </div>

      <!-- My Plan -->
      <div class="card">
        <div class="card-header"><h2>My Plan</h2></div>
        <div class="card-body">
          <div v-if="planLoading" style="text-align: center; padding: 20px 0"><div class="spinner"></div></div>
          <template v-else-if="userPlan">
            <div style="display: flex; align-items: center; gap: 8px; margin-bottom: 16px">
              <h3 style="margin: 0; font-size: 16px">{{ userPlan.name }}</h3>
              <span class="badge badge-success" v-if="userPlan.is_active">Active</span>
              <span class="badge badge-info" v-if="userPlan.is_default">Default</span>
            </div>
            <p v-if="userPlan.description" style="margin: 0 0 16px; font-size: 13px; color: var(--text-secondary)">{{ userPlan.description }}</p>
            <div class="plan-limits-grid">
              <div class="plan-limit-group">
                <h4 class="plan-limit-title">Rate Limits</h4>
                <div class="plan-limit-row"><span>Hourly</span><span>{{ formatLimit(userPlan.hourly_rate_limit) }}</span></div>
                <div class="plan-limit-row"><span>Daily</span><span>{{ formatLimit(userPlan.daily_rate_limit) }}</span></div>
              </div>
              <div class="plan-limit-group">
                <h4 class="plan-limit-title">Resource Limits</h4>
                <div class="plan-limit-row"><span>Workspaces</span><span>{{ formatLimit(userPlan.max_workspaces) }}</span></div>
                <div class="plan-limit-row"><span>API Keys</span><span>{{ formatLimit(userPlan.max_api_keys) }}</span></div>
                <div class="plan-limit-row"><span>Domains</span><span>{{ formatLimit(userPlan.max_domains) }}</span></div>
                <div class="plan-limit-row"><span>SMTP Servers</span><span>{{ formatLimit(userPlan.max_smtp_servers) }}</span></div>
              </div>
              <div class="plan-limit-group">
                <h4 class="plan-limit-title">Email Constraints</h4>
                <div class="plan-limit-row"><span>Attachment</span><span>{{ userPlan.max_attachment_size_mb === 0 ? 'Unlimited' : userPlan.max_attachment_size_mb + ' MB' }}</span></div>
                <div class="plan-limit-row"><span>Batch Size</span><span>{{ formatLimit(userPlan.max_batch_size) }}</span></div>
                <div class="plan-limit-row"><span>Log Retention</span><span>{{ userPlan.email_log_retention_days === 0 ? 'Default' : userPlan.email_log_retention_days + ' days' }}</span></div>
              </div>
            </div>
          </template>
          <div v-else class="text-muted" style="font-size: 13px">
            No plan assigned. Your account is using the platform's global default settings.
          </div>
        </div>
      </div>

      <!-- Two-Factor Authentication -->
      <div id="two-factor-card" class="card">
        <div class="card-header">
          <h2>Two-Factor Authentication</h2>
          <span v-if="twoFactorEnabled" class="badge badge-success">Enabled</span>
          <span v-else class="badge badge-secondary">Disabled</span>
        </div>
        <div class="card-body">
          <!-- GAP-12: the two_factor_required policy demands enrollment. -->
          <div v-if="enrollmentRequired && !twoFactorEnabled" class="enrollment-banner" role="alert">
            <strong>Two-factor authentication is required.</strong>
            Your administrator requires every account to have a second factor.
            Enable it below to finish setting up your account.
          </div>
          <!-- 2FA Not Enabled -->
          <template v-if="!twoFactorEnabled && !show2FASetup">
            <p class="tfa-description">Add an extra layer of security to your account by requiring a code from your authenticator app.</p>
            <button class="btn btn-primary" :disabled="tfaLoading" @click="startSetup2FA">
              {{ tfaLoading ? 'Setting up...' : 'Enable 2FA' }}
            </button>
          </template>

          <!-- 2FA Setup Flow -->
          <template v-if="show2FASetup">
            <div class="tfa-setup">
              <p class="tfa-description">Scan this QR code with your authenticator app (Google Authenticator, Authy, etc.):</p>
              <div class="tfa-qr">
                <img :src="`https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(tfaURL)}`" alt="QR Code" width="200" height="200" />
              </div>
              <div class="tfa-secret-group">
                <label class="form-label">Or enter this secret manually:</label>
                <code class="tfa-secret">{{ tfaSecret }}</code>
              </div>
              <form @submit.prevent="verify2FA" class="profile-form" style="margin-top: 16px">
                <div class="form-group">
                  <label class="form-label" for="tfa-code">Verification Code</label>
                  <input
                    id="tfa-code"
                    v-model="tfaCode"
                    type="text"
                    class="form-input totp-input"
                    placeholder="000000"
                    maxlength="6"
                    inputmode="numeric"
                    autocomplete="one-time-code"
                  />
                  <small class="form-hint">Enter the 6-digit code from your authenticator app to verify setup</small>
                </div>
                <div style="display: flex; gap: 8px">
                  <button type="submit" class="btn btn-primary" :disabled="tfaLoading">
                    {{ tfaLoading ? 'Verifying...' : 'Verify & Enable' }}
                  </button>
                  <button type="button" class="btn btn-secondary" @click="cancel2FASetup">Cancel</button>
                </div>
              </form>
            </div>
          </template>

          <!-- 2FA Enabled -->
          <template v-if="twoFactorEnabled && !show2FADisable">
            <p class="tfa-description">Two-factor authentication is currently enabled on your account.</p>
            <button class="btn btn-danger" @click="show2FADisable = true">Disable 2FA</button>

            <!-- Recovery codes (R-10): single-use backup codes for a lost
                 authenticator. Shown once at generation, never retrievable. -->
            <div class="recovery-section">
              <div class="recovery-head">
                <div>
                  <h3 class="recovery-title">Recovery codes</h3>
                  <p class="tfa-description" style="margin-bottom: 0">
                    <template v-if="recoveryRemaining === null">Loading remaining codes…</template>
                    <template v-else-if="recoveryRemaining === 0">No unused recovery codes left — generate a new set.</template>
                    <template v-else>{{ recoveryRemaining }} unused recovery code{{ recoveryRemaining === 1 ? '' : 's' }} left. Each works once in place of your authenticator code at sign-in.</template>
                  </p>
                </div>
                <button class="btn btn-secondary btn-sm" :disabled="recoveryRegenLoading" @click="showRecoveryRegen = !showRecoveryRegen">
                  Regenerate codes
                </button>
              </div>

              <form v-if="showRecoveryRegen" @submit.prevent="regenerateRecoveryCodes" class="profile-form" style="margin-top: 12px">
                <div class="form-group">
                  <label class="form-label" for="recovery-regen-code">Confirm with your authenticator</label>
                  <input
                    id="recovery-regen-code"
                    v-model="recoveryRegenCode"
                    type="text"
                    class="form-input totp-input"
                    placeholder="000000"
                    maxlength="6"
                    inputmode="numeric"
                    autocomplete="one-time-code"
                  />
                  <small class="form-hint">Regenerating invalidates every outstanding code.</small>
                </div>
                <div style="display: flex; gap: 8px">
                  <button type="submit" class="btn btn-primary btn-sm" :disabled="recoveryRegenLoading">
                    {{ recoveryRegenLoading ? 'Generating…' : 'Generate new codes' }}
                  </button>
                  <button type="button" class="btn btn-secondary btn-sm" @click="showRecoveryRegen = false; recoveryRegenCode = ''">Cancel</button>
                </div>
              </form>

              <div v-if="recoveryCodes.length > 0" class="recovery-codes">
                <p class="recovery-once-warning">
                  Save these codes now — they are shown only this once and cannot be retrieved again.
                </p>
                <div class="recovery-code-grid">
                  <code v-for="code in recoveryCodes" :key="code" class="recovery-code">{{ code }}</code>
                </div>
                <div style="display: flex; gap: 8px; margin-top: 12px">
                  <button type="button" class="btn btn-secondary btn-sm" @click="copyRecoveryCodes">Copy all</button>
                  <button type="button" class="btn btn-secondary btn-sm" @click="downloadRecoveryCodes">Download .txt</button>
                  <button type="button" class="btn btn-ghost btn-sm" @click="recoveryCodes = []">I've saved them</button>
                </div>
              </div>
            </div>
          </template>

          <!-- 2FA Disable Flow -->
          <template v-if="show2FADisable">
            <form @submit.prevent="disable2FA" class="profile-form">
              <p class="tfa-description">Enter a code from your authenticator app to confirm disabling 2FA.</p>
              <div class="form-group">
                <label class="form-label" for="tfa-disable-code">Authentication Code</label>
                <input
                  id="tfa-disable-code"
                  v-model="tfaDisableCode"
                  type="text"
                  class="form-input totp-input"
                  placeholder="000000"
                  maxlength="6"
                  inputmode="numeric"
                  autocomplete="one-time-code"
                />
              </div>
              <div style="display: flex; gap: 8px">
                <button type="submit" class="btn btn-danger" :disabled="tfaDisableLoading">
                  {{ tfaDisableLoading ? 'Disabling...' : 'Confirm Disable' }}
                </button>
                <button type="button" class="btn btn-secondary" @click="show2FADisable = false; tfaDisableCode = ''">Cancel</button>
              </div>
            </form>
          </template>
        </div>
      </div>

      <!-- Passkeys (R-19) -->
      <div class="card" v-if="passkeysSupportedHere">
        <div class="card-header">
          <h2>Passkeys</h2>
          <span v-if="passkeyCredentials.length > 0" class="badge badge-success">{{ passkeyCredentials.length }}</span>
        </div>
        <div class="card-body">
          <p class="tfa-description">
            Passkeys let you sign in with your fingerprint, face or device PIN — no password, phishing-proof. They stay
            on this device.
          </p>

          <div v-if="passkeyLoading && passkeyCredentials.length === 0" style="text-align: center; padding: 12px 0">
            <div class="spinner"></div>
          </div>

          <div v-else-if="passkeyCredentials.length === 0" class="text-muted" style="font-size: 13px; margin-bottom: 12px">
            No passkeys registered yet.
          </div>

          <div v-else class="session-list" style="margin-bottom: 12px">
            <div v-for="cred in passkeyCredentials" :key="cred.id" class="session-item">
              <div class="session-info">
                <template v-if="renamingPasskeyId === cred.id">
                  <input
                    v-model="renamePasskeyValue"
                    type="text"
                    class="form-input"
                    style="max-width: 240px"
                    maxlength="64"
                    @keyup.enter="renamePasskey(cred)"
                    @keyup.esc="renamingPasskeyId = null"
                  />
                </template>
                <template v-else>
                  <div class="session-browser">{{ cred.name }}</div>
                </template>
                <div class="session-meta">Last used {{ formatPasskeyDate(cred.last_used_at) }} &middot; Added {{ formatPasskeyDate(cred.created_at) }}</div>
              </div>
              <div style="display: flex; gap: 6px">
                <button
                  class="btn btn-ghost btn-sm"
                  @click="renamingPasskeyId = cred.id; renamePasskeyValue = cred.name"
                >Rename</button>
                <button class="btn btn-danger btn-sm" @click="deletePasskey(cred)">Remove</button>
              </div>
            </div>
          </div>

          <template v-if="showPasskeyAdd">
            <form @submit.prevent="registerPasskey" class="profile-form">
              <div class="form-group">
                <label class="form-label" for="passkey-name">Name this passkey</label>
                <input id="passkey-name" v-model="newPasskeyName" type="text" class="form-input"
                  placeholder="e.g. Work laptop" maxlength="64" />
              </div>
              <div style="display: flex; gap: 8px">
                <button type="submit" class="btn btn-primary" :disabled="passkeyLoading">
                  {{ passkeyLoading ? 'Waiting for your device…' : 'Create passkey' }}
                </button>
                <button type="button" class="btn btn-secondary" @click="showPasskeyAdd = false; newPasskeyName = ''">Cancel</button>
              </div>
            </form>
          </template>
          <button v-else class="btn btn-primary" :disabled="passkeyLoading" @click="showPasskeyAdd = true">
            Add a passkey
          </button>
        </div>
      </div>

      <!-- Linked OAuth Accounts -->
      <div class="card" v-if="oauthProviders.length > 0 || linkedAccounts.length > 0">
        <div class="card-header"><h2>Linked Accounts</h2></div>
        <div class="card-body">
          <div v-if="oauthLoading" style="text-align: center; padding: 20px 0"><div class="spinner"></div></div>
          <template v-else>
            <div v-if="linkedAccounts.length > 0" class="session-list" style="margin-bottom: 16px">
              <div v-for="account in linkedAccounts" :key="account.id" class="session-item">
                <div class="session-info">
                  <div class="session-browser">
                    {{ account.provider_name }}
                    <span class="badge badge-neutral" style="margin-left: 6px">{{ account.provider_type }}</span>
                  </div>
                  <div class="session-meta">{{ account.email }}</div>
                </div>
                <button
                  class="btn btn-danger btn-sm"
                  :disabled="unlinkingAccount === account.id"
                  @click="unlinkOAuthAccount(account)"
                >
                  {{ unlinkingAccount === account.id ? 'Unlinking...' : 'Unlink' }}
                </button>
              </div>
            </div>
            <div v-else class="text-muted" style="font-size: 13px; margin-bottom: 16px">
              No linked accounts.
            </div>

            <div v-if="availableProviders.length > 0">
              <p class="tfa-description">Link an additional login method to your account:</p>
              <div style="display: flex; gap: 8px; flex-wrap: wrap">
                <button
                  v-for="provider in availableProviders"
                  :key="provider.slug"
                  class="btn btn-secondary"
                  @click="linkOAuthProvider(provider.slug)"
                >
                  Link {{ provider.name }}
                </button>
              </div>
            </div>
          </template>
        </div>
      </div>

      <!-- Active Sessions -->
      <div class="card">
        <div class="card-header">
          <h2>Active Sessions</h2>
          <button
            v-if="sessions.length > 1"
            class="btn btn-danger btn-sm"
            :disabled="revokingOthers"
            @click="revokeOtherSessions"
          >
            {{ revokingOthers ? 'Revoking...' : 'Revoke All Others' }}
          </button>
        </div>
        <div class="card-body">
          <p class="tfa-description">
            These are the devices and browsers currently logged in to your account.
          </p>

          <div v-if="sessionsLoading" style="text-align: center; padding: 20px 0">
            <div class="spinner"></div>
          </div>

          <div v-else-if="sessions.length === 0" class="text-muted" style="text-align: center; padding: 16px 0">
            No active sessions found.
          </div>

          <div v-else class="session-list">
            <div v-for="s in sessions" :key="s.id" class="session-item" :class="{ 'session-current': s.current }">
              <div class="session-info">
                <div class="session-browser">
                  {{ sessionLabel(s) }}
                  <span v-if="sessionDevice(s)" class="badge" style="margin-left: 6px">{{ sessionDevice(s) }}</span>
                  <span v-if="s.current" class="badge badge-success" style="margin-left: 6px">Current</span>
                </div>
                <div class="session-meta">
                  {{ s.ip_address }} &middot; Created {{ formatSessionDate(s.created_at) }} &middot; Expires {{ formatSessionDate(s.expires_at) }}
                </div>
              </div>
              <button
                v-if="!s.current"
                class="btn btn-danger btn-sm"
                :disabled="revokingSession === s.id"
                @click="revokeSession(s)"
              >
                {{ revokingSession === s.id ? 'Revoking...' : 'Revoke' }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- App Passwords (SF-35/SF-67): mail-client credentials -->
      <div class="card">
        <div class="card-header"><h2>App Passwords</h2></div>
        <div class="card-body">
          <p class="tfa-description">
            Separate passwords for mail clients (IMAP, POP3, submission) so your account password never
            has to be pasted into an email app. Each password is shown once, then stored only as a
            one-way digest.
          </p>

          <!-- One-time display of a freshly generated password -->
          <div v-if="appPasswordSecret" class="app-password-reveal">
            <p class="tfa-description" style="margin-bottom: 8px">
              <strong>{{ appPasswordSecretName }}</strong> — store this password now. It will never be
              shown again.
            </p>
            <div class="app-password-row">
              <code class="app-password-value">{{ appPasswordSecret }}</code>
              <button class="btn btn-secondary btn-sm" @click="copyAppPassword">
                {{ appPasswordCopied ? 'Copied' : 'Copy' }}
              </button>
              <button class="btn btn-primary btn-sm" @click="dismissAppPasswordSecret">Done</button>
            </div>
          </div>

          <form v-else class="app-password-form" @submit.prevent="createAppPassword">
            <div class="form-group">
              <label class="form-label" for="app-password-name">New app password name</label>
              <div class="app-password-row">
                <input
                  id="app-password-name"
                  v-model="appPasswordName"
                  type="text"
                  class="form-input"
                  placeholder="e.g. Laptop Thunderbird"
                  maxlength="100"
                  required
                />
                <button type="submit" class="btn btn-primary" :disabled="appPasswordCreating">
                  {{ appPasswordCreating ? 'Generating...' : 'Generate' }}
                </button>
              </div>
            </div>
          </form>

          <div v-if="appPasswordsLoading" style="text-align: center; padding: 12px 0">
            <div class="spinner"></div>
          </div>
          <div v-else-if="appPasswords.length === 0" class="text-muted" style="font-size: 13px">
            No app passwords yet.
          </div>
          <div v-else class="session-list">
            <div v-for="p in appPasswords" :key="p.id" class="session-item">
              <div class="session-info">
                <div class="session-browser">
                  {{ p.name }}
                  <span v-if="p.revoked_at" class="badge badge-secondary" style="margin-left: 6px">Revoked</span>
                </div>
                <div class="session-meta">
                  Created {{ formatAppPasswordDate(p.created_at) }} &middot; Last used {{ formatAppPasswordDate(p.last_used_at ?? null) }}
                </div>
              </div>
              <button
                v-if="!p.revoked_at"
                class="btn btn-danger btn-sm"
                @click="revokeAppPassword(p)"
              >
                Revoke
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Change Password -->
      <div class="card">
        <div class="card-header"><h2>Change Password</h2></div>
        <div class="card-body">
          <form @submit.prevent="handlePasswordChange" class="profile-form">
            <div class="form-group">
              <label class="form-label" for="current-password">Current Password</label>
              <input id="current-password" v-model="currentPassword" type="password" class="form-input" placeholder="Enter current password" required autocomplete="current-password" />
            </div>
            <div class="form-group">
              <label class="form-label" for="new-password">New Password</label>
              <input id="new-password" v-model="newPassword" type="password" class="form-input" placeholder="Minimum 8 characters" required minlength="8" autocomplete="new-password" />
            </div>
            <div class="form-group">
              <label class="form-label" for="confirm-password">Confirm New Password</label>
              <input id="confirm-password" v-model="confirmPassword" type="password" class="form-input" placeholder="Re-enter new password" required minlength="8" autocomplete="new-password" />
            </div>
            <button type="submit" class="btn btn-primary" :disabled="passwordLoading">
              {{ passwordLoading ? 'Updating...' : 'Change Password' }}
            </button>
          </form>
        </div>
      </div>

      <!-- Browser push notifications (SF-89) -->
      <div class="card" v-if="pushState !== 'off'">
        <div class="card-header"><h2>Browser Notifications</h2></div>
        <div class="card-body">
          <p class="tfa-description">
            Get a browser notification when new activity lands in your workspaces — message replies,
            form submissions, and campaign progress — even when the dashboard is closed.
          </p>

          <div v-if="pushState === 'loading'" style="text-align: center; padding: 12px 0">
            <div class="spinner"></div>
          </div>

          <div v-else-if="pushState === 'unsupported'" class="text-muted" style="font-size: 13px">
            This browser does not support push notifications.
          </div>

          <div v-else-if="pushState === 'denied'" class="text-muted" style="font-size: 13px">
            Notifications are blocked for this site. Re-enable them in your browser's site settings,
            then reload this page.
          </div>

          <template v-else>
            <button class="btn btn-primary" :disabled="pushBusy" @click="enablePush">
              {{ pushBusy ? 'Working...' : 'Enable browser notifications' }}
            </button>

            <div v-if="pushSubs.length > 0" class="session-list" style="margin-top: 12px">
              <div v-for="s in pushSubs" :key="s.id" class="session-item">
                <div class="session-info">
                  <div class="session-browser">
                    {{ pushSubscriptionLabel(s) }}
                  </div>
                  <div class="session-meta">
                    Subscribed {{ formatAppPasswordDate(s.created_at) }}
                  </div>
                </div>
                <button class="btn btn-danger btn-sm" :disabled="pushBusy" @click="removePush(s.id)">
                  Remove
                </button>
              </div>
            </div>
          </template>
        </div>
      </div>

      <!-- Danger Zone -->
      <div class="card danger-card" v-if="!auth.isAdmin">
        <div class="card-header"><h2>Danger Zone</h2></div>
        <div class="card-body">
          <!-- Deletion already scheduled -->
          <template v-if="scheduledDeletionAt">
            <div class="deletion-notice">
              <p class="deletion-warning">Your account is scheduled for permanent deletion on <strong>{{ formatDeletionDate(scheduledDeletionAt) }}</strong>.</p>
              <p class="tfa-description">Your account has been deactivated. All your data will be permanently removed after this date. You can cancel the deletion to reactivate your account.</p>
              <button class="btn btn-primary" :disabled="deletionLoading" @click="cancelAccountDeletion">
                {{ deletionLoading ? 'Cancelling...' : 'Cancel Deletion' }}
              </button>
            </div>
          </template>

          <!-- No deletion scheduled -->
          <template v-else>
            <p class="tfa-description">
              Permanently delete your account and all associated data. Your account will be deactivated immediately and permanently deleted after 7 days. This gives you time to change your mind.
            </p>
            <button class="btn btn-danger" :disabled="deletionLoading" @click="requestAccountDeletion">
              {{ deletionLoading ? 'Processing...' : 'Delete My Account' }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.profile-grid {
  display: grid;
  gap: 24px;
  max-width: 640px;
}

.profile-form {
  display: grid;
  gap: 1rem;
}

.form-hint {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 4px;
}

.tfa-description {
  font-size: 13px;
  color: var(--text-secondary);
  margin-bottom: 16px;
}

.tfa-setup {
  display: grid;
  gap: 16px;
}

.tfa-qr {
  display: flex;
  justify-content: center;
  padding: 16px;
  background: var(--bg-primary);
  border-radius: var(--radius);
  border: 1px solid var(--border-primary);
  width: fit-content;
}

.tfa-secret-group {
  display: grid;
  gap: 6px;
}

.tfa-secret {
  display: block;
  padding: 10px 14px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  font-size: 14px;
  font-family: var(--font-mono);
  letter-spacing: 2px;
  word-break: break-all;
  user-select: all;
}

.totp-input {
  font-size: 20px;
  text-align: center;
  letter-spacing: 8px;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  max-width: 220px;
}

/* Recovery codes (R-10) */
.recovery-section {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--border-primary);
}

.recovery-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.recovery-title {
  margin: 0 0 4px;
  font-size: 14px;
  font-weight: 600;
}

.recovery-codes {
  margin-top: 14px;
  padding: 14px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
}

.recovery-once-warning {
  margin: 0 0 12px;
  font-size: 13px;
  font-weight: 500;
  color: var(--warning-text, var(--text-secondary));
}

.recovery-code-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 8px;
}

.recovery-code {
  padding: 8px 10px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  font-family: var(--font-mono);
  font-size: 14px;
  letter-spacing: 1px;
  text-align: center;
  user-select: all;
}

.session-list {
  display: grid;
  gap: 8px;
}

.session-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-tertiary);
}

.session-current {
  border-color: var(--primary-300);
  background: var(--primary-50);
}

.session-info {
  min-width: 0;
}

.session-browser {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary);
  display: flex;
  align-items: center;
}

.session-meta {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
  font-variant-numeric: tabular-nums;
}

.danger-card {
  border-color: color-mix(in srgb, var(--danger-500) 35%, transparent);
}

.danger-card .card-header h2 {
  color: var(--danger-text);
}

.deletion-notice {
  display: grid;
  gap: 12px;
}

.deletion-warning {
  font-size: 14px;
  color: var(--danger-text);
  margin: 0;
}

.plan-limits-grid {
  display: grid;
  gap: 16px;
}

.plan-limit-group {
  padding: 12px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
}

.plan-limit-title {
  margin: 0 0 8px;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.08em;
}

.plan-limit-row {
  display: flex;
  justify-content: space-between;
  padding: 4px 0;
  font-size: 13px;
}

.plan-limit-row span:first-child {
  color: var(--text-secondary);
}

.plan-limit-row span:last-child {
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}
/* GAP-12: forced-enrollment banner on the 2FA card. */
.enrollment-banner {
  margin-bottom: 14px;
  padding: 10px 12px;
  font-size: 13px;
  border: 1px solid color-mix(in srgb, var(--warning-text, #b45309) 35%, transparent);
  background: color-mix(in srgb, var(--warning-text, #b45309) 8%, transparent);
  border-radius: var(--radius);
  color: var(--text-primary);
}

/* SF-35/SF-67: app passwords card. */
.app-password-row {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.app-password-row .form-input {
  flex: 1;
  min-width: 220px;
}
.app-password-value {
  flex: 1;
  min-width: 220px;
  padding: 8px 10px;
  font-family: var(--font-mono);
  font-size: 13px;
  word-break: break-all;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
}
.app-password-reveal {
  margin-bottom: 16px;
  padding: 12px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-tertiary);
}
</style>
