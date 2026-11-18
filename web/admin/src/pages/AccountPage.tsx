import { type FormEvent, useState } from 'react'
import { Link } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
import { Alert, Button, Card, Input, PageHeader, toast } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { nameOf } from '@/lib/session'

/**
 * AccountPage is the account view (webapp 01 §4.4): who you are and the name Jarvis calls you,
 * with a link to the Household page (members, invites, the household's switches). The password
 * is changed from the header's key button (/change-password).
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
      setError(errorMessage(err, "The name wasn't saved. Try again in a moment."))
    } finally {
      setSaving(false)
    }
  }

  if (!user) return null
  const unchanged = value.trim() === (user.display_name ?? '').trim()

  return (
    <div className="mx-auto max-w-2xl">
      <PageHeader title="Account" description="Who you are to Jarvis." />
      <div className="space-y-4">
        <Card as="section" aria-labelledby="account-profile">
          <h2 id="account-profile" className="sr-only">
            Profile
          </h2>
          <p className="text-heading break-words text-text" data-testid="account-name">
            {nameOf(user)}
          </p>
          <p className="break-all text-small text-text-muted">{user.email}</p>

          <form onSubmit={save} className="mt-5 flex flex-col gap-3 sm:flex-row sm:items-start">
            <Input
              id="display-name"
              label="Display name"
              hint="What Jarvis calls you."
              wrapperClassName="flex-1"
              type="text"
              maxLength={100}
              value={value}
              placeholder={user.username ?? ''}
              onChange={(e) => setDraft(e.target.value)}
            />
            <Button type="submit" variant="primary" loading={saving} disabled={unchanged} className="sm:mt-6.25">
              Save
            </Button>
          </form>
          {error && (
            <Alert tone="danger" title="Couldn't save the name" className="mt-3">
              {error}
            </Alert>
          )}
        </Card>
        <Link
          to="/household"
          className="flex items-center justify-between gap-3 rounded-card border border-border bg-surface px-5 py-4 text-body text-text transition-colors duration-(--duration-hover) hover:bg-surface-alt"
        >
          <span>
            <span className="block font-medium">Household</span>
            <span className="block text-small text-text-muted">Members, invites and the household’s features</span>
          </span>
          <ChevronRight size={18} aria-hidden className="text-text-muted" />
        </Link>
      </div>
    </div>
  )
}
