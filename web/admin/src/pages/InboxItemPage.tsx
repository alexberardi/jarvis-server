import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, ExternalLink, Trash2 } from 'lucide-react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { confirmationOf, deleteItem, getInboxItem, markRead, sourcesOf } from '@/api/inbox'
import type { InboxAction } from '@/api/inbox'
import { sendNodeAction } from '@/api/chat'
import Markdown from '@/components/chat/Markdown'
import { Badge, Button, Card, ErrorState, Skeleton, type ButtonVariant } from '@/components/ui'
import InboxAttachments from '@/components/inbox/InboxAttachments'
import { useAuth } from '@/hooks/useAuth'
import { inboxKeys } from '@/hooks/useInbox'
import { errorMessage, errorStatus } from '@/lib/errors'
import { formatServerTime } from '@/lib/time'

// A confirmation's button_type, as the posting service names it, to a Button variant.
const actionVariant = (t?: string): ButtonVariant => (t === 'primary' ? 'primary' : t === 'destructive' ? 'danger' : 'secondary')

/** InboxItemPage shows one item: its body as markdown, attachments, sources, and a confirmation's buttons. */
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

  if (q.isLoading)
    return (
      <div role="status" aria-label="Loading the item" className="mx-auto max-w-3xl space-y-4">
        <Skeleton className="h-4 w-16" />
        <Skeleton className="h-7 w-3/4" />
        <Skeleton className="h-40 w-full rounded-card" />
      </div>
    )
  if (q.isError || !item)
    return (
      <div className="mx-auto max-w-3xl space-y-4">
        <BackLink />
        <Card>
          <ErrorState
            title="Couldn't open this item"
            message={errorMessage(q.error, 'It may have been deleted.')}
            onRetry={errorStatus(q.error) === 404 ? undefined : () => q.refetch()}
          />
        </Card>
      </div>
    )

  const sources = sourcesOf(item)
  const m = item.metadata ?? {}
  const stats = typeof m.elapsed_seconds === 'number' || typeof m.elapsed_seconds === 'string'

  return (
    <article className="mx-auto max-w-3xl space-y-5">
      <div className="flex items-center justify-between gap-2">
        <BackLink />
        <Button size="sm" variant="danger" icon={<Trash2 size={16} aria-hidden />} onClick={() => void remove()}>
          Delete
        </Button>
      </div>
      <header className="space-y-2">
        <h1 className="text-title text-text [overflow-wrap:anywhere]">{item.title}</h1>
        <p className="flex flex-wrap items-center gap-2 text-small text-text-muted">
          <Badge>{item.category.replace(/_/g, ' ')}</Badge>
          <span>{formatServerTime(item.created_at)}</span>
        </p>
      </header>

      {confirmation && (
        <Card as="section" aria-labelledby="inbox-answer" className="space-y-3 border-warning/40 bg-warning-soft">
          <h2 id="inbox-answer" className="text-subheading text-text">
            Jarvis needs your answer
          </h2>
          {done ? (
            <p role="status" className="text-body text-text">
              {done}
            </p>
          ) : (
            <div className="flex flex-wrap items-center gap-2">
              {confirmation.actions.map((a) => (
                <Button
                  key={a.button_action}
                  variant={actionVariant(a.button_type)}
                  loading={running === a.button_action}
                  disabled={running !== null}
                  onClick={() => void run(a)}
                >
                  {a.button_text}
                </Button>
              ))}
            </div>
          )}
        </Card>
      )}

      <Card className="text-body text-text md:p-6">
        <Markdown>{item.body || item.summary || ''}</Markdown>
      </Card>

      <InboxAttachments itemId={item.id} attachments={item.attachments ?? []} />

      {sources.length > 0 && (
        <section aria-labelledby="inbox-sources" className="space-y-2">
          <h2 id="inbox-sources" className="text-subheading text-text">
            Sources
          </h2>
          <ol className="space-y-1.5 text-small">
            {sources.map((s, i) => (
              <li key={`${s.url}-${i}`}>
                <a
                  href={s.url}
                  target="_blank"
                  rel="noopener noreferrer nofollow"
                  className="inline-flex items-center gap-1 break-all text-primary underline-offset-2 hover:underline"
                >
                  {s.title}
                  <ExternalLink size={12} className="shrink-0" aria-hidden />
                </a>
              </li>
            ))}
          </ol>
        </section>
      )}
      {stats && (
        <p className="text-caption text-text-muted">
          Researched in {String(m.elapsed_seconds)}s
          {m.pages_attempted != null && ` · ${String(m.pages_scraped ?? 0)}/${String(m.pages_attempted)} pages`}
        </p>
      )}
    </article>
  )
}

function BackLink() {
  return (
    <Link
      to="/inbox"
      className="inline-flex min-h-10 items-center gap-1 rounded-control text-small text-text-muted transition-colors duration-(--duration-hover) hover:text-text"
    >
      <ArrowLeft size={16} aria-hidden /> Inbox
    </Link>
  )
}
