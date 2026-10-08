/**
 * The privacy choices the setup wizard asks about (AD3a, install ID8): every feature that sends
 * data off this machine or keeps personal data on it, with one line on what that is.
 *
 * Off-box features default off. Local-only features keep their current default but are shown,
 * so the operator knows they exist. Values are the install-wide defaults
 * (`PUT /api/settings/{service}/{key}`); households can still change their own from the app.
 */
import type { ServiceSettingsResult } from '@/types/settings'

export interface PrivacyToggle {
  service: string
  key: string
  title: string
  /** What leaves the box (offBox) or what is kept on it (local). */
  what: string
  offBox: boolean
}

export const PRIVACY_TOGGLES: PrivacyToggle[] = [
  {
    service: 'cc',
    key: 'web_search.enabled',
    title: 'Web search',
    what: 'What you ask Jarvis to look up is sent to a web search engine, and the pages it reads are fetched from the internet.',
    offBox: true,
  },
  {
    service: 'cc',
    key: 'web_scraping.allow_external',
    title: 'Reader proxy for web research',
    what: "When a page can't be opened directly, its address is sent to r.jina.ai, a third-party service, which then sees what pages your household reads.",
    offBox: true,
  },
  {
    service: 'admin',
    key: 'updates.enabled',
    title: 'Check for updates',
    what: 'jarvisd asks GitHub for the list of releases, so GitHub sees your IP address. Nothing installs by itself.',
    offBox: true,
  },
  {
    service: 'cc',
    key: 'memory.enabled',
    title: 'Memories',
    what: 'Facts Jarvis is told to remember about your household, used to answer later.',
    offBox: false,
  },
  {
    service: 'cc',
    key: 'memory.extraction_enabled',
    title: 'Learn from conversations',
    what: "A recognised speaker's recent voice turns are kept for a few days and turned into memories.",
    offBox: false,
  },
  {
    service: 'cc',
    key: 'ambient_context.enabled',
    title: 'Ambient context',
    what: 'The time, weather, calendar and household signals Jarvis already holds are added to each request, and recognised voices mark who is home.',
    offBox: false,
  },
  {
    service: 'stt',
    key: 'voice.recognition_enabled',
    title: 'Speaker recognition',
    what: 'Voiceprints of the people who enrol, used to know who is speaking.',
    offBox: false,
  },
]

export type PrivacyValues = Record<string, boolean>

export function privacyId(t: Pick<PrivacyToggle, 'service' | 'key'>): string {
  return `${t.service}/${t.key}`
}

/**
 * readPrivacy finds each toggle's current value (stored, else env fallback, else default; every
 * off-box feature defaults off). A key this jarvisd doesn't define is left out, and the step
 * shows it as unavailable.
 */
export function readPrivacy(services: ServiceSettingsResult[] | undefined): PrivacyValues {
  const out: PrivacyValues = {}
  for (const t of PRIVACY_TOGGLES) {
    const svc = services?.find((s) => s.service_name === t.service)
    const row = svc?.settings.find((s) => s.key === t.key)
    if (!row) continue
    out[privacyId(t)] = row.value === true || row.value === 'true'
  }
  return out
}

/** privacyChanges lists the toggles whose chosen value differs from the current one. */
export function privacyChanges(current: PrivacyValues, chosen: PrivacyValues): { service: string; key: string; value: boolean }[] {
  return PRIVACY_TOGGLES.filter((t) => privacyId(t) in chosen && chosen[privacyId(t)] !== current[privacyId(t)]).map((t) => ({
    service: t.service,
    key: t.key,
    value: chosen[privacyId(t)],
  }))
}

/** The project's push relay (the legacy installer's default), offered when push is turned on. */
export const DEFAULT_RELAY_URL = 'https://relay.jarvisautomation.io'

/**
 * relayChange is the notifications relay.url write for the push toggle, or null when nothing
 * changes. Turning it on keeps a relay already configured (e.g. a self-hosted one) and otherwise
 * uses the project's; turning it off clears it.
 */
export function relayChange(current: string, on: boolean): { service: string; key: string; value: string } | null {
  if (on === (current !== '')) return null
  return { service: 'notifications', key: 'relay.url', value: on ? DEFAULT_RELAY_URL : '' }
}
