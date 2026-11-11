import api from './client'
import type { ApiResponse, PaginatedResponse, Domain, SigningKey, DnsRecordView, DomainHealthReport, DomainDmarcResponse, DomainReputationReport, IPPoolView, IPPoolMemberView, DnsStatusView, DnsPublishResult, DnsDriftStatusView, DnsCredentialView } from './types'

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
  retireSigningKey(id: number, keyId: number) {
    return api.post<ApiResponse<SigningKey>>(`/workspaces/current/domains/${id}/signing-keys/${keyId}/retire`)
  },
  deleteSigningKey(id: number, keyId: number) {
    return api.delete(`/workspaces/current/domains/${id}/signing-keys/${keyId}`)
  },

  // ---- Deliverability health (R-07) ----
  // GET returns the stored report; POST re-runs the live DNS checks and needs
  // edit rights on the workspace (the backend 403s without them).
  health(id: number) {
    return api.get<ApiResponse<DomainHealthReport>>(`/workspaces/current/domains/${id}/health`)
  },
  diagnose(id: number) {
    return api.post<ApiResponse<DomainHealthReport>>(`/workspaces/current/domains/${id}/health/diagnose`)
  },

  // Stored DMARC aggregate-report view (R-17): summary over every ingested
  // report plus the newest stored records.
  dmarcSummary(id: number) {
    return api.get<ApiResponse<DomainDmarcResponse>>(`/workspaces/current/domains/${id}/dmarc`)
  },

  // ---- IP pools, warm-up, reputation (OB-6 W3, ADR-007 decision 7) ----
  // The pool list returns the workspace's own pools plus the deployment-wide
  // shared ones; assignment validates visibility server-side.
  listIPPools() {
    return api.get<ApiResponse<IPPoolView[]>>('/workspaces/current/ip-pools')
  },
  createIPPool(name: string, description = '') {
    return api.post<ApiResponse<IPPoolView>>('/workspaces/current/ip-pools', { name, description })
  },
  deleteIPPool(poolId: number) {
    return api.delete(`/workspaces/current/ip-pools/${poolId}`)
  },
  addIPPoolMember(poolId: number, ip: string, ptrName = '') {
    return api.post<ApiResponse<IPPoolMemberView>>(`/workspaces/current/ip-pools/${poolId}/members`, { ip, ptr_name: ptrName })
  },
  removeIPPoolMember(poolId: number, memberId: number) {
    return api.delete(`/workspaces/current/ip-pools/${poolId}/members/${memberId}`)
  },
  assignPool(id: number, poolId: number | null) {
    return api.put<ApiResponse<{ domain_id: number; pool_id: number | null }>>(`/workspaces/current/domains/${id}/pool`, { pool_id: poolId })
  },
  setWarmup(id: number, startVolume: number, dailyMultiplier: number, days: number) {
    return api.put<ApiResponse<{ domain_id: number; active: boolean; start_volume: number; daily_multiplier: number; days: number }>>(
      `/workspaces/current/domains/${id}/warmup`,
      { start_volume: startVolume, daily_multiplier: dailyMultiplier, days },
    )
  },
  reputation(id: number, days = 14) {
    return api.get<ApiResponse<DomainReputationReport>>(`/workspaces/current/domains/${id}/reputation`, { params: { days } })
  },

  // ---- DNS automation (OB-6 W1, ADR-007 decision 1) ----
  // The connect call accepts the credential material ONCE; every response
  // afterwards carries only a masked fingerprint. Publish writes the planned
  // records and ALWAYS re-runs the verification flow. Routes exist only when
  // the deployment enabled POSTA_DNS_AUTOMATION_ENABLED — callers treat 404
  // as "feature off".
  connectDNSProvider(id: number, provider: string, credential: { token?: string; access_key_id?: string; secret_access_key?: string; session_token?: string; region?: string }) {
    return api.post<ApiResponse<DnsCredentialView>>(`/workspaces/current/domains/${id}/dns-provider`, {
      provider,
      ...credential,
    })
  },
  dnsProviderStatus(id: number) {
    return api.get<ApiResponse<DnsStatusView>>(`/workspaces/current/domains/${id}/dns-provider`)
  },
  disconnectDNSProvider(id: number) {
    return api.delete<ApiResponse<{ revoked: boolean }>>(`/workspaces/current/domains/${id}/dns-provider`)
  },
  publishDNSRecords(id: number) {
    return api.post<ApiResponse<DnsPublishResult>>(`/workspaces/current/domains/${id}/dns-records/publish`)
  },
  dnsDrift(id: number) {
    return api.get<ApiResponse<DnsDriftStatusView>>(`/workspaces/current/domains/${id}/dns-drift`)
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
