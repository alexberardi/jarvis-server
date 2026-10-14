import { type FormEvent, useState } from 'react'
import { toast } from 'sonner'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { nameOf } from '@/lib/session'
import { cn } from '@/lib/utils'

/**
 * AccountPage is the v1 account view (webapp 01 §4.4): who you are and the name Jarvis calls
 * you. Password, household and invite management stay in the mobile app for v1.
 */
export default function AccountPage() {
  const { state, updateDisplayName } = useAuth()
  const user = state.user
  const [draft, setDraft] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const value = draft ?? user?.display_name ?? ''

  async function save(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setSaving(true)
    try {
      await updateDisplayName(value.trim())
      setDraft(null)
      toast.success('Saved')
    } catch (err) {
      setError(errorMessage(err, 'Could not save the name'))
    } finally {
      setSaving(false)
    }
  }

  if (!user) return null
  const unchanged = value.trim() === (user.display_name ?? '').trim()

  return (
    <div className="mx-auto max-w-lg space-y-4">
      <h1 className="text-xl font-semibold text-[var(--color-text)]">Account</h1>
      <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-6">
        <p className="text-lg font-medium text-[var(--color-text)]" data-testid="account-name">
          {nameOf(user)}
        </p>
        <p className="mb-4 break-all text-sm text-[var(--color-text-muted)]">{user.email}</p>

        <form onSubmit={save} className="space-y-3">
          <label htmlFor="display-name" className="block text-sm text-[var(--color-text-muted)]">
            Display name: what Jarvis calls you
          </label>
          <input
            id="display-name"
            type="text"
            maxLength={100}
            value={value}
            placeholder={user.username ?? ''}
            onChange={(e) => setDraft(e.target.value)}
            className={cn(
              'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2',
              'text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]',
              'outline-none focus:ring-2 focus:ring-[var(--color-primary)]',
            )}
          />
          {error && (
            <p role="alert" className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-500">
              {error}
            </p>
          )}
          <button
            type="submit"
            disabled={saving || unchanged}
            className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            {saving ? 'Saving...' : 'Save'}
          </button>
        </form>
      </div>
    </div>
  )
}
