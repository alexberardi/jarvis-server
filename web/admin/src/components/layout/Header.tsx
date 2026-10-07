import { LogOut, Moon, Sun } from 'lucide-react'
import { useAuth } from '@/hooks/useAuth'
import { useTheme } from '@/theme/ThemeProvider'

export default function Header() {
  const { state, logout } = useAuth()
  const { theme, toggleTheme } = useTheme()

  return (
    <header className="flex h-14 items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-surface)] px-6">
      <h2 className="text-sm font-medium text-[var(--color-text-muted)]">Admin Dashboard</h2>

      <div className="flex items-center gap-3">
        {state.user && (
          <span className="text-sm text-[var(--color-text-muted)]">{state.user.email}</span>
        )}

        <button
          onClick={toggleTheme}
          className="rounded-lg p-2 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]"
          title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}
        >
          {theme === 'dark' ? <Sun size={18} /> : <Moon size={18} />}
        </button>

        <button
          onClick={logout}
          className="rounded-lg p-2 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-red-500"
          title="Logout"
        >
          <LogOut size={18} />
        </button>
      </div>
    </header>
  )
}
