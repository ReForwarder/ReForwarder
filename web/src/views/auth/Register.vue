<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { authApi } from '../../api/auth'
import { useAuthStore } from '../../stores/auth'
import { useNotificationStore } from '../../stores/notification'
import AuthLayout from './AuthLayout.vue'

const router = useRouter()
const auth = useAuthStore()
const notification = useNotificationStore()

const name = ref('')
const email = ref('')
const password = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const registrationEnabled = ref<boolean | null>(null)

const nameError = ref('')
const emailError = ref('')
const passwordError = ref('')
const confirmError = ref('')
const showPassword = ref(false)
const showConfirm = ref(false)

onMounted(async () => {
  try {
    const res = await authApi.registrationStatus()
    registrationEnabled.value = res.data.data.registration_enabled
    if (!res.data.data.registration_enabled) {
      router.replace('/login')
    }
  } catch {
    router.replace('/login')
  }
})

async function handleRegister() {
  nameError.value = ''
  emailError.value = ''
  passwordError.value = ''
  confirmError.value = ''
  if (!name.value.trim()) nameError.value = 'Enter your name.'
  if (!email.value) emailError.value = 'Enter your email address.'
  if (!password.value) {
    passwordError.value = 'Enter a password.'
  } else if (password.value.length < 8) {
    passwordError.value = 'Password must be at least 8 characters.'
  }
  if (!confirmPassword.value) {
    confirmError.value = 'Confirm your password.'
  } else if (password.value && password.value !== confirmPassword.value) {
    confirmError.value = 'Passwords do not match.'
  }
  if (nameError.value || emailError.value || passwordError.value || confirmError.value) return

  loading.value = true
  try {
    // Register auto-logs-in by setting the session cookie on the response.
    const res = await authApi.register(name.value.trim(), email.value.trim(), password.value)
    auth.setUser(res.data.data.user)
    window.location.href = '/'
  } catch (err: any) {
    const message = err?.response?.data?.error?.message || err?.response?.data?.error || err?.message || 'Registration failed.'
    notification.error(message)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <AuthLayout
    v-if="registrationEnabled"
    title="Create your account"
    subtitle="Get started in under a minute."
  >
    <form class="auth-form" @submit.prevent="handleRegister">
      <div class="form-group">
        <label class="form-label" for="name">Name</label>
        <input
          id="name"
          v-model="name"
          type="text"
          class="form-input"
          :class="{ 'form-input-error': nameError }"
          placeholder="Your name"
          autocomplete="name"
          @input="nameError = ''"
        />
        <small v-if="nameError" class="form-error">{{ nameError }}</small>
      </div>
      <div class="form-group">
        <label class="form-label" for="email">Email</label>
        <input
          id="email"
          v-model="email"
          type="email"
          class="form-input"
          :class="{ 'form-input-error': emailError }"
          placeholder="you@example.com"
          autocomplete="email"
          @input="emailError = ''"
        />
        <small v-if="emailError" class="form-error">{{ emailError }}</small>
      </div>
      <div class="form-group">
        <label class="form-label" for="password">Password</label>
        <div class="password-wrap">
          <input
            id="password"
            v-model="password"
            :type="showPassword ? 'text' : 'password'"
            class="form-input"
            :class="{ 'form-input-error': passwordError }"
            placeholder="Minimum 8 characters"
            autocomplete="new-password"
            @input="passwordError = ''"
          />
          <button
            type="button"
            class="password-toggle"
            :aria-label="showPassword ? 'Hide password' : 'Show password'"
            :title="showPassword ? 'Hide password' : 'Show password'"
            @click="showPassword = !showPassword"
          >
            <svg v-if="showPassword" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0112 20c-7 0-11-8-11-8a18.45 18.45 0 015.06-5.94M9.9 4.24A9.12 9.12 0 0112 4c7 0 11 8 11 8a18.5 18.5 0 01-2.16 3.19m-6.72-1.07a3 3 0 11-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
            <svg v-else width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
          </button>
        </div>
        <small v-if="passwordError" class="form-error">{{ passwordError }}</small>
      </div>
      <div class="form-group">
        <label class="form-label" for="confirm-password">Confirm password</label>
        <div class="password-wrap">
          <input
            id="confirm-password"
            v-model="confirmPassword"
            :type="showConfirm ? 'text' : 'password'"
            class="form-input"
            :class="{ 'form-input-error': confirmError }"
            placeholder="Re-enter your password"
            autocomplete="new-password"
            @input="confirmError = ''"
          />
          <button
            type="button"
            class="password-toggle"
            :aria-label="showConfirm ? 'Hide password' : 'Show password'"
            :title="showConfirm ? 'Hide password' : 'Show password'"
            @click="showConfirm = !showConfirm"
          >
            <svg v-if="showConfirm" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0112 20c-7 0-11-8-11-8a18.45 18.45 0 015.06-5.94M9.9 4.24A9.12 9.12 0 0112 4c7 0 11 8 11 8a18.5 18.5 0 01-2.16 3.19m-6.72-1.07a3 3 0 11-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
            <svg v-else width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
          </button>
        </div>
        <small v-if="confirmError" class="form-error">{{ confirmError }}</small>
      </div>
      <button type="submit" class="btn btn-primary auth-btn" :disabled="loading">
        <span v-if="loading" class="spinner"></span>
        {{ loading ? 'Creating account…' : 'Create account' }}
      </button>
    </form>

    <div class="auth-footer">
      <span>Already have an account?</span>
      <router-link to="/login">Sign in</router-link>
    </div>
  </AuthLayout>
</template>

<style scoped>
.auth-form { margin-top: 4px; }

.auth-btn {
  width: 100%;
  padding: 11px 18px;
  font-size: 15px;
  margin-top: 4px;
}

.password-wrap { position: relative; }
.password-wrap .form-input { padding-right: 40px; }
.password-toggle {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: 36px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
}
.password-toggle:hover { color: var(--text-primary); }

.form-input-error { border-color: var(--danger-500); }
.form-input-error:focus { border-color: var(--danger-500); }

.form-error {
  display: block;
  font-size: 12px;
  color: var(--danger-text);
  margin-top: 6px;
}

.auth-footer {
  text-align: center;
  margin-top: 22px;
  font-size: 14px;
  color: var(--text-muted);
  display: flex;
  gap: 6px;
  justify-content: center;
}
.auth-footer a { color: var(--accent); font-weight: 500; }
</style>
