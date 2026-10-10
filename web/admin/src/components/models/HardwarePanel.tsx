import { useState } from 'react'
import { Cpu, RefreshCw, Server } from 'lucide-react'
import { toast } from 'sonner'
import type { EngineInstance, HardwareResponse } from '@/api/llm'
import { useFetchEngine, useHardware, useRefreshHardware } from '@/hooks/useModelManager'
import { errorMessage } from '@/lib/errors'
import { formatMB } from '@/lib/format'
import { cn } from '@/lib/utils'
import GpuFaultBanner from './GpuFaultBanner'
import { buttonClass, stateTone } from './styles'
import { Pill, Section } from './ui'

/** DetectedHardware is the GPU summary the A7 wizard's Hardware step starts from. */
export function DetectedHardware({ data }: { data: Pick<HardwareResponse, 'hardware' | 'proposal'> }) {
  const hw = data.hardware
  const devices = hw.devices ?? []
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-sm text-[var(--color-text)]">
        <span>
          {hw.os}/{hw.arch}
        </span>
        <Pill tone={hw.flavour === 'cpu' ? 'muted' : 'ok'}>{hw.flavour === 'cpu' ? 'CPU only' : hw.flavour.toUpperCase()}</Pill>
      </div>
      {devices.length === 0 ? (
        <p className="text-xs text-[var(--color-text-muted)]">
          No usable GPU found. Models run on the CPU (slower); a small model or a remote endpoint is worth considering.
        </p>
      ) : (
        <ul className="space-y-1">
          {devices.map((d) => (
            <li key={`${d.backend}-${d.index}`} className="flex flex-wrap items-center gap-2 text-xs">
              <code className="text-[var(--color-text-muted)]">{d.id || `${d.backend}${d.index}`}</code>
              <span className="text-[var(--color-text)]">{d.name}</span>
              <span className="text-[var(--color-text-muted)]">
                {formatMB(d.free_mb)} free of {formatMB(d.total_mb)}
              </span>
            </li>
          ))}
        </ul>
      )}
      {(hw.ignored ?? []).length > 0 && (
        <p className="text-[11px] text-[var(--color-text-muted)]">
          Ignored (integrated): {(hw.ignored ?? []).map((d) => d.name).join(', ')}
        </p>
      )}
      {Object.keys(data.proposal ?? {}).length > 0 && (
        <p className="text-[11px] text-[var(--color-text-muted)]">
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
  return (
    <li className="rounded-lg border border-[var(--color-border)] p-2">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <div className="flex flex-wrap items-center gap-2">
          <code className="text-[var(--color-text)]">{e.name}</code>
          <Pill tone={stateTone(e.state)}>{e.state}</Pill>
          <span className="text-[var(--color-text-muted)]">
            {e.kind} · {e.flavour} · {(e.labels ?? []).join(', ')}
          </span>
        </div>
        <div className="flex items-center gap-2 text-[var(--color-text-muted)]">
          {e.pid > 0 && <span>pid {e.pid}</span>}
          {e.port > 0 && <span>:{e.port}</span>}
          {e.restarts > 0 && <span>{e.restarts} restarts</span>}
          <button type="button" className="underline" onClick={() => setOpen(!open)} aria-expanded={open}>
            {open ? 'hide' : 'details'}
          </button>
        </div>
      </div>
      {e.last_error && <p className="mt-1 text-xs text-red-500">{e.last_error}</p>}
      {open && (
        <div className="mt-2 space-y-1">
          <p className="break-all font-mono text-[11px] text-[var(--color-text-muted)]">{(e.args ?? []).join(' ')}</p>
          {(e.output ?? []).length > 0 && (
            <pre className="max-h-48 overflow-auto rounded bg-[var(--color-surface-alt)] p-2 text-[11px] text-[var(--color-text-muted)]">
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
  const { data, isLoading, isError, error } = useHardware()
  const refresh = useRefreshHardware()
  const fetchEngine = useFetchEngine()

  return (
    <Section
      title="Hardware and engines"
      icon={Cpu}
      actions={
        <button
          type="button"
          className={buttonClass.secondary}
          disabled={refresh.isPending}
          onClick={() =>
            refresh.mutate(undefined, { onError: (err) => toast.error(errorMessage(err, 'Detection failed')) })
          }
        >
          <RefreshCw size={12} className={cn(refresh.isPending && 'animate-spin')} /> Detect again
        </button>
      }
    >
      {isLoading && <p className="text-sm text-[var(--color-text-muted)]">Detecting…</p>}
      {isError && <p className="text-sm text-red-500">{errorMessage(error, 'Could not read the hardware')}</p>}
      {data && (
        <div className="space-y-4">
          <GpuFaultBanner fault={data.gpu_fault ?? data.hardware.gpu_fault} />
          <DetectedHardware data={data} />

          <div className="space-y-1">
            <h3 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">Engine builds</h3>
            <ul className="space-y-1 text-xs">
              {Object.entries(data.builds).map(([kind, b]) => {
                const have = new Set((data.installed ?? []).filter((i) => i.kind === kind && i.pinned).map((i) => i.flavour))
                return (
                  <li key={kind} className="flex flex-wrap items-center gap-2">
                    <code className="text-[var(--color-text)]">{kind}</code>
                    <span className="text-[var(--color-text-muted)]">{b.build}</span>
                    {b.flavours.map((f) =>
                      have.has(f) ? (
                        <Pill key={f} tone="ok" title="Downloaded">
                          {f}
                        </Pill>
                      ) : (
                        <button
                          key={f}
                          type="button"
                          title={`Download the ${f} build now (it is otherwise fetched on first use)`}
                          className="rounded-full border border-dashed border-[var(--color-border)] px-2 py-0.5 text-[11px] text-[var(--color-text-muted)] hover:text-[var(--color-text)] disabled:opacity-50"
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
                          {f} ↓
                        </button>
                      ),
                    )}
                  </li>
                )
              })}
            </ul>
          </div>

          <div className="space-y-1">
            <h3 className="flex items-center gap-1 text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">
              <Server size={12} /> Running engines
            </h3>
            {(data.engines ?? []).length === 0 ? (
              <p className="text-xs text-[var(--color-text-muted)]">None. An engine starts once a label has a model.</p>
            ) : (
              <ul className="space-y-1">
                {(data.engines ?? []).map((e) => (
                  <EngineRow key={e.name} e={e} />
                ))}
              </ul>
            )}
          </div>

          {(data.voice ?? []).length > 0 && (
            <div className="space-y-1">
              <h3 className="text-xs font-semibold uppercase tracking-wider text-[var(--color-text-muted)]">In-binary voice models</h3>
              <ul className="space-y-1 text-xs">
                {(data.voice ?? []).map((v) => (
                  <li key={v.label} className="flex flex-wrap items-center gap-2">
                    <code className="text-[var(--color-text)]">{v.label}</code>
                    {v.problem ? <Pill tone="warn">{v.problem}</Pill> : <span className="text-[var(--color-text-muted)]">{v.id}</span>}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </Section>
  )
}
