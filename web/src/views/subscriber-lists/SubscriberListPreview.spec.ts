import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The axios client is mocked one level below the api module so the real
// subscriberLists.ts runs and the spec pins the exact endpoint URL — the
// preview control used to POST to /preview while the backend registers
// /preview-segment, and no amount of component-level mocking would have
// caught that.
const apiGet = vi.hoisted(() => vi.fn())
const apiPost = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', () => ({
  default: { get: apiGet, post: apiPost },
}))

// The detail view reads the list id from the route.
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: '5' } }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

const notifyError = vi.hoisted(() => vi.fn())
const notifySuccess = vi.hoisted(() => vi.fn())

vi.mock('../../stores/notification', () => ({
  useNotificationStore: () => ({ error: notifyError, success: notifySuccess }),
}))

vi.mock('../../stores/workspace', () => ({
  useWorkspaceStore: () => ({ canEdit: true }),
}))

import SubscriberListDetail from './SubscriberListDetail.vue'

const FILTER_RULES = [
  { field: 'email', operator: 'contains', value: 'example.com' },
]

// The mock mimics the axios response: { data: { success, data, ... } }.
function listEnvelope() {
  return {
    data: {
      success: true,
      data: {
        id: 5,
        name: 'VIPs',
        description: 'High-value contacts',
        type: 'dynamic',
        filter_rules: FILTER_RULES,
        member_count: 0,
        created_at: '2026-11-01T00:00:00Z',
        updated_at: null,
      },
    },
  }
}

function membersEnvelope() {
  return {
    data: {
      success: true,
      data: [],
      pageable: { current_page: 0, size: 20, total_pages: 0, total_elements: 0, empty: true },
    },
  }
}

function previewEnvelope(count: number, subscribers: Array<{ id: number; email: string; name: string; status: string }>) {
  return {
    data: {
      success: true,
      data: { count, subscribers },
    },
  }
}

async function mountPage() {
  const wrapper = mount(SubscriberListDetail, {
    global: { stubs: { RouterLink: true } },
  })
  await flushPromises()
  return wrapper
}

function previewButton(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('button').find(b => b.text() === 'Preview Matches')!
}

beforeEach(() => {
  apiGet.mockReset()
  apiPost.mockReset()
  notifyError.mockClear()
  notifySuccess.mockClear()
  apiGet.mockImplementation((url: string) => {
    if (url === '/workspaces/current/subscriber-lists/5') return Promise.resolve(listEnvelope())
    if (url === '/workspaces/current/subscriber-lists/5/members') return Promise.resolve(membersEnvelope())
    return Promise.reject(new Error('unexpected GET ' + url))
  })
})

describe('SubscriberListDetail segment preview', () => {
  it('posts the list filter rules to /preview-segment and renders the live count', async () => {
    apiPost.mockResolvedValueOnce(previewEnvelope(7, [
      { id: 21, email: 'alice@example.com', name: 'Alice', status: 'subscribed' },
      { id: 22, email: 'bob@example.com', name: '', status: 'unsubscribed' },
    ]))
    const wrapper = await mountPage()

    await previewButton(wrapper).trigger('click')
    await flushPromises()

    // The endpoint the backend actually registers (R-21 regression pin).
    expect(apiPost).toHaveBeenCalledTimes(1)
    expect(apiPost).toHaveBeenCalledWith(
      '/workspaces/current/subscriber-lists/preview-segment',
      { filter_rules: FILTER_RULES },
    )

    const text = wrapper.text()
    expect(text).toContain('7 subscribers match these rules.')
    // The sample rows render with the minimally-shaped entries.
    expect(text).toContain('alice@example.com')
    expect(text).toContain('bob@example.com')
    expect(text).toContain('subscribed')
  })

  it('shows the zero-match state when no subscriber matches', async () => {
    apiPost.mockResolvedValueOnce(previewEnvelope(0, []))
    const wrapper = await mountPage()

    await previewButton(wrapper).trigger('click')
    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('No subscribers match these filters.')
    expect(text).not.toContain('Matching Subscribers')
  })

  it('caps the sample with a "showing first N" note when more matched', async () => {
    apiPost.mockResolvedValueOnce(previewEnvelope(40, [
      { id: 21, email: 'alice@example.com', name: 'Alice', status: 'subscribed' },
    ]))
    const wrapper = await mountPage()

    await previewButton(wrapper).trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Showing first 1 of 40.')
  })

  it('toasts an error when the preview call fails', async () => {
    apiPost.mockRejectedValueOnce({ response: { data: { error: { message: 'boom' } } } })
    const wrapper = await mountPage()

    await previewButton(wrapper).trigger('click')
    await flushPromises()

    expect(notifyError).toHaveBeenCalledWith('Failed to preview segment')
  })
})
