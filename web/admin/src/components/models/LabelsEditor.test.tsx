import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import * as llm from '@/api/llm'
import LabelsEditor, { ImageInputToggle } from './LabelsEditor'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))
vi.mock('@/api/llm', async (orig) => ({
  ...(await orig<typeof import('@/api/llm')>()),
  getLabels: vi.fn(),
  listInstalled: vi.fn(),
  getHardware: vi.fn(),
  putLabels: vi.fn(),
}))

const api = vi.mocked(llm)

function config(label: string, mmproj: string): llm.LabelConfig {
  return {
    label,
    kind: 'llm',
    model_kind: 'llm',
    engine: 'local',
    model: 'qwen3.5-9b',
    model_path: '/m/q.gguf',
    mmproj,
    mmproj_path: '',
    context: 0,
    parallel: 1,
    gpu_backend: 'auto',
    gpu_devices: '',
    split_mode: '',
    tensor_split: '',
    gpu_layers: 999,
    kv_cache_type: 'f16',
    flash_attn: 'auto',
    extra_args: '',
    embedding: false,
    remote_url: '',
    remote_model: '',
    remote_vision: false,
  }
}

function status(label: string, mmproj: string, vision?: boolean): llm.LabelStatus {
  return {
    label,
    config: config(label, mmproj),
    state: 'ready',
    endpoint:
      vision === undefined
        ? undefined
        : {
            label,
            kind: 'llm',
            base_url: 'http://127.0.0.1:1',
            model: 'q',
            vision,
            embeddings: false,
            remote: false,
            context_length: 8192,
            parallel: 1,
          },
  }
}

function renderEditor() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <LabelsEditor labels={['live', 'background']} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listInstalled.mockResolvedValue({ models: [], disk_bytes: 0, dir: '/m' })
  api.getHardware.mockRejectedValue(new Error('no hardware in tests'))
})

describe('ImageInputToggle', () => {
  it('is on for "" and off for "none", writing those values', () => {
    const onChange = vi.fn()
    const { rerender } = render(<ImageInputToggle id="t" label="live" mmproj="" running onChange={onChange} />)
    const sw = screen.getByRole('switch', { name: 'Image input' })
    expect(sw).toBeChecked()
    expect(screen.getByText(/Running engine has image input/)).toBeInTheDocument()
    fireEvent.click(sw)
    expect(onChange).toHaveBeenLastCalledWith('none')

    rerender(<ImageInputToggle id="t" label="live" mmproj="none" running={false} onChange={onChange} />)
    expect(screen.getByRole('switch', { name: 'Image input' })).not.toBeChecked()
    expect(screen.getByText(/Running engine has no image input/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: 'Image input' }))
    expect(onChange).toHaveBeenLastCalledWith('')
  })

  it('keeps an explicit projector for the raw editor', () => {
    render(<ImageInputToggle id="t" label="live" mmproj="qwen-mmproj-f16" running onChange={vi.fn()} />)
    expect(screen.queryByRole('switch')).toBeNull()
    expect(screen.getByText('qwen-mmproj-f16')).toBeInTheDocument()
    expect(screen.getByText(/change it under Advanced/)).toBeInTheDocument()
  })

  it('warns that background off disables recipe photo vision', () => {
    render(<ImageInputToggle id="t" label="background" mmproj="" onChange={vi.fn()} />)
    expect(screen.getByText(/recipe photo import/)).toBeInTheDocument()
    expect(screen.getByText(/Not running/)).toBeInTheDocument()
  })
})

describe('LabelsEditor image input', () => {
  it('shows a toggle per LLM slot and saves mmproj "none" when switched off', async () => {
    const labels: llm.LabelsResponse = {
      labels: [status('live', '', true), status('background', 'none', false)],
      voice: null,
      engines: null,
      proposal: {},
      recommend: {},
      warnings: null,
    }
    api.getLabels.mockResolvedValue(labels)
    api.putLabels.mockResolvedValue(labels)
    renderEditor()
    const live = await screen.findByTestId('label-live')
    const bg = screen.getByTestId('label-background')
    expect(within(live).getByRole('switch', { name: 'Image input' })).toBeChecked()
    expect(within(bg).getByRole('switch', { name: 'Image input' })).not.toBeChecked()
    expect(within(bg).getByText(/recipe photo import/)).toBeInTheDocument()

    fireEvent.click(within(live).getByRole('switch', { name: 'Image input' }))
    fireEvent.click(within(live).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ live: { mmproj: 'none' } }))
  })
})
