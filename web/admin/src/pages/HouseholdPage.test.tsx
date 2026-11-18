import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import HouseholdPage from './HouseholdPage'

const api = vi.hoisted(() => ({
  listMembers: vi.fn(), setMemberRole: vi.fn(), removeMember: vi.fn(), renameHousehold: vi.fn(), leaveHousehold: vi.fn(),
  listInvites: vi.fn(), createInvite: vi.fn(), revokeInvite: vi.fn(), joinHousehold: vi.fn(),
  getHouseholdSettings: vi.fn(), putHouseholdSetting: vi.fn(),
}))
vi.mock('@/api/household', async (orig) => ({ ...(await orig<typeof import('@/api/household')>()), ...api }))
const auth = vi.hoisted(() => ({
  role: 'admin', households: [] as { id: string; name: string; role: string }[], reloadHouseholds: vi.fn(), switchHousehold: vi.fn(),
}))
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({ activeHouseholdId: 'h1', households: auth.households, state: { user: { id: 7 } }, reloadHouseholds: auth.reloadHouseholds, switchHousehold: auth.switchHousehold }),
}))

const members = [
  { user_id: 7, username: 'sam', display_name: 'Sam', email: 'sam@example.com', role: 'admin', created_at: '' },
  { user_id: 8, username: 'kim', display_name: null, email: 'kim@example.com', role: 'member', created_at: '' },
]
const invite = { id: 3, code: 'ABCD-EFGH', default_role: 'member', max_uses: 1, use_count: 0, expires_at: '2026-11-24T10:00:00', revoked: false, created_at: '' }

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <HouseholdPage />
    </QueryClientProvider>,
  )
}

describe('HouseholdPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    auth.households = [{ id: 'h1', name: 'Home', role: 'admin' }]
    api.listMembers.mockResolvedValue(members)
    api.listInvites.mockResolvedValue([
      invite,
      { ...invite, id: 4, code: 'OLD0-CODE', revoked: true },
      { ...invite, id: 6, code: 'USED-UP00', use_count: 1 },
      { ...invite, id: 7, code: 'EXPI-RED0', expires_at: '2020-01-01T00:00:00' },
    ])
    api.getHouseholdSettings.mockResolvedValue({})
    for (const f of [api.setMemberRole, api.removeMember, api.renameHousehold, api.leaveHousehold, api.revokeInvite, auth.reloadHouseholds, auth.switchHousehold]) f.mockResolvedValue(undefined)
  })

  it('an admin renames, changes a role and removes someone, but not themself', async () => {
    renderPage()
    const name = screen.getByLabelText('Household name')
    fireEvent.change(name, { target: { value: 'The Avery House' } })
    fireEvent.click(within(screen.getByRole('region', { name: 'Name' })).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(api.renameHousehold).toHaveBeenCalledWith('h1', 'The Avery House'))
    expect(auth.reloadHouseholds).toHaveBeenCalled()

    const list = await screen.findByRole('list', { name: 'Members' })
    expect(within(list).getByText('(you)')).toBeInTheDocument()
    expect(within(list).queryByLabelText('Role of Sam')).toBeNull()
    fireEvent.change(within(list).getByLabelText('Role of kim'), { target: { value: 'power_user' } })
    await vi.waitFor(() => expect(api.setMemberRole).toHaveBeenCalledWith('h1', 8, 'power_user'))

    fireEvent.click(within(list).getByRole('button', { name: 'Remove kim' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    await vi.waitFor(() => expect(api.removeMember).toHaveBeenCalledWith('h1', 8))
  })

  it('makes an invite code, shows it to copy, and lists only open ones', async () => {
    api.createInvite.mockResolvedValue({ ...invite, id: 5, code: 'NEW1-CODE', default_role: 'power_user', max_uses: null })
    renderPage()
    const invites = screen.getByRole('region', { name: 'Invites' })
    expect(await within(invites).findByText('ABCD-EFGH')).toBeInTheDocument()
    for (const gone of ['OLD0-CODE', 'USED-UP00', 'EXPI-RED0']) expect(within(invites).queryByText(gone)).toBeNull()
    fireEvent.change(within(invites).getByLabelText('Joins as'), { target: { value: 'power_user' } })
    fireEvent.change(within(invites).getByLabelText('Can be used'), { target: { value: '' } })
    fireEvent.click(within(invites).getByRole('button', { name: 'New invite code' }))
    expect(await within(invites).findByRole('status')).toHaveTextContent('NEW1-CODE')
    expect(api.createInvite).toHaveBeenCalledWith('h1', { default_role: 'power_user', expires_in_days: 7, max_uses: null })

    fireEvent.click(within(invites).getByRole('button', { name: 'Revoke ABCD-EFGH' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Revoke' }))
    await vi.waitFor(() => expect(api.revokeInvite).toHaveBeenCalledWith('h1', 3))
  })

  it('a member sees the people but no controls or codes, and can’t leave an only household', async () => {
    auth.households = [{ id: 'h1', name: 'Home', role: 'member' }]
    renderPage()
    const list = await screen.findByRole('list', { name: 'Members' })
    expect(within(list).getByText('Admin')).toBeInTheDocument()
    expect(within(list).queryByRole('button')).toBeNull()
    expect(screen.queryByRole('region', { name: 'Name' })).toBeNull()
    // As on mobile, codes are only for those who may invite.
    expect(screen.queryByRole('region', { name: 'Invites' })).toBeNull()
    expect(api.listInvites).not.toHaveBeenCalled()
    expect(screen.queryByRole('region', { name: 'Leave this household' })).toBeNull()
  })

  it('joins another household with a code and switches to it; a bad code says why', async () => {
    api.joinHousehold.mockRejectedValueOnce(Object.assign(new Error('x'), { isAxiosError: true, response: { status: 404, data: { detail: 'Invite code not found' } } }))
    api.joinHousehold.mockResolvedValueOnce({ household_id: 'h9', household_name: 'Grandma’s', role: 'member' })
    renderPage()
    const join = screen.getByRole('region', { name: 'Join a household' })
    fireEvent.change(within(join).getByLabelText('Invite code'), { target: { value: 'NOPE' } })
    fireEvent.click(within(join).getByRole('button', { name: 'Join' }))
    expect(await within(join).findByText('Invite code not found')).toBeInTheDocument()
    fireEvent.change(within(join).getByLabelText('Invite code'), { target: { value: ' WXYZ-1234 ' } })
    fireEvent.click(within(join).getByRole('button', { name: 'Join' }))
    await vi.waitFor(() => expect(auth.switchHousehold).toHaveBeenCalledWith('h9'))
    expect(api.joinHousehold).toHaveBeenLastCalledWith('WXYZ-1234')
  })

  it('with two households, leaving switches to the other one', async () => {
    auth.households = [{ id: 'h1', name: 'Home', role: 'member' }, { id: 'h2', name: 'Cabin', role: 'admin' }]
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: 'Leave Home' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Leave' }))
    await vi.waitFor(() => expect(auth.switchHousehold).toHaveBeenCalledWith('h2'))
    expect(api.leaveHousehold).toHaveBeenCalledWith('h1')
  })
})
