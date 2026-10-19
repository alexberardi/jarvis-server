import { Link } from 'react-router-dom'
import { useMutation } from '@tanstack/react-query'
import { Play, Workflow } from 'lucide-react'
import { toast } from 'sonner'
import { runWorkflow } from '@/api/workflows'
import { useAuth } from '@/hooks/useAuth'
import { useWorkflows } from '@/hooks/useWorkflows'
import { errorMessage, errorStatus } from '@/lib/errors'

const button =
  'inline-flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50'

/**
 * WorkflowsPage lists what Jarvis was taught (computer control, plan P3): the caller's own
 * workflows and the household's shared ones, each opening its step list.
 */
export default function WorkflowsPage() {
  const { state } = useAuth()
  const me = state.user?.id
  const list = useWorkflows()
  const run = useMutation({
    mutationFn: runWorkflow,
    onSuccess: () => toast.success('Started'),
    onError: (err) =>
      toast.error(errorStatus(err) === 501 ? "Running a workflow from here isn't available yet." : errorMessage(err, 'Could not run it')),
  })
  const items = list.data ?? []

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-xl font-semibold text-[var(--color-text)]">Workflows</h1>
        <Link to="/recordings" className="text-sm text-[var(--color-primary)] hover:underline">
          Recordings
        </Link>
      </div>

      {list.isError ? (
        <p role="alert" className="text-sm text-red-500">
          {errorMessage(list.error, 'Could not load the workflows')}
        </p>
      ) : list.isLoading ? (
        <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center rounded-xl border border-dashed border-[var(--color-border)] p-10 text-center">
          <Workflow size={32} className="mb-2 text-[var(--color-text-muted)]" aria-hidden="true" />
          <p className="text-sm text-[var(--color-text-muted)]">
            Nothing taught yet. Say "Jarvis, watch me" on your computer and do the task once.
          </p>
        </div>
      ) : (
        <ul className="divide-y divide-[var(--color-border)] overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
          {items.map((w) => (
            <li key={w.id} className="flex items-center gap-3 p-3">
              <Link to={`/workflows/${encodeURIComponent(w.id)}`} className="min-w-0 flex-1 rounded hover:underline">
                <span className="block truncate text-sm font-medium text-[var(--color-text)]">{w.name}</span>
                <span className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--color-text-muted)]">
                  <span>{w.owner_id === me ? 'Yours' : 'Another member’s'}</span>
                  <span>
                    {w.steps.length} {w.steps.length === 1 ? 'step' : 'steps'}
                  </span>
                  {/* Runs are recorded once replay lands (piece 31); none so far. */}
                  <span>Last run: never</span>
                  {w.shared && (
                    <span className="rounded-full bg-[var(--color-surface-alt)] px-2 py-0.5">Shared with the household</span>
                  )}
                </span>
              </Link>
              <button
                type="button"
                className={button}
                aria-label={`Run ${w.name}`}
                disabled={run.isPending}
                onClick={() => run.mutate(w.id)}
              >
                <Play size={14} aria-hidden="true" /> Run
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
