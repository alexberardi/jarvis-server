import { apiClient } from './client'

/**
 * A household's own settings: cc's household-controllable allowlist
 * (internal/modules/cc/household_settings.go) through the gateway's member routes. Any member
 * reads them; cc lets only a household admin (or owner) write.
 */
const root = (householdId: string) => `/api/cc/api/v0/mobile/household/${encodeURIComponent(householdId)}`
const base = (householdId: string) => `${root(householdId)}/settings`

/** getHouseholdSettings is every allowlisted key's effective value for the household. */
export async function getHouseholdSettings(householdId: string): Promise<Record<string, unknown>> {
  const { data } = await apiClient.get<{ settings: Record<string, unknown> }>(base(householdId))
  return data.settings ?? {}
}

export async function putHouseholdSetting(householdId: string, key: string, value: unknown): Promise<void> {
  await apiClient.put(`${base(householdId)}/${encodeURIComponent(key)}`, { value })
}

/**
 * A household's people (mobile's Household settings), through auth's routes on the gateway.
 * Auth checks the caller's role per route: any member lists; power users invite and revoke;
 * admins rename, change roles and remove; leaving refuses the last admin and an only household.
 */
const hh = (id: string) => `/api/households/${encodeURIComponent(id)}`

export type HouseholdRole = 'admin' | 'power_user' | 'member'

export const ROLE_LABEL: Record<string, string> = { admin: 'Admin', power_user: 'Power user', member: 'Member' }

export interface Member {
  user_id: number
  username: string
  display_name: string | null
  email: string
  role: HouseholdRole
  created_at: string
}

export interface Invite {
  id: number
  code: string
  default_role: HouseholdRole
  max_uses: number | null
  use_count: number
  expires_at: string | null
  revoked: boolean
  created_at: string
}

export async function renameHousehold(id: string, name: string): Promise<void> {
  await apiClient.patch(hh(id), { name })
}

export async function listMembers(id: string): Promise<Member[]> {
  return (await apiClient.get<Member[]>(`${hh(id)}/members`)).data
}

export async function setMemberRole(id: string, userId: number, role: HouseholdRole): Promise<void> {
  await apiClient.patch(`${hh(id)}/members/${userId}`, { role })
}

export async function removeMember(id: string, userId: number): Promise<void> {
  await apiClient.delete(`${hh(id)}/members/${userId}`)
}

export async function leaveHousehold(id: string): Promise<void> {
  await apiClient.post(`${hh(id)}/leave`, {})
}

export async function listInvites(id: string): Promise<Invite[]> {
  return (await apiClient.get<Invite[]>(`${hh(id)}/invites`)).data
}

export async function createInvite(id: string, body: { default_role: HouseholdRole; expires_in_days: number; max_uses: number | null }): Promise<Invite> {
  return (await apiClient.post<Invite>(`${hh(id)}/invites`, body)).data
}

export async function revokeInvite(id: string, inviteId: number): Promise<void> {
  await apiClient.delete(`${hh(id)}/invites/${inviteId}`)
}

/** joinHousehold joins another household with an invite code, as the caller, in the invite's role. */
export async function joinHousehold(code: string): Promise<{ household_id: string; household_name: string; role: HouseholdRole }> {
  return (await apiClient.post(`/api/households/join`, { invite_code: code })).data
}

/** The persona starters (mobile's chips): load one into the editor, tweak, save. */
export interface PersonaPresets {
  presets: { id: string; label: string; text: string }[]
  default_preset_id: string
  default_text: string
  max_chars: number
}

export async function getPersonaPresets(id: string): Promise<PersonaPresets> {
  return (await apiClient.get<PersonaPresets>(`${root(id)}/persona/presets`)).data
}

/** The zone in effect: the setting, else the nodes' (source "node"), else UTC ("default"). */
export interface HouseholdTimezone {
  timezone: string
  source: 'setting' | 'node' | 'default'
  /** What the most recently seen node reported ("" = none). */
  node_timezone: string
}

export async function getHouseholdTimezone(id: string): Promise<HouseholdTimezone> {
  return (await apiClient.get<HouseholdTimezone>(`${root(id)}/timezone`)).data
}
