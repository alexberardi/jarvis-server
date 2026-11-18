import { apiClient } from './client'

/**
 * Memories (mobile's Memories screens): what Jarvis remembers about the caller, and for power
 * users and admins the household's shared ones. cc decides who may see and edit each.
 */
const BASE = '/api/cc/api/v0/mobile/memories'

export type MemoryScope = 'user' | 'household'

export interface Memory {
  id: number
  /** null for a household memory. */
  user_id: number | null
  household_id: string
  category: string
  content: string
  /** "agent" for ones a background agent keeps (calendar, news, weather). */
  source: string
  is_pinned: boolean
  created_at: string
  /** False for ones the caller can only read (agent-kept, or another member's). */
  editable: boolean
}

export interface MemoryWrite {
  content: string
  category: string
  is_pinned: boolean
  /** On create only: whose it is. */
  scope?: MemoryScope
}

export async function listMemories(hh: string): Promise<Memory[]> {
  const { data } = await apiClient.get<Memory[]>(BASE, { params: { household_id: hh } })
  return data
}

export async function createMemory(hh: string, body: MemoryWrite): Promise<Memory> {
  const { data } = await apiClient.post<Memory>(BASE, body, { params: { household_id: hh } })
  return data
}

/** An update can't move a memory between you and the household (no scope). */
export async function updateMemory(hh: string, id: number, body: Omit<MemoryWrite, 'scope'>): Promise<Memory> {
  const { data } = await apiClient.put<Memory>(`${BASE}/${id}`, body, { params: { household_id: hh } })
  return data
}

export async function deleteMemory(hh: string, id: number): Promise<void> {
  await apiClient.delete(`${BASE}/${id}`, { params: { household_id: hh } })
}
