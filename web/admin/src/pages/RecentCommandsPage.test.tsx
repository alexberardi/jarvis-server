import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { callSummary, toolCalls } from '@/api/transcripts'
import RecentCommandsPage from './RecentCommandsPage'

const api = vi.hoisted(() => ({ listRecentTranscripts: vi.fn() }))
vi.mock('@/api/transcripts', async (orig) => ({ ...(await orig<typeof import('@/api/transcripts')>()), ...api }))

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RecentCommandsPage />
    </QueryClientProvider>,
  )
}

describe('RecentCommandsPage', () => {
  beforeEach(() => vi.clearAllMocks())

  it('lists what was said and what ran; opening one shows every call and the reply', async () => {
    api.listRecentTranscripts.mockResolvedValue([
      { id: 2, household_id: 'h1', user_message: 'turn on the kitchen lights and set a timer', assistant_message: 'Done. Timer set for 10 minutes.', created_at: '2026-11-17T10:00:00',
        // As cc stores them: OpenAI-shaped, arguments a JSON string.
        tool_calls: [
          { id: 'c1', type: 'function', function: { name: 'control_device', arguments: '{"device_name": "Kitchen Lights", "action": "turn_on"}' }, failure_message: null },
          { id: 'c2', type: 'function', function: { name: 'set_timer', arguments: '{"minutes": 10}' }, failure_message: null },
        ] },
      { id: 1, household_id: 'h1', user_message: 'tell me a joke', assistant_message: 'Why did…', created_at: '2026-11-17T09:00:00', tool_calls: null },
    ])
    renderPage()
    const items = await screen.findAllByRole('listitem')
    expect(items[0]).toHaveTextContent('“turn on the kitchen lights and set a timer”')
    expect(items[0]).toHaveTextContent('→ control_device(device_name=Kitchen Lights, action=turn_on) +1')
    // No node command recorded: the reply says what happened.
    expect(items[1]).toHaveTextContent('Why did…')
    expect(api.listRecentTranscripts).toHaveBeenCalledWith(50)

    fireEvent.click(within(items[0]).getByText('“turn on the kitchen lights and set a timer”'))
    expect(within(items[0]).getByText('set_timer')).toBeVisible()
    expect(within(items[0]).getByText('10')).toBeVisible()
    expect(within(items[0]).getByText('Done. Timer set for 10 minutes.')).toBeVisible()
  })

  it('says when there is nothing yet', async () => {
    api.listRecentTranscripts.mockResolvedValue([])
    renderPage()
    expect(await screen.findByText('Nothing yet')).toBeInTheDocument()
  })

  it('summarises a call, cutting long values', () => {
    expect(callSummary({ name: 'remember', arguments: { content: 'The e2e code word is marmalade', category: 'fact' } })).toBe('remember(content=The e2e code word i…, category=fact)')
    expect(callSummary({ name: 'tell_joke', arguments: {} })).toBe('tell_joke')
  })

  it('reads stored calls in either shape and skips nameless ones', () => {
    const t = { id: 1, household_id: 'h', user_message: '', assistant_message: null, created_at: '', tool_calls: [
      { function: { name: 'a', arguments: '{"x": 1}' } }, { name: 'b', arguments: { y: 2 } }, { function: { name: 'c', arguments: 'not json' } }, { foo: 1 },
    ] }
    expect(toolCalls(t)).toEqual([{ name: 'a', arguments: { x: 1 } }, { name: 'b', arguments: { y: 2 } }, { name: 'c', arguments: { arguments: 'not json' } }])
  })
})
