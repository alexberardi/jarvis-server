import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { RefreshCw, Brain, Server, GitMerge } from 'lucide-react'
import { toast } from 'sonner'
import { useContainers, useRestartContainer } from '@/hooks/useContainers'
import { useLlmStatus } from '@/hooks/useLlmSetup'
import { getInstallStatus } from '@/api/install'
import ServiceHealthCard from '@/components/dashboard/ServiceHealthCard'
import UpdateBanner from '@/components/dashboard/UpdateBanner'
import { cn } from '@/lib/utils'

const LLM_SETUP_DISMISSED_KEY = 'jarvis-admin:llm-setup-dismissed'

export default function DashboardPage() {
  const navigate = useNavigate()
  const [isComposeExport, setIsComposeExport] = useState(false)
  const { data, isLoading, isError, error, refetch, isFetching } = useContainers()
  const restartMutation = useRestartContainer()
  const llmStatus = useLlmStatus()
  const [dismissed, setDismissed] = useState(
    () => !!localStorage.getItem(LLM_SETUP_DISMISSED_KEY),
  )

  useEffect(() => {
    getInstallStatus().then((status) => {
      if (status.deployMode === 'compose-export') {
        setIsComposeExport(true)
      }
    }).catch(() => {})
  }, [])

  const showLlmBanner =
    !!llmStatus.data && !llmStatus.data.configured && !dismissed

  const handleRestart = (id: string) => {
    restartMutation.mutate(id, {
      onSuccess: () => toast.success('Container restart initiated'),
      onError: (err) => toast.error(`Restart failed: ${err.message}`),
    })
  }

  // Compose-export mode: no Docker socket, can't manage containers
  if (isComposeExport) {
    return (
      <div className="mx-auto max-w-4xl space-y-4">
        <h1 className="text-xl font-bold text-[var(--color-text)]">Dashboard</h1>

        <UpdateBanner />

        {showLlmBanner && (
          <div className="flex items-center justify-between rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-4">
            <div className="flex items-center gap-3">
              <Brain size={20} className="text-[var(--color-primary)]" />
              <div>
                <p className="text-sm font-medium text-[var(--color-text)]">
                  LLM not configured
                </p>
                <p className="text-xs text-[var(--color-text-muted)]">
                  Set up a language model to enable voice command processing
                </p>
              </div>
            </div>
            <div className="flex items-center gap-2">
              <button
                onClick={() => {
                  localStorage.setItem(LLM_SETUP_DISMISSED_KEY, 'true')
                  setDismissed(true)
                }}
                className="text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
              >
                Dismiss
              </button>
              <button
                onClick={() => navigate('/llm-setup')}
                className="rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs text-white hover:opacity-90"
              >
                Set up LLM
              </button>
            </div>
          </div>
        )}

        {/* Sync is available in compose-export mode too — admin has the docker
            socket + the compose mount, so it can regenerate + re-register
            (e.g. to apply mobile-reachable service URLs). */}
        <div className="flex items-center justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          <div className="flex items-center gap-3">
            <GitMerge size={20} className="text-[var(--color-text-muted)]" />
            <div>
              <p className="text-sm font-medium text-[var(--color-text)]">
                Sync compose to latest registry
              </p>
              <p className="text-xs text-[var(--color-text-muted)]">
                Regenerate docker-compose.yml, apply changes, and re-register services with config-service
              </p>
            </div>
          </div>
          <button
            onClick={() => navigate('/reconcile')}
            className="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]"
          >
            Sync now
          </button>
        </div>

        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-8 text-center">
          <Server size={32} className="mx-auto mb-3 text-[var(--color-text-muted)]" />
          <p className="text-sm font-medium text-[var(--color-text)]">
            Running in Docker Compose mode
          </p>
          <p className="mt-1 text-xs text-[var(--color-text-muted)]">
            Container management is handled by your Docker environment (TrueNAS, Portainer, etc.).
            Use your platform's tools to view container status, logs, and restarts.
          </p>
        </div>
      </div>
    )
  }

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
        <p className="mb-2 text-red-500">Failed to load containers</p>
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

  const containers = data?.containers ?? []
  const running = containers.filter((c) => c.state === 'running')
  const stopped = containers.filter((c) => c.state !== 'running')

  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold text-[var(--color-text)]">Dashboard</h1>

        <div className="flex items-center gap-2 text-xs text-[var(--color-text-muted)]">
          <span>
            {running.length}/{containers.length} running
          </span>
          <button
            onClick={() => refetch()}
            disabled={isFetching}
            className={cn(
              'rounded-lg p-1.5 hover:bg-[var(--color-surface-alt)]',
              isFetching && 'animate-spin',
            )}
            title="Refresh"
          >
            <RefreshCw size={14} />
          </button>
        </div>
      </div>

      <UpdateBanner />

      {showLlmBanner && (
        <div className="flex items-center justify-between rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-4">
          <div className="flex items-center gap-3">
            <Brain size={20} className="text-[var(--color-primary)]" />
            <div>
              <p className="text-sm font-medium text-[var(--color-text)]">
                LLM not configured
              </p>
              <p className="text-xs text-[var(--color-text-muted)]">
                Set up a language model to enable voice command processing
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => {
                localStorage.setItem(LLM_SETUP_DISMISSED_KEY, 'true')
                setDismissed(true)
              }}
              className="text-xs text-[var(--color-text-muted)] hover:text-[var(--color-text)]"
            >
              Dismiss
            </button>
            <button
              onClick={() => navigate('/llm-setup')}
              className="rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs text-white hover:opacity-90"
            >
              Set up LLM
            </button>
          </div>
        </div>
      )}

      <div className="flex items-center justify-between rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
        <div className="flex items-center gap-3">
          <GitMerge size={20} className="text-[var(--color-text-muted)]" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">
              Sync compose to latest registry
            </p>
            <p className="text-xs text-[var(--color-text-muted)]">
              Regenerate docker-compose.yml and apply changes — adds new background workers and services without recreating unchanged containers
            </p>
          </div>
        </div>
        <button
          onClick={() => navigate('/reconcile')}
          className="rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text)] hover:bg-[var(--color-surface-alt)]"
        >
          Sync now
        </button>
      </div>

      {containers.length === 0 && (
        <div className="py-12 text-center">
          <p className="text-sm text-[var(--color-text-muted)]">
            {data?.error
              ? data.error
              : 'No Jarvis containers found. Is Docker running?'}
          </p>
        </div>
      )}

      {running.length > 0 && (
        <div className="space-y-2">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
            Running
          </h2>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {running.map((c) => (
              <ServiceHealthCard
                key={c.id}
                container={c}
                onRestart={handleRestart}
                isRestarting={
                  restartMutation.isPending && restartMutation.variables === c.id
                }
              />
            ))}
          </div>
        </div>
      )}

      {stopped.length > 0 && (
        <div className="space-y-2">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
            Stopped
          </h2>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {stopped.map((c) => (
              <ServiceHealthCard
                key={c.id}
                container={c}
                onRestart={handleRestart}
                isRestarting={
                  restartMutation.isPending && restartMutation.variables === c.id
                }
              />
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
