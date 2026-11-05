import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

// ─── API contract: the frontend half of the Section-11 gate ────────────────
//
// internal/routes/contract_test.go registers every backend route and dumps the
// METHOD PATH pairs into docs/implementation/api_routes.txt. This spec reads
// the same file and asserts that every endpoint the api/*.ts modules call is
// registered on the backend. This is the R-21 guard: the frontend once called
// /subscriber-lists/preview while the backend registered
// /subscriber-lists/preview-segment, and nothing on either side failed until a
// user clicked the button.
//
// Workflow after an intentional API change:
//   1. regenerate the golden file: go test ./internal/routes/ -run TestAPIRouteContract -update
//   2. update the api/*.ts callers
//   3. if an endpoint is deliberately unreachable yet (backend route missing),
//      add it to KNOWN_DRIFT below with a comment — never delete the assertion.

const here = dirname(fileURLToPath(import.meta.url)) // web/src/api
const goldenPath = join(here, '..', '..', '..', 'docs', 'implementation', 'api_routes.txt')

const golden = readFileSync(goldenPath, 'utf-8')

/** Backend route paths, normalized: /api/v1 prefix stripped, {id:int} → {}. */
const backendRoutes = new Set(
  golden
    .split('\n')
    .filter((line) => line && !line.startsWith('#'))
    .map((line) => {
      const methodAndPath = line.match(/^(\S+)\s+(.*)$/)
      return (methodAndPath?.[2] ?? line).replace(/^\/api\/v1/, '').replace(/\{[^}]*\}/g, '{}')
    }),
)

// api modules that hold no endpoint calls.
const NON_ENDPOINT_MODULES = new Set(['client.ts', 'types.ts'])

/**
 * Endpoints the frontend calls that the backend does not register (checked
 * fresh on every run, so the list cannot rot silently). Each entry is a
 * documented, known defect of the R-21 class — the call sites exist, the
 * routes do not. Fixing one means deleting its entry; this spec then fails
 * if the endpoint ever drifts again.
 */
const KNOWN_DRIFT: Array<{ path: string; why: string }> = [
  {
    path: '/admin/api-keys',
    why: 'adminApi.listApiKeys (admin.ts) — dead API method, no view calls it, no GET /admin/api-keys route exists',
  },
  {
    path: '/admin/api-keys/{}',
    why: 'adminApi.revokeApiKey (admin.ts) — dead API method, no DELETE /admin/api-keys/{id} route exists',
  },
  {
    path: '/admin/emails',
    why: 'adminApi.listEmails (admin.ts) — dead API method, no view calls it, no GET /admin/emails route exists',
  },
  {
    path: '/users/me/contact-lists',
    why: 'contactListsApi list/create (contactLists.ts) — the backend ContactListHandler is complete but registered NOWHERE; Contact Lists pages 404',
  },
  {
    path: '/users/me/contact-lists/{}',
    why: 'contactListsApi update/delete (contactLists.ts) — same unregistered ContactListHandler',
  },
  {
    path: '/users/me/contact-lists/{}/members',
    why: 'contactListsApi listMembers/addMember/removeMember (contactLists.ts) — same unregistered ContactListHandler',
  },
]

/** api.<verb>(<first argument>) — the endpoint string as written in the module. */
const CALL_RE =
  /\bapi\.(?:get|post|put|delete|patch)(?:<[^()]*>)?\(\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|`[^`]*`)/g

/**
 * Pull the endpoint strings out of one api module. Template-literal prefixes
 * that reference a module-local BASE constant are resolved so the endpoints
 * are checked against the contract too.
 */
function extractEndpoints(source: string): string[] {
  const endpoints: string[] = []
  for (const match of source.matchAll(CALL_RE)) {
    let path = match[1].slice(1, -1).trim()
    path = path.replace(/^\$\{([A-Z_][A-Z0-9_]*)\}/, (placeholder, name: string) => {
      const constant = source.match(new RegExp('const ' + name + " = '([^']+)'"))
      return constant ? constant[1] : placeholder
    })
    if (path.startsWith('/')) endpoints.push(path)
  }
  return endpoints
}

/** Normalize a frontend path the same way the golden side is normalized. */
function canonicalize(path: string): string {
  let p = path.replace(/^\/api\/v1/, '')
  p = p.replace(/\$\{[^}]*\}/g, '{}')
  const query = p.indexOf('?')
  if (query >= 0) p = p.slice(0, query)
  return p
}

function matchesBackend(canonical: string): boolean {
  if (backendRoutes.has(canonical)) return true
  // Trailing {} placeholders are optional query-string suffixes (SSE stream
  // URLs append `?params`); anything after a real path separator must match.
  for (const route of backendRoutes) {
    if (canonical.startsWith(route) && /^(\{\}|\/\{\})+$/.test(canonical.slice(route.length))) {
      return true
    }
  }
  return false
}

const apiModules = readdirSync(here)
  .filter((file) => file.endsWith('.ts') && !file.endsWith('.spec.ts') && !NON_ENDPOINT_MODULES.has(file))
  .sort()

describe('API route contract (docs/implementation/api_routes.txt)', () => {
  it('golden file is a real route dump, not a stub', () => {
    expect(backendRoutes.size).toBeGreaterThan(200)
    // R-21 pin: the subscriber-list preview endpoint the frontend uses.
    expect(backendRoutes.has('/workspaces/current/subscriber-lists/preview-segment')).toBe(true)
    expect(backendRoutes.has('/info')).toBe(true)
  })

  it('every endpoint the api modules call is registered on the backend', () => {
    const unknown: string[] = []
    for (const file of apiModules) {
      const source = readFileSync(join(here, file), 'utf-8')
      for (const raw of extractEndpoints(source)) {
        const canonical = canonicalize(raw)
        if (matchesBackend(canonical)) continue
        if (KNOWN_DRIFT.some((d) => d.path === canonical)) continue
        unknown.push(`${file}: ${raw} (canonical ${canonical})`)
      }
    }
    expect(unknown).toEqual([])
  })

  it('every known-drift allowance is still drifted (stale entries must be removed)', () => {
    const stale = KNOWN_DRIFT.filter((d) => matchesBackend(d.path)).map((d) => d.path)
    expect(
      stale.map((p) => `${p} is now registered — delete its KNOWN_DRIFT entry`),
    ).toEqual([])
  })
})
