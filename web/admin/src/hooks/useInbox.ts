import { useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { listInbox, unreadCount } from '@/api/inbox'
import { useAuth } from '@/hooks/useAuth'

/** The unread badge re-polls every 10 s while the tab is visible, so new items toast promptly. */
export const UNREAD_POLL_MS = 10_000

/** Keys include the household: the inbox follows the active household's token. */
export const inboxKeys = {
  list: (hh: string) => ['inbox', hh, 'list'] as const,
  item: (hh: string, id: string) => ['inbox', hh, 'item', id] as const,
  unread: (hh: string) => ['inbox', hh, 'unread'] as const,
  all: ['inbox'] as const,
}

export function useInboxList() {
  const { activeHouseholdId } = useAuth()
  return useQuery({ queryKey: inboxKeys.list(activeHouseholdId), queryFn: () => listInbox({ limit: 100 }), retry: false })
}

export function useUnreadCount(enabled = true) {
  const { activeHouseholdId } = useAuth()
  return useQuery({
    queryKey: inboxKeys.unread(activeHouseholdId),
    queryFn: unreadCount,
    refetchInterval: UNREAD_POLL_MS,
    retry: false,
    enabled,
  })
}

/**
 * useInboxAlerts keeps the unread count live and toasts what just arrived (docs/webapp/03 step 5,
 * docs/decisions/2026-11-15-web-inbox-alerts-badge-and-toast.md). The count polls while the tab
 * is visible (react-query pauses it in the background and refetches on focus); when it rises,
 * the newest unread items are fetched and each gets a toast with Open. The first count after
 * sign-in or a household switch is the baseline, not news.
 */
export function useInboxAlerts(enabled = true) {
  const { activeHouseholdId } = useAuth()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const unread = useUnreadCount(enabled)
  const seen = useRef<{ hh: string; count: number } | null>(null)
  const count = unread.data

  useEffect(() => {
    if (count === undefined) return
    const prev = seen.current
    seen.current = { hh: activeHouseholdId, count }
    if (!prev || prev.hh !== activeHouseholdId || count <= prev.count) return
    void qc.invalidateQueries({ queryKey: inboxKeys.list(activeHouseholdId) })
    // shortcut: at most 3 toasts per rise; the badge carries the rest.
    listInbox({ limit: Math.min(count - prev.count, 3), is_read: false })
      .then((items) => {
        for (const it of items)
          toast(it.title, {
            description: it.summary || undefined,
            action: { label: 'Open', onClick: () => navigate(`/inbox/${encodeURIComponent(it.id)}`) },
          })
      })
      .catch(() => {}) // the badge already shows the count; a missed toast isn't worth an error
  }, [count, activeHouseholdId, qc, navigate])

  return unread
}
