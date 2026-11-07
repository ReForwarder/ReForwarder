<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { logsApi } from '../../api/logs'
import type { MessageDetailRow, MessageContent, EmailEventRecord } from '../../api/types'

const route = useRoute()
const router = useRouter()

const loading = ref(true)
const notFound = ref(false)
const message = ref<MessageDetailRow | null>(null)
const events = ref<EmailEventRecord[]>([])
const content = ref<MessageContent | null>(null)
const activeTab = ref<'html' | 'mjml' | 'data' | 'meta'>('html')

// ---- Live trace stream (SF-56) ----
// The SSE tail opens at the newest existing event, so the history fetched
// above and the stream interleave without duplicates; rows are deduped by id
// regardless (the SecurityEvents pattern).
const streaming = ref(false)
let eventSource: EventSource | null = null

function startStream(uuid: string) {
  const baseUrl = import.meta.env.VITE_API_URL || '/api/v1'
  // EventSource cannot set an Authorization header, but it does send the
  // HttpOnly session cookie on the handshake; the active workspace travels as
  // the workspace_id query parameter the server's stream middleware reads.
  eventSource = new EventSource(`${baseUrl}/workspaces/current/logs/${encodeURIComponent(uuid)}/events/stream`, {
    withCredentials: true,
  })
  eventSource.onopen = () => {
    streaming.value = true
  }
  eventSource.addEventListener('email.event', (e) => {
    try {
      const evt: EmailEventRecord = JSON.parse((e as MessageEvent).data)
      if (events.value.some((existing) => existing.id === evt.id)) return
      events.value = [...events.value, evt].sort((a, b) => a.id - b.id)
    } catch {
      // ignore malformed frames
    }
  })
  eventSource.onerror = () => {
    streaming.value = false
  }
}

function stopStream() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
  streaming.value = false
}

onBeforeUnmount(() => {
  stopStream()
  stopStatusPolling()
})

// ---- Delivery-status polling (CA-02) ----
// While a send is queued or processing, the lightweight
// GET /workspaces/current/emails/{id}/status view (RF-F103's dead route) is
// polled every 4s until it reports a terminal state or the poll budget runs
// out; the badge updates in place and the timeline refetches once on arrival.
const TERMINAL_STATUSES = new Set(['sent', 'failed', 'suppressed'])
const STATUS_POLL_MS = 4000
const STATUS_POLL_MAX = 75 // ~5 minutes, then give up quietly
let statusTimer: ReturnType<typeof setInterval> | null = null
let statusPolls = 0

function startStatusPolling(uuid: string) {
  if (statusTimer) return
  statusTimer = setInterval(async () => {
    statusPolls++
    if (statusPolls > STATUS_POLL_MAX) {
      stopStatusPolling()
      return
    }
    try {
      const resp = await logsApi.getEmailStatus(uuid)
      const status = resp.data.data.status
      if (message.value && message.value.status !== status) {
        message.value = { ...message.value, status }
      }
      if (TERMINAL_STATUSES.has(status)) {
        stopStatusPolling()
        // The terminal row may carry new timeline entries; refetch once.
        try {
          const evs = await logsApi.getMessageEvents(uuid)
          events.value = evs.data.data || []
        } catch { /* the timeline keeps whatever it had */ }
      }
    } catch {
      // A single failed probe is ignored; the next tick retries. The SSE tail
      // remains the primary live channel.
    }
  }, STATUS_POLL_MS)
}

function stopStatusPolling() {
  if (statusTimer) {
    clearInterval(statusTimer)
    statusTimer = null
  }
}

onMounted(async () => {
  const uuid = route.params.uuid as string
  // Three independent reads; the page renders with whichever parts resolved so
  // a content hiccup never hides the metadata.
  const [msg, evs, cnt] = await Promise.allSettled([
    logsApi.getMessage(uuid),
    logsApi.getMessageEvents(uuid),
    logsApi.getMessageContent(uuid),
  ])
  if (msg.status === 'fulfilled') {
    message.value = msg.value.data.data
  } else {
    notFound.value = true
  }
  if (evs.status === 'fulfilled') events.value = evs.value.data.data || []
  if (cnt.status === 'fulfilled') content.value = cnt.value.data.data
  loading.value = false
  // Live tail: new timeline rows arrive over SSE while the page is open.
  if (!notFound.value) startStream(uuid)
  // CA-02: poll the delivery status while the send is still in flight.
  if (!notFound.value && message.value && !TERMINAL_STATUSES.has(message.value.status)) {
    startStatusPolling(uuid)
  }
})

// The content endpoint answers {redacted: true} — and nothing else — when the
// platform's email_content_visibility setting hides bodies.
const redacted = computed(() => content.value?.redacted === true)
const hasHtml = computed(() => !!content.value?.html_body)

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

// The R-05 classification the ingestion pipeline stamped onto the event's
// metadata JSONB ({"classification": "mailbox_not_found", …}); empty when the
// event carries none.
function eventClassification(ev: EmailEventRecord): string {
  if (!ev.metadata) return ''
  try {
    const meta = JSON.parse(ev.metadata) as Record<string, unknown>
    return typeof meta.classification === 'string' ? meta.classification : ''
  } catch {
    return ''
  }
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

function formatDate(date: string | null | undefined) {
  if (!date) return '-'
  return new Date(date).toLocaleString(undefined, { timeZoneName: 'short' })
}

const templateDataJson = computed(() =>
  JSON.stringify(content.value?.template_data ?? {}, null, 2)
)

// Metadata tab: the transport headers plus the identifiers and send params of
// the message — everything that describes the send without its body.
const metadataJson = computed(() =>
  JSON.stringify(
    {
      uuid: message.value?.uuid,
      external_id: message.value?.external_id || undefined,
      provider: message.value?.provider || undefined,
      smtp_hostname: message.value?.smtp_hostname || undefined,
      headers: content.value?.headers ?? {},
    },
    null,
    2
  )
)

// Copy pattern: clipboard write + a short "Copied" flash on the button.
const copiedKey = ref<string | null>(null)
let copiedTimer: ReturnType<typeof setTimeout> | null = null
function copy(key: string, text: string) {
  navigator.clipboard.writeText(text)
  copiedKey.value = key
  if (copiedTimer) clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => (copiedKey.value = null), 2000)
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Message Detail</h1>
        <p class="page-description" v-if="message">{{ message.subject }}</p>
      </div>
      <button class="btn btn-secondary" @click="router.push('/logs/messages')">Back to Message History</button>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <template v-else-if="message">
      <div class="card" style="margin-bottom: 24px">
        <div class="card-header">
          <h2>{{ message.subject }}</h2>
          <span :class="statusBadgeClass(message.status)">{{ message.status }}</span>
        </div>
        <div class="card-body">
          <table>
            <tbody>
              <tr>
                <td style="font-weight: 600; width: 150px">From</td>
                <td style="font-family: var(--font-mono); font-size: 13px">{{ message.sender }}</td>
              </tr>
              <tr>
                <td style="font-weight: 600">To</td>
                <td style="font-family: var(--font-mono); font-size: 13px">{{ (message.recipients || []).join(', ') }}</td>
              </tr>
              <tr>
                <td style="font-weight: 600">Subject</td>
                <td>{{ message.subject }}</td>
              </tr>
              <tr>
                <td style="font-weight: 600">UUID</td>
                <td style="font-family: var(--font-mono); font-size: 13px">
                  {{ message.uuid }}
                  <button class="copy-btn" type="button" title="Copy UUID" @click="copy('uuid', message!.uuid)">
                    <span class="mdi" :class="copiedKey === 'uuid' ? 'mdi-check' : 'mdi-content-copy'"></span>
                  </button>
                </td>
              </tr>
              <tr>
                <td style="font-weight: 600">External ID</td>
                <td style="font-family: var(--font-mono); font-size: 13px">{{ message.external_id || 'N/A' }}</td>
              </tr>
              <tr>
                <td style="font-weight: 600">Status</td>
                <td><span :class="statusBadgeClass(message.status)">{{ message.status }}</span></td>
              </tr>
              <tr>
                <td style="font-weight: 600">Created At</td>
                <td>{{ formatDate(message.created_at) }}</td>
              </tr>
              <tr>
                <td style="font-weight: 600">Sent At</td>
                <td>{{ formatDate(message.sent_at) }}</td>
              </tr>
              <tr v-if="message.delivered_at">
                <td style="font-weight: 600">Delivered At</td>
                <td>{{ formatDate(message.delivered_at) }}</td>
              </tr>
              <tr v-if="message.opened_at">
                <td style="font-weight: 600">Opened At</td>
                <td>{{ formatDate(message.opened_at) }}</td>
              </tr>
              <tr v-if="message.clicked_at">
                <td style="font-weight: 600">Clicked At</td>
                <td>{{ formatDate(message.clicked_at) }}</td>
              </tr>
              <tr v-if="message.bounced_at">
                <td style="font-weight: 600">Bounced At</td>
                <td>{{ formatDate(message.bounced_at) }}</td>
              </tr>
              <tr v-if="message.complained_at">
                <td style="font-weight: 600">Complained At</td>
                <td>{{ formatDate(message.complained_at) }}</td>
              </tr>
              <tr v-if="message.unsubscribed_at">
                <td style="font-weight: 600">Unsubscribed At</td>
                <td>{{ formatDate(message.unsubscribed_at) }}</td>
              </tr>
              <tr>
                <td style="font-weight: 600">Provider</td>
                <td>
                  <span v-if="message.provider" class="badge badge-info">{{ message.provider }}</span>
                  <span v-else style="color: var(--text-muted)">N/A</span>
                </td>
              </tr>
              <tr>
                <td style="font-weight: 600">SMTP Server</td>
                <td style="font-family: var(--font-mono); font-size: 13px">{{ message.smtp_hostname || 'N/A' }}</td>
              </tr>
              <tr>
                <td style="font-weight: 600">Template</td>
                <td>{{ message.template_name || 'N/A' }}</td>
              </tr>
              <tr v-if="message.error_message">
                <td style="font-weight: 600">Error</td>
                <td style="color: var(--danger-text)">{{ message.error_message }}</td>
              </tr>
              <tr v-if="events.length">
                <td style="font-weight: 600">
                  Timeline
                  <span v-if="streaming" class="live-indicator" title="Streaming new events">live</span>
                </td>
                <td>
                  <div class="timeline">
                    <span
                      v-for="ev in events"
                      :key="ev.id"
                      class="badge event-badge"
                      :class="eventMeta(ev.type).cls"
                      :title="`${formatDate(ev.occurred_at)}${ev.url ? ' · ' + ev.url : ''}${ev.error ? ' · ' + ev.error : ''}`"
                    >
                      <span class="mdi" :class="eventMeta(ev.type).icon"></span>
                      {{ ev.type }}
                      <span v-if="eventClassification(ev)" class="event-classification">
                        · {{ eventClassification(ev).replace(/_/g, ' ') }}
                      </span>
                    </span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card">
        <!-- Privacy notice in place of the preview: the instance hides bodies. -->
        <template v-if="redacted">
          <div class="card-body" style="text-align: center; padding: 48px 24px;">
            <span class="mdi mdi-eye-off" style="font-size: 48px; color: var(--text-muted); display: block; margin-bottom: 12px;"></span>
            <h3 style="margin: 0 0 8px; color: var(--text-secondary);">Content Hidden</h3>
            <p style="color: var(--text-muted); margin: 0;">Email body content is not available, email content disabled for privacy.</p>
          </div>
        </template>
        <template v-else>
          <div class="card-header">
            <div class="tabs" style="margin-bottom: 0;">
              <button class="tab" :class="{ active: activeTab === 'html' }" @click="activeTab = 'html'">HTML Preview</button>
              <button class="tab" :class="{ active: activeTab === 'mjml' }" @click="activeTab = 'mjml'">MJML Source</button>
              <button class="tab" :class="{ active: activeTab === 'data' }" @click="activeTab = 'data'">Template Data</button>
              <button class="tab" :class="{ active: activeTab === 'meta' }" @click="activeTab = 'meta'">Metadata</button>
            </div>
          </div>
          <div class="card-body">
            <!-- HTML Preview: sandboxed — no scripts, no same-origin access. -->
            <template v-if="activeTab === 'html'">
              <iframe
                v-if="hasHtml"
                :srcdoc="content?.html_body"
                sandbox=""
                style="width: 100%; min-height: 400px; border: 1px solid var(--border-primary); border-radius: var(--radius)"
              ></iframe>
              <div v-else class="empty-state">
                <h3>No HTML body</h3>
                <p>This message has no stored HTML content.</p>
              </div>
            </template>

            <!-- MJML: honestly not captured — only the rendered HTML is stored. -->
            <template v-else-if="activeTab === 'mjml'">
              <div class="empty-state">
                <h3>No MJML source</h3>
                <p>MJML source is not captured — ReForwarder stores the rendered HTML at send time.</p>
              </div>
            </template>

            <template v-else-if="activeTab === 'data'">
              <div class="code-head">
                <span class="code-head__label">Render data</span>
                <button class="copy-btn" type="button" @click="copy('data', templateDataJson)">
                  <span class="mdi" :class="copiedKey === 'data' ? 'mdi-check' : 'mdi-content-copy'"></span>
                  {{ copiedKey === 'data' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="code-block">{{ templateDataJson }}</pre>
            </template>

            <template v-else>
              <div class="code-head">
                <span class="code-head__label">Headers, identifiers &amp; send params</span>
                <button class="copy-btn" type="button" @click="copy('meta', metadataJson)">
                  <span class="mdi" :class="copiedKey === 'meta' ? 'mdi-check' : 'mdi-content-copy'"></span>
                  {{ copiedKey === 'meta' ? 'Copied' : 'Copy' }}
                </button>
              </div>
              <pre class="code-block">{{ metadataJson }}</pre>
            </template>
          </div>
        </template>
      </div>
    </template>

    <div v-else class="empty-state">
      <h3>Message not found</h3>
      <p>The message you are looking for does not exist or is not part of this workspace.</p>
    </div>
  </div>
</template>

<style scoped>
.timeline {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.event-badge {
  gap: 4px;
  font-size: 11px;
  padding: 2px 8px;
}

.event-badge .mdi {
  font-size: 13px;
}

/* Classification stamped by the ingestion pipeline onto the event (SF-53). */
.event-classification {
  font-weight: 500;
  opacity: 0.85;
}

/* Live-tail marker on the timeline row (SF-56). */
.live-indicator {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 7px;
  border-radius: 999px;
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.4px;
  text-transform: uppercase;
  background: var(--success-50, #e6f6ec);
  color: var(--success-text, #157347);
}

/* Accent badge for clicks (the signature ramp). */
.badge-accent {
  background: var(--primary-50);
  color: var(--primary-600);
}

.code-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.code-head__label {
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-tertiary);
}

.copy-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-tertiary);
  color: var(--text-secondary);
  font-size: 12px;
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
}

.copy-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.copy-btn .mdi {
  font-size: 14px;
}
</style>
