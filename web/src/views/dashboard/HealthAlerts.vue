<script setup lang="ts">
import { computed, onMounted, watch } from "vue";
import { storeToRefs } from "pinia";
import { useRouter } from "vue-router";
import { useInboxStore } from "../../stores/inbox";
import type { InboxNotification } from "../../api/inbox";
import type { DashboardStats } from "../../api/types";


const props = defineProps<{ stats: DashboardStats }>();

const router = useRouter();
const store = useInboxStore();
const { banner } = storeToRefs(store);

const alerts = computed(() => banner.value);

function tone(n: InboxNotification) {
  return n.severity === "critical" ? "danger" : n.severity === "warning" ? "warning" : "info";
}

function act(n: InboxNotification) {
  if (n.link) router.push(n.link);
}

onMounted(() => void store.loadBanner());
watch(() => props.stats, () => void store.loadBanner());
</script>

<template>
  <div v-if="alerts.length" class="alerts">
    <div
      v-for="a in alerts"
      :key="a.id"
      class="alert-banner"
      :class="`alert-${tone(a)}`"
      role="status"
    >
      <svg
        class="ab-icon"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <template v-if="a.kind === 'announcement'">
          <circle cx="12" cy="12" r="10" />
          <line x1="12" y1="16" x2="12" y2="12" />
          <line x1="12" y1="8" x2="12.01" y2="8" />
        </template>
        <template v-else>
          <path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" />
          <line x1="12" y1="9" x2="12" y2="13" />
          <line x1="12" y1="17" x2="12.01" y2="17" />
        </template>
      </svg>
      <div class="ab-text">
        <strong>{{ a.title }}</strong>
        <span>{{ a.body }}</span>
      </div>
      <button v-if="a.link" class="btn btn-secondary btn-sm" @click="act(a)">
        {{ a.action_text || "Open" }}
      </button>
      <button
        class="ab-dismiss"
        type="button"
        :aria-label="`Dismiss: ${a.title}`"
        title="Dismiss. Comes back if this gets worse."
        @click="store.dismiss(a.id)"
      >
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true">
          <path d="M4 4l8 8M12 4l-8 8" />
        </svg>
      </button>
    </div>
  </div>
</template>

<style scoped>
.alerts {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.alert-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px 16px;
  border-radius: var(--radius-md);
  border: 1px solid;
}

.alert-danger {
  background: var(--danger-50);
  border-color: color-mix(in srgb, var(--danger-500) 30%, transparent);
  color: var(--danger-text);
}

.alert-warning {
  background: var(--warning-50);
  border-color: color-mix(in srgb, var(--warning-500) 30%, transparent);
  color: var(--warning-text);
}

.alert-info {
  background: var(--primary-50);
  border-color: color-mix(in srgb, var(--accent) 30%, transparent);
  color: var(--accent);
}

.ab-icon {
  width: 20px;
  height: 20px;
  flex: none;
}

.ab-text {
  flex: 1;
  min-width: 220px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 13px;
  color: var(--text-secondary);
}

.ab-text strong {
  color: var(--text-primary);
  font-size: 13px;
}

.ab-dismiss {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  padding: 0;
  border: 0;
  border-radius: 6px;
  background: none;
  color: var(--text-tertiary);
  cursor: pointer;
}

.ab-dismiss svg {
  width: 14px;
  height: 14px;
}

.ab-dismiss:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}
</style>
