<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import type { DashboardFeatures } from '../../api/types'

const props = defineProps<{ features?: DashboardFeatures | null }>()

const router = useRouter()

const actions = computed(() => {
  const items = [
    { to: '/emails', icon: 'send', label: 'Send an email', description: 'Compose and send an email.' },
    { to: '/templates', icon: 'file-text', label: 'New template', description: 'Create a reusable email template.' },
    { to: '/campaigns', icon: 'mail', label: 'New campaign', description: 'Create and manage an email campaign.' },
    { to: '/domains', icon: 'globe', label: 'Add domain', description: 'Connect a domain for email sending.' },
    { to: '/api-keys', icon: 'key', label: 'Create API key', description: 'Generate an API credential.' },
    { to: '/webhooks', icon: 'link', label: 'Add webhook', description: 'Connect an external service.' },
  ]
  if (props.features?.messages) {
    items.splice(3, 0, {
      to: '/forms',
      icon: 'edit-3',
      label: 'New form',
      description: 'Collect messages from your website.',
    })
  }
  return items
})

const icons: Record<string, string> = {
  send: '<path d="M16.5 1.5L8.25 9.75M16.5 1.5l-5.25 15-3-6.75L1.5 6.75l15-5.25z"/>',
  'file-text': '<path d="M10.5 1.5H4.5a1.5 1.5 0 00-1.5 1.5v12a1.5 1.5 0 001.5 1.5h9a1.5 1.5 0 001.5-1.5V6l-4.5-4.5z"/><path d="M10.5 1.5V6H15"/>',
  mail: '<rect x="2" y="3.5" width="14" height="11" rx="2"/><path d="M2 5.5l7 5 7-5"/>',
  globe: '<circle cx="9" cy="9" r="7"/><path d="M2 9h14M9 2a11 11 0 013 7 11 11 0 01-3 7 11 11 0 01-3-7 11 11 0 013-7z"/>',
  key: '<path d="M15.5 2.5l-2 2m1 1l-2 2-3-3 2-2m-3.18 3.18a4 4 0 10-5.64 5.64 4 4 0 005.64-5.64z"/>',
  link: '<path d="M7.5 10.5a3.75 3.75 0 005.3.45l2.25-2.25a3.75 3.75 0 00-5.3-5.3l-1.29 1.28"/><path d="M10.5 7.5a3.75 3.75 0 00-5.3-.45L2.96 9.3a3.75 3.75 0 005.3 5.3l1.28-1.28"/>',
  'edit-3': '<path d="M12 2.25l3.75 3.75L6 15.75H2.25V12L12 2.25z"/>',
}
</script>

<template>
  <section class="quick-actions">
    <div class="qa-header">
      <h2 class="qa-heading">Quick Actions</h2>
      <p class="qa-subtitle">Common tasks, right at your fingertips.</p>
    </div>
    <div class="qa-grid">
      <button
        v-for="a in actions"
        :key="a.to"
        type="button"
        class="qa-card"
        @click="router.push(a.to)"
      >
        <span class="qa-icon-chip" aria-hidden="true">
          <svg
            width="20"
            height="20"
            viewBox="0 0 18 18"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
            v-html="icons[a.icon]"
          ></svg>
        </span>
        <span class="qa-card-title">{{ a.label }}</span>
        <span class="qa-card-desc">{{ a.description }}</span>
      </button>
    </div>
  </section>
</template>

<style scoped>
.qa-header {
  margin-bottom: 14px;
}

.qa-heading {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.01em;
  margin: 0;
}

.qa-subtitle {
  font-size: 13px;
  color: var(--text-muted);
  margin: 2px 0 0;
}

.qa-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.qa-card {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 10px;
  min-height: 148px;
  padding: 20px;
  border-radius: var(--radius-lg);
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  color: var(--text-primary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.15s ease, background 0.15s ease, transform 0.15s ease;
}

.qa-card:hover {
  border-color: color-mix(in srgb, var(--accent) 45%, transparent);
  background: var(--bg-hover);
  transform: translateY(-2px);
}

.qa-card:focus-visible {
  outline: 2px solid var(--border-focus);
  outline-offset: 2px;
}

.qa-icon-chip {
  flex: none;
  width: 42px;
  height: 42px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--accent) 25%, transparent);
  color: var(--accent);
}

.qa-card-title {
  font-size: 14.5px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.01em;
}

.qa-card-desc {
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--text-muted);
}

@media (max-width: 1023px) {
  .qa-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 639px) {
  .qa-grid {
    grid-template-columns: 1fr;
  }

  .qa-card {
    min-height: 0;
  }
}
</style>
