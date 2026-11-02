<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { logsApi } from '../../api/logs'
import type { OutgoingDeliveryRow, DeliveryAttemptRow } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import { usePagination } from '../../composables/usePagination'
import { useModalSafeClose } from '../../composables/useModalSafeClose'
import { apiMessage } from '../../composables/apiError'
import { useNotificationStore } from '../../stores/notification'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()

const rows = ref<OutgoingDeliveryRow[]>([])
const loading = ref(true)
const loadError = ref('')

const STATUS_OPTIONS = ['success', 'failed']

function queryValue(key: string): string {
  const v = route.query[key]
  return typeof v === 'string' ? v : ''
}

const webhookId = ref(queryValue('webhook_id'))
const status = ref(queryValue('status'))
const openFilter = ref<'webhook_id' | 'status' | null>(null)
const draft = ref('')

function openPopover(key: 'webhook_id' | 'status') {
  openFilter.value = key
  draft.value = key === 'webhook_id' ? webhookId.value : status.value
}

function cancelPopover() {
  openFilter.value = null
}

function applyPopover() {
  if (!openFilter.value) return
  if (openFilter.value === 'webhook_id') {
    webhookId.value = draft.value.trim()
  } else {
    status.value = draft.value.trim().toLowerCase()
  }
  openFilter.value = null
  apply()
}

const hasActiveFilters = computed(() => !!webhookId.value || !!status.value)

function apply() {
  const query: Record<string, string> = {}
  if (webhookId.value) query.webhook_id = webhookId.value
  if (status.value) query.status = status.value
  router.replace({ query })
  load(0)
}

function clearAll() {
  webhookId.value = ''
  status.value = ''
  apply()
}

async function load(page: number) {
  loading.value = true
  loadError.value = ''
  try {
    const res = await logsApi.listOutgoing({
      page,
      size: pageable.value.size,
      webhook_id: webhookId.value || undefined,
      status: status.value || undefined,
    })
    rows.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    loadError.value = apiMessage(e, 'Failed to load outgoing webhooks')
    notify.error(loadError.value)
  } finally {
    loading.value = false
  }
}

const { pageable, goToPage } = usePagination(load)

function refresh() {
  load(pageable.value.current_page)
}

function closePopover(e: MouseEvent) {
  if (!openFilter.value) return
  if ((e.target as Element)?.closest?.('.chip-popover')) return
  openFilter.value = null
}
onMounted(() => document.addEventListener('click', closePopover))
onBeforeUnmount(() => document.removeEventListener('click', closePopover))

// ---- Detail modal: delivery info + every recorded HTTP attempt ----
const selected = ref<OutgoingDeliveryRow | null>(null)
const attempts = ref<DeliveryAttemptRow[]>([])
const attemptsLoading = ref(false)

async function openDetail(row: OutgoingDeliveryRow) {
  selected.value = row
  attempts.value = []
  attemptsLoading.value = true
  try {
    const res = await logsApi.getOutgoingAttempts(row.id)
    attempts.value = res.data.data || []
  } catch (e) {
    notify.error(apiMessage(e, 'Failed to load delivery attempts'))
  } finally {
    attemptsLoading.value = false
  }
}

function closeDetail() {
  selected.value = null
  attempts.value = []
}

const { watchClickStart, confirmClickEnd } = useModalSafeClose(closeDetail)

function statusBadgeClass(status: string) {
  return status === 'success' ? 'badge badge-success' : 'badge badge-danger'
}

function truncate(text: string | undefined, max = 40): string {
  if (!text) return ''
  return text.length > max ? text.slice(0, max) + '…' : text
}

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Outgoing Webhooks</h1>
        <p class="page-description">Delivery history of platform events pushed to your webhooks, with every HTTP attempt.</p>
      </div>
      <button class="btn btn-secondary" :disabled="loading" @click="refresh">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>
        Refresh
      </button>
    </div>

    <div class="card">
      <div class="card-body filters">
        <div class="chips-row">
          <div class="chip-wrap">
            <button
              type="button"
              class="filter-chip"
              :class="{ 'filter-chip--active': webhookId }"
              @click="openFilter === 'webhook_id' ? cancelPopover() : openPopover('webhook_id')"
            >
              <span class="mdi mdi-filter-variant"></span>
              <span class="filter-chip__label">Webhook</span>
              <span v-if="webhookId" class="filter-chip__value">#{{ webhookId }}</span>
            </button>
            <div v-if="openFilter === 'webhook_id'" class="chip-popover" @click.stop>
              <label class="form-label">Webhook ID</label>
              <input
                v-model="draft"
                type="text"
                class="form-input"
                placeholder="e.g. 4"
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
              :class="{ 'filter-chip--active': status }"
              @click="openFilter === 'status' ? cancelPopover() : openPopover('status')"
            >
              <span class="mdi mdi-filter-variant"></span>
              <span class="filter-chip__label">Status</span>
              <span v-if="status" class="filter-chip__value">{{ status }}</span>
            </button>
            <div v-if="openFilter === 'status'" class="chip-popover" @click.stop>
              <label class="form-label">Delivery status</label>
              <select v-model="draft" class="form-select">
                <option value="">Any status</option>
                <option v-for="s in STATUS_OPTIONS" :key="s" :value="s">{{ s }}</option>
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
            <tr><th>Webhook</th><th>Event</th><th>Status</th><th>HTTP Code</th><th>Attempts</th><th>Created</th><th>Last Error</th></tr>
          </thead>
          <tbody>
            <tr v-for="n in 8" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 40px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 110px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 70px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 40px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 30px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 130px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 150px;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="rows.length === 0" class="empty-state">
        <h3>No deliveries yet</h3>
        <p v-if="hasActiveFilters">No deliveries match the current filters. Try adjusting or clearing them.</p>
        <p>Webhook delivery attempts will appear here.</p>
      </div>

      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Webhook</th>
                <th>Event</th>
                <th>Status</th>
                <th>HTTP Code</th>
                <th>Attempts</th>
                <th>Created</th>
                <th>Last Error</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in rows" :key="row.id" class="clickable-row" :title="'Delivery #' + row.id" @click="openDetail(row)">
                <td class="cell-mono">#{{ row.webhook_id }}</td>
                <td><span class="badge badge-neutral type-mono">{{ row.event }}</span></td>
                <td><span :class="statusBadgeClass(row.status)">{{ row.status }}</span></td>
                <td>{{ row.http_status_code || '-' }}</td>
                <td>{{ row.attempt_count }}</td>
                <td style="white-space: nowrap">{{ formatDate(row.created_at) }}</td>
                <td class="error-cell" :title="row.error_message || ''">{{ truncate(row.error_message) || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
      </template>
    </div>

    <!-- Delivery detail modal -->
    <div
      v-if="selected"
      class="modal-overlay"
      @mousedown="watchClickStart"
      @mouseup="confirmClickEnd"
    >
      <div class="modal" style="max-width: 760px;">
        <div class="modal-header">
          <h3>Delivery #{{ selected.id }}</h3>
          <button class="modal-close" type="button" aria-label="Close" @click="closeDetail">×</button>
        </div>
        <div class="modal-body">
          <table class="detail-table">
            <tbody>
              <tr>
                <td>Webhook</td>
                <td class="cell-mono">#{{ selected.webhook_id }}</td>
              </tr>
              <tr>
                <td>Event</td>
                <td><span class="badge badge-neutral type-mono">{{ selected.event }}</span></td>
              </tr>
              <tr>
                <td>Status</td>
                <td><span :class="statusBadgeClass(selected.status)">{{ selected.status }}</span></td>
              </tr>
              <tr>
                <td>HTTP Code</td>
                <td>{{ selected.http_status_code || '-' }}</td>
              </tr>
              <tr>
                <td>Recorded Attempt</td>
                <td>{{ selected.attempt }}</td>
              </tr>
              <tr>
                <td>Created</td>
                <td>{{ formatDate(selected.created_at) }}</td>
              </tr>
              <tr v-if="selected.error_message">
                <td>Error</td>
                <td style="color: var(--danger-text)">{{ selected.error_message }}</td>
              </tr>
            </tbody>
          </table>

          <p class="attempts-label">Attempts</p>
          <div v-if="attemptsLoading" class="loading-page">
            <div class="spinner"></div>
          </div>
          <div v-else-if="attempts.length === 0" class="empty-state">
            <p>No recorded attempts for this delivery.</p>
          </div>
          <div v-else class="table-wrapper attempts-wrapper">
            <table>
              <thead>
                <tr>
                  <th>#</th>
                  <th>Status Code</th>
                  <th>Response</th>
                  <th>Error</th>
                  <th>Time</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="a in attempts" :key="a.id">
                  <td>{{ a.attempt }}</td>
                  <td :class="a.status_code >= 200 && a.status_code < 400 ? 'code-ok' : 'code-bad'">{{ a.status_code || '-' }}</td>
                  <td><pre class="attempt-body">{{ truncate(a.response_body, 160) || '-' }}</pre></td>
                  <td class="error-cell" :title="a.error || ''">{{ truncate(a.error) || '-' }}</td>
                  <td style="white-space: nowrap">{{ formatDate(a.created_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.filters {
  display: flex;
  flex-direction: column;
  gap: 12px;
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

.clickable-row {
  cursor: pointer;
}

.clickable-row:hover {
  background: var(--bg-hover);
}

.cell-mono {
  font-family: var(--font-mono);
  font-size: 13px;
}

.type-mono {
  font-family: var(--font-mono);
  font-size: 11px;
}

.error-cell {
  color: var(--danger-text);
  max-width: 240px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-table {
  width: 100%;
  margin-bottom: 14px;
}

.detail-table td {
  padding: 4px 8px 4px 0;
  vertical-align: top;
}

.detail-table td:first-child {
  font-weight: 600;
  width: 150px;
  white-space: nowrap;
}

.attempts-label {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-tertiary);
  margin: 0 0 8px;
}

.attempts-wrapper {
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
}

.attempt-body {
  margin: 0;
  font-family: var(--font-mono);
  font-size: 11.5px;
  white-space: pre-wrap;
  word-break: break-word;
  max-width: 260px;
}

.code-ok {
  color: var(--success-text);
  font-variant-numeric: tabular-nums;
}

.code-bad {
  color: var(--danger-text);
  font-variant-numeric: tabular-nums;
}

.modal-close {
  background: none;
  border: none;
  font-size: 22px;
  line-height: 1;
  color: var(--text-muted);
  cursor: pointer;
  padding: 2px 6px;
  border-radius: var(--radius-sm);
}

.modal-close:hover {
  color: var(--text-primary);
  background: var(--bg-hover);
}

.table-wrapper table {
  min-width: 640px;
}
</style>
