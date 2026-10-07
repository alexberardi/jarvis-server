import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Activity, Box, Brain, Cpu, Loader2, RefreshCw, Server, Stethoscope } from 'lucide-react'
import { fetchTraces } from '@/api/traces'
import { LABEL_TITLE, type Label } from '@/api/llm'
import UpdateBanner from '@/components/dashboard/UpdateBanner'
import DoctorChecks from '@/components/doctor/DoctorChecks'
import { buttonClass, stateTone } from '@/components/models/styles'
import { Pill, Section } from '@/components/models/ui'
import { useLabels } from '@/hooks/useModelManager'
import { useNodeLiveness } from '@/hooks/useNodes'
import { useDoctor, useRerunDoctor, useSetupState } from '@/hooks/useSetup'
import { useSystemInfo } from '@/hooks/useSystem'
import { errorMessage } from '@/lib/errors'
import { formatBytes, formatUptime } from '@/lib/format'
import { cn } from '@/lib/utils'

const LABELS: Label[] = ['live', 'background', 'embeddings', 'stt', 'tts', 'speaker']

const STATE_TEXT: Record<string, string> = {
  not_configured: 'not configured',
  fetching_engine: 'fetching engine',
  no_engine_build: 'no engine build',
}

function ago(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  if (!Number.isFinite(s)) return iso
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  return `${Math.round(s / 86400)} d ago`
}

/** The banner while the live label has no model, or has one that isn't serving yet. */
function ModelBanner() {
  const { data } = useSetupState()
  if (!data?.superuser) return null
  if (data.setup_step) {
    // A10 F9: the wizard was left before Done (another tab, a closed browser).
    return (
      <div className="flex items-center justify-between rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-4">
        <p className="text-sm text-[var(--color-text)]">Setup isn't finished: models, privacy choices and next steps.</p>
        <Link to="/setup" className="rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs text-white hover:opacity-90">
          Finish setup
        </Link>
      </div>
    )
  }
  if (data.models_configured === false) {
    return (
      <div className="flex items-center justify-between rounded-lg border border-[var(--color-primary)]/30 bg-[var(--color-primary)]/5 p-4">
        <div className="flex items-center gap-3">
          <Brain size={20} className="text-[var(--color-primary)]" />
          <div>
            <p className="text-sm font-medium text-[var(--color-text)]">No language model yet</p>
            <p className="text-xs text-[var(--color-text-muted)]">Jarvis can't answer voice requests until the live job has a model.</p>
          </div>
        </div>
        <Link to="/models" className="rounded-lg bg-[var(--color-primary)] px-3 py-1.5 text-xs text-white hover:opacity-90">
          Set up models
        </Link>
      </div>
    )
  }
  if (data.models_configured && data.live_ready === false) {
    const st = data.labels?.live ?? 'unknown'
    return (
      <div className="flex items-center gap-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-4">
        <Loader2 size={18} className="animate-spin text-amber-500" />
        <p className="text-sm text-[var(--color-text)]">
          The live model is {STATE_TEXT[st] ?? st}. Voice requests are answered once it is ready.{' '}
          <Link to="/models" className="text-[var(--color-primary)] hover:underline">
            Details
          </Link>
        </p>
      </div>
    )
  }
  return null
}

function SystemCard() {
  const { data, isError, error } = useSystemInfo()
  return (
    <Section title="System" icon={Server}>
      {isError && <p className="text-sm text-red-500">{errorMessage(error)}</p>}
      {data && (
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
          <dt className="text-[var(--color-text-muted)]">Host</dt>
          <dd className="text-[var(--color-text)]">{data.hostname}</dd>
          <dt className="text-[var(--color-text-muted)]">Platform</dt>
          <dd className="text-[var(--color-text)]">
            {data.platform}
            {data.arch ? `/${data.arch}` : ''} · {data.cpuCount} cores · {Math.round(data.totalMemoryMb / 1024)} GB
          </dd>
          <dt className="text-[var(--color-text-muted)]">jarvisd</dt>
          <dd className="text-[var(--color-text)]">
            {data.version} · up {formatUptime(data.uptime)}
          </dd>
          <dt className="text-[var(--color-text-muted)]">Data</dt>
          <dd className="text-[var(--color-text)]" title={data.home}>
            DB {formatBytes(data.db_bytes)} · {formatBytes(data.disk_free_bytes)} free
          </dd>
          <dt className="text-[var(--color-text-muted)]">Listeners</dt>
          <dd className="text-[var(--color-text)]">
            {(data.listeners ?? []).filter((l) => l.served).length} serving ·{' '}
            <Link to="/connections" className="text-[var(--color-primary)] hover:underline">
              connections
            </Link>
          </dd>
        </dl>
      )}
    </Section>
  )
}

function HealthCard() {
  const { data, isLoading, isError, error } = useDoctor()
  const rerun = useRerunDoctor()
  const tone = data?.status === 'ok' ? 'ok' : data?.status === 'fail' ? 'bad' : 'warn'
  return (
    <Section
      title="Health check"
      icon={Stethoscope}
      actions={
        <button type="button" className={buttonClass.icon} title="Run again" aria-label="Run the health check again" disabled={rerun.isPending} onClick={() => rerun.mutate()}>
          <RefreshCw size={14} className={cn(rerun.isPending && 'animate-spin')} />
        </button>
      }
    >
      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Running…</p>}
      {isError && <p className="text-sm text-red-500">{errorMessage(error, 'The health check could not run')}</p>}
      {data && (
        <div className="space-y-2">
          <p className="text-sm text-[var(--color-text)]">
            <Pill tone={tone}>{data.status}</Pill>{' '}
            {data.checks.filter((c) => c.status === 'ok').length} of {data.checks.length} checks pass
          </p>
          <DoctorChecks checks={data.checks} onlyProblems />
        </div>
      )}
    </Section>
  )
}

function ModelsCard() {
  const setup = useSetupState()
  const labels = useLabels()
  const models = Object.fromEntries((labels.data?.labels ?? []).map((l) => [l.label, l.config.model]))
  const voice = Object.fromEntries((labels.data?.voice ?? []).map((v) => [v.label, v.id]))
  return (
    <Section
      title="Models"
      icon={Box}
      actions={
        <Link to="/models" className="text-xs text-[var(--color-primary)] hover:underline">
          Manage
        </Link>
      }
    >
      <ul className="space-y-1 text-sm">
        {LABELS.map((l) => {
          const st = setup.data?.labels?.[l] ?? labels.data?.labels.find((x) => x.label === l)?.state ?? 'unknown'
          const model = models[l] || voice[l]
          return (
            <li key={l} className="flex items-center justify-between gap-2">
              <span className="min-w-0 truncate text-[var(--color-text)]">
                {LABEL_TITLE[l]}
                {model && <span className="ml-2 text-xs text-[var(--color-text-muted)]">{model}</span>}
              </span>
              <Pill tone={stateTone(st)}>{STATE_TEXT[st] ?? st}</Pill>
            </li>
          )
        })}
      </ul>
      {(labels.data?.warnings ?? []).map((w) => (
        <p key={w} className="mt-2 text-xs text-amber-500">
          {w}
        </p>
      ))}
    </Section>
  )
}

function NodesCard() {
  const setup = useSetupState()
  const { data, isError } = useNodeLiveness()
  const nodes = data ?? []
  const online = nodes.filter((n) => n.online).length
  return (
    <Section
      title="Nodes"
      icon={Cpu}
      actions={
        <Link to="/nodes" className="text-xs text-[var(--color-primary)] hover:underline">
          All nodes
        </Link>
      }
    >
      {isError ? (
        <p className="text-sm text-[var(--color-text-muted)]">Node status is unavailable.</p>
      ) : (
        <div className="space-y-2">
          <p className="text-sm text-[var(--color-text)]">
            <strong>{online}</strong> of {nodes.length} online
            {setup.data?.households !== undefined && (
              <span className="text-[var(--color-text-muted)]"> · {setup.data.households} households</span>
            )}
          </p>
          {nodes.length === 0 ? (
            <p className="text-xs text-[var(--color-text-muted)]">No nodes yet. Add one from the mobile app.</p>
          ) : (
            <ul className="space-y-1 text-xs">
              {nodes.slice(0, 6).map((n) => (
                <li key={n.node_id} className="flex items-center justify-between gap-2">
                  <span className="flex min-w-0 items-center gap-2">
                    <span className={cn('h-2 w-2 shrink-0 rounded-full', n.online ? 'bg-green-500' : 'bg-[var(--color-border)]')} />
                    <span className="truncate text-[var(--color-text)]">{n.room || n.node_id}</span>
                  </span>
                  <span className="text-[var(--color-text-muted)]">{n.online ? 'online' : ago(n.last_seen)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Section>
  )
}

function TracesCard() {
  const { data, isError } = useQuery({
    queryKey: ['traces', 'recent'],
    queryFn: () => fetchTraces({ limit: 6 }),
    staleTime: 15_000,
    refetchInterval: 30_000,
  })
  return (
    <Section
      title="Recent requests"
      icon={Activity}
      actions={
        <Link to="/traces" className="text-xs text-[var(--color-primary)] hover:underline">
          All traces
        </Link>
      }
    >
      {isError && <p className="text-sm text-[var(--color-text-muted)]">Traces are unavailable.</p>}
      {data && data.traces.length === 0 && <p className="text-xs text-[var(--color-text-muted)]">No requests yet.</p>}
      {data && data.traces.length > 0 && (
        <ul className="space-y-1 text-xs">
          {data.traces.map((t) => (
            <li key={t.id}>
              <Link to={`/traces/${t.id}`} className="flex items-center justify-between gap-2 rounded px-1 py-0.5 hover:bg-[var(--color-surface-alt)]">
                <span className="min-w-0 truncate text-[var(--color-text)]">{t.user_command || t.request_type}</span>
                <span className="flex shrink-0 items-center gap-2 text-[var(--color-text-muted)]">
                  <Pill tone={t.status === 'success' || t.status === 'ok' ? 'ok' : t.status === 'error' ? 'bad' : 'muted'}>{t.status}</Pill>
                  {Math.round(t.total_duration_ms)} ms · {ago(t.created_at)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

/** DashboardPage (S5): one look at the install, with no containers to manage. */
export default function DashboardPage() {
  return (
    <div className="mx-auto max-w-5xl space-y-4">
      <h1 className="text-xl font-bold text-[var(--color-text)]">Dashboard</h1>
      <ModelBanner />
      <UpdateBanner />
      <div className="grid gap-4 md:grid-cols-2">
        <SystemCard />
        <HealthCard />
        <ModelsCard />
        <NodesCard />
      </div>
      <TracesCard />
    </div>
  )
}
