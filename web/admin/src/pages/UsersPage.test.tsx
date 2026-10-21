import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, within } from '@testing-library/react'
import { fetchAllUsers, type AdminUser } from '@/api/admin'
import { renderPage } from '@/test/renderPage'
import UsersPage from './UsersPage'

vi.mock('@/api/admin', async (orig) => ({
  ...(await orig<typeof import('@/api/admin')>()),
  fetchAllUsers: vi.fn(),
  issueTempPassword: vi.fn(),
}))

const user: AdminUser = {
  id: 7,
  email: 'sam@example.com',
  username: 'sam',
  display_name: 'Sam',
  is_active: true,
  is_superuser: false,
  must_change_password: false,
  created_at: '2026-10-01T00:00:00Z',
  updated_at: null,
  households: [{ household_id: 'hh', household_name: 'Home', role: 'member' }],
} as AdminUser

describe('UsersPage', () => {
  it('lists the users, each with a reset that asks first', async () => {
    vi.mocked(fetchAllUsers).mockResolvedValue([user])
    renderPage(<UsersPage />)
    const row = await screen.findByTestId('user-7')
    expect(row).toHaveTextContent('Home (member)')
    fireEvent.click(within(row).getByRole('button', { name: 'Reset password' }))
    fireEvent.change(within(row).getByPlaceholderText(/Custom temp password/), { target: { value: 'short' } })
    expect(within(row).getByText('Use at least 8 characters')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Reset password' })).toBeDisabled()
    fireEvent.click(within(row).getByRole('button', { name: 'Cancel' }))
    expect(within(row).getByRole('button', { name: 'Reset password' })).toBeEnabled()
  })

  it('shows the error with a retry', async () => {
    vi.mocked(fetchAllUsers).mockRejectedValueOnce(new Error('the database is busy'))
    renderPage(<UsersPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load users")
    vi.mocked(fetchAllUsers).mockResolvedValue([user])
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByTestId('user-7')).toBeInTheDocument()
  })

  it('says so when there are no users', async () => {
    vi.mocked(fetchAllUsers).mockResolvedValue([])
    renderPage(<UsersPage />)
    expect(await screen.findByText('No users yet')).toBeInTheDocument()
  })
})
