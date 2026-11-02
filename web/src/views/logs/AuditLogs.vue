<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { logsApi } from '../../api/logs'
import type { AuditRow } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import { usePagination } from '../../composables/usePagination'
import { apiMessage } from '../../composables/apiError'
import { useNotificationStore } from '../../stores/notification'
import { useAuthStore } from '../../stores/auth'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const auth = useAuthStore()

const rows = ref<AuditRow[]>([])
const loading = ref(true)
const loadError = ref('')

const TEXT_FILTERS = [
  { key: 'action', label: 'Action', placeholder: 'e.g. member.invited' },
  { key: 'actor', label: 'Actor', placeholder: 'Name or ID' },
  { key: 'target_id', label: 'Target ID', placeholder: 'e.g. 42' },
  { key: 'ip', label: 'IP', placeholder: 'e.g. 10.0.0.1' },
  { key: 'from', label: 'Date From', placeholder: 'YYYY-MM-DD' },
  { key: 'to', label: 'Date To', placeholder: 'YYYY-MM-DD' },
] as const

type FilterKey = (typeof TEXT_FILTERS)[number]['key']

const OUTCOMES = ['success', 'failure', 'denied']

function queryValue(key: string): string {
  const v = route.query[key]
  return typeof v === 'string' ? v : ''
}

const filters = ref<Record<FilterKey, string>>(
  Object.fromEntries(TEXT_FILTERS.map(f => [f.key, queryValue(f.key)])) as Record<FilterKey, string>
)
const outcome = ref(queryValue('outcome'))
const openFilter = ref<FilterKey | 'outcome' | null>(null)
const draft = ref('')

// Scope pills: the workspace view is the default; spanning the whole
// deployment is a dashboard-admin capability (the API refuses anyone else).
const scope = ref(queryValue('scope') === 'deployment' && auth.isAdmin ? 'deployment' : 'workspace')

function openPopover(key: FilterKey | 'outcome') {
  openFilter.value = key
  draft.value = key === 'outcome' ? outcome.value : filters.value[key]
}

function cancelPopover() {
  openFilter.value = null
}

function applyPopover() {
  if (!openFilter.value) return
  if (openFilter.value === 'outcome') {
    outcome.value = draft.value.trim().toLowerCase()
  } else {
    filters.value[openFilter.value] = draft.value.trim()
  }
  openFilter.value = null
  apply()
}

const hasActiveFilters = computed(
  () => !!outcome.value || TEXT_FILTERS.some(f => filters.value[f.key]) || scope.value === 'deployment'
)

function chipValue(key: FilterKey | 'outcome'): string {
  return key === 'outcome' ? outcome.value : filters.value[key]
}

function apply() {
  const query: Record<string, string> = {}
  for (const f of TEXT_FILTERS) {
    const v = filters.value[f.key]
    if (v) query[f.key] = v
  }
  if (outcome.value) query.outcome = outcome.value
  // workspace is the default; only remember the admin's wider view.
  if (scope.value === 'deployment' && auth.isAdmin) query.scope = 'deployment'
  router.replace({ query })
  load(0)
}

function setScope(next: 'workspace' | 'deployment') {
  if (next === 'deployment' && !auth.isAdmin) return
  scope.value = next
  apply()
}

function clearAll() {
  for (const f of TEXT_FILTERS) filters.value[f.key] = ''
  outcome.value = ''
  scope.value = 'workspace'
  apply()
}

async function load(page: number) {
  loading.value = true
  loadError.value = ''
  try {
    const res = await logsApi.listAudit({
      page,
      size: pageable.value.size,
      action: filters.value.action || undefined,
      outcome: outcome.value || undefined,
      actor: filters.value.actor || undefined,
      target_id: filters.value.target_id || undefined,
      ip: filters.value.ip || undefined,
      from: filters.value.from || undefined,
      to: filters.value.to || undefined,
      scope: scope.value === 'deployment' && auth.isAdmin ? 'deployment' : undefined,
    })
    rows.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    loadError.value = apiMessage(e, 'Failed to load audit log')
    notify.error(loadError.value)
  } finally {
    loading.value = false
  }
}

const { pageable, goToPage } = usePagination(load)

function refresh() {
  load(pageable.value.current_page)
}

// CSV export: open the export endpoint with the same filters and scope — the
// session cookie travels with the navigation and the server streams the file.
function exportCsv() {
  const url = logsApi.getAuditExportUrl({
    action: filters.value.action || undefined,
    outcome: outcome.value || undefined,
    actor: filters.value.actor || undefined,
    target_id: filters.value.target_id || undefined,
    ip: filters.value.ip || undefined,
    from: filters.value.from || undefined,
    to: filters.value.to || undefined,
    scope: scope.value === 'deployment' && auth.isAdmin ? 'deployment' : undefined,
  })
  window.open(url, '_blank')
}

function closePopover(e: MouseEvent) {
  if (!openFilter.value) return
  if ((e.target as Element)?.closest?.('.chip-popover')) return
  openFilter.value = null
}
onMounted(() => document.addEventListener('click', closePopover))
onBeforeUnmount(() => document.removeEventListener('click', closePopover))

function outcomeBadgeClass(outcome?: string) {
  switch (outcome) {
    case 'success': return 'badge badge-success'
    case 'denied': return 'badge badge-warning'
    case 'failure': return 'badge badge-danger'
    default: return 'badge badge-neutral'
  }
}

function truncate(text: string, max = 16): string {
  if (!text) return ''
  return text.length > max ? text.slice(0, max) + '…' : text
}

function formatDate(date: string) {
  return new Date(date).toLocaleString(undefined, { timeZoneName: 'short' })
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Audit Log</h1>
        <p class="page-description">Who did what, to which resource, and how it ended — filterable and exportable as CSV.</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary" :disabled="loading" @click="refresh">
          <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>
          Refresh
        </button>
        <button class="btn btn-primary" @click="exportCsv">
          <span class="mdi mdi-download"></span>
          Export CSV
        </button>
      </div>
    </div>

    <div class="card">
      <div class="card-body filters">
        <!-- Scope pills: workspace is the default view; deployment is admin-only. -->
        <div class="scope-row" role="group" aria-label="Audit scope">
          <button
            type="button"
            class="scope-pill"
            :class="{ 'scope-pill--active': scope === 'workspace' }"
            @click="setScope('workspace')"
          >
            <span class="mdi mdi-briefcase-outline"></span>
            This Workspace
          </button>
          <button
            v-if="auth.isAdmin"
            type="button"
            class="scope-pill"
            :class="{ 'scope-pill--active': scope === 'deployment' }"
            @click="setScope('deployment')"
          >
            <span class="mdi mdi-earth"></span>
            Whole deployment
          </button>
        </div>

        <div class="chips-row">
          <div v-for="f in TEXT_FILTERS" :key="f.key" class="chip-wrap">
            <button
              type="button"
              class="filter-chip"
              :class="{ 'filter-chip--active': filters[f.key] }"
              @click="openFilter === f.key ? cancelPopover() : openPopover(f.key)"
            >
              <span class="mdi mdi-filter-variant"></span>
              <span class="filter-chip__label">{{ f.label }}</span>
              <span v-if="filters[f.key]" class="filter-chip__value" :title="filters[f.key]">{{ truncate(filters[f.key], 18) }}</span>
            </button>
            <div v-if="openFilter === f.key" class="chip-popover" @click.stop>
              <label class="form-label">{{ f.label }}</label>
              <!-- Date bounds are dates, everything else is free text. -->
              <input
                v-if="f.key === 'from' || f.key === 'to'"
                v-model="draft"
                type="date"
                class="form-input"
              />
              <input
                v-else
                v-model="draft"
                type="text"
                class="form-input"
                :placeholder="f.placeholder"
                @keyup.enter="applyPopover"
              />
              <div class="chip-popover__actions">
                <button type="button" class="btn btn-secondary btn-sm" @click="cancelPopover">Cancel</button>
                <button type="button" class="btn btn-primary btn-sm" @click="applyPopover">Apply</button>
              </div>
            </div>
          </div>

          <div class="chip-wrap">
            <button
              type="button"
              class="filter-chip"
              :class="{ 'filter-chip--active': outcome }"
              @click="openFilter === 'outcome' ? cancelPopover() : openPopover('outcome')"
            >
              <span class="mdi mdi-filter-variant"></span>
              <span class="filter-chip__label">Outcome</span>
              <span v-if="outcome" class="filter-chip__value">{{ outcome }}</span>
            </button>
            <div v-if="openFilter === 'outcome'" class="chip-popover" @click.stop>
              <label class="form-label">Outcome</label>
              <select v-model="draft" class="form-select">
                <option value="">Any outcome</option>
                <option v-for="o in OUTCOMES" :key="o" :value="o">{{ o }}</option>
              </select>
              <div class="chip-popover__actions">
                <button type="button" class="btn btn-secondary btn-sm" @click="cancelPopover">Cancel</button>
                <button type="button" class="btn btn-primary btn-sm" @click="applyPopover">Apply</button>
              </div>
            </div>
          </div>

          <button v-if="hasActiveFilters" type="button" class="filter-chip filter-chip--clear" @click="clearAll">
            <span class="mdi mdi-close"></span>
            Clear All
          </button>
        </div>

        <div v-if="loadError" class="app-banner app-banner--danger">
          <span class="app-banner-content">{{ loadError }}</span>
          <button class="btn btn-secondary btn-sm" @click="refresh">Retry</button>
        </div>
      </div>

      <!-- Loading: skeleton rows preserve the table layout while data streams in. -->
      <div v-if="loading" class="card-body">
        <table class="table">
          <thead>
            <tr><th>When</th><th>Actor</th><th>Action</th><th>Target</th><th>Outcome</th><th>IP</th></tr>
          </thead>
          <tbody>
            <tr v-for="n in 8" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 140px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 120px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 110px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 90px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 70px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 100px;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="rows.length === 0" class="empty-state">
        <h3>No audit events</h3>
        <p v-if="hasActiveFilters">No events match the current filters. Try adjusting or clearing them.</p>
        <p>Workspace activity — member changes, server, webhook and API key updates — will appear here.</p>
      </div>

      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Actor</th>
                <th>Action</th>
                <th>Target</th>
                <th>Outcome</th>
                <th>IP</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="event in rows" :key="event.id">
                <td style="white-space: nowrap">{{ formatDate(event.created_at) }}</td>
                <td class="cell-mono" :title="event.actor_id ? `Actor #${event.actor_id}` : ''">{{ event.actor_name || '—' }}</td>
                <td><code class="action-code">{{ event.type }}</code></td>
                <td>
                  <span v-if="event.target_type || event.target_id" class="target-cell" :title="`${event.target_type || ''} ${event.target_id || ''}`.trim()">
                    <span v-if="event.target_type" class="badge badge-neutral">{{ event.target_type }}</span>
                    <span v-if="event.target_id" class="cell-mono">{{ event.target_id }}</span>
                  </span>
                  <span v-else class="text-muted">—</span>
                </td>
                <td><span :class="outcomeBadgeClass(event.outcome)">{{ event.outcome || '—' }}</span></td>
                <td><code v-if="event.client_ip" class="ip-code">{{ event.client_ip }}</code><span v-else class="text-muted">—</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
      </template>
    </div>
  </div>
</template>

<style scoped>
.header-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.filters {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.scope-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.scope-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  font-size: 13px;
  font-weight: 500;
  border-radius: 999px;
  border: 1px solid var(--border-primary);
  background: var(--bg-tertiary);
  color: var(--text-secondary);
  cursor: pointer;
  transition: background var(--transition), border-color var(--transition), color var(--transition);
}

.scope-pill .mdi {
  font-size: 15px;
}

.scope-pill:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.scope-pill--active {
  background: var(--primary-600);
  border-color: var(--primary-600);
  color: var(--text-on-primary);
}

.scope-pill--active:hover {
  background: var(--primary-600);
  color: var(--text-on-primary);
}

.chips-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.chip-wrap {
  position: relative;
}

.filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 260px;
  padding: 5px 12px;
  font-size: 13px;
  border-radius: 999px;
  border: 1px dashed var(--border-primary);
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  transition: background var(--transition), border-color var(--transition), color var(--transition);
}

.filter-chip .mdi {
  font-size: 14px;
}

.filter-chip:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.filter-chip--active {
  border-style: solid;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
}

.filter-chip__label {
  white-space: nowrap;
}

.filter-chip__value {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--primary-500);
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.filter-chip--clear {
  border-style: solid;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
}

.chip-popover {
  position: absolute;
  top: calc(100% + 6px);
  left: 0;
  z-index: 60;
  width: 240px;
  max-width: calc(100vw - 48px);
  padding: 14px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.chip-popover .form-label {
  margin: 0;
}

.chip-popover__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.cell-mono {
  font-family: var(--font-mono);
  font-size: 13px;
}

/* Action types and IPs render as quiet mono pills, like the workspace audit log. */
.action-code,
.ip-code {
  display: inline-block;
  font-family: var(--font-mono);
  font-size: 12px;
  font-weight: 500;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: 9999px;
  padding: 1px 8px;
  white-space: nowrap;
}

.target-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 220px;
  overflow: hidden;
}

.table-wrapper table {
  min-width: 680px;
}
</style>
