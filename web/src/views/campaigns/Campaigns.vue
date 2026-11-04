<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { campaignsApi } from '../../api/campaigns'
import { subscriberListsApi } from '../../api/subscriberLists'
import { segmentsApi } from '../../api/segments'
import type { Campaign, CampaignStatus, Pageable, SegmentRow, SubscriberListItem } from '../../api/types'
import SearchableSelect from '../../components/SearchableSelect.vue'
import { useNotificationStore } from '../../stores/notification'
import { useConfirm } from '../../composables/useConfirm'
import { useModalSafeClose } from '../../composables/useModalSafeClose'
import { useWorkspaceStore } from '../../stores/workspace'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'

const router = useRouter()
const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

const campaigns = ref<Campaign[]>([])
const loading = ref(true)
const statusFilter = ref<string>('')

const statusTabs: { label: string; value: string }[] = [
  { label: 'All', value: '' },
  { label: 'Draft', value: 'draft' },
  { label: 'Scheduled', value: 'scheduled' },
  { label: 'Sending', value: 'sending' },
  { label: 'Sent', value: 'sent' },
  { label: 'Paused', value: 'paused' },
  { label: 'Cancelled', value: 'cancelled' },
]

// Create modal
const showModal = ref(false)
const formName = ref('')
const formSubject = ref('')
const formFromEmail = ref('')
const formFromName = ref('')
const formTemplateId = ref<number | null>(null)
const formListId = ref<number | null>(null)
const formSendRate = ref(0)
const saving = ref(false)

// Audience: subscriber list XOR segment — the payload carries exactly one.
const audienceMode = ref<'list' | 'segment'>('list')
const formSegmentId = ref('')

// Lists and segments for the create form
const lists = ref<SubscriberListItem[]>([])
const segments = ref<SegmentRow[]>([])
const templates = ref<{ id: number; name: string }[]>([])


const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await campaignsApi.list(page, pageable.value.size, statusFilter.value || undefined)
    campaigns.value = res.data.data ?? []
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load campaigns', e)
  } finally {
    loading.value = false
  }
})

function switchStatus(status: string) {
  statusFilter.value = status
  goToPage(0)
}

async function loadFormData() {
  try {
    const [listsRes] = await Promise.all([
      subscriberListsApi.list(0, 100),
    ])
    lists.value = listsRes.data.data ?? []
  } catch {
    // non-critical
  }
  try {
    const segmentsRes = await segmentsApi.list(0, 100)
    segments.value = segmentsRes.data.data ?? []
  } catch {
    // non-critical
  }
  try {
    const { default: api } = await import('../../api/client')
    const res = await api.get<any>('/workspaces/current/templates', { params: { page: 0, size: 100 } })
    templates.value = (res.data.data ?? []).map((t: any) => ({ id: t.id, name: t.name }))
  } catch {
    // non-critical
  }
}

const segmentOptions = computed(() => segments.value.map((s) => ({ value: String(s.id), label: s.name })))
const selectedSegment = computed(() => segments.value.find((s) => String(s.id) === formSegmentId.value) ?? null)

// Recipient hint for the chosen segment: one preview call, count only.
const segmentCount = ref<number | null>(null)
const segmentCountLoading = ref(false)
let countSeq = 0

watch(selectedSegment, async (seg) => {
  const mySeq = ++countSeq
  segmentCount.value = null
  if (!seg) return
  segmentCountLoading.value = true
  try {
    const res = await segmentsApi.preview({ condition: seg.condition, page: 0, size: 1 })
    if (mySeq !== countSeq) return
    segmentCount.value = res.data.data.count
  } catch {
    // The hint is cosmetic; the count is re-evaluated server-side at send time.
    if (mySeq === countSeq) segmentCount.value = null
  } finally {
    if (mySeq === countSeq) segmentCountLoading.value = false
  }
})

function openCreate() {
  formName.value = ''
  formSubject.value = ''
  formFromEmail.value = ''
  formFromName.value = ''
  formTemplateId.value = null
  formListId.value = null
  formSendRate.value = 0
  audienceMode.value = 'list'
  formSegmentId.value = ''
  segmentCount.value = null
  showModal.value = true
  loadFormData()
}

async function saveCampaign() {
  const missingAudience = audienceMode.value === 'list' ? !formListId.value : !formSegmentId.value
  if (!formName.value.trim() || !formSubject.value.trim() || !formFromEmail.value.trim() || !formTemplateId.value || missingAudience) {
    notify.error('Please fill in all required fields')
    return
  }
  saving.value = true
  try {
    await campaignsApi.create({
      name: formName.value.trim(),
      subject: formSubject.value.trim(),
      from_email: formFromEmail.value.trim(),
      from_name: formFromName.value.trim(),
      template_id: formTemplateId.value,
      ...(audienceMode.value === 'list'
        ? { list_id: formListId.value! }
        : { segment_id: Number(formSegmentId.value) }),
      send_rate: formSendRate.value,
    })
    notify.success('Campaign created')
    showModal.value = false
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to create campaign')
  } finally {
    saving.value = false
  }
}

async function deleteCampaign(campaign: Campaign) {
  const warnLossOfData = ['sent', 'paused'].includes(campaign.status)
  const confirmed = await confirm({
    title: 'Delete Campaign',
    message: warnLossOfData
      ? `Delete "${campaign.name}"? Per-message delivery records will be removed. An aggregate stats snapshot is kept for reporting.`
      : `Are you sure you want to delete "${campaign.name}"? This action cannot be undone.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await campaignsApi.delete(campaign.id)
    notify.success('Campaign deleted')
    await goToPage(pageable.value.current_page)
  } catch (e: any) {
    notify.error(e?.response?.data?.error?.message || 'Failed to delete campaign')
  }
}

function canDeleteStatus(status: CampaignStatus): boolean {
  return status !== 'sending'
}

// Brand status mapping: draft=neutral, waiting (scheduled/paused)=yellow,
// sending=orange accent, sent=green, cancelled=red.
function statusBadgeClass(status: CampaignStatus): string {
  switch (status) {
    case 'draft': return 'badge badge-neutral'
    case 'scheduled': return 'badge badge-warning'
    case 'sending': return 'badge badge-info'
    case 'sent': return 'badge badge-success'
    case 'paused': return 'badge badge-warning'
    case 'cancelled': return 'badge badge-danger'
    default: return 'badge'
  }
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

function statsLabel(campaign: Campaign): string {
  if (!campaign.stats) return '-'
  const s = campaign.stats
  if (s.total === 0) return '0'
  return `${s.sent}/${s.total} sent`
}

const { watchClickStart, confirmClickEnd } = useModalSafeClose(() => {
  showModal.value = false
})

</script>

<template>
  <div>
    <div class="page-header">
      <h1>Campaigns</h1>
      <button v-if="wsStore.canEdit" class="btn btn-primary" @click="openCreate">Create Campaign</button>
    </div>

    <!-- Status filter tabs -->
    <div class="tabs" style="margin-bottom: 16px;">
      <button v-for="tab in statusTabs" :key="tab.value" class="btn btn-sm"
        :class="statusFilter === tab.value ? 'btn-primary' : 'btn-secondary'" @click="switchStatus(tab.value)"
        style="margin-right: 4px;">
        {{ tab.label }}
      </button>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <div v-else class="card">
      <div v-if="campaigns.length === 0" class="empty-state">
        <h3>No Campaigns</h3>
        <p>Create a campaign to start sending emails to your subscribers.</p>
      </div>

      <template v-else>
        <div class="table-wrapper">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Progress</th>
                <th>Created</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="campaign in campaigns" :key="campaign.id">
                <td>
                  <a class="link" @click="router.push(`/campaigns/${campaign.id}`)">{{ campaign.name }}</a>
                  <div style="font-size: 0.85em; color: var(--text-secondary)">{{ campaign.subject }}</div>
                </td>
                <td><span :class="statusBadgeClass(campaign.status)">{{ campaign.status }}</span></td>
                <td class="col-progress">{{ statsLabel(campaign) }}</td>
                <td>{{ formatDate(campaign.created_at) }}</td>
                <td>
                  <div style="display: flex; gap: 6px">
                    <button class="btn btn-secondary btn-sm"
                      @click="router.push(`/campaigns/${campaign.id}`)">View</button>
                    <button v-if="wsStore.canEdit && canDeleteStatus(campaign.status)" class="btn btn-danger btn-sm"
                      @click="deleteCampaign(campaign)">Delete</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <Pagination :pageable="pageable" @page="goToPage" />
    
      </template>
    </div>

    <!-- Create Modal -->
    <div v-if="showModal" class="modal-overlay" @mousedown="watchClickStart" @mouseup="confirmClickEnd">
      <div class="modal" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h3>Create Campaign</h3>
        </div>
        <div class="modal-body">
          <div class="form-group">
            <label class="form-label">Name *</label>
            <input v-model="formName" class="form-input" placeholder="e.g. March Newsletter" />
          </div>
          <div class="form-group">
            <label class="form-label">Subject *</label>
            <input v-model="formSubject" class="form-input" placeholder="Email subject line" />
          </div>
          <div class="form-group">
            <label class="form-label">From Email *</label>
            <input v-model="formFromEmail" class="form-input" placeholder="sender@example.com" />
          </div>
          <div class="form-group">
            <label class="form-label">From Name</label>
            <input v-model="formFromName" class="form-input" placeholder="Sender Name" />
          </div>
          <div class="form-group">
            <label class="form-label">Template *</label>
            <select v-model="formTemplateId" class="form-select">
              <option :value="null" disabled>Select template</option>
              <option v-for="t in templates" :key="t.id" :value="t.id">{{ t.name }}</option>
            </select>
          </div>
          <div class="form-group">
            <label class="form-label">Audience *</label>
            <div class="audience-toggle">
              <label class="audience-option">
                <input type="radio" value="list" v-model="audienceMode" />
                <span>Subscriber list</span>
              </label>
              <label class="audience-option">
                <input type="radio" value="segment" v-model="audienceMode" />
                <span>Segment</span>
              </label>
            </div>
          </div>
          <div v-if="audienceMode === 'list'" class="form-group">
            <label class="form-label">Subscriber List *</label>
            <select v-model="formListId" class="form-select">
              <option :value="null" disabled>Select list</option>
              <option v-for="l in lists" :key="l.id" :value="l.id">{{ l.name }} ({{ l.member_count }} members)</option>
            </select>
          </div>
          <div v-else class="form-group">
            <label class="form-label">Segment *</label>
            <SearchableSelect
              :options="segmentOptions"
              :model-value="formSegmentId"
              placeholder="Select segment…"
              empty-text="No segments found"
              @update:model-value="formSegmentId = $event"
            />
            <p v-if="segments.length === 0" class="form-hint">
              No segments yet — create one under Audience → Segments.
            </p>
            <p v-else-if="segmentCountLoading" class="form-hint">Counting recipients…</p>
            <p v-else-if="segmentCount !== null" class="form-hint">
              ≈ {{ segmentCount.toLocaleString() }} recipients currently match this segment
            </p>
            <p v-else class="form-hint">Recipients are evaluated when the campaign is sent.</p>
          </div>
          <div class="form-group">
            <label class="form-label">Send Rate (msgs/min, 0 = unlimited)</label>
            <input v-model.number="formSendRate" type="number" class="form-input" min="0" />
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-secondary" @click="showModal = false">Cancel</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveCampaign">
            {{ saving ? 'Creating...' : 'Create' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Monochrome ink row link; the orange accent is reserved for the hover state. */
.link {
  color: var(--text-primary);
  cursor: pointer;
  font-weight: 500;
}

.link:hover {
  color: var(--accent);
  text-decoration: underline;
}

/* Delivery counts align across rows. */
.col-progress {
  font-variant-numeric: tabular-nums;
}

/* Audience radio: subscriber list vs segment. */
.audience-toggle {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}

.audience-option {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  color: var(--text-secondary);
  cursor: pointer;
}

.audience-option input {
  cursor: pointer;
}
</style>
