import { useMemo, useState } from 'react'
import { BookOpen, CheckCircle2, Sparkles, Star } from 'lucide-react'
import {
  ALL_LABELS,
  LABEL_TITLE,
  isActiveInstall,
  labelsForKind,
  type CatalogEntry,
  type CatalogResponse,
  type InstallRequest,
  type Label,
} from '@/api/llm'
import { useCatalog, useInstalls, useLabels } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import AssignPicker from './AssignPicker'
import FitBadge, { ResidentsNote } from './FitBadge'
import { currentModels, defaultAssign, fitSummary, recommendedFor, recommendedInstalls } from './logic'
import { useInstallAction } from './useInstallAction'
import { buttonClass } from './styles'
import { Pill, Section } from './ui'

const KIND_GROUPS: { kind: string; title: string }[] = [
  { kind: 'llm', title: 'Language models' },
  { kind: 'embedding', title: 'Embeddings' },
  { kind: 'stt', title: 'Speech to text' },
  { kind: 'tts', title: 'Text to speech' },
  { kind: 'speaker', title: 'Speaker ID' },
]

function CatalogRow({
  entry,
  recommended,
  current,
  installing,
  onNeedsToken,
}: {
  entry: CatalogEntry
  recommended: CatalogResponse['recommended']
  current: Record<Label, string>
  installing: boolean
  onNeedsToken?: (reason: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [assign, setAssign] = useState<Label[]>([])
  const [withMMProj, setWithMMProj] = useState(true)
  const { install, isPending } = useInstallAction(onNeedsToken)
  const recFor = recommendedFor(entry, recommended)
  const usedBy = ALL_LABELS.filter((l) => current[l] === entry.id)

  function toggle() {
    if (!open) {
      setAssign(defaultAssign(entry, recommended, current))
      setWithMMProj(true)
    }
    setOpen(!open)
  }

  function start() {
    const req: InstallRequest = { catalog_id: entry.id, assign }
    if (entry.mmproj) req.with_mmproj = withMMProj
    void install(req, entry.display, () => setOpen(false))
  }

  return (
    <li className="rounded-lg border border-[var(--color-border)] p-3" data-testid={`catalog-${entry.id}`}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-[var(--color-text)]">{entry.display}</span>
            {recFor.length > 0 && (
              <Pill tone="info" title={`Recommended for ${recFor.map((l) => LABEL_TITLE[l]).join(', ')}`}>
                <Star size={10} /> Recommended
              </Pill>
            )}
            <FitBadge fit={entry.fit} />
            {entry.mmproj && <Pill>vision</Pill>}
            {entry.thinking && <Pill>thinking</Pill>}
          </div>
          <p className="text-xs text-[var(--color-text-muted)]">
            {formatBytes(entry.size)}
            {entry.context_default ? ` · ${entry.context_default.toLocaleString()} context` : ''}
            {entry.prompt_provider ? ` · prompt ${entry.prompt_provider}` : ''} · {fitSummary(entry.fit)}
          </p>
          {entry.notes && <p className="text-xs text-[var(--color-text-muted)]">{entry.notes}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {entry.installed ? (
            <Pill tone="ok">
              <CheckCircle2 size={10} /> Installed{usedBy.length > 0 ? ` · ${usedBy.join(', ')}` : ''}
            </Pill>
          ) : installing ? (
            <Pill tone="warn">Installing…</Pill>
          ) : (
            <button type="button" className={buttonClass.secondary} onClick={toggle} aria-expanded={open}>
              {open ? 'Close' : 'Install'}
            </button>
          )}
        </div>
      </div>

      {open && !entry.installed && (
        <div className="mt-3 space-y-2 border-t border-[var(--color-border)] pt-3">
          <AssignPicker kind={entry.kind} value={assign} onChange={setAssign} idPrefix={entry.id} />
          {entry.mmproj && (
            <label className="flex items-center gap-1.5 text-xs text-[var(--color-text)]">
              <input type="checkbox" checked={withMMProj} onChange={(e) => setWithMMProj(e.target.checked)} />
              Include the vision projector (lets this model see images)
            </label>
          )}
          <div className="flex justify-end">
            <button type="button" className={buttonClass.primary} disabled={isPending} onClick={start}>
              {isPending ? 'Starting…' : `Install ${entry.display}`}
            </button>
          </div>
        </div>
      )}
    </li>
  )
}

/**
 * CatalogList is the curated catalog with a fit verdict per model, judged next to what already
 * runs on the card. `kinds` narrows it (the A7 wizard shows one kind per sub-step if it wants).
 */
export default function CatalogList({
  kinds,
  onNeedsToken,
  showInstallRecommended = true,
}: {
  kinds?: string[]
  onNeedsToken?: (reason: string) => void
  showInstallRecommended?: boolean
}) {
  const { data, isLoading, isError, error } = useCatalog()
  const { data: labels } = useLabels()
  const { data: installs } = useInstalls()
  const { installAll, isPending } = useInstallAction(onNeedsToken)

  const current = useMemo(() => currentModels(labels), [labels])
  const installingIds = useMemo(
    () => new Set((installs ?? []).filter(isActiveInstall).map((i) => i.model_id)),
    [installs],
  )

  if (isLoading) return <Section title="Catalog" icon={BookOpen}><p className="text-sm text-[var(--color-text-muted)]">Loading…</p></Section>
  if (isError || !data) {
    return (
      <Section title="Catalog" icon={BookOpen}>
        <p className="text-sm text-red-500">{errorMessage(error, 'Could not load the catalog')}</p>
      </Section>
    )
  }

  const pending = recommendedInstalls(data).filter((r) => !installingIds.has(r.catalog_id ?? ''))
  const groups = KIND_GROUPS.filter((g) => !kinds || kinds.includes(g.kind))

  function installRecommended() {
    void installAll(
      pending.map((req) => ({
        req,
        name: data!.models.find((m) => m.id === req.catalog_id)?.display ?? req.catalog_id ?? 'model',
      })),
    )
  }

  return (
    <Section
      title="Catalog"
      icon={BookOpen}
      description="Tested models with pinned downloads. The verdict accounts for what already runs on your GPU."
      actions={
        showInstallRecommended && pending.length > 0 ? (
          <button
            type="button"
            className={buttonClass.primary}
            disabled={isPending}
            onClick={installRecommended}
            title={pending.map((r) => `${r.catalog_id} → ${(r.assign ?? []).join(', ')}`).join('\n')}
          >
            <Sparkles size={12} /> Install recommended ({pending.length})
          </button>
        ) : undefined
      }
    >
      <div className="space-y-4">
        <ResidentsNote residents={data.residents} />
        {groups.map((g) => {
          const entries = data.models.filter((m) => m.kind === g.kind)
          if (entries.length === 0) return null
          return (
            <div key={g.kind} className="space-y-2">
              <h3 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
                {g.title}
                {labelsForKind(g.kind).length > 0 && (
                  <span className="ml-2 font-normal normal-case tracking-normal">
                    ({labelsForKind(g.kind).map((l) => LABEL_TITLE[l]).join(', ')})
                  </span>
                )}
              </h3>
              <ul className="space-y-2">
                {entries.map((e) => (
                  <CatalogRow
                    key={e.id}
                    entry={e}
                    recommended={data.recommended}
                    current={current}
                    installing={installingIds.has(e.id)}
                    onNeedsToken={onNeedsToken}
                  />
                ))}
              </ul>
            </div>
          )
        })}
      </div>
    </Section>
  )
}
