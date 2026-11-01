<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { adminApi } from '../../../api/admin'
import type { SecurityRule } from '../../../api/types'
import { useNotificationStore } from '../../../stores/notification'

const notify = useNotificationStore()
const loading = ref(true)
const rules = ref<SecurityRule[]>([])

// Presentation for the groups the backend stamps on each rule, in a fixed
// reading order (whatever the API returns, the page keeps the same layout).
const groupOrder = ['general', 'signals', 'weights', 'escalation', 'retention']
const groupMeta: Record<string, { title: string; description: string }> = {
  general: { title: 'General', description: 'Risk scoring and the instant-ban trigger.' },
  signals: { title: 'Signal Thresholds & Windows', description: 'Per-signal counts and sliding windows before a signal fires.' },
  weights: { title: 'Signal Weights', description: 'Risk score contributed when a signal is exceeded.' },
  escalation: { title: 'Ban Escalation', description: 'Automatic ban duration by offense count.' },
  retention: { title: 'Retention', description: 'How long detection events are kept.' },
}

const groups = computed(() =>
  groupOrder
    .map(g => ({
      key: g,
      title: groupMeta[g]?.title || g,
      description: groupMeta[g]?.description || '',
      rules: rules.value.filter(r => r.group === g),
    }))
    .filter(g => g.rules.length > 0)
)

const editedValues = ref<Record<string, string>>({})
const savingKeys = ref<Record<string, boolean>>({})
const savedKeys = ref<Record<string, boolean>>({})

// Debounce timers for number inputs (toggles save immediately).
const SAVE_DEBOUNCE_MS = 700
const timers: Record<string, ReturnType<typeof setTimeout>> = {}
const savedFlashTimers: Record<string, ReturnType<typeof setTimeout>> = {}

onMounted(async () => {
  try {
    const res = await adminApi.getSecurityRules()
    rules.value = res.data.data || []
  } catch {
    notify.error('Failed to load security rules')
  } finally {
    loading.value = false
  }
})

function getEditedValue(key: string, original: string): string {
  return key in editedValues.value ? editedValues.value[key] : original
}

function displayValue(rule: SecurityRule): string {
  return String(rule.value)
}

const anySaving = computed(() => Object.values(savingKeys.value).some(Boolean))

// Clamp a submitted int to the rule's declared bounds so the round trip never
// bounces off the server-side validation.
function clamp(rule: SecurityRule, value: number): number {
  if (rule.min !== undefined && value < rule.min) return rule.min
  if (rule.max !== undefined && value > rule.max) return rule.max
  return value
}

// Persist a single rule if its working value differs from the committed one.
async function commit(key: string) {
  clearTimeout(timers[key])
  const rule = rules.value.find(r => r.key === key)
  if (!rule) return

  let value = getEditedValue(key, displayValue(rule))

  if (rule.type === 'int') {
    const parsed = parseInt(value, 10)
    if (isNaN(parsed)) {
      delete editedValues.value[key]
      return
    }
    const clamped = clamp(rule, parsed)
    if (String(clamped) !== value) {
      value = String(clamped)
      editedValues.value[key] = value
    }
  }

  if (value === displayValue(rule)) {
    delete editedValues.value[key]
    return
  }

  savingKeys.value[key] = true
  try {
    const res = await adminApi.updateSecurityRules([{ key, value, type: rule.type }])
    // Adopt the server's canonical value (the response echoes the fresh rule
    // list) and clear the edit.
    const updated = (res.data.data || []).find(r => r.key === key)
    if (updated) rule.value = updated.value
    delete editedValues.value[key]
    flashSaved(key)
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to save'
    notify.error(`${rule.label}: ${message}`)
    // Revert the field to the last committed value.
    delete editedValues.value[key]
  } finally {
    savingKeys.value[key] = false
  }
}

function flashSaved(key: string) {
  savedKeys.value[key] = true
  clearTimeout(savedFlashTimers[key])
  savedFlashTimers[key] = setTimeout(() => {
    savedKeys.value[key] = false
  }, 2000)
}

// Number input: track the value and debounce the save.
function onInput(rule: SecurityRule, event: Event) {
  const value = (event.target as HTMLInputElement).value
  editedValues.value[rule.key] = value
  clearTimeout(timers[rule.key])
  timers[rule.key] = setTimeout(() => commit(rule.key), SAVE_DEBOUNCE_MS)
}

// Boolean toggle: flip and persist immediately.
function toggleBool(rule: SecurityRule) {
  const next = getEditedValue(rule.key, displayValue(rule)) === 'true' ? 'false' : 'true'
  editedValues.value[rule.key] = next
  commit(rule.key)
}
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Detection Rules</h1>
        <p class="page-description">Thresholds, windows and weights that drive automatic bans.</p>
      </div>
      <div class="autosave-status" :class="{ active: anySaving }">
        <template v-if="anySaving">
          <span class="autosave-spinner"></span>
          <span>Saving…</span>
        </template>
        <template v-else>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"
            stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M20 6 9 17l-5-5" />
          </svg>
          <span>Changes save automatically</span>
        </template>
      </div>
    </div>

    <div v-if="loading" class="loading-page">
      <div class="spinner"></div>
    </div>

    <div v-else class="rules-stack">
      <div v-for="group in groups" :key="group.key" class="card">
        <div class="card-header">
          <div>
            <h2>{{ group.title }}</h2>
            <p class="group-description">{{ group.description }}</p>
          </div>
        </div>
        <div class="card-body">
          <div v-for="rule in group.rules" :key="rule.key" class="setting-row">
            <div class="setting-info">
              <label class="setting-label" :for="`rule-${rule.key}`">{{ rule.label }}</label>
              <span class="setting-description">{{ rule.description }}</span>
            </div>
            <div class="setting-control">
              <span class="setting-status" aria-live="polite">
                <span v-if="savingKeys[rule.key]" class="setting-status-saving">Saving…</span>
                <span v-else-if="savedKeys[rule.key]" class="setting-status-saved">Saved ✓</span>
              </span>
              <!-- Boolean toggle -->
              <template v-if="rule.type === 'bool'">
                <button
                  :id="`rule-${rule.key}`"
                  :class="['toggle-btn', { active: getEditedValue(rule.key, displayValue(rule)) === 'true' }]"
                  :disabled="savingKeys[rule.key]"
                  :aria-label="rule.label"
                  @click="toggleBool(rule)"
                >
                  <span class="toggle-slider"></span>
                </button>
              </template>
              <!-- Number input -->
              <template v-else>
                <input
                  :id="`rule-${rule.key}`"
                  type="number"
                  class="form-input setting-input-number"
                  :min="rule.min"
                  :max="rule.max"
                  :value="getEditedValue(rule.key, displayValue(rule))"
                  :aria-label="rule.label"
                  @input="onInput(rule, $event)"
                  @blur="commit(rule.key)"
                  @keyup.enter="commit(rule.key)"
                />
              </template>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-description {
  font-size: 13px;
  color: var(--text-muted);
  margin-top: 4px;
}

.rules-stack {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.group-description {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
  font-weight: 400;
}

.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  padding: 14px 0;
  border-bottom: 1px solid var(--border-primary);
}

.setting-row:last-child {
  border-bottom: none;
}

.setting-info {
  flex: 1;
  min-width: 0;
}

.setting-label {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary);
  display: block;
}

.setting-description {
  font-size: 12px;
  color: var(--text-muted);
  margin-top: 2px;
  display: block;
}

.setting-control {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 10px;
}

/* Header auto-save status */
.autosave-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  color: var(--text-muted);
  white-space: nowrap;
}
.autosave-status.active { color: var(--primary-600); }
.autosave-spinner {
  width: 13px;
  height: 13px;
  border: 2px solid color-mix(in srgb, var(--primary-600) 30%, transparent);
  border-top-color: var(--primary-600);
  border-radius: 50%;
  animation: autosave-spin 0.6s linear infinite;
}
@keyframes autosave-spin { to { transform: rotate(360deg); } }

/* Per-row save status */
.setting-status {
  font-size: 11.5px;
  min-width: 56px;
  text-align: right;
}
.setting-status-saving { color: var(--text-muted); }
.setting-status-saved { color: var(--success-600); font-weight: 500; }

.setting-input-number {
  width: 110px;
  text-align: right;
}

.toggle-btn {
  position: relative;
  width: 44px;
  height: 24px;
  border-radius: 12px;
  border: none;
  background: var(--border-primary);
  cursor: pointer;
  transition: background 0.2s;
  padding: 0;
  flex-shrink: 0;
}

.toggle-btn.active {
  background: var(--primary-600);
}

.toggle-slider {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  background: white;
  transition: transform 0.2s;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.15);
}

.toggle-btn.active .toggle-slider {
  transform: translateX(20px);
}

@media (max-width: 640px) {
  .setting-row {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .setting-control {
    width: 100%;
    justify-content: flex-end;
  }
}
</style>
