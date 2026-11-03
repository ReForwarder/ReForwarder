<script setup lang="ts">
import { computed } from 'vue'
import type { GraphNode } from '@vue-flow/core'
import SearchableSelect from '../../../components/SearchableSelect.vue'
import { NODE_META, nodeMeta, type BuilderNodeType, type SelectOption } from '../../../composables/useAutomationBuilder'
import type { AutomationABVariant, AutomationFilterRule } from '../../../api/types'

const props = defineProps<{
  node: GraphNode
  lists: SelectOption[]
  templates: SelectOption[]
  templatesLoading?: boolean
  /** Nodes the selected node may point at (itself and the trigger excluded). */
  eligibleTargets: SelectOption[]
}>()

const emit = defineEmits<{
  (e: 'patch', patch: Record<string, any>): void
  (e: 'close'): void
  (e: 'delete'): void
}>()

const EMPTY_TARGET_OPTION: SelectOption = { value: '', label: '— journey completes' }

const nodeType = computed<BuilderNodeType>(() => (props.node.data?.nodeType as BuilderNodeType) ?? 'delay')
const meta = computed(() => nodeMeta(nodeType.value))
const nodeId = computed(() => props.node.id)

const config = computed<Record<string, any>>(() => (props.node.data?.config as Record<string, any>) ?? {})

function set(key: string, value: any) {
  emit('patch', { [key]: value })
}

function num(value: any): string {
  return value === 0 || value === undefined || value === null ? '' : String(value)
}

// --- Trigger ---------------------------------------------------------------

const EVENT_KIND_OPTIONS: SelectOption[] = [
  { value: 'list.subscribed', label: 'Contact joins a list' },
  { value: 'contact.created', label: 'Contact created' },
  { value: 'contact.unsubscribed', label: 'Contact unsubscribed' },
  { value: 'email.opened', label: 'Email opened' },
  { value: 'email.clicked', label: 'Email clicked' },
  { value: 'email.bounced', label: 'Email bounced' },
  { value: 'custom', label: 'Custom event' },
]

const triggerEventKind = computed({
  get: () => String(config.value.event_kind ?? ''),
  set: (v: string) => set('event_kind', v),
})

const triggerListId = computed({
  get: () => (config.value.list_id ? String(config.value.list_id) : ''),
  set: (v: string) => set('list_id', v ? Number(v) : null),
})

const triggerFrequency = computed(() => String(config.value.frequency ?? 'once'))

// --- Conditions (trigger + filter; AND-combined) ---------------------------

const DIRECT_FIELDS: SelectOption[] = [
  { value: 'email', label: 'Email' },
  { value: 'name', label: 'Name' },
  { value: 'status', label: 'Status' },
  { value: '__custom', label: 'Custom field…' },
]

const DIRECT_OPERATORS: SelectOption[] = [
  { value: 'equals', label: 'equals' },
  { value: 'not_equals', label: 'not equals' },
  { value: 'contains', label: 'contains' },
]

const CUSTOM_OPERATORS: SelectOption[] = [
  ...DIRECT_OPERATORS,
  { value: 'exists', label: 'exists' },
  { value: 'not_exists', label: 'does not exist' },
]

const CUSTOM_PREFIX = 'custom_fields.'

function isCustomField(field: string): boolean {
  return typeof field === 'string' && field.startsWith(CUSTOM_PREFIX)
}

function customKey(field: string): string {
  return isCustomField(field) ? field.slice(CUSTOM_PREFIX.length) : ''
}

function conditionsOf(key = 'conditions'): AutomationFilterRule[] {
  return Array.isArray(config.value[key]) ? (config.value[key] as AutomationFilterRule[]) : []
}

function patchConditions(next: AutomationFilterRule[], key = 'conditions') {
  emit('patch', { [key]: next })
}

function addCondition(key = 'conditions') {
  patchConditions([...conditionsOf(key), { field: 'email', operator: 'equals', value: '' }], key)
}

function removeCondition(index: number, key = 'conditions') {
  patchConditions(conditionsOf(key).filter((_, i) => i !== index), key)
}

function patchCondition(index: number, patch: Partial<AutomationFilterRule>, key = 'conditions') {
  patchConditions(
    conditionsOf(key).map((rule, i) => (i === index ? { ...rule, ...patch } : rule)),
    key,
  )
}

/** Switching the field resets the operator when the new field type differs. */
function onConditionField(index: number, field: string, key = 'conditions') {
  const rule = conditionsOf(key)[index]
  if (field === '__custom') {
    patchCondition(index, { field: `${CUSTOM_PREFIX}`, operator: 'equals' }, key)
    return
  }
  const wasCustom = isCustomField(rule?.field ?? '')
  if (wasCustom && rule?.operator && !['exists', 'not_exists'].includes(rule.operator)) {
    patchCondition(index, { field, operator: rule.operator }, key)
  } else {
    patchCondition(index, { field, operator: field === 'status' ? 'equals' : rule?.operator ?? 'equals' }, key)
  }
}

function onCustomKeyInput(index: number, keyValue: string, key = 'conditions') {
  patchCondition(index, { field: `${CUSTOM_PREFIX}${keyValue.trim()}` }, key)
}

function conditionValueMode(rule: AutomationFilterRule): 'none' | 'text' | 'number' {
  if (rule.operator === 'exists' || rule.operator === 'not_exists') return 'none'
  if (isCustomField(rule.field)) return 'text'
  return 'text'
}

// --- Delay -----------------------------------------------------------------

const DELAY_UNITS: SelectOption[] = [
  { value: 'minutes', label: 'Minutes' },
  { value: 'hours', label: 'Hours' },
  { value: 'days', label: 'Days' },
  { value: 'weeks', label: 'Weeks' },
]

// --- A/B test ---------------------------------------------------------------

const abVariants = computed<AutomationABVariant[]>(() => (Array.isArray(config.value.variants) ? (config.value.variants as AutomationABVariant[]) : []))

const abWeightSum = computed(() => abVariants.value.reduce((sum, v) => sum + (Number(v.weight) || 0), 0))

let variantCounter = 0

function addVariant() {
  const variants = [...abVariants.value]
  variantCounter += 1
  variants.push({ id: `v${Date.now().toString(36)}${variantCounter}`, name: `Variant ${String.fromCharCode(65 + variants.length)}`, weight: 0, next_node_id: '' })
  set('variants', variants)
}

function patchVariant(index: number, patch: Partial<AutomationABVariant>) {
  set('variants', abVariants.value.map((v, i) => (i === index ? { ...v, ...patch } : v)))
}

function removeVariant(index: number) {
  set('variants', abVariants.value.filter((_, i) => i !== index))
}

// --- Webhook ----------------------------------------------------------------

const webhookHeaders = computed<[string, string][]>(() => {
  const headers = config.value.headers
  if (!headers || typeof headers !== 'object') return []
  return Object.entries(headers as Record<string, string>)
})

let headerCounter = 0

function addHeader() {
  headerCounter += 1
  const headers = { ...(config.value.headers ?? {}) }
  headers[`Header-${headerCounter}`] = ''
  set('headers', headers)
}

function patchHeader(oldKey: string, newKey: string, value: string) {
  const current = (config.value.headers ?? {}) as Record<string, string>
  const headers: Record<string, string> = {}
  for (const [k, v] of Object.entries(current)) {
    headers[k === oldKey ? newKey : k] = k === oldKey ? value : String(v)
  }
  set('headers', headers)
}

function removeHeader(key: string) {
  const headers = { ...(config.value.headers ?? {}) }
  delete headers[key]
  set('headers', headers)
}

// --- Targets ----------------------------------------------------------------

const filterContinue = computed({
  get: () => String(config.value.continue_node_id ?? ''),
  set: (v: string) => set('continue_node_id', v),
})
const filterExit = computed({
  get: () => String(config.value.exit_node_id ?? ''),
  set: (v: string) => set('exit_node_id', v),
})

const listStatusTargets = computed(() => [
  { key: 'active_node_id', label: 'Active in list' },
  { key: 'non_active_node_id', label: 'In list, not active' },
  { key: 'not_in_list_node_id', label: 'Not in list' },
])

const configListId = computed({
  get: () => (config.value.list_id ? String(config.value.list_id) : ''),
  set: (v: string) => set('list_id', v ? Number(v) : 0),
})

const emailTemplateId = computed({
  get: () => (config.value.template_id ? String(config.value.template_id) : ''),
  set: (v: string) => set('template_id', v ? Number(v) : 0),
})

function variantTarget(variant: AutomationABVariant): string {
  return variant.next_node_id ?? ''
}

function targetFor(key: string): string {
  return String(config.value[key] ?? '')
}
</script>

<template>
  <aside class="cfg">
    <header class="cfg__header">
      <span class="cfg__icon" :class="`cfg__icon--${meta.color}`">
        <span class="mdi" :class="meta.icon"></span>
      </span>
      <div class="cfg__heading">
        <h2>{{ meta.label }}</h2>
        <span class="cfg__node-id">{{ nodeId }}</span>
      </div>
      <button class="cfg__close" aria-label="Close panel" @click="emit('close')">
        <span class="mdi mdi-close"></span>
      </button>
    </header>

    <div class="cfg__body">
      <!-- Trigger -->
      <template v-if="nodeType === 'trigger'">
        <div class="cfg-field">
          <label class="form-label">Description</label>
          <input
            class="form-input"
            :value="config.description ?? ''"
            placeholder="What starts this workflow?"
            @input="set('description', ($event.target as HTMLInputElement).value)"
          />
        </div>
        <div class="cfg-field">
          <label class="form-label">When</label>
          <select v-model="triggerEventKind" class="form-select">
            <option v-for="o in EVENT_KIND_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
        </div>
        <div v-if="triggerEventKind === 'list.subscribed'" class="cfg-field">
          <label class="form-label">List</label>
          <SearchableSelect
            :options="lists"
            :model-value="triggerListId"
            placeholder="Select the automation list…"
            empty-text="No lists yet"
            @update:model-value="triggerListId = $event"
          />
          <p class="form-hint">Falls back to the automation list when empty.</p>
        </div>
        <div v-if="triggerEventKind === 'custom'" class="cfg-field">
          <label class="form-label">Event name</label>
          <input
            class="form-input"
            :value="config.custom_event_name ?? ''"
            placeholder="e.g. order_completed"
            @input="set('custom_event_name', ($event.target as HTMLInputElement).value)"
          />
          <p class="form-hint">Matches POST /automations/events payloads with the same name.</p>
        </div>
        <div class="cfg-field">
          <label class="form-label">Run</label>
          <div class="cfg-radio-group">
            <label class="cfg-radio">
              <input type="radio" value="once" :checked="triggerFrequency === 'once'" @change="set('frequency', 'once')" />
              <span>Once per contact</span>
            </label>
            <label class="cfg-radio">
              <input type="radio" value="every_time" :checked="triggerFrequency === 'every_time'" @change="set('frequency', 'every_time')" />
              <span>Every time</span>
            </label>
          </div>
        </div>
        <div class="cfg-field">
          <label class="form-label">Conditions <span class="cfg-field__and">all must match</span></label>
          <div class="cfg-conds">
            <div v-for="(rule, i) in conditionsOf()" :key="i" class="cfg-cond">
              <div class="cfg-cond__row">
                <select
                  class="form-select"
                  :value="isCustomField(rule.field) ? '__custom' : rule.field"
                  @change="onConditionField(i, ($event.target as HTMLSelectElement).value)"
                >
                  <option v-for="o in DIRECT_FIELDS" :key="o.value" :value="o.value">{{ o.label }}</option>
                </select>
                <button class="cfg-cond__remove" aria-label="Remove condition" @click="removeCondition(i)">
                  <span class="mdi mdi-close"></span>
                </button>
              </div>
              <input
                v-if="isCustomField(rule.field)"
                class="form-input"
                placeholder="custom_fields. key"
                :value="customKey(rule.field)"
                @input="onCustomKeyInput(i, ($event.target as HTMLInputElement).value)"
              />
              <div class="cfg-cond__row">
                <select class="form-select" :value="rule.operator" @change="patchCondition(i, { operator: ($event.target as HTMLSelectElement).value })">
                  <option v-for="o in (isCustomField(rule.field) ? CUSTOM_OPERATORS : DIRECT_OPERATORS)" :key="o.value" :value="o.value">{{ o.label }}</option>
                </select>
                <input
                  v-if="conditionValueMode(rule) === 'text'"
                  class="form-input"
                  placeholder="Value"
                  :value="rule.value ?? ''"
                  @input="patchCondition(i, { value: ($event.target as HTMLInputElement).value })"
                />
              </div>
            </div>
            <button class="cfg-add" type="button" @click="addCondition()">
              <span class="mdi mdi-plus"></span> Add condition
            </button>
          </div>
        </div>
      </template>

      <!-- Delay -->
      <template v-else-if="nodeType === 'delay'">
        <div class="cfg-field">
          <label class="form-label">Wait for</label>
          <div class="cfg-cond__row">
            <input
              class="form-input"
              type="number"
              min="1"
              :value="num(config.duration)"
              placeholder="1"
              @input="set('duration', Number(($event.target as HTMLInputElement).value) || 0)"
            />
            <select class="form-select" :value="config.unit ?? 'hours'" @change="set('unit', ($event.target as HTMLSelectElement).value)">
              <option v-for="u in DELAY_UNITS" :key="u.value" :value="u.value">{{ u.label }}</option>
            </select>
          </div>
        </div>
        <div class="cfg-field">
          <label class="form-label">Description</label>
          <input
            class="form-input"
            :value="config.description ?? ''"
            placeholder="Optional note"
            @input="set('description', ($event.target as HTMLInputElement).value)"
          />
        </div>
      </template>

      <!-- Email -->
      <template v-else-if="nodeType === 'email'">
        <div class="cfg-field">
          <label class="form-label">Template</label>
          <SearchableSelect
            :options="templates"
            :model-value="emailTemplateId"
            :search-placeholder="templatesLoading ? 'Loading templates…' : 'Search templates…'"
            placeholder="Select a template…"
            empty-text="No templates found"
            @update:model-value="emailTemplateId = $event"
          />
        </div>
        <div class="cfg-field">
          <label class="form-label">Subject override</label>
          <input
            class="form-input"
            :value="config.subject_override ?? ''"
            placeholder="Uses the template subject"
            @input="set('subject_override', ($event.target as HTMLInputElement).value)"
          />
        </div>
        <div class="cfg-field">
          <label class="form-label">From override</label>
          <input
            class="form-input"
            :value="config.from_override ?? ''"
            placeholder="Uses the workspace default"
            @input="set('from_override', ($event.target as HTMLInputElement).value)"
          />
        </div>
        <p class="cfg-note">
          <span class="mdi mdi-information-outline"></span>
          Email nodes require the automation list.
        </p>
      </template>

      <!-- Filter -->
      <template v-else-if="nodeType === 'filter'">
        <div class="cfg-field">
          <label class="form-label">Conditions <span class="cfg-field__and">all must match</span></label>
          <div class="cfg-conds">
            <div v-for="(rule, i) in conditionsOf()" :key="i" class="cfg-cond">
              <div class="cfg-cond__row">
                <select
                  class="form-select"
                  :value="isCustomField(rule.field) ? '__custom' : rule.field"
                  @change="onConditionField(i, ($event.target as HTMLSelectElement).value)"
                >
                  <option v-for="o in DIRECT_FIELDS" :key="o.value" :value="o.value">{{ o.label }}</option>
                </select>
                <button class="cfg-cond__remove" aria-label="Remove condition" @click="removeCondition(i)">
                  <span class="mdi mdi-close"></span>
                </button>
              </div>
              <input
                v-if="isCustomField(rule.field)"
                class="form-input"
                placeholder="custom_fields. key"
                :value="customKey(rule.field)"
                @input="onCustomKeyInput(i, ($event.target as HTMLInputElement).value)"
              />
              <div class="cfg-cond__row">
                <select class="form-select" :value="rule.operator" @change="patchCondition(i, { operator: ($event.target as HTMLSelectElement).value })">
                  <option v-for="o in (isCustomField(rule.field) ? CUSTOM_OPERATORS : DIRECT_OPERATORS)" :key="o.value" :value="o.value">{{ o.label }}</option>
                </select>
                <input
                  v-if="conditionValueMode(rule) === 'text'"
                  class="form-input"
                  placeholder="Value"
                  :value="rule.value ?? ''"
                  @input="patchCondition(i, { value: ($event.target as HTMLInputElement).value })"
                />
              </div>
            </div>
            <button class="cfg-add" type="button" @click="addCondition()">
              <span class="mdi mdi-plus"></span> Add condition
            </button>
          </div>
        </div>
        <div class="cfg-field">
          <label class="form-label">Continue when match</label>
          <SearchableSelect
            :options="[EMPTY_TARGET_OPTION, ...eligibleTargets]"
            :model-value="filterContinue"
            placeholder="Select target…"
            @update:model-value="filterContinue = $event"
          />
        </div>
        <div class="cfg-field">
          <label class="form-label">Exit otherwise</label>
          <SearchableSelect
            :options="[EMPTY_TARGET_OPTION, ...eligibleTargets]"
            :model-value="filterExit"
            placeholder="Select target…"
            @update:model-value="filterExit = $event"
          />
        </div>
      </template>

      <!-- A/B test -->
      <template v-else-if="nodeType === 'ab_test'">
        <div class="cfg-field">
          <label class="form-label">
            Variants
            <span class="cfg-sum" :class="abWeightSum === 100 ? 'cfg-sum--ok' : 'cfg-sum--bad'">sum {{ abWeightSum }}%</span>
          </label>
          <div class="cfg-variants">
            <div v-for="(v, i) in abVariants" :key="v.id || i" class="cfg-variant">
              <div class="cfg-cond__row">
                <input
                  class="form-input"
                  :value="v.name"
                  placeholder="Variant name"
                  @input="patchVariant(i, { name: ($event.target as HTMLInputElement).value })"
                />
                <button class="cfg-cond__remove" aria-label="Remove variant" @click="removeVariant(i)">
                  <span class="mdi mdi-close"></span>
                </button>
              </div>
              <div class="cfg-cond__row">
                <input
                  class="form-input"
                  type="number"
                  min="0"
                  max="100"
                  :value="num(v.weight)"
                  placeholder="Weight %"
                  @input="patchVariant(i, { weight: Number(($event.target as HTMLInputElement).value) || 0 })"
                />
                <SearchableSelect
                  class="cfg-variant__target"
                  :options="[EMPTY_TARGET_OPTION, ...eligibleTargets]"
                  :model-value="variantTarget(v)"
                  placeholder="Target…"
                  @update:model-value="patchVariant(i, { next_node_id: $event })"
                />
              </div>
            </div>
            <button class="cfg-add" type="button" @click="addVariant()">
              <span class="mdi mdi-plus"></span> Add variant
            </button>
          </div>
        </div>
      </template>

      <!-- List status branch -->
      <template v-else-if="nodeType === 'list_status'">
        <div class="cfg-field">
          <label class="form-label">List to check</label>
          <SearchableSelect
            :options="lists"
            :model-value="configListId"
            placeholder="Select a list…"
            empty-text="No lists yet"
            @update:model-value="configListId = $event"
          />
        </div>
        <div v-for="t in listStatusTargets" :key="t.key" class="cfg-field">
          <label class="form-label">{{ t.label }}</label>
          <SearchableSelect
            :options="[EMPTY_TARGET_OPTION, ...eligibleTargets]"
            :model-value="targetFor(t.key)"
            placeholder="Select target…"
            @update:model-value="set(t.key, $event)"
          />
        </div>
      </template>

      <!-- Add / remove from list -->
      <template v-else-if="nodeType === 'add_to_list' || nodeType === 'remove_from_list'">
        <div class="cfg-field">
          <label class="form-label">List</label>
          <SearchableSelect
            :options="lists"
            :model-value="configListId"
            placeholder="Select a list…"
            empty-text="No lists yet"
            @update:model-value="configListId = $event"
          />
        </div>
      </template>

      <!-- Webhook -->
      <template v-else-if="nodeType === 'webhook'">
        <div class="cfg-field">
          <label class="form-label">URL</label>
          <input
            class="form-input"
            :value="config.url ?? ''"
            placeholder="https://example.com/hook"
            @input="set('url', ($event.target as HTMLInputElement).value)"
          />
          <p class="form-hint">POST · http/https · private addresses are rejected.</p>
        </div>
        <div class="cfg-field">
          <label class="form-label">Headers</label>
          <div class="cfg-conds">
            <div v-for="[key, value] in webhookHeaders" :key="key" class="cfg-cond">
              <div class="cfg-cond__row">
                <input
                  class="form-input"
                  placeholder="Header"
                  :value="key"
                  @change="patchHeader(key, ($event.target as HTMLInputElement).value, String(value))"
                />
                <button class="cfg-cond__remove" aria-label="Remove header" @click="removeHeader(key)">
                  <span class="mdi mdi-close"></span>
                </button>
              </div>
              <input
                class="form-input"
                placeholder="Value"
                :value="value"
                @change="patchHeader(key, key, ($event.target as HTMLInputElement).value)"
              />
            </div>
            <button class="cfg-add" type="button" @click="addHeader()">
              <span class="mdi mdi-plus"></span> Add header
            </button>
          </div>
        </div>
        <div class="cfg-field">
          <label class="form-label">Body</label>
          <textarea
            class="form-textarea"
            rows="5"
            :value="config.body ?? ''"
            placeholder='{ "email": "{{contact_email}}" }'
            @input="set('body', ($event.target as HTMLTextAreaElement).value)"
          ></textarea>
          <p class="form-hint">JSON with <code v-pre>{{contact_email}}</code>-style placeholders.</p>
        </div>
        <div class="cfg-field">
          <label class="form-label">Secret</label>
          <input
            class="form-input"
            :value="config.secret ?? ''"
            placeholder="Optional"
            @input="set('secret', ($event.target as HTMLInputElement).value)"
          />
        </div>
      </template>
    </div>

    <footer class="cfg__footer">
      <button v-if="nodeType !== 'trigger'" class="btn btn-danger btn-sm" @click="emit('delete')">
        <span class="mdi mdi-delete-outline"></span>
        Delete node
      </button>
    </footer>
  </aside>
</template>

<style scoped>
.cfg {
  display: flex;
  flex-direction: column;
  width: 320px;
  flex: none;
  border-left: 1px solid var(--border-primary);
  background: var(--bg-secondary);
  max-height: 100%;
}

.cfg__header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--border-primary);
}

.cfg__icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: none;
  border-radius: var(--radius-sm);
  font-size: 18px;
}

.cfg__icon--trigger { background: var(--primary-50); color: var(--primary-600); }
.cfg__icon--delay { background: var(--warning-50); color: var(--warning-text); }
.cfg__icon--email { background: var(--primary-50); color: var(--primary-600); }
.cfg__icon--filter { background: var(--primary-50); color: var(--primary-600); }
.cfg__icon--abtest { background: var(--primary-50); color: var(--primary-600); }
.cfg__icon--liststatus { background: var(--primary-50); color: var(--primary-600); }
.cfg__icon--listaction { background: var(--success-50); color: var(--success-text); }
.cfg__icon--webhook { background: var(--primary-50); color: var(--primary-600); }

.cfg__heading {
  display: flex;
  flex-direction: column;
  min-width: 0;
  flex: 1;
}

.cfg__heading h2 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
}

.cfg__node-id {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cfg__close {
  display: inline-flex;
  padding: 6px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

.cfg__close:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.cfg__body {
  flex: 1;
  overflow-y: auto;
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.cfg-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.cfg-field .form-label {
  margin: 0;
  display: flex;
  align-items: center;
  gap: 8px;
}

.cfg-field__and {
  font-size: 11px;
  font-weight: 500;
  color: var(--text-muted);
}

.cfg-note {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  background: var(--warning-50);
  color: var(--warning-text);
  font-size: 12.5px;
}

.cfg-radio-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.cfg-radio {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13.5px;
  color: var(--text-secondary);
  cursor: pointer;
}

.cfg-radio input {
  cursor: pointer;
}

.cfg-conds,
.cfg-variants {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.cfg-cond,
.cfg-variant {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-sm);
  background: var(--bg-primary);
}

.cfg-cond__row {
  display: flex;
  gap: 6px;
  align-items: center;
}

.cfg-cond__row > .form-input,
.cfg-cond__row > .form-select {
  flex: 1;
  min-width: 0;
}

.cfg-cond__remove {
  display: inline-flex;
  padding: 6px;
  flex: none;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
}

.cfg-cond__remove:hover {
  background: var(--danger-50);
  color: var(--danger-text);
}

.cfg-add {
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

.cfg-add:hover {
  border-color: var(--primary-500);
  color: var(--primary-600);
}

.cfg-sum {
  margin-left: auto;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.cfg-sum--ok {
  background: var(--success-50);
  color: var(--success-text);
}

.cfg-sum--bad {
  background: var(--danger-50);
  color: var(--danger-text);
}

.cfg-variant__target {
  flex: 1;
  min-width: 0;
}

.cfg__footer {
  padding: 10px 14px;
  border-top: 1px solid var(--border-primary);
}
</style>
