import { apiClient } from './client'

export interface TraceSpan {
  name: string
  service: string
  start_ms: number
  end_ms: number
  duration_ms: number
  status: string
  metadata: Record<string, unknown>
}

export interface TraceListItem {
  id: string
  conversation_id: string
  request_type: string
  source: string
  node_id: string | null
  household_id: string | null
  user_command: string | null
  assistant_message: string | null
  status: string
  total_duration_ms: number
  span_count: number
  created_at: string
}

export interface TraceDetail extends Omit<TraceListItem, 'span_count'> {
  error_message: string | null
  spans: TraceSpan[]
}

export interface TraceListResponse {
  traces: TraceListItem[]
  total: number
}

export interface TraceListParams {
  limit?: number
  offset?: number
  status?: string
  source?: string
  household_id?: string
  node_id?: string
}

export async function fetchTraces(params: TraceListParams = {}): Promise<TraceListResponse> {
  const { data } = await apiClient.get<TraceListResponse>('/api/traces', { params })
  return data
}

export async function fetchTrace(traceId: string): Promise<TraceDetail> {
  const { data } = await apiClient.get<TraceDetail>(`/api/traces/${traceId}`)
  return data
}
