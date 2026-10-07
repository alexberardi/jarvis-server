import { useState } from 'react'
import { HardDrive, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import type { InstalledModel } from '@/api/llm'
import { useDeleteModel, useInstalled } from '@/hooks/useModelManager'
import { errorMessage, errorStatus } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import { buttonClass, stateTone } from './styles'
import { Pill, Section } from './ui'

/**
 * A pending delete. `force` is set once the server answered 409 because labels use the model:
 * the operator then confirms clearing those labels.
 */
interface PendingDelete {
  model: InstalledModel
  conflict?: string
}

function ConfirmDelete({
  pending,
  busy,
  onConfirm,
  onCancel,
}: {
  pending: PendingDelete
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  const { model, conflict } = pending
  return (
    <div role="alertdialog" aria-label={`Delete ${model.id}`} className="mt-2 space-y-2 rounded-lg border border-red-500/30 bg-red-500/5 p-3">
      <p className="text-xs text-[var(--color-text)]">
        {conflict ? (
          <>
            <span className="font-medium">{conflict}.</span> Delete anyway? Those labels will have no model until you
            assign another.
          </>
        ) : model.external ? (
          <>Forget {model.id}? The file stays on disk (it was registered in place).</>
        ) : (
          <>
            Delete {model.id} and its files ({formatBytes(model.bytes_done || model.size)})?
          </>
        )}
      </p>
      <div className="flex justify-end gap-2">
        <button type="button" className={buttonClass.secondary} onClick={onCancel} disabled={busy}>
          Keep it
        </button>
        <button type="button" className={buttonClass.danger} onClick={onConfirm} disabled={busy}>
          {busy ? 'Deleting…' : conflict ? 'Delete and unassign' : 'Delete'}
        </button>
      </div>
    </div>
  )
}

/** InstalledList is every installed model (all kinds, voice included) with delete and force. */
export default function InstalledList() {
  const { data, isLoading, isError, error } = useInstalled()
  const del = useDeleteModel()
  const [pending, setPending] = useState<PendingDelete | null>(null)

  function confirm() {
    if (!pending) return
    const { model, conflict } = pending
    del.mutate(
      { id: model.id, force: Boolean(conflict) },
      {
        onSuccess: () => {
          toast.success(`Deleted ${model.id}`)
          setPending(null)
        },
        onError: (err) => {
          if (errorStatus(err) === 409 && !conflict && /assigned/i.test(errorMessage(err, ''))) {
            // The labels changed since the list loaded: ask again, now as a forced delete.
            setPending({ model, conflict: errorMessage(err).replace(/\s*\(use force=true\)\s*$/, '') })
            return
          }
          toast.error(`Could not delete ${model.id}: ${errorMessage(err)}`)
          setPending(null)
        },
      },
    )
  }

  function ask(model: InstalledModel) {
    setPending({
      model,
      conflict: model.labels.length > 0 ? `${model.id} is assigned to ${model.labels.join(', ')}` : undefined,
    })
  }

  return (
    <Section
      title="Installed"
      icon={HardDrive}
      description={data ? `${formatBytes(data.disk_bytes)} in ${data.dir}` : undefined}
    >
      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Loading…</p>}
      {isError && <p className="text-sm text-red-500">{errorMessage(error, 'Could not list installed models')}</p>}
      {data && data.models.length === 0 && (
        <p className="text-sm text-[var(--color-text-muted)]">No models installed yet. Pick one from the catalog.</p>
      )}
      {data && data.models.length > 0 && (
        <ul className="divide-y divide-[var(--color-border)]">
          {data.models.map((m) => (
            <li key={m.id} className="py-2" data-testid={`installed-${m.id}`}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-sm text-[var(--color-text)]">{m.id}</span>
                    <Pill>{m.kind}</Pill>
                    {m.state !== 'ready' && <Pill tone={stateTone(m.state)}>{m.state}</Pill>}
                    {m.external && <Pill title={m.path}>registered</Pill>}
                    {m.labels.map((l) => (
                      <Pill key={l} tone="info">
                        {l}
                      </Pill>
                    ))}
                  </div>
                  <p className="text-xs text-[var(--color-text-muted)]">
                    {m.display !== m.id ? `${m.display} · ` : ''}
                    {formatBytes(m.size)}
                    {m.prompt_provider ? ` · prompt ${m.prompt_provider}` : ''}
                    {m.mmproj_id ? ` · projector ${m.mmproj_id}` : ''}
                  </p>
                  {m.error && <p className="text-xs text-red-500">{m.error}</p>}
                </div>
                <button
                  type="button"
                  className={buttonClass.icon}
                  title={`Delete ${m.id}`}
                  aria-label={`Delete ${m.id}`}
                  onClick={() => ask(m)}
                  disabled={del.isPending}
                >
                  <Trash2 size={14} />
                </button>
              </div>
              {pending?.model.id === m.id && (
                <ConfirmDelete pending={pending} busy={del.isPending} onConfirm={confirm} onCancel={() => setPending(null)} />
              )}
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}
