import { apiClient } from './client'

/**
 * A household's own settings: cc's household-controllable allowlist
 * (internal/modules/cc/household_settings.go) through the gateway's member routes. Any member
 * reads them; cc lets only a household admin (or owner) write.
 */
const base = (householdId: string) =>
  `/api/cc/api/v0/mobile/household/${encodeURIComponent(householdId)}/settings`

/** getHouseholdSettings is every allowlisted key's effective value for the household. */
export async function getHouseholdSettings(householdId: string): Promise<Record<string, unknown>> {
  const { data } = await apiClient.get<{ settings: Record<string, unknown> }>(base(householdId))
  return data.settings ?? {}
}

export async function putHouseholdSetting(householdId: string, key: string, value: unknown): Promise<void> {
  await apiClient.put(`${base(householdId)}/${encodeURIComponent(key)}`, { value })
}
