import { apiClient } from './client'

/**
 * Call details (mobile's Call details): what Jarvis may tell a business when it calls for you,
 * per user, through the gateway's member route. The server stores the whole list on each save.
 */
export type CallTier = 'state' | 'if_asked'

export interface CallField {
  /** Set for a well-known detail ("full_name"); a custom one has none and the server derives it. */
  key?: string
  label: string
  value: string
  category: string
  tier: CallTier
}

export interface CallCatalog {
  well_known: { key: string; label: string; category: string; tier: CallTier }[]
  categories: { value: string; label: string }[]
  tiers: { value: CallTier; label: string }[]
}

export interface CallContext {
  fields: CallField[]
  catalog: CallCatalog
}

const url = '/api/cc/api/v0/mobile/call-context'

export async function getCallContext(): Promise<CallContext> {
  return (await apiClient.get<CallContext>(url)).data
}

export async function putCallContext(fields: CallField[]): Promise<CallContext> {
  return (await apiClient.put<CallContext>(url, { fields })).data
}
