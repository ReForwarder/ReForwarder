import api from './client'
import type { ApiResponse, PaginatedResponse, Domain, SigningKey, DnsRecordView } from './types'

export const domainsApi = {
  list(page = 0, size = 20) {
    return api.get<PaginatedResponse<Domain>>('/workspaces/current/domains', { params: { page, size } })
  },
  get(id: number) {
    return api.get<ApiResponse<Domain>>(`/workspaces/current/domains/${id}`)
  },
  create(domain: string) {
    return api.post<ApiResponse<Domain>>('/workspaces/current/domains', { domain })
  },
  verify(id: number) {
    return api.post<ApiResponse<Domain>>(`/workspaces/current/domains/${id}/verify`)
  },
  delete(id: number) {
    return api.delete(`/workspaces/current/domains/${id}`)
  },

  // ---- Managed DKIM signing keys (R-02) ----
  // The private key never leaves the server: every response carries only the
  // public DNS TXT record to publish.
  listSigningKeys(id: number) {
    return api.get<ApiResponse<SigningKey[]>>(`/workspaces/current/domains/${id}/signing-keys`)
  },
  generateSigningKey(id: number, selector: string, algorithm: string) {
    return api.post<ApiResponse<SigningKey>>(`/workspaces/current/domains/${id}/signing-keys`, {
      selector,
      algorithm,
    })
  },
  rotateSigningKey(id: number, selector: string, algorithm: string) {
    return api.post<ApiResponse<SigningKey>>(`/workspaces/current/domains/${id}/signing-keys/rotate`, {
      selector,
      algorithm,
    })
  },
  verifySigningKeyDns(id: number, keyId: number) {
    return api.post<ApiResponse<SigningKeyDnsCheck>>(`/workspaces/current/domains/${id}/signing-keys/${keyId}/verify-dns`)
  },
  deleteSigningKey(id: number, keyId: number) {
    return api.delete(`/workspaces/current/domains/${id}/signing-keys/${keyId}`)
  },
}

/** Result of POST .../signing-keys/{keyId}/verify-dns. */
export interface SigningKeyDnsCheck {
  published: boolean
  dns_state: string
  record?: string
  error?: string
  key: SigningKey
}

export type { DnsRecordView }
