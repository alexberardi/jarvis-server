import { useState } from 'react'
import { Link } from 'react-router-dom'
import { CheckCheck, Inbox, ListChecks, Trash2, X } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { bulkDelete, bulkRead, confirmationOf } from '@/api/inbox'
import type { InboxItem } from '@/api/inbox'
import { Badge, Button, Card, EmptyState, ErrorState, List, PageHeader, SkeletonList } from '@/components/ui'
import { inboxKeys, useInboxList } from '@/hooks/useInbox'
import { errorMessage } from '@/lib/errors'
import { timeAgo } from '@/lib/time'
import { cn } from '@/lib/utils'

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

  const actions =
    items.length === 0 ? null : selecting ? (
      <>
        <span className="text-small text-text-muted">{selected.size} selected</span>
        <Button
          size="sm"
          onClick={() => setSelected(selected.size === items.length ? new Set() : new Set(items.map((i) => i.id)))}
        >
          {selected.size === items.length ? 'None' : 'All'}
        </Button>
        <Button
          size="sm"
          icon={<CheckCheck size={16} aria-hidden />}
          disabled={!selected.size || busy}
          onClick={() => void bulk('read')}
        >
          Mark read
        </Button>
        <Button
          size="sm"
          variant="danger"
          icon={<Trash2 size={16} aria-hidden />}
          disabled={!selected.size || busy}
          onClick={() => void bulk('delete')}
        >
          Delete
        </Button>
        <Button
          size="icon"
          variant="ghost"
          aria-label="Cancel selection"
          onClick={() => {
            setSelecting(false)
            setSelected(new Set())
          }}
        >
          <X size={18} aria-hidden />
        </Button>
      </>
    ) : (
      <Button size="sm" icon={<ListChecks size={16} aria-hidden />} onClick={() => setSelecting(true)}>
        Select
      </Button>
    )

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title="Inbox" actions={actions} />

      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState
            title="Couldn't load your inbox"
            message={errorMessage(list.error, 'Check your connection and try again.')}
            onRetry={() => list.refetch()}
          />
        ) : list.isLoading ? (
          <SkeletonList rows={3} label="Loading your inbox" />
        ) : items.length === 0 ? (
          <EmptyState
            icon={<Inbox size={20} />}
            title="Nothing here yet"
            description="Research results, questions that need your answer, and notices from Jarvis land here."
          />
        ) : (
          <List label="Inbox items">
            {items.map((it) => (
              <li key={it.id}>
                {selecting ? (
                  <label className={cn('flex cursor-pointer gap-3 px-4 py-3', selected.has(it.id) && 'bg-surface-alt')}>
                    <input
                      type="checkbox"
                      className="mt-1 size-4 shrink-0 accent-primary"
                      checked={selected.has(it.id)}
                      onChange={() => toggle(it.id)}
                      aria-label={`Select ${it.title}`}
                    />
                    <Row item={it} />
                  </label>
                ) : (
                  <Link
                    to={`/inbox/${encodeURIComponent(it.id)}`}
                    className="flex gap-3 px-4 py-3 transition-colors duration-(--duration-hover) hover:bg-surface-alt"
                  >
                    <Row item={it} />
                  </Link>
                )}
              </li>
            ))}
          </List>
        )}
      </Card>
    </div>
  )
}

/**
 * Row is one item: unread dot, title and time, the summary, and its badges. A confirmation that
 * is still unread says it needs an answer, so it doesn't read like an FYI.
 */
function Row({ item }: { item: InboxItem }) {
  const asks = !item.is_read && confirmationOf(item) !== null
  return (
    <>
      <span
        aria-label={item.is_read ? undefined : 'Unread'}
        className={cn('mt-2 size-2 shrink-0 rounded-pill', item.is_read ? 'bg-transparent' : 'bg-primary')}
      />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline justify-between gap-3">
          <span className={cn('truncate text-body text-text', !item.is_read && 'font-semibold')}>{item.title}</span>
          <span className="shrink-0 text-caption text-text-muted">{timeAgo(item.created_at)}</span>
        </span>
        {item.summary && <span className="line-clamp-2 block text-small text-text-muted">{item.summary}</span>}
        <span className="mt-1.5 flex flex-wrap gap-1.5">
          {asks && (
            <Badge tone="warning" dot>
              Needs your answer
            </Badge>
          )}
          <Badge>{item.category.replace(/_/g, ' ')}</Badge>
        </span>
      </span>
    </>
  )
}
