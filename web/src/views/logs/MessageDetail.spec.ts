import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// MessageDetail: the per-event classification badge (SF-53) and the SF-56
// live-trace stream — the page must open the per-message SSE stream and fold
// streamed rows into the timeline.
const logsApi = vi.hoisted(() => ({
  getMessage: vi.fn(),
  getMessageEvents: vi.fn(),
  getMessageContent: vi.fn(),
}))
vi.mock('../../api/logs', () => ({ logsApi }))

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { uuid: 'em-1' } }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

// happy-dom has no EventSource: the fake records the handshake URL and the
// registered listeners so tests can drive frames.
class FakeEventSource {
  static last: FakeEventSource | null = null
  url: string
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners = new Map<string, ((e: MessageEvent) => void)[]>()

  constructor(url: string) {
    this.url = url
    FakeEventSource.last = this
  }

  addEventListener(name: string, fn: (e: MessageEvent) => void) {
    const list = this.listeners.get(name) || []
    list.push(fn)
    this.listeners.set(name, list)
  }

  close = vi.fn()

  emit(name: string, data: unknown) {
    for (const fn of this.listeners.get(name) || []) {
      fn(new MessageEvent(name, { data: JSON.stringify(data) }))
    }
  }
}
vi.stubGlobal('EventSource', FakeEventSource)

import MessageDetail from './MessageDetail.vue'

function messageRow() {
  return {
    data: {
      success: true,
      data: {
        uuid: 'em-1',
        sender: 'news@posta.dev',
        recipients: ['alice@example.com'],
        subject: 'Welcome',
        status: 'sent',
        created_at: '2026-05-01T08:00:00Z',
      },
    },
  }
}

function eventRow(overrides: Record<string, unknown> = {}) {
  return {
    id: 5,
    email_uuid: 'em-1',
    type: 'bounced',
    occurred_at: '2026-05-01T08:05:00Z',
    provider: 'postmark',
    recipient: 'alice@example.com',
    metadata: '{}',
    created_at: '2026-05-01T08:05:00Z',
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  FakeEventSource.last = null
  logsApi.getMessage.mockResolvedValue(messageRow())
  logsApi.getMessageEvents.mockResolvedValue({ data: { success: true, data: [] } })
  logsApi.getMessageContent.mockResolvedValue({
    data: { success: true, data: { redacted: true } },
  })
})

describe('MessageDetail classification + live trace (SF-53 / SF-56)', () => {
  it('renders the per-event classification from the event metadata', async () => {
    logsApi.getMessageEvents.mockResolvedValue({
      data: {
        success: true,
        data: [eventRow({ metadata: '{"classification":"mailbox_not_found","smtp_code":550}' })],
      },
    })
    const wrapper = mount(MessageDetail)
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('bounced')
    expect(text).toContain('mailbox not found')
  })

  it('omits the classification marker for events without one', async () => {
    logsApi.getMessageEvents.mockResolvedValue({
      data: { success: true, data: [eventRow({ type: 'sent' })] },
    })
    const wrapper = mount(MessageDetail)
    await flushPromises()

    expect(wrapper.text()).toContain('sent')
    expect(wrapper.find('.event-classification').exists()).toBe(false)
  })

  it('opens the per-message SSE stream on mount', async () => {
    mount(MessageDetail)
    await flushPromises()

    expect(FakeEventSource.last).not.toBeNull()
    expect(FakeEventSource.last!.url).toContain('/workspaces/current/logs/em-1/events/stream')
    expect(FakeEventSource.last!.listeners.has('email.event')).toBe(true)
  })

  it('folds streamed rows into the timeline and marks it live', async () => {
    // One historical row so the timeline renders; the streamed row arrives
    // over the live feed.
    logsApi.getMessageEvents.mockResolvedValue({
      data: { success: true, data: [eventRow()] },
    })
    const wrapper = mount(MessageDetail)
    await flushPromises()

    const source = FakeEventSource.last!
    source.onopen?.()
    await flushPromises()
    expect(wrapper.find('.live-indicator').exists()).toBe(true)

    source.emit('email.event', eventRow({ id: 6, type: 'delivered', metadata: '{"classification":"delivered"}' }))
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('delivered')
    expect(wrapper.findAll('.event-classification').length).toBe(1)
  })

  it('closes the stream when the page unmounts', async () => {
    const wrapper = mount(MessageDetail)
    await flushPromises()

    const source = FakeEventSource.last!
    wrapper.unmount()
    expect(source.close).toHaveBeenCalled()
  })
})
