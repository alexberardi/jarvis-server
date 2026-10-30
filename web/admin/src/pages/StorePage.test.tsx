import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AxiosError, AxiosHeaders } from 'axios'
import { AuthProvider } from '@/auth/AuthContext'
import type { NodeLiveness } from '@/api/nodes'
import type { StoreDetail } from '@/api/store'
import StorePage from './StorePage'

const store = vi.hoisted(() => ({ browseStore: vi.fn(), getStorePackage: vi.fn(), requestInstall: vi.fn(), pollInstall: vi.fn() }))
const settings = vi.hoisted(() => ({ getHouseholdSettings: vi.fn(), putHouseholdSetting: vi.fn() }))
const nodes = vi.hoisted(() => ({ getNodeLiveness: vi.fn() }))
const households = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/store', async (orig) => ({ ...(await orig<typeof import('@/api/store')>()), ...store }))
vi.mock('@/api/household', () => settings)
vi.mock('@/api/nodes', async (orig) => ({ ...(await orig<typeof import('@/api/nodes')>()), ...nodes }))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), listHouseholds: households.list }))

const detail = (command_name: string, over: Partial<StoreDetail> = {}): StoreDetail => ({
  command_name,
  display_name: command_name === 'get_news' ? 'News' : 'Doorbell',
  description: 'Does a thing',
  author: 'acme',
  latest_version: '1.0.0',
  categories: [],
  verified: false,
  github_repo_url: `https://github.com/acme/${command_name}`,
  git_tag: 'v1.0.0',
  ...over,
})

const node = (node_id: string, over: Partial<NodeLiveness> = {}): NodeLiveness => ({
  node_id,
  room: node_id,
  household_id: 'h1',
  online: true,
  last_seen: null,
  last_seen_version: null,
  is_busy: false,
  node_kind: null,
  ...over,
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter>
          <StorePage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

const pkgs = { get_news: detail('get_news'), doorbell: detail('doorbell', { targets: ['node'], apt_packages: ['ffmpeg'] }) }

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', 'a.eyJob3VzZWhvbGRfaWQiOiJoMSJ9.s') // {"household_id":"h1"}
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'a@example.com', is_superuser: true }))
  households.list.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'admin' }])
  store.browseStore.mockResolvedValue(Object.values(pkgs))
  store.getStorePackage.mockImplementation(async (_hh: string, n: keyof typeof pkgs) => pkgs[n])
  settings.getHouseholdSettings.mockResolvedValue({ 'pantry.default_bundle': 'get_news' })
  settings.putHouseholdSetting.mockResolvedValue(undefined)
  nodes.getNodeLiveness.mockResolvedValue([
    node('kitchen'),
    node('laptop', { node_kind: 'desktop', name: 'Laptop', room: 'desktop' }),
    node('garage', { online: false }),
    node('elsewhere', { household_id: 'h2' }),
  ])
  store.requestInstall.mockImplementation(async (id: string) => `r-${id}`)
  store.pollInstall.mockResolvedValue({ status: 'completed', error_message: null })
})

describe('StorePage', () => {
  it('lists the packages and keeps the default bundle', async () => {
    renderPage()
    expect(await screen.findByTestId('package-doorbell')).toHaveTextContent('Doorbell')
    const bundle = screen.getByRole('list', { name: 'In the bundle' })
    await waitFor(() => expect(bundle).toHaveTextContent('News'))

    fireEvent.click(screen.getByRole('switch', { name: 'Doorbell in the default bundle' }))
    await waitFor(() => expect(settings.putHouseholdSetting).toHaveBeenCalledWith('h1', 'pantry.default_bundle', 'get_news,doorbell'))
    await waitFor(() => expect(bundle).toHaveTextContent('Doorbell'))
  })

  it('installs a package on the chosen nodes, leaving out what cannot run it', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Install Doorbell' }))
    const dialog = await screen.findByRole('dialog')
    // One household: straight to its nodes.
    const kitchen = await within(dialog).findByRole('checkbox', { name: /kitchen/ })
    expect(within(dialog).getByRole('checkbox', { name: /Laptop/ })).toBeDisabled()
    expect(within(dialog).getByText('Doorbell is made for Pi nodes, not computers')).toBeInTheDocument()
    expect(within(dialog).getByRole('checkbox', { name: /garage/ })).toBeDisabled()
    expect(within(dialog).queryByText('elsewhere')).toBeNull()
    expect(within(dialog).getByText('ffmpeg')).toBeInTheDocument()

    fireEvent.click(kitchen)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Install on 1 node' }))
    await waitFor(() => expect(store.requestInstall).toHaveBeenCalledWith('kitchen', pkgs.doorbell))
    expect(store.requestInstall).toHaveBeenCalledTimes(1)
    const job = await within(dialog).findByTestId('install-job')
    await waitFor(() => expect(job).toHaveTextContent('kitchenInstalled'))
  })

  it('installs the bundle, and shows a node that refused', async () => {
    store.requestInstall.mockRejectedValueOnce(new Error('Only the member who added this computer, or a household admin, can change its packages'))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Install bundle' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(await within(dialog).findByRole('checkbox', { name: /Laptop/ }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Install on 1 node' }))
    expect(await within(dialog).findByTestId('install-job')).toHaveTextContent('Only the member who added this computer')
  })

  it('asks to turn the Pantry on when it is off', async () => {
    store.browseStore.mockRejectedValue(
      new AxiosError('403', '403', undefined, undefined, {
        status: 403,
        statusText: 'Forbidden',
        headers: {},
        config: { headers: new AxiosHeaders() },
        data: { code: 'pantry_disabled', detail: 'off' },
      }),
    )
    renderPage()
    expect(await screen.findByText('The Pantry is turned off for this household')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Turn it on in Account' })).toHaveAttribute('href', '/account')
  })
})
