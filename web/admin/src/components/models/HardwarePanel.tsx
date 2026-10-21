import { useState } from 'react'
import { Cpu, Download, RefreshCw, Server } from 'lucide-react'
import { toast } from 'sonner'
import type { EngineInstance, HardwareResponse } from '@/api/llm'
import { useFetchEngine, useHardware, useRefreshHardware, useRetryEngine } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { formatBytes, formatMB } from '@/lib/format'
import GpuFaultBanner from './GpuFaultBanner'
import { Badge, Button, ErrorState, Skeleton } from '@/components/ui'
import ModelCard from './ModelCard'
import { stateBadge } from './tone'

/** DetectedHardware is the GPU summary the A7 wizard's Hardware step starts from. */
export function DetectedHardware({ data }: { data: Pick<HardwareResponse, 'hardware' | 'proposal'> }) {
  const hw = data.hardware
  const devices = hw.devices ?? []
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-body text-text">
        <span>
          {hw.os}/{hw.arch}
        </span>
        <Badge tone={hw.flavour === 'cpu' ? 'neutral' : 'success'}>{hw.flavour === 'cpu' ? 'CPU only' : hw.flavour.toUpperCase()}</Badge>
      </div>
      {devices.length === 0 ? (
        <p className="text-small text-text-muted">
          No usable GPU found. Models run on the CPU (slower); a small model or a remote endpoint is worth considering.
        </p>
      ) : (
        <ul className="space-y-1">
          {devices.map((d) => (
            <li key={`${d.backend}-${d.index}`} className="flex flex-wrap items-center gap-x-2 text-small">
              <code className="font-mono text-text-muted">{d.id || `${d.backend}${d.index}`}</code>
              <span className="text-text">{d.name}</span>
              <span className="text-text-muted">
                {formatMB(d.free_mb)} free of {formatMB(d.total_mb)}
              </span>
            </li>
          ))}
        </ul>
      )}
      {(hw.ignored ?? []).length > 0 && (
        <p className="text-caption font-normal text-text-muted">
          Ignored (integrated): {(hw.ignored ?? []).map((d) => d.name).join(', ')}
        </p>
      )}
      {Object.keys(data.proposal ?? {}).length > 0 && (
        <p className="text-caption font-normal text-text-muted">
          Proposed placement:{' '}
          {Object.entries(data.proposal)
            .map(([label, p]) => `${label} → ${p.gpu_backend}${p.gpu_devices ? ` ${p.gpu_devices}` : ''}`)
            .join(' · ')}
        </p>
      )}
    </div>
  )
}

function EngineRow({ e }: { e: EngineInstance }) {
  const [open, setOpen] = useState(false)
  const retry = useRetryEngine()
  return (
    <li className="py-2 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-2 text-small">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <code className="font-mono break-all text-text">{e.name}</code>
          <Badge tone={stateBadge(e.state)}>{e.state}</Badge>
          <span className="text-text-muted">
            {e.kind} · {e.flavour} · {(e.labels ?? []).join(', ')}
          </span>
        </div>
        <div className="flex items-center gap-2 font-mono text-text-muted">
          {e.pid > 0 && <span>pid {e.pid}</span>}
          {e.port > 0 && <span>:{e.port}</span>}
          {e.restarts > 0 && <span className="font-sans">{e.restarts} restarts</span>}
          <Button variant="ghost" size="sm" onClick={() => setOpen(!open)} aria-expanded={open}>
            {open ? 'Hide details' : 'Details'}
          </Button>
        </div>
      </div>
      {e.last_error && <p className="mt-1 text-small break-words text-danger">{e.last_error}</p>}
      {e.gave_up && (
        <div className="mt-2 flex flex-wrap items-center gap-2 text-small">
          <span className="text-text-muted">
            Gave up after {e.retries} retries. Fix the model or its settings, then retry.
          </span>
          <Button
            size="sm"
            icon={<RefreshCw size={14} aria-hidden />}
            loading={retry.isPending}
            onClick={() => retry.mutate(e.name, { onError: (err) => toast.error(errorMessage(err, 'Retry failed')) })}
          >
            Retry
          </Button>
        </div>
      )}
      {open && (
        <div className="mt-2 space-y-1">
          <p className="font-mono text-caption font-normal break-all text-text-muted">{(e.args ?? []).join(' ')}</p>
          {(e.output ?? []).length > 0 && (
            <pre className="max-h-48 overflow-auto rounded-control bg-surface-alt p-2 font-mono text-caption font-normal text-text-muted">
              {(e.output ?? []).join('\n')}
            </pre>
          )}
        </div>
      )}
    </li>
  )
}

/**
 * HardwarePanel shows GPU detection, engine builds (with fetch for a missing flavour), running
 * engine processes and the in-binary voice models.
 */
export default function HardwarePanel() {
  const { data, isLoading, isError, error, refetch } = useHardware()
  const refresh = useRefreshHardware()
  const fetchEngine = useFetchEngine()

  return (
    <ModelCard
      title="Hardware and engines"
      icon={Cpu}
      actions={
        <Button
          size="sm"
          icon={<RefreshCw size={14} aria-hidden />}
          loading={refresh.isPending}
          onClick={() =>
            refresh.mutate(undefined, { onError: (err) => toast.error(errorMessage(err, 'Detection failed')) })
          }
        >
          Detect again
        </Button>
      }
    >
      {isLoading && (
        <div role="status" aria-label="Detecting the hardware" className="space-y-2">
          <Skeleton className="w-1/3" />
          <Skeleton className="w-2/3" />
          <Skeleton className="w-1/2" />
        </div>
      )}
      {isError && (
        <ErrorState
          className="py-6"
          title="Couldn't read the hardware"
          message={errorMessage(error, 'The server did not answer.')}
          onRetry={() => refetch()}
        />
      )}
      {data && (
        <div className="space-y-6">
          <GpuFaultBanner fault={data.gpu_fault ?? data.hardware.gpu_fault} />
          <DetectedHardware data={data} />

          <div className="space-y-2">
            <h3 className="text-subheading text-text">Engine builds</h3>
            <ul className="space-y-2 text-small">
              {Object.entries(data.builds).map(([kind, b]) => {
                const have = new Set((data.installed ?? []).filter((i) => i.kind === kind && i.pinned).map((i) => i.flavour))
                return (
                  <li key={kind} className="flex flex-wrap items-center gap-2">
                    <code className="font-mono text-text">{kind}</code>
                    <span className="text-text-muted">{b.build}</span>
                    {b.unavailable && <span className="text-text-muted">{b.unavailable}</span>}
                    {!b.unavailable && b.disk_bytes && b.flavours.some((f) => !have.has(f)) && (
                      <span className="text-text-muted">
                        {formatBytes(b.download_bytes)} download, needs {formatBytes(b.disk_bytes)} free while installing
                      </span>
                    )}
                    {b.flavours.map((f) =>
                      have.has(f) ? (
                        <Badge key={f} tone="success" title="Downloaded">
                          {f}
                        </Badge>
                      ) : (
                        <Button
                          key={f}
                          size="sm"
                          variant="ghost"
                          icon={<Download size={14} aria-hidden />}
                          aria-label={`Download the ${f} build`}
                          title={`Download the ${f} build now (it is otherwise fetched on first use)`}
                          disabled={fetchEngine.isPending}
                          onClick={() =>
                            fetchEngine.mutate(
                              { kind, flavour: f },
                              {
                                onSuccess: (r) => toast.success(r.installed ? `${kind} ${f} is already downloaded` : `Fetching ${kind} ${f}`),
                                onError: (err) => toast.error(errorMessage(err)),
                              },
                            )
                          }
                        >
                          {f}
                        </Button>
                      ),
                    )}
                  </li>
                )
              })}
            </ul>
          </div>

          <div className="space-y-2">
            <h3 className="flex items-center gap-2 text-subheading text-text">
              <Server size={16} aria-hidden className="text-text-muted" /> Running engines
            </h3>
            {(data.engines ?? []).length === 0 ? (
              <p className="text-small text-text-muted">None yet. An engine starts once a job has a model.</p>
            ) : (
              <ul className="divide-y divide-border">
                {(data.engines ?? []).map((e) => (
                  <EngineRow key={e.name} e={e} />
                ))}
              </ul>
            )}
          </div>

          {(data.voice ?? []).length > 0 && (
            <div className="space-y-2">
              <h3 className="text-subheading text-text">Built-in voice models</h3>
              <ul className="space-y-1 text-small">
                {(data.voice ?? []).map((v) => (
                  <li key={v.label} className="flex flex-wrap items-center gap-2">
                    <code className="font-mono text-text">{v.label}</code>
                    {v.problem ? <Badge tone="warning">{v.problem}</Badge> : <span className="text-text-muted">{v.id}</span>}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </ModelCard>
  )
}
