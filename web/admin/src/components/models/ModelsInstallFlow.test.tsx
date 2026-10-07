import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders } from 'axios'
import { toast } from 'sonner'
import * as llm from '@/api/llm'
import type { CatalogEntry, CatalogResponse, Install, InstalledModel, LabelsResponse } from '@/api/llm'
import CatalogList from './CatalogList'
import InstallsList from './InstallsList'
import InstalledList from './InstalledList'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))

vi.mock('@/api/llm', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/llm')>()
  return {
    ...actual,
    getCatalog: vi.fn(),
    getLabels: vi.fn(),
    listInstalls: vi.fn(),
    listInstalled: vi.fn(),
    startInstall: vi.fn(),
    deleteModel: vi.fn(),
    getHardware: vi.fn(),
    getPromptProvider: vi.fn(),
    hfTokenIsSet: vi.fn(),
  }
})

const api = vi.mocked(llm)

const fit = { verdict: 'fits', needed_mb: 3000, context: 8192, device: 'RTX 3080 Ti', device_mb: 12288, kv_estimated: false }

function entry(over: Partial<CatalogEntry>): CatalogEntry {
  return {
    id: 'x',
    display: 'X',
    kind: 'llm',
    file: 'x.gguf',
    size: 1 << 30,
    sha256: '',
    fit,
    installed: false,
    ...over,
  }
}

const catalog: CatalogResponse = {
  models: [
    entry({ id: 'qwen3-4b', display: 'Qwen 3 4B', tags: ['live', 'background'], prompt_provider: 'Qwen3_8B_Compressed' }),
    entry({ id: 'qwen3.5-9b', display: 'Qwen 3.5 9B', mmproj: 'qwen3.5-9b-mmproj' }),
    entry({ id: 'qwen3.5-9b-mmproj', display: 'projector', kind: 'mmproj' }),
    entry({ id: 'whisper-small.en', display: 'Whisper small.en', kind: 'stt' }),
  ],
  recommended: { live: 'qwen3-4b', background: 'qwen3-4b', stt: 'whisper-small.en' },
  hardware: { os: 'linux', arch: 'amd64', devices: [], sources: [], flavour: 'cuda', detected_at: '' },
  residents: [{ labels: ['other programs'], model: '', needed_mb: 2410, devices: [0] }],
}

function labelStatus(label: string, model = ''): LabelsResponse['labels'][number] {
  return {
    label,
    state: model ? 'ready' : 'not_configured',
    config: { label, model, engine: 'local', model_kind: label === 'stt' ? 'stt' : 'llm' } as LabelsResponse['labels'][number]['config'],
  }
}

const labels: LabelsResponse = {
  labels: [labelStatus('live'), labelStatus('background'), labelStatus('embeddings'), labelStatus('stt')],
  voice: [],
  engines: [],
  proposal: {},
  recommend: {},
  warnings: [],
}

function install(over: Partial<Install>): Install {
  return {
    id: 7,
    model_id: 'qwen3-4b',
    assign: ['live', 'background'],
    state: 'running',
    phase: 'model',
    bytes_total: 1000,
    bytes_done: 100,
    created_at: '',
    updated_at: new Date().toISOString(),
    ...over,
  }
}

const installedModel: InstalledModel = {
  id: 'qwen3-4b',
  kind: 'llm',
  display: 'Qwen 3 4B',
  files: [],
  path: '/m/qwen.gguf',
  size: 1 << 30,
  state: 'ready',
  bytes_done: 1 << 30,
  external: false,
  labels: ['live', 'background'],
  prompt_provider: 'Qwen3_8B_Compressed',
}

function renderFlow() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <InstallsList />
      <CatalogList />
      <InstalledList />
    </QueryClientProvider>,
  )
}

function httpError(status: number, detail: string): AxiosError {
  const config = { headers: new AxiosHeaders() }
  return new AxiosError('fail', 'ERR', config, null, { status, statusText: '', headers: {}, config, data: { detail } })
}

beforeEach(() => {
  vi.clearAllMocks()
  api.getCatalog.mockResolvedValue(catalog)
  api.getLabels.mockResolvedValue(labels)
  api.listInstalls.mockResolvedValue([])
  api.listInstalled.mockResolvedValue({ models: [], disk_bytes: 0, dir: '/m' })
})

describe('Models install flow', () => {
  it('installs a catalog model assigned to its recommended labels, shows progress, then lists it', async () => {
    renderFlow()
    const row = await screen.findByTestId('catalog-qwen3-4b')
    expect(within(row).getByText('Recommended')).toBeInTheDocument()
    expect(within(row).getByText('Fits')).toBeInTheDocument()
    // Residents: what is already on the card is part of the verdict.
    expect(screen.getByText(/Already on the GPU/).parentElement).toHaveTextContent('other programs 2.4 GB on GPU 0')

    fireEvent.click(within(row).getByRole('button', { name: 'Install' }))
    // Live and background have no model yet and this is their recommendation: pre-checked.
    expect(within(row).getByLabelText('Live (voice replies)')).toBeChecked()
    expect(within(row).getByLabelText('Background (memory, planning)')).toBeChecked()

    api.startInstall.mockResolvedValue({ install: install({}), existing: false })
    // The first poll after starting still sees it running, the next one done.
    api.listInstalls
      .mockResolvedValueOnce([install({})])
      .mockResolvedValue([install({ state: 'done', phase: 'done', bytes_done: 1000 })])

    fireEvent.click(within(row).getByRole('button', { name: 'Install Qwen 3 4B' }))

    await waitFor(() =>
      expect(api.startInstall).toHaveBeenCalledWith({ catalog_id: 'qwen3-4b', assign: ['live', 'background'] }),
    )
    const progress = await screen.findByRole('progressbar', { name: 'qwen3-4b progress' })
    expect(progress).toHaveAttribute('aria-valuenow', '10')

    // When the install finishes, the installed list refreshes on its own.
    api.listInstalled.mockResolvedValue({ models: [installedModel], disk_bytes: 1 << 30, dir: '/m' })
    api.getCatalog.mockResolvedValue({
      ...catalog,
      models: catalog.models.map((m) => (m.id === 'qwen3-4b' ? { ...m, installed: true } : m)),
    })
    const installed = await screen.findByTestId('installed-qwen3-4b', {}, { timeout: 4000 })
    expect(within(installed).getByText('live')).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).toBeNull()
  })

  it('sends the vision projector for a model that has one', async () => {
    api.startInstall.mockResolvedValue({ install: install({ model_id: 'qwen3.5-9b' }), existing: false })
    renderFlow()
    const row = await screen.findByTestId('catalog-qwen3.5-9b')
    fireEvent.click(within(row).getByRole('button', { name: 'Install' }))
    fireEvent.click(within(row).getByLabelText('Live (voice replies)'))
    fireEvent.click(within(row).getByRole('button', { name: 'Install Qwen 3.5 9B' }))
    await waitFor(() =>
      expect(api.startInstall).toHaveBeenCalledWith({ catalog_id: 'qwen3.5-9b', assign: ['live'], with_mmproj: true }),
    )
    // Projector entries are installed with their model, not listed on their own.
    expect(screen.queryByTestId('catalog-qwen3.5-9b-mmproj')).toBeNull()
  })

  it('shows the fit warning but still installs (warn, never refuse)', async () => {
    api.startInstall.mockResolvedValue({ install: install({}), existing: false, warning: 'GPU 0 would be over by 1.2 GB' })
    renderFlow()
    const row = await screen.findByTestId('catalog-qwen3-4b')
    fireEvent.click(within(row).getByRole('button', { name: 'Install' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Install Qwen 3 4B' }))
    await waitFor(() => expect(toast.warning).toHaveBeenCalledWith('GPU 0 would be over by 1.2 GB', expect.anything()))
    expect(toast.success).toHaveBeenCalledWith('Installing Qwen 3 4B')
  })

  it('"Install recommended" queues one install per distinct recommended model', async () => {
    api.startInstall.mockResolvedValue({ install: install({}), existing: false })
    renderFlow()
    const btn = await screen.findByRole('button', { name: /Install recommended \(2\)/ })
    fireEvent.click(btn)
    await waitFor(() => expect(api.startInstall).toHaveBeenCalledTimes(2))
    expect(api.startInstall).toHaveBeenNthCalledWith(1, { catalog_id: 'qwen3-4b', assign: ['live', 'background'] })
    expect(api.startInstall).toHaveBeenNthCalledWith(2, { catalog_id: 'whisper-small.en', assign: ['stt'] })
  })

  it('reports a gated repo as needing a Hugging Face token', async () => {
    const onNeedsToken = vi.fn()
    api.startInstall.mockRejectedValue(httpError(403, 'repo is gated or private: set llm.hf_token to a token with access'))
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <CatalogList onNeedsToken={onNeedsToken} />
      </QueryClientProvider>,
    )
    const row = await screen.findByTestId('catalog-qwen3-4b')
    fireEvent.click(within(row).getByRole('button', { name: 'Install' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Install Qwen 3 4B' }))
    await waitFor(() => expect(onNeedsToken).toHaveBeenCalledWith(expect.stringMatching(/gated/)))
  })

  it('deleting an assigned model asks first, then forces', async () => {
    api.listInstalled.mockResolvedValue({ models: [installedModel], disk_bytes: 1 << 30, dir: '/m' })
    api.deleteModel.mockResolvedValue(undefined)
    renderFlow()
    const row = await screen.findByTestId('installed-qwen3-4b')
    fireEvent.click(within(row).getByRole('button', { name: 'Delete qwen3-4b' }))
    const dialog = within(row).getByRole('alertdialog')
    expect(dialog).toHaveTextContent('assigned to live, background')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete and unassign' }))
    await waitFor(() => expect(api.deleteModel).toHaveBeenCalledWith('qwen3-4b', true))
  })

  it('a 409 on an unassigned-looking model turns into a forced-delete question', async () => {
    api.listInstalled.mockResolvedValue({ models: [{ ...installedModel, labels: [] }], disk_bytes: 0, dir: '/m' })
    api.deleteModel
      .mockRejectedValueOnce(httpError(409, 'model is assigned to stt (use force=true)'))
      .mockResolvedValueOnce(undefined)
    renderFlow()
    const row = await screen.findByTestId('installed-qwen3-4b')
    fireEvent.click(within(row).getByRole('button', { name: 'Delete qwen3-4b' }))
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(within(row).getByRole('alertdialog')).toHaveTextContent('model is assigned to stt'))
    fireEvent.click(within(row).getByRole('button', { name: 'Delete and unassign' }))
    await waitFor(() => expect(api.deleteModel).toHaveBeenLastCalledWith('qwen3-4b', true))
  })
})
