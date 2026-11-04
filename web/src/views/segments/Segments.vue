<script setup lang="ts">
import { computed, ref } from 'vue'
import { segmentsApi, segmentsApiError, fieldDefFor, isCustomField, customFieldKey, SEGMENT_OPERATOR_PHRASES } from '../../api/segments'
import type { SegmentCondition, SegmentRule, SegmentRow } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import SegmentDrawer from './SegmentDrawer.vue'
import { useNotificationStore } from '../../stores/notification'
import { useConfirm } from '../../composables/useConfirm'
import { usePagination } from '../../composables/usePagination'
import { useWorkspaceStore } from '../../stores/workspace'

const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

const segments = ref<SegmentRow[]>([])
const loading = ref(true)

// Server 400s surface verbatim: the banner shows the error line plus every
// validation_errors entry, like the automations dashboard banner.
const errorBanner = ref<{ message: string; validationErrors: string[] } | null>(null)

function fail(e: any, fallback: string) {
  errorBanner.value = segmentsApiError(e, fallback)
}

async function load(page: number) {
  loading.value = true
  try {
    const res = await segmentsApi.list(page, pageable.value.size)
    segments.value = res.data.data ?? []
    pageable.value = res.data.pageable
    errorBanner.value = null
  } catch (e) {
    fail(e, 'Failed to load segments')
  } finally {
    loading.value = false
  }
}

const { pageable, goToPage } = usePagination(load)

const hasAny = computed(() => segments.value.length > 0)

// --- Condition summary ------------------------------------------------------

const DATE_FIELDS = new Set(['created_at', 'subscribed_at', 'unsubscribed_at'])
const NUMBER_FIELDS = new Set([
  'engagement.inactive_days',
  'engagement.active_days',
  'engagement.total_opens_gte',
  'engagement.total_clicks_gte',
])

function fieldLabel(rule: SegmentRule): string {
  if (isCustomField(rule.field)) {
    const key = customFieldKey(rule.field)
    return key ? `custom "${key}"` : 'custom field'
  }
  return fieldDefFor(rule.field).label.toLowerCase()
}

function operatorPhrase(rule: SegmentRule): string {
  if (rule.operator === 'greater_than') {
    if (DATE_FIELDS.has(rule.field)) return 'is after'
    if (NUMBER_FIELDS.has(rule.field)) return 'is more than'
  }
  if (rule.operator === 'less_than') {
    if (DATE_FIELDS.has(rule.field)) return 'is before'
    if (NUMBER_FIELDS.has(rule.field)) return 'is less than'
  }
  return SEGMENT_OPERATOR_PHRASES[rule.operator] ?? rule.operator
}

function ruleSummary(rule: SegmentRule): string {
  const phrase = operatorPhrase(rule)
  if (!rule.value) return `${fieldLabel(rule)} ${phrase}`
  return `${fieldLabel(rule)} ${phrase} ${rule.value}`
}

/** "email contains example.com AND status is one of subscribed" — groups parenthesized. */
function summarize(condition: SegmentCondition | null | undefined): string {
  const groups = condition?.groups ?? []
  if (groups.length === 0) return 'No conditions'
  const groupJoiner = condition?.logic === 'or' ? ' OR ' : ' AND '
  const parts = groups.map((group) => {
    const rules = group.rules ?? []
    if (rules.length === 0) return 'no conditions'
    const inner = rules.map(ruleSummary).join(group.logic === 'or' ? ' OR ' : ' AND ')
    return groups.length > 1 ? `(${inner})` : inner
  })
  return parts.join(groupJoiner)
}

// --- Row actions ------------------------------------------------------------

const drawerOpen = ref(false)
const editingSegment = ref<SegmentRow | null>(null)

function openCreate() {
  editingSegment.value = null
  drawerOpen.value = true
}

function openEdit(segment: SegmentRow) {
  editingSegment.value = segment
  drawerOpen.value = true
}

function onSaved(message: string) {
  drawerOpen.value = false
  notify.success(message)
  load(pageable.value.current_page)
}

const busyId = ref<number | null>(null)

/** Duplicate = fetch the row, then POST it back with a " (copy)" name. */
async function duplicate(segment: SegmentRow) {
  if (busyId.value) return
  busyId.value = segment.id
  errorBanner.value = null
  try {
    const res = await segmentsApi.get(segment.id)
    const source = res.data.data
    const created = await segmentsApi.create({
      name: `${source.name} (copy)`,
      description: source.description || undefined,
      condition: source.condition,
    })
    notify.success(`Duplicated as "${created.data.data.name}"`)
    await load(pageable.value.current_page)
  } catch (e) {
    fail(e, 'Failed to duplicate segment')
  } finally {
    busyId.value = null
  }
}

async function remove(segment: SegmentRow) {
  const ok = await confirm({
    title: 'Delete Segment',
    message: `Are you sure you want to delete "${segment.name}"? Campaigns using it will keep their last saved audience. This cannot be undone.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!ok) return
  errorBanner.value = null
  try {
    await segmentsApi.remove(segment.id)
    notify.success('Segment deleted')
    // Refetch rather than splice so the page stays consistent with the server.
    if (segments.value.length === 1 && pageable.value.current_page > 0) load(pageable.value.current_page - 1)
    else load(pageable.value.current_page)
  } catch (e) {
    fail(e, 'Failed to delete segment')
  }
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
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Segments</h1>
        <p class="page-description">Reusable filters that group subscribers by their attributes and engagement.</p>
      </div>
      <button v-if="wsStore.canEdit" class="btn btn-primary" @click="openCreate">
        <span class="mdi mdi-plus"></span>
        Create Segment
      </button>
    </div>

    <div v-if="errorBanner" class="banner" role="alert">
      <div class="banner-title">
        <span class="mdi mdi-alert-circle-outline"></span>
        {{ errorBanner.message }}
      </div>
      <ul v-if="errorBanner.validationErrors.length" class="banner-list">
        <li v-for="(err, i) in errorBanner.validationErrors" :key="i">{{ err }}</li>
      </ul>
      <div class="banner-actions">
        <button class="btn btn-sm btn-secondary" @click="load(pageable.current_page)">Retry</button>
      </div>
      <button class="banner-close" aria-label="Dismiss" @click="errorBanner = null">
        <span class="mdi mdi-close"></span>
      </button>
    </div>

    <div class="card">
      <div v-if="loading" class="seg-skeleton" aria-hidden="true">
        <div v-for="n in 4" :key="n" class="seg-skeleton__row">
          <div class="skeleton skeleton-text" style="width: 22%"></div>
          <div class="skeleton skeleton-text" style="width: 30%"></div>
          <div class="skeleton skeleton-text" style="width: 34%"></div>
          <div class="skeleton skeleton-text" style="width: 8%"></div>
        </div>
      </div>

      <template v-else>
        <div v-if="!hasAny" class="empty-state">
          <span class="mdi mdi-shape-outline empty-state__icon"></span>
          <h3>No segments yet</h3>
          <p>Create a segment to target subscribers by attributes and engagement.</p>
          <button v-if="wsStore.canEdit" class="btn btn-primary" @click="openCreate">
            <span class="mdi mdi-plus"></span>
            Create Segment
          </button>
        </div>

        <template v-else>
          <div class="table-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Description</th>
                  <th>Conditions</th>
                  <th class="seg-cell-num">Contacts</th>
                  <th>Updated</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="segment in segments" :key="segment.id">
                  <td>
                    <span class="seg-name">{{ segment.name }}</span>
                  </td>
                  <td class="seg-description">{{ segment.description || '—' }}</td>
                  <td>
                    <span class="seg-summary" :title="summarize(segment.condition)">{{ summarize(segment.condition) }}</span>
                  </td>
                  <td class="seg-cell-num">{{ segment.matching_count }}</td>
                  <td class="seg-updated" :title="new Date(segment.updated_at).toLocaleString()">
                    {{ relativeTime(segment.updated_at) }}
                  </td>
                  <td>
                    <div class="seg-actions">
                      <button v-if="wsStore.canEdit" class="btn btn-secondary btn-sm" @click="openEdit(segment)">Edit</button>
                      <button v-if="wsStore.canEdit" class="btn btn-secondary btn-sm" :disabled="busyId === segment.id" @click="duplicate(segment)">
                        Duplicate
                      </button>
                      <button v-if="wsStore.canEdit" class="btn btn-danger btn-sm" @click="remove(segment)">Delete</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <Pagination :pageable="pageable" @page="goToPage" />
        </template>
      </template>
    </div>

    <SegmentDrawer
      v-if="drawerOpen"
      :segment="editingSegment"
      @close="drawerOpen = false"
      @saved="onSaved"
    />

    <ConfirmDialog />
  </div>
</template>

<style scoped>
.page-description {
  margin: 4px 0 0;
  font-size: 13.5px;
  color: var(--text-muted);
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

.banner-actions {
  margin-top: 4px;
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

/* Loading: skeleton rows keep the table rhythm while segments stream in. */
.seg-skeleton {
  display: flex;
  flex-direction: column;
  gap: 18px;
  padding: 16px;
}

.seg-skeleton__row {
  display: grid;
  grid-template-columns: 1.2fr 1.4fr 2fr 0.6fr;
  gap: 16px;
  align-items: center;
}

.seg-name {
  font-weight: 600;
  color: var(--text-primary);
}

.seg-description {
  color: var(--text-secondary);
  font-size: 13px;
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.seg-summary {
  display: inline-block;
  max-width: 420px;
  padding: 3px 10px;
  border-radius: 999px;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1.6;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

.seg-cell-num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.seg-updated {
  color: var(--text-muted);
  font-size: 13px;
  white-space: nowrap;
}

.seg-actions {
  display: flex;
  gap: 6px;
  white-space: nowrap;
}

.empty-state__icon {
  font-size: 44px;
  color: var(--text-muted);
  display: block;
  margin-bottom: 8px;
}

@media (max-width: 640px) {
  .seg-actions {
    flex-wrap: wrap;
  }
}
</style>
