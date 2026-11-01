<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '../../../api/admin'
import type { AllowlistEntry, Pageable } from '../../../api/types'
import { useNotificationStore } from '../../../stores/notification'
import { useConfirm } from '../../../composables/useConfirm'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const { confirm } = useConfirm()
const loading = ref(true)
const entries = ref<AllowlistEntry[]>([])

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await adminApi.listAllowlist(page, pageable.value.size)
    entries.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load allowlist', e)
  } finally {
    loading.value = false
  }
})

// ---- Create ----

// Deep-linked from the Blocked IPs page ("Add to allowlist" prefill): the
// query carries the address, so open the form ready to save it.
const prefilledIp = typeof route.query.ip === 'string' ? route.query.ip : ''
const showCreateForm = ref(prefilledIp !== '')
const createLoading = ref(false)
const form = ref({ ip_cidr: prefilledIp, description: '' })

async function createEntry() {
  if (!form.value.ip_cidr.trim()) {
    notify.error('An IP address or CIDR block is required')
    return
  }
  createLoading.value = true
  try {
    await adminApi.createAllowlistEntry({
      ip_cidr: form.value.ip_cidr.trim(),
      description: form.value.description.trim() || undefined,
    })
    notify.success('Address allowlisted')
    showCreateForm.value = false
    form.value = { ip_cidr: '', description: '' }
    // Drop the prefill query so a manual reopen starts clean.
    router.replace({ query: { ...route.query, ip: undefined } })
    await goToPage(0)
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to add allowlist entry'
    notify.error(message)
  } finally {
    createLoading.value = false
  }
}

async function deleteEntry(entry: AllowlistEntry) {
  const okToDelete = await confirm({
    title: 'Remove Allowlist Entry',
    message: `Remove ${entry.ip_cidr} from the allowlist? The address becomes subject to detection and existing bans again.`,
    confirmText: 'Remove',
    variant: 'danger',
  })
  if (!okToDelete) return
  try {
    await adminApi.deleteAllowlistEntry(entry.id)
    notify.success('Allowlist entry removed')
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to remove allowlist entry'
    notify.error(message)
  }
}

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Allowlist</h1>
        <p class="page-description">Addresses and CIDR blocks exempt from detection and bans.</p>
      </div>
      <button class="btn btn-primary" @click="showCreateForm = !showCreateForm">
        {{ showCreateForm ? 'Cancel' : 'Add Entry' }}
      </button>
    </div>

    <div v-if="showCreateForm" class="card" style="margin-bottom: 1.5rem;">
      <div class="card-header"><h2>Add Allowlist Entry</h2></div>
      <div class="card-body">
        <form @submit.prevent="createEntry" style="display: grid; gap: 1rem; max-width: 480px;">
          <div class="form-group">
            <label class="form-label" for="allow-ip">IP address or CIDR</label>
            <input id="allow-ip" v-model="form.ip_cidr" type="text" class="form-input" placeholder="10.0.0.0/8" required />
            <span class="form-hint">IP or CIDR e.g. 10.0.0.0/8</span>
          </div>
          <div class="form-group">
            <label class="form-label" for="allow-desc">Description</label>
            <input id="allow-desc" v-model="form.description" type="text" class="form-input" placeholder="Why is this address exempt?" />
          </div>
          <div>
            <button type="submit" class="btn btn-primary" :disabled="createLoading">
              {{ createLoading ? 'Adding...' : 'Add Entry' }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <div class="card">
      <div class="card-header"><h2>Allowlisted Addresses</h2></div>

      <!-- Loading: skeleton rows preserve the table layout while data streams in. -->
      <div v-if="loading" class="card-body">
        <table class="table">
          <thead>
            <tr>
              <th>IP / CIDR</th>
              <th>Description</th>
              <th>Added By</th>
              <th>Added</th>
              <th style="text-align: right;">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in 5" :key="n">
              <td><span class="skeleton skeleton-text" style="width: 130px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 45%;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 110px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 120px;"></span></td>
              <td><span class="skeleton skeleton-text" style="width: 48px; margin-left: auto;"></span></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="entries.length === 0" class="empty-state">
        <h3>No allowlist entries</h3>
        <p v-if="form.ip_cidr">The prefilled address has not been saved yet — use the form above.</p>
        <p v-else>Every address is subject to detection. Add trusted infrastructure (monitoring probes, load balancers) to exempt it.</p>
      </div>

      <div v-else class="card-body">
        <div class="table-wrapper">
          <table class="table">
            <thead>
              <tr>
                <th>IP / CIDR</th>
                <th>Description</th>
                <th>Added By</th>
                <th>Added</th>
                <th style="text-align: right;">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="entry in entries" :key="entry.id">
                <td><code>{{ entry.ip_cidr }}</code></td>
                <td>{{ entry.description || '—' }}</td>
                <td><code>{{ entry.created_by }}</code></td>
                <td style="white-space: nowrap;"><span :title="formatDate(entry.created_at)">{{ formatDate(entry.created_at) }}</span></td>
                <td>
                  <div class="table-actions">
                    <button class="btn btn-icon btn-icon-danger" title="Remove entry" @click="deleteEntry(entry)">
                      <span class="mdi mdi-trash-can-outline"></span>
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
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

.form-hint {
  display: block;
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 6px;
  font-family: var(--font-mono);
}

/* Addresses and actor emails are technical identifiers — set them in the mono face. */
code {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--text-secondary);
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  padding: 1px 6px;
}
</style>
