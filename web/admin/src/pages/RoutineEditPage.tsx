import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, Plus, Trash2, X } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getNodeTools } from '@/api/chat'
import { deviceNodeName } from '@/api/devices'
import { createRoutine, deleteRoutine, getRoutine, updateRoutine, type ResponseLength, type Routine, type RoutineStep, type RoutineWrite } from '@/api/routines'
import { Alert, Button, Card, Dialog, ErrorState, Input, PageHeader, Select, Skeleton, Switch } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import ChoiceGroup from '@/components/routines/ChoiceGroup'
import StepCard from '@/components/routines/StepCard'
import { useRoutineNodes } from '@/components/routines/useRoutineNodes'
import { useAuth } from '@/hooks/useAuth'
import { errorMessage } from '@/lib/errors'
import { cleanSteps, commandCatalog, formProblem, withRequiredArgs } from '@/lib/routines/commands'
import { ALL_DAYS, buildCron, DAYS, INTERVAL_PRESETS, minutesLabel, parseCron, WEEKDAYS, WEEKENDS, type Day } from '@/lib/routines/schedule'
import { cn } from '@/lib/utils'

const browserZone = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/** RoutineEditPage makes or edits a routine (mobile's RoutineEditScreen): /routines/new, /routines/:id. */
export default function RoutineEditPage() {
  const { id } = useParams()
  const { activeHouseholdId: hh } = useAuth()
  const existing = useQuery({ queryKey: ['routines', hh, 'item', id], queryFn: () => getRoutine(hh, id!), enabled: !!id, retry: false })
  if (id && existing.isError) {
    return (
      <div className="mx-auto max-w-3xl">
        <BackLink />
        <Card>
          <ErrorState title="Couldn't load this routine" message={errorMessage(existing.error, 'It may have been deleted.')} onRetry={() => existing.refetch()} />
        </Card>
      </div>
    )
  }
  if (id && !existing.data) {
    return (
      <div className="mx-auto max-w-3xl space-y-4" aria-busy="true">
        <BackLink />
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    )
  }
  // Keyed so the form starts from the loaded routine without an effect copying it in.
  return <RoutineForm key={id ?? 'new'} initial={existing.data ?? null} />
}

function BackLink() {
  return (
    <Link to="/routines" className="inline-flex min-h-10 items-center gap-1 rounded-control text-small text-text-muted transition-colors duration-(--duration-hover) hover:text-text">
      <ArrowLeft size={16} aria-hidden /> Routines
    </Link>
  )
}

function RoutineForm({ initial }: { initial: Routine | null }) {
  const { activeHouseholdId: hh } = useAuth()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const nodes = useRoutineNodes(hh)
  const cron = parseCron(initial?.schedule?.type === 'cron' ? initial.schedule.cron : null)

  const [mode, setMode] = useState<'simple' | 'advanced'>(initial ? (initial.mode ?? 'advanced') : 'simple')
  const [name, setName] = useState(initial?.name ?? '')
  const [phrases, setPhrases] = useState<string[]>(initial?.trigger_phrases ?? [])
  const [phrase, setPhrase] = useState('')
  const [utterance, setUtterance] = useState(initial?.utterance ?? '')
  const [steps, setSteps] = useState<RoutineStep[]>(initial?.steps ?? [])
  const [instruction, setInstruction] = useState(initial?.response_instruction ?? '')
  const [length, setLength] = useState<ResponseLength>(initial?.response_length ?? 'short')
  const [scheduled, setScheduled] = useState(!!initial?.schedule)
  const [scheduleType, setScheduleType] = useState<'cron' | 'interval'>(initial?.schedule?.type ?? 'cron')
  const [time, setTime] = useState(cron.time)
  const [days, setDays] = useState<Day[]>(cron.days)
  const [interval, setIntervalMinutes] = useState(initial?.schedule?.type === 'interval' ? Math.max(1, Math.round((initial.schedule.interval_seconds ?? 3600) / 60)) : 60)
  const [target, setTarget] = useState<string | null>(initial?.schedule?.target_node_id ?? null)
  const [catalogPick, setCatalogPick] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)

  // Commands come from a node's report; default to the primary node if online, else any online.
  const catalogNode =
    catalogPick ??
    (nodes.nodes.find((n) => n.node_id === nodes.primary && n.online) ?? nodes.nodes.find((n) => n.online) ?? nodes.nodes[0])?.node_id ??
    null
  const tools = useQuery({ queryKey: ['routines', hh, 'catalog', catalogNode], queryFn: () => getNodeTools(catalogNode!), enabled: mode === 'advanced' && !!catalogNode, staleTime: 60_000, retry: false })
  const catalog = commandCatalog(tools.data?.available_commands ?? [])
  const scheduleTarget = target ?? (scheduled ? nodes.primary : null)

  const problem = formProblem(
    { mode, name, triggerPhrases: phrases, utterance, steps, scheduled, scheduleType, days, intervalMinutes: interval, targetNodeId: scheduleTarget },
    catalog,
    nodes.simpleOn,
  )

  function addPhrase() {
    const p = phrase.trim()
    if (p && !phrases.includes(p)) setPhrases([...phrases, p])
    setPhrase('')
  }

  const setStep = (i: number, s: RoutineStep) => setSteps(steps.map((x, j) => (j === i ? s : x)))
  function moveStep(i: number, by: -1 | 1) {
    const next = [...steps]
    ;[next[i], next[i + by]] = [next[i + by], next[i]]
    setSteps(next)
  }

  async function save() {
    if (problem) return
    const body: RoutineWrite = {
      mode,
      utterance,
      name: name.trim(),
      trigger_phrases: phrases,
      steps: cleanSteps(steps),
      response_instruction: instruction.trim(),
      response_length: length,
      schedule: scheduled
        ? {
            type: scheduleType,
            ...(scheduleType === 'cron' ? { cron: buildCron(time, days) } : { interval_seconds: interval * 60 }),
            // Editing keeps the schedule's zone; a new one runs in this browser's.
            timezone: initial?.schedule?.timezone || browserZone(),
            target_node_id: scheduleTarget,
            enabled: true,
          }
        : null,
    }
    setSaving(true)
    try {
      if (initial) await updateRoutine(hh, initial.id, body)
      else await createRoutine(hh, body)
      await qc.invalidateQueries({ queryKey: ['routines', hh] })
      toast.success(initial ? 'Routine saved' : 'Routine created')
      navigate('/routines')
    } catch (err) {
      toast.error(errorMessage(err, 'Could not save the routine'))
    } finally {
      setSaving(false)
    }
  }

  async function remove() {
    if (!initial) return
    setSaving(true)
    try {
      await deleteRoutine(hh, initial.id)
      await qc.invalidateQueries({ queryKey: ['routines', hh] })
      toast(`Deleted “${initial.name}”`)
      navigate('/routines')
    } catch (err) {
      toast.error(errorMessage(err, 'Could not delete it'))
      setSaving(false)
    }
  }

  const nodeOptions = (simpleOnly: boolean) =>
    nodes.nodes.map((n) => ({
      value: n.node_id,
      label: `${deviceNodeName(n)}${n.online ? '' : ' (offline)'}${simpleOnly && !nodes.simpleOn(n.node_id) ? ' (needs an update)' : ''}`,
      disabled: simpleOnly && !nodes.simpleOn(n.node_id),
    }))

  return (
    <div className="mx-auto max-w-3xl">
      <BackLink />
      <PageHeader
        title={initial ? 'Edit routine' : 'New routine'}
        actions={
          initial && (
            <Button size="sm" variant="ghost" icon={<Trash2 size={16} aria-hidden />} onClick={() => setDeleting(true)}>
              Delete
            </Button>
          )
        }
      />
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <Card className="space-y-4">
          <ChoiceGroup
            legend="Kind"
            value={mode}
            onChange={setMode}
            options={[
              { value: 'simple', label: 'Simple' },
              { value: 'advanced', label: 'Advanced' },
            ]}
          />
          <p className="text-small text-text-muted">
            {mode === 'simple'
              ? 'Simple: one instruction in your own words, worked out each time it runs.'
              : 'Advanced: a fixed list of commands, run in order, then one reply.'}
          </p>
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Good morning" />
          <div className="space-y-2">
            <div className="flex items-end gap-2">
              <Input
                label="Trigger phrases"
                hint="Saying any of these runs the routine."
                value={phrase}
                placeholder="e.g. good morning"
                onChange={(e) => setPhrase(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    addPhrase()
                  }
                }}
                wrapperClassName="flex-1"
              />
              <Button className="mb-6" onClick={addPhrase} disabled={!phrase.trim()}>
                Add
              </Button>
            </div>
            {phrases.length > 0 && (
              <ul className="flex flex-wrap gap-1.5" aria-label="Trigger phrases">
                {phrases.map((p) => (
                  <li key={p} className="inline-flex items-center gap-1 rounded-pill bg-primary-soft py-0.5 pr-1 pl-3 text-small text-primary">
                    “{p}”
                    <button type="button" aria-label={`Remove “${p}”`} onClick={() => setPhrases(phrases.filter((x) => x !== p))} className="rounded-pill p-1 hover:bg-primary/10">
                      <X size={12} aria-hidden />
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Card>

        {mode === 'simple' ? (
          <Card className="space-y-2">
            <label htmlFor="routine-utterance" className="text-small font-medium text-text">
              What should Jarvis do?
            </label>
            <textarea
              id="routine-utterance"
              rows={5}
              maxLength={5000}
              className={cn(fieldControl, 'h-auto py-2')}
              value={utterance}
              placeholder="Turn on the living room lights and tell me today’s weather."
              onChange={(e) => setUtterance(e.target.value)}
            />
            <p className="text-small text-text-muted">Write it as you’d ask Jarvis. It works it out each time.</p>
          </Card>
        ) : (
          <Card className="space-y-4">
            <div className="flex flex-wrap items-end justify-between gap-3">
              <h2 className="text-subheading">Steps</h2>
              {nodes.nodes.length > 0 && (
                <Select
                  label="Commands from"
                  value={catalogNode ?? ''}
                  onValueChange={setCatalogPick}
                  options={nodeOptions(false)}
                  wrapperClassName="w-full sm:w-64"
                />
              )}
            </div>
            {tools.isError && <Alert tone="warning">Couldn’t get that node’s commands. Type the command names, or pick another node.</Alert>}
            {tools.isSuccess && Object.keys(catalog).length === 0 && <Alert tone="warning">That node reported no commands. Pick another node, or type the command names.</Alert>}
            {steps.map((s, i) => (
              <StepCard
                key={i}
                index={i}
                count={steps.length}
                step={s}
                catalog={catalog}
                onChange={(next) => setStep(i, next)}
                onCommand={(command) => setStep(i, withRequiredArgs({ ...s, command, label: s.label || command }, catalog[command]))}
                onMove={(by) => moveStep(i, by)}
                onRemove={() => setSteps(steps.filter((_, j) => j !== i))}
              />
            ))}
            <Button icon={<Plus size={16} aria-hidden />} onClick={() => setSteps([...steps, { command: '', label: '', args: [] }])}>
              Add step
            </Button>
            <div className="space-y-2 border-t border-border pt-4">
              <label htmlFor="routine-reply" className="text-small font-medium text-text">
                How should Jarvis reply?
              </label>
              <textarea
                id="routine-reply"
                rows={2}
                className={cn(fieldControl, 'h-auto py-2')}
                value={instruction}
                placeholder="Summarise the weather and what was switched on."
                onChange={(e) => setInstruction(e.target.value)}
              />
            </div>
            <ChoiceGroup
              legend="Reply length"
              value={length}
              onChange={setLength}
              options={[
                { value: 'short', label: 'Short' },
                { value: 'medium', label: 'Medium' },
                { value: 'long', label: 'Long' },
              ]}
            />
          </Card>
        )}

        <Card className="space-y-4">
          <Switch checked={scheduled} onCheckedChange={setScheduled} label="Run on a schedule" description="Besides its phrases, run it at set times." />
          {scheduled && (
            <>
              <ChoiceGroup
                legend="When"
                value={scheduleType}
                onChange={setScheduleType}
                options={[
                  { value: 'cron', label: 'At a time' },
                  { value: 'interval', label: 'Repeating' },
                ]}
              />
              {scheduleType === 'cron' ? (
                <div className="space-y-3">
                  <Input type="time" label="Time" value={time} onChange={(e) => setTime(e.target.value)} wrapperClassName="w-40" />
                  <fieldset>
                    <legend className="mb-1.5 text-small font-medium text-text">Days</legend>
                    <div className="mb-2 flex flex-wrap gap-1.5">
                      {[
                        ['Every day', ALL_DAYS],
                        ['Weekdays', WEEKDAYS],
                        ['Weekends', WEEKENDS],
                      ].map(([label, set]) => (
                        <Button key={label as string} size="sm" variant="ghost" onClick={() => setDays([...(set as Day[])])}>
                          {label as string}
                        </Button>
                      ))}
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      {DAYS.map((d) => {
                        const on = days.includes(d.key)
                        return (
                          <button
                            key={d.key}
                            type="button"
                            aria-pressed={on}
                            onClick={() => setDays(on ? days.filter((x) => x !== d.key) : [...days, d.key])}
                            className={cn(
                              'min-h-10 min-w-12 rounded-control border px-2 text-small transition-colors duration-(--duration-hover)',
                              on ? 'border-primary bg-primary-soft font-medium text-primary' : 'border-border-strong text-text-muted hover:text-text',
                            )}
                          >
                            {d.label}
                          </button>
                        )
                      })}
                    </div>
                  </fieldset>
                </div>
              ) : (
                <Select
                  label="Every"
                  value={String(interval)}
                  onValueChange={(v) => setIntervalMinutes(Number(v))}
                  options={[...new Set([...INTERVAL_PRESETS, interval])].sort((a, b) => a - b).map((m) => ({ value: String(m), label: minutesLabel(m) }))}
                  wrapperClassName="w-40"
                />
              )}
              <Select
                label="Run on"
                hint="The node that runs it and speaks the reply."
                value={scheduleTarget ?? ''}
                placeholder="Choose a node"
                onValueChange={setTarget}
                options={nodeOptions(mode === 'simple')}
              />
            </>
          )}
        </Card>

        <div className="flex flex-wrap items-center justify-end gap-3">
          {problem && (
            <p className="mr-auto text-small text-text-muted" role="status">
              {problem}
            </p>
          )}
          <Button onClick={() => navigate('/routines')}>Cancel</Button>
          <Button type="submit" variant="primary" loading={saving} disabled={!!problem}>
            {initial ? 'Save' : 'Create routine'}
          </Button>
        </div>
      </form>

      <Dialog
        open={deleting}
        onClose={() => !saving && setDeleting(false)}
        dismissible={!saving}
        title="Delete this routine?"
        description={initial ? `“${initial.name}” stops answering its phrases and its schedule. This can’t be undone.` : undefined}
        footer={
          <>
            <Button disabled={saving} onClick={() => setDeleting(false)}>
              Keep it
            </Button>
            <Button variant="danger" loading={saving} onClick={() => void remove()}>
              Delete routine
            </Button>
          </>
        }
      />
    </div>
  )
}
