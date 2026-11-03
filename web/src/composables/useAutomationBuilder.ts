import { computed, nextTick, ref, watch } from 'vue'
import {
  useVueFlow,
  type Connection,
  type Edge,
  type EdgeChange,
  type GraphNode,
  type Node,
  type NodeChange,
} from '@vue-flow/core'
import type {
  AutomationABVariant,
  AutomationFilterRule,
  AutomationNodeSpec,
  AutomationRow,
  AutomationTriggerConfig,
  SaveAutomationPayload,
} from '../api/types'

// ---------------------------------------------------------------------------
// Node metadata (palette, canvas nodes, panel headings)
// ---------------------------------------------------------------------------

export type BuilderNodeType =
  | 'trigger'
  | 'delay'
  | 'email'
  | 'filter'
  | 'ab_test'
  | 'list_status'
  | 'add_to_list'
  | 'remove_from_list'
  | 'webhook'

export interface NodeMeta {
  label: string
  icon: string
  description: string
  /** Color class suffix for the node chrome (`.flow-node--<color>`). */
  color: string
  /** Shown in the palette; the trigger lives on the canvas only. */
  palette?: boolean
}

export const NODE_META: Record<BuilderNodeType, NodeMeta> = {
  trigger: { label: 'Trigger', icon: 'mdi-bolt', description: 'Starts the workflow on an event', color: 'trigger' },
  delay: { label: 'Delay', icon: 'mdi-clock-outline', description: 'Wait before the next step', color: 'delay', palette: true },
  email: { label: 'Email', icon: 'mdi-email-outline', description: 'Send a template email to the contact', color: 'email', palette: true },
  filter: { label: 'Filter', icon: 'mdi-filter-outline', description: 'Branch on conditions — Yes / No', color: 'filter', palette: true },
  ab_test: { label: 'A/B Test', icon: 'mdi-chart-bar', description: 'Split contacts across weighted variants', color: 'abtest', palette: true },
  list_status: { label: 'List Status', icon: 'mdi-account-multiple-check', description: 'Branch on list membership and status', color: 'liststatus', palette: true },
  add_to_list: { label: 'Add to List', icon: 'mdi-playlist-plus', description: 'Add the contact to a list', color: 'listaction', palette: true },
  remove_from_list: { label: 'Remove from List', icon: 'mdi-playlist-remove', description: 'Remove the contact from a list', color: 'listaction', palette: true },
  webhook: { label: 'Webhook', icon: 'mdi-web', description: 'Call an outbound HTTP endpoint', color: 'webhook', palette: true },
}

/** Default node config per type, mirroring the backend's accepted shapes. */
export const DEFAULT_NODE_CONFIG: Record<BuilderNodeType, Record<string, any>> = {
  trigger: { description: '', event_kind: 'list.subscribed', list_id: null, custom_event_name: '', frequency: 'once', conditions: [] },
  delay: { duration: 1, unit: 'hours', description: '' },
  email: { template_id: 0, subject_override: '', from_override: '' },
  filter: { conditions: [], continue_node_id: '', exit_node_id: '' },
  ab_test: {
    variants: [
      { id: 'a', name: 'Variant A', weight: 50, next_node_id: '' },
      { id: 'b', name: 'Variant B', weight: 50, next_node_id: '' },
    ] as AutomationABVariant[],
  },
  list_status: { list_id: 0, active_node_id: '', non_active_node_id: '', not_in_list_node_id: '' },
  add_to_list: { list_id: 0 },
  remove_from_list: { list_id: 0 },
  webhook: { url: '', method: 'POST', headers: {}, body: '', secret: '' },
}

/** Branch source handles of filter / list_status nodes: handle id → config key. */
const BRANCH_KEYS: Partial<Record<BuilderNodeType, { handle: string; key: string; label: string }[]>> = {
  filter: [
    { handle: 'continue', key: 'continue_node_id', label: 'Yes' },
    { handle: 'exit', key: 'exit_node_id', label: 'No' },
  ],
  list_status: [
    { handle: 'active', key: 'active_node_id', label: 'Active' },
    { handle: 'non_active', key: 'non_active_node_id', label: 'Not active' },
    { handle: 'not_in_list', key: 'not_in_list_node_id', label: 'Not in list' },
  ],
}

/** Human labels for branch edges (A/B variants resolve to their own names). */
export const BRANCH_LABELS: Record<string, string> = {
  continue: 'Yes',
  exit: 'No',
  active: 'Active',
  non_active: 'Not active',
  not_in_list: 'Not in list',
}

export function isBranchType(type: string): boolean {
  return type === 'filter' || type === 'ab_test' || type === 'list_status'
}

/** One option of a SearchableSelect-driven config field. */
export interface SelectOption {
  value: string
  label: string
}

export function nodeMeta(type: string): NodeMeta {
  return (NODE_META as Record<string, NodeMeta>)[type] ?? NODE_META.delay
}

const DELAY_UNITS = ['minutes', 'hours', 'days', 'weeks']
const EVENT_KINDS = [
  'list.subscribed',
  'contact.created',
  'contact.unsubscribed',
  'email.opened',
  'email.clicked',
  'email.bounced',
  'custom',
]
const FREQUENCIES = ['once', 'every_time']

/** Serialized builder document — exactly the wire shape of Automation.Nodes. */
export interface BuilderDoc {
  nodes: AutomationNodeSpec[]
  root_node_id: string
}

function docsEqual(a: BuilderDoc, b: BuilderDoc): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

/**
 * Deep clone for values read out of the Vue Flow store: node configs arrive as
 * reactive proxies, which structuredClone rejects (DataCloneError). Node
 * configs are JSON documents by contract (they round-trip through the API), so
 * a JSON round-trip is both safe and normalizing. History stack entries built
 * from these clones are fully plain and use structuredClone directly.
 */
function cloneConfig(value: unknown): Record<string, any> {
  return JSON.parse(JSON.stringify(value ?? {}))
}

let idCounter = 0
function nextNodeId(): string {
  idCounter += 1
  return `n_${Date.now().toString(36)}_${idCounter.toString(36)}`
}

// ---------------------------------------------------------------------------
// Composable
// ---------------------------------------------------------------------------

let flowInstanceCounter = 0

export function useAutomationBuilder() {
  const flowId = `automation-builder-${++flowInstanceCounter}`

  const store = useVueFlow({
    id: flowId,
    deleteKeyCode: ['Backspace', 'Delete'],
    minZoom: 0.2,
    maxZoom: 1.5,
    defaultEdgeOptions: { type: 'smoothstep' },
  })

  const { nodes, edges, findNode, setNodes, setEdges, addNodes, removeNodes, removeEdges, addEdges, updateNodeData, fitView } = store

  // Builder form state (kept here so the dirty check sees everything).
  const name = ref('')
  const listId = ref<number | null>(null)
  const exitOnReply = ref(false)
  const selectedNodeId = ref<string | null>(null)

  const selectedNode = computed<GraphNode | null>(() => {
    if (!selectedNodeId.value) return null
    return (findNode(selectedNodeId.value) as GraphNode | undefined) ?? null
  })

  // --- History -------------------------------------------------------------
  // Snapshot-based: every state change (drag, connect, delete key, config edit)
  // flows through the Vue Flow store; a watcher serializes the graph and pushes
  // the previous document onto the undo stack. Typing one field collapses into
  // a single step via the 1-second coalescing window.

  const undoStack = ref<BuilderDoc[]>([])
  const redoStack = ref<BuilderDoc[]>([])
  const MAX_HISTORY = 200
  const COALESCE_MS = 1000
  let lastChangeAt = 0
  let suspended = true // history recording starts after hydration

  const currentDoc = ref<BuilderDoc | null>(null)

  /** The live graph serialized to the wire shape (canvas is the source of truth). */
  const graphDoc = computed<BuilderDoc>(() => serializeDoc())
  const graphJson = computed(() => JSON.stringify(graphDoc.value))

  watch(graphJson, () => {
    if (suspended) return
    const doc = graphDoc.value
    const now = Date.now()
    const coalesce = now - lastChangeAt <= COALESCE_MS
    if (!coalesce && currentDoc.value && !docsEqual(currentDoc.value, doc)) {
      undoStack.value.push(structuredClone(currentDoc.value))
      if (undoStack.value.length > MAX_HISTORY) undoStack.value.shift()
    }
    // Any user edit invalidates the redo branch — including coalesced ones.
    redoStack.value = []
    currentDoc.value = doc
    lastChangeAt = now
  })

  async function applyDoc(doc: BuilderDoc) {
    suspended = true
    const { flowNodes, flowEdges } = specsToFlow(doc.nodes)
    setNodes(flowNodes)
    setEdges(flowEdges)
    currentDoc.value = structuredClone(doc)
    await nextTick()
    suspended = false
  }

  function undo() {
    const prev = undoStack.value.pop()
    if (!prev || !currentDoc.value) return
    redoStack.value.push(structuredClone(currentDoc.value))
    lastChangeAt = 0
    applyDoc(prev)
  }

  function redo() {
    const next = redoStack.value.pop()
    if (!next || !currentDoc.value) return
    undoStack.value.push(structuredClone(currentDoc.value))
    lastChangeAt = 0
    applyDoc(next)
  }

  const canUndo = computed(() => undoStack.value.length > 0)
  const canRedo = computed(() => redoStack.value.length > 0)

  // --- Dirty tracking ------------------------------------------------------

  const baseline = ref<{ name: string; listId: number | null; docJson: string } | null>(null)

  const hasUnsavedChanges = computed(() => {
    if (!baseline.value) return false
    return name.value !== baseline.value.name || listId.value !== baseline.value.listId || graphJson.value !== baseline.value.docJson
  })

  function markSaved() {
    baseline.value = { name: name.value, listId: listId.value, docJson: graphJson.value }
  }

  /**
   * Hydrate the canvas from an automation row (or a blank trigger for a new
   * one). Suspends history recording while it runs, then records the loaded
   * state as the clean baseline.
   */
  async function hydrate(automation?: AutomationRow) {
    suspended = true
    selectedNodeId.value = null
    let flowNodes: Node[] = []
    let flowEdges: Edge[] = []
    if (automation) {
      name.value = automation.name
      listId.value = automation.list_id ?? null
      exitOnReply.value = automation.exit_on_reply
      const specs = rehydrateSpecs(automation)
      const flow = specsToFlow(specs)
      flowNodes = flow.flowNodes
      flowEdges = flow.flowEdges
    } else {
      name.value = ''
      listId.value = null
      exitOnReply.value = false
      flowNodes = [
        {
          id: nextNodeId(),
          type: 'automation',
          position: { x: 80, y: 80 },
          data: { nodeType: 'trigger', config: structuredClone(DEFAULT_NODE_CONFIG.trigger) },
        },
      ]
    }
    setNodes(flowNodes)
    setEdges(flowEdges)
    undoStack.value = []
    redoStack.value = []
    await nextTick()
    // Normalize through the serializer so the baseline matches exactly what the
    // watcher computes — stale branch targets from the server get repaired on
    // both sides and never read as unsaved changes.
    currentDoc.value = serializeDoc()
    baseline.value = { name: name.value, listId: listId.value, docJson: JSON.stringify(currentDoc.value) }
    suspended = false
    lastChangeAt = 0
  }

  // --- Serialization (flow ⇄ wire shape) ------------------------------------

  /** Row specs with the row-level trigger config rehydrated into the trigger node. */
  function rehydrateSpecs(automation: AutomationRow): AutomationNodeSpec[] {
    const specs = (automation.nodes ?? []).map((spec) => ({
      id: spec.id,
      type: spec.type,
      config: structuredClone(spec.config ?? {}),
      next_node_id: spec.next_node_id ?? null,
      position: { x: spec.position?.x ?? 0, y: spec.position?.y ?? 0 },
    }))
    const trigger = specs.find((s) => s.type === 'trigger')
    const rowCfg = automation.trigger_config
    if (trigger && rowCfg && typeof rowCfg.event_kind === 'string' && rowCfg.event_kind) {
      const cfg = trigger.config ?? {}
      if (!cfg.event_kind) {
        trigger.config = {
          description: '',
          event_kind: rowCfg.event_kind,
          list_id: rowCfg.list_id ?? null,
          custom_event_name: rowCfg.custom_event_name ?? '',
          frequency: rowCfg.frequency || 'once',
          conditions: Array.isArray(rowCfg.conditions) ? structuredClone(rowCfg.conditions) : [],
          ...cfg,
        }
      }
    }
    return specs
  }

  /** Node specs + edges → flow nodes and labeled branch edges. */
  function specsToFlow(specs: AutomationNodeSpec[]): { flowNodes: Node[]; flowEdges: Edge[] } {
    const flowNodes: Node[] = specs.map((spec) => ({
      id: spec.id,
      type: 'automation',
      position: { x: spec.position?.x ?? 0, y: spec.position?.y ?? 0 },
      data: { nodeType: spec.type, config: structuredClone(spec.config ?? {}) },
    }))
    const flowEdges: Edge[] = []
    for (const spec of specs) {
      const cfg = spec.config ?? {}
      if (spec.type === 'filter' || spec.type === 'list_status') {
        for (const { handle, key, label } of BRANCH_KEYS[spec.type] ?? []) {
          const target = cfg[key]
          if (typeof target === 'string' && target) {
            flowEdges.push(branchEdge(spec.id, handle, target, label))
          }
        }
      } else if (spec.type === 'ab_test') {
        for (const v of (cfg.variants ?? []) as AutomationABVariant[]) {
          if (v && v.next_node_id) {
            flowEdges.push(branchEdge(spec.id, v.id, v.next_node_id, `${v.name} (${v.weight}%)`))
          }
        }
      } else if (spec.next_node_id) {
        flowEdges.push({
          id: `e-${spec.id}-out-${spec.next_node_id}`,
          source: spec.id,
          target: spec.next_node_id,
          sourceHandle: null,
          targetHandle: null,
          type: 'smoothstep',
        })
      }
    }
    return { flowNodes, flowEdges }
  }

  function branchEdge(source: string, handle: string, target: string, label: string): Edge {
    return {
      id: `e-${source}-${handle}-${target}`,
      source,
      target,
      sourceHandle: handle,
      targetHandle: null,
      type: 'smoothstep',
      label,
    }
  }

  /**
   * Live canvas → wire document. Edge topology is the source of truth: linear
   * targets become next_node_id, branch edges are written into the source
   * node's config fields, and stale targets without a backing edge (or pointing
   * at deleted nodes) are pruned.
   */
  function serializeDoc(): BuilderDoc {
    const specs: AutomationNodeSpec[] = (nodes.value as GraphNode[]).map((n) => ({
      id: n.id,
      type: (n.data?.nodeType as BuilderNodeType) ?? 'delay',
      config: cloneConfig(n.data?.config),
      next_node_id: null,
      position: { x: n.position.x, y: n.position.y },
    }))
    const byId = new Map(specs.map((s) => [s.id, s]))

    const edgeFor = (sourceId: string, handle: string) =>
      edges.value.find((e) => e.source === sourceId && (e.sourceHandle ?? '') === handle && byId.has(e.target))

    // Write branch targets from the edges (the reference workflow platform invariant: onConnect keeps
    // them in config; serialization re-derives them so the two never drift).
    for (const spec of specs) {
      if (spec.type === 'filter' || spec.type === 'list_status') {
        for (const { handle, key } of BRANCH_KEYS[spec.type] ?? []) {
          const edge = edgeFor(spec.id, handle)
          if (edge) spec.config![key] = edge.target
          else if (spec.config![key]) spec.config![key] = ''
        }
      } else if (spec.type === 'ab_test') {
        for (const v of (spec.config!.variants ?? []) as AutomationABVariant[]) {
          if (!v || !v.id) continue
          const edge = edgeFor(spec.id, v.id)
          if (edge) v.next_node_id = edge.target
          else if (v.next_node_id) v.next_node_id = ''
        }
      } else {
        const edge = edgeFor(spec.id, '')
        spec.next_node_id = edge ? edge.target : null
      }
    }

    // Prune dangling references to nodes that no longer exist.
    for (const spec of specs) {
      const missing = (target: any): boolean => typeof target === 'string' && target !== '' && !byId.has(target)
      if (spec.next_node_id && missing(spec.next_node_id)) spec.next_node_id = null
      if (spec.type === 'filter' || spec.type === 'list_status') {
        for (const { key } of BRANCH_KEYS[spec.type] ?? []) {
          if (missing(spec.config![key])) spec.config![key] = ''
        }
      } else if (spec.type === 'ab_test') {
        for (const v of (spec.config!.variants ?? []) as AutomationABVariant[]) {
          if (v && v.next_node_id && missing(v.next_node_id)) v.next_node_id = ''
        }
      }
    }

    const trigger = specs.find((s) => s.type === 'trigger')
    return { nodes: specs, root_node_id: trigger?.id ?? '' }
  }

  /** Wire document → flow nodes/edges (used by undo/redo restore). */
  function docToFlow(doc: BuilderDoc) {
    return specsToFlow(doc.nodes)
  }

  /** Row-level trigger config derived from the trigger node (never drifts). */
  function triggerConfigFromNode(): AutomationTriggerConfig | undefined {
    const doc = graphDoc.value
    const trigger = doc.nodes.find((s) => s.type === 'trigger')
    if (!trigger) return undefined
    const cfg = trigger.config ?? {}
    const kind = typeof cfg.event_kind === 'string' ? cfg.event_kind.trim() : ''
    if (!kind) return undefined
    return {
      event_kind: kind,
      list_id: typeof cfg.list_id === 'number' ? cfg.list_id : null,
      custom_event_name: typeof cfg.custom_event_name === 'string' ? cfg.custom_event_name : '',
      frequency: typeof cfg.frequency === 'string' && cfg.frequency ? cfg.frequency : 'once',
      conditions: Array.isArray(cfg.conditions) ? (structuredClone(cfg.conditions) as AutomationFilterRule[]) : [],
    }
  }

  function toAutomationPayload(): SaveAutomationPayload {
    const doc = graphDoc.value
    return {
      name: name.value.trim(),
      list_id: listId.value,
      exit_on_reply: exitOnReply.value,
      trigger_config: triggerConfigFromNode(),
      nodes: structuredClone(doc.nodes),
      root_node_id: doc.root_node_id,
    }
  }

  // --- Mutations -------------------------------------------------------------

  /** Add a node of `type`; position defaults to the center of the canvas. */
  function addNode(type: BuilderNodeType, position?: { x: number; y: number }): string | null {
    if (type === 'trigger') {
      if (nodes.value.some((n: any) => n.data?.nodeType === 'trigger')) return null
    }
    const pos = position ?? canvasCenter()
    const node: Node = {
      id: nextNodeId(),
      type: 'automation',
      position: pos,
      data: { nodeType: type, config: structuredClone(DEFAULT_NODE_CONFIG[type]) },
    }
    addNodes([node])
    selectedNodeId.value = node.id
    return node.id
  }

  function canvasCenter(): { x: number; y: number } {
    const el = document.querySelector('.vue-flow')
    if (el) {
      const rect = el.getBoundingClientRect()
      return store.screenToFlowCoordinate({ x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 })
    }
    return { x: 0, y: 0 }
  }

  /** Delete a node: drops its edges and clears branch targets on the other endpoints. */
  function deleteNode(id: string) {
    const connected = edges.value.filter((e) => e.source === id || e.target === id)
    clearTargetsFromEdges(connected.filter((e) => e.source !== id))
    if (selectedNodeId.value === id) selectedNodeId.value = null
    removeNodes([id])
    removeEdges(connected)
  }

  /** Patch a node's config (ConfigPanel forms go through here). */
  function patchConfig(id: string, patch: Record<string, any>) {
    updateNodeData(id, (n) => ({
      config: { ...((n.data?.config as Record<string, any>) ?? {}), ...patch },
    }))
  }

  /** Clear branch target fields on the SOURCES of the given edges. */
  function clearTargetsFromEdges(removed: Edge[]) {
    for (const edge of removed) {
      const source = findNode(edge.source) as GraphNode | undefined
      if (!source) continue
      const type = source.data?.nodeType as BuilderNodeType
      const handle = edge.sourceHandle ?? ''
      const cfg = { ...((source.data?.config as Record<string, any>) ?? {}) }
      if (type === 'filter' || type === 'list_status') {
        const entry = (BRANCH_KEYS[type] ?? []).find((k) => k.handle === handle)
        if (entry) patchConfig(source.id, { [entry.key]: '' })
      } else if (type === 'ab_test') {
        const variants = ((cfg.variants ?? []) as AutomationABVariant[]).map((v) =>
          v.id === handle ? { ...v, next_node_id: '' } : v,
        )
        patchConfig(source.id, { variants })
      }
      // Linear targets derive from edges at serialization — nothing to clear.
    }
  }

  // The Vue Flow store applies default changes (drags, delete key); these hooks
  // only repair what the graph model cannot see.

  store.onNodesChange((changes: NodeChange[]) => {
    const removed = changes.filter((c): c is NodeChange & { type: 'remove' } => c.type === 'remove')
    if (!removed.length) return
    // Nodes removed via the keyboard: vue-flow drops their edges; clear the
    // branch targets that pointed at them from surviving sources.
    const removedIds = new Set(removed.map((c) => c.id))
    const dangling = edges.value.filter((e) => removedIds.has(e.source) || removedIds.has(e.target))
    clearTargetsFromEdges(dangling.filter((e) => !removedIds.has(e.source)))
    if (dangling.length) removeEdges(dangling)
    if (selectedNodeId.value && removedIds.has(selectedNodeId.value)) selectedNodeId.value = null
  })

  store.onEdgesChange((changes: EdgeChange[]) => {
    const removed = changes.filter((c) => c.type === 'remove')
    if (!removed.length) return
    // Edge deleted via the keyboard: clear the target it wrote into its source.
    clearTargetsFromEdges(
      removed.map((c) => ({
        id: c.id,
        source: (c as EdgeChange & { source: string }).source,
        target: (c as EdgeChange & { target: string }).target,
        sourceHandle: (c as EdgeChange & { sourceHandle: string | null }).sourceHandle ?? null,
        targetHandle: null,
      })) as Edge[],
    )
  })

  // --- Connections -----------------------------------------------------------

  function reachable(from: string, to: string): boolean {
    const adjacency = new Map<string, string[]>()
    for (const e of edges.value) {
      const list = adjacency.get(e.source) ?? []
      list.push(e.target)
      adjacency.set(e.source, list)
    }
    const seen = new Set<string>()
    const queue = [from]
    while (queue.length) {
      const id = queue.shift()!
      if (id === to) return true
      if (seen.has(id)) continue
      seen.add(id)
      queue.push(...(adjacency.get(id) ?? []))
    }
    return false
  }

  function connectErrorMessage(connection: Connection): string | null {
    const target = findNode(connection.target) as GraphNode | undefined
    const source = findNode(connection.source) as GraphNode | undefined
    if (!source || !target) return 'That node no longer exists'
    if ((target.data?.nodeType as string) === 'trigger') return 'Nothing can connect to the trigger node'
    if (connection.source === connection.target) return 'A node cannot connect to itself'
    if (reachable(connection.target, connection.source)) return 'That connection would create an infinite loop'
    return null
  }

  store.onConnect((connection: Connection) => {
    const error = connectErrorMessage(connection)
    if (error) {
      onConnectRefused.value = error
      return
    }
    const handle = connection.sourceHandle ?? ''
    // One edge per source handle: replace any previous one.
    const previous = edges.value.filter((e) => e.source === connection.source && (e.sourceHandle ?? '') === handle)
    if (previous.length) removeEdges(previous)
    addEdges([
      {
        id: `e-${connection.source}-${handle || 'out'}-${connection.target}`,
        source: connection.source,
        target: connection.target,
        sourceHandle: connection.sourceHandle ?? null,
        targetHandle: connection.targetHandle ?? null,
        type: 'smoothstep',
      },
    ])
    // the reference workflow platform invariant: branch targets live in the source node's config.
    writeBranchTarget(connection.source, handle, connection.target)
  })

  function writeBranchTarget(sourceId: string, handle: string, target: string) {
    const source = findNode(sourceId) as GraphNode | undefined
    if (!source) return
    const type = source.data?.nodeType as BuilderNodeType
    if (type === 'filter' || type === 'list_status') {
      const entry = (BRANCH_KEYS[type] ?? []).find((k) => k.handle === handle)
      if (entry) patchConfig(sourceId, { [entry.key]: target })
    } else if (type === 'ab_test') {
      const cfg = (source.data?.config as Record<string, any>) ?? {}
      const variants = ((cfg.variants ?? []) as AutomationABVariant[]).map((v) =>
        v.id === handle ? { ...v, next_node_id: target } : v,
      )
      patchConfig(sourceId, { variants })
    }
  }

  /** Transient connect-refusal message; the builder surfaces it as a toast. */
  const onConnectRefused = ref('')

  // --- Validation (client mirror of internal/services/automation/domain.go) ---

  function filterRuleProblem(rule: any): string {
    const field = typeof rule?.field === 'string' ? rule.field.trim() : ''
    const operator = typeof rule?.operator === 'string' ? rule.operator.trim() : ''
    if (!field) return 'field is required'
    if (!operator) return 'operator is required'
    if (field === 'email' || field === 'name' || field === 'status') {
      if (!['equals', 'not_equals', 'contains'].includes(operator)) {
        return `operator "${operator}" is not supported for field "${field}": use equals, not_equals or contains`
      }
      return ''
    }
    if (field === 'in_list') {
      if (!Number.isFinite(Number(rule?.value)) || Number(rule?.value) <= 0) return 'in_list needs a numeric list id as its value'
      return ''
    }
    if (field.startsWith('custom_fields.') && field.length > 'custom_fields.'.length) {
      if (!['equals', 'not_equals', 'contains', 'exists', 'not_exists'].includes(operator)) {
        return `operator "${operator}" is not supported for custom fields: use equals, not_equals, contains, exists or not_exists`
      }
      return ''
    }
    return `unknown field "${field}"`
  }

  /**
   * Client-side validation pass, returning the same messages the server's
   * ValidateWorkflow produces so the builder can pre-flight before saving.
   */
  function validate(payload: SaveAutomationPayload): string[] {
    const errs: string[] = []
    const specs = payload.nodes
    if (specs.length === 0) return ['workflow has no nodes: add a trigger node first']

    const ids = new Map<string, AutomationNodeSpec>()
    let triggers = 0
    for (const n of specs) {
      if (!n.id) {
        errs.push('a node is missing an id: every node needs a unique id')
        continue
      }
      if (ids.has(n.id)) {
        errs.push(`node "${n.id}": duplicate node id: ids must be unique`)
        continue
      }
      ids.set(n.id, n)
      if (n.type === 'trigger') triggers += 1
      if (!NODE_META[n.type as BuilderNodeType]) errs.push(`node "${n.id}": unknown node type "${n.type}"`)
    }

    if (triggers === 0) errs.push('workflow has no trigger node: add exactly one trigger node and make it the root')
    else if (triggers > 1) errs.push(`workflow has ${triggers} trigger nodes: exactly one trigger node is allowed`)

    const root = payload.root_node_id ? ids.get(payload.root_node_id) : undefined
    if (!payload.root_node_id) errs.push('root_node_id is empty: it must reference the trigger node')
    else if (!root) errs.push(`root_node_id "${payload.root_node_id}" does not reference an existing node`)
    else if (root.type !== 'trigger') errs.push(`root_node_id "${payload.root_node_id}" references a "${root.type}" node: the root must be the trigger node`)

    // Trigger config.
    const triggerNode = specs.find((n) => n.type === 'trigger')
    if (triggerNode && triggers === 1) {
      const cfg = triggerNode.config ?? {}
      const kind = typeof cfg.event_kind === 'string' ? cfg.event_kind : ''
      if (!EVENT_KINDS.includes(kind)) errs.push(`trigger node "${triggerNode.id}": unknown event_kind "${kind}"`)
      if (kind === 'list.subscribed' && !cfg.list_id && !payload.list_id) {
        errs.push(`trigger node "${triggerNode.id}": event_kind "list.subscribed" requires a list_id`)
      }
      if (kind === 'custom' && !(typeof cfg.custom_event_name === 'string' && cfg.custom_event_name.trim())) {
        errs.push(`trigger node "${triggerNode.id}": event_kind "custom" requires a custom_event_name`)
      }
      if (!FREQUENCIES.includes(cfg.frequency)) {
        errs.push(`trigger node "${triggerNode.id}": unknown frequency "${cfg.frequency}": use "once" or "every_time"`)
      }
      const conditions = Array.isArray(cfg.conditions) ? cfg.conditions : []
      conditions.forEach((rule: any, i: number) => {
        const problem = filterRuleProblem(rule)
        if (problem) errs.push(`trigger node "${triggerNode.id}": condition #${i + 1}: ${problem}`)
      })
    }

    // Per-node config.
    for (const n of specs) {
      const cfg = n.config ?? {}
      switch (n.type) {
        case 'delay': {
          const duration = Number(cfg.duration)
          if (!Number.isFinite(duration) || duration <= 0) errs.push(`delay node "${n.id}": duration must be a number greater than 0`)
          if (!DELAY_UNITS.includes(cfg.unit)) errs.push(`delay node "${n.id}": unknown unit "${cfg.unit}": use minutes, hours, days or weeks`)
          break
        }
        case 'email': {
          if (!payload.list_id) errs.push(`email node "${n.id}": the automation must have a list: email nodes send to the automation's list`)
          if (!Number(cfg.template_id)) errs.push(`email node "${n.id}": template_id is required`)
          break
        }
        case 'filter': {
          const conditions = Array.isArray(cfg.conditions) ? cfg.conditions : []
          if (conditions.length === 0) errs.push(`filter node "${n.id}": at least one condition is required`)
          conditions.forEach((rule: any, i: number) => {
            const problem = filterRuleProblem(rule)
            if (problem) errs.push(`filter node "${n.id}": condition #${i + 1}: ${problem}`)
          })
          break
        }
        case 'ab_test': {
          const variants = Array.isArray(cfg.variants) ? (cfg.variants as AutomationABVariant[]) : []
          if (variants.length < 2) errs.push(`ab_test node "${n.id}": at least 2 variants are required`)
          let sum = 0
          let weightsOK = variants.length > 0
          const seen = new Set<string>()
          variants.forEach((v, i) => {
            const label = v?.id || `#${i + 1}`
            if (!v?.id) errs.push(`ab_test node "${n.id}": variant ${label} is missing an id`)
            else if (seen.has(v.id)) errs.push(`ab_test node "${n.id}": duplicate variant id "${v.id}"`)
            else seen.add(v.id)
            const weight = Number(v?.weight)
            if (!Number.isFinite(weight)) {
              errs.push(`ab_test node "${n.id}": variant ${label} is missing a numeric weight`)
              weightsOK = false
            } else {
              if (weight < 0 || weight > 100) {
                errs.push(`ab_test node "${n.id}": variant ${label} weight must be between 0 and 100`)
                weightsOK = false
              }
              sum += weight
            }
            if (!v?.next_node_id) errs.push(`ab_test node "${n.id}": variant ${label} has no next_node_id: every variant must point at a node`)
          })
          if (weightsOK && variants.length >= 2 && Math.abs(sum - 100) > 1e-9) {
            errs.push(`ab_test node "${n.id}": variant weights must sum to exactly 100 (got ${sum})`)
          }
          break
        }
        case 'list_status': {
          if (!Number(cfg.list_id)) errs.push(`list_status node "${n.id}": list_id is required`)
          const targets = ['active_node_id', 'non_active_node_id', 'not_in_list_node_id'].filter((k) => cfg[k]).length
          if (targets === 0) {
            errs.push(`list_status node "${n.id}": at least one branch target is required (active_node_id, non_active_node_id or not_in_list_node_id)`)
          }
          break
        }
        case 'add_to_list':
        case 'remove_from_list': {
          if (!Number(cfg.list_id)) errs.push(`${n.type} node "${n.id}": list_id is required`)
          break
        }
        case 'webhook': {
          const url = typeof cfg.url === 'string' ? cfg.url : ''
          if (!url) errs.push(`webhook node "${n.id}": url is required`)
          else {
            let ok = false
            try {
              const parsed = new URL(url)
              ok = !!parsed.host && (parsed.protocol === 'http:' || parsed.protocol === 'https:')
            } catch {
              ok = false
            }
            if (!ok) errs.push(`webhook node "${n.id}": url must be a valid http or https URL`)
          }
          break
        }
      }
    }

    // Every target resolves; nothing targets the trigger.
    for (const n of specs) {
      const targets: string[] = []
      if (n.next_node_id) targets.push(n.next_node_id)
      if (n.type === 'filter' || n.type === 'list_status') {
        for (const { key } of BRANCH_KEYS[n.type] ?? []) {
          if (typeof n.config?.[key] === 'string' && n.config[key]) targets.push(n.config[key])
        }
      } else if (n.type === 'ab_test') {
        for (const v of (n.config?.variants ?? []) as AutomationABVariant[]) {
          if (v?.next_node_id) targets.push(v.next_node_id)
        }
      }
      for (const target of targets) {
        const resolved = ids.get(target)
        if (!resolved) {
          errs.push(`node "${n.id}": references missing node "${target}"`)
          continue
        }
        if (resolved.type === 'trigger') errs.push(`node "${n.id}": cannot target the trigger node "${target}"`)
      }
    }

    // Reachability from the root (orphans).
    const adjacency = new Map<string, string[]>()
    for (const n of specs) {
      const targets: string[] = []
      if (n.next_node_id) targets.push(n.next_node_id)
      if (n.type === 'filter' || n.type === 'list_status') {
        for (const { key } of BRANCH_KEYS[n.type] ?? []) {
          if (n.config?.[key]) targets.push(n.config[key])
        }
      } else if (n.type === 'ab_test') {
        for (const v of (n.config?.variants ?? []) as AutomationABVariant[]) {
          if (v?.next_node_id) targets.push(v.next_node_id)
        }
      }
      adjacency.set(n.id, targets.filter((t) => ids.has(t)))
    }
    if (payload.root_node_id && ids.has(payload.root_node_id)) {
      const reach = new Set<string>()
      const queue = [payload.root_node_id]
      while (queue.length) {
        const id = queue.shift()!
        if (reach.has(id)) continue
        reach.add(id)
        queue.push(...(adjacency.get(id) ?? []))
      }
      for (const n of specs) {
        if (!reach.has(n.id)) errs.push(`node "${n.id}" is not reachable from the trigger: connect it to the flow or remove it`)
      }
      // Cycle: an edge back into the current walk's path (converging branches
      // are legitimate; the backend uses the same on-path DFS).
      const visited = new Set<string>()
      const onPath = new Map<string, number>()
      let path: string[] = []
      const cycle: { ids: string[] | null } = { ids: null }
      const dfs = (id: string): boolean => {
        visited.add(id)
        onPath.set(id, path.length)
        path.push(id)
        for (const target of adjacency.get(id) ?? []) {
          if (onPath.has(target)) {
            cycle.ids = [...path.slice(onPath.get(target)!), target]
            return true
          }
          if (!visited.has(target) && dfs(target)) return true
        }
        onPath.delete(id)
        path = path.slice(0, -1)
        return false
      }
      dfs(payload.root_node_id)
      if (cycle.ids) errs.push(`workflow contains an infinite loop: ${cycle.ids.join(' -> ')}`)
    }

    return errs
  }

  return {
    flowId,
    nodes,
    edges,
    store,
    fitView: () => fitView({ padding: 0.2, maxZoom: 0.9 }),
    screenToFlowCoordinate: (pos: { x: number; y: number }) => store.screenToFlowCoordinate(pos),
    name,
    listId,
    exitOnReply,
    selectedNodeId,
    selectedNode,
    addNode,
    deleteNode,
    patchConfig,
    hydrate,
    hasUnsavedChanges,
    markSaved,
    undo,
    redo,
    canUndo,
    canRedo,
    graphDoc,
    toAutomationPayload,
    validate,
    onConnectRefused,
  }
}
