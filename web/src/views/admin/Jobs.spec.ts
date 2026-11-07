import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The queue-operations surface (SF-54 pause/resume + SF-59 actions): the
// buttons are the assertion target — they must be admin-gated, must show the
// paused state, and must call the API behind a confirmation.
const adminApi = vi.hoisted(() => ({
  listJobs: vi.fn(),
  listQueues: vi.fn(),
  listQueueTasks: vi.fn(),
  pauseQueues: vi.fn(),
  resumeQueues: vi.fn(),
  runQueueAction: vi.fn(),
}))
vi.mock('../../api/admin', () => ({ adminApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

// Jobs.vue is an admin page; the operations are additionally gated on the
// session role so an impersonated or degraded session sees read-only UI.
const isAdmin = vi.hoisted(() => ({ value: true }))
vi.mock('../../stores/auth', () => ({
  useAuthStore: () => ({ get isAdmin() { return isAdmin.value } }),
}))

const confirm = vi.hoisted(() => vi.fn())
vi.mock('../../composables/useConfirm', () => ({ useConfirm: () => ({ confirm }) }))

import Jobs from './Jobs.vue'

function queue(overrides: Record<string, unknown> = {}) {
  return {
    queue: 'transactional',
    size: 5,
    pending: 2,
    active: 1,
    scheduled: 0,
    retry: 1,
    archived: 3,
    processed: 100,
    failed: 2,
    latency_ms: 20,
    paused: false,
    ...overrides,
  }
}

function overview(queues: Record<string, unknown>[]) {
  return { data: { success: true, data: { queues, active_workers: 1 } } }
}

async function mountJobs() {
  const wrapper = mount(Jobs)
  await flushPromises()
  return wrapper
}

function buttonByText(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll('button').find((b) => b.text().includes(text))
}

beforeEach(() => {
  vi.clearAllMocks()
  isAdmin.value = true
  confirm.mockResolvedValue(true)
  adminApi.listJobs.mockResolvedValue({ data: { success: true, data: [] } })
  adminApi.listQueueTasks.mockResolvedValue({
    data: { success: true, data: { queue: 'transactional', state: 'pending', tasks: [], page: 0, size: 20, total: 0 } },
  })
})

describe('Jobs queue operations (SF-54 / SF-59)', () => {
  it('shows pause, resume-offering and action controls to a platform admin', async () => {
    adminApi.listQueues.mockResolvedValue(overview([queue()]))
    const wrapper = await mountJobs()

    expect(buttonByText(wrapper, 'Pause all queues')).toBeTruthy()
    expect(buttonByText(wrapper, 'Requeue stuck messages')).toBeTruthy()
    expect(buttonByText(wrapper, 'Purge dead letters')).toBeTruthy()
  })

  it('hides the destructive operations from non-admin sessions', async () => {
    isAdmin.value = false
    adminApi.listQueues.mockResolvedValue(overview([queue()]))
    const wrapper = await mountJobs()

    expect(buttonByText(wrapper, 'Pause all queues')).toBeUndefined()
    expect(buttonByText(wrapper, 'Requeue stuck messages')).toBeUndefined()
    expect(buttonByText(wrapper, 'Purge dead letters')).toBeUndefined()
  })

  it('badges a paused queue and offers resume while delivery is stopped', async () => {
    adminApi.listQueues.mockResolvedValue(overview([
      queue({ paused: true }),
      queue({ queue: 'bulk', paused: true }),
    ]))
    const wrapper = await mountJobs()

    expect(wrapper.text()).toContain('paused')
    expect(wrapper.text()).toContain('Delivery is paused')
    expect(buttonByText(wrapper, 'Resume all queues')).toBeTruthy()
    expect(buttonByText(wrapper, 'Pause all queues')).toBeUndefined()
  })

  it('pauses all queues after an explicit confirmation', async () => {
    adminApi.listQueues.mockResolvedValue(overview([queue()]))
    adminApi.pauseQueues.mockResolvedValue({
      data: { success: true, data: { paused: true, queues: [{ queue: 'transactional', paused: true, changed: true }] } },
    })
    const wrapper = await mountJobs()

    await buttonByText(wrapper, 'Pause all queues')!.trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm.mock.calls[0][0].variant).toBe('danger')
    expect(adminApi.pauseQueues).toHaveBeenCalledTimes(1)
    expect(adminApi.resumeQueues).not.toHaveBeenCalled()
  })

  it('resumes all queues through the mirror endpoint', async () => {
    adminApi.listQueues.mockResolvedValue(overview([queue({ paused: true })]))
    adminApi.resumeQueues.mockResolvedValue({
      data: { success: true, data: { paused: false, queues: [{ queue: 'transactional', paused: false, changed: true }] } },
    })
    const wrapper = await mountJobs()

    await buttonByText(wrapper, 'Resume all queues')!.trigger('click')
    await flushPromises()

    expect(adminApi.resumeQueues).toHaveBeenCalledTimes(1)
    expect(adminApi.pauseQueues).not.toHaveBeenCalled()
  })

  it('runs the requeue_stuck sweep after confirmation', async () => {
    adminApi.listQueues.mockResolvedValue(overview([queue()]))
    adminApi.runQueueAction.mockResolvedValue({
      data: { success: true, data: { action: 'requeue_stuck', requeued: 2, purged: 0, queues: ['transactional'] } },
    })
    const wrapper = await mountJobs()

    await buttonByText(wrapper, 'Requeue stuck messages')!.trigger('click')
    await flushPromises()

    expect(adminApi.runQueueAction).toHaveBeenCalledWith('requeue_stuck')
    expect(notify.success).toHaveBeenCalled()
  })

  it('refuses the purge when the confirmation is declined', async () => {
    adminApi.listQueues.mockResolvedValue(overview([queue()]))
    confirm.mockResolvedValue(false)
    const wrapper = await mountJobs()

    await buttonByText(wrapper, 'Purge dead letters')!.trigger('click')
    await flushPromises()

    expect(adminApi.runQueueAction).not.toHaveBeenCalled()
  })

  it('purges the dead-letter set through the actions endpoint', async () => {
    adminApi.listQueues.mockResolvedValue(overview([queue()]))
    adminApi.runQueueAction.mockResolvedValue({
      data: { success: true, data: { action: 'purge_archived_before', requeued: 0, purged: 7, queues: ['transactional', 'bulk', 'low'] } },
    })
    const wrapper = await mountJobs()

    await buttonByText(wrapper, 'Purge dead letters')!.trigger('click')
    await flushPromises()

    expect(adminApi.runQueueAction).toHaveBeenCalledWith('purge_archived_before')
    expect(notify.success).toHaveBeenCalled()
  })
})
