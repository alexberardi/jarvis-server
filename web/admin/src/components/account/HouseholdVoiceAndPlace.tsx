import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { getHouseholdSettings, getHouseholdTimezone, getPersonaPresets, putHouseholdSetting } from '@/api/household'
import { Button, Card, CardHeader, Input, Select } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import { errorMessage } from '@/lib/errors'
import { cn } from '@/lib/utils'

const settingsKey = (hh: string) => ['household-settings', hh]

/** Every IANA zone the browser knows, for the time zone picker. */
const ZONES: string[] = (() => {
  try {
    return Intl.supportedValuesOf('timeZone')
  } catch {
    return []
  }
})()

/** zoneLabel is "America/New_York" as "America / New York". */
const zoneLabel = (z: string) => z.replace(/_/g, ' ').replace(/\//g, ' / ')

/**
 * VoiceCard is how Jarvis talks for the household (mobile's Voice): persona.household_prompt,
 * with the server's starters to load and tweak. Any member reads; an admin saves.
 */
export function VoiceCard({ hh, canEdit }: { hh: string; canEdit: boolean }) {
  const qc = useQueryClient()
  const settings = useQuery({ queryKey: settingsKey(hh), queryFn: () => getHouseholdSettings(hh) })
  const presets = useQuery({ queryKey: ['persona-presets', hh], queryFn: () => getPersonaPresets(hh), retry: false, staleTime: Infinity })
  const saved = typeof settings.data?.['persona.household_prompt'] === 'string' ? (settings.data['persona.household_prompt'] as string) : ''
  const [draft, setDraft] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const text = draft ?? saved
  const max = presets.data?.max_chars ?? 2000

  async function save() {
    setSaving(true)
    try {
      await putHouseholdSetting(hh, 'persona.household_prompt', text.trim())
      qc.setQueryData<Record<string, unknown>>(settingsKey(hh), (old) => ({ ...old, 'persona.household_prompt': text.trim() }))
      setDraft(null)
      toast.success('Saved')
    } catch (err) {
      toast.error(errorMessage(err, 'Could not save it'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card as="section" aria-labelledby="household-voice" className="space-y-3">
      <CardHeader
        id="household-voice"
        title="How Jarvis talks"
        description={`Its tone and personality: this shapes how it speaks, not what it can do.${canEdit ? ' Load a starter, then change the wording however you like.' : ''}`}
      />
      {canEdit && (presets.data?.presets.length ?? 0) > 0 && (
        <div className="flex flex-wrap gap-1.5" role="group" aria-label="Starters">
          {presets.data!.presets.map((p) => (
            <Button
              key={p.id}
              size="sm"
              variant="ghost"
              aria-pressed={text.trim() === p.text.trim()}
              className="aria-pressed:bg-primary-soft aria-pressed:text-primary"
              onClick={() => setDraft(p.text)}
            >
              {p.label}
              {p.id === presets.data!.default_preset_id && ' (default)'}
            </Button>
          ))}
        </div>
      )}
      {canEdit ? (
        <>
          <label htmlFor="household-persona" className="sr-only">
            How Jarvis should sound
          </label>
          <textarea
            id="household-persona"
            rows={5}
            maxLength={max}
            disabled={settings.isLoading}
            className={cn(fieldControl, 'h-auto py-2')}
            value={text}
            placeholder="Describe how Jarvis should sound…"
            onChange={(e) => setDraft(e.target.value)}
          />
        </>
      ) : (
        // Read-only: the text itself, not a greyed-out box to scroll.
        <p className="rounded-control bg-surface-alt px-3 py-2 text-body text-pretty whitespace-pre-line text-text">{text}</p>
      )}
      <div className="flex items-center justify-between gap-3">
        {canEdit && (
          <span className="text-small text-text-muted tabular-nums">
            {text.length}/{max}
          </span>
        )}
        {canEdit ? (
          <Button loading={saving} disabled={text.trim() === saved.trim()} onClick={() => void save()}>
            Save
          </Button>
        ) : (
          <span className="text-small text-text-muted">Only a household admin can change this.</span>
        )}
      </div>
    </Card>
  )
}

/**
 * PlaceCard is where the household is (mobile's Location and Time zone): a town for finding
 * nearby businesses, and the time zone, Automatic (the nodes') unless chosen.
 */
export function PlaceCard({ hh, canEdit }: { hh: string; canEdit: boolean }) {
  const qc = useQueryClient()
  const settings = useQuery({ queryKey: settingsKey(hh), queryFn: () => getHouseholdSettings(hh) })
  const tz = useQuery({ queryKey: ['household-timezone', hh], queryFn: () => getHouseholdTimezone(hh), retry: false })
  const savedLocation = typeof settings.data?.['household.location'] === 'string' ? (settings.data['household.location'] as string) : ''
  const savedZone = typeof settings.data?.['household.timezone'] === 'string' ? (settings.data['household.timezone'] as string) : ''
  const [location, setLocation] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const loc = location ?? savedLocation

  async function write(key: string, value: string, done: string) {
    setBusy(key)
    try {
      await putHouseholdSetting(hh, key, value)
      qc.setQueryData<Record<string, unknown>>(settingsKey(hh), (old) => ({ ...old, [key]: value }))
      if (key === 'household.timezone') await qc.invalidateQueries({ queryKey: ['household-timezone', hh] })
      if (key === 'household.location') setLocation(null)
      toast.success(done)
    } catch (err) {
      toast.error(errorMessage(err, 'Could not save it'))
    } finally {
      setBusy(null)
    }
  }

  const nodeZone = tz.data?.node_timezone
  const automatic = `Automatic · ${nodeZone ? zoneLabel(nodeZone) : 'UTC for now'}`
  // A saved zone the browser doesn't list stays choosable.
  const zones = savedZone && !ZONES.includes(savedZone) ? [savedZone, ...ZONES] : ZONES

  return (
    <Card as="section" aria-labelledby="household-place" className="space-y-4">
      <CardHeader id="household-place" title="Where you are" description={canEdit ? undefined : 'Only a household admin can change these.'} />
      <form
        className="flex items-start gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          void write('household.location', loc.trim(), 'Location saved')
        }}
      >
        <Input
          label="Town"
          hint="So Jarvis finds the nearby business when it looks up a phone number. A town and state or a ZIP code is enough."
          value={loc}
          placeholder="e.g. Springfield, IL"
          autoComplete="address-level2"
          disabled={!canEdit}
          onChange={(e) => setLocation(e.target.value)}
          wrapperClassName="flex-1"
        />
        {canEdit && (
          <Button type="submit" className="mt-6" loading={busy === 'household.location'} disabled={loc.trim() === savedLocation}>
            Save
          </Button>
        )}
      </form>
      <Select
        label="Time zone"
        hint="For reminders, schedules and “what time is it”. Automatic follows the zone your nodes report."
        value={savedZone}
        disabled={!canEdit || busy === 'household.timezone'}
        onValueChange={(z) => void write('household.timezone', z, 'Time zone saved')}
        options={[{ value: '', label: automatic }, ...zones.map((z) => ({ value: z, label: zoneLabel(z) }))]}
      />
    </Card>
  )
}
