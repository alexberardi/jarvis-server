import { useEffect, useState } from 'react'
import { CheckCircle2, AlertTriangle, Loader2 } from 'lucide-react'
import { useWizard } from '@/context/WizardContext'
import { getServiceRegistry } from '@/api/install'
import type { ServiceRegistry } from '@/types/wizard'

export default function ReviewStep() {
  const { state } = useWizard()
  const [registry, setRegistry] = useState<ServiceRegistry | null>(null)

  useEffect(() => {
    getServiceRegistry().then(setRegistry).catch(() => {})
  }, [])

  if (!registry) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 size={32} className="animate-spin text-[var(--color-primary)]" />
      </div>
    )
  }

  const coreServices = registry.services.filter((s) => s.category === 'core')
  const enabledOptional = registry.services.filter(
    (s) => s.category !== 'core' && state.enabledModules.includes(s.id),
  )
  const allEnabled = [...coreServices, ...enabledOptional]

  // Port conflict detection
  const portMap = new Map<number, string[]>()
  for (const svc of allEnabled) {
    const port = state.portOverrides[svc.id] ?? svc.port
    const existing = portMap.get(port) ?? []
    portMap.set(port, [...existing, svc.name])
  }
  const conflicts = [...portMap.entries()].filter(([, names]) => names.length > 1)

  // Resource estimates. A service is "native" only when the user opted into
  // it in the native-services step — being nativeCapable just means it's
  // eligible. On Linux, nativeServices is always empty.
  const dbCount = allEnabled.filter((s) => s.database).length
  const nativeServices = allEnabled.filter((s) => state.nativeServices.includes(s.id))
  const dockerServices = allEnabled.filter((s) => !state.nativeServices.includes(s.id))

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold text-[var(--color-text)]">Review Configuration</h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          Review your selections before installing.
        </p>
      </div>

      {/* Summary */}
      <div className="grid grid-cols-3 gap-3">
        <SummaryCard label="Docker Services" value={dockerServices.length} />
        <SummaryCard label="Native Services" value={nativeServices.length} />
        <SummaryCard label="Databases" value={dbCount} />
      </div>

      {/* Deployment Mode */}
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] p-4">
        <p className="text-xs font-medium uppercase text-[var(--color-text-muted)]">
          Deployment Mode
        </p>
        <p className="mt-1 text-sm font-medium text-[var(--color-text)]">
          {state.deploymentMode === 'local' ? 'Local (all services on this machine)' : 'Remote LLM'}
        </p>
        {state.deploymentMode === 'remote-llm' && state.remoteLlmUrl && (
          <p className="mt-0.5 text-xs text-[var(--color-text-muted)]">
            LLM: {state.remoteLlmUrl}
          </p>
        )}
      </div>

      {/* Service List */}
      <div className="rounded-lg border border-[var(--color-border)] overflow-hidden">
        <div className="border-b border-[var(--color-border)] bg-[var(--color-surface-alt)] px-3 py-2">
          <span className="text-xs font-medium text-[var(--color-text-muted)]">
            Services ({allEnabled.length})
          </span>
        </div>
        <div className="divide-y divide-[var(--color-border)]">
          {allEnabled.map((svc) => (
            <div key={svc.id} className="flex items-center justify-between px-3 py-2">
              <div className="flex items-center gap-2">
                <CheckCircle2 size={14} className="text-green-500" />
                <span className="text-sm text-[var(--color-text)]">{svc.name}</span>
              </div>
              <span className="text-xs text-[var(--color-text-muted)]">
                :{state.portOverrides[svc.id] ?? svc.port}
              </span>
            </div>
          ))}
        </div>
      </div>

      {/* Jarvis Relay */}
      {state.relayEnabled && (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] p-4">
          <p className="text-xs font-medium uppercase text-[var(--color-text-muted)]">
            Jarvis Relay
          </p>
          <p className="mt-1 text-sm font-medium text-[var(--color-text)]">
            Enabled
          </p>
          <p className="mt-0.5 text-xs text-[var(--color-text-muted)]">
            https://relay.jarvisautomation.io
          </p>
        </div>
      )}

      {/* Port Conflicts */}
      {conflicts.length > 0 && (
        <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-4">
          <div className="flex items-center gap-2">
            <AlertTriangle size={16} className="text-amber-500" />
            <span className="text-sm font-medium text-amber-500">Port Conflicts</span>
          </div>
          {conflicts.map(([port, names]) => (
            <p key={port} className="mt-1 text-xs text-[var(--color-text-muted)]">
              Port {port}: {names.join(', ')}
            </p>
          ))}
        </div>
      )}
    </div>
  )
}

function SummaryCard({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] p-3 text-center">
      <p className="text-2xl font-bold text-[var(--color-primary)]">{value}</p>
      <p className="text-xs text-[var(--color-text-muted)]">{label}</p>
    </div>
  )
}
