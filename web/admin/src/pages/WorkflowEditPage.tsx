import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowLeft, ArrowUp, GripVertical, Plus, ShieldAlert, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { saveWorkflow } from '@/api/workflows'
import type { ParamKind, SuccessCheck, TaughtWorkflow, WStep } from '@/api/workflows'
import { useWorkflow, workflowKeys } from '@/hooks/useWorkflows'
import { errorMessage } from '@/lib/errors'
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

const button =
  'inline-flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50'
const iconButton =
  'inline-flex h-8 w-8 items-center justify-center rounded-lg text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-text)] disabled:opacity-30'
const input =
  'w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-1.5 text-sm text-[var(--color-text)] outline-none focus:ring-2 focus:ring-[var(--color-primary)] disabled:opacity-70'
const section = 'rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-6'
const KINDS: ParamKind[] = ['text', 'date', 'month', 'number', 'file']

/** WorkflowEditPage is a taught workflow as a readable step list its owner (or an admin) edits. */
export default function WorkflowEditPage() {
  const { id = '' } = useParams()
  const wf = useWorkflow(id)
  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <Link to="/workflows" className="inline-flex items-center gap-1 text-sm text-[var(--color-primary)] hover:underline">
        <ArrowLeft size={14} aria-hidden="true" /> Workflows
      </Link>
      {wf.isError ? (
        <p role="alert" className="text-sm text-red-500">
          {errorMessage(wf.error, 'Could not load the workflow')}
        </p>
      ) : !wf.data ? (
        <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
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
      <h1 className="text-xl font-semibold text-[var(--color-text)]">{wf.name}</h1>
      {!edit && (
        <p className="rounded-lg bg-[var(--color-surface-alt)] px-3 py-2 text-sm text-[var(--color-text-muted)]">
          Shared with you. Only the member who taught it, or a household admin, can change it.
        </p>
      )}

      <section aria-labelledby="wf-about" className={cn(section, 'space-y-3')}>
        <h2 id="wf-about" className="sr-only">
          Name and sharing
        </h2>
        <label className="block text-sm font-medium text-[var(--color-text)]">
          Name
          <input className={cn(input, 'mt-1')} value={name} disabled={!edit} maxLength={100} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="flex items-center gap-3 text-sm text-[var(--color-text)]">
          <input
            type="checkbox"
            role="switch"
            checked={shared}
            disabled={!edit}
            onChange={(e) => setShared(e.target.checked)}
            className="h-4 w-4 accent-[var(--color-primary)]"
          />
          Share with the household
        </label>
      </section>

      <section aria-labelledby="wf-steps" className={section}>
        <h2 id="wf-steps" className="text-lg font-medium text-[var(--color-text)]">
          Steps
        </h2>
        <ol className="mt-3 space-y-2">
          {shown.map((d, i) => {
            const raw = steps[i]
            const risky = !!d.step.risk
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
                className={cn(
                  'rounded-lg border border-[var(--color-border)] p-3',
                  dragFrom === i && 'opacity-50',
                  risky && 'border-amber-500/60',
                )}
              >
                <div className="flex items-start gap-2">
                  {edit && <GripVertical size={16} className="mt-0.5 shrink-0 cursor-grab text-[var(--color-text-muted)]" aria-hidden="true" />}
                  <span className="mt-px w-5 shrink-0 text-sm text-[var(--color-text-muted)]">{i + 1}.</span>
                  <div className="min-w-0 flex-1 space-y-2">
                    <p className="text-sm text-[var(--color-text)] [overflow-wrap:anywhere]">
                      <Sentence parts={stepSentence(d.step)} />
                      {risky && (
                        <span
                          title={riskLabel(d.step.risk)}
                          className="ml-2 inline-flex items-center gap-1 rounded-full bg-amber-500/15 px-2 py-0.5 text-xs font-medium text-amber-700 dark:text-amber-400"
                        >
                          <ShieldAlert size={12} aria-hidden="true" /> Asks first
                        </span>
                      )}
                    </p>
                    {edit ? (
                      <label className="block text-xs text-[var(--color-text-muted)]">
                        What it does
                        <input
                          className={cn(input, 'mt-0.5')}
                          aria-label={`Step ${i + 1} intent`}
                          value={d.step.intent}
                          onChange={(e) => {
                            const v = e.target.value
                            patch(raw.uid, (s) => ({ ...s, intent: v, action: { ...s.action, intent: v } }))
                          }}
                        />
                      </label>
                    ) : (
                      <p className="text-xs text-[var(--color-text-muted)]">{d.step.intent}</p>
                    )}
                    {edit && (
                      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--color-text-muted)]">
                        <label className="inline-flex items-center gap-2">
                          <input
                            type="checkbox"
                            role="switch"
                            aria-label={`Ask first before step ${i + 1}`}
                            checked={risky}
                            disabled={raw.lockedRisk}
                            onChange={(e) => {
                              const on = e.target.checked
                              patch(raw.uid, (s) => ({ ...s, risk: on ? 'submit_third_party' : '' }))
                            }}
                            className="h-4 w-4 accent-[var(--color-primary)]"
                          />
                          {raw.lockedRisk ? `Always asks: ${riskLabel(d.step.risk).toLowerCase()}` : 'Ask me first'}
                        </label>
                        {risky && !raw.lockedRisk && (
                          <select
                            aria-label={`Why step ${i + 1} asks`}
                            className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 py-1 text-xs text-[var(--color-text)]"
                            value={d.step.risk}
                            onChange={(e) => {
                              const v = e.target.value
                              patch(raw.uid, (s) => ({ ...s, risk: v }))
                            }}
                          >
                            {RISKS.map((r) => (
                              <option key={r.value} value={r.value}>
                                {r.label}
                              </option>
                            ))}
                          </select>
                        )}
                      </div>
                    )}
                  </div>
                  {edit && (
                    <div className="flex shrink-0 flex-col sm:flex-row">
                      <button type="button" className={iconButton} aria-label={`Move step ${i + 1} up`} disabled={i === 0} onClick={() => move(i, i - 1)}>
                        <ArrowUp size={16} />
                      </button>
                      <button
                        type="button"
                        className={iconButton}
                        aria-label={`Move step ${i + 1} down`}
                        disabled={i === steps.length - 1}
                        onClick={() => move(i, i + 1)}
                      >
                        <ArrowDown size={16} />
                      </button>
                      <button
                        type="button"
                        className={cn(iconButton, 'hover:text-red-500')}
                        aria-label={`Delete step ${i + 1}`}
                        onClick={() => setSteps((cur) => cur.filter((s) => s.uid !== raw.uid))}
                      >
                        <Trash2 size={16} />
                      </button>
                    </div>
                  )}
                </div>
              </li>
            )
          })}
        </ol>
        {edit && (
          <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-end">
            <label className="flex-1 text-xs text-[var(--color-text-muted)]">
              New step
              <input
                className={cn(input, 'mt-0.5')}
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
            </label>
            <label className="text-xs text-[var(--color-text-muted)]">
              On
              <select
                className={cn(input, 'mt-0.5')}
                value={newTarget}
                onChange={(e) => setNewTarget(e.target.value as WStep['target'])}
              >
                <option value="browser">the browser</option>
                <option value="desktop">the desktop</option>
              </select>
            </label>
            <button type="button" className={button} disabled={!newIntent.trim()} onClick={addStep}>
              <Plus size={14} aria-hidden="true" /> Add step
            </button>
          </div>
        )}
      </section>

      <section aria-labelledby="wf-params" className={section}>
        <h2 id="wf-params" className="text-lg font-medium text-[var(--color-text)]">
          Which of these change each time?
        </h2>
        {live.length === 0 ? (
          <p className="mt-1 text-sm text-[var(--color-text-muted)]">Nothing this workflow types changes between runs.</p>
        ) : (
          <ul className="mt-3 space-y-3">
            {live.map((c) => (
              <li key={c.key} className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <label className="flex min-w-0 flex-1 items-center gap-3 text-sm text-[var(--color-text)]">
                  <input
                    type="checkbox"
                    role="switch"
                    aria-label={`${c.example || c.name} changes each time`}
                    checked={c.on}
                    // A param with no example has nothing to fall back to.
                    disabled={!edit || (c.on && !c.example)}
                    onChange={(e) => patchCand(c.key, { on: e.target.checked })}
                    className="h-4 w-4 shrink-0 accent-[var(--color-primary)]"
                  />
                  <span className="truncate font-mono text-xs">{c.example || `{${c.name}}`}</span>
                </label>
                {c.on && (
                  <div className="flex gap-2">
                    <input
                      className={cn(input, 'sm:w-40')}
                      aria-label={`Name for ${c.example || c.name}`}
                      value={c.name}
                      disabled={!edit}
                      onChange={(e) => patchCand(c.key, { name: e.target.value.trim() })}
                    />
                    <select
                      className={cn(input, 'w-28')}
                      aria-label={`Kind of ${c.example || c.name}`}
                      value={c.kind}
                      disabled={!edit}
                      onChange={(e) => patchCand(c.key, { kind: e.target.value as ParamKind })}
                    >
                      {KINDS.map((k) => (
                        <option key={k} value={k}>
                          {k}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section aria-labelledby="wf-success" className={cn(section, 'space-y-3')}>
        <h2 id="wf-success" className="text-lg font-medium text-[var(--color-text)]">
          I'll know it worked when …
        </h2>
        <p className="text-sm text-[var(--color-text)]" data-testid="success-sentence">
          {clauses.length ? clauses.join(', and ') + '.' : 'Nothing is checked yet: Jarvis will ask you.'}
        </p>
        {edit && (
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="text-xs text-[var(--color-text-muted)]">
              A file downloads named like
              <input
                className={cn(input, 'mt-0.5')}
                placeholder="statement*.csv"
                value={success.download?.name_like ?? ''}
                onChange={(e) => setSuccess((s) => ({ ...s, download: { ...s.download, name_like: e.target.value } }))}
              />
            </label>
            <label className="text-xs text-[var(--color-text-muted)]">
              The address contains
              <input
                className={cn(input, 'mt-0.5')}
                value={success.url_contains ?? ''}
                onChange={(e) => setSuccess((s) => ({ ...s, url_contains: e.target.value }))}
              />
            </label>
            <label className="text-xs text-[var(--color-text-muted)]">
              The page shows (one per line)
              <textarea
                className={cn(input, 'mt-0.5')}
                rows={2}
                value={(success.text_present ?? []).join('\n')}
                onChange={(e) => setSuccess((s) => ({ ...s, text_present: e.target.value.split('\n') }))}
              />
            </label>
            <label className="text-xs text-[var(--color-text-muted)]">
              A yes/no question about the screen
              <textarea
                className={cn(input, 'mt-0.5')}
                rows={2}
                value={success.screen ?? ''}
                onChange={(e) => setSuccess((s) => ({ ...s, screen: e.target.value }))}
              />
            </label>
          </div>
        )}
      </section>

      {edit && (
        <div className="flex flex-wrap items-center justify-end gap-3">
          {problem && (
            <p role="alert" className="text-sm text-red-500">
              {problem}
            </p>
          )}
          <button
            type="submit"
            disabled={!!problem || save.isPending}
            className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
          >
            {save.isPending ? 'Saving...' : 'Save'}
          </button>
        </div>
      )}
    </form>
  )
}
