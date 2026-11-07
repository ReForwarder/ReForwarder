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

import api from './client'
import type {
  ApiResponse,
  PaginatedResponse,
  InboundRoute,
  InboundRouteInput,
  InboundForwardTarget,
  InboundCreatedForwardTarget,
} from './types'

export interface InboundRouteListParams {
  page?: number
  size?: number
  domain_id?: number
}

// Inbound routing rules (OB-2, ADR-002 decision 1): per-verified-domain
// recipient patterns (exact address, sub-address spec, or "@domain"
// catch-all) whose action the engine executes only AFTER the message is
// durably persisted. First-match-wins by priority; a recipient that matches
// no rule and no catch-all is rejected.
//
// NOTE (integration): the rules CRUD handler is landing in parallel; this
// client targets the workspace-scoped resource convention every other
// collection in this API follows (list/create/PUT update/DELETE under
// /workspaces/current/<resource>, dto envelope + PageableResponse).
export const inboundRoutesApi = {
  list(params: InboundRouteListParams = {}) {
    return api.get<PaginatedResponse<InboundRoute>>('/workspaces/current/inbound-routes', {
      params: { page: 0, size: 100, ...params },
    })
  },
  create(data: InboundRouteInput) {
    return api.post<ApiResponse<InboundRoute>>('/workspaces/current/inbound-routes', data)
  },
  update(id: number, data: InboundRouteInput) {
    return api.put<ApiResponse<InboundRoute>>(`/workspaces/current/inbound-routes/${id}`, data)
  },
  remove(id: number) {
    return api.delete<ApiResponse<InboundRoute>>(`/workspaces/current/inbound-routes/${id}`)
  },
}

export interface ForwardTargetListParams {
  page?: number
  size?: number
}

// Confirmed forward destinations (OB-2, ADR-002 decision 2 — endpoints in
// internal/routes/inbound_forward_routes.go): forwarding re-sends to an
// address only while its target row is confirmed, i.e. the mailbox owner
// used the single-use plain-text confirmation link. The link URL comes back
// exactly once at creation; the dashboard only displays and copies it — the
// public GET /inbound/forward-confirm/{token} endpoint is the link's
// destination, consumed in a browser, not through this client.
export const inboundForwardTargetsApi = {
  list(params: ForwardTargetListParams = {}) {
    return api.get<PaginatedResponse<InboundForwardTarget>>(
      '/workspaces/current/inbound/forward-targets',
      { params: { page: 0, size: 50, ...params } },
    )
  },
  create(target: string) {
    return api.post<ApiResponse<InboundCreatedForwardTarget>>(
      '/workspaces/current/inbound/forward-targets',
      { target },
    )
  },
  remove(id: number) {
    return api.delete<ApiResponse<InboundForwardTarget>>(
      `/workspaces/current/inbound/forward-targets/${id}`,
    )
  },
}
