import { useState } from 'react'
import { BellOff } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { deleteSuppression, listSuppressions } from '@/api/suppressions'
import type { Suppression } from '@/api/suppressions'
import { Button, Card, EmptyState, ErrorState, List, PageHeader, SkeletonList } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'
import { BackLink } from './InboxItemPage'

const label = (s: Suppression) => s.descriptor || s.source_key || s.command

/**
 * SuppressedSuggestionsPage lists the suggestions the user blocked with "Never suggest this"
 * (docs/webapp/03 step 4, mobile's SuppressedSuggestionsScreen). Allowing one again deletes the
 * block; a later card can block it again, so there is no confirm.
 */
export default function SuppressedSuggestionsPage() {
  const { activeHouseholdId } = useAuth()
  const qc = useQueryClient()
  const key = ['proposal-suppressions', activeHouseholdId]
  const list = useQuery({ queryKey: key, queryFn: () => listSuppressions(activeHouseholdId), retry: false })
  const [busy, setBusy] = useState<string | null>(null)
  const rows = list.data ?? []

  async function allow(s: Suppression) {
    setBusy(s.id)
    try {
      await deleteSuppression(activeHouseholdId, s.id)
      await qc.invalidateQueries({ queryKey: key })
      toast(`Jarvis may suggest “${label(s)}” again`)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not allow it again'))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="mx-auto max-w-3xl">
      <BackLink />
      <PageHeader title="Blocked suggestions" description="Suggestions you told Jarvis never to make again." />
      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState
            title="Couldn't load blocked suggestions"
            message={errorMessage(list.error, 'Check your connection and try again.')}
            onRetry={() => list.refetch()}
          />
        ) : list.isLoading ? (
          <SkeletonList rows={2} label="Loading blocked suggestions" />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<BellOff size={20} />}
            title="Nothing blocked"
            description="When you pick “Never suggest this” on a suggestion, it shows up here so you can allow it again later."
          />
        ) : (
          <List label="Blocked suggestions">
            {rows.map((s) => (
              // Not ListItem: its title truncates, and here the words are the content.
              <li key={s.id} className="flex items-start gap-3 px-4 py-3">
                <BellOff size={18} aria-hidden className="mt-0.5 shrink-0 text-text-muted" />
                <span className="min-w-0 flex-1">
                  <span className="block text-subheading text-pretty text-text">{label(s)}</span>
                  <span className="block text-small text-text-muted">
                    {[s.command, formatServerTime(s.created_at, { dateStyle: 'medium' })].filter(Boolean).join(' · ')}
                  </span>
                </span>
                <Button size="sm" className="shrink-0" disabled={busy !== null} aria-label={`Allow again: ${label(s)}`} onClick={() => void allow(s)}>
                  Allow again
                </Button>
              </li>
            ))}
          </List>
        )}
      </Card>
    </div>
  )
}
