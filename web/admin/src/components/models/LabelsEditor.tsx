import { useState, type ReactNode } from 'react'
import { AlertTriangle, Globe, SlidersHorizontal } from 'lucide-react'
import { toast } from 'sonner'
import {
  LABEL_KIND,
  LABEL_TITLE,
  type Device,
  type EngineLabel,
  type InstalledModel,
  type Label,
  type LabelConfig,
  type LabelStatus,
  type LabelUpdate,
  type VoiceModel,
} from '@/api/llm'
import { useHardware, useInstalled, useLabels, usePutLabels } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { labelDraftDiff } from './logic'
import { buttonClass, inputClass, stateTone } from './styles'
import { Pill, Section } from './ui'

/** Engine modes each label may take (06 §3). */
const MODES: Record<EngineLabel, string[]> = {
  live: ['local', 'remote', 'off'],
  background: ['local', 'shared', 'remote', 'off'],
  embeddings: ['local', 'remote', 'off'],
  stt: ['local', 'off'],
}

const MODE_TITLE: Record<string, string> = {
  local: 'On this machine',
  shared: "Share live's engine",
  remote: 'Remote endpoint',
  off: 'Off',
}

const STATE_TEXT: Record<string, string> = {
  not_configured: 'not configured',
  fetching_engine: 'fetching engine',
  no_engine_build: 'no engine build',
}

const BUILD_KIND: Record<EngineLabel, string> = {
  live: 'llama-server',
  background: 'llama-server',
  embeddings: 'llama-server',
  stt: 'whisper-server',
}

function Field({ label, htmlFor, hint, children }: { label: string; htmlFor: string; hint?: string; children: ReactNode }) {
  return (
    <div className="space-y-1">
      <label htmlFor={htmlFor} className="block text-xs font-medium text-[var(--color-text-muted)]">
        {label}
      </label>
      {children}
      {hint && <p className="text-[11px] text-[var(--color-text-muted)]">{hint}</p>}
    </div>
  )
}

const selectClass = inputClass

function ModelSelect({
  id,
  value,
  models,
  onChange,
  allowEmpty = true,
}: {
  id: string
  value: string
  models: InstalledModel[]
  onChange: (v: string) => void
  allowEmpty?: boolean
}) {
  const known = models.some((m) => m.id === value)
  return (
    <select id={id} value={value} onChange={(e) => onChange(e.target.value)} className={selectClass}>
      {allowEmpty && <option value="">(none)</option>}
      {!known && value && <option value={value}>{value}</option>}
      {models.map((m) => (
        <option key={m.id} value={m.id} disabled={m.state !== 'ready'}>
          {m.display && m.display !== m.id ? `${m.display} (${m.id})` : m.id}
          {m.state !== 'ready' ? ` [${m.state}]` : ''}
        </option>
      ))}
    </select>
  )
}

/** DevicePicker sets gpu_devices ("" = auto, "1", "0,1") from the detected cards. */
function DevicePicker({ id, value, devices, onChange }: { id: string; value: string; devices: Device[]; onChange: (v: string) => void }) {
  if (devices.length <= 1) {
    return (
      <input id={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder="auto" className={inputClass} />
    )
  }
  const chosen = new Set(value.split(',').map((s) => s.trim()).filter(Boolean))
  return (
    <div id={id} className="flex flex-wrap gap-3">
      {devices.map((d) => {
        const key = String(d.index)
        return (
          <label key={key} className="flex items-center gap-1.5 text-xs text-[var(--color-text)]">
            <input
              type="checkbox"
              checked={chosen.has(key)}
              onChange={(e) => {
                const next = new Set(chosen)
                if (e.target.checked) next.add(key)
                else next.delete(key)
                onChange([...next].sort().join(','))
              }}
            />
            {d.index}: {d.name}
          </label>
        )
      })}
      <span className="text-[11px] text-[var(--color-text-muted)]">{chosen.size === 0 ? '(auto)' : ''}</span>
    </div>
  )
}

function EngineLabelCard({
  status,
  models,
  devices,
  flavours,
}: {
  status: LabelStatus
  models: InstalledModel[]
  devices: Device[]
  flavours: string[]
}) {
  const label = status.label as EngineLabel
  const cfg = status.config
  const put = usePutLabels()
  const [draft, setDraft] = useState<LabelUpdate>({})
  const [advanced, setAdvanced] = useState(false)
  const isLLM = cfg.model_kind === 'llm'
  const kindModels = models.filter((m) => m.kind === LABEL_KIND[label])
  const projectors = models.filter((m) => m.kind === 'mmproj')

  const v = <K extends keyof LabelConfig & keyof LabelUpdate>(k: K) =>
    (draft[k] !== undefined ? draft[k] : cfg[k]) as LabelConfig[K]
  const set = (patch: LabelUpdate) => setDraft((d) => ({ ...d, ...patch }))
  const mode = v('engine')
  const diff = labelDraftDiff(cfg, draft)
  const dirty = Object.keys(diff).length > 0
  const id = (f: string) => `label-${label}-${f}`

  function save() {
    put.mutate(
      { [label]: diff },
      {
        onSuccess: () => {
          toast.success(`${LABEL_TITLE[label]} saved; its engine restarts if needed`)
          setDraft({})
        },
        onError: (err) => toast.error(`${LABEL_TITLE[label]}: ${errorMessage(err)}`),
      },
    )
  }

  const num = (s: string) => (s === '' ? 0 : Math.max(0, Math.floor(Number(s)) || 0))

  return (
    <div className="space-y-3 rounded-lg border border-[var(--color-border)] p-3" data-testid={`label-${label}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium text-[var(--color-text)]">{LABEL_TITLE[label]}</span>
          <code className="text-xs text-[var(--color-text-muted)]">{label}</code>
          <Pill tone={stateTone(status.state)} title={status.reason || undefined}>
            {STATE_TEXT[status.state] ?? status.state}
          </Pill>
          {status.endpoint?.vision && <Pill>vision</Pill>}
          {status.endpoint?.engine && <Pill title="Engine instance (shared labels name the same one)">{status.endpoint.engine}</Pill>}
        </div>
        {status.endpoint && status.endpoint.context_length > 0 && (
          <span className="text-xs text-[var(--color-text-muted)]">
            {status.endpoint.context_length.toLocaleString()} ctx × {status.endpoint.parallel}
          </span>
        )}
      </div>
      {(status.reason || cfg.problem) && (
        <p className="text-xs text-amber-500">{cfg.problem || status.reason}</p>
      )}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label="Runs" htmlFor={id('engine')}>
          <select id={id('engine')} value={mode} onChange={(e) => set({ engine: e.target.value })} className={selectClass}>
            {MODES[label].map((m) => (
              <option key={m} value={m}>
                {MODE_TITLE[m]}
              </option>
            ))}
          </select>
        </Field>

        {mode === 'local' && (
          <Field label="Model" htmlFor={id('model')} hint={kindModels.length === 0 ? 'Install one from the catalog first.' : undefined}>
            <ModelSelect id={id('model')} value={v('model')} models={kindModels} onChange={(m) => set({ model: m })} />
          </Field>
        )}
      </div>

      {mode === 'remote' && (
        <div className="space-y-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
          <p className="flex items-start gap-2 text-xs text-[var(--color-text)]">
            <Globe size={14} className="mt-0.5 shrink-0 text-amber-500" />
            Requests for this label leave this machine: prompts, conversation text
            {isLLM ? ' and any images' : ''} go to the endpoint below. Use it only with a provider you trust.
          </p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Base URL (OpenAI-compatible)" htmlFor={id('remote_url')}>
              <input
                id={id('remote_url')}
                value={v('remote_url')}
                onChange={(e) => set({ remote_url: e.target.value })}
                placeholder="https://api.example.com/v1"
                className={inputClass}
              />
            </Field>
            <Field label="Model name" htmlFor={id('remote_model')}>
              <input
                id={id('remote_model')}
                value={v('remote_model')}
                onChange={(e) => set({ remote_model: e.target.value })}
                className={inputClass}
              />
            </Field>
            <Field label="API key" htmlFor={id('remote_api_key')} hint="Write-only: leave empty to keep the stored key.">
              <input
                id={id('remote_api_key')}
                type="password"
                autoComplete="off"
                value={draft.remote_api_key ?? ''}
                onChange={(e) => set({ remote_api_key: e.target.value })}
                className={inputClass}
              />
            </Field>
            {isLLM && (
              <label className="flex items-center gap-2 self-end text-xs text-[var(--color-text)]">
                <input type="checkbox" checked={v('remote_vision')} onChange={(e) => set({ remote_vision: e.target.checked })} />
                The remote model accepts images
              </label>
            )}
          </div>
        </div>
      )}

      {mode === 'local' && (
        <>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {cfg.model_kind !== 'stt' && (
              <Field label="Context" htmlFor={id('context')} hint="0 = the model's default">
                <input
                  id={id('context')}
                  type="number"
                  min={0}
                  step={1024}
                  value={v('context')}
                  onChange={(e) => set({ context: num(e.target.value) })}
                  className={inputClass}
                />
              </Field>
            )}
            {cfg.model_kind !== 'stt' && (
              <Field label="Parallel slots" htmlFor={id('parallel')}>
                <input
                  id={id('parallel')}
                  type="number"
                  min={1}
                  value={v('parallel')}
                  onChange={(e) => set({ parallel: Math.max(1, num(e.target.value)) })}
                  className={inputClass}
                />
              </Field>
            )}
            <Field label="GPU devices" htmlFor={id('gpu_devices')}>
              <DevicePicker id={id('gpu_devices')} value={v('gpu_devices')} devices={devices} onChange={(d) => set({ gpu_devices: d })} />
            </Field>
          </div>

          <button
            type="button"
            className="flex items-center gap-1 text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
            onClick={() => setAdvanced(!advanced)}
            aria-expanded={advanced}
          >
            <SlidersHorizontal size={12} /> {advanced ? 'Hide advanced' : 'Advanced'}
          </button>

          {advanced && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <Field label="GPU backend" htmlFor={id('gpu_backend')}>
                <select id={id('gpu_backend')} value={v('gpu_backend')} onChange={(e) => set({ gpu_backend: e.target.value })} className={selectClass}>
                  {['auto', ...flavours.filter((f) => f !== 'auto')].map((f) => (
                    <option key={f} value={f}>
                      {f}
                    </option>
                  ))}
                  {!['auto', ...flavours].includes(v('gpu_backend')) && <option value={v('gpu_backend')}>{v('gpu_backend')}</option>}
                </select>
              </Field>
              <Field label="GPU layers" htmlFor={id('gpu_layers')} hint={cfg.model_kind === 'stt' ? '0 = CPU only' : '999 = all; 0 = CPU only'}>
                <input
                  id={id('gpu_layers')}
                  type="number"
                  min={0}
                  value={v('gpu_layers')}
                  onChange={(e) => set({ gpu_layers: num(e.target.value) })}
                  className={inputClass}
                />
              </Field>
              <Field label="Flash attention" htmlFor={id('flash_attn')}>
                <select id={id('flash_attn')} value={v('flash_attn')} onChange={(e) => set({ flash_attn: e.target.value })} className={selectClass}>
                  {['auto', 'on', 'off'].map((f) => (
                    <option key={f}>{f}</option>
                  ))}
                </select>
              </Field>
              {isLLM && (
                <Field label="KV cache" htmlFor={id('kv_cache_type')}>
                  <select id={id('kv_cache_type')} value={v('kv_cache_type')} onChange={(e) => set({ kv_cache_type: e.target.value })} className={selectClass}>
                    {['f16', 'q8_0', 'q4_0'].map((f) => (
                      <option key={f}>{f}</option>
                    ))}
                  </select>
                </Field>
              )}
              {isLLM && (
                <Field label="Vision projector" htmlFor={id('mmproj')} hint="Empty = the model's own, if installed">
                  <select id={id('mmproj')} value={v('mmproj')} onChange={(e) => set({ mmproj: e.target.value })} className={selectClass}>
                    <option value="">(model's own)</option>
                    <option value="none">none (no vision)</option>
                    {projectors.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.id}
                      </option>
                    ))}
                    {v('mmproj') && v('mmproj') !== 'none' && !projectors.some((p) => p.id === v('mmproj')) && (
                      <option value={v('mmproj')}>{v('mmproj')}</option>
                    )}
                  </select>
                </Field>
              )}
              {cfg.model_kind !== 'stt' && devices.length > 1 && (
                <>
                  <Field label="Split mode" htmlFor={id('split_mode')}>
                    <select id={id('split_mode')} value={v('split_mode')} onChange={(e) => set({ split_mode: e.target.value })} className={selectClass}>
                      <option value="">(default)</option>
                      {['none', 'layer', 'row'].map((f) => (
                        <option key={f}>{f}</option>
                      ))}
                    </select>
                  </Field>
                  <Field label="Tensor split" htmlFor={id('tensor_split')} hint='e.g. "1,1"'>
                    <input id={id('tensor_split')} value={v('tensor_split')} onChange={(e) => set({ tensor_split: e.target.value })} className={inputClass} />
                  </Field>
                </>
              )}
              <Field label="Extra engine flags" htmlFor={id('extra_args')}>
                <input id={id('extra_args')} value={v('extra_args')} onChange={(e) => set({ extra_args: e.target.value })} className={inputClass} />
              </Field>
            </div>
          )}
        </>
      )}

      {dirty && (
        <div className="flex justify-end gap-2">
          <button type="button" className={buttonClass.secondary} onClick={() => setDraft({})} disabled={put.isPending}>
            Reset
          </button>
          <button type="button" className={buttonClass.primary} onClick={save} disabled={put.isPending}>
            {put.isPending ? 'Saving…' : 'Save'}
          </button>
        </div>
      )}
    </div>
  )
}

function VoiceLabelCard({ voice, models }: { voice: VoiceModel; models: InstalledModel[] }) {
  const label = voice.label as Label
  const put = usePutLabels()
  const kindModels = models.filter((m) => m.kind === LABEL_KIND[label])
  const [draft, setDraft] = useState<string | null>(null)
  const value = draft ?? voice.id ?? ''

  function save() {
    put.mutate(
      { [label]: { model: value } },
      {
        onSuccess: () => {
          toast.success(`${LABEL_TITLE[label]} saved`)
          setDraft(null)
        },
        onError: (err) => toast.error(`${LABEL_TITLE[label]}: ${errorMessage(err)}`),
      },
    )
  }

  return (
    <div className="space-y-2 rounded-lg border border-[var(--color-border)] p-3" data-testid={`label-${label}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium text-[var(--color-text)]">{LABEL_TITLE[label]}</span>
        <code className="text-xs text-[var(--color-text-muted)]">{label}</code>
        <Pill tone={voice.problem ? 'warn' : 'ok'}>{voice.problem ?? 'ready'}</Pill>
        <span className="text-xs text-[var(--color-text-muted)]">runs inside jarvisd (CPU)</span>
      </div>
      <div className="flex gap-2">
        <div className="flex-1">
          <label htmlFor={`label-${label}-model`} className="sr-only">
            {LABEL_TITLE[label]} model
          </label>
          <ModelSelect id={`label-${label}-model`} value={value} models={kindModels} onChange={setDraft} />
        </div>
        {draft !== null && draft !== (voice.id ?? '') && (
          <button type="button" className={buttonClass.primary} onClick={save} disabled={put.isPending}>
            {put.isPending ? 'Saving…' : 'Save'}
          </button>
        )}
      </div>
    </div>
  )
}

/**
 * LabelsEditor assigns models to labels and tunes their engines, through the one validated
 * write path (`PUT /api/llm/v1/models/labels`, I4). `labels` narrows which cards show (the A7
 * wizard's Models step can show just live/background/stt).
 */
export default function LabelsEditor({ labels: only }: { labels?: Label[] }) {
  const { data, isLoading, isError, error } = useLabels()
  const { data: installed } = useInstalled()
  const { data: hw } = useHardware()
  const models = installed?.models ?? []
  const devices = hw?.hardware.devices ?? []

  return (
    <Section
      title="Labels"
      icon={SlidersHorizontal}
      description="What each job runs on. Changes apply without restarting jarvisd; only the affected engine restarts."
    >
      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Loading…</p>}
      {isError && <p className="text-sm text-red-500">{errorMessage(error, 'Could not load the labels')}</p>}
      {data && (
        <div className="space-y-3">
          {(data.warnings ?? []).map((w) => (
            <p key={w} className="flex items-start gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-xs text-amber-500">
              <AlertTriangle size={14} className="mt-0.5 shrink-0" />
              {w}
            </p>
          ))}
          {data.labels
            .filter((l) => !only || only.includes(l.label as Label))
            .map((l) => (
              <EngineLabelCard
                key={l.label}
                status={l}
                models={models}
                devices={devices}
                flavours={hw?.builds[BUILD_KIND[l.label as EngineLabel]]?.flavours ?? []}
              />
            ))}
          {(data.voice ?? [])
            .filter((v) => !only || only.includes(v.label as Label))
            .map((v) => (
              <VoiceLabelCard key={v.label} voice={v} models={models} />
            ))}
        </div>
      )}
    </Section>
  )
}
