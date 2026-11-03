<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { automationsApi, automationApiError } from '../../api/automations'
import { subscriberListsApi } from '../../api/subscriberLists'
import { useNotificationStore } from '../../stores/notification'
import { useWorkspaceStore } from '../../stores/workspace'
import type { AutomationRow, AutomationStatsResponse, JourneyRow } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import { useConfirm } from '../../composables/useConfirm'
import { usePagination } from '../../composables/usePagination'
import StatCanvas from './components/StatCanvas.vue'
import ExecutionDrawer from './components/ExecutionDrawer.vue'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

const automationId = computed(() => {
  const raw = route.params.id
  const id = Number(Array.isArray(raw) ? raw[0] : raw)
  return Number.isFinite(id) && id > 0 ? id : 0
})

const loading = ref(true)
const notFound = ref(false)
const automation = ref<AutomationRow | null>(null)
const stats = ref<AutomationStatsResponse | null>(null)
const listNames = ref<Map<number, string>>(new Map())
const errorBanner = ref<{ message: string; validationErrors: string[] } | null>(null)

const busy = ref(false)

// --- Loading ---------------------------------------------------------------

async function load() {
  if (!automationId.value) {
    notFound.value = true
    loading.value = false
    return
  }
  loading.value = true
  errorBanner.value = null
  const [autoRes, statsRes, listsRes] = await Promise.allSettled([
    automationsApi.get(automationId.value),
    automationsApi.stats(automationId.value),
    subscriberListsApi.list(0, 100),
  ])
  if (autoRes.status === 'fulfilled') automation.value = autoRes.value.data.data
  else notFound.value = true
  if (statsRes.status === 'fulfilled') stats.value = statsRes.value.data.data
  if (listsRes.status === 'fulfilled') listNames.value = new Map(listsRes.value.data.data.map((l) => [l.id, l.name]))
  loading.value = false
  loadExecutions(pageable.value.current_page)
}

onMounted(load)

// --- Derived ---------------------------------------------------------------

function listName(listId: number | null | undefined): string {
  if (!listId) return '—'
  return listNames.value.get(listId) ?? `List #${listId}`
}

const EVENT_KIND_LABELS: Record<string, string> = {
  'list.subscribed': 'Joined list',
  'contact.created': 'Contact created',
  'contact.unsubscribed': 'Unsubscribed',
  'email.opened': 'Email opened',
  'email.clicked': 'Email clicked',
  'email.bounced': 'Email bounced',
  custom: 'Custom event',
}

function triggerLabel(): string {
  const cfg = automation.value?.trigger_config
  if (!cfg?.event_kind) return 'No trigger'
  const kind = EVENT_KIND_LABELS[cfg.event_kind] ?? cfg.event_kind
  if (cfg.event_kind === 'custom' && cfg.custom_event_name) return `${kind} · ${cfg.custom_event_name}`
  if (cfg.event_kind === 'list.subscribed') {
    const listId = cfg.list_id ?? automation.value?.list_id
    if (listId) return `${kind} · ${listName(listId)}`
  }
  return kind
}

const FREQUENCY_LABELS: Record<string, string> = {
  once: 'Once per contact',
  every_time: 'Every time',
}

function statusBadgeClass(status: string) {
  switch (status) {
    case 'live': case 'completed': return 'badge badge-success'
    case 'active': return 'badge badge-info'
    case 'paused': case 'processing': return 'badge badge-warning'
    case 'failed': return 'badge badge-danger'
    case 'exited': return 'badge badge-neutral'
    default: return 'badge badge-neutral'
  }
}

function statusLabel(status: string) {
  switch (status) {
    case 'live': return 'Live'
    case 'paused': return 'Paused'
    default: return 'Draft'
  }
}

function formatDate(value: string | null | undefined): string {
  if (!value) return '—'
  return new Date(value).toLocaleString()
}

function nodeName(id: string): string {
  const spec = automation.value?.nodes.find((n) => n.id === id)
  if (!spec) return id || '—'
  const labels: Record<string, string> = {
    trigger: 'Trigger', delay: 'Delay', email: 'Email', filter: 'Filter',
    ab_test: 'A/B Test', list_status: 'List Status', add_to_list: 'Add to List',
    remove_from_list: 'Remove from List', webhook: 'Webhook',
  }
  return `${labels[spec.type] ?? spec.type} · ${id}`
}

// --- Actions ----------------------------------------------------------------

function fail(e: any, fallback: string) {
  errorBanner.value = automationApiError(e, fallback)
}

function replaceRow(updated: AutomationRow) {
  automation.value = updated
}

async function activate() {
  if (!automation.value || busy.value) return
  busy.value = true
  errorBanner.value = null
  try {
    const res = await automationsApi.activate(automation.value.id)
    replaceRow(res.data.data)
    notify.success(`"${automation.value.name}" is live`)
  } catch (e) {
    fail(e, 'Failed to activate automation')
  } finally {
    busy.value = false
  }
}

async function pause() {
  if (!automation.value || busy.value) return
  busy.value = true
  errorBanner.value = null
  try {
    const res = await automationsApi.pause(automation.value.id)
    replaceRow(res.data.data)
    notify.success(`"${automation.value.name}" paused`)
  } catch (e) {
    fail(e, 'Failed to pause automation')
  } finally {
    busy.value = false
  }
}

async function duplicate() {
  if (!automation.value || busy.value) return
  busy.value = true
  errorBanner.value = null
  try {
    const res = await automationsApi.duplicate(automation.value.id)
    notify.success(`Duplicated as "${res.data.data.name}"`)
    router.push(`/automations/${res.data.data.id}`)
  } catch (e) {
    fail(e, 'Failed to duplicate automation')
  } finally {
    busy.value = false
  }
}

// --- Executions --------------------------------------------------------------

const executions = ref<JourneyRow[]>([])
const execLoading = ref(false)
const statusFilter = ref('')
const contactFilter = ref('')

let searchTimeout: ReturnType<typeof setTimeout> | null = null

async function loadExecutions(page: number) {
  if (!automationId.value) return
  execLoading.value = true
  try {
    const res = await automationsApi.executions(automationId.value, {
      page,
      size: pageable.value.size,
      status: statusFilter.value || undefined,
      contact: contactFilter.value.trim() || undefined,
    })
    executions.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    fail(e, 'Failed to load executions')
  } finally {
    execLoading.value = false
  }
}

const { pageable, goToPage } = usePagination(loadExecutions)

function applyExecFilters() {
  router.replace({ query: { ...route.query, page: undefined } })
  loadExecutions(0)
}

function onContactInput() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(applyExecFilters, 350)
}

function truncate(text: string | undefined, max = 60): string {
  if (!text) return ''
  return text.length > max ? `${text.slice(0, max)}…` : text
}

// --- Execution drawer ---------------------------------------------------------

const drawerJourney = ref<JourneyRow | null>(null)

function openExecution(journey: JourneyRow) {
  drawerJourney.value = journey
}

function closeDrawer() {
  drawerJourney.value = null
}
</script>

<template>
  <div>
    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <div v-else-if="notFound || !automation" class="empty-state">
      <h3>Automation not found</h3>
      <p>The automation you are looking for does not exist.</p>
      <button class="btn btn-secondary" @click="router.push('/automations')">Back to Automations</button>
    </div>

    <template v-else>
      <div class="page-header">
        <div>
          <h1>{{ automation.name }}</h1>
          <p class="page-description">
            <span :class="statusBadgeClass(automation.status)">{{ statusLabel(automation.status) }}</span>
            <span class="detail-meta">
              <span class="mdi mdi-bolt"></span> {{ triggerLabel() }}
              <span class="detail-meta__sep">·</span> {{ FREQUENCY_LABELS[automation.trigger_config?.frequency] ?? 'Once per contact' }}
              <span class="detail-meta__sep">·</span> {{ listName(automation.list_id) }}
            </span>
          </p>
        </div>
        <div v-if="wsStore.canEdit" class="detail-actions">
          <button
            v-if="automation.status !== 'live'"
            class="btn btn-primary"
            :disabled="busy"
            @click="activate"
          >
            Activate
          </button>
          <button
            v-if="automation.status === 'live'"
            class="btn btn-secondary"
            :disabled="busy"
            @click="pause"
          >
            Pause
          </button>
          <button class="btn btn-secondary" @click="router.push(`/automations/${automation.id}/edit`)">
            <span class="mdi mdi-pencil-outline"></span>
            Edit
          </button>
          <button class="btn btn-secondary" :disabled="busy" @click="duplicate">Duplicate</button>
        </div>
      </div>

      <div v-if="errorBanner" class="banner" role="alert">
        <div class="banner-title">
          <span class="mdi mdi-alert-circle-outline"></span>
          {{ errorBanner.message }}
        </div>
        <ul v-if="errorBanner.validationErrors.length" class="banner-list">
          <li v-for="(err, i) in errorBanner.validationErrors" :key="i">{{ err }}</li>
        </ul>
        <button class="banner-close" aria-label="Dismiss" @click="errorBanner = null">
          <span class="mdi mdi-close"></span>
        </button>
      </div>

      <div class="stats-grid">
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Enrolled</div>
            <div class="stat-icon stat-icon-secondary">
              <span class="mdi mdi-account-plus-outline"></span>
            </div>
          </div>
          <div class="stat-value">{{ (stats?.enrolled ?? automation.stats?.enrolled ?? 0).toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Completed</div>
            <div class="stat-icon stat-icon-success">
              <span class="mdi mdi-check-circle-outline"></span>
            </div>
          </div>
          <div class="stat-value">{{ (stats?.completed ?? automation.stats?.completed ?? 0).toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Exited</div>
            <div class="stat-icon stat-icon-info">
              <span class="mdi mdi-exit-run"></span>
            </div>
          </div>
          <div class="stat-value">{{ (stats?.exited ?? automation.stats?.exited ?? 0).toLocaleString() }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-header">
            <div class="stat-label">Failed</div>
            <div class="stat-icon stat-icon-danger">
              <span class="mdi mdi-alert-circle-outline"></span>
            </div>
          </div>
          <div class="stat-value" :style="(stats?.failed ?? automation.stats?.failed) ? 'color: var(--danger-text)' : undefined">
            {{ (stats?.failed ?? automation.stats?.failed ?? 0).toLocaleString() }}
          </div>
        </div>
      </div>

      <div class="card" style="margin-bottom: 24px">
        <div class="card-header">
          <h2>Flow</h2>
          <span class="flow-sub">Node run counts, live from the execution log</span>
        </div>
        <div class="card-body">
          <StatCanvas :nodes="automation.nodes ?? []" :node-stats="stats?.node_stats ?? []" />
        </div>
      </div>

      <div class="card">
        <div class="card-header">
          <h2>Executions</h2>
          <div class="exec-filters">
            <input
              v-model="contactFilter"
              class="form-input exec-filters__contact"
              placeholder="Search contact email…"
              @input="onContactInput"
              @keyup.enter="applyExecFilters"
            />
            <select v-model="statusFilter" class="form-select exec-filters__status" @change="applyExecFilters">
              <option value="">Any status</option>
              <option value="active">Active</option>
              <option value="completed">Completed</option>
              <option value="exited">Exited</option>
              <option value="failed">Failed</option>
            </select>
          </div>
        </div>

        <div v-if="execLoading" class="loading-page">
          <div class="spinner"></div>
        </div>

        <template v-else>
          <div v-if="executions.length === 0" class="empty-state">
            <h3>No executions yet</h3>
            <p>Contacts enroll here when the trigger fires{{ automation.status !== 'live' ? ' (the automation is not live)' : '' }}.</p>
          </div>
          <template v-else>
            <div class="table-wrapper">
              <table>
                <thead>
                  <tr>
                    <th>Contact</th>
                    <th>Status</th>
                    <th>Entered</th>
                    <th>Current Node</th>
                    <th>Completed</th>
                    <th>Error</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="journey in executions"
                    :key="journey.id"
                    style="cursor: pointer"
                    @click="openExecution(journey)"
                  >
                    <td class="cell-mono">{{ journey.email }}</td>
                    <td><span :class="statusBadgeClass(journey.status)">{{ journey.status }}</span></td>
                    <td>{{ formatDate(journey.entered_at) }}</td>
                    <td>{{ nodeName(journey.current_node_id) }}</td>
                    <td>{{ formatDate(journey.completed_at) }}</td>
                    <td class="cell-error" :title="journey.last_error">{{ truncate(journey.last_error) || '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <Pagination :pageable="pageable" @page="goToPage" />
          </template>
        </template>
      </div>
    </template>

    <ExecutionDrawer :automation-id="automationId" :journey="drawerJourney" @close="closeDrawer" />
    <ConfirmDialog />
  </div>
</template>

<style scoped>
.detail-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.detail-meta {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-left: 10px;
  color: var(--text-secondary);
}

.detail-meta__sep {
  color: var(--text-muted);
}

.page-description .badge {
  vertical-align: middle;
}

.flow-sub {
  font-size: 12.5px;
  color: var(--text-muted);
}

.stat-icon .mdi {
  font-size: 20px;
}

.stat-icon-success { color: var(--success-text); }
.stat-icon-info { color: var(--primary-600); }
.stat-icon-danger { color: var(--danger-text); }
.stat-icon-secondary { color: var(--text-tertiary); }

.exec-filters {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.exec-filters__contact {
  width: 220px;
  height: 36px;
  font-size: 13px;
}

.exec-filters__status {
  width: 150px;
  height: 36px;
  font-size: 13px;
}

.cell-mono {
  font-family: var(--font-mono);
  font-size: 13px;
}

.cell-error {
  color: var(--danger-text);
  font-size: 12.5px;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.banner {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 16px;
  padding: 12px 40px 12px 14px;
  border: 1px solid var(--danger-500);
  border-radius: var(--radius);
  background: var(--danger-50);
}

.banner-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--danger-text);
}

.banner-list {
  margin: 0;
  padding-left: 30px;
  font-size: 13px;
  color: var(--danger-text);
  line-height: 1.7;
}

.banner-close {
  position: absolute;
  top: 8px;
  right: 8px;
  display: inline-flex;
  padding: 4px;
  border: none;
  background: transparent;
  color: var(--danger-text);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

@media (max-width: 640px) {
  .page-header {
    flex-direction: column;
    align-items: flex-start;
    gap: 12px;
  }

  .detail-actions {
    width: 100%;
  }

  .exec-filters__contact {
    width: 100%;
  }
}
</style>
