import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider } from '@/auth/AuthContext'
import type { NodeLiveness } from '@/api/nodes'
import AddonsPage from './AddonsPage'

const api = vi.hoisted(() => ({ getNodeLiveness: vi.fn(), getPairings: vi.fn(), getSystemInfo: vi.fn(), signOutDevice: vi.fn() }))
const chatApi = vi.hoisted(() => ({ getThisDevice: vi.fn() }))
const households = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/nodes', async (orig) => ({ ...(await orig<typeof import('@/api/nodes')>()), ...api }))
vi.mock('@/api/chat', async (orig) => ({ ...(await orig<typeof import('@/api/chat')>()), ...chatApi }))
vi.mock('@/api/system', async (orig) => ({ ...(await orig<typeof import('@/api/system')>()), getSystemInfo: api.getSystemInfo }))
vi.mock('@/api/auth', async (orig) => ({
  ...(await orig<typeof import('@/api/auth')>()),
  listHouseholds: households.list,
}))

const node = (node_id: string, room: string, online: boolean, ext?: string, hh = 'h1'): NodeLiveness => ({
  node_id,
  room,
  household_id: hh,
  online,
  last_seen: '2026-10-13T10:00:00',
  last_seen_version: null,
  is_busy: false,
  node_kind: 'desktop',
  capabilities: ext ? { browser_extension: ext } : {},
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <MemoryRouter>
          <AddonsPage />
        </MemoryRouter>
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.clearAllMocks()
  localStorage.setItem('jarvis-admin:access_token', 'a.e30.s')
  localStorage.setItem('jarvis-admin:refresh_token', 'r')
  localStorage.setItem('jarvis-admin:user', JSON.stringify({ id: 7, email: 'm@example.com', is_superuser: false }))
  api.getPairings.mockResolvedValue({ waiting: 0, households: [{ id: 'h1', name: 'Home', role: 'member' }] })
  households.list.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'member' }])
  chatApi.getThisDevice.mockResolvedValue({ node_id: null, reason: 'no_match' })
  api.signOutDevice.mockResolvedValue(undefined)
  api.getNodeLiveness.mockResolvedValue([
    { ...node('a', 'office', true, 'connected'), owner_user_id: 7 },
    node('b', 'laptop', false, 'disconnected'),
    { ...node('pi', 'kitchen', true), node_kind: null },
    node('x', 'elsewhere', true, 'connected', 'h9'),
  ])
})

describe('AddonsPage', () => {
  it("lists the user's desktop agents with online and extension state", async () => {
    renderPage()
    const list = await screen.findByRole('list', { name: 'Your devices' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('office')
    expect(rows[0]).toHaveTextContent('Online')
    expect(rows[0]).toHaveTextContent('Browser extension connected')
    expect(rows[1]).toHaveTextContent('laptop')
    expect(rows[1]).toHaveTextContent('Offline')
    expect(rows[1]).toHaveTextContent('Browser extension not connected')
    expect(screen.queryByText('kitchen')).toBeNull()
    expect(screen.queryByText('elsewhere')).toBeNull()
  })

  it('offers Start only for an agent that is known but offline', async () => {
    renderPage()
    const list = await screen.findByRole('list', { name: 'Your devices' })
    const [online, offline] = within(list).getAllByRole('listitem')
    expect(within(online).queryByRole('link', { name: 'Start' })).toBeNull()
    expect(within(offline).getByRole('link', { name: 'Start' })).toHaveAttribute('href', 'jarvis-agent://start')
  })

  it('offers the verified download and links the extension without detecting anything, and never asks for system info as a member', async () => {
    renderPage()
    await screen.findByRole('list', { name: 'Your devices' })
    // The agent comes through jarvisd (install plan 3.1), with a picker for other computers.
    expect(screen.getByRole('button', { name: /^Download/ })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: /Another computer|Choose your computer/ })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Install guide/ })).toHaveAttribute('href', expect.stringContaining('github.com'))
    expect(screen.getByRole('link', { name: /How browser control works/ })).toBeInTheDocument()
    expect(screen.getByText('Phone app')).toBeInTheDocument()
    expect(await screen.findByText(/No computers waiting/)).toBeInTheDocument()
    expect(api.getSystemInfo).not.toHaveBeenCalled()
  })

  it('says how to turn on website access next to the store links, for both browsers, with the docs', async () => {
    renderPage()
    const section = (await screen.findByRole('heading', { name: 'Browser extension' })).closest('section')!
    expect(section).toHaveTextContent('After installing, turn on website access')
    expect(section).toHaveTextContent('about:addons')
    expect(section).toHaveTextContent('"Access your data for all websites"')
    expect(section).toHaveTextContent('Site access to "On all sites"')
    expect(section).toHaveTextContent('macro recordings capture only page loads')
    expect(within(section).getByRole('link', { name: /More about website access/ })).toHaveAttribute(
      'href',
      'https://docs.jarvisautomation.dev/clients/desktop-agent/#turn-on-website-access',
    )
  })

  const withAccess = (caps: Record<string, string>) =>
    api.getNodeLiveness.mockResolvedValue([
      { ...node('a', 'office', true, 'connected'), capabilities: { browser_extension: 'connected', ...caps } },
    ])
  const row = async () => within(await screen.findByRole('list', { name: 'Your devices' })).getAllByRole('listitem')[0]

  it("warns when a device's website access is off, with the step for that browser", async () => {
    withAccess({ browser_website_access: 'off', browser_website_access_off_in: 'Firefox' })
    renderPage()
    const r = await row()
    expect(within(r).getByTestId('extension-state')).toHaveTextContent('Website access is off')
    const warn = within(r).getByTestId('website-access-off')
    expect(warn).toHaveTextContent('about:addons')
    expect(warn).not.toHaveTextContent('On all sites')
    expect(within(warn).getByRole('link', { name: /More about website access/ })).toBeInTheDocument()
  })

  it('gives the Chrome step for a Chromium browser', async () => {
    withAccess({ browser_website_access: 'off', browser_website_access_off_in: 'Google Chrome' })
    renderPage()
    const warn = within(await row()).getByTestId('website-access-off')
    expect(warn).toHaveTextContent('On all sites')
    expect(warn).not.toHaveTextContent('about:addons')
  })

  it('gives both steps when an older agent says off without naming the browser', async () => {
    withAccess({ browser_website_access: 'off' })
    renderPage()
    const warn = within(await row()).getByTestId('website-access-off')
    expect(warn).toHaveTextContent('about:addons')
    expect(warn).toHaveTextContent('On all sites')
  })

  it('just says connected when website access is on or not reported', async () => {
    withAccess({ browser_website_access: 'on' })
    renderPage()
    const r = await row()
    expect(within(r).getByTestId('extension-state')).toHaveTextContent('Browser extension connected')
    expect(within(r).queryByTestId('website-access-off')).toBeNull()
  })

  it('says nothing about website access when the extension did not report it', async () => {
    withAccess({})
    renderPage()
    const r = await row()
    expect(within(r).getByTestId('extension-state')).toHaveTextContent('Browser extension connected')
    expect(within(r).queryByTestId('website-access-off')).toBeNull()
  })

  it('says so when there are no computers yet', async () => {
    api.getNodeLiveness.mockResolvedValue([])
    renderPage()
    expect(await screen.findByText(/No computers yet/)).toBeInTheDocument()
  })

  it('names this computer when the server matched one of your agents', async () => {
    chatApi.getThisDevice.mockResolvedValue({ node_id: 'a', household_id: 'h1', online: true })
    renderPage()
    expect(await screen.findByTestId('this-computer')).toHaveTextContent('This computer is office: the agent is running.')
    const list = await screen.findByRole('list', { name: 'Your devices' })
    expect(within(within(list).getAllByRole('listitem')[0]).getByText('This computer')).toBeInTheDocument()
  })

  it("says it can't tell, rather than guessing", async () => {
    chatApi.getThisDevice.mockResolvedValue({ node_id: null, reason: 'ambiguous' })
    renderPage()
    expect(await screen.findByTestId('this-computer')).toHaveTextContent("We can't tell which of your computers this is")
  })

  it('points settings to the mobile app on every device', async () => {
    renderPage()
    const list = await screen.findByRole('list', { name: 'Your devices' })
    for (const row of within(list).getAllByRole('listitem')) {
      expect(row).toHaveTextContent('Settings for this computer are managed in the Jarvis Automation mobile app (admins).')
    }
  })

  it('lets the member who paired a device sign it out, after confirming', async () => {
    renderPage()
    const list = await screen.findByRole('list', { name: 'Your devices' })
    const [mine, theirs] = within(list).getAllByRole('listitem')
    expect(within(theirs).queryByRole('button', { name: 'Sign out' })).toBeNull()
    fireEvent.click(within(mine).getByRole('button', { name: 'Sign out' }))
    const confirm = within(mine).getByRole('group', { name: 'Sign out office' })
    expect(confirm).toHaveTextContent('settings on that computer are kept')
    expect(api.signOutDevice).not.toHaveBeenCalled()
    fireEvent.click(within(confirm).getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(api.signOutDevice).toHaveBeenCalledWith('a'))
    await waitFor(() => expect(api.getNodeLiveness.mock.calls.length).toBeGreaterThan(1))
  })

  it("lets a household admin sign out anyone's device", async () => {
    households.list.mockResolvedValue([{ id: 'h1', name: 'Home', role: 'admin' }])
    renderPage()
    const list = await screen.findByRole('list', { name: 'Your devices' })
    await waitFor(() => expect(within(list).getAllByRole('button', { name: 'Sign out' })).toHaveLength(2))
  })

  it("says when your devices couldn't load, and loads them again on Try again", async () => {
    api.getNodeLiveness.mockRejectedValueOnce(new Error('connection refused'))
    renderPage()
    const section = (await screen.findByRole('heading', { name: 'Your devices' })).closest('section')!
    expect(await within(section).findByRole('alert')).toHaveTextContent("Couldn't load your devices")
    fireEvent.click(within(section).getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('list', { name: 'Your devices' })).toBeInTheDocument()
  })
})

