import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The health endpoints are the assertion target: the expanded row must load the
// stored report lazily (R-07) and Diagnose must re-check with edit rights.
const domainsApi = vi.hoisted(() => ({
  list: vi.fn(),
  get: vi.fn(),
  create: vi.fn(),
  verify: vi.fn(),
  delete: vi.fn(),
  listSigningKeys: vi.fn(),
  generateSigningKey: vi.fn(),
  rotateSigningKey: vi.fn(),
  verifySigningKeyDns: vi.fn(),
  deleteSigningKey: vi.fn(),
  health: vi.fn(),
  diagnose: vi.fn(),
  dmarcSummary: vi.fn(),
}))

vi.mock('../../api/domains', () => ({ domainsApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

// Mutable so each test can flip edit rights on the workspace store.
const wsState = vi.hoisted(() => ({ canEdit: true }))
vi.mock('../../stores/workspace', () => ({
  useWorkspaceStore: () => ({ canEdit: wsState.canEdit }),
}))

vi.mock('../../composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(false) }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

import Domains from './Domains.vue'

const domainRow = {
  id: 5,
  user_id: 1,
  domain: 'example.dev',
  ownership_verified: true,
  spf_verified: true,
  dkim_verified: false,
  dmarc_verified: true,
  verification_token: 'tok',
  created_at: '2026-01-01T00:00:00Z',
  // The expanded row only renders when dns_records are present.
  dns_records: {
    verification: { type: 'TXT', host: 'example.dev', value: 'tok' },
    spf: { type: 'TXT', host: 'example.dev', value: 'v=spf1 include:_spf.example.dev ~all' },
    dkim: { type: 'TXT', host: 'posta._domainkey.example.dev', value: 'v=DKIM1; k=rsa; p=MIIB' },
    dmarc: { type: 'TXT', host: '_dmarc.example.dev', value: 'v=DMARC1; p=none' },
  },
}

function makeReport(overrides: Record<string, unknown> = {}) {
  return {
    domain_id: 5,
    domain: 'example.dev',
    ownership_verified: true,
    spf: { verified: true, record: 'v=spf1 include:_spf.example.dev ~all' },
    dkim: { verified: false, guidance: 'Publish the DKIM TXT record for selector posta' },
    dmarc: { verified: true, record: 'v=DMARC1; p=none' },
    signing: { enabled: true, selector: 'posta', algorithm: 'rsa-sha256', dns_state: 'published' },
    sending: {
      sent: 1000,
      bounced: 25,
      complained: 3,
      bounce_rate: 2.5,
      complaint_rate: 0.3,
      since: '2026-10-07T00:00:00Z',
    },
    checked_at: '2026-11-06T12:00:00Z',
    ...overrides,
  }
}

function paginated(data: unknown[]) {
  return {
    data: {
      success: true,
      data,
      pageable: { current_page: 0, size: 20, total_pages: 1, total_elements: data.length, empty: data.length === 0 },
    },
  }
}

function reportEnvelope(report = makeReport()) {
  return { data: { success: true, data: report } }
}

function makeDmarcResponse() {
  return {
    summary: {
      record_count: 850,
      message_count: 12500,
      dkim_aligned_messages: 12250,
      spf_aligned_messages: 11900,
      dkim_alignment: 0.98,
      spf_alignment: 0.952,
      disposition_none: 820,
      disposition_quarantine: 25,
      disposition_reject: 5,
      latest_window_end: '2026-11-05T23:59:59Z',
    },
    records: [
      {
        id: 42,
        user_id: 1,
        workspace_id: 2,
        domain_id: 5,
        inbound_id: 9,
        domain: 'example.dev',
        reporter: 'Google',
        reporter_email: 'noreply-dmarc@google.com',
        report_id: 'gid-1',
        source_ip: '192.0.2.10',
        window_start: '2026-11-05T00:00:00Z',
        window_end: '2026-11-05T23:59:59Z',
        count: 640,
        dkim_aligned: true,
        spf_aligned: true,
        disposition: 'none',
        header_from: 'example.dev',
        created_at: '2026-11-06T01:00:00Z',
      },
      {
        id: 41,
        user_id: 1,
        workspace_id: 2,
        domain_id: 5,
        inbound_id: 8,
        domain: 'example.dev',
        reporter: 'Microsoft',
        reporter_email: 'dmarc@microsoft.com',
        report_id: 'msid-2',
        source_ip: '198.51.100.7',
        window_start: '2026-11-04T00:00:00Z',
        window_end: '2026-11-04T23:59:59Z',
        count: 30,
        dkim_aligned: false,
        spf_aligned: false,
        disposition: 'quarantine',
        header_from: 'example.dev',
        created_at: '2026-11-05T01:00:00Z',
      },
    ],
  }
}

function dmarcEnvelope(payload = makeDmarcResponse()) {
  return { data: { success: true, data: payload } }
}

async function mountAndExpand() {
  const wrapper = mount(Domains, {
    global: { stubs: { Pagination: true } },
  })
  await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === 'View DNS Records')!.trigger('click')
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  wsState.canEdit = true
  domainsApi.list.mockResolvedValue(paginated([domainRow]))
  domainsApi.get.mockResolvedValue({ data: { success: true, data: domainRow } })
  domainsApi.listSigningKeys.mockResolvedValue({ data: { success: true, data: [] } })
  domainsApi.health.mockResolvedValue(reportEnvelope())
  domainsApi.dmarcSummary.mockResolvedValue(dmarcEnvelope())
})

describe('Domains deliverability health panel', () => {
  it('loads the report lazily on first expansion and renders rates and check badges', async () => {
    const wrapper = await mountAndExpand()

    expect(domainsApi.health).toHaveBeenCalledTimes(1)
    expect(domainsApi.health).toHaveBeenCalledWith(5)

    const text = wrapper.text()
    // SPF verified / DKIM pending badges, plus guidance on the failing check.
    expect(text).toContain('Verified')
    expect(text).toContain('Pending')
    expect(text).toContain('Publish the DKIM TXT record for selector posta')
    // Sending counts and the two rates as percentages of sent.
    expect(text).toContain('1000')
    expect(text).toContain('25')
    expect(text).toContain('3')
    expect(text).toContain('2.50%')
    expect(text).toContain('0.30%')

    // Cached per domain id: collapsing and re-expanding must not refetch.
    await wrapper.findAll('button').find(b => b.text() === 'Hide DNS')!.trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find(b => b.text() === 'View DNS Records')!.trigger('click')
    await flushPromises()
    expect(domainsApi.health).toHaveBeenCalledTimes(1)
  })

  it('hides Diagnose without edit rights', async () => {
    wsState.canEdit = false
    const wrapper = await mountAndExpand()

    expect(wrapper.findAll('button').some(b => b.text() === 'Diagnose')).toBe(false)
    expect(domainsApi.diagnose).not.toHaveBeenCalled()
  })

  it('runs Diagnose with a loading state and refreshes the report', async () => {
    const wrapper = await mountAndExpand()

    // Hold the diagnose call open to observe the loading state.
    let resolveDiagnose!: (v: unknown) => void
    const fresh = makeReport({
      dkim: { verified: true, record: 'v=DKIM1; k=rsa; p=MIIB' },
      sending: { sent: 2000, bounced: 198, complained: 4, bounce_rate: 9.9, complaint_rate: 0.2, since: '2026-10-07T00:00:00Z' },
      checked_at: '2026-11-06T13:30:00Z',
    })
    domainsApi.diagnose.mockReturnValue(new Promise((res) => { resolveDiagnose = res }))

    await wrapper.findAll('button').find(b => b.text() === 'Diagnose')!.trigger('click')
    await flushPromises()
    expect(domainsApi.diagnose).toHaveBeenCalledWith(5)
    expect(wrapper.findAll('button').find(b => b.text() === 'Diagnosing…')!.attributes('disabled')).toBeDefined()

    resolveDiagnose(reportEnvelope(fresh))
    await flushPromises()

    // The panel now shows the fresh report, not the stored one.
    const text = wrapper.text()
    expect(text).toContain('9.90%')
    expect(text).not.toContain('2.50%')
    expect(domainsApi.health).toHaveBeenCalledTimes(1)
  })

  it('shows the fallback message when loading the report fails', async () => {
    domainsApi.health.mockRejectedValue(new Error('network down'))
    await mountAndExpand()

    expect(notify.error).toHaveBeenCalledWith('Failed to load deliverability health')
  })

  it('renders the DMARC aggregate summary and newest records from the endpoint', async () => {
    const wrapper = await mountAndExpand()

    expect(domainsApi.dmarcSummary).toHaveBeenCalledTimes(1)
    expect(domainsApi.dmarcSummary).toHaveBeenCalledWith(5)

    const text = wrapper.text()
    // Record and message counts, alignment as percentages of messages.
    expect(text).toContain('DMARC aggregate reports')
    expect(text).toContain('850')
    expect(text).toContain('12500')
    expect(text).toContain('98.0%')
    expect(text).toContain('95.2%')
    // Disposition breakdown badges with counts.
    expect(text).toContain('none 820')
    expect(text).toContain('quarantine 25')
    expect(text).toContain('reject 5')
    // Newest records: reporter and disposition badge.
    expect(text).toContain('Google')
    expect(text).toContain('Microsoft')
    expect(text).toContain('None')
    expect(text).toContain('Quarantine')
    expect(text).toContain('640 messages')
    expect(text).toContain('30 messages')
  })

  it('shows the muted empty state when the DMARC endpoint fails', async () => {
    domainsApi.dmarcSummary.mockRejectedValue(new Error('network down'))
    const wrapper = await mountAndExpand()

    const text = wrapper.text()
    // The failure is non-fatal: the health panel still renders…
    expect(text).toContain('Deliverability health')
    expect(text).toContain('2.50%')
    // …and the DMARC subsection falls back to the empty state.
    expect(text).toContain('No DMARC reports ingested yet.')
    expect(notify.error).not.toHaveBeenCalledWith('network down')
  })
})
