<!--
  Copyright (c) 2026 Jonas Kaninda.

  This file is part of Posta.

  This program is free software: you can redistribute it and/or modify
  it under the terms of the GNU Affero General Public License as published by
  the Free Software Foundation, either version 3 of the License, or
  (at your option) any later version.

  This program is distributed in the hope that it will be useful,
  but WITHOUT ANY WARRANTY; without even the implied warranty of
  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
  GNU Affero General Public License for more details.

  You should have received a copy of the GNU Affero General Public License
  along with this program.  If not, see <https://www.gnu.org/licenses/>.

  SPDX-License-Identifier: AGPL-3.0-or-later
-->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { domainsApi } from '../../api/domains'
import { inboundRoutesApi, inboundForwardTargetsApi } from '../../api/inboundRoutes'
import type {
  Domain,
  InboundRoute,
  InboundRouteAction,
  InboundForwardTarget,
} from '../../api/types'
import { useNotificationStore } from '../../stores/notification'
import { apiMessage } from '../../composables/apiError'
import { useConfirm } from '../../composables/useConfirm'
import { useModalSafeClose } from '../../composables/useModalSafeClose'
import { useWorkspaceStore } from '../../stores/workspace'
import { usePagination } from '../../composables/usePagination'
import Pagination from '../../components/Pagination.vue'

// Inbound routing rules (OB-2, ADR-002 decision 1): per-verified-domain
// recipient patterns with an action the engine runs only AFTER the message is
// durably persisted. Rules resolve only through tenant-verified domains, so
// the drawer offers verified domains and unverified rows are marked inert.
// Forward rules run only to a confirmed forward target (ADR-002 decision 2):
// the drawer registers the target and surfaces its one-time confirmation URL.
const notify = useNotificationStore()
const wsStore = useWorkspaceStore()
const { confirm } = useConfirm()

// ---- Domains (the per-domain grouping of the rules table) -------------------

const domains = ref<Domain[]>([])
const loading = ref(true)

const { pageable, goToPage } = usePagination(async (page) => {
  loading.value = true
  try {
    const res = await domainsApi.list(page)
    domains.value = res.data.data
    pageable.value = res.data.pageable
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to load domains'))
  } finally {
    loading.value = false
  }
})

// ---- Rules, loaded lazily per expanded domain row ---------------------------

const expandedDomainId = ref<number | null>(null)
const rulesLoadingId = ref<number | null>(null)
const routesByDomain = ref<Record<number, InboundRoute[]>>({})
// Load failures surface inline in the expanded row (with a retry) instead of a
// global toast: the failure belongs to the row's lazy load, and a transient
// toast used to obscure the sticky topbar before it expired.
const rulesErrorId = ref<number | null>(null)
const rulesErrorMessage = ref('')

function rulesFor(domainId: number): InboundRoute[] {
  return routesByDomain.value[domainId] ?? []
}

async function toggleRules(domain: Domain) {
  if (expandedDomainId.value === domain.id) {
    expandedDomainId.value = null
    return
  }
  expandedDomainId.value = domain.id
  await loadRules(domain.id)
}

async function loadRules(domainId: number) {
  rulesLoadingId.value = domainId
  rulesErrorId.value = null
  try {
    const res = await inboundRoutesApi.list({ domain_id: domainId })
    routesByDomain.value = { ...routesByDomain.value, [domainId]: res.data.data }
  } catch (e: any) {
    rulesErrorId.value = domainId
    rulesErrorMessage.value = apiMessage(e, 'Failed to load routing rules')
  } finally {
    rulesLoadingId.value = null
  }
}

const actionLabels: Record<InboundRouteAction, string> = {
  deliver: 'Deliver',
  forward: 'Forward',
  webhook: 'Webhook',
  quarantine: 'Quarantine',
}

function actionBadgeClass(action: InboundRouteAction): string {
  switch (action) {
    case 'forward':
      return 'badge badge-info'
    case 'webhook':
      return 'badge badge-secondary'
    case 'quarantine':
      return 'badge badge-warning'
    default:
      return 'badge badge-neutral'
  }
}

function isCatchAll(pattern: string): boolean {
  return pattern.startsWith('@')
}

// ---- Create / edit drawer ----------------------------------------------------

const drawerOpen = ref(false)
const editing = ref<InboundRoute | null>(null)
const saving = ref(false)
const drawerError = ref('')

const formDomainId = ref<number | null>(null)
const formPattern = ref('')
const formAction = ref<InboundRouteAction>('deliver')
const formTarget = ref('')
const formPriority = ref(100)
const formEnabled = ref(true)

// Routing resolves only through tenant-verified domains (ADR-002
// non-negotiable), so the drawer's domain choices are the verified ones.
const verifiedDomains = computed(() => domains.value.filter((d) => d.ownership_verified))
const formDomain = computed(
  () => verifiedDomains.value.find((d) => d.id === formDomainId.value) ?? null,
)
const drawerNeedsDomain = computed(() => !editing.value && verifiedDomains.value.length === 0)

// Webhook targets name an event of the workspace's registered webhooks; the
// established inbound event is the empty default. Forward targets must be a
// confirmed destination address.
const needsTarget = computed(() => formAction.value === 'forward' || formAction.value === 'webhook')

function openCreate(domain?: Domain) {
  if (verifiedDomains.value.length === 0) {
    notify.error('Add and verify a domain first — routing rules resolve only through verified domains')
    return
  }
  editing.value = null
  drawerError.value = ''
  freshConfirm.value = null
  formDomainId.value = domain && domain.ownership_verified ? domain.id : verifiedDomains.value[0].id
  formPattern.value = ''
  formAction.value = 'deliver'
  formTarget.value = ''
  formPriority.value = 100
  formEnabled.value = true
  drawerOpen.value = true
}

function openEdit(route: InboundRoute) {
  editing.value = route
  drawerError.value = ''
  freshConfirm.value = null
  formDomainId.value = route.domain_id
  formPattern.value = route.pattern
  formAction.value = route.action
  formTarget.value = route.target ?? ''
  formPriority.value = route.priority
  formEnabled.value = route.enabled
  drawerOpen.value = true
}

const { watchClickStart, confirmClickEnd } = useModalSafeClose(() => {
  if (!saving.value) drawerOpen.value = false
})

function validate(): string {
  if (!formDomainId.value) return 'Choose a verified domain'
  const pattern = formPattern.value.trim()
  if (!pattern) return 'Pattern is required'
  if (!isCatchAll(pattern) && !pattern.includes('@')) {
    return 'Use an exact address (user@example.com), a sub-address spec (user+tag@example.com), or @example.com for the catch-all'
  }
  if (needsTarget.value && !formTarget.value.trim()) {
    return formAction.value === 'forward'
      ? 'Forward needs a destination address'
      : 'Webhook needs an event name'
  }
  return ''
}

async function saveRule() {
  if (saving.value) return
  drawerError.value = ''
  const invalid = validate()
  if (invalid) {
    drawerError.value = invalid
    return
  }
  saving.value = true
  const payload = {
    domain_id: formDomainId.value!,
    pattern: formPattern.value.trim(),
    action: formAction.value,
    target: needsTarget.value ? formTarget.value.trim() : '',
    priority: Number(formPriority.value) || 100,
    enabled: formEnabled.value,
  }
  try {
    if (editing.value) {
      await inboundRoutesApi.update(editing.value.id, payload)
      notify.success('Routing rule updated')
    } else {
      await inboundRoutesApi.create(payload)
      notify.success('Routing rule created')
    }
    drawerOpen.value = false
    if (payload.domain_id) await loadRules(payload.domain_id)
  } catch (e: any) {
    drawerError.value = apiMessage(
      e,
      editing.value ? 'Failed to update routing rule' : 'Failed to create routing rule',
    )
  } finally {
    saving.value = false
  }
}

async function toggleRoute(route: InboundRoute) {
  const domainId = route.domain_id
  try {
    await inboundRoutesApi.update(route.id, {
      domain_id: route.domain_id,
      pattern: route.pattern,
      action: route.action,
      target: route.target ?? '',
      priority: route.priority,
      enabled: !route.enabled,
    })
    notify.success(route.enabled ? 'Rule disabled' : 'Rule enabled')
    await loadRules(domainId)
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to update routing rule'))
  }
}

async function deleteRule(route: InboundRoute) {
  const confirmed = await confirm({
    title: 'Delete routing rule',
    message: `Delete the rule "${route.pattern}"? Recipients it matched will fall through to the catch-all or be rejected.`,
    confirmText: 'Delete',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await inboundRoutesApi.remove(route.id)
    notify.success('Routing rule deleted')
    await loadRules(route.domain_id)
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to delete routing rule'))
  }
}

// ---- Forward targets + the confirmation flow (ADR-002 decision 2) -----------

const targets = ref<InboundForwardTarget[]>([])
const targetsLoading = ref(false)
const registering = ref(false)
// The one-time confirmation URL of the target just registered — shown once,
// because only the token's hash is stored server-side.
const freshConfirm = ref<{ target: string; confirm_url: string } | null>(null)

async function loadTargets() {
  targetsLoading.value = true
  try {
    const res = await inboundForwardTargetsApi.list()
    targets.value = res.data.data
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to load forward targets'))
  } finally {
    targetsLoading.value = false
  }
}

const normalizedTarget = computed(() => formTarget.value.trim().toLowerCase())

// The typed forward destination's confirmation state, or null when it is not
// registered yet — the drawer then offers to register it.
const targetState = computed<InboundForwardTarget | null>(() =>
  formAction.value === 'forward'
    ? targets.value.find((t) => t.target === normalizedTarget.value) ?? null
    : null,
)

async function registerTarget() {
  if (registering.value || !normalizedTarget.value) return
  registering.value = true
  try {
    const res = await inboundForwardTargetsApi.create(normalizedTarget.value)
    freshConfirm.value = {
      target: res.data.data.target.target,
      confirm_url: res.data.data.confirm_url,
    }
    targets.value = [...targets.value, res.data.data.target]
    notify.success('Forward target registered — send the confirmation link to the mailbox')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to register forward target'))
  } finally {
    registering.value = false
  }
}

async function revokeTarget(target: InboundForwardTarget) {
  const confirmed = await confirm({
    title: 'Revoke forward target',
    message: `Revoke "${target.target}"? Forwarding rules pointing at it stop running until the address is confirmed again.`,
    confirmText: 'Revoke',
    variant: 'danger',
  })
  if (!confirmed) return
  try {
    await inboundForwardTargetsApi.remove(target.id)
    targets.value = targets.value.filter((t) => t.id !== target.id)
    notify.success('Forward target revoked')
  } catch (e: any) {
    notify.error(apiMessage(e, 'Failed to revoke forward target'))
  }
}

async function copy(text: string, label = 'Link') {
  try {
    await navigator.clipboard.writeText(text)
    notify.success(`${label} copied`)
  } catch {
    notify.error('Copy failed')
  }
}

onMounted(loadTargets)
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Inbound Routes</h1>
      <button v-if="wsStore.canEdit" class="btn btn-primary" @click="openCreate()">Add Rule</button>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <template v-else>
      <!-- Per-domain rules: one expandable row per domain (Domains.vue idiom) -->
      <div class="card">
        <div class="table-wrapper" v-if="domains.length > 0">
          <table>
            <thead>
              <tr>
                <th>Domain</th>
                <th>Ownership</th>
                <th>Rules</th>
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
                  <td>{{ expandedDomainId === domain.id ? rulesFor(domain.id).length : '—' }}</td>
                  <td>
                    <div class="flex gap-2">
                      <button class="btn btn-secondary btn-sm" @click="toggleRules(domain)">
                        {{ expandedDomainId === domain.id ? 'Hide Rules' : 'View Rules' }}
                      </button>
                      <button
                        v-if="wsStore.canEdit && domain.ownership_verified"
                        class="btn btn-primary btn-sm"
                        @click="openCreate(domain)"
                      >
                        Add Rule
                      </button>
                    </div>
                  </td>
                </tr>
                <tr v-if="expandedDomainId === domain.id">
                  <td colspan="4" style="padding: 20px 16px; background: var(--bg-tertiary)">
                    <div v-if="rulesLoadingId === domain.id" class="loading-page" style="min-height: 80px">
                      <div class="spinner"></div>
                    </div>
                    <div v-else-if="rulesErrorId === domain.id" class="empty-state">
                      <h3>Failed to load routing rules</h3>
                      <p>{{ rulesErrorMessage }}</p>
                      <button class="btn btn-secondary btn-sm" @click="loadRules(domain.id)">Retry</button>
                    </div>
                    <template v-else>
                      <h4 class="dns-panel-title">Routing rules for {{ domain.domain }}</h4>
                      <p class="dns-panel-subtitle">
                        First match wins by priority (lower first); exact and sub-address patterns
                        beat the catch-all. Recipients matching no enabled rule are rejected.
                      </p>

                      <div v-if="rulesFor(domain.id).length === 0" class="empty-state">
                        <h3>No routing rules</h3>
                        <p>
                          Add a rule to route this domain's mail — unmatched recipients are
                          rejected.
                        </p>
                      </div>
                      <div v-else class="table-wrapper">
                        <table>
                          <thead>
                            <tr>
                              <th>Pattern</th>
                              <th>Action</th>
                              <th>Target</th>
                              <th>Priority</th>
                              <th>Status</th>
                              <th v-if="wsStore.canEdit">Actions</th>
                            </tr>
                          </thead>
                          <tbody>
                            <tr v-for="route in rulesFor(domain.id)" :key="route.id">
                              <td>
                                <code class="mono">{{ route.pattern }}</code>
                                <span v-if="isCatchAll(route.pattern)" class="badge badge-neutral" style="margin-left: 6px">
                                  catch-all
                                </span>
                              </td>
                              <td>
                                <span :class="actionBadgeClass(route.action)">
                                  {{ actionLabels[route.action] || route.action }}
                                </span>
                              </td>
                              <td>
                                <code v-if="route.target" class="mono">{{ route.target }}</code>
                                <span v-else class="text-muted">—</span>
                              </td>
                              <td>{{ route.priority }}</td>
                              <td>
                                <button
                                  v-if="wsStore.canEdit"
                                  class="badge"
                                  :class="route.enabled ? 'badge-success' : 'badge-neutral'"
                                  style="border: none; cursor: pointer"
                                  :title="route.enabled ? 'Click to disable' : 'Click to enable'"
                                  @click="toggleRoute(route)"
                                >
                                  {{ route.enabled ? 'Enabled' : 'Disabled' }}
                                </button>
                                <span v-else :class="route.enabled ? 'badge badge-success' : 'badge badge-neutral'">
                                  {{ route.enabled ? 'Enabled' : 'Disabled' }}
                                </span>
                              </td>
                              <td v-if="wsStore.canEdit">
                                <div class="flex gap-2">
                                  <button class="btn btn-secondary btn-sm" @click="openEdit(route)">Edit</button>
                                  <button class="btn btn-danger btn-sm" @click="deleteRule(route)">Delete</button>
                                </div>
                              </td>
                            </tr>
                          </tbody>
                        </table>
                      </div>

                      <p v-if="!domain.ownership_verified" class="form-error">
                        This domain is not verified — its rules never evaluate until ownership is
                        verified in Domains.
                      </p>
                    </template>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>

        <div v-else class="empty-state">
          <h3>No domains</h3>
          <p>Add and verify a domain in Domains first — routing rules resolve only through verified domains.</p>
        </div>
        <Pagination :pageable="pageable" @page="goToPage" />
      </div>

      <!-- Confirmed forward destinations (ADR-002 decision 2) -->
      <div class="card" style="margin-top: 16px">
        <div class="card-body">
          <h3 style="margin: 0 0 4px">Forward targets</h3>
          <p class="form-hint">
            Forward rules re-send mail only to a confirmed target: the destination mailbox receives
            a one-time confirmation link (sent by you), and forwarding activates once it is used.
          </p>

          <div v-if="targetsLoading" class="loading-page" style="min-height: 60px">
            <div class="spinner"></div>
          </div>
          <template v-else>
            <div v-if="targets.length === 0" class="empty-state">
              <h3>No forward targets</h3>
              <p>Register a destination while creating a forward rule — it gets a confirmation link.</p>
            </div>
            <div v-else class="table-wrapper">
              <table>
                <thead>
                  <tr>
                    <th>Target</th>
                    <th>Status</th>
                    <th>Registered</th>
                    <th v-if="wsStore.canEdit">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="t in targets" :key="t.id">
                    <td class="mono">{{ t.target }}</td>
                    <td>
                      <span v-if="t.confirmed_at" class="badge badge-success">Confirmed</span>
                      <span v-else class="badge badge-warning">Pending confirmation</span>
                    </td>
                    <td>{{ new Date(t.created_at).toLocaleString() }}</td>
                    <td v-if="wsStore.canEdit">
                      <button class="btn btn-danger btn-sm" @click="revokeTarget(t)">Revoke</button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </template>
        </div>
      </div>
    </template>

    <!-- Create / edit rule drawer (Segments.vue drawer idiom) -->
    <Teleport to="body">
      <div v-if="drawerOpen" class="ir-overlay" @mousedown="watchClickStart" @mouseup="confirmClickEnd">
        <div class="ir-drawer" role="dialog" aria-modal="true" aria-label="Routing rule editor">
          <header class="ir-drawer__header">
            <div class="ir-drawer__heading">
              <h2>{{ editing ? 'Edit Routing Rule' : 'New Routing Rule' }}</h2>
              <span v-if="formDomain" class="ir-drawer__sub">{{ formDomain.domain }}</span>
            </div>
            <button class="ir-drawer__close" aria-label="Close" @click="drawerOpen = false">
              &times;
            </button>
          </header>

          <div class="ir-drawer__body">
            <div v-if="drawerError" class="ir-error-banner" role="alert">
              <div class="ir-error-title">{{ drawerError }}</div>
            </div>

            <div v-if="drawerNeedsDomain" class="empty-state">
              <h3>No verified domain</h3>
              <p>Verify a domain's ownership first — routing rules resolve only through verified domains.</p>
            </div>

            <template v-else>
              <div class="form-group">
                <label class="form-label">Domain</label>
                <select v-model="formDomainId" class="form-select" :disabled="!!editing">
                  <option v-for="d in verifiedDomains" :key="d.id" :value="d.id">{{ d.domain }}</option>
                </select>
                <p v-if="!editing" class="form-hint">Only verified domains are offered.</p>
              </div>

              <div class="form-group">
                <label class="form-label">Recipient pattern</label>
                <input
                  v-model="formPattern"
                  type="text"
                  class="form-input"
                  placeholder="user@example.com, user+tag@example.com, or @example.com"
                />
                <p class="form-hint">
                  Exact address, sub-address spec, or <code>@domain</code> for the domain's
                  catch-all fallback.
                </p>
              </div>

              <div class="form-group">
                <label class="form-label">Action</label>
                <select v-model="formAction" class="form-select">
                  <option value="deliver">Deliver — store for the inbound archive</option>
                  <option value="forward">Forward — re-send to a confirmed target</option>
                  <option value="webhook">Webhook — POST to a registered endpoint</option>
                  <option value="quarantine">Quarantine — hold for review</option>
                </select>
              </div>

              <div v-if="needsTarget" class="form-group">
                <label class="form-label">{{ formAction === 'forward' ? 'Forward to' : 'Webhook event' }}</label>
                <input
                  v-model="formTarget"
                  type="text"
                  class="form-input"
                  :placeholder="formAction === 'forward' ? 'destination@example.com' : 'email.inbound'"
                />

                <!-- Forward-target confirmation flow (ADR-002 decision 2) -->
                <template v-if="formAction === 'forward'">
                  <p v-if="targetState?.confirmed_at" class="form-hint" style="color: var(--success-text, green)">
                    &#10003; {{ targetState.target }} is a confirmed target.
                  </p>
                  <p v-else-if="targetState" class="form-hint">
                    Registered but not confirmed yet — forwarding stays off until the confirmation
                    link is used.
                  </p>
                  <div v-else-if="normalizedTarget" style="margin-top: 8px">
                    <button class="btn btn-secondary btn-sm" :disabled="registering" @click="registerTarget">
                      {{ registering ? 'Registering…' : 'Register target & get confirmation link' }}
                    </button>
                  </div>

                  <div v-if="freshConfirm" class="ir-confirm-banner">
                    <div class="ir-confirm-title">
                      Confirmation link for {{ freshConfirm.target }} — shown once, copy it now:
                    </div>
                    <div class="ir-confirm-row">
                      <code class="ir-confirm-url">{{ freshConfirm.confirm_url }}</code>
                      <button type="button" class="btn btn-secondary btn-sm" @click="copy(freshConfirm.confirm_url, 'Confirmation link')">
                        Copy
                      </button>
                    </div>
                    <p class="form-hint">
                      Send this link to the mailbox owner; forwarding activates after they open it.
                    </p>
                  </div>
                </template>
              </div>

              <div class="form-group">
                <label class="form-label">Priority</label>
                <input v-model.number="formPriority" type="number" class="form-input" min="0" />
                <p class="form-hint">Lower values evaluate first; the catch-all is always the fallback.</p>
              </div>

              <div class="form-group">
                <label class="form-label" style="display: flex; align-items: center; gap: 8px">
                  <input v-model="formEnabled" type="checkbox" />
                  Enabled
                </label>
              </div>
            </template>
          </div>

          <footer class="ir-drawer__footer">
            <button class="btn btn-secondary" :disabled="saving" @click="drawerOpen = false">Cancel</button>
            <button
              v-if="!drawerNeedsDomain"
              class="btn btn-primary"
              :disabled="saving"
              @click="saveRule"
            >
              {{ saving ? 'Saving…' : editing ? 'Save Changes' : 'Create Rule' }}
            </button>
          </footer>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
/* Domain names, patterns and addresses are technical identifiers. */
.mono {
  font-family: var(--font-mono);
  font-size: 13px;
}

/* Right-hand drawer over the rules table — same shape as SegmentDrawer. */
.ir-overlay {
  position: fixed;
  inset: 0;
  z-index: 60;
  background: rgba(0, 0, 0, 0.45);
}

.ir-drawer {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(520px, 100vw);
  display: flex;
  flex-direction: column;
  background: var(--bg-primary);
  border-left: 1px solid var(--border-primary);
  box-shadow: var(--shadow-lg);
}

.ir-drawer__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border-primary);
}

.ir-drawer__heading {
  min-width: 0;
}

.ir-drawer__heading h2 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}

.ir-drawer__sub {
  display: block;
  font-size: 12.5px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ir-drawer__close {
  display: inline-flex;
  padding: 4px 10px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
  font-size: 20px;
  line-height: 1;
}

.ir-drawer__close:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.ir-drawer__body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ir-drawer__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--border-primary);
}

/* Drawer validation / API error banner (per-view scoped, like SegmentDrawer). */
.ir-error-banner {
  padding: 10px 12px;
  border: 1px solid var(--danger-500, var(--border-primary));
  border-radius: var(--radius-md, 8px);
  background: var(--bg-secondary);
}

.ir-error-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--danger-text, var(--text-primary));
}

/* One-time confirmation URL banner inside the drawer. */
.ir-confirm-banner {
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px solid var(--warning-50, var(--border-primary));
  border-radius: var(--radius-md, 8px);
  background: var(--bg-secondary);
}

.ir-confirm-title {
  font-size: 12.5px;
  font-weight: 600;
  margin-bottom: 6px;
}

.ir-confirm-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.ir-confirm-url {
  font-family: var(--font-mono);
  font-size: 12px;
  word-break: break-all;
  flex: 1;
  min-width: 200px;
}
</style>
