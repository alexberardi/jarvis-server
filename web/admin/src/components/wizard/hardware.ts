/**
 * Pure logic for the setup wizard's Hardware step (AD3): what the detection proposes, what the
 * labels are set to now, and the smallest `PUT /api/llm/v1/models/labels` body that turns the
 * operator's choices into settings.
 *
 * "Effective" values treat the label defaults as what they mean: `gpu_backend: auto` is the
 * detected flavour and an empty `gpu_devices` is the proposal's device. A choice that matches
 * the effective value writes nothing, so accepting the defaults ("Looks good") leaves every
 * label on auto and a later hardware change is still picked up.
 */
import type { SetupHardware } from '@/api/auth'
import type { Device, EngineLabel, LabelConfig, LabelStatus, LabelUpdate } from '@/api/llm'

export type SttTarget = 'gpu' | 'cpu'

export interface HardwareChoices {
  /** llama-server flavour for the language-model labels (live, background, embeddings). */
  flavour: string
  stt: SttTarget
  /** Device index for STT on a multi-GPU box; "" = automatic. */
  sttDevice: string
  liveDevice: string
  backgroundDevice: string
}

const LLAMA_LABELS: EngineLabel[] = ['live', 'background', 'embeddings']

function config(labels: LabelStatus[] | undefined, name: string): LabelConfig | undefined {
  return labels?.find((l) => l.label === name)?.config
}

function isAuto(v: string | undefined): boolean {
  return !v || v.toLowerCase() === 'auto'
}

/** The llama-server flavours this platform has builds for: the only engine choices offered. */
export function llamaFlavours(s: SetupHardware): string[] {
  const f = s.flavours?.['llama-server'] ?? []
  return f.length > 0 ? f : ['cpu']
}

/** The detected flavour, if this platform has a build for it; else CPU (or the first build). */
export function detectedFlavour(s: SetupHardware): string {
  const offered = llamaFlavours(s)
  const hw = s.hardware?.flavour || 'cpu'
  if (offered.includes(hw)) return hw
  return offered.includes('cpu') ? 'cpu' : offered[0]
}

export function gpus(s: SetupHardware): Device[] {
  return s.hardware?.devices ?? []
}

/** STT can run on a GPU when one was detected and whisper-server has a GPU build here. */
export function sttGpuAvailable(s: SetupHardware): boolean {
  const whisperGpu = (s.flavours?.['whisper-server'] ?? []).filter((f) => f !== 'cpu')
  return gpus(s).length > 0 && whisperGpu.length > 0 && (s.hardware?.flavour ?? 'cpu') !== 'cpu'
}

/** currentChoices is what the labels effectively run today: the step's pre-filled answer. */
export function currentChoices(s: SetupHardware, labels: LabelStatus[] | undefined): HardwareChoices {
  const live = config(labels, 'live')
  const bg = config(labels, 'background')
  const stt = config(labels, 'stt')
  const offered = llamaFlavours(s)
  const flavour = !isAuto(live?.gpu_backend) && offered.includes(live!.gpu_backend.toLowerCase())
    ? live!.gpu_backend.toLowerCase()
    : detectedFlavour(s)
  const sttOnCpu = stt?.gpu_backend?.toLowerCase() === 'cpu' || stt?.gpu_layers === 0
  return {
    flavour,
    stt: !sttOnCpu && sttGpuAvailable(s) ? 'gpu' : 'cpu',
    sttDevice: stt?.gpu_devices ?? '',
    liveDevice: live?.gpu_devices || s.proposal?.live?.gpu_devices || '',
    backgroundDevice: bg?.gpu_devices || s.proposal?.background?.gpu_devices || '',
  }
}

/** The gpu_backend value for a chosen flavour: "auto" when it is the detected one. */
function backendValue(s: SetupHardware, flavour: string): string {
  return flavour === detectedFlavour(s) ? 'auto' : flavour
}

/**
 * labelUpdates is the labels PUT body for `choices`, holding only what differs from
 * `currentChoices`. Empty means nothing to write.
 */
export function labelUpdates(
  s: SetupHardware,
  labels: LabelStatus[] | undefined,
  choices: HardwareChoices,
): Partial<Record<EngineLabel, LabelUpdate>> {
  const base = currentChoices(s, labels)
  const out: Partial<Record<EngineLabel, LabelUpdate>> = {}
  const put = (label: EngineLabel, u: LabelUpdate) => {
    out[label] = { ...(out[label] ?? {}), ...u }
  }

  if (choices.flavour !== base.flavour) {
    for (const l of LLAMA_LABELS) put(l, { gpu_backend: backendValue(s, choices.flavour) })
  }
  if (choices.liveDevice !== base.liveDevice) put('live', { gpu_devices: choices.liveDevice })
  if (choices.backgroundDevice !== base.backgroundDevice) put('background', { gpu_devices: choices.backgroundDevice })

  const whisper = s.flavours?.['whisper-server'] ?? []
  if (choices.stt !== base.stt) {
    if (choices.stt === 'cpu') {
      put('stt', { gpu_backend: 'cpu' })
    } else {
      const f = whisper.includes(choices.flavour) ? backendValue(s, choices.flavour) : 'auto'
      const u: LabelUpdate = { gpu_backend: f }
      // gpu_layers 0 is the other way STT gets pinned to the CPU.
      if (config(labels, 'stt')?.gpu_layers === 0) u.gpu_layers = 999
      put('stt', u)
    }
  } else if (choices.stt === 'gpu' && choices.flavour !== base.flavour && whisper.includes(choices.flavour)) {
    // STT follows the language models onto the newly chosen GPU runtime.
    put('stt', { gpu_backend: backendValue(s, choices.flavour) })
  }
  if (choices.stt === 'gpu' && choices.sttDevice !== base.sttDevice) put('stt', { gpu_devices: choices.sttDevice })
  return out
}
