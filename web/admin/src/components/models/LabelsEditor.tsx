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
import GpuFaultBanner from './GpuFaultBanner'
import { Badge, Banner, Button, ErrorState, SkeletonList } from '@/components/ui'
import { fieldControl } from '@/components/ui/fieldStyles'
import ModelCard from './ModelCard'
import { stateBadge } from './tone'

/** Engine modes each label may take (06 §3). */
const MODES: Record<EngineLabel, string[]> = {
  live: ['local', 'remote', 'off'],
  background: ['local', 'shared', 'remote', 'off'],
  embeddings: ['local', 'remote', 'off'],
  stt: ['local', 'remote', 'off'],
  cloud: ['remote', 'off'],
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
  gpu_unavailable: 'GPU unavailable',
}

const BUILD_KIND: Record<EngineLabel, string> = {
  live: 'llama-server',
  background: 'llama-server',
  embeddings: 'llama-server',
  stt: 'whisper-server',
  cloud: 'llama-server',
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

// Native selects and fields with the shared field look (fieldStyles), for the dense label forms.
const selectClass = fieldControl
const inputClass = fieldControl

/** Engines that run a Hugging Face model folder (text-only). */
const PY_ENGINES = ['mlx-lm', 'vllm', 'transformers']

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
          {m.directory ? ' · model folder' : ''}
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
          <label key={key} className="flex min-h-10 items-center gap-2 text-small text-text">
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
      <span className="self-center text-small text-text-muted">{chosen.size === 0 ? '(auto)' : ''}</span>
    </div>
  )
}

/**
 * ImageInputToggle shows llm.<slot>.mmproj as an on/off switch (chat-images CI1): on = ""
 * (the model's own projector), off = "none". An explicit projector id or path keeps the raw
 * editor under Advanced. `running` is whether the engine running now has vision.
 */
export function ImageInputToggle({
  id,
  label,
  mmproj,
  running,
  onChange,
}: {
  id: string
  label: 'live' | 'background'
  mmproj: string
  running?: boolean
  onChange: (mmproj: string) => void
}) {
  const simple = mmproj === '' || mmproj === 'none'
  const runningText =
    running === undefined ? 'Not running' : running ? 'Running engine has image input' : 'Running engine has no image input'
  return (
    <div className="space-y-1" data-testid={`image-input-${label}`}>
      {simple ? (
        <label htmlFor={id} className="flex min-h-10 items-center gap-2 text-small font-medium text-text">
          <input
            id={id}
            type="checkbox"
            role="switch"
            checked={mmproj === ''}
            onChange={(e) => onChange(e.target.checked ? '' : 'none')}
          />
          Image input
        </label>
      ) : (
        <p className="text-small text-text">
          Image input: projector <code>{mmproj}</code> (change it under Advanced)
        </p>
      )}
      <p className="text-small text-text-muted">
        {runningText}.{' '}
        {simple && "On uses the model's own vision projector when it has one; off frees its memory."}
        {label === 'live' && ' Chat image upload in the app needs this on.'}
        {label === 'background' && ' Off also turns off LLM vision in recipe photo import, and chat images are then described by the live model.'}
      </p>
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
  const isSTT = cfg.model_kind === 'stt'
  const kindModels = models.filter((m) => m.kind === LABEL_KIND[label])
  const projectors = models.filter((m) => m.kind === 'mmproj')
  // A Python engine (a Hugging Face model folder) takes no projector: the label is text-only.
  const pyEngine = PY_ENGINES.includes(cfg.kind)

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

  // ID12: strict chat templates (Qwen 3.5/3.8) reject a system message that isn't first.
  const foldField = () => (
    <Field
      label="Later system messages"
      htmlFor={id('fold_system_messages')}
      hint={cfg.fold_system_messages_effective ? 'Folded into the user turn now' : 'Sent as system messages now'}
    >
      <select
        id={id('fold_system_messages')}
        value={v('fold_system_messages') ?? 'auto'}
        onChange={(e) => set({ fold_system_messages: e.target.value })}
        className={selectClass}
      >
        <option value="auto">{mode === 'remote' ? 'auto (send as is)' : "auto (the model's catalog entry or folder)"}</option>
        <option value="on">fold into the user turn</option>
        <option value="off">send as is</option>
      </select>
    </Field>
  )

  return (
    <div className="space-y-4 py-5 first:pt-0 last:pb-0" data-testid={`label-${label}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-subheading text-text">{LABEL_TITLE[label]}</h3>
          <code className="font-mono text-small text-text-muted">{label}</code>
          <Badge tone={stateBadge(status.state)} title={status.reason || undefined}>
            {STATE_TEXT[status.state] ?? status.state}
          </Badge>
          {status.endpoint?.vision && <Badge>vision</Badge>}
          {status.endpoint?.engine && <Badge title="Engine instance (shared labels name the same one)">{status.endpoint.engine}</Badge>}
        </div>
        {status.endpoint && status.endpoint.context_length > 0 && (
          <span className="text-small text-text-muted">
            {status.endpoint.context_length.toLocaleString()} ctx × {status.endpoint.parallel}
          </span>
        )}
      </div>
      {(status.reason || cfg.problem) && (
        <p className="text-small text-warning">{cfg.problem || status.reason}</p>
      )}
      {cfg.note && <p className="text-small text-text-muted">{cfg.note}</p>}
      {label === 'cloud' && (
        <p className="text-small text-text-muted">
          A provider's model, next to the ones on this machine. Nothing uses it until you pick it for a feature under
          Feature models; leave the URL empty to keep everything on your network.
        </p>
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
        <div className="space-y-3">
          <Banner tone="warning" icon={<Globe size={16} />}>
            {label === 'cloud'
              ? 'Requests from the features you set to cloud leave this machine: their prompts and any images go to the endpoint below. Use it only with a provider you trust.'
              : isSTT
                ? 'The audio of every voice turn leaves this machine (twice when it starts with the wake word): it goes to the endpoint below to be transcribed, and each turn waits for it. Speaker recognition stays on this machine. Use it only with a provider you trust.'
                : `Requests for this label leave this machine: prompts, conversation text${isLLM ? ' and any images' : ''} go to the endpoint below. Use it only with a provider you trust.`}
          </Banner>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="Base URL" htmlFor={id('remote_url')}>
              <input
                id={id('remote_url')}
                value={v('remote_url')}
                onChange={(e) => set({ remote_url: e.target.value })}
                placeholder={isSTT ? 'https://api.openai.com/v1' : 'https://api.example.com/v1'}
                className={inputClass}
              />
            </Field>
            {isLLM && (
              <Field
                label="API"
                htmlFor={id('remote_api')}
                hint={cfg.remote_api_effective === 'anthropic' ? 'Anthropic Messages API now' : 'OpenAI-compatible now'}
              >
                <select
                  id={id('remote_api')}
                  value={v('remote_api') || 'auto'}
                  onChange={(e) => set({ remote_api: e.target.value })}
                  className={selectClass}
                >
                  <option value="auto">auto (Anthropic for api.anthropic.com)</option>
                  <option value="openai">OpenAI-compatible (/chat/completions)</option>
                  <option value="anthropic">Anthropic Messages API (/v1/messages)</option>
                </select>
              </Field>
            )}
            <Field
              label="Model name"
              htmlFor={id('remote_model')}
              hint={isSTT ? 'e.g. gpt-transcribe (OpenAI) or whisper-large-v3-turbo (Groq, at https://api.groq.com/openai/v1)' : undefined}
            >
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
              <label className="flex min-h-10 items-center gap-2 self-end text-small text-text">
                <input type="checkbox" checked={v('remote_vision')} onChange={(e) => set({ remote_vision: e.target.checked })} />
                The remote model accepts images
              </label>
            )}
            {isSTT && (
              <Field label="Timeout (ms)" htmlFor={id('remote_timeout_ms')} hint="How long a voice turn waits for the transcript">
                <input
                  id={id('remote_timeout_ms')}
                  type="number"
                  min={0}
                  step={1000}
                  value={v('remote_timeout_ms') ?? 0}
                  onChange={(e) => set({ remote_timeout_ms: num(e.target.value) })}
                  className={inputClass}
                />
              </Field>
            )}
            {isLLM && foldField()}
            {isLLM && (
              <Field label="Context window" htmlFor={id('remote_context')} hint="Tokens; 0 = unknown (conversation compaction stays off)">
                <input
                  id={id('remote_context')}
                  type="number"
                  min={0}
                  step={1024}
                  value={v('context')}
                  onChange={(e) => set({ context: num(e.target.value) })}
                  className={inputClass}
                />
              </Field>
            )}
          </div>
        </div>
      )}

      {mode === 'local' && isLLM && !pyEngine && (label === 'live' || label === 'background') && (
        <ImageInputToggle
          id={id('image_input')}
          label={label}
          mmproj={v('mmproj')}
          running={status.endpoint ? status.endpoint.vision : undefined}
          onChange={(mmproj) => set({ mmproj })}
        />
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

          <Button
            variant="ghost"
            size="sm"
            className="-ml-3"
            icon={<SlidersHorizontal size={14} aria-hidden />}
            onClick={() => setAdvanced(!advanced)}
            aria-expanded={advanced}
          >
            {advanced ? 'Hide advanced' : 'Advanced'}
          </Button>

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
              {cfg.kind === 'vllm' && (
                <Field label="GPU memory share" htmlFor={id('gpu_memory_utilization')} hint="vLLM preallocates this share of the card; 0 = sized to the model">
                  <input
                    id={id('gpu_memory_utilization')}
                    type="number"
                    min={0}
                    max={0.95}
                    step={0.01}
                    value={v('gpu_memory_utilization') ?? 0}
                    onChange={(e) => set({ gpu_memory_utilization: Math.min(0.95, Math.max(0, Number(e.target.value) || 0)) })}
                    className={inputClass}
                  />
                </Field>
              )}
              {pyEngine && (
                <Field label="Directory engine" htmlFor={id('dir_engine')} hint="auto = MLX on Apple Silicon, vLLM on Linux, else Transformers">
                  <select id={id('dir_engine')} value={v('dir_engine') || 'auto'} onChange={(e) => set({ dir_engine: e.target.value })} className={selectClass}>
                    {['auto', 'mlx-lm', 'transformers', 'vllm'].map((f) => (
                      <option key={f}>{f}</option>
                    ))}
                  </select>
                </Field>
              )}
              {cfg.kind === 'transformers' && (
                <Field label="Precision" htmlFor={id('dtype')} hint="auto = as the model is stored">
                  <select id={id('dtype')} value={v('dtype') || 'auto'} onChange={(e) => set({ dtype: e.target.value })} className={selectClass}>
                    {['auto', 'bfloat16', 'float16', 'float32'].map((f) => (
                      <option key={f}>{f}</option>
                    ))}
                  </select>
                </Field>
              )}
              {cfg.kind === 'transformers' && (
                <Field label="Quantization" htmlFor={id('quantization')} hint="bitsandbytes, CUDA only">
                  <select id={id('quantization')} value={v('quantization') || 'none'} onChange={(e) => set({ quantization: e.target.value })} className={selectClass}>
                    {['none', 'bnb-4bit', 'bnb-8bit'].map((f) => (
                      <option key={f}>{f}</option>
                    ))}
                  </select>
                </Field>
              )}
              {isLLM && (
                <Field label="KV cache" htmlFor={id('kv_cache_type')}>
                  <select id={id('kv_cache_type')} value={v('kv_cache_type')} onChange={(e) => set({ kv_cache_type: e.target.value })} className={selectClass}>
                    {['f16', 'q8_0', 'q4_0'].map((f) => (
                      <option key={f}>{f}</option>
                    ))}
                  </select>
                </Field>
              )}
              {isLLM && !pyEngine && (
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
              {isLLM && foldField()}
              <Field label="Extra engine flags" htmlFor={id('extra_args')}>
                <input id={id('extra_args')} value={v('extra_args')} onChange={(e) => set({ extra_args: e.target.value })} className={inputClass} />
              </Field>
            </div>
          )}
        </>
      )}

      {dirty && (
        <div className="flex justify-end gap-2">
          <Button onClick={() => setDraft({})} disabled={put.isPending}>
            Reset
          </Button>
          <Button variant="primary" onClick={save} loading={put.isPending}>
            Save
          </Button>
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
    <div className="space-y-3 py-5 first:pt-0 last:pb-0" data-testid={`label-${label}`}>
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-subheading text-text">{LABEL_TITLE[label]}</h3>
        <code className="font-mono text-small text-text-muted">{label}</code>
        <Badge tone={voice.problem ? 'warning' : 'success'}>{voice.problem ?? 'ready'}</Badge>
        <span className="text-small text-text-muted">runs inside jarvisd (CPU)</span>
      </div>
      <div className="flex flex-wrap gap-2">
        <div className="min-w-0 flex-1 basis-60">
          <label htmlFor={`label-${label}-model`} className="sr-only">
            {LABEL_TITLE[label]} model
          </label>
          <ModelSelect id={`label-${label}-model`} value={value} models={kindModels} onChange={setDraft} />
        </div>
        {draft !== null && draft !== (voice.id ?? '') && (
          <Button variant="primary" onClick={save} loading={put.isPending}>
            Save
          </Button>
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
  const { data, isLoading, isError, error, refetch } = useLabels()
  const { data: installed } = useInstalled()
  const { data: hw } = useHardware()
  const models = installed?.models ?? []
  const devices = hw?.hardware.devices ?? []

  return (
    <ModelCard
      title="Labels"
      icon={SlidersHorizontal}
      description="What each job runs on. Changes apply without restarting jarvisd; only the affected engine restarts."
    >
      {isLoading && <SkeletonList rows={3} label="Loading the labels" />}
      {isError && (
        <ErrorState
          className="py-6"
          title="Couldn't load the labels"
          message={errorMessage(error, 'The server did not answer.')}
          onRetry={() => refetch()}
        />
      )}
      {data && (
        <div className="space-y-4">
          <GpuFaultBanner fault={data.gpu_fault} />
          {(data.warnings ?? []).map((w) => (
            <Banner key={w} tone="warning" icon={<AlertTriangle size={16} />}>
              {w}
            </Banner>
          ))}
          <div className="divide-y divide-border">
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
        </div>
      )}
    </ModelCard>
  )
}
