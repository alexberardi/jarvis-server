import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getHouseholdSettings } from '@/api/household'
import { parseStores, STORES_KEY, type PantryStore } from '@/api/store'
import { useAuth } from '@/hooks/useAuth'

const pickKey = (hh: string) => `jarvis-admin:pantry-store:${hh}`

function readPick(hh: string): string | null {
  try {
    return localStorage.getItem(pickKey(hh))
  } catch {
    return null
  }
}

/**
 * usePantryStore is the active household's Pantry stores and the one this browser picked
 * (decision 2026-11-11-several-pantries: the pick is per device, never stored on the server).
 * storeId is set only when there is a choice, so with one store requests stay as they were.
 * householdId defaults to the active household (a node's own household on its test page).
 */
export function usePantryStore(householdId?: string): {
  stores: PantryStore[]
  store: PantryStore | undefined
  storeId: string | undefined
  /** ready: the list has loaded (or failed, leaving cc's default), so storeId is final. */
  ready: boolean
  pick: (id: string) => void
} {
  const { activeHouseholdId } = useAuth()
  const hh = householdId ?? activeHouseholdId
  const settings = useQuery({
    queryKey: ['household-settings', hh],
    queryFn: () => getHouseholdSettings(hh),
    enabled: !!hh,
  })
  const stores = parseStores(settings.data?.[STORES_KEY])
  const [picks, setPicks] = useState<Record<string, string | null>>({})
  const picked = hh in picks ? picks[hh] : readPick(hh)
  // A pick of a store that was since removed falls back to the default.
  const store = stores.find((s) => s.id === picked) ?? stores[0]
  return {
    stores,
    store,
    storeId: stores.length > 1 ? store?.id : undefined,
    ready: !settings.isLoading,
    pick: (id) => {
      try {
        localStorage.setItem(pickKey(hh), id)
      } catch {
        // private mode: the pick lasts until the page reloads
      }
      setPicks((p) => ({ ...p, [hh]: id }))
    },
  }
}
