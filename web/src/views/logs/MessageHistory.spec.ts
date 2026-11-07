import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// MessageHistory: the SF-53 classification-breakdown card — visible when the
// analytics endpoint answers counts, hidden when it does not.
const logsApi = vi.hoisted(() => ({
  listMessages: vi.fn(),
  getClassifications: vi.fn(),
}))
vi.mock('../../api/logs', () => ({ logsApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

import MessageHistory from './MessageHistory.vue'

function paginated(data: unknown[]) {
  return {
    data: {
      success: true,
      data,
      pageable: { current_page: 0, size: 20, total_pages: 1, total_elements: data.length, empty: data.length === 0 },
    },
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  logsApi.listMessages.mockResolvedValue(paginated([]))
})

function breakdown(counts: Record<string, number>, total: number) {
  return { data: { success: true, data: { counts, total } } }
}

async function mountHistory() {
  const wrapper = mount(MessageHistory, {
    global: { stubs: { Pagination: true } },
  })
  await flushPromises()
  return wrapper
}

describe('MessageHistory classification card (SF-53)', () => {
  it('renders the classification breakdown with counts', async () => {
    logsApi.getClassifications.mockResolvedValue(
      breakdown({ bad_domain: 4, mailbox_not_found: 2, '(none)': 1 }, 7),
    )
    const wrapper = await mountHistory()

    const card = wrapper.find('.classification-card')
    expect(card.exists()).toBe(true)
    const text = card.text()
    expect(text).toContain('Bad domain')
    expect(text).toContain('4')
    expect(text).toContain('Mailbox not found')
    expect(text).toContain('7 classified events')
  })

  it('stays hidden when the analytics service is unavailable', async () => {
    logsApi.getClassifications.mockRejectedValue(new Error('503'))
    const wrapper = await mountHistory()

    expect(wrapper.find('.classification-card').exists()).toBe(false)
  })

  it('stays hidden when nothing is classified', async () => {
    logsApi.getClassifications.mockResolvedValue(breakdown({}, 0))
    const wrapper = await mountHistory()

    expect(wrapper.find('.classification-card').exists()).toBe(false)
  })
})
