import type { ReactNode } from 'react'
import { AlertTriangle, CheckCircle2, CircleDashed, Download, Loader2, XCircle } from 'lucide-react'
import type { SetupJob } from '@/api/auth'
import { ProgressBar } from '@/components/models/ui'
import { Badge, type BadgeTone } from '@/components/ui'
import { cn } from '@/lib/utils'
import { JOBS, jobFor, jobPercent, jobStateText, underWay, type JobDef } from './jobs'

function tone(job: SetupJob): BadgeTone {
  switch (job.state) {
    case 'ready':
      return 'success'
    case 'loading':
    case 'downloading':
      return 'warning'
    case 'failed':
      return 'danger'
    default:
      return job.required ? 'danger' : 'neutral'
  }
}

function Icon({ job }: { job: SetupJob }) {
  const cls = 'shrink-0'
  switch (job.state) {
    case 'ready':
      return <CheckCircle2 size={16} className={cn(cls, 'text-success')} />
    case 'loading':
      return <Loader2 size={16} className={cn(cls, 'animate-spin text-warning')} />
    case 'downloading':
      return <Download size={16} className={cn(cls, 'text-warning')} />
    case 'failed':
      return <XCircle size={16} className={cn(cls, 'text-danger')} />
    default:
      return job.required ? (
        <AlertTriangle size={16} className={cn(cls, 'text-danger')} />
      ) : (
        <CircleDashed size={16} className={cn(cls, 'text-text-muted')} />
      )
  }
}

/**
 * JobChecklist is the per-job model checklist (AD3b): ready, starting, downloading with its
 * progress, failed, or missing (flagged when voice needs it). The wizard's Done step says
 * "Skipped" for missing; the Models page says "Not set up" and offers an action per job.
 */
export default function JobChecklist({
  jobs,
  wizard = false,
  action,
}: {
  jobs: SetupJob[] | undefined
  wizard?: boolean
  action?: (def: JobDef, job: SetupJob) => ReactNode
}) {
  return (
    <ul className="divide-y divide-border" aria-label="Model jobs">
      {JOBS.map((def) => {
        const job = jobFor(jobs, def.id)
        const pct = jobPercent(job)
        const flagged = def.required && !underWay(job.state)
        return (
          <li key={def.id} className="space-y-1.5 py-2.5" data-testid={`job-${def.id}`}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex min-w-0 items-center gap-2">
                <Icon job={job} />
                <span className="text-subheading text-text">{def.title}</span>
                {def.required ? (
                  <span className="text-caption text-text-muted">required for voice</span>
                ) : (
                  <span className="text-caption text-text-muted">optional</span>
                )}
              </div>
              <div className="flex items-center gap-2">
                <Badge tone={tone(job)}>{jobStateText(job, wizard)}</Badge>
                {action?.(def, job)}
              </div>
            </div>
            {pct !== null && <ProgressBar value={pct} max={100} label={`${def.title} download`} />}
            {job.state === 'downloading' && job.install && (
              <p className="text-small text-text-muted">{job.install.model_id}</p>
            )}
            {job.state === 'failed' && (
              <p className="text-small text-danger">
                {job.install?.error
                  ? `${job.install.model_id}: ${job.install.error}`
                  : `The ${def.label} engine is ${job.label_state.replace(/_/g, ' ')}.`}
              </p>
            )}
            {flagged && job.state === 'missing' && (
              <p className="text-small text-danger">Voice requests can't work without it.</p>
            )}
          </li>
        )
      })}
    </ul>
  )
}
