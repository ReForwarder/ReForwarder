<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { inboxApi, type InboxNotification, type NotificationCategory } from "../api/inbox";
import { useInboxStore } from "../stores/inbox";
import { useNotificationStore } from "../stores/notification";
import { apiMessage } from "../composables/apiError";

const router = useRouter();
const store = useInboxStore();
const notify = useNotificationStore();

const items = ref<InboxNotification[]>([]);
const loading = ref(true);
const loadingMore = ref(false);
const exhausted = ref(false);
const filter = ref<"all" | "unread" | "open">("all");
const category = ref<NotificationCategory | "">("");

const PAGE = 30;

const categories: { value: NotificationCategory | ""; label: string }[] = [
  { value: "", label: "All categories" },
  { value: "platform", label: "Platform" },
  { value: "domains", label: "Domains" },
  { value: "deliverability", label: "Deliverability" },
  { value: "security", label: "Security" },
  { value: "messages", label: "Messages" },
];

const unreadCount = computed(() => items.value.filter((n) => !n.read_at).length);

function params(before?: number) {
  return {
    unread: filter.value === "unread",
    open: filter.value === "open",
    category: category.value || undefined,
    before,
    limit: PAGE,
  };
}

async function load() {
  loading.value = true;
  exhausted.value = false;
  try {
    const res = await inboxApi.list(params());
    items.value = res.data.data ?? [];
    exhausted.value = items.value.length < PAGE;
  } catch (e: any) {
    notify.error(apiMessage(e, "Failed to load notifications"));
  } finally {
    loading.value = false;
  }
}

async function loadMore() {
  const last = items.value[items.value.length - 1];
  if (!last || loadingMore.value) return;
  loadingMore.value = true;
  try {
    const res = await inboxApi.list(params(last.id));
    const page = res.data.data ?? [];
    items.value = items.value.concat(page);
    exhausted.value = page.length < PAGE;
  } catch (e: any) {
    notify.error(apiMessage(e, "Failed to load more"));
  } finally {
    loadingMore.value = false;
  }
}

function setFilter(f: "all" | "unread" | "open") {
  filter.value = f;
  void load();
}

async function open(n: InboxNotification) {
  if (!n.read_at) {
    n.read_at = new Date().toISOString();
    await store.markRead([n.id]);
  }
  if (n.link) router.push(n.link);
}

async function markAllRead() {
  try {
    await store.markAllRead();
    const now = new Date().toISOString();
    for (const n of items.value) if (!n.read_at) n.read_at = now;
  } catch (e: any) {
    notify.error(apiMessage(e, "Failed to mark all read"));
  }
}

async function dismiss(n: InboxNotification) {
  try {
    await store.dismiss(n.id);
    const now = new Date().toISOString();
    n.dismissed_at = now;
    n.read_at = n.read_at || now;
  } catch (e: any) {
    notify.error(apiMessage(e, "Failed to dismiss"));
  }
}

function state(n: InboxNotification) {
  if (n.resolved_at) return { label: "Resolved", cls: "badge-success" };
  if (n.dismissed_at) return { label: "Dismissed", cls: "badge-neutral" };
  return { label: "Open", cls: n.severity === "critical" ? "badge-danger" : "badge-warning" };
}

function formatDate(iso: string) {
  return new Date(iso).toLocaleString();
}

onMounted(load);
</script>

<template>
  <div>
    <div class="page-header">
      <div>
        <h1>Notifications</h1>
        <p class="page-subtitle">
          What ReForwarder noticed about your workspaces, and notices from the platform.
        </p>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary" :disabled="!unreadCount" @click="markAllRead">
          Mark all read
        </button>
      </div>
    </div>

    <div class="toolbar">
      <div class="chips">
        <button
          v-for="f in (['all', 'unread', 'open'] as const)"
          :key="f"
          class="chip"
          :class="{ active: filter === f }"
          type="button"
          @click="setFilter(f)"
        >
          {{ f === "all" ? "All" : f === "unread" ? "Unread" : "Needs attention" }}
        </button>
      </div>
      <select v-model="category" class="form-select" @change="load">
        <option v-for="c in categories" :key="c.value" :value="c.value">{{ c.label }}</option>
      </select>
    </div>

    <div v-if="loading" class="loading-page"><div class="spinner"></div></div>

    <div v-else-if="!items.length" class="card">
      <div class="empty-state">
        <h3>Nothing here</h3>
        <p>
          ReForwarder writes to this inbox when something about a workspace needs attention — an
          unverified domain, a climbing bounce rate, an API key about to lapse.
        </p>
      </div>
    </div>

    <div v-else class="card">
      <ul class="feed">
        <li v-for="n in items" :key="n.id" class="row" :class="{ unread: !n.read_at }">
          <span class="dot" :class="`sev-${n.severity}`" aria-hidden="true"></span>
          <div class="row-body">
            <div class="row-head">
              <button class="row-title" type="button" @click="open(n)">{{ n.title }}</button>
              <span class="badge" :class="state(n).cls">{{ state(n).label }}</span>
            </div>
            <p class="row-text">{{ n.body }}</p>
            <div class="row-meta">
              <span>{{ formatDate(n.created_at) }}</span>
              <span class="sep">·</span>
              <span>{{ n.category }}</span>
              <template v-if="n.link">
                <span class="sep">·</span>
                <button class="row-link" type="button" @click="open(n)">
                  {{ n.action_text || "Open" }}
                </button>
              </template>
            </div>
          </div>
          <button
            v-if="!n.dismissed_at && !n.resolved_at"
            class="row-dismiss"
            type="button"
            :aria-label="`Dismiss: ${n.title}`"
            title="Dismiss. Comes back if the condition gets worse."
            @click="dismiss(n)"
          >
            Dismiss
          </button>
        </li>
      </ul>

      <div v-if="!exhausted" class="feed-foot">
        <button class="btn btn-secondary" :disabled="loadingMore" @click="loadMore">
          {{ loadingMore ? "Loading…" : "Load more" }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-subtitle {
  font-size: 13px;
  color: var(--text-tertiary);
  margin-top: 4px;
}

.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}

.chips {
  display: flex;
  gap: 6px;
}

.chip {
  padding: 6px 12px;
  border: 1px solid var(--border-primary);
  border-radius: 999px;
  background: var(--bg-primary);
  font-size: 12px;
  font-weight: 500;
  color: var(--text-secondary);
  cursor: pointer;
}

.chip.active {
  background: var(--primary-600);
  border-color: var(--primary-600);
  color: var(--text-on-primary);
}

.toolbar .form-select {
  width: auto;
  min-width: 180px;
}

.feed {
  list-style: none;
  margin: 0;
  padding: 0;
}

.row {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border-secondary);
}

.row:last-child {
  border-bottom: 0;
}

.row.unread {
  background: var(--bg-tertiary);
}

.dot {
  flex: none;
  width: 8px;
  height: 8px;
  margin-top: 6px;
  border-radius: 50%;
  background: var(--text-tertiary);
}

.sev-warning {
  background: var(--warning-500);
}

.sev-critical {
  background: var(--danger-600);
}

.sev-info {
  background: var(--info-500);
}

.row-body {
  flex: 1;
  min-width: 0;
}

.row-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.row-title {
  border: 0;
  background: none;
  padding: 0;
  font: inherit;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
  cursor: pointer;
  text-align: left;
}

.row-title:hover {
  color: var(--info-text);
}

.row-text {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--text-secondary);
  line-height: 1.6;
}

.row-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: 6px;
  font-size: 12px;
  color: var(--text-tertiary);
}

.sep {
  color: var(--border-primary);
}

.row-link {
  border: 0;
  background: none;
  padding: 0;
  font: inherit;
  font-size: 12px;
  color: var(--info-text);
  cursor: pointer;
}

.row-link:hover {
  text-decoration: underline;
}

.row-dismiss {
  flex: none;
  border: 0;
  background: none;
  padding: 0;
  font: inherit;
  font-size: 12px;
  color: var(--text-tertiary);
  cursor: pointer;
}

.row-dismiss:hover {
  color: var(--text-primary);
  text-decoration: underline;
}

.feed-foot {
  padding: 14px 16px;
  text-align: center;
  border-top: 1px solid var(--border-secondary);
}
</style>
