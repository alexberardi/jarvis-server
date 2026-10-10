import { Link } from 'react-router-dom'
import { Brain, Loader2 } from 'lucide-react'
import { JOBS, jobFor, jobStateText, joinTitles, missingRequired } from '@/components/setup/jobs'
import { useSetupState } from '@/hooks/useSetup'

const STATE_TEXT: Record<string, string> = {
  not_configured: 'not configured',
  fetching_engine: 'fetching engine',
  no_engine_build: 'no engine build',
}

/**
 * The banner while setup isn't finished, while voice lacks a required model job (AD3b: named,
 * from the per-job summary), while models download, or while the live model isn't serving yet.
 */
export default function ModelBanner() {
  const { data } = useSetupState()
  if (!data?.superuser) return null
  if (data.setup_step) {
    // A10 F9: the wizard was left before Done (another tab, a closed browser).
    return (
      <div className="flex items-center justify-between rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-4">
        <p className="text-sm text-[var(--color-text)]">Setup isn't finished: models, privacy choices and next steps.</p>
        <Link to="/setup" className="rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs text-white hover:opacity-90">
          Finish setup
        </Link>
      </div>
    )
  }
  const missing = missingRequired(data.jobs)
  if (missing.length > 0 || (!data.jobs && data.models_configured === false)) {
    const names = missing.length > 0 ? joinTitles(missing.map((m) => m.title)) : 'Language model'
    return (
      <div
        role="status"
        aria-label="Voice setup"
        className="flex items-center justify-between gap-3 rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-4"
      >
        <div className="flex items-center gap-3">
          <Brain size={20} className="text-[var(--color-primary)]" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">
              Voice isn't ready: {names} {missing.length > 1 ? 'are' : 'is'} missing
            </p>
            <p className="text-xs text-[var(--color-text-muted)]">
              Jarvis can't answer voice requests until {missing.length > 1 ? 'they are' : 'it is'} installed.
            </p>
          </div>
        </div>
        <Link
          to={`/models?job=${missing[0]?.id ?? 'llm'}`}
          className="shrink-0 rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs text-white hover:opacity-90"
        >
          Set up models
        </Link>
      </div>
    )
  }
  const downloading = JOBS.map((d) => ({ d, j: jobFor(data.jobs, d.id) })).filter(({ j }) => j.state === 'downloading')
  if (downloading.length > 0) {
    return (
      <div role="status" className="flex items-center gap-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-4">
        <Loader2 size={18} className="animate-spin text-amber-500" />
        <p className="text-sm text-[var(--color-text)]">
          {downloading.map(({ d, j }) => `${d.title}: ${jobStateText(j).toLowerCase()}`).join(' · ')}.{' '}
          <Link to="/models" className="text-[var(--color-primary)] hover:underline">
            Details
          </Link>
        </p>
      </div>
    )
  }
  if (data.models_configured && data.live_ready === false) {
    const st = data.labels?.live ?? 'unknown'
    // A lost GPU isn't something to wait out: the dashboard's GPU banner says what to do.
    if (st === 'gpu_unavailable') return null
    return (
      <div className="flex items-center gap-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-4">
        <Loader2 size={18} className="animate-spin text-amber-500" />
        <p className="text-sm text-[var(--color-text)]">
          The live model is {STATE_TEXT[st] ?? st}. Voice requests are answered once it is ready.{' '}
          <Link to="/models" className="text-[var(--color-primary)] hover:underline">
            Details
          </Link>
        </p>
      </div>
    )
  }
  return null
}
