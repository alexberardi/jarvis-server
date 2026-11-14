import { apiClient } from './client'

/**
 * Taught workflows and the recordings they are learnt from (computer control, plan P3 piece 26):
 * cc's routes through the gateway's member tier. Who sees and changes what is
 * docs/decisions/2026-10-19-workflow-and-recording-access.md: a workflow is its owner's (and the
 * household's when shared); a recording is its owner's alone.
 */
const V0 = '/api/cc/api/v0'

export type ParamKind = 'date' | 'month' | 'text' | 'number' | 'file'

/** A value that changes each run; steps name it as `{name}`. */
export interface Param {
  name: string
  kind: ParamKind
  example?: string
  description?: string
}

/** computeruse.Action: what a step does (only the fields the pages read are spelled out). */
export interface Action {
  kind: string
  intent: string
  text?: string
  url?: string
  keys?: string[]
  ref?: string
  ms?: number
  dx?: number
  dy?: number
  [field: string]: unknown
}

/** The element a step acted on, as the recording saw it. */
export interface RecordedElement {
  role?: string
  name?: string
  selector?: string
  frame?: string
  ax_path?: string
}

export interface WStep {
  intent: string
  target: 'browser' | 'desktop'
  site?: string
  app?: string
  /** desktop: the window the step was recorded in */
  window?: { class?: string; title?: string }
  action: Action
  element?: RecordedElement
  /** A computeruse risk category: the step asks first. "" or absent is none. */
  risk?: string
  /** The point of the step, and what can be observed once it worked (the workflow's skill). */
  goal?: string
  expect?: SuccessCheck
}

/** computer.SuccessCheck: how a run is checked afterwards. */
export interface SuccessCheck {
  url_contains?: string
  text_present?: string[]
  download?: { name_like: string; min_bytes?: number }
  file?: { path: string; contains?: string[] }
  screen?: string
}

export type PrerequisiteKind = 'app_open' | 'file_open' | 'page_open' | 'signed_in' | 'other'

/** Something that must be true before step 1; a run tries to achieve an unmet one. */
export interface Prerequisite {
  kind: PrerequisiteKind
  text?: string
  app?: string
  site?: string
  file?: string
  url?: string
}

/**
 * computer.Skill: what a workflow knows about its task beyond its steps
 * (docs/agent/WORKFLOW-SKILLS.md). Separate fields, never a document.
 */
export interface Skill {
  goal?: { reason?: string; end_state?: string }
  when_to_use?: string
  prerequisites?: Prerequisite[]
  /** strict | creative: set with the run that reads it; the page only keeps it. */
  guardrails?: string
  notes?: { text: string; app?: string }[]
}

export interface TaughtWorkflow {
  id: string
  household_id: string
  owner_id: number
  name: string
  slug: string
  triggers: string[] | null
  params: Param[] | null
  steps: WStep[]
  success: SuccessCheck
  skill?: Skill
  shared: boolean
  /** A run of it has finished with its result checked. Until then only its owner starts it. */
  proven?: boolean
  recording_id?: string
  version: number
  created_at: string
  updated_at: string
  /** Whether the caller may change it (its owner, or a household admin). */
  editable: boolean
}

/** WorkflowPut is PUT's body: exactly these fields (cc refuses any other). */
export interface WorkflowPut {
  name: string
  triggers: string[]
  params: Param[]
  steps: WStep[]
  success: SuccessCheck
  shared: boolean
  /** Left out, cc keeps the stored skill. */
  skill?: Skill
}

export async function listWorkflows(householdId: string): Promise<TaughtWorkflow[]> {
  const { data } = await apiClient.get<{ workflows: TaughtWorkflow[] }>(`${V0}/workflows`, {
    params: { household_id: householdId },
  })
  return data.workflows ?? []
}

export async function getWorkflow(id: string): Promise<TaughtWorkflow> {
  const { data } = await apiClient.get<TaughtWorkflow>(`${V0}/workflows/${encodeURIComponent(id)}`)
  return data
}

export async function saveWorkflow(id: string, body: WorkflowPut): Promise<TaughtWorkflow> {
  const { data } = await apiClient.put<TaughtWorkflow>(`${V0}/workflows/${encodeURIComponent(id)}`, body)
  return data
}

/** relearnWorkflow learns it again from its recording and answers the draft; cc stores nothing until it is saved. */
export async function relearnWorkflow(id: string): Promise<WorkflowPut> {
  const { data } = await apiClient.post<WorkflowPut>(`${V0}/workflows/${encodeURIComponent(id)}/relearn`, {})
  return data
}

/** runWorkflow asks cc to replay it now, in an errand whose card shows how it goes (piece 31). */
export async function runWorkflow(id: string, params?: Record<string, string>): Promise<void> {
  await apiClient.post(`${V0}/workflows/${encodeURIComponent(id)}/run`, params ? { params } : {})
}

export type RunState = 'running' | 'waiting' | 'done' | 'failed' | 'stopped'
export type RunItemState = 'waiting' | 'done' | 'verified' | 'asked' | 'failed'

/** One prerequisite, step or the result of a run, with what it expected and what a failed check found. */
export interface RunItem {
  kind: 'prerequisite' | 'step' | 'result'
  text: string
  state: RunItemState
  expected?: string
  seen?: string
}

/** computer.RunRecord: a workflow's last run, for whoever taught it. */
export interface RunRecord {
  run_id: string
  state: RunState
  params?: Record<string, string>
  summary?: string
  ask?: string
  started_at: string
  updated_at: string
  items: RunItem[] | null
}

/** getLastRun is the workflow's last run (null: it has never run). Only its owner may ask: 403 otherwise. */
export async function getLastRun(id: string): Promise<RunRecord | null> {
  const { data } = await apiClient.get<{ run: RunRecord | null }>(`${V0}/workflows/${encodeURIComponent(id)}/last-run`)
  return data.run ?? null
}

/** One recorded event (the extension's trace format, plan P3 piece 28). */
export interface TraceEvent {
  kind?: string
  /** ms since the epoch, the page's clock */
  ts?: number
  tab?: number
  url?: string
  /** where a click, submit or key took its tab (foldTrace) */
  result_url?: string
  value?: string
  masked?: boolean
  key?: string
  filename?: string
  element?: { role?: string; name?: string; tag?: string; type?: string }
  /** its place in the trace (a desktop click's keyframe is stored by it) */
  seq?: number
  /** desktop: the app and the focused window it happened in */
  app?: string
  window?: { class?: string; title?: string }
  [field: string]: unknown
}

export interface Recording {
  id: string
  household_id: string
  owner_id: number
  node_id?: string
  mode: 'browser' | 'desktop'
  /** Only on a single recording (the list leaves it out). */
  trace?: unknown
  narration: string
  /** How many keyframes the computer that recorded it took. */
  keyframes: number
  /** Only on a single recording: the click keyframes the server keeps, by the event's seq (crop: its crop too). */
  keyframe_images?: { seq: number; crop: boolean }[]
  /** The trace's events. */
  steps: number
  created_at: string
  expires_at: string
}

export async function listRecordings(householdId: string): Promise<Recording[]> {
  const { data } = await apiClient.get<{ recordings: Recording[] }>(`${V0}/recordings`, {
    params: { household_id: householdId },
  })
  return data.recordings ?? []
}

export async function getRecording(id: string): Promise<Recording> {
  const { data } = await apiClient.get<Recording>(`${V0}/recordings/${encodeURIComponent(id)}`)
  return data
}

/**
 * getRecordingKeyframe downloads one click's keyframe (the screen, the click marked) or its crop.
 * The gateway wants the bearer header, which an <img> src can't send, so it comes as a Blob.
 */
export async function getRecordingKeyframe(id: string, seq: number, crop: boolean): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(
    `${V0}/recordings/${encodeURIComponent(id)}/keyframes/${seq}${crop ? '/crop' : ''}`,
    { responseType: 'blob' },
  )
  return data
}

export async function deleteRecording(id: string): Promise<void> {
  await apiClient.delete(`${V0}/recordings/${encodeURIComponent(id)}`)
}

/** The household setting the Recordings page edits (household admins only). */
export const RETENTION_KEY = 'computer.recording_retention_days'
