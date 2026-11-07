import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

// GAP-12: the login page is the enforcement front-end for the
// two_factor_required policy. When the store surfaces the forced-enrollment
// marker (the session is live, but the account owes its 2FA setup), the page
// must redirect to the Profile 2FA setup card instead of the dashboard.

const routerPush = vi.hoisted(() => vi.fn())
vi.mock('vue-router', () => ({
  // Login.vue also reads router.currentRoute.value.query.error at setup, so
  // the router double must carry a currentRoute too.
  useRouter: () => ({ push: routerPush, currentRoute: { value: { query: {} } } }),
  useRoute: () => ({ query: {} }),
}))

const authApi = vi.hoisted(() => ({
  registrationStatus: vi.fn().mockResolvedValue({
    data: { success: true, data: { registration_enabled: false, password_reset_enabled: false } },
  }),
}))
vi.mock('../../api/auth', () => ({ authApi }))

vi.mock('../../api/oauth', () => ({
  oauthApi: { providers: vi.fn().mockResolvedValue({ data: { success: true, data: { providers: [], sso_available: false } } }) },
}))

const webauthnApi = vi.hoisted(() => ({
  beginLogin: vi.fn(),
  finishLogin: vi.fn(),
}))
const passkeysSupported = vi.hoisted(() => vi.fn(() => false))
vi.mock('../../api/webauthn', () => ({
  webauthnApi,
  passkeysSupported,
  serializeAssertion: vi.fn(),
}))

const notify = vi.hoisted(() => ({ info: vi.fn(), error: vi.fn(), success: vi.fn() }))
vi.mock('../../stores/notification', () => ({ useNotificationStore: () => notify }))

// The store double reproduces the two ways login() can signal the enrollment
// requirement: the GAP-12 marker thrown by the store after a successful login,
// and the ordinary requires_2fa pending marker of an enrolled account.
const loginImpl = vi.hoisted(() => ({
  impl: (async () => undefined) as unknown as (email: string, password: string, code?: string) => Promise<unknown>,
}))
vi.mock('../../stores/auth', () => ({
  useAuthStore: () => ({
    login: (...args: [string, string, string?]) => loginImpl.impl(...args),
    passkeyLogin: vi.fn(),
  }),
}))

import Login from './Login.vue'

async function mountLogin() {
  const wrapper = mount(Login, {
    global: {
      stubs: { AuthLayout: { template: '<div><slot /></div>' }, RouterLink: true, Transition: true },
    },
  })
  await flushPromises()
  return wrapper
}

async function submitCredentials(wrapper: Awaited<ReturnType<typeof mountLogin>>) {
  await wrapper.find('#email').setValue('user@example.com')
  await wrapper.find('#password').setValue('correct-horse-battery')
  await wrapper.find('form').trigger('submit.prevent')
  await flushPromises()
}

beforeEach(() => {
  routerPush.mockClear()
  notify.info.mockClear()
  notify.error.mockClear()
  loginImpl.impl = async () => undefined
})

describe('Login.vue — GAP-12 forced enrollment', () => {
  it('redirects to the 2FA setup card when the policy demands enrollment', async () => {
    loginImpl.impl = async () => {
      throw { requires2FASetup: true }
    }

    const wrapper = await mountLogin()
    await submitCredentials(wrapper)

    expect(routerPush).toHaveBeenCalledWith({ path: '/profile', query: { setup: '2fa' } })
    expect(notify.info).toHaveBeenCalled()
  })

  it('does not redirect when login succeeds without the marker', async () => {
    loginImpl.impl = async () => ({ token: 'jwt', user: { id: 1 } })

    const wrapper = await mountLogin()
    await submitCredentials(wrapper)

    expect(routerPush).not.toHaveBeenCalledWith({ path: '/profile', query: { setup: '2fa' } })
    // The normal redirect happens (or stays silent) — never the setup card.
    expect(notify.info).not.toHaveBeenCalled()
  })

  it('stays on the 2FA code step for an enrolled account (requires_2fa)', async () => {
    loginImpl.impl = async () => {
      throw { requires2FA: true }
    }

    const wrapper = await mountLogin()
    await submitCredentials(wrapper)

    expect(routerPush).not.toHaveBeenCalled()
    expect(notify.info).toHaveBeenCalledWith('Please enter your two-factor authentication code.')
    // The code input appears in place of the password form. (Attribute
    // selector: an id starting with a digit is not valid CSS #id syntax.)
    expect(wrapper.find('[id="2fa-code"]').exists()).toBe(true)
  })
})
