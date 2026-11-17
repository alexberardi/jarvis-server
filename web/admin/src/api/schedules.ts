import { apiClient } from './client'

/**
 * Scheduled errands: the household's one-time and recurring errands, each cancellable (mobile's
 * Schedules screen, the rows "what errands do I have scheduled?" reads). cc checks membership.
 */
const base = (hh: string) => `/api/cc/api/v0/mobile/household/${encodeURIComponent(hh)}/schedules`

export interface Schedule {
  id: string
  /** What the errand will do: the goal each fire re-plans. */
  intent: string
  state: string
  next_fire_at: string | null
  is_recurring: boolean
  /** The recurrence in words ("every day at 8:00", "once"). */
  cadence: string
  /** The next fire in the household's time zone, in words; empty when there is none. */
  next_local: string
  description: string
}

export async function listSchedules(householdId: string): Promise<Schedule[]> {
  const { data } = await apiClient.get<{ schedules: Schedule[] }>(base(householdId))
  return data.schedules ?? []
}

/** cancelSchedule stops one and returns the list as it is now. */
export async function cancelSchedule(householdId: string, id: string): Promise<Schedule[]> {
  const { data } = await apiClient.post<{ cancelled: boolean; schedules: Schedule[] }>(`${base(householdId)}/${encodeURIComponent(id)}/cancel`, {})
  return data.schedules ?? []
}
