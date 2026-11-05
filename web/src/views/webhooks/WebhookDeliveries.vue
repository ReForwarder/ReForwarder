<script setup lang="ts">
import { ref } from 'vue'
import { webhookDeliveriesApi } from '../../api/webhooks'
import type { WebhookDelivery } from '../../api/types'
import Pagination from '../../components/Pagination.vue'
import { usePagination } from '../../composables/usePagination'
import { useConfirm } from '../../composables/useConfirm'
import { useNotificationStore } from '../../stores/notification'
import { useWorkspaceStore } from '../../stores/workspace'

const loading = ref(true)
const deliveries = ref<WebhookDelivery[]>([])
const replayingId = ref<number | null>(null)

const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await webhookDeliveriesApi.list(page)
    deliveries.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load webhook deliveries', e)
  } finally {
    loading.value = false
  }
})

function statusBadgeClass(status: string) {
  return status === 'success' ? 'badge badge-success' : 'badge badge-danger'
}

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}

// R-06: re-deliver a failed delivery with its original payload after an
// explicit confirmation. Only failed deliveries offer the action, and the
// backend caps replays at 5 per delivery per hour.
async function replayDelivery(d: WebhookDelivery) {
  const confirmed = await confirm({
    title: 'Replay Webhook Delivery',
    message: `Re-deliver the "${d.event}" delivery to the webhook's endpoint with the original payload? The attempt is recorded in this delivery's history.`,
    confirmText: 'Replay',
    variant: 'info',
  })
  if (!confirmed) return
  replayingId.value = d.id
  try {
    const res = await webhookDeliveriesApi.replay(d.id)
    const outcome = res.data.data
    if (outcome?.status === 'success') {
      notify.success(`Replay delivered (HTTP ${outcome.http_status_code})`)
    } else {
      notify.error(`Replay failed (HTTP ${outcome?.http_status_code || '-'}): ${outcome?.error_message || 'the endpoint rejected the delivery'}`)
    }
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to replay delivery')
  } finally {
    replayingId.value = null
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Webhook Deliveries</h1>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <div v-else class="card">
      <div v-if="deliveries.length === 0" class="empty-state">
        <h3>No deliveries yet</h3>
        <p>Webhook delivery attempts will appear here.</p>
      </div>
      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Event</th>
                <th>Status</th>
                <th>HTTP Status</th>
                <th>Attempt</th>
                <th>Error</th>
                <th>Delivered At</th>
                <th v-if="wsStore.canEdit">Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="d in deliveries" :key="d.id">
                <td><span class="badge badge-neutral">{{ d.event }}</span></td>
                <td><span :class="statusBadgeClass(d.status)">{{ d.status }}</span></td>
                <td>{{ d.http_status_code || '-' }}</td>
                <td>{{ d.attempt }}</td>
                <td class="truncate" style="max-width: 280px;">{{ d.error_message || '-' }}</td>
                <td>{{ formatDate(d.created_at) }}</td>
                <td v-if="wsStore.canEdit">
                  <button
                    v-if="d.status === 'failed'"
                    class="btn btn-secondary btn-sm"
                    :disabled="replayingId === d.id"
                    @click="replayDelivery(d)"
                  >
                    {{ replayingId === d.id ? 'Replaying...' : 'Replay' }}
                  </button>
                  <span v-else style="color: var(--text-secondary);">-</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
      </template>
    </div>
  </div>
</template>

<style scoped>
/* Event names are machine codes — mono badges. */
.badge-neutral {
  font-family: var(--font-mono);
  font-size: 11.5px;
}
</style>
