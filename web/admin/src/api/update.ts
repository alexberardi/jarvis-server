import { apiClient } from './client'

export interface UpdateInfo {
  currentVersion: string
  latestVersion: string
  updateAvailable: boolean
  releaseUrl: string
  releaseNotes: string
  publishedAt: string
  /**
   * Whether this box is allowed to check for updates at all.
   *
   * When false the server never contacts GitHub and reports
   * `updateAvailable: false` — which is indistinguishable from "you are on the
   * latest version" unless you look at this flag. The UI must not claim the
   * user is up to date when no check actually happened.
   */
  updatesEnabled: boolean
}

export interface UpgradeStatus {
  inProgress: boolean
  phase?: string
  version?: string
  startedAt?: string
  /** Set when the server-side resume failed; `phase` is then "error". */
  error?: string
}

export async function getUpdateInfo(): Promise<UpdateInfo> {
  const { data } = await apiClient.get<UpdateInfo>('/api/update/check')
  return data
}

export async function forceUpdateCheck(): Promise<UpdateInfo> {
  const { data } = await apiClient.post<UpdateInfo>('/api/update/check')
  return data
}

export async function getUpgradeStatus(): Promise<UpgradeStatus> {
  const { data } = await apiClient.get<UpgradeStatus>('/api/update/status')
  return data
}

/**
 * Turn the box-level update opt-in on or off.
 *
 * Persists to ~/.jarvis/admin.json server-side and takes effect immediately —
 * this is what spares a self-hoster from hand-editing a launchd plist or a
 * compose .env just to receive updates.
 */
export async function setUpdatesEnabled(allowUpdates: boolean): Promise<{ allowUpdates: boolean }> {
  const { data } = await apiClient.post<{ allowUpdates: boolean }>('/api/update/settings', {
    allowUpdates,
  })
  return data
}
