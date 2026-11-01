<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { adminApi } from '../../../api/admin'
import type { SecurityEventRecord, Pageable } from '../../../api/types'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'

const loading = ref(true)
const events = ref<SecurityEventRecord[]>([])
const eventType = ref('')
const liveEvents = ref<SecurityEventRecord[]>([])
const streaming = ref(false)
let eventSource: EventSource | null = null
// ban.created / ban.lifted rows are broadcast twice (as security.event and as
// the dedicated lifecycle event), so the live feed dedupes on the row id.
const seenLiveIds = new Set<number>()
const selected = ref<SecurityEventRecord | null>(null)

onMounted(() => {
  startStream()
})

onBeforeUnmount(() => {
  stopStream()
})

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminApi.listSecurityEvents(page, pageable.value.size, eventType.value)
    events.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load security events', e)
  } finally {
    loading.value = false
  }
})

function setEventType(type: string) {
  eventType.value = type
  goToPage(0)
}

// Filter pills come from the data itself: the distinct event types on the
// loaded page (capped so the row never overflows on narrow screens).
const eventTypeOptions = computed(() => {
  const types = Array.from(new Set(events.value.map(e => e.event_type)))
  return types.slice(0, 6)
})

function startStream() {
  const baseUrl = import.meta.env.VITE_API_URL || '/api/v1'
  // EventSource cannot set an Authorization header, but it does send the
  // HttpOnly session cookie on the handshake, which is what the server reads.
  eventSource = new EventSource(`${baseUrl}/admin/security/events/stream`, { withCredentials: true })

  eventSource.onopen = () => {
    streaming.value = true
  }

  const pushLive = (data: string) => {
    try {
      const evt: SecurityEventRecord = JSON.parse(data)
      if (seenLiveIds.has(evt.id)) return
      seenLiveIds.add(evt.id)
      if (seenLiveIds.size > 200) {
        // Bound the dedupe set; the feed itself is capped below anyway.
        const first = seenLiveIds.values().next().value
        if (first !== undefined) seenLiveIds.delete(first)
      }
      liveEvents.value.unshift(evt)
      if (liveEvents.value.length > 50) {
        liveEvents.value.pop()
      }
    } catch {
      // ignore parse errors
    }
  }

  for (const name of ['security.event', 'ban.created', 'ban.lifted']) {
    eventSource.addEventListener(name, (e) => {
      pushLive((e as MessageEvent).data)
    })
  }

  eventSource.onerror = () => {
    streaming.value = false
  }
}

function stopStream() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
    streaming.value = false
  }
}

function severityBadgeClass(severity: string) {
  switch (severity) {
    case 'critical': return 'badge badge-dot badge-danger'
    case 'warning': return 'badge badge-dot badge-warning'
    default: return 'badge badge-dot badge-info'
  }
}

function actionBadgeClass(action: string) {
  switch (action) {
    case 'banned': return 'badge badge-danger'
    case 'rejected': return 'badge badge-warning'
    default: return 'badge badge-neutral'
  }
}

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}

function timeAgo(date: string) {
  const seconds = Math.floor((Date.now() - new Date(date).getTime()) / 1000)
  if (seconds < 60) return 'just now'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`
  return `${Math.floor(seconds / 86400)}d ago`
}

// Pretty-print the JSON evidence blob; fall back to the raw string if it
// isn't valid JSON (or empty).
function prettyEvidence(raw: string | undefined): string {
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

function openDetail(evt: SecurityEventRecord) {
  selected.value = evt
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Detection Events</h1>
        <p class="page-description">Signal crossings and ban lifecycle records from the security engine.</p>
      </div>
      <span v-if="streaming" class="stream-indicator stream-active">Live</span>
      <span v-else class="stream-indicator stream-inactive">Disconnected</span>
    </div>

    <!-- Live Feed -->
    <div v-if="liveEvents.length > 0" class="card" style="margin-bottom: 1.5rem;">
      <div class="card-header">
        <h2>Live Feed</h2>
        <button class="btn btn-sm btn-secondary" @click="liveEvents = []">Clear</button>
      </div>
      <div class="card-body">
        <div class="live-events">
          <div v-for="(evt, i) in liveEvents" :key="'live-' + i" class="live-event-item clickable" @click="openDetail(evt)">
            <span :class="severityBadgeClass(evt.severity)">{{ evt.severity }}</span>
            <span class="event-type">{{ evt.event_type }}</span>
            <span class="event-ip">{{ evt.source_ip }}</span>
            <span class="event-action">{{ evt.action_taken }}</span>
            <span class="event-time">{{ timeAgo(evt.created_at) }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Event type filter (options derived from the loaded page) -->
    <div style="display: flex; gap: 0.5rem; margin-bottom: 1rem; flex-wrap: wrap;">
      <button class="btn btn-sm" :class="eventType === '' ? 'btn-primary' : 'btn-secondary'" @click="setEventType('')">All</button>
      <button
        v-for="type in eventTypeOptions"
        :key="type"
        class="btn btn-sm"
        :class="eventType === type ? 'btn-primary' : 'btn-secondary'"
        @click="setEventType(type)"
      >{{ type }}</button>
    </div>

    <!-- Loading: skeleton rows preserve the table layout while data streams in. -->
    <div v-if="loading" class="card">
      <div class="card-body">
        <table class="table">
          <thead>
            <tr>
              <th>Source IP</th>
              <th>Event Type</th>
              <th>Severity</th>
              <th>Protocol</th>
              <th>Port</th>
              <th>Risk</th>
              <th>Action</th>
              <th>Time</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in 6" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 110px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 130px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 60px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 48px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 36px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 36px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 70px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 120px;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else class="card">
      <div v-if="events.length === 0" class="empty-state">
        <h3>No events found</h3>
        <p v-if="eventType">No events match this event type.</p>
        <p v-else>No detection activity has been recorded yet.</p>
      </div>
      <div v-else class="card-body">
        <div class="table-wrapper">
          <table class="table">
            <thead>
              <tr>
                <th>Source IP</th>
                <th>Event Type</th>
                <th>Severity</th>
                <th>Protocol</th>
                <th>Port</th>
                <th>Risk</th>
                <th>Action</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="evt in events" :key="evt.id" class="row-clickable" @click="openDetail(evt)">
                <td><code>{{ evt.source_ip }}</code></td>
                <td><code>{{ evt.event_type }}</code></td>
                <td><span :class="severityBadgeClass(evt.severity)">{{ evt.severity }}</span></td>
                <td><code v-if="evt.protocol">{{ evt.protocol }}</code><span v-else>—</span></td>
                <td>{{ evt.destination_port ?? '—' }}</td>
                <td>{{ evt.risk_score }}</td>
                <td><span :class="actionBadgeClass(evt.action_taken)">{{ evt.action_taken }}</span></td>
                <td style="white-space: nowrap;"><span :title="formatDate(evt.created_at)">{{ formatDate(evt.created_at) }}</span></td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
      </div>
    </div>

    <!-- Event Detail Modal -->
    <div v-if="selected" class="modal-overlay" @click.self="selected = null">
      <div class="modal modal-wide">
        <div class="modal-header">
          <h3><code>{{ selected.event_type }}</code></h3>
          <span :class="severityBadgeClass(selected.severity)">{{ selected.severity }}</span>
        </div>
        <div class="modal-body">
          <dl class="detail-grid">
            <dt>Event ID</dt>
            <dd><code>{{ selected.id }}</code></dd>

            <dt>Source IP</dt>
            <dd><code>{{ selected.source_ip }}</code></dd>

            <dt>Protocol</dt>
            <dd>{{ selected.protocol || '—' }}</dd>

            <dt>Destination port</dt>
            <dd>{{ selected.destination_port ?? '—' }}</dd>

            <dt>Risk score</dt>
            <dd>{{ selected.risk_score }}</dd>

            <dt>Action taken</dt>
            <dd><span :class="actionBadgeClass(selected.action_taken)">{{ selected.action_taken }}</span></dd>

            <dt>Recorded at</dt>
            <dd>{{ formatDate(selected.created_at) }}</dd>
          </dl>

          <h4 class="evidence-heading">Evidence</h4>
          <pre v-if="prettyEvidence(selected.evidence)" class="code-block evidence-block">{{ prettyEvidence(selected.evidence) }}</pre>
          <p v-else class="evidence-empty">No evidence was recorded for this event.</p>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="selected = null">Close</button>
        </div>
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

.stream-indicator {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 600;
  padding: 4px 10px;
  border-radius: 999px;
}
.stream-indicator::before {
  content: '';
  width: 8px;
  height: 8px;
  border-radius: 50%;
}
.stream-active {
  color: var(--success-text);
  background: var(--success-50);
}
.stream-active::before {
  background: var(--success-500);
  animation: pulse 2s infinite;
}
.stream-inactive {
  color: var(--text-muted);
  background: var(--bg-secondary);
}
.stream-inactive::before {
  background: var(--text-muted);
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

.live-events {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 300px;
  overflow-y: auto;
}

.live-event-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border-radius: var(--radius-sm, 4px);
  background: var(--bg-secondary);
  font-size: 13px;
  animation: fadeIn 0.3s ease;
}

.live-event-item.clickable {
  cursor: pointer;
}

.live-event-item.clickable:hover {
  background: var(--bg-hover);
}

.event-type {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-secondary);
  min-width: 120px;
}

.event-ip {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
}

.event-action {
  flex: 1;
  color: var(--text-primary);
}

.event-time {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
}

@keyframes fadeIn {
  from { opacity: 0; transform: translateY(-4px); }
  to { opacity: 1; transform: translateY(0); }
}

.modal-wide {
  max-width: 680px;
  width: calc(100% - 32px);
}

.detail-grid {
  display: grid;
  grid-template-columns: 150px 1fr;
  gap: 10px 16px;
  margin: 0;
}

.detail-grid dt {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-muted);
  padding-top: 3px;
}

.detail-grid dd {
  margin: 0;
  font-size: 14px;
  color: var(--text-primary);
  word-break: break-word;
}

.evidence-heading {
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--text-muted);
  margin: 18px 0 8px;
}

.evidence-block {
  margin: 0;
  max-height: 260px;
  overflow-y: auto;
}

.evidence-empty {
  font-size: 13px;
  color: var(--text-muted);
  margin: 0;
}

/* IPs, event types and protocols are technical identifiers — mono face. */
code {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  padding: 1px 6px;
}

@media (max-width: 640px) {
  .detail-grid {
    grid-template-columns: 1fr;
    gap: 2px 0;
  }

  .detail-grid dt {
    padding-top: 8px;
  }
}
</style>
