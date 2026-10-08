import { useState } from 'react'
import { Globe, HardDrive, Info, ShieldCheck } from 'lucide-react'
import { toast } from 'sonner'
import { updateSetting } from '@/api/settings'
import { buttonClass } from '@/components/models/styles'
import { useAllSettings } from '@/hooks/useSettings'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'
import type { ServiceSettingsResult } from '@/types/settings'
import { useQueryClient } from '@tanstack/react-query'
import {
  DEFAULT_RELAY_URL,
  PRIVACY_TOGGLES,
  privacyChanges,
  privacyId,
  readPrivacy,
  relayChange,
  type PrivacyValues,
} from './privacy'

function settingValue(services: ServiceSettingsResult[] | undefined, service: string, key: string): unknown {
  return services?.find((s) => s.service_name === service)?.settings.find((s) => s.key === key)?.value
}

function Toggle({
  id,
  checked,
  disabled,
  onChange,
}: {
  id: string
  checked: boolean
  disabled?: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <input
      id={id}
      type="checkbox"
      role="switch"
      checked={checked}
      disabled={disabled}
      onChange={(e) => onChange(e.target.checked)}
      className="mt-0.5 h-4 w-4 shrink-0 accent-[var(--color-primary)]"
    />
  )
}

function PrivacyForm({ services, onDone }: { services: ServiceSettingsResult[]; onDone: () => void }) {
  const qc = useQueryClient()
  const [current] = useState<PrivacyValues>(() => readPrivacy(services))
  const [chosen, setChosen] = useState<PrivacyValues>(() => readPrivacy(services))
  const [saving, setSaving] = useState(false)
  const relayRaw = settingValue(services, 'notifications', 'relay.url')
  const relayKnown = relayRaw !== undefined
  const relay = typeof relayRaw === 'string' ? relayRaw : ''
  const [push, setPush] = useState(relay !== '')
  const relayWrite = relayKnown ? relayChange(relay, push) : null
  const changes = [...privacyChanges(current, chosen), ...(relayWrite ? [relayWrite] : [])]

  async function save() {
    if (changes.length === 0) {
      onDone()
      return
    }
    setSaving(true)
    try {
      for (const c of changes) await updateSetting(c.service, c.key, c.value)
      await qc.invalidateQueries({ queryKey: ['settings'] })
      toast.success('Privacy choices saved')
      onDone()
    } catch (err) {
      toast.error(errorMessage(err, 'Could not save the privacy choices'))
    } finally {
      setSaving(false)
    }
  }

  const pantry = settingValue(services, 'cc', 'pantry.base_url')
  const hf = settingValue(services, 'llm', 'llm.hf_endpoint')

  const group = (offBox: boolean) => (
    <ul className="space-y-3">
      {PRIVACY_TOGGLES.filter((t) => t.offBox === offBox).map((t) => {
        const id = privacyId(t)
        const known = id in chosen
        return (
          <li key={id} className="flex items-start gap-3">
            <Toggle
              id={`privacy-${id}`}
              checked={known && chosen[id]}
              disabled={!known}
              onChange={(v) => setChosen((c) => ({ ...c, [id]: v }))}
            />
            <label htmlFor={`privacy-${id}`} className={cn('min-w-0', !known && 'opacity-60')}>
              <span className="block text-sm font-medium text-[var(--color-text)]">{t.title}</span>
              <span className="block text-xs text-[var(--color-text-muted)]">
                {t.what}
                {!known && ' (Not available in this jarvisd version.)'}
              </span>
            </label>
          </li>
        )
      })}
    </ul>
  )

  return (
    <div className="space-y-5">
      <section className="space-y-2">
        <h3 className="flex items-center gap-2 text-sm font-semibold text-[var(--color-text)]">
          <Globe size={14} /> Sends data off this machine
        </h3>
        <p className="text-xs text-[var(--color-text-muted)]">Off unless you turn them on.</p>
        {group(true)}
        <div className="flex items-start gap-3">
          <Toggle id="privacy-push" checked={relayKnown && push} disabled={!relayKnown} onChange={setPush} />
          <label htmlFor="privacy-push" className={cn('min-w-0', !relayKnown && 'opacity-60')}>
            <span className="block text-sm font-medium text-[var(--color-text)]">Phone push notifications</span>
            <span className="block text-xs text-[var(--color-text-muted)]">
              Each notification's title and text go through a push relay (
              <code className="break-all">{relay || DEFAULT_RELAY_URL}</code>) to Apple or Google so phones get them.
              Off: notifications stay in the app's inbox.
              {!relayKnown && ' (Not available in this jarvisd version.)'}
            </span>
          </label>
        </div>
      </section>

      <section className="space-y-2">
        <h3 className="flex items-center gap-2 text-sm font-semibold text-[var(--color-text)]">
          <HardDrive size={14} /> Keeps personal data on this machine
        </h3>
        <p className="text-xs text-[var(--color-text-muted)]">All of this stays on this machine.</p>
        {group(false)}
      </section>

      <section className="space-y-1 rounded-lg border border-[var(--color-border)] p-3 text-xs text-[var(--color-text-muted)]">
        <h3 className="flex items-center gap-2 text-sm font-semibold text-[var(--color-text)]">
          <Info size={14} /> Also good to know
        </h3>
        <p>
          <strong className="text-[var(--color-text)]">Command packages</strong> are browsed and installed from the Pantry
          {typeof pantry === 'string' && pantry ? (
            <>
              {' '}at <code className="break-all">{pantry}</code>
            </>
          ) : null}
          , which sees which packages you look at. Change it under Settings.
        </p>
        <p>
          <strong className="text-[var(--color-text)]">Model downloads</strong> come from{' '}
          {typeof hf === 'string' && hf ? <code className="break-all">{hf}</code> : 'Hugging Face'}, only when you install
          a model.
        </p>
        <p>
          <strong className="text-[var(--color-text)]">Remote model endpoints</strong> are off. A job sends your requests
          to another server only if you point it at one on the Models page.
        </p>
      </section>

      <p className="text-xs text-[var(--color-text-muted)]">
        These are the defaults for the whole install. Each household can change its own from the mobile app.
      </p>

      <div className="flex justify-end">
        <button type="button" className={buttonClass.primary} disabled={saving} onClick={() => void save()}>
          {saving ? 'Saving…' : changes.length > 0 ? 'Save and continue' : 'Continue'}
        </button>
      </div>
    </div>
  )
}

/** PrivacyStep (AD3a, ID8): what leaves the box and what is kept, each in one line. */
export default function PrivacyStep({ onDone }: { onDone: () => void }) {
  const { data, isLoading, isError, error } = useAllSettings()
  return (
    <div className="space-y-4">
      <div>
        <h2 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
          <ShieldCheck size={20} /> Privacy
        </h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          Jarvis runs on this machine. These are the features that reach the internet or keep personal data, so you
          decide.
        </p>
      </div>
      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Loading…</p>}
      {isError && <p className="text-sm text-red-500">{errorMessage(error, 'Could not read the settings')}</p>}
      {data && <PrivacyForm services={data.services} onDone={onDone} />}
    </div>
  )
}
