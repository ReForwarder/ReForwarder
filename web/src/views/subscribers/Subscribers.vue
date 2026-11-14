<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { subscribersApi } from '../../api/subscribers'
import { subscriberListsApi } from '../../api/subscriberLists'
import type { BulkImportResult, Subscriber, SubscriberStatus, SubscriberListItem } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import { usePagination } from '../../composables/usePagination'
import { useNotificationStore } from '../../stores/notification'
import { useModalSafeClose } from '../../composables/useModalSafeClose'
import { useWorkspaceStore } from '../../stores/workspace'

const router = useRouter()
const notify = useNotificationStore()
const wsStore = useWorkspaceStore()

const subscribers = ref<Subscriber[]>([])
const loading = ref(true)
const search = ref('')
const statusFilter = ref<SubscriberStatus | ''>('')
let searchTimeout: ReturnType<typeof setTimeout> | null = null

// Create modal
const showCreateModal = ref(false)
const createForm = ref({ email: '', name: '', custom_fields: '' })
const creating = ref(false)

// Import JSON modal
const showImportJsonModal = ref(false)
const importJsonText = ref('')
const importing = ref(false)

// CSV import
const csvFileInput = ref<HTMLInputElement | null>(null)
const showImportCsvModal = ref(false)
const csvSelectedFile = ref<File | null>(null)
const importingCsv = ref(false)

// Target list for bulk imports (double opt-in, RF-F075): static lists only,
// since dynamic lists have no members to target. Options are fetched once and
// cached for both import dialogs.
const importLists = ref<SubscriberListItem[]>([])
const importListsLoaded = ref(false)
const importListsLoading = ref(false)
const importTargetListId = ref<number | ''>('')



const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await subscribersApi.list(page, pageable.value.size, search.value, statusFilter.value || undefined)
    subscribers.value = res.data.data ?? []
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load subscribers', e)
  } finally {
    loading.value = false
  }
})


function onSearchInput() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => goToPage(0), 300)
}

function onStatusChange() {
  goToPage(0)
}

function openCreate() {
  createForm.value = { email: '', name: '', custom_fields: '' }
  showCreateModal.value = true
}

async function createSubscriber() {
  if (!createForm.value.email.trim()) return
  creating.value = true
  try {
    let customFields: Record<string, any> = {}
    if (createForm.value.custom_fields.trim()) {
      customFields = JSON.parse(createForm.value.custom_fields)
    }
    await subscribersApi.create({
      email: createForm.value.email.trim(),
      name: createForm.value.name.trim(),
      custom_fields: customFields,
    })
    notify.success('Subscriber created')
    showCreateModal.value = false
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    if (e instanceof SyntaxError) {
      notify.error('Invalid JSON in custom fields')
    } else {
      notify.error(e?.response?.data?.error?.message || 'Failed to create subscriber')
    }
  } finally {
    creating.value = false
  }
}

function openImportJson() {
  importJsonText.value = ''
  importTargetListId.value = ''
  showImportJsonModal.value = true
  ensureImportLists()
}

function openImportCsv() {
  csvSelectedFile.value = null
  importTargetListId.value = ''
  showImportCsvModal.value = true
  ensureImportLists()
}

async function ensureImportLists() {
  if (importListsLoaded.value || importListsLoading.value) return
  importListsLoading.value = true
  try {
    const res = await subscriberListsApi.list(0, 200)
    importLists.value = (res.data.data ?? []).filter(list => list.type === 'static')
    importListsLoaded.value = true
  } catch {
    notify.error('Failed to load lists')
  } finally {
    importListsLoading.value = false
  }
}

function importSummary(prefix: string, result: BulkImportResult): string {
  let msg = `${prefix}: ${result.created} created, ${result.skipped} skipped out of ${result.total}`
  if (result.invalid_timezones) {
    msg += ` (${result.invalid_timezones} with an unrecognised timezone, imported without one)`
  }
  if (result.members_added !== undefined) {
    msg += `; Added to list: ${result.members_added}`
  }
  if (result.confirmations_sent !== undefined) {
    msg += `; Confirmations sent: ${result.confirmations_sent}`
  }
  return msg
}

async function importJson() {
  if (!importJsonText.value.trim()) return
  importing.value = true
  try {
    const data = JSON.parse(importJsonText.value)
    const subscribers = Array.isArray(data) ? data : [data]
    const listId = importTargetListId.value === '' ? undefined : importTargetListId.value
    const res = await subscribersApi.bulkImportJSON(subscribers, listId)
    const result = res.data.data
    notify.success(importSummary('Import complete', result))
    showImportJsonModal.value = false
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    if (e instanceof SyntaxError) {
      notify.error('Invalid JSON format')
    } else {
      notify.error(e?.response?.data?.error?.message || 'Failed to import subscribers')
    }
  } finally {
    importing.value = false
  }
}

function triggerCsvUpload() {
  csvFileInput.value?.click()
}

// The file is only staged here; the import runs when the modal's Import
// button is clicked.
function onCsvFileSelected(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  csvSelectedFile.value = file ?? null
}

async function importCsv() {
  if (!csvSelectedFile.value) return
  importingCsv.value = true
  try {
    const listId = importTargetListId.value === '' ? undefined : importTargetListId.value
    const res = await subscribersApi.bulkImportCSV(csvSelectedFile.value, undefined, listId)
    const result = res.data.data
    notify.success(importSummary('CSV import complete', result))
    showImportCsvModal.value = false
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to import CSV')
  } finally {
    importingCsv.value = false
    // Allow re-selecting the same file after a failed attempt.
    if (csvFileInput.value) csvFileInput.value.value = ''
  }
}

function statusBadgeClass(status: SubscriberStatus): string {
  switch (status) {
    case 'subscribed': return 'badge badge-success'
    case 'unsubscribed': return 'badge badge-neutral'
    case 'bounced': return 'badge badge-danger'
    case 'complained': return 'badge badge-danger'
    default: return 'badge'
  }
}

function truncateCustomFields(fields: Record<string, any>): string {
  if (!fields || Object.keys(fields).length === 0) return '-'
  const str = JSON.stringify(fields)
  return str.length > 60 ? str.substring(0, 60) + '...' : str
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

const { watchClickStart: watchCreateStart, confirmClickEnd: confirmCreateEnd } = useModalSafeClose(() => {
  showCreateModal.value = false
})
const { watchClickStart: watchImportStart, confirmClickEnd: confirmImportEnd } = useModalSafeClose(() => {
  showImportJsonModal.value = false
})
const { watchClickStart: watchCsvStart, confirmClickEnd: confirmCsvEnd } = useModalSafeClose(() => {
  showImportCsvModal.value = false
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Subscribers</h1>
    </div>

    <div class="card">
      <div class="card-header" style="display: flex; gap: 12px; align-items: center">
        <input
          v-model="search"
          type="text"
          class="form-input"
          placeholder="Search by email or name..."
          style="max-width: 320px"
          @input="onSearchInput"
        />
        <select v-model="statusFilter" class="form-select" style="max-width: 180px" @change="onStatusChange">
          <option value="">All Statuses</option>
          <option value="subscribed">Subscribed</option>
          <option value="unsubscribed">Unsubscribed</option>
          <option value="bounced">Bounced</option>
          <option value="complained">Complained</option>
        </select>
        <div v-if="wsStore.canEdit" class="flex gap-2" style="margin-left: auto">
          <button class="btn btn-secondary" @click="openImportJson">Import JSON</button>
          <button class="btn btn-secondary" :disabled="importingCsv" @click="openImportCsv">
            {{ importingCsv ? 'Importing...' : 'Import CSV' }}
          </button>
          <button class="btn btn-primary" @click="openCreate">Add Subscriber</button>
          <input ref="csvFileInput" type="file" accept=".csv" style="display: none" @change="onCsvFileSelected" />
        </div>
      </div>

      <div v-if="loading" class="loading-page">
        <div class="spinner"></div>
      </div>

      <template v-else>
        <div v-if="subscribers.length === 0" class="empty-state">
          <h3>No Subscribers</h3>
          <p v-if="search || statusFilter">No subscribers matching your filters.</p>
          <p v-else>Add subscribers manually or import them from a file.</p>
        </div>

        <template v-else>
          <div class="table-wrapper">
            <table>
              <thead>
                <tr>
                  <th>Email</th>
                  <th>Name</th>
                  <th>Status</th>
                  <th>Custom Fields</th>
                  <th>Created</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="sub in subscribers" :key="sub.id" style="cursor: pointer" @click="router.push(`/subscribers/${sub.id}`)">
                  <td class="cell-mono">{{ sub.email }}</td>
                  <td>{{ sub.name || '-' }}</td>
                  <td><span :class="statusBadgeClass(sub.status)">{{ sub.status }}</span></td>
                  <td class="cell-mono cell-fields">{{ truncateCustomFields(sub.custom_fields) }}</td>
                  <td>{{ formatDate(sub.created_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <Pagination :pageable="pageable" @page="goToPage" />
        </template>
      </template>
    </div>

    <!-- Create Subscriber Modal -->
    <div v-if="showCreateModal" class="modal-overlay" @mousedown="watchCreateStart" @mouseup="confirmCreateEnd">
      <div class="modal" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h2>Add Subscriber</h2>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">Email</label>
            <input v-model="createForm.email" type="email" class="form-input" placeholder="user@example.com" />
          </div>
          <div class="form-group">
            <label class="form-label">Name</label>
            <input v-model="createForm.name" type="text" class="form-input" placeholder="Name (optional)" />
          </div>
          <div class="form-group">
            <label class="form-label">Custom Fields (JSON)</label>
            <textarea v-model="createForm.custom_fields" class="form-input" rows="4" placeholder='{"company": "Acme", "plan": "pro"}'></textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="showCreateModal = false">Cancel</button>
          <button class="btn btn-primary" :disabled="creating || !createForm.email.trim()" @click="createSubscriber">
            {{ creating ? 'Creating...' : 'Create' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Import JSON Modal -->
    <div v-if="showImportJsonModal" class="modal-overlay" @mousedown="watchImportStart" @mouseup="confirmImportEnd">
      <div class="modal" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h2>Import Subscribers (JSON)</h2>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">Target list</label>
            <select v-model="importTargetListId" class="form-select" :disabled="importListsLoading">
              <option value="">No list (contacts only)</option>
              <option v-for="list in importLists" :key="list.id" :value="list.id">{{ list.name }}</option>
            </select>
          </div>
          <div class="form-group">
            <label class="form-label">JSON Data</label>
            <textarea v-model="importJsonText" class="form-input" rows="10" placeholder='[{"email": "user@example.com", "name": "User"}]'></textarea>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="showImportJsonModal = false">Cancel</button>
          <button class="btn btn-primary" :disabled="importing || !importJsonText.trim()" @click="importJson">
            {{ importing ? 'Importing...' : 'Import' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Import CSV Modal -->
    <div v-if="showImportCsvModal" class="modal-overlay" @mousedown="watchCsvStart" @mouseup="confirmCsvEnd">
      <div class="modal" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h2>Import Subscribers (CSV)</h2>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">Target list</label>
            <select v-model="importTargetListId" class="form-select" :disabled="importListsLoading">
              <option value="">No list (contacts only)</option>
              <option v-for="list in importLists" :key="list.id" :value="list.id">{{ list.name }}</option>
            </select>
          </div>
          <div class="form-group">
            <label class="form-label">CSV file</label>
            <button class="btn btn-secondary" :disabled="importingCsv" @click="triggerCsvUpload">
              {{ csvSelectedFile ? csvSelectedFile.name : 'Choose file...' }}
            </button>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="showImportCsvModal = false">Cancel</button>
          <button class="btn btn-primary" :disabled="importingCsv || !csvSelectedFile" @click="importCsv">
            {{ importingCsv ? 'Importing...' : 'Import' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Addresses and serialized data render in the mono face — technical values read as data. */
.cell-mono {
  font-family: var(--font-mono);
  font-size: 13px;
}
.cell-fields {
  color: var(--text-tertiary);
  font-size: 12px;
}
</style>
