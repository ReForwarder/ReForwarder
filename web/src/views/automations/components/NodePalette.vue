<script setup lang="ts">
import { NODE_META, type BuilderNodeType } from '../../../composables/useAutomationBuilder'

/**
 * Left rail of the builder: all 9 node types. Items are draggable — the type
 * travels as `application/x-ref-node` — and clickable to add at the canvas
 * center. Trigger is disabled while the workflow already has one (exactly one
 * is allowed) and email is disabled until the automation list is chosen.
 */
const props = defineProps<{
  /** The workflow already has its one allowed trigger node. */
  hasTrigger?: boolean
  /** Email nodes send to the automation list, so they are disabled until one is chosen. */
  emailDisabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'add', type: BuilderNodeType): void
}>()

const PALETTE_ORDER: BuilderNodeType[] = [
  'trigger',
  'delay',
  'email',
  'filter',
  'ab_test',
  'list_status',
  'add_to_list',
  'remove_from_list',
  'webhook',
]

const DRAG_MIME = 'application/x-ref-node'

function triggerDisabled(): boolean {
  return !!props.hasTrigger
}

function itemDisabled(type: BuilderNodeType): boolean {
  if (type === 'trigger') return triggerDisabled()
  if (type === 'email') return !!props.emailDisabled
  return false
}

function disabledTitle(type: BuilderNodeType): string {
  if (type === 'trigger' && triggerDisabled()) return 'The workflow already has a trigger node'
  if (type === 'email' && props.emailDisabled) return 'Choose an automation list first'
  return NODE_META[type].description
}

function onDragStart(event: DragEvent, type: BuilderNodeType) {
  if (itemDisabled(type)) {
    event.preventDefault()
    return
  }
  event.dataTransfer?.setData(DRAG_MIME, type)
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'copy'
}

function onClick(type: BuilderNodeType) {
  if (itemDisabled(type)) return
  emit('add', type)
}
</script>

<template>
  <aside class="palette">
    <h2 class="palette__title">Nodes</h2>
    <p class="palette__hint">Drag onto the canvas or click to add.</p>
    <div class="palette__list">
      <button
        v-for="type in PALETTE_ORDER"
        :key="type"
        type="button"
        class="palette__item"
        :class="{ 'palette__item--disabled': itemDisabled(type) }"
        :title="disabledTitle(type)"
        draggable="true"
        @dragstart="onDragStart($event, type)"
        @click="onClick(type)"
      >
        <span class="palette__icon" :class="`palette__icon--${NODE_META[type].color}`">
          <span class="mdi" :class="NODE_META[type].icon"></span>
        </span>
        <span class="palette__text">
          <span class="palette__label">{{ NODE_META[type].label }}</span>
          <span class="palette__desc">{{ NODE_META[type].description }}</span>
        </span>
      </button>
    </div>
  </aside>
</template>

<style scoped>
.palette {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 216px;
  flex: none;
  padding: 14px 12px;
  border-right: 1px solid var(--border-primary);
  background: var(--bg-secondary);
  overflow-y: auto;
}

.palette__title {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-tertiary);
}

.palette__hint {
  margin: 0 0 4px;
  font-size: 12px;
  color: var(--text-muted);
}

.palette__list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.palette__item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 8px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-primary);
  cursor: grab;
  text-align: left;
  font-family: inherit;
  transition: border-color var(--transition), box-shadow var(--transition), opacity var(--transition);
}

.palette__item:hover {
  border-color: var(--primary-500);
  box-shadow: var(--shadow-sm);
}

.palette__item:active {
  cursor: grabbing;
}

.palette__item--disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.palette__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  flex: none;
  border-radius: var(--radius-sm);
  font-size: 17px;
}

.palette__icon--trigger { background: var(--primary-50); color: var(--primary-600); }
.palette__icon--delay { background: var(--warning-50); color: var(--warning-text); }
.palette__icon--email { background: var(--primary-50); color: var(--primary-600); }
.palette__icon--filter { background: var(--primary-50); color: var(--primary-600); }
.palette__icon--abtest { background: var(--primary-50); color: var(--primary-600); }
.palette__icon--liststatus { background: var(--primary-50); color: var(--primary-600); }
.palette__icon--listaction { background: var(--success-50); color: var(--success-text); }
.palette__icon--webhook { background: var(--primary-50); color: var(--primary-600); }

.palette__text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.palette__label {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
}

.palette__desc {
  font-size: 11.5px;
  color: var(--text-muted);
  line-height: 1.35;
}
</style>
