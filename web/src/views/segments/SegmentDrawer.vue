<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  segmentsApi,
  segmentsApiError,
  fieldDefFor,
  isCustomField,
  customFieldKey,
  CUSTOM_FIELD_OPTION,
  CUSTOM_FIELD_PREFIX,
  SEGMENT_FIELDS,
  NO_VALUE_OPERATORS,
} from '../../api/segments'
import type { SegmentCondition, SegmentInput, SegmentPreview, SegmentRule, SegmentRuleGroup, SegmentRow } from '../../api/types'
import { useModalSafeClose } from '../../composables/useModalSafeClose'

/**
 * Right-hand drawer over a segment: name/description plus the grouped
 * condition builder and a debounced live preview of the matching subscribers.
 * `segment` is null in create mode.
 */
const props = defineProps<{
  segment: SegmentRow | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', message: string): void
}>()

// --- Form state -------------------------------------------------------------

function emptyRule(): SegmentRule {
  return { field: 'email', operator: 'contains', value: '' }
}

function emptyGroup(): SegmentRuleGroup {
  return { logic: 'and', rules: [emptyRule()] }
}

function cloneCondition(source: SegmentCondition): SegmentCondition {
  return {
    logic: source.logic === 'or' ? 'or' : 'and',
    groups: (source.groups ?? []).map((group) => ({
      logic: group.logic === 'or' ? 'or' : 'and',
      rules: (group.rules ?? []).map((rule) => ({
        field: rule.field,
        operator: rule.operator,
        value: rule.value ?? '',
      })),
    })),
  }
}

const name = ref(props.segment?.name ?? '')
const description = ref(props.segment?.description ?? '')
const condition = ref<SegmentCondition>(
  props.segment ? cloneCondition(props.segment.condition) : { logic: 'and', groups: [emptyGroup()] },
)

const saving = ref(false)
const clientError = ref('')
const errorBanner = ref<{ message: string; validationErrors: string[] } | null>(null)

const banner = computed(() =>
  clientError.value ? { message: clientError.value, validationErrors: [] } : errorBanner.value,
)

const { watchClickStart, confirmClickEnd } = useModalSafeClose(() => {
  if (!saving.value) emit('close')
})

// --- Condition mutations ----------------------------------------------------

/** Every mutation clones first, so the deep watcher below reliably fires. */
function updateCondition(mutate: (draft: SegmentCondition) => void) {
  const draft = cloneCondition(condition.value)
  mutate(draft)
  condition.value = draft
}

function patchRule(groupIndex: number, ruleIndex: number, patch: Partial<SegmentRule>) {
  updateCondition((draft) => {
    const rule = draft.groups[groupIndex]?.rules[ruleIndex]
    if (rule) Object.assign(rule, patch)
  })
}

function addRule(groupIndex: number) {
  updateCondition((draft) => {
    draft.groups[groupIndex]?.rules.push(emptyRule())
  })
}

/** Removing a group's last rule removes the group; the last group never dies. */
function removeRule(groupIndex: number, ruleIndex: number) {
  updateCondition((draft) => {
    const group = draft.groups[groupIndex]
    if (!group) return
    group.rules.splice(ruleIndex, 1)
    if (group.rules.length === 0) {
      draft.groups.splice(groupIndex, 1)
      if (draft.groups.length === 0) draft.groups.push(emptyGroup())
    }
  })
}

function addGroup() {
  updateCondition((draft) => {
    draft.groups.push(emptyGroup())
  })
}

function setConditionLogic(logic: SegmentCondition['logic']) {
  updateCondition((draft) => {
    draft.logic = logic
  })
}

function setGroupLogic(groupIndex: number, logic: SegmentRuleGroup['logic']) {
  updateCondition((draft) => {
    const group = draft.groups[groupIndex]
    if (group) group.logic = logic
  })
}

function removeGroup(groupIndex: number) {
  updateCondition((draft) => {
    draft.groups.splice(groupIndex, 1)
    if (draft.groups.length === 0) draft.groups.push(emptyGroup())
  })
}

/** Picking a field resets the operator when the new field doesn't support it. */
function onRuleField(groupIndex: number, ruleIndex: number, selected: string) {
  if (selected === CUSTOM_FIELD_OPTION) {
    patchRule(groupIndex, ruleIndex, { field: CUSTOM_FIELD_PREFIX, operator: 'equals' })
    return
  }
  const def = fieldDefFor(selected)
  const current = condition.value.groups[groupIndex]?.rules[ruleIndex]
  const patch: Partial<SegmentRule> = { field: selected }
  if (!def.operators.includes(current?.operator ?? '')) {
    patch.operator = def.defaultOperator
  }
  if (def.type === 'boolean' && !['true', 'false'].includes(current?.value ?? '')) {
    patch.value = 'true'
  }
  patchRule(groupIndex, ruleIndex, patch)
}

function onCustomKey(groupIndex: number, ruleIndex: number, keyValue: string) {
  patchRule(groupIndex, ruleIndex, { field: `${CUSTOM_FIELD_PREFIX}${keyValue.trim()}` })
}

// --- Field / operator catalogs ---------------------------------------------

const SUBSCRIBER_FIELDS = SEGMENT_FIELDS.filter((f) => f.group === 'subscriber')
const ENGAGEMENT_FIELDS = SEGMENT_FIELDS.filter((f) => f.group === 'engagement')

const OPERATOR_LABELS: Record<string, string> = {
  equals: 'equals',
  not_equals: 'not equals',
  contains: 'contains',
  not_contains: 'does not contain',
  starts_with: 'starts with',
  ends_with: 'ends with',
  is_empty: 'is empty',
  is_not_empty: 'is not empty',
  one_of: 'is one of',
  not_one_of: 'is not one of',
  greater_than: 'is greater than',
  less_than: 'is less than',
  exists: 'exists',
  not_exists: 'does not exist',
}

function selectValueFor(field: string): string {
  return isCustomField(field) ? CUSTOM_FIELD_OPTION : field
}

function operatorLabelFor(field: string, operator: string): string {
  const type = fieldDefFor(field).type
  if (operator === 'greater_than') {
    if (type === 'date') return 'is after'
    if (type === 'number') return 'is more than'
  }
  if (operator === 'less_than') {
    if (type === 'date') return 'is before'
    if (type === 'number') return 'is less than'
  }
  return OPERATOR_LABELS[operator] ?? operator
}

function operatorOptionsFor(field: string): { value: string; label: string }[] {
  return fieldDefFor(field).operators.map((op) => ({ value: op, label: operatorLabelFor(field, op) }))
}

type ValueMode = 'none' | 'text' | 'multi' | 'number' | 'date' | 'boolean'

function valueModeFor(rule: SegmentRule): ValueMode {
  if (NO_VALUE_OPERATORS.includes(rule.operator)) return 'none'
  const type = fieldDefFor(rule.field).type
  if (type === 'date') return 'date'
  if (type === 'number') return 'number'
  if (type === 'boolean') return 'boolean'
  return rule.operator === 'one_of' || rule.operator === 'not_one_of' ? 'multi' : 'text'
}

// --- Live preview (debounced, sequence-guarded) ------------------------------

const PREVIEW_SIZE = 10
const PREVIEW_DEBOUNCE_MS = 400

const preview = ref<SegmentPreview | null>(null)
const previewLoading = ref(false)
const previewError = ref('')
const previewPage = ref(0)

// Monotonic counter: only the response of the newest request may render — a
// slower earlier reply that lands late is dropped instead of overwriting.
let previewSeq = 0
let previewTimer: ReturnType<typeof setTimeout> | null = null

function schedulePreview(page = 0) {
  if (previewTimer) clearTimeout(previewTimer)
  previewTimer = setTimeout(() => runPreview(page), PREVIEW_DEBOUNCE_MS)
}

async function runPreview(page: number) {
  const mySeq = ++previewSeq
  previewLoading.value = true
  previewError.value = ''
  try {
    const res = await segmentsApi.preview({ condition: condition.value, page, size: PREVIEW_SIZE })
    if (mySeq !== previewSeq) return
    preview.value = res.data.data
    previewPage.value = page
  } catch (e) {
    if (mySeq !== previewSeq) return
    previewError.value = segmentsApiError(e, 'Preview failed').message
  } finally {
    if (mySeq === previewSeq) previewLoading.value = false
  }
}

watch(condition, () => schedulePreview(0), { deep: true })

schedulePreview(0)

onBeforeUnmount(() => {
  if (previewTimer) clearTimeout(previewTimer)
})

// --- Save --------------------------------------------------------------------

function validate(): string {
  if (!name.value.trim()) return 'Name is required'
  if (condition.value.groups.length === 0) return 'Add at least one condition group'
  if (!condition.value.groups.some((g) => (g.rules ?? []).length > 0)) return 'Add at least one condition'
  return ''
}

async function save() {
  if (saving.value) return
  clientError.value = ''
  errorBanner.value = null
  const invalid = validate()
  if (invalid) {
    clientError.value = invalid
    return
  }
  saving.value = true
  try {
    const payload: SegmentInput = {
      name: name.value.trim(),
      description: description.value.trim() || undefined,
      condition: condition.value,
    }
    if (props.segment) {
      await segmentsApi.update(props.segment.id, payload)
      emit('saved', 'Segment updated')
    } else {
      await segmentsApi.create(payload)
      emit('saved', 'Segment created')
    }
  } catch (e) {
    errorBanner.value = segmentsApiError(e, props.segment ? 'Failed to update segment' : 'Failed to create segment')
  } finally {
    saving.value = false
  }
}

// --- Preview sample helpers ---------------------------------------------------

function statusBadgeClass(status: string): string {
  switch (status) {
    case 'subscribed': return 'badge badge-success'
    case 'unsubscribed': return 'badge badge-warning'
    case 'bounced':
    case 'complained': return 'badge badge-danger'
    default: return 'badge badge-neutral'
  }
}

function formatCount(count: number): string {
  return count.toLocaleString()
}
</script>

<template>
  <Teleport to="body">
    <div class="sdrawer-overlay" @mousedown="watchClickStart" @mouseup="confirmClickEnd">
      <div class="sdrawer" role="dialog" aria-modal="true" aria-label="Segment editor">
        <header class="sdrawer__header">
          <div class="sdrawer__heading">
            <h2>{{ segment ? 'Edit Segment' : 'New Segment' }}</h2>
            <span v-if="segment" class="sdrawer__sub">{{ segment.name }}</span>
          </div>
          <button class="sdrawer__close" aria-label="Close" @click="emit('close')">
            <span class="mdi mdi-close"></span>
          </button>
        </header>

        <div class="sdrawer__body">
          <div v-if="banner" class="banner" role="alert">
            <div class="banner-title">
              <span class="mdi mdi-alert-circle-outline"></span>
              {{ banner.message }}
            </div>
            <ul v-if="banner.validationErrors.length" class="banner-list">
              <li v-for="(err, i) in banner.validationErrors" :key="i">{{ err }}</li>
            </ul>
            <button class="banner-close" aria-label="Dismiss" @click="clientError = ''; errorBanner = null">
              <span class="mdi mdi-close"></span>
            </button>
          </div>

          <div class="sdrawer-field">
            <label class="form-label" for="seg-name">Name <span class="sdrawer-req">*</span></label>
            <input id="seg-name" v-model="name" class="form-input" placeholder="e.g. Engaged openers" />
          </div>

          <div class="sdrawer-field">
            <label class="form-label" for="seg-description">Description</label>
            <input id="seg-description" v-model="description" class="form-input" placeholder="What this segment is for" />
          </div>

          <!-- Condition builder -->
          <div class="sdrawer-field">
            <div class="sdrawer-logic">
              <label class="form-label">Conditions</label>
              <div class="sdrawer-toggle" role="group" aria-label="How groups are combined">
                <button
                  type="button"
                  class="sdrawer-toggle__item"
                  :class="{ 'sdrawer-toggle__item--active': condition.logic === 'and' }"
                  @click="setConditionLogic('and')"
                >
                  Match all groups
                </button>
                <button
                  type="button"
                  class="sdrawer-toggle__item"
                  :class="{ 'sdrawer-toggle__item--active': condition.logic === 'or' }"
                  @click="setConditionLogic('or')"
                >
                  Match any group
                </button>
              </div>
            </div>

            <template v-for="(group, gi) in condition.groups" :key="gi">
              <div v-if="gi > 0" class="sdrawer-joiner" aria-hidden="true">
                <span>{{ condition.logic === 'or' ? 'OR' : 'AND' }}</span>
              </div>

              <div class="sdrawer-group">
                <div class="sdrawer-group__head">
                  <div class="sdrawer-toggle sdrawer-toggle--sm" role="group" :aria-label="`How rules in group ${gi + 1} are combined`">
                    <button
                      type="button"
                      class="sdrawer-toggle__item"
                      :class="{ 'sdrawer-toggle__item--active': group.logic === 'and' }"
                      @click="setGroupLogic(gi, 'and')"
                    >
                      Match all
                    </button>
                    <button
                      type="button"
                      class="sdrawer-toggle__item"
                      :class="{ 'sdrawer-toggle__item--active': group.logic === 'or' }"
                      @click="setGroupLogic(gi, 'or')"
                    >
                      Match any
                    </button>
                  </div>
                  <span class="sdrawer-group__label">Group {{ gi + 1 }}</span>
                  <button
                    v-if="condition.groups.length > 1"
                    class="sdrawer-group__remove"
                    aria-label="Remove group"
                    @click="removeGroup(gi)"
                  >
                    <span class="mdi mdi-trash-can-outline"></span>
                  </button>
                </div>

                <div v-for="(rule, ri) in group.rules" :key="ri" class="sdrawer-cond">
                  <div class="sdrawer-cond__row">
                    <select
                      class="form-select"
                      :value="selectValueFor(rule.field)"
                      @change="onRuleField(gi, ri, ($event.target as HTMLSelectElement).value)"
                    >
                      <optgroup label="Subscriber fields">
                        <option v-for="f in SUBSCRIBER_FIELDS" :key="f.field" :value="f.field">{{ f.label }}</option>
                      </optgroup>
                      <optgroup label="Engagement">
                        <option v-for="f in ENGAGEMENT_FIELDS" :key="f.field" :value="f.field">{{ f.label }}</option>
                      </optgroup>
                      <optgroup label="Custom">
                        <option :value="CUSTOM_FIELD_OPTION">Custom field…</option>
                      </optgroup>
                    </select>
                    <button class="sdrawer-cond__remove" aria-label="Remove condition" @click="removeRule(gi, ri)">
                      <span class="mdi mdi-close"></span>
                    </button>
                  </div>

                  <input
                    v-if="isCustomField(rule.field)"
                    class="form-input"
                    placeholder="custom_fields. key"
                    :value="customFieldKey(rule.field)"
                    @input="onCustomKey(gi, ri, ($event.target as HTMLInputElement).value)"
                  />

                  <div class="sdrawer-cond__row">
                    <select
                      class="form-select"
                      :value="rule.operator"
                      @change="patchRule(gi, ri, { operator: ($event.target as HTMLSelectElement).value })"
                    >
                      <option v-for="o in operatorOptionsFor(rule.field)" :key="o.value" :value="o.value">{{ o.label }}</option>
                    </select>
                    <select
                      v-if="valueModeFor(rule) === 'boolean'"
                      class="form-select"
                      :value="rule.value || 'true'"
                      @change="patchRule(gi, ri, { value: ($event.target as HTMLSelectElement).value })"
                    >
                      <option value="true">true</option>
                      <option value="false">false</option>
                    </select>
                    <input
                      v-else-if="valueModeFor(rule) === 'number'"
                      class="form-input"
                      type="number"
                      placeholder="0"
                      :value="rule.value"
                      @input="patchRule(gi, ri, { value: ($event.target as HTMLInputElement).value })"
                    />
                    <input
                      v-else-if="valueModeFor(rule) === 'date'"
                      class="form-input"
                      type="date"
                      :value="rule.value"
                      @input="patchRule(gi, ri, { value: ($event.target as HTMLInputElement).value })"
                    />
                    <input
                      v-else-if="valueModeFor(rule) === 'text' || valueModeFor(rule) === 'multi'"
                      class="form-input"
                      type="text"
                      :placeholder="valueModeFor(rule) === 'multi' ? 'value1, value2' : 'Value'"
                      :value="rule.value"
                      @input="patchRule(gi, ri, { value: ($event.target as HTMLInputElement).value })"
                    />
                  </div>
                </div>

                <button class="sdrawer-add" type="button" @click="addRule(gi)">
                  <span class="mdi mdi-plus"></span> Add condition
                </button>
              </div>
            </template>

            <button class="sdrawer-add sdrawer-add--group" type="button" @click="addGroup()">
              <span class="mdi mdi-plus"></span> Add group
            </button>
          </div>

          <!-- Live preview -->
          <section class="sdrawer-preview">
            <header class="sdrawer-preview__head">
              <h3>Live preview</h3>
              <span v-if="preview && !previewError" class="sdrawer-preview__count">
                {{ formatCount(preview.count) }} matching subscribers
              </span>
              <span v-else-if="previewLoading && !preview" class="sdrawer-preview__count">Counting…</span>
            </header>

            <div v-if="previewError" class="sdrawer-preview__error">
              <span>{{ previewError }}</span>
              <button class="btn btn-secondary btn-sm" @click="runPreview(previewPage)">Retry</button>
            </div>

            <div v-else-if="previewLoading && !preview" class="sdrawer-preview__skeleton" aria-hidden="true">
              <div v-for="n in 3" :key="n" class="skeleton skeleton-text"></div>
            </div>

            <template v-else-if="preview">
              <div v-if="preview.count === 0" class="sdrawer-preview__empty">
                No subscribers match these conditions
              </div>

              <template v-else>
                <table class="sdrawer-preview__table" :class="{ 'is-refreshing': previewLoading }">
                  <thead>
                    <tr>
                      <th>Email</th>
                      <th>Name</th>
                      <th>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="contact in preview.contacts" :key="contact.id">
                      <td class="sdrawer-preview__mono">{{ contact.email }}</td>
                      <td>{{ contact.name || '—' }}</td>
                      <td><span :class="statusBadgeClass(contact.status)">{{ contact.status }}</span></td>
                    </tr>
                  </tbody>
                </table>

                <div v-if="preview.total_pages > 1" class="sdrawer-preview__paging">
                  <button class="btn btn-secondary btn-sm" :disabled="previewPage === 0" @click="runPreview(previewPage - 1)">
                    Previous
                  </button>
                  <span>Page {{ previewPage + 1 }} of {{ preview.total_pages }}</span>
                  <button class="btn btn-secondary btn-sm" :disabled="previewPage >= preview.total_pages - 1" @click="runPreview(previewPage + 1)">
                    Next
                  </button>
                </div>
              </template>
            </template>
          </section>
        </div>

        <footer class="sdrawer__footer">
          <button class="btn btn-secondary" :disabled="saving" @click="emit('close')">Cancel</button>
          <button class="btn btn-primary" :disabled="saving" @click="save">
            {{ saving ? 'Saving…' : 'Save Segment' }}
          </button>
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.sdrawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 60;
  background: rgba(0, 0, 0, 0.45);
}

.sdrawer {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: min(560px, 100vw);
  display: flex;
  flex-direction: column;
  background: var(--bg-primary);
  border-left: 1px solid var(--border-primary);
  box-shadow: var(--shadow-lg);
}

.sdrawer__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border-primary);
}

.sdrawer__heading {
  min-width: 0;
}

.sdrawer__heading h2 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}

.sdrawer__sub {
  display: block;
  font-size: 12.5px;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sdrawer__close {
  display: inline-flex;
  padding: 6px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

.sdrawer__close:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.sdrawer__body {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.sdrawer__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--border-primary);
}

.sdrawer-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.sdrawer-req {
  color: var(--danger-text);
}

/* AND / OR segmented toggles */
.sdrawer-logic {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}

.sdrawer-logic .form-label {
  margin: 0;
}

.sdrawer-toggle {
  display: inline-flex;
  padding: 2px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-tertiary);
  gap: 2px;
}

.sdrawer-toggle__item {
  padding: 4px 10px;
  border: none;
  border-radius: calc(var(--radius-sm) - 2px);
  background: transparent;
  color: var(--text-secondary);
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
  white-space: nowrap;
  transition: background var(--transition), color var(--transition);
}

.sdrawer-toggle__item--active {
  background: var(--bg-primary);
  color: var(--text-primary);
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}

.sdrawer-toggle--sm .sdrawer-toggle__item {
  padding: 3px 8px;
  font-size: 11.5px;
}

/* Group cards */
.sdrawer-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-secondary);
}

.sdrawer-group__head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sdrawer-group__label {
  font-size: 11.5px;
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-muted);
  margin-right: auto;
}

.sdrawer-group__remove {
  display: inline-flex;
  padding: 5px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

.sdrawer-group__remove:hover {
  background: var(--danger-50);
  color: var(--danger-text);
}

.sdrawer-joiner {
  display: flex;
  align-items: center;
  gap: 10px;
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
}

.sdrawer-joiner::before,
.sdrawer-joiner::after {
  content: '';
  flex: 1;
  height: 1px;
  background: var(--border-primary);
}

/* Condition rows */
.sdrawer-cond {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-primary);
}

.sdrawer-cond__row {
  display: flex;
  gap: 6px;
  align-items: center;
}

.sdrawer-cond__row > .form-input,
.sdrawer-cond__row > .form-select {
  flex: 1;
  min-width: 0;
}

.sdrawer-cond__remove {
  display: inline-flex;
  padding: 6px;
  flex: none;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

.sdrawer-cond__remove:hover {
  background: var(--danger-50);
  color: var(--danger-text);
}

.sdrawer-add {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 7px 10px;
  border: 1px dashed var(--border-input);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
}

.sdrawer-add:hover {
  border-color: var(--primary-500);
  color: var(--primary-600);
}

.sdrawer-add--group {
  align-self: flex-start;
}

/* Live preview */
.sdrawer-preview {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  background: var(--bg-secondary);
}

.sdrawer-preview__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}

.sdrawer-preview__head h3 {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-tertiary);
}

.sdrawer-preview__count {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  font-variant-numeric: tabular-nums;
}

.sdrawer-preview__table {
  width: 100%;
  border-collapse: collapse;
  transition: opacity var(--transition);
}

.sdrawer-preview__table.is-refreshing {
  opacity: 0.55;
}

.sdrawer-preview__table th {
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--text-muted);
  padding: 4px 8px;
  border-bottom: 1px solid var(--border-primary);
}

.sdrawer-preview__table td {
  padding: 6px 8px;
  font-size: 13px;
  color: var(--text-secondary);
  border-bottom: 1px solid var(--border-secondary);
}

.sdrawer-preview__mono {
  font-family: var(--font-mono);
  font-size: 12.5px;
}

.sdrawer-preview__paging {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 12px;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

.sdrawer-preview__empty {
  padding: 18px 8px;
  text-align: center;
  font-size: 13px;
  color: var(--text-muted);
}

.sdrawer-preview__error {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--danger-500);
  border-radius: var(--radius-sm);
  background: var(--danger-50);
  color: var(--danger-text);
  font-size: 13px;
}

.sdrawer-preview__skeleton {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 4px 0;
}

/* Error banner */
.banner {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px 40px 12px 14px;
  border: 1px solid var(--danger-500);
  border-radius: var(--radius);
  background: var(--danger-50);
}

.banner-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13.5px;
  font-weight: 600;
  color: var(--danger-text);
}

.banner-list {
  margin: 0;
  padding-left: 30px;
  font-size: 12.5px;
  color: var(--danger-text);
  line-height: 1.7;
}

.banner-close {
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

@media (max-width: 520px) {
  .sdrawer {
    width: 100vw;
    border-left: none;
  }
}
</style>
