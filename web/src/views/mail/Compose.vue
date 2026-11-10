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
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { mailApi } from '../../api/mail'
import type { Mailbox } from '../../api/mail'
import { mailComposeApi } from '../../api/mailCompose'
import type { ComposeAttachmentRef, ComposeMessageInput, ComposeReplyFrame } from '../../api/mailCompose'
import { apiMessage } from '../../composables/apiError'
import { useNotificationStore } from '../../stores/notification'

// Webmail composer (OB-5 W4, ADR-005 decision 2): to/cc/bcc + subject + body
// with staged attachments (each upload passes the ADR-006 scan gate — a
// clamd-positive is refused 409 and surfaced verbatim) and two exits: send
// (Sent copy + outbound pipeline, ADR-001) and save-draft (Drafts folder).
// Reply/reply-all/forward prefills arrive through the sessionStorage handoff
// MessageView.vue writes after fetching the reply-frame endpoint.

const COMPOSE_HANDOFF_KEY = 'posta_mail_compose_prefill'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()

const loading = ref(true)
const mailboxes = ref<Mailbox[]>([])
const mailboxId = ref<number>(0)

const from = ref('')
const to = ref('')
const cc = ref('')
const bccVisible = ref(false)
const bcc = ref('')
const subject = ref('')
const body = ref('')
const signature = ref('')

const attachments = ref<ComposeAttachmentRef[]>([])
const uploading = ref(false)
const sending = ref(false)
const draftSaving = ref(false)
const draftUid = ref<number | null>(null)
const draftFolderId = ref<number | null>(null)

const toInvalid = ref(false)
const fromInvalid = ref(false)

const selectedMailbox = computed(() => mailboxes.value.find((m) => m.id === mailboxId.value) ?? null)

function splitAddresses(raw: string): string[] {
  return raw
    .split(/[,;]+/)
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
}

const SIGNATURE_KEY = 'posta_mail_signature'

function loadSignature() {
  signature.value = localStorage.getItem(SIGNATURE_KEY) ?? ''
}

function insertSignature() {
  if (!signature.value.trim()) return
  body.value = body.value.trimEnd() + '\n\n' + signature.value
}

async function loadMailboxes() {
  const res = await mailApi.mailboxes()
  mailboxes.value = (res.data.data ?? []).filter((m) => m.status === 'active')
  mailboxId.value = mailboxes.value[0]?.id ?? 0
  from.value = mailboxes.value[0]?.name ?? ''
}

function applyMailbox() {
  const mb = selectedMailbox.value
  if (mb) from.value = mb.name
}

/** Applies the reply/forward prefill frame (ComposeReplyFrame) written by
 * MessageView.vue. */
function applyPrefill(frame: ComposeReplyFrame) {
  mailboxId.value = frame.mailbox_id
  const mb = mailboxes.value.find((m) => m.id === frame.mailbox_id)
  from.value = mb?.name ?? from.value
  to.value = frame.to.join(', ')
  if (frame.cc.length > 0) {
    cc.value = frame.cc.join(', ')
  }
  subject.value = frame.subject
  const header =
    frame.mode === 'forward'
      ? `---------- Forwarded message ----------\nFrom: ${frame.original.from}\nDate: ${frame.original.date}\nSubject: ${frame.original.subject}\n\n`
      : `On ${frame.original.date}, ${frame.original.from} wrote:\n`
  const quote = frame.quote_text
    .split('\n')
    .map((line) => (line.startsWith('>') ? line : `> ${line}`))
    .join('\n')
  body.value = `\n\n${header}${quote}`
  draftUid.value = null
}

function readPrefill(): boolean {
  const raw = sessionStorage.getItem(COMPOSE_HANDOFF_KEY)
  if (!raw) return false
  sessionStorage.removeItem(COMPOSE_HANDOFF_KEY)
  try {
    const frame = JSON.parse(raw) as ComposeReplyFrame
    if (frame && Array.isArray(frame.to) && typeof frame.subject === 'string') {
      applyPrefill(frame)
      return true
    }
  } catch {
    // Malformed handoff: fall through to a blank composer.
  }
  return false
}

function toInput(): ComposeMessageInput {
  const input: ComposeMessageInput = {
    mailbox_id: mailboxId.value || undefined,
    from: from.value.trim(),
    to: splitAddresses(to.value),
    subject: subject.value,
    text: body.value,
  }
  if (cc.value.trim()) input.cc = splitAddresses(cc.value)
  if (bcc.value.trim()) input.bcc = splitAddresses(bcc.value)
  if (attachments.value.length > 0) {
    input.attachments = attachments.value.map((a) => ({
      blob_hash: a.blob_hash,
      name: a.name,
      content_type: a.content_type,
    }))
  }
  return input
}

/** Client-side validation before the API sees the request: a from address and
 * at least one recipient (the backend refuses both conditions too). */
function validate(): boolean {
  toInvalid.value = splitAddresses(to.value).length === 0
  fromInvalid.value = from.value.trim().length === 0
  return !toInvalid.value && !fromInvalid.value
}

async function uploadAttachments(files: FileList | null) {
  if (!files || files.length === 0) return
  uploading.value = true
  try {
    for (const file of Array.from(files)) {
      const res = await mailComposeApi.uploadAttachment(file)
      for (const ref of res.data.data ?? []) attachments.value.push(ref)
    }
    notify.success('Attachment uploaded')
  } catch (e: any) {
    // The ADR-006 verdict rides the error envelope: a clamd-positive is a 409
    // naming the signature — surface it verbatim, never paraphrase it away.
    notify.error(apiMessage(e, 'Attachment was refused'))
  } finally {
    uploading.value = false
    if (fileInput.value) fileInput.value.value = ''
  }
}

const fileInput = ref<HTMLInputElement | null>(null)

function removeAttachment(index: number) {
  attachments.value = attachments.value.filter((_, i) => i !== index)
}

async function send() {
  if (!validate()) {
    notify.error('A from address and at least one recipient are required')
    return
  }
  sending.value = true
  try {
    if (draftUid.value !== null && mailboxId.value) {
      // Editing a stored draft: replace it, then send the stored copy.
      await mailComposeApi.updateDraft(draftUid.value, mailboxId.value, toInput())
      await mailComposeApi.sendDraft(draftUid.value, mailboxId.value)
    } else {
      await mailComposeApi.send(toInput())
    }
    notify.success('Message sent')
    sessionStorage.removeItem(COMPOSE_HANDOFF_KEY)
    router.push({ name: 'mail' })
  } catch (e: any) {
    notify.error(apiMessage(e, 'Sending failed'))
  } finally {
    sending.value = false
  }
}

async function saveDraft() {
  if (fromInvalid.value || from.value.trim().length === 0) {
    notify.error('A from address is required to save a draft')
    return
  }
  draftSaving.value = true
  try {
    if (draftUid.value !== null && mailboxId.value) {
      const res = await mailComposeApi.updateDraft(draftUid.value, mailboxId.value, toInput())
      draftUid.value = res.data.data.uid
    } else {
      const res = await mailComposeApi.saveDraft(toInput())
      draftUid.value = res.data.data.uid
      draftFolderId.value = res.data.data.folder_id
    }
    notify.success('Draft saved')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to save the draft'))
  } finally {
    draftSaving.value = false
  }
}

onMounted(async () => {
  try {
    loading.value = true
    await loadMailboxes()
    loadSignature()
    readPrefill()
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to load the composer'))
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="compose-page">
    <div class="page-header">
      <div>
        <h1>Compose</h1>
        <p class="page-description">Send from your hosted mailbox. Recipients may be comma- or semicolon-separated.</p>
      </div>
      <router-link class="btn btn-ghost" :to="{ name: 'mail' }">Cancel</router-link>
    </div>

    <div v-if="loading" class="loading-page"><div class="spinner"></div></div>

    <div v-else-if="mailboxes.length === 0" class="empty-state">
      <h3>No mailbox</h3>
      <p>Sending needs an active hosted mailbox. An administrator can provision one under Admin · Mailboxes.</p>
    </div>

    <div v-else class="card">
      <div class="card-body">
        <form class="compose-form" @submit.prevent="send">
          <div class="form-group">
            <label class="form-label" for="compose-from">From</label>
            <select id="compose-from" v-model="mailboxId" class="form-select" :class="{ 'form-error': fromInvalid }" @change="applyMailbox">
              <option v-for="mb in mailboxes" :key="mb.id" :value="mb.id">{{ mb.name }}</option>
            </select>
          </div>

          <div class="form-group">
            <label class="form-label" for="compose-to">To</label>
            <input
              id="compose-to"
              v-model="to"
              class="form-input"
              :class="{ 'form-error': toInvalid }"
              type="text"
              placeholder="ada@example.test, grace@example.test"
              autocomplete="off"
            />
          </div>

          <div class="form-group compose-cc-row">
            <button v-if="!bccVisible" class="btn btn-ghost btn-sm" type="button" @click="bccVisible = true">Add Bcc</button>
          </div>
          <div class="form-group">
            <label class="form-label" for="compose-cc">Cc</label>
            <input id="compose-cc" v-model="cc" class="form-input" type="text" autocomplete="off" />
          </div>
          <div v-if="bccVisible" class="form-group">
            <label class="form-label" for="compose-bcc">Bcc</label>
            <input id="compose-bcc" v-model="bcc" class="form-input" type="text" autocomplete="off" />
          </div>

          <div class="form-group">
            <label class="form-label" for="compose-subject">Subject</label>
            <input id="compose-subject" v-model="subject" class="form-input" type="text" autocomplete="off" />
          </div>

          <div class="form-group">
            <div class="justify-between items-center compose-body-label">
              <label class="form-label" for="compose-body">Body</label>
              <button
                v-if="signature.trim()"
                class="btn btn-ghost btn-sm"
                type="button"
                title="Insert your signature (set under Mail Settings)"
                @click="insertSignature"
              >
                <span class="mdi mdi-signature-text"></span> Insert signature
              </button>
            </div>
            <textarea id="compose-body" v-model="body" class="form-textarea compose-body" rows="12"></textarea>
          </div>

          <div class="form-group">
            <label class="form-label" for="compose-attachments">Attachments</label>
            <input
              id="compose-attachments"
              ref="fileInput"
              class="form-input"
              type="file"
              multiple
              :disabled="uploading"
              @change="uploadAttachments(($event.target as HTMLInputElement).files)"
            />
            <p class="form-hint">Each attachment is malware-scanned (clamd) before it can be sent. A refused upload names the detection.</p>
            <ul v-if="attachments.length > 0" class="compose-attachment-list">
              <li v-for="(att, i) in attachments" :key="att.blob_hash" class="compose-attachment">
                <span class="mdi mdi-paperclip"></span>
                <span class="compose-attachment-name">{{ att.name }}</span>
                <span class="text-sm text-muted">{{ att.scan_state === 'clean' ? 'scanned clean' : 'scanner unavailable' }}</span>
                <button class="btn btn-ghost btn-sm compose-attachment-remove" type="button" :aria-label="`Remove ${att.name}`" @click="removeAttachment(i)">
                  <span class="mdi mdi-close"></span>
                </button>
              </li>
            </ul>
          </div>

          <div class="compose-actions">
            <button class="btn btn-primary" type="submit" :disabled="sending || uploading">
              <span class="mdi mdi-send"></span>
              {{ sending ? 'Sending…' : 'Send' }}
            </button>
            <button class="btn btn-secondary" type="button" :disabled="draftSaving || uploading" @click="saveDraft">
              {{ draftSaving ? 'Saving…' : 'Save draft' }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </div>
</template>

<style scoped>
.compose-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.compose-cc-row {
  margin-bottom: -8px;
}

.compose-body-label {
  margin-bottom: 4px;
}

.compose-body {
  font-family: inherit;
  resize: vertical;
}

.compose-attachment-list {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.compose-attachment {
  display: flex;
  align-items: center;
  gap: 8px;
}

.compose-attachment-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 320px;
}

.compose-attachment-remove {
  margin-left: auto;
}

.compose-actions {
  display: flex;
  gap: 8px;
  padding-top: 8px;
}
</style>
