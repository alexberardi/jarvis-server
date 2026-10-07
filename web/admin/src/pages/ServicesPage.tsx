import { useState, useCallback, useRef, useEffect, useMemo } from 'react'
import { RefreshCw, Copy, Check, AlertTriangle, Info, Plus, X, ChevronDown } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import {
  useServiceRegistry,
  useRegisterServices,
  useRotateKey,
  useAddService,
  useDeleteService,
  useServiceSuggestions,
} from '@/hooks/useServices'
import { probeServiceHealth } from '@/api/services'
import ServiceRegistrationRow from '@/components/services/ServiceRegistrationRow'
import type { HealthStatus } from '@/components/services/ServiceRegistrationRow'
import type {
  ServiceRegisterItem,
  ServiceRegisterResult,
  KeyRotateResponse,
  ServiceSuggestion,
} from '@/types/services'

interface RowState {
  checked: boolean
  host: string
  port: number
  scheme: string
}

interface HealthState {
  status: HealthStatus
  error?: string
  latency?: number
}

const EMPTY_ADD_FORM = {
  name: '',
  host: 'localhost',
  port: '',
  scheme: 'http',
  health_path: '/health',
  description: '',
}

export default function ServicesPage() {
  const { data, isLoading, isError, error, refetch, isFetching } = useServiceRegistry()
  const mutation = useRegisterServices()
  const rotateMutation = useRotateKey()
  const addMutation = useAddService()
  const deleteMutation = useDeleteService()
  const { data: suggestionsData } = useServiceSuggestions()

  const [rowStates, setRowStates] = useState<Record<string, RowState>>({})
  const [results, setResults] = useState<ServiceRegisterResult[] | null>(null)
  const [rotateResult, setRotateResult] = useState<KeyRotateResponse | null>(null)
  const [rotatingService, setRotatingService] = useState<string | null>(null)
  const [deletingService, setDeletingService] = useState<string | null>(null)
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const [healthStates, setHealthStates] = useState<Record<string, HealthState>>({})
  const [showAddForm, setShowAddForm] = useState(false)
  const [addForm, setAddForm] = useState(EMPTY_ADD_FORM)
  const [comboOpen, setComboOpen] = useState(false)
  const comboRef = useRef<HTMLDivElement>(null)

  // Filter suggestions: exclude services already in the registry
  const registeredNames = useMemo(
    () => new Set(data?.services.map((s) => s.name) ?? []),
    [data],
  )

  const filteredSuggestions = useMemo(() => {
    const all = suggestionsData?.suggestions ?? []
    const unregistered = all.filter((s) => !registeredNames.has(s.id))
    if (!addForm.name.trim()) return unregistered
    const q = addForm.name.toLowerCase()
    return unregistered.filter(
      (s) =>
        s.id.toLowerCase().includes(q) ||
        s.name.toLowerCase().includes(q) ||
        s.description.toLowerCase().includes(q),
    )
  }, [suggestionsData, registeredNames, addForm.name])

  const selectSuggestion = useCallback((s: ServiceSuggestion) => {
    setAddForm({
      name: s.id,
      description: s.description,
      host: 'localhost',
      port: String(s.port),
      scheme: 'http',
      health_path: s.healthCheck,
    })
    setComboOpen(false)
  }, [])

  // Close combobox on outside click
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (comboRef.current && !comboRef.current.contains(e.target as Node)) {
        setComboOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [])

  // Derive the effective state for each row (local override or defaults from server)
  const getRowState = useCallback(
    (name: string): RowState => {
      if (rowStates[name]) return rowStates[name]
      const entry = data?.services.find((s) => s.name === name)
      if (!entry) return { checked: false, host: 'localhost', port: 0, scheme: 'http' }
      return {
        checked: entry.config_registered,
        host: entry.current_host ?? 'localhost',
        port: entry.current_port ?? entry.default_port,
        scheme: entry.current_scheme ?? 'http',
      }
    },
    [rowStates, data],
  )

  const updateRow = useCallback((name: string, patch: Partial<RowState>) => {
    setRowStates((prev) => {
      const current = prev[name] ?? { checked: false, host: 'localhost', port: 0 }
      return { ...prev, [name]: { ...current, ...patch } }
    })
  }, [])

  const probeHealth = useCallback(
    async (name: string, host: string, port: number, healthPath: string, scheme: string = 'http') => {
      if (!host || !port) return

      setHealthStates((prev) => ({ ...prev, [name]: { status: 'probing' } }))
      try {
        const resp = await probeServiceHealth({ host, port, health_path: healthPath, scheme })
        setHealthStates((prev) => ({
          ...prev,
          [name]: {
            status: resp.healthy ? 'healthy' : 'unhealthy',
            error: resp.error ?? undefined,
            latency: resp.latency_ms ?? undefined,
          },
        }))
      } catch {
        setHealthStates((prev) => ({
          ...prev,
          [name]: { status: 'unhealthy', error: 'Request failed' },
        }))
      }
    },
    [],
  )

  const handleRegister = useCallback(() => {
    if (!data) return

    const items: ServiceRegisterItem[] = data.services
      .filter((entry) => getRowState(entry.name).checked)
      .map((entry) => {
        const state = getRowState(entry.name)
        return { name: entry.name, host: state.host, port: state.port, scheme: state.scheme }
      })

    if (items.length === 0) {
      toast.error('No services selected')
      return
    }

    setResults(null)
    setRotateResult(null)
    mutation.mutate(
      { services: items },
      {
        onSuccess: (resp) => {
          setResults(resp.results)
          const ok = resp.results.filter((r) => r.config_ok && r.auth_ok).length
          const total = resp.results.length
          if (ok === total) {
            toast.success(`All ${total} services registered`)
          } else {
            toast.warning(`${ok}/${total} services registered successfully`)
          }
        },
        onError: (err) => {
          toast.error(`Registration failed: ${err.message}`)
        },
      },
    )
  }, [data, getRowState, mutation])

  const handleRotateKey = useCallback(
    (serviceName: string) => {
      setRotatingService(serviceName)
      setRotateResult(null)
      rotateMutation.mutate(
        { service_name: serviceName },
        {
          onSuccess: (resp) => {
            setRotateResult(resp)
            setRotatingService(null)
            toast.success(`Key rotated for ${serviceName}`)
          },
          onError: (err) => {
            setRotatingService(null)
            toast.error(`Key rotation failed: ${err.message}`)
          },
        },
      )
    },
    [rotateMutation],
  )

  const handleDeleteService = useCallback(
    (serviceName: string) => {
      setDeletingService(serviceName)
      deleteMutation.mutate(serviceName, {
        onSuccess: () => {
          setDeletingService(null)
          toast.success(`Removed ${serviceName}`)
        },
        onError: (err) => {
          setDeletingService(null)
          toast.error(`Failed to remove: ${err.message}`)
        },
      })
    },
    [deleteMutation],
  )

  const handleAddService = useCallback(() => {
    const portNum = parseInt(addForm.port, 10)
    if (!addForm.name.trim()) {
      toast.error('Service name is required')
      return
    }
    if (!portNum || portNum < 1 || portNum > 65535) {
      toast.error('Port must be between 1 and 65535')
      return
    }

    addMutation.mutate(
      {
        name: addForm.name.trim(),
        host: addForm.host.trim() || 'localhost',
        port: portNum,
        scheme: addForm.scheme,
        health_path: addForm.health_path.trim() || '/health',
        description: addForm.description.trim(),
      },
      {
        onSuccess: (resp) => {
          const r = resp.results[0]
          if (r?.config_ok) {
            toast.success(`Added ${addForm.name.trim()}`)
            setAddForm(EMPTY_ADD_FORM)
            setShowAddForm(false)
          } else {
            toast.error(`Failed to add: ${r?.error ?? 'Unknown error'}`)
          }
        },
        onError: (err) => {
          toast.error(`Failed to add service: ${err.message}`)
        },
      },
    )
  }, [addForm, addMutation])

  const handleCopyKey = useCallback((key: string) => {
    navigator.clipboard.writeText(key).then(() => {
      setCopiedKey(key)
      setTimeout(() => setCopiedKey(null), 2000)
    })
  }, [])

  const checkedCount = data?.services.filter((e) => getRowState(e.name).checked).length ?? 0
  const hasEnvWrites =
    results?.some((r) => r.env_written) || rotateResult?.env_written === true

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <RefreshCw className="animate-spin text-[var(--color-primary)]" size={24} />
      </div>
    )
  }

  if (isError) {
    return (
      <div className="py-20 text-center">
        <p className="mb-2 text-red-500">Failed to load service registry</p>
        <p className="mb-4 text-sm text-[var(--color-text-muted)]">
          {(error as Error)?.message ?? 'Unknown error'}
        </p>
        <button
          onClick={() => refetch()}
          className="rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm text-white hover:opacity-90"
        >
          Retry
        </button>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-4xl space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <h1 className="text-xl font-bold text-[var(--color-text)]">Services</h1>
          <span className="rounded-full bg-[var(--color-surface-alt)] px-2.5 py-0.5 text-xs font-medium text-[var(--color-text-muted)]">
            {data?.services.length ?? 0}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setShowAddForm((v) => !v)}
            className={cn(
              'flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium',
              showAddForm
                ? 'bg-[var(--color-surface-alt)] text-[var(--color-text)]'
                : 'bg-[var(--color-primary)] text-white hover:opacity-90',
            )}
          >
            {showAddForm ? <X size={14} /> : <Plus size={14} />}
            {showAddForm ? 'Cancel' : 'Add Service'}
          </button>
          <button
            onClick={() => refetch()}
            disabled={isFetching}
            className={cn(
              'rounded-lg p-1.5 text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)]',
              isFetching && 'animate-spin',
            )}
            title="Refresh"
          >
            <RefreshCw size={14} />
          </button>
        </div>
      </div>

      {/* Add Service form */}
      {showAddForm && (
        <div className="rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-4">
          <h2 className="mb-3 text-sm font-semibold text-[var(--color-text)]">Add Service</h2>
          <div className="grid grid-cols-2 gap-3">
            {/* Combobox: service name with suggestions */}
            <div ref={comboRef} className="relative">
              <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Service</label>
              <div className="relative">
                <input
                  type="text"
                  value={addForm.name}
                  onChange={(e) => {
                    setAddForm((f) => ({ ...f, name: e.target.value }))
                    setComboOpen(true)
                  }}
                  onFocus={() => setComboOpen(true)}
                  placeholder="Select or type a service name..."
                  className={cn(
                    'w-full rounded border border-[var(--color-border)] bg-[var(--color-background)] px-2.5 py-1.5 pr-8',
                    'text-sm text-[var(--color-text)] outline-none focus:ring-1 focus:ring-[var(--color-primary)]',
                  )}
                />
                <button
                  type="button"
                  onClick={() => setComboOpen((v) => !v)}
                  className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
                >
                  <ChevronDown size={14} />
                </button>
              </div>
              {comboOpen && filteredSuggestions.length > 0 && (
                <div className="absolute z-10 mt-1 max-h-48 w-full overflow-y-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] shadow-lg">
                  {filteredSuggestions.map((s) => (
                    <button
                      key={s.id}
                      type="button"
                      onClick={() => selectSuggestion(s)}
                      className="flex w-full flex-col px-3 py-2 text-left hover:bg-[var(--color-surface-alt)]"
                    >
                      <span className="text-sm font-medium text-[var(--color-text)]">
                        {s.name}
                        <span className="ml-2 font-normal text-[var(--color-text-muted)]">:{s.port}</span>
                      </span>
                      <span className="text-xs text-[var(--color-text-muted)]">{s.description}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>
            <div>
              <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Description</label>
              <input
                type="text"
                value={addForm.description}
                onChange={(e) => setAddForm((f) => ({ ...f, description: e.target.value }))}
                placeholder="e.g. Camera streaming via WebRTC"
                className={cn(
                  'w-full rounded border border-[var(--color-border)] bg-[var(--color-background)] px-2.5 py-1.5',
                  'text-sm text-[var(--color-text)] outline-none focus:ring-1 focus:ring-[var(--color-primary)]',
                )}
              />
            </div>
            <div>
              <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Host</label>
              <input
                type="text"
                value={addForm.host}
                onChange={(e) => setAddForm((f) => ({ ...f, host: e.target.value }))}
                placeholder="localhost"
                className={cn(
                  'w-full rounded border border-[var(--color-border)] bg-[var(--color-background)] px-2.5 py-1.5',
                  'text-sm text-[var(--color-text)] outline-none focus:ring-1 focus:ring-[var(--color-primary)]',
                )}
              />
            </div>
            <div className="flex gap-3">
              <div className="flex-1">
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Port</label>
                <input
                  type="number"
                  value={addForm.port}
                  onChange={(e) => setAddForm((f) => ({ ...f, port: e.target.value }))}
                  placeholder="e.g. 1984"
                  className={cn(
                    'w-full rounded border border-[var(--color-border)] bg-[var(--color-background)] px-2.5 py-1.5',
                    'text-sm text-[var(--color-text)] outline-none focus:ring-1 focus:ring-[var(--color-primary)]',
                  )}
                />
              </div>
              <div className="w-20">
                <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Scheme</label>
                <select
                  value={addForm.scheme}
                  onChange={(e) => setAddForm((f) => ({ ...f, scheme: e.target.value }))}
                  className={cn(
                    'w-full rounded border border-[var(--color-border)] bg-[var(--color-background)] px-2 py-1.5',
                    'text-sm text-[var(--color-text)] outline-none focus:ring-1 focus:ring-[var(--color-primary)]',
                  )}
                >
                  <option value="http">http</option>
                  <option value="https">https</option>
                  <option value="mqtt">mqtt</option>
                  <option value="mqtts">mqtts</option>
                  <option value="ws">ws</option>
                  <option value="wss">wss</option>
                </select>
              </div>
            </div>
            <div>
              <label className="mb-1 block text-xs text-[var(--color-text-muted)]">Health Path</label>
              <input
                type="text"
                value={addForm.health_path}
                onChange={(e) => setAddForm((f) => ({ ...f, health_path: e.target.value }))}
                placeholder="/health"
                className={cn(
                  'w-full rounded border border-[var(--color-border)] bg-[var(--color-background)] px-2.5 py-1.5',
                  'text-sm text-[var(--color-text)] outline-none focus:ring-1 focus:ring-[var(--color-primary)]',
                )}
              />
            </div>
          </div>
          <div className="mt-3 flex justify-end">
            <button
              onClick={handleAddService}
              disabled={addMutation.isPending || !addForm.name.trim() || !addForm.port}
              className={cn(
                'rounded-lg bg-[var(--color-primary)] px-4 py-1.5 text-sm font-medium text-white',
                'hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50',
              )}
            >
              {addMutation.isPending ? 'Adding...' : 'Add Service'}
            </button>
          </div>
        </div>
      )}

      {/* Service list */}
      <div className="space-y-2">
        {data?.services.map((entry) => {
          const state = getRowState(entry.name)
          const health = healthStates[entry.name] ?? { status: 'idle' as const }
          return (
            <ServiceRegistrationRow
              key={entry.name}
              entry={entry}
              checked={state.checked}
              host={state.host}
              port={state.port}
              scheme={state.scheme}
              onToggle={() => {
                const nowChecked = !state.checked
                updateRow(entry.name, { ...state, checked: nowChecked })
                if (nowChecked) {
                  probeHealth(entry.name, state.host, state.port, entry.health_path, state.scheme)
                } else {
                  setHealthStates((prev) => ({ ...prev, [entry.name]: { status: 'idle' } }))
                }
              }}
              onHostChange={(host) => updateRow(entry.name, { ...state, host })}
              onPortChange={(port) => updateRow(entry.name, { ...state, port })}
              onSchemeChange={(scheme) => updateRow(entry.name, { ...state, scheme })}
              onHostBlur={() => {
                if (state.checked) {
                  probeHealth(entry.name, state.host, state.port, entry.health_path, state.scheme)
                }
              }}
              onPortBlur={() => {
                if (state.checked) {
                  probeHealth(entry.name, state.host, state.port, entry.health_path, state.scheme)
                }
              }}
              onRotateKey={() => handleRotateKey(entry.name)}
              onDelete={() => handleDeleteService(entry.name)}
              disabled={mutation.isPending}
              rotating={rotatingService === entry.name}
              deleting={deletingService === entry.name}
              healthStatus={health.status}
              healthError={health.error}
              healthLatency={health.latency}
            />
          )
        })}
      </div>

      {/* Register button */}
      <button
        onClick={handleRegister}
        disabled={mutation.isPending || checkedCount === 0}
        className={cn(
          'rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white',
          'hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50',
        )}
      >
        {mutation.isPending ? 'Registering...' : `Register Selected (${checkedCount})`}
      </button>

      {/* Rotate key result */}
      {rotateResult && (
        <KeyResultPanel
          serviceName={rotateResult.service_name}
          appKey={rotateResult.app_key}
          envWritten={rotateResult.env_written}
          copiedKey={copiedKey}
          onCopy={handleCopyKey}
          label="Rotated Key"
        />
      )}

      {/* Registration results */}
      {results && (
        <div className="space-y-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          <h2 className="text-sm font-semibold text-[var(--color-text)]">Registration Results</h2>
          {results.map((r) => (
            <div
              key={r.name}
              className={cn(
                'flex flex-wrap items-center gap-3 rounded-lg border p-3 text-sm',
                r.config_ok && r.auth_ok
                  ? 'border-green-500/20 bg-green-500/5'
                  : 'border-yellow-500/20 bg-yellow-500/5',
              )}
            >
              <span className="min-w-[180px] font-medium text-[var(--color-text)]">{r.name}</span>

              <span
                className={cn(
                  'text-xs',
                  r.config_ok ? 'text-green-600 dark:text-green-400' : 'text-red-500',
                )}
              >
                Config: {r.config_ok ? 'OK' : 'Failed'}
              </span>

              <span
                className={cn(
                  'text-xs',
                  r.auth_ok ? 'text-green-600 dark:text-green-400' : 'text-red-500',
                )}
              >
                Auth: {r.auth_ok ? (r.auth_created ? 'Created' : 'Exists') : 'Failed'}
              </span>

              {r.env_written !== null && (
                <span
                  className={cn(
                    'text-xs',
                    r.env_written ? 'text-green-600 dark:text-green-400' : 'text-red-500',
                  )}
                >
                  .env: {r.env_written ? 'Written' : 'Failed'}
                </span>
              )}

              {r.error && (
                <span className="flex items-center gap-1 text-xs text-yellow-600 dark:text-yellow-400">
                  <AlertTriangle size={12} />
                  {r.error}
                </span>
              )}

              {r.auth_created && r.app_key && (
                <div className="mt-1 w-full">
                  <div className="mb-1 flex items-center gap-1 text-xs font-medium text-yellow-600 dark:text-yellow-400">
                    <AlertTriangle size={12} />
                    App key (shown once only):
                  </div>
                  <div className="flex items-center gap-2">
                    <code className="flex-1 overflow-x-auto rounded bg-[var(--color-background)] px-2 py-1 font-mono text-xs text-[var(--color-text)]">
                      {r.app_key}
                    </code>
                    <button
                      onClick={() => handleCopyKey(r.app_key!)}
                      className="rounded p-1 hover:bg-[var(--color-surface-alt)]"
                      title="Copy to clipboard"
                    >
                      {copiedKey === r.app_key ? (
                        <Check size={14} className="text-green-500" />
                      ) : (
                        <Copy size={14} className="text-[var(--color-text-muted)]" />
                      )}
                    </button>
                  </div>
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Restart notice */}
      {hasEnvWrites && (
        <div className="flex items-start gap-2 rounded-lg border border-blue-500/20 bg-blue-500/5 p-3">
          <Info size={16} className="mt-0.5 shrink-0 text-blue-500" />
          <p className="text-sm text-[var(--color-text)]">
            Services whose .env files were updated need to be restarted to pick up the new
            credentials. Restart each affected service's container or process.
          </p>
        </div>
      )}
    </div>
  )
}

function KeyResultPanel({
  serviceName,
  appKey,
  envWritten,
  copiedKey,
  onCopy,
  label,
}: {
  serviceName: string
  appKey: string
  envWritten: boolean | null
  copiedKey: string | null
  onCopy: (key: string) => void
  label: string
}) {
  return (
    <div className="space-y-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <h2 className="text-sm font-semibold text-[var(--color-text)]">
        {label}: {serviceName}
      </h2>
      <div className="rounded-lg border border-green-500/20 bg-green-500/5 p-3">
        {envWritten !== null && (
          <span
            className={cn(
              'mb-2 inline-block text-xs',
              envWritten ? 'text-green-600 dark:text-green-400' : 'text-red-500',
            )}
          >
            .env: {envWritten ? 'Written' : 'Failed'}
          </span>
        )}
        <div>
          <div className="mb-1 flex items-center gap-1 text-xs font-medium text-yellow-600 dark:text-yellow-400">
            <AlertTriangle size={12} />
            New app key (shown once only):
          </div>
          <div className="flex items-center gap-2">
            <code className="flex-1 overflow-x-auto rounded bg-[var(--color-background)] px-2 py-1 font-mono text-xs text-[var(--color-text)]">
              {appKey}
            </code>
            <button
              onClick={() => onCopy(appKey)}
              className="rounded p-1 hover:bg-[var(--color-surface-alt)]"
              title="Copy to clipboard"
            >
              {copiedKey === appKey ? (
                <Check size={14} className="text-green-500" />
              ) : (
                <Copy size={14} className="text-[var(--color-text-muted)]" />
              )}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
