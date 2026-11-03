<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { VueFlow, Handle, Position, type NodeMouseEvent } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import '@vue-flow/minimap/dist/style.css'

import { automationsApi, automationApiError } from '../../api/automations'
import { subscriberListsApi } from '../../api/subscriberLists'
import { templatesApi } from '../../api/templates'
import { useNotificationStore } from '../../stores/notification'
import type { AutomationABVariant, SubscriberListItem, TemplateListItem } from '../../api/types'
import SearchableSelect from '../../components/SearchableSelect.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import { useConfirm } from '../../composables/useConfirm'
import { useUnsavedChanges } from '../../composables/useUnsavedChanges'
import {
  useAutomationBuilder,
  nodeMeta,
  type BuilderNodeType,
  type SelectOption,
} from '../../composables/useAutomationBuilder'
import NodePalette from './components/NodePalette.vue'
import ConfigPanel from './components/ConfigPanel.vue'

const route = useRoute()
const router = useRouter()
const notify = useNotificationStore()
const { confirm } = useConfirm()

const builder = useAutomationBuilder()
const {
  flowId,
  name,
  listId,
  selectedNodeId,
  selectedNode,
  hasUnsavedChanges,
  markSaved,
  undo,
  redo,
  canUndo,
  canRedo,
  toAutomationPayload,
  validate,
  onConnectRefused,
} = builder

const automationId = computed(() => {
  const raw = route.params.id
  const id = Number(Array.isArray(raw) ? raw[0] : raw)
  return Number.isFinite(id) && id > 0 ? id : null
})

const loading = ref(true)
const saving = ref(false)
const errorBanner = ref<{ message: string; validationErrors: string[] } | null>(null)

// --- Reference data --------------------------------------------------------

const lists = ref<SubscriberListItem[]>([])
const templates = ref<TemplateListItem[]>([])
const templatesLoading = ref(true)

const listOptions = computed<SelectOption[]>(() => [
  { value: '', label: 'No list' },
  ...lists.value.map((l) => ({ value: String(l.id), label: l.name })),
])

const templateOptions = computed<SelectOption[]>(() => templates.value.map((t) => ({ value: String(t.id), label: t.name })))

function listName(id: number | null | undefined): string {
  if (!id) return ''
  return lists.value.find((l) => l.id === id)?.name ?? `List #${id}`
}

function templateName(id: number | null | undefined): string {
  if (!id) return ''
  return templates.value.find((t) => t.id === id)?.name ?? `Template #${id}`
}

const listValue = computed({
  get: () => (listId.value ? String(listId.value) : ''),
  set: (v: string) => {
    listId.value = v ? Number(v) : null
  },
})

// --- Canvas node rendering -------------------------------------------------

function branchHandles(data: any): { id: string; label: string }[] {
  const type = data?.nodeType as BuilderNodeType
  const cfg = (data?.config ?? {}) as Record<string, any>
  if (type === 'filter') return [{ id: 'continue', label: 'Yes' }, { id: 'exit', label: 'No' }]
  if (type === 'list_status') {
    return [
      { id: 'active', label: 'Active' },
      { id: 'non_active', label: 'Not active' },
      { id: 'not_in_list', label: 'Not in list' },
    ]
  }
  if (type === 'ab_test') {
    return ((cfg.variants ?? []) as AutomationABVariant[])
      .filter((v) => v?.id)
      .map((v) => ({ id: v.id, label: `${v.name} (${v.weight}%)` }))
  }
  return []
}

function branchTop(index: number, count: number): string {
  if (count <= 1) return '50%'
  return `${25 + (index * 50) / (count - 1)}%`
}

function nodeLabel(data: any): string {
  return nodeMeta((data?.nodeType as BuilderNodeType) ?? 'delay').label
}

function nodeSub(data: any): string {
  const type = data?.nodeType as BuilderNodeType
  const cfg = (data?.config ?? {}) as Record<string, any>
  switch (type) {
    case 'trigger': {
      const kind = String(cfg.event_kind ?? '')
      const names: Record<string, string> = {
        'list.subscribed': `Joins ${listName(cfg.list_id) || listName(listId.value) || 'a list'}`,
        'contact.created': 'Contact created',
        'contact.unsubscribed': 'Contact unsubscribed',
        'email.opened': 'Email opened',
        'email.clicked': 'Email clicked',
        'email.bounced': 'Email bounced',
        custom: `Event: ${cfg.custom_event_name || '…'}`,
      }
      return names[kind] ?? 'No event selected'
    }
    case 'delay': {
      const duration = Number(cfg.duration)
      return duration > 0 ? `Wait ${duration} ${cfg.unit ?? ''}`.trim() : 'Set a duration'
    }
    case 'email':
      return templateName(cfg.template_id) || 'No template selected'
    case 'filter': {
      const count = Array.isArray(cfg.conditions) ? cfg.conditions.length : 0
      return count === 1 ? '1 condition' : `${count} conditions`
    }
    case 'ab_test': {
      const count = Array.isArray(cfg.variants) ? cfg.variants.length : 0
      return count === 1 ? '1 variant' : `${count} variants`
    }
    case 'list_status':
      return listName(cfg.list_id) || 'No list selected'
    case 'add_to_list':
    case 'remove_from_list':
      return listName(cfg.list_id) || 'No list selected'
    case 'webhook':
      return String(cfg.url || 'No URL set')
    default:
      return ''
  }
}

const hasTrigger = computed(() => builder.nodes.value.some((n: any) => n.data?.nodeType === 'trigger'))

function onPaletteAdd(type: BuilderNodeType) {
  if (type === 'trigger' && hasTrigger.value) {
    notify.error('The workflow already has a trigger node')
    return
  }
  builder.addNode(type)
}

const DRAG_MIME = 'application/x-ref-node'

function onDrop(event: DragEvent) {
  const type = event.dataTransfer?.getData(DRAG_MIME) as BuilderNodeType | ''
  if (!type) return
  if (type === 'trigger' && hasTrigger.value) {
    notify.error('The workflow already has a trigger node')
    return
  }
  if (type === 'email' && !listId.value) {
    notify.error('Choose an automation list before adding email nodes')
    return
  }
  builder.addNode(type, builder.screenToFlowCoordinate({ x: event.clientX, y: event.clientY }))
}

function onDeleteNode(id: string) {
  builder.deleteNode(id)
}

function onNodeClick(event: NodeMouseEvent) {
  selectedNodeId.value = event.node.id
}

function onPaneClick() {
  selectedNodeId.value = null
}

// Node props → ConfigPanel target options: everything except itself and the
// trigger (nothing may target the trigger, and a node never targets itself).
const eligibleTargets = computed<SelectOption[]>(() => {
  const selected = selectedNodeId.value
  return (builder.nodes.value as any[])
    .filter((n) => n.id !== selected && n.data?.nodeType !== 'trigger')
    .map((n) => ({ value: n.id, label: `${nodeMeta(n.data?.nodeType).label} · ${n.id}` }))
})

// --- Wiring ----------------------------------------------------------------

watch(onConnectRefused, (message) => {
  if (!message) return
  notify.error(message)
  onConnectRefused.value = ''
})

// Desktop-first: below 1024px the three-area layout degrades gracefully.
const isNarrow = ref(typeof window !== 'undefined' ? window.innerWidth < 1024 : false)
function onResize() {
  isNarrow.value = window.innerWidth < 1024
}
onMounted(() => window.addEventListener('resize', onResize))
onBeforeUnmount(() => window.removeEventListener('resize', onResize))

// --- Lifecycle ---------------------------------------------------------------

onMounted(async () => {
  const [listsRes, templatesRes] = await Promise.allSettled([subscriberListsApi.list(0, 200), templatesApi.list(0, 200)])
  if (listsRes.status === 'fulfilled') lists.value = listsRes.value.data.data
  if (templatesRes.status === 'fulfilled') templates.value = templatesRes.value.data.data
  templatesLoading.value = false

  try {
    if (automationId.value) {
      const res = await automationsApi.get(automationId.value)
      await builder.hydrate(res.data.data)
    } else {
      await builder.hydrate()
    }
  } catch (e) {
    notify.error(automationApiError(e, 'Failed to load automation').message)
    router.push('/automations')
    return
  }
  loading.value = false

  // Fit the (now populated) graph once the nodes have measured dimensions.
  const stop = builder.store.onNodesInitialized(() => {
    stop.off()
    void nextTick(() => builder.fitView())
  })
})

// --- Save / cancel / guards --------------------------------------------------

function fail(e: any, fallback: string) {
  errorBanner.value = automationApiError(e, fallback)
}

async function save() {
  if (saving.value) return
  errorBanner.value = null

  const payload = toAutomationPayload()
  if (!payload.name) {
    errorBanner.value = { message: 'name is required', validationErrors: [] }
    return
  }
  const problems = validate(payload)
  if (problems.length) {
    errorBanner.value = { message: 'Automation has problems that must be fixed before saving', validationErrors: problems }
    return
  }

  saving.value = true
  try {
    const res = automationId.value
      ? await automationsApi.update(automationId.value, payload)
      : await automationsApi.create(payload)
    markSaved()
    notify.success(automationId.value ? 'Automation saved' : 'Automation created')
    // Navigate away from the editor: the detail page is the source of truth.
    router.push(`/automations/${res.data.data.id}`)
  } catch (e) {
    // Server validation 400s surface verbatim.
    fail(e, 'Failed to save automation')
  } finally {
    saving.value = false
  }
}

async function cancel() {
  if (hasUnsavedChanges.value) {
    const ok = await confirm({
      title: 'Unsaved changes',
      message: 'You have unsaved changes. Leave without saving?',
      confirmText: 'Leave',
      variant: 'warning',
    })
    if (!ok) return
  }
  router.push('/automations')
}

useUnsavedChanges(
  hasUnsavedChanges,
  async () => {
    return await confirm({
      title: 'Unsaved changes',
      message: 'You have unsaved changes. Leave without saving?',
      confirmText: 'Leave',
      variant: 'warning',
    })
  },
)

// Route edit → new (or vice versa) reuses this component: rehydrate.
watch(automationId, async (id, prev) => {
  if (id === prev || !id) return
  loading.value = true
  try {
    const res = await automationsApi.get(id)
    await builder.hydrate(res.data.data)
  } catch {
    router.push('/automations')
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="builder">
    <header class="builder__toolbar">
      <button class="builder__back" aria-label="Back to automations" @click="cancel">
        <span class="mdi mdi-arrow-left"></span>
      </button>

      <input
        v-model="name"
        class="builder__name"
        placeholder="Automation name"
        maxlength="255"
        spellcheck="false"
      />

      <div class="builder__list">
        <span class="builder__list-label">List</span>
        <SearchableSelect
          class="builder__list-select"
          :options="listOptions"
          :model-value="listValue"
          placeholder="No list"
          search-placeholder="Search lists…"
          empty-text="No lists yet"
          @update:model-value="listValue = $event"
        />
      </div>

      <span v-if="hasUnsavedChanges" class="builder__dirty">
        <i class="builder__dirty-dot"></i>
        Unsaved changes
      </span>

      <div class="builder__spacer"></div>

      <div class="builder__history">
        <button class="builder__tool" :disabled="!canUndo" title="Undo" @click="undo">
          <span class="mdi mdi-undo"></span>
        </button>
        <button class="builder__tool" :disabled="!canRedo" title="Redo" @click="redo">
          <span class="mdi mdi-redo"></span>
        </button>
      </div>

      <button class="btn btn-secondary" @click="cancel">Cancel</button>
      <button class="btn btn-primary" :disabled="saving || loading" @click="save">
        {{ saving ? 'Saving…' : 'Save' }}
      </button>
    </header>

    <div v-if="errorBanner" class="builder__banner" role="alert">
      <div class="builder__banner-title">
        <span class="mdi mdi-alert-circle-outline"></span>
        {{ errorBanner.message }}
      </div>
      <ul v-if="errorBanner.validationErrors.length" class="builder__banner-list">
        <li v-for="(err, i) in errorBanner.validationErrors" :key="i">{{ err }}</li>
      </ul>
      <button class="builder__banner-close" aria-label="Dismiss" @click="errorBanner = null">
        <span class="mdi mdi-close"></span>
      </button>
    </div>

    <div class="builder__body">
      <NodePalette :has-trigger="hasTrigger" :email-disabled="!listId" @add="onPaletteAdd" />

      <div class="builder__canvas" @dragover.prevent @drop.prevent="onDrop">
        <VueFlow
          :id="flowId"
          :nodes-draggable="true"
          :zoom-on-double-click="false"
          @node-click="onNodeClick"
          @pane-click="onPaneClick"
        >
          <Background :gap="18" :size="1" pattern-color="rgba(128, 134, 139, 0.28)" />
          <Controls position="top-left" />
          <MiniMap position="bottom-left" pannable zoomable />

          <template #node-automation="nodeProps">
            <div
              class="flow-node"
              :class="[`flow-node--${nodeMeta(nodeProps.data.nodeType).color}`, { 'flow-node--selected': nodeProps.selected }]"
            >
              <Handle
                v-if="nodeProps.data.nodeType !== 'trigger'"
                type="target"
                :position="Position.Left"
                class="flow-node__handle"
              />
              <span class="flow-node__icon">
                <span class="mdi" :class="nodeMeta(nodeProps.data.nodeType).icon"></span>
              </span>
              <span class="flow-node__text">
                <span class="flow-node__label">{{ nodeLabel(nodeProps.data) }}</span>
                <span class="flow-node__sub">{{ nodeSub(nodeProps.data) }}</span>
              </span>
              <button
                v-if="nodeProps.selected && nodeProps.data.nodeType !== 'trigger'"
                class="flow-node__delete"
                title="Delete node"
                @click.stop="onDeleteNode(nodeProps.id)"
              >
                <span class="mdi mdi-close"></span>
              </button>
              <template v-if="branchHandles(nodeProps.data).length">
                <template
                  v-for="(b, i) in branchHandles(nodeProps.data)"
                  :key="b.id"
                >
                  <span
                    class="flow-node__branch-label"
                    :style="{ top: branchTop(i, branchHandles(nodeProps.data).length) }"
                  >{{ b.label }}</span>
                  <Handle
                    type="source"
                    :id="b.id"
                    :position="Position.Right"
                    class="flow-node__handle"
                    :style="{ top: branchTop(i, branchHandles(nodeProps.data).length) }"
                  />
                </template>
              </template>
              <Handle v-else type="source" :position="Position.Right" class="flow-node__handle" />
            </div>
          </template>
        </VueFlow>

        <div v-if="isNarrow" class="builder__narrow">
          <span class="mdi mdi-monitor"></span>
          The automation builder works best on a screen of at least 1024px.
        </div>
      </div>

      <ConfigPanel
        v-if="selectedNode"
        :key="selectedNode.id"
        :node="selectedNode"
        :lists="listOptions.slice(1)"
        :templates="templateOptions"
        :templates-loading="templatesLoading"
        :eligible-targets="eligibleTargets"
        @patch="builder.patchConfig(selectedNode.id, $event)"
        @close="selectedNodeId = null"
        @delete="onDeleteNode(selectedNode.id)"
      />
    </div>

    <ConfirmDialog />
  </div>
</template>

<style scoped>
.builder {
  display: flex;
  flex-direction: column;
  /* Full-bleed editor: cancel the layout's 28px padding and sit flush under
     the 56px topbar; the builder scrolls internally, not the page. */
  height: calc(100vh - 56px);
  min-height: 480px;
  margin: -28px;
  overflow: hidden;
}

@media (max-width: 1279px) {
  .builder {
    margin: -24px;
  }
}

.builder__toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--border-primary);
  background: var(--bg-primary);
  flex-wrap: wrap;
}

.builder__back {
  display: inline-flex;
  padding: 8px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  font-size: 18px;
}

.builder__back:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.builder__name {
  width: 240px;
  padding: 8px 12px;
  border: 1px solid transparent;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-primary);
  font-size: 15px;
  font-weight: 600;
  font-family: inherit;
}

.builder__name:hover {
  border-color: var(--border-input);
}

.builder__name:focus {
  outline: none;
  border-color: var(--border-focus);
  box-shadow: var(--shadow-focus);
  background: var(--bg-input);
}

.builder__list {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 200px;
}

.builder__list-label {
  font-size: 12px;
  color: var(--text-muted);
  flex: none;
}

.builder__list-select {
  min-width: 180px;
}

.builder__dirty {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  color: var(--warning-text);
  white-space: nowrap;
}

.builder__dirty-dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
  background: var(--warning-500);
}

.builder__spacer {
  flex: 1;
}

.builder__history {
  display: inline-flex;
  gap: 4px;
}

.builder__tool {
  display: inline-flex;
  padding: 7px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-primary);
  color: var(--text-secondary);
  cursor: pointer;
  font-size: 16px;
}

.builder__tool:hover:not(:disabled) {
  border-color: var(--primary-500);
  color: var(--primary-600);
}

.builder__tool:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.builder__banner {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 40px 10px 14px;
  border-bottom: 1px solid var(--danger-500);
  background: var(--danger-50);
}

.builder__banner-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13.5px;
  font-weight: 600;
  color: var(--danger-text);
}

.builder__banner-list {
  margin: 0;
  padding-left: 30px;
  font-size: 12.5px;
  color: var(--danger-text);
  line-height: 1.7;
}

.builder__banner-close {
  position: absolute;
  top: 8px;
  right: 8px;
  display: inline-flex;
  padding: 4px;
  border: none;
  background: transparent;
  color: var(--danger-text);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

.builder__body {
  display: flex;
  flex: 1;
  min-height: 0;
}

.builder__canvas {
  position: relative;
  flex: 1;
  min-width: 0;
  background: var(--bg-secondary);
}

.builder__narrow {
  position: absolute;
  left: 50%;
  bottom: 16px;
  transform: translateX(-50%);
  z-index: 6;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 8px 14px;
  border-radius: 999px;
  border: 1px solid var(--warning-500);
  background: var(--bg-primary);
  color: var(--warning-text);
  font-size: 13px;
  white-space: nowrap;
  box-shadow: var(--shadow-md);
}

/* --- Vue Flow dark-theme overrides --- */

.builder__canvas :deep(.vue-flow__pane) {
  background: var(--bg-secondary);
}

.builder__canvas :deep(.vue-flow__edge-path) {
  stroke: var(--border-input);
  stroke-width: 1.6;
}

.builder__canvas :deep(.vue-flow__edge.selected .vue-flow__edge-path) {
  stroke: var(--primary-500);
}

.builder__canvas :deep(.vue-flow__edge-text) {
  fill: var(--text-secondary);
  font-size: 10px;
}

.builder__canvas :deep(.vue-flow__edge-textbg) {
  fill: var(--bg-primary);
}

.builder__canvas :deep(.vue-flow__handle) {
  width: 9px;
  height: 9px;
  border: 2px solid var(--bg-primary);
  background: var(--primary-500);
}

.builder__canvas :deep(.vue-flow__handle.connectionindicator:hover) {
  box-shadow: 0 0 0 3px var(--primary-50);
}

.builder__canvas :deep(.vue-flow__controls) {
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  overflow: hidden;
  box-shadow: var(--shadow);
}

.builder__canvas :deep(.vue-flow__controls-button) {
  background: var(--bg-primary);
  border-bottom: 1px solid var(--border-primary);
  color: var(--text-secondary);
}

.builder__canvas :deep(.vue-flow__controls-button:hover) {
  background: var(--bg-tertiary);
}

.builder__canvas :deep(.vue-flow__controls-button svg) {
  fill: currentColor;
}

.builder__canvas :deep(.vue-flow__minimap) {
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  overflow: hidden;
  background: var(--bg-primary);
}

.builder__canvas :deep(.vue-flow__minimap-mask) {
  stroke: var(--primary-500);
  stroke-width: 1.5;
  fill: rgba(128, 134, 139, 0.08);
}

/* --- Canvas nodes --- */

.builder__canvas :deep(.flow-node) {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  width: 210px;
  min-height: 46px;
  padding: 8px 12px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  box-shadow: var(--shadow-sm);
  font-size: 13px;
  transition: box-shadow var(--transition), border-color var(--transition);
}

.builder__canvas :deep(.flow-node--selected) {
  border-color: var(--primary-500);
  box-shadow: var(--shadow-focus);
}

.builder__canvas :deep(.flow-node--trigger) {
  border-color: var(--primary-500);
}

.builder__canvas :deep(.flow-node__icon) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  flex: none;
  border-radius: var(--radius-sm);
  background: var(--primary-50);
  color: var(--primary-600);
  font-size: 16px;
}

.builder__canvas :deep(.flow-node--delay .flow-node__icon) {
  background: var(--warning-50);
  color: var(--warning-text);
}

.builder__canvas :deep(.flow-node--listaction .flow-node__icon) {
  background: var(--success-50);
  color: var(--success-text);
}

.builder__canvas :deep(.flow-node__text) {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  flex: 1;
}

.builder__canvas :deep(.flow-node__label) {
  font-weight: 600;
  color: var(--text-primary);
}

.builder__canvas :deep(.flow-node__sub) {
  font-size: 11.5px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.builder__canvas :deep(.flow-node__delete) {
  position: absolute;
  top: -9px;
  right: -9px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 1px solid var(--danger-500);
  border-radius: 999px;
  background: var(--bg-primary);
  color: var(--danger-text);
  cursor: pointer;
  font-size: 13px;
}

.builder__canvas :deep(.flow-node__delete:hover) {
  background: var(--danger-50);
}

.builder__canvas :deep(.flow-node__branch-label) {
  position: absolute;
  right: -30px;
  transform: translateY(-50%);
  font-size: 10px;
  color: var(--text-muted);
  white-space: nowrap;
  pointer-events: none;
}

@media (max-width: 640px) {
  .builder {
    margin: -20px -16px;
    height: calc(100vh - 56px);
  }

  .builder__name {
    width: 100%;
    order: 10;
  }

  .builder__list {
    order: 11;
    width: 100%;
  }
}
</style>
