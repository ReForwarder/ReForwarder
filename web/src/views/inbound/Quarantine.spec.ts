// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Posta.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import type { InboundQuarantine } from '../../api/types'

// The quarantine review view (OB-2, ADR-002 decision 3): the assertion
// targets are the list call (scoped, status-filtered) and the two review
// dispositions — release re-runs routing server-side, delete is permanent —
// including the confirm gate in front of both.
const quarantineApi = vi.hoisted(() => ({
  list: vi.fn(),
  release: vi.fn(),
  delete: vi.fn(),
}))
vi.mock('../../api/inbound', () => ({ inboundQuarantineApi: quarantineApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

const confirmMock = vi.hoisted(() => vi.fn())
vi.mock('../../composables/useConfirm', () => ({
  useConfirm: () => ({ confirm: confirmMock }),
}))

const canEdit = vi.hoisted(() => ({ value: true }))
vi.mock('../../stores/workspace', () => ({
  useWorkspaceStore: () => ({ get canEdit() { return canEdit.value } }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

import Quarantine from './Quarantine.vue'

function paginated(data: unknown[]) {
  return {
    data: {
      success: true,
      data,
      pageable: { current_page: 0, size: 20, total_pages: 1, total_elements: data.length, empty: data.length === 0 },
    },
  }
}

function quarantineRow(overrides: Partial<InboundQuarantine> = {}): InboundQuarantine {
  return {
    id: 1,
    user_id: 7,
    workspace_id: 3,
    domain_id: 1,
    inbound_email_id: 42,
    route_id: 4,
    reason: 'matched quarantine route',
    score: null,
    status: 'pending',
    received_at: '2026-11-06T10:00:00Z',
    reviewed_by: null,
    reviewed_at: null,
    created_at: '2026-11-06T10:00:00Z',
    updated_at: '2026-11-06T10:00:00Z',
    inbound: {
      uuid: 'inb-1',
      sender: 'alice@example.test',
      recipients: ['user@routed.test'],
      subject: 'Held message',
      size: 120,
      status: 'quarantined',
    },
    ...overrides,
  }
}

async function mountView() {
  const wrapper = mount(Quarantine, {
    global: {
      stubs: { Pagination: true },
    },
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  canEdit.value = true
  confirmMock.mockResolvedValue(true)
  quarantineApi.list.mockResolvedValue(paginated([quarantineRow()]))
  quarantineApi.release.mockResolvedValue({
    data: { success: true, data: quarantineRow({ status: 'released' }) },
  })
  quarantineApi.delete.mockResolvedValue({
    data: { success: true, data: quarantineRow({ status: 'deleted' }) },
  })
})

describe('Quarantine review view', () => {
  it('lists pending records by default with the held message summary', async () => {
    const wrapper = await mountView()

    expect(quarantineApi.list).toHaveBeenCalledTimes(1)
    expect(quarantineApi.list).toHaveBeenCalledWith({ page: 0, status: 'pending' })
    const text = wrapper.text()
    expect(text).toContain('alice@example.test')
    expect(text).toContain('Held message')
    expect(text).toContain('matched quarantine route')
    expect(text).toContain('pending')
  })

  it('releases a pending record after confirmation', async () => {
    const wrapper = await mountView()

    const release = wrapper.findAll('button').find(b => b.text() === 'Release')!
    await release.trigger('click')
    await flushPromises()

    expect(confirmMock).toHaveBeenCalledTimes(1)
    expect(quarantineApi.release).toHaveBeenCalledWith(1)
    expect(quarantineApi.delete).not.toHaveBeenCalled()
    expect(notify.success).toHaveBeenCalled()
  })

  it('does not release when the operator cancels the confirm', async () => {
    confirmMock.mockResolvedValue(false)
    const wrapper = await mountView()

    const release = wrapper.findAll('button').find(b => b.text() === 'Release')!
    await release.trigger('click')
    await flushPromises()

    expect(quarantineApi.release).not.toHaveBeenCalled()
  })

  it('surfaces the fail-closed release policy error (422)', async () => {
    quarantineApi.release.mockRejectedValue({ response: { status: 422 } })
    const wrapper = await mountView()

    await wrapper.findAll('button').find(b => b.text() === 'Release')!.trigger('click')
    await flushPromises()

    expect(notify.error).toHaveBeenCalledWith(
      'Cannot release: the recipient no longer matches an enabled route on a verified domain',
    )
  })

  it('permanently deletes a pending record after a danger confirmation', async () => {
    const wrapper = await mountView()

    await wrapper.findAll('button').find(b => b.text() === 'Delete')!.trigger('click')
    await flushPromises()

    expect(confirmMock).toHaveBeenCalledWith(
      expect.objectContaining({ variant: 'danger', title: 'Delete message permanently' }),
    )
    expect(quarantineApi.delete).toHaveBeenCalledWith(1)
    expect(quarantineApi.release).not.toHaveBeenCalled()
    expect(notify.success).toHaveBeenCalled()
  })

  it('hides the actions column and buttons for view-only members', async () => {
    canEdit.value = false
    const wrapper = await mountView()

    // No Actions header and no review buttons (the filter dropdown's
    // "Released"/"Deleted" options are unrelated text, so assert on elements).
    expect(wrapper.findAll('th').map(th => th.text())).not.toContain('Actions')
    expect(wrapper.findAll('button').filter(b => ['Release', 'Delete'].includes(b.text()))).toHaveLength(0)
  })

  it('shows a tombstone row without message details for deleted records', async () => {
    quarantineApi.list.mockResolvedValue(
      paginated([quarantineRow({ status: 'deleted', inbound: undefined })]),
    )
    const wrapper = await mountView()

    expect(wrapper.text()).toContain('deleted')
    expect(wrapper.text()).not.toContain('alice@example.test')
    // No review buttons on a settled record.
    expect(wrapper.findAll('button').filter(b => b.text() === 'Release' || b.text() === 'Delete')).toHaveLength(0)
  })
})
