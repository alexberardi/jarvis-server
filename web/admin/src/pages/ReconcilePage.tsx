import { useState, useRef, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { CheckCircle2, Loader2, AlertTriangle, RefreshCw, GitMerge, Circle, Download, Lock, Unlock } from 'lucide-react'
import { cn } from '@/lib/utils'

type Phase = 'loading_options' | 'options' | 'regenerate' | 'pull' | 'apply' | 'done' | 'error'

interface ServiceOption {
  id: string
  name: string
  description: string
  category: string
  enabled: boolean
}

interface ReconcileOptions {
  services: ServiceOption[]
  relayEnabled: boolean
  relayUrl: string
  whisperModelPath: string
  whisperBackend?: 'cpu' | 'cuda' | 'vulkan' | 'rocm'
  ttsBackend?: 'cpu' | 'cuda'
  pinImages?: boolean
  releaseTrack: 'stable' | 'dev'
  bgModelEnabled?: boolean
  bgModelFile?: string
  bgModelSupported?: boolean
}

interface LogLine {
  text: string
  phase?: string
}

const PHASE_LABELS: Record<string, string> = {
  regenerate: 'Regenerating compose from registry',
  pull: 'Pulling images for new release track',
  apply: 'Applying changes (docker compose up)',
  done: 'Complete',
}

export default function ReconcilePage() {
  const navigate = useNavigate()
  const token =
    localStorage.getItem('jarvis-admin:access_token') ?? localStorage.getItem('access_token')
  const [phase, setPhase] = useState<Phase>('loading_options')
  const [logs, setLogs] = useState<LogLine[]>([])
  const [error, setError] = useState<string | null>(null)
  const logRef = useRef<HTMLDivElement>(null)

  // Options state
  const [serviceOptions, setServiceOptions] = useState<ServiceOption[]>([])
  const [relayEnabled, setRelayEnabled] = useState(false)
  const [relayUrl, setRelayUrl] = useState('https://relay.jarvisautomation.io')
  const [whisperModelPath, setWhisperModelPath] = useState('/whisper-models/ggml-base.en.bin')
  const [whisperBackend, setWhisperBackend] = useState<'cpu' | 'cuda' | 'vulkan' | 'rocm'>('cpu')
  const [ttsBackend, setTtsBackend] = useState<'cpu' | 'cuda'>('cpu')
  const [pinImages, setPinImages] = useState(false)
  const [releaseTrack, setReleaseTrack] = useState<'stable' | 'dev'>('stable')
  const [bgModelEnabled, setBgModelEnabled] = useState(false)
  const [bgModelFile, setBgModelFile] = useState('')
  const [bgModelSupported, setBgModelSupported] = useState(true)
  const [installedModels, setInstalledModels] = useState<string[]>([])

  // Download-instead-of-apply: regenerated files the operator swaps in by hand.
  const [regenFiles, setRegenFiles] = useState<{ compose: string; env: string; initDb: string } | null>(null)
  const [downloading, setDownloading] = useState(false)

  const addLog = useCallback((line: LogLine) => {
    setLogs((prev) => [...prev, line])
  }, [])

  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight
    }
  }, [logs])

  // Load current options on mount
  useEffect(() => {
    async function loadOptions() {
      try {
        const res = await fetch('/api/install/reconcile/options', {
          headers: { Authorization: `Bearer ${token}` },
        })
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const data: ReconcileOptions = await res.json()
        setServiceOptions(data.services)
        setRelayEnabled(data.relayEnabled)
        setRelayUrl(data.relayUrl || 'https://relay.jarvisautomation.io')
        setWhisperModelPath(data.whisperModelPath || '/whisper-models/ggml-base.en.bin')
        // Hydrate from the persisted install — leaving these at the useState
        // default ('cpu') silently downgrades a CUDA whisper/TTS on reconcile.
        setWhisperBackend(data.whisperBackend ?? 'cpu')
        setTtsBackend(data.ttsBackend ?? 'cpu')
        setPinImages(data.pinImages ?? false)
        setReleaseTrack(data.releaseTrack ?? 'stable')
        setBgModelEnabled(data.bgModelEnabled ?? false)
        setBgModelFile(data.bgModelFile ?? '')
        setBgModelSupported(data.bgModelSupported ?? true)
        // Installed GGUFs feed the background-model picker; a failure here just
        // leaves the datalist empty (free-text input still works).
        try {
          const modelsRes = await fetch('/api/models', {
            headers: { Authorization: `Bearer ${token}` },
          })
          if (modelsRes.ok) {
            const models: { name: string }[] = await modelsRes.json()
            setInstalledModels(models.map((m) => m.name).filter((n) => n.endsWith('.gguf')))
          }
        } catch {
          // non-fatal
        }
        setPhase('options')
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load options')
        setPhase('error')
      }
    }
    loadOptions()
  }, [token])

  function toggleService(id: string) {
    setServiceOptions((prev) =>
      prev.map((s) => (s.id === id ? { ...s, enabled: !s.enabled } : s)),
    )
  }

  async function handleReconcile() {
    setPhase('regenerate')
    setLogs([])
    setError(null)

    const enabledModules = serviceOptions.filter((s) => s.enabled).map((s) => s.id)

    try {
      const res = await fetch('/api/install/reconcile', {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${token}`,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ enabledModules, relayEnabled, relayUrl, whisperModelPath, whisperBackend, ttsBackend, pinImages, releaseTrack, bgModelEnabled, bgModelFile }),
      })

      if (!res.ok || !res.body) {
        const body = await res.text()
        throw new Error(body || `HTTP ${res.status}`)
      }

      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop() ?? ''

        for (const line of lines) {
          if (!line.startsWith('data: ')) continue
          try {
            const data = JSON.parse(line.slice(6))
            if (data.phase) setPhase(data.phase as Phase)
            if (data.message) addLog({ text: data.message, phase: data.phase })
            if (data.stream && data.text) addLog({ text: data.text.trim() })
            if (data.done && data.code !== 0) {
              throw new Error(data.message ?? 'Reconcile failed')
            }
          } catch (e) {
            if (e instanceof SyntaxError) continue
            throw e
          }
        }
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setError(msg)
      setPhase('error')
      addLog({ text: `Error: ${msg}` })
    }
  }

  function currentOverrides() {
    return {
      enabledModules: serviceOptions.filter((s) => s.enabled).map((s) => s.id),
      relayEnabled,
      relayUrl,
      whisperModelPath,
      whisperBackend,
      ttsBackend,
      pinImages,
      releaseTrack,
      bgModelEnabled,
      bgModelFile,
    }
  }

  // Regenerate the compose from the current install + selections and hand the
  // files back for the operator to review, drop in, and `docker compose up -d`.
  // Never touches the running stack — the safe alternative to "Sync now".
  async function handleDownload(opts?: { mqttAllowAnon?: boolean; latest?: boolean }) {
    setDownloading(true)
    setError(null)
    setRegenFiles(null)
    try {
      // `latest` is a query flag (refresh digests from GHCR), not a body override.
      const { latest, ...bodyExtra } = opts ?? {}
      const url = `/api/install/regenerate-download${latest ? '?latest=true' : ''}`
      const res = await fetch(url, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...currentOverrides(), ...bodyExtra }),
      })
      if (!res.ok) {
        const body = await res.text()
        throw new Error(body || `HTTP ${res.status}`)
      }
      const data = await res.json()
      setRegenFiles({ compose: data.compose, env: data.env, initDb: data.initDb })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setDownloading(false)
    }
  }

  function saveFile(name: string, content: string) {
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  const completedPhases = Object.keys(PHASE_LABELS).filter((p) => {
    const order = Object.keys(PHASE_LABELS)
    const currentIdx = order.indexOf(phase)
    const phaseIdx = order.indexOf(p)
    return phaseIdx < currentIdx || (phase === 'done' && p === 'done')
  })

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <div>
        <h1 className="text-2xl font-bold text-[var(--color-text)]">Sync Compose</h1>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          Regenerate <code className="rounded bg-[var(--color-surface)] px-1.5 py-0.5 text-xs">docker-compose.yml</code> from the latest service registry, preserving your
          secrets. <strong className="text-[var(--color-text)]">Sync now</strong> applies it to the running stack;
          <strong className="text-[var(--color-text)]"> Download</strong> hands you the files to review and apply
          by hand (<code className="rounded bg-[var(--color-surface)] px-1 text-xs">docker compose up -d</code>).
          {' '}<strong className="text-[var(--color-text)]">Update stack to latest</strong> downloads the same, pinned to the newest published images.
        </p>
      </div>

      {/* Loading options */}
      {phase === 'loading_options' && (
        <div className="flex items-center justify-center py-12">
          <Loader2 size={32} className="animate-spin text-[var(--color-primary)]" />
        </div>
      )}

      {/* Options selection */}
      {phase === 'options' && (
        <div className="space-y-4">
          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <h2 className="mb-3 text-sm font-semibold text-[var(--color-text)]">Services</h2>
            <div className="space-y-2">
              {serviceOptions.map((svc) => (
                <button
                  key={svc.id}
                  onClick={() => toggleService(svc.id)}
                  className="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left hover:bg-[var(--color-bg-secondary)] transition-colors"
                >
                  {svc.enabled ? (
                    <CheckCircle2 size={18} className="shrink-0 text-[var(--color-primary)]" />
                  ) : (
                    <Circle size={18} className="shrink-0 text-[var(--color-text-muted)]" />
                  )}
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-[var(--color-text)]">{svc.name}</div>
                    <div className="text-xs text-[var(--color-text-muted)]">{svc.description}</div>
                  </div>
                </button>
              ))}
            </div>
          </div>

          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <h2 className="mb-3 text-sm font-semibold text-[var(--color-text)]">Release Track</h2>
            <div className="flex items-center justify-between px-3 py-2">
              <div>
                <div className="text-sm font-medium text-[var(--color-text)]">
                  {releaseTrack === 'dev' ? 'Dev' : 'Stable'}
                </div>
                <div className="text-xs text-[var(--color-text-muted)]">
                  {releaseTrack === 'dev'
                    ? 'Using latest main branch builds (may be unstable)'
                    : 'Using tagged releases'}
                </div>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={releaseTrack === 'dev'}
                onClick={() => setReleaseTrack(releaseTrack === 'dev' ? 'stable' : 'dev')}
                className={`relative h-6 w-11 shrink-0 rounded-full transition-colors ${
                  releaseTrack === 'dev' ? 'bg-amber-500' : 'bg-[var(--color-surface-alt)]'
                }`}
              >
                <span className={`absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white transition-transform ${
                  releaseTrack === 'dev' ? 'translate-x-5' : 'translate-x-0'
                }`} />
              </button>
            </div>
            {releaseTrack === 'dev' && (
              <div className="mx-3 mt-1 rounded-lg border border-amber-300 bg-amber-500/10 px-3 py-2 text-xs text-amber-400">
                Switching tracks will pull all images and force-recreate containers.
              </div>
            )}
          </div>

          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <h2 className="mb-3 text-sm font-semibold text-[var(--color-text)]">Integrations</h2>
            <button
              onClick={() => setRelayEnabled(!relayEnabled)}
              className="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left hover:bg-[var(--color-bg-secondary)] transition-colors"
            >
              {relayEnabled ? (
                <CheckCircle2 size={18} className="shrink-0 text-[var(--color-primary)]" />
              ) : (
                <Circle size={18} className="shrink-0 text-[var(--color-text-muted)]" />
              )}
              <div className="min-w-0">
                <div className="text-sm font-medium text-[var(--color-text)]">Jarvis Relay</div>
                <div className="text-xs text-[var(--color-text-muted)]">
                  Routes OAuth callbacks through a cloud relay for external providers (Google, Spotify, etc.)
                </div>
              </div>
            </button>
            {relayEnabled && (
              <div className="mt-2 px-3">
                <label className="text-xs font-medium text-[var(--color-text-muted)]">Relay URL</label>
                <input
                  type="url"
                  value={relayUrl}
                  onChange={(e) => setRelayUrl(e.target.value)}
                  placeholder="https://relay.jarvisautomation.io"
                  className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
            )}
          </div>

          {serviceOptions.some((s) => s.id === 'jarvis-whisper-api' && s.enabled) && (
            <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
              <h2 className="mb-3 text-sm font-semibold text-[var(--color-text)]">Whisper</h2>
              <div className="px-3">
                <label className="text-xs font-medium text-[var(--color-text-muted)]">Model path</label>
                <input
                  type="text"
                  value={whisperModelPath}
                  onChange={(e) => setWhisperModelPath(e.target.value)}
                  placeholder="/whisper-models/ggml-base.en.bin"
                  className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm font-mono text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
                <p className="mt-1 text-xs text-[var(--color-text-muted)]">
                  Path inside the container. Place model files in <code className="rounded bg-[var(--color-bg)] px-1">./whisper-models/</code> next to your compose file.
                </p>
                <label className="mt-3 block text-xs font-medium text-[var(--color-text-muted)]">GPU backend</label>
                <select
                  value={whisperBackend}
                  onChange={(e) => setWhisperBackend(e.target.value as 'cpu' | 'cuda' | 'vulkan' | 'rocm')}
                  className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text)]"
                >
                  <option value="cpu">CPU (default)</option>
                  <option value="cuda">NVIDIA (CUDA)</option>
                  <option value="vulkan">AMD / generic (Vulkan)</option>
                  <option value="rocm">AMD (ROCm)</option>
                </select>
                <p className="mt-1 text-xs text-[var(--color-text-muted)]">
                  Where speech-to-text runs. CPU leaves the GPU for the LLM; pick a GPU backend to run Whisper on the GPU.
                </p>
              </div>
            </div>
          )}

          {serviceOptions.some((s) => s.id === 'jarvis-tts' && s.enabled) && (
            <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
              <h2 className="mb-3 text-sm font-semibold text-[var(--color-text)]">Text-to-speech</h2>
              <div className="px-3">
                <label className="text-xs font-medium text-[var(--color-text-muted)]">Inference device</label>
                <select
                  value={ttsBackend}
                  onChange={(e) => setTtsBackend(e.target.value as 'cpu' | 'cuda')}
                  className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm text-[var(--color-text)]"
                >
                  <option value="cpu">CPU (default)</option>
                  <option value="cuda">NVIDIA (CUDA)</option>
                </select>
                <p className="mt-1 text-xs text-[var(--color-text-muted)]">
                  Where Kokoro synthesis runs. CPU is fine for most installs; CUDA gives the fastest
                  first-audio. On multi-GPU hosts set <code className="rounded bg-[var(--color-bg)] px-1">TTS_GPU_DEVICE</code> in
                  .env to pick which GPU (default 0).
                </p>
              </div>
            </div>
          )}

          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <h2 className="mb-3 text-sm font-semibold text-[var(--color-text)]">Background model</h2>
            <div className="px-3">
              <label className="flex items-start gap-3 text-sm">
                <input
                  type="checkbox"
                  checked={bgModelEnabled}
                  disabled={!bgModelSupported}
                  onChange={(e) => setBgModelEnabled(e.target.checked)}
                  className="mt-0.5"
                  data-testid="bg-model-checkbox"
                />
                <span>
                  <span className="font-medium text-[var(--color-text)]">Run a second model for background work</span>
                  <span className="mt-1 block text-xs text-[var(--color-text-muted)]">
                    Serves memory extraction, errand planning, and other background jobs from a
                    dedicated model so reasoning never slows down live voice. NVIDIA/Linux only —
                    needs enough free VRAM alongside the live model.
                  </span>
                </span>
              </label>
              {!bgModelSupported && (
                <p className="mt-2 text-xs text-[var(--color-text-muted)]">
                  Not available on this host (requires Linux with an NVIDIA GPU).
                </p>
              )}
              {bgModelEnabled && (
                <div className="mt-3">
                  <label className="text-xs font-medium text-[var(--color-text-muted)]">Model file</label>
                  <input
                    type="text"
                    value={bgModelFile}
                    onChange={(e) => setBgModelFile(e.target.value)}
                    list="bg-model-files"
                    placeholder="Qwen3.8-27B-UD-Q3_K_XL.gguf"
                    className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-sm font-mono text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                    data-testid="bg-model-file-input"
                  />
                  <datalist id="bg-model-files">
                    {installedModels.map((m) => (
                      <option key={m} value={m} />
                    ))}
                  </datalist>
                  <p className="mt-1 text-xs text-[var(--color-text-muted)]">
                    A GGUF from your models directory — download one on the{' '}
                    <a href="/models" className="underline">Models</a> page first. After syncing,
                    point the LLM proxy&apos;s background slot at it (model.background.backend=REST,
                    rest_url http://llama-server-bg:8080) and reload the model service.
                  </p>
                </div>
              )}
            </div>
          </div>

          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
            <label className="flex items-start gap-3 text-sm">
              <input
                type="checkbox"
                checked={pinImages}
                onChange={(e) => setPinImages(e.target.checked)}
                className="mt-0.5"
                data-testid="pin-images-checkbox"
              />
              <span>
                <span className="font-medium text-[var(--color-text)]">Pin images by digest</span>
                <span className="mt-1 block text-xs text-[var(--color-text-muted)]">
                  Advanced supply-chain hardening: locks every image to an exact build.
                  Leave OFF for normal use — with pinning on, <code className="rounded bg-[var(--color-bg)] px-1">docker compose pull</code> will
                  never update anything and services only change via this page.
                </span>
              </span>
            </label>
          </div>
        </div>
      )}

      {/* Regenerated files ready to download + apply by hand */}
      {regenFiles && phase === 'options' && (
        <div className="rounded-lg border border-[var(--color-primary)]/40 bg-[var(--color-surface)] p-4">
          <h2 className="mb-1 flex items-center gap-2 text-sm font-semibold text-[var(--color-text)]">
            <CheckCircle2 size={16} className="text-green-500" />
            Updated files ready
          </h2>
          <p className="mb-3 text-xs text-[var(--color-text-muted)]">
            Your existing secrets and settings are carried over. Download, replace the files next to your
            compose, then run <code className="rounded bg-[var(--color-bg)] px-1">docker compose up -d</code>.
            Nothing on the server was changed.
          </p>
          <div className="flex flex-wrap gap-2">
            <button
              onClick={() => saveFile('docker-compose.yml', regenFiles.compose)}
              className="flex items-center gap-2 rounded-lg bg-[var(--color-primary)] px-3 py-2 text-sm font-medium text-white hover:opacity-90"
            >
              <Download size={15} />
              docker-compose.yml
            </button>
            <button
              onClick={() => saveFile('.env', regenFiles.env)}
              className="flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-3 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]"
            >
              <Download size={15} />
              .env
            </button>
            <button
              onClick={() => saveFile('init-db.sh', regenFiles.initDb)}
              className="flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-3 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]"
            >
              <Download size={15} />
              init-db.sh
            </button>
          </div>
          <p className="mt-3 text-xs text-[var(--color-text-muted)]">
            The <code className="rounded bg-[var(--color-bg)] px-1">.env</code> merges new keys into your
            existing values, and <code className="rounded bg-[var(--color-bg)] px-1">init-db.sh</code> covers any
            new databases — replace all three to be safe. Diff them against your current files first if you want
            to eyeball the changes.
          </p>
        </div>
      )}

      {/* Progress phases */}
      {phase !== 'loading_options' && phase !== 'options' && (
        <div className="space-y-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          {Object.entries(PHASE_LABELS).map(([key, label]) => {
            const isActive = key === phase
            const isComplete = completedPhases.includes(key)
            const isError = key === phase && phase === 'error'
            return (
              <div key={key} className="flex items-center gap-2">
                {isComplete ? (
                  <CheckCircle2 size={16} className="text-green-500" />
                ) : isError ? (
                  <AlertTriangle size={16} className="text-red-500" />
                ) : isActive ? (
                  <Loader2 size={16} className="animate-spin text-[var(--color-primary)]" />
                ) : (
                  <div className="h-4 w-4 rounded-full border border-[var(--color-border)]" />
                )}
                <span
                  className={cn(
                    'text-sm',
                    isActive ? 'font-medium text-[var(--color-text)]' : 'text-[var(--color-text-muted)]',
                    isComplete && 'text-green-500',
                  )}
                >
                  {label}
                </span>
              </div>
            )
          })}
        </div>
      )}

      {logs.length > 0 && (
        <div
          ref={logRef}
          className="max-h-64 overflow-y-auto rounded-lg border border-[var(--color-border)] bg-[#0d1117] p-3 font-mono text-xs text-gray-300"
        >
          {logs.map((line, i) => (
            <div key={i}>{line.text}</div>
          ))}
        </div>
      )}

      {error && (
        <div className="flex items-center gap-2 rounded-lg bg-red-500/10 px-3 py-2">
          <AlertTriangle size={14} className="text-red-500" />
          <span className="text-sm text-red-500">{error}</span>
        </div>
      )}

      <div className="flex gap-3">
        {phase === 'options' && (
          <button
            onClick={handleReconcile}
            className="flex items-center gap-2 rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white hover:opacity-90"
          >
            <GitMerge size={16} />
            Sync now
          </button>
        )}
        {phase === 'options' && (
          <button
            onClick={() => handleDownload()}
            disabled={downloading}
            title="Regenerate the compose and download it to apply by hand — doesn't touch the running stack"
            className="flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            {downloading ? <Loader2 size={16} className="animate-spin" /> : <Download size={16} />}
            Download updated compose
          </button>
        )}
        {phase === 'options' && (
          <button
            onClick={() => handleDownload({ latest: true })}
            disabled={downloading}
            title="Download a compose pinned to the LATEST published images (digests refreshed from the registry). Apply it yourself on the host — docker compose pull && docker compose up -d — which also updates jarvis-admin."
            className="flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            {downloading ? <Loader2 size={16} className="animate-spin" /> : <RefreshCw size={16} />}
            Update stack to latest
          </button>
        )}
        {phase === 'options' && (
          <button
            onClick={() => handleDownload({ mqttAllowAnon: false })}
            disabled={downloading}
            title="Download a compose with the MQTT broker locked (allow_anonymous=false). Do this only after every node has authenticated, or un-migrated nodes will drop off."
            className="flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            {downloading ? <Loader2 size={16} className="animate-spin" /> : <Lock size={16} />}
            Download + lock broker
          </button>
        )}
        {phase === 'options' && (
          <button
            onClick={() => handleDownload({ mqttAllowAnon: true })}
            disabled={downloading}
            title="Download a compose that re-opens the MQTT broker to anonymous clients (allow_anonymous=true) — the transition/rollback state."
            className="flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            {downloading ? <Loader2 size={16} className="animate-spin" /> : <Unlock size={16} />}
            Download + unlock broker
          </button>
        )}
        {/* Back-to-dashboard available in options + done; disabled while reconcile is in flight. */}
        {(phase === 'options' || phase === 'regenerate' || phase === 'apply' || phase === 'done') && (
          <button
            onClick={() => navigate('/dashboard')}
            disabled={phase === 'regenerate' || phase === 'apply'}
            className="flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-4 py-2 text-sm font-medium text-[var(--color-text)] hover:bg-[var(--color-surface-alt)] disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:bg-transparent"
          >
            <CheckCircle2 size={16} />
            Back to dashboard
          </button>
        )}
        {phase === 'error' && (
          <button
            onClick={handleReconcile}
            className="flex items-center gap-2 rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white hover:opacity-90"
          >
            <RefreshCw size={16} />
            Retry
          </button>
        )}
      </div>
    </div>
  )
}
