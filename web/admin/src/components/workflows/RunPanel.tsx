import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, CheckCircle2, Circle, HelpCircle, Play, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { reviseWorkflow, runWorkflow } from '@/api/workflows'
import type { Revision, RunItem, RunItemState, RunState, TaughtWorkflow } from '@/api/workflows'
import { Badge, Button, Card, CardHeader, Input, Spinner } from '@/components/ui'
import { TextArea } from '@/components/workflows/SkillFields'
import type { BadgeTone } from '@/components/ui'
import { useLastRun, workflowKeys } from '@/hooks/useWorkflows'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

const RUN: Record<RunState, { label: string; tone: BadgeTone }> = {
  running: { label: 'Running', tone: 'primary' },
  waiting: { label: 'Waiting for you', tone: 'warning' },
  done: { label: 'Done', tone: 'success' },
  failed: { label: 'Didn’t finish', tone: 'danger' },
  stopped: { label: 'Stopped', tone: 'neutral' },
}

const KIND = { prerequisite: 'Before it starts', step: 'Step', result: 'Result' } as const

// What each state is called for someone who can't see the icon.
const ITEM: Record<RunItemState, string> = {
  waiting: 'not reached',
  done: 'done',
  verified: 'done and checked',
  asked: 'needs you',
  failed: 'failed',
}

function ItemIcon({ state, now }: { state: RunItemState; now: boolean }) {
  if (now) return <Spinner className="text-primary" />
  switch (state) {
    case 'verified':
      return <CheckCircle2 size={16} aria-hidden className="text-success" />
    case 'done':
      return <Check size={16} aria-hidden className="text-text-muted" />
    case 'asked':
      return <HelpCircle size={16} aria-hidden className="text-warning" />
    case 'failed':
      return <XCircle size={16} aria-hidden className="text-danger" />
    default:
      return <Circle size={16} aria-hidden className="text-text-faint" />
  }
}

/**
 * RunPanel is where the workflow's owner runs it and follows the run: each prerequisite, step
 * and the result, what was expected of it and, where a check failed, what was found. Anyone
 * else gets nothing here (the server refuses them the record).
 */
export default function RunPanel({ wf, onRevision }: { wf: TaughtWorkflow; onRevision: (r: Revision) => void }) {
  const qc = useQueryClient()
  const last = useLastRun(wf.id, true)
  const [params, setParams] = useState<Record<string, string>>({})
  const asks = wf.params ?? []
  const missing = asks.some((p) => !(params[p.name] ?? '').trim())
  const run = useMutation({
    mutationFn: () => runWorkflow(wf.id, params),
    onSuccess: () => {
      toast.success('Started')
      void qc.invalidateQueries({ queryKey: workflowKeys.lastRun(wf.id) })
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not run it')),
  })
  const [note, setNote] = useState('')
  const revise = useMutation({
    mutationFn: () => reviseWorkflow(wf.id, note.trim()),
    onSuccess: onRevision,
    onError: (err) => toast.error(errorMessage(err, 'Could not work on that')),
  })
  if (last.isError) return null // not the owner's to see, or it can't be read: the page works without it

  const rec = last.data
  const items = rec?.items ?? []
  // The first item not yet settled is the one a going run is on.
  const nowAt = rec?.state === 'running' ? items.findIndex((it) => it.state === 'waiting') : -1
  let step = 0
  return (
    <Card as="section" aria-labelledby="wf-run" className="space-y-4">
      <CardHeader
        id="wf-run"
        title="Run it"
        description={rec ? undefined : 'It hasn’t run yet. Run it while you watch: this shows each step as it goes.'}
        actions={rec && <Badge tone={RUN[rec.state].tone}>{RUN[rec.state].label}</Badge>}
        className="mb-2"
      />
      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          e.stopPropagation() // the page's own form saves the workflow
          if (!missing) run.mutate()
        }}
      >
        {asks.map((p) => (
          <div key={p.name} className="min-w-40 flex-1">
            <Input
              label={p.name}
              placeholder={p.example}
              value={params[p.name] ?? ''}
              onChange={(e) => setParams((cur) => ({ ...cur, [p.name]: e.target.value }))}
            />
          </div>
        ))}
        <Button
          type="submit"
          icon={<Play size={16} aria-hidden />}
          disabled={missing || rec?.state === 'running'}
          loading={run.isPending}
        >
          {rec ? 'Run again' : 'Run'}
        </Button>
      </form>

      {rec && (
        <>
          {rec.ask && rec.state === 'waiting' && (
            <div role="status" className="space-y-2 rounded-control bg-warning-soft p-3 text-small text-text">
              <p>{rec.ask}</p>
              <p>Answer on the run’s card in the Jarvis app on your phone: this page can’t answer it yet.</p>
            </div>
          )}
          {rec.summary && rec.state !== 'waiting' && <p className="text-small text-text">{rec.summary}</p>}
          <ol className="divide-y divide-border" aria-live="polite">
            {items.map((it: RunItem, i) => {
              if (it.kind === 'step') step++
              const now = i === nowAt
              return (
                <li key={i} data-testid="run-item" className="flex items-start gap-3 py-3">
                  <span className="mt-0.5 flex shrink-0">
                    <ItemIcon state={it.state} now={now} />
                  </span>
                  <div className="min-w-0 flex-1 space-y-1">
                    <p className={cn('text-body [overflow-wrap:anywhere]', it.state === 'waiting' && !now ? 'text-text-muted' : 'text-text')}>
                      <span className="text-text-muted">{it.kind === 'step' ? `${KIND.step} ${step}` : KIND[it.kind]}: </span>
                      {it.text}
                      <span className="sr-only"> ({now ? 'doing it now' : ITEM[it.state]})</span>
                    </p>
                    {it.expected && (
                      <p className="text-small text-text-muted [overflow-wrap:anywhere]">Expected: {it.expected}</p>
                    )}
                    {it.seen && (
                      <p className={cn('text-small [overflow-wrap:anywhere]', it.state === 'failed' ? 'text-danger' : 'text-text')}>
                        Found: {it.seen}
                      </p>
                    )}
                  </div>
                </li>
              )
            })}
          </ol>
          {rec.state !== 'running' && (
            <form
              className="space-y-3 border-t border-border pt-4"
              onSubmit={(e) => {
                e.preventDefault()
                e.stopPropagation()
                if (note.trim()) revise.mutate()
              }}
            >
              <TextArea
                label="What went wrong?"
                hint="Say it as you would to a person: “it picked the wrong account at step 2”. Jarvis proposes changes for you to review; nothing changes until you save."
                maxLength={1000}
                value={note}
                onChange={setNote}
              />
              <Button type="submit" disabled={!note.trim()} loading={revise.isPending}>
                Suggest a fix
              </Button>
            </form>
          )}
        </>
      )}
    </Card>
  )
}
