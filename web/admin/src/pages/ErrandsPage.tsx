import { useState } from 'react'
import { CalendarClock, Hourglass } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { cancelSchedule, listSchedules } from '@/api/schedules'
import type { Schedule } from '@/api/schedules'
import { Alert, Button, Card, Dialog, EmptyState, ErrorState, List, PageHeader, SkeletonList } from '@/components/ui'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'

/**
 * ErrandsPage lists the household's scheduled errands, one-time and recurring, and stops one
 * after a confirm (mobile's ScheduleListScreen). Errands are made by asking Jarvis; a stopped
 * one can't be brought back, so stopping asks first.
 */
export default function ErrandsPage() {
  const { activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const key = ['schedules', hh]
  const list = useQuery({ queryKey: key, queryFn: () => listSchedules(hh), retry: false })
  const [stopping, setStopping] = useState<Schedule | null>(null)
  const [busy, setBusy] = useState(false)
  const rows = list.data ?? []

  async function stop() {
    if (!stopping) return
    setBusy(true)
    try {
      qc.setQueryData(key, await cancelSchedule(hh, stopping.id))
      toast(`Stopped “${stopping.intent}”`)
      setStopping(null)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not stop that errand'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title="Scheduled errands" description="What Jarvis will do later, once or on repeat." />
      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState
            title="Couldn't load scheduled errands"
            message={errorMessage(list.error, 'Check your connection and try again.')}
            onRetry={() => list.refetch()}
          />
        ) : list.isLoading ? (
          <SkeletonList rows={3} label="Loading scheduled errands" />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<CalendarClock size={20} />}
            title="No scheduled errands"
            description="Ask Jarvis in chat or by voice, for example “every morning at 8, check the weather and tell me if I need an umbrella.”"
          />
        ) : (
          <List label="Scheduled errands">
            {rows.map((s) => {
              const Icon = s.is_recurring ? CalendarClock : Hourglass
              return (
                <li key={s.id} className="flex items-start gap-3 px-4 py-3">
                  <Icon size={18} aria-hidden className="mt-0.5 shrink-0 text-text-muted" />
                  <span className="min-w-0 flex-1">
                    <span className="block text-subheading text-pretty text-text">{s.intent}</span>
                    <span className="block text-small text-text-muted">
                      {[s.cadence, s.next_local && `next ${s.next_local}`].filter(Boolean).join(' · ')}
                    </span>
                  </span>
                  <Button size="sm" className="shrink-0" aria-label={`Stop: ${s.intent}`} onClick={() => setStopping(s)}>
                    Stop
                  </Button>
                </li>
              )
            })}
          </List>
        )}
      </Card>

      <Dialog
        open={stopping !== null}
        onClose={() => !busy && setStopping(null)}
        dismissible={!busy}
        title="Stop this errand?"
        footer={
          <>
            <Button disabled={busy} onClick={() => setStopping(null)}>
              Keep it
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void stop()}>
              Stop errand
            </Button>
          </>
        }
      >
        {stopping && (
          <Alert tone="warning" title={stopping.intent}>
            {stopping.is_recurring ? 'It will stop repeating. ' : ''}To have it again, ask Jarvis.
          </Alert>
        )}
      </Dialog>
    </div>
  )
}
