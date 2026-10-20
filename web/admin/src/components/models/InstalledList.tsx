import { useState } from 'react'
import { HardDrive, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import type { InstalledModel } from '@/api/llm'
import { useDeleteModel, useInstalled } from '@/hooks/useModelManager'
import { errorMessage, errorStatus } from '@/lib/errors'
import { formatBytes } from '@/lib/format'
import { Badge, Banner, Button, EmptyState, ErrorState, SkeletonList } from '@/components/ui'
import ModelCard from './ModelCard'
import { stateBadge } from './tone'

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
    <Banner
      tone="danger"
      role="alertdialog"
      aria-label={`Delete ${model.id}`}
      className="mt-3"
      icon={<Trash2 size={16} />}
      actions={
        <>
          <Button size="sm" onClick={onCancel} disabled={busy}>
            Keep it
          </Button>
          <Button size="sm" variant="danger" onClick={onConfirm} loading={busy}>
            {conflict ? 'Delete and unassign' : 'Delete'}
          </Button>
        </>
      }
    >
      {conflict ? (
        <>
          <span className="font-medium">{conflict}.</span> Delete anyway? Those labels will have no model until you
          assign another.
        </>
      ) : model.external ? (
        <>Forget {model.id}? The file stays on disk (it was registered in place).</>
      ) : (
        <>Delete {model.id} and its files ({formatBytes(model.bytes_done || model.size)})?</>
      )}
    </Banner>
  )
}

/** InstalledList is every installed model (all kinds, voice included) with delete and force. */
export default function InstalledList() {
  const { data, isLoading, isError, error, refetch } = useInstalled()
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
    <ModelCard
      title="Installed"
      icon={HardDrive}
      description={data ? `${formatBytes(data.disk_bytes)} in ${data.dir}` : undefined}
    >
      {isLoading && <SkeletonList rows={3} label="Loading installed models" />}
      {isError && (
        <ErrorState
          className="py-6"
          title="Couldn't list the installed models"
          message={errorMessage(error, 'The server did not answer.')}
          onRetry={() => refetch()}
        />
      )}
      {data && data.models.length === 0 && (
        <EmptyState
          className="py-6"
          icon={<HardDrive size={20} />}
          title="No models installed yet"
          description="Install one from the catalog: the recommended ones fit this computer."
        />
      )}
      {data && data.models.length > 0 && (
        <ul className="divide-y divide-border">
          {data.models.map((m) => (
            <li key={m.id} className="py-3 first:pt-0 last:pb-0" data-testid={`installed-${m.id}`}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono text-subheading break-all text-text">{m.id}</span>
                    <Badge>{m.kind}</Badge>
                    {m.state !== 'ready' && <Badge tone={stateBadge(m.state)}>{m.state}</Badge>}
                    {m.external && <Badge title={m.path}>registered</Badge>}
                    {m.labels.map((l) => (
                      <Badge key={l} tone="primary">
                        {l}
                      </Badge>
                    ))}
                  </div>
                  <p className="text-small text-text-muted">
                    {m.display !== m.id ? `${m.display} · ` : ''}
                    {formatBytes(m.size)}
                    {m.prompt_provider ? ` · prompt ${m.prompt_provider}` : ''}
                    {m.mmproj_id ? ` · projector ${m.mmproj_id}` : ''}
                  </p>
                  {m.error && <p className="text-small text-danger">{m.error}</p>}
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  className="px-2"
                  title={`Delete ${m.id}`}
                  aria-label={`Delete ${m.id}`}
                  icon={<Trash2 size={14} aria-hidden />}
                  onClick={() => ask(m)}
                  disabled={del.isPending}
                >
                  Delete
                </Button>
              </div>
              {pending?.model.id === m.id && (
                <ConfirmDelete pending={pending} busy={del.isPending} onConfirm={confirm} onCancel={() => setPending(null)} />
              )}
            </li>
          ))}
        </ul>
      )}
    </ModelCard>
  )
}
