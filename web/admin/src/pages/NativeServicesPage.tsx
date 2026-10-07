import { useState } from 'react'
import { Play, Square, RotateCw, Trash2, FileText, RefreshCw, AlertCircle, Cpu } from 'lucide-react'
import { toast } from 'sonner'
import { cn } from '@/lib/utils'
import {
  useNativeServices,
  useStartNativeService,
  useStopNativeService,
  useUninstallNativeService,
  useNativeLogs,
} from '@/hooks/useNativeServices'
import type { NativeServiceEntry } from '@/api/nativeServices'

export default function NativeServicesPage() {
  const { data, isLoading, isError, error, refetch, isFetching } = useNativeServices()

  if (isLoading) {
    return (
      <div className="p-6">
        <p className="text-sm text-[var(--color-text-muted)]">Loading native services…</p>
      </div>
    )
  }

  if (isError) {
    return (
      <div className="p-6">
        <ErrorBanner message={error instanceof Error ? error.message : String(error)} />
      </div>
    )
  }

  if (!data) return null

  if (!data.supported) {
    return (
      <div className="p-6">
        <header className="mb-4">
          <h1 className="text-2xl font-bold text-[var(--color-text)]">Native Services</h1>
          <p className="mt-1 text-sm text-[var(--color-text-muted)]">
            Manage Jarvis services running as macOS LaunchAgents.
          </p>
        </header>
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] p-4">
          <p className="text-sm text-[var(--color-text-muted)]">
            Native services are only available on macOS. On Linux all services run in Docker.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-6">
      <header className="mb-4 flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold text-[var(--color-text)]">Native Services</h1>
          <p className="mt-1 text-sm text-[var(--color-text-muted)]">
            macOS LaunchAgents — these services run outside Docker so they can use the Apple GPU
            (MLX / Metal / MPS). Status reflects <code>launchctl print</code>.
          </p>
        </div>
        <button
          type="button"
          onClick={() => refetch()}
          disabled={isFetching}
          className={cn(
            'flex items-center gap-2 rounded-lg border border-[var(--color-border)] px-3 py-1.5 text-sm',
            'hover:bg-[var(--color-surface-alt)] transition-colors',
            isFetching && 'opacity-50',
          )}
        >
          <RefreshCw size={14} className={isFetching ? 'animate-spin' : ''} />
          Refresh
        </button>
      </header>

      {data.services.length === 0 ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] p-4">
          <p className="text-sm text-[var(--color-text-muted)]">
            No native-capable services are registered. Re-run the install wizard to add them.
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {data.services.map((svc) => (
            <NativeServiceCard key={svc.id} service={svc} />
          ))}
        </div>
      )}
    </div>
  )
}

function NativeServiceCard({ service }: { service: NativeServiceEntry }) {
  const start = useStartNativeService()
  const stop = useStopNativeService()
  const uninstall = useUninstallNativeService()
  const [logsOpen, setLogsOpen] = useState(false)
  const [logStream, setLogStream] = useState<'stdout' | 'stderr'>('stderr')
  const logs = useNativeLogs(service.id, logStream, logsOpen)

  const { status } = service

  async function handle<T>(promise: Promise<T>, success: string) {
    try {
      await promise
      toast.success(success)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleUninstall() {
    if (!confirm(`Uninstall ${service.id}? The LaunchAgent will be removed; source files at ${status.sourceDir ?? '~/.jarvis/native'} stay on disk.`)) {
      return
    }
    await handle(uninstall.mutateAsync(service.id), `${service.id} uninstalled`)
  }

  const stateLabel = !status.installed
    ? 'Not installed'
    : status.running
      ? `Running (pid ${status.pid ?? '?'})`
      : 'Installed, stopped'
  const stateColor = !status.installed
    ? 'bg-[var(--color-text-muted)]/30'
    : status.running
      ? 'bg-green-500'
      : 'bg-amber-500'

  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] overflow-hidden">
      <div className="flex items-center justify-between gap-3 px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-3">
            <span className={cn('h-2.5 w-2.5 shrink-0 rounded-full', stateColor)} />
            <span className="text-sm font-medium text-[var(--color-text)]">{service.name}</span>
            <span className="text-xs text-[var(--color-text-muted)]">:{status.port ?? service.port}</span>
            <code className="text-xs text-[var(--color-text-muted)]">{status.label}</code>
          </div>
          <p className="ml-5 mt-0.5 text-xs text-[var(--color-text-muted)]">
            {stateLabel}
            {status.sourceDir && <span className="ml-2 opacity-60">· {status.sourceDir}</span>}
          </p>
        </div>

        <div className="flex shrink-0 items-center gap-2">
          {status.installed && (
            <>
              {status.running ? (
                <ActionButton
                  icon={Square}
                  label="Stop"
                  onClick={() => handle(stop.mutateAsync(service.id), `${service.id} stopped`)}
                  disabled={stop.isPending}
                />
              ) : (
                <ActionButton
                  icon={Play}
                  label="Start"
                  onClick={() => handle(start.mutateAsync(service.id), `${service.id} started`)}
                  disabled={start.isPending}
                />
              )}
              {status.running && (
                <ActionButton
                  icon={RotateCw}
                  label="Restart"
                  onClick={() => handle(start.mutateAsync(service.id), `${service.id} restarted`)}
                  disabled={start.isPending}
                />
              )}
              <ActionButton
                icon={FileText}
                label={logsOpen ? 'Hide logs' : 'Logs'}
                onClick={() => setLogsOpen((v) => !v)}
              />
              <ActionButton
                icon={Trash2}
                label="Uninstall"
                onClick={handleUninstall}
                disabled={uninstall.isPending}
                danger
              />
            </>
          )}
          {!status.installed && (
            <span className="flex items-center gap-1 text-xs text-[var(--color-text-muted)]">
              <Cpu size={12} /> Re-run install wizard to enable
            </span>
          )}
        </div>
      </div>

      {logsOpen && status.installed && (
        <div className="border-t border-[var(--color-border)] bg-[var(--color-background)] p-3">
          <div className="mb-2 flex items-center gap-2">
            <StreamButton active={logStream === 'stderr'} onClick={() => setLogStream('stderr')}>
              stderr
            </StreamButton>
            <StreamButton active={logStream === 'stdout'} onClick={() => setLogStream('stdout')}>
              stdout
            </StreamButton>
            <code className="ml-2 text-xs text-[var(--color-text-muted)] opacity-60">
              {logs.data?.file}
            </code>
          </div>
          <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded bg-[var(--color-surface-alt)] p-2 text-xs font-mono text-[var(--color-text)]">
            {logs.isLoading
              ? 'Loading…'
              : logs.isError
                ? `Failed to load logs: ${logs.error instanceof Error ? logs.error.message : String(logs.error)}`
                : logs.data?.content || '(empty)'}
          </pre>
        </div>
      )}
    </div>
  )
}

interface ActionButtonProps {
  icon: React.ComponentType<{ size?: number; className?: string }>
  label: string
  onClick: () => void
  disabled?: boolean
  danger?: boolean
}

function ActionButton({ icon: Icon, label, onClick, disabled, danger }: ActionButtonProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={cn(
        'flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs',
        'transition-colors disabled:cursor-not-allowed disabled:opacity-50',
        danger
          ? 'border-red-500/30 text-red-500 hover:bg-red-500/10'
          : 'border-[var(--color-border)] text-[var(--color-text-muted)] hover:bg-[var(--color-surface-alt)] hover:text-[var(--color-text)]',
      )}
    >
      <Icon size={12} />
      {label}
    </button>
  )
}

function StreamButton({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'rounded px-2 py-0.5 text-xs',
        active
          ? 'bg-[var(--color-primary)] text-white'
          : 'border border-[var(--color-border)] text-[var(--color-text-muted)] hover:text-[var(--color-text)]',
      )}
    >
      {children}
    </button>
  )
}

function ErrorBanner({ message }: { message: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-500">
      <AlertCircle size={14} />
      {message}
    </div>
  )
}
