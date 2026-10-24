import { Link } from 'react-router-dom'
import { Brain, Sparkles } from 'lucide-react'
import { JOBS, jobFor, jobStateText, joinTitles, missingRequired } from '@/components/setup/jobs'
import { Banner, buttonStyles, Spinner } from '@/components/ui'
import { useSetupState } from '@/hooks/useSetup'

const STATE_TEXT: Record<string, string> = {
  not_configured: 'not configured',
  fetching_engine: 'fetching engine',
  no_engine_build: 'no engine build',
  does_not_fit: 'too big for the GPU next to the other engines',
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
      <Banner
        tone="primary"
        icon={<Sparkles size={20} />}
        actions={
          <Link to="/setup" className={buttonStyles({ variant: 'primary', size: 'sm' })}>
            Finish setup
          </Link>
        }
      >
        <p className="text-body">Setup isn't finished: models, privacy choices and next steps.</p>
      </Banner>
    )
  }
  const missing = missingRequired(data.jobs)
  if (missing.length > 0 || (!data.jobs && data.models_configured === false)) {
    const names = missing.length > 0 ? joinTitles(missing.map((m) => m.title)) : 'Language model'
    return (
      <Banner
        role="status"
        aria-label="Voice setup"
        tone="primary"
        icon={<Brain size={20} />}
        title={`Voice isn't ready: ${names} ${missing.length > 1 ? 'are' : 'is'} missing`}
        actions={
          <Link to={`/admin/models?job=${missing[0]?.id ?? 'llm'}`} className={buttonStyles({ variant: 'primary', size: 'sm' })}>
            Set up models
          </Link>
        }
      >
        Jarvis can't answer voice requests until {missing.length > 1 ? 'they are' : 'it is'} installed.
      </Banner>
    )
  }
  const downloading = JOBS.map((d) => ({ d, j: jobFor(data.jobs, d.id) })).filter(({ j }) => j.state === 'downloading')
  if (downloading.length > 0) {
    return (
      <Banner role="status" tone="warning" icon={<Spinner className="size-5" />}>
        <p className="text-body">
          {downloading.map(({ d, j }) => `${d.title}: ${jobStateText(j).toLowerCase()}`).join(' · ')}.{' '}
          <Link to="/admin/models" className="text-primary underline-offset-2 hover:underline">
            Details
          </Link>
        </p>
      </Banner>
    )
  }
  if (data.models_configured && data.live_ready === false) {
    const st = data.labels?.live ?? 'unknown'
    // A lost GPU isn't something to wait out: the dashboard's GPU banner says what to do.
    if (st === 'gpu_unavailable') return null
    return (
      <Banner tone="warning" icon={<Spinner className="size-5" />}>
        <p className="text-body">
          The live model is {STATE_TEXT[st] ?? st}. Voice requests are answered once it is ready.{' '}
          <Link to="/admin/models" className="text-primary underline-offset-2 hover:underline">
            Details
          </Link>
        </p>
      </Banner>
    )
  }
  return null
}
