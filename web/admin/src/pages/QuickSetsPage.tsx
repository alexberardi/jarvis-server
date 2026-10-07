import { useState, useMemo } from 'react'
import { Zap, Check, RefreshCw, AlertTriangle, Cpu, Plus, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import { useQuickSets, useApplyQuickSet, useCreateCustomPreset, useDeleteCustomPreset } from '@/hooks/useQuickSets'
import { useInstalledModels } from '@/hooks/useModels'
import type { QuickSetPreset } from '@/api/quickSets'

const BACKEND_OPTIONS = ['GGUF', 'MLX', 'VLLM', 'TRANSFORMERS', 'REST']

export default function QuickSetsPage() {
  const { data, isLoading, error } = useQuickSets()
  const applyMutation = useApplyQuickSet()
  const createMutation = useCreateCustomPreset()
  const deleteMutation = useDeleteCustomPreset()

  const { data: installedModels } = useInstalledModels()
  // Installed models on disk become dropdown options for the model path. Values are
  // '.models/'-prefixed to match what the backend writes to model.live/background.name.
  const modelOptions = useMemo(
    () => (installedModels ?? []).map((m) => `.models/${m.name}`),
    [installedModels],
  )

  // Apply panel state
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [modelName, setModelName] = useState('')
  // When true (or when the value isn't an installed model), show a free-text input so
  // HuggingFace IDs and not-yet-downloaded paths remain enterable.
  const [modelOtherMode, setModelOtherMode] = useState(false)
  const [contextWindow, setContextWindow] = useState<number | ''>('')
  const [backend, setBackend] = useState('')
  const [chatFormat, setChatFormat] = useState('')
  const [promptProvider, setPromptProvider] = useState('')
  const [targets, setTargets] = useState<Set<'live' | 'background'>>(new Set(['live']))

  // Create custom preset state
  const [showCreate, setShowCreate] = useState(false)
  const [newName, setNewName] = useState('')
  const [newFamily, setNewFamily] = useState('')
  const [newChatFormat, setNewChatFormat] = useState('')
  const [newPromptProvider, setNewPromptProvider] = useState('')
  const [newBackend, setNewBackend] = useState('GGUF')
  const [newContextWindow, setNewContextWindow] = useState<number | ''>(8192)

  const presets = data?.presets
  const grouped = useMemo(() => {
    if (!presets) return new Map<string, QuickSetPreset[]>()
    const map = new Map<string, QuickSetPreset[]>()
    for (const preset of presets) {
      const family = preset.family || 'Custom'
      const list = map.get(family) ?? []
      list.push(preset)
      map.set(family, list)
    }
    return map
  }, [presets])

  function handleSelect(preset: QuickSetPreset) {
    if (selectedId === preset.id) {
      setSelectedId(null)
      return
    }
    setSelectedId(preset.id)
    setModelName(data?.currentValues.modelName ?? '')
    setModelOtherMode(false)
    setBackend(preset.defaultBackend)
    setChatFormat(preset.chatFormat)
    setPromptProvider(preset.promptProvider)
    const currentCtx = data?.currentValues.contextWindow
    setContextWindow(currentCtx && currentCtx > 0 ? currentCtx : preset.defaultContextWindow)
  }

  function toggleTarget(target: 'live' | 'background') {
    setTargets((prev) => {
      const next = new Set(prev)
      if (next.has(target)) {
        if (next.size > 1) next.delete(target)
      } else {
        next.add(target)
      }
      return next
    })
  }

  function handleApply() {
    if (!selectedId || !modelName.trim()) return

    applyMutation.mutate(
      {
        presetId: selectedId,
        modelName: modelName.trim(),
        contextWindow: contextWindow ? Number(contextWindow) : undefined,
        backend,
        chatFormat,
        promptProvider,
        targets: Array.from(targets),
      },
      {
        onSuccess: (res) => {
          if (res.success) {
            toast.success(res.message)
          } else {
            toast.warning(res.message)
          }
          setSelectedId(null)
        },
        onError: (err) => {
          toast.error(err.message || 'Failed to apply preset')
        },
      },
    )
  }

  function handleCreate() {
    if (!newName.trim() || !newChatFormat.trim() || !newPromptProvider.trim()) return

    createMutation.mutate(
      {
        name: newName.trim(),
        family: newFamily.trim() || 'Custom',
        chatFormat: newChatFormat.trim(),
        promptProvider: newPromptProvider.trim(),
        defaultBackend: newBackend,
        defaultContextWindow: newContextWindow ? Number(newContextWindow) : 8192,
      },
      {
        onSuccess: () => {
          toast.success(`Created preset "${newName.trim()}"`)
          setShowCreate(false)
          setNewName('')
          setNewFamily('')
          setNewChatFormat('')
          setNewPromptProvider('')
          setNewBackend('GGUF')
          setNewContextWindow(8192)
        },
        onError: (err) => {
          toast.error(err.message || 'Failed to create preset')
        },
      },
    )
  }

  function handleDelete(preset: QuickSetPreset) {
    deleteMutation.mutate(preset.id, {
      onSuccess: () => {
        toast.success(`Deleted "${preset.name}"`)
        if (selectedId === preset.id) setSelectedId(null)
      },
      onError: () => toast.error('Failed to delete preset'),
    })
  }

  const selectedPreset = data?.presets.find((p) => p.id === selectedId)

  return (
    <div className="mx-auto max-w-4xl space-y-8 p-6">
      <div>
        <h1 className="text-2xl font-bold text-[var(--color-text)]">Quick Sets</h1>
        <p className="text-sm text-[var(--color-text-muted)]">
          Select a model preset to auto-configure chat format, prompt provider, and backend
        </p>
      </div>

      {/* Current Model Banner */}
      {data?.currentValues.modelName && (
        <div className="flex items-center gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3">
          <Cpu size={16} className="text-[var(--color-primary)]" />
          <div className="min-w-0 flex-1">
            <span className="text-xs text-[var(--color-text-muted)]">Current model</span>
            <p className="truncate text-sm font-medium text-[var(--color-text)]">
              {data.currentValues.modelName}
            </p>
          </div>
          {data.currentValues.contextWindow > 0 && (
            <span className="rounded bg-[var(--color-surface-alt)] px-2 py-0.5 text-xs text-[var(--color-text-muted)]">
              {data.currentValues.contextWindow.toLocaleString()} ctx
            </span>
          )}
        </div>
      )}

      {/* Loading / Error */}
      {isLoading && (
        <div className="flex items-center justify-center py-12">
          <RefreshCw size={20} className="animate-spin text-[var(--color-text-muted)]" />
        </div>
      )}
      {error && (
        <div className="flex items-center gap-2 rounded-lg border border-red-500/30 bg-red-500/5 px-4 py-3">
          <AlertTriangle size={16} className="text-red-500" />
          <span className="text-sm text-red-500">Failed to load presets</span>
        </div>
      )}

      {/* Preset Cards by Family */}
      {!isLoading &&
        Array.from(grouped.entries()).map(([family, presets]) => (
          <section key={family}>
            <h2 className="mb-3 text-sm font-semibold text-[var(--color-text-muted)]">{family}</h2>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              {presets.map((preset) => (
                <div
                  key={preset.id}
                  className={cn(
                    'relative rounded-lg border px-4 py-3 transition-colors',
                    selectedId === preset.id
                      ? 'border-[var(--color-primary)] bg-[var(--color-primary)]/5'
                      : 'border-[var(--color-border)] bg-[var(--color-surface)] hover:bg-[var(--color-surface-alt)]',
                  )}
                >
                  <button
                    onClick={() => handleSelect(preset)}
                    className="w-full text-left"
                  >
                    <div className="flex items-center justify-between">
                      <span className="text-sm font-medium text-[var(--color-text)]">
                        {preset.name}
                      </span>
                      {selectedId === preset.id && (
                        <Check size={16} className="text-[var(--color-primary)]" />
                      )}
                    </div>
                    {preset.description && (
                      <p className="mt-1 text-xs text-[var(--color-text-muted)]">
                        {preset.description}
                      </p>
                    )}
                    <div className="mt-2 flex items-center gap-2">
                      <span className="rounded bg-[var(--color-surface-alt)] px-1.5 py-0.5 text-xs text-[var(--color-text-muted)]">
                        {preset.chatFormat}
                      </span>
                      <span className="rounded bg-[var(--color-surface-alt)] px-1.5 py-0.5 text-xs text-[var(--color-text-muted)]">
                        {preset.defaultBackend}
                      </span>
                    </div>
                  </button>
                  {preset.isCustom && (
                    <button
                      onClick={() => handleDelete(preset)}
                      disabled={deleteMutation.isPending}
                      className="absolute right-2 top-2 rounded p-1 text-[var(--color-text-muted)] hover:bg-red-500/10 hover:text-red-500"
                      title="Delete custom preset"
                    >
                      <Trash2 size={14} />
                    </button>
                  )}
                </div>
              ))}
            </div>
          </section>
        ))}

      {/* Create Custom Preset */}
      {!isLoading && !showCreate && (
        <button
          onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 rounded-lg border border-dashed border-[var(--color-border)] px-4 py-3 text-sm text-[var(--color-text-muted)] transition-colors hover:border-[var(--color-primary)] hover:text-[var(--color-primary)]"
        >
          <Plus size={16} />
          Create custom preset
        </button>
      )}

      {showCreate && (
        <section className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          <div className="mb-4 flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Plus size={16} className="text-[var(--color-primary)]" />
              <h2 className="text-sm font-semibold text-[var(--color-text)]">New Custom Preset</h2>
            </div>
            <button
              onClick={() => setShowCreate(false)}
              className="rounded p-1 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]"
            >
              <X size={16} />
            </button>
          </div>
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Name *</label>
                <input
                  type="text"
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="My Custom Model"
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Family</label>
                <input
                  type="text"
                  value={newFamily}
                  onChange={(e) => setNewFamily(e.target.value)}
                  placeholder="Custom"
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Chat format *</label>
                <input
                  type="text"
                  value={newChatFormat}
                  onChange={(e) => setNewChatFormat(e.target.value)}
                  placeholder="chatml"
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Prompt provider *</label>
                <input
                  type="text"
                  value={newPromptProvider}
                  onChange={(e) => setNewPromptProvider(e.target.value)}
                  placeholder="Qwen25MediumUntrained"
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Default backend</label>
                <select
                  value={newBackend}
                  onChange={(e) => setNewBackend(e.target.value)}
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)]"
                >
                  {BACKEND_OPTIONS.map((opt) => (
                    <option key={opt} value={opt}>{opt}</option>
                  ))}
                </select>
              </div>
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Default context window</label>
                <input
                  type="number"
                  value={newContextWindow}
                  onChange={(e) => setNewContextWindow(e.target.value ? parseInt(e.target.value, 10) : '')}
                  placeholder="8192"
                  min={1}
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
            </div>
            <div className="flex items-center gap-3 pt-1">
              <button
                onClick={handleCreate}
                disabled={!newName.trim() || !newChatFormat.trim() || !newPromptProvider.trim() || createMutation.isPending}
                className="flex items-center gap-2 rounded-md bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white transition-colors hover:opacity-90 disabled:opacity-50"
              >
                {createMutation.isPending ? (
                  <RefreshCw size={14} className="animate-spin" />
                ) : (
                  <Plus size={14} />
                )}
                {createMutation.isPending ? 'Creating...' : 'Create Preset'}
              </button>
              <button
                onClick={() => setShowCreate(false)}
                className="rounded-md px-3 py-2 text-sm text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]"
              >
                Cancel
              </button>
            </div>
          </div>
        </section>
      )}

      {/* Apply Panel */}
      {selectedPreset && (
        <section className="rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-surface)] p-4">
          <div className="mb-4 flex items-center gap-2">
            <Zap size={16} className="text-[var(--color-primary)]" />
            <h2 className="text-sm font-semibold text-[var(--color-text)]">
              Apply: {selectedPreset.name}
            </h2>
          </div>

          {/* Target Checkboxes */}
          <div className="mb-4">
            <span className="mb-2 block text-xs font-medium text-[var(--color-text-muted)]">Apply to</span>
            <div className="flex items-center gap-4">
              {(['live', 'background'] as const).map((tier) => (
                <label key={tier} className="flex items-center gap-2 text-sm text-[var(--color-text)]">
                  <input
                    type="checkbox"
                    checked={targets.has(tier)}
                    onChange={() => toggleTarget(tier)}
                    className="rounded border-[var(--color-border)] accent-[var(--color-primary)]"
                  />
                  <span className="capitalize">{tier}</span>
                </label>
              ))}
            </div>
          </div>

          {/* User Inputs */}
          <div className="space-y-3">
            <div className="space-y-2">
              <label className="mb-1 block text-xs text-[var(--color-text-muted)]">
                Model file path
              </label>
              {modelOptions.length > 0 && (
                <select
                  value={
                    modelOtherMode || (modelName !== '' && !modelOptions.includes(modelName))
                      ? '__other__'
                      : modelName
                  }
                  onChange={(e) => {
                    if (e.target.value === '__other__') {
                      setModelOtherMode(true)
                    } else {
                      setModelOtherMode(false)
                      setModelName(e.target.value)
                    }
                  }}
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)]"
                >
                  <option value="" disabled>
                    Select a model…
                  </option>
                  {modelOptions.map((opt) => (
                    <option key={opt} value={opt}>
                      {opt.replace(/^\.models\//, '')}
                    </option>
                  ))}
                  <option value="__other__">Other (custom path / HF ID)…</option>
                </select>
              )}
              {(modelOptions.length === 0 ||
                modelOtherMode ||
                (modelName !== '' && !modelOptions.includes(modelName))) && (
                <input
                  type="text"
                  value={modelName}
                  onChange={(e) => setModelName(e.target.value)}
                  placeholder=".models/model-name.gguf"
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              )}
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">
                  Chat format
                </label>
                <input
                  type="text"
                  value={chatFormat}
                  onChange={(e) => setChatFormat(e.target.value)}
                  placeholder="chatml"
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">
                  Prompt provider
                </label>
                <input
                  type="text"
                  value={promptProvider}
                  onChange={(e) => setPromptProvider(e.target.value)}
                  placeholder="Qwen25MediumUntrained"
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">
                  Context window (tokens)
                </label>
                <input
                  type="number"
                  value={contextWindow}
                  onChange={(e) => setContextWindow(e.target.value ? parseInt(e.target.value, 10) : '')}
                  placeholder={String(selectedPreset.defaultContextWindow)}
                  min={1}
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)] placeholder:text-[var(--color-text-muted)]"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Backend</label>
                <select
                  value={backend}
                  onChange={(e) => setBackend(e.target.value)}
                  className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-background)] px-3 py-2 text-sm text-[var(--color-text)]"
                >
                  {BACKEND_OPTIONS.map((opt) => (
                    <option key={opt} value={opt}>
                      {opt}
                    </option>
                  ))}
                </select>
              </div>
            </div>
          </div>

          {/* Apply Button */}
          <div className="mt-4 flex items-center gap-3">
            <button
              onClick={handleApply}
              disabled={!modelName.trim() || applyMutation.isPending}
              className="flex items-center gap-2 rounded-md bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white transition-colors hover:opacity-90 disabled:opacity-50"
            >
              {applyMutation.isPending ? (
                <RefreshCw size={14} className="animate-spin" />
              ) : (
                <Zap size={14} />
              )}
              {applyMutation.isPending ? 'Applying...' : 'Apply Settings'}
            </button>
            <button
              onClick={() => setSelectedId(null)}
              className="rounded-md px-3 py-2 text-sm text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]"
            >
              Cancel
            </button>
          </div>
        </section>
      )}
    </div>
  )
}
