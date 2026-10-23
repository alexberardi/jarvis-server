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

describe('LabelsEditor remote API', () => {
  it('offers the dialect for a remote LLM label and saves remote_api', async () => {
    const live = status('live', '')
    live.config = {
      ...live.config,
      engine: 'remote',
      remote_url: 'https://api.anthropic.com',
      remote_api: 'auto',
      remote_api_effective: 'anthropic',
    }
    const labels: llm.LabelsResponse = {
      labels: [live, status('background', '')],
      voice: null,
      engines: null,
      proposal: {},
      recommend: {},
      warnings: null,
    }
    api.getLabels.mockResolvedValue(labels)
    api.putLabels.mockResolvedValue(labels)
    renderEditor()
    const card = await screen.findByTestId('label-live')
    const select = within(card).getByLabelText('API')
    expect(select).toHaveValue('auto')
    expect(within(card).getByText('Anthropic Messages API now')).toBeInTheDocument()
    fireEvent.change(select, { target: { value: 'openai' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ live: { remote_api: 'openai' } }))
  })
})

describe('GPU fault (prod outage 2026-10-10)', () => {
  it('warns prominently that the driver update needs a reboot and names the stopped labels', async () => {
    const live = { ...status('live', ''), state: 'gpu_unavailable', reason: "The GPU driver was updated; reboot the server to finish (Jarvis can't use the GPU until then)." }
    api.getLabels.mockResolvedValue({
      labels: [live, status('background', '')],
      voice: [],
      engines: [],
      proposal: {},
      recommend: {},
      warnings: null,
      gpu_fault: {
        kind: 'driver_mismatch',
        kernel_version: '580.173.04',
        library_version: '580.178.04',
        labels: ['live'],
        message: 'NVIDIA driver updated (kernel 580.173.04, libraries 580.178.04): reboot the server.',
        user_message: "The GPU driver was updated; reboot the server to finish (Jarvis can't use the GPU until then).",
      },
    })
    renderEditor()
    const alert = await screen.findByRole('alert', { name: 'GPU problem' })
    expect(alert).toHaveTextContent('NVIDIA driver updated (kernel 580.173.04, libraries 580.178.04): reboot the server.')
    expect(alert).toHaveTextContent('Live (voice replies)')
    expect(screen.getByText('GPU unavailable')).toBeInTheDocument()
  })

  it('shows nothing when the GPU is fine', async () => {
    api.getLabels.mockResolvedValue({ labels: [status('live', '')], voice: [], engines: [], proposal: {}, recommend: {}, warnings: null })
    renderEditor()
    await screen.findAllByText('ready')
    expect(screen.queryByRole('alert', { name: 'GPU problem' })).toBeNull()
  })
})

describe('LabelsEditor vLLM GPU memory', () => {
  it('offers the GPU share for a vLLM label only and saves it as a fraction', async () => {
    const live = status('live', '')
    live.config = { ...live.config, kind: 'vllm', model: '/m/Qwen3-0.6B', model_path: '/m/Qwen3-0.6B', gpu_memory_utilization: 0 }
    const labels: llm.LabelsResponse = {
      labels: [live, status('background', '')],
      voice: null,
      engines: null,
      proposal: {},
      recommend: {},
      warnings: null,
    }
    api.getLabels.mockResolvedValue(labels)
    api.putLabels.mockResolvedValue(labels)
    renderEditor()
    const card = await screen.findByTestId('label-live')
    fireEvent.click(within(card).getByRole('button', { name: /Advanced/ }))
    const input = within(card).getByLabelText('GPU memory share')
    expect(input).toHaveValue(0)
    fireEvent.change(input, { target: { value: '0.45' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ live: { gpu_memory_utilization: 0.45 } }))

    const bg = await screen.findByTestId('label-background')
    fireEvent.click(within(bg).getByRole('button', { name: /Advanced/ }))
    expect(within(bg).queryByLabelText('GPU memory share')).not.toBeInTheDocument()
  })
})

describe('LabelsEditor model directory engine', () => {
  it('offers the directory engine for a directory model, and dtype and quantization on Transformers', async () => {
    const live = status('live', '')
    live.config = { ...live.config, kind: 'transformers', model: '/m/SmolLM2', model_path: '/m/SmolLM2', dir_engine: 'transformers', dtype: 'auto', quantization: 'none' }
    const bg = status('background', '')
    bg.config = { ...bg.config, kind: 'mlx-lm', model: '/m/Qwen3-4bit', model_path: '/m/Qwen3-4bit', dir_engine: 'auto', dtype: 'auto', quantization: 'none' }
    const labels: llm.LabelsResponse = {
      labels: [live, bg],
      voice: null,
      engines: null,
      proposal: {},
      recommend: {},
      warnings: null,
    }
    api.getLabels.mockResolvedValue(labels)
    api.putLabels.mockResolvedValue(labels)
    renderEditor()
    const card = await screen.findByTestId('label-live')
    fireEvent.click(within(card).getByRole('button', { name: /Advanced/ }))
    expect(within(card).getByLabelText('Directory engine')).toHaveValue('transformers')
    fireEvent.change(within(card).getByLabelText('Precision'), { target: { value: 'bfloat16' } })
    fireEvent.change(within(card).getByLabelText('Quantization'), { target: { value: 'bnb-4bit' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ live: { dtype: 'bfloat16', quantization: 'bnb-4bit' } }))

    const bgCard = await screen.findByTestId('label-background')
    fireEvent.click(within(bgCard).getByRole('button', { name: /Advanced/ }))
    fireEvent.change(within(bgCard).getByLabelText('Directory engine'), { target: { value: 'transformers' } })
    expect(within(bgCard).queryByLabelText('Precision')).not.toBeInTheDocument()
    fireEvent.click(within(bgCard).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ background: { dir_engine: 'transformers' } }))
  })
})

describe('LabelsEditor model folders', () => {
  it('lists an installed MLX folder in the model dropdown and shows the text-only note, not the image switch', async () => {
    const live = status('live', '')
    live.config = {
      ...live.config,
      kind: 'mlx-lm',
      model: 'qwen3.5-9b-4bit',
      model_path: '/m/mlx-community--Qwen3.5-9B-4bit',
      dir_engine: 'auto',
      note: 'mlx-lm runs this model text-only: vision is off for this label',
    }
    const bg = status('background', '')
    const labels: llm.LabelsResponse = {
      labels: [live, bg],
      voice: null,
      engines: null,
      proposal: {},
      recommend: {},
      warnings: null,
    }
    api.getLabels.mockResolvedValue(labels)
    api.putLabels.mockResolvedValue(labels)
    api.listInstalled.mockResolvedValue({
      models: [
        {
          id: 'qwen3.5-9b-4bit',
          kind: 'llm',
          display: 'Qwen3.5-9B-4bit',
          files: [],
          path: '/m/mlx-community--Qwen3.5-9B-4bit',
          size: 5_100_000_000,
          prompt_provider: 'Qwen3_5_9B_Compressed',
          state: 'ready',
          bytes_done: 5_100_000_000,
          external: false,
          labels: ['live'],
          directory: true,
        },
      ],
      disk_bytes: 0,
      dir: '/m',
    })
    renderEditor()
    const card = await screen.findByTestId('label-live')
    expect(within(card).getByText(/text-only: vision is off/)).toBeInTheDocument()
    expect(within(card).queryByRole('switch', { name: 'Image input' })).toBeNull()
    const bgCard = screen.getByTestId('label-background')
    const option = await within(bgCard).findByRole('option', { name: /Qwen3.5-9B-4bit \(qwen3.5-9b-4bit\) · model folder/ })
    fireEvent.change(within(bgCard).getByLabelText('Model'), { target: { value: (option as HTMLOptionElement).value } })
    fireEvent.click(within(bgCard).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.putLabels).toHaveBeenCalledWith({ background: { model: 'qwen3.5-9b-4bit' } }))
  })
})

describe('LabelsEditor cloud slot', () => {
  it('runs remote or off only, with the remote fields and no local model', async () => {
    const cloud: llm.LabelStatus = {
      label: 'cloud',
      config: { ...config('cloud', ''), engine: 'remote', model: '', model_path: '' },
      state: 'not_configured',
    }
    const labels = { labels: [cloud], engines: [], voice: [], proposal: {}, recommend: {}, warnings: [] } as unknown as llm.LabelsResponse
    api.getLabels.mockResolvedValue(labels)
    api.putLabels.mockResolvedValue(labels)
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <LabelsEditor labels={['cloud']} />
      </QueryClientProvider>,
    )
    const card = await screen.findByTestId('label-cloud')
    expect(within(card).getByText('Cloud (optional)')).toBeInTheDocument()
    expect(within(card).getByText(/Nothing uses it until you pick it for a feature/)).toBeInTheDocument()
    const runs = within(card).getByLabelText('Runs') as HTMLSelectElement
    expect([...runs.options].map((o) => o.value)).toEqual(['remote', 'off'])
    expect(within(card).queryByLabelText('Model')).not.toBeInTheDocument()
    fireEvent.change(within(card).getByLabelText('Base URL'), { target: { value: 'https://api.anthropic.com' } })
    fireEvent.change(within(card).getByLabelText('Model name'), { target: { value: 'claude-sonnet-5-5' } })
    fireEvent.change(within(card).getByLabelText('API key'), { target: { value: 'sk-ant' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() =>
      expect(api.putLabels).toHaveBeenCalledWith({
        cloud: { remote_url: 'https://api.anthropic.com', remote_model: 'claude-sonnet-5-5', remote_api_key: 'sk-ant' },
      }),
    )
  })
})

describe('LabelsEditor speech to text', () => {
  it('runs local, remote or off; remote shows the provider fields, says the audio leaves, and saves them', async () => {
    const stt: llm.LabelStatus = {
      label: 'stt',
      config: { ...config('stt', ''), kind: 'whisper', model_kind: 'stt', model: 'whisper-small.en', remote_timeout_ms: 8000 },
      state: 'ready',
    }
    const labels = { labels: [stt], engines: [], voice: [], proposal: {}, recommend: {}, warnings: [] } as unknown as llm.LabelsResponse
    api.getLabels.mockResolvedValue(labels)
    api.putLabels.mockResolvedValue(labels)
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <LabelsEditor labels={['stt']} />
      </QueryClientProvider>,
    )
    const card = await screen.findByTestId('label-stt')
    const runs = within(card).getByLabelText('Runs') as HTMLSelectElement
    expect([...runs.options].map((o) => o.value)).toEqual(['local', 'remote', 'off'])
    fireEvent.change(runs, { target: { value: 'remote' } })
    expect(within(card).getByText(/audio of every voice turn/)).toBeInTheDocument()
    expect(within(card).getByText(/Speaker recognition stays on this machine/)).toBeInTheDocument()
    // No LLM-only fields, and none of whisper-server's local ones.
    for (const f of ['API', 'Context window', 'Model', 'GPU layers']) expect(within(card).queryByLabelText(f)).not.toBeInTheDocument()
    expect(within(card).queryByText(/accepts images/)).not.toBeInTheDocument()
    expect((within(card).getByLabelText('API key') as HTMLInputElement).type).toBe('password')
    fireEvent.change(within(card).getByLabelText('Base URL'), { target: { value: 'https://api.groq.com/openai/v1' } })
    fireEvent.change(within(card).getByLabelText('Model name'), { target: { value: 'whisper-large-v3-turbo' } })
    fireEvent.change(within(card).getByLabelText('API key'), { target: { value: 'gsk-1' } })
    fireEvent.change(within(card).getByLabelText('Timeout (ms)'), { target: { value: '5000' } })
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() =>
      expect(api.putLabels).toHaveBeenCalledWith({
        stt: {
          engine: 'remote',
          remote_url: 'https://api.groq.com/openai/v1',
          remote_model: 'whisper-large-v3-turbo',
          remote_api_key: 'gsk-1',
          remote_timeout_ms: 5000,
        },
      }),
    )
  })
})
