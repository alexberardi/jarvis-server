import { isAxiosError } from 'axios'
import { apiClient } from './client'
import { markFeatureMissing } from '@/lib/features'

/**
 * GET /api/update and friends (AD5, A4 "As built" #9). Every route answers this one shape.
 *
 * Honesty rule (I1): `update_available: false` does NOT mean "up to date". It is also false when
 * checks are off (no request was made), when the check failed, and for a dev build. Only
 * `up_to_date: true` means a real, comparable check found nothing newer. Use `updateStatus`.
 */
export interface UpdateInfo {
  updates_enabled: boolean
  /** A check actually ran and succeeded. */
  checked: boolean
  /** Why there is no verdict (checks off, a failed check, a dev build). */
  reason: string | null
  checked_at: string | null
  current_version: string
  latest_version: string | null
  update_available: boolean
  up_to_date: boolean
  prerelease: boolean
  release_url: string | null
  release_notes: string | null
  published_at: string | null
  /** e.g. "linux-amd64". */
  platform: string
  asset: { name: string; url: string; size: number } | null
  checksums_url: string | null
  install_command: string | null
  install_hint: string
}

export type UpdateStatus = 'disabled' | 'unchecked' | 'available' | 'up_to_date' | 'unknown'

/** updateStatus is the one place the page decides what it may claim (I1). */
export function updateStatus(u: UpdateInfo): UpdateStatus {
  if (!u.updates_enabled) return 'disabled'
  if (!u.checked) return 'unchecked'
  if (u.update_available) return 'available'
  if (u.up_to_date) return 'up_to_date'
  return 'unknown'
}

/** getUpdate returns the cached verdict (at most an hour old); it never checks when off. */
export async function getUpdate(): Promise<UpdateInfo> {
  const { data } = await apiClient.get<UpdateInfo>('/api/update')
  return data
}

/** checkNow forces a check (still none when updates are off). */
export async function checkNow(): Promise<UpdateInfo> {
  const { data } = await apiClient.post<UpdateInfo>('/api/update/check')
  return data
}

/** setUpdatesEnabled stores the opt-in (admin `updates.enabled`); it never checks by itself. */
export async function setUpdatesEnabled(enabled: boolean): Promise<UpdateInfo> {
  const { data } = await apiClient.put<UpdateInfo>('/api/update/settings', { enabled })
  return data
}

/**
 * The result of `POST /api/update/apply` (AD5 signed self-update):
 * - started: jarvisd downloads, verifies, swaps and restarts itself
 * - manual: it can't here (unsupervised, or a platform it can't swap on); `command` if known
 * - unsupported: this jarvisd has no apply route (404)
 */
export type ApplyResult =
  | { kind: 'started'; version?: string }
  | { kind: 'manual'; detail: string; command?: string }
  | { kind: 'unsupported' }

export async function applyUpdate(version?: string | null): Promise<ApplyResult> {
  try {
    const { data } = await apiClient.post<{ version?: string }>('/api/update/apply', version ? { version } : {})
    return { kind: 'started', version: data?.version ?? version ?? undefined }
  } catch (err) {
    if (isAxiosError(err)) {
      const status = err.response?.status
      const body = (err.response?.data ?? {}) as { detail?: string; command?: string }
      if (status === 404) {
        markFeatureMissing('update-apply')
        return { kind: 'unsupported' }
      }
      if (status === 409) return { kind: 'manual', detail: body.detail ?? 'jarvisd cannot update itself here', command: body.command }
    }
    throw err
  }
}
