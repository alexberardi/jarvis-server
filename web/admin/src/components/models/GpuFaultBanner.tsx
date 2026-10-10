import { AlertOctagon } from 'lucide-react'
import { LABEL_TITLE, type GPUFault, type Label } from '@/api/llm'

/**
 * GpuFaultBanner is the prominent warning for a GPU jarvisd can't use: an NVIDIA driver update
 * waiting for a reboot (driver_mismatch), or a GPU the models expect that detection no longer
 * sees. Models that need the GPU are refused, not run on the CPU, until it is fixed.
 */
export default function GpuFaultBanner({ fault }: { fault?: GPUFault | null }) {
  if (!fault) return null
  const labels = (fault.labels ?? []).map((l) => LABEL_TITLE[l as Label] ?? l)
  return (
    <div
      role="alert"
      aria-label="GPU problem"
      className="flex items-start gap-3 rounded-lg border border-red-500/40 bg-red-500/10 p-4"
    >
      <AlertOctagon size={20} className="mt-0.5 shrink-0 text-red-500" />
      <div className="min-w-0 space-y-1">
        <p className="text-sm font-semibold text-red-500">{fault.message}</p>
        <p className="text-xs text-[var(--color-text)]">
          {labels.length > 0
            ? `Not running until then: ${labels.join(', ')}. Jarvis won't move them to the CPU (every request would time out); requests fail at once with this reason instead.`
            : 'Engines already running keep working until the reboot, but any that restart will not get the GPU.'}
        </p>
      </div>
    </div>
  )
}
