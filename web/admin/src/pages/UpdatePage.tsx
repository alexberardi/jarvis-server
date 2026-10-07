import { useState, useEffect, useRef, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { ArrowUpCircle, CheckCircle2, Loader2, AlertTriangle, RefreshCw, Download } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'
import { setUpdatesEnabled, getUpgradeStatus } from '@/api/update'

type Phase = 'idle' | 'preflight' | 'download' | 'binary' | 'restarting' | 'compose' | 'pull' | 'restart' | 'native' | 'verify' | 'done' | 'error'

interface LogLine {
  text: string
  phase?: string
}

const PHASE_LABELS: Record<string, string> = {
  preflight: 'Checking prerequisites',
  download: 'Downloading update',
  binary: 'Updating binary',
  restarting: 'Restarting admin server',
  compose: 'Updating configuration',
  pull: 'Pulling images',
  restart: 'Restarting services',
  // macOS only: whisper / tts / llm-proxy run as launchd agents from git
  // checkouts, so `docker compose pull` never touched them. The step no-ops on
  // Linux, where everything is a container.
  native: 'Updating native services',
  verify: 'Verifying health',
  done: 'Complete',
}

export default function UpdatePage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { data: updateInfo, isLoading } = useUpdateCheck()
  const [phase, setPhase] = useState<Phase>('idle')
  const [logs, setLogs] = useState<LogLine[]>([])
  const [error, setError] = useState<string | null>(null)
  const [isTogglingUpdates, setIsTogglingUpdates] = useState(false)
  // The native-services step only exists on macOS. Don't render the row (and
  // don't let the index-based completedPhases paint it green) on a box that has
  // no native services — that's the same "tick a step that never ran" bug this
  // page just got fixed for.
  const [sawNativePhase, setSawNativePhase] = useState(false)
  const logRef = useRef<HTMLDivElement>(null)

  const updatesEnabled = updateInfo?.updatesEnabled

  /**
   * Flip the opt-in, then re-run the check. Enabling it is the first moment the
   * server is allowed to contact GitHub, so the freshly-invalidated query is
   * what actually surfaces "an update is available" — without this refetch the
   * user would flip the switch and still see the stale "no update" answer.
   */
  const toggleUpdates = useCallback(
    async (next: boolean) => {
      setIsTogglingUpdates(true)
      setError(null)
      try {
        await setUpdatesEnabled(next)
        await queryClient.invalidateQueries({ queryKey: ['update-check'] })
      } catch {
        setError(
          next
            ? 'Could not turn on update checks. You need to be signed in as an admin.'
            : 'Could not turn off update checks.',
        )
      } finally {
        setIsTogglingUpdates(false)
      }
    },
    [queryClient],
  )

  const addLog = useCallback((line: LogLine) => {
    setLogs((prev) => [...prev, line])
  }, [])

  // Auto-scroll log
  useEffect(() => {
    if (logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight
    }
  }, [logs])

  /**
   * Follow the server-side tail of the upgrade.
   *
   * Swapping the binary restarts the admin process, which kills the SSE stream —
   * so compose regen, image pull, service restart and health verify all run in
   * the NEW process with no client attached. The page used to simply assert
   * "Upgrade complete!" at that point, while docker was still pulling images.
   *
   * The server tracks the real state in ~/.jarvis/upgrade-in-progress.json and
   * exposes it at /api/update/status; it clears the marker only once the upgrade
   * genuinely finishes. So: poll until it's gone.
   */
  const followServerUpgrade = useCallback(async () => {
    const DEADLINE = Date.now() + 20 * 60 * 1000 // pulls can be slow, but not endless
    let lastPhase = ''

    while (Date.now() < DEADLINE) {
      try {
        const status = await getUpgradeStatus()

        if (status.phase === 'error') {
          const msg = status.error ?? 'Upgrade failed after restart'
          setError(msg)
          setPhase('error')
          addLog({ text: `Error: ${msg}` })
          return
        }

        // Marker gone → the server finished and recorded the new version.
        if (!status.inProgress) {
          setPhase('done')
          addLog({ text: 'Upgrade complete!' })
          await queryClient.invalidateQueries({ queryKey: ['update-check'] })
          return
        }

        if (status.phase && status.phase !== lastPhase) {
          lastPhase = status.phase
          const label = PHASE_LABELS[status.phase]
          if (label) addLog({ text: `${label}...` })
          // 'binary-updated' is the marker's own bookkeeping, not a UI phase;
          // the work it precedes is the compose step.
          if (status.phase === 'native') setSawNativePhase(true)
          setPhase(status.phase === 'binary-updated' ? 'compose' : (status.phase as Phase))
        }
      } catch {
        // Admin may still be bouncing — keep polling rather than declaring failure.
      }

      await new Promise((r) => setTimeout(r, 3000))
    }

    setError('Upgrade is taking longer than expected — check the admin logs.')
    setPhase('error')
  }, [addLog, queryClient])

  async function handleUpdate() {
    setPhase('preflight')
    setLogs([])
    setError(null)

    try {
      // AuthContext stores under the namespaced key; legacy "access_token" is
      // only set by the setup wizard's account step. Try the namespaced one
      // first so a fresh login through the login page works.
      const token =
        localStorage.getItem('jarvis-admin:access_token') ?? localStorage.getItem('access_token')
      const res = await fetch('/api/update/apply', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
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

            if (data.phase === 'native') setSawNativePhase(true)
            if (data.phase) setPhase(data.phase as Phase)
            if (data.message) addLog({ text: data.message, phase: data.phase })
            if (data.stream && data.text) addLog({ text: data.text.trim() })

            if (data.restart) {
              setPhase('restarting')
              addLog({ text: 'Admin server restarting with new version...' })
              await waitForRestart(updateInfo?.latestVersion ?? '')

              // The binary swap kills this SSE stream, so the rest of the
              // upgrade (compose → pull → restart → verify) runs server-side in
              // the NEW process with nobody listening. We used to just declare
              // "Upgrade complete!" here — while docker was still pulling images
              // — and the phase list rendered every remaining step as a green
              // tick, because completedPhases is derived from the phase index.
              // Follow the real work instead: poll /api/update/status until the
              // server clears its upgrade marker.
              addLog({ text: 'Admin back up. Finishing upgrade (configuration, images, services)...' })
              await followServerUpgrade()
              return
            }

            if (data.done && data.code !== 0) {
              throw new Error(data.error ?? 'Upgrade failed')
            }
            if (data.done && data.code === 0) {
              setPhase('done')
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

  if (isLoading) {
    return (
      <div className="flex min-h-[50vh] items-center justify-center">
        <Loader2 size={24} className="animate-spin text-[var(--color-primary)]" />
      </div>
    )
  }

  const completedPhases = Object.keys(PHASE_LABELS).filter((p) => {
    const order = Object.keys(PHASE_LABELS)
    const currentIdx = order.indexOf(phase)
    const phaseIdx = order.indexOf(p)
    return phaseIdx < currentIdx
  })

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <div>
        <h1 className="text-2xl font-bold text-[var(--color-text)]">Update Jarvis</h1>
        {updatesEnabled === false ? (
          // Never claim "you're up to date" here: with update checks off, the
          // server short-circuits to updateAvailable:false WITHOUT contacting
          // GitHub, so we genuinely don't know. Saying otherwise would tell
          // someone sitting on a vulnerable release that they're current.
          <p className="text-sm text-[var(--color-text-muted)]">
            Update checks are off — currently running v{updateInfo?.currentVersion ?? '?'}
          </p>
        ) : updateInfo?.updateAvailable ? (
          <p className="text-sm text-[var(--color-text-muted)]">
            v{updateInfo.currentVersion} → v{updateInfo.latestVersion}
          </p>
        ) : (
          <p className="text-sm text-[var(--color-text-muted)]">
            You're running the latest version (v{updateInfo?.currentVersion ?? '?'})
          </p>
        )}
      </div>

      {/* Update opt-in. Jarvis makes no outbound calls unless you allow it, so
          this is off by default — but it used to be reachable only by editing a
          launchd plist / compose .env and restarting, which put updates out of
          reach for most self-hosters.

          ALWAYS rendered. It's a settings control, not a step in the upgrade: you
          might well want to switch checks back off the moment an update finishes.
          An earlier cut hid it unless phase === 'idle', which meant that running
          a single update made the toggle disappear for the rest of the session. */}
      <div className="flex items-start justify-between gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <Download size={16} className="text-[var(--color-text-muted)]" />
            <span className="text-sm font-medium text-[var(--color-text)]">Check for updates</span>
          </div>
          <p className="text-xs text-[var(--color-text-muted)]">
            Lets Jarvis contact GitHub to see if a new release is available. This is the only
            outbound connection it makes — turn it off to stay fully offline.
          </p>
        </div>

        {/* Track is 44x24 (w-11/h-6) with a 20px knob (h-5/w-5) inset 2px, so the
            knob travels exactly 44 - 20 - 2 - 2 = 20px (translate-x-5).
            `p-0` matters: a <button> carries UA padding, and the knob is
            positioned `absolute` — without an explicit `left` its origin starts
            AFTER that padding, which pushed it past the right edge of the track. */}
        <button
          role="switch"
          aria-checked={updatesEnabled === true}
          aria-label="Check for updates"
          disabled={isTogglingUpdates || updatesEnabled === undefined}
          onClick={() => toggleUpdates(!updatesEnabled)}
          className={cn(
            'relative mt-1 h-6 w-11 shrink-0 rounded-full border-0 p-0 transition-colors disabled:opacity-50',
            updatesEnabled ? 'bg-green-600' : 'bg-[var(--color-border)]',
          )}
        >
          <span
            className={cn(
              'absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white shadow-sm transition-transform',
              updatesEnabled ? 'translate-x-5' : 'translate-x-0',
            )}
          />
        </button>
      </div>

      {/* Phase progress */}
      {phase !== 'idle' && (
        <div className="space-y-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          {Object.entries(PHASE_LABELS)
            .filter(([key]) => key !== 'native' || sawNativePhase)
            .map(([key, label]) => {
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

      {/* Log output */}
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

      {/* Error */}
      {error && (
        <div className="flex items-center gap-2 rounded-lg bg-red-500/10 px-3 py-2">
          <AlertTriangle size={14} className="text-red-500" />
          <span className="text-sm text-red-500">{error}</span>
        </div>
      )}

      {/* Actions */}
      <div className="flex gap-3">
        {phase === 'idle' && updateInfo?.updateAvailable && (
          <button
            onClick={handleUpdate}
            className="flex items-center gap-2 rounded-lg bg-green-600 px-4 py-2 text-sm font-medium text-white hover:opacity-90"
          >
            <ArrowUpCircle size={16} />
            Update to v{updateInfo.latestVersion}
          </button>
        )}
        {phase === 'done' && (
          <button
            onClick={() => navigate('/dashboard')}
            className="flex items-center gap-2 rounded-lg bg-[var(--color-primary)] px-4 py-2 text-sm font-medium text-white hover:opacity-90"
          >
            <CheckCircle2 size={16} />
            Go to Dashboard
          </button>
        )}
        {phase === 'error' && (
          <button
            onClick={handleUpdate}
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

/** Poll /health until the new version responds. */
async function waitForRestart(_targetVersion: string, timeoutMs = 60_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 2000))
    try {
      const res = await fetch('/health')
      if (res.ok) {
        const data = await res.json() as { version?: string }
        if (data.version && data.version !== '0.0.0-dev') {
          return // New version is up
        }
      }
    } catch {
      // Not ready yet
    }
  }
  throw new Error('Admin server did not restart within 60 seconds')
}
