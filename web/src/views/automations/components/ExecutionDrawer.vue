<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { automationsApi, automationApiError } from '../../../api/automations'
import type { AutomationExecutionDetail, JourneyRow, NodeRunRow } from '../../../api/types'

/**
 * Right-hand drawer over an execution: the journey summary plus its node-run
 * timeline (oldest first), with each run's output JSON and error.
 */
const props = defineProps<{
  automationId: number
  /** null when the drawer is closed. */
  journey: JourneyRow | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const loading = ref(false)
const detail = ref<AutomationExecutionDetail | null>(null)
const error = ref('')

watch(
  () => [props.automationId, props.journey?.id] as const,
  async ([, journeyId]) => {
    detail.value = null
    error.value = ''
    if (!journeyId) return
    loading.value = true
    try {
      const res = await automationsApi.execution(props.automationId, journeyId)
      detail.value = res.data.data
    } catch (e) {
      error.value = automationApiError(e, 'Failed to load execution').message
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const journey = computed<JourneyRow | null>(() => detail.value?.journey ?? props.journey)
const timeline = computed<NodeRunRow[]>(() => detail.value?.timeline ?? [])

const STATUS_BADGES: Record<string, string> = {
  active: 'badge badge-info',
  completed: 'badge badge-success',
  exited: 'badge badge-neutral',
  failed: 'badge badge-danger',
  entered: 'badge badge-info',
  processing: 'badge badge-warning',
}

function statusBadge(status: string): string {
  return STATUS_BADGES[status] ?? 'badge'
}

function formatDate(value: string | null | undefined): string {
  if (!value) return '—'
  return new Date(value).toLocaleString()
}

function prettyOutput(output: string): string {
  if (!output || output === '{}' || output === 'null') return ''
  try {
    return JSON.stringify(JSON.parse(output), null, 2)
  } catch {
    return output
  }
}

const NODE_TYPE_LABELS: Record<string, string> = {
  trigger: 'Trigger',
  delay: 'Delay',
  email: 'Email',
  filter: 'Filter',
  ab_test: 'A/B Test',
  list_status: 'List Status',
  add_to_list: 'Add to List',
  remove_from_list: 'Remove from List',
  webhook: 'Webhook',
}

function nodeTypeLabel(type: string): string {
  return NODE_TYPE_LABELS[type] ?? type
}

const NODE_TYPE_ICONS: Record<string, string> = {
  trigger: 'mdi-bolt',
  delay: 'mdi-clock-outline',
  email: 'mdi-email-outline',
  filter: 'mdi-filter-outline',
  ab_test: 'mdi-chart-bar',
  list_status: 'mdi-account-multiple-check',
  add_to_list: 'mdi-playlist-plus',
  remove_from_list: 'mdi-playlist-remove',
  webhook: 'mdi-web',
}

function nodeTypeIcon(type: string): string {
  return NODE_TYPE_ICONS[type] ?? 'mdi-circle-outline'
}

function durationLabel(ms: number): string {
  if (!ms) return '—'
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(1)} s`
}
</script>

<template>
  <Teleport to="body">
    <div v-if="journey" class="edrawer-overlay" @click.self="emit('close')">
      <div class="edrawer" role="dialog" aria-modal="true" aria-label="Execution detail">
        <header class="edrawer__header">
          <div class="edrawer__heading">
            <h2>Execution</h2>
            <span class="edrawer__email">{{ journey.email }}</span>
          </div>
          <button class="edrawer__close" aria-label="Close" @click="emit('close')">
            <span class="mdi mdi-close"></span>
          </button>
        </header>

        <div class="edrawer__body">
          <div v-if="loading" class="edrawer__loading">
            <div class="spinner"></div>
          </div>

          <template v-else-if="error">
            <div class="banner banner-error" role="alert">{{ error }}</div>
          </template>

          <template v-else-if="journey">
            <dl class="edrawer__info">
              <div class="edrawer__info-row">
                <dt>Status</dt>
                <dd><span :class="statusBadge(journey.status)">{{ journey.status }}</span></dd>
              </div>
              <div class="edrawer__info-row">
                <dt>Entered</dt>
                <dd>{{ formatDate(journey.entered_at) }}</dd>
              </div>
              <div class="edrawer__info-row">
                <dt>Current node</dt>
                <dd class="edrawer__mono">{{ journey.current_node_id || '—' }}</dd>
              </div>
              <div class="edrawer__info-row">
                <dt>Completed</dt>
                <dd>{{ formatDate(journey.completed_at) }}</dd>
              </div>
              <div v-if="journey.exit_reason" class="edrawer__info-row">
                <dt>Exit reason</dt>
                <dd>{{ journey.exit_reason }}</dd>
              </div>
              <div class="edrawer__info-row">
                <dt>Retries</dt>
                <dd>{{ journey.retry_count }}</dd>
              </div>
              <div v-if="journey.last_error" class="edrawer__info-row">
                <dt>Last error</dt>
                <dd class="edrawer__error-text">{{ journey.last_error }}</dd>
              </div>
            </dl>

            <h3 class="edrawer__timeline-title">Node runs</h3>
            <div v-if="timeline.length === 0" class="edrawer__empty">No node runs recorded yet.</div>
            <ol v-else class="edrawer__timeline">
              <li v-for="run in timeline" :key="run.id" class="run" :class="{ 'run--failed': run.action === 'failed' }">
                <div class="run__head">
                  <span class="run__icon"><span class="mdi" :class="nodeTypeIcon(run.node_type)"></span></span>
                  <span class="run__node">{{ nodeTypeLabel(run.node_type) }}</span>
                  <span class="run__node-id">{{ run.node_id }}</span>
                  <span :class="statusBadge(run.action)">{{ run.action }}</span>
                </div>
                <div class="run__meta">
                  <span>{{ formatDate(run.entered_at) }}</span>
                  <span v-if="run.completed_at">→ {{ formatDate(run.completed_at) }}</span>
                  <span v-if="run.duration_ms">· {{ durationLabel(run.duration_ms) }}</span>
                </div>
                <p v-if="run.error" class="run__error">{{ run.error }}</p>
                <pre v-if="prettyOutput(run.output)" class="run__output">{{ prettyOutput(run.output) }}</pre>
              </li>
            </ol>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.edrawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 60;
  background: rgba(0, 0, 0, 0.45);
}

.edrawer {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(480px, 100vw);
  display: flex;
  flex-direction: column;
  background: var(--bg-primary);
  border-left: 1px solid var(--border-primary);
  box-shadow: var(--shadow-lg);
}

.edrawer__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border-primary);
}

.edrawer__heading {
  min-width: 0;
}

.edrawer__heading h2 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}

.edrawer__email {
  display: block;
  font-family: var(--font-mono);
  font-size: 12.5px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.edrawer__close {
  display: inline-flex;
  padding: 6px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

.edrawer__close:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.edrawer__body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.edrawer__loading {
  display: flex;
  justify-content: center;
  padding: 40px 0;
}

.edrawer__info {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  font-size: 13.5px;
}

.edrawer__info-row {
  display: flex;
  gap: 12px;
}

.edrawer__info-row dt {
  flex: 0 0 110px;
  color: var(--text-muted);
  font-size: 12.5px;
}

.edrawer__info-row dd {
  margin: 0;
  color: var(--text-secondary);
  min-width: 0;
  overflow-wrap: anywhere;
}

.edrawer__mono {
  font-family: var(--font-mono);
  font-size: 12.5px;
}

.edrawer__error-text {
  color: var(--danger-text);
}

.edrawer__timeline-title {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-tertiary);
}

.edrawer__empty {
  font-size: 13px;
  color: var(--text-muted);
}

.edrawer__timeline {
  list-style: none;
  margin: 0;
  padding: 0 0 0 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  border-left: 2px solid var(--border-primary);
}

.run {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-secondary);
}

.run--failed {
  border-color: var(--danger-500);
}

.run__head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.run__icon {
  display: inline-flex;
  font-size: 15px;
  color: var(--text-tertiary);
}

.run__node {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}

.run__node-id {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 130px;
}

.run__head .badge {
  margin-left: auto;
}

.run__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 12px;
  color: var(--text-muted);
}

.run__error {
  margin: 0;
  font-size: 12.5px;
  color: var(--danger-text);
  overflow-wrap: anywhere;
}

.run__output {
  margin: 0;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: var(--bg-code);
  color: #c8ccd0;
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.5;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-word;
}

.banner-error {
  padding: 10px 12px;
  border: 1px solid var(--danger-500);
  border-radius: var(--radius);
  background: var(--danger-50);
  color: var(--danger-text);
  font-size: 13.5px;
}

@media (max-width: 520px) {
  .edrawer {
    width: 100vw;
    border-left: none;
  }
}
</style>
