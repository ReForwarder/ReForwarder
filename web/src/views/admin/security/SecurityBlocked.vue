<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '../../../api/admin'
import type { SecurityBan, Pageable } from '../../../api/types'
import { useNotificationStore } from '../../../stores/notification'
import { useConfirm } from '../../../composables/useConfirm'
import { useModalSafeClose } from '../../../composables/useModalSafeClose'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'

const router = useRouter()
const notify = useNotificationStore()
const { confirm } = useConfirm()
const loading = ref(true)
const bans = ref<SecurityBan[]>([])

const search = ref('')
const filter = ref<'active' | 'expired' | 'all'>('active')
let searchTimeout: ReturnType<typeof setTimeout> | null = null

// Reason codes the backend accepts for manual bans (securityReasons whitelist).
const reasonCodes = [
  'MANUAL_ADMIN_BLOCK',
  'PORT_SCAN_DETECTED',
  'RAPID_CONNECTION_BURST',
  'CONNECTION_ABUSE',
  'INCOMPLETE_HANDSHAKE_ABUSE',
  'TLS_PROBING',
  'SMTP_PROTOCOL_ABUSE',
  'HTTP_RECONNAISSANCE',
  'MALFORMED_PROTOCOL_ABUSE',
  'AUTH_BRUTE_FORCE',
]

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminApi.listSecurityBans(page, pageable.value.size, search.value, filter.value)
    bans.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load bans', e)
  } finally {
    loading.value = false
  }
})

// Debounce keystrokes, then reset to the first page of results.
function onSearchInput() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => goToPage(0), 300)
}

function setFilter(f: 'active' | 'expired' | 'all') {
  filter.value = f
  goToPage(0)
}

// ---- Manual ban ----

const showBanForm = ref(false)
const banLoading = ref(false)
const banForm = ref({ ip_address: '', description: '', permanent: false, duration_hours: 24, reason_code: 'MANUAL_ADMIN_BLOCK' })

async function createBan() {
  if (!banForm.value.ip_address.trim()) {
    notify.error('An IP address or CIDR block is required')
    return
  }
  if (!banForm.value.permanent && banForm.value.duration_hours <= 0) {
    notify.error('A temporary ban needs a positive duration in hours')
    return
  }
  banLoading.value = true
  try {
    const res = await adminApi.createSecurityBan({
      ip_address: banForm.value.ip_address.trim(),
      description: banForm.value.description.trim() || undefined,
      permanent: banForm.value.permanent,
      duration_hours: banForm.value.permanent ? undefined : banForm.value.duration_hours,
      reason_code: banForm.value.reason_code || undefined,
    })
    const data = res.data.data
    if (data && 'suppressed' in data && data.suppressed) {
      notify.info(data.message)
    } else {
      notify.success('Ban placed')
    }
    showBanForm.value = false
    banForm.value = { ip_address: '', description: '', permanent: false, duration_hours: 24, reason_code: 'MANUAL_ADMIN_BLOCK' }
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to place ban'
    notify.error(message)
  } finally {
    banLoading.value = false
  }
}

// ---- Detail modal ----

const selected = ref<SecurityBan | null>(null)
const extendHours = ref(24)
const extendLoading = ref(false)

function openDetail(ban: SecurityBan) {
  selected.value = ban
  extendHours.value = 24
}

// Pretty-print the JSON evidence blob; fall back to the raw string if it
// isn't valid JSON (or empty).
const prettyEvidence = computed(() => {
  const raw = selected.value?.evidence
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
})

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}

function expiryBadge(ban: SecurityBan): { cls: string; text: string } {
  if (!ban.expires_at) return { cls: 'badge badge-dot badge-neutral', text: 'Permanent' }
  const ms = new Date(ban.expires_at).getTime() - Date.now()
  if (ms <= 0) return { cls: 'badge badge-dot badge-danger', text: 'Expired' }
  const mins = Math.floor(ms / 60000)
  if (mins < 60) return { cls: 'badge badge-dot badge-success', text: `in ${mins}m` }
  const hours = Math.floor(mins / 60)
  if (hours < 48) return { cls: 'badge badge-dot badge-success', text: `in ${hours}h` }
  return { cls: 'badge badge-dot badge-success', text: `in ${Math.floor(hours / 24)}d` }
}

function reasonBadgeClass(reason: string) {
  switch (reason) {
    case 'PORT_SCAN_DETECTED':
    case 'AUTH_BRUTE_FORCE':
    case 'HTTP_RECONNAISSANCE':
      return 'badge badge-dot badge-danger'
    case 'MANUAL_ADMIN_BLOCK':
      return 'badge badge-dot badge-neutral'
    default:
      return 'badge badge-dot badge-warning'
  }
}

function typeBadgeClass(ban: SecurityBan) {
  return ban.ban_type === 'automatic' ? 'badge badge-info' : 'badge badge-neutral'
}

async function unban(ban: SecurityBan) {
  const okToUnban = await confirm({
    title: 'Unblock IP',
    message: `Lift the ban on ${ban.ip_address}? The address becomes subject to detection again immediately.`,
    confirmText: 'Unblock',
    variant: 'danger',
  })
  if (!okToUnban) return
  try {
    await adminApi.unbanSecurityIp(ban.id)
    notify.success(`Ban lifted for ${ban.ip_address}`)
    selected.value = null
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to lift ban'
    notify.error(message)
  }
}

async function extendBan() {
  if (!selected.value) return
  if (extendHours.value <= 0) {
    notify.error('Hours must be a positive number')
    return
  }
  extendLoading.value = true
  try {
    await adminApi.extendSecurityBan(selected.value.id, extendHours.value)
    notify.success(`Ban extended by ${extendHours.value} hours`)
    selected.value = null
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to extend ban'
    notify.error(message)
  } finally {
    extendLoading.value = false
  }
}

// Hand the address to the allowlist page's create form via the query string.
function allowlistAddress(ip: string) {
  router.push({ path: '/admin/security/allowlist', query: { ip } })
}

const { watchClickStart, confirmClickEnd } = useModalSafeClose(() => {
  selected.value = null
})
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Blocked IPs</h1>
        <p class="page-description">Addresses banned by the detection engine or by administrators.</p>
      </div>
      <button class="btn btn-primary" @click="showBanForm = !showBanForm">
        {{ showBanForm ? 'Cancel' : 'Ban IP' }}
      </button>
    </div>

    <div v-if="showBanForm" class="card" style="margin-bottom: 1.5rem;">
      <div class="card-header"><h2>Place Manual Ban</h2></div>
      <div class="card-body">
        <form @submit.prevent="createBan" style="display: grid; gap: 1rem; max-width: 480px;">
          <div class="form-group">
            <label class="form-label" for="ban-ip">IP address or CIDR</label>
            <input id="ban-ip" v-model="banForm.ip_address" type="text" class="form-input" placeholder="203.0.113.7 or 10.0.0.0/8" required />
          </div>
          <div class="form-group">
            <label class="form-label" for="ban-reason">Reason</label>
            <select id="ban-reason" v-model="banForm.reason_code" class="form-select">
              <option v-for="code in reasonCodes" :key="code" :value="code">{{ code }}</option>
            </select>
          </div>
          <div class="form-group">
            <label class="form-label" for="ban-desc">Description</label>
            <input id="ban-desc" v-model="banForm.description" type="text" class="form-input" placeholder="Why is this address banned?" />
          </div>
          <div class="form-group ban-duration-row">
            <label class="form-label" for="ban-permanent">
              <input id="ban-permanent" v-model="banForm.permanent" type="checkbox" />
              Permanent
            </label>
            <template v-if="!banForm.permanent">
              <label class="form-label" for="ban-hours">Duration (hours)</label>
              <input id="ban-hours" v-model.number="banForm.duration_hours" type="number" class="form-input" min="1" style="width: 110px;" />
            </template>
          </div>
          <div>
            <button type="submit" class="btn btn-danger" :disabled="banLoading">
              {{ banLoading ? 'Placing...' : 'Place Ban' }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <div class="card">
      <div class="card-header" style="display: flex; gap: 12px; align-items: center; flex-wrap: wrap;">
        <h2>Bans</h2>
        <input
          v-model="search"
          type="text"
          class="form-input"
          placeholder="Search by IP address..."
          style="max-width: 320px; margin-left: auto;"
          aria-label="Search bans by IP address"
          @input="onSearchInput"
        />
      </div>

      <!-- Filter pills -->
      <div style="display: flex; gap: 0.5rem; padding: 16px 24px 0;">
        <button class="btn btn-sm" :class="filter === 'active' ? 'btn-primary' : 'btn-secondary'" @click="setFilter('active')">Active</button>
        <button class="btn btn-sm" :class="filter === 'expired' ? 'btn-primary' : 'btn-secondary'" @click="setFilter('expired')">Expired</button>
        <button class="btn btn-sm" :class="filter === 'all' ? 'btn-primary' : 'btn-secondary'" @click="setFilter('all')">All</button>
      </div>

      <!-- Loading: skeleton rows preserve the table layout while data streams in. -->
      <div v-if="loading" class="card-body">
        <table class="table">
          <thead>
            <tr>
              <th>IP Address</th>
              <th>Reason</th>
              <th>Risk</th>
              <th>Type</th>
              <th>Blocked</th>
              <th>Expires</th>
              <th style="text-align: right;">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in 6" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 110px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 130px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 36px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 70px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 120px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 68px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 110px; margin-left: auto;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="bans.length === 0" class="empty-state">
        <h3>No bans found</h3>
        <p v-if="search || filter !== 'all'">No bans match the current search and filter.</p>
        <p v-else>Nothing has been banned yet.</p>
      </div>

      <div v-else class="card-body">
        <div class="table-wrapper">
          <table class="table">
            <thead>
              <tr>
                <th>IP Address</th>
                <th>Reason</th>
                <th>Risk</th>
                <th>Type</th>
                <th>Blocked</th>
                <th>Expires</th>
                <th style="text-align: right;">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="ban in bans" :key="ban.id" class="row-clickable" @click="openDetail(ban)">
                <td><code>{{ ban.ip_address }}</code></td>
                <td><span :class="reasonBadgeClass(ban.reason_code)">{{ ban.reason_code }}</span></td>
                <td>{{ ban.risk_score }}</td>
                <td><span :class="typeBadgeClass(ban)">{{ ban.ban_type }}</span></td>
                <td style="white-space: nowrap;"><span :title="formatDate(ban.created_at)">{{ formatDate(ban.created_at) }}</span></td>
                <td><span :class="expiryBadge(ban).cls">{{ expiryBadge(ban).text }}</span></td>
                <td>
                  <div class="table-actions" @click.stop>
                    <button class="btn btn-icon btn-icon-muted" title="View details" @click="openDetail(ban)">
                      <span class="mdi mdi-eye-outline"></span>
                    </button>
                    <button class="btn btn-icon btn-icon-danger" title="Unblock" @click="unban(ban)">
                      <span class="mdi mdi-lock-open-variant-outline"></span>
                    </button>
                    <button
                      class="btn btn-icon btn-icon-muted"
                      :title="ban.expires_at ? 'Extend ban' : 'Permanent bans cannot be extended'"
                      :disabled="!ban.expires_at"
                      @click="openDetail(ban)"
                    >
                      <span class="mdi mdi-clock-outline"></span>
                    </button>
                    <button class="btn btn-icon btn-icon-muted" title="Add to allowlist" @click="allowlistAddress(ban.ip_address)">
                      <span class="mdi mdi-shield-check-outline"></span>
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
      </div>
    </div>

    <!-- Ban Detail Modal -->
    <div v-if="selected" class="modal-overlay" @mousedown="watchClickStart" @mouseup="confirmClickEnd">
      <div class="modal modal-wide" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h3>Ban <code>{{ selected.ip_address }}</code></h3>
          <span :class="reasonBadgeClass(selected.reason_code)">{{ selected.reason_code }}</span>
        </div>
        <div class="modal-body">
          <dl class="detail-grid">
            <dt>Reason</dt>
            <dd>{{ selected.reason_description }}</dd>

            <dt>Risk score</dt>
            <dd>{{ selected.risk_score }}</dd>

            <dt>Offense count</dt>
            <dd>{{ selected.offense_count }}</dd>

            <dt>Type</dt>
            <dd><span :class="typeBadgeClass(selected)">{{ selected.ban_type }}</span></dd>

            <dt>Blocked by</dt>
            <dd><code>{{ selected.created_by }}</code></dd>

            <dt>Blocked at</dt>
            <dd>{{ formatDate(selected.created_at) }}</dd>

            <dt>Expires</dt>
            <dd>{{ selected.expires_at ? formatDate(selected.expires_at) : 'Never (permanent)' }}</dd>
          </dl>

          <h4 class="evidence-heading">Evidence</h4>
          <pre v-if="prettyEvidence" class="code-block evidence-block">{{ prettyEvidence }}</pre>
          <p v-else class="evidence-empty">No evidence was recorded for this ban.</p>

          <div v-if="selected.expires_at" class="extend-row">
            <label class="form-label" for="extend-hours">Extend by (hours)</label>
            <input id="extend-hours" v-model.number="extendHours" type="number" class="form-input" min="1" style="width: 100px;" />
            <button class="btn btn-secondary" :disabled="extendLoading" @click="extendBan">
              {{ extendLoading ? 'Extending...' : 'Extend' }}
            </button>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="allowlistAddress(selected.ip_address)">
            <span class="mdi mdi-shield-check-outline"></span>
            Add to allowlist
          </button>
          <button class="btn btn-danger" @click="unban(selected)">Unblock</button>
          <button class="btn btn-secondary" @click="selected = null">Close</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-description {
  font-size: 13px;
  color: var(--text-muted);
  margin-top: 4px;
}

.ban-duration-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.ban-duration-row label {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
}

.modal-wide {
  max-width: 680px;
  width: calc(100% - 32px);
}

.detail-grid {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: 10px 16px;
  margin: 0;
}

.detail-grid dt {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-muted);
  padding-top: 3px;
}

.detail-grid dd {
  margin: 0;
  font-size: 14px;
  color: var(--text-primary);
  word-break: break-word;
}

.evidence-heading {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-muted);
  margin: 18px 0 8px;
}

.evidence-block {
  margin: 0;
  max-height: 260px;
  overflow-y: auto;
}

.evidence-empty {
  font-size: 13px;
  color: var(--text-muted);
  margin: 0;
}

.extend-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--border-primary);
}

.extend-row .form-label {
  margin: 0;
  white-space: nowrap;
}

/* IPs and actor emails are technical identifiers — set them in the mono face. */
code {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  padding: 1px 6px;
}

@media (max-width: 640px) {
  .detail-grid {
    grid-template-columns: 1fr;
    gap: 2px 0;
  }

  .detail-grid dt {
    padding-top: 8px;
  }
}
</style>
