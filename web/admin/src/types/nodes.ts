export interface Household {
  id: string
  name: string
  role: string
  created_at: string
}

export interface HouseholdNode {
  node_id: string
  name: string
  is_active: boolean
  household_id: string
  registered_by_user_id: number | null
  created_at: string
  updated_at: string | null
  last_rotated_at: string | null
  services: string[]
}

export interface TrainAdapterResponse {
  status: string
  request_id: string
}
