<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { authApi } from '../../api/auth'
import AuthLayout from './AuthLayout.vue'

const email = ref('')
const emailError = ref('')
const loading = ref(false)
const sent = ref(false)
const message = ref('')
const emailInput = ref<HTMLInputElement | null>(null)

const heading = computed(() => (sent.value ? 'Check your email' : 'Forgot your password?'))
const subheading = computed(() =>
  sent.value ? message.value : "Enter your account email and we'll send you a link to reset your password."
)

onMounted(() => emailInput.value?.focus())

async function submit() {
  emailError.value = ''
  if (!email.value) {
    emailError.value = 'Enter your email address.'
    return
  }
  loading.value = true
  try {
    const res = await authApi.forgotPassword(email.value)
    message.value = res.data.data?.message || 'If an account exists for that email, a reset link is on its way.'
    sent.value = true
  } catch (err: any) {
    emailError.value = err?.response?.data?.error?.message || 'Could not send the reset email. Please try again.'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <AuthLayout :hero="false" :title="heading" :subtitle="subheading">
    <form v-if="!sent" class="auth-form" @submit.prevent="submit">
      <div class="form-group">
        <label class="form-label" for="email">Email</label>
        <input
          id="email"
          ref="emailInput"
          v-model="email"
          type="email"
          class="form-input"
          :class="{ 'form-input-error': emailError }"
          placeholder="you@example.com"
          autocomplete="email"
          :disabled="loading"
          :aria-invalid="!!emailError"
          :aria-describedby="emailError ? 'email-error' : undefined"
          @input="emailError = ''"
        />
        <small v-if="emailError" id="email-error" class="form-error">{{ emailError }}</small>
      </div>
      <button type="submit" class="btn btn-primary auth-btn" :disabled="loading">
        <span v-if="loading" class="spinner"></span>
        {{ loading ? 'Sending...' : 'Send reset link' }}
      </button>
    </form>

    <div v-else class="auth-form">
      <div class="auth-success" role="status">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg>
        <span>Reset link sent. The link expires in 1 hour.</span>
      </div>
    </div>

    <div class="auth-footer">
      <router-link :to="{ name: 'login' }">Back to sign in</router-link>
    </div>
  </AuthLayout>
</template>

<style scoped>
.auth-form { margin-top: 4px; }
.auth-btn { width: 100%; padding: 11px 18px; font-size: 15px; margin-top: 4px; }
.form-input-error { border-color: var(--danger-500); }
.form-error { display: block; font-size: 12px; color: var(--danger-text); margin-top: 6px; }
.auth-success {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  font-size: 14px;
  color: var(--success-text);
  background: var(--success-50);
  border: 1px solid color-mix(in srgb, var(--success-500) 30%, transparent);
  border-radius: var(--radius);
}
.auth-success svg { flex-shrink: 0; }
.auth-footer {
  text-align: center;
  margin-top: 22px;
  font-size: 14px;
  color: var(--text-muted);
}
.auth-footer a { color: var(--accent); font-weight: 500; }
</style>
