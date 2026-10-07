import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { AxiosError, AxiosHeaders } from 'axios'
import { AuthProvider } from '@/auth/AuthContext'
import { clearSetupToken, getSetupToken, setSetupToken } from '@/auth/setupToken'
import * as authApi from '@/api/auth'
import AccountStep from './AccountStep'

vi.mock('@/api/auth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/auth')>()
  return { ...actual, setup: vi.fn(), getSetupState: vi.fn() }
})

const setupMock = vi.mocked(authApi.setup)
const stateMock = vi.mocked(authApi.getSetupState)

const TOKEN_FILE = '/home/op/.jarvisd/setup-token'

function httpError(status: number, detail: string): AxiosError {
  const config = { headers: new AxiosHeaders() }
  return new AxiosError('fail', 'ERR_BAD_REQUEST', config, null, {
    status,
    statusText: '',
    headers: {},
    config,
    data: { detail },
  })
}

const tokens = {
  access_token: 'acc',
  refresh_token: 'ref',
  token_type: 'bearer' as const,
  user: { id: 1, email: 'admin@example.com', is_superuser: true },
}

function fillForm() {
  fireEvent.change(screen.getByLabelText('Display Name'), { target: { value: 'Admin' } })
  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'admin@example.com' } })
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'password123' } })
  fireEvent.change(screen.getByLabelText('Confirm Password'), { target: { value: 'password123' } })
}

function renderStep(onCreated = vi.fn()) {
  render(
    <AuthProvider>
      <AccountStep onCreated={onCreated} />
    </AuthProvider>,
  )
  return onCreated
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  clearSetupToken()
  setupMock.mockReset()
  stateMock.mockReset()
  stateMock.mockResolvedValue({
    needs_superuser: true,
    setup_token_required: true,
    version: 'dev',
    superuser: false,
    setup_token_file: TOKEN_FILE,
  })
})

afterEach(() => {
  clearSetupToken()
})

describe('AccountStep setup token', () => {
  it('sends the token captured from the link and stores the session namespaced (O5)', async () => {
    setSetupToken('from-link')
    setupMock.mockResolvedValue(tokens)
    const onCreated = renderStep()

    await waitFor(() => expect(stateMock).toHaveBeenCalled())
    // A token came with the link: no paste field.
    expect(screen.queryByLabelText('Setup token')).toBeNull()

    fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))

    await waitFor(() => expect(onCreated).toHaveBeenCalled())
    expect(setupMock).toHaveBeenCalledWith('admin@example.com', 'password123', 'Admin', 'from-link')
    expect(localStorage.getItem('jarvis-admin:access_token')).toBe('acc')
    expect(localStorage.getItem('jarvis-admin:refresh_token')).toBe('ref')
    expect(localStorage.getItem('access_token')).toBeNull()
    // Used once, then forgotten.
    expect(getSetupToken()).toBeNull()
    expect(screen.getByText('Superuser account created')).toBeInTheDocument()
  })

  it('asks for the token up front, naming the file, when the link had none', async () => {
    setupMock.mockResolvedValue(tokens)
    renderStep()

    const input = await screen.findByLabelText('Setup token')
    expect(screen.getByText(TOKEN_FILE)).toBeInTheDocument()

    fillForm()
    const submit = screen.getByRole('button', { name: 'Create Superuser Account' })
    expect(submit).toBeDisabled()

    fireEvent.change(input, { target: { value: '  pasted-token  ' } })
    fireEvent.click(submit)

    await waitFor(() => expect(setupMock).toHaveBeenCalled())
    expect(setupMock.mock.calls[0][3]).toBe('pasted-token')
  })

  it('on 403 asks the operator to paste the token, then retries with it', async () => {
    setSetupToken('stale')
    setupMock.mockRejectedValueOnce(httpError(403, 'Invalid setup token')).mockResolvedValueOnce(tokens)
    const onCreated = renderStep()
    await waitFor(() => expect(stateMock).toHaveBeenCalled())

    fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('not accepted')
    expect(screen.getByText(TOKEN_FILE)).toBeInTheDocument()
    expect(getSetupToken()).toBeNull()

    fireEvent.change(screen.getByLabelText('Setup token'), { target: { value: 'fresh' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))

    await waitFor(() => expect(onCreated).toHaveBeenCalled())
    expect(setupMock.mock.calls[1][3]).toBe('fresh')
  })

  it('on 401 says a token is required', async () => {
    setSetupToken('x')
    setupMock.mockRejectedValueOnce(httpError(401, 'Setup token required'))
    renderStep()
    await waitFor(() => expect(stateMock).toHaveBeenCalled())
    fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('needs its setup token')
    expect(screen.getByLabelText('Setup token')).toBeInTheDocument()
  })

  it('shows other errors from {detail} without asking for a token', async () => {
    setSetupToken('ok')
    setupMock.mockRejectedValueOnce(httpError(409, 'Setup already completed'))
    renderStep()
    await waitFor(() => expect(stateMock).toHaveBeenCalled())
    fillForm()
    fireEvent.click(screen.getByRole('button', { name: 'Create Superuser Account' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Setup already completed')
    expect(screen.queryByLabelText('Setup token')).toBeNull()
  })
})
