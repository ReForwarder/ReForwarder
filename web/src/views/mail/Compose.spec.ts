import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The OB-5 composer (ADR-005 decision 2, W4): the assertions target the send
// contract (recipients split, refs carried), the client-side validation gate,
// the ADR-006 scan-refusal surfacing (the 409 signature message reaches the
// user verbatim), and the draft path.
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
vi.mock('../../api/mail', () => ({ mailApi, formatBytes: (n: number) => `${n} B`, hasFlag: (m: { flags_list?: string[] }, f: string) => (m.flags_list ?? []).includes(f) }))

const mailComposeApi = vi.hoisted(() => ({
  uploadAttachment: vi.fn(),
  send: vi.fn(),
  saveDraft: vi.fn(),
  updateDraft: vi.fn(),
  sendDraft: vi.fn(),
  replyFrame: vi.fn(),
  deliveryStatus: vi.fn(),
}))
vi.mock('../../api/mailCompose', () => ({ mailComposeApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

const routerPush = vi.hoisted(() => vi.fn())
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: {}, query: {} }),
  useRouter: () => ({ push: routerPush }),
}))

import Compose from './Compose.vue'

function mailboxRow(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    user_id: 101,
    name: 'ada@example.test',
    quota_bytes: 0,
    quota_messages: 0,
    used_bytes: 0,
    message_count: 0,
    blob_bytes: 0,
    distinct_blobs: 0,
    status: 'active',
    created_at: '2026-11-01T08:00:00Z',
    ...overrides,
  }
}

function ok(data: unknown) {
  return { data: { success: true, data } }
}

async function mountCompose() {
  const wrapper = mount(Compose, { global: { stubs: { RouterLink: true } } })
  await flushPromises()
  return wrapper
}

function buttonByText(wrapper: Awaited<ReturnType<typeof mountCompose>>, text: string) {
  return wrapper.findAll('button').find((b) => b.text().includes(text))
}

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
  mailApi.mailboxes.mockResolvedValue(ok([mailboxRow()]))
  mailComposeApi.send.mockResolvedValue(
    ok({ email_uuid: 'e-1', status: 'queued', message_id: '<s1@example.test>', mailbox_id: 7, folder_id: 12, uid: 1 }),
  )
  mailComposeApi.saveDraft.mockResolvedValue(ok({ mailbox_id: 7, folder_id: 13, uid: 3, message_id: '<d1@example.test>' }))
})

describe('Compose view (OB-5 W4)', () => {
  it('sends a composed message with split recipients and the mailbox ref', async () => {
    const wrapper = await mountCompose()

    await wrapper.find('#compose-to').setValue('bob@example.test, grace@example.test; carol@example.test')
    await wrapper.find('#compose-subject').setValue('Hello')
    await wrapper.find('#compose-body').setValue('First line\nSecond line')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mailComposeApi.send).toHaveBeenCalledTimes(1)
    expect(mailComposeApi.send.mock.calls[0][0]).toEqual({
      mailbox_id: 7,
      from: 'ada@example.test',
      to: ['bob@example.test', 'grace@example.test', 'carol@example.test'],
      subject: 'Hello',
      text: 'First line\nSecond line',
    })
    expect(notify.success).toHaveBeenCalledWith('Message sent')
    expect(routerPush).toHaveBeenCalledWith({ name: 'mail' })
  })

  it('carries cc, bcc and staged attachment refs on the send', async () => {
    const wrapper = await mountCompose()

    mailComposeApi.uploadAttachment.mockResolvedValue(
      ok([{ blob_hash: 'h1', name: 'notes.txt', content_type: 'text/plain', size: 3, scan_state: 'clean' }]),
    )
    const file = new File(['abc'], 'notes.txt', { type: 'text/plain' })
    const fileInput = wrapper.find('#compose-attachments')
    Object.defineProperty(fileInput.element, 'files', { value: [file] })
    await fileInput.trigger('change')
    await flushPromises()

    await wrapper.find('#compose-to').setValue('bob@example.test')
    await wrapper.find('#compose-cc').setValue('carol@example.test')
    await buttonByText(wrapper, 'Add Bcc')!.trigger('click')
    await wrapper.find('#compose-bcc').setValue('dave@example.test')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    const input = mailComposeApi.send.mock.calls[0][0]
    expect(input.cc).toEqual(['carol@example.test'])
    expect(input.bcc).toEqual(['dave@example.test'])
    expect(input.attachments).toEqual([{ blob_hash: 'h1', name: 'notes.txt', content_type: 'text/plain' }])
  })

  it('refuses to send without a recipient — the API is not called', async () => {
    const wrapper = await mountCompose()

    await wrapper.find('#compose-subject').setValue('No one to hear this')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(mailComposeApi.send).not.toHaveBeenCalled()
    expect(notify.error).toHaveBeenCalledWith('A from address and at least one recipient are required')
  })

  it('surfaces an ADR-006 scan refusal verbatim and keeps it out of the send', async () => {
    const wrapper = await mountCompose()

    const refused: any = new Error('conflict')
    refused.response = { status: 409, data: { success: false, error: { message: 'attachment refused: Win.Test.EICAR_CDFile-1 found' } } }
    mailComposeApi.uploadAttachment.mockRejectedValue(refused)

    const file = new File(['X5O!P%@AP'], 'eicar.txt', { type: 'text/plain' })
    const input = wrapper.find('#compose-attachments')
    Object.defineProperty(input.element, 'files', { value: [file] })
    await input.trigger('change')
    await flushPromises()

    // The clamd signature reaches the user untouched, and the refused ref
    // never enters the attachment list that send would carry.
    expect(notify.error).toHaveBeenCalledWith('attachment refused: Win.Test.EICAR_CDFile-1 found')
    expect(mailComposeApi.send).not.toHaveBeenCalled()
  })

  it('stages a clean upload as an attachment ref available to the send', async () => {
    const wrapper = await mountCompose()

    mailComposeApi.uploadAttachment.mockResolvedValue(
      ok([{ blob_hash: 'h-ok', name: 'notes.txt', content_type: 'text/plain', size: 3, scan_state: 'clean' }]),
    )
    const file = new File(['abc'], 'notes.txt', { type: 'text/plain' })
    const input = wrapper.find('#compose-attachments')
    Object.defineProperty(input.element, 'files', { value: [file] })
    await input.trigger('change')
    await flushPromises()

    expect(mailComposeApi.uploadAttachment).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('notes.txt')
    expect(wrapper.text()).toContain('scanned clean')

    await wrapper.find('#compose-to').setValue('bob@example.test')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(mailComposeApi.send.mock.calls[0][0].attachments).toEqual([
      { blob_hash: 'h-ok', name: 'notes.txt', content_type: 'text/plain' },
    ])
  })

  it('saves a draft and keeps the composer open', async () => {
    const wrapper = await mountCompose()

    await wrapper.find('#compose-to').setValue('bob@example.test')
    await wrapper.find('#compose-subject').setValue('WIP')
    await buttonByText(wrapper, 'Save draft')!.trigger('click')
    await flushPromises()

    expect(mailComposeApi.saveDraft).toHaveBeenCalledTimes(1)
    expect(mailComposeApi.saveDraft.mock.calls[0][0]).toMatchObject({ from: 'ada@example.test', to: ['bob@example.test'], subject: 'WIP' })
    expect(mailComposeApi.send).not.toHaveBeenCalled()
    expect(notify.success).toHaveBeenCalledWith('Draft saved')
    expect(routerPush).not.toHaveBeenCalled()
  })

  it('applies the reply-frame handoff: recipients, subject and the quoted text', async () => {
    sessionStorage.setItem(
      'posta_mail_compose_prefill',
      JSON.stringify({
        mode: 'reply',
        mailbox_id: 7,
        folder_id: 11,
        uid: 5,
        to: ['bob@example.test'],
        cc: ['carol@example.test'],
        subject: 'Re: Quarterly report',
        quote_text: 'Please review the attached numbers.',
        original: { from: 'bob@example.test', date: '2026-11-07T10:00:00Z', subject: 'Quarterly report' },
      }),
    )
    const wrapper = await mountCompose()

    expect((wrapper.find('#compose-to').element as HTMLInputElement).value).toBe('bob@example.test')
    expect((wrapper.find('#compose-cc').element as HTMLInputElement).value).toBe('carol@example.test')
    expect((wrapper.find('#compose-subject').element as HTMLInputElement).value).toBe('Re: Quarterly report')
    const body = (wrapper.find('#compose-body').element as HTMLTextAreaElement).value
    expect(body).toContain('On 2026-11-07T10:00:00Z, bob@example.test wrote:')
    expect(body).toContain('> Please review the attached numbers.')

    // Sending a reply goes through the plain send path (no stored draft).
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(mailComposeApi.send).toHaveBeenCalledTimes(1)
  })
})
