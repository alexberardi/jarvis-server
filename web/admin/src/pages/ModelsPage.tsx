import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Filter, ListChecks, X } from 'lucide-react'
import CatalogList from '@/components/models/CatalogList'
import HardwarePanel from '@/components/models/HardwarePanel'
import HfBrowser from '@/components/models/HfBrowser'
import HfTokenPrompt from '@/components/models/HfTokenPrompt'
import InstalledList from '@/components/models/InstalledList'
import InstallsList from '@/components/models/InstallsList'
import FeatureModelsCard from '@/components/models/FeatureModelsCard'
import LabelsEditor from '@/components/models/LabelsEditor'
import SpeechCard from '@/components/models/SpeechCard'
import PromptProviderCard from '@/components/models/PromptProviderCard'
import PromptEditorCard from '@/components/models/PromptEditorCard'
import ModelCard from '@/components/models/ModelCard'
import { Banner, Button, PageHeader, Tabs } from '@/components/ui'
import JobChecklist from '@/components/setup/JobChecklist'
import { jobDef, underWay } from '@/components/setup/jobs'
import { useSetupState } from '@/hooks/useSetup'

const TABS = ['jobs', 'catalog', 'installed', 'prompts', 'hardware'] as const
type Tab = (typeof TABS)[number]

/**
 * ModelsPage is the model manager (S8), in tabs so a phone isn't one 8,000px stack: Jobs (the
 * per-job checklist, AD3b, with a "Set up" per missing job, and what each label runs), Catalog
 * (the catalog with fit, narrowed by a job's "Set up", and Hugging Face installs), Installed,
 * Prompts and Hardware. Installs in progress stay above the tabs. `?job=stt` (the dashboard
 * banner's link) opens the Catalog with that filter; `?tab=` opens a tab (a tab chosen here is always
 * written to it, so Jobs opens even while a filter is on).
 */
export default function ModelsPage() {
  const [tokenReason, setTokenReason] = useState<string | null>(null)
  const [params, setParams] = useSearchParams()
  const setup = useSetupState()
  const catalogRef = useRef<HTMLDivElement>(null)
  const filter = jobDef(params.get('job') ?? '')
  const asked = params.get('tab') as Tab | null
  const tab: Tab = asked && TABS.includes(asked) ? asked : filter ? 'catalog' : 'jobs'

  // Following a "Set up" (here or from the dashboard) lands on the narrowed catalog.
  useEffect(() => {
    if (filter) catalogRef.current?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
  }, [filter])

  function update(patch: Record<string, string | null>) {
    const next = new URLSearchParams(params)
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v)
      else next.delete(k)
    }
    setParams(next, { replace: true })
  }
  const showJob = (id: string | null) => update({ job: id, tab: 'catalog' })

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader title="Models" description="Install models, choose what each job runs on, and see what is loaded on your GPU." />

      <div className="space-y-4">
        {tokenReason && (
          <HfTokenPrompt reason={`${tokenReason}. Save a token, then install again.`} onSaved={() => setTokenReason(null)} />
        )}
        <InstallsList />

        <Tabs
          label="Models"
          value={tab}
          onValueChange={(id) => update({ tab: id })}
          tabs={[
            {
              id: 'jobs',
              label: 'Jobs',
              content: (
                <div className="space-y-4">
                  {setup.data?.jobs && (
                    <ModelCard title="Jobs" icon={ListChecks} description="What each part of Jarvis needs, and whether it has it.">
                      <JobChecklist
                        jobs={setup.data.jobs}
                        action={(def, job) =>
                          !underWay(job.state) && (
                            <Button size="sm" onClick={() => showJob(def.id)} aria-label={`Set up ${def.title}`}>
                              Set up
                            </Button>
                          )
                        }
                      />
                    </ModelCard>
                  )}
                  <LabelsEditor />
                  <SpeechCard />
                  <FeatureModelsCard />
                </div>
              ),
            },
            {
              id: 'catalog',
              label: 'Catalog',
              content: (
                <div className="space-y-4">
                  <div ref={catalogRef} className="scroll-mt-4 space-y-3">
                    {filter && (
                      <Banner
                        tone="primary"
                        icon={<Filter size={16} />}
                        actions={
                          <Button size="sm" variant="ghost" icon={<X size={14} aria-hidden />} onClick={() => showJob(null)}>
                            Show all
                          </Button>
                        }
                      >
                        Showing models for <strong>{filter.title}</strong>
                      </Banner>
                    )}
                    <CatalogList onNeedsToken={setTokenReason} kinds={filter ? [filter.kind] : undefined} />
                  </div>
                  <HfBrowser />
                </div>
              ),
            },
            { id: 'installed', label: 'Installed', content: <InstalledList /> },
            {
              id: 'prompts',
              label: 'Prompts',
              content: (
                <div className="space-y-4">
                  <PromptProviderCard />
                  <PromptEditorCard />
                </div>
              ),
            },
            { id: 'hardware', label: 'Hardware', content: <HardwarePanel /> },
          ]}
        />
      </div>
    </div>
  )
}
