import { featureMissing } from './features'
import { restartJarvisd } from './restart'
import type { ServiceSettingsResult, SettingResponse } from '@/types/settings'

/**
 * isLabelSetting says a setting belongs to one model label (`llm.live.*`, `llm.background.*`,
 * `llm.embeddings.*`, `stt.*`, `tts.model`, `speaker.model`). Those are written only through the
 * Models page's `PUT /api/llm/v1/models/labels`, which validates the whole label at once (I4),
 * so the generic Settings page hides them. The llm module files them under `engine.<label>`;
 * `engine.downloads` (HF token, mirrors, engine paths) are ordinary settings and stay.
 */
export function isLabelSetting(serviceName: string, s: SettingResponse): boolean {
  return serviceName === 'llm' && s.category.startsWith('engine.') && s.category !== 'engine.downloads'
}

/** withoutLabelSettings drops the label keys from every service (I4). */
export function withoutLabelSettings(services: ServiceSettingsResult[]): ServiceSettingsResult[] {
  return services.map((svc) => ({
    ...svc,
    settings: svc.settings.filter((s) => !isLabelSetting(svc.service_name, s)),
  }))
}

/** formatSettingValue renders a setting's value for display (secrets never echo, I5). */
export function formatSettingValue(setting: SettingResponse): string {
  if (setting.is_secret) return secretIsSet(setting) ? 'set (hidden)' : 'not set'
  if (setting.value === null || setting.value === undefined) return '(not set)'
  if (setting.value_type === 'json') return JSON.stringify(setting.value)
  return String(setting.value)
}

/** A secret's value comes back masked ("********") when set and empty when not (I5). */
export function secretIsSet(s: SettingResponse): boolean {
  return s.is_secret && s.value !== null && s.value !== undefined && s.value !== ''
}

/**
 * restartAction is the toast action for a `requires_reload` save (AD8): restart jarvisd through
 * `POST /api/system/restart`. Null once that route has answered 404 (an older jarvisd), so the
 * toast only says "Applies after jarvisd restarts".
 */
export function restartAction(): (() => void) | null {
  return featureMissing('restart') ? null : () => void restartJarvisd()
}

/**
 * whoIsOn says which households a household-scoped switch is on for: the default covers every
 * household without its own value.
 */
export function whoIsOn(s: SettingResponse): string {
  const own = s.household_values ?? []
  const names = (on: boolean) => own.filter((h) => (h.value === true) === on).map((h) => h.household_name)
  if (s.value === true) {
    const off = names(false)
    if (off.length === 0) return 'On for every household'
    if ((s.households_using_default ?? 0) + names(true).length === 0) return 'Off for every household'
    return `On for every household except ${off.join(', ')}`
  }
  const on = names(true)
  return on.length ? `On for ${on.join(', ')}` : 'Off for every household'
}
