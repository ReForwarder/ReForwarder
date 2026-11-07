// Posta push service worker (SF-89 Web Push).
//
// Receives the aes128gcm-decrypted JSON payload the backend pushed (see
// internal/services/webpush) and surfaces it as a notification. Like the
// realtime SSE contract, a push is a refetch hint, not a record: the body
// carries {type, workspace_id, message}.
self.addEventListener('push', (event) => {
  let body = { type: 'update', message: 'New activity in Posta' }
  try {
    if (event.data) body = event.data.json()
  } catch (e) {
    // Malformed frames still surface a generic notification.
  }
  const title = 'Posta'
  const options = {
    body: body.message || body.type || 'New activity',
    tag: body.type || 'posta',
    data: body,
    icon: '/logo-navy.png',
    badge: '/favicon-96x96.png',
  }
  event.waitUntil(self.registration.showNotification(title, options))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clientList) => {
      for (const client of clientList) {
        if ('focus' in client) return client.focus()
      }
      return self.clients.openWindow('/')
    }),
  )
})
