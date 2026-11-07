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
import type { Domain, InboundRoute } from '../../api/types'

// The inbound routing rules view (OB-2, ADR-002 decision 1): per-domain rule
// table, create/edit drawer, the enable/disable + delete flows, and the
// forward-target confirmation flow (ADR-002 decision 2).
const domainsApi = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('../../api/domains', () => ({ domainsApi }))

const routesApi = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
}))
const targetsApi = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  remove: vi.fn(),
}))
vi.mock('../../api/inboundRoutes', () => ({
  inboundRoutesApi: routesApi,
  inboundForwardTargetsApi: targetsApi,
}))

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

import InboundRoutes from './InboundRoutes.vue'

function paginated(data: unknown[]) {
  return {
    data: {
      success: true,
      data,
      pageable: { current_page: 0, size: 20, total_pages: 1, total_elements: data.length, empty: data.length === 0 },
    },
  }
}

function domainRow(overrides: Partial<Domain> = {}): Domain {
  return {
    id: 1,
    user_id: 7,
    domain: 'example.test',
    ownership_verified: true,
    spf_verified: true,
    dkim_verified: true,
    dmarc_verified: false,
    verification_token: 'tok',
    created_at: '2026-11-06T10:00:00Z',
    ...overrides,
  }
}

function routeRow(overrides: Partial<InboundRoute> = {}): InboundRoute {
  return {
    id: 11,
    user_id: 7,
    workspace_id: 3,
    domain_id: 1,
    pattern: 'team@example.test',
    action: 'forward',
    target: 'dest@elsewhere.test',
    priority: 10,
    enabled: true,
    created_at: '2026-11-06T10:00:00Z',
    updated_at: '2026-11-06T10:00:00Z',
    ...overrides,
  }
}

const verifiedDomain = domainRow()
const unverifiedDomain = domainRow({ id: 2, domain: 'pending.test', ownership_verified: false })
const forwardRule = routeRow()
const catchAllRule = routeRow({
  id: 12,
  pattern: '@example.test',
  action: 'deliver',
  target: undefined,
  priority: 100,
  enabled: false,
})

async function mountView() {
  const wrapper = mount(InboundRoutes, {
    global: {
      // Render the create/edit drawer inline instead of teleporting to body.
      stubs: { Pagination: true, Teleport: true },
    },
  })
  await flushPromises()
  return wrapper
}

function buttonByText(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll('button').find((b) => b.text().trim() === text)
}

beforeEach(() => {
  vi.clearAllMocks()
  canEdit.value = true
  confirmMock.mockResolvedValue(true)
  domainsApi.list.mockResolvedValue(paginated([verifiedDomain, unverifiedDomain]))
  routesApi.list.mockResolvedValue(paginated([forwardRule, catchAllRule]))
  routesApi.create.mockResolvedValue({ data: { success: true, data: routeRow({ id: 13 }) } })
  routesApi.update.mockResolvedValue({ data: { success: true, data: forwardRule } })
  routesApi.remove.mockResolvedValue({ data: { success: true, data: forwardRule } })
  targetsApi.list.mockResolvedValue(paginated([]))
  targetsApi.create.mockResolvedValue({
    data: {
      success: true,
      data: {
        target: routeRow().target ? { id: 5, user_id: 7, target: 'dest@elsewhere.test', created_at: '2026-11-06T10:00:00Z', updated_at: '2026-11-06T10:00:00Z' } : {},
        confirm_url: 'https://posta.test/api/v1/inbound/forward-confirm/tok-123',
      },
    },
  })
  targetsApi.remove.mockResolvedValue({ data: { success: true, data: {} } })
})

describe('InboundRoutes view', () => {
  it('lists the domains with ownership state and loads nothing until a row expands', async () => {
    const wrapper = await mountView()

    expect(domainsApi.list).toHaveBeenCalledWith(0)
    expect(routesApi.list).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('example.test')
    expect(wrapper.text()).toContain('pending.test')
  })

  it('expands a domain and renders its rules with pattern, action badge, target and priority', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'View Rules')!.trigger('click')
    await flushPromises()

    expect(routesApi.list).toHaveBeenCalledWith({ domain_id: 1 })
    expect(wrapper.text()).toContain('team@example.test')
    expect(wrapper.text()).toContain('dest@elsewhere.test')
    expect(wrapper.text()).toContain('@example.test')
    expect(wrapper.text()).toContain('catch-all')
    expect(wrapper.text()).toContain('Forward')
  })

  it('creates a rule from the drawer and reloads the expanded domain rules', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Add Rule')!.trigger('click')
    await flushPromises()

    const patternInput = wrapper.find('input[placeholder="user@example.com, user+tag@example.com, or @example.com"]')
    await patternInput.setValue('team@example.test')
    await buttonByText(wrapper, 'Create Rule')!.trigger('click')
    await flushPromises()

    expect(routesApi.create).toHaveBeenCalledWith({
      domain_id: 1,
      pattern: 'team@example.test',
      action: 'deliver',
      target: '',
      priority: 100,
      enabled: true,
    })
    expect(notify.success).toHaveBeenCalled()
    // The drawer closed and the domain's rules were refreshed.
    expect(wrapper.text()).not.toContain('New Routing Rule')
    expect(routesApi.list).toHaveBeenCalledWith({ domain_id: 1 })
  })

  it('registers an unconfirmed forward target from the drawer and shows the one-time confirm URL', async () => {
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Add Rule')!.trigger('click')
    await flushPromises()

    // Switch the action to forward; the target field and registration flow appear.
    const actionSelect = wrapper.findAll('select').find((s) => s.findAll('option').some((o) => o.element.value === 'forward'))!
    await actionSelect.setValue('forward')
    await flushPromises()

    const targetInput = wrapper.find('input[placeholder="destination@example.com"]')
    await targetInput.setValue('dest@elsewhere.test')
    await flushPromises()

    await buttonByText(wrapper, 'Register target & get confirmation link')!.trigger('click')
    await flushPromises()

    expect(targetsApi.create).toHaveBeenCalledWith('dest@elsewhere.test')
    expect(wrapper.text()).toContain('https://posta.test/api/v1/inbound/forward-confirm/tok-123')
    expect(wrapper.text()).toContain('shown once')
  })

  it('toggles a rule through the update endpoint', async () => {
    const wrapper = await mountView()
    await buttonByText(wrapper, 'View Rules')!.trigger('click')
    await flushPromises()

    const toggle = wrapper.findAll('button').find((b) => b.text() === 'Enabled')!
    await toggle.trigger('click')
    await flushPromises()

    expect(routesApi.update).toHaveBeenCalledWith(11, {
      domain_id: 1,
      pattern: 'team@example.test',
      action: 'forward',
      target: 'dest@elsewhere.test',
      priority: 10,
      enabled: false,
    })
    expect(notify.success).toHaveBeenCalled()
  })

  it('deletes a rule after a danger confirmation and not when cancelled', async () => {
    const wrapper = await mountView()
    await buttonByText(wrapper, 'View Rules')!.trigger('click')
    await flushPromises()

    await buttonByText(wrapper, 'Delete')!.trigger('click')
    await flushPromises()

    expect(confirmMock).toHaveBeenCalledWith(expect.objectContaining({ variant: 'danger', title: 'Delete routing rule' }))
    expect(routesApi.remove).toHaveBeenCalledWith(11)

    confirmMock.mockResolvedValue(false)
    await buttonByText(wrapper, 'Delete')!.trigger('click')
    await flushPromises()
    expect(routesApi.remove).toHaveBeenCalledTimes(1)
  })

  it('surfaces the API error message when creating a rule fails', async () => {
    routesApi.create.mockRejectedValue({
      response: {
        status: 422,
        data: { success: false, data: null, error: { code: 'invalid_pattern', message: 'pattern conflicts with an existing rule', error: '' } },
      },
    })
    const wrapper = await mountView()

    await buttonByText(wrapper, 'Add Rule')!.trigger('click')
    await flushPromises()
    await wrapper
      .find('input[placeholder="user@example.com, user+tag@example.com, or @example.com"]')
      .setValue('team@example.test')
    await buttonByText(wrapper, 'Create Rule')!.trigger('click')
    await flushPromises()

    // The drawer stays open with the server's message; no success toast.
    expect(wrapper.text()).toContain('pattern conflicts with an existing rule')
    expect(wrapper.text()).toContain('New Routing Rule')
    expect(notify.success).not.toHaveBeenCalled()
  })

  it('hides mutations for view-only members', async () => {
    canEdit.value = false
    const wrapper = await mountView()
    await buttonByText(wrapper, 'View Rules')!.trigger('click')
    await flushPromises()

    expect(buttonByText(wrapper, 'Add Rule')).toBeUndefined()
    expect(buttonByText(wrapper, 'Edit')).toBeUndefined()
    expect(buttonByText(wrapper, 'Delete')).toBeUndefined()
    expect(buttonByText(wrapper, 'Revoke')).toBeUndefined()
    // The rules sub-table hides its Actions column (the outer domain table
    // keeps one — View Rules stays available to read the rules). The rules
    // table is nested inside the expanded outer row, so assert on the thead
    // that owns the unique "Pattern" header.
    const patternTh = wrapper.findAll('th').find((th) => th.text() === 'Pattern')!
    const ruleHeaders = Array.from(
      (patternTh.element.closest('thead') as HTMLTableSectionElement).querySelectorAll('th'),
    ).map((th) => th.textContent?.trim())
    expect(ruleHeaders).not.toContain('Actions')
    // View-only members still see the rules and the enabled state.
    expect(wrapper.text()).toContain('team@example.test')
    expect(wrapper.text()).toContain('Enabled')
  })

  it('offers the drawer only over verified domains and flags unverified domains as inert', async () => {
    const wrapper = await mountView()
    await buttonByText(wrapper, 'View Rules')!.trigger('click')
    await flushPromises()

    // The unverified domain's expanded panel carries the inert hint...
    await buttonByText(wrapper, 'View Rules')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('not verified')

    // ...and the drawer's domain select lists only the verified domain.
    await buttonByText(wrapper, 'Add Rule')!.trigger('click')
    await flushPromises()
    const domainSelect = wrapper.findAll('select')[0]
    expect(domainSelect.findAll('option').map((o) => o.text())).toEqual(['example.test'])
  })

  it('revokes a forward target after confirmation', async () => {
    targetsApi.list.mockResolvedValue(
      paginated([
        { id: 5, user_id: 7, target: 'dest@elsewhere.test', confirmed_at: '2026-11-06T11:00:00Z', created_at: '2026-11-06T10:00:00Z', updated_at: '2026-11-06T10:00:00Z' },
      ]),
    )
    const wrapper = await mountView()
    await flushPromises()

    await buttonByText(wrapper, 'Revoke')!.trigger('click')
    await flushPromises()

    expect(confirmMock).toHaveBeenCalledWith(expect.objectContaining({ variant: 'danger', title: 'Revoke forward target' }))
    expect(targetsApi.remove).toHaveBeenCalledWith(5)
    expect(notify.success).toHaveBeenCalled()
  })
})
