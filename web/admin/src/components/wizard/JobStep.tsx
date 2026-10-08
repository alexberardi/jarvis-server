import { useMemo, useState } from 'react'
import { AudioLines, Brain, CheckCircle2, Fingerprint, Mic, Search, Sparkles, Star } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { toast } from 'sonner'
import { setVoice } from '@/api/tts'
import FitBadge from '@/components/models/FitBadge'
import HfBrowser from '@/components/models/HfBrowser'
import HfTokenPrompt from '@/components/models/HfTokenPrompt'
import PromptProviderCard from '@/components/models/PromptProviderCard'
import { currentModels, fitSummary } from '@/components/models/logic'
import { buttonClass } from '@/components/models/styles'
import { Pill, ProgressBar } from '@/components/models/ui'
import { useInstallAction } from '@/components/models/useInstallAction'
import {
  everythingRecommended,
  defaultChoice,
  jobFor,
  jobPercent,
  jobStateText,
  recommendedEntry,
  requestFor,
  underWay,
  type JobDef,
} from '@/components/setup/jobs'
import { useCatalog, useLabels } from '@/hooks/useModelManager'
import { useSetupState } from '@/hooks/useSetup'
import { errorMessage } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import { cn } from '@/lib/utils'
import { currentChoices } from './hardware'
import VoicePicker from './VoicePicker'

const ICONS: Record<string, LucideIcon> = {
  llm: Brain,
  stt: Mic,
  voice: AudioLines,
  speaker: Fingerprint,
  memory: Search,
}

/** Where speech-to-text runs, as the Hardware step left it. */
function SttPlacement() {
  const { data } = useLabels()
  const setup = useSetupState()
  const hw = setup.data?.hardware
  if (!data || !hw) return null
  // The Hardware step's own reading of the labels, so both steps say the same thing.
  const c = currentChoices(hw, data.labels)
  return (
    <p className="text-xs text-[var(--color-text-muted)]">
      {c.stt === 'cpu'
        ? 'Runs on the CPU, as set in the Hardware step.'
        : `Runs on the GPU (${c.flavour}${c.sttDevice ? `, device ${c.sttDevice}` : ''}), as set in the Hardware step.`}
    </p>
  )
}

/**
 * JobStep is one model job of the setup wizard (AD3b): the catalog's models of its kind,
 * pre-selected to what it runs, what is downloading for it, or the hardware-fit
 * recommendation. Confirming starts the install and moves on at once; the download continues
 * in the background (the model manager queues it) while the later steps are filled in. The
 * first step also offers "Install everything recommended", which confirms all five.
 */
export default function JobStep({
  def,
  onNext,
  onBack,
  onAllConfirmed,
}: {
  def: JobDef
  onNext: () => void
  onBack: () => void
  onAllConfirmed?: () => void
}) {
  const catalog = useCatalog()
  const labels = useLabels()
  const state = useSetupState()
  const [tokenReason, setTokenReason] = useState<string | null>(null)
  const { install, installAll, isPending } = useInstallAction(setTokenReason)
  const [picked, setPicked] = useState<string | null>(null)
  const [voice, setVoiceChoice] = useState('')
  const [voiceDirty, setVoiceDirty] = useState(false)
  const [saving, setSaving] = useState(false)

  const current = useMemo(() => currentModels(labels.data), [labels.data])
  const job = jobFor(state.data?.jobs, def.id)
  const options = catalog.data?.models.filter((m) => m.kind === def.kind) ?? []
  const choice = picked ?? defaultChoice(def, catalog.data, current, job)
  const entry = options.find((m) => m.id === choice)
  const recommended = recommendedEntry(def, catalog.data)
  // The server's job state can be newer than the labels list (an install finished between two
  // label polls): a job that runs, with the pre-filled choice untouched, needs no install.
  const settled =
    current[def.label] === choice ||
    (job.state === 'downloading' && job.install?.model_id === choice) ||
    ((job.state === 'ready' || job.state === 'loading') && !current[def.label] && picked === null)
  const everything = catalog.data ? everythingRecommended(catalog.data, state.data?.jobs, current) : []
  const everythingBytes = everything.reduce((n, it) => {
    const e = catalog.data?.models.find((m) => m.id === it.req.catalog_id)
    const proj = e?.mmproj ? catalog.data?.models.find((m) => m.id === e.mmproj) : undefined
    return n + (e && !e.installed ? e.size : 0) + (proj && !proj.installed ? proj.size : 0)
  }, 0)
  const pct = jobPercent(job)
  const Icon = ICONS[def.id] ?? Sparkles
  const busy = isPending || saving

  async function confirm() {
    setSaving(true)
    try {
      if (def.id === 'voice' && voiceDirty && voice) {
        try {
          await setVoice(voice)
        } catch (err) {
          toast.error(`Could not save the voice: ${errorMessage(err)}`)
          return
        }
      }
      if (entry && !settled) {
        const res = await install(requestFor(def, entry, current), entry.display)
        if (!res) return
      }
      onNext()
    } finally {
      setSaving(false)
    }
  }

  async function confirmAll() {
    await installAll(everything)
    onAllConfirmed?.()
  }

  const primaryText = busy
    ? 'Starting…'
    : settled || !entry
      ? 'Continue'
      : entry.installed
        ? 'Use and continue'
        : 'Install and continue'

  return (
    <div className="space-y-4">
      {onAllConfirmed && everything.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-3">
          <div className="min-w-0">
            <p className="text-sm font-medium text-[var(--color-text)]">Install everything recommended</p>
            <p className="text-xs text-[var(--color-text-muted)]">
              Picks the recommended model for each of the five jobs on this machine and starts the downloads
              {everythingBytes > 0 ? ` (${formatBytes(everythingBytes)} in all)` : ''}. You can change any of them later.
            </p>
          </div>
          <button type="button" className={buttonClass.primary} disabled={busy} onClick={confirmAll}>
            <Sparkles size={12} /> Install everything recommended
          </button>
        </div>
      )}

      <div>
        <h2 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
          <Icon size={20} /> {def.title}
          <Pill tone={def.required ? 'info' : 'muted'}>{def.required ? 'Required' : 'Optional'}</Pill>
        </h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">{def.blurb}</p>
      </div>

      {job.state !== 'missing' && (
        <div className="space-y-1.5 rounded-lg bg-[var(--color-surface-alt)] px-3 py-2" data-testid="job-status">
          <p className="text-xs text-[var(--color-text)]">
            {job.state === 'failed' && job.install?.error
              ? `${job.install.model_id} failed: ${job.install.error}`
              : `${jobStateText(job, true)}${job.install && job.state === 'downloading' ? ` · ${job.install.model_id}` : ''}${
                  current[def.label] && job.state !== 'downloading' ? ` · ${current[def.label]}` : ''
                }`}
          </p>
          {pct !== null && <ProgressBar value={pct} max={100} label={`${def.title} download`} />}
        </div>
      )}

      {tokenReason && (
        <HfTokenPrompt reason={`${tokenReason}. Save a token, then install again.`} onSaved={() => setTokenReason(null)} />
      )}

      {catalog.isLoading && <p className="text-sm text-[var(--color-text-muted)]">Loading the catalog…</p>}
      {catalog.isError && <p className="text-sm text-red-500">{errorMessage(catalog.error, 'Could not load the catalog')}</p>}

      {options.length > 0 && (
        <fieldset className="space-y-2">
          <legend className="sr-only">{def.title} model</legend>
          {options.map((m) => {
            const isRec = recommended?.id === m.id
            const inUse = current[def.label] === m.id
            return (
              <label
                key={m.id}
                className={cn(
                  'flex cursor-pointer items-start gap-3 rounded-lg border p-3',
                  choice === m.id ? 'border-[var(--color-primary)] bg-[var(--color-primary)]/5' : 'border-[var(--color-border)]',
                )}
              >
                <input
                  type="radio"
                  name={`model-${def.id}`}
                  value={m.id}
                  checked={choice === m.id}
                  onChange={() => setPicked(m.id)}
                  className="mt-1"
                  aria-label={m.display}
                />
                <div className="min-w-0 flex-1 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium text-[var(--color-text)]">{m.display}</span>
                    {isRec && (
                      <Pill tone="info">
                        <Star size={10} /> Recommended
                      </Pill>
                    )}
                    <FitBadge fit={m.fit} />
                    {m.mmproj && <Pill>vision</Pill>}
                    {inUse ? (
                      <Pill tone="ok">
                        <CheckCircle2 size={10} /> In use
                      </Pill>
                    ) : (
                      m.installed && <Pill tone="ok">Installed</Pill>
                    )}
                  </div>
                  <p className="text-xs text-[var(--color-text-muted)]">
                    {formatBytes(m.size)} · {fitSummary(m.fit)}
                  </p>
                  {m.notes && <p className="text-xs text-[var(--color-text-muted)]">{m.notes}</p>}
                </div>
              </label>
            )
          })}
        </fieldset>
      )}
      {catalog.data && options.length === 0 && (
        <p className="text-sm text-[var(--color-text-muted)]">The catalog has no {def.title.toLowerCase()} models for this machine.</p>
      )}

      {def.id === 'llm' && entry?.mmproj && (
        <p className="text-xs text-[var(--color-text-muted)]">
          Includes its vision projector, so Jarvis can also look at photos (recipes, receipts).
        </p>
      )}
      {def.id === 'stt' && <SttPlacement />}
      {def.id === 'voice' && (
        <VoicePicker
          value={voice}
          onChange={(v) => {
            if (voice) setVoiceDirty(true)
            setVoiceChoice(v)
          }}
          canSample={job.state === 'ready'}
        />
      )}
      {def.id === 'speaker' && (
        <p className="text-xs text-[var(--color-text-muted)]">
          Each person enrols their voice later in the Jarvis mobile app; until then nobody is recognised. Runs on the CPU.
        </p>
      )}
      {def.id === 'memory' && (
        <p className="text-xs text-[var(--color-text-muted)]">Runs on the CPU next to everything else; it is small.</p>
      )}
      {def.id === 'llm' && (
        <>
          <details className="rounded-xl border border-[var(--color-border)] p-3">
            <summary className="cursor-pointer text-sm text-[var(--color-text)]">Install a different model from Hugging Face</summary>
            <div className="mt-3">
              <HfBrowser />
            </div>
          </details>
          <PromptProviderCard />
        </>
      )}

      <div className="flex flex-wrap items-center justify-between gap-3">
        <button type="button" className={buttonClass.secondary} onClick={onBack} disabled={busy}>
          Back
        </button>
        <div className="flex items-center gap-3">
          {!underWay(job.state) && (
            <button
              type="button"
              className="text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
              onClick={onNext}
              disabled={busy}
            >
              {def.required ? 'Skip for now' : 'Skip'}
            </button>
          )}
          <button type="button" className={buttonClass.primary} disabled={busy} onClick={confirm}>
            {primaryText}
          </button>
        </div>
      </div>
    </div>
  )
}
