import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The axios client is mocked one level below the api module so the real
// subscriberLists.ts runs and the spec pins the exact endpoint URL — the same
// idiom as SubscriberListPreview.spec.ts. This sibling covers the double
// opt-in (RF-F075) surface, which needs a static list (the preview spec pins a
// dynamic one).
const apiGet = vi.hoisted(() => vi.fn())
const apiPost = vi.hoisted(() => vi.fn())
const apiPut = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', () => ({
  default: { get: apiGet, post: apiPost, put: apiPut, delete: vi.fn() },
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

// The mock mimics the axios response: { data: { success, data, ... } }.
function staticListEnvelope(doubleOptIn = false) {
  return {
    data: {
      success: true,
      data: {
        id: 5,
        name: 'VIPs',
        description: 'High-value contacts',
        type: 'static',
        double_opt_in: doubleOptIn,
        member_count: 0,
        created_at: '2026-11-01T00:00:00Z',
        updated_at: null,
      },
    },
  }
}

function member(id: number, email: string, memberStatus?: string) {
  return {
    id,
    email,
    name: '',
    status: 'subscribed',
    custom_fields: {},
    subscribed_at: null,
    unsubscribed_at: null,
    created_at: '2026-11-01T00:00:00Z',
    updated_at: null,
    // Only the list-members endpoint returns this; undefined = confirmed-era
    // row from before the flag existed.
    member_status: memberStatus,
  }
}

function membersEnvelope(members: ReturnType<typeof member>[]) {
  return {
    data: {
      success: true,
      data: members,
      pageable: {
        current_page: 0,
        size: 20,
        total_pages: members.length === 0 ? 0 : 1,
        total_elements: members.length,
        empty: members.length === 0,
      },
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

function resendButtons(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAll('button').filter(b => b.text() === 'Resend confirmation')
}

let membersData: ReturnType<typeof member>[] = []

beforeEach(() => {
  apiGet.mockReset()
  apiPost.mockReset()
  apiPut.mockReset()
  notifyError.mockClear()
  notifySuccess.mockClear()
  membersData = []
  apiGet.mockImplementation((url: string) => {
    if (url === '/workspaces/current/subscriber-lists/5') return Promise.resolve(staticListEnvelope())
    if (url === '/workspaces/current/subscriber-lists/5/members') return Promise.resolve(membersEnvelope(membersData))
    return Promise.reject(new Error('unexpected GET ' + url))
  })
})

describe('SubscriberListDetail double opt-in', () => {
  it('renders the DOI checkbox for a static list and sends double_opt_in on save', async () => {
    const wrapper = await mountPage()

    const checkbox = wrapper.find('input[type="checkbox"]')
    expect(checkbox.exists()).toBe(true)
    expect((checkbox.element as HTMLInputElement).checked).toBe(false)

    await checkbox.setValue(true)
    apiPut.mockResolvedValueOnce(staticListEnvelope(true))
    await wrapper.findAll('button').find(b => b.text() === 'Save')!.trigger('click')
    await flushPromises()

    expect(apiPut).toHaveBeenCalledTimes(1)
    expect(apiPut).toHaveBeenCalledWith(
      '/workspaces/current/subscriber-lists/5',
      expect.objectContaining({ name: 'VIPs', description: 'High-value contacts', double_opt_in: true }),
    )
  })

  it('shows a pending badge and Resend confirmation that POSTs to the resend endpoint', async () => {
    membersData = [
      member(7, 'pending@example.com', 'pending'),
      member(8, 'confirmed@example.com', 'confirmed'),
    ]
    const wrapper = await mountPage()

    // Only the pending row carries the warning badge and the resend action.
    expect(wrapper.find('.badge-warning').exists()).toBe(true)
    expect(wrapper.find('.badge-warning').text()).toBe('pending')
    expect(resendButtons(wrapper).length).toBe(1)

    apiPost.mockResolvedValueOnce({ data: { success: true, data: null } })
    await resendButtons(wrapper)[0].trigger('click')
    await flushPromises()

    // The endpoint the backend actually registers — pinned exactly.
    expect(apiPost).toHaveBeenCalledTimes(1)
    expect(apiPost).toHaveBeenCalledWith('/workspaces/current/subscriber-lists/5/members/7/resend-confirmation')
    expect(notifySuccess).toHaveBeenCalledWith('Confirmation email sent')

    // The members table is refetched after the resend.
    const memberFetches = apiGet.mock.calls.filter((c: string[]) => c[0] === '/workspaces/current/subscriber-lists/5/members')
    expect(memberFetches.length).toBe(2)
  })

  it('shows no resend button for confirmed rows and keeps the subscriber-status badge', async () => {
    membersData = [
      member(8, 'confirmed@example.com', 'confirmed'),
      member(9, 'legacy@example.com'),
    ]
    const wrapper = await mountPage()

    expect(wrapper.find('.badge-warning').exists()).toBe(false)
    expect(resendButtons(wrapper).length).toBe(0)
    // The pre-existing subscriber-status badge still renders for these rows.
    expect(wrapper.findAll('.badge-success').length).toBe(2)
    expect(wrapper.text()).toContain('subscribed')
  })
})
