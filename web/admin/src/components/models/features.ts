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
 * docs/decisions/2026-10-22-cloud-model-slot.md). Add the planner or summaries here when they
 * get their setting.
 */
export interface Feature {
  key: string
  service: string
  title: string
  auto: string
  /** What leaves the network when the feature runs on cloud, as one sentence. */
  notice: (provider: string) => string
}

export const FEATURES: Feature[] = [
  {
    key: 'computer.model',
    service: 'cc',
    title: 'Computer control',
    auto: 'the largest local vision model',
    notice: (provider) => `Page contents and screenshots of your browser are sent to ${provider} while a task runs.`,
  },
]

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
