<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { adminApi } from '../../api/admin'
import type { CronJob, QueueStats, QueueTask } from '../../api/types'
import { useNotificationStore } from '../../stores/notification'
import { useConfirm } from '../../composables/useConfirm'

const notify = useNotificationStore()
const { confirm } = useConfirm()

const loading = ref(true)
const jobs = ref<CronJob[]>([])
let refreshTimer: ReturnType<typeof setInterval> | null = null

// ---- Queue administration (R-03) ----
const queues = ref<QueueStats[]>([])
const activeWorkers = ref(0)
const queuesLoading = ref(false)
const selectedQueue = ref('')
const selectedState = ref('pending')
const tasks = ref<QueueTask[]>([])
const tasksLoading = ref(false)
const tasksError = ref('')
const taskPage = ref(0)
const taskHasMore = ref(false)
const actingTaskId = ref('')

const queueStates = [
  { value: 'pending', label: 'Pending' },
  { value: 'active', label: 'Active' },
  { value: 'scheduled', label: 'Scheduled' },
  { value: 'retry', label: 'Retrying' },
  { value: 'archived', label: 'Dead letter' },
]

onMounted(() => {
  loadJobs()
  loadQueues()
  refreshTimer = setInterval(() => {
    loadJobs()
    loadQueues()
  }, 30000)
})

onBeforeUnmount(() => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
  }
})

async function loadJobs() {
  try {
    const res = await adminApi.listJobs()
    jobs.value = res.data.data
  } catch (e) {
    console.error('Failed to load jobs', e)
  } finally {
    loading.value = false
  }
}

async function loadQueues() {
  queuesLoading.value = true
  try {
    const res = await adminApi.listQueues()
    queues.value = res.data.data.queues
    activeWorkers.value = res.data.data.active_workers
    if (!selectedQueue.value && queues.value.length > 0) {
      selectedQueue.value = queues.value[0].queue
      await loadTasks()
    }
  } catch (e) {
    console.error('Failed to load queues', e)
  } finally {
    queuesLoading.value = false
  }
}

async function loadTasks(page = 0) {
  if (!selectedQueue.value) return
  tasksLoading.value = true
  tasksError.value = ''
  try {
    const res = await adminApi.listQueueTasks(selectedQueue.value, selectedState.value, page)
    tasks.value = res.data.data.tasks
    taskPage.value = res.data.data.page
    // The backend reports a page-local total: "more" means the page came back full.
    taskHasMore.value = res.data.data.tasks.length >= res.data.data.size
  } catch (e: any) {
    tasksError.value = e?.response?.data?.message || 'Failed to load queue tasks'
    tasks.value = []
  } finally {
    tasksLoading.value = false
  }
}

function selectQueue(name: string) {
  selectedQueue.value = name
  taskPage.value = 0
  loadTasks(0)
}

function selectState(state: string) {
  selectedState.value = state
  taskPage.value = 0
  loadTasks(0)
}

function payloadOf(task: QueueTask): string {
  // email:send payloads carry the message id; show it as a readable link target.
  try {
    const parsed = JSON.parse(task.payload_snippet)
    if (parsed && typeof parsed.email_id === 'number') {
      return `email #${parsed.email_id}`
    }
  } catch {
    // fall through to the raw snippet
  }
  return task.payload_snippet
}

async function act(task: QueueTask, action: 'cancel' | 'run' | 'archive') {
  const titles = {
    cancel: 'Cancel task',
    run: 'Run task now',
    archive: 'Archive task',
  } as const
  const messages = {
    cancel: `Remove ${task.type} (${payloadOf(task)}) from the ${task.queue} queue? Dead-lettered tasks are deleted by a cancel.`,
    run: `Schedule ${task.type} (${payloadOf(task)}) for immediate processing?`,
    archive: `Move ${task.type} (${payloadOf(task)}) to the dead-letter set?`,
  } as const
  const ok = await confirm({
    title: titles[action],
    message: messages[action],
    confirmText: titles[action],
    variant: action === 'cancel' ? 'danger' : 'info',
  })
  if (!ok) return
  actingTaskId.value = task.id
  try {
    if (action === 'cancel') await adminApi.cancelQueueTask(task.queue, task.id)
    else if (action === 'run') await adminApi.runQueueTask(task.queue, task.id)
    else await adminApi.archiveQueueTask(task.queue, task.id)
    notify.success(`Task ${action === 'cancel' ? 'cancelled' : action === 'run' ? 'scheduled for run' : 'archived'}`)
    await loadTasks(taskPage.value)
    loadQueues()
  } catch (e: any) {
    notify.error(e?.response?.data?.message || `Failed to ${action} task`)
  } finally {
    actingTaskId.value = ''
  }
}

function formatDate(date: string | null) {
  if (!date) return '-'
  return new Date(date).toLocaleString()
}

function timeUntil(date: string | null) {
  if (!date) return '-'
  const diff = new Date(date).getTime() - Date.now()
  if (diff <= 0) return 'now'
  const minutes = Math.floor(diff / 60000)
  const hours = Math.floor(minutes / 60)
  if (hours > 0) return `${hours}h ${minutes % 60}m`
  return `${minutes}m`
}

function scheduleName(schedule: string) {
  const known: Record<string, string> = {
    '0 3 * * *': 'Daily at 03:00 UTC',
    '0 7 * * *': 'Daily at 07:00 UTC',
  }
  return known[schedule] || schedule
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Scheduled Jobs</h1>
      <button class="btn btn-sm btn-secondary" @click="loadJobs(); loadQueues()" :disabled="loading">
        Refresh
      </button>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <template v-else>
      <!-- ==================== Delivery queues ==================== -->
      <section class="queues-section">
        <div class="section-head">
          <h2>Delivery queues</h2>
          <span class="section-note">
            {{ activeWorkers }} worker{{ activeWorkers === 1 ? '' : 's' }} connected
          </span>
        </div>

        <div class="queue-cards">
          <button v-for="q in queues" :key="q.queue" class="queue-card"
            :class="{ selected: selectedQueue === q.queue }" @click="selectQueue(q.queue)">
            <div class="queue-card-head">
              <span class="queue-name">{{ q.queue }}</span>
              <span v-if="q.archived > 0" class="badge badge-error">{{ q.archived }} dead</span>
            </div>
            <div class="queue-nums">
              <span class="queue-num"><strong>{{ q.pending }}</strong> pending</span>
              <span class="queue-num"><strong>{{ q.active }}</strong> active</span>
              <span class="queue-num"><strong>{{ q.retry }}</strong> retry</span>
              <span class="queue-num"><strong>{{ q.archived }}</strong> archived</span>
            </div>
            <div class="queue-meta">
              {{ q.processed }} processed · {{ q.failed }} failed · latency {{ q.latency_ms }}ms
            </div>
          </button>
        </div>

        <div class="card">
          <div class="card-body">
            <div class="task-toolbar">
              <div class="state-tabs">
                <button v-for="s in queueStates" :key="s.value" class="state-tab"
                  :class="{ active: selectedState === s.value }" @click="selectState(s.value)">
                  {{ s.label }}
                </button>
              </div>
              <button class="btn btn-secondary btn-sm" :disabled="tasksLoading" @click="loadTasks(taskPage)">
                Refresh tasks
              </button>
            </div>

            <div v-if="tasksError" class="tasks-error">{{ tasksError }}</div>

            <div v-if="tasksLoading" class="loading-page" style="min-height: 80px">
              <div class="spinner"></div>
            </div>

            <div v-else-if="tasks.length === 0" class="empty-state" style="padding: 24px 0">
              <p class="text-muted">No {{ selectedState }} tasks in <strong>{{ selectedQueue }}</strong>.</p>
            </div>

            <div v-else class="table-wrapper">
              <table>
                <thead>
                  <tr>
                    <th>Type</th>
                    <th>Task</th>
                    <th>State</th>
                    <th>Retries</th>
                    <th>Last error</th>
                    <th>Next / last attempt</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="task in tasks" :key="task.id">
                    <td><code class="task-type">{{ task.type }}</code></td>
                    <td>
                      <code class="task-id" :title="task.id">{{ task.id }}</code>
                      <div class="task-payload">{{ payloadOf(task) }}</div>
                    </td>
                    <td><span class="badge badge-neutral">{{ task.state }}</span></td>
                    <td>{{ task.retried }} / {{ task.max_retry }}</td>
                    <td class="task-error" :title="task.last_error">
                      {{ task.last_error || '—' }}
                    </td>
                    <td>
                      <template v-if="task.next_process_at">
                        in {{ timeUntil(task.next_process_at) }}
                      </template>
                      <template v-else-if="task.last_failed_at">
                        failed {{ formatDate(task.last_failed_at) }}
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td>
                      <div class="flex gap-2">
                        <button class="btn btn-danger btn-sm" :disabled="actingTaskId === task.id"
                          @click="act(task, 'cancel')">Cancel</button>
                        <button class="btn btn-secondary btn-sm" :disabled="actingTaskId === task.id"
                          @click="act(task, 'run')">Run now</button>
                        <button class="btn btn-secondary btn-sm" :disabled="actingTaskId === task.id"
                          @click="act(task, 'archive')">Archive</button>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>

            <div class="task-pager" v-if="taskPage > 0 || taskHasMore">
              <button class="btn btn-secondary btn-sm" :disabled="taskPage === 0 || tasksLoading"
                @click="loadTasks(taskPage - 1)">Previous</button>
              <span class="pager-note">Page {{ taskPage + 1 }}</span>
              <button class="btn btn-secondary btn-sm" :disabled="!taskHasMore || tasksLoading"
                @click="loadTasks(taskPage + 1)">Next</button>
            </div>
          </div>
        </div>
      </section>

      <!-- ==================== Scheduled cron jobs ==================== -->
      <section class="jobs-section">
        <div class="section-head">
          <h2>Scheduled jobs</h2>
        </div>
        <div v-if="jobs.length === 0" class="empty-state">
          <h3>No scheduled jobs</h3>
          <p>No cron jobs are currently registered. Jobs are only available when the server is running in production mode.</p>
        </div>

        <div v-else class="jobs-grid">
          <div v-for="job in jobs" :key="job.name" class="card job-card">
            <div class="card-body">
              <div class="job-header">
                <h3 class="job-name">{{ job.name }}</h3>
                <span v-if="job.running" class="badge badge-running">Running</span>
                <span v-else-if="job.last_error" class="badge badge-error">Failed</span>
                <span v-else-if="job.last_run_at" class="badge badge-success">OK</span>
                <span v-else class="badge badge-neutral">Pending</span>
              </div>

              <div class="job-schedule">
                <code>{{ job.schedule }}</code>
                <span class="schedule-label">{{ scheduleName(job.schedule) }}</span>
              </div>

              <div class="job-details">
                <div class="job-detail">
                  <span class="detail-label">Last run</span>
                  <span class="detail-value">{{ formatDate(job.last_run_at) }}</span>
                </div>
                <div class="job-detail">
                  <span class="detail-label">Next run</span>
                  <span class="detail-value">
                    {{ formatDate(job.next_run_at) }}
                    <span v-if="job.next_run_at" class="time-until">(in {{ timeUntil(job.next_run_at) }})</span>
                  </span>
                </div>
              </div>

              <div v-if="job.last_error" class="job-error">
                <span class="error-label">Last error</span>
                <code class="error-message">{{ job.last_error }}</code>
              </div>
            </div>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.queues-section,
.jobs-section {
  margin-bottom: 2rem;
}

.section-head {
  display: flex;
  align-items: baseline;
  gap: 0.75rem;
  margin-bottom: 0.75rem;
}

.section-head h2 {
  font-size: 18px;
  font-weight: 600;
  margin: 0;
}

.section-note {
  font-size: 13px;
  color: var(--text-muted);
}

.queue-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 0.75rem;
  margin-bottom: 1rem;
}

.queue-card {
  text-align: left;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-md, 8px);
  background: var(--bg-secondary);
  padding: 0.75rem 0.9rem;
  cursor: pointer;
}

.queue-card.selected {
  border-color: var(--primary, var(--text-primary));
  box-shadow: 0 0 0 1px var(--primary, var(--text-primary));
}

.queue-card-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0.5rem;
}

.queue-name {
  font-family: var(--font-mono);
  font-weight: 600;
}

.queue-nums {
  display: flex;
  gap: 0.75rem;
  font-size: 12px;
  color: var(--text-muted);
}

.queue-num strong {
  color: var(--text-primary);
}

.queue-meta {
  margin-top: 0.5rem;
  font-size: 11px;
  color: var(--text-muted);
}

.task-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 1rem;
  margin-bottom: 0.75rem;
  flex-wrap: wrap;
}

.state-tabs {
  display: flex;
  gap: 0.25rem;
  flex-wrap: wrap;
}

.state-tab {
  border: 1px solid var(--border-primary);
  background: transparent;
  border-radius: 999px;
  padding: 4px 12px;
  font-size: 13px;
  color: var(--text-muted);
  cursor: pointer;
}

.state-tab.active {
  background: var(--bg-tertiary);
  color: var(--text-primary);
  border-color: var(--text-primary);
}

.tasks-error {
  margin-bottom: 0.75rem;
  padding: 8px 12px;
  border-radius: var(--radius-sm, 4px);
  background: var(--danger-50);
  color: var(--danger-text);
  font-size: 13px;
}

.task-type {
  font-family: var(--font-mono);
  font-size: 12px;
}

.task-id {
  font-family: var(--font-mono);
  font-size: 11px;
  display: block;
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-payload {
  font-size: 12px;
  color: var(--text-muted);
}

.task-error {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: 12px;
}

.task-pager {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-top: 0.75rem;
}

.pager-note {
  font-size: 13px;
  color: var(--text-muted);
}

.jobs-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(400px, 1fr));
  gap: 1rem;
}

.job-card {
  border: 1px solid var(--border-primary);
}

.job-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.75rem;
}

.job-name {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
}

/* Status colors per brand spec — the template class names are kept as-is and
   remapped here: running=green, failed=red, ok=neutral, pending=yellow.
   (In this component "OK" renders via .badge-success and "Pending" via
   .badge-neutral; the colors below are the source of truth.) */
.badge-running {
  background: var(--success-50);
  color: var(--success-text);
}

.badge-error {
  background: var(--danger-50);
  color: var(--danger-text);
}

.badge-success {
  background: var(--bg-tertiary);
  color: var(--text-tertiary);
}

.badge-neutral {
  background: var(--warning-50);
  color: var(--warning-text);
}

.job-schedule {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 1rem;
  padding-bottom: 0.75rem;
  border-bottom: 1px solid var(--border-primary);
}

.job-schedule code {
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 2px 8px;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm, 4px);
  color: var(--text-primary);
}

.schedule-label {
  font-size: 13px;
  color: var(--text-muted);
}

.job-details {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.job-detail {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 13px;
}

.detail-label {
  color: var(--text-muted);
  font-weight: 500;
}

.detail-value {
  color: var(--text-primary);
}

.time-until {
  color: var(--text-muted);
  font-size: 12px;
  margin-left: 4px;
}

.job-error {
  margin-top: 0.75rem;
  padding-top: 0.75rem;
  border-top: 1px solid var(--border-primary);
}

.error-label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: var(--danger-text);
  margin-bottom: 4px;
}

.error-message {
  display: block;
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 6px 10px;
  background: var(--danger-50);
  color: var(--danger-text);
  border-radius: var(--radius-sm, 4px);
  word-break: break-word;
}
</style>
