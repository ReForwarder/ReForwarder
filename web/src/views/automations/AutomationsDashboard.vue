<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { automationsApi, automationApiError } from '../../api/automations'
import { subscriberListsApi } from '../../api/subscriberLists'
import { useNotificationStore } from '../../stores/notification'
import { useWorkspaceStore } from '../../stores/workspace'
import type { AutomationRow, AutomationStatus } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import { useConfirm } from '../../composables/useConfirm'
import { usePagination } from '../../composables/usePagination'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

const loading = ref(true)
const automations = ref<AutomationRow[]>([])
// Mirrored to the URL (?status=) so filtered views are shareable.
const statusFilter = ref(typeof route.query.status === 'string' ? route.query.status : '')

// The list-name column resolves against the workspace's subscriber lists once;
// lists deleted since the automation was saved fall back to the raw id.
const listNames = ref<Map<number, string>>(new Map())

// Server 400s surface verbatim: the banner shows the error line plus every
// validation_errors entry (one per violated workflow rule).
const errorBanner = ref<{ message: string; validationErrors: string[] } | null>(null)

function fail(e: any, fallback: string) {
  errorBanner.value = automationApiError(e, fallback)
}

async function load(page: number) {
  loading.value = true
  try {
    const res = await automationsApi.list({ page, size: pageable.value.size, status: statusFilter.value || undefined })
    automations.value = res.data.data
    pageable.value = res.data.pageable
    errorBanner.value = null
  } catch (e) {
    fail(e, 'Failed to load automations')
  } finally {
    loading.value = false
  }
}

const { pageable, goToPage } = usePagination(load)

onMounted(async () => {
  try {
    const res = await subscriberListsApi.list(0, 100)
    listNames.value = new Map(res.data.data.map((l) => [l.id, l.name]))
  } catch {
    // Names are cosmetic; ids render instead.
  }
})

function applyFilter(status: string) {
  statusFilter.value = status
  router.replace({ query: { ...route.query, status: status || undefined, page: undefined } })
  load(0)
}

// --- Card helpers ----------------------------------------------------------

function statusBadgeClass(status: AutomationStatus) {
  switch (status) {
    case 'live': return 'badge badge-success'
    case 'paused': return 'badge badge-warning'
    default: return 'badge badge-neutral'
  }
}

function statusLabel(status: AutomationStatus) {
  switch (status) {
    case 'live': return 'Live'
    case 'paused': return 'Paused'
    default: return 'Draft'
  }
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

/** Trigger chip: human event kind + the list / custom event it matches. */
function triggerLabel(a: AutomationRow): string {
  const cfg = a.trigger_config
  const kind = EVENT_KIND_LABELS[cfg?.event_kind] ?? cfg?.event_kind ?? 'No trigger'
  if (cfg?.event_kind === 'custom' && cfg.custom_event_name) return `${kind} · ${cfg.custom_event_name}`
  if (cfg?.event_kind === 'list.subscribed') {
    const listId = cfg.list_id ?? a.list_id
    if (listId) return `${kind} · ${listName(listId)}`
  }
  return kind
}

function listName(listId: number | null | undefined): string {
  if (!listId) return '—'
  return listNames.value.get(listId) ?? `List #${listId}`
}

const FREQUENCY_LABELS: Record<string, string> = {
  once: 'Once per contact',
  every_time: 'Every time',
}

function frequencyLabel(a: AutomationRow): string {
  return FREQUENCY_LABELS[a.trigger_config?.frequency] ?? a.trigger_config?.frequency ?? 'Once per contact'
}

function relativeTime(date: string): string {
  const ms = Date.now() - new Date(date).getTime()
  const mins = Math.floor(ms / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return new Date(date).toLocaleDateString()
}

// --- Actions ---------------------------------------------------------------

const busyId = ref<number | null>(null)

async function activate(a: AutomationRow) {
  if (busyId.value) return
  busyId.value = a.id
  errorBanner.value = null
  try {
    const res = await automationsApi.activate(a.id)
    replaceRow(res.data.data)
    notify.success(`"${a.name}" is live`)
  } catch (e) {
    fail(e, 'Failed to activate automation')
  } finally {
    busyId.value = null
  }
}

async function pause(a: AutomationRow) {
  if (busyId.value) return
  busyId.value = a.id
  errorBanner.value = null
  try {
    const res = await automationsApi.pause(a.id)
    replaceRow(res.data.data)
    notify.success(`"${a.name}" paused`)
  } catch (e) {
    fail(e, 'Failed to pause automation')
  } finally {
    busyId.value = null
  }
}

async function duplicate(a: AutomationRow) {
  if (busyId.value) return
  busyId.value = a.id
  errorBanner.value = null
  try {
    const res = await automationsApi.duplicate(a.id)
    notify.success(`Duplicated as "${res.data.data.name}"`)
    load(pageable.value.current_page)
  } catch (e) {
    fail(e, 'Failed to duplicate automation')
  } finally {
    busyId.value = null
  }
}

function replaceRow(updated: AutomationRow) {
  const i = automations.value.findIndex((a) => a.id === updated.id)
  if (i >= 0) automations.value[i] = updated
}

async function remove(a: AutomationRow) {
  const ok = await confirm({
    title: 'Delete automation',
    message: `Delete "${a.name}"? Its active contacts will exit the workflow. This cannot be undone.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!ok) return
  errorBanner.value = null
  try {
    await automationsApi.remove(a.id)
    notify.success('Automation deleted')
    // Refetch rather than splice so the page stays consistent with the server.
    if (automations.value.length === 1 && pageable.value.current_page > 0) load(pageable.value.current_page - 1)
    else load(pageable.value.current_page)
  } catch (e) {
    // A live automation refuses deletion: surface the server's "pause it
    // first" message verbatim.
    fail(e, 'Failed to delete automation')
  }
}

const hasAny = computed(() => automations.value.length > 0)
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Automations</h1>
        <p class="page-description">Multi-step workflows that react to your contacts: triggers, delays, email sends and branches.</p>
      </div>
      <button v-if="wsStore.canEdit" class="btn btn-primary" @click="router.push('/automations/new')">
        <span class="mdi mdi-plus"></span>
        Create Automation
      </button>
    </div>

    <div v-if="errorBanner" class="banner banner-error" role="alert">
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

    <div class="list-toolbar">
      <div class="segmented">
        <button
          v-for="opt in [
            { value: '', label: 'All' },
            { value: 'draft', label: 'Draft' },
            { value: 'live', label: 'Live' },
            { value: 'paused', label: 'Paused' },
          ]"
          :key="opt.value"
          class="segmented__item"
          :class="{ 'segmented__item--active': statusFilter === opt.value }"
          @click="applyFilter(opt.value)"
        >
          {{ opt.label }}
        </button>
      </div>
      <span class="list-toolbar__count">{{ pageable.total_elements }} total</span>
    </div>

    <!-- Loading: skeleton cards keep the grid while automations stream in. -->
    <div v-if="loading" class="cards-grid">
      <div v-for="n in 6" :key="n" class="auto-card">
        <div class="skeleton skeleton-text" style="width: 55%; height: 18px"></div>
        <div class="skeleton skeleton-text" style="width: 40%"></div>
        <div class="auto-card__stats">
          <div v-for="m in 4" :key="m" class="skeleton skeleton-text" style="height: 32px"></div>
        </div>
        <div class="skeleton skeleton-text" style="width: 70%"></div>
      </div>
    </div>

    <div v-else-if="!hasAny" class="empty-state">
      <span class="mdi mdi-shuffle-variant empty-state__icon"></span>
      <h3>{{ statusFilter ? 'No automations with this status' : 'No automations yet' }}</h3>
      <p v-if="statusFilter">Try a different status filter.</p>
      <p v-else>Create your first automation</p>
      <button v-if="!statusFilter && wsStore.canEdit" class="btn btn-primary" @click="router.push('/automations/new')">
        <span class="mdi mdi-plus"></span>
        Create Automation
      </button>
    </div>

    <template v-else>
      <div class="cards-grid">
        <article v-for="a in automations" :key="a.id" class="auto-card">
          <header class="auto-card__header">
            <router-link :to="`/automations/${a.id}`" class="auto-card__name">{{ a.name }}</router-link>
            <span :class="statusBadgeClass(a.status)">{{ statusLabel(a.status) }}</span>
          </header>

          <div class="auto-card__stats">
            <div class="auto-card__stat">
              <span class="auto-card__stat-value">{{ a.stats?.enrolled ?? 0 }}</span>
              <span class="auto-card__stat-label">Enrolled</span>
            </div>
            <div class="auto-card__stat">
              <span class="auto-card__stat-value">{{ a.stats?.completed ?? 0 }}</span>
              <span class="auto-card__stat-label">Completed</span>
            </div>
            <div class="auto-card__stat">
              <span class="auto-card__stat-value">{{ a.stats?.exited ?? 0 }}</span>
              <span class="auto-card__stat-label">Exited</span>
            </div>
            <div class="auto-card__stat">
              <span class="auto-card__stat-value" :style="a.stats?.failed ? 'color: var(--danger-text)' : undefined">
                {{ a.stats?.failed ?? 0 }}
              </span>
              <span class="auto-card__stat-label">Failed</span>
            </div>
          </div>

          <dl class="auto-card__meta">
            <div class="auto-card__meta-row">
              <dt>ID</dt>
              <dd class="auto-card__mono">{{ a.uuid || a.id }}</dd>
            </div>
            <div class="auto-card__meta-row">
              <dt>Trigger</dt>
              <dd>
                <span class="chip">
                  <span class="mdi mdi-bolt chip__icon"></span>
                  {{ triggerLabel(a) }}
                </span>
              </dd>
            </div>
            <div class="auto-card__meta-row">
              <dt>List</dt>
              <dd>{{ a.list_id ? listName(a.list_id) : '—' }}</dd>
            </div>
            <div class="auto-card__meta-row">
              <dt>Frequency</dt>
              <dd>{{ frequencyLabel(a) }}</dd>
            </div>
          </dl>

          <footer class="auto-card__footer">
            <span class="auto-card__updated" :title="new Date(a.updated_at).toLocaleString()">
              Updated {{ relativeTime(a.updated_at) }}
            </span>
            <div v-if="wsStore.canEdit" class="auto-card__actions">
              <button
                v-if="a.status !== 'live'"
                class="btn btn-sm btn-primary"
                :disabled="busyId === a.id"
                @click="activate(a)"
              >
                {{ busyId === a.id ? '…' : 'Activate' }}
              </button>
              <button
                v-if="a.status === 'live'"
                class="btn btn-sm btn-secondary"
                :disabled="busyId === a.id"
                @click="pause(a)"
              >
                Pause
              </button>
              <button class="btn btn-sm btn-secondary" @click="router.push(`/automations/${a.id}/edit`)">Edit</button>
              <button class="btn btn-sm btn-secondary" :disabled="busyId === a.id" @click="duplicate(a)">Duplicate</button>
              <button class="btn btn-sm btn-danger" :disabled="busyId === a.id" @click="remove(a)">Delete</button>
            </div>
          </footer>
        </article>
      </div>
      <Pagination :pageable="pageable" @page="goToPage" />
    </template>

    <ConfirmDialog />
  </div>
</template>

<style scoped>
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

.list-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}

.segmented {
  display: inline-flex;
  padding: 3px;
  border: 1px solid var(--border-primary);
  border-radius: 999px;
  background: var(--bg-tertiary);
}

.segmented__item {
  padding: 6px 14px;
  border: none;
  border-radius: 999px;
  background: transparent;
  color: var(--text-secondary);
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
}

.segmented__item--active {
  background: var(--bg-primary);
  color: var(--text-primary);
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}

.list-toolbar__count {
  font-size: 13px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.cards-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 340px), 1fr));
  gap: 16px;
  margin-bottom: 16px;
}

.auto-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  min-width: 0;
}

.auto-card__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.auto-card__name {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  text-decoration: none;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.auto-card__name:hover {
  color: var(--primary-600);
}

.auto-card__stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
}

.auto-card__stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: var(--bg-tertiary);
  min-width: 0;
}

.auto-card__stat-value {
  font-size: 16px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  color: var(--text-primary);
}

.auto-card__stat-label {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.auto-card__meta {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  font-size: 13px;
}

.auto-card__meta-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  min-width: 0;
}

.auto-card__meta-row dt {
  flex: 0 0 68px;
  color: var(--text-muted);
  font-size: 12px;
}

.auto-card__meta-row dd {
  margin: 0;
  color: var(--text-secondary);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.auto-card__mono {
  font-family: var(--font-mono);
  font-size: 12px;
}

.chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--primary-50);
  color: var(--primary-700);
  font-size: 12px;
  font-weight: 500;
}

.chip__icon {
  font-size: 13px;
  flex: none;
}

.auto-card__footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-top: auto;
  padding-top: 10px;
  border-top: 1px solid var(--border-secondary);
}

.auto-card__updated {
  font-size: 12px;
  color: var(--text-muted);
  white-space: nowrap;
}

.auto-card__actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.empty-state__icon {
  font-size: 44px;
  color: var(--text-muted);
  display: block;
  margin-bottom: 8px;
}

@media (max-width: 480px) {
  .auto-card__actions {
    justify-content: flex-start;
  }

  .auto-card__footer {
    flex-direction: column;
    align-items: flex-start;
  }
}
</style>
