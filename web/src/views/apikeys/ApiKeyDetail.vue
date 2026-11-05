<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { apiKeysApi } from '../../api/apikeys'
import type { ApiKey, ApiKeyUsage } from '../../api/types'
import { useNotificationStore } from '../../stores/notification'
import { useConfirm } from '../../composables/useConfirm'
import { useWorkspaceStore } from '../../stores/workspace'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

const loading = ref(true)
const key = ref<ApiKey | null>(null)
// R-18: trailing daily usage series for this key.
const usage = ref<ApiKeyUsage | null>(null)
const usageLoading = ref(false)

const keyId = Number(route.params.id)

onMounted(async () => {
  try {
    const res = await apiKeysApi.get(keyId)
    key.value = res.data.data
  } catch {
    notify.error('Failed to load API key')
  } finally {
    loading.value = false
  }
  loadUsage()
})

async function loadUsage() {
  usageLoading.value = true
  try {
    const res = await apiKeysApi.usage(keyId, 30)
    usage.value = res.data.data
  } catch {
    // Usage analytics are best-effort; the key details remain usable without them.
    usage.value = null
  } finally {
    usageLoading.value = false
  }
}

const status = computed<{ label: string; class: string }>(() => {
  const k = key.value
  if (!k) return { label: '', class: '' }
  if (k.revoked) return { label: 'Revoked', class: 'badge-danger' }
  if (k.expires_at && new Date(k.expires_at) < new Date()) return { label: 'Expired', class: 'badge-warning' }
  return { label: 'Active', class: 'badge-success' }
})

// An empty/missing scope set means the key predates scopes; show it as send.
const keyScopes = computed<string[]>(() => key.value?.scopes ?? [])

// R-15: per-key request caps, 0 = unlimited.
const rateLimitLabel = computed(() => {
  const k = key.value
  if (!k) return ''
  const hourly = k.rate_limit_hourly ?? 0
  const daily = k.rate_limit_daily ?? 0
  if (hourly <= 0 && daily <= 0) return ''
  const parts: string[] = []
  if (hourly > 0) parts.push(`${hourly.toLocaleString()}/hour`)
  if (daily > 0) parts.push(`${daily.toLocaleString()}/day`)
  return parts.join(' · ')
})

const usageMax = computed(() => {
  const days = usage.value?.days ?? []
  return days.reduce((max, d) => Math.max(max, d.requests), 0)
})

const isActive = computed(() => {
  const k = key.value
  return !!k && !k.revoked && !(k.expires_at && new Date(k.expires_at) < new Date())
})

const canDelete = computed(() => {
  const k = key.value
  return !!k && (k.revoked || (!!k.expires_at && new Date(k.expires_at) < new Date()))
})

function formatDate(dateStr: string | null): string {
  if (!dateStr) return 'Never'
  return new Date(dateStr).toLocaleString()
}

function formatDay(day: string): string {
  const d = new Date(day + 'T00:00:00Z')
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

async function revoke() {
  if (!key.value) return
  const confirmed = await confirm({
    title: 'Revoke API Key',
    message: `Are you sure you want to revoke "${key.value.name}"? This key will immediately stop working and cannot be reactivated.`,
    confirmText: 'Revoke',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await apiKeysApi.revoke(key.value.id)
    notify.success('API key revoked')
    const res = await apiKeysApi.get(keyId)
    key.value = res.data.data
  } catch {
    notify.error('Failed to revoke API key')
  }
}

async function remove() {
  if (!key.value) return
  const confirmed = await confirm({
    title: 'Delete API Key',
    message: `Are you sure you want to permanently delete "${key.value.name}"? This action cannot be undone.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await apiKeysApi.delete(key.value.id)
    notify.success('API key deleted')
    router.push('/api-keys')
  } catch {
    notify.error('Failed to delete API key')
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>{{ key?.name || 'API Key' }}</h1>
      <div style="display: flex; gap: 8px">
        <button v-if="wsStore.canEdit && isActive" class="btn btn-danger" @click="revoke">Revoke</button>
        <button v-if="wsStore.canEdit && canDelete" class="btn btn-danger" @click="remove">Delete</button>
        <button class="btn btn-secondary" @click="router.push('/api-keys')">Back to API Keys</button>
      </div>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <div v-else-if="!key" class="card">
      <div class="empty-state">
        <h3>API key not found</h3>
        <p>It may have been deleted, or belongs to another workspace.</p>
      </div>
    </div>

    <div v-else class="card">
      <div class="card-header">
        <h2>{{ key.name }}</h2>
        <span class="badge" :class="status.class">{{ status.label }}</span>
      </div>
      <div class="card-body">
        <table>
          <tbody>
            <tr>
              <td style="font-weight: 600; width: 160px">Key ID</td>
              <td><code>{{ key.id }}</code></td>
            </tr>
            <tr>
              <td style="font-weight: 600">Prefix</td>
              <td><code>{{ key.key_prefix }}…</code></td>
            </tr>
            <tr>
              <td style="font-weight: 600">Created</td>
              <td>{{ formatDate(key.created_at) }}</td>
            </tr>
            <tr v-if="key.created_by">
              <td style="font-weight: 600">Created by</td>
              <td>{{ key.created_by.name }}</td>
            </tr>
            <tr>
              <td style="font-weight: 600">Last used</td>
              <td>{{ formatDate(key.last_used_at) }}</td>
            </tr>
            <tr>
              <td style="font-weight: 600">Expires</td>
              <td>{{ formatDate(key.expires_at) }}</td>
            </tr>
            <tr>
              <td style="font-weight: 600">Scopes</td>
              <td>
                <template v-if="keyScopes.length > 0">
                  <span
                    v-for="s in keyScopes"
                    :key="s"
                    style="display: inline-block; padding: 2px 8px; margin-right: 4px; font-size: 12px; font-family: var(--font-mono); border-radius: 4px; background: var(--bg-tertiary); border: 1px solid var(--border-primary); color: var(--text-secondary)"
                    >{{ s }}</span
                  >
                </template>
                <span v-else style="color: var(--text-muted)">send</span>
              </td>
            </tr>
            <tr>
              <td style="font-weight: 600">Allowed IPs</td>
              <td>
                <template v-if="key.allowed_ips && key.allowed_ips.length > 0">
                  <code v-for="(ip, i) in key.allowed_ips" :key="i" style="margin-right: 6px; font-size: 12px">{{ ip }}</code>
                </template>
                <span v-else style="color: var(--text-muted)">Any IP</span>
              </td>
            </tr>
            <tr>
              <td style="font-weight: 600">Rate limits</td>
              <td>
                <span v-if="rateLimitLabel">{{ rateLimitLabel }}</span>
                <span v-else style="color: var(--text-muted)">Unlimited</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- R-18: per-key usage analytics -->
    <div v-if="key" class="card" style="margin-top: 16px">
      <div class="card-header">
        <h2>Usage</h2>
        <span v-if="usage" class="badge">last {{ usage.days_trailing }} days</span>
      </div>
      <div class="card-body">
        <div v-if="usageLoading" class="loading-page" style="min-height: 80px">
          <div class="spinner"></div>
        </div>
        <template v-else-if="usage">
          <div class="usage-totals">
            <div class="usage-total">
              <span class="usage-total__value">{{ usage.totals.requests.toLocaleString() }}</span>
              <span class="usage-total__label">requests</span>
            </div>
            <div class="usage-total">
              <span class="usage-total__value">{{ usage.totals.sends.toLocaleString() }}</span>
              <span class="usage-total__label">sends</span>
            </div>
          </div>
          <div class="usage-chart" role="img" aria-label="Daily requests over the trailing window">
            <div v-for="d in usage.days" :key="d.day" class="usage-chart__col" :title="`${formatDay(d.day)}: ${d.requests} requests, ${d.sends} sends`">
              <div class="usage-chart__bar" :style="{ height: usageMax > 0 ? Math.max((d.requests / usageMax) * 100, d.requests > 0 ? 4 : 0) + '%' : '0%' }"></div>
              <span class="usage-chart__day">{{ formatDay(d.day) }}</span>
            </div>
          </div>
        </template>
        <p v-else class="empty-state" style="padding: 8px 0">Usage history is not available for this key yet.</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Key IDs, key prefixes, and allowed IPs are technical identifiers — render
   them as mono chips on the tertiary surface (brand spec). */
td code {
  font-family: var(--font-mono);
  font-size: 12.5px;
  color: var(--text-primary);
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  padding: 2px 8px;
}

/* R-18 usage card: totals row + a dependency-free daily bar series. */
.usage-totals {
  display: flex;
  gap: 24px;
  margin-bottom: 14px;
}

.usage-total {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.usage-total__value {
  font-size: 20px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  color: var(--text-primary);
}

.usage-total__label {
  font-size: 12px;
  color: var(--text-muted);
}

.usage-chart {
  display: flex;
  align-items: flex-end;
  gap: 4px;
  height: 120px;
  padding-top: 4px;
}

.usage-chart__col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  height: 100%;
  justify-content: flex-end;
}

.usage-chart__bar {
  width: 100%;
  max-width: 22px;
  border-radius: 3px 3px 0 0;
  background: var(--primary-500);
  min-height: 0;
}

.usage-chart__day {
  font-size: 9px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
}
</style>
