import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The OB-5 mail settings view (ADR-005 decision 3 self-service, W4): display
// name through the existing PUT /users/me profile endpoint, the signature
// (per-device storage — no server-side settings endpoint exists in this
// wave's surface), the quota/usage display from GET /mail/mailboxes, and the
// link into the Profile card's app-password management.
const mailApi = vi.hoisted(() => ({
  mailboxes: vi.fn(),
  folders: vi.fn(),
  messages: vi.fn(),
  message: vi.fn(),
  attachmentUrl: vi.fn(),
  updateFlags: vi.fn(),
  moveMessage: vi.fn(),
  copyMessage: vi.fn(),
  deleteMessage: vi.fn(),
  search: vi.fn(),
}))
vi.mock('../../api/mail', () => ({
  mailApi,
  formatBytes: (n: number) => (n >= 1073741824 ? `${n / 1073741824} GiB` : `${Math.round(n / 1048576)} MiB`),
  hasFlag: (m: { flags_list?: string[] }, f: string) => (m.flags_list ?? []).includes(f),
}))

const authApi = vi.hoisted(() => ({
  updateProfile: vi.fn(),
}))
vi.mock('../../api/auth', () => ({ authApi }))

const authStore = vi.hoisted(() => ({
  user: { name: 'Ada Lovelace', email: 'ada@example.test' },
  setUser: vi.fn(),
}))
vi.mock('../../stores/auth', () => ({ useAuthStore: () => authStore }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: {}, query: {} }),
  useRouter: () => ({ push: vi.fn() }),
}))

import MailSettings from './MailSettings.vue'

function mailboxRow(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    user_id: 101,
    name: 'ada@example.test',
    quota_bytes: 2 * 1073741824,
    quota_messages: 100,
    used_bytes: 1073741824,
    message_count: 42,
    blob_bytes: 800,
    distinct_blobs: 30,
    status: 'active',
    created_at: '2026-11-01T08:00:00Z',
    ...overrides,
  }
}

function ok(data: unknown) {
  return { data: { success: true, data } }
}

// RouterLink stub that renders its slot so the link text stays assertable,
// carrying the raw `to` target as a data attribute.
const RouterLinkSlotStub = {
  name: 'RouterLink',
  props: ['to'],
  template: '<a :data-to="JSON.stringify(to)"><slot /></a>',
}

async function mountSettings() {
  const wrapper = mount(MailSettings, { global: { stubs: { RouterLink: RouterLinkSlotStub } } })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  authStore.user = { name: 'Ada Lovelace', email: 'ada@example.test' }
  mailApi.mailboxes.mockResolvedValue(ok([mailboxRow()]))
  authApi.updateProfile.mockResolvedValue(ok({ name: 'Grace Hopper' }))
})

describe('MailSettings view (OB-5 W4)', () => {
  it('displays mailbox quota and usage with the ADR-003 §5 counters', async () => {
    const wrapper = await mountSettings()

    expect(wrapper.text()).toContain('ada@example.test')
    // Whitespace between the interpolated template chunks is collapsed here.
    expect(wrapper.text()).toMatch(/1 GiB\s+of 2 GiB \(50%\)/)
    expect(wrapper.text()).toMatch(/42\s+of 100 \(42%\)/)
    expect(wrapper.text()).toMatch(/0 MiB across 30 blobs/)
    expect(wrapper.text()).toContain('active')
  })

  it('saves the display name through the existing profile endpoint', async () => {
    const wrapper = await mountSettings()

    await wrapper.find('#mail-display-name').setValue('Grace Hopper')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(authApi.updateProfile).toHaveBeenCalledWith({ name: 'Grace Hopper' })
    expect(authStore.setUser).toHaveBeenCalledWith({ name: 'Grace Hopper' })
    expect(notify.success).toHaveBeenCalledWith('Display name updated')
  })

  it('refuses an empty display name without calling the API', async () => {
    const wrapper = await mountSettings()

    await wrapper.find('#mail-display-name').setValue('   ')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(authApi.updateProfile).not.toHaveBeenCalled()
    expect(notify.error).toHaveBeenCalledWith('Display name cannot be empty')
  })

  it('keeps the signature on this device and reports the limitation', async () => {
    const wrapper = await mountSettings()

    expect(wrapper.text()).toContain('Kept on this device')

    await wrapper.find('#mail-signature').setValue('— Ada')
    // Submit the signature form specifically (the second card's form).
    const forms = wrapper.findAll('form')
    await forms[1].trigger('submit')
    await flushPromises()

    expect(localStorage.getItem('posta_mail_signature')).toBe('— Ada')
    expect(notify.success).toHaveBeenCalledWith('Signature saved on this device')
  })

  it('links to the Profile card for app-password management', async () => {
    const wrapper = await mountSettings()

    const appPasswordLink = wrapper.findAll('a').find((l) => l.text().includes('Manage app passwords'))
    expect(appPasswordLink).toBeDefined()
    expect(JSON.parse(appPasswordLink!.attributes('data-to') ?? '{}')).toEqual({ name: 'profile' })
  })

  it('shows the no-mailbox state instead of quota cards', async () => {
    mailApi.mailboxes.mockResolvedValue(ok([]))
    const wrapper = await mountSettings()

    expect(wrapper.text()).toContain('No hosted mailboxes on this account.')
  })
})
