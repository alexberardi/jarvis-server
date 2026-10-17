import { apiClient } from './client'
import type { ModeSetting } from './llm'

/**
 * The household's voice prompt (docs/cc/prompt-contract.md) through the admin gateway's cc
 * routes: read, edit, reset the edit, pick a built-in, import a file, preview for a node.
 */
const base = (householdId: string) => `/api/cc/api/v0/households/${encodeURIComponent(householdId)}/prompt`

/** One {{name}} a prompt may use. */
export interface PromptVariable {
  name: string
  kind: 'inline' | 'section' | 'directive'
  description: string
}

/** A prompt's own mode values; "" = from the model. */
export interface PromptPrefs {
  tool_calling: string
  thinking_switch: string
}

export interface HouseholdPrompt {
  /** builtin, import, or default (the household follows the install's default). */
  source: 'builtin' | 'import' | 'default'
  name: string
  /** What turns use: the edit, else the original. */
  text: string
  original: string
  edited: boolean
  /** Why the household's own prompt is not used ("" when it is). */
  fallback: string
  builtins: string[]
  variables: PromptVariable[]
  prefs: PromptPrefs
  tool_calling: ModeSetting
  thinking_switch: ModeSetting
}

/** The body of a 400: the template error, its line and the variable when one is unknown. */
export interface PromptError {
  detail: string
  line?: number
  variable?: string
}

export interface PromptPreview {
  system_prompt: string
  tool_calling: string
  tools_via_api: number
  user_suffix: string
  /** No warmup from the node yet: rendered without its tools, devices or command flags. */
  partial: boolean
}

export async function getHouseholdPrompt(householdId: string): Promise<HouseholdPrompt> {
  const { data } = await apiClient.get<HouseholdPrompt>(base(householdId))
  return data
}

/** savePrompt stores the edit and the prompt's values; text equal to the original is no edit. */
export async function savePrompt(householdId: string, body: { text: string } & Partial<PromptPrefs>): Promise<HouseholdPrompt> {
  const { data } = await apiClient.put<HouseholdPrompt>(base(householdId), body)
  return data
}

/** resetPrompt drops the edit; the prompt and its values stay. */
export async function resetPrompt(householdId: string): Promise<HouseholdPrompt> {
  const { data } = await apiClient.post<HouseholdPrompt>(`${base(householdId)}/reset`)
  return data
}

/** setPromptBase picks a built-in; "" makes the household follow the install default. */
export async function setPromptBase(householdId: string, builtin: string): Promise<HouseholdPrompt> {
  const { data } = await apiClient.put<HouseholdPrompt>(`${base(householdId)}/base`, { builtin })
  return data
}

/** importPrompt makes a file's text the household's prompt, named name. */
export async function importPrompt(
  householdId: string,
  name: string,
  text: string,
  prefs?: Partial<PromptPrefs>,
): Promise<HouseholdPrompt> {
  const { data } = await apiClient.post<HouseholdPrompt>(`${base(householdId)}/import`, text, {
    headers: { 'Content-Type': 'text/plain' },
    params: { name, ...prefs },
  })
  return data
}

/** previewPrompt renders a draft for one of the household's nodes. */
export async function previewPrompt(householdId: string, text: string, nodeId: string): Promise<PromptPreview> {
  const { data } = await apiClient.post<PromptPreview>(`${base(householdId)}/preview`, { text, node_id: nodeId })
  return data
}
