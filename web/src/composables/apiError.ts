// Status phrases the server sends as `error.message` when it has nothing more
// specific to say. Showing one of these to a user is no better than showing
// nothing, so the more detailed `error.error` is preferred whenever the message
// is only the status text — that is where a bind or validation failure explains
// itself.
const GENERIC = new Set([
  'bad request',
  'unauthorized',
  'forbidden',
  'not found',
  'method not allowed',
  'conflict',
  'unprocessable entity',
  'too many requests',
  'internal server error',
  'bad gateway',
  'service unavailable',
  'error',
])

function usable(value: unknown): string {
  return typeof value === 'string' && value.trim() ? value.trim() : ''
}

function specific(value: unknown): string {
  const text = usable(value)
  return text && !GENERIC.has(text.toLowerCase()) ? text : ''
}

export function apiMessage(e: any, fallback: string): string {
  const data = e?.response?.data

  // A message the handler wrote itself wins; the status phrase is a last resort
  // ahead of the caller's fallback only because it at least names the failure.
  return (
    specific(data?.error?.message) ||
    specific(data?.error?.error) ||
    specific(data?.message) ||
    specific(data?.details) ||
    fallback
  )
}
