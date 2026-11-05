import api from './client'
import type { ApiResponse, AuthResponse, WebAuthnCredential } from './types'

// R-19 passkey support. The browser ceremony types are structural: the begin
// endpoints return the exact PublicKeyCredentialCreationOptions /
// PublicKeyCredentialRequestOptions dictionaries navigator.credentials
// consumes, and the finish endpoints accept the serialized credential.
//
// WebAuthnLevel-3 has PublicKeyCredential.toJSON, but serializing by hand keeps
// the payload byte-shape stable across browsers and lets us attach the email
// wrapper the login finish endpoint expects.

export interface WebAuthnAssertionPayload {
  email: string
  assertion?: SerializedCredential
  pending_token?: string
  two_factor_code?: string
}

export interface SerializedCredential {
  id: string
  rawId: string
  type: string
  response: Record<string, string | string[]>
}

export interface Requires2FAResult {
  requires_2fa: true
  message: string
  passkey_verified: boolean
  pending_token: string
}

function toBase64URL(buffer: ArrayBuffer | null): string {
  if (!buffer) return ''
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i])
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// serializeCreation converts a navigator.credentials.create() result into the
// registration finish payload.
export function serializeCreation(credential: PublicKeyCredential): SerializedCredential {
  const r = credential.response as AuthenticatorAttestationResponse
  const withTransports = r as AuthenticatorAttestationResponse & {
    getTransports?: () => string[]
  }
  return {
    id: credential.id,
    rawId: credential.id,
    type: credential.type,
    response: {
      clientDataJSON: toBase64URL(r.clientDataJSON),
      attestationObject: toBase64URL(r.attestationObject),
      transports: withTransports.getTransports?.() ?? [],
    },
  }
}

// serializeAssertion converts a navigator.credentials.get() result into the
// login finish payload.
export function serializeAssertion(credential: PublicKeyCredential): SerializedCredential {
  const r = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: credential.id,
    type: credential.type,
    response: {
      authenticatorData: toBase64URL(r.authenticatorData),
      clientDataJSON: toBase64URL(r.clientDataJSON),
      signature: toBase64URL(r.signature),
      userHandle: toBase64URL(r.userHandle),
    },
  }
}

// passkeysSupported reports whether this browser can run WebAuthn ceremonies
// at all; the login page hides the passkey button when it cannot.
export function passkeysSupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator?.credentials
}

export const webauthnApi = {
  // Credential management (Profile page)
  listCredentials() {
    return api.get<ApiResponse<WebAuthnCredential[]>>('/auth/webauthn/credentials')
  },
  renameCredential(id: string, name: string) {
    return api.put<ApiResponse<{ message: string }>>(
      `/auth/webauthn/credentials/${encodeURIComponent(id)}`,
      { name },
    )
  },
  deleteCredential(id: string) {
    return api.delete<ApiResponse<{ message: string }>>(
      `/auth/webauthn/credentials/${encodeURIComponent(id)}`,
    )
  },
  // Registration ceremony (Profile page)
  beginRegistration() {
    return api.post<ApiResponse<Record<string, unknown>>>('/auth/webauthn/register/begin')
  },
  finishRegistration(credential: SerializedCredential) {
    return api.post<ApiResponse<WebAuthnCredential>>('/auth/webauthn/register/finish', credential)
  },
  // Login ceremony (Login page). A 401 with requires_2fa + pending_token means
  // the passkey verified but the account owes the second factor.
  beginLogin(email: string) {
    return api.post<ApiResponse<Record<string, unknown>>>('/auth/webauthn/login/begin', { email })
  },
  finishLogin(payload: WebAuthnAssertionPayload) {
    return api.post<ApiResponse<AuthResponse>>('/auth/webauthn/login/finish', payload)
  },
}
