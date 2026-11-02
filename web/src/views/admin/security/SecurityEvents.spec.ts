import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The page pulls its rows through the admin API; the mock records every call
// so the regression tests can assert the exact event_type filter that was sent.
const listSecurityEvents = vi.hoisted(() => vi.fn())

vi.mock('../../../api/admin', () => ({
  adminApi: { listSecurityEvents: listSecurityEvents },
}))

// usePagination reads route.query and replaces the URL page param.
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

// The live feed opens an SSE stream on mount — happy-dom has no EventSource.
class FakeEventSource {
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  addEventListener = vi.fn()
  close = vi.fn()
}
vi.stubGlobal('EventSource', FakeEventSource)

import SecurityEvents from './SecurityEvents.vue'

// The backend applies the filter server-side, so a filtered page only ever
// contains rows of the selected type — exactly what collapsed the pills.
// The mock mimics the axios response: { data: { success, data, pageable } }.
function envelopeOf(type: string) {
  return {
    data: {
      success: true,
      data: [
        {
          id: 1,
          event_type: type,
          severity: 'info',
          source_ip: '172.18.0.1',
          protocol: null,
          port: null,
          risk_score: 10,
          action_taken: 'recorded',
          created_at: '2026-11-02T00:00:00Z',
        },
      ],
      pageable: { current_page: 0, size: 20, total_pages: 1, total_elements: 1, empty: false },
    },
  }
}

async function mountPage() {
  const wrapper = mount(SecurityEvents, {
    global: { stubs: { Pagination: true } },
  })
  await flushPromises()
  return wrapper
}

function pills(wrapper: ReturnType<typeof mount>) {
  return wrapper.find('.event-type-filter').findAll('button').map(b => ({
    label: b.text(),
    active: b.classes().includes('btn-primary'),
  }))
}

const ALL_TYPES = [
  'distinct_ports',
  'new_connections',
  'incomplete_handshakes',
  'connect_aborts',
  'malformed_preauth',
  'suspicious_paths',
  'auth_failures',
  'ban.created',
  'ban.lifted',
]

beforeEach(() => {
  listSecurityEvents.mockReset()
  listSecurityEvents.mockResolvedValue(envelopeOf(''))
})

describe('Detection Events filter bar', () => {
  it('renders All plus every event type pill on initial load', async () => {
    const wrapper = await mountPage()
    const p = pills(wrapper)
    expect(p.map(x => x.label)).toEqual(['All', ...ALL_TYPES])
    expect(p[0].active).toBe(true)
    p.slice(1).forEach(x => expect(x.active).toBe(false))
  })

  it('keeps every pill visible when new_connections is selected', async () => {
    const wrapper = await mountPage()
    const row = wrapper.find('.event-type-filter')
    await row.findAll('button').find(b => b.text() === 'new_connections')!.trigger('click')
    await flushPromises()

    const p = pills(wrapper)
    expect(p.map(x => x.label)).toEqual(['All', ...ALL_TYPES]) // nothing disappeared
    expect(p.find(x => x.label === 'new_connections')!.active).toBe(true)
    expect(p.filter(x => x.active).map(x => x.label)).toEqual(['new_connections'])
    expect(listSecurityEvents).toHaveBeenLastCalledWith(0, 20, 'new_connections')
  })

  it('switches directly between types without going through All', async () => {
    const wrapper = await mountPage()
    const row = wrapper.find('.event-type-filter')
    await row.findAll('button').find(b => b.text() === 'new_connections')!.trigger('click')
    await flushPromises()
    await row.findAll('button').find(b => b.text() === 'ban.lifted')!.trigger('click')
    await flushPromises()

    const p = pills(wrapper)
    expect(p.map(x => x.label)).toEqual(['All', ...ALL_TYPES])
    expect(p.find(x => x.label === 'ban.lifted')!.active).toBe(true)
    expect(listSecurityEvents).toHaveBeenLastCalledWith(0, 20, 'ban.lifted')
  })

  it('resets to the unfiltered table when All is clicked', async () => {
    const wrapper = await mountPage()
    const row = wrapper.find('.event-type-filter')
    await row.findAll('button').find(b => b.text() === 'auth_failures')!.trigger('click')
    await flushPromises()
    await row.findAll('button').find(b => b.text() === 'All')!.trigger('click')
    await flushPromises()

    expect(pills(wrapper).find(x => x.label === 'All')!.active).toBe(true)
    expect(listSecurityEvents).toHaveBeenLastCalledWith(0, 20, '')
  })

  it('renders the same pill list no matter which rows the API returns', async () => {
    // A page of a single type is what made the pills collapse before.
    listSecurityEvents.mockResolvedValue(envelopeOf('ban.lifted'))
    const wrapper = await mountPage()
    await flushPromises()
    expect(pills(wrapper).map(x => x.label)).toEqual(['All', ...ALL_TYPES])
  })
})
