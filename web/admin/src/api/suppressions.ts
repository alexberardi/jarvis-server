import { apiClient } from './client'

/**
 * Suggestions the user told Jarvis never to make again ("Never suggest this" on a proposal
 * card), per household (docs/webapp/03 step 4). Same routes as the mobile app's
 * SuppressedSuggestionsScreen; cc returns only the caller's own rows.
 */
const BASE = '/api/cc/api/v0/mobile/proposal-suppressions'

export interface Suppression {
  id: string
  /** The command whose suggestions are blocked, e.g. "send_message". */
  command: string
  source_key: string | null
  /** What was blocked, in words; null for older rows. */
  descriptor: string | null
  created_at: string
}

export async function listSuppressions(householdId: string): Promise<Suppression[]> {
  const { data } = await apiClient.get<{ suppressions: Suppression[] }>(BASE, { params: { household_id: householdId } })
  return data.suppressions ?? []
}

export async function deleteSuppression(householdId: string, id: string): Promise<void> {
  await apiClient.delete(`${BASE}/${encodeURIComponent(id)}`, { params: { household_id: householdId } })
}
