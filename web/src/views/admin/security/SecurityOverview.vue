<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '../../../api/admin'
import type { SecurityOverview, SecurityBan } from '../../../api/types'
import Sparkline from '@/components/Sparkline.vue'

const router = useRouter()
const loading = ref(true)
const overview = ref<SecurityOverview | null>(null)
const recentBans = ref<SecurityBan[]>([])

onMounted(async () => {
  // The security list endpoints are 1-based; the first page is page 1.
  const [overviewRes, bansRes] = await Promise.allSettled([
    adminApi.getSecurityOverview(),
    adminApi.listSecurityBans(1, 5, '', 'active'),
  ])
  if (overviewRes.status === 'fulfilled') {
    overview.value = overviewRes.value.data.data
  }
  if (bansRes.status === 'fulfilled') {
    recentBans.value = bansRes.value.data.data
  }
  loading.value = false
})

const banTrend = computed(() => overview.value?.trend.map(d => d.bans) ?? [])
const eventTrend = computed(() => overview.value?.trend.map(d => d.events) ?? [])
const trendTotal = computed(() =>
  overview.value ? overview.value.bans_last_24h + overview.value.events_last_24h : 0
)

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
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Security</h1>
        <p class="page-description">Bans, detection activity and tunable thresholds at a glance.</p>
      </div>
      <button class="btn btn-secondary" @click="router.push('/admin/security/events')">
        Detection Events
      </button>
    </div>

    <!-- Loading: skeleton stat tiles keep the layout while counts stream in. -->
    <div v-if="loading" class="stats-grid">
      <div v-for="n in 7" :key="n" class="stat-card">
        <div class="stat-header">
          <span class="skeleton skeleton-text" style="width: 60%;"></span>
        </div>
        <span class="skeleton skeleton-text" style="width: 40%; height: 24px;"></span>
      </div>
    </div>

    <template v-else-if="overview">
      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Active Bans</div>
            <div class="stat-icon stat-icon-danger">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10" /><line x1="4.93" y1="4.93" x2="19.07" y2="19.07" /></svg>
            </div>
          </div>
          <div class="stat-value">{{ overview.active_bans.toLocaleString() }}</div>
          <div class="stat-sub">{{ overview.automatic_bans }} automatic · {{ overview.manual_bans }} manual</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Automatic Bans</div>
            <div class="stat-icon stat-icon-warning">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" /><line x1="12" y1="9" x2="12" y2="13" /><line x1="12" y1="17" x2="12.01" y2="17" /></svg>
            </div>
          </div>
          <div class="stat-value">{{ overview.automatic_bans.toLocaleString() }}</div>
          <div class="stat-sub">Placed by the detection engine</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Manual Bans</div>
            <div class="stat-icon stat-icon-secondary">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" /><circle cx="12" cy="7" r="4" /></svg>
            </div>
          </div>
          <div class="stat-value">{{ overview.manual_bans.toLocaleString() }}</div>
          <div class="stat-sub">Placed by administrators</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Bans (24h)</div>
            <div class="stat-icon stat-icon-warning">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10" /><polyline points="12 6 12 12 16 14" /></svg>
            </div>
          </div>
          <div class="stat-value">{{ overview.bans_last_24h.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Events (24h)</div>
            <div class="stat-icon stat-icon-info">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="22 12 16 12 14 15 10 15 8 12 2 12" /><path d="M5.45 5.11L2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z" /></svg>
            </div>
          </div>
          <div class="stat-value">{{ overview.events_last_24h.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Port Scans Detected</div>
            <div class="stat-icon stat-icon-danger">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line x1="12" y1="16" x2="12.01" y2="16" /></svg>
            </div>
          </div>
          <div class="stat-value" :style="{ color: overview.port_scans_detected > 0 ? 'var(--danger-text)' : undefined }">{{ overview.port_scans_detected.toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Auth Attacks Detected</div>
            <div class="stat-icon stat-icon-danger">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2" ry="2" /><path d="M7 11V7a5 5 0 0 1 9.9-1" /></svg>
            </div>
          </div>
          <div class="stat-value" :style="{ color: overview.auth_attacks_detected > 0 ? 'var(--danger-text)' : undefined }">{{ overview.auth_attacks_detected.toLocaleString() }}</div>
        </div>
      </div>

      <!-- 14-day trend: two light sparklines, one per series. -->
      <div class="card" style="margin-bottom: 24px;">
        <div class="card-header">
          <h2>Last 14 Days</h2>
          <span class="trend-total">{{ trendTotal.toLocaleString() }} recorded events and bans</span>
        </div>
        <div class="card-body">
          <div v-if="overview.trend.length === 0" class="empty-state">
            <h3>No activity yet</h3>
            <p>Detection events and bans will chart here as they are recorded.</p>
          </div>
          <div v-else class="trend-rows">
            <div class="trend-row">
              <span class="trend-label">Bans</span>
              <Sparkline :values="banTrend" :width="480" :height="40" stroke="var(--danger-500)" />
              <span class="trend-sum">{{ banTrend.reduce((a, b) => a + b, 0).toLocaleString() }}</span>
            </div>
            <div class="trend-row">
              <span class="trend-label">Detection events</span>
              <Sparkline :values="eventTrend" :width="480" :height="40" stroke="var(--warning-500)" />
              <span class="trend-sum">{{ eventTrend.reduce((a, b) => a + b, 0).toLocaleString() }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Recent bans -->
      <div class="card">
        <div class="card-header">
          <h2>Recent Bans</h2>
          <button class="btn btn-sm btn-secondary" @click="router.push('/admin/security/blocked')">
            View all
          </button>
        </div>
        <div v-if="recentBans.length === 0" class="empty-state">
          <h3>No active bans</h3>
          <p>Addresses that trip the detection engine will appear here.</p>
        </div>
        <div v-else class="card-body">
          <div class="table-wrapper">
            <table class="table">
              <thead>
                <tr>
                  <th>IP Address</th>
                  <th>Reason</th>
                  <th>Type</th>
                  <th>Blocked</th>
                  <th>Expires</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="ban in recentBans" :key="ban.id" class="row-clickable" @click="router.push('/admin/security/blocked')">
                  <td><code>{{ ban.ip_address }}</code></td>
                  <td><span :class="reasonBadgeClass(ban.reason_code)">{{ ban.reason_code }}</span></td>
                  <td><span :class="typeBadgeClass(ban)">{{ ban.ban_type }}</span></td>
                  <td style="white-space: nowrap;"><span :title="formatDate(ban.created_at)">{{ formatDate(ban.created_at) }}</span></td>
                  <td><span :class="expiryBadge(ban).cls">{{ expiryBadge(ban).text }}</span></td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </template>

    <div v-else class="card">
      <div class="empty-state">
        <h3>Security overview unavailable</h3>
        <p>The overview counts could not be loaded. Check the server logs and retry.</p>
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

.trend-total {
  font-size: 12px;
  color: var(--text-muted);
}

.trend-rows {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.trend-row {
  display: flex;
  align-items: center;
  gap: 16px;
}

.trend-label {
  width: 130px;
  flex-shrink: 0;
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-muted);
}

.trend-row svg {
  flex: 1;
  min-width: 0;
}

.trend-sum {
  width: 64px;
  flex-shrink: 0;
  text-align: right;
  font-size: 13px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  color: var(--text-secondary);
}

@media (max-width: 640px) {
  .trend-row {
    flex-wrap: wrap;
    min-width: 0;
  }

  /* Keep flex-basis 0% here: a 100% basis on an <svg> with an intrinsic
     width attribute makes its min-content contribution the attribute width,
     which stretches the whole card chain past the mobile viewport. With
     basis 0 + grow the sparkline takes its own full line without forcing
     width. */
  .trend-row svg {
    order: 3;
  }
}

/* IPs and reason codes are technical identifiers — set them in the mono face. */
td code {
  font-family: var(--font-mono);
  font-size: 12.5px;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  padding: 1px 6px;
}
</style>
