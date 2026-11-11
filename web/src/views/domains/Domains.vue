<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { domainsApi } from '../../api/domains'
import type { Domain, DnsRecord, Pageable, SigningKey, DomainHealthReport, DomainDmarcResponse, DomainReputationReport, IPPoolView, DnsStatusView, DnsPublishResult } from '../../api/types'
import { useNotificationStore } from '../../stores/notification'
import { apiMessage } from '../../composables/apiError'
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
  loadHealth(domain.id)
  loadDmarcSummary(domain.id)
  loadPools(domain.id)
  loadReputation(domain.id)
  loadDNSAutomation(domain.id)
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

async function retireKey(domain: Domain, key: SigningKey) {
  const confirmed = await confirm({
    title: 'Retire signing key',
    message: `Retire the key "${key.selector}"? Only do this once receivers no longer see its signatures — a retired key no longer verifies old mail and cannot be un-retired, but it can then be deleted.`,
    confirmText: 'Retire',
  })
  if (!confirmed) return
  signingBusy.value = true
  try {
    await domainsApi.retireSigningKey(domain.id, key.id)
    notify.success(`Signing key "${key.selector}" retired`)
    await loadSigningKeys(domain.id)
  } catch (e: any) {
    notify.error(e?.response?.data?.message || 'Failed to retire signing key')
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

// ---- Deliverability health (R-07) ----
// The stored report loads lazily on first row expansion and is cached per
// domain id; Diagnose replaces the cache entry with the fresh report the
// backend returns from the live re-check.
const healthByDomain = ref<Record<number, DomainHealthReport>>({})
const healthLoadingId = ref<number | null>(null)
const diagnosingId = ref<number | null>(null)

const expandedHealth = computed(() =>
  expandedDomainId.value ? healthByDomain.value[expandedDomainId.value] || null : null,
)

const healthCheckRows = computed(() => {
  const h = expandedHealth.value
  if (!h) return []
  return [
    { key: 'spf', label: 'SPF', result: h.spf },
    { key: 'dkim', label: 'DKIM', result: h.dkim },
    { key: 'dmarc', label: 'DMARC', result: h.dmarc },
  ]
})

async function loadHealth(domainId: number) {
  if (healthByDomain.value[domainId]) return
  healthLoadingId.value = domainId
  try {
    const res = await domainsApi.health(domainId)
    healthByDomain.value[domainId] = res.data.data
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to load deliverability health'))
  } finally {
    healthLoadingId.value = null
  }
}

async function diagnoseDomain(domain: Domain) {
  diagnosingId.value = domain.id
  try {
    const res = await domainsApi.diagnose(domain.id)
    healthByDomain.value[domain.id] = res.data.data
  } catch (e: any) {
    notify.error(apiMessage(e, 'Diagnose failed'))
  } finally {
    diagnosingId.value = null
  }
}

// ---- DMARC aggregate reports (R-17) ----
// Loaded alongside the health report on first expansion and cached per domain
// id. A failure here is non-fatal: the panel falls back to the empty state
// instead of an error toast.
const dmarcByDomain = ref<Record<number, DomainDmarcResponse | null>>({})
const dmarcLoadingId = ref<number | null>(null)

const expandedDmarc = computed(() =>
  expandedDomainId.value ? dmarcByDomain.value[expandedDomainId.value] ?? null : null,
)

const dispositionLabels: Record<string, string> = {
  none: 'None',
  quarantine: 'Quarantine',
  reject: 'Reject',
}

function dispositionBadgeClass(disposition: string): string {
  if (disposition === 'quarantine') return 'badge-warning'
  if (disposition === 'reject') return 'badge-danger'
  return 'badge-neutral'
}

async function loadDmarcSummary(domainId: number) {
  if (dmarcByDomain.value[domainId] !== undefined) return
  dmarcLoadingId.value = domainId
  try {
    const res = await domainsApi.dmarcSummary(domainId)
    dmarcByDomain.value[domainId] = res.data.data
  } catch {
    dmarcByDomain.value[domainId] = null
  } finally {
    dmarcLoadingId.value = null
  }
}

// ---- DNS automation (OB-6 W1, ADR-007 decision 1) ----
// The provider/publish/drift surface exists only when the deployment enabled
// POSTA_DNS_AUTOMATION_ENABLED; a 404/503 from the status call flips the
// feature off for the session instead of surfacing an error. The credential
// is sent ONCE at connect — every later view carries only the masked
// fingerprint the backend returns.
const dnsAutoByDomain = ref<Record<number, DnsStatusView | null>>({})
const dnsAutoLoadingId = ref<number | null>(null)
const dnsAutoBusy = ref(false)
const dnsProviderKind = ref<'cloudflare' | 'route53' | 'digitalocean'>('cloudflare')
const dnsToken = ref('')
const dnsAccessKeyID = ref('')
const dnsSecretAccessKey = ref('')
const dnsSessionToken = ref('')
const dnsRegion = ref('us-east-1')

const expandedDNSAuto = computed(() =>
  expandedDomainId.value ? dnsAutoByDomain.value[expandedDomainId.value] || null : null,
)

const dnsFeatureOff = computed(() =>
  expandedDomainId.value !== null && dnsAutoByDomain.value[expandedDomainId.value] === null,
)

const planKindLabels: Record<string, string> = {
  verification: 'Ownership verification',
  spf: 'SPF',
  dkim: 'DKIM',
  dmarc: 'DMARC',
  mx: 'MX (mail routing)',
  'return-path': 'Return-path (bounce host)',
}

const driftLabels: Record<string, string> = {
  unknown: 'Not compared yet',
  ok: 'In sync',
  drift: 'Drift detected',
}

const providerLabels: Record<string, string> = {
  cloudflare: 'Cloudflare',
  route53: 'Route53',
  digitalocean: 'DigitalOcean',
}

async function loadDNSAutomation(domainId: number) {
  if (dnsAutoByDomain.value[domainId] !== undefined) return
  dnsAutoLoadingId.value = domainId
  try {
    const res = await domainsApi.dnsProviderStatus(domainId)
    dnsAutoByDomain.value[domainId] = res.data.data
  } catch {
    // The routes are absent when the feature is disabled on this deployment.
    dnsAutoByDomain.value[domainId] = null
  } finally {
    dnsAutoLoadingId.value = null
  }
}

async function connectDNSProvider(domain: Domain) {
  dnsAutoBusy.value = true
  try {
    const credential = dnsProviderKind.value === 'route53'
      ? {
          access_key_id: dnsAccessKeyID.value.trim(),
          secret_access_key: dnsSecretAccessKey.value,
          session_token: dnsSessionToken.value.trim() || undefined,
          region: dnsRegion.value.trim() || 'us-east-1',
        }
      : { token: dnsToken.value }
    const res = await domainsApi.connectDNSProvider(domain.id, dnsProviderKind.value, credential)
    dnsAutoByDomain.value[domain.id] = { ...(dnsAutoByDomain.value[domain.id] || { plan: [] }), connected: res.data.data } as DnsStatusView
    dnsToken.value = ''
    dnsAccessKeyID.value = ''
    dnsSecretAccessKey.value = ''
    dnsSessionToken.value = ''
    notify.success(`${providerLabels[dnsProviderKind.value]} connected — credentials encrypted and masked`)
  } catch (e: any) {
    notify.error(apiMessage(e, 'Connecting the DNS provider failed'))
  } finally {
    dnsAutoBusy.value = false
  }
}

async function disconnectDNSProvider(domain: Domain) {
  const confirmed = await confirm({
    title: 'Revoke DNS provider',
    message: `Revoke the stored ${domain.domain} provider credential? Publication and drift history is kept, but automated publishing and drift checks stop until you connect again.`,
    confirmText: 'Revoke',
    variant: 'danger',
  })
  if (!confirmed) return
  dnsAutoBusy.value = true
  try {
    await domainsApi.disconnectDNSProvider(domain.id)
    if (dnsAutoByDomain.value[domain.id]) {
      dnsAutoByDomain.value[domain.id]!.connected = null
    }
    notify.success('DNS provider credential revoked')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Revoking the credential failed'))
  } finally {
    dnsAutoBusy.value = false
  }
}

async function publishDNSRecords(domain: Domain) {
  const confirmed = await confirm({
    title: 'Publish DNS records',
    message: `Write the planned record set for ${domain.domain} through the connected provider? Verification re-runs automatically afterwards; records may take time to propagate.`,
    confirmText: 'Publish',
    variant: 'info',
  })
  if (!confirmed) return
  dnsAutoBusy.value = true
  try {
    const res = await domainsApi.publishDNSRecords(domain.id)
    const result: DnsPublishResult = res.data.data
    notify.success(`Published ${result.records.length} record(s) — verification re-ran automatically`)
    // Refresh the status (publication + drift) and the health report so the
    // stored check booleans reflect the fresh verification.
    delete dnsAutoByDomain.value[domain.id]
    await loadDNSAutomation(domain.id)
    delete healthByDomain.value[domain.id]
    await loadHealth(domain.id)
  } catch (e: any) {
    notify.error(apiMessage(e, 'DNS publication failed'))
  } finally {
    dnsAutoBusy.value = false
  }
}

// ---- IP pools, warm-up, reputation (OB-6 W3, ADR-007 decision 7) ----
// The pool list (workspace pools + deployment-wide shared ones) loads once
// per expansion; the reputation detail loads with the health report. A
// reputation failure is non-fatal (the panel shows its empty state) — the
// rollup cron may not have produced a window yet.
const pools = ref<IPPoolView[]>([])
const poolsLoading = ref(false)
const poolBusy = ref(false)
const newPoolName = ref('')
const newPoolIP = ref('')
const warmupStartVolume = ref(100)
const warmupMultiplier = ref(2)
const warmupDays = ref(14)
const warmupBusy = ref(false)

const expandedPool = computed(() => {
  const pid = dnsRecordsDomain.value?.pool_id
  return pid ? pools.value.find((p) => p.id === pid) || null : null
})

async function loadPools(_domainId: number) {
  if (pools.value.length > 0) return
  poolsLoading.value = true
  try {
    const res = await domainsApi.listIPPools()
    pools.value = res.data.data
  } catch {
    pools.value = []
  } finally {
    poolsLoading.value = false
  }
}

async function createPool() {
  const name = newPoolName.value.trim()
  if (!name) return
  poolBusy.value = true
  try {
    const res = await domainsApi.createIPPool(name)
    pools.value.push(res.data.data)
    newPoolName.value = ''
    notify.success('IP pool created — add member IPs next')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to create IP pool'))
  } finally {
    poolBusy.value = false
  }
}

async function addPoolMember(pool: IPPoolView) {
  const ip = newPoolIP.value.trim()
  if (!ip) return
  poolBusy.value = true
  try {
    const res = await domainsApi.addIPPoolMember(pool.id, ip)
    pool.members.push(res.data.data as any)
    newPoolIP.value = ''
    notify.success(`Member ${ip} added`)
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to add pool member'))
  } finally {
    poolBusy.value = false
  }
}

async function removePoolMember(pool: IPPoolView, memberId: number) {
  poolBusy.value = true
  try {
    await domainsApi.removeIPPoolMember(pool.id, memberId)
    pool.members = pool.members.filter((m) => m.id !== memberId)
    notify.success('Member removed')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to remove pool member'))
  } finally {
    poolBusy.value = false
  }
}

async function deletePool(pool: IPPoolView) {
  const confirmed = await confirm({
    title: 'Delete IP pool',
    message: `Delete pool "${pool.name}"? Refused while any domain still sends from it.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!confirmed) return
  poolBusy.value = true
  try {
    await domainsApi.deleteIPPool(pool.id)
    pools.value = pools.value.filter((p) => p.id !== pool.id)
    notify.success('IP pool deleted')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to delete IP pool'))
  } finally {
    poolBusy.value = false
  }
}

async function assignPool(domain: Domain, poolId: number | null) {
  poolBusy.value = true
  try {
    await domainsApi.assignPool(domain.id, poolId)
    if (dnsRecordsDomain.value && dnsRecordsDomain.value.id === domain.id) {
      dnsRecordsDomain.value.pool_id = poolId
    }
    notify.success(poolId ? 'IP pool assigned' : 'Pool assignment cleared')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to assign pool'))
  } finally {
    poolBusy.value = false
  }
}

async function setWarmup(domain: Domain) {
  warmupBusy.value = true
  try {
    await domainsApi.setWarmup(domain.id, warmupStartVolume.value, warmupMultiplier.value, warmupDays.value)
    notify.success('Warm-up schedule set — day 0 starts now')
    const res = await domainsApi.health(domain.id)
    healthByDomain.value[domain.id] = res.data.data
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to set warm-up schedule'))
  } finally {
    warmupBusy.value = false
  }
}

// ---- Reputation detail (OB-6 W3) ----
const reputationByDomain = ref<Record<number, DomainReputationReport | null>>({})
const reputationLoadingId = ref<number | null>(null)

const expandedReputation = computed(() =>
  expandedDomainId.value ? reputationByDomain.value[expandedDomainId.value] ?? null : null,
)

function gradeBadgeClass(grade: string): string {
  if (grade === 'good') return 'badge-success'
  if (grade === 'warning') return 'badge-warning'
  if (grade === 'poor') return 'badge-danger'
  return 'badge-neutral'
}

async function loadReputation(domainId: number) {
  if (reputationByDomain.value[domainId] !== undefined) return
  reputationLoadingId.value = domainId
  try {
    const res = await domainsApi.reputation(domainId)
    reputationByDomain.value[domainId] = res.data.data
  } catch {
    reputationByDomain.value[domainId] = null
  } finally {
    reputationLoadingId.value = null
  }
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString()
}

// The backend already expresses the rates as percent of sent.
function ratePct(rate: number): string {
  return `${rate.toFixed(2)}%`
}

// The DMARC alignment ratios are 0..1 fractions of messages.
function ratioPct(ratio: number): string {
  return `${(ratio * 100).toFixed(1)}%`
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
                              <button v-if="wsStore.canEdit && key.status === 'retiring'" class="btn btn-secondary btn-sm"
                                :disabled="signingBusy" @click="retireKey(domain, key)">
                                Retire
                              </button>
                              <button v-if="wsStore.canEdit && key.status === 'retired'" class="btn btn-danger btn-sm"
                                :disabled="signingBusy" @click="deleteKey(domain, key)">
                                Delete
                              </button>
                              <span v-if="key.status === 'retiring'" class="signing-note">
                                Keep until receivers stop seeing its signatures, then retire it and delete it.
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

                      <!-- ==================== DNS automation (OB-6, ADR-007) ==================== -->
                      <div v-if="!dnsFeatureOff" class="signing-section">
                        <div class="signing-head">
                          <div>
                            <h4 class="dns-panel-title">DNS automation</h4>
                            <p class="dns-panel-subtitle">
                              Connect Cloudflare, Route53 or DigitalOcean and publish the record set below through
                              the provider API. Publication always re-runs verification, and an hourly drift check
                              compares the live zone with the published plan.
                            </p>
                          </div>
                          <div v-if="expandedDNSAuto?.drift" class="dns-panel-actions">
                            <span class="badge" :class="{
                              'badge-success': expandedDNSAuto.drift.status === 'ok',
                              'badge-warning': expandedDNSAuto.drift.status === 'unknown',
                              'badge-error': expandedDNSAuto.drift.status === 'drift',
                            }">{{ driftLabels[expandedDNSAuto.drift.status] || expandedDNSAuto.drift.status }}</span>
                          </div>
                        </div>

                        <div v-if="dnsAutoLoadingId === domain.id" class="loading-page" style="min-height: 60px">
                          <div class="spinner"></div>
                        </div>

                        <template v-else>
                          <!-- Connected provider -->
                          <div v-if="expandedDNSAuto?.connected" class="signing-key">
                            <div class="signing-key-head">
                              <div class="signing-key-id">
                                <span class="dns-type-badge">{{ providerLabels[expandedDNSAuto.connected.provider] || expandedDNSAuto.connected.provider }}</span>
                                <code class="signing-selector">{{ expandedDNSAuto.connected.masked }}</code>
                              </div>
                              <div class="signing-key-badges">
                                <span v-if="expandedDNSAuto.publication?.published_at" class="badge badge-success">
                                  published {{ formatDate(expandedDNSAuto.publication.published_at) }}
                                </span>
                                <span v-else class="badge badge-neutral">not published yet</span>
                                <button v-if="wsStore.canEdit" class="btn btn-danger btn-xs"
                                  :disabled="dnsAutoBusy" @click="disconnectDNSProvider(domain)">Revoke</button>
                              </div>
                            </div>
                            <p v-if="expandedDNSAuto.publication?.publish_error" class="signing-note" role="alert">
                              Last publish failed: {{ expandedDNSAuto.publication.publish_error }}
                            </p>
                            <div v-if="expandedDNSAuto.drift?.findings?.length" class="signing-empty">
                              <p v-for="f in expandedDNSAuto.drift.findings" :key="f.record" class="dns-field-hint">
                                <strong>{{ f.kind === 'missing' ? 'Missing' : 'Changed' }}</strong>:
                                {{ f.record }} — expected
                                <code>{{ f.expected }}</code>
                                <template v-if="f.actual">, live <code>{{ f.actual }}</code></template>
                              </p>
                            </div>
                            <div v-if="wsStore.canEdit" class="signing-actions">
                              <button class="btn btn-primary btn-sm" :disabled="dnsAutoBusy"
                                @click="publishDNSRecords(domain)">
                                {{ dnsAutoBusy ? 'Working…' : 'Publish planned records' }}
                              </button>
                              <span v-if="expandedDNSAuto.drift?.last_checked_at" class="signing-note">
                                drift checked {{ formatDate(expandedDNSAuto.drift.last_checked_at) }}
                              </span>
                            </div>
                          </div>

                          <!-- Planned record set -->
                          <div v-if="expandedDNSAuto?.plan?.length" class="signing-empty">
                            <div class="dns-field" v-for="p in expandedDNSAuto.plan" :key="p.kind + p.record.name">
                              <div class="dns-field-label">{{ planKindLabels[p.kind] || p.kind }}</div>
                              <div class="dns-field-row">
                                <span class="dns-type-badge">{{ p.record.type }}</span>
                                <code class="dns-field-value">{{ p.record.name }}</code>
                              </div>
                              <div class="dns-field-row">
                                <code class="dns-field-value dns-field-value-block">{{ p.record.value }}</code>
                                <button type="button" class="btn btn-ghost btn-xs"
                                  @click="copy(p.record.value, planKindLabels[p.kind] || p.kind)">Copy</button>
                              </div>
                            </div>
                          </div>

                          <!-- Connect form -->
                          <div v-if="wsStore.canEdit" class="signing-form">
                            <div class="signing-form-title">
                              {{ expandedDNSAuto?.connected ? 'Replace provider connection' : 'Connect a DNS provider' }}
                            </div>
                            <div class="signing-form-row">
                              <select v-model="dnsProviderKind" class="form-input signing-input signing-select">
                                <option value="cloudflare">Cloudflare (API token)</option>
                                <option value="route53">AWS Route53 (access key)</option>
                                <option value="digitalocean">DigitalOcean (API token)</option>
                              </select>
                              <template v-if="dnsProviderKind === 'route53'">
                                <input v-model="dnsAccessKeyID" type="text" class="form-input signing-input"
                                  placeholder="access key id" autocomplete="off" />
                                <input v-model="dnsSecretAccessKey" type="password" class="form-input signing-input"
                                  placeholder="secret access key" autocomplete="new-password" />
                              </template>
                              <input v-else v-model="dnsToken" type="password" class="form-input signing-input"
                                placeholder="API token" autocomplete="new-password" />
                              <button class="btn btn-primary btn-sm" :disabled="dnsAutoBusy
                                || (dnsProviderKind === 'route53'
                                  ? !dnsAccessKeyID.trim() || !dnsSecretAccessKey
                                  : !dnsToken.trim())"
                                @click="connectDNSProvider(domain)">
                                {{ dnsAutoBusy ? 'Validating…' : 'Connect' }}
                              </button>
                            </div>
                            <p class="signing-hint">
                              The credential is validated with a read-only zone check, stored encrypted, and never
                              shown again. A token without write scope fails the publish step with a typed error —
                              nothing partial is written.
                            </p>
                          </div>
                        </template>
                      </div>

                      <!-- ==================== Deliverability health (R-07) ==================== -->
                      <div class="signing-section">
                        <div class="signing-head">
                          <div>
                            <h4 class="dns-panel-title">Deliverability health</h4>
                            <p class="dns-panel-subtitle">
                              Stored report from the last verification<span v-if="expandedHealth"> — last checked
                                {{ formatDate(expandedHealth.checked_at) }}</span>. Diagnose re-runs the live DNS
                              checks.
                            </p>
                          </div>
                          <div class="dns-panel-actions">
                            <button v-if="wsStore.canEdit" class="btn btn-primary btn-sm"
                              :disabled="diagnosingId === domain.id" @click="diagnoseDomain(domain)">
                              {{ diagnosingId === domain.id ? 'Diagnosing…' : 'Diagnose' }}
                            </button>
                          </div>
                        </div>

                        <div v-if="healthLoadingId === domain.id" class="loading-page" style="min-height: 60px">
                          <div class="spinner"></div>
                        </div>

                        <template v-else-if="expandedHealth">
                          <div v-for="check in healthCheckRows" :key="check.key" class="health-row">
                            <div class="health-row-head">
                              <span class="dns-label">{{ check.label }}</span>
                              <span class="badge" :class="check.result.verified ? 'badge-success' : 'badge-neutral'">
                                {{ check.result.verified ? 'Verified' : 'Pending' }}
                              </span>
                            </div>
                            <code v-if="check.result.record"
                              class="dns-field-value dns-field-value-block health-record">{{ check.result.record }}</code>
                            <p v-if="check.result.guidance" class="dns-field-hint">{{ check.result.guidance }}</p>
                          </div>

                          <div class="health-row">
                            <div class="health-row-head">
                              <span class="dns-label">DKIM signing</span>
                              <span class="health-row-badges">
                                <span class="badge"
                                  :class="expandedHealth.signing.enabled ? 'badge-success' : 'badge-neutral'">
                                  {{ expandedHealth.signing.enabled ? 'Signing' : 'Not signing' }}
                                </span>
                                <span v-if="expandedHealth.signing.dns_state" class="badge"
                                  :class="expandedHealth.signing.dns_state === 'published' ? 'badge-success' : 'badge-neutral'">
                                  {{ dnsStateLabels[expandedHealth.signing.dns_state] || expandedHealth.signing.dns_state }}
                                </span>
                              </span>
                            </div>
                            <div v-if="expandedHealth.signing.selector || expandedHealth.signing.algorithm"
                              class="health-signing-meta">
                              <code v-if="expandedHealth.signing.selector" class="signing-selector">{{ expandedHealth.signing.selector }}</code>
                              <span v-if="expandedHealth.signing.algorithm" class="dns-type-badge">{{ expandedHealth.signing.algorithm }}</span>
                            </div>
                            <p v-if="expandedHealth.signing.guidance" class="dns-field-hint">{{ expandedHealth.signing.guidance }}</p>
                          </div>

                          <div class="health-row">
                            <div class="health-row-head">
                              <span class="dns-label">Sending health</span>
                              <span class="health-since">since {{ formatDate(expandedHealth.sending.since) }}</span>
                            </div>
                            <div class="health-stats">
                              <div class="health-stat">
                                <span class="health-stat-value">{{ expandedHealth.sending.sent }}</span>
                                <span class="health-stat-label">sent</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ expandedHealth.sending.bounced }}</span>
                                <span class="health-stat-label">bounced ({{ ratePct(expandedHealth.sending.bounce_rate) }})</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ expandedHealth.sending.complained }}</span>
                                <span class="health-stat-label">complained ({{ ratePct(expandedHealth.sending.complaint_rate) }})</span>
                              </div>
                            </div>
                          </div>
                          <!-- IP reputation per source IP (OB-6 W3) -->
                          <div v-for="iprep in expandedHealth.reputation?.ips ?? []" :key="iprep.ip" class="health-row">
                            <div class="health-row-head">
                              <span class="dns-label">IP reputation &middot; <code class="signing-selector">{{ iprep.ip }}</code></span>
                              <span class="health-row-badges">
                                <span class="badge" :class="gradeBadgeClass(iprep.grade)">
                                  {{ iprep.grade === 'unknown' ? 'no data' : iprep.grade }}
                                </span>
                              </span>
                            </div>
                            <div class="health-stats">
                              <div class="health-stat">
                                <span class="health-stat-value">{{ iprep.attempts }}</span>
                                <span class="health-stat-label">attempts</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ iprep.delivered }}</span>
                                <span class="health-stat-label">delivered</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ ratePct(iprep.hard_fail_rate) }}</span>
                                <span class="health-stat-label">hard fail</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ ratePct(iprep.deferred_rate) }}</span>
                                <span class="health-stat-label">deferred</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ ratePct(iprep.bounce_rate) }}</span>
                                <span class="health-stat-label">bounce rate</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ ratePct(iprep.complaint_rate) }}</span>
                                <span class="health-stat-label">complaint rate</span>
                              </div>
                            </div>
                            <p class="dns-field-hint">{{ expandedHealth.reputation?.limitation_note }}</p>
                          </div>
                        </template>

                        <div v-else class="signing-empty">
                          No health report available yet.
                        </div>

                        <!-- DMARC aggregate reports (R-17) — non-fatal when nothing is ingested -->
                        <div class="health-dmarc">
                          <div class="health-row-head">
                            <span class="dns-label">DMARC aggregate reports</span>
                            <span v-if="expandedDmarc && expandedDmarc.summary.latest_window_end" class="health-since">
                              latest window ended {{ formatDate(expandedDmarc.summary.latest_window_end) }}
                            </span>
                          </div>

                          <div v-if="dmarcLoadingId === domain.id" class="loading-page" style="min-height: 40px">
                            <div class="spinner"></div>
                          </div>

                          <template v-else-if="expandedDmarc && expandedDmarc.summary.record_count > 0">
                            <div class="health-stats">
                              <div class="health-stat">
                                <span class="health-stat-value">{{ expandedDmarc.summary.record_count }}</span>
                                <span class="health-stat-label">report records</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ expandedDmarc.summary.message_count }}</span>
                                <span class="health-stat-label">messages</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ ratioPct(expandedDmarc.summary.dkim_alignment) }}</span>
                                <span class="health-stat-label">DKIM aligned</span>
                              </div>
                              <div class="health-stat">
                                <span class="health-stat-value">{{ ratioPct(expandedDmarc.summary.spf_alignment) }}</span>
                                <span class="health-stat-label">SPF aligned</span>
                              </div>
                            </div>

                            <div class="health-row-badges">
                              <span class="badge badge-neutral">none {{ expandedDmarc.summary.disposition_none }}</span>
                              <span class="badge badge-warning">quarantine {{ expandedDmarc.summary.disposition_quarantine }}</span>
                              <span class="badge badge-danger">reject {{ expandedDmarc.summary.disposition_reject }}</span>
                            </div>

                            <div v-for="rec in expandedDmarc.records" :key="rec.id" class="health-dmarc-record">
                              <div class="health-row-head">
                                <span class="health-dmarc-reporter">{{ rec.reporter || rec.reporter_email || rec.source_ip }}</span>
                                <span class="badge" :class="dispositionBadgeClass(rec.disposition)">
                                  {{ dispositionLabels[rec.disposition] || rec.disposition }}
                                </span>
                              </div>
                              <div class="health-dmarc-meta">
                                {{ rec.count }} messages · {{ formatDate(rec.window_start) }} →
                                {{ formatDate(rec.window_end) }}
                              </div>
                            </div>
                          </template>

                          <div v-else class="signing-empty">No DMARC reports ingested yet.</div>
                        </div>
                      </div>

                      <!-- ==================== IP pools + warm-up (OB-6 W3) ==================== -->
                      <div class="signing-section">
                        <div class="signing-head">
                          <div>
                            <h4 class="dns-panel-title">Sending IPs &amp; warm-up</h4>
                            <p class="dns-panel-subtitle">
                              Direct delivery rotates across the assigned pool's member IPs; a warm-up ramp caps the
                              daily volume while the domain builds reputation. Shared pools are managed by the admin.
                            </p>
                          </div>
                        </div>

                        <div v-if="poolsLoading" class="loading-page" style="min-height: 60px">
                          <div class="spinner"></div>
                        </div>

                        <template v-else>
                          <div class="health-row">
                            <div class="health-row-head">
                              <span class="dns-label">Assigned pool</span>
                              <span v-if="expandedPool" class="badge"
                                :class="expandedPool.scope === 'shared' ? 'badge-neutral' : 'badge-success'">
                                {{ expandedPool.scope === 'shared' ? 'shared' : 'workspace' }}
                              </span>
                            </div>
                            <div class="signing-form-row" v-if="wsStore.canEdit">
                              <select class="form-input signing-input signing-select"
                                :value="dnsRecordsDomain?.pool_id ?? ''" :disabled="poolBusy"
                                @change="assignPool(domain, ($event.target as HTMLSelectElement).value === '' ? null : Number(($event.target as HTMLSelectElement).value))">
                                <option value="">No pool (static bind IP)</option>
                                <option v-for="p in pools" :key="p.id" :value="p.id">
                                  {{ p.name }} ({{ p.members.filter((m) => m.enabled).length }} IPs)
                                </option>
                              </select>
                            </div>
                            <p v-else-if="expandedPool" class="dns-field-hint">{{ expandedPool.name }}</p>

                            <template v-if="expandedPool">
                              <div class="dns-field">
                                <div class="dns-field-label">Member IPs (enabled ones rotate per delivery)</div>
                                <div v-for="m in expandedPool.members" :key="m.id" class="dns-field-row">
                                  <code class="dns-field-value">{{ m.ip }}</code>
                                  <span v-if="!m.enabled" class="badge badge-neutral">disabled</span>
                                  <button v-if="wsStore.canEdit" type="button" class="btn btn-ghost btn-xs"
                                    :disabled="poolBusy" @click="removePoolMember(expandedPool, m.id)">Remove</button>
                                </div>
                                <div v-if="wsStore.canEdit" class="dns-field-row">
                                  <input v-model="newPoolIP" type="text" class="form-input signing-input"
                                    placeholder="add member IP, e.g. 192.0.2.10" />
                                  <button class="btn btn-secondary btn-sm" :disabled="poolBusy || !newPoolIP.trim()"
                                    @click="addPoolMember(expandedPool)">Add IP</button>
                                </div>
                              </div>
                              <div v-if="wsStore.canEdit && expandedPool.scope !== 'shared'" class="dns-field-row">
                                <button class="btn btn-danger btn-sm" :disabled="poolBusy"
                                  @click="deletePool(expandedPool)">Delete pool</button>
                              </div>
                            </template>
                            <p v-else class="dns-field-hint">
                              No pool assigned &mdash; deliveries use the domain's single outbound bind IP.
                            </p>
                          </div>

                          <div class="health-row">
                            <div class="health-row-head">
                              <span class="dns-label">Warm-up ramp</span>
                              <span v-if="expandedHealth && expandedHealth.warmup" class="badge"
                                :class="expandedHealth.warmup.daily_cap > 0 ? 'badge-warning' : 'badge-success'">
                                {{ expandedHealth.warmup.daily_cap > 0
                                  ? `day ${expandedHealth.warmup.day_index} &middot; cap ${expandedHealth.warmup.daily_cap}/day`
                                  : 'ramp complete' }}
                              </span>
                              <span v-else class="badge badge-neutral">none</span>
                            </div>
                            <p v-if="expandedHealth && expandedHealth.warmup" class="dns-field-hint">
                              start {{ expandedHealth.warmup.start_volume }}/day &middot; &times;{{ expandedHealth.warmup.daily_multiplier }}
                              &middot; {{ expandedHealth.warmup.days }} days &middot; enforced hourly cap
                              {{ expandedHealth.warmup.hourly_cap }}/h
                            </p>
                            <div v-if="wsStore.canEdit" class="signing-form-row">
                              <input v-model.number="warmupStartVolume" type="number" min="1" class="form-input signing-input"
                                title="Day-0 daily volume" />
                              <input v-model.number="warmupMultiplier" type="number" min="1" step="0.1"
                                class="form-input signing-input" title="Daily multiplier" />
                              <input v-model.number="warmupDays" type="number" min="1" class="form-input signing-input"
                                title="Ramp length (days)" />
                              <button class="btn btn-primary btn-sm" :disabled="warmupBusy"
                                @click="setWarmup(domain)">Start ramp</button>
                            </div>
                            <p v-if="wsStore.canEdit" class="dns-field-hint">
                              Day N allows round(start &times; multiplier^N) messages; starting a new ramp replaces the
                              previous one.
                            </p>
                          </div>

                          <div v-if="wsStore.canEdit" class="health-row">
                            <div class="health-row-head">
                              <span class="dns-label">New workspace pool</span>
                            </div>
                            <div class="signing-form-row">
                              <input v-model="newPoolName" type="text" class="form-input signing-input"
                                placeholder="pool name, e.g. eu-prod-2026" />
                              <button class="btn btn-secondary btn-sm" :disabled="poolBusy || !newPoolName.trim()"
                                @click="createPool">Create pool</button>
                            </div>
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

/* Deliverability health panel (R-07) — same card idiom as the signing keys */
.health-row {
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-md, 8px);
  padding: 0.75rem;
  margin-bottom: 0.75rem;
  background: var(--bg-secondary);
}

.health-row-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.health-row-badges {
  display: flex;
  gap: 0.5rem;
}

.health-record {
  margin-top: 0.5rem;
}

.health-signing-meta {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-top: 0.5rem;
}

.health-since {
  font-size: 12px;
  color: var(--text-muted);
}

.health-stats {
  display: flex;
  gap: 1.5rem;
  margin-top: 0.5rem;
  flex-wrap: wrap;
}

.health-stat {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
}

.health-stat-value {
  font-family: var(--font-mono);
  font-weight: 600;
  font-size: 14px;
}

.health-stat-label {
  font-size: 12px;
  color: var(--text-muted);
}

/* DMARC aggregate reports subsection (R-17) */
.health-dmarc {
  margin-top: 0.25rem;
}

.health-dmarc-record {
  border-top: 1px solid var(--border-primary);
  padding: 0.5rem 0;
}

.health-dmarc-reporter {
  font-size: 13px;
  font-weight: 600;
}

.health-dmarc-meta {
  margin-top: 0.25rem;
  font-size: 12px;
  color: var(--text-muted);
}
</style>
