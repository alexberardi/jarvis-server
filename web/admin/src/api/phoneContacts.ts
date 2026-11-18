import { isAxiosError } from 'axios'
import { apiClient } from './client'

/**
 * The phonebook (mobile's Phonebook screens): businesses Jarvis may call for the household,
 * added by hand, saved from a call or found by search.
 */
const base = (hh: string) => `/api/cc/api/v0/mobile/household/${encodeURIComponent(hh)}/phone-contacts`

export interface PhoneContact {
  id: string
  name: string
  number: string
  address: string | null
  source: 'manual' | 'call' | 'web' | string
  line_type: string | null
  /** Jarvis refuses to call it. */
  do_not_call: boolean
  notes: string | null
  created_at: string
}

export interface PhoneContactWrite {
  name: string
  number: string
  address: string
  notes: string
  do_not_call?: boolean
}

export async function listPhoneContacts(hh: string): Promise<PhoneContact[]> {
  const { data } = await apiClient.get<{ contacts: PhoneContact[] }>(base(hh))
  return data.contacts ?? []
}

export async function createPhoneContact(hh: string, body: PhoneContactWrite): Promise<PhoneContact> {
  const { data } = await apiClient.post<PhoneContact>(base(hh), body)
  return data
}

export async function updatePhoneContact(hh: string, id: string, body: PhoneContactWrite): Promise<PhoneContact> {
  const { data } = await apiClient.patch<PhoneContact>(`${base(hh)}/${encodeURIComponent(id)}`, body)
  return data
}

export async function deletePhoneContact(hh: string, id: string): Promise<void> {
  await apiClient.delete(`${base(hh)}/${encodeURIComponent(id)}`)
}

/** contactRejection is which field cc refused and why: a duplicate name (409) or a bad number (400). */
export function contactRejection(err: unknown): { field: 'name' | 'number'; message: string } | null {
  if (!isAxiosError(err)) return null
  const d = err.response?.data as { detail?: unknown; message?: unknown } | undefined
  const said = [d?.detail, d?.message].find((x): x is string => typeof x === 'string' && x.trim() !== '')
  if (err.response?.status === 409) return { field: 'name', message: said ?? 'There’s already a business with that name.' }
  if (err.response?.status === 400) return { field: 'number', message: said ?? 'That phone number looks invalid.' }
  return null
}

/** formatPhone shows a North American number as (555) 123-4567; anything else as stored. */
export function formatPhone(raw: string): string {
  const digits = raw.replace(/\D/g, '')
  const n = digits.length === 11 && digits.startsWith('1') ? digits.slice(1) : digits
  return n.length === 10 ? `(${n.slice(0, 3)}) ${n.slice(3, 6)}-${n.slice(6)}` : raw
}

export const SOURCE_LABEL: Record<string, string> = { manual: 'Added by you', call: 'Saved from a call', web: 'Found by search' }
