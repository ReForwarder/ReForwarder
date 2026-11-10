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
import { mailApi, formatBytes } from '../../api/mail'
import type { Mailbox } from '../../api/mail'
import { authApi } from '../../api/auth'
import { useAuthStore } from '../../stores/auth'
import { apiMessage } from '../../composables/apiError'
import { useNotificationStore } from '../../stores/notification'

// Mailbox settings (OB-5 W4, ADR-005 decision 3, self-service half): the
// display name (through the existing PUT /users/me profile endpoint), the
// compose signature, the per-mailbox quota and usage display (ADR-003 §5
// counters), and the link into the existing Profile card that manages app
// passwords (ADR-004).
//
// Documented limitation: the signature has no server-side storage in this
// wave's endpoint surface (no /mail settings handler exists — the W1–W3
// endpoint shapes cover mailboxes, messages and compose only), so it is kept
// per browser (localStorage) and inserted into the composer on demand.

const SIGNATURE_KEY = 'posta_mail_signature'

const auth = useAuthStore()
const notify = useNotificationStore()

const loading = ref(true)
const savingName = ref(false)

const displayName = ref('')
const signature = ref('')
const mailboxes = ref<Mailbox[]>([])

const nameDirty = computed(() => displayName.value.trim() !== (auth.user?.name ?? ''))
const signatureDirty = computed(() => signature.value !== (localStorage.getItem(SIGNATURE_KEY) ?? ''))

function quotaPercent(mb: Mailbox): number | null {
  if (mb.quota_bytes <= 0) return null
  return Math.min(100, Math.round((mb.used_bytes / mb.quota_bytes) * 100))
}

function messagePercent(mb: Mailbox): number | null {
  if (mb.quota_messages <= 0) return null
  return Math.min(100, Math.round((mb.message_count / mb.quota_messages) * 100))
}

async function load() {
  loading.value = true
  try {
    displayName.value = auth.user?.name ?? ''
    try {
      const res = await mailApi.mailboxes()
      mailboxes.value = res.data.data ?? []
    } catch (e: any) {
      if (e?.response?.status !== 503) notify.error(apiMessage(e, 'Failed to load mailboxes'))
    }
    signature.value = localStorage.getItem(SIGNATURE_KEY) ?? ''
  } finally {
    loading.value = false
  }
}

async function saveName() {
  const name = displayName.value.trim()
  if (!name) {
    notify.error('Display name cannot be empty')
    return
  }
  savingName.value = true
  try {
    const res = await authApi.updateProfile({ name })
    auth.setUser(res.data.data)
    notify.success('Display name updated')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to update the display name'))
  } finally {
    savingName.value = false
  }
}

function saveSignature() {
  localStorage.setItem(SIGNATURE_KEY, signature.value)
  notify.success('Signature saved on this device')
}

onMounted(load)
</script>

<template>
  <div class="settings-page">
    <div class="page-header">
      <div>
        <h1>Mail Settings</h1>
        <p class="page-description">Identity, signature, and storage for your hosted mailboxes.</p>
      </div>
      <router-link class="btn btn-ghost" :to="{ name: 'mail' }">Back to mailbox</router-link>
    </div>

    <div v-if="loading" class="loading-page"><div class="spinner"></div></div>

    <template v-else>
      <div class="card">
        <div class="card-header"><h2>Identity</h2></div>
        <div class="card-body">
          <form class="settings-form" @submit.prevent="saveName">
            <div class="form-group">
              <label class="form-label" for="mail-display-name">Display name</label>
              <input id="mail-display-name" v-model="displayName" class="form-input" type="text" autocomplete="name" />
              <p class="form-hint">Shown to recipients of mail you send from your hosted mailboxes.</p>
            </div>
            <button class="btn btn-primary" type="submit" :disabled="savingName || !nameDirty">
              {{ savingName ? 'Saving…' : 'Save display name' }}
            </button>
          </form>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h2>Signature</h2></div>
        <div class="card-body">
          <form class="settings-form" @submit.prevent="saveSignature">
            <div class="form-group">
              <label class="form-label" for="mail-signature">Compose signature</label>
              <textarea id="mail-signature" v-model="signature" class="form-textarea" rows="4" placeholder="— Your name"></textarea>
              <p class="form-hint">
                Kept on this device (this deployment stores no server-side signature yet) and inserted
                from the composer's "Insert signature" button.
              </p>
            </div>
            <button class="btn btn-primary" type="submit" :disabled="!signatureDirty">
              Save signature
            </button>
          </form>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h2>Storage</h2></div>
        <div class="card-body">
          <div v-if="mailboxes.length === 0" class="text-muted">No hosted mailboxes on this account.</div>
          <div v-for="mb in mailboxes" :key="mb.id" class="mail-quota-card">
            <div class="justify-between items-center">
              <strong>{{ mb.name }}</strong>
              <span class="badge" :class="mb.status === 'active' ? 'badge-success' : 'badge-warning'">{{ mb.status }}</span>
            </div>
            <dl class="mail-quota-stats">
              <div class="mail-quota-stat">
                <dt>Storage</dt>
                <dd>
                  {{ formatBytes(mb.used_bytes) }}
                  <template v-if="mb.quota_bytes > 0"> of {{ formatBytes(mb.quota_bytes) }} ({{ quotaPercent(mb) }}%)</template>
                  <template v-else> (no limit)</template>
                </dd>
              </div>
              <div class="mail-quota-stat">
                <dt>Messages</dt>
                <dd>
                  {{ mb.message_count }}
                  <template v-if="mb.quota_messages > 0"> of {{ mb.quota_messages }} ({{ messagePercent(mb) }}%)</template>
                  <template v-else> (no limit)</template>
                </dd>
              </div>
              <div class="mail-quota-stat">
                <dt>Stored (deduplicated)</dt>
                <dd>{{ formatBytes(mb.blob_bytes) }} across {{ mb.distinct_blobs }} blobs</dd>
              </div>
            </dl>
            <div v-if="quotaPercent(mb) !== null" class="mail-quota-bar"><div class="mail-quota-fill" :style="{ width: quotaPercent(mb) + '%' }"></div></div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-header"><h2>Mail client access</h2></div>
        <div class="card-body">
          <p class="text-muted">
            Connect IMAP/POP3 clients with an app password — create and revoke them from the
            App Passwords card in your profile.
          </p>
          <router-link class="btn btn-secondary" :to="{ name: 'profile' }">
            <span class="mdi mdi-key-variant"></span> Manage app passwords
          </router-link>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.settings-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.settings-form {
  max-width: 480px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  align-items: flex-start;
}

.settings-form .form-group {
  width: 100%;
}

.mail-quota-card {
  padding: 12px 0;
  border-bottom: 1px solid var(--border-primary);
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.mail-quota-card:last-child {
  border-bottom: none;
}

.mail-quota-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 24px;
  margin: 0;
}

.mail-quota-stat dt {
  font-size: 12px;
  color: var(--text-muted);
}

.mail-quota-stat dd {
  margin: 2px 0 0;
  font-size: 14px;
}

.mail-quota-bar {
  height: 6px;
  border-radius: 999px;
  background: var(--bg-tertiary);
  overflow: hidden;
  max-width: 360px;
}

.mail-quota-fill {
  height: 100%;
  border-radius: 999px;
  background: var(--primary-600);
}
</style>
