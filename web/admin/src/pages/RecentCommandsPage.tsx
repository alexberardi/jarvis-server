import { ChevronRight, History, MessageSquareText, Wrench } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { callSummary, listRecentTranscripts, toolCalls, type Transcript } from '@/api/transcripts'
import { Card, EmptyState, ErrorState, PageHeader, SkeletonList } from '@/components/ui'
import { errorMessage } from '@/lib/errors'
import { formatServerTime, timeAgo } from '@/lib/time'

/**
 * RecentCommandsPage is your last 50 requests to Jarvis (mobile's Recent Commands), by voice or
 * in chat: what you said and the command a node ran (or the reply); open one for every call's
 * arguments and the reply. Mobile's thumbs ratings are left out: nothing reads them since the fine-tune pipeline
 * was cut (docs/decisions/2026-11-17-web-recent-commands-no-ratings.md).
 */
export default function RecentCommandsPage() {
  const list = useQuery({ queryKey: ['transcripts', 'recent'], queryFn: () => listRecentTranscripts(50), retry: false })
  const rows = list.data ?? []

  return (
    <div className="mx-auto max-w-3xl">
      <PageHeader title="Recent commands" description="What you asked Jarvis lately, and what it did." />
      <Card flush className="overflow-hidden">
        {list.isError ? (
          <ErrorState title="Couldn't load recent commands" message={errorMessage(list.error, 'Check your connection and try again.')} onRetry={() => list.refetch()} />
        ) : list.isLoading ? (
          <SkeletonList rows={4} label="Loading recent commands" />
        ) : rows.length === 0 ? (
          <EmptyState icon={<History size={20} />} title="Nothing yet" description="Ask Jarvis something, by voice or in chat, and it shows up here." />
        ) : (
          <ul aria-label="Recent commands" className="divide-y divide-border">
            {rows.map((t) => (
              <li key={t.id}>
                <Row t={t} />
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  )
}

function Row({ t }: { t: Transcript }) {
  const calls = toolCalls(t)
  // cc records only the commands a node ran; without one, the reply says what happened.
  const second = calls.length ? `→ ${callSummary(calls[0])}${calls.length > 1 ? ` +${calls.length - 1}` : ''}` : (t.assistant_message ?? '')
  return (
    <details className="group">
      <summary className="flex cursor-pointer list-none items-start gap-3 px-4 py-3 transition-colors duration-(--duration-hover) hover:bg-surface-alt [&::-webkit-details-marker]:hidden">
        <ChevronRight size={16} aria-hidden className="mt-1 shrink-0 text-text-muted transition-transform duration-(--duration-hover) group-open:rotate-90 motion-reduce:transition-none" />
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline justify-between gap-3">
            <span className="text-body text-pretty text-text">“{t.user_message}”</span>
            <time dateTime={t.created_at} title={formatServerTime(t.created_at)} className="shrink-0 text-caption text-text-muted">
              {timeAgo(t.created_at)}
            </time>
          </span>
          {second && <span className={`mt-0.5 block truncate text-small text-text-muted ${calls.length ? 'font-mono' : ''}`}>{second}</span>}
        </span>
      </summary>
      <div className="space-y-3 px-4 pb-4 pl-11">
        {calls.map((c, i) => (
          <div key={i}>
            <p className="flex items-center gap-1.5 text-small font-medium text-text">
              <Wrench size={14} aria-hidden className="text-text-muted" /> {c.name}
            </p>
            {Object.keys(c.arguments).length > 0 && (
              <dl className="mt-1 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-0.5 text-small">
                {Object.entries(c.arguments).map(([k, v]) => (
                  <div key={k} className="contents">
                    <dt className="font-mono text-text-muted">{k}</dt>
                    <dd className="min-w-0 font-mono break-words text-text">{typeof v === 'string' ? v : JSON.stringify(v)}</dd>
                  </div>
                ))}
              </dl>
            )}
          </div>
        ))}
        {t.assistant_message && (
          <div>
            <p className="flex items-center gap-1.5 text-small font-medium text-text">
              <MessageSquareText size={14} aria-hidden className="text-text-muted" /> Jarvis said
            </p>
            <p className="mt-1 text-small text-pretty whitespace-pre-line text-text">{t.assistant_message}</p>
          </div>
        )}
      </div>
    </details>
  )
}
