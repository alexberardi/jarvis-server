import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { TraceCards, type ConversationGroup } from './TracesPage'
import type { TraceListItem } from '@/api/traces'

function trace(id: string, over: Partial<TraceListItem> = {}): TraceListItem {
  return {
    id,
    conversation_id: 'conv-1',
    request_type: 'voice',
    source: 'node',
    node_id: 'node-1',
    household_id: 'hh-1',
    user_command: 'what time is it',
    assistant_message: 'It is noon.',
    status: 'ok',
    total_duration_ms: 420,
    span_count: 3,
    created_at: '2026-10-12T12:00:00Z',
    ...over,
  }
}

const group: ConversationGroup = {
  conversation_id: 'conv-1',
  traces: [trace('t1'), trace('t2', { request_type: 'warmup', created_at: '2026-10-12T11:59:00Z' })],
  newest_at: '2026-10-12T12:00:00Z',
  user_command: 'what time is it',
  assistant_message: 'It is noon.',
  source: 'node',
  node_id: 'node-1',
  household_id: 'hh-1',
  total_duration_ms: 840,
  status: 'ok',
}

function renderCards(expanded: Set<string>, onToggle = vi.fn(), onOpen = vi.fn()) {
  render(
    <TraceCards
      groups={[group]}
      expanded={expanded}
      onToggle={onToggle}
      onOpen={onOpen}
      nodeLabel={(id) => (id === 'node-1' ? 'kitchen' : '-')}
      householdName={() => 'Home'}
    />,
  )
  return { onToggle, onOpen }
}

describe('TraceCards (the traces table below lg)', () => {
  it('shows each conversation as a card that is hidden from lg up', () => {
    renderCards(new Set())
    const list = screen.getByTestId('trace-cards')
    expect(list.className).toMatch(/\blg:hidden\b/)
    const card = within(list).getByRole('button', { expanded: false })
    expect(card).toHaveTextContent('what time is it')
    expect(card).toHaveTextContent('It is noon.')
    expect(card).toHaveTextContent('kitchen · Home')
    expect(card).toHaveTextContent('840ms')
  })

  it('toggles a conversation open', () => {
    const { onToggle } = renderCards(new Set())
    fireEvent.click(screen.getByRole('button', { expanded: false }))
    expect(onToggle).toHaveBeenCalledWith('conv-1')
  })

  it('lists an open conversation’s traces oldest first and opens one', () => {
    const { onOpen } = renderCards(new Set(['conv-1']))
    const rows = screen.getAllByRole('button').filter((b) => b.getAttribute('aria-expanded') === null)
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('warmup')
    fireEvent.click(rows[1])
    expect(onOpen).toHaveBeenCalledWith('t1')
  })

  it('says so when there are no traces', () => {
    render(
      <TraceCards groups={[]} expanded={new Set()} onToggle={vi.fn()} onOpen={vi.fn()} nodeLabel={() => '-'} householdName={() => ''} />,
    )
    expect(screen.getByText('No traces yet')).toBeInTheDocument()
  })
})
