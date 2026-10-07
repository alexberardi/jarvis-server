import { NavLink } from 'react-router-dom'
import { LayoutDashboard, Settings, Server, Cpu, Box, Zap, Activity, Boxes, Users, ArrowUpCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import SystemInfoBar from './SystemInfoBar'
import type { LucideIcon } from 'lucide-react'

interface NavItem {
  label: string
  icon: LucideIcon
  path: string
}

const navItems: NavItem[] = [
  { label: 'Dashboard', icon: LayoutDashboard, path: '/dashboard' },
  { label: 'Settings', icon: Settings, path: '/settings' },
  { label: 'Services', icon: Server, path: '/services' },
  { label: 'Models', icon: Box, path: '/models' },
  { label: 'Quick Sets', icon: Zap, path: '/quick-sets' },
  { label: 'Traces', icon: Activity, path: '/traces' },
  { label: 'Nodes', icon: Cpu, path: '/nodes' },
  { label: 'Users', icon: Users, path: '/users' },
  { label: 'Native', icon: Boxes, path: '/native-services' },
  // /update used to be reachable ONLY via the dashboard's UpdateBanner, which
  // renders only when an update is available. With update checks off, the server
  // never contacts GitHub and always reports updateAvailable:false — so the
  // banner never appeared, so the page was unreachable, so its "Check for
  // updates" toggle was unreachable. To turn updates ON you had to reach a page
  // that only existed once updates were already on. It needs a permanent home.
  { label: 'Updates', icon: ArrowUpCircle, path: '/update' },
]

export default function Sidebar() {
  return (
    <aside className="flex w-56 flex-col border-r border-[var(--color-border)] bg-[var(--color-surface)]">
      <div className="flex h-14 items-center border-b border-[var(--color-border)] px-4">
        <span className="text-lg font-bold text-[var(--color-primary)]">Jarvis</span>
      </div>

      <nav className="flex-1 space-y-1 p-2">
        {navItems.map((item) => (
          <NavLink
            key={item.path}
            to={item.path}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors',
                isActive
                  ? 'bg-[var(--color-primary)]/10 text-[var(--color-primary)]'
                  : 'text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-text)]',
              )
            }
          >
            <item.icon size={18} />
            {item.label}
          </NavLink>
        ))}
      </nav>

      <SystemInfoBar />
    </aside>
  )
}
