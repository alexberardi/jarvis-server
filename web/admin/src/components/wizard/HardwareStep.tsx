import { useState } from 'react'
import { Cpu } from 'lucide-react'
import { toast } from 'sonner'
import { DetectedHardware } from '@/components/models/HardwarePanel'
import { buttonClass } from '@/components/models/styles'
import { useLabels, usePutLabels } from '@/hooks/useModelManager'
import { useSetupState } from '@/hooks/useSetup'
import { errorMessage } from '@/lib/errors'
import { formatMB } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { SetupHardware } from '@/api/auth'
import {
  currentChoices,
  detectedFlavour,
  gpus,
  labelUpdates,
  llamaFlavours,
  sttGpuAvailable,
  type HardwareChoices,
} from './hardware'

const FLAVOUR_TITLE: Record<string, string> = {
  cpu: 'CPU only',
  cuda: 'NVIDIA CUDA',
  rocm: 'AMD ROCm',
  vulkan: 'Vulkan',
  metal: 'Apple Metal',
}

const selectClass =
  'rounded-lg border border-[var(--color-border)] bg-[var(--color-surface-alt)] px-2 py-1 text-sm text-[var(--color-text)] outline-none focus:ring-2 focus:ring-[var(--color-primary)]'

function DeviceSelect({
  id,
  label,
  value,
  onChange,
  summary,
}: {
  id: string
  label: string
  value: string
  onChange: (v: string) => void
  summary: SetupHardware
}) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label htmlFor={id} className="w-48 text-sm text-[var(--color-text)]">
        {label}
      </label>
      <select id={id} className={selectClass} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Automatic</option>
        {gpus(summary).map((d) => (
          <option key={d.index} value={String(d.index)}>
            GPU {d.index}: {d.name} ({formatMB(d.total_mb)})
          </option>
        ))}
      </select>
    </div>
  )
}

/**
 * HardwareChoicesForm is the editable part, rendered once detection and the label settings are
 * in, so its state starts from them.
 */
function HardwareChoicesForm({ summary, onDone }: { summary: SetupHardware; onDone: () => void }) {
  const labels = useLabels()
  const put = usePutLabels()
  const [choices, setChoices] = useState<HardwareChoices>(() => currentChoices(summary, labels.data?.labels))
  const set = (patch: Partial<HardwareChoices>) => setChoices((c) => ({ ...c, ...patch }))

  const offered = llamaFlavours(summary)
  const detected = detectedFlavour(summary)
  const multiGpu = gpus(summary).length > 1
  const sttGpu = sttGpuAvailable(summary)
  const updates = labelUpdates(summary, labels.data?.labels, choices)
  const changed = Object.keys(updates).length > 0

  function save() {
    if (!changed) {
      onDone()
      return
    }
    put.mutate(updates, {
      onSuccess: () => {
        toast.success('Hardware choices saved')
        onDone()
      },
      onError: (err) => toast.error(errorMessage(err, 'Could not save the hardware choices')),
    })
  }

  return (
    <div className="space-y-4">
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-[var(--color-text)]">Language-model engine</legend>
        <p className="text-xs text-[var(--color-text-muted)]">
          Which llama.cpp build runs the language models. Only builds that exist for this platform are listed.
        </p>
        <div className="flex flex-wrap gap-2">
          {offered.map((f) => (
            <label
              key={f}
              className={cn(
                'flex cursor-pointer items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm',
                choices.flavour === f
                  ? 'border-[var(--color-primary)] bg-[var(--color-primary)]/10 text-[var(--color-text)]'
                  : 'border-[var(--color-border)] text-[var(--color-text-muted)]',
              )}
            >
              <input
                type="radio"
                name="engine-flavour"
                value={f}
                checked={choices.flavour === f}
                onChange={() => set({ flavour: f })}
              />
              {FLAVOUR_TITLE[f] ?? f}
              {f === detected && <span className="text-[10px] text-[var(--color-text-muted)]">(detected)</span>}
            </label>
          ))}
        </div>
      </fieldset>

      {multiGpu && choices.flavour !== 'cpu' && (
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-[var(--color-text)]">Which GPU runs what</legend>
          <DeviceSelect
            id="device-live"
            label="Live model (voice replies)"
            value={choices.liveDevice}
            onChange={(v) => set({ liveDevice: v })}
            summary={summary}
          />
          <DeviceSelect
            id="device-background"
            label="Background model"
            value={choices.backgroundDevice}
            onChange={(v) => set({ backgroundDevice: v })}
            summary={summary}
          />
        </fieldset>
      )}

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium text-[var(--color-text)]">Speech to text</legend>
        <div className="flex flex-wrap gap-4 text-sm text-[var(--color-text)]">
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="stt-target"
              value="gpu"
              checked={choices.stt === 'gpu'}
              disabled={!sttGpu}
              onChange={() => set({ stt: 'gpu' })}
            />
            On the GPU (fast)
          </label>
          <label className="flex items-center gap-1.5">
            <input
              type="radio"
              name="stt-target"
              value="cpu"
              checked={choices.stt === 'cpu'}
              onChange={() => set({ stt: 'cpu' })}
            />
            On the CPU
          </label>
        </div>
        {!sttGpu && (
          <p className="text-xs text-[var(--color-text-muted)]">No GPU build of the speech engine fits this machine, so it runs on the CPU.</p>
        )}
        {multiGpu && choices.stt === 'gpu' && (
          <DeviceSelect
            id="device-stt"
            label="Speech-to-text GPU"
            value={choices.sttDevice}
            onChange={(v) => set({ sttDevice: v })}
            summary={summary}
          />
        )}
        <p className="text-xs text-[var(--color-text-muted)]">
          Text to speech and speaker recognition always run on the CPU; they are small and fast there.
        </p>
      </fieldset>

      <div className="flex justify-end">
        <button type="button" className={buttonClass.primary} disabled={put.isPending} onClick={save}>
          {put.isPending ? 'Saving…' : changed ? 'Save and continue' : 'Looks good'}
        </button>
      </div>
    </div>
  )
}

/**
 * HardwareStep (AD3) shows what jarvisd detected and asks the few placement questions that
 * matter, every one pre-filled to the detection default so one click ("Looks good") accepts
 * them. Choices are written through the validated labels PUT (I4).
 */
export default function HardwareStep({ onDone }: { onDone: () => void }) {
  const state = useSetupState()
  const labels = useLabels()
  const summary = state.data?.hardware

  return (
    <div className="space-y-4">
      <div>
        <h2 className="flex items-center gap-2 text-xl font-bold text-[var(--color-text)]">
          <Cpu size={20} /> Hardware
        </h2>
        <p className="mt-1 text-sm text-[var(--color-text-muted)]">
          This is what jarvisd found. The choices below are already set to what suits it.
        </p>
      </div>

      {(state.isLoading || labels.isLoading) && <p className="text-sm text-[var(--color-text-muted)]">Detecting…</p>}
      {state.isError && <p className="text-sm text-red-500">{errorMessage(state.error, 'Could not read the hardware')}</p>}

      {state.data && !summary && (
        <>
          <p className="text-sm text-[var(--color-text-muted)]">
            This jarvisd runs without its local model engines, so there is nothing to place here.
          </p>
          <div className="flex justify-end">
            <button type="button" className={buttonClass.primary} onClick={onDone}>
              Continue
            </button>
          </div>
        </>
      )}

      {summary && !labels.isLoading && (
        <>
          <div className="rounded-lg border border-[var(--color-border)] p-3">
            <DetectedHardware data={summary} />
          </div>
          <HardwareChoicesForm summary={summary} onDone={onDone} />
        </>
      )}
    </div>
  )
}
