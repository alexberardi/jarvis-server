import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { InboxItem } from '@/api/inbox'
import InboxCardActions from './InboxCardActions'

const api = vi.hoisted(() => ({ sendCallback: vi.fn(), callbackStatus: vi.fn() }))
vi.mock('@/api/callbacks', async (orig) => ({ ...(await orig<typeof import('@/api/callbacks')>()), ...api }))
const toastError = vi.hoisted(() => vi.fn())
vi.mock('sonner', async (orig) => ({ ...(await orig<typeof import('sonner')>()), toast: { error: toastError, success: vi.fn() } }))

// A phone-call confirm card, as cc/phone/cards.go writes it: a tel and a multiline field, a
// confirm that carries both and a cancel that carries neither, on the server plane.
const callCard = (meta: Record<string, unknown> = {}): InboxItem => ({
  id: 'c1',
  user_id: 7,
  household_id: 'h1',
  title: 'Call the dentist?',
  summary: '',
  body: 'Jarvis will call to book a cleaning.',
  category: 'confirmation',
  source_service: 'cc',
  is_read: true,
  created_at: '2026-11-15T10:00:00',
  metadata: {
    editor_schema: 2,
    editable_fields: [
      { label: 'Phone number', initial: '+1 (312) 847-1928', data_key: 'dialed_number', input_type: 'tel' },
      { label: 'Details', initial: 'Book a cleaning', data_key: 'details', input_type: 'multiline' },
    ],
    interactive_elements: [
      { id: 'go', label: 'Call', command: 'make_phone_call', callback: 'confirm_call', target: 'server', data: { plan_id: 'p1', dialed_number: '', details: '' } },
      { id: 'no', label: 'Cancel', command: 'make_phone_call', callback: 'cancel_call', target: 'server', data: { plan_id: 'p1' } },
    ],
    ...meta,
  },
})

const renderCard = (item: InboxItem, refresh = vi.fn().mockResolvedValue(item)) => render(<InboxCardActions item={item} refresh={refresh} />)

describe('InboxCardActions', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.sendCallback.mockResolvedValue({ id: 'job1', status: 'pending' })
    api.callbackStatus.mockResolvedValue({ id: 'job1', status: 'completed', navigation_type: 'new_notification', completed_at: null, error_message: null, context_data: null })
  })

  it('sends the edited fields with a server-plane tap and ticks it when the job completes', async () => {
    renderCard(callCard())
    fireEvent.change(screen.getByLabelText('Phone number'), { target: { value: '+1 (312) 555-0142' } })
    fireEvent.click(screen.getByRole('button', { name: 'Call' }))
    await screen.findByRole('button', { name: /Call \(sent\)/ })
    expect(api.sendCallback).toHaveBeenCalledWith({
      command_name: 'make_phone_call',
      callback_name: 'confirm_call',
      data: { plan_id: 'p1', dialed_number: '+1 (312) 555-0142', details: 'Book a cleaning' },
      navigation_type: 'new_notification',
      household_id: 'h1',
    })
    expect(screen.getByRole('button', { name: /Call \(sent\)/ })).toBeDisabled()
  })

  it('leaves elements without a field untouched and blocks a tap on an empty required field', async () => {
    renderCard(callCard())
    fireEvent.change(screen.getByLabelText('Phone number'), { target: { value: '  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Call' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Phone number is required')
    expect(api.sendCallback).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(api.sendCallback).toHaveBeenCalledTimes(1))
    expect(api.sendCallback.mock.calls[0][0].data).toEqual({ plan_id: 'p1' })
  })

  it('sends a node-plane tap to the item’s node and shows a failed job', async () => {
    api.callbackStatus.mockResolvedValue({ id: 'job1', status: 'failed', navigation_type: 'new_notification', completed_at: null, error_message: 'Node offline', context_data: null })
    renderCard(callCard({ node_id: 'node-h1', editable_fields: undefined, editor_schema: undefined, interactive_elements: [{ label: 'Tom Hanks', sublabel: 'as Forrest', kind: 'actor', command: 'movies', callback: 'actor', data: { id: 31 } }] }))
    fireEvent.click(screen.getByRole('button', { name: 'Tom Hanks · as Forrest' }))
    await waitFor(() => expect(toastError).toHaveBeenCalledWith('Node offline'))
    expect(api.sendCallback.mock.calls[0][0]).toMatchObject({ target_node_id: 'node-h1', command_name: 'movies' })
    expect(api.sendCallback.mock.calls[0][0]).not.toHaveProperty('household_id')
    expect(screen.getByRole('button', { name: 'Tom Hanks · as Forrest' })).toBeEnabled()
  })

  it('fails closed on a field type it cannot render', () => {
    renderCard(callCard({ editable_fields: [{ label: 'When', initial: '', data_key: 'when', input_type: 'slider' }] }))
    expect(screen.getByRole('note')).toHaveTextContent("can't show yet")
    expect(screen.getByRole('button', { name: 'Call' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
  })

  it('turns an expired card off, and one the server calls expired', async () => {
    const { unmount } = renderCard(callCard({ expires_at: '2020-01-01T00:00:00Z' }))
    expect(screen.getByRole('status')).toHaveTextContent('expired')
    expect(screen.getByRole('button', { name: 'Call' })).toBeDisabled()
    unmount()

    api.sendCallback.mockRejectedValue({ response: { status: 410, data: { detail: 'Plan expired' } } })
    renderCard(callCard())
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(await screen.findByRole('status')).toHaveTextContent('expired')
    expect(toastError).not.toHaveBeenCalled()
  })

  it('renders nothing for an item without fields or elements', () => {
    const { container } = renderCard({ ...callCard(), metadata: { sources: [] } })
    expect(container).toBeEmptyDOMElement()
  })
})
