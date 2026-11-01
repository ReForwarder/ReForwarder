<script setup lang="ts">
import { ref } from 'vue'
import { authApi } from '../../api/auth'
import { useNotificationStore } from '../../stores/notification'

const notify = useNotificationStore()

const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const loading = ref(false)

async function handleSubmit() {
  if (!currentPassword.value || !newPassword.value || !confirmPassword.value) {
    notify.error('Please fill in all fields')
    return
  }
  if (newPassword.value.length < 8) {
    notify.error('New password must be at least 8 characters')
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    notify.error('New passwords do not match')
    return
  }
  loading.value = true
  try {
    await authApi.changePassword(currentPassword.value, newPassword.value)
    notify.success('Password changed successfully')
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  } catch (e: any) {
    const message = e?.response?.data?.error?.message || 'Failed to change password'
    notify.error(message)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Change Password</h1>
    </div>

    <div class="card">
      <div class="card-header"><h2>Update your password</h2></div>
      <div class="card-body">
        <form @submit.prevent="handleSubmit" style="display: grid; gap: 1rem; max-width: 480px;">
          <div class="form-group">
            <label class="form-label" for="current-password">Current Password</label>
            <input id="current-password" v-model="currentPassword" type="password" class="form-input" placeholder="Enter current password" required autocomplete="current-password" />
          </div>
          <div class="form-group">
            <label class="form-label" for="new-password">New Password</label>
            <input id="new-password" v-model="newPassword" type="password" class="form-input" placeholder="Minimum 8 characters" required minlength="8" autocomplete="new-password" />
          </div>
          <div class="form-group">
            <label class="form-label" for="confirm-password">Confirm New Password</label>
            <input id="confirm-password" v-model="confirmPassword" type="password" class="form-input" placeholder="Re-enter new password" required minlength="8" autocomplete="new-password" />
          </div>
          <button type="submit" class="btn btn-primary" :disabled="loading">
            {{ loading ? 'Updating...' : 'Change Password' }}
          </button>
        </form>
      </div>
    </div>
  </div>
</template>
