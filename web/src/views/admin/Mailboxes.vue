<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { adminMailboxesApi } from '../../api/mailboxes'
import type { AdminMailbox, AdminMailboxUsage } from '../../api/mailboxes'
import type { Pageable } from '../../api/types'
import { useNotificationStore } from '../../stores/notification'
import { useConfirm } from '../../composables/useConfirm'
import { useModalSafeClose } from '../../composables/useModalSafeClose'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'

// Admin mailbox administration (OB-5 W2, ADR-005 decision 3): the
// platform-wide hosted-mailbox table. Deletion is irreversible — it removes
// the mailbox with all folders and messages — so every destructive action
// runs behind a confirm dialog, and the backend audits each action
// (admin.mailbox.*) regardless.

const notify = useNotificationStore()
const { confirm } = useConfirm()
const loading = ref(true)
const mailboxes = ref<AdminMailbox[]>([])

const search = ref('')
const statusFilter = ref<'' | AdminMailbox['status']>('')
let searchTimeout: ReturnType<typeof setTimeout> | null = null

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminMailboxesApi.list(page, pageable.value.size, search.value, 0, 0, statusFilter.value)
    mailboxes.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load mailboxes', e)
    notify.error('Failed to load mailboxes')
  } finally {
    loading.value = false
  }
})

// Debounce keystrokes, then reset to the first page of results.
function onSearchInput() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => goToPage(0), 300)
}

function onStatusFilter() {
  goToPage(0)
}

// ---- provision modal ----
const showProvision = ref(false)
const provisionLoading = ref(false)
const provisionForm = ref({ user_id: '', name: '', quota_gb: '', quota_messages: '', workspace_id: '' })

function openProvision() {
  provisionForm.value = { user_id: '', name: '', quota_gb: '', quota_messages: '', workspace_id: '' }
  showProvision.value = true
}

async function provision() {
  const userId = Number(provisionForm.value.user_id)
  if (!userId || !provisionForm.value.name.trim()) {
    notify.error('Owner user id and mailbox name are required')
    return
  }
  const quotaBytes = gbToBytes(provisionForm.value.quota_gb)
  if (quotaBytes === null) {
    notify.error('Quota must be a non-negative number of GiB (empty = unlimited)')
    return
  }
  provisionLoading.value = true
  try {
    await adminMailboxesApi.provision({
      user_id: userId,
      name: provisionForm.value.name.trim(),
      quota_bytes: quotaBytes,
      quota_messages: numberOrZero(provisionForm.value.quota_messages),
      workspace_id: numberOrZero(provisionForm.value.workspace_id) || undefined,
    })
    notify.success('Mailbox provisioned')
    showProvision.value = false
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to provision mailbox')
  } finally {
    provisionLoading.value = false
  }
}

// ---- quota edit modal ----
const editingMailbox = ref<AdminMailbox | null>(null)
const editLoading = ref(false)
const editForm = ref({ quota_gb: '', quota_messages: '' })
const editUsage = ref<AdminMailboxUsage | null>(null)

async function openEdit(mailbox: AdminMailbox) {
  editingMailbox.value = mailbox
  editUsage.value = null
  editForm.value = {
    quota_gb: mailbox.quota_bytes > 0 ? String(bytesToGiB(mailbox.quota_bytes)) : '',
    quota_messages: mailbox.quota_messages > 0 ? String(mailbox.quota_messages) : '',
  }
  try {
    const res = await adminMailboxesApi.usage(mailbox.id)
    editUsage.value = res.data.data
  } catch {
    // The quota edit does not depend on the usage readout.
  }
}

async function saveEdit() {
  if (!editingMailbox.value) return
  const quotaBytes = gbToBytes(editForm.value.quota_gb)
  if (quotaBytes === null) {
    notify.error('Quota must be a non-negative number of GiB (empty = unlimited)')
    return
  }
  editLoading.value = true
  try {
    const res = await adminMailboxesApi.update(editingMailbox.value.id, {
      quota_bytes: quotaBytes,
      quota_messages: numberOrZero(editForm.value.quota_messages),
    })
    replaceRow(res.data.data)
    editingMailbox.value = null
    notify.success('Mailbox updated')
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to update mailbox')
  } finally {
    editLoading.value = false
  }
}

// ---- suspend / reactivate ----
async function toggleStatus(mailbox: AdminMailbox) {
  const next: AdminMailbox['status'] = mailbox.status === 'active' ? 'suspended' : 'active'
  try {
    const res = await adminMailboxesApi.update(mailbox.id, { status: next })
    replaceRow(res.data.data)
    notify.success(next === 'suspended' ? 'Mailbox suspended' : 'Mailbox reactivated')
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to update mailbox')
  }
}

// ---- delete ----
async function deleteMailbox(mailbox: AdminMailbox) {
  const confirmed = await confirm({
    title: 'Delete Mailbox',
    message: `Delete "${mailbox.name}" (owner: ${mailbox.owner_email || `user ${mailbox.user_id}`})? All folders and messages are removed immediately and cannot be recovered. Unreferenced storage is reclaimed by the GC sweep.`,
    confirmText: 'Delete Mailbox',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await adminMailboxesApi.remove(mailbox.id)
    notify.success('Mailbox deleted')
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to delete mailbox')
  }
}

// ---- helpers ----

function replaceRow(updated: AdminMailbox) {
  const idx = mailboxes.value.findIndex((m) => m.id === updated.id)
  if (idx !== -1) mailboxes.value[idx] = updated
}

/** "2.5" GiB → bytes; empty/0 → 0 (unlimited); anything else → null. The
    form fields are number inputs, so v-model hands us numbers at runtime. */
function gbToBytes(gb: string | number): number | null {
  const t = String(gb ?? '').trim()
  if (t === '') return 0
  const n = Number(t)
  if (!Number.isFinite(n) || n < 0) return null
  return Math.round(n * 1024 * 1024 * 1024)
}

function bytesToGiB(bytes: number): number {
  return Math.round((bytes / (1024 * 1024 * 1024)) * 100) / 100
}

function numberOrZero(v: string | number): number {
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value >= 100 ? Math.round(value) : Math.round(value * 10) / 10} ${units[unit]}`
}

/** Percentage of the byte quota in use; null when unlimited. */
function usagePercent(mailbox: AdminMailbox): number | null {
  if (mailbox.quota_bytes <= 0) return null
  return Math.min(100, Math.round((mailbox.used_bytes / mailbox.quota_bytes) * 100))
}

function statusBadgeClass(status: AdminMailbox['status']) {
  switch (status) {
    case 'active': return 'badge badge-dot badge-success'
    case 'suspended': return 'badge badge-disabled'
    default: return 'badge'
  }
}

function formatDate(date: string) {
  return new Date(date).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

const { watchClickStart, confirmClickEnd } = useModalSafeClose(() => {
  showProvision.value = false
  editingMailbox.value = null
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Mailbox Administration</h1>
      <button class="btn btn-primary" @click="openProvision">
        Provision Mailbox
      </button>
    </div>

    <div class="card">
      <div class="card-header" style="display: flex; gap: 12px; align-items: center; flex-wrap: wrap;">
        <h2>Mailboxes</h2>
        <select v-model="statusFilter" class="form-select" style="max-width: 160px; margin-left: auto;" @change="onStatusFilter">
          <option value="">All statuses</option>
          <option value="active">Active</option>
          <option value="suspended">Suspended</option>
          <option value="closed">Closed</option>
        </select>
        <input
          v-model="search"
          type="text"
          class="form-input"
          placeholder="Search by mailbox name..."
          style="max-width: 320px;"
          @input="onSearchInput"
        />
      </div>

      <!-- Loading: skeleton rows preserve the table layout while data streams in. -->
      <div v-if="loading" class="card-body">
        <table class="table">
          <thead>
            <tr>
              <th>Mailbox</th>
              <th>Quota</th>
              <th>Messages</th>
              <th>Status</th>
              <th>Created</th>
              <th style="text-align: right;">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in 6" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 60%;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 80px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 48px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 68px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 88px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 120px; margin-left: auto;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="mailboxes.length === 0" class="empty-state">
        <span class="mdi mdi-email-outline" style="font-size: 44px; display: block; margin-bottom: 10px;"></span>
        <h3>No mailboxes found</h3>
        <p v-if="search || statusFilter">No mailboxes match the current filters.</p>
        <p v-else>No hosted mailboxes exist yet — provision one for a user to get started.</p>
      </div>

      <div v-else class="card-body">
        <table class="table">
          <thead>
            <tr>
              <th>Mailbox</th>
              <th>Quota</th>
              <th>Messages</th>
              <th>Status</th>
              <th>Created</th>
              <th style="text-align: right;">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="mailbox in mailboxes" :key="mailbox.id">
              <td>
                <div class="cell-text">
                  <span class="cell-title">{{ mailbox.name }}</span>
                  <span class="cell-sub">{{ mailbox.owner_name || '—' }} · {{ mailbox.owner_email || `user ${mailbox.user_id}` }}</span>
                </div>
              </td>
              <td>
                <div class="cell-text">
                  <span class="cell-title">{{ formatBytes(mailbox.used_bytes) }}<template v-if="mailbox.quota_bytes > 0"> / {{ formatBytes(mailbox.quota_bytes) }}</template><template v-else> · unlimited</template></span>
                  <span v-if="usagePercent(mailbox) !== null" class="cell-sub">{{ usagePercent(mailbox) }}% used</span>
                </div>
              </td>
              <td>{{ mailbox.message_count }}<template v-if="mailbox.quota_messages > 0"> / {{ mailbox.quota_messages }}</template></td>
              <td>
                <span :class="statusBadgeClass(mailbox.status)">{{ mailbox.status }}</span>
              </td>
              <td><span :title="new Date(mailbox.created_at).toLocaleString()">{{ formatDate(mailbox.created_at) }}</span></td>
              <td>
                <div class="table-actions">
                  <button class="btn btn-icon btn-icon-muted" title="Edit quota" @click="openEdit(mailbox)">
                    <span class="mdi mdi-gauge"></span>
                  </button>
                  <button
                    class="btn btn-icon btn-icon-muted"
                    :title="mailbox.status === 'active' ? 'Suspend mailbox' : 'Reactivate mailbox'"
                    @click="toggleStatus(mailbox)"
                  >
                    <span class="mdi" :class="mailbox.status === 'active' ? 'mdi-pause-circle-outline' : 'mdi-play-circle-outline'"></span>
                  </button>
                  <button class="btn btn-icon btn-icon-danger" title="Delete mailbox" @click="deleteMailbox(mailbox)">
                    <span class="mdi mdi-trash-can-outline"></span>
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <Pagination :pageable="pageable" @page="goToPage" />
      </div>
    </div>

    <!-- Provision Mailbox Modal -->
    <div v-if="showProvision" class="modal-overlay" @mousedown="watchClickStart" @mouseup="confirmClickEnd">
      <div class="modal" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h3>Provision Mailbox</h3>
        </div>
        <div class="modal-body">
          <form @submit.prevent="provision" style="display: grid; gap: 1rem;">
            <div class="form-group">
              <label class="form-label" for="mbx-user">Owner user ID</label>
              <input id="mbx-user" v-model="provisionForm.user_id" type="number" min="1" class="form-input" placeholder="e.g. 7" required />
            </div>
            <div class="form-group">
              <label class="form-label" for="mbx-name">Mailbox name</label>
              <input id="mbx-name" v-model="provisionForm.name" type="text" class="form-input" placeholder="user@example.com" required maxlength="255" />
            </div>
            <div class="form-group">
              <label class="form-label" for="mbx-quota">Byte quota (GiB)</label>
              <input id="mbx-quota" v-model="provisionForm.quota_gb" type="number" min="0" step="0.01" class="form-input" placeholder="Empty = unlimited" />
            </div>
            <div class="form-group">
              <label class="form-label" for="mbx-quota-msg">Message-count quota</label>
              <input id="mbx-quota-msg" v-model="provisionForm.quota_messages" type="number" min="0" class="form-input" placeholder="Empty = unlimited" />
            </div>
            <div class="form-group">
              <label class="form-label" for="mbx-workspace">Workspace ID (optional)</label>
              <input id="mbx-workspace" v-model="provisionForm.workspace_id" type="number" min="0" class="form-input" placeholder="The user must be a member" />
            </div>
            <button type="submit" class="btn btn-primary" :disabled="provisionLoading">
              {{ provisionLoading ? 'Provisioning...' : 'Provision Mailbox' }}
            </button>
          </form>
        </div>
      </div>
    </div>

    <!-- Edit Quota Modal -->
    <div v-if="editingMailbox" class="modal-overlay" @mousedown="watchClickStart" @mouseup="confirmClickEnd">
      <div class="modal" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h3>Edit Quota — {{ editingMailbox.name }}</h3>
        </div>
        <div class="modal-body">
          <p v-if="editUsage" class="cell-sub" style="margin-bottom: 1rem;">
            In use: {{ formatBytes(editUsage.raw_bytes) }} across {{ editUsage.message_count }} messages
            ({{ formatBytes(editUsage.blob_bytes) }} stored after deduplication).
          </p>
          <form @submit.prevent="saveEdit" style="display: grid; gap: 1rem;">
            <div class="form-group">
              <label class="form-label" for="edit-quota">Byte quota (GiB)</label>
              <input id="edit-quota" v-model="editForm.quota_gb" type="number" min="0" step="0.01" class="form-input" placeholder="Empty = unlimited" />
            </div>
            <div class="form-group">
              <label class="form-label" for="edit-quota-msg">Message-count quota</label>
              <input id="edit-quota-msg" v-model="editForm.quota_messages" type="number" min="0" class="form-input" placeholder="Empty = unlimited" />
            </div>
            <button type="submit" class="btn btn-primary" :disabled="editLoading">
              {{ editLoading ? 'Saving...' : 'Save Quotas' }}
            </button>
          </form>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Suspended mailboxes read as a neutral pill with danger text (the disabled
   account treatment from Users.vue). */
.badge-disabled {
  background: var(--bg-tertiary);
  color: var(--danger-text);
}

/* The mailbox name is a technical identifier — set it in the mono face. */
.cell-title {
  font-family: var(--font-mono);
  font-size: 13px;
}
</style>
