import { apiClient } from './client'

/**
 * The Phone page's SIP support routes (docs/cc/phone-sip.md §6), all superuser: every SIP
 * account's registration, the self-tests and the SIP trace.
 */

export type SipTestName = 'connection' | 'echo' | 'digits' | 'call_me' | 'inbound'
export type SipTestState = 'running' | 'passed' | 'failed'

export interface SipInbound {
  at: string
  caller: string
  caller_name?: string
  to: string
  media?: string
  codecs: string[]
  response: number
}

export interface SipTestRun {
  test: SipTestName
  state: SipTestState
  started: string
  finished?: string
  summary: string
  hint?: string
  steps: string[]
  register_rtt_ms?: number
  options_rtt_ms?: number
  latency_ms?: number
  expected?: string
  heard?: string
  wer?: number
  session_id?: string
  inbound?: SipInbound
}

/** sip.State, or not_started when the account's UA could not start. */
export type SipState = 'registering' | 'registered' | 'retrying' | 'stopped' | 'closed' | 'not_started'

export interface SipAccount {
  local_port: number
  server: string
  port: number
  transport: string
  extension: string
  dial_format: string
  dial_prefix: string
  /** "" for the system account. */
  household_id: string
  state: SipState
  since?: string
  last_error: string
  hint: string
  auth_failures: number
  attempts: number
  register_rtt_ms: number | null
  options_rtt_ms: number | null
  last_options?: string
  active_calls: number
  last_inbound?: SipInbound
  tests: Partial<Record<SipTestName, SipTestRun>>
}

export interface SipStatus {
  accounts: SipAccount[]
  tests: SipTestName[]
  feature_codes: string[]
}

export interface SipTraceMessage {
  at: string
  dir: 'sent' | 'received'
  transport: string
  local: string
  remote: string
  first_line: string
  text: string
}

export interface SipTraceDialog {
  call_id: string
  account: string
  kind: 'call' | 'registration'
  method: string
  started: string
  updated: string
  messages: SipTraceMessage[]
  dropped: number
}

export async function getSipStatus(): Promise<SipStatus> {
  const { data } = await apiClient.get<SipStatus>('/api/phone/sip')
  return data
}

/** startSipTest starts a self-test; the status shows its progress. call_me needs `number`. */
export async function startSipTest(port: number, test: SipTestName, body: { number?: string } = {}): Promise<SipTestRun> {
  const { data } = await apiClient.post<SipTestRun>(`/api/phone/sip/${port}/tests/${test}`, body)
  return data
}

export async function getSipTrace(): Promise<SipTraceDialog[]> {
  const { data } = await apiClient.get<{ dialogs: SipTraceDialog[] }>('/api/phone/sip/trace')
  return data.dialogs
}

/** downloadSipTrace saves the plain-text export (the bearer token rules out a plain link). */
export async function downloadSipTrace(): Promise<void> {
  const res = await apiClient.get<Blob>('/api/phone/sip/trace', { params: { format: 'text' }, responseType: 'blob' })
  const name = /filename="([^"]+)"/.exec(String(res.headers['content-disposition'] ?? ''))?.[1] ?? 'jarvis-sip-trace.txt'
  const url = URL.createObjectURL(res.data)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
