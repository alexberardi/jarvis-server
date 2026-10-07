import { useEffect, useState } from 'react'
import { Monitor, Server, AlertCircle, CheckCircle2, Loader2, RotateCcw } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useWizard } from '@/context/WizardContext'
import { getInstallStatus } from '@/api/install'
import type { InstallStatus } from '@/types/wizard'

export default function WelcomeStep() {
  const { state, dispatch } = useWizard()
  const [status, setStatus] = useState<InstallStatus | null>(null)
  const [checking, setChecking] = useState(true)
  const [dockerFailed, setDockerFailed] = useState(false)

  function checkDocker() {
    setChecking(true)
    setDockerFailed(false)
    getInstallStatus()
      .then((s) => {
        setStatus(s)
        const failed = s.reason === 'docker_not_found'
        setDockerFailed(failed)
        // Block "Next" if Docker is missing
        if (failed) {
          dispatch({ type: 'SET_INSTALL_RUNNING', running: true })
        } else {
          dispatch({ type: 'SET_INSTALL_RUNNING', running: false })
        }
      })
      .catch(() => {
        setStatus({ configured: false, reason: 'error' })
        setDockerFailed(true)
        dispatch({ type: 'SET_INSTALL_RUNNING', running: true })
      })
      .finally(() => setChecking(false))
  }

  useEffect(() => {
    // Intentional one-time probe on mount. checking/dockerFailed already
    // initialise to what checkDocker() resets them to, so the synchronous
    // setState it does is a no-op here (no cascading render).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    checkDocker()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const dockerOk = status && status.reason !== 'docker_not_found'

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold text-[var(--color-text)]">Welcome to Jarvis</h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          Let's get your personal voice assistant up and running. This wizard will guide you
          through selecting services, configuring hardware, and starting everything up.
        </p>
      </div>

      {/* Docker Check */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] p-4">
        <div className="flex items-center gap-3">
          {checking ? (
            <Loader2 size={20} className="animate-spin text-[var(--color-primary)]" />
          ) : dockerOk ? (
            <CheckCircle2 size={20} className="text-green-500" />
          ) : (
            <AlertCircle size={20} className="text-red-500" />
          )}
          <div className="flex-1">
            <p className="text-sm font-medium text-[var(--color-text)]">
              {checking ? 'Checking Docker...' : dockerOk ? 'Docker is running' : 'Docker not found'}
            </p>
            {!checking && !dockerOk && (
              <div className="mt-1 space-y-1">
                <p className="text-xs text-red-400">
                  Docker is required to run Jarvis services.
                </p>
                <p className="text-xs text-[var(--color-text-muted)]">
                  Install{' '}
                  <a
                    href="https://www.docker.com/products/docker-desktop/"
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-[var(--color-primary)] underline hover:no-underline"
                  >
                    Docker Desktop
                  </a>
                  {' '}for your platform, start it, then click Retry.
                </p>
              </div>
            )}
          </div>
          {dockerFailed && !checking && (
            <button
              type="button"
              onClick={checkDocker}
              className="flex items-center gap-1 rounded-md border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
            >
              <RotateCcw size={12} />
              Retry
            </button>
          )}
        </div>
      </div>

      {/* Deployment Mode */}
      <div>
        <label className="mb-2 block text-sm font-medium text-[var(--color-text)]">
          Deployment Mode
        </label>
        <div className="grid grid-cols-2 gap-3">
          <button
            type="button"
            onClick={() => dispatch({ type: 'SET_DEPLOYMENT_MODE', mode: 'local' })}
            className={cn(
              'flex flex-col items-center gap-2 rounded-lg border p-4 transition-colors',
              state.deploymentMode === 'local'
                ? 'border-[var(--color-primary)] bg-[var(--color-primary)]/10'
                : 'border-[var(--color-border)] hover:border-[var(--color-primary)]/50',
            )}
          >
            <Monitor size={24} className="text-[var(--color-primary)]" />
            <span className="text-sm font-medium text-[var(--color-text)]">Local</span>
            <span className="text-xs text-center text-[var(--color-text-muted)]">
              Everything on this machine
            </span>
          </button>
          <button
            type="button"
            onClick={() => dispatch({ type: 'SET_DEPLOYMENT_MODE', mode: 'remote-llm' })}
            className={cn(
              'flex flex-col items-center gap-2 rounded-lg border p-4 transition-colors',
              state.deploymentMode === 'remote-llm'
                ? 'border-[var(--color-primary)] bg-[var(--color-primary)]/10'
                : 'border-[var(--color-border)] hover:border-[var(--color-primary)]/50',
            )}
          >
            <Server size={24} className="text-[var(--color-primary)]" />
            <span className="text-sm font-medium text-[var(--color-text)]">Remote LLM</span>
            <span className="text-xs text-center text-[var(--color-text-muted)]">
              LLM on separate GPU machine
            </span>
          </button>
        </div>
      </div>
    </div>
  )
}
