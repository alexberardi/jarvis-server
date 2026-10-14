import { useState } from 'react'
import { Link } from 'react-router-dom'
import { CheckCheck, Inbox, ListChecks, Trash2, X } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { bulkDelete, bulkRead } from '@/api/inbox'
import { inboxKeys, useInboxList } from '@/hooks/useInbox'
import { errorMessage } from '@/lib/errors'
import { timeAgo } from '@/lib/time'
import { cn } from '@/lib/utils'

const button =
  'inline-flex items-center gap-1.5 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:opacity-50'

/** InboxPage lists what Jarvis sent back (webapp 01 P5): research results, confirmations, notices. */
export default function InboxPage() {
  const qc = useQueryClient()
  const list = useInboxList()
  const items = list.data ?? []
  const [selecting, setSelecting] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)

  const toggle = (id: string) =>
    setSelected((cur) => {
      const next = new Set(cur)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  async function bulk(kind: 'read' | 'delete') {
    const ids = [...selected]
    setBusy(true)
    try {
      await (kind === 'read' ? bulkRead(ids) : bulkDelete(ids))
      setSelected(new Set())
      setSelecting(false)
      await qc.invalidateQueries({ queryKey: inboxKeys.all })
    } catch (err) {
      toast.error(errorMessage(err, kind === 'read' ? 'Could not mark them read' : 'Could not delete them'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto max-w-3xl space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-xl font-semibold text-[var(--color-text)]">Inbox</h1>
        {items.length > 0 &&
          (selecting ? (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm text-[var(--color-text-muted)]">{selected.size} selected</span>
              <button
                type="button"
                className={button}
                onClick={() => setSelected(selected.size === items.length ? new Set() : new Set(items.map((i) => i.id)))}
              >
                {selected.size === items.length ? 'None' : 'All'}
              </button>
              <button type="button" className={button} disabled={!selected.size || busy} onClick={() => void bulk('read')}>
                <CheckCheck size={14} aria-hidden="true" /> Mark read
              </button>
              <button
                type="button"
                className={cn(button, 'text-red-500')}
                disabled={!selected.size || busy}
                onClick={() => void bulk('delete')}
              >
                <Trash2 size={14} aria-hidden="true" /> Delete
              </button>
              <button
                type="button"
                aria-label="Cancel selection"
                className={button}
                onClick={() => {
                  setSelecting(false)
                  setSelected(new Set())
                }}
              >
                <X size={14} />
              </button>
            </div>
          ) : (
            <button type="button" className={button} onClick={() => setSelecting(true)}>
              <ListChecks size={14} aria-hidden="true" /> Select
            </button>
          ))}
      </div>

      {list.isError ? (
        <p role="alert" className="text-sm text-red-500">
          {errorMessage(list.error, 'Could not load the inbox')}
        </p>
      ) : list.isLoading ? (
        <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
      ) : items.length === 0 ? (
        <div className="flex flex-col items-center rounded-xl border border-dashed border-[var(--color-border)] p-10 text-center">
          <Inbox size={32} className="mb-2 text-[var(--color-text-muted)]" aria-hidden="true" />
          <p className="text-sm text-[var(--color-text-muted)]">
            Nothing here yet. Research results and confirmations from Jarvis land here.
          </p>
        </div>
      ) : (
        <ul className="divide-y divide-[var(--color-border)] overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
          {items.map((it) => {
            const inner = (
              <>
                <span
                  aria-label={it.is_read ? undefined : 'Unread'}
                  className={cn('mt-1.5 h-2 w-2 shrink-0 rounded-full', it.is_read ? 'bg-transparent' : 'bg-[var(--color-primary)]')}
                />
                <span className="min-w-0 flex-1">
                  <span className="flex items-baseline justify-between gap-2">
                    <span className={cn('truncate text-sm text-[var(--color-text)]', !it.is_read && 'font-semibold')}>{it.title}</span>
                    <span className="shrink-0 text-xs text-[var(--color-text-muted)]">{timeAgo(it.created_at)}</span>
                  </span>
                  {it.summary && <span className="line-clamp-2 block text-sm text-[var(--color-text-muted)]">{it.summary}</span>}
                  <span className="mt-1 inline-block rounded-full bg-[var(--color-surface-alt)] px-2 py-0.5 text-xs text-[var(--color-text-muted)]">
                    {it.category.replace(/_/g, ' ')}
                  </span>
                </span>
              </>
            )
            return (
              <li key={it.id}>
                {selecting ? (
                  <label className={cn('flex cursor-pointer gap-3 p-3', selected.has(it.id) && 'bg-[var(--color-surface-alt)]')}>
                    <input type="checkbox" checked={selected.has(it.id)} onChange={() => toggle(it.id)} aria-label={`Select ${it.title}`} />
                    {inner}
                  </label>
                ) : (
                  <Link to={`/inbox/${encodeURIComponent(it.id)}`} className="flex gap-3 p-3 hover:bg-[var(--color-surface-alt)]">
                    {inner}
                  </Link>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
