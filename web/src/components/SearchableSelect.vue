<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

interface Option {
  value: string
  label: string
}

const props = withDefaults(
  defineProps<{
    options: Option[]
    modelValue?: string
    placeholder?: string
    searchPlaceholder?: string
    emptyText?: string
  }>(),
  {
    modelValue: '',
    placeholder: 'Select…',
    searchPlaceholder: 'Search…',
    emptyText: 'No matches',
  },
)

const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

const open = ref(false)
const query = ref('')
const highlighted = ref(0)
const root = ref<HTMLElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const listEl = ref<HTMLElement | null>(null)

const selectedLabel = computed(
  () => props.options.find((o) => o.value === props.modelValue)?.label ?? props.modelValue,
)

const hasSelection = computed(() => !!props.modelValue)

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return props.options
  return props.options.filter(
    (o) => o.label.toLowerCase().includes(q) || o.value.toLowerCase().includes(q),
  )
})

function openMenu() {
  if (open.value) return
  open.value = true
  query.value = ''
  highlighted.value = Math.max(
    0,
    props.options.findIndex((o) => o.value === props.modelValue),
  )
  nextTick(() => searchInput.value?.focus())
}

function closeMenu() {
  open.value = false
}

function toggleMenu() {
  open.value ? closeMenu() : openMenu()
}

function choose(option: Option) {
  emit('update:modelValue', option.value)
  closeMenu()
}

function move(delta: number) {
  if (!filtered.value.length) return
  highlighted.value = (highlighted.value + delta + filtered.value.length) % filtered.value.length
  nextTick(() => {
    listEl.value
      ?.querySelector(`[data-index="${highlighted.value}"]`)
      ?.scrollIntoView({ block: 'nearest' })
  })
}

function onKeydown(e: KeyboardEvent) {
  switch (e.key) {
    case 'ArrowDown':
      e.preventDefault()
      if (!open.value) openMenu()
      else move(1)
      break
    case 'ArrowUp':
      e.preventDefault()
      if (!open.value) openMenu()
      else move(-1)
      break
    case 'Enter':
      if (open.value && filtered.value[highlighted.value]) {
        e.preventDefault()
        choose(filtered.value[highlighted.value])
      }
      break
    case 'Escape':
      if (open.value) {
        e.stopPropagation()
        closeMenu()
      }
      break
    case 'Tab':
      closeMenu()
      break
  }
}

function onDocumentMousedown(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) closeMenu()
}

onMounted(() => document.addEventListener('mousedown', onDocumentMousedown))
onBeforeUnmount(() => document.removeEventListener('mousedown', onDocumentMousedown))

// Reset the highlight to the top whenever the filter narrows the list.
watch(query, () => {
  highlighted.value = 0
})
</script>

<template>
  <div ref="root" class="ssel" @keydown="onKeydown">
    <button
      type="button"
      class="ssel__trigger"
      :class="{ open }"
      :aria-expanded="open"
      aria-haspopup="listbox"
      @click="toggleMenu"
    >
      <span class="ssel__value" :class="{ 'is-placeholder': !hasSelection }">{{ selectedLabel || placeholder }}</span>
      <svg class="ssel__chevron" width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
        <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </button>

    <div v-if="open" class="ssel__menu">
      <input
        ref="searchInput"
        v-model="query"
        class="ssel__search"
        type="text"
        role="combobox"
        :aria-expanded="open"
        aria-controls="ssel-listbox"
        aria-autocomplete="list"
        :placeholder="searchPlaceholder"
      />
      <ul ref="listEl" id="ssel-listbox" class="ssel__list" role="listbox">
        <li
          v-for="(o, i) in filtered"
          :key="o.value"
          :data-index="i"
          class="ssel__option"
          role="option"
          :aria-selected="o.value === modelValue"
          :class="{ 'is-active': i === highlighted, 'is-selected': o.value === modelValue }"
          @mouseenter="highlighted = i"
          @click="choose(o)"
        >
          <span class="ssel__option-label">{{ o.label }}</span>
          <svg v-if="o.value === modelValue" class="ssel__check" width="14" height="14" viewBox="0 0 16 16"
            fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
            aria-hidden="true">
            <path d="M3.5 8.5l3 3 6-7" />
          </svg>
        </li>
        <li v-if="!filtered.length" class="ssel__empty">{{ emptyText }}</li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.ssel {
  position: relative;
  width: 100%;
}

.ssel__trigger {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 14px;
  border: 1px solid var(--border-input);
  border-radius: var(--radius);
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 14px;
  font-family: inherit;
  cursor: pointer;
  transition: border-color var(--transition), box-shadow var(--transition);
}

.ssel__trigger:hover {
  border-color: var(--text-muted);
}

.ssel__trigger.open,
.ssel__trigger:focus-visible {
  border-color: var(--border-focus);
  box-shadow: var(--shadow-focus);
}

.ssel__value {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssel__value.is-placeholder {
  color: var(--text-muted);
}

.ssel__chevron {
  flex: none;
  color: var(--text-tertiary);
  transition: transform var(--transition);
}

.ssel__trigger.open .ssel__chevron {
  transform: rotate(180deg);
}

.ssel__menu {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  z-index: 20;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  padding: 6px;
}

.ssel__search {
  width: 100%;
  padding: 8px 10px;
  margin-bottom: 6px;
  border: 1px solid var(--border-input);
  border-radius: var(--radius-sm);
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 13px;
  font-family: inherit;
  outline: none;
  transition: border-color var(--transition), box-shadow var(--transition);
}

.ssel__search:focus {
  border-color: var(--border-focus);
  box-shadow: var(--shadow-focus);
}

.ssel__search::placeholder {
  color: var(--text-muted);
}

.ssel__list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 280px;
  overflow-y: auto;
}

.ssel__option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 10px;
  border-radius: calc(var(--radius) - 2px);
  font-size: 13.5px;
  color: var(--text-primary);
  cursor: pointer;
}

.ssel__option.is-active {
  background: var(--bg-hover);
}

.ssel__option-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ssel__check {
  flex: none;
  color: var(--accent);
}

.ssel__empty {
  padding: 12px 10px;
  font-size: 13px;
  color: var(--text-muted);
  text-align: center;
}
</style>
