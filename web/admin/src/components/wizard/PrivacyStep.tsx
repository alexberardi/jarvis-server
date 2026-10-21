import { useState } from 'react'
import { Globe, HardDrive, Info, ShieldCheck } from 'lucide-react'
import { toast } from 'sonner'
import { updateSetting } from '@/api/settings'
import { Button, ErrorState, Switch } from '@/components/ui'
import { useAllSettings } from '@/hooks/useSettings'
import { errorMessage } from '@/lib/errors'
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
import { StepActions, StepHeader, StepLoading } from './StepFrame'

const sectionTitle = 'flex items-center gap-2 text-subheading text-text'

function settingValue(services: ServiceSettingsResult[] | undefined, service: string, key: string): unknown {
  return services?.find((s) => s.service_name === service)?.settings.find((s) => s.key === key)?.value
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

  const unavailable = ' (Not available in this jarvisd version.)'
  const group = (offBox: boolean) => (
    <ul className="space-y-4">
      {PRIVACY_TOGGLES.filter((t) => t.offBox === offBox).map((t) => {
        const id = privacyId(t)
        const known = id in chosen
        return (
          <li key={id}>
            <Switch
              id={`privacy-${id}`}
              label={t.title}
              description={known ? t.what : t.what + unavailable}
              checked={known && chosen[id]}
              disabled={!known}
              onCheckedChange={(v) => setChosen((c) => ({ ...c, [id]: v }))}
            />
          </li>
        )
      })}
    </ul>
  )

  return (
    <div className="space-y-6">
      <section className="space-y-3" aria-labelledby="privacy-off-box">
        <div>
          <h3 id="privacy-off-box" className={sectionTitle}>
            <Globe size={16} aria-hidden className="text-text-muted" /> Sends data off this machine
          </h3>
          <p className="text-small text-text-muted">Off unless you turn them on.</p>
        </div>
        {group(true)}
        <Switch
          id="privacy-push"
          label="Phone push notifications"
          description={
            <>
              Each notification's title and text go through a push relay (
              <code className="break-all">{relay || DEFAULT_RELAY_URL}</code>) to Apple or Google so phones get them.
              Off: notifications stay in the app's inbox.
              {!relayKnown && unavailable}
            </>
          }
          checked={relayKnown && push}
          disabled={!relayKnown}
          onCheckedChange={setPush}
        />
      </section>

      <section className="space-y-3" aria-labelledby="privacy-on-box">
        <div>
          <h3 id="privacy-on-box" className={sectionTitle}>
            <HardDrive size={16} aria-hidden className="text-text-muted" /> Keeps personal data on this machine
          </h3>
          <p className="text-small text-text-muted">All of this stays on this machine.</p>
        </div>
        {group(false)}
      </section>

      <section className="space-y-2 border-t border-border pt-5 text-small text-text-muted" aria-labelledby="privacy-more">
        <h3 id="privacy-more" className={sectionTitle}>
          <Info size={16} aria-hidden className="text-text-muted" /> Also good to know
        </h3>
        <p>
          <strong className="font-medium text-text">Command packages</strong> are browsed and installed from the Pantry
          {typeof pantry === 'string' && pantry ? (
            <>
              {' '}at <code className="break-all">{pantry}</code>
            </>
          ) : null}
          , which sees which packages you look at. Change it under Settings.
        </p>
        <p>
          <strong className="font-medium text-text">Model downloads</strong> come from{' '}
          {typeof hf === 'string' && hf ? <code className="break-all">{hf}</code> : 'Hugging Face'}, only when you install
          a model.
        </p>
        <p>
          <strong className="font-medium text-text">Remote model endpoints</strong> are off. A job sends your requests
          to another server only if you point it at one on the Models page.
        </p>
        <p>
          These are the defaults for the whole install. Each household can change its own from the mobile app, and a
          household that already has its own value keeps it. Settings shows every household's value.
        </p>
      </section>

      <StepActions>
        <Button variant="primary" loading={saving} onClick={() => void save()}>
          {changes.length > 0 ? 'Save and continue' : 'Continue'}
        </Button>
      </StepActions>
    </div>
  )
}

/** PrivacyStep (AD3a, ID8): what leaves the box and what is kept, each in one line. */
export default function PrivacyStep({ onDone }: { onDone: () => void }) {
  const { data, isLoading, isError, error, refetch } = useAllSettings()
  return (
    <div className="space-y-5">
      <StepHeader icon={<ShieldCheck size={22} />} title="Privacy">
        Jarvis runs on this machine. These are the features that reach the internet or keep personal data, so you
        decide.
      </StepHeader>
      {isLoading && <StepLoading>Loading the settings</StepLoading>}
      {isError && (
        <ErrorState
          title="Couldn't read the settings"
          message={errorMessage(error, 'The server did not answer.')}
          onRetry={() => refetch()}
        />
      )}
      {data && <PrivacyForm services={data.services} onDone={onDone} />}
    </div>
  )
}
