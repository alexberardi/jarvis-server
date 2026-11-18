import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import SignalAutomations from './SignalAutomations'

const api = vi.hoisted(() => ({ listSignalAutomations: vi.fn(), putSignalAutomation: vi.fn() }))
vi.mock('@/api/household', async (orig) => ({ ...(await orig<typeof import('@/api/household')>()), ...api }))

const LEFT = { kind: 'presence.left', label: 'I leave home', description: 'Your phone leaves the home area.', example: 'Lock the front door', observed: true }
const ARRIVE = {
  kind: 'presence.seen',
  label: 'I arrive home',
  description: 'Your phone enters the home area.',
  example: 'Turn on the lights',
  observed: false,
}

function renderCard(canEdit: boolean) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <SignalAutomations hh="h1" canEdit={canEdit} />
    </QueryClientProvider>,
  )
}

describe('signal automations', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.listSignalAutomations.mockResolvedValue([
      { ...LEFT, instruction: '', enabled: false, delivery: 'notification' },
      { ...ARRIVE, instruction: 'Turn on the porch light', enabled: true, delivery: 'automatic' },
    ])
    api.putSignalAutomation.mockImplementation(async (_h, _k, r) => r)
  })

  it('an admin writes a rule: typing switches it on, then it saves', async () => {
    renderCard(true)
    const form = await screen.findByRole('form', { name: 'I leave home' })
    const on = within(form).getByRole('switch', { name: 'On' })
    expect(on).toBeDisabled()
    expect(within(form).getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.change(within(form).getByRole('textbox'), { target: { value: '  Lock the front door ' } })
    expect(on).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(within(form).getByLabelText('Just do it'))
    fireEvent.click(within(form).getByRole('button', { name: 'Save' }))
    await vi.waitFor(() =>
      expect(api.putSignalAutomation).toHaveBeenCalledWith('h1', 'presence.left', { instruction: 'Lock the front door', enabled: true, delivery: 'automatic' }),
    )
    expect(await within(form).findByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('emptying a saved rule offers Clear and switches it off', async () => {
    renderCard(true)
    const form = await screen.findByRole('form', { name: 'I arrive home' })
    expect(within(form).getByText('Hasn’t happened yet')).toBeInTheDocument()
    fireEvent.change(within(form).getByRole('textbox'), { target: { value: '' } })
    expect(within(form).getByRole('switch', { name: 'On' })).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(within(form).getByRole('button', { name: 'Clear' }))
    await vi.waitFor(() =>
      expect(api.putSignalAutomation).toHaveBeenCalledWith('h1', 'presence.seen', { instruction: '', enabled: false, delivery: 'automatic' }),
    )
  })

  it('a member reads the rules without controls', async () => {
    renderCard(false)
    expect(await screen.findByText(/Turn on the porch light/)).toBeInTheDocument()
    expect(screen.getByText(/runs by itself/)).toBeInTheDocument()
    expect(screen.getByText('Nothing set.')).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
  })
})
