import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { ListChecks, X } from 'lucide-react'
import CatalogList from '@/components/models/CatalogList'
import HardwarePanel from '@/components/models/HardwarePanel'
import HfBrowser from '@/components/models/HfBrowser'
import HfTokenPrompt from '@/components/models/HfTokenPrompt'
import InstalledList from '@/components/models/InstalledList'
import InstallsList from '@/components/models/InstallsList'
import LabelsEditor from '@/components/models/LabelsEditor'
import PromptProviderCard from '@/components/models/PromptProviderCard'
import { buttonClass } from '@/components/models/styles'
import { Section } from '@/components/models/ui'
import JobChecklist from '@/components/setup/JobChecklist'
import { jobDef, underWay } from '@/components/setup/jobs'
import { useSetupState } from '@/hooks/useSetup'

/**
 * ModelsPage is the model manager (S8): the per-job checklist (AD3b) with a "Set up" per
 * missing job that narrows the catalog to that job's models, then what each label runs,
 * installs with progress, the catalog with fit, Hugging Face installs, installed models, and
 * the hardware view. `?job=stt` (the dashboard banner's link) opens with that filter.
 */
export default function ModelsPage() {
  const [tokenReason, setTokenReason] = useState<string | null>(null)
  const [params, setParams] = useSearchParams()
  const setup = useSetupState()
  const catalogRef = useRef<HTMLDivElement>(null)
  const filter = jobDef(params.get('job') ?? '')

  // Following a "Set up" (here or from the dashboard) lands on the narrowed catalog.
  useEffect(() => {
    if (filter) catalogRef.current?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
  }, [filter])

  function showJob(id: string | null) {
    const next = new URLSearchParams(params)
    if (id) next.set('job', id)
    else next.delete('job')
    setParams(next, { replace: true })
  }

  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <div>
        <h1 className="text-xl font-bold text-[var(--color-text)]">Models</h1>
        <p className="text-sm text-[var(--color-text-muted)]">
          Install models, choose what each job runs on, and see what is loaded on your GPU.
        </p>
      </div>

      {tokenReason && (
        <HfTokenPrompt reason={`${tokenReason}. Save a token, then install again.`} onSaved={() => setTokenReason(null)} />
      )}

      {setup.data?.jobs && (
        <Section title="Jobs" icon={ListChecks} description="What each part of Jarvis needs, and whether it has it.">
          <JobChecklist
            jobs={setup.data.jobs}
            action={(def, job) =>
              !underWay(job.state) && (
                <button
                  type="button"
                  className={buttonClass.secondary}
                  onClick={() => showJob(def.id)}
                  aria-label={`Set up ${def.title}`}
                >
                  Set up
                </button>
              )
            }
          />
        </Section>
      )}

      <InstallsList />
      <LabelsEditor />
      <PromptProviderCard />
      <div ref={catalogRef} className="scroll-mt-4 space-y-2">
        {filter && (
          <div className="flex items-center justify-between rounded-lg bg-[var(--color-surface-alt)] px-3 py-2 text-xs text-[var(--color-text)]">
            <span>
              Showing models for <strong>{filter.title}</strong>
            </span>
            <button type="button" className={buttonClass.secondary} onClick={() => showJob(null)}>
              <X size={12} /> Show all
            </button>
          </div>
        )}
        <CatalogList onNeedsToken={setTokenReason} kinds={filter ? [filter.kind] : undefined} />
      </div>
      <HfBrowser />
      <InstalledList />
      <HardwarePanel />
    </div>
  )
}
