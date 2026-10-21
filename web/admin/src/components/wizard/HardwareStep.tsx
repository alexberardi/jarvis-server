import { useState } from 'react'
import { Cpu } from 'lucide-react'
import { toast } from 'sonner'
import { DetectedHardware } from '@/components/models/HardwarePanel'
import { Button, ErrorState, Select } from '@/components/ui'
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
import { StepActions, StepHeader, StepLoading } from './StepFrame'

const FLAVOUR_TITLE: Record<string, string> = {
  cpu: 'CPU only',
  cuda: 'NVIDIA CUDA',
  rocm: 'AMD ROCm',
  vulkan: 'Vulkan',
  metal: 'Apple Metal',
}

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
    <Select
      id={id}
      label={label}
      wrapperClassName="max-w-md"
      value={value}
      onValueChange={onChange}
      options={[
        { value: '', label: 'Automatic' },
        ...gpus(summary).map((d) => ({ value: String(d.index), label: `GPU ${d.index}: ${d.name} (${formatMB(d.total_mb)})` })),
      ]}
    />
  )
}

/** A radio drawn as a choice chip: the selected one on the primary's soft fill. */
function choiceClass(selected: boolean, disabled?: boolean) {
  return cn(
    'flex min-h-10 items-center gap-2 rounded-control border px-3 text-body',
    disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
    selected ? 'border-primary bg-primary-soft text-text' : 'border-border-strong text-text hover:bg-surface-alt',
  )
}

const legendClass = 'text-subheading text-text'
const noteClass = 'text-small text-text-muted'

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
    <div className="space-y-6">
      <fieldset className="space-y-2">
        <legend className={legendClass}>Language-model engine</legend>
        <p className={noteClass}>
          Which llama.cpp build runs the language models. Only builds that exist for this platform are listed.
        </p>
        <div className="flex flex-wrap gap-2">
          {offered.map((f) => (
            <label key={f} className={choiceClass(choices.flavour === f)}>
              <input
                type="radio"
                name="engine-flavour"
                value={f}
                checked={choices.flavour === f}
                onChange={() => set({ flavour: f })}
                className="accent-primary"
              />
              {FLAVOUR_TITLE[f] ?? f}
              {f === detected && <span className="text-caption text-text-muted">(detected)</span>}
            </label>
          ))}
        </div>
      </fieldset>

      {multiGpu && choices.flavour !== 'cpu' && (
        <fieldset className="space-y-3">
          <legend className={cn(legendClass, 'mb-2')}>Which GPU runs what</legend>
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
        <legend className={legendClass}>Speech to text</legend>
        <div className="flex flex-wrap gap-2">
          <label className={choiceClass(choices.stt === 'gpu', !sttGpu)}>
            <input
              type="radio"
              name="stt-target"
              value="gpu"
              checked={choices.stt === 'gpu'}
              disabled={!sttGpu}
              onChange={() => set({ stt: 'gpu' })}
              className="accent-primary"
            />
            On the GPU (fast)
          </label>
          <label className={choiceClass(choices.stt === 'cpu')}>
            <input
              type="radio"
              name="stt-target"
              value="cpu"
              checked={choices.stt === 'cpu'}
              onChange={() => set({ stt: 'cpu' })}
              className="accent-primary"
            />
            On the CPU
          </label>
        </div>
        {!sttGpu && (
          <p className={noteClass}>No GPU build of the speech engine fits this machine, so it runs on the CPU.</p>
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
        <p className={noteClass}>
          Text to speech and speaker recognition always run on the CPU; they are small and fast there.
        </p>
      </fieldset>

      <StepActions>
        <Button variant="primary" loading={put.isPending} onClick={save}>
          {changed ? 'Save and continue' : 'Looks good'}
        </Button>
      </StepActions>
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
    <div className="space-y-5">
      <StepHeader icon={<Cpu size={22} />} title="Hardware">
        This is what jarvisd found. The choices below are already set to what suits it.
      </StepHeader>

      {(state.isLoading || labels.isLoading) && <StepLoading>Detecting the hardware</StepLoading>}
      {state.isError && (
        <ErrorState
          title="Couldn't read the hardware"
          message={errorMessage(state.error, 'The server did not answer.')}
          onRetry={() => state.refetch()}
        />
      )}

      {state.data && !summary && (
        <>
          <p className="text-body text-text-muted">
            This jarvisd runs without its local model engines, so there is nothing to place here.
          </p>
          <StepActions>
            <Button variant="primary" onClick={onDone}>
              Continue
            </Button>
          </StepActions>
        </>
      )}

      {summary && !labels.isLoading && (
        <>
          <div className="rounded-card bg-surface-alt p-4">
            <DetectedHardware data={summary} />
          </div>
          <HardwareChoicesForm summary={summary} onDone={onDone} />
        </>
      )}
    </div>
  )
}
