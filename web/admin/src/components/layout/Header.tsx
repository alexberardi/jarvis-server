import { KeyRound, LogOut, Menu, Moon, Sun } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useAuth } from '@/hooks/useAuth'
import { useTheme } from '@/theme/ThemeProvider'
import { SIDEBAR_ID } from './Sidebar'

interface HeaderProps {
  /** Opens the navigation drawer (below md, where the sidebar is hidden). */
  onOpenNav?: () => void
  navOpen?: boolean
}

const iconButton =
  'inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]'

export default function Header({ onOpenNav, navOpen = false }: HeaderProps) {
  const { state, logout } = useAuth()
  const { theme, toggleTheme } = useTheme()

  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-2 sm:px-4 md:px-6">
      <div className="flex min-w-0 items-center gap-1">
        <button
          type="button"
          onClick={onOpenNav}
          aria-label="Open navigation"
          aria-controls={SIDEBAR_ID}
          aria-expanded={navOpen}
          className={`${iconButton} md:hidden`}
        >
          <Menu size={20} />
        </button>
        <span className="text-lg font-bold text-[var(--color-primary)] md:hidden">Jarvis</span>
        <h2 className="hidden text-sm font-medium text-[var(--color-text-muted)] md:block">Admin Dashboard</h2>
      </div>

      <div className="flex min-w-0 items-center gap-1 sm:gap-2">
        {state.user && (
          <span className="hidden min-w-0 truncate text-sm text-[var(--color-text-muted)] sm:block" title={state.user.email}>
            {state.user.email}
          </span>
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

        <Link to="/change-password" className={iconButton} title="Change password" aria-label="Change password">
          <KeyRound size={18} />
        </Link>

        <button
          type="button"
          onClick={logout}
          className={`${iconButton} hover:text-red-500`}
          title="Logout"
          aria-label="Logout"
        >
          <LogOut size={18} />
        </button>
      </div>
    </header>
  )
}
