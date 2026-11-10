import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The OB-5 Mailbox view (ADR-005 decision 2, W4): the assertions target the
// list contract — folder tree with unseen counters, the unread/flagged/search
// filters reaching GET /mail/messages with their handler query names, and the
// bulk flag/delete actions calling the flags/delete endpoints per selected UID.
const mailApi = vi.hoisted(() => ({
  mailboxes: vi.fn(),
  folders: vi.fn(),
  createFolder: vi.fn(),
  patchFolder: vi.fn(),
  deleteFolder: vi.fn(),
  expungeFolder: vi.fn(),
  messages: vi.fn(),
  message: vi.fn(),
  attachmentUrl: vi.fn(),
  updateFlags: vi.fn(),
  moveMessage: vi.fn(),
  copyMessage: vi.fn(),
  deleteMessage: vi.fn(),
  search: vi.fn(),
}))
vi.mock('../../api/mail', () => ({ mailApi, formatBytes: (n: number) => `${n} B`, hasFlag: (m: { flags_list?: string[] }, f: string) => (m.flags_list ?? []).includes(f) }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

const confirm = vi.hoisted(() => vi.fn())
vi.mock('../../composables/useConfirm', () => ({ useConfirm: () => ({ confirm }) }))

const routerPush = vi.hoisted(() => vi.fn())
const routeParams = vi.hoisted(() => ({ folderId: '11' }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: routeParams, query: {} }),
  useRouter: () => ({ push: routerPush }),
}))

import Mailbox from './Mailbox.vue'

function mailboxRow(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    user_id: 101,
    name: 'ada@example.test',
    quota_bytes: 1073741824,
    quota_messages: 100,
    used_bytes: 536870912,
    message_count: 42,
    blob_bytes: 1000,
    distinct_blobs: 2,
    status: 'active',
    created_at: '2026-11-01T08:00:00Z',
    ...overrides,
  }
}

function folder(overrides: Record<string, unknown> = {}) {
  return {
    id: 11,
    mailbox_id: 7,
    parent_id: null,
    name: 'INBOX',
    imap_path: 'INBOX',
    uid_validity: 1,
    uid_next: 10,
    subscribed: true,
    total_count: 3,
    unseen_count: 2,
    created_at: '2026-11-01T08:00:00Z',
    ...overrides,
  }
}

function summary(overrides: Record<string, unknown> = {}) {
  return {
    id: 500,
    uid: 101,
    folder_id: 11,
    message_id: '<m1@example.test>',
    subject: 'Quarterly report',
    from: 'bob@example.test',
    to: 'ada@example.test',
    date: '2026-11-07T10:00:00Z',
    size: 2048,
    flags: 0,
    flags_list: [],
    mod_seq: 3,
    snippet: 'Please review the attached numbers',
    has_attachments: false,
    created_at: '2026-11-07T10:00:00Z',
    ...overrides,
  }
}

function page(messages: ReturnType<typeof summary>[], total = messages.length) {
  return {
    messages,
    pageable: { current_page: 1, size: 25, total_pages: Math.max(1, Math.ceil(total / 25)), total_elements: total, empty: messages.length === 0 },
  }
}

async function mountMailbox() {
  const wrapper = mount(Mailbox, { global: { stubs: { RouterLink: true, Pagination: true } } })
  await flushPromises()
  return wrapper
}

function buttonByText(wrapper: Awaited<ReturnType<typeof mountMailbox>>, text: string) {
  return wrapper.findAll('button').find((b) => b.text().includes(text))
}

beforeEach(() => {
  vi.clearAllMocks()
  mailApi.mailboxes.mockResolvedValue({ data: { success: true, data: [mailboxRow()] } })
  mailApi.folders.mockResolvedValue({
    data: {
      success: true,
      data: [
        folder(),
        folder({ id: 12, name: 'Sent', imap_path: 'Sent', total_count: 1, unseen_count: 0 }),
        folder({ id: 13, name: 'Archive/2026', imap_path: 'Archive/2026', parent_id: 14, total_count: 0, unseen_count: 0 }),
        folder({ id: 14, name: 'Archive', imap_path: 'Archive', total_count: 0, unseen_count: 0 }),
      ],
    },
  })
  mailApi.messages.mockResolvedValue(page([summary()]))
  mailApi.updateFlags.mockResolvedValue({ data: { success: true, data: { uid: 101, flags: 1, mod_seq: 4 } } })
  mailApi.deleteMessage.mockResolvedValue({ data: { success: true, data: { message: 'message marked deleted' } } })
  confirm.mockResolvedValue(true)
})

describe('Mailbox view (OB-5 W4)', () => {
  it('renders the folder tree with unseen counters and the message list', async () => {
    const wrapper = await mountMailbox()

    expect(wrapper.text()).toContain('INBOX')
    expect(wrapper.text()).toContain('Sent')
    // Children nest under their parent, parents before children.
    const folderNames = wrapper.findAll('.mail-folder-name').map((n) => n.text())
    expect(folderNames.indexOf('Archive')).toBeLessThan(folderNames.indexOf('Archive/2026'))
    // Unseen badge on INBOX (2 unseen), none on Sent.
    expect(wrapper.find('.mail-unseen').text()).toBe('2')

    expect(wrapper.text()).toContain('Quarterly report')
    expect(wrapper.text()).toContain('bob@example.test')
    expect(wrapper.text()).toContain('Please review the attached numbers')
    // The listing was fetched for the folder the route names.
    expect(mailApi.messages).toHaveBeenCalledWith(expect.objectContaining({ folder: 11, page: 1 }))
  })

  it('applies the unread, flagged and full-text filters with the handler query names', async () => {
    const wrapper = await mountMailbox()

    const unread = wrapper.find('input[type="checkbox"]')
    await unread.setValue(true)
    await flushPromises()
    expect(mailApi.messages).toHaveBeenLastCalledWith(expect.objectContaining({ folder: 11, unread: true }))

    const checkboxes = wrapper.findAll('.mail-filters input[type="checkbox"]')
    await checkboxes[1].setValue(true)
    await flushPromises()
    expect(mailApi.messages).toHaveBeenLastCalledWith(expect.objectContaining({ folder: 11, unread: true, flagged: true }))

    await wrapper.find('.mail-search input').setValue('invoice')
    await wrapper.find('.mail-search').trigger('submit')
    await flushPromises()
    expect(mailApi.messages).toHaveBeenLastCalledWith(expect.objectContaining({ folder: 11, q: 'invoice', unread: true, flagged: true }))
  })

  it('marks selected messages read through the flags endpoint', async () => {
    mailApi.messages.mockResolvedValue(page([summary({ uid: 101, flags_list: [] }), summary({ uid: 102, id: 501, message_id: '<m2@example.test>' })]))
    const wrapper = await mountMailbox()

    const checkboxes = wrapper.findAll('tbody input[type="checkbox"]')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await buttonByText(wrapper, 'Mark read')!.trigger('click')
    await flushPromises()

    expect(mailApi.updateFlags).toHaveBeenCalledTimes(2)
    expect(mailApi.updateFlags).toHaveBeenCalledWith(101, 11, { seen: true })
    expect(mailApi.updateFlags).toHaveBeenCalledWith(102, 11, { seen: true })
    // Counters refresh after the bulk action.
    expect(mailApi.folders).toHaveBeenCalledTimes(2)
  })

  it('marks selected messages unread and flags them with the same path', async () => {
    mailApi.messages.mockResolvedValue(page([summary({ uid: 101, flags_list: ['seen'] })]))
    const wrapper = await mountMailbox()

    await wrapper.findAll('tbody input[type="checkbox"]')[0].setValue(true)
    await buttonByText(wrapper, 'Mark unread')!.trigger('click')
    await flushPromises()
    expect(mailApi.updateFlags).toHaveBeenCalledWith(101, 11, { seen: false })

    // The reload after a bulk action clears the selection — re-select before
    // the flag flip, exactly like the UI requires.
    await wrapper.findAll('tbody input[type="checkbox"]')[0].setValue(true)
    await buttonByText(wrapper, 'Flag')!.trigger('click')
    await flushPromises()
    expect(mailApi.updateFlags).toHaveBeenLastCalledWith(101, 11, { flagged: true })
  })

  it('deletes selected messages only behind the danger confirmation', async () => {
    mailApi.messages.mockResolvedValue(page([summary({ uid: 101 }), summary({ uid: 102, id: 501 })]))
    const wrapper = await mountMailbox()

    await wrapper.findAll('tbody input[type="checkbox"]')[1].setValue(true)
    await buttonByText(wrapper, 'Delete')!.trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm.mock.calls[0][0].variant).toBe('danger')
    expect(mailApi.deleteMessage).toHaveBeenCalledTimes(1)
    expect(mailApi.deleteMessage).toHaveBeenCalledWith(102, 11)
    expect(notify.success).toHaveBeenCalledWith('1 message deleted')
  })

  it('keeps the messages when the delete confirmation is declined', async () => {
    mailApi.messages.mockResolvedValue(page([summary({ uid: 101 })]))
    confirm.mockResolvedValue(false)
    const wrapper = await mountMailbox()

    await wrapper.findAll('tbody input[type="checkbox"]')[0].setValue(true)
    await buttonByText(wrapper, 'Delete')!.trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(mailApi.deleteMessage).not.toHaveBeenCalled()
  })

  it('moves selected messages to the chosen folder', async () => {
    mailApi.messages.mockResolvedValue(page([summary({ uid: 101 })]))
    const wrapper = await mountMailbox()

    await wrapper.findAll('tbody input[type="checkbox"]')[0].setValue(true)
    const select = wrapper.find('.mail-bulk-move')
    await select.setValue('12')
    await select.trigger('change')
    await flushPromises()

    expect(mailApi.moveMessage).toHaveBeenCalledWith(101, 11, 12)
  })

  it('lands on the first folder (INBOX first) when the route names none', async () => {
    routeParams.folderId = ''
    const wrapper = await mountMailbox()

    // The mailbox redirect goes to the first folder in tree order (INBOX) and
    // no listing is fetched for the folder-less route.
    expect(routerPush).toHaveBeenCalledWith({ name: 'mail-folder', params: { folderId: '11' } })
    expect(mailApi.messages).not.toHaveBeenCalled()
    void wrapper
    routeParams.folderId = '11'
  })

  it('renders the empty search state when nothing matches', async () => {
    mailApi.messages.mockResolvedValue(page([]))
    const wrapper = await mountMailbox()

    expect(wrapper.text()).toContain('No messages')
  })
})
