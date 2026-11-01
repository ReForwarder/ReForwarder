import { defineStore } from 'pinia'
import { ref } from 'vue'

interface Notification {
  id: number
  message: string
  type: 'success' | 'error' | 'info'
}

export const useNotificationStore = defineStore('notification', () => {
  const notifications = ref<Notification[]>([])
  let nextId = 0

  function show(message: string, type: 'success' | 'error' | 'info' = 'info', duration = 4000) {
    const id = nextId++
    notifications.value.push({ id, message, type })
    if (duration > 0) {
      setTimeout(() => dismiss(id), duration)
    }
  }

  function success(message: string) { show(message, 'success') }
  function error(message: string) { show(message, 'error', 6000) }
  function info(message: string) { show(message, 'info') }

  function dismiss(id: number) {
    notifications.value = notifications.value.filter(n => n.id !== id)
  }

  return { notifications, show, success, error, info, dismiss }
})
