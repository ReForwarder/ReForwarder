<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { logsApi } from '../../api/logs'
import type { MessageRow } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import { usePagination } from '../../composables/usePagination'
import { apiMessage } from '../../composables/apiError'
import { useNotificationStore } from '../../stores/notification'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()

const rows = ref<MessageRow[]>([])
const loading = ref(true)
const loadError = ref('')

// ---- Status chips: multi-select, toggleable, mirrored to ?status=a,b ----
const STATUS_CHIPS = [
  { value: 'sent', label: 'Sent', icon: 'mdi-send' },
  { value: 'delivered', label: 'Delivered', icon: 'mdi-check-circle' },
  { value: 'failed', label: 'Failed', icon: 'mdi-close-circle' },
  { value: 'opened', label: 'Opened', icon: 'mdi-eye' },
  { value: 'clicked', label: 'Clicked', icon: 'mdi-cursor-default-click' },
  { value: 'bounced', label: 'Bounced', icon: 'mdi-alert' },
  { value: 'complained', label: 'Complained', icon: 'mdi-flag-outline' },
  { value: 'unsubscribed', label: 'Unsubscribed', icon: 'mdi-email-off-outline' },
]

// ---- Second-line filter chips: each opens a small input with Apply ----
const TEXT_FILTERS = [
  { key: 'channel', label: 'Channel', placeholder: 'email' },
  { key: 'contact', label: 'Contact Email', placeholder: 'recipient@example.com' },
  { key: 'message_id', label: 'Message ID', placeholder: 'Message UUID' },
  { key: 'external_id', label: 'External ID', placeholder: 'External reference' },
  { key: 'list_id', label: 'List ID', placeholder: 'e.g. 3' },
  { key: 'template_id', label: 'Template ID', placeholder: 'Template name' },
  { key: 'block_id', label: 'Block ID', placeholder: 'e.g. 2' },
  { key: 'broadcast_id', label: 'Broadcast ID', placeholder: 'e.g. 7' },
] as const

type FilterKey = (typeof TEXT_FILTERS)[number]['key']

function queryValue(key: string): string {
  const v = route.query[key]
  return typeof v === 'string' ? v : ''
}

const statuses = ref<string[]>(queryValue('status').split(',').map(s => s.trim()).filter(Boolean))
const filters = ref<Record<FilterKey, string>>(
  Object.fromEntries(TEXT_FILTERS.map(f => [f.key, queryValue(f.key)])) as Record<FilterKey, string>
)

const openFilter = ref<FilterKey | null>(null)
const draft = ref('')

function toggleStatus(value: string) {
  statuses.value = statuses.value.includes(value)
    ? statuses.value.filter(s => s !== value)
    : [...statuses.value, value]
  apply()
}

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

const hasActiveFilters = computed(
  () => statuses.value.length > 0 || TEXT_FILTERS.some(f => filters.value[f.key])
)

// Push the current filters into the URL (so refresh and deep links restore
// them) and reload from the first page.
function apply() {
  const query: Record<string, string> = {}
  if (statuses.value.length) query.status = statuses.value.join(',')
  for (const f of TEXT_FILTERS) {
    const v = filters.value[f.key]
    if (v) query[f.key] = v
  }
  router.replace({ query })
  load(0)
}

function clearAll() {
  statuses.value = []
  for (const f of TEXT_FILTERS) filters.value[f.key] = ''
  apply()
}

async function load(page: number) {
  loading.value = true
  loadError.value = ''
  try {
    const res = await logsApi.listMessages({
      page,
      size: pageable.value.size,
      status: statuses.value,
      channel: filters.value.channel || undefined,
      contact: filters.value.contact || undefined,
      message_id: filters.value.message_id || undefined,
      external_id: filters.value.external_id || undefined,
      list_id: filters.value.list_id || undefined,
      template_id: filters.value.template_id || undefined,
      block_id: filters.value.block_id || undefined,
      broadcast_id: filters.value.broadcast_id || undefined,
    })
    rows.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    loadError.value = apiMessage(e, 'Failed to load message history')
    notify.error(loadError.value)
  } finally {
    loading.value = false
  }
}

const { pageable, goToPage } = usePagination(load)

function refresh() {
  load(pageable.value.current_page)
}

// Close the open filter popover when clicking anywhere outside of it.
function closePopover(e: MouseEvent) {
  if (!openFilter.value) return
  if ((e.target as Element)?.closest?.('.chip-popover')) return
  openFilter.value = null
}
onMounted(() => document.addEventListener('click', closePopover))
onBeforeUnmount(() => document.removeEventListener('click', closePopover))

// Timeline badges: one badge per recorded event, color-coded by type.
const EVENT_META: Record<string, { icon: string; cls: string }> = {
  sent: { icon: 'mdi-send', cls: 'badge-info' },
  delivered: { icon: 'mdi-check-circle', cls: 'badge-success' },
  failed: { icon: 'mdi-close-circle', cls: 'badge-danger' },
  opened: { icon: 'mdi-eye', cls: 'badge-warning' },
  clicked: { icon: 'mdi-cursor-default-click', cls: 'badge-accent' },
  bounced: { icon: 'mdi-alert', cls: 'badge-danger' },
  complained: { icon: 'mdi-flag-outline', cls: 'badge-danger' },
  unsubscribed: { icon: 'mdi-email-off-outline', cls: 'badge-neutral' },
}

function eventMeta(type: string) {
  return EVENT_META[type] || { icon: 'mdi-dots-horizontal', cls: 'badge-neutral' }
}

function statusBadgeClass(status: string) {
  switch (status) {
    case 'sent': return 'badge badge-secondary'
    case 'failed': return 'badge badge-danger'
    case 'pending': case 'queued': case 'processing': case 'scheduled': return 'badge badge-warning'
    case 'suppressed': return 'badge badge-neutral'
    default: return 'badge'
  }
}

// Ancillary per-row attributes rendered as small metadata chips; the timeline
// row carries no free-form JSON, so nothing parseable simply renders as —.
function metaChips(row: MessageRow): { label: string; title: string; cls: string }[] {
  const chips: { label: string; title: string; cls: string }[] = []
  if (row.provider) chips.push({ label: row.provider, title: `Provider: ${row.provider}`, cls: 'badge-info' })
  if (row.external_id) chips.push({ label: row.external_id, title: `External ID: ${row.external_id}`, cls: 'meta-mono' })
  if (row.smtp_hostname) chips.push({ label: row.smtp_hostname, title: `SMTP: ${row.smtp_hostname}`, cls: 'meta-mono' })
  return chips
}

function truncate(text: string, max = 12): string {
  if (!text) return ''
  return text.length > max ? text.slice(0, max) + '…' : text
}

function formatDate(date: string | null | undefined) {
  if (!date) return '-'
  return new Date(date).toLocaleString(undefined, { timeZoneName: 'short' })
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Message History</h1>
        <p class="page-description">Every outbound message with its delivery timeline — sent, opened, clicked, bounced.</p>
      </div>
      <button class="btn btn-secondary" :disabled="loading" @click="refresh">
        <span class="mdi" :class="loading ? 'mdi-loading mdi-spin' : 'mdi-refresh'"></span>
        Refresh
      </button>
    </div>

    <div class="card">
      <div class="card-body filters">
        <div class="chips-row" role="group" aria-label="Status filter">
          <button
            v-for="s in STATUS_CHIPS"
            :key="s.value"
            type="button"
            class="status-chip"
            :class="{ 'status-chip--active': statuses.includes(s.value) }"
            :aria-pressed="statuses.includes(s.value)"
            @click="toggleStatus(s.value)"
          >
            <span class="mdi" :class="s.icon"></span>
            {{ s.label }}
          </button>
        </div>

        <div class="chips-row chips-row--filters">
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
            <tr>
              <th>Contact Email</th><th>Message ID</th><th>Metadata</th><th>Template</th>
              <th>Broadcast</th><th>List</th><th>Events</th><th>Error</th><th>Created At</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in 8" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 150px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 90px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 70px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 80px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 60px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 40px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 110px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 90px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 120px;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="rows.length === 0" class="empty-state">
        <h3>No messages found</h3>
        <p v-if="hasActiveFilters">No messages match the current filters. Try adjusting or clearing them.</p>
        <p>Messages sent through the API, SMTP relay and campaigns will appear here.</p>
      </div>

      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Contact Email</th>
                <th>Message ID</th>
                <th>Metadata</th>
                <th>Template</th>
                <th>Broadcast</th>
                <th>List</th>
                <th>Events</th>
                <th>Error</th>
                <th>Created At</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in rows"
                :key="row.uuid"
                class="clickable-row"
                @click="router.push(`/logs/messages/${row.uuid}`)"
              >
                <td class="cell-mono">{{ row.recipient || '—' }}</td>
                <td class="cell-mono" :title="row.uuid">{{ truncate(row.uuid) }}</td>
                <td>
                  <template v-if="metaChips(row).length">
                    <span
                      v-for="chip in metaChips(row)"
                      :key="chip.title"
                      class="badge meta-chip"
                      :class="chip.cls"
                      :title="chip.title"
                    >{{ chip.label }}</span>
                  </template>
                  <span v-else class="text-muted">—</span>
                </td>
                <td>{{ row.template_name || '—' }}</td>
                <td :title="row.broadcast_name || ''">{{ row.broadcast_name || '—' }}</td>
                <td>{{ row.list_id ?? '—' }}</td>
                <td>
                  <div class="events-cell">
                    <span
                      v-for="(ev, i) in row.events"
                      :key="`${ev.type}-${ev.occurred_at}-${i}`"
                      class="badge event-badge"
                      :class="eventMeta(ev.type).cls"
                      :title="`${ev.type} · ${formatDate(ev.occurred_at)}`"
                    >
                      <span class="mdi" :class="eventMeta(ev.type).icon"></span>
                      {{ ev.type }}
                    </span>
                    <span v-if="row.events.length === 0" class="text-muted">—</span>
                  </div>
                </td>
                <td class="error-cell" :title="row.error || ''">{{ row.error || '—' }}</td>
                <td style="white-space: nowrap">{{ formatDate(row.created_at) }}</td>
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

/* Status chips: quiet when off, filled accent when on. */
.status-chip {
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

.status-chip .mdi {
  font-size: 15px;
}

.status-chip:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.status-chip--active {
  background: var(--primary-600);
  border-color: var(--primary-600);
  color: var(--text-on-primary);
}

.status-chip--active:hover {
  background: var(--primary-600);
  color: var(--text-on-primary);
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

/* Small popover anchored to its chip. */
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

.meta-chip {
  margin: 1px 4px 1px 0;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

.meta-mono {
  font-family: var(--font-mono);
  font-size: 11px;
}

/* Accent badge for clicks (the signature ramp), matching .badge proportions. */
.badge-accent {
  background: var(--primary-50);
  color: var(--primary-600);
}

.events-cell {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 260px;
}

.event-badge {
  gap: 4px;
  font-size: 11px;
  padding: 2px 8px;
}

.event-badge .mdi {
  font-size: 13px;
}

.error-cell {
  color: var(--danger-text);
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.table-wrapper table {
  min-width: 860px;
}
</style>
