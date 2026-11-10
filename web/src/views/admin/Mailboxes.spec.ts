import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The OB-5 admin Mailboxes view (ADR-005 decision 3): the destructive
// actions are the assertion target — provisioning reaches the API only with
// a valid owner, suspension/reactivation flips the status through PATCH,
// and deletion runs strictly behind the confirm dialog.
const mailboxesApi = vi.hoisted(() => ({
  list: vi.fn(),
  provision: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
  usage: vi.fn(),
}))
vi.mock('../../api/mailboxes', () => ({ adminMailboxesApi: mailboxesApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

const confirm = vi.hoisted(() => vi.fn())
vi.mock('../../composables/useConfirm', () => ({ useConfirm: () => ({ confirm }) }))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

import Mailboxes from './Mailboxes.vue'

function mailbox(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    user_id: 101,
    owner_name: 'Alice',
    owner_email: 'alice@example.test',
    workspace_id: 77,
    name: 'alice@example.test',
    quota_bytes: 1024 * 1024 * 1024,
    quota_messages: 100,
    used_bytes: 512 * 1024 * 1024,
    message_count: 42,
    status: 'active',
    created_at: '2026-11-01T08:00:00Z',
    ...overrides,
  }
}

function paginated(data: unknown[]) {
  return {
    data: {
      success: true,
      data,
      pageable: { current_page: 0, size: 20, total_pages: 1, total_elements: data.length, empty: data.length === 0 },
    },
  }
}

async function mountMailboxes() {
  const wrapper = mount(Mailboxes, { global: { stubs: { Pagination: true } } })
  await flushPromises()
  return wrapper
}

function buttonByText(wrapper: Awaited<ReturnType<typeof mountMailboxes>>, text: string) {
  return wrapper.findAll('button').find((b) => b.text().includes(text))
}

beforeEach(() => {
  vi.clearAllMocks()
  confirm.mockResolvedValue(true)
  mailboxesApi.list.mockResolvedValue(paginated([]))
  mailboxesApi.usage.mockResolvedValue({
    data: { success: true, data: { mailbox_id: 1, user_id: 101, name: 'alice@example.test', quota_bytes: 1073741824, raw_bytes: 536870912, message_count: 42, blob_bytes: 1000, distinct_blobs: 2 } },
  })
})

describe('Mailboxes admin view (OB-5 W2)', () => {
  it('renders the mailbox table with owner identity and usage', async () => {
    mailboxesApi.list.mockResolvedValue(paginated([mailbox()]))
    const wrapper = await mountMailboxes()

    expect(wrapper.text()).toContain('alice@example.test')
    expect(wrapper.text()).toContain('Alice · alice@example.test')
    expect(wrapper.text()).toContain('512 MiB / 1 GiB')
    expect(wrapper.text()).toContain('50% used')
    expect(wrapper.text()).toContain('42 / 100')
    expect(wrapper.text()).toContain('active')
  })

  it('provisions a mailbox after validating the owner input', async () => {
    mailboxesApi.provision.mockResolvedValue({ data: { success: true, data: mailbox() } })
    const wrapper = await mountMailboxes()

    await buttonByText(wrapper, 'Provision Mailbox')!.trigger('click')
    await flushPromises()
    const submit = () => wrapper.find('.modal form').trigger('submit')

    // Missing owner: the API must not be called.
    await submit()
    await flushPromises()
    expect(mailboxesApi.provision).not.toHaveBeenCalled()
    expect(notify.error).toHaveBeenCalled()

    await wrapper.find('#mbx-user').setValue('101')
    await wrapper.find('#mbx-name').setValue('new@example.test')
    await wrapper.find('#mbx-quota').setValue('2')
    await submit()
    await flushPromises()

    expect(mailboxesApi.provision).toHaveBeenCalledTimes(1)
    expect(mailboxesApi.provision.mock.calls[0][0]).toMatchObject({
      user_id: 101,
      name: 'new@example.test',
      quota_bytes: 2 * 1024 * 1024 * 1024,
    })
    expect(notify.success).toHaveBeenCalledWith('Mailbox provisioned')
  })

  it('edits the quota through the usage-informed modal', async () => {
    mailboxesApi.list.mockResolvedValue(paginated([mailbox()]))
    mailboxesApi.update.mockResolvedValue({
      data: { success: true, data: mailbox({ quota_bytes: 2 * 1024 * 1024 * 1024 }) },
    })
    const wrapper = await mountMailboxes()

    await wrapper.find('[title="Edit quota"]').trigger('click')
    await flushPromises()
    expect(mailboxesApi.usage).toHaveBeenCalledWith(1)
    expect(wrapper.text()).toContain('In use:')

    await wrapper.find('#edit-quota').setValue('2')
    await wrapper.find('.modal form').trigger('submit')
    await flushPromises()

    expect(mailboxesApi.update).toHaveBeenCalledWith(1, {
      quota_bytes: 2 * 1024 * 1024 * 1024,
      quota_messages: 100,
    })
    expect(notify.success).toHaveBeenCalledWith('Mailbox updated')
  })

  it('suspends an active mailbox with a status PATCH', async () => {
    mailboxesApi.list.mockResolvedValue(paginated([mailbox()]))
    mailboxesApi.update.mockResolvedValue({ data: { success: true, data: mailbox({ status: 'suspended' }) } })
    const wrapper = await mountMailboxes()

    await wrapper.find('[title="Suspend mailbox"]').trigger('click')
    await flushPromises()

    expect(mailboxesApi.update).toHaveBeenCalledWith(1, { status: 'suspended' })
    expect(notify.success).toHaveBeenCalledWith('Mailbox suspended')
  })

  it('offers reactivation for a suspended mailbox', async () => {
    mailboxesApi.list.mockResolvedValue(paginated([mailbox({ status: 'suspended' })]))
    mailboxesApi.update.mockResolvedValue({ data: { success: true, data: mailbox() } })
    const wrapper = await mountMailboxes()

    await wrapper.find('[title="Reactivate mailbox"]').trigger('click')
    await flushPromises()

    expect(mailboxesApi.update).toHaveBeenCalledWith(1, { status: 'active' })
    expect(notify.success).toHaveBeenCalledWith('Mailbox reactivated')
  })

  it('deletes a mailbox only behind the danger confirmation', async () => {
    mailboxesApi.list.mockResolvedValue(paginated([mailbox()]))
    mailboxesApi.remove.mockResolvedValue({ data: { success: true, data: { message: 'deleted' } } })
    const wrapper = await mountMailboxes()

    await wrapper.find('[title="Delete mailbox"]').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm.mock.calls[0][0].variant).toBe('danger')
    expect(confirm.mock.calls[0][0].message).toContain('cannot be recovered')
    expect(mailboxesApi.remove).toHaveBeenCalledWith(1)
    expect(notify.success).toHaveBeenCalledWith('Mailbox deleted')
  })

  it('keeps the mailbox when the confirmation is declined', async () => {
    mailboxesApi.list.mockResolvedValue(paginated([mailbox()]))
    confirm.mockResolvedValue(false)
    const wrapper = await mountMailboxes()

    await wrapper.find('[title="Delete mailbox"]').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(mailboxesApi.remove).not.toHaveBeenCalled()
  })
})
