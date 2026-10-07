import { useState } from 'react'
import CatalogList from '@/components/models/CatalogList'
import HardwarePanel from '@/components/models/HardwarePanel'
import HfBrowser from '@/components/models/HfBrowser'
import HfTokenPrompt from '@/components/models/HfTokenPrompt'
import InstalledList from '@/components/models/InstalledList'
import InstallsList from '@/components/models/InstallsList'
import LabelsEditor from '@/components/models/LabelsEditor'
import PromptProviderCard from '@/components/models/PromptProviderCard'

/**
 * ModelsPage is the model manager (S8): what each label runs, installs with progress, the
 * catalog with fit, Hugging Face installs, installed models, and the hardware view. Every
 * section is a standalone component, so the A7 setup wizard's Hardware and Models steps reuse
 * them.
 */
export default function ModelsPage() {
  const [tokenReason, setTokenReason] = useState<string | null>(null)

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

      <InstallsList />
      <LabelsEditor />
      <PromptProviderCard />
      <CatalogList onNeedsToken={setTokenReason} />
      <HfBrowser />
      <InstalledList />
      <HardwarePanel />
    </div>
  )
}
