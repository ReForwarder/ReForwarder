import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The OB-5 MessageView (ADR-005 decisions 2 + 5, W4): the assertions target
// rendering security and message actions — the sandboxed iframe with the
// remote-content-blocked default and its per-message opt-in, the
// authenticated attachment download URLs, and reply/forward handoff to the
// composer through the reply-frame endpoint.
const mailApi = vi.hoisted(() => ({
  mailboxes: vi.fn(),
  folders: vi.fn(),
  messages: vi.fn(),
  message: vi.fn(),
  attachmentUrl: vi.fn(
    (uid: number, index: number, folderId: number) =>
      `/api/v1/mail/messages/${uid}/attachments/${index}?folder=${folderId}`,
  ),
  updateFlags: vi.fn(),
  moveMessage: vi.fn(),
  deleteMessage: vi.fn(),
}))
vi.mock('../../api/mail', () => ({ mailApi, formatBytes: (n: number) => `${n} B`, hasFlag: (m: { flags_list?: string[] }, f: string) => (m.flags_list ?? []).includes(f) }))

const mailComposeApi = vi.hoisted(() => ({
  replyFrame: vi.fn(),
  deliveryStatus: vi.fn(),
  uploadAttachment: vi.fn(),
  send: vi.fn(),
  saveDraft: vi.fn(),
  updateDraft: vi.fn(),
  sendDraft: vi.fn(),
}))
vi.mock('../../api/mailCompose', () => ({ mailComposeApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

const confirm = vi.hoisted(() => vi.fn())
vi.mock('../../composables/useConfirm', () => ({ useConfirm: () => ({ confirm }) }))

const routerPush = vi.hoisted(() => vi.fn())
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { folderId: '11', uid: '5' }, query: {} }),
  useRouter: () => ({ push: routerPush }),
}))

import MessageView from './MessageView.vue'

function mailboxRow() {
  return {
    id: 7,
    user_id: 101,
    name: 'ada@example.test',
    quota_bytes: 0,
    quota_messages: 0,
    used_bytes: 0,
    message_count: 1,
    blob_bytes: 0,
    distinct_blobs: 0,
    status: 'active',
    created_at: '2026-11-01T08:00:00Z',
  }
}

function folderRow(overrides: Record<string, unknown> = {}) {
  return {
    id: 11,
    mailbox_id: 7,
    parent_id: null,
    name: 'INBOX',
    imap_path: 'INBOX',
    uid_validity: 1,
    uid_next: 6,
    subscribed: true,
    total_count: 1,
    unseen_count: 1,
    created_at: '2026-11-01T08:00:00Z',
    ...overrides,
  }
}

function message(overrides: Record<string, unknown> = {}) {
  return {
    id: 900,
    uid: 5,
    folder_id: 11,
    message_id: '<orig@example.test>',
    subject: 'Quarterly report',
    from: 'bob@example.test',
    to: 'ada@example.test',
    date: '2026-11-07T10:00:00Z',
    size: 2048,
    flags: 0,
    flags_list: [],
    mod_seq: 3,
    body: {
      text: 'Please review the attached numbers.',
      html: '<p>Quarterly numbers are in.</p><img src="https://tracker.example.test/pixel.png">',
      has_html: true,
    },
    attachments: [
      { index: 0, name: 'numbers.pdf', content_type: 'application/pdf', size: 12345 },
    ],
    created_at: '2026-11-07T10:00:00Z',
    ...overrides,
  }
}

function ok(data: unknown) {
  return { data: { success: true, data } }
}

async function mountView() {
  const wrapper = mount(MessageView)
  await flushPromises()
  return wrapper
}

function buttonByText(wrapper: Awaited<ReturnType<typeof mountView>>, text: string) {
  return wrapper.findAll('button').find((b) => b.text().includes(text))
}

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
  mailApi.mailboxes.mockResolvedValue(ok([mailboxRow()]))
  mailApi.folders.mockResolvedValue(
    ok([folderRow(), folderRow({ id: 12, name: 'Sent', imap_path: 'Sent', total_count: 0, unseen_count: 0 })]),
  )
  mailApi.message.mockResolvedValue(ok(message()))
  mailApi.updateFlags.mockResolvedValue(ok({ uid: 5, flags: 4, mod_seq: 5 }))
  mailApi.deleteMessage.mockResolvedValue(ok({ message: 'message marked deleted' }))
  mailComposeApi.replyFrame.mockResolvedValue(
    ok({
      mode: 'reply',
      mailbox_id: 7,
      folder_id: 11,
      uid: 5,
      to: ['bob@example.test'],
      cc: [],
      subject: 'Re: Quarterly report',
      quote_text: 'Please review the attached numbers.',
      original: { from: 'bob@example.test', date: '2026-11-07T10:00:00Z', subject: 'Quarterly report' },
    }),
  )
  confirm.mockResolvedValue(true)
})

describe('MessageView (OB-5 W4)', () => {
  it('renders the metadata header with sender, recipient and subject', async () => {
    const wrapper = await mountView()

    expect(wrapper.text()).toContain('Quarterly report')
    expect(wrapper.text()).toContain('bob@example.test')
    expect(wrapper.text()).toContain('ada@example.test')
    expect(wrapper.text()).toContain('Remote content is blocked for this message.')
  })

  it('renders the HTML body in a sandboxed iframe with remote content blocked by default', async () => {
    const wrapper = await mountView()

    const iframe = wrapper.find('iframe')
    expect(iframe.exists()).toBe(true)
    // The Logs content-iframe pattern: sandbox="" — no scripts, no same-origin.
    expect(iframe.attributes('sandbox')).toBe('')
    const srcdoc = iframe.attributes('srcdoc')
    // The CSP meta blocks remote subresources and scripts; the sanitized body
    // (the server already stripped the remote img) rides along.
    expect(srcdoc).toContain("http-equiv=\"Content-Security-Policy\"")
    expect(srcdoc).toContain("script-src 'none'")
    expect(srcdoc).toContain('img-src data:')
    expect(srcdoc).toContain('<p>Quarterly numbers are in.</p>')
  })

  it('relaxes the iframe CSP only after the per-message load-remote opt-in', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Load remote content')!.trigger('click')
    await flushPromises()

    const srcdoc = wrapper.find('iframe').attributes('srcdoc')
    expect(srcdoc).toContain('img-src https: http: data:')
    // Scripts stay dead even after the opt-in.
    expect(srcdoc).toContain("script-src 'none'")
    expect(wrapper.text()).toContain('Remote content allowed for this message.')
  })

  it('falls back to the plain-text body when the message has no HTML', async () => {
    mailApi.message.mockResolvedValue(
      ok(message({ body: { text: 'Plain text only', html: '', has_html: false } })),
    )
    const wrapper = await mountView()

    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.find('pre').text()).toContain('Plain text only')
  })

  it('offers authenticated attachment downloads addressed by folder, UID and index', async () => {
    const wrapper = await mountView()

    const link = wrapper.find('.message-attachment-link')
    expect(link.exists()).toBe(true)
    expect(link.attributes('href')).toBe('/api/v1/mail/messages/5/attachments/0?folder=11')
    // The download attribute keeps the browser from navigating to the file.
    expect(link.attributes('download')).toBe('numbers.pdf')
    expect(link.text()).toContain('numbers.pdf')
  })

  it('prefills the composer through the reply-frame endpoint and hands it over', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Reply')!.trigger('click')
    await flushPromises()

    expect(mailComposeApi.replyFrame).toHaveBeenCalledWith(5, 11, 7, 'reply')
    const handoff = JSON.parse(sessionStorage.getItem('posta_mail_compose_prefill') ?? '{}')
    expect(handoff.subject).toBe('Re: Quarterly report')
    expect(handoff.mailbox_id).toBe(7)
    expect(routerPush).toHaveBeenCalledWith({ name: 'mail-compose', query: { prefill: 'reply' } })
  })

  it('passes reply_all and forward modes to the reply-frame endpoint', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Forward')!.trigger('click')
    await flushPromises()
    expect(mailComposeApi.replyFrame).toHaveBeenLastCalledWith(5, 11, 7, 'forward')

    await buttonByText(wrapper, 'Reply all')!.trigger('click')
    await flushPromises()
    expect(mailComposeApi.replyFrame).toHaveBeenLastCalledWith(5, 11, 7, 'reply_all')
  })

  it('flags and unflags through the flags endpoint with the folder in scope', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Flag')!.trigger('click')
    await flushPromises()
    expect(mailApi.updateFlags).toHaveBeenCalledWith(5, 11, { flagged: true })

    mailApi.message.mockResolvedValue(ok(message({ flags_list: ['flagged'] })))
    await buttonByText(wrapper, 'Unflag')!.trigger('click')
    await flushPromises()
    expect(mailApi.updateFlags).toHaveBeenLastCalledWith(5, 11, { flagged: false })
  })

  it('deletes the message only behind the danger confirmation', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Delete')!.trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm.mock.calls[0][0].variant).toBe('danger')
    expect(mailApi.deleteMessage).toHaveBeenCalledWith(5, 11)
    expect(routerPush).toHaveBeenCalledWith({ name: 'mail-folder', params: { folderId: '11' } })
  })

  it('moves the message to the selected folder and returns to the list', async () => {
    const wrapper = await mountView()
    await wrapper.find('.message-move-select').setValue('12')
    await wrapper.find('.message-move-select').trigger('change')
    await flushPromises()

    expect(mailApi.moveMessage).toHaveBeenCalledWith(5, 11, 12)
    expect(routerPush).toHaveBeenCalledWith({ name: 'mail-folder', params: { folderId: '11' } })
  })

  it('reads a foreign message as not found', async () => {
    const notFound: any = new Error('not found')
    notFound.response = { status: 404 }
    mailApi.message.mockRejectedValue(notFound)
    const wrapper = await mountView()

    expect(wrapper.text()).toContain('Message not found')
  })
})
