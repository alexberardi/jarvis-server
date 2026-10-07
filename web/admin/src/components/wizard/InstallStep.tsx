import { useState, useCallback, useEffect } from 'react'
import { CheckCircle2, XCircle, Loader2, Play, RotateCcw, AlertTriangle, FileText } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useWizard } from '@/context/WizardContext'
import { useInstallStream } from '@/hooks/useInstallStream'
import { generateInstall, runPreflight } from '@/api/install'
import TerminalOutput from './TerminalOutput'
import type { PreflightResult, ServiceHealthResult } from '@/types/wizard'

type Phase = 'idle' | 'preflight' | 'generating' | 'pulling' | 'starting' | 'registering' | 'native' | 'done' | 'error'

export default function InstallStep() {
  const { state, dispatch } = useWizard()
  const pullStream = useInstallStream()
  const startStream = useInstallStream()
  const nativeStream = useInstallStream()
  const [phase, setPhase] = useState<Phase>('idle')
  const [error, setError] = useState<string | null>(null)
  const [preflightResult, setPreflightResult] = useState<PreflightResult | null>(null)
  const [serviceHealth, setServiceHealth] = useState<Record<string, ServiceHealthResult>>({})
  const [containerLogs, setContainerLogs] = useState<Record<string, string>>({})
  const [failedPhase, setFailedPhase] = useState<Phase | null>(null)
  const [redirectCountdown, setRedirectCountdown] = useState<number | null>(null)
  const [currentNativeId, setCurrentNativeId] = useState<string | null>(null)
  const [nativeResults, setNativeResults] = useState<Record<string, { ok: boolean; error?: string }>>({})

  // Auto-redirect to the containerized admin dashboard after install.
  // On macOS native there is no container to hand off to — THIS process keeps
  // serving the wizard. A full-page redirect would reload the app into the
  // "deployed-needs-account" path and skip the remaining in-session steps
  // (Account, Models). So on darwin we stay put and let the user click Next.
  useEffect(() => {
    const redirectUrl = startStream.redirect
    if (phase !== 'done' || !redirectUrl) return
    if (state.platform === 'darwin') return

    setRedirectCountdown(5)
    const interval = setInterval(() => {
      setRedirectCountdown((prev) => {
        if (prev === null || prev <= 1) {
          clearInterval(interval)
          window.location.href = redirectUrl
          return 0
        }
        return prev - 1
      })
    }, 1000)

    return () => clearInterval(interval)
  }, [phase, startStream.redirect])

  const runPreflightCheck = useCallback(async (): Promise<boolean> => {
    setPhase('preflight')
    setPreflightResult(null)
    setError(null)

    try {
      const result = await runPreflight(state.enabledModules)
      setPreflightResult(result)

      if (!result.canProceed) {
        setError('Pre-flight checks failed. Fix the issues above and retry.')
        setPhase('error')
        setFailedPhase('preflight')
        return false
      }
      return true
    } catch (err) {
      setError(`Pre-flight check failed: ${err instanceof Error ? err.message : String(err)}`)
      setPhase('error')
      setFailedPhase('preflight')
      return false
    }
  }, [state.enabledModules])

  /**
   * Install LaunchAgents for every service the user opted into native mode.
   * Sequential so the wizard's terminal output stays readable. Each install
   * can take minutes on first run (pip build of pywhispercpp, MLX wheels).
   */
  async function runNativeInstalls() {
    if (state.nativeServices.length === 0) return
    setPhase('native')
    for (const id of state.nativeServices) {
      setCurrentNativeId(id)
      try {
        await nativeStream.run(`/api/native-services/${id}/install`)
        setNativeResults((prev) => ({ ...prev, [id]: { ok: true } }))
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err)
        setNativeResults((prev) => ({ ...prev, [id]: { ok: false, error: message } }))
        // Don't abort the whole install — let other natives try, surface failures at end
      }
    }
    setCurrentNativeId(null)
  }

  async function runInstall() {
    setError(null)
    setServiceHealth({})
    setFailedPhase(null)
    setNativeResults({})
    dispatch({ type: 'SET_INSTALL_RUNNING', running: true })

    try {
      // Phase 0: Pre-flight checks
      const preflightOk = await runPreflightCheck()
      if (!preflightOk) {
        dispatch({ type: 'SET_INSTALL_RUNNING', running: false })
        return
      }

      // Phase 1: Generate config files
      setPhase('generating')
      await generateInstall(state)

      // Phase 2: Pull images (SSE stream)
      setPhase('pulling')
      await pullStream.run('/api/install/pull')

      // Phase 3: Tiered startup — infra -> config -> auth -> register -> all services
      // Parse SSE events for per-service health
      setPhase('starting')
      await startStream.run('/api/install/start', (data) => {
        // Handle per-service health events from SSE
        if (data.phase === 'health' && typeof data.service === 'string') {
          setServiceHealth((prev) => ({
            ...prev,
            [data.service as string]: {
              healthy: data.healthy as boolean,
              error: data.error as string | undefined,
            },
          }))
        }
        // Handle final serviceHealth payload
        if (data.serviceHealth && typeof data.serviceHealth === 'object') {
          const healthMap = data.serviceHealth as Record<string, ServiceHealthResult>
          setServiceHealth((prev) => ({ ...prev, ...healthMap }))
        }
      })

      // Phase 4: install LaunchAgents (only on macOS with native opt-ins). The
      // backend filtered these out of compose, so they don't run yet — install
      // happens AFTER startup so the .env has app keys from registration.
      await runNativeInstalls()

      setPhase('done')
      dispatch({ type: 'SET_INSTALL_COMPLETE' })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setPhase('error')
      setFailedPhase(phase)
      dispatch({ type: 'SET_INSTALL_RUNNING', running: false })
    }
  }

  async function handleRetry() {
    if (failedPhase === 'preflight') {
      // Just re-run preflight, then continue if it passes
      setError(null)
      dispatch({ type: 'SET_INSTALL_RUNNING', running: true })
      const ok = await runPreflightCheck()
      if (!ok) {
        dispatch({ type: 'SET_INSTALL_RUNNING', running: false })
        return
      }
      // Continue with the rest of the install
      try {
        setPhase('generating')
        await generateInstall(state)

        setPhase('pulling')
        await pullStream.run('/api/install/pull')

        setPhase('starting')
        await startStream.run('/api/install/start', (data) => {
          if (data.phase === 'health' && typeof data.service === 'string') {
            setServiceHealth((prev) => ({
              ...prev,
              [data.service as string]: {
                healthy: data.healthy as boolean,
                error: data.error as string | undefined,
              },
            }))
          }
          if (data.serviceHealth && typeof data.serviceHealth === 'object') {
            const healthMap = data.serviceHealth as Record<string, ServiceHealthResult>
            setServiceHealth((prev) => ({ ...prev, ...healthMap }))
          }
        })

        await runNativeInstalls()

        setPhase('done')
        dispatch({ type: 'SET_INSTALL_COMPLETE' })
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
        setPhase('error')
        dispatch({ type: 'SET_INSTALL_RUNNING', running: false })
      }
    } else {
      // Full retry
      runInstall()
    }
  }

  async function viewLogs(serviceId: string) {
    try {
      const res = await fetch(`/api/containers/${serviceId}/logs`)
      if (res.ok) {
        const text = await res.text()
        setContainerLogs((prev) => ({ ...prev, [serviceId]: text }))
      } else {
        setContainerLogs((prev) => ({ ...prev, [serviceId]: `Failed to fetch logs: HTTP ${res.status}` }))
      }
    } catch (err) {
      setContainerLogs((prev) => ({
        ...prev,
        [serviceId]: `Failed to fetch logs: ${err instanceof Error ? err.message : String(err)}`,
      }))
    }
  }

  const phases: Array<{ key: Phase; label: string }> = [
    { key: 'preflight', label: 'Pre-flight checks' },
    { key: 'generating', label: 'Generate configuration' },
    { key: 'pulling', label: 'Pull Docker images' },
    { key: 'starting', label: 'Start services' },
    ...(state.nativeServices.length > 0
      ? [{ key: 'native' as Phase, label: `Install ${state.nativeServices.length} native service${state.nativeServices.length === 1 ? '' : 's'}` }]
      : []),
  ]

  const phaseOrder = phases.map((p) => p.key)
  const currentPhaseIdx = phaseOrder.indexOf(phase as Phase)

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold text-[var(--color-text)]">Install</h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          {phase === 'idle'
            ? 'Ready to install. This will pull Docker images and start all services.'
            : phase === 'done'
              ? startStream.redirect && state.platform !== 'darwin'
                ? `Installation complete! Redirecting to your admin dashboard in ${redirectCountdown ?? '...'}s...`
                : 'Installation complete! Click Next to create your account and choose your models.'
              : phase === 'error'
                ? 'Installation encountered an error.'
                : 'Installing...'}
        </p>
      </div>

      {/* Pre-flight results */}
      {preflightResult && (
        <div className="rounded-lg border border-[var(--color-border)] overflow-hidden">
          <div className="border-b border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2">
            <span className="text-xs font-medium text-[var(--color-text-muted)]">Pre-flight Checks</span>
          </div>
          <div className="divide-y divide-[var(--color-border)]">
            {preflightResult.checks.map((check) => (
              <div key={check.name} className="flex items-center justify-between px-3 py-2">
                <div className="flex items-center gap-2">
                  {check.status === 'pass' ? (
                    <CheckCircle2 size={14} className="text-green-500" />
                  ) : check.status === 'warn' ? (
                    <AlertTriangle size={14} className="text-amber-500" />
                  ) : (
                    <XCircle size={14} className="text-red-500" />
                  )}
                  <div>
                    <span className="text-sm text-[var(--color-text)]">{check.name}</span>
                    <p className="text-xs text-[var(--color-text-muted)]">{check.message}</p>
                    {check.details && check.status !== 'pass' && (
                      <p className="text-xs text-[var(--color-text-muted)] opacity-60">{check.details}</p>
                    )}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Progress steps */}
      <div className="space-y-2">
        {phases.map((p, i) => {
          const isActive = p.key === phase
          const isDone = currentPhaseIdx > i || phase === 'done'
          const isFailed = phase === 'error' && p.key === failedPhase

          return (
            <div key={p.key} className="flex items-center gap-3">
              {isDone ? (
                <CheckCircle2 size={18} className="text-green-500" />
              ) : isFailed ? (
                <XCircle size={18} className="text-red-500" />
              ) : isActive ? (
                <Loader2 size={18} className="animate-spin text-[var(--color-primary)]" />
              ) : (
                <div className="h-[18px] w-[18px] rounded-full border-2 border-[var(--color-border)]" />
              )}
              <span
                className={cn(
                  'text-sm',
                  isDone
                    ? 'text-green-500'
                    : isActive
                      ? 'font-medium text-[var(--color-text)]'
                      : 'text-[var(--color-text-muted)]',
                )}
              >
                {p.label}
              </span>
            </div>
          )
        })}
      </div>

      {/* Per-service health during starting phase */}
      {(phase === 'starting' || phase === 'done' || phase === 'error') &&
        Object.keys(serviceHealth).length > 0 && (
          <div className="rounded-lg border border-[var(--color-border)] overflow-hidden">
            <div className="border-b border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2">
              <span className="text-xs font-medium text-[var(--color-text-muted)]">Service Status</span>
            </div>
            <div className="divide-y divide-[var(--color-border)]">
              {Object.entries(serviceHealth).map(([id, status]) => (
                <div key={id}>
                  <div className="flex items-center justify-between px-3 py-2">
                    <span className="text-sm text-[var(--color-text)]">{id}</span>
                    <div className="flex items-center gap-2">
                      {!status.healthy && (
                        <button
                          type="button"
                          onClick={() => viewLogs(id)}
                          className="flex items-center gap-1 text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
                        >
                          <FileText size={12} />
                          Logs
                        </button>
                      )}
                      {status.healthy ? (
                        <CheckCircle2 size={14} className="text-green-500" />
                      ) : (
                        <XCircle size={14} className="text-red-500" />
                      )}
                    </div>
                  </div>
                  {status.error && (
                    <p className="px-3 pb-2 text-xs text-red-400">{status.error}</p>
                  )}
                  {containerLogs[id] && (
                    <div className="mx-3 mb-2 max-h-32 overflow-auto rounded bg-[var(--color-background)] p-2 text-xs font-mono text-[var(--color-text-muted)]">
                      <pre className="whitespace-pre-wrap">{containerLogs[id]}</pre>
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>
        )}

      {/* Terminal output */}
      {(pullStream.lines.length > 0 || pullStream.running) && (
        <TerminalOutput
          lines={pullStream.lines}
          running={pullStream.running}
          title="docker compose pull"
        />
      )}

      {(startStream.lines.length > 0 || startStream.running) && (
        <TerminalOutput
          lines={startStream.lines}
          running={startStream.running}
          title="docker compose up -d"
        />
      )}

      {/* Per-service native install results */}
      {(phase === 'native' || Object.keys(nativeResults).length > 0) && (
        <div className="rounded-lg border border-[var(--color-border)] overflow-hidden">
          <div className="border-b border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2">
            <span className="text-xs font-medium text-[var(--color-text-muted)]">
              Native Services (LaunchAgents)
            </span>
          </div>
          <div className="divide-y divide-[var(--color-border)]">
            {state.nativeServices.map((id) => {
              const result = nativeResults[id]
              const isCurrent = currentNativeId === id && phase === 'native'
              return (
                <div key={id} className="px-3 py-2">
                  <div className="flex items-center justify-between">
                    <span className="text-sm text-[var(--color-text)]">{id}</span>
                    {result?.ok ? (
                      <CheckCircle2 size={14} className="text-green-500" />
                    ) : result ? (
                      <XCircle size={14} className="text-red-500" />
                    ) : isCurrent ? (
                      <Loader2 size={14} className="animate-spin text-[var(--color-primary)]" />
                    ) : (
                      <div className="h-3 w-3 rounded-full border border-[var(--color-border)]" />
                    )}
                  </div>
                  {result?.error && (
                    <p className="mt-1 text-xs text-red-400">{result.error}</p>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {(nativeStream.lines.length > 0 || nativeStream.running) && (
        <TerminalOutput
          lines={nativeStream.lines}
          running={nativeStream.running}
          title={currentNativeId ? `deploy-launchd.sh (${currentNativeId})` : 'deploy-launchd.sh'}
        />
      )}

      {/* Error */}
      {error && (
        <div className="rounded-lg bg-red-500/10 px-3 py-2 text-sm text-red-500">{error}</div>
      )}

      {/* Start / Retry button */}
      {(phase === 'idle' || phase === 'error') && (
        <button
          type="button"
          onClick={phase === 'error' ? handleRetry : runInstall}
          className={cn(
            'flex w-full items-center justify-center gap-2 rounded-lg bg-[var(--color-primary)] px-4 py-3 font-medium text-white',
            'hover:opacity-90 transition-opacity',
          )}
        >
          {phase === 'error' ? <RotateCcw size={18} /> : <Play size={18} />}
          {phase === 'error' ? 'Retry Installation' : 'Start Installation'}
        </button>
      )}
    </div>
  )
}
