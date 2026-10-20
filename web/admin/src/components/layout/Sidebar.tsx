import { NavLink } from 'react-router-dom'
import {
  LayoutDashboard,
  Settings,
  Link2,
  Cpu,
  Box,
  Activity,
  ScrollText,
  Users,
  ArrowUpCircle,
  X,
  MessageSquare,
  Puzzle,
  Inbox,
  Workflow,
  Video,
} from 'lucide-react'
import { useAuth } from '@/hooks/useAuth'
import { useUnreadCount } from '@/hooks/useInbox'
import { cn } from '@/lib/utils'
import SystemInfoBar from './SystemInfoBar'
import HouseholdSwitcher from './HouseholdSwitcher'
import type { LucideIcon } from 'lucide-react'

interface NavItem {
  label: string
  icon: LucideIcon
  path: string
  /** A count shown after the label (the inbox's unread items). */
  badge?: number
}

/** Every user's pages (webapp 01 §4.1). */
const memberNav: NavItem[] = [
  { label: 'Chat', icon: MessageSquare, path: '/chat' },
  { label: 'Add-ons', icon: Puzzle, path: '/addons' },
  { label: 'Inbox', icon: Inbox, path: '/inbox' },
  { label: 'Workflows', icon: Workflow, path: '/workflows' },
  { label: 'Recordings', icon: Video, path: '/recordings' },
]

/** The Admin group: superusers only, and the server refuses these routes to anyone else (G1). */
const adminNav: NavItem[] = [
  { label: 'Dashboard', icon: LayoutDashboard, path: '/admin/dashboard' },
  { label: 'Models', icon: Box, path: '/admin/models' },
  { label: 'Settings', icon: Settings, path: '/admin/settings' },
  { label: 'Connections', icon: Link2, path: '/admin/connections' },
  { label: 'Logs', icon: ScrollText, path: '/admin/logs' },
  { label: 'Traces', icon: Activity, path: '/admin/traces' },
  { label: 'Nodes', icon: Cpu, path: '/admin/nodes' },
  { label: 'Users', icon: Users, path: '/admin/users' },
  // /update used to be reachable ONLY via the dashboard's UpdateBanner, which
  // renders only when an update is available. With update checks off, the server
  // never contacts GitHub and always reports updateAvailable:false — so the
  // banner never appeared, so the page was unreachable, so its "Check for
  // updates" toggle was unreachable. To turn updates ON you had to reach a page
  // that only existed once updates were already on. It needs a permanent home.
  { label: 'Updates', icon: ArrowUpCircle, path: '/admin/update' },
]

function NavItems({ items, onPick }: { items: NavItem[]; onPick: () => void }) {
  return items.map((item) => (
    <NavLink
      key={item.path}
      to={item.path}
      onClick={onPick}
      className={({ isActive }) =>
        cn(
          'flex min-h-9 items-center gap-3 rounded-control px-3 py-2 text-small font-medium max-md:min-h-11',
          'transition-colors duration-(--duration-hover)',
          isActive ? 'bg-primary-soft text-primary' : 'text-text-muted hover:bg-surface-alt hover:text-text',
        )
      }
    >
      <item.icon size={18} aria-hidden />
      {item.label}
      {!!item.badge && (
        <span
          aria-label={`${item.badge} unread`}
          className="ml-auto inline-flex h-5 min-w-5 items-center justify-center rounded-pill bg-primary px-1.5 text-caption text-on-primary"
        >
          {item.badge > 99 ? '99+' : item.badge}
        </span>
      )}
    </NavLink>
  ))
}

interface SidebarProps {
  /** Below the md breakpoint the sidebar is an off-canvas drawer; this opens it. */
  open?: boolean
  onClose?: () => void
}

export const SIDEBAR_ID = 'admin-sidebar'

/**
 * Sidebar is a static column from md up. Below md it is a drawer the header's menu button opens;
 * picking a destination, the backdrop or Escape closes it.
 */
export default function Sidebar({ open = false, onClose }: SidebarProps) {
  const close = () => onClose?.()
  const { state } = useAuth()
  const superuser = Boolean(state.user?.is_superuser)
  // The unread badge polls only for a usable session (not while the password must change).
  const unread = useUnreadCount(state.isAuthenticated && !state.user?.must_change_password)
  const member = memberNav.map((i) => (i.path === '/inbox' ? { ...i, badge: unread.data ?? 0 } : i))
  return (
    <>
      {open && (
        <div
          className="fixed inset-0 z-40 animate-fade-in bg-black/50 md:hidden"
          aria-hidden="true"
          data-testid="sidebar-backdrop"
          onClick={close}
        />
      )}
      <aside
        id={SIDEBAR_ID}
        aria-label="Main navigation"
        data-open={open ? 'true' : 'false'}
        className={cn(
          'flex w-64 shrink-0 flex-col border-r border-border bg-surface md:w-56',
          // Drawer below md: fixed, off-canvas unless open. Static column from md. It slides in
          // 220ms on the drawer curve; visibility flips after the slide, so closing is seen too.
          'fixed inset-y-0 left-0 z-50 max-w-[85vw] md:static md:z-auto md:max-w-none md:translate-x-0',
          'transition-[translate,visibility] duration-(--duration-drawer) ease-drawer motion-reduce:transition-none',
          open ? 'translate-x-0 shadow-overlay md:shadow-none' : '-translate-x-full max-md:invisible',
        )}
      >
        <div className="flex h-14 shrink-0 items-center justify-between border-b border-border px-4">
          <span className="text-heading text-primary">Jarvis Automation</span>
          <button
            type="button"
            onClick={close}
            aria-label="Close navigation"
            className="press -mr-2 inline-flex size-10 items-center justify-center rounded-control text-text-muted hover:bg-surface-alt hover:text-text md:hidden"
          >
            <X size={20} aria-hidden />
          </button>
        </div>

        <nav className="flex-1 space-y-0.5 overflow-y-auto p-2">
          <HouseholdSwitcher className="mb-2 w-full sm:hidden" />
          <NavItems items={member} onPick={close} />
          {superuser && (
            <>
              <p className="px-3 pt-5 pb-1 text-caption tracking-wide text-text-muted uppercase">
                Admin
              </p>
              <NavItems items={adminNav} onPick={close} />
            </>
          )}
        </nav>

        {superuser && <SystemInfoBar />}
      </aside>
    </>
  )
}
