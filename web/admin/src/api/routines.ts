import { apiClient } from './client'

/**
 * Routines (mobile's Routines tab): the household's routines in cc. Nodes pull them and run
 * them on a trigger phrase or a schedule; run-now fires one on a node and waits for its report.
 */
const base = (hh: string) => `/api/cc/api/v0/households/${encodeURIComponent(hh)}/routines`

export interface RoutineStepArg {
  key: string
  value: string
}

export interface RoutineStep {
  command: string
  args: RoutineStepArg[]
  label: string
}

export type ResponseLength = 'short' | 'medium' | 'long'
export type RoutineMode = 'simple' | 'advanced'

/** A routine's schedule; null on a routine means voice and run-now only. */
export interface RoutineSchedule {
  type: 'interval' | 'cron'
  cron?: string | null
  interval_seconds?: number | null
  timezone: string
  /** The node a scheduled run fires on (cc falls back to the household's primary node). */
  target_node_id?: string | null
  enabled: boolean
  last_fired_at?: string | null
}

export interface Routine {
  id: string
  mode?: RoutineMode
  utterance?: string
  name: string
  trigger_phrases: string[]
  steps: RoutineStep[]
  response_instruction: string
  response_length: ResponseLength
  schedule: RoutineSchedule | null
  enabled?: boolean
}

export type RoutineWrite = Omit<Routine, 'id' | 'enabled'>

export interface RunNowResult {
  success: boolean
  status: 'success' | 'partial' | 'failed' | 'timeout'
  message: string | null
}

export async function listRoutines(hh: string): Promise<Routine[]> {
  const { data } = await apiClient.get<{ routines: Routine[] }>(base(hh))
  return data.routines ?? []
}

export async function getRoutine(hh: string, id: string): Promise<Routine> {
  const { data } = await apiClient.get<Routine>(`${base(hh)}/${encodeURIComponent(id)}`)
  return data
}

export async function createRoutine(hh: string, body: RoutineWrite): Promise<Routine> {
  const { data } = await apiClient.post<Routine>(base(hh), body)
  return data
}

export async function updateRoutine(hh: string, id: string, body: RoutineWrite): Promise<Routine> {
  const { data } = await apiClient.patch<Routine>(`${base(hh)}/${encodeURIComponent(id)}`, body)
  return data
}

export async function deleteRoutine(hh: string, id: string): Promise<void> {
  await apiClient.delete(`${base(hh)}/${encodeURIComponent(id)}`)
}

/** cc holds run-now open until the node reports, up to 60 s, then answers status "timeout". */
export const RUN_NOW_TIMEOUT_MS = 90_000

export async function runRoutineNow(hh: string, id: string, nodeId: string | null): Promise<RunNowResult> {
  const { data } = await apiClient.post<RunNowResult>(`${base(hh)}/${encodeURIComponent(id)}/run-now`, { node_id: nodeId }, { timeout: RUN_NOW_TIMEOUT_MS })
  return data
}
