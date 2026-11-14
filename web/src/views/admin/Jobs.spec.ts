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
  triggerJob: vi.fn(),
  listJobHistory: vi.fn(),
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
  adminApi.listJobHistory.mockResolvedValue({
    data: { success: true, data: [], pageable: { current_page: 0, size: 10, total_pages: 0, total_elements: 0, empty: true } },
  })
})

function cronJob(overrides: Record<string, unknown> = {}) {
  return {
    name: 'queue_reconcile',
    schedule: '*/5 * * * *',
    running: false,
    last_run_at: '2026-11-15T08:00:00Z',
    next_run_at: '2026-11-15T08:05:00Z',
    ...overrides,
  }
}

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

describe('Jobs run history + manual trigger (SF-99)', () => {
  it('shows the Run-now button on each job card for a platform admin and triggers behind a confirmation', async () => {
    adminApi.listJobs.mockResolvedValue({ data: { success: true, data: [cronJob()] } })
    adminApi.triggerJob.mockResolvedValue({ data: { success: true, data: { message: 'job triggered' } } })
    const wrapper = await mountJobs()

    const btn = buttonByText(wrapper, 'Run now')
    expect(btn).toBeTruthy()
    await btn!.trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm.mock.calls[0][0].title).toContain('queue_reconcile')
    expect(adminApi.triggerJob).toHaveBeenCalledWith('queue_reconcile')
    expect(notify.success).toHaveBeenCalled()
  })

  it('hides the Run-now button from non-admin sessions', async () => {
    isAdmin.value = false
    adminApi.listJobs.mockResolvedValue({ data: { success: true, data: [cronJob()] } })
    const wrapper = await mountJobs()

    expect(buttonByText(wrapper, 'Run now')).toBeUndefined()
    expect(adminApi.triggerJob).not.toHaveBeenCalled()
  })

  it('disables Run-now for a mid-run job', async () => {
    adminApi.listJobs.mockResolvedValue({ data: { success: true, data: [cronJob({ running: true })] } })
    const wrapper = await mountJobs()

    const btn = buttonByText(wrapper, 'Run now')!
    expect(btn.attributes('disabled')).toBeDefined()
  })

  it('opens the run history drawer and loads the history rows', async () => {
    adminApi.listJobs.mockResolvedValue({ data: { success: true, data: [cronJob()] } })
    adminApi.listJobHistory.mockResolvedValue({
      data: {
        success: true,
        data: [
          { id: 1, job_name: 'queue_reconcile', trigger: 'manual', started_at: '2026-11-15T08:00:00Z', finished_at: '2026-11-15T08:00:02Z', outcome: 'success', duration_ms: 2000 },
          { id: 2, job_name: 'queue_reconcile', trigger: 'schedule', started_at: '2026-11-15T07:55:00Z', finished_at: '2026-11-15T07:55:05Z', outcome: 'failed', detail: 'boom', duration_ms: 5000 },
        ],
        pageable: { current_page: 0, size: 10, total_pages: 1, total_elements: 2, empty: false },
      },
    })
    const wrapper = await mountJobs()

    // The drawer is collapsed until the Show toggle opens it; opening it
    // loads the newest-first rows (manual + schedule triggers, failure detail).
    const toggle = buttonByText(wrapper, 'Show')!
    expect(toggle).toBeTruthy()
    await toggle.trigger('click')
    await flushPromises()

    expect(adminApi.listJobHistory).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('Run history')
    expect(wrapper.text()).toContain('manual')
    expect(wrapper.text()).toContain('boom')
  })

  it('filters the history by job from the drawer selector', async () => {
    adminApi.listJobs.mockResolvedValue({ data: { success: true, data: [cronJob()] } })
    const wrapper = await mountJobs()

    await buttonByText(wrapper, 'Show')!.trigger('click')
    await flushPromises()

    const select = wrapper.find('select')
    await select.setValue('queue_reconcile')
    await flushPromises()

    expect(adminApi.listJobHistory).toHaveBeenLastCalledWith(0, 10, 'queue_reconcile')
  })
})
