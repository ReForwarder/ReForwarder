import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The layout pulls the feature flags from /info; the mock is the single knob
// the tests turn.
const infoGet = vi.hoisted(() => vi.fn())

vi.mock('../api/info', () => ({
  infoApi: { get: infoGet },
}))

// Platform-admin-only update banner; never called with isAdmin: false, but the
// module must resolve for the layout to mount.
const getUpdate = vi.hoisted(() => vi.fn())
vi.mock('../api/admin', () => ({
  adminApi: { getUpdate, dismissUpdate: vi.fn() },
}))

vi.mock('../stores/auth', () => ({
  useAuthStore: () => ({
    isAdmin: false,
    user: { name: 'Test User', email: 'test@example.dev' },
    logout: vi.fn().mockResolvedValue(undefined),
  }),
}))

vi.mock('../stores/theme', () => ({
  useThemeStore: () => ({ isDark: false, mode: 'light', toggle: vi.fn(), setMode: vi.fn() }),
}))

vi.mock('../stores/workspace', () => ({
  useWorkspaceStore: () => ({
    isWorkspaceContext: false,
    isWorkspaceAdmin: false,
    currentWorkspaceIsSystem: false,
    currentWorkspaceId: null,
    currentWorkspace: null,
    contextLabel: 'Personal',
    workspaces: [],
    workspaceDisplayName: (ws: { name: string } | null) => ws?.name ?? 'Personal',
    fetchWorkspaces: vi.fn(),
    setWorkspace: vi.fn(),
  }),
}))

// The real components pull in stores and APIs the layout tests do not need.
vi.mock('../components/NotificationBell.vue', () => ({
  default: { name: 'NotificationBell', template: '<div class="notification-bell-stub" />' },
}))
vi.mock('../components/EmailVerificationBanner.vue', () => ({
  default: { name: 'EmailVerificationBanner', template: '<div />' },
}))
vi.mock('../components/ImpersonationBanner.vue', () => ({
  default: { name: 'ImpersonationBanner', template: '<div />' },
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/', query: {}, params: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

import DashboardLayout from './DashboardLayout.vue'

function envelope(features: Record<string, boolean> | undefined) {
  return {
    data: {
      success: true,
      data: {
        name: 'Posta',
        version: 'v1.0.0',
        commit_id: 'abc1234',
        openapi_docs: true,
        features,
      },
    },
  }
}

async function mountLayout(features: Record<string, boolean> | undefined) {
  infoGet.mockResolvedValue(envelope(features))
  const wrapper = mount(DashboardLayout, {
    global: {
      stubs: { RouterLink: { template: '<a><slot /></a>' }, RouterView: true },
    },
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  infoGet.mockReset()
  getUpdate.mockReset()
})

describe('DashboardLayout feature-flag navigation', () => {
  it('hides Messages/Forms/Spam Filters and Inbound Emails when their flags are off', async () => {
    const wrapper = await mountLayout({
      messages_enabled: false,
      inbound_enabled: false,
      relay_enabled: true,
      metrics_enabled: true,
    })

    const text = wrapper.text()
    expect(text).not.toContain('Inbound Emails')
    expect(text).not.toContain('Spam Filters')
    expect(text).not.toContain('Forms')
    // "Messages" as a nav label only exists on the messages entry; Message
    // History is a separate item and does not carry the plural.
    expect(wrapper.findAll('.nav-label').map(l => l.text())).not.toContain('Messages')

    // Everything else stays visible.
    expect(text).toContain('Emails')
    expect(text).toContain('Campaigns')
    expect(text).toContain('Inbound') // the section header remains
  })

  it('shows the gated entries when the flags are on', async () => {
    const wrapper = await mountLayout({
      messages_enabled: true,
      inbound_enabled: true,
      relay_enabled: true,
      metrics_enabled: false,
    })

    const text = wrapper.text()
    expect(text).toContain('Inbound Emails')
    expect(text).toContain('Forms')
    expect(text).toContain('Spam Filters')
    expect(wrapper.findAll('.nav-label').map(l => l.text())).toContain('Messages')
  })

  it('fails open when /info carries no features object (older backend)', async () => {
    const wrapper = await mountLayout(undefined)

    const text = wrapper.text()
    expect(text).toContain('Inbound Emails')
    expect(text).toContain('Forms')
    expect(text).toContain('Spam Filters')
  })

  it('fails open when /info itself fails', async () => {
    infoGet.mockRejectedValue(new Error('info down'))
    const wrapper = mount(DashboardLayout, {
      global: { stubs: { RouterLink: { template: '<a><slot /></a>' }, RouterView: true } },
    })
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('Inbound Emails')
    expect(text).toContain('Forms')
    expect(text).toContain('Emails')
  })
})
