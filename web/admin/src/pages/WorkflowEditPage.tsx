import { useId, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowLeft, ArrowUp, GripVertical, Plus, ShieldAlert, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { saveWorkflow } from '@/api/workflows'
import type { ParamKind, SuccessCheck, TaughtWorkflow, WStep } from '@/api/workflows'
import { Badge, Button, Card, CardHeader, ErrorState, Input, PageHeader, Select, Skeleton, Switch } from '@/components/ui'
import { FieldFrame } from '@/components/ui/Field'
import { fieldControl } from '@/components/ui/fieldStyles'
import { useWorkflow, workflowKeys } from '@/hooks/useWorkflows'
import { errorMessage, errorStatus } from '@/lib/errors'
import { cn } from '@/lib/utils'
import {
  RISKS,
  applyParams,
  candidatesOf,
  draftSteps,
  paramError,
  riskLabel,
  stepSentence,
  successParts,
  toPut,
} from '@/lib/workflows'
import type { Candidate, DraftStep } from '@/lib/workflows'
import Sentence from '@/components/workflows/Sentence'

const KINDS: ParamKind[] = ['text', 'date', 'month', 'number', 'file']
const TARGETS = [
  { value: 'browser', label: 'the browser' },
  { value: 'desktop', label: 'the desktop' },
]

/** WorkflowEditPage is a taught workflow as a readable step list its owner (or an admin) edits. */
export default function WorkflowEditPage() {
  const { id = '' } = useParams()
  const wf = useWorkflow(id)
  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <Link
        to="/workflows"
        className="inline-flex min-h-10 items-center gap-1 rounded-control text-small text-text-muted transition-colors duration-(--duration-hover) hover:text-text"
      >
        <ArrowLeft size={16} aria-hidden /> Workflows
      </Link>
      {wf.isError ? (
        <Card>
          <ErrorState
            title="Couldn't load this workflow"
            message={errorMessage(wf.error, 'Check your connection and try again.')}
            onRetry={errorStatus(wf.error) === 404 ? undefined : () => wf.refetch()}
          />
        </Card>
      ) : !wf.data ? (
        <div role="status" aria-label="Loading the workflow" className="space-y-4">
          <Skeleton className="h-7 w-2/3" />
          <Skeleton className="h-28 w-full rounded-card" />
          <Skeleton className="h-64 w-full rounded-card" />
        </div>
      ) : (
        <Editor key={`${wf.data.id}:${wf.data.updated_at}`} wf={wf.data} />
      )}
    </div>
  )
}

let added = 0

function Editor({ wf }: { wf: TaughtWorkflow }) {
  const qc = useQueryClient()
  const edit = wf.editable
  const [name, setName] = useState(wf.name)
  const [shared, setShared] = useState(wf.shared)
  const [steps, setSteps] = useState<DraftStep[]>(() => draftSteps(wf.steps))
  const [cands, setCands] = useState<Candidate[]>(() => candidatesOf(wf.params, draftSteps(wf.steps)))
  const [success, setSuccess] = useState<SuccessCheck>(wf.success ?? {})
  const [dragFrom, setDragFrom] = useState<number | null>(null)
  const [newIntent, setNewIntent] = useState('')
  const [newTarget, setNewTarget] = useState<WStep['target']>(wf.steps[0]?.target ?? 'browser')

  const shown = applyParams(steps, cands)
  const live = cands.filter((c) => !('step' in c.from) || steps.some((s) => s.uid === (c.from as { step: string }).step))
  const problem = !name.trim() ? 'The workflow needs a name.' : !steps.length ? 'A workflow needs at least one step.' : paramError(live)

  const save = useMutation({
    mutationFn: () => saveWorkflow(wf.id, toPut({ name, triggers: wf.triggers, steps, cands, success, shared })),
    onSuccess: (got) => {
      qc.setQueryData(workflowKeys.item(wf.id), got)
      void qc.invalidateQueries({ queryKey: ['workflows'] })
      toast.success('Saved')
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not save the workflow')),
  })

  const move = (from: number, to: number) => {
    if (to < 0 || to >= steps.length || from === to) return
    setSteps((cur) => {
      const next = [...cur]
      const [s] = next.splice(from, 1)
      next.splice(to, 0, s)
      return next
    })
  }
  const patch = (uid: string, f: (s: WStep) => WStep) =>
    setSteps((cur) => cur.map((d) => (d.uid === uid ? { ...d, step: f(d.step) } : d)))
  const patchCand = (key: string, p: Partial<Candidate>) => setCands((cur) => cur.map((c) => (c.key === key ? { ...c, ...p } : c)))

  function addStep() {
    const intent = newIntent.trim()
    if (!intent) return
    // A step added here has no recorded element: replay works it out from the intent, so its
    // action only looks at the screen first.
    const step: WStep = { intent, target: newTarget, action: { kind: 'screenshot', intent } }
    setSteps((cur) => [...cur, { uid: `n${++added}`, step, lockedRisk: false }])
    setNewIntent('')
  }

  const clauses = successParts(success)
  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault()
        if (edit && !problem) save.mutate()
      }}
    >
      <PageHeader
        className="mb-2"
        title={<span className="[overflow-wrap:anywhere]">{wf.name}</span>}
        description={edit ? undefined : 'Shared with you. Only the member who taught it, or a household admin, can change it.'}
      />

      <Card as="section" aria-labelledby="wf-about" className="space-y-4">
        <h2 id="wf-about" className="sr-only">
          Name and sharing
        </h2>
        <Input label="Name" value={name} disabled={!edit} maxLength={100} onChange={(e) => setName(e.target.value)} />
        <Switch label="Share with the household" checked={shared} disabled={!edit} onCheckedChange={setShared} />
      </Card>

      <Card as="section" aria-labelledby="wf-steps">
        <CardHeader id="wf-steps" title="Steps" className="mb-1" />
        <ol className="divide-y divide-border">
          {shown.map((d, i) => {
            const raw = steps[i]
            const risky = !!d.step.risk
            const icons = edit && (
              <div className="flex shrink-0 items-center">
                <Button size="icon" variant="ghost" aria-label={`Move step ${i + 1} up`} disabled={i === 0} onClick={() => move(i, i - 1)}>
                  <ArrowUp size={16} aria-hidden />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label={`Move step ${i + 1} down`}
                  disabled={i === steps.length - 1}
                  onClick={() => move(i, i + 1)}
                >
                  <ArrowDown size={16} aria-hidden />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="hover:bg-danger-soft hover:text-danger"
                  aria-label={`Delete step ${i + 1}`}
                  onClick={() => setSteps((cur) => cur.filter((s) => s.uid !== raw.uid))}
                >
                  <Trash2 size={16} aria-hidden />
                </Button>
              </div>
            )
            return (
              <li
                key={d.uid}
                data-testid="step"
                draggable={edit}
                onDragStart={() => setDragFrom(i)}
                onDragOver={(e) => edit && e.preventDefault()}
                onDrop={(e) => {
                  e.preventDefault()
                  if (dragFrom !== null) move(dragFrom, i)
                  setDragFrom(null)
                }}
                onDragEnd={() => setDragFrom(null)}
                className={cn('flex items-start gap-2 py-3', dragFrom === i && 'opacity-50')}
              >
                {edit && <GripVertical size={16} className="mt-1 hidden shrink-0 cursor-grab text-text-muted sm:block" aria-hidden />}
                <span className="mt-px w-5 shrink-0 text-body text-text-muted tabular-nums">{i + 1}.</span>
                <div className="min-w-0 flex-1 space-y-2">
                  <p className="text-body text-text [overflow-wrap:anywhere]">
                    <Sentence parts={stepSentence(d.step)} />
                    {risky && (
                      <Badge tone="warning" title={riskLabel(d.step.risk)} className="ml-2 align-middle">
                        <ShieldAlert size={12} aria-hidden /> Asks first
                      </Badge>
                    )}
                  </p>
                  {edit ? (
                    <Input
                      aria-label={`Step ${i + 1} intent`}
                      placeholder="What it does"
                      value={d.step.intent}
                      onChange={(e) => {
                        const v = e.target.value
                        patch(raw.uid, (s) => ({ ...s, intent: v, action: { ...s.action, intent: v } }))
                      }}
                    />
                  ) : (
                    <p className="text-small text-text-muted">{d.step.intent}</p>
                  )}
                  {edit && (
                    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
                      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                        <Switch
                          aria-label={`Ask first before step ${i + 1}`}
                          label={<span className="text-small text-text-muted">{raw.lockedRisk ? `Always asks: ${riskLabel(d.step.risk).toLowerCase()}` : 'Ask me first'}</span>}
                          checked={risky}
                          disabled={raw.lockedRisk}
                          onCheckedChange={(on) => patch(raw.uid, (s) => ({ ...s, risk: on ? 'submit_third_party' : '' }))}
                        />
                        {risky && !raw.lockedRisk && (
                          <Select
                            aria-label={`Why step ${i + 1} asks`}
                            className="h-8 text-small"
                            options={RISKS}
                            value={d.step.risk}
                            onValueChange={(v) => patch(raw.uid, (s) => ({ ...s, risk: v }))}
                          />
                        )}
                      </div>
                      {icons}
                    </div>
                  )}
                </div>
              </li>
            )
          })}
        </ol>
        {edit && (
          <div className="mt-2 flex flex-col gap-3 border-t border-border pt-4 sm:flex-row sm:items-end">
            <Input
              label="New step"
              wrapperClassName="flex-1"
              placeholder="e.g. Open the statements page"
              value={newIntent}
              onChange={(e) => setNewIntent(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  addStep()
                }
              }}
            />
            <Select
              label="On"
              wrapperClassName="sm:w-40"
              options={TARGETS}
              value={newTarget}
              onValueChange={(v) => setNewTarget(v as WStep['target'])}
            />
            <Button icon={<Plus size={16} aria-hidden />} disabled={!newIntent.trim()} onClick={addStep}>
              Add step
            </Button>
          </div>
        )}
      </Card>

      <Card as="section" aria-labelledby="wf-params">
        <CardHeader
          id="wf-params"
          title="Which of these change each time?"
          description={live.length === 0 ? 'Nothing this workflow types changes between runs.' : undefined}
          className={live.length === 0 ? 'mb-0' : undefined}
        />
        {live.length > 0 && (
          <ul className="space-y-3">
            {live.map((c) => (
              <li key={c.key} className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <div className="flex min-w-0 flex-1 items-center gap-3">
                  <Switch
                    aria-label={`${c.example || c.name} changes each time`}
                    checked={c.on}
                    // A param with no example has nothing to fall back to.
                    disabled={!edit || (c.on && !c.example)}
                    onCheckedChange={(on) => patchCand(c.key, { on })}
                  />
                  <span className="truncate font-mono text-small text-text">{c.example || `{${c.name}}`}</span>
                </div>
                {c.on && (
                  <div className="flex gap-2">
                    <Input
                      aria-label={`Name for ${c.example || c.name}`}
                      wrapperClassName="flex-1 sm:w-40 sm:flex-none"
                      value={c.name}
                      disabled={!edit}
                      onChange={(e) => patchCand(c.key, { name: e.target.value.trim() })}
                    />
                    <Select
                      aria-label={`Kind of ${c.example || c.name}`}
                      wrapperClassName="w-28"
                      options={KINDS.map((k) => ({ value: k, label: k }))}
                      value={c.kind}
                      disabled={!edit}
                      onValueChange={(v) => patchCand(c.key, { kind: v as ParamKind })}
                    />
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card as="section" aria-labelledby="wf-success" className="space-y-4">
        <CardHeader id="wf-success" title="I'll know it worked when …" className="mb-2" />
        <p className="text-body text-text" data-testid="success-sentence">
          {clauses.length ? clauses.join(', and ') + '.' : 'Nothing is checked yet: Jarvis will ask you.'}
        </p>
        {edit && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Input
              label="A file downloads named like"
              placeholder="statement*.csv"
              value={success.download?.name_like ?? ''}
              onChange={(e) => setSuccess((s) => ({ ...s, download: { ...s.download, name_like: e.target.value } }))}
            />
            <Input
              label="The address contains"
              value={success.url_contains ?? ''}
              onChange={(e) => setSuccess((s) => ({ ...s, url_contains: e.target.value }))}
            />
            <TextArea
              label="The page shows (one per line)"
              value={(success.text_present ?? []).join('\n')}
              onChange={(v) => setSuccess((s) => ({ ...s, text_present: v.split('\n') }))}
            />
            <TextArea
              label="A yes/no question about the screen"
              value={success.screen ?? ''}
              onChange={(v) => setSuccess((s) => ({ ...s, screen: v }))}
            />
          </div>
        )}
      </Card>

      {edit && (
        <div className="flex flex-wrap items-center justify-end gap-3">
          {problem && (
            <p role="alert" className="text-small text-danger">
              {problem}
            </p>
          )}
          <Button type="submit" variant="primary" disabled={!!problem} loading={save.isPending}>
            Save
          </Button>
        </div>
      )}
    </form>
  )
}

/** TextArea is a two-line field on the base components' frame and field style. */
function TextArea({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  const id = useId()
  return (
    <FieldFrame id={id} label={label}>
      <textarea id={id} rows={2} className={cn(fieldControl, 'h-auto py-2')} value={value} onChange={(e) => onChange(e.target.value)} />
    </FieldFrame>
  )
}
