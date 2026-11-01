<script setup lang="ts">
import { computed } from 'vue'
import type { Domain } from '../api/types'

const props = defineProps<{
  domain: Domain
  size?: 'sm' | 'md'
}>()

const checks = computed(() => [
  props.domain.ownership_verified,
  props.domain.spf_verified,
  props.domain.dkim_verified,
  props.domain.dmarc_verified,
])

const verifiedCount = computed(() => checks.value.filter(Boolean).length)
const total = 4

const status = computed(() => {
  if (verifiedCount.value === total) return 'verified'
  if (verifiedCount.value === 0) return 'unverified'
  return 'partial'
})

const label = computed(() => {
  if (status.value === 'verified') return 'Fully verified'
  if (status.value === 'unverified') return 'Not verified'
  return `${verifiedCount.value} of ${total} verified`
})

const icon = computed(() => {
  if (status.value === 'verified') return '✓'
  if (status.value === 'unverified') return '○'
  return '◐'
})
</script>

<template>
  <span
    class="status-pill"
    :class="[`status-pill-${status}`, size === 'sm' ? 'status-pill-sm' : '']"
    :title="`Ownership: ${domain.ownership_verified ? 'yes' : 'no'} · SPF: ${domain.spf_verified ? 'yes' : 'no'} · DKIM: ${domain.dkim_verified ? 'yes' : 'no'} · DMARC: ${domain.dmarc_verified ? 'yes' : 'no'}`"
  >
    <span class="status-pill-icon">{{ icon }}</span>
    <span>{{ label }}</span>
  </span>
</template>

<!-- Scoped styles for the status-pill classes (they are used only by this
     component and were previously undefined). Tinted badge surfaces with a
     hairline border; the status color comes from the *-text tokens so both
     themes keep AA contrast. -->
<style scoped>
.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 10px;
  border-radius: 9999px;
  border: 1px solid transparent;
  font-size: 12px;
  font-weight: 500;
  letter-spacing: 0.01em;
  white-space: nowrap;
}

.status-pill-icon {
  font-size: 10px;
  line-height: 1;
}

.status-pill-verified {
  background: var(--success-50);
  color: var(--success-text);
  border-color: color-mix(in srgb, var(--success-500) 25%, transparent);
}

.status-pill-partial {
  background: var(--warning-50);
  color: var(--warning-text);
  border-color: color-mix(in srgb, var(--warning-500) 25%, transparent);
}

.status-pill-unverified {
  background: var(--bg-tertiary);
  color: var(--text-tertiary);
  border-color: var(--border-primary);
}

.status-pill-sm {
  padding: 2px 8px;
  font-size: 11px;
}
</style>
