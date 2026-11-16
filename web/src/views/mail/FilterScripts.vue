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
// SF-14 (the ADR-006 amendment): per-mailbox filter scripts — the webmail
// management UI. Scripts are evaluated at mailbox delivery in priority
// order; the first match wins. The bounded operations: fileinto, redirect,
// discard, keep.
import { computed, onMounted, ref } from 'vue'
import { listFilterScripts, createFilterScript, patchFilterScript, deleteFilterScript } from '../../api/filterScripts'
import { mailApi } from '../../api/mail'
import type { FilterScript, FilterScriptAction, FilterScriptCondition } from '../../api/filterScripts'
import { useNotificationStore } from '../../stores/notification'

const notify = useNotificationStore()

const loading = ref(true)
const mailboxes = ref<{ id: number; name: string }[]>([])
const mailbox = ref(0)
const scripts = ref<FilterScript[]>([])
const creating = ref(false)
const busyId = ref(0)

const newName = ref('')
const newPriority = ref(100)
const newConditionField = ref('subject')
const newConditionOperator = ref('contains')
const newConditionValue = ref('')
const newActions = ref<FilterScriptAction[]>([{ type: 'keep' }])

const canSave = computed(() =>
  newName.value.trim() !== '' &&
  newConditionValue.value.trim() !== '' &&
  newActions.value.length > 0
)

onMounted(async () => {
  loading.value = true
  try {
    const { data } = await mailApi.mailboxes()
    mailboxes.value = (data.data || []).map((mb: { id: number; name: string }) => ({ id: mb.id, name: mb.name }))
    if (mailboxes.value.length > 0) {
      mailbox.value = mailboxes.value[0].id
      await loadScripts()
    }
  } catch {
    notify.error('Failed to load mailboxes')
  } finally {
    loading.value = false
  }
})

async function loadScripts() {
  if (!mailbox.value) return
  try {
    const { data } = await listFilterScripts(mailbox.value)
    scripts.value = data.data || []
  } catch {
    notify.error('Failed to load filter scripts')
  }
}

function onMailboxChange() {
  loadScripts()
}

async function save() {
  if (!canSave.value) return
  creating.value = true
  try {
    await createFilterScript({
      mailbox: mailbox.value,
      name: newName.value.trim(),
      priority: newPriority.value,
      enabled: true,
      conditions: [{
        field: newConditionField.value,
        operator: newConditionOperator.value,
        value: newConditionValue.value.trim(),
      }],
      actions: newActions.value,
    })
    notify.success('Filter script created')
    newName.value = ''
    newConditionValue.value = ''
    await loadScripts()
  } catch (e: any) {
    notify.error(e?.response?.data?.message || 'Failed to create the filter script')
  } finally {
    creating.value = false
  }
}

async function toggle(script: FilterScript) {
  busyId.value = script.id
  try {
    await patchFilterScript(script.id, { enabled: !script.enabled })
    script.enabled = !script.enabled
  } catch {
    notify.error('Failed to toggle the script')
  } finally {
    busyId.value = 0
  }
}

async function remove(script: FilterScript) {
  busyId.value = script.id
  try {
    await deleteFilterScript(script.id)
    notify.success('Filter script deleted')
    await loadScripts()
  } catch {
    notify.error('Failed to delete the script')
  } finally {
    busyId.value = 0
  }
}

function actionsLabel(script: FilterScript): string {
  try {
    const acts = JSON.parse(script.actions_json) as FilterScriptAction[]
    return acts.map((a) => (a.type === 'fileinto' ? `fileinto ${a.target}` : a.type)).join(', ')
  } catch {
    return script.actions_json
  }
}

function conditionsLabel(script: FilterScript): string {
  const conds = (script.conditions || []) as FilterScriptCondition[]
  return conds.map((c) => `${c.field} ${c.operator} ${String(c.value)}`).join(' AND ') || '(always)'
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Mail Filters</h1>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <template v-else>
      <div class="card">
        <div class="card-body">
          <p class="hint">
            Per-mailbox filter scripts run at delivery, after spam filtering.
            The first matching script wins; its actions file the message into a
            folder, redirect it to another address, discard it, or keep it in
            the inbox.
          </p>

          <div class="composer" v-if="mailboxes.length > 0">
            <select v-model.number="mailbox" class="form-input" aria-label="Mailbox" @change="onMailboxChange">
              <option v-for="mb in mailboxes" :key="mb.id" :value="mb.id">{{ mb.name }}</option>
            </select>
            <input v-model="newName" class="form-input" placeholder="Script name" aria-label="Script name" />
            <input v-model.number="newPriority" class="form-input narrow" type="number" aria-label="Priority" />
            <select v-model="newConditionField" class="form-input" aria-label="Condition field">
              <option value="subject">subject</option>
              <option value="from">from</option>
              <option value="text_body">text body</option>
              <option value="html_body">html body</option>
              <option value="recipients">recipients</option>
              <option value="message_size">message size</option>
            </select>
            <select v-model="newConditionOperator" class="form-input" aria-label="Operator">
              <option value="contains">contains</option>
              <option value="equals">equals</option>
              <option value="not_equals">not equals</option>
              <option value="starts_with">starts with</option>
              <option value="ends_with">ends with</option>
              <option value="greater_than">greater than</option>
              <option value="less_than">less than</option>
            </select>
            <input v-model="newConditionValue" class="form-input" placeholder="value" aria-label="Condition value" />
            <select v-model="newActions[0].type" class="form-input" aria-label="Action">
              <option value="keep">keep (inbox)</option>
              <option value="fileinto">file into folder…</option>
              <option value="discard">discard</option>
              <option value="redirect">redirect to address…</option>
            </select>
            <input
              v-if="newActions[0].type === 'fileinto' || newActions[0].type === 'redirect'"
              v-model="newActions[0].target" class="form-input" placeholder="target" aria-label="Action target" />
            <button class="btn btn-primary btn-sm" :disabled="!canSave || creating" @click="save">
              {{ creating ? 'Saving…' : 'Add script' }}
            </button>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-body">
          <table v-if="scripts.length" class="scripts-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Priority</th>
                <th>Conditions</th>
                <th>Actions</th>
                <th>Enabled</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="script in scripts" :key="script.id">
                <td>{{ script.name }}</td>
                <td>{{ script.priority }}</td>
                <td>{{ conditionsLabel(script) }}</td>
                <td>{{ actionsLabel(script) }}</td>
                <td>
                  <button class="toggle-btn" :disabled="busyId === script.id" @click="toggle(script)">
                    {{ script.enabled ? 'on' : 'off' }}
                  </button>
                </td>
                <td>
                  <button class="btn btn-danger btn-sm" :disabled="busyId === script.id" @click="remove(script)">
                    Delete
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
          <p v-else class="empty-note">No filter scripts for this mailbox yet.</p>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.hint {
  font-size: 13px;
  color: var(--text-muted);
  margin-bottom: 16px;
}

.composer {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.form-input.narrow {
  width: 90px;
}

.scripts-table {
  width: 100%;
  font-size: 13px;
  border-collapse: collapse;
}

.scripts-table th,
.scripts-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border-primary);
  text-align: left;
}

.scripts-table th {
  font-weight: 500;
  color: var(--text-muted);
}

.toggle-btn {
  padding: 3px 10px;
  border-radius: 6px;
  border: 1px solid var(--border-primary);
  background: transparent;
  cursor: pointer;
  font-size: 12px;
}

.empty-note {
  font-size: 13px;
  color: var(--text-muted);
  padding: 12px 0;
}
</style>
