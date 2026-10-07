import { isAxiosError } from 'axios'
import { apiClient } from './client'
import type { SystemInfo } from '@/types/system'
import { markFeatureMissing } from '@/lib/features'

export async function getSystemInfo(): Promise<SystemInfo> {
  const { data } = await apiClient.get<SystemInfo>('/api/system/info')
  return data
}

/**
 * The result of asking jarvisd to restart (AD8, `POST /api/system/restart`):
 * - restarting: a supervisor (systemd, launchd, a Windows service) brings it back
 * - manual: no supervisor; the operator runs `command` (409 `{detail, command}`)
 * - unsupported: this jarvisd has no restart route (404)
 */
export type RestartResult =
  | { kind: 'restarting' }
  | { kind: 'manual'; detail: string; command?: string }
  | { kind: 'unsupported' }

export async function requestRestart(): Promise<RestartResult> {
  try {
    await apiClient.post('/api/system/restart')
    return { kind: 'restarting' }
  } catch (err) {
    if (isAxiosError(err)) {
      const status = err.response?.status
      const body = (err.response?.data ?? {}) as { detail?: string; command?: string }
      if (status === 404) {
        markFeatureMissing('restart')
        return { kind: 'unsupported' }
      }
      if (status === 409) {
        return { kind: 'manual', detail: body.detail ?? 'jarvisd is not running under a supervisor', command: body.command }
      }
    }
    throw err
  }
}

/**
 * waitForRestart polls /api/system/info until a process started after `before` answers
 * (or until `timeoutMs`). Returns the new info, or null on timeout.
 */
export async function waitForRestart(before: string | undefined, timeoutMs = 120_000, intervalMs = 1500): Promise<SystemInfo | null> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, intervalMs))
    try {
      const info = await getSystemInfo()
      if (!before || (info.started_at && info.started_at !== before)) return info
    } catch {
      // Down while it restarts.
    }
  }
  return null
}
