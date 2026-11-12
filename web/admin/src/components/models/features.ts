import type { LabelConfig } from '@/api/llm'

/** A feature's slot choice (llm.SlotChoices): auto is the feature's own pick and never cloud. */
export type Slot = 'auto' | 'live' | 'background' | 'cloud'

export const SLOT_TITLE: Record<Slot, string> = {
  auto: 'Auto',
  live: 'Live',
  background: 'Background',
  cloud: 'Cloud',
}

/**
 * A feature that runs on the model slot it is set to (llm.SlotSetting,
 * docs/llm/SPEC-feature-models.md). Each key is a registered slot setting; a Go test keeps this
 * table and the server's definitions from drifting apart.
 */
export interface Feature {
  key: string
  service: string
  title: string
  /** The slot auto resolves to: the label the server hardcoded before the setting existed. */
  auto: Exclude<Slot, 'auto' | 'cloud'>
  /** The feature sends images, so a slot whose model cannot see them is not offered. */
  vision?: boolean
  /** What leaves the network when the feature runs on cloud, as one sentence. */
  notice: (provider: string) => string
}

/** A run of features that share why you would move them, shown under one heading. */
export interface FeatureGroup {
  id: string
  title: string
  blurb: string
  features: Feature[]
}

export const FEATURE_GROUPS: FeatureGroup[] = [
  {
    id: 'waiting',
    title: 'Someone is waiting',
    blurb: 'A person is listening while these run, so a slow or distant slot is felt as a pause.',
    features: [
      {
        key: 'conversation.model',
        service: 'cc',
        title: 'Conversation',
        auto: 'live',
        notice: (p) => `What you say and the context of the turn are sent to ${p}.`,
      },
      {
        key: 'phone_calls.model',
        service: 'cc',
        title: 'Phone calls',
        auto: 'live',
        notice: (p) => `What is said on the call is sent to ${p}, as the call happens.`,
      },
      {
        key: 'recipes.import.model',
        service: 'recipes',
        title: 'Recipe import',
        auto: 'live',
        notice: (p) => `The text of the page or photo you are importing is sent to ${p}.`,
      },
    ],
  },
  {
    id: 'background',
    title: 'Runs in the background',
    blurb: 'Nobody waits on these, so a larger, slower slot costs you nothing but electricity.',
    features: [
      {
        key: 'memory.model',
        service: 'cc',
        title: 'Memory',
        auto: 'background',
        notice: (p) => `A finished conversation is sent to ${p} to pull memories out of it.`,
      },
      {
        key: 'phone_calls.planning_model',
        service: 'cc',
        title: 'Call planning and follow-ups',
        auto: 'background',
        notice: (p) => `A call's goal, and its transcript afterwards, are sent to ${p}.`,
      },
      {
        key: 'errands.model',
        service: 'cc',
        title: 'Errands',
        auto: 'background',
        notice: (p) => `An errand's goal, and what it reads along the way, are sent to ${p}.`,
      },
      {
        key: 'signals.model',
        service: 'cc',
        title: 'Routines and reactions',
        auto: 'background',
        notice: (p) => `What set the routine off, and your household's context, are sent to ${p}.`,
      },
      {
        key: 'watches.model',
        service: 'cc',
        title: 'Web watches',
        auto: 'background',
        notice: (p) => `The contents of every page you watch are sent to ${p} on each check.`,
      },
      {
        key: 'research.model',
        service: 'cc',
        title: 'Deep research',
        auto: 'background',
        notice: (p) => `Your query and the pages it found are sent to ${p} to be written up.`,
      },
      {
        key: 'workflows.model',
        service: 'cc',
        title: 'Learned workflows',
        auto: 'background',
        notice: (p) => `The steps you took, and what they touched, are sent to ${p}.`,
      },
      {
        key: 'recipes.grocery.model',
        service: 'recipes',
        title: 'Grocery matching',
        auto: 'background',
        notice: (p) => `Your ingredients and the store's listing are sent to ${p}.`,
      },
    ],
  },
  {
    id: 'vision',
    title: 'Needs to see images',
    blurb: 'These send pictures, so they can only run on a slot whose model has vision.',
    features: [
      {
        key: 'computer.model',
        service: 'cc',
        title: 'Computer control',
        auto: 'background',
        vision: true,
        notice: (p) => `Page contents and screenshots of your browser are sent to ${p} while a task runs.`,
      },
      {
        key: 'ocr.model',
        service: 'ocr',
        title: 'Documents and images',
        auto: 'background',
        vision: true,
        notice: (p) => `The document or photo itself is sent to ${p} to be read.`,
      },
    ],
  },
]

export const FEATURES: Feature[] = FEATURE_GROUPS.flatMap((g) => g.features)

const PROVIDERS: Record<string, string> = {
  'api.anthropic.com': 'Anthropic',
  'api.openai.com': 'OpenAI',
  'openrouter.ai': 'OpenRouter',
  'generativelanguage.googleapis.com': 'Google',
}

/** The cloud slot's provider for a notice ("" when it has no URL). */
export function cloudProvider(cfg: LabelConfig | undefined): string {
  const raw = cfg?.engine === 'off' ? '' : (cfg?.remote_url ?? '').trim()
  if (!raw) return ''
  let host: string
  try {
    host = new URL(raw.includes('://') ? raw : `https://${raw}`).hostname
  } catch {
    return raw
  }
  const name = PROVIDERS[host.toLowerCase()]
  return name ? `${name} (${host})` : host
}

/** Whether a slot's model can see images: a local projector, or a remote that says so. */
export function slotSeesImages(cfg: LabelConfig | undefined): boolean {
  if (!cfg || cfg.engine === 'off') return false
  if (cfg.remote_url.trim()) return cfg.remote_vision
  return Boolean(cfg.mmproj || cfg.mmproj_path)
}
