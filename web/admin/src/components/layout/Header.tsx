import { KeyRound, LogOut, Menu, Moon, Sun, UserRound } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { nameOf } from '@/lib/session'
import HouseholdSwitcher from './HouseholdSwitcher'
import { useTheme } from '@/theme/ThemeProvider'
import { SIDEBAR_ID } from './Sidebar'

interface HeaderProps {
  /** Opens the navigation drawer (below md, where the sidebar is hidden). */
  onOpenNav?: () => void
  navOpen?: boolean
}

const iconButton =
  'press inline-flex size-10 shrink-0 items-center justify-center rounded-control text-text-muted hover:bg-surface-alt hover:text-text'

export default function Header({ onOpenNav, navOpen = false }: HeaderProps) {
  const { state, logout } = useAuth()
  const { theme, toggleTheme } = useTheme()

  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-2 border-b border-border bg-surface px-2 sm:px-4 md:px-6">
      <div className="flex min-w-0 items-center gap-1">
        <button
          type="button"
          onClick={onOpenNav}
          aria-label="Open navigation"
          aria-controls={SIDEBAR_ID}
          aria-expanded={navOpen}
          className={`${iconButton} md:hidden`}
        >
          <Menu size={20} aria-hidden />
        </button>
        <span className="truncate text-heading text-primary md:hidden">Jarvis Automation</span>
      </div>

      <div className="flex min-w-0 items-center gap-1 sm:gap-2">
        <HouseholdSwitcher className="max-sm:hidden" />

        {state.user && (
          <Link
            to="/account"
            className="hidden h-10 min-w-0 items-center gap-1.5 truncate rounded-control px-2 text-small text-text-muted transition-colors duration-(--duration-hover) hover:bg-surface-alt hover:text-text sm:flex"
            title={state.user.email}
          >
            <UserRound size={16} className="shrink-0" />
            <span className="truncate">{nameOf(state.user)}</span>
          </Link>
        )}
        {state.user && (
          <Link to="/account" className={`${iconButton} sm:hidden`} title="Account" aria-label="Account">
            <UserRound size={18} />
          </Link>
        )}

        <button
          type="button"
          onClick={toggleTheme}
          className={iconButton}
          title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}
          aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}
        >
          {theme === 'dark' ? <Sun size={18} /> : <Moon size={18} />}
        </button>

        <Link to="/change-password" className={`${iconButton} max-sm:hidden`} title="Change password" aria-label="Change password">
          <KeyRound size={18} />
        </Link>

        <button
          type="button"
          onClick={logout}
          className={`${iconButton} hover:text-danger`}
          title="Log out"
          aria-label="Log out"
        >
          <LogOut size={18} />
        </button>
      </div>
    </header>
  )
}
