<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { domainsApi } from '../../api/domains'
import type { Domain, DnsRecord, Pageable, SigningKey } from '../../api/types'
import { useNotificationStore } from '../../stores/notification'
import { useConfirm } from '../../composables/useConfirm'
import { useModalSafeClose } from '../../composables/useModalSafeClose';
import { useWorkspaceStore } from '../../stores/workspace'
import { usePagination } from '@/composables/usePagination'
import Pagination from '@/components/Pagination.vue'


const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

const domains = ref<Domain[]>([])
const loading = ref(true)
const currentPage = ref(0)

const showAddModal = ref(false)
const newDomain = ref('')
const saving = ref(false)

const expandedDomainId = ref<number | null>(null)
const dnsLoading = ref(false)
const dnsRecordsDomain = ref<Domain | null>(null)
const verifying = ref(false)

type RecordKey = 'verification' | 'spf' | 'dkim' | 'dmarc'

interface RecordRow {
  key: RecordKey
  label: string
  description: string
  record: DnsRecord
  verified: boolean
}

const recordDescriptions: Record<RecordKey, { label: string; description: string }> = {
  verification: {
    label: 'Ownership Verification',
    description: 'Proves you control this domain. ReForwarder will not send for an unverified domain when strict mode is enabled.',
  },
  spf: {
    label: 'SPF',
    description: 'Authorizes ReForwarder’s mail servers to send on your behalf. Required to avoid spam filtering.',
  },
  dkim: {
    label: 'DKIM',
    description: 'Cryptographically signs each message so receivers can verify it wasn’t tampered with.',
  },
  dmarc: {
    label: 'DMARC',
    description: 'Tells receivers how to handle mail that fails SPF/DKIM. Start with p=none for monitoring.',
  },
}

const recordRows = computed<RecordRow[]>(() => {
  const d = dnsRecordsDomain.value
  if (!d || !d.dns_records) return []
  return [
    { key: 'verification', ...recordDescriptions.verification, record: d.dns_records.verification, verified: !!d.ownership_verified },
    { key: 'spf', ...recordDescriptions.spf, record: d.dns_records.spf, verified: !!d.spf_verified },
    { key: 'dkim', ...recordDescriptions.dkim, record: d.dns_records.dkim, verified: !!d.dkim_verified },
    { key: 'dmarc', ...recordDescriptions.dmarc, record: d.dns_records.dmarc, verified: !!d.dmarc_verified },
  ]
})

function hostShortForm(fullHost: string, domain: string): string {
  if (!fullHost || !domain) return fullHost
  if (fullHost === domain) return '@'
  const suffix = '.' + domain
  if (fullHost.endsWith(suffix)) return fullHost.slice(0, -suffix.length)
  return fullHost
}

// DNS convention: an empty host is the zone apex, written "@". The backend
// omits the name for apex records, which rendered as a collapsed empty field.
function hostDisplay(name: string): string {
  return name && name.trim() ? name : '@'
}

async function copy(text: string, label = 'Value') {
  try {
    await navigator.clipboard.writeText(text)
    notify.success(`${label} copied`)
  } catch {
    notify.error('Copy failed')
  }
}

async function copyAllRecords() {
  const d = dnsRecordsDomain.value
  if (!d || !d.dns_records) return
  const lines = ['Type\tHost\tValue']
  for (const row of recordRows.value) {
    lines.push(`${row.record.type}\t${hostDisplay(row.record.host)}\t${row.record.value}`)
  }
  await copy(lines.join('\n'), 'All records')
}

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await domainsApi.list(currentPage.value)
    domains.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e) {
    console.error('Failed to load domains', e)
  } finally {
    loading.value = false
  }
})


async function addDomain() {
  if (!newDomain.value.trim()) return
  saving.value = true
  try {
    await domainsApi.create(newDomain.value.trim())
    notify.success('Domain added')
    showAddModal.value = false
    newDomain.value = ''
    await goToPage(pageable.value.current_page)
  } catch {
    notify.error('Failed to add domain')
  } finally {
    saving.value = false
  }
}

async function verifyDomain(domain: Domain) {
  verifying.value = true
  try {
    await domainsApi.verify(domain.id)
    notify.success(`Verification initiated for ${domain.domain}`)
    await goToPage(pageable.value.current_page)
    if (expandedDomainId.value === domain.id) {
      const res = await domainsApi.get(domain.id)
      dnsRecordsDomain.value = res.data.data
    }
  } catch {
    notify.error('Verification failed')
  } finally {
    verifying.value = false
  }
}

async function viewDnsRecords(domain: Domain) {
  if (expandedDomainId.value === domain.id) {
    expandedDomainId.value = null
    dnsRecordsDomain.value = null
    return
  }
  dnsLoading.value = true
  expandedDomainId.value = domain.id
  signingFreshId.value = null
  loadSigningKeys(domain.id)
  try {
    const res = await domainsApi.get(domain.id)
    dnsRecordsDomain.value = res.data.data
  } catch {
    notify.error('Failed to load DNS records')
    expandedDomainId.value = null
  } finally {
    dnsLoading.value = false
  }
}

// ---- Managed DKIM signing keys (R-02) ----
const signingKeys = ref<SigningKey[]>([])
const signingLoading = ref(false)
const signingSelector = ref('')
const signingAlgorithm = ref<'rsa-sha256' | 'ed25519-sha256'>('rsa-sha256')
const signingBusy = ref(false)
const signingMode = ref<'generate' | 'rotate'>('generate')
const signingFreshId = ref<number | null>(null)

const activeSigningKey = computed(() => signingKeys.value.find((k) => k.status === 'active') || null)

async function loadSigningKeys(domainId: number) {
  signingLoading.value = true
  try {
    const res = await domainsApi.listSigningKeys(domainId)
    signingKeys.value = res.data.data
    signingMode.value = activeSigningKey.value ? 'rotate' : 'generate'
  } catch {
    signingKeys.value = []
  } finally {
    signingLoading.value = false
  }
}

const statusLabels: Record<string, string> = {
  active: 'Active',
  retiring: 'Retiring',
  retired: 'Retired',
}

const dnsStateLabels: Record<string, string> = {
  published: 'DNS published',
  'pending-retry': 'DNS pending',
}

async function createOrRotateKey(domain: Domain) {
  const selector = signingSelector.value.trim()
  if (!selector) return
  const rotating = signingMode.value === 'rotate'
  const confirmed = rotating
    ? await confirm({
        title: 'Rotate DKIM key',
        message: `Create replacement key "${selector}" for ${domain.domain}? The current key stays active for mail in transit and must be published, verified, then retired manually.`,
        confirmText: 'Rotate',
        variant: 'info',
      })
    : true
  if (!confirmed) return
  signingBusy.value = true
  try {
    const res = rotating
      ? await domainsApi.rotateSigningKey(domain.id, selector, signingAlgorithm.value)
      : await domainsApi.generateSigningKey(domain.id, selector, signingAlgorithm.value)
    notify.success(rotating ? 'Signing key rotated — publish the new DNS record' : 'Signing key generated — publish the DNS record')
    signingSelector.value = ''
    signingFreshId.value = res.data.data.id
    await loadSigningKeys(domain.id)
  } catch (e: any) {
    notify.error(e?.response?.data?.message || 'Failed to create signing key')
  } finally {
    signingBusy.value = false
  }
}

async function verifyKeyDns(domain: Domain, key: SigningKey) {
  signingBusy.value = true
  try {
    const res = await domainsApi.verifySigningKeyDns(domain.id, key.id)
    if (res.data.data.published) {
      notify.success(`TXT record for "${key.selector}" confirmed in DNS`)
    } else {
      notify.error(res.data.data.error || 'Record not found in DNS yet — propagation can take up to 48 hours')
    }
    await loadSigningKeys(domain.id)
  } catch (e: any) {
    notify.error(e?.response?.data?.message || 'DNS check failed')
  } finally {
    signingBusy.value = false
  }
}

async function deleteKey(domain: Domain, key: SigningKey) {
  const confirmed = await confirm({
    title: 'Delete signing key',
    message: `Delete the retired key "${key.selector}"? Signatures made with it can no longer be verified once receivers stop caching it.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!confirmed) return
  signingBusy.value = true
  try {
    await domainsApi.deleteSigningKey(domain.id, key.id)
    notify.success('Signing key deleted')
    await loadSigningKeys(domain.id)
  } catch (e: any) {
    notify.error(e?.response?.data?.message || 'Failed to delete signing key')
  } finally {
    signingBusy.value = false
  }
}

async function deleteDomain(domain: Domain) {
  const confirmed = await confirm({
    title: 'Delete Domain',
    message: `Are you sure you want to delete "${domain.domain}"? This will remove all associated DNS records and verification status.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await domainsApi.delete(domain.id)
    notify.success('Domain deleted')
    if (expandedDomainId.value === domain.id) {
      expandedDomainId.value = null
      dnsRecordsDomain.value = null
    }
    await goToPage(pageable.value.current_page)
  } catch {
    notify.error('Failed to delete domain')
  }
}
const { watchClickStart, confirmClickEnd } = useModalSafeClose(() => {
  showAddModal.value = false;
});
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Domains</h1>
      <button v-if="wsStore.canEdit" class="btn btn-primary" @click="showAddModal = true">Add Domain</button>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <template v-else>
      <div class="card">
        <div class="table-wrapper" v-if="domains.length > 0">
          <table>
            <thead>
              <tr>
                <th>Domain</th>
                <th>Ownership</th>
                <th>SPF</th>
                <th>DKIM</th>
                <th>DMARC</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="domain in domains" :key="domain.id">
                <tr>
                  <td class="mono">{{ domain.domain }}</td>
                  <td>
                    <span v-if="domain.ownership_verified" class="verified">&#10003;</span>
                    <span v-else class="unverified">&#10005;</span>
                  </td>
                  <td>
                    <span v-if="domain.spf_verified" class="verified">&#10003;</span>
                    <span v-else class="unverified">&#10005;</span>
                  </td>
                  <td>
                    <span v-if="domain.dkim_verified" class="verified">&#10003;</span>
                    <span v-else class="unverified">&#10005;</span>
                  </td>
                  <td>
                    <span v-if="domain.dmarc_verified" class="verified">&#10003;</span>
                    <span v-else class="unverified">&#10005;</span>
                  </td>
                  <td>
                    <div class="flex gap-2">
                      <button v-if="wsStore.canEdit" class="btn btn-secondary btn-sm"
                        @click="verifyDomain(domain)">Verify</button>
                      <button class="btn btn-secondary btn-sm" @click="viewDnsRecords(domain)">
                        {{ expandedDomainId === domain.id ? 'Hide DNS' : 'View DNS Records' }}
                      </button>
                      <button v-if="wsStore.canEdit" class="btn btn-danger btn-sm"
                        @click="deleteDomain(domain)">Delete</button>
                    </div>
                  </td>
                </tr>
                <tr v-if="expandedDomainId === domain.id">
                  <td colspan="6" style="padding: 20px 16px; background: var(--bg-tertiary);">
                    <div v-if="dnsLoading" class="loading-page" style="min-height: 100px;">
                      <div class="spinner"></div>
                    </div>
                    <div v-else-if="dnsRecordsDomain && dnsRecordsDomain.dns_records" class="dns-panel">
                      <div class="dns-panel-header">
                        <div>
                          <h4 class="dns-panel-title">DNS records for {{ domain.domain }}</h4>
                          <p class="dns-panel-subtitle">
                            Add the records below at your DNS provider, then click
                            <strong>Re-check</strong>. Propagation is usually under 15 minutes but can take up to 48
                            hours.
                          </p>
                        </div>
                        <div class="dns-panel-actions">
                          <button class="btn btn-secondary btn-sm" @click="copyAllRecords">Copy all</button>
                          <button v-if="wsStore.canEdit" class="btn btn-primary btn-sm" :disabled="verifying"
                            @click="verifyDomain(domain)">
                            {{ verifying ? 'Checking…' : 'Re-check' }}
                          </button>
                        </div>
                      </div>

                      <!-- ==================== DKIM signing keys (R-02) ==================== -->
                      <div class="signing-section">
                        <div class="signing-head">
                          <div>
                            <h4 class="dns-panel-title">DKIM signing</h4>
                            <p class="dns-panel-subtitle">
                              Managed keys sign every message sent from {{ domain.domain }} once the TXT record is
                              confirmed. Without a key, mail goes out unsigned (upstream signer mode).
                            </p>
                          </div>
                        </div>

                        <div v-if="signingLoading" class="loading-page" style="min-height: 60px">
                          <div class="spinner"></div>
                        </div>

                        <template v-else>
                          <div v-if="signingKeys.length === 0" class="signing-empty">
                            No signing key yet — generate one, publish the DNS record, then confirm it below.
                          </div>

                          <div v-for="key in signingKeys" :key="key.id" class="signing-key"
                            :class="{ 'signing-key-fresh': signingFreshId === key.id }">
                            <div class="signing-key-head">
                              <div class="signing-key-id">
                                <code class="signing-selector">{{ key.selector }}</code>
                                <span class="dns-type-badge">{{ key.algorithm }}</span>
                              </div>
                              <div class="signing-key-badges">
                                <span class="badge" :class="{
                                  'badge-success': key.status === 'active',
                                  'badge-neutral': key.status === 'retiring',
                                  'badge-error': key.status === 'retired',
                                }">{{ statusLabels[key.status] || key.status }}</span>
                                <span class="badge" :class="key.dns_state === 'published' ? 'badge-success' : 'badge-neutral'">
                                  {{ dnsStateLabels[key.dns_state] || key.dns_state }}
                                </span>
                              </div>
                            </div>

                            <div v-if="key.dns_record" class="dns-field">
                              <div class="dns-field-label">DNS TXT record to publish</div>
                              <div class="dns-field-row">
                                <code class="dns-field-value">{{ key.dns_record.host }}</code>
                                <button type="button" class="btn btn-ghost btn-xs"
                                  @click="copy(key.dns_record.host, 'Host')">Copy</button>
                              </div>
                              <div class="dns-field-row">
                                <code class="dns-field-value dns-field-value-block">{{ key.dns_record.value }}</code>
                                <button type="button" class="btn btn-ghost btn-xs"
                                  @click="copy(key.dns_record.value, 'TXT record')">Copy</button>
                              </div>
                            </div>

                            <div class="signing-actions">
                              <button v-if="wsStore.canEdit && key.dns_state !== 'published'" class="btn btn-primary btn-sm"
                                :disabled="signingBusy" @click="verifyKeyDns(domain, key)">
                                Check DNS
                              </button>
                              <button v-if="wsStore.canEdit && key.status === 'retired'" class="btn btn-danger btn-sm"
                                :disabled="signingBusy" @click="deleteKey(domain, key)">
                                Delete
                              </button>
                              <span v-if="key.status === 'retiring'" class="signing-note">
                                Keep until receivers stop seeing its signatures, then retire and delete it.
                              </span>
                            </div>
                          </div>

                          <div v-if="wsStore.canEdit" class="signing-form">
                            <div class="signing-form-title">
                              {{ signingMode === 'rotate' ? 'Rotate key' : 'Generate key' }}
                            </div>
                            <div class="signing-form-row">
                              <input v-model="signingSelector" type="text" class="form-input signing-input"
                                placeholder="selector, e.g. posta or mail2026" />
                              <select v-model="signingAlgorithm" class="form-input signing-input signing-select">
                                <option value="rsa-sha256">rsa-sha256 (recommended)</option>
                                <option value="ed25519-sha256">ed25519-sha256 (compact)</option>
                              </select>
                              <button class="btn btn-primary btn-sm" :disabled="signingBusy || !signingSelector.trim()"
                                @click="createOrRotateKey(domain)">
                                {{ signingBusy ? 'Working…' : signingMode === 'rotate' ? 'Rotate' : 'Generate' }}
                              </button>
                            </div>
                            <p v-if="signingMode === 'rotate'" class="signing-hint">
                              The current active key becomes retiring; the replacement is used once its DNS record is
                              verified.
                            </p>
                          </div>
                        </template>
                      </div>

                      <div class="dns-record-list">
                        <div v-for="row in recordRows" :key="row.key" class="dns-record"
                          :class="{ 'dns-record-highlight': row.key === 'verification', 'dns-record-verified': row.verified }">
                          <div class="dns-record-head">
                            <div class="dns-record-head-left">
                              <span class="dns-label">{{ row.label }}</span>
                              <span class="dns-type-badge">{{ row.record.type }}</span>
                            </div>
                            <span v-if="row.verified" class="dns-status dns-status-ok">
                              &#10003; Verified
                            </span>
                            <span v-else class="dns-status dns-status-pending">
                              &#9679; Pending
                            </span>
                          </div>
                          <p class="dns-record-desc">{{ row.description }}</p>

                          <div class="dns-field">
                            <div class="dns-field-label">Host / Name</div>
                            <div class="dns-field-row">
                              <code class="dns-field-value">{{ hostDisplay(row.record.host) }}</code>
                              <button type="button" class="btn btn-ghost btn-xs"
                                @click="copy(hostDisplay(row.record.host), 'Host')">Copy</button>
                            </div>
                            <div v-if="hostShortForm(row.record.host, domain.domain) !== row.record.host"
                              class="dns-field-hint">
                              Some providers (Cloudflare, Route 53, GoDaddy) only accept the subdomain part —
                              use <code>{{ hostShortForm(row.record.host, domain.domain) }}</code> instead.
                            </div>
                          </div>

                          <div class="dns-field">
                            <div class="dns-field-label">Value</div>
                            <div class="dns-field-row">
                              <code class="dns-field-value dns-field-value-block">{{ row.record.value }}</code>
                              <button type="button" class="btn btn-ghost btn-xs"
                                @click="copy(row.record.value, 'Value')">Copy</button>
                            </div>
                          </div>

                          <div class="dns-field-hint dns-ttl-hint">
                            TTL: Auto (or 3600). Leave priority blank unless your provider requires one.
                          </div>
                        </div>
                      </div>
                    </div>
                    <div v-else class="text-muted text-sm">
                      No DNS records available for this domain.
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>

        <div v-else class="empty-state">
          <h3>No domains</h3>
          <p>Add a domain to verify your sending identity.</p>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />


      </div>
    </template>

    <!-- Add Domain Modal -->
    <div v-if="showAddModal" class="modal-overlay" @mousedown="watchClickStart" @mouseup="confirmClickEnd">
      <div class="modal" @mousedown.stop @mouseup.stop>
        <div class="modal-header">
          <h3>Add Domain</h3>
        </div>
        <form @submit.prevent="addDomain">
          <div class="modal-body">
            <div class="form-group">
              <label class="form-label">Domain</label>
              <input v-model="newDomain" type="text" class="form-input" placeholder="example.com" required />
            </div>
          </div>
          <div class="modal-footer">
            <button type="button" class="btn btn-secondary" @click="showAddModal = false">Cancel</button>
            <button type="submit" class="btn btn-primary" :disabled="saving">
              {{ saving ? 'Adding...' : 'Add Domain' }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Domain names are hosts — technical identifiers in the mono face. */
.mono {
  font-family: var(--font-mono);
  font-size: 13px;
}

/* DKIM signing panel */
.signing-section {
  margin-bottom: 1.25rem;
  padding-bottom: 1rem;
  border-bottom: 1px solid var(--border-primary);
}

.signing-empty {
  font-size: 13px;
  color: var(--text-muted);
  margin-bottom: 0.75rem;
}

.signing-key {
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-md, 8px);
  padding: 0.75rem;
  margin-bottom: 0.75rem;
  background: var(--bg-secondary);
}

.signing-key-fresh {
  border-color: var(--primary, var(--text-primary));
}

.signing-key-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.5rem;
  flex-wrap: wrap;
}

.signing-key-id {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.signing-selector {
  font-family: var(--font-mono);
  font-weight: 600;
}

.signing-key-badges {
  display: flex;
  gap: 0.5rem;
}

.signing-actions {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-top: 0.5rem;
  flex-wrap: wrap;
}

.signing-note {
  font-size: 12px;
  color: var(--text-muted);
}

.signing-form {
  margin-top: 0.75rem;
}

.signing-form-title {
  font-size: 13px;
  font-weight: 600;
  margin-bottom: 0.5rem;
}

.signing-form-row {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.signing-input {
  max-width: 240px;
}

.signing-select {
  max-width: 220px;
}

.signing-hint {
  margin-top: 0.4rem;
  font-size: 12px;
  color: var(--text-muted);
}
</style>
