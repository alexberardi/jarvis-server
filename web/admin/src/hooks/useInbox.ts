import { useQuery } from '@tanstack/react-query'
import { listInbox, unreadCount } from '@/api/inbox'
import { useAuth } from '@/hooks/useAuth'

/** The unread badge re-polls every 30 s (jarvis-web's interval). */
export const UNREAD_POLL_MS = 30_000

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
