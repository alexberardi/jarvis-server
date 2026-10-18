import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, ExternalLink, Loader2, Trash2 } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { confirmationOf, deleteItem, getInboxItem, markRead, sourcesOf } from '@/api/inbox'
import type { InboxAction } from '@/api/inbox'
import { sendNodeAction } from '@/api/chat'
import Markdown from '@/components/chat/Markdown'
import { useAuth } from '@/hooks/useAuth'
import { inboxKeys } from '@/hooks/useInbox'
import { errorMessage } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'
import { cn } from '@/lib/utils'

const actionStyle = (t?: string) =>
  t === 'primary'
    ? 'bg-[var(--color-primary)] text-white hover:opacity-90'
    : t === 'destructive'
      ? 'bg-red-600 text-white hover:opacity-90'
      : 'border border-[var(--color-border)] text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]'

/** InboxItemPage shows one item: its body as markdown, sources, and a confirmation's buttons. */
export default function InboxItemPage() {
  const { id = '' } = useParams()
  const { activeHouseholdId } = useAuth()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const q = useQuery({ queryKey: inboxKeys.item(activeHouseholdId, id), queryFn: () => getInboxItem(id), retry: false })
  const item = q.data
  const [running, setRunning] = useState<string | null>(null)
  const [done, setDone] = useState<string | null>(null)

  // Opening an item reads it. The server's GET already marks it read (legacy parity), so it
  // usually arrives read: the list and the unread badge catch up either way (not the item
  // itself, which would refetch it and run this again).
  useEffect(() => {
    if (!item) return
    const read = item.is_read ? Promise.resolve() : markRead(item.id)
    read
      .then(() => qc.invalidateQueries({ queryKey: inboxKeys.all, predicate: (q) => q.queryKey[2] !== 'item' }))
      .catch(() => {})
  }, [item, qc])

  async function remove() {
    if (!item || !window.confirm(`Delete "${item.title}"?`)) return
    try {
      await deleteItem(item.id)
      await qc.invalidateQueries({ queryKey: inboxKeys.all })
      navigate('/inbox', { replace: true })
    } catch (err) {
      toast.error(errorMessage(err, 'Could not delete it'))
    }
  }

  const confirmation = item ? confirmationOf(item) : null
  async function run(a: InboxAction) {
    if (!confirmation) return
    setRunning(a.button_action)
    try {
      const res = await sendNodeAction(confirmation.nodeId, {
        command_name: confirmation.commandName,
        action_name: a.button_action,
        context: confirmation.context,
      })
      if (res.status === 'timeout') setDone('The device did not answer in time.')
      else if (res.success === false) setDone(res.error || 'Action failed.')
      else setDone(a.button_action.includes('cancel') ? 'Cancelled.' : 'Done!')
    } catch (err) {
      setDone(errorMessage(err, 'Action failed.'))
    } finally {
      setRunning(null)
    }
  }

  if (q.isLoading) return <p className="text-sm text-[var(--color-text-muted)]">Loading...</p>
  if (q.isError || !item)
    return (
      <div className="mx-auto max-w-3xl space-y-3">
        <p role="alert" className="text-sm text-red-500">
          {errorMessage(q.error, 'This item is gone')}
        </p>
        <Link to="/inbox" className="text-sm text-[var(--color-primary)] underline">
          Back to the inbox
        </Link>
      </div>
    )

  const sources = sourcesOf(item)
  const m = item.metadata ?? {}
  const stats = typeof m.elapsed_seconds === 'number' || typeof m.elapsed_seconds === 'string'

  return (
    <article className="mx-auto max-w-3xl space-y-4">
      <div className="flex items-center justify-between gap-2">
        <Link to="/inbox" className="inline-flex items-center gap-1 text-sm text-[var(--color-text-muted)] hover:text-[var(--color-text)]">
          <ArrowLeft size={16} aria-hidden="true" /> Inbox
        </Link>
        <button
          type="button"
          onClick={() => void remove()}
          className="inline-flex items-center gap-1.5 rounded-lg border border-red-500/40 px-3 py-1.5 text-sm text-red-500 hover:bg-red-500/10"
        >
          <Trash2 size={14} aria-hidden="true" /> Delete
        </button>
      </div>
      <header>
        <h1 className="text-xl font-semibold text-[var(--color-text)]">{item.title}</h1>
        <p className="text-xs text-[var(--color-text-muted)]">
          {item.category.replace(/_/g, ' ')} · {formatServerTime(item.created_at)}
        </p>
      </header>

      {confirmation && (
        <div className="flex flex-wrap items-center gap-2">
          {done ? (
            <p role="status" className="text-sm text-[var(--color-text-muted)]">
              {done}
            </p>
          ) : (
            confirmation.actions.map((a) => (
              <button
                key={a.button_action}
                type="button"
                disabled={running !== null}
                onClick={() => void run(a)}
                className={cn('inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-medium disabled:opacity-50', actionStyle(a.button_type))}
              >
                {running === a.button_action && <Loader2 size={14} className="animate-spin" aria-hidden="true" />}
                {a.button_text}
              </button>
            ))
          )}
        </div>
      )}

      <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 text-sm text-[var(--color-text)] sm:p-6">
        <Markdown>{item.body || item.summary || ''}</Markdown>
      </div>

      {sources.length > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold text-[var(--color-text)]">Sources</h2>
          <ol className="space-y-1 text-sm">
            {sources.map((s, i) => (
              <li key={`${s.url}-${i}`}>
                <a href={s.url} target="_blank" rel="noopener noreferrer nofollow" className="inline-flex items-center gap-1 break-all text-[var(--color-primary)] hover:underline">
                  {s.title}
                  <ExternalLink size={12} className="shrink-0" aria-hidden="true" />
                </a>
              </li>
            ))}
          </ol>
        </section>
      )}
      {stats && (
        <p className="text-xs text-[var(--color-text-muted)]">
          Researched in {String(m.elapsed_seconds)}s
          {m.pages_attempted != null && ` · ${String(m.pages_scraped ?? 0)}/${String(m.pages_attempted)} pages`}
        </p>
      )}
    </article>
  )
}
