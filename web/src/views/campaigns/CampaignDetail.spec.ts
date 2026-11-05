import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// CampaignDetail pulls the campaign, its A/B decision (R-08) and the timezone
// distribution (R-09); the mock records which side loads run.
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

describe('CampaignDetail A/B winner display', () => {
  it('shows the winner badge and highlights the winning variant row when a winner is set', async () => {
    const wrapper = await mountDetail(
      { ...CAMPAIGN_BASE, ab_test_enabled: true, ab_test_metric: 'open_rate', ab_test_winner: 'B', ab_test_winner_sent: true },
      AB_STATS,
    )

    const text = wrapper.text()
    // Header grid badge (from the campaign) and card badge (from the stats).
    expect(text).toContain('Winner: B')
    // No "still testing" badge anywhere.
    expect(text).not.toContain('Test in progress')
    expect(text).not.toContain('Testing (open rate)')

    // The per-variant table rendered with both rows; the winner row is
    // highlighted and carries its own badge.
    const winnerRow = wrapper.find('.ab-winner-row')
    expect(winnerRow.exists()).toBe(true)
    expect(winnerRow.text()).toContain('B')
    expect(winnerRow.text()).toContain('34.0%')
    expect(winnerRow.find('.badge-success').text()).toBe('Winner')
    expect(wrapper.findAll('tbody tr').length).toBe(2)
  })

  it('shows the in-progress badges and no winner row while the test runs', async () => {
    const wrapper = await mountDetail(
      { ...CAMPAIGN_BASE, ab_test_enabled: true, ab_test_metric: 'open_rate' },
      {
        ...AB_STATS,
        winner: undefined,
        winner_sent: false,
        variants: AB_STATS.variants.map(v => ({ ...v, winner: false })),
      },
    )

    const text = wrapper.text()
    expect(text).toContain('Testing (open rate)')
    expect(text).toContain('Test in progress')
    expect(text).not.toContain('Winner:')
    expect(wrapper.find('.ab-winner-row').exists()).toBe(false)
    // The stats table still lists the variants.
    expect(wrapper.findAll('tbody tr').length).toBe(2)
  })

  it('does not load A/B stats for a plain campaign', async () => {
    await mountDetail({ ...CAMPAIGN_BASE }, null)

    expect(campaignsApi.abTest).not.toHaveBeenCalled()
    expect(campaignsApi.timezones).toHaveBeenCalledTimes(1)
  })
})
