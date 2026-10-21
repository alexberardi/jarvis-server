import { Link } from 'react-router-dom'
import { useMutation } from '@tanstack/react-query'
import { Play, Video, Workflow } from 'lucide-react'
import { toast } from 'sonner'
import { runWorkflow } from '@/api/workflows'
import { Badge, Button, Card, EmptyState, ErrorState, List, PageHeader, SkeletonList } from '@/components/ui'
import CrossLink from '@/components/workflows/CrossLink'
import { useAuth } from '@/hooks/useAuth'
import { useWorkflows } from '@/hooks/useWorkflows'
import { errorMessage } from '@/lib/errors'

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
    // The server says why it can't start: a value it needs (it asks by voice), no computer with
    // computer control on, errands off.
    onError: (err) => toast.error(errorMessage(err, 'Could not run it')),
  })
  const items = list.data ?? []

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader
        title="Workflows"
        description="Tasks you taught Jarvis on your computer. Run one here, or ask Jarvis by name."
        actions={
          <CrossLink to="/recordings" icon={<Video size={16} aria-hidden />}>
            Recordings
          </CrossLink>
        }
      />

      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState
            title="Couldn't load your workflows"
            message={errorMessage(list.error, 'Check your connection and try again.')}
            onRetry={() => list.refetch()}
          />
        ) : list.isLoading ? (
          <SkeletonList rows={3} label="Loading your workflows" />
        ) : items.length === 0 ? (
          <EmptyState
            icon={<Workflow size={20} />}
            title="Nothing taught yet"
            description="Say “Jarvis, watch me” on your computer and do the task once. It shows up here to run again."
          />
        ) : (
          <List label="Workflows">
            {items.map((w) => (
              <li key={w.id} className="flex items-center gap-3 pr-4">
                <Link
                  to={`/workflows/${encodeURIComponent(w.id)}`}
                  className="min-w-0 flex-1 py-3 pl-4 transition-colors duration-(--duration-hover) hover:text-primary"
                >
                  <span className="block truncate text-subheading text-text">{w.name}</span>
                  <span className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-small text-text-muted">
                    <span>{w.owner_id === me ? 'Yours' : 'Another member’s'}</span>
                    <span>
                      {w.steps.length} {w.steps.length === 1 ? 'step' : 'steps'}
                    </span>
                    {/* Runs are recorded once replay lands (piece 31); none so far. */}
                    <span>Last run: never</span>
                    {w.shared && <Badge>Shared with the household</Badge>}
                  </span>
                </Link>
                <Button
                  size="sm"
                  icon={<Play size={16} aria-hidden />}
                  aria-label={`Run ${w.name}`}
                  disabled={run.isPending}
                  onClick={() => run.mutate(w.id)}
                >
                  Run
                </Button>
              </li>
            ))}
          </List>
        )}
      </Card>
    </div>
  )
}
