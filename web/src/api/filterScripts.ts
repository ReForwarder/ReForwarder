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
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// SF-14 per-mailbox filter scripts (the ADR-006 amendment): the webmail-side
// client for the per-user script management surface.
import api from './client'
import type { ApiResponse } from './types'

export interface FilterScriptCondition {
  field: string
  operator: string
  value: unknown
}

export interface FilterScriptAction {
  type: 'fileinto' | 'redirect' | 'discard' | 'keep'
  target?: string
}

export interface FilterScript {
  id: number
  user_id: number
  mailbox_id: number
  name: string
  priority: number
  enabled: boolean
  conditions: FilterScriptCondition[]
  actions_json: string
  created_at: string
  updated_at: string
}

export interface FilterScriptCreateInput {
  mailbox: number
  name: string
  priority?: number
  enabled?: boolean
  conditions: FilterScriptCondition[]
  actions: FilterScriptAction[]
}

export interface FilterScriptPatchInput {
  name?: string
  priority?: number
  enabled?: boolean
  conditions?: FilterScriptCondition[]
  actions?: FilterScriptAction[]
}

export function listFilterScripts(mailbox: number) {
  return api.get<ApiResponse<FilterScript[]>>('/mail/filter-scripts', { params: { mailbox } })
}

export function createFilterScript(input: FilterScriptCreateInput) {
  return api.post<ApiResponse<FilterScript>>('/mail/filter-scripts', input)
}

export function patchFilterScript(id: number, input: FilterScriptPatchInput) {
  return api.patch<ApiResponse<FilterScript>>(`/mail/filter-scripts/${id}`, input)
}

export function deleteFilterScript(id: number) {
  return api.delete<ApiResponse<{ message: string }>>(`/mail/filter-scripts/${id}`)
}
