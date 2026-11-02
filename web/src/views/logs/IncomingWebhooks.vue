<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { logsApi } from '../../api/logs'
import type { IncomingWebhookEventRow, IncomingWebhookEventDetail } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import { usePagination } from '../../composables/usePagination'
import { useModalSafeClose } from '../../composables/useModalSafeClose'
import { apiMessage } from '../../composables/apiError'
import { useNotificationStore } from '../../stores/notification'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()

const rows = ref<IncomingWebhookEventRow[]>([])
const loading = ref(true)
const loadError = ref('')

const TEXT_FILTERS = [
  { key: 'source', label: 'Source', placeholder: 'bounce, tracking, smtp…' },
  { key: 'type', label: 'Type', placeholder: 'bounced, opened…' },
  { key: 'recipient', label: 'Recipient Email', placeholder: 'recipient@example.com' },
  { key: 'email_uuid', label: 'Email UUID', placeholder: 'Message UUID' },
] as const

type FilterKey = (typeof TEXT_FILTERS)[number]['key']

function queryValue(key: string): string {
  const v = route.query[key]
  return typeof v === 'string' ? v : ''
}

const filters = ref<Record<FilterKey, string>>(
  Object.fromEntries(TEXT_FILTERS.map(f => [f.key, queryValue(f.key)])) as Record<FilterKey, string>
)
const openFilter = ref<FilterKey | null>(null)
const draft = ref('')

function openPopover(key: FilterKey) {
  openFilter.value = key
  draft.value = filters.value[key]
}

function cancelPopover() {
  openFilter.value = null
}

function applyPopover() {
  if (!openFilter.value) return
  filters.value[openFilter.value] = draft.value.trim()
  openFilter.value = null
  apply()
}

const hasActiveFilters = computed(() => TEXT_FILTERS.some(f => filters.value[f.key]))

function apply() {
  const query: Record<string, string> = {}
  for (const f of TEXT_FILTERS) {
    const v = filters.value[f.key]
    if (v) query[f.key] = v
  }
  router.replace({ query })
  load(0)
}

function clearAll() {
  for (const f of TEXT_FILTERS) filters.value[f.key] = ''
  apply()
}

async function load(page: number) {
  loading.value = true
  loadError.value = ''
  try {
    const res = await logsApi.listIncoming({
      page,
      size: pageable.value.size,
      source: filters.value.source || undefined,
      type: filters.value.type || undefined,
      recipient: filters.value.recipient || undefined,
      email_uuid: filters.value.email_uuid || undefined,
    })
    rows.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    loadError.value = apiMessage(e, 'Failed to load incoming webhooks')
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

// ---- Detail modal: the list never carries payloads; fetch on open ----
const selected = ref<IncomingWebhookEventDetail | null>(null)
const detailLoading = ref(false)

async function openDetail(row: IncomingWebhookEventRow) {
  selected.value = { ...row, raw_payload: '' }
  detailLoading.value = true
  try {
    const res = await logsApi.getIncoming(row.id)
    selected.value = res.data.data
  } catch (e) {
    notify.error(apiMessage(e, 'Failed to load webhook payload'))
    selected.value = null
  } finally {
    detailLoading.value = false
  }
}

function closeDetail() {
  selected.value = null
}

const { watchClickStart, confirmClickEnd } = useModalSafeClose(closeDetail)

function truncate(text: string | undefined, max = 14): string {
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
        <h1>Incoming Webhooks</h1>
        <p class="page-description">Raw provider payloads captured before processing — bounce and tracking callbacks as they arrived.</p>
      </div>
      <button class="btn btn-secondary" :disabled="loading" @click="refresh">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>
        Refresh
      </button>
    </div>

    <div class="card">
      <div class="card-body filters">
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
              <input
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
            <tr><th>Source</th><th>Type</th><th>Recipient</th><th>Email UUID</th><th>Message ID</th><th>Created At</th></tr>
          </thead>
          <tbody>
            <tr v-for="n in 8" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 70px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 80px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 160px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 100px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 110px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 130px;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="rows.length === 0" class="empty-state">
        <h3>No incoming webhooks</h3>
        <p v-if="hasActiveFilters">No events match the current filters. Try adjusting or clearing them.</p>
        <p>Bounce and tracking callbacks received by this platform will appear here.</p>
      </div>

      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Source</th>
                <th>Type</th>
                <th>Recipient</th>
                <th>Email UUID</th>
                <th>Message ID</th>
                <th>Created At</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in rows" :key="row.id" class="clickable-row" @click="openDetail(row)">
                <td><span class="badge badge-info">{{ row.source }}</span></td>
                <td><span class="badge badge-neutral type-mono">{{ row.type }}</span></td>
                <td class="cell-mono">{{ row.recipient || '—' }}</td>
                <td>
                  <router-link
                    v-if="row.email_uuid"
                    class="uuid-link"
                    :to="`/logs/messages/${row.email_uuid}`"
                    :title="`Open message ${row.email_uuid}`"
                    @click.stop
                  >{{ truncate(row.email_uuid) }}</router-link>
                  <span v-else class="text-muted">—</span>
                </td>
                <td class="cell-mono" :title="row.message_id || ''">{{ truncate(row.message_id, 20) || '—' }}</td>
                <td style="white-space: nowrap">{{ formatDate(row.created_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
      </template>
    </div>

    <!-- Raw payload modal -->
    <div
      v-if="selected"
      class="modal-overlay"
      @mousedown="watchClickStart"
      @mouseup="confirmClickEnd"
    >
      <div class="modal" style="max-width: 720px;">
        <div class="modal-header">
          <h3>
            Webhook payload
            <span class="badge badge-info" style="margin-left: 8px">{{ selected.source }}</span>
            <span class="badge badge-neutral" style="margin-left: 4px">{{ selected.type }}</span>
          </h3>
          <button class="modal-close" type="button" aria-label="Close" @click="closeDetail">×</button>
        </div>
        <div class="modal-body">
          <table class="detail-table">
            <tbody>
              <tr v-if="selected.recipient">
                <td>Recipient</td>
                <td class="cell-mono">{{ selected.recipient }}</td>
              </tr>
              <tr v-if="selected.email_uuid">
                <td>Email UUID</td>
                <td>
                  <router-link class="uuid-link" :to="`/logs/messages/${selected.email_uuid}`" @click="closeDetail">
                    {{ selected.email_uuid }}
                  </router-link>
                </td>
              </tr>
              <tr v-if="selected.message_id">
                <td>Message ID</td>
                <td class="cell-mono">{{ selected.message_id }}</td>
              </tr>
              <tr>
                <td>Created</td>
                <td>{{ formatDate(selected.created_at) }}</td>
              </tr>
            </tbody>
          </table>

          <div v-if="selected.process_error" class="app-banner app-banner--warning process-error">
            <div class="app-banner-content">
              <p class="app-banner-title">Processing failed</p>
              <p class="app-banner-text">{{ selected.process_error }}</p>
            </div>
          </div>

          <div v-if="detailLoading" class="loading-page">
            <div class="spinner"></div>
          </div>
          <template v-else>
            <p class="payload-label">Raw payload</p>
            <pre class="code-block">{{ selected.raw_payload || '—' }}</pre>
          </template>
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
  width: 260px;
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

.uuid-link {
  font-family: var(--font-mono);
  font-size: 12px;
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
  width: 120px;
  white-space: nowrap;
}

.process-error {
  margin-bottom: 14px;
}

.payload-label {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-tertiary);
  margin: 0 0 8px;
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
