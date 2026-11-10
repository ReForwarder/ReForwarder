<!--
  Copyright (c) 2026 Jonas Kaninda.

  This file is part of Posta.

  This program is free software: you can redistribute it and/or modify
  it under the terms of the GNU Affero General Public License as published by
  the Free Software Foundation, either version 3 of the License, or
  (at your option) any later version.

  This program is distributed in the hope that it will be useful,
  but WITHOUT ANY WARRANTY; without even the implied warranty of
  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
  GNU Affero General Public License for more details.

  You should have received a copy of the GNU Affero General Public License
  along with this program. If not, see <https://www.gnu.org/licenses/>.

  SPDX-License-Identifier: AGPL-3.0-or-later
-->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { mailApi, formatBytes, hasFlag } from '../../api/mail'
import type { MailFolder, MailMessage, Mailbox } from '../../api/mail'
import { mailComposeApi } from '../../api/mailCompose'
import type { ComposeReplyFrame } from '../../api/mailCompose'
import { apiMessage } from '../../composables/apiError'
import { useConfirm } from '../../composables/useConfirm'
import { useNotificationStore } from '../../stores/notification'

// Webmail message view (OB-5 W4, ADR-005 decisions 2 + 5): metadata header,
// sanitized HTML in a sandboxed iframe (the Logs MessageDetail srcdoc +
// sandbox="" pattern), attachments as authenticated downloads, and the
// reply/reply-all/forward/flag/delete/move controls.
//
// Remote content: BLOCKED by default, twice over. The server sanitizer strips
// remote-loading references before the body is ever served
// (internal/handlers/mail_html.go — http(s)/cid: img sources removed because a
// sandbox="" iframe alone does NOT block network fetches), and this view
// additionally pins a Content-Security-Policy <meta> into the iframe document
// that refuses remote subresources unless the user opts in per message via
// "Load remote content" (the opt-in relaxes img-src only; scripts stay dead —
// sandbox="" AND script-src 'none'). Note: with today's server payload the
// remote references are already gone when we render, so the opt-in is
// belt-and-braces; it becomes the load path the moment the backend ships a
// remote-keeping variant of the message body.

const COMPOSE_HANDOFF_KEY = 'posta_mail_compose_prefill'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const { confirm } = useConfirm()

const loading = ref(true)
const notFound = ref(false)
const message = ref<MailMessage | null>(null)
const folders = ref<MailFolder[]>([])
const mailbox = ref<Mailbox | null>(null)
const moveTarget = ref('')
const busy = ref(false)

// Per-message remote-content opt-in: keyed by Message-ID so navigating between
// messages re-arms the block by default.
const remoteAllowed = ref<Record<string, boolean>>({})

const folderId = computed(() => Number(route.params.folderId) || 0)
const uid = computed(() => Number(route.params.uid) || 0)

const currentFolder = computed(() => folders.value.find((f) => f.id === folderId.value) ?? null)
const moveTargets = computed(() => folders.value.filter((f) => f.id !== folderId.value))

const remoteOptedIn = computed(() => {
  if (!message.value) return false
  return !!remoteAllowed.value[message.value.message_id]
})

/** The srcdoc document: a CSP meta ahead of the sanitized HTML. Default
 * refuses remote subresources and scripts; the opt-in allows remote images
 * (the tracking-beacon cost of that is the user's informed choice, ADR-005
 * decision 5) while scripts stay refused either way. */
const iframeDoc = computed(() => {
  if (!message.value?.body.has_html) return ''
  const imgSrc = remoteOptedIn.value ? "https: http: data: 'unsafe-inline'" : "data:"
  const csp = `default-src 'none'; img-src ${imgSrc}; style-src 'unsafe-inline'; font-src data:; script-src 'none'; frame-src 'none'; connect-src 'none'`
  return `<!DOCTYPE html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"></head><body style="margin:0;">${message.value.body.html}</body></html>`
})

function allowRemote() {
  if (!message.value) return
  remoteAllowed.value = { ...remoteAllowed.value, [message.value.message_id]: true }
}

async function loadFoldersFor(mailboxId: number) {
  const res = await mailApi.folders(mailboxId)
  return res.data.data ?? []
}

/** The message view needs the owning mailbox id for the reply-frame endpoint;
 * the (folder, uid) pair already proves access, so the first mailbox whose
 * folder list contains the folder id is the owner. */
async function resolveMailbox(): Promise<Mailbox | null> {
  const mbs = (await mailApi.mailboxes()).data.data ?? []
  for (const mb of mbs) {
    const fs = await loadFoldersFor(mb.id)
    if (fs.some((f) => f.id === folderId.value)) {
      folders.value = fs
      return mb
    }
  }
  folders.value = []
  return null
}

async function loadMessage() {
  const res = await mailApi.message(uid.value, folderId.value)
  message.value = res.data.data
}

async function load() {
  loading.value = true
  notFound.value = false
  try {
    mailbox.value = await resolveMailbox()
    if (!mailbox.value) {
      notFound.value = true
      return
    }
    await loadMessage()
  } catch (e: any) {
    if (e?.response?.status === 404) notFound.value = true
    else notify.error(apiMessage(e, 'Failed to load message'))
  } finally {
    loading.value = false
  }
}

function backToList() {
  router.push({ name: 'mail-folder', params: { folderId: String(folderId.value) } })
}

async function updateFlag(patch: { seen?: boolean; flagged?: boolean }) {
  if (!message.value) return
  try {
    const res = await mailApi.updateFlags(message.value.uid, folderId.value, patch)
    if (message.value) {
      message.value.flags = res.data.data.flags
      message.value.mod_seq = res.data.data.mod_seq
      const names = new Set(message.value.flags_list)
      for (const [flag, maskFlag] of [['seen', 'seen'], ['flagged', 'flagged']] as const) {
        if (patch[flag] === true) names.add(maskFlag)
        if (patch[flag] === false) names.delete(maskFlag)
      }
      message.value.flags_list = [...names]
    }
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to update flags'))
  }
}

async function deleteMessage() {
  if (!message.value) return
  const okToDelete = await confirm({
    title: 'Delete message',
    message: 'The message is marked \\Deleted and leaves permanently at folder expunge.',
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!okToDelete) return
  busy.value = true
  try {
    await mailApi.deleteMessage(message.value.uid, folderId.value)
    notify.success('Message deleted')
    backToList()
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to delete message'))
  } finally {
    busy.value = false
  }
}

async function moveMessage() {
  if (!message.value || !moveTarget.value) return
  const target = Number(moveTarget.value)
  if (!target || target === folderId.value) return
  busy.value = true
  try {
    await mailApi.moveMessage(message.value.uid, folderId.value, target)
    notify.success('Message moved')
    backToList()
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to move message'))
  } finally {
    busy.value = false
  }
}

/** Reply/reply-all/forward: fetch the composer prefill frame, hand it to
 * Compose.vue through a sessionStorage handoff (the frame carries the quoted
 * text and the original's sanitized-for-render HTML), then open the composer. */
async function openReply(mode: 'reply' | 'reply_all' | 'forward') {
  if (!message.value || !mailbox.value) return
  busy.value = true
  try {
    const res = await mailComposeApi.replyFrame(message.value.uid, folderId.value, mailbox.value.id, mode)
    const frame: ComposeReplyFrame = res.data.data
    sessionStorage.setItem(COMPOSE_HANDOFF_KEY, JSON.stringify(frame))
    router.push({ name: 'mail-compose', query: { prefill: frame.mode } })
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to prepare the reply'))
  } finally {
    busy.value = false
  }
}

function messageTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString()
}

onMounted(load)

// Navigating between messages (folder/uid change) reloads the view.
watch([folderId, uid], () => {
  if (folderId.value && uid.value) load()
})
</script>

<template>
  <div class="message-page">
    <div v-if="loading" class="loading-page"><div class="spinner"></div></div>

    <div v-else-if="notFound" class="empty-state">
      <h3>Message not found</h3>
      <p>The message does not exist, or it is not one of your own.</p>
      <button class="btn btn-secondary" type="button" @click="backToList">Back to mailbox</button>
    </div>

    <template v-else-if="message">
      <div class="message-toolbar">
        <button class="btn btn-ghost" type="button" @click="backToList">
          <span class="mdi mdi-arrow-left"></span> Back
        </button>
        <div class="message-toolbar-actions">
          <button class="btn btn-secondary btn-sm" type="button" @click="updateFlag({ flagged: !hasFlag(message, 'flagged') })">
            <span class="mdi" :class="hasFlag(message, 'flagged') ? 'mdi-star' : 'mdi-star-outline'"></span>
            {{ hasFlag(message, 'flagged') ? 'Unflag' : 'Flag' }}
          </button>
          <button class="btn btn-secondary btn-sm" type="button" @click="updateFlag({ seen: !hasFlag(message, 'seen') })">
            <span class="mdi" :class="hasFlag(message, 'seen') ? 'mdi-email-open' : 'mdi-email'"></span>
            {{ hasFlag(message, 'seen') ? 'Mark unread' : 'Mark read' }}
          </button>
          <select v-model="moveTarget" class="form-select message-move-select" aria-label="Move message to folder" :disabled="busy" @change="moveMessage">
            <option value="">Move to…</option>
            <option v-for="f in moveTargets" :key="f.id" :value="String(f.id)">{{ f.name }}</option>
          </select>
          <button class="btn btn-danger btn-sm" type="button" :disabled="busy" @click="deleteMessage">
            <span class="mdi mdi-delete-outline"></span> Delete
          </button>
        </div>
      </div>

      <div class="card message-card">
        <div class="card-body">
          <h2 class="message-subject">{{ message.subject || '(no subject)' }}</h2>
          <div class="message-meta">
            <div class="message-meta-row"><span class="message-meta-label">From</span><span>{{ message.from }}</span></div>
            <div class="message-meta-row"><span class="message-meta-label">To</span><span>{{ message.to }}</span></div>
            <div v-if="message.cc" class="message-meta-row"><span class="message-meta-label">Cc</span><span>{{ message.cc }}</span></div>
            <div class="message-meta-row"><span class="message-meta-label">Date</span><span>{{ messageTime(message.date) }}</span></div>
          </div>

          <div class="message-reply-row">
            <button class="btn btn-primary btn-sm" type="button" :disabled="busy" @click="openReply('reply')">
              <span class="mdi mdi-reply"></span> Reply
            </button>
            <button class="btn btn-secondary btn-sm" type="button" :disabled="busy" @click="openReply('reply_all')">
              <span class="mdi mdi-reply-all"></span> Reply all
            </button>
            <button class="btn btn-secondary btn-sm" type="button" :disabled="busy" @click="openReply('forward')">
              <span class="mdi mdi-forward"></span> Forward
            </button>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-body message-body">
          <!-- Remote content is blocked by default (ADR-005 decision 5): the
               server sanitizer strips remote references and the iframe CSP
               refuses the rest. The opt-in is per message. -->
          <div v-if="message.body.has_html" class="message-remote-bar">
            <span class="text-sm text-muted">
              <span class="mdi mdi-shield-lock-outline"></span>
              {{ remoteOptedIn ? 'Remote content allowed for this message.' : 'Remote content is blocked for this message.' }}
            </span>
            <button v-if="!remoteOptedIn" class="btn btn-secondary btn-sm" type="button" @click="allowRemote">
              Load remote content
            </button>
          </div>

          <iframe
            v-if="message.body.has_html"
            :srcdoc="iframeDoc"
            sandbox=""
            class="message-iframe"
            title="Message body"
          ></iframe>
          <pre v-else class="message-text">{{ message.body.text || '(empty message)' }}</pre>

          <div v-if="message.attachments.length > 0" class="message-attachments">
            <h3>Attachments ({{ message.attachments.length }})</h3>
            <ul class="message-attachment-list">
              <li v-for="att in message.attachments" :key="att.index" class="message-attachment">
                <a
                  class="message-attachment-link"
                  :href="mailApi.attachmentUrl(message.uid, att.index, folderId)"
                  target="_blank"
                  rel="noopener"
                  :download="att.name || `attachment-${att.index}`"
                >
                  <span class="mdi mdi-paperclip"></span>
                  {{ att.name || `attachment-${att.index}` }}
                </a>
                <span class="text-sm text-muted">{{ att.content_type || 'file' }} · {{ formatBytes(att.size) }}</span>
              </li>
            </ul>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.message-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.message-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.message-toolbar-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.message-move-select {
  width: auto;
  min-width: 130px;
}

.message-subject {
  margin: 0 0 12px;
  font-size: 18px;
  overflow-wrap: anywhere;
}

.message-meta {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 12px;
}

.message-meta-row {
  display: flex;
  gap: 8px;
  font-size: 13px;
  overflow-wrap: anywhere;
}

.message-meta-label {
  width: 44px;
  flex-shrink: 0;
  color: var(--text-muted);
}

.message-reply-row {
  display: flex;
  gap: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--border-primary);
}

.message-remote-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 12px;
  margin-bottom: 12px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-tertiary);
}

/* The Logs content-iframe pattern: srcdoc + sandbox="". No scripts, no
   same-origin access; the CSP meta inside the document blocks remote loads. */
.message-iframe {
  width: 100%;
  min-height: 400px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: #fff;
}

.message-text {
  margin: 0;
  padding: 16px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-tertiary);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-family: inherit;
  font-size: 14px;
}

.message-attachments {
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px solid var(--border-primary);
}

.message-attachments h3 {
  margin: 0 0 8px;
  font-size: 13px;
}

.message-attachment-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.message-attachment {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}

.message-attachment-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--primary-600);
  font-weight: 500;
  overflow-wrap: anywhere;
}
</style>
