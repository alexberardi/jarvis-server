import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Globe, Play, Volume2 } from 'lucide-react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { engineText, latencyText } from '@/api/speech'
import { getServiceSettings, updateSetting } from '@/api/settings'
import { getVoices, sampleVoice } from '@/api/tts'
import { mmKeys, useLabels } from '@/hooks/useModelManager'
import { errorMessage, errorStatus } from '@/lib/errors'
import { Banner, Button, ErrorState, SkeletonList } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import type { AggregatedSettingsResponse } from '@/types/settings'
import ModelCard from './ModelCard'
import SpeechPresetPicker from './SpeechPresetPicker'

const ttsSettingsKey = ['settings', 'tts'] as const
const voicesKey = ['tts', 'voices'] as const

/** The editable tts settings (docs/llm/07-remote-speech.md §3), without their tts. prefix. */
type Draft = Partial<{
  provider: string
  remote_url: string
  remote_api: string
  remote_model: string
  remote_api_key: string
  remote_timeout_ms: number
  fallback: string
  voice: string
}>

function settingValue(data: AggregatedSettingsResponse | undefined, key: string): unknown {
  for (const svc of data?.services ?? []) {
    const s = svc.settings.find((x) => x.key === key)
    if (s) return s.value
  }
  return undefined
}

function Field({ label, htmlFor, hint, children }: { label: string; htmlFor: string; hint?: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-1.5">
      <label htmlFor={htmlFor} className="block text-small font-medium text-text">
        {label}
      </label>
      {children}
      {hint && <p className="text-small text-text-muted">{hint}</p>}
    </div>
  )
}

/**
 * SpeechCard is the Models page's text-to-speech half of the Speech section
 * (docs/llm/07-remote-speech.md §4): where speaking runs (Kokoro on this machine, or a remote
 * provider with its preset/URL/model/key), the one voice everything is spoken with, the
 * fallback to local Kokoro, and a test that plays a sample and says which engine rendered it.
 * The Kokoro model itself is assigned under Labels; this card is the provider and voice.
 */
export default function SpeechCard() {
  const qc = useQueryClient()
  const settings = useQuery({ queryKey: ttsSettingsKey, queryFn: () => getServiceSettings('tts') })
  const voices = useQuery({ queryKey: voicesKey, queryFn: getVoices, staleTime: 60_000 })
  const labels = useLabels()
  const [draft, setDraft] = useState<Draft>({})

  const str = (key: string, def = '') => {
    const v = settingValue(settings.data, key)
    return typeof v === 'string' && v !== '' ? v : def
  }
  const saved: Required<Omit<Draft, 'voice'>> & { voice: string } = {
    provider: str('tts.provider', 'kokoro'),
    remote_url: str('tts.remote_url'),
    remote_api: str('tts.remote_api', 'openai'),
    remote_model: str('tts.remote_model'),
    remote_api_key: '', // write-only: the server never sends it back
    remote_timeout_ms: Number(settingValue(settings.data, 'tts.remote_timeout_ms') ?? 8000),
    fallback: str('tts.fallback', 'off'),
    voice: voices.data?.current ?? '',
  }
  const v = <K extends keyof Draft>(k: K) => (draft[k] !== undefined ? draft[k] : saved[k]) as NonNullable<Draft[K]>
  const set = (patch: Draft) => setDraft((d) => ({ ...d, ...patch }))
  const diff: Draft = {}
  for (const k of Object.keys(draft) as (keyof Draft)[]) {
    if (draft[k] !== undefined && draft[k] !== saved[k]) (diff as Record<string, unknown>)[k] = draft[k]
  }
  if (diff.remote_api_key === '') delete diff.remote_api_key
  const dirty = Object.keys(diff).length > 0
  const remote = v('provider') === 'remote'

  const save = useMutation({
    mutationFn: async (d: Draft) => {
      // The voice goes to the key the voices response names (tts.kokoro_voice or
      // tts.remote_voice, per the saved provider); everything else to its tts.* key.
      const writes: Array<[string, unknown]> = []
      for (const k of ['provider', 'remote_url', 'remote_api', 'remote_model', 'remote_api_key', 'remote_timeout_ms', 'fallback'] as const) {
        if (d[k] !== undefined) writes.push([`tts.${k}`, d[k]])
      }
      if (d.voice !== undefined) {
        const setting = voices.data?.setting ?? 'tts/tts.kokoro_voice'
        writes.push([setting.slice(setting.indexOf('/') + 1), d.voice])
      }
      for (const [key, value] of writes) await updateSetting('tts', key, value)
    },
    onSuccess: () => {
      toast.success('Speaking saved')
      setDraft({})
      for (const key of [ttsSettingsKey, voicesKey, mmKeys.labels]) void qc.invalidateQueries({ queryKey: key })
    },
    onError: (err) => toast.error(errorMessage(err, 'Could not save the speech settings')),
  })

  const ttsVoice = labels.data?.voice?.find((x) => x.label === 'tts')
  const fb = ttsVoice?.fallback
  const voiceNames = voices.data?.names ?? {}
  const voiceList = voices.data?.voices ?? []
  const freeText = voices.data?.free_text ?? false

  return (
    <ModelCard
      title="Speaking"
      icon={Volume2}
      description="Where Jarvis renders speech, and the one voice everything is spoken with — phone calls included."
      data-testid="speech-card"
    >
      {settings.isLoading && <SkeletonList rows={2} label="Loading the speech settings" />}
      {settings.isError && (
        <ErrorState
          className="py-6"
          title="Couldn't load the speech settings"
          message={errorMessage(settings.error, 'The server did not answer.')}
          onRetry={() => settings.refetch()}
        />
      )}
      {settings.data && (
        <div className="space-y-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Runs" htmlFor="speech-provider">
              <select
                id="speech-provider"
                value={v('provider')}
                onChange={(e) => set({ provider: e.target.value })}
                className={fieldControl}
              >
                <option value="kokoro">On this machine (Kokoro)</option>
                <option value="remote">Remote endpoint</option>
              </select>
            </Field>
          </div>

          {remote && (
            <div className="space-y-3">
              <Banner tone="warning" icon={<Globe size={16} />}>
                Everything Jarvis says leaves this machine as text: each sentence goes to the endpoint below to be
                rendered, and the reply waits for it. Use it only with a provider you trust.
              </Banner>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <SpeechPresetPicker
                  id="speech-preset"
                  kind="tts"
                  onPick={(p) =>
                    set({ remote_url: p.url, remote_api: p.api, remote_model: p.tts_model ?? '', voice: p.tts_voice })
                  }
                />
                <Field
                  label="API"
                  htmlFor="speech-remote-api"
                  hint={v('remote_api') === 'elevenlabs' ? 'ElevenLabs API' : 'OpenAI-compatible'}
                >
                  <select
                    id="speech-remote-api"
                    value={v('remote_api') === 'elevenlabs' ? 'elevenlabs' : 'openai'}
                    onChange={(e) => set({ remote_api: e.target.value })}
                    className={fieldControl}
                  >
                    <option value="openai">OpenAI-compatible (/audio/speech)</option>
                    <option value="elevenlabs">ElevenLabs (/v1/text-to-speech)</option>
                  </select>
                </Field>
                <Field label="Base URL" htmlFor="speech-remote-url">
                  <input
                    id="speech-remote-url"
                    value={v('remote_url')}
                    onChange={(e) => set({ remote_url: e.target.value })}
                    placeholder="https://api.openai.com/v1"
                    className={fieldControl}
                  />
                </Field>
                <Field label="Model name" htmlFor="speech-remote-model">
                  <input
                    id="speech-remote-model"
                    value={v('remote_model')}
                    onChange={(e) => set({ remote_model: e.target.value })}
                    className={fieldControl}
                  />
                </Field>
                <Field label="API key" htmlFor="speech-remote-key" hint="Write-only: leave empty to keep the stored key.">
                  <input
                    id="speech-remote-key"
                    type="password"
                    autoComplete="off"
                    value={draft.remote_api_key ?? ''}
                    onChange={(e) => set({ remote_api_key: e.target.value })}
                    className={fieldControl}
                  />
                </Field>
                <Field label="Timeout (ms)" htmlFor="speech-remote-timeout" hint="How long one sentence may take to render">
                  <input
                    id="speech-remote-timeout"
                    type="number"
                    min={0}
                    step={1000}
                    value={v('remote_timeout_ms')}
                    onChange={(e) => set({ remote_timeout_ms: Math.max(0, Math.floor(Number(e.target.value)) || 0) })}
                    className={fieldControl}
                  />
                </Field>
                <Field
                  label="If the provider fails"
                  htmlFor="speech-fallback"
                  hint="The fallback speaks the rest of the reply with local Kokoro, in its own voice."
                >
                  <select
                    id="speech-fallback"
                    value={v('fallback') || 'off'}
                    onChange={(e) => set({ fallback: e.target.value })}
                    className={fieldControl}
                  >
                    <option value="off">The reply goes unspoken</option>
                    <option value="cpu">Fall back to Kokoro on this machine</option>
                  </select>
                </Field>
              </div>
              {fb?.warning && (
                <Banner tone="warning" icon={<AlertTriangle size={16} />}>
                  {fb.warning}.{' '}
                  <Link to="/admin/models?job=tts&tab=catalog" className="font-medium underline underline-offset-2">
                    Install one from the catalog
                  </Link>
                </Banner>
              )}
              {fb && (fb.fallbacks > 0 || fb.last_remote_error) && (
                <p className="text-small text-text-muted">
                  {fb.fallbacks > 0 &&
                    `Kokoro answered ${fb.fallbacks} request${fb.fallbacks === 1 ? '' : 's'} for the remote since the last restart. `}
                  {fb.last_remote_error && `Last remote error: ${fb.last_remote_error}`}
                </p>
              )}
            </div>
          )}

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field
              label="Voice"
              htmlFor="speech-voice"
              hint={
                draft.provider !== undefined && draft.provider !== saved.provider
                  ? 'Save the provider first to list its voices.'
                  : freeText
                    ? "The provider lists no voices; type the voice's name or id."
                    : undefined
              }
            >
              {freeText ? (
                <input
                  id="speech-voice"
                  value={v('voice')}
                  onChange={(e) => set({ voice: e.target.value })}
                  className={fieldControl}
                />
              ) : (
                <select
                  id="speech-voice"
                  value={v('voice')}
                  disabled={!voices.data}
                  onChange={(e) => set({ voice: e.target.value })}
                  className={fieldControl}
                >
                  {v('voice') && !voiceList.includes(v('voice')) && <option value={v('voice')}>{v('voice')}</option>}
                  {voiceList.map((name) => (
                    <option key={name} value={name}>
                      {voiceNames[name] ?? name}
                      {name === voices.data?.default ? ' · default' : ''}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          </div>

          <TTSTest voice={saved.voice} dirty={dirty} />

          {dirty && (
            <div className="flex justify-end gap-2">
              <Button onClick={() => setDraft({})} disabled={save.isPending}>
                Reset
              </Button>
              <Button variant="primary" onClick={() => save.mutate(diff)} loading={save.isPending}>
                Save
              </Button>
            </div>
          )}
        </div>
      )}
    </ModelCard>
  )
}

/**
 * TTSTest plays a sample through the saved speech settings and says how long one sentence took
 * and which engine rendered it (the X-Speech-Engine header). Disabled while the form is dirty:
 * the sample uses what is saved.
 */
function TTSTest({ voice, dirty }: { voice: string; dirty: boolean }) {
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ latency: number; engine: string | null } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const url = useRef<string | null>(null)

  useEffect(
    () => () => {
      if (url.current) URL.revokeObjectURL(url.current)
    },
    [],
  )

  async function run() {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      const t0 = performance.now()
      const { blob, engine } = await sampleVoice(voice)
      setResult({ latency: performance.now() - t0, engine })
      if (url.current) URL.revokeObjectURL(url.current)
      url.current = URL.createObjectURL(blob)
      await new Audio(url.current).play()
    } catch (err) {
      setError(
        errorStatus(err) === 409 ? 'The voice model is not installed yet.' : errorMessage(err, 'The test failed'),
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-2" data-testid="tts-test">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={run} loading={busy} disabled={dirty || !voice} icon={<Play size={14} aria-hidden />}>
          Play test sentence
        </Button>
        {dirty && <span className="text-small text-text-muted">Save first: the test uses the saved settings.</span>}
      </div>
      {result && (
        <p role="status" className="text-small text-text">
          Rendered in {latencyText(result.latency)}
          {result.engine ? `, ${engineText(result.engine)}` : ''}.
        </p>
      )}
      {error && (
        <p role="alert" className="text-small text-danger">
          {error}
        </p>
      )}
    </div>
  )
}
