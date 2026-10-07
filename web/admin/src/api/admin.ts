import { apiClient } from './client'

export interface AdminHousehold {
  id: string
  name: string
  created_at: string
  updated_at: string
}

export interface AdminNode {
  node_id: string
  name: string
  household_id: string
  is_active: boolean
  registered_by_user_id: number | null
  created_at: string
  updated_at: string | null
  last_rotated_at: string | null
  services: string[]
}

export interface AdminUserHousehold {
  household_id: string
  household_name: string
  role: 'member' | 'power_user' | 'admin'
}

export interface AdminUser {
  id: number
  email: string
  username: string
  is_active: boolean
  is_superuser: boolean
  must_change_password: boolean
  created_at: string
  updated_at: string | null
  households: AdminUserHousehold[]
}

export interface TempPasswordResult {
  temp_password: string
  expires_at: string
  must_change_password: boolean
}

export interface TempPasswordOptions {
  temp_password?: string
  expires_in_hours?: number
}

export async function fetchAllHouseholds(): Promise<AdminHousehold[]> {
  const { data } = await apiClient.get<AdminHousehold[]>('/api/admin/households')
  return data
}

export async function fetchAllNodes(): Promise<AdminNode[]> {
  const { data } = await apiClient.get<AdminNode[]>('/api/admin/nodes')
  return data
}

export async function fetchAllUsers(): Promise<AdminUser[]> {
  const { data } = await apiClient.get<AdminUser[]>('/api/admin/users')
  return data
}

export async function issueTempPassword(
  userId: number,
  options: TempPasswordOptions = {},
): Promise<TempPasswordResult> {
  const { data } = await apiClient.post<TempPasswordResult>(
    `/api/admin/users/${userId}/temp-password`,
    options,
  )
  return data
}
