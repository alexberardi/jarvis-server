import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { StoreDetail } from '@/api/store'
import StorePackagePage from './StorePackagePage'

const store = vi.hoisted(() => ({ getStorePackage: vi.fn(), requestInstall: vi.fn(), pollInstall: vi.fn() }))
const settings = vi.hoisted(() => ({ getHouseholdSettings: vi.fn(), putHouseholdSetting: vi.fn() }))
const nodes = vi.hoisted(() => ({ getNodeLiveness: vi.fn() }))
const households = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/store', async (orig) => ({ ...(await orig<typeof import('@/api/store')>()), ...store }))
vi.mock('@/api/household', () => settings)
vi.mock('@/api/nodes', async (orig) => ({ ...(await orig<typeof import('@/api/nodes')>()), ...nodes }))
vi.mock('@/api/auth', async (orig) => ({ ...(await orig<typeof import('@/api/auth')>()), listHouseholds: households.list }))

const ha: StoreDetail = {
  command_name: 'home-assistant',
  display_name: 'Home Assistant',
  description: 'Full Home Assistant integration.',
  author: { github: 'acme', display_name: 'Acme' },
  latest_version: '1.0.2',
  categories: ['smart-home'],
  verified: false,
  github_repo_url: 'https://github.com/acme/ha',
  package_type: 'bundle',
  platforms: ['darwin', 'linux'],
  license: 'Apache-2.0',
  install_count: 17,
  danger_rating: 4,
  apt_packages: ['ffmpeg'],
  targets: [],
  components: [
    { type: 'command', name: 'control_device' },
    { type: 'agent', name: 'home_assistant' },
  ],
  security_report: { summary: 'Talks to devices on the network.', concerns: ['Hardcoded endpoint URLs'], recommendation: 'flag_for_review' },
}

function renderPage(path = '/admin/store/home-assistant') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/admin/store/:name" element={<StorePackagePage />} />
            <Route path="/admin/store" element={<p>the store</p>} />
          </Routes>
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', 'a.eyJob3VzZWhvbGRfaWQiOiJoMSJ9.s') // {"household_id":"h1"}
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'a@example.com', is_superuser: true }))
  households.list.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'admin' }])
  store.getStorePackage.mockResolvedValue(ha)
  settings.getHouseholdSettings.mockResolvedValue({ 'pantry.default_bundle': '' })
  settings.putHouseholdSetting.mockResolvedValue(undefined)
  nodes.getNodeLiveness.mockResolvedValue([])
})

describe('StorePackagePage', () => {
  it('shows the package in full, without asking Pantry for an install', async () => {
    renderPage()
    expect(await screen.findByRole('heading', { level: 1, name: 'Home Assistant' })).toBeInTheDocument()
    expect(store.getStorePackage).toHaveBeenCalledWith('h1', 'home-assistant')
    const about = screen.getByRole('region', { name: 'About this package' })
    expect(within(about).getByText('Pi nodes only')).toBeInTheDocument() // apt packages
    expect(within(about).getByText('Apache-2.0')).toBeInTheDocument()
    expect(within(about).getByRole('link', { name: /github.com\/acme\/ha/ })).toHaveAttribute('href', 'https://github.com/acme/ha')
    expect(screen.getByText('ffmpeg')).toBeInTheDocument()
    const parts = screen.getByRole('region', { name: 'What it adds' })
    expect(within(parts).getByText('home_assistant')).toBeInTheDocument()
    expect(within(parts).getByText('Background agent')).toBeInTheDocument()
    const security = screen.getByRole('region', { name: 'Security review' })
    expect(within(security).getByText('Risky')).toBeInTheDocument()
    expect(within(security).getByText('Hardcoded endpoint URLs')).toBeInTheDocument()
  })

  it('adds it to the bundle and opens Install, which asks for the commit', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('switch', { name: 'Home Assistant in the default bundle' }))
    await waitFor(() => expect(settings.putHouseholdSetting).toHaveBeenCalledWith('h1', 'pantry.default_bundle', 'home-assistant'))
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    await screen.findByRole('dialog')
    await waitFor(() => expect(store.getStorePackage).toHaveBeenCalledWith('h1', 'home-assistant', true))
  })

  it('goes back to the Store', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Back to the Store' }))
    expect(await screen.findByText('the store')).toBeInTheDocument()
  })

  it('says so when Pantry has no such package', async () => {
    store.getStorePackage.mockRejectedValue(new Error('The Pantry has no package nope'))
    renderPage('/admin/store/nope')
    expect(await screen.findByText("Couldn't load nope")).toBeInTheDocument()
  })
})
