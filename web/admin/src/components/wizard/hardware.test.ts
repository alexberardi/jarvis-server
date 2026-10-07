import { describe, expect, it } from 'vitest'
import type { SetupHardware } from '@/api/auth'
import type { LabelConfig, LabelStatus } from '@/api/llm'
import { currentChoices, detectedFlavour, labelUpdates, llamaFlavours, sttGpuAvailable } from './hardware'

function gpu(index: number, name = `GPU${index}`) {
  return { backend: 'cuda', index, id: `CUDA${index}`, name, total_mb: 12288, free_mb: 11000 }
}

function summary(over: Partial<SetupHardware> = {}, devices = [gpu(0)], flavour = 'cuda'): SetupHardware {
  return {
    hardware: { os: 'linux', arch: 'amd64', devices, sources: ['nvidia-smi'], flavour, detected_at: '' },
    proposal: {
      live: { gpu_backend: flavour, gpu_devices: devices.length ? '0' : '' },
      background: { gpu_backend: flavour, gpu_devices: devices.length > 1 ? '1' : devices.length ? '0' : '' },
      embeddings: { gpu_backend: flavour, gpu_devices: '' },
    },
    flavours: { 'llama-server': ['cpu', 'cuda', 'rocm', 'vulkan'], 'whisper-server': ['cpu', 'cuda', 'vulkan'] },
    ...over,
  }
}

function label(name: string, cfg: Partial<LabelConfig> = {}): LabelStatus {
  return {
    label: name,
    state: 'not_configured',
    config: { gpu_backend: 'auto', gpu_devices: '', gpu_layers: 999, ...cfg } as LabelConfig,
  }
}

const defaults = () => [label('live'), label('background'), label('embeddings', { gpu_layers: 0 }), label('stt')]

describe('hardware step logic', () => {
  it('pre-fills from detection and writes nothing when accepted ("Looks good")', () => {
    const s = summary()
    const c = currentChoices(s, defaults())
    expect(c).toEqual({ flavour: 'cuda', stt: 'gpu', sttDevice: '', liveDevice: '0', backgroundDevice: '0' })
    expect(labelUpdates(s, defaults(), c)).toEqual({})
  })

  it('offers only the flavours this platform has builds for', () => {
    const s = summary({ flavours: { 'llama-server': ['cpu', 'metal'], 'whisper-server': ['cpu', 'metal'] } }, [], 'metal')
    expect(llamaFlavours(s)).toEqual(['cpu', 'metal'])
    expect(detectedFlavour(s)).toBe('metal')
    // Detected a flavour with no build here: fall back to CPU.
    expect(detectedFlavour(summary({ flavours: { 'llama-server': ['cpu'] } }))).toBe('cpu')
  })

  it('writes an explicit flavour to every language-model label, and back to auto for the detected one', () => {
    const s = summary()
    const c = { ...currentChoices(s, defaults()), flavour: 'vulkan' }
    expect(labelUpdates(s, defaults(), c)).toEqual({
      live: { gpu_backend: 'vulkan' },
      background: { gpu_backend: 'vulkan' },
      embeddings: { gpu_backend: 'vulkan' },
      stt: { gpu_backend: 'vulkan' },
    })
    const pinned = [label('live', { gpu_backend: 'vulkan' }), label('background'), label('embeddings'), label('stt')]
    expect(currentChoices(s, pinned).flavour).toBe('vulkan')
    expect(labelUpdates(s, pinned, { ...currentChoices(s, pinned), flavour: 'cuda' }).live).toEqual({ gpu_backend: 'auto' })
  })

  it('moves STT to the CPU and back', () => {
    const s = summary()
    const c = { ...currentChoices(s, defaults()), stt: 'cpu' as const }
    expect(labelUpdates(s, defaults(), c)).toEqual({ stt: { gpu_backend: 'cpu' } })

    const onCpu = [label('live'), label('background'), label('embeddings'), label('stt', { gpu_layers: 0 })]
    expect(currentChoices(s, onCpu).stt).toBe('cpu')
    expect(labelUpdates(s, onCpu, { ...currentChoices(s, onCpu), stt: 'gpu' })).toEqual({
      stt: { gpu_backend: 'auto', gpu_layers: 999 },
    })
  })

  it('keeps STT on the CPU when there is no GPU or no GPU speech build', () => {
    expect(sttGpuAvailable(summary({}, [], 'cpu'))).toBe(false)
    expect(sttGpuAvailable(summary({ flavours: { 'llama-server': ['cpu', 'cuda'], 'whisper-server': ['cpu'] } }))).toBe(false)
    expect(currentChoices(summary({}, [], 'cpu'), defaults()).stt).toBe('cpu')
  })

  it('places labels per GPU on a multi-GPU box, defaulting to the proposal', () => {
    const s = summary({}, [gpu(0), gpu(1)])
    const c = currentChoices(s, defaults())
    expect(c.liveDevice).toBe('0')
    expect(c.backgroundDevice).toBe('1')
    expect(labelUpdates(s, defaults(), c)).toEqual({})
    expect(labelUpdates(s, defaults(), { ...c, backgroundDevice: '0', sttDevice: '1' })).toEqual({
      background: { gpu_devices: '0' },
      stt: { gpu_devices: '1' },
    })
  })
})
