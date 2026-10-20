import { useId, useState, type ComponentType, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Activity, AlertTriangle, Box, Cpu, RefreshCw, Server, Stethoscope, XCircle } from 'lucide-react'
import { fetchTraces } from '@/api/traces'
import { LABEL_TITLE, type Label } from '@/api/llm'
import UpdateBanner from '@/components/dashboard/UpdateBanner'
import DoctorChecks from '@/components/doctor/DoctorChecks'
import ModelBanner from '@/components/dashboard/ModelBanner'
import GpuFaultBanner from '@/components/models/GpuFaultBanner'
import PowerControls from '@/components/dashboard/PowerControls'
import { stateTone, type Tone } from '@/components/models/styles'
import {
  Badge,
  Button,
  buttonStyles,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  List,
  ListItem,
  PageHeader,
  Skeleton,
  SkeletonList,
  type BadgeTone,
} from '@/components/ui'
import { useLabels } from '@/hooks/useModelManager'
import { useNodeLiveness } from '@/hooks/useNodes'
import { useDoctor, useRerunDoctor, useSetupState } from '@/hooks/useSetup'
import { useSystemInfo } from '@/hooks/useSystem'
import { errorMessage } from '@/lib/errors'
import { formatBytes, formatUptime, formatVersion } from '@/lib/format'
import { timeAgo } from '@/lib/time'
import { cn } from '@/lib/utils'

const LABELS: Label[] = ['live', 'background', 'embeddings', 'stt', 'tts', 'speaker']

const STATE_TEXT: Record<string, string> = {
  not_configured: 'Not configured',
  fetching_engine: 'Fetching engine',
  no_engine_build: 'No engine build',
  gpu_unavailable: 'GPU unavailable',
}

const BADGE_TONE: Record<Tone, BadgeTone> = { ok: 'success', warn: 'warning', bad: 'danger', info: 'primary', muted: 'neutral' }

const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1).replaceAll('_', ' ')

// A flush card's list rows line up with its heading, which gets 20px from md (Card's padding).
const ROWS = 'md:[&>li>*]:px-5'

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

/** Panel is one dashboard card: a labelled region with an icon heading and its link or buttons. */
function Panel({
  title,
  icon: Icon,
  actions,
  flush,
  children,
}: {
  title: string
  icon: ComponentType<{ size?: number; className?: string; 'aria-hidden'?: boolean }>
  actions?: ReactNode
  /** For a card whose body is a List that runs edge to edge. */
  flush?: boolean
  children: ReactNode
}) {
  const id = useId()
  return (
    <Card as="section" aria-labelledby={id} flush={flush} className={cn(flush && 'overflow-hidden pb-1')}>
      <CardHeader
        id={id}
        title={
          <span className="flex items-center gap-2">
            <Icon size={18} aria-hidden className="text-text-muted" />
            {title}
          </span>
        }
        actions={actions}
        className={cn('items-center', flush && 'mb-1 px-4 pt-4 md:px-5 md:pt-5')}
      />
      {children}
    </Card>
  )
}

/** A card's link to its full page ("All nodes"). */
function CardLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link to={to} className={buttonStyles({ variant: 'ghost', size: 'sm', className: '-mr-2' })}>
      {children}
    </Link>
  )
}

function SystemCard() {
  const { data, isLoading, isError, error, refetch } = useSystemInfo()
  return (
    <Panel title="System" icon={Server} actions={data && <PowerControls info={data} />}>
      {isLoading && (
        <div role="status" aria-label="Loading the server's details" className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-3">
          {[0, 1, 2, 3, 4].map((i) => (
            <div key={i} className="contents">
              <Skeleton className="w-16" />
              <Skeleton className="w-2/3" />
            </div>
          ))}
        </div>
      )}
      {isError && (
        <ErrorState className="py-6" title="Couldn't load the server's details" message={errorMessage(error)} onRetry={() => refetch()} />
      )}
      {data && (
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2 text-body [&_dd]:break-words [&_dd]:text-text [&_dt]:text-text-muted">
          <dt>Host</dt>
          <dd>{data.hostname}</dd>
          <dt>Platform</dt>
          <dd>
            {data.platform}
            {data.arch ? `/${data.arch}` : ''} · {data.cpuCount} cores · {Math.round(data.totalMemoryMb / 1024)} GB
          </dd>
          <dt>jarvisd</dt>
          <dd>
            {formatVersion(data.version)} · up {formatUptime(data.uptime)}
          </dd>
          <dt>Data</dt>
          <dd title={data.home}>
            DB {formatBytes(data.db_bytes)} · {formatBytes(data.disk_free_bytes)} free
          </dd>
          <dt>Listeners</dt>
          <dd>
            {(data.listeners ?? []).filter((l) => l.served).length} serving ·{' '}
            <Link to="/admin/connections" className="text-primary underline-offset-2 hover:underline">
              connections
            </Link>
          </dd>
        </dl>
      )}
    </Panel>
  )
}

const HEALTH: Record<string, { tone: BadgeTone; text: string }> = {
  ok: { tone: 'success', text: 'Passing' },
  warn: { tone: 'warning', text: 'Warnings' },
  fail: { tone: 'danger', text: 'Failing' },
}

/**
 * HealthCard is what needs the user first. A failing check's detail and fix (a long firewall
 * command) sit behind "Show details" on a phone, under a list of what fails; wider screens show
 * them at once.
 */
function HealthCard() {
  const { data, isLoading, isError, error, refetch } = useDoctor()
  const rerun = useRerunDoctor()
  const [open, setOpen] = useState(false)
  const detailsId = useId()
  const problems = data?.checks.filter((c) => c.status !== 'ok') ?? []
  const health = HEALTH[data?.status ?? ''] ?? HEALTH.warn
  return (
    <Panel
      title="Health check"
      icon={Stethoscope}
      actions={
        <Button
          variant="ghost"
          size="icon"
          className="-my-2 -mr-2"
          title="Run again"
          aria-label="Run the health check again"
          loading={rerun.isPending}
          icon={<RefreshCw size={16} aria-hidden />}
          onClick={() => rerun.mutate()}
        />
      }
    >
      {isLoading && (
        <div role="status" aria-label="Running the health check" className="space-y-3">
          <Skeleton className="h-6 w-40" />
          <Skeleton className="w-full" />
          <Skeleton className="w-3/4" />
        </div>
      )}
      {isError && (
        <ErrorState
          className="py-6"
          title="The health check couldn't run"
          message={errorMessage(error, 'jarvisd did not answer.')}
          onRetry={() => refetch()}
        />
      )}
      {data && (
        <>
          <p className="mb-3 flex flex-wrap items-center gap-2 text-body text-text">
            <Badge tone={health.tone} dot>
              {health.text}
            </Badge>
            {data.checks.length - problems.length} of {data.checks.length} checks pass
          </p>
          {problems.length === 0 ? (
            <DoctorChecks checks={data.checks} onlyProblems />
          ) : (
            <>
              <ul className={cn('space-y-1 md:hidden', open && 'hidden')}>
                {problems.map((c, i) => (
                  <li key={`${c.name}-${i}`} className="flex items-center gap-2 text-body text-text">
                    {c.status === 'fail' ? (
                      <XCircle size={16} className="shrink-0 text-danger" aria-label="failed" />
                    ) : (
                      <AlertTriangle size={16} className="shrink-0 text-warning" aria-label="warning" />
                    )}
                    <span className="min-w-0 truncate">{c.name}</span>
                  </li>
                ))}
              </ul>
              <div id={detailsId} className={cn(!open && 'max-md:hidden')}>
                <DoctorChecks checks={data.checks} onlyProblems />
              </div>
              <Button
                variant="ghost"
                size="sm"
                className="mt-2 -ml-3 md:hidden"
                aria-expanded={open}
                aria-controls={detailsId}
                onClick={() => setOpen((o) => !o)}
              >
                {open ? 'Hide details' : 'Show details'}
              </Button>
            </>
          )}
        </>
      )}
    </Panel>
  )
}

function ModelsCard() {
  const setup = useSetupState()
  const labels = useLabels()
  const models = Object.fromEntries((labels.data?.labels ?? []).map((l) => [l.label, l.config.model]))
  const voice = Object.fromEntries((labels.data?.voice ?? []).map((v) => [v.label, v.id]))
  return (
    <Panel flush title="Models" icon={Box} actions={<CardLink to="/admin/models">Manage</CardLink>}>
      {labels.isError && !labels.data && !setup.data ? (
        <ErrorState className="py-6" title="Couldn't load the models" message={errorMessage(labels.error)} onRetry={() => labels.refetch()} />
      ) : !labels.data && !setup.data ? (
        <SkeletonList rows={LABELS.length} label="Loading models" />
      ) : (
        <List className={ROWS}>
          {LABELS.map((l) => {
            const st = setup.data?.labels?.[l] ?? labels.data?.labels.find((x) => x.label === l)?.state ?? 'unknown'
            return (
              <ListItem
                key={l}
                title={LABEL_TITLE[l]}
                description={models[l] || voice[l] || undefined}
                trailing={<Badge tone={BADGE_TONE[stateTone(st)]}>{STATE_TEXT[st] ?? sentence(st)}</Badge>}
              />
            )
          })}
        </List>
      )}
      {(labels.data?.warnings ?? []).map((w) => (
        <p key={w} className="px-4 pt-2 pb-3 text-small text-warning md:px-5">
          {w}
        </p>
      ))}
    </Panel>
  )
}

function NodesCard() {
  const setup = useSetupState()
  const { data, isLoading, isError, error, refetch } = useNodeLiveness()
  const nodes = data ?? []
  const online = nodes.filter((n) => n.online).length
  const households = setup.data?.households
  return (
    <Panel flush title="Nodes" icon={Cpu} actions={<CardLink to="/admin/nodes">All nodes</CardLink>}>
      {isError ? (
        <ErrorState className="py-6" title="Couldn't load node status" message={errorMessage(error)} onRetry={() => refetch()} />
      ) : isLoading ? (
        <SkeletonList rows={2} label="Loading nodes" />
      ) : nodes.length === 0 ? (
        <EmptyState className="py-6" icon={<Cpu size={20} />} title="No nodes yet" description="Add one from the mobile app." />
      ) : (
        <>
          <p className="px-4 pb-1 text-body text-text md:px-5">
            <strong className="font-semibold">{online}</strong> of {nodes.length} online
            {households !== undefined && <span className="text-text-muted"> · {plural(households, 'household', 'households')}</span>}
          </p>
          <List className={ROWS}>
            {nodes.slice(0, 6).map((n) => (
              <ListItem
                key={n.node_id}
                title={n.room || n.node_id}
                description={n.online ? undefined : n.last_seen ? `Last seen ${timeAgo(n.last_seen)}` : 'Never seen'}
                trailing={
                  <Badge tone={n.online ? 'success' : 'neutral'} dot>
                    {n.online ? 'Online' : 'Offline'}
                  </Badge>
                }
              />
            ))}
          </List>
        </>
      )}
    </Panel>
  )
}

function TracesCard() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['traces', 'recent'],
    queryFn: () => fetchTraces({ limit: 6 }),
    staleTime: 15_000,
    refetchInterval: 30_000,
  })
  return (
    <Panel flush title="Recent requests" icon={Activity} actions={<CardLink to="/admin/traces">All traces</CardLink>}>
      {isError ? (
        <ErrorState className="py-6" title="Couldn't load recent requests" message={errorMessage(error)} onRetry={() => refetch()} />
      ) : isLoading ? (
        <SkeletonList rows={3} label="Loading recent requests" />
      ) : !data || data.traces.length === 0 ? (
        <EmptyState
          className="py-6"
          icon={<Activity size={20} />}
          title="No requests yet"
          description="What people ask Jarvis shows up here, with how long the answer took."
        />
      ) : (
        <List className={ROWS}>
          {data.traces.map((t) => (
            <ListItem
              key={t.id}
              to={`/admin/traces/${t.id}`}
              title={t.user_command || t.request_type}
              description={`${Math.round(t.total_duration_ms)} ms · ${timeAgo(t.created_at)}`}
              trailing={
                <Badge tone={t.status === 'success' || t.status === 'ok' ? 'success' : t.status === 'error' ? 'danger' : 'neutral'}>
                  {t.status === 'ok' ? 'OK' : sentence(t.status)}
                </Badge>
              }
            />
          ))}
        </List>
      )}
    </Panel>
  )
}

/**
 * DashboardPage (S5): one look at the install, with no containers to manage. What needs the
 * user comes first (the banners, then the health check); the machine's details come last.
 */
export default function DashboardPage() {
  const labels = useLabels()
  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader title="Dashboard" />
      <div className="flex flex-col gap-4">
        <GpuFaultBanner fault={labels.data?.gpu_fault} />
        <ModelBanner />
        <UpdateBanner />
        <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-2">
          <div className="flex min-w-0 flex-col gap-4">
            <HealthCard />
            <ModelsCard />
          </div>
          <div className="flex min-w-0 flex-col gap-4">
            <NodesCard />
            <TracesCard />
            <SystemCard />
          </div>
        </div>
      </div>
    </div>
  )
}
