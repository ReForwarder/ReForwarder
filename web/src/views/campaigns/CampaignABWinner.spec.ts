import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The winner badge is the assertion target: it renders from the campaign's
// ab_test_winner / ab_test_winner_sent fields (R-08), with the A/B card
// loading the per-variant stats alongside. The API layer is stubbed at the
// campaigns module boundary.
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

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

vi.mock('../../stores/workspace', () => ({
  useWorkspaceStore: () => ({ canEdit: false }),
}))

vi.mock('../../composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(false) }),
}))

// The route carries the campaign id; the row actions push with the router.
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: '9' }, query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

import CampaignDetail from './CampaignDetail.vue'

function envelope(data: unknown) {
  return { data: { success: true, data } }
}

function paginated(data: unknown[]) {
  return {
    data: {
      success: true,
      data,
      pageable: { current_page: 0, size: 20, total_pages: 0, total_elements: data.length, empty: data.length === 0 },
    },
  }
}

const CAMPAIGN_BASE = {
  id: 9,
  name: 'November Newsletter',
  subject: 'Hello',
  from_email: 'news@example.dev',
  from_name: 'News',
  template_id: 7,
  status: 'sent',
  list_id: 3,
  send_rate: 0,
  created_at: '2026-11-01T00:00:00Z',
  stats: { total: 10, pending: 0, queued: 0, sent: 10, failed: 0, skipped: 0 },
}

// The A/B card data the detail page loads once ab_test_enabled is set:
// variant B won by open rate and the win was distributed.
const AB_STATS = {
  enabled: true,
  metric: 'open_rate',
  window_hours: 24,
  min_sample: 50,
  winner: 'B',
  winner_sent: true,
  window_ends_at: '2026-11-02T00:00:00Z',
  variants: [
    { variant: 'A', sent: 100, opened: 10, clicked: 2, open_rate: 10, click_rate: 2, winner: false },
    { variant: 'B', sent: 100, opened: 34, clicked: 5, open_rate: 34, click_rate: 5, winner: true },
  ],
}

async function mountDetail(campaign: Record<string, unknown>, ab: Record<string, unknown> | null) {
  campaignsApi.get.mockResolvedValue(envelope(campaign))
  if (ab !== null) campaignsApi.abTest.mockResolvedValue(envelope(ab))
  campaignsApi.listMessages.mockResolvedValue(paginated([]))
  campaignsApi.timezones.mockResolvedValue(envelope({ send_at_local_time: false, local_send_hour: null, timezones: [] }))
  const wrapper = mount(CampaignDetail, {
    global: { stubs: { Pagination: true } },
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('CampaignDetail A/B winner badge', () => {
  it('renders the winner badge when ab_test_winner is set', async () => {
    const wrapper = await mountDetail(
      { ...CAMPAIGN_BASE, ab_test_enabled: true, ab_test_metric: 'open_rate', ab_test_winner: 'B', ab_test_winner_sent: true },
      AB_STATS,
    )

    // The campaign grid badge names the winning variant…
    expect(wrapper.text()).toContain('Winner: B')
    // …and the A/B card carries the same decision, so no "in progress"
    // badge renders anywhere.
    expect(wrapper.text()).not.toContain('Test in progress')
    expect(wrapper.text()).not.toContain('Testing (open rate)')

    // The per-variant table highlights the winning row with its own badge.
    const winnerRow = wrapper.find('.ab-winner-row')
    expect(winnerRow.exists()).toBe(true)
    expect(winnerRow.find('.badge-success').text()).toBe('Winner')
    expect(winnerRow.text()).toContain('34.0%')
  })

  it('shows no winner badge while the test has no winner yet', async () => {
    const wrapper = await mountDetail(
      { ...CAMPAIGN_BASE, ab_test_enabled: true, ab_test_metric: 'open_rate' },
      {
        ...AB_STATS,
        winner: undefined,
        winner_sent: false,
        variants: AB_STATS.variants.map(v => ({ ...v, winner: false })),
      },
    )

    expect(wrapper.text()).not.toContain('Winner:')
    // Both in-progress badges render instead: the campaign grid one…
    expect(wrapper.text()).toContain('Testing (open rate)')
    // …and the A/B card one.
    expect(wrapper.text()).toContain('Test in progress')
    expect(wrapper.find('.ab-winner-row').exists()).toBe(false)
  })
})
