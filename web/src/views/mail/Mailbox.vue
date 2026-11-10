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
import type { MailFolder, MailMessageSummary, Mailbox } from '../../api/mail'
import Pagination from '../../components/Pagination.vue'
import { apiMessage } from '../../composables/apiError'
import { useConfirm } from '../../composables/useConfirm'
import { useNotificationStore } from '../../stores/notification'

// Webmail mailbox view (OB-5 W4, ADR-005 decision 2): the folder tree with
// unseen counters, the paged message list (unread/flagged filters + the
// ADR-003 full-text query), and bulk flag/delete/move. Everything is
// user-scoped server-side — this view only ever names the session user's own
// mailbox, folder and UIDs.

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const { confirm } = useConfirm()

const loading = ref(true)
const listLoading = ref(false)
const mailUnavailable = ref(false)
const mailboxes = ref<Mailbox[]>([])
const mailbox = ref<Mailbox | null>(null)
const folders = ref<MailFolder[]>([])
const messages = ref<MailMessageSummary[]>([])
const pageable = ref({ current_page: 1, size: 25, total_pages: 0, total_elements: 0, empty: true })

// Filters (unread/flagged combine with the FTS query server-side).
const unreadOnly = ref(false)
const flaggedOnly = ref(false)
const searchQuery = ref('')
const appliedQuery = ref('')

// Bulk selection: the list rows are addressed by folder UID.
const selected = ref<Set<number>>(new Set())
const bulkBusy = ref(false)

const currentFolderId = computed(() => Number(route.params.folderId) || 0)
const currentFolder = computed(() => folders.value.find((f) => f.id === currentFolderId.value) ?? null)

// The folder tree: root folders in IMAP convention (INBOX first), children
// indented under their parent.
interface FolderNode {
  folder: MailFolder
  depth: number
}
const folderTree = computed<FolderNode[]>(() => {
  const byParent = new Map<number | null, MailFolder[]>()
  for (const f of folders.value) {
    const key = f.parent_id ?? null
    if (!byParent.has(key)) byParent.set(key, [])
    byParent.get(key)!.push(f)
  }
  const inbox = (byParent.get(null) ?? []).find((f) => f.name.toUpperCase() === 'INBOX')
  const roots = (byParent.get(null) ?? []).filter((f) => f !== inbox)
  const order: MailFolder[] = []
  if (inbox) order.push(inbox)
  order.push(...roots)
  const out: FolderNode[] = []
  const walk = (list: MailFolder[], depth: number) => {
    for (const f of list) {
      out.push({ folder: f, depth })
      walk(byParent.get(f.id) ?? [], depth + 1)
    }
  }
  walk(order, 0)
  return out
})

const totalSelectedSize = computed(() =>
  messages.value.filter((m) => selected.value.has(m.uid)).length,
)

const quotaPercent = computed(() => {
  const mb = mailbox.value
  if (!mb || mb.quota_bytes <= 0) return null
  return Math.min(100, Math.round((mb.used_bytes / mb.quota_bytes) * 100))
})

// The shared Pagination component is 0-based (the rest of the app's endpoints
// are); the /mail listing contract is 1-based (handlers normalize page < 1 to
// 1), so the conversion lives here at the edge.
const uiPageable = computed(() => ({ ...pageable.value, current_page: Math.max(0, pageable.value.current_page - 1) }))

async function loadMailboxes() {
  const res = await mailApi.mailboxes()
  mailboxes.value = res.data.data ?? []
  mailbox.value = mailboxes.value.find((m) => m.status === 'active') ?? mailboxes.value[0] ?? null
}

async function loadFolders() {
  if (!mailbox.value) {
    folders.value = []
    return
  }
  const res = await mailApi.folders(mailbox.value.id)
  folders.value = res.data.data ?? []
}

async function loadMessages(page = pageable.value.current_page || 1) {
  if (!currentFolderId.value) return
  listLoading.value = true
  try {
    const res = await mailApi.messages({
      folder: currentFolderId.value,
      page,
      size: pageable.value.size,
      unread: unreadOnly.value || undefined,
      flagged: flaggedOnly.value || undefined,
      q: appliedQuery.value || undefined,
    })
    messages.value = res.messages
    pageable.value = res.pageable
    selected.value = new Set()
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to load messages'))
  } finally {
    listLoading.value = false
  }
}

async function refreshCounters() {
  try {
    await loadFolders()
  } catch {
    // Counter refresh is best-effort; the list itself stays usable.
  }
}

function selectFolder(id: number) {
  router.push({ name: 'mail-folder', params: { folderId: String(id) } })
}

function openMessage(m: MailMessageSummary) {
  router.push({ name: 'mail-message', params: { folderId: String(m.folder_id), uid: String(m.uid) } })
}

function applyFilters() {
  appliedQuery.value = searchQuery.value.trim()
  loadMessages(1)
}

function resetFilters() {
  unreadOnly.value = false
  flaggedOnly.value = false
  searchQuery.value = ''
  appliedQuery.value = ''
  loadMessages(1)
}

function goToPage(page: number) {
  // page is 0-based from the shared component; the listing is 1-based.
  loadMessages(page + 1)
}

function toggleSelected(uid: number) {
  const next = new Set(selected.value)
  if (next.has(uid)) next.delete(uid)
  else next.add(uid)
  selected.value = next
}

function selectAllVisible() {
  const allSelected = messages.value.length > 0 && messages.value.every((m) => selected.value.has(m.uid))
  selected.value = allSelected ? new Set() : new Set(messages.value.map((m) => m.uid))
}

/** Applies one flag flip to every selected UID, then reloads. */
async function bulkFlags(patch: { seen?: boolean; flagged?: boolean }) {
  const uids = [...selected.value]
  if (uids.length === 0 || bulkBusy.value) return
  bulkBusy.value = true
  let failures = 0
  for (const uid of uids) {
    try {
      await mailApi.updateFlags(uid, currentFolderId.value, patch)
    } catch {
      failures++
    }
  }
  bulkBusy.value = false
  if (failures > 0) notify.error(`${failures} of ${uids.length} updates failed`)
  await Promise.all([loadMessages(), refreshCounters()])
}

async function bulkDelete() {
  const uids = [...selected.value]
  if (uids.length === 0 || bulkBusy.value) return
  const okToDelete = await confirm({
    title: `Delete ${uids.length} message${uids.length === 1 ? '' : 's'}`,
    message: 'The messages are marked \\Deleted and leave permanently at folder expunge.',
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!okToDelete) return
  bulkBusy.value = true
  let failures = 0
  for (const uid of uids) {
    try {
      await mailApi.deleteMessage(uid, currentFolderId.value)
    } catch {
      failures++
    }
  }
  bulkBusy.value = false
  if (failures > 0) notify.error(`${failures} of ${uids.length} deletions failed`)
  else notify.success(`${uids.length} message${uids.length === 1 ? '' : 's'} deleted`)
  await Promise.all([loadMessages(), refreshCounters()])
}

async function bulkMove(targetFolderId: number) {
  const uids = [...selected.value]
  if (uids.length === 0 || bulkBusy.value || !targetFolderId || targetFolderId === currentFolderId.value) return
  bulkBusy.value = true
  let failures = 0
  for (const uid of uids) {
    try {
      await mailApi.moveMessage(uid, currentFolderId.value, targetFolderId)
    } catch {
      failures++
    }
  }
  bulkBusy.value = false
  if (failures > 0) notify.error(`${failures} of ${uids.length} moves failed`)
  else notify.success(`${uids.length} message${uids.length === 1 ? '' : 's'} moved`)
  await Promise.all([loadMessages(), refreshCounters()])
}

function rowClass(m: MailMessageSummary): string {
  return hasFlag(m, 'seen') ? '' : 'unread'
}

function messageTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString()
}

onMounted(async () => {
  try {
    loading.value = true
    await loadMailboxes()
    await loadFolders()
    // No folder in the URL: land on the first folder (INBOX first in the
    // tree order) instead of an empty list.
    if (!currentFolderId.value && folderTree.value.length > 0) {
      selectFolder(folderTree.value[0].folder.id)
      return
    }
    if (currentFolderId.value) await loadMessages()
  } catch (e: any) {
    if (e?.response?.status === 503) {
      mailUnavailable.value = true
    } else {
      notify.error(apiMessage(e, 'Failed to load mail'))
    }
  } finally {
    loading.value = false
  }
})

// Folder navigation (and a folder disappearing mid-session) reloads the list.
watch(currentFolderId, async (id, old) => {
  if (id === old) return
  if (!id) return
  await loadMessages(1)
  selected.value = new Set()
})
</script>

<template>
  <div class="mail-page">
    <div class="page-header mail-header">
      <div>
        <h1>Mail</h1>
        <p class="page-description" v-if="mailbox">
          {{ mailbox.name }} — {{ mailbox.message_count }} messages,
          {{ formatBytes(mailbox.used_bytes) }}<template v-if="mailbox.quota_bytes > 0"> of {{ formatBytes(mailbox.quota_bytes) }}</template> used
        </p>
      </div>
      <div class="mail-header-actions">
        <router-link class="btn btn-primary" :to="{ name: 'mail-compose' }">
          <span class="mdi mdi-pencil"></span> Compose
        </router-link>
        <router-link class="btn btn-secondary" :to="{ name: 'mail-settings' }">
          <span class="mdi mdi-cog"></span> Settings
        </router-link>
      </div>
    </div>

    <div v-if="mailUnavailable" class="empty-state">
      <h3>Mail is not available</h3>
      <p>This deployment has no mailbox store configured (blob store or encryption key missing).</p>
    </div>

    <div v-else-if="loading" class="loading-page"><div class="spinner"></div></div>

    <div v-else-if="!mailbox" class="empty-state">
      <h3>No mailbox</h3>
      <p>This account has no hosted mailbox yet. An administrator can provision one under Admin · Mailboxes.</p>
    </div>

    <div v-else class="mail-layout">
      <aside class="mail-sidebar card">
        <div class="card-body mail-folder-list">
          <button
            v-for="node in folderTree"
            :key="node.folder.id"
            class="mail-folder-item"
            :class="{ active: node.folder.id === currentFolderId }"
            :style="{ paddingLeft: 12 + node.depth * 16 + 'px' }"
            type="button"
            @click="selectFolder(node.folder.id)"
          >
            <span class="mdi" :class="node.folder.name.toUpperCase() === 'INBOX' ? 'mdi-inbox' : 'mdi-folder-outline'"></span>
            <span class="mail-folder-name">{{ node.folder.name }}</span>
            <span v-if="node.folder.unseen_count > 0" class="badge badge-secondary mail-unseen">{{ node.folder.unseen_count }}</span>
          </button>
          <div v-if="folderTree.length === 0" class="text-muted text-sm">No folders.</div>
        </div>
        <div v-if="quotaPercent !== null" class="card-body mail-quota">
          <div class="justify-between items-center text-sm">
            <span class="text-muted">Storage</span>
            <span>{{ quotaPercent }}%</span>
          </div>
          <div class="mail-quota-bar"><div class="mail-quota-fill" :style="{ width: quotaPercent + '%' }"></div></div>
        </div>
      </aside>

      <section class="mail-list card">
        <div class="card-header mail-toolbar">
          <form class="mail-search" @submit.prevent="applyFilters">
            <input
              v-model="searchQuery"
              class="form-input"
              type="search"
              placeholder="Search this folder…"
              aria-label="Search messages"
            />
            <button class="btn btn-secondary btn-sm" type="submit">Search</button>
          </form>
          <div class="mail-filters">
            <label class="checkbox-label">
              <input v-model="unreadOnly" type="checkbox" @change="applyFilters" /> Unread
            </label>
            <label class="checkbox-label">
              <input v-model="flaggedOnly" type="checkbox" @change="applyFilters" /> Flagged
            </label>
            <button
              v-if="unreadOnly || flaggedOnly || appliedQuery"
              class="btn btn-ghost btn-sm"
              type="button"
              @click="resetFilters"
            >
              Clear
            </button>
          </div>
        </div>

        <div v-if="totalSelectedSize > 0" class="mail-bulkbar">
          <span>{{ totalSelectedSize }} selected</span>
          <button class="btn btn-secondary btn-sm" type="button" :disabled="bulkBusy" @click="bulkFlags({ seen: true })">Mark read</button>
          <button class="btn btn-secondary btn-sm" type="button" :disabled="bulkBusy" @click="bulkFlags({ seen: false })">Mark unread</button>
          <button class="btn btn-secondary btn-sm" type="button" :disabled="bulkBusy" @click="bulkFlags({ flagged: true })">Flag</button>
          <button class="btn btn-secondary btn-sm" type="button" :disabled="bulkBusy" @click="bulkFlags({ flagged: false })">Unflag</button>
          <select class="form-select mail-bulk-move" aria-label="Move selected to folder" :disabled="bulkBusy" @change="bulkMove(Number(($event.target as HTMLSelectElement).value)); ($event.target as HTMLSelectElement).value = ''">
            <option value="">Move to…</option>
            <option v-for="f in folders.filter((f) => f.id !== currentFolderId)" :key="f.id" :value="f.id">{{ f.name }}</option>
          </select>
          <button class="btn btn-danger btn-sm" type="button" :disabled="bulkBusy" @click="bulkDelete">Delete</button>
        </div>

        <div class="table-wrapper">
          <table class="mail-table">
            <thead>
              <tr>
                <th class="mail-col-check">
                  <input
                    type="checkbox"
                    :checked="messages.length > 0 && messages.every((m) => selected.has(m.uid))"
                    aria-label="Select all messages on this page"
                    @change="selectAllVisible"
                  />
                </th>
                <th>From</th>
                <th>Subject</th>
                <th class="text-right">Date</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="listLoading"><td colspan="4" class="text-center text-muted">Loading…</td></tr>
              <tr v-else-if="messages.length === 0">
                <td colspan="4">
                  <div class="empty-state">
                    <h3>{{ appliedQuery ? 'No matches' : 'No messages' }}</h3>
                    <p>{{ appliedQuery ? 'Nothing in this folder matches the search and filters.' : 'This folder is empty.' }}</p>
                  </div>
                </td>
              </tr>
              <tr
                v-else
                v-for="m in messages"
                :key="m.uid"
                class="row-clickable"
                :class="rowClass(m)"
                @click="openMessage(m)"
              >
                <td class="mail-col-check" @click.stop>
                  <input type="checkbox" :checked="selected.has(m.uid)" :aria-label="`Select ${m.subject || '(no subject)'}`" @change="toggleSelected(m.uid)" />
                </td>
                <td class="mail-col-from">
                  <span class="mail-from" :class="{ 'mail-from-unread': !hasFlag(m, 'seen') }">{{ m.from }}</span>
                </td>
                <td class="mail-col-subject">
                  <span class="mdi mdi-star mail-flagged-icon" v-if="hasFlag(m, 'flagged')" title="Flagged"></span>
                  <span class="mdi mdi-reply" v-if="hasFlag(m, 'answered')" title="Answered"></span>
                  <span class="mdi mdi-paperclip" v-if="m.has_attachments" title="Has attachments"></span>
                  <span class="mail-subject">{{ m.subject || '(no subject)' }}</span>
                  <span class="mail-snippet text-muted">{{ m.snippet }}</span>
                </td>
                <td class="text-right mail-col-date">{{ messageTime(m.date) }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="mail-pagination">
          <Pagination :pageable="uiPageable" @page="goToPage" />
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.mail-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.mail-header-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.mail-layout {
  display: grid;
  grid-template-columns: 240px 1fr;
  gap: 16px;
  align-items: start;
}

/* Grid items default to min-width:auto: the message table's nowrap content
   (the snippet's 520px max-width floor plus the fixed-width columns) would
   otherwise blow the 1fr track out past the viewport — the date column and
   toolbar get clipped under .main-content's overflow-x: clip. */
.mail-list {
  min-width: 0;
}

/* The wrapper is the scroll container of last resort: if the table still
   cannot fit (very narrow viewport), it scrolls inside the card instead of
   clipping against the viewport. */
.table-wrapper {
  overflow-x: auto;
}

/* Kill the subject cell's min-content floor: the nowrap snippet's 520px
   max-width would otherwise force the auto table layout to ~950px minimum.
   Subject text wraps at the real cell width; the snippet keeps its own
   ellipsis at the cell edge. */
.mail-col-subject {
  max-width: 0;
  overflow: hidden;
}

.mail-folder-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
}

.mail-folder-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 12px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  font-size: 13px;
  cursor: pointer;
  text-align: left;
  transition: background var(--transition), color var(--transition);
}

.mail-folder-item:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.mail-folder-item.active {
  background: var(--primary-50);
  color: var(--primary-600);
  font-weight: 600;
}

.mail-folder-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mail-unseen {
  flex-shrink: 0;
}

.mail-quota-bar {
  height: 6px;
  margin-top: 6px;
  border-radius: 999px;
  background: var(--bg-tertiary);
  overflow: hidden;
}

.mail-quota-fill {
  height: 100%;
  border-radius: 999px;
  background: var(--primary-600);
}

.mail-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.mail-search {
  display: flex;
  gap: 8px;
  flex: 1;
  min-width: 220px;
}

.mail-filters {
  display: flex;
  align-items: center;
  gap: 12px;
}

.mail-bulkbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--border-primary);
  background: var(--bg-tertiary);
}

.mail-bulk-move {
  width: auto;
  min-width: 130px;
}

.mail-table {
  width: 100%;
}

.mail-table td,
.mail-table th {
  padding: 10px 12px;
  vertical-align: top;
}

.mail-col-check {
  width: 36px;
}

.mail-col-from {
  width: 220px;
}

.mail-col-date {
  width: 170px;
  color: var(--text-muted);
  font-size: 12px;
  white-space: nowrap;
}

.mail-from {
  display: block;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mail-from-unread {
  color: var(--text-primary);
  font-weight: 600;
}

.mail-subject {
  color: var(--text-primary);
}

.mail-snippet {
  display: block;
  margin-top: 2px;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 520px;
}

/* Unread rows carry a subtle accent instead of bolding everything. */
.mail-table tr.unread {
  background: var(--primary-50);
}

.mail-flagged-icon {
  color: var(--warning, #d97706);
}

.mail-pagination {
  display: flex;
  justify-content: center;
  padding: 12px;
}

@media (max-width: 900px) {
  .mail-layout {
    grid-template-columns: 1fr;
  }

  .mail-col-from {
    width: 120px;
  }

  .mail-col-date {
    display: none;
  }
}
</style>
