import { useState } from 'react'
import { Link } from 'react-router-dom'
import { CalendarClock, Mic, Pencil, Play, Plus, Timer, Trash2, Workflow } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { deleteRoutine, listRoutines, runRoutineNow, type Routine } from '@/api/routines'
import { Badge, Button, buttonStyles, Card, Dialog, EmptyState, ErrorState, List, PageHeader, Select, SkeletonList } from '@/components/ui'
import { useRoutineNodes } from '@/components/routines/useRoutineNodes'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { scheduleLabel } from '@/lib/routines/schedule'

/**
 * RoutinesPage lists the household's routines (mobile's Routines tab): a phrase that runs a
 * group of commands, or one instruction, by voice or on a schedule. Run fires one now on a node
 * (cc waits for its report, up to a minute); Scheduled errands sit beside it, as on mobile.
 */
export default function RoutinesPage() {
  const { activeHouseholdId: hh } = useAuth()
  const qc = useQueryClient()
  const key = ['routines', hh, 'list']
  const list = useQuery({ queryKey: key, queryFn: () => listRoutines(hh), retry: false })
  const nodes = useRoutineNodes(hh)
  const [picking, setPicking] = useState<Routine | null>(null)
  const [pickNode, setPickNode] = useState('')
  const [running, setRunning] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<Routine | null>(null)
  const [busy, setBusy] = useState(false)
  const rows = list.data ?? []

  const eligible = (r: Routine) => (r.mode === 'simple' ? nodes.nodes.filter((n) => nodes.simpleOn(n.node_id)) : nodes.nodes)

  function startRun(r: Routine) {
    const ok = eligible(r)
    if (r.mode === 'simple' && ok.length === 0) {
      toast.error(nodes.capsKnown ? 'Update Jarvis on a node to run Simple routines.' : 'Could not check which nodes can run this. Try again.')
      return
    }
    // One node (or none known: cc falls back to the primary): run at once; several: ask which.
    if (ok.length <= 1) return void run(r, ok[0]?.node_id ?? nodes.primary)
    setPickNode(ok.find((n) => n.node_id === nodes.primary)?.node_id ?? ok[0].node_id)
    setPicking(r)
  }

  async function run(r: Routine, nodeId: string | null) {
    setPicking(null)
    setRunning(r.id)
    try {
      const res = await runRoutineNow(hh, r.id, nodeId)
      if (res.status === 'timeout') toast.warning(res.message || 'The node did not answer. Some steps may have run.')
      else if (res.success) toast.success(res.message || `${r.name}: done`)
      else toast.error(res.message || `${r.name} reported an error`)
    } catch (err) {
      toast.error(errorMessage(err, `Could not run ${r.name}`))
    } finally {
      setRunning(null)
    }
  }

  async function remove() {
    if (!deleting) return
    setBusy(true)
    try {
      await deleteRoutine(hh, deleting.id)
      await qc.invalidateQueries({ queryKey: key })
      toast(`Deleted “${deleting.name}”`)
      setDeleting(null)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not delete it'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="Routines"
        description="A phrase that runs several commands, or one instruction, by voice or on a schedule."
        actions={
          <>
            <Link to="/routines/errands" className={buttonStyles({ size: 'sm', variant: 'ghost' })}>
              <CalendarClock size={16} aria-hidden /> Scheduled errands
            </Link>
            <Link to="/routines/new" className={buttonStyles({ size: 'sm', variant: 'primary' })}>
              <Plus size={16} aria-hidden /> New routine
            </Link>
          </>
        }
      />
      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState title="Couldn't load routines" message={errorMessage(list.error, 'Check your connection and try again.')} onRetry={() => list.refetch()} />
        ) : list.isLoading ? (
          <SkeletonList rows={3} label="Loading routines" />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Workflow size={20} />}
            title="No routines yet"
            description="Make one to run a few commands with one phrase, like “good morning” turning on the lights and reading the weather."
          />
        ) : (
          <List label="Routines">
            {rows.map((r) => {
              const when = scheduleLabel(r.schedule)
              const Icon = !r.schedule ? Mic : r.schedule.type === 'cron' ? CalendarClock : Timer
              return (
                <li key={r.id} className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3">
                  <Icon size={18} aria-hidden className="mt-0.5 shrink-0 text-text-muted" />
                  <span className="min-w-0 flex-1">
                    <Link to={`/routines/${encodeURIComponent(r.id)}`} className="block text-subheading text-pretty text-text hover:underline">
                      {r.name}
                    </Link>
                    <span className="block text-small text-text-muted">
                      {r.trigger_phrases.slice(0, 3).map((p) => `“${p}”`).join(', ')}
                    </span>
                    <span className="mt-1.5 flex flex-wrap gap-1.5">
                      <Badge>{r.mode === 'simple' ? 'Simple' : `${r.steps.length} step${r.steps.length === 1 ? '' : 's'}`}</Badge>
                      {when && <Badge tone="primary">{when}</Badge>}
                    </span>
                  </span>
                  {/* Below the text on a phone (the name and phrases get the width), beside it from sm. */}
                  <span className="ml-[30px] flex w-full shrink-0 gap-1 sm:ml-0 sm:w-auto">
                    <Button size="sm" icon={<Play size={14} aria-hidden />} loading={running === r.id} disabled={running !== null} aria-label={`Run now: ${r.name}`} onClick={() => startRun(r)}>
                      {running === r.id ? 'Running…' : 'Run'}
                    </Button>
                    <Link to={`/routines/${encodeURIComponent(r.id)}`} className={buttonStyles({ size: 'icon', variant: 'ghost' })} aria-label={`Edit ${r.name}`}>
                      <Pencil size={16} aria-hidden />
                    </Link>
                    <Button size="icon" variant="ghost" aria-label={`Delete ${r.name}`} onClick={() => setDeleting(r)}>
                      <Trash2 size={16} aria-hidden />
                    </Button>
                  </span>
                </li>
              )
            })}
          </List>
        )}
      </Card>

      <Dialog
        open={picking !== null}
        onClose={() => setPicking(null)}
        title={picking ? `Run ${picking.name}` : 'Run'}
        description="It runs on the node you pick, as if you had said it there."
        footer={
          <>
            <Button onClick={() => setPicking(null)}>Cancel</Button>
            <Button variant="primary" onClick={() => picking && void run(picking, pickNode)}>
              Run now
            </Button>
          </>
        }
      >
        {picking && (
          <Select
            label="Run on"
            value={pickNode}
            onValueChange={setPickNode}
            options={eligible(picking).map((n) => ({ value: n.node_id, label: `${nodes.label(n.node_id)}${n.online ? '' : ' (offline)'}` }))}
          />
        )}
      </Dialog>

      <Dialog
        open={deleting !== null}
        onClose={() => !busy && setDeleting(null)}
        dismissible={!busy}
        title="Delete this routine?"
        description={deleting ? `“${deleting.name}” stops answering its phrases and its schedule. This can’t be undone.` : undefined}
        footer={
          <>
            <Button disabled={busy} onClick={() => setDeleting(null)}>
              Keep it
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void remove()}>
              Delete routine
            </Button>
          </>
        }
      />
    </div>
  )
}
