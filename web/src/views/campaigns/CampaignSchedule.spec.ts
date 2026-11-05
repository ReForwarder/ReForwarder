import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The create call is the assertion target: the schedule-send field (R-01) must
// send scheduled_at only when the operator picked a time, so every other
// endpoint is stubbed and just keeps the page mountable.
const campaignsApi = vi.hoisted(() => ({
  list: vi.fn(),
  get: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  delete: vi.fn(),
  send: vi.fn(),
  pause: vi.fn(),
  resume: vi.fn(),
  cancel: vi.fn(),
  listMessages: vi.fn(),
  analytics: vi.fn(),
  abTest: vi.fn(),
  timezones: vi.fn(),
}))

vi.mock('../../api/campaigns', () => ({ campaignsApi }))

const subscriberListsApi = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('../../api/subscriberLists', () => ({ subscriberListsApi }))

const segmentsApi = vi.hoisted(() => ({ list: vi.fn(), preview: vi.fn() }))
vi.mock('../../api/segments', () => ({ segmentsApi }))

// The template dropdown is loaded through the shared axios client (dynamic
// import inside the view).
const apiGet = vi.hoisted(() => vi.fn())
vi.mock('../../api/client', () => ({ default: { get: apiGet } }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

vi.mock('../../stores/workspace', () => ({
  useWorkspaceStore: () => ({ canEdit: true }),
}))

vi.mock('../../composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(false) }),
}))

// usePagination reads route.query and the row links push with the router.
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

import Campaigns from './Campaigns.vue'

function paginated(data: unknown[]) {
  return {
    data: {
      success: true,
      data,
      pageable: { current_page: 0, size: 20, total_pages: 1, total_elements: data.length, empty: data.length === 0 },
    },
  }
}

async function openCreateModal() {
  const wrapper = mount(Campaigns, {
    global: {
      stubs: {
        Pagination: true,
        SearchableSelect: { props: ['options', 'modelValue', 'placeholder', 'emptyText'], template: '<select />' },
      },
    },
  })
  await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === 'Create Campaign')!.trigger('click')
  await flushPromises()
  return wrapper
}

/** Fill every required field so saveCampaign reaches the API call. */
async function fillRequiredFields(wrapper: ReturnType<typeof mount>) {
  await wrapper.find('input[placeholder="e.g. March Newsletter"]').setValue('Launch')
  await wrapper.find('input[placeholder="Email subject line"]').setValue('Hello')
  await wrapper.find('input[placeholder="sender@example.com"]').setValue('news@example.dev')
  // First select is the template picker, second the subscriber list.
  await wrapper.findAll('select.form-select')[0].setValue('7')
  await wrapper.findAll('select.form-select')[1].setValue('3')
}

beforeEach(() => {
  vi.clearAllMocks()
  campaignsApi.list.mockResolvedValue(paginated([]))
  campaignsApi.create.mockResolvedValue({ data: { success: true, data: { id: 1 } } })
  subscriberListsApi.list.mockResolvedValue(paginated([
    { id: 3, name: 'VIPs', member_count: 12 },
  ]))
  segmentsApi.list.mockResolvedValue(paginated([]))
  apiGet.mockResolvedValue(paginated([
    { id: 7, name: 'Welcome' },
  ]))
})

describe('Campaigns schedule send field', () => {
  it('sends scheduled_at as RFC3339 when a schedule is set', async () => {
    const wrapper = await openCreateModal()
    await fillRequiredFields(wrapper)

    // datetime-local has no timezone; the view converts to ISO (RFC3339).
    await wrapper.find('input[type="datetime-local"]').setValue('2026-12-01T10:30')
    await wrapper.findAll('.modal-footer button').find(b => b.text() === 'Create')!.trigger('click')
    await flushPromises()

    expect(campaignsApi.create).toHaveBeenCalledTimes(1)
    const payload = campaignsApi.create.mock.calls[0][0]
    expect(payload.scheduled_at).toBe(new Date('2026-12-01T10:30').toISOString())
    expect(payload.scheduled_at).toMatch(/Z$/)
    // The required fields ride along.
    expect(payload.name).toBe('Launch')
    expect(payload.template_id).toBe(7)
    expect(payload.list_id).toBe(3)
    expect(notify.success).toHaveBeenCalledWith('Campaign created')
  })

  it('omits scheduled_at entirely when the schedule is left empty', async () => {
    const wrapper = await openCreateModal()
    await fillRequiredFields(wrapper)

    await wrapper.findAll('.modal-footer button').find(b => b.text() === 'Create')!.trigger('click')
    await flushPromises()

    expect(campaignsApi.create).toHaveBeenCalledTimes(1)
    const payload = campaignsApi.create.mock.calls[0][0]
    // Empty schedule = send immediately: the key must be absent, not null,
    // so the backend keeps its "no scheduled_at → draft" semantics.
    expect(payload).not.toHaveProperty('scheduled_at')
    expect(notify.success).toHaveBeenCalledWith('Campaign created')
  })
})
