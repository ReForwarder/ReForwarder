<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useVueFlow, VueFlow, Handle, Position, type Node, type Edge } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import { nodeMeta } from '../../../composables/useAutomationBuilder'
import type { AutomationNodeSpec, NodeStatRow } from '../../../api/types'

/**
 * Read-only flow snapshot for the detail page: the workflow graph with each
 * node's entered / completed / failed counts overlaid. The Vue Flow instance
 * is component-scoped and configured inert — nothing here edits the workflow.
 */
const props = defineProps<{
  nodes: AutomationNodeSpec[]
  nodeStats: NodeStatRow[]
}>()

const FLOW_ID = `automation-stat-canvas-${Math.random().toString(36).slice(2, 8)}`

const store = useVueFlow()

const statsByNode = computed(() => {
  const map = new Map<string, NodeStatRow>()
  for (const row of props.nodeStats) map.set(row.node_id, row)
  return map
})

function counts(id: string) {
  return statsByNode.value.get(id) ?? { node_id: id, node_type: '', entered: 0, completed: 0, failed: 0 }
}

function summaryFor(spec: AutomationNodeSpec): string {
  const cfg = spec.config ?? {}
  switch (spec.type) {
    case 'delay': {
      const d = cfg.duration
      return d ? `Wait ${d} ${cfg.unit ?? ''}`.trim() : 'Wait'
    }
    case 'email':
      return cfg.template_id ? `Template #${cfg.template_id}` : 'Send email'
    case 'webhook':
      return typeof cfg.url === 'string' && cfg.url ? cfg.url : 'Call URL'
    case 'add_to_list':
    case 'remove_from_list':
    case 'list_status':
      return cfg.list_id ? `List #${cfg.list_id}` : nodeMeta(spec.type).label
    default:
      return nodeMeta(spec.type).label
  }
}

function branchLabels(spec: AutomationNodeSpec): { id: string; label: string }[] {
  if (spec.type === 'filter') return [{ id: 'continue', label: 'Yes' }, { id: 'exit', label: 'No' }]
  if (spec.type === 'list_status') {
    return [
      { id: 'active', label: 'Active' },
      { id: 'non_active', label: 'Not active' },
      { id: 'not_in_list', label: 'Not in list' },
    ]
  }
  if (spec.type === 'ab_test') {
    const variants = Array.isArray(spec.config?.variants) ? (spec.config.variants as any[]) : []
    return variants.filter((v) => v?.id).map((v) => ({ id: String(v.id), label: `${v.name} (${v.weight}%)` }))
  }
  return []
}

/** Vertical position (%) of a branch label along the node's right edge. */
function branchTop(index: number, count: number): string {
  if (count <= 1) return '50%'
  return `${25 + (index * 50) / (count - 1)}%`
}

const flowNodes = computed<Node[]>(() =>
  props.nodes.map((spec) => ({
    id: spec.id,
    type: 'stat-node',
    position: { x: spec.position?.x ?? 0, y: spec.position?.y ?? 0 },
    data: {
      nodeType: spec.type,
      label: nodeMeta(spec.type).label,
      icon: nodeMeta(spec.type).icon,
      color: nodeMeta(spec.type).color,
      isTrigger: spec.type === 'trigger',
      summary: summaryFor(spec),
      branches: branchLabels(spec),
      spec,
    },
  })),
)

const flowEdges = computed<Edge[]>(() => {
  const ids = new Set(props.nodes.map((n) => n.id))
  const edges: Edge[] = []
  for (const spec of props.nodes) {
    const cfg = spec.config ?? {}
    if (spec.type === 'filter' || spec.type === 'list_status') {
      const pairs =
        spec.type === 'filter'
          ? [
              { key: 'continue_node_id', handle: 'continue', label: 'Yes' },
              { key: 'exit_node_id', handle: 'exit', label: 'No' },
            ]
          : [
              { key: 'active_node_id', handle: 'active', label: 'Active' },
              { key: 'non_active_node_id', handle: 'non_active', label: 'Not active' },
              { key: 'not_in_list_node_id', handle: 'not_in_list', label: 'Not in list' },
            ]
      for (const p of pairs) {
        const target = cfg[p.key]
        if (typeof target === 'string' && target && ids.has(target)) {
          edges.push({ id: `se-${spec.id}-${p.handle}`, source: spec.id, target, sourceHandle: p.handle, type: 'smoothstep', label: p.label })
        }
      }
    } else if (spec.type === 'ab_test') {
      for (const v of (Array.isArray(cfg.variants) ? cfg.variants : []) as any[]) {
        if (v?.next_node_id && ids.has(v.next_node_id)) {
          edges.push({ id: `se-${spec.id}-${v.id}`, source: spec.id, target: v.next_node_id, sourceHandle: String(v.id), type: 'smoothstep', label: `${v.name} (${v.weight}%)` })
        }
      }
    } else if (spec.next_node_id && ids.has(spec.next_node_id)) {
      edges.push({ id: `se-${spec.id}-out`, source: spec.id, target: spec.next_node_id, type: 'smoothstep' })
    }
  }
  return edges
})

onMounted(() => {
  // Static content: fit once the nodes have measured dimensions.
  const stop = store.onNodesInitialized(() => {
    stop.off()
    store.fitView({ padding: 0.15, maxZoom: 1 })
  })
})
</script>

<template>
  <div class="stat-canvas">
    <VueFlow
      :id="FLOW_ID"
      :nodes="flowNodes"
      :edges="flowEdges"
      fit-view-on-init
      :fit-view-on-init-options="{ padding: 0.15, maxZoom: 1 }"
      :min-zoom="0.2"
      :max-zoom="1.5"
      :nodes-draggable="false"
      :nodes-connectable="false"
      :elements-selectable="false"
      :zoom-on-double-click="false"
      :delete-key-code="null"
    >
      <Background :gap="18" :size="1" pattern-color="rgba(128, 134, 139, 0.28)" />

      <template #node-stat-node="nodeProps">
        <div
          class="stat-node"
          :class="[`stat-node--${nodeProps.data.color}`, { 'stat-node--trigger': nodeProps.data.isTrigger }]"
          :style="`--branches: ${nodeProps.data.branches.length || 1}`"
        >
          <Handle v-if="!nodeProps.data.isTrigger" type="target" :position="Position.Left" class="stat-node__handle" />
          <div class="stat-node__head">
            <span class="stat-node__icon"><span class="mdi" :class="nodeProps.data.icon"></span></span>
            <span class="stat-node__label">{{ nodeProps.data.label }}</span>
          </div>
          <span class="stat-node__summary">{{ nodeProps.data.summary }}</span>
          <div class="stat-node__counts">
            <span class="stat-node__count stat-node__count--entered" title="Entered">{{ counts(nodeProps.id).entered }}</span>
            <span class="stat-node__count stat-node__count--completed" title="Completed">{{ counts(nodeProps.id).completed }}</span>
            <span class="stat-node__count stat-node__count--failed" title="Failed">{{ counts(nodeProps.id).failed }}</span>
          </div>
          <template v-for="(b, i) in nodeProps.data.branches" :key="b.id">
            <span class="stat-node__branch-label" :style="{ top: branchTop(Number(i), nodeProps.data.branches.length) }">{{ b.label }}</span>
            <Handle type="source" :id="b.id" :position="Position.Right" class="stat-node__handle" :style="{ top: branchTop(Number(i), nodeProps.data.branches.length) }" />
          </template>
          <Handle v-if="!nodeProps.data.branches.length" type="source" :position="Position.Right" class="stat-node__handle" />
        </div>
      </template>
    </VueFlow>

    <div class="stat-canvas__legend">
      <span><i class="legend-dot legend-dot--entered"></i> Entered</span>
      <span><i class="legend-dot legend-dot--completed"></i> Completed</span>
      <span><i class="legend-dot legend-dot--failed"></i> Failed</span>
    </div>
  </div>
</template>

<style scoped>
.stat-canvas {
  position: relative;
  height: 340px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  overflow: hidden;
  background: var(--bg-secondary);
}

.stat-canvas__legend {
  position: absolute;
  top: 10px;
  right: 10px;
  z-index: 5;
  display: flex;
  gap: 12px;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  font-size: 11.5px;
  color: var(--text-secondary);
}

.legend-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 999px;
  margin-right: 4px;
}

.legend-dot--entered { background: var(--primary-500); }
.legend-dot--completed { background: var(--success-500); }
.legend-dot--failed { background: var(--danger-500); }

/* Read-only node */
.stat-node {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 168px;
  padding: 10px 12px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  box-shadow: var(--shadow-sm);
  font-size: 12px;
}

.stat-node--trigger {
  border-color: var(--primary-500);
}

.stat-node__head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.stat-node__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  flex: none;
  border-radius: var(--radius-sm);
  background: var(--primary-50);
  color: var(--primary-600);
  font-size: 15px;
}

.stat-node--delay .stat-node__icon { background: var(--warning-50); color: var(--warning-text); }
.stat-node--listaction .stat-node__icon { background: var(--success-50); color: var(--success-text); }

.stat-node__label {
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
}

.stat-node__summary {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-muted);
  font-size: 11.5px;
}

.stat-node__counts {
  display: flex;
  gap: 6px;
}

.stat-node__count {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 34px;
  padding: 1px 6px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.stat-node__count--entered { background: var(--primary-50); color: var(--primary-600); }
.stat-node__count--completed { background: var(--success-50); color: var(--success-text); }
.stat-node__count--failed { background: var(--danger-50); color: var(--danger-text); }

.stat-node__branch-label {
  position: absolute;
  right: -34px;
  transform: translateY(-50%);
  font-size: 10px;
  color: var(--text-muted);
  white-space: nowrap;
  pointer-events: none;
}

/* Handles exist so edges can anchor; visually invisible on the snapshot. */
.stat-node__handle {
  width: 6px;
  height: 6px;
  border: none;
  background: transparent;
  min-width: 0;
  min-height: 0;
}
</style>
