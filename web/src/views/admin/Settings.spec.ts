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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// The settings change-history surface (SF-100): the history section must load
// per-key entries (old → new, newest first), disable rollback on creation
// entries (null old_value), and route a rollback through the confirmation
// dialog to the API.
const settingsApi = vi.hoisted(() => ({
  getAdminSettings: vi.fn(),
  updateAdminSettings: vi.fn(),
  getSettingHistory: vi.fn(),
  rollbackSetting: vi.fn(),
}))
vi.mock('../../api/settings', () => ({ settingsApi }))

const notify = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

const confirm = vi.hoisted(() => vi.fn())
vi.mock('../../composables/useConfirm', () => ({ useConfirm: () => ({ confirm }) }))

import Settings from './Settings.vue'
import type { AdminSettingHistoryEntry } from '../../api/types'

function setting(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    key: 'retention_days',
    value: '30',
    type: 'int',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function historyEntry(overrides: Partial<AdminSettingHistoryEntry> = {}): AdminSettingHistoryEntry {
  return {
    id: 21,
    key: 'retention_days',
    old_value: '30',
    new_value: '45',
    changed_by: 7,
    changed_by_email: 'admin@example.com',
    created_at: '2026-11-07T10:00:00Z',
    workspace_id: null,
    ...overrides,
  }
}

function historyResponse(entries: AdminSettingHistoryEntry[], totalElements = entries.length) {
  return {
    data: {
      success: true,
      data: entries,
      pageable: {
        current_page: 0,
        size: 10,
        total_pages: entries.length > 0 ? 1 : 0,
        total_elements: totalElements,
        empty: entries.length === 0,
      },
    },
  }
}

async function mountSettings() {
  const wrapper = mount(Settings)
  await flushPromises()
  return wrapper
}

function buttonByText(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll('button').find((b) => b.text().includes(text))
}

beforeEach(() => {
  vi.clearAllMocks()
  confirm.mockResolvedValue(true)
  settingsApi.getAdminSettings.mockResolvedValue({
    data: { success: true, data: [setting(), setting({ id: 2, key: 'maintenance_mode', value: 'false', type: 'bool' })] },
  })
  settingsApi.getSettingHistory.mockResolvedValue(historyResponse([]))
  settingsApi.rollbackSetting.mockResolvedValue({ data: { success: true, data: [setting()] } })
})

describe('Settings change history (SF-100)', () => {
  it('keeps the history section collapsed until the operator opens it', async () => {
    const wrapper = await mountSettings()

    const toggle = buttonByText(wrapper, 'Show')
    expect(toggle).toBeTruthy()
    expect(wrapper.find('.history-table').exists()).toBe(false)

    await toggle!.trigger('click')
    await flushPromises()
    expect(buttonByText(wrapper, 'Hide')).toBeTruthy()
    expect(wrapper.find('.history-key-select').exists()).toBe(true)
  })

  it('loads a key’s history on demand and renders old → new rows', async () => {
    settingsApi.getSettingHistory.mockResolvedValue(historyResponse([
      historyEntry(),
      historyEntry({ id: 20, old_value: null, new_value: '30', created_at: '2026-11-01T09:00:00Z' }),
    ]))
    const wrapper = await mountSettings()

    await buttonByText(wrapper, 'Show')!.trigger('click')
    await wrapper.find('select.history-key-select').setValue('retention_days')
    await buttonByText(wrapper, 'Load history')!.trigger('click')
    await flushPromises()

    expect(settingsApi.getSettingHistory).toHaveBeenCalledWith('retention_days', 0, 10)

    const rows = wrapper.findAll('.history-table tbody tr')
    expect(rows).toHaveLength(2)
    // Newest first: the change entry shows both values; the creation entry
    // renders "creation" and cannot be rolled back.
    expect(rows[0].text()).toContain('admin@example.com')
    expect(rows[0].text()).toContain('30')
    expect(rows[0].text()).toContain('45')
    expect(rows[1].text()).toContain('creation')
    expect((rows[1].find('.history-rollback-btn').element as HTMLButtonElement).disabled).toBe(true)
    expect((rows[0].find('.history-rollback-btn').element as HTMLButtonElement).disabled).toBe(false)
  })

  it('rolls a key back after confirmation and refreshes the history', async () => {
    settingsApi.getSettingHistory.mockResolvedValue(historyResponse([historyEntry()]))
    const wrapper = await mountSettings()

    await buttonByText(wrapper, 'Show')!.trigger('click')
    await wrapper.find('select.history-key-select').setValue('retention_days')
    await buttonByText(wrapper, 'Load history')!.trigger('click')
    await flushPromises()

    await wrapper.find('.history-rollback-btn').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm.mock.calls[0][0].title).toContain('Roll back')
    expect(settingsApi.rollbackSetting).toHaveBeenCalledWith('retention_days', 21)
    // The history reloads after the rollback (current page).
    expect(settingsApi.getSettingHistory).toHaveBeenCalledTimes(2)
    expect(notify.success).toHaveBeenCalled()
  })

  it('does not call the API when the operator declines the rollback', async () => {
    settingsApi.getSettingHistory.mockResolvedValue(historyResponse([historyEntry()]))
    confirm.mockResolvedValue(false)
    const wrapper = await mountSettings()

    await buttonByText(wrapper, 'Show')!.trigger('click')
    await wrapper.find('select.history-key-select').setValue('retention_days')
    await buttonByText(wrapper, 'Load history')!.trigger('click')
    await flushPromises()

    await wrapper.find('.history-rollback-btn').trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(settingsApi.rollbackSetting).not.toHaveBeenCalled()
    expect(notify.error).not.toHaveBeenCalled()
  })

  it('shows an empty state for a key without recorded changes', async () => {
    settingsApi.getSettingHistory.mockResolvedValue(historyResponse([]))
    const wrapper = await mountSettings()

    await buttonByText(wrapper, 'Show')!.trigger('click')
    await wrapper.find('select.history-key-select').setValue('retention_days')
    await buttonByText(wrapper, 'Load history')!.trigger('click')
    await flushPromises()

    expect(wrapper.find('.history-empty').text()).toContain('No recorded changes')
  })
})
