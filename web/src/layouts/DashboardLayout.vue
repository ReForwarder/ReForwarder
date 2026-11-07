<script setup lang="ts">
import { computed, ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { useThemeStore } from '../stores/theme'
import { useWorkspaceStore } from '../stores/workspace'
import { infoApi, type AppInfo } from '../api/info'
import { adminApi } from '../api/admin'
import NotificationBell from '../components/NotificationBell.vue'
import type { UpdateInfo } from '../api/types'
import EmailVerificationBanner from '../components/EmailVerificationBanner.vue'
import ImpersonationBanner from '../components/ImpersonationBanner.vue'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const theme = useThemeStore()
const wsStore = useWorkspaceStore()
const sidebarCollapsed = ref(localStorage.getItem('posta_sidebar_collapsed') === 'true')
const mobileOpen = ref(false)
const appInfo = ref<AppInfo | null>(null)
const userMenuOpen = ref(false)
const wsSwitcherOpen = ref(false)
const themeModes = ['light', 'dark', 'system'] as const


function goWorkspaceSettings() {
  const id = wsStore.currentWorkspace?.id
  if (id) router.push(`/workspaces/${id}`)
}

function closeUserMenu(e: MouseEvent) {
  const el = document.querySelector('.user-menu')
  if (el && !el.contains(e.target as Node)) {
    userMenuOpen.value = false
  }
  if (wsSwitcherOpen.value) {
    const inSwitcher = (e.target as Element)?.closest?.('.ws-switcher')
    if (!inSwitcher) wsSwitcherOpen.value = false
  }
}

const toggleSidebar = () => {
  sidebarCollapsed.value = !sidebarCollapsed.value
  localStorage.setItem('posta_sidebar_collapsed', String(sidebarCollapsed.value))
}

// New-release notice (platform admins only). Dismissal is stored server-side
// against the version, not in localStorage: it must survive a browser change and
// it must come back when the *next* version lands.
const update = ref<UpdateInfo | null>(null)
const showUpdateBanner = computed(() => auth.isAdmin && update.value?.update_available === true)

async function loadUpdate() {
  if (!auth.isAdmin) return
  try {
    update.value = (await adminApi.getUpdate()).data.data
  } catch {
    // Non-critical; the dashboard works without it.
  }
}

async function dismissUpdate() {
  const version = update.value?.latest_version
  if (!version) return
  const previous = update.value
  update.value = { ...previous!, update_available: false } // optimistic
  try {
    await adminApi.dismissUpdate(version)
  } catch {
    update.value = previous
  }
}

onMounted(async () => {
  document.addEventListener('click', closeUserMenu)
  try {
    const res = await infoApi.get()
    appInfo.value = res.data.data
  } catch {
    // Version display is non-critical
  }

  loadUpdate()
  wsStore.fetchWorkspaces()
})
onBeforeUnmount(() => {
  document.removeEventListener('click', closeUserMenu)
})

const user = computed(() => auth.user)

interface NavItem {
  name: string
  path: string
  icon: string
  /** Render as a plain external link instead of a router-link. */
  external?: boolean
  /** Only show when a real workspace (not Personal) is selected. */
  requiresWorkspace?: boolean
  /** Only show when the user is an owner or admin of the current workspace. */
  requiresWorkspaceAdmin?: boolean
  /** Hidden while the active workspace is the platform's system workspace. */
  hideInSystem?: boolean
  /** Only show when the backend exposes the OpenAPI docs. */
  requiresOpenapiDocs?: boolean
  /** Only show when the backend reports POSTA_MESSAGES_ENABLED (via /info). */
  requiresMessages?: boolean
  /** Only show when the backend reports POSTA_INBOUND_ENABLED (via /info). */
  requiresInbound?: boolean
  /** Deep-link into a WorkspaceSettings tab of the current workspace. */
  workspaceTab?: 'members' | 'plan' | 'settings'
  /** Link to a dedicated workspace sub-page (e.g. /workspaces/:id/members). */
  workspaceSubpath?: string
  /** Additional route path prefixes that should keep this item highlighted (grouped tab pages). */
  matchPaths?: string[]
  /** Match only the exact path — for children whose parent path is their prefix. */
  exact?: boolean
  /** Render as a collapsible group header instead of a link; clicking toggles the children. */
  collapsible?: boolean
  /** Nested items shown indented under this entry. */
  children?: NavItem[]
}

interface NavSection {
  id: string
  title: string
  items: NavItem[]
  /** Only rendered for platform admins. */
  admin?: boolean
  /** Hidden in Personal mode; only shown when a real workspace is selected. */
  requiresWorkspace?: boolean
  /** Whether the section starts expanded. Defaults to true. */
  defaultOpen?: boolean
}

const navSections: NavSection[] = [
  {
    id: 'overview',
    title: 'Overview',
    items: [
      { name: 'Dashboard', path: '/', icon: 'grid' },
      { name: 'Analytics', path: '/analytics', icon: 'bar-chart' },
    ],
  },
  {
    id: 'sending',
    title: 'Sending',
    items: [
      { name: 'Emails', path: '/emails', icon: 'mail' },
      { name: 'Campaigns', path: '/campaigns', icon: 'send' },
      {
        name: 'Templates',
        path: '/templates',
        icon: 'file-text',
        children: [
          { name: 'Stylesheets', path: '/stylesheets', icon: 'type' },
          { name: 'Languages', path: '/languages', icon: 'globe' },
        ],
      },
    ],
  },
  {
    id: 'inbound',
    title: 'Inbound',
    items: [
      // POSTA_INBOUND_ENABLED gates the whole inbound pipeline; when it is off
      // every inbound page errors, so the entry point is hidden (Section-11).
      { name: 'Inbound Emails', path: '/inbound-emails', icon: 'inbox', requiresInbound: true },
      { name: 'Quarantine', path: '/inbound-quarantine', icon: 'shield', requiresInbound: true },
      { name: 'Routes', path: '/inbound-routes', icon: 'layers', requiresInbound: true },
      {
        name: 'Messages',
        path: '/messages',
        icon: 'message-square',
        requiresMessages: true,
        children: [
          { name: 'Forms', path: '/forms', icon: 'edit-3', requiresMessages: true },
          { name: 'Spam Filters', path: '/message-filters', icon: 'shield', requiresMessages: true },
        ],
      },
    ],
  },
  {
    id: 'audience',
    title: 'Audience',
    items: [
      { name: 'Contacts', path: '/contacts', icon: 'users' },
      { name: 'Subscribers', path: '/subscribers', icon: 'user-check' },
      { name: 'Subscriber Lists', path: '/subscriber-lists', icon: 'list' },
      { name: 'Segments', path: '/segments', icon: 'users-round' },
      { name: 'Automations', path: '/automations', icon: 'workflow' },
      { name: 'Unsubscribe Lists', path: '/unsubscribe-lists', icon: 'mail-x' },
    ],
  },
  {
    id: 'deliverability',
    title: 'Deliverability',
    items: [
      { name: 'Bounces & Suppressions', path: '/bounces', icon: 'alert-triangle' },
    ],
  },
  {
    id: 'logs',
    title: 'Logs',
    items: [
      {
        name: 'Message History',
        path: '/logs/messages',
        icon: 'history',
        // Group header, not a link — Message History is this group's landing page.
        collapsible: true,
        children: [
          { name: 'Message History', path: '/logs/messages', icon: 'history' },
          { name: 'Incoming Webhooks', path: '/logs/incoming', icon: 'inbox' },
          { name: 'Outgoing Webhooks', path: '/logs/outgoing', icon: 'send' },
          { name: 'Audit Logs', path: '/logs/audit', icon: 'list' },
        ],
      },
    ],
  },
  {
    id: 'developers',
    title: 'Developers',
    defaultOpen: true,
    items: [
      { name: 'API Keys', path: '/api-keys', icon: 'key' },
      { name: 'SMTP Relay', path: '/smtp-relay', icon: 'server' },
      { name: 'Webhooks', path: '/webhooks', icon: 'link' },
      { name: 'Webhook Deliveries', path: '/webhook-deliveries', icon: 'activity' },
      { name: 'API Reference', path: '/docs', icon: 'book-open', external: true, requiresOpenapiDocs: true },
    ],
  },
  {
    id: 'infrastructure',
    title: 'Infrastructure',
    defaultOpen: true,
    items: [
      { name: 'Domains', path: '/domains', icon: 'globe' },
      { name: 'SMTP Servers', path: '/smtp-servers', icon: 'server' },
    ],
  },
  {
    id: 'workspace',
    title: 'Workspace',
    requiresWorkspace: true,
    defaultOpen: true,
    items: [
      { name: 'All Workspaces', path: '/workspaces', icon: 'layers' },
      { name: 'Members', path: '', icon: 'users', workspaceSubpath: 'members' },
      { name: 'Audit Log', path: '/audit-log', icon: 'history' },
      { name: 'Settings', path: '', icon: 'settings', workspaceTab: 'settings', requiresWorkspaceAdmin: true },
    ],
  },
  {
    id: 'admin',
    title: 'Admin',
    admin: true,
    defaultOpen: false,
    items: [
      // The dashboard is the landing page for this section, so it leads.
      { name: 'Dashboard', path: '/admin/dashboard', icon: 'pie-chart' },
      { name: 'Users', path: '/admin/users', icon: 'users' },
      { name: 'Workspaces', path: '/admin/workspaces', icon: 'briefcase' },
      { name: 'Plans', path: '/admin/plans', icon: 'layers' },
      { name: 'Shared Servers', path: '/admin/servers', icon: 'server' },
      { name: 'Domains', path: '/admin/domains', icon: 'globe' },
      { name: 'Announcements', path: '/admin/announcements', icon: 'bell' },
      { name: 'OAuth', path: '/admin/oauth', icon: 'shield' },
      { name: 'Jobs', path: '/admin/jobs', icon: 'clock' },
      { name: 'Events', path: '/admin/events', icon: 'zap' },
      {
        name: 'Security',
        path: '/admin/security',
        icon: 'shield',
        // Group header, not a link — Overview is this section's landing page.
        collapsible: true,
        children: [
          // Overview shares the group's path, so it matches exactly only.
          { name: 'Overview', path: '/admin/security', icon: 'shield', exact: true },
          { name: 'Blocked IPs', path: '/admin/security/blocked', icon: 'alert-triangle' },
          { name: 'Detection Events', path: '/admin/security/events', icon: 'zap' },
          { name: 'Rules', path: '/admin/security/rules', icon: 'settings' },
          { name: 'Allowlist', path: '/admin/security/allowlist', icon: 'user-check' },
        ],
      },
      { name: 'Platform Settings', path: '/admin/settings', icon: 'settings' },
    ],
  },
]

const SECTION_STATE_KEY = 'posta_nav_sections'

function loadSectionState(): Record<string, boolean> {
  const defaults: Record<string, boolean> = {}
  navSections.forEach((s) => {
    defaults[s.id] = s.defaultOpen !== false
  })
  try {
    const saved = JSON.parse(localStorage.getItem(SECTION_STATE_KEY) || '{}')
    return { ...defaults, ...saved }
  } catch {
    return defaults
  }
}

const sectionOpen = ref<Record<string, boolean>>(loadSectionState())

function toggleSection(id: string) {
  sectionOpen.value[id] = !sectionOpen.value[id]
  localStorage.setItem(SECTION_STATE_KEY, JSON.stringify(sectionOpen.value))
}

const GROUP_STATE_KEY = 'posta_nav_groups'

function loadGroupState(): Record<string, boolean> {
  // Groups start expanded, matching the always-visible children this replaces.
  const defaults: Record<string, boolean> = {}
  navSections.forEach((s) => {
    s.items.forEach((item) => {
      if (item.collapsible) defaults[item.name] = true
    })
  })
  try {
    const saved = JSON.parse(localStorage.getItem(GROUP_STATE_KEY) || '{}')
    return { ...defaults, ...saved }
  } catch {
    return defaults
  }
}

const groupOpen = ref<Record<string, boolean>>(loadGroupState())

function toggleGroup(name: string) {
  groupOpen.value[name] = !groupOpen.value[name]
  localStorage.setItem(GROUP_STATE_KEY, JSON.stringify(groupOpen.value))
}

const visibleSections = computed(() =>
  navSections.filter((s) => {
    if (s.admin && !auth.isAdmin) return false
    if (s.requiresWorkspace && !wsStore.isWorkspaceContext) return false
    return true
  })
)

/** One visibility predicate for top-level items and nested children alike. */
function isItemVisible(item: NavItem): boolean {
  if (item.requiresWorkspace && !wsStore.isWorkspaceContext) return false
  if (item.requiresWorkspaceAdmin && !wsStore.isWorkspaceAdmin) return false
  if (item.hideInSystem && wsStore.currentWorkspaceIsSystem) return false
  if (item.requiresOpenapiDocs && !appInfo.value?.openapi_docs) return false
  // Feature-gated entries (Section-11 gate): hidden only when /info has
  // explicitly reported the flag false. Unlike requiresOpenapiDocs this
  // fails open — before /info answers (or if it never does) the entries
  // stay visible, because hiding the Messages or Inbound pages on a failed
  // info call would hide core navigation, not a docs link.
  if (item.requiresMessages && appInfo.value?.features?.messages_enabled === false) return false
  if (item.requiresInbound && appInfo.value?.features?.inbound_enabled === false) return false
  return true
}

function sectionItems(section: NavSection): NavItem[] {
  return section.items.filter(isItemVisible)
}

/** Children of a collapsible item, filtered through the same predicate. */
function visibleChildren(item: NavItem): NavItem[] {
  return (item.children ?? []).filter(isItemVisible)
}

/** Resolve the effective target of an item (handles workspace tab deep-links). */
function itemTo(item: NavItem): string {
  if (item.workspaceSubpath) {
    return `/workspaces/${wsStore.currentWorkspaceId}/${item.workspaceSubpath}`
  }
  if (item.workspaceTab) {
    return `/workspaces/${wsStore.currentWorkspaceId}?tab=${item.workspaceTab}`
  }
  return item.path
}

function isActive(path: string): boolean {
  if (path === '/') return route.path === '/'
  return route.path === path || route.path.startsWith(path + '/')
}

function isItemActive(item: NavItem): boolean {
  if (item.workspaceSubpath) {
    return route.path === `/workspaces/${wsStore.currentWorkspaceId}/${item.workspaceSubpath}`
  }
  if (item.workspaceTab) {
    return (
      route.path === `/workspaces/${wsStore.currentWorkspaceId}` &&
      (route.query.tab || 'settings') === item.workspaceTab
    )
  }
  // "Manage Workspaces…" should only highlight on the list page itself,
  // not on a workspace detail/tab page.
  if (item.path === '/workspaces') return route.path === '/workspaces'
  if (item.matchPaths?.some((p) => route.path === p || route.path.startsWith(p + '/'))) return true
  // Group children that live under their parent's path (Admin · Security)
  // must not stay lit on their siblings' pages.
  if (item.exact) return route.path === item.path
  return isActive(item.path)
}

/** A collapsible group is "in section" when any child matches the current route. */
function isGroupActive(item: NavItem): boolean {
  return !!item.children?.some((child) => isItemActive(child))
}

// Keep collapsible groups expanded while one of their children is the active
// page — including direct loads of a child URL. Only opens; never force-closes.
watch(
  () => route.path,
  () => {
    navSections.forEach((section) => {
      section.items.forEach((item) => {
        if (item.collapsible && isGroupActive(item)) groupOpen.value[item.name] = true
      })
    })
  },
  { immediate: true }
)

function navigate(path: string) {
  router.push(path)
}

function switchContext(wsId: number | null) {
  wsStore.setWorkspace(wsId)
  wsSwitcherOpen.value = false
  router.push('/')
}

function manageWorkspaces() {
  wsSwitcherOpen.value = false
  mobileOpen.value = false
  router.push('/workspaces')
}

function createWorkspace() {
  wsSwitcherOpen.value = false
  mobileOpen.value = false
  router.push('/workspaces?create=1')
}

async function logout() {
  await auth.logout()
  router.push('/login')
}

function getIcon(name: string): string {
  const icons: Record<string, string> = {
    'grid': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><rect x="1.5" y="1.5" width="6" height="6" rx="1.5" stroke="currentColor" stroke-width="1.5"/><rect x="10.5" y="1.5" width="6" height="6" rx="1.5" stroke="currentColor" stroke-width="1.5"/><rect x="1.5" y="10.5" width="6" height="6" rx="1.5" stroke="currentColor" stroke-width="1.5"/><rect x="10.5" y="10.5" width="6" height="6" rx="1.5" stroke="currentColor" stroke-width="1.5"/></svg>',
    'mail': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><rect x="2" y="3.5" width="14" height="11" rx="2" stroke="currentColor" stroke-width="1.5"/><path d="M2 5.5l7 5 7-5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'inbox': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M16.5 10.5H13.5l-1.5 2.25h-6L4.5 10.5H1.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M4.1 3.2L1.5 10.5v4.5A1.5 1.5 0 003 16.5h12a1.5 1.5 0 001.5-1.5v-4.5l-2.6-7.3A1.5 1.5 0 0012.48 2.25H5.52a1.5 1.5 0 00-1.42 1.05z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'key': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M15.5 2.5l-2 2m1 1l-2 2-3-3 2-2m-3.18 3.18a4 4 0 10-5.64 5.64 4 4 0 005.64-5.64z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'file-text': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M10.5 1.5H4.5a1.5 1.5 0 00-1.5 1.5v12a1.5 1.5 0 001.5 1.5h9a1.5 1.5 0 001.5-1.5V6l-4.5-4.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M10.5 1.5V6H15M12 9.75H6M12 12.75H6M7.5 6.75H6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'server': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><rect x="2" y="2" width="14" height="5" rx="1.5" stroke="currentColor" stroke-width="1.5"/><rect x="2" y="11" width="14" height="5" rx="1.5" stroke="currentColor" stroke-width="1.5"/><circle cx="5" cy="4.5" r="0.75" fill="currentColor"/><circle cx="5" cy="13.5" r="0.75" fill="currentColor"/></svg>',
    'bell': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M13.5 6a4.5 4.5 0 10-9 0c0 5.25-2.25 6.75-2.25 6.75h13.5S13.5 11.25 13.5 6z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M10.3 15.75a1.5 1.5 0 01-2.6 0" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'globe': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><circle cx="9" cy="9" r="7" stroke="currentColor" stroke-width="1.5"/><path d="M2 9h14M9 2a11.05 11.05 0 013 7 11.05 11.05 0 01-3 7 11.05 11.05 0 01-3-7 11.05 11.05 0 013-7z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'link': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M7.5 10.5a3.75 3.75 0 005.3.45l2.25-2.25a3.75 3.75 0 00-5.3-5.3l-1.29 1.28" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M10.5 7.5a3.75 3.75 0 00-5.3-.45L2.96 9.3a3.75 3.75 0 005.3 5.3l1.28-1.28" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'alert-triangle': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M7.86 2.87L1.21 14.25a1.31 1.31 0 001.14 1.97h13.3a1.31 1.31 0 001.14-1.97L10.14 2.87a1.31 1.31 0 00-2.28 0z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M9 6.75v3M9 12.75h.007" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>',
    'users': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M12.75 15.75v-1.5a3 3 0 00-3-3h-6a3 3 0 00-3 3v1.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><circle cx="6.75" cy="5.25" r="3" stroke="currentColor" stroke-width="1.5"/><path d="M17.25 15.75v-1.5a3 3 0 00-2.25-2.9M12 2.33a3 3 0 010 5.84" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'bar-chart': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M13.5 15V7.5M9 15V3M4.5 15v-4.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'type': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M3 3h12M9 3v12M5.25 15h7.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'clock': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><circle cx="9" cy="9" r="7" stroke="currentColor" stroke-width="1.5"/><path d="M9 4.5V9l3 1.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'activity': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M16.5 9h-3l-2.25 6.75L6.75 2.25 4.5 9h-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'book-open': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M1.5 2.25h5.25a3 3 0 013 3v10.5a2.25 2.25 0 00-2.25-2.25H1.5V2.25zM16.5 2.25h-5.25a3 3 0 00-3 3v10.5a2.25 2.25 0 012.25-2.25h6V2.25z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'list': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M6.75 4.5h9M6.75 9h9M6.75 13.5h9M2.25 4.5h.007M2.25 9h.007M2.25 13.5h.007" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'edit-3': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M12 2.25l3.75 3.75L6 15.75H2.25V12L12 2.25z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'send': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M16.5 1.5L8.25 9.75M16.5 1.5l-5.25 15-3-6.75L1.5 6.75l15-5.25z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'briefcase': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><rect x="2" y="6" width="14" height="10" rx="1.5" stroke="currentColor" stroke-width="1.5"/><path d="M12 6V4.5A1.5 1.5 0 0010.5 3h-3A1.5 1.5 0 006 4.5V6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'settings': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><circle cx="9" cy="9" r="2.25" stroke="currentColor" stroke-width="1.5"/><path d="M14.7 11.1a1.2 1.2 0 00.24 1.32l.04.04a1.46 1.46 0 11-2.06 2.06l-.04-.04a1.2 1.2 0 00-1.32-.24 1.2 1.2 0 00-.73 1.1v.12a1.46 1.46 0 01-2.91 0v-.06a1.2 1.2 0 00-.79-1.1 1.2 1.2 0 00-1.32.24l-.04.04a1.46 1.46 0 11-2.06-2.06l.04-.04a1.2 1.2 0 00.24-1.32 1.2 1.2 0 00-1.1-.73h-.12a1.46 1.46 0 010-2.91h.06a1.2 1.2 0 001.1-.79 1.2 1.2 0 00-.24-1.32l-.04-.04a1.46 1.46 0 112.06-2.06l.04.04a1.2 1.2 0 001.32.24h.06a1.2 1.2 0 00.73-1.1v-.12a1.46 1.46 0 012.91 0v.06a1.2 1.2 0 00.73 1.1 1.2 1.2 0 001.32-.24l.04-.04a1.46 1.46 0 112.06 2.06l-.04.04a1.2 1.2 0 00-.24 1.32v.06a1.2 1.2 0 001.1.73h.12a1.46 1.46 0 010 2.91h-.06a1.2 1.2 0 00-1.1.73z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'layers': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M9 1.5L1.5 6 9 10.5 16.5 6 9 1.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M1.5 12L9 16.5 16.5 12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M1.5 9L9 13.5 16.5 9" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'info': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><circle cx="9" cy="9" r="7" stroke="currentColor" stroke-width="1.5"/><path d="M9 12.75V9M9 5.25h.007" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>',
    'mail-x': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M16 8.5V4a2 2 0 00-2-2H4a2 2 0 00-2 2v8a2 2 0 002 2h6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M2 4.5l7 5 7-5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M12.5 12.5l3.5 3.5M16 12.5l-3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>',
    'user-check': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M11.25 15.75v-1.5a3 3 0 00-3-3h-4.5a3 3 0 00-3 3v1.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><circle cx="6" cy="5.25" r="3" stroke="currentColor" stroke-width="1.5"/><path d="M12.75 9l1.5 1.5 3-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'credit-card': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><rect x="1.5" y="3.75" width="15" height="10.5" rx="1.5" stroke="currentColor" stroke-width="1.5"/><path d="M1.5 7.5h15" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/><path d="M4.5 11.25h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>',
    'history': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M2.25 9a6.75 6.75 0 113.02 5.62" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M2.25 13.5V9.75H6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M9 5.25V9l2.25 1.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'shield': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M9 1.5l6 2.25v4.5c0 3.6-2.55 6.6-6 7.5-3.45-.9-6-3.9-6-7.5v-4.5L9 1.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M6.75 9l1.5 1.5 3-3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'pie-chart': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M16.5 11.4A7.5 7.5 0 116.6 1.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><path d="M16.5 9A7.5 7.5 0 009 1.5V9h7.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'zap': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M9.75 1.5L3 10.5h5.25L8.25 16.5 15 7.5H9.75l0-6z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'code': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M12 4.5L16.5 9 12 13.5M6 4.5L1.5 9 6 13.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    'message-square': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M15.75 11.25a1.5 1.5 0 01-1.5 1.5H5.25L2.25 15.75V3.75a1.5 1.5 0 011.5-1.5h10.5a1.5 1.5 0 011.5 1.5v7.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    // Lucide "workflow" (v1.67.0) path data scaled 18/24 into this file's 18-viewBox convention.
    'workflow': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><rect x="2.25" y="2.25" width="6" height="6" rx="1.5" stroke="currentColor" stroke-width="1.5"/><path d="M5.25 8.25v3a1.5 1.5 0 001.5 1.5h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><rect x="9.75" y="9.75" width="6" height="6" rx="1.5" stroke="currentColor" stroke-width="1.5"/></svg>',
    // Lucide "users-round" (v1.67.0), scaled the same way.
    'users-round': '<svg width="18" height="18" viewBox="0 0 18 18" fill="none"><path d="M13.5 15.75a6 6 0 00-12 0" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/><circle cx="7.5" cy="6" r="3.75" stroke="currentColor" stroke-width="1.5"/><path d="M16.5 15c0-2.53-1.5-4.88-3-6a3.75 3.75 0 00-.34-6.23" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  }
  return icons[name] || ''
}
</script>

<template>
  <div class="layout" :class="{ 'sidebar-collapsed': sidebarCollapsed }">
    <aside class="sidebar">
        <div class="sidebar-header">
            <!-- :src, not src: a static asset path compiles into an import that
                 vite-node cannot resolve under vitest (public-dir asset); the
                 dynamic binding yields the identical runtime URL. The image is
                 display:none either way — the wordmark below is the logo. -->
            <img :src="'/logo-navy.png'" alt="ReForwarder" class="sidebar-logo" @click="navigate('/')" />
            <span class="sidebar-brand-text" @click="navigate('/')">ReForwarder<span class="sidebar-brand-text-dot">.</span></span>
         
          <button class="sidebar-collapse-btn" @click="toggleSidebar"
            :title="sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'"
            :aria-label="sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
              stroke-linecap="round" stroke-linejoin="round">
              <rect x="3" y="3" width="18" height="18" rx="2" />
              <path d="M9 3v18" />
              <path :d="sidebarCollapsed ? 'm14 9 3 3-3 3' : 'm16 15-3-3 3-3'" />
            </svg>
          </button>

        </div>
        <!-- Workspace Switcher -->
        <div class="ws-switcher">
          <div class="ws-switcher-toggle" @click="wsSwitcherOpen = !wsSwitcherOpen">
            <div class="ws-switcher-current">
              <div class="ws-avatar">{{ wsStore.currentWorkspace?.system ? 'S' : (wsStore.currentWorkspace?.name?.charAt(0)?.toUpperCase() || 'R') }}</div>
              <span v-if="!sidebarCollapsed" class="ws-switcher-name">{{ wsStore.contextLabel }}</span>
            </div>
            <svg v-if="!sidebarCollapsed" width="14" height="14" viewBox="0 0 16 16" fill="none">
              <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"
                stroke-linejoin="round" />
            </svg>
          </div>
          <div v-if="wsSwitcherOpen" class="ws-switcher-dropdown">
            <div v-for="ws in wsStore.workspaces" :key="ws.id" class="ws-switcher-option"
              :class="{ active: wsStore.currentWorkspaceId === ws.id }" @click="switchContext(ws.id)">
              <div class="ws-avatar-sm">{{ ws.system ? 'S' : ws.name.charAt(0).toUpperCase() }}</div>
              <span>{{ wsStore.workspaceDisplayName(ws) }}</span>
              <span class="ws-role-badge" :class="{ 'ws-role-badge--system': ws.system }">{{ ws.system ? 'system' : ws.role }}</span>
            </div>
            <div class="ws-switcher-divider"></div>
            <div class="ws-switcher-action" @click="createWorkspace">
              <span class="ws-switcher-action-icon">+</span>
              <span>Create workspace</span>
            </div>
            <div class="ws-switcher-action" @click="manageWorkspaces">
              <span class="nav-icon" v-html="getIcon('briefcase')"></span>
              <span>Manage workspaces…</span>
            </div>
          </div>
        </div>

        <nav class="sidebar-nav">
          <div v-for="section in visibleSections" :key="section.id" class="nav-section">
            <button v-if="!sidebarCollapsed" class="nav-section-title nav-section-toggle"
              :aria-expanded="sectionOpen[section.id]" @click="toggleSection(section.id)">
              <span>{{ section.title }}</span>
              <svg class="nav-section-chevron" :class="{ collapsed: !sectionOpen[section.id] }" width="12" height="12"
                viewBox="0 0 16 16" fill="none">
                <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"
                  stroke-linejoin="round" />
              </svg>
            </button>
            <div v-show="sidebarCollapsed || sectionOpen[section.id]" class="nav-section-items">
              <template v-for="item in sectionItems(section)" :key="item.name">
                <a v-if="item.external" class="nav-item" :href="item.path" target="_blank" rel="noopener noreferrer"
                  :title="sidebarCollapsed ? item.name : ''">
                  <span class="nav-icon" v-html="getIcon(item.icon)"></span>
                  <span v-if="!sidebarCollapsed" class="nav-label">{{ item.name }}</span>
                </a>
                <button v-else-if="item.collapsible" class="nav-item nav-group-toggle"
                  :class="{ 'in-section': isGroupActive(item) }" type="button" :aria-expanded="groupOpen[item.name]"
                  :title="sidebarCollapsed ? item.name : ''" @click="toggleGroup(item.name)">
                  <span class="nav-icon" v-html="getIcon(item.icon)"></span>
                  <span v-if="!sidebarCollapsed" class="nav-label">{{ item.name }}</span>
                  <svg v-if="!sidebarCollapsed" class="nav-section-chevron nav-group-chevron"
                    :class="{ collapsed: !groupOpen[item.name] }" width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"
                      stroke-linejoin="round" />
                  </svg>
                </button>
                <router-link v-else class="nav-item" :class="{ active: isItemActive(item) }"
                  :title="sidebarCollapsed ? item.name : ''" :to="itemTo(item)">
                  <span class="nav-icon" v-html="getIcon(item.icon)"></span>
                  <span v-if="!sidebarCollapsed" class="nav-label">{{ item.name }}</span>
                </router-link>
                <router-link v-for="child in visibleChildren(item)" v-show="!item.collapsible || groupOpen[item.name]"
                  :key="child.path" class="nav-item nav-subitem" :class="{ active: isItemActive(child) }"
                  :title="sidebarCollapsed ? child.name : ''" :to="child.path">
                  <span class="nav-icon" v-html="getIcon(child.icon)"></span>
                  <span v-if="!sidebarCollapsed" class="nav-label">{{ child.name }}</span>
                </router-link>
              </template>
            </div>
          </div>
        </nav>
    </aside>

    <div class="main-wrapper">
      <header class="topbar">
        <div class="topbar-left">
          <!-- Mobile menu button -->
          <button class="mobile-menu-btn" @click="mobileOpen = true">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
              stroke-linecap="round" stroke-linejoin="round">
              <line x1="3" y1="12" x2="21" y2="12" />
              <line x1="3" y1="6" x2="21" y2="6" />
              <line x1="3" y1="18" x2="21" y2="18" />
            </svg>
          </button>
        </div>
        <div class="topbar-right">
          <button
            class="theme-toggle-btn"
            type="button"
            :title="theme.isDark ? 'Switch to light mode' : 'Switch to dark mode'"
            :aria-label="theme.isDark ? 'Switch to light mode' : 'Switch to dark mode'"
            @click="theme.toggle()"
          >
            <svg v-if="theme.isDark" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor"
              stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <circle cx="12" cy="12" r="4" />
              <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
            </svg>
            <svg v-else width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor"
              stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M21 12.79A9 9 0 1111.21 3 7 7 0 0021 12.79z" />
            </svg>
          </button>
          <NotificationBell />
          <div class="user-menu" @click="userMenuOpen = !userMenuOpen">
            <div class="user-avatar">{{ user?.name?.charAt(0)?.toUpperCase() || '?' }}</div>
            <div class="user-menu-info">
              <div class="user-name">{{ user?.name || 'User' }}</div>
              <div class="user-email">{{ user?.email || '' }}</div>
            </div>
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
              <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"
                stroke-linejoin="round" />
            </svg>

            <div v-if="userMenuOpen" class="user-dropdown">
              <div class="user-dropdown-header">
                <span class="user-avatar user-avatar-lg">{{ user?.name?.charAt(0)?.toUpperCase() || '?' }}</span>
                <div class="user-dropdown-info">
                  <div class="user-dropdown-name">{{ user?.name || 'User' }}</div>
                  <div class="user-dropdown-email">{{ user?.email || '' }}</div>
                </div>
              </div>
              <div class="user-dropdown-divider" />

              <a class="user-dropdown-item" @click.stop="navigate('/profile'); userMenuOpen = false">
                <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <path d="M12.67 14v-1.33A2.67 2.67 0 0010 10H6a2.67 2.67 0 00-2.67 2.67V14" stroke="currentColor"
                    stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                  <circle cx="8" cy="5.33" r="2.67" stroke="currentColor" stroke-width="1.5" />
                </svg>
                My Profile
              </a>
              <a class="user-dropdown-item" @click.stop="navigate('/settings'); userMenuOpen = false">
                <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <circle cx="8" cy="8" r="2" stroke="currentColor" stroke-width="1.5" />
                  <path d="M13 9.6a1.1 1.1 0 00.22 1.18l.04.04a1.3 1.3 0 11-1.84 1.84l-.04-.04a1.1 1.1 0 00-1.18-.22 1.1 1.1 0 00-.66.98v.12a1.3 1.3 0 11-2.6 0v-.06a1.1 1.1 0 00-.7-.98 1.1 1.1 0 00-1.18.22l-.04.04a1.3 1.3 0 11-1.84-1.84l.04-.04a1.1 1.1 0 00.22-1.18A1.1 1.1 0 002.28 9H2.2a1.3 1.3 0 110-2.6h.06a1.1 1.1 0 00.98-.7 1.1 1.1 0 00-.22-1.18l-.04-.04a1.3 1.3 0 111.84-1.84l.04.04a1.1 1.1 0 001.18.22h.06a1.1 1.1 0 00.66-.98V1.8a1.3 1.3 0 112.6 0v.06a1.1 1.1 0 00.66.98 1.1 1.1 0 001.18-.22l.04-.04a1.3 1.3 0 111.84 1.84l-.04.04a1.1 1.1 0 00-.22 1.18v.06a1.1 1.1 0 00.98.66h.12a1.3 1.3 0 110 2.6h-.06a1.1 1.1 0 00-.98.66z"
                    stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
                Settings
              </a>
              <a class="user-dropdown-item" @click.stop="navigate('/about'); userMenuOpen = false">
                <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.5" />
                  <path d="M8 7.33V11.33" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
                  <circle cx="8" cy="4.83" r="0.83" fill="currentColor" />
                </svg>
                About
              </a>
              <div class="user-dropdown-divider"></div>
              <div class="user-dropdown-theme">
                <div class="user-dropdown-theme-label">
                  <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                    <circle cx="8" cy="8" r="3" stroke="currentColor" stroke-width="1.5" />
                    <path
                      d="M8 1v2M8 13v2M1 8h2M13 8h2M3.05 3.05l1.41 1.41M11.54 11.54l1.41 1.41M3.05 12.95l1.41-1.41M11.54 4.46l1.41-1.41"
                      stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
                  </svg>
                  Theme
                </div>
                <div class="theme-switcher">
                  <button v-for="m in themeModes" :key="m" :class="['theme-btn', { active: theme.mode === m }]"
                    :title="m.charAt(0).toUpperCase() + m.slice(1)" @click.stop="theme.setMode(m)">
                    <svg v-if="m === 'light'" width="14" height="14" viewBox="0 0 16 16" fill="none">
                      <circle cx="8" cy="8" r="3" stroke="currentColor" stroke-width="1.5" />
                      <path
                        d="M8 1v2M8 13v2M1 8h2M13 8h2M3.05 3.05l1.41 1.41M11.54 11.54l1.41 1.41M3.05 12.95l1.41-1.41M11.54 4.46l1.41-1.41"
                        stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
                    </svg>
                    <svg v-else-if="m === 'dark'" width="14" height="14" viewBox="0 0 16 16" fill="none">
                      <path d="M14 9.5A6.5 6.5 0 016.5 2 6.5 6.5 0 1014 9.5z" stroke="currentColor" stroke-width="1.5"
                        stroke-linecap="round" stroke-linejoin="round" />
                    </svg>
                    <svg v-else width="14" height="14" viewBox="0 0 16 16" fill="none">
                      <rect x="2" y="3" width="12" height="10" rx="1.5" stroke="currentColor" stroke-width="1.5" />
                      <path d="M2 5.5h12" stroke="currentColor" stroke-width="1.5" />
                    </svg>
                  </button>
                </div>
              </div>
              <div class="user-dropdown-divider"></div>
              <a class="user-dropdown-item user-dropdown-logout" @click.stop="logout">
                <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <path
                    d="M6 14H3.33A1.33 1.33 0 012 12.67V3.33A1.33 1.33 0 013.33 2H6M10.67 11.33L14 8l-3.33-3.33M14 8H6"
                    stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
                Logout
              </a>
            </div>
          </div>
        </div>
      </header>
      <main class="main-content">
        <EmailVerificationBanner />
        <!-- R-20: "Viewing as …" banner with the exit action while an admin
             impersonates this session. -->
        <ImpersonationBanner />
        <!-- A newer ReForwarder release exists (platform admins). Links to the release
             notes; it never upgrades anything on its own. -->
        <div v-if="showUpdateBanner" class="app-banner app-banner--success">
          <svg class="app-banner-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
            stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <circle cx="12" cy="12" r="10" />
            <line x1="12" y1="16" x2="12" y2="8" />
            <polyline points="8 12 12 8 16 12" />
          </svg>
          <div class="app-banner-content">
            <p class="app-banner-title">ReForwarder {{ update?.latest_version }} is available</p>
            <p class="app-banner-text">
              You're running <strong>{{ update?.current_version }}</strong>.
            </p>
          </div>
          <div class="app-banner-actions">
            <a class="app-banner-btn" :href="update?.release_url" target="_blank" rel="noopener noreferrer">
              Release notes
            </a>
            <button class="app-banner-dismiss" title="Dismiss until the next release"
              aria-label="Dismiss until the next release" @click="dismissUpdate">×</button>
          </div>
        </div>
        <router-view />
      </main>
      <footer class="main-footer">
        <div class="footer-left">
          <span>&copy; {{ new Date().getFullYear() }} Jonas Kaninda</span>
          <span v-if="appInfo" class="footer-version" :title="appInfo.commit_id ? `Commit: ${appInfo.commit_id}` : ''">
            v{{ appInfo.version }}<template v-if="appInfo.commit_id"> ({{ appInfo.commit_id.slice(0, 7) }})</template>
          </span>
        </div>
        <div class="footer-right">
          <a href="https://github.com/ReForwarder/ReForwarder" target="_blank" rel="noopener noreferrer" class="footer-link">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
              <path
                d="M8 1C4.13 1 1 4.13 1 8a7 7 0 004.79 6.65c.35.06.48-.15.48-.34 0-.17-.01-.71-.01-1.29-1.76.33-2.2-.43-2.34-.82-.08-.2-.42-.82-.71-.99-.24-.13-.59-.46-.01-.47.55-.01.94.51 1.07.71.63 1.05 1.63.76 2.03.57.06-.45.24-.76.44-.93-1.55-.17-3.18-.78-3.18-3.46 0-.76.27-1.39.71-1.88-.07-.17-.31-.89.07-1.85 0 0 .58-.19 1.9.71a6.5 6.5 0 013.46 0c1.32-.9 1.9-.71 1.9-.71.38.96.14 1.68.07 1.85.44.49.71 1.11.71 1.88 0 2.69-1.64 3.29-3.19 3.46.25.22.47.64.47 1.29 0 .93-.01 1.68-.01 1.91 0 .19.13.41.48.34A7 7 0 0015 8c0-3.87-3.13-7-7-7z"
                fill="currentColor" />
            </svg>
            GitHub
          </a>
        </div>
      </footer>
    </div>
    <!-- Mobile sidebar overlay -->
    <Transition name="overlay-fade">
      <div v-if="mobileOpen" class="sidebar-overlay" @click="mobileOpen = false" />
    </Transition>

    <!-- Mobile sidebar -->
    <Transition name="sidebar-slide">
      <aside v-if="mobileOpen" class="sidebar sidebar-mobile">
        <div class="sidebar-header">
          <img :src="'/logo.png'" alt="ReForwarder" class="sidebar-logo" />
          <span class="sidebar-brand-text">ReForwarder<span class="sidebar-brand-text-dot">.</span></span>
          <button class="sidebar-collapse-btn" @click="mobileOpen = false">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
              stroke-linecap="round" stroke-linejoin="round">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>

        <!-- ws Switcher (mobile) -->
        <div class="ws-switcher">
          <div class="ws-switcher-toggle" @click="wsSwitcherOpen = !wsSwitcherOpen">
            <div style="display: flex; align-items: center; gap: 8px; overflow: hidden;">
              <span class="ws-avatar">
                {{ wsStore.currentWorkspace?.system ? 'S' : (wsStore.currentWorkspace?.name?.charAt(0)?.toUpperCase() || 'R') }}
              </span>
              <span class="ws-name">{{ wsStore.contextLabel }}</span>
            </div>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
              stroke-linecap="round" stroke-linejoin="round">
              <polyline points="6 9 12 15 18 9" />
            </svg>
          </div>

          <Transition name="dropdown">
            <div v-if="wsSwitcherOpen" class="ws-dropdown">
              <div v-for="ws in wsStore.workspaces" :key="ws.id" class="ws-option"
                :class="{ active: wsStore.currentWorkspaceId === ws.id }"
                @click="switchContext(ws.id); mobileOpen = false">
                <span class="ws-avatar">{{ ws.system ? 'S' : ws.name?.charAt(0)?.toUpperCase() }}</span>
                <div style="display: flex; flex-direction: column;">
                  <span>{{ wsStore.workspaceDisplayName(ws) }}</span>
                  <small style="font-size: 0.75rem; opacity: 0.7;">{{ ws.system ? 'system' : ws.role }}</small>
                </div>
              </div>

              <div class="ws-switcher-divider"></div>
              <div class="ws-option" @click="createWorkspace">
                <span class="ws-switcher-action-icon">+</span>
                <span>Create workspace</span>
              </div>
              <div class="ws-option" @click="manageWorkspaces">
                <span class="nav-icon" v-html="getIcon('briefcase')"></span>
                <span>Manage workspaces…</span>
              </div>
            </div>
          </Transition>
        </div>

        <nav class="sidebar-nav">
          <div v-for="section in visibleSections" :key="section.id" class="nav-section">
            <button class="nav-section-title nav-section-toggle" :aria-expanded="sectionOpen[section.id]"
              @click="toggleSection(section.id)">
              <span>{{ section.title }}</span>
              <svg class="nav-section-chevron" :class="{ collapsed: !sectionOpen[section.id] }" width="12" height="12"
                viewBox="0 0 16 16" fill="none">
                <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"
                  stroke-linejoin="round" />
              </svg>
            </button>
            <div v-show="sectionOpen[section.id]" class="nav-section-items">
              <template v-for="item in sectionItems(section)" :key="item.name">
                <a v-if="item.external" class="nav-item" :href="item.path" target="_blank" rel="noopener noreferrer"
                  @click="mobileOpen = false">
                  <span class="nav-icon" v-html="getIcon(item.icon)"></span>
                  <span class="nav-label">{{ item.name }}</span>
                </a>
                <button v-else-if="item.collapsible" class="nav-item nav-group-toggle"
                  :class="{ 'in-section': isGroupActive(item) }" type="button" :aria-expanded="groupOpen[item.name]"
                  @click="toggleGroup(item.name)">
                  <span class="nav-icon" v-html="getIcon(item.icon)"></span>
                  <span class="nav-label">{{ item.name }}</span>
                  <svg class="nav-section-chevron nav-group-chevron" :class="{ collapsed: !groupOpen[item.name] }"
                    width="12" height="12" viewBox="0 0 16 16" fill="none">
                    <path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"
                      stroke-linejoin="round" />
                  </svg>
                </button>
                <router-link v-else class="nav-item" :class="{ active: isItemActive(item) }"
                  @click="mobileOpen = false" :to="itemTo(item)">
                  <span class="nav-icon" v-html="getIcon(item.icon)"></span>
                  <span class="nav-label">{{ item.name }}</span>
                </router-link>
                <router-link v-for="child in visibleChildren(item)" v-show="!item.collapsible || groupOpen[item.name]"
                  :key="child.path" class="nav-item nav-subitem" :class="{ active: isItemActive(child) }"
                  @click="mobileOpen = false" :to="child.path">
                  <span class="nav-icon" v-html="getIcon(child.icon)"></span>
                  <span class="nav-label">{{ child.name }}</span>
                </router-link>
              </template>
            </div>
          </div>
        </nav>
      </aside>
    </Transition>
  </div>
</template>


<style scoped>
.layout {
  display: flex;
  min-height: 100vh;
  background: var(--bg-secondary);
}

/* ─── Sidebar ─── */
.sidebar {
  position: fixed;
  top: 0;
  left: 0;
  bottom: 0;
  width: 240px;
  background: var(--bg-sidebar);
  display: flex;
  flex-direction: column;
  z-index: 40;
  /* Intentional layout transition (design-audit exception): the sidebar is
     persistent chrome resized by a discrete user toggle. Animating width here
     — and margin-left on .main-wrapper below — is the one honest way to keep
     the content reflow attached to the toggle; a transform would require an
     off-canvas restructure that breaks the fixed-sidebar layout model. */
  transition: width var(--transition-slow);
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) {
  width: 64px;
}

.sidebar-header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 16px 14px 12px;
  flex-shrink: 0;
  position: relative;
}

/* Brand: the text wordmark (Instrument Serif) is the visual logo. The img
   asset stays in the template/DOM but is hidden so the navy bitmap never
   fights the monochrome sidebar. */
.sidebar-logo {
  display: none;
}

.sidebar-brand-text-dot {
  color: var(--accent);
  margin-left: 1px;
}
.sidebar-brand-text {
  font-family: var(--font-display);
  font-size: 20px;
  font-weight: 400;
  letter-spacing: 0.01em;
  color: var(--sidebar-text-active);
  white-space: nowrap;
  overflow: hidden;
  opacity: 1;
  transition: opacity var(--transition-slow);
  cursor: pointer;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .sidebar-brand-text {
  opacity: 0;
  width: 0;
}

.sidebar-collapse-btn {
  background: none;
  border: none;
  color: var(--sidebar-text);
  cursor: pointer;
  padding: 4px;
  border-radius: var(--radius-sm);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: color var(--transition), background var(--transition);
  flex-shrink: 0;
  position: absolute;
  top: 18px;
  right: 10px;
}

.sidebar-collapse-btn:hover {
  color: var(--sidebar-text-active);
  background: var(--sidebar-hover);
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .sidebar-collapse-btn {
  /* left: 50%; */
  transform: translateX(-50%);
  right: -15px;
  top: 20px;
  z-index: 999;
}

/* ─── Navigation ─── */
.sidebar-nav {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
  padding: 8px;
}

.nav-section {
  margin-bottom: 2px;
}

.nav-section + .nav-section {
  margin-top: 18px;
}

.nav-section-title {
  font-size: 10.5px;
  font-weight: 600;
  color: var(--sidebar-text);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  padding: 4px 12px 6px;
  white-space: nowrap;
  overflow: hidden;
  opacity: 0.55;
  transition: opacity var(--transition-slow);
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .nav-section-title {
  opacity: 0;
  height: 0;
  padding: 0;
  margin: 0;
}

.nav-section-toggle {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  background: none;
  border: none;
  cursor: pointer;
  font-family: inherit;
  border-radius: var(--radius-sm);
}

.nav-section-toggle:hover {
  color: var(--sidebar-text-active);
}

.nav-section-chevron {
  flex-shrink: 0;
  opacity: 0.7;
  transition: transform var(--transition);
}

.nav-section-chevron.collapsed {
  transform: rotate(-90deg);
}

/* Collapsible item groups (Admin · Security): a toggle button styled like a
   nav item. "In section" brightens text/icon without the selected chip so the
   parent never reads as the selected child. */
.nav-group-toggle {
  width: 100%;
  background: none;
  border: none;
  font-family: inherit;
  text-align: left;
}

.nav-group-toggle .nav-label {
  flex: 1;
}

.nav-group-toggle.in-section {
  color: var(--sidebar-text-active);
}

.nav-group-toggle.in-section .nav-icon {
  opacity: 1;
}

.nav-group-chevron {
  flex-shrink: 0;
}

.nav-subitem {
  padding-left: 34px;
  font-size: 13px;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .nav-subitem {
  padding-left: 9px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 12px;
  border-radius: var(--radius);
  color: var(--sidebar-text);
  font-size: 13px;
  font-weight: 400;
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
  text-decoration: none;
  white-space: nowrap;
  overflow: hidden;
}

.nav-item:hover {
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
}

.nav-item.active {
  /* Neutral surface chip — active state reads through weight, icon opacity,
     and text color; no accent bar. */
  background: var(--bg-tertiary);
  color: var(--sidebar-text-active);
  font-weight: 500;
}

.nav-icon {
  flex-shrink: 0;
  width: 17px;
  height: 17px;
  opacity: 0.7;
}

.nav-item.active .nav-icon {
  opacity: 1;
}

.nav-label {
  overflow: hidden;
  opacity: 1;
  transition: opacity var(--transition-slow);
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .nav-label {
  opacity: 0;
  width: 0;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .nav-item {
  justify-content: center;
  padding: 9px;
}

/* ─── Main wrapper ─── */
.main-wrapper {
  flex: 1;
  /* As a flex item of .layout, allow shrinking below the content's minimum
     width — without this, any page whose content has a large min-content
     (tables, grids) blows the whole layout past the mobile viewport. */
  min-width: 0;
  margin-left: 240px;
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  transition: margin-left var(--transition-slow);
}

.sidebar-collapsed .main-wrapper {
  margin-left: 64px;
}

/* ─── Top bar ─── */
.topbar {
  position: sticky;
  top: 0;
  z-index: 30;
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 56px;
  padding: 0 28px;
  background: var(--bg-primary);
  border-bottom: 1px solid var(--border-primary);
  transition: background var(--transition-slow), border-color var(--transition-slow);
}

.topbar-left {
  display: flex;
  align-items: center;
}

.mobile-menu-btn {
  display: none;
  align-items: center;
  justify-content: center;
  background: none;
  border: none;
  color: var(--text-tertiary);
  cursor: pointer;
  padding: 6px;
  border-radius: var(--radius-sm);
  transition: color var(--transition), background var(--transition);
}

.mobile-menu-btn:hover {
  color: var(--text-primary);
  background: var(--bg-hover);
}

.topbar-right {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-left: auto;
}

/* Quick light/dark switch — mirrors the bell button exactly. */
.theme-toggle-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border-radius: var(--radius);
  border: 1px solid transparent;
  background: none;
  color: var(--text-secondary);
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
}

.theme-toggle-btn:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

/* Workspace Switcher */
.ws-switcher {
  padding: 0 8px 8px;
  position: relative;
}

.ws-switcher-toggle {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-radius: var(--radius, 8px);
  cursor: pointer;
  transition: all var(--transition, 150ms ease);
  color: var(--sidebar-text);
  border: 1px solid var(--sidebar-border);
}

.ws-switcher-toggle:hover {
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
}

.ws-switcher-current {
  display: flex;
  align-items: center;
  gap: 8px;
  overflow: hidden;
}

.ws-avatar {
  width: 24px;
  height: 24px;
  border-radius: 6px;
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
  border: 1px solid var(--sidebar-border);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}

.ws-avatar-sm {
  width: 20px;
  height: 20px;
  border-radius: 5px;
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
  border: 1px solid var(--sidebar-border);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  font-weight: 600;
  flex-shrink: 0;
}

.ws-switcher-name {
  font-size: 13px;
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ws-switcher.collapsed .ws-switcher-toggle {
  justify-content: center;
  padding: 8px;
  border: none;
}

.ws-switcher-dropdown {
  position: absolute;
  top: calc(100% + 2px);
  left: 8px;
  right: 8px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  padding: 4px;
  z-index: 200;
  min-width: 200px;
}

.ws-switcher-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: all var(--transition);
}

.ws-switcher-option:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.ws-switcher-option.active {
  background: var(--bg-active);
  color: var(--primary-600);
}

.ws-role-badge {
  margin-left: auto;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  color: var(--text-muted);
  letter-spacing: 0.05em;
}

.ws-switcher-divider {
  height: 1px;
  background: var(--border-primary);
  margin: 4px 6px;
}

.ws-switcher-action {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: all var(--transition);
}

.ws-switcher-action:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.ws-switcher-action-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  font-size: 18px;
  font-weight: 400;
  line-height: 1;
}

/* ─── User menu ─── */
.user-menu {
  position: relative;
}

.user-menu-trigger {
  display: flex;
  align-items: center;
  gap: 8px;
  background: none;
  border: 1px solid transparent;
  padding: 5px 10px 5px 5px;
  border-radius: var(--radius);
  cursor: pointer;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: 14px;
  transition: background var(--transition), border-color var(--transition);
}

.user-menu-trigger:hover {
  background: var(--bg-hover);
  border-color: var(--border-primary);
}

.user-avatar {
  width: 30px;
  height: 30px;
  border-radius: 50%;
  background: var(--bg-tertiary);
  color: var(--text-primary);
  border: 1px solid var(--border-primary);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  font-weight: 600;
  flex-shrink: 0;
}

.user-avatar-lg {
  width: 36px;
  height: 36px;
  font-size: 15px;
}

.user-name {
  font-weight: 500;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ─── User dropdown ─── */
.user-dropdown {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  width: 280px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  z-index: 50;
  overflow: hidden;
}

.user-dropdown-header {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 16px;
}

.user-dropdown-info {
  overflow: hidden;
}

.user-dropdown-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.user-dropdown-email {
  font-size: 12px;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.user-dropdown-divider {
  height: 1px;
  background: var(--border-primary);
}

.user-dropdown-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 16px;
  font-size: 14px;
  color: var(--text-secondary);
  cursor: pointer;
  transition: background var(--transition), color var(--transition);
  text-decoration: none;
  border: none;
  background: none;
  width: 100%;
  font-family: inherit;
}

.user-dropdown-item:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.user-dropdown-logout {
  color: var(--danger-600);
}

.user-dropdown-logout:hover {
  background: var(--danger-50);
  color: var(--danger-700);
}

/* ─── Theme switcher ─── */
.user-dropdown-theme {
  padding: 10px 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.user-dropdown-theme-label {
  font-size: 13px;
  color: var(--text-muted);
  font-weight: 500;
}

.theme-switcher {
  display: flex;
  background: var(--bg-tertiary);
  border-radius: var(--radius-sm);
  padding: 2px;
  gap: 2px;
}

.theme-btn {
  padding: 4px 10px;
  border: none;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  color: var(--text-tertiary);
  background: transparent;
  font-family: inherit;
  transition: all var(--transition);
}

.theme-btn:hover {
  color: var(--text-primary);
}

.theme-btn.active {
  background: var(--bg-primary);
  color: var(--text-primary);
  box-shadow: var(--shadow-sm);
}

/* ─── Main content ─── */
.main-content {
  flex: 1;
  padding: 28px;
  width: 100%;
  min-width: 0;
  /* Clamp horizontal overflow at every width (clip, not hidden: no scroll
     container, so position:sticky chrome keeps working). */
  overflow-x: clip;
}

@media (max-width: 1279px) {
  .main-content {
    padding: 24px;
  }
}

.ws-role-badge--system {
  background: var(--warning-50);
  color: var(--warning-600);
}

/* ─── Footer ─── */
.main-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 28px;
  border-top: 1px solid var(--border-primary);
  font-size: 13px;
  color: var(--text-muted);
  background: var(--bg-primary);
  transition: background var(--transition-slow), border-color var(--transition-slow);
}

.main-footer a {
  color: var(--text-secondary);
  text-decoration: none;
}

.main-footer a:hover {
  color: var(--text-primary);
}

.footer-version {
  color: var(--text-muted);
  font-size: 12px;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
}

.footer-github {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--text-muted);
  transition: color var(--transition);
}

.footer-github:hover {
  color: var(--text-primary);
}

/* ─── Mobile overlay ─── */
.sidebar-overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  z-index: 35;
  backdrop-filter: blur(4px);
}

.sidebar-mobile {
  z-index: 45;
  width: 240px;
}

.user-menu {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 12px;
  border-radius: var(--radius);
  cursor: pointer;
  transition: all var(--transition);
  position: relative;
  user-select: none;
  color: var(--text-secondary);
}

.user-menu:hover {
  background: var(--bg-hover);
}

.user-avatar {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: var(--bg-tertiary);
  color: var(--text-primary);
  border: 1px solid var(--border-primary);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  font-weight: 600;
  flex-shrink: 0;
}

.user-menu-info {
  overflow: hidden;
}

.user-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.user-email {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.user-dropdown {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  min-width: 200px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  padding: 4px;
  z-index: 200;
}

.dropdown-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-secondary);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: all var(--transition);
  text-decoration: none;
}

.dropdown-item:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.dropdown-item-danger:hover {
  background: var(--danger-50);
  color: var(--danger-text);
}

.dropdown-divider {
  height: 1px;
  background: var(--border-primary);
  margin: 4px 0;
}

.sidebar-app-info {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 12px 2px;
  gap: 6px;
}

.sidebar-app-name {
  font-size: 12px;
  font-weight: 600;
  color: var(--sidebar-text-active);
  opacity: 0.75;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.sidebar-app-version {
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--sidebar-text);
  opacity: 0.5;
  flex-shrink: 0;
}

.overlay-fade-enter-active {
  transition: opacity 200ms ease;
}

.overlay-fade-leave-active {
  transition: opacity 150ms ease;
}

.overlay-fade-enter-from,
.overlay-fade-leave-to {
  opacity: 0;
}

.sidebar-slide-enter-active {
  transition: transform 200ms ease;
}

.sidebar-slide-leave-active {
  transition: transform 150ms ease;
}

.sidebar-slide-enter-from,
.sidebar-slide-leave-to {
  transform: translateX(-100%);
}

/* ─── Responsive ─── */
@media (max-width: 1024px) {
  .sidebar:not(.sidebar-mobile) {
    display: none;
  }

  .main-wrapper {
    margin-left: 0 !important;
  }

  .mobile-menu-btn {
    display: flex;
  }
}

@media (max-width: 640px) {
  .main-content {
    padding: 20px 16px;
    width: 100%;
    /* 100% not 100vw: 100vw includes the vertical-scrollbar area and makes
       every scrolled admin page 5px wider than the viewport on mobile. */
    max-width: 100%;
    box-sizing: border-box;
    min-width: 0;
    overflow-x: hidden;
  }

  .topbar {
    padding: 0 16px;
  }

  .main-footer {
    padding: 14px 16px;
    flex-direction: column;
    gap: 8px;
    text-align: center;
  }

  .user-name,
  .user-email {
    display: none;
  }
}

/* ─── ws Switcher ─── */
.ws-switcher {
  padding: 0 8px 8px;
  position: relative;
}

.ws-switcher-toggle {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-radius: var(--radius);
  cursor: pointer;
  color: var(--sidebar-text);
  border: 1px solid var(--sidebar-border);
  transition: background var(--transition), color var(--transition);
}

.ws-switcher-toggle:hover {
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
}

.ws-avatar {
  width: 24px;
  height: 24px;
  border-radius: 6px;
  background: var(--sidebar-hover);
  color: var(--sidebar-text-active);
  border: 1px solid var(--sidebar-border);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}

.ws-name {
  font-size: 13px;
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ws-dropdown {
  position: absolute;
  top: 100%;
  left: 8px;
  right: 8px;
  background: var(--bg-primary);
  border: 1px solid var(--border-primary);
  border-radius: var(--radius);
  box-shadow: var(--shadow-lg);
  padding: 4px;
  z-index: 200;
  margin-top: 4px;
}

.ws-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  font-size: 13px;
  color: var(--text-secondary);
  transition: background var(--transition), color var(--transition);
}

.ws-option:hover {
  background: var(--bg-hover);
  color: var(--text-primary);
}

.ws-option.active {
  background: var(--bg-active);
  color: var(--primary-600);
  font-weight: 500;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .ws-switcher-toggle {
  justify-content: center;
  padding: 8px;
}

.sidebar-collapsed .sidebar:not(.sidebar-mobile) .ws-switcher-toggle .ws-name,
.sidebar-collapsed .sidebar:not(.sidebar-mobile) .ws-switcher-toggle svg.nav-label {
  opacity: 0;
  width: 0;
  overflow: hidden;
}
</style>
