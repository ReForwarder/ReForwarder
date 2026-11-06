<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '../api/admin'
import { useAuthStore } from '../stores/auth'
import { useNotificationStore } from '../stores/notification'

// R-20 impersonation indicator. While an admin acts as this user the session's
// profile carries impersonated_by (types.ts UserProfile); everything the
// banner shows keys off that flag. The exit endpoint lives outside the admin
// middleware because the impersonated session carries the target's role —
// see internal/routes/user_routes.go.

const auth = useAuthStore()
const notification = useNotificationStore()
const router = useRouter()
const exiting = ref(false)

const impersonating = computed(() => {
  const u = auth.user as { impersonated_by?: number } | null
  return !!u?.impersonated_by
})

const reason = computed(() => {
  const u = auth.user as { impersonation_reason?: string } | null
  return (u?.impersonation_reason || '').trim()
})

async function exitImpersonation() {
  exiting.value = true
  try {
    await adminApi.exitImpersonation()
    // The impersonation session was revoked server-side and the cookie
    // expired: local state goes with it, and the admin re-authenticates
    // through the normal login flow.
    auth.clearSession()
    notification.success('Impersonation ended. Sign in again with your own account.')
    router.push('/login')
  } catch (err: any) {
    const msg =
      err?.response?.data?.error?.message ||
      err?.response?.data?.error ||
      err?.message ||
      'Failed to exit impersonation.'
    notification.error(msg)
  } finally {
    exiting.value = false
  }
}
</script>

<template>
  <div v-if="impersonating" class="app-banner app-banner--warning impersonation-banner" role="alert">
    <svg class="app-banner-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
      stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
    <div class="app-banner-content">
      <p class="app-banner-title">Viewing as {{ auth.user?.name || auth.user?.email }}</p>
      <p class="app-banner-text">
        You are impersonating this account{{ reason ? ` — reason: “${reason}”` : '' }}. Everything you do here is
        attributed to the impersonation session.
      </p>
    </div>
    <div class="app-banner-actions">
      <button class="app-banner-btn" :disabled="exiting" @click="exitImpersonation">
        {{ exiting ? 'Exiting…' : 'Exit impersonation' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
/* Layout-only override; visual styling comes from the shared .app-banner system. */
.impersonation-banner {
  border-color: color-mix(in srgb, var(--warning-500, #d97706) 45%, transparent);
}
</style>
