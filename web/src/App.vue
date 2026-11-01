<script setup lang="ts">
import { useNotificationStore } from './stores/notification'
import { useThemeStore } from './stores/theme'
import ConfirmDialog from './components/ConfirmDialog.vue'

useThemeStore()
const notify = useNotificationStore()
</script>

<template>
  <div>
    <router-view />
    <ConfirmDialog />
    <div class="toast-container">
      <div
        v-for="n in notify.notifications"
        :key="n.id"
        class="toast"
        :class="`toast-${n.type}`"
        @click="notify.dismiss(n.id)"
      >
        {{ n.message }}
      </div>
    </div>
  </div>
</template>
