import { useState } from 'react'
import { Box } from 'lucide-react'
import CatalogList from '@/components/models/CatalogList'
import HfBrowser from '@/components/models/HfBrowser'
import HfTokenPrompt from '@/components/models/HfTokenPrompt'
import InstallsList from '@/components/models/InstallsList'
import PromptProviderCard from '@/components/models/PromptProviderCard'
import { buttonClass } from '@/components/models/styles'
import { useLabels } from '@/hooks/useModelManager'

/**
 * ModelsStep installs the models Jarvis needs, on the model manager (LD3: nothing downloads
 * by itself). "Install recommended" in the catalog is the one click; installs keep running
 * in the background, so the operator may continue (or skip) while they download. The
 * dashboard keeps reminding while the live label has no model.
 */
export default function ModelsStep({ onDone }: { onDone: () => void }) {
  const [tokenReason, setTokenReason] = useState<string | null>(null)
  const { data: labels } = useLabels()
  const live = labels?.labels.find((l) => l.label === 'live')
  const configured = Boolean(live && live.state !== 'not_configured')

  return (
    <div className="space-y-4">
      <div>
        <h2 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
          <Box size={20} /> Models
        </h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          Jarvis needs a language model to answer, plus small speech models. <strong>Install recommended</strong> picks a
          set that fits this machine. Downloads continue in the background, so you can move on while they finish, or skip
          this and install models later from the Models page.
        </p>
      </div>

      {tokenReason && (
        <HfTokenPrompt reason={`${tokenReason}. Save a token, then install again.`} onSaved={() => setTokenReason(null)} />
      )}

      <InstallsList />
      <CatalogList onNeedsToken={setTokenReason} />
      <details className="rounded-xl border border-[var(--color-border)] p-3">
        <summary className="cursor-pointer text-sm text-[var(--color-text)]">Install a different model from Hugging Face</summary>
        <div className="mt-3">
          <HfBrowser />
        </div>
      </details>
      <PromptProviderCard />

      <div className="flex items-center justify-end gap-3">
        {!configured && (
          <button type="button" className="text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]" onClick={onDone}>
            Skip for now
          </button>
        )}
        <button type="button" className={buttonClass.primary} onClick={onDone} disabled={!configured}>
          Continue
        </button>
      </div>
    </div>
  )
}
