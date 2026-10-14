import { beforeEach, describe, expect, it } from 'vitest'
import type { ChatEvent } from '@/api/chat'
import { applyEvent, chatKey, emptyChat, endTurn, loadChat, optionLabel, saveChat, splitThink } from './chatState'
import type { ChatState } from './chatState'

const turn = (): ChatState => ({
  nodeId: 'n1',
  conversationId: '',
  messages: [
    { id: 'u', role: 'user', text: 'hi' },
    { id: 'a', role: 'assistant', text: '', pending: true },
  ],
})
const fold = (events: ChatEvent[], s = turn()) => events.reduce(applyEvent, s)
const last = (s: ChatState) => s.messages[s.messages.length - 1]

describe('applyEvent: the chat stream reducer (cc/13 §3.2)', () => {
  it('shows status and the acknowledgment until text arrives', () => {
    let s = fold([{ type: 'status', message: 'Starting conversation...' }])
    expect(last(s).status).toBe('Starting conversation...')
    s = fold([{ type: 'acknowledgment', text: 'On it.' }], s)
    expect(last(s).status).toBe('On it.')
    expect(last(s).text).toBe('')
  })

  it('appends deltas, then done.full_text replaces the bubble and keeps the conversation', () => {
    const s = fold([
      { type: 'delta', text: 'Let me check… ' },
      { type: 'status', message: 'Running command on node...' },
      { type: 'delta', text: 'It is ' },
      { type: 'delta', text: 'sunny.' },
    ])
    expect(last(s).text).toBe('Let me check… It is sunny.')
    expect(last(s).pending).toBe(true)
    const d = applyEvent(s, { type: 'done', full_text: 'It is sunny.', conversation_id: 'c1', stop_reason: 'complete' })
    expect(last(d)).toMatchObject({ text: 'It is sunny.', pending: false })
    expect(d.conversationId).toBe('c1')
  })

  it('keeps reasoning, actions and validation from done', () => {
    const d = fold([
      {
        type: 'done',
        full_text: 'Type it?',
        conversation_id: 'c',
        stop_reason: 'complete',
        reasoning: 'user wants typing',
        actions: [{ button_text: 'Confirm', button_action: 'confirm', button_type: 'primary' }],
        action_context: { command_name: 'desktop_type_text', context: { text: 'hello' } },
      },
    ])
    expect(last(d).reasoning).toBe('user wants typing')
    expect(last(d).actions?.[0].button_action).toBe('confirm')
    expect(last(d).actionContext?.command_name).toBe('desktop_type_text')
    const v = fold([
      {
        type: 'done',
        full_text: 'Which room?',
        stop_reason: 'validation',
        validation: { question: 'Which room?', parameter_name: 'room', options: ['Kitchen', 'Office'] },
      },
    ])
    expect(last(v).validation?.options).toEqual(['Kitchen', 'Office'])
  })

  it('moves an inline <think> block to reasoning', () => {
    const d = fold([{ type: 'done', full_text: '<think>hmm</think>\nHello!' }])
    expect(last(d)).toMatchObject({ text: 'Hello!', reasoning: 'hmm' })
    expect(splitThink('no think')).toEqual({ text: 'no think', reasoning: '' })
    expect(splitThink('<think>unclosed')).toEqual({ text: '', reasoning: 'unclosed' })
  })

  it('shows an error and keeps its conversation id', () => {
    const e = fold([{ type: 'delta', text: 'partial' }, { type: 'error', message: 'model failed', conversation_id: 'c9' }])
    expect(last(e)).toMatchObject({ text: 'model failed', error: true, pending: false })
    expect(e.conversationId).toBe('c9')
  })

  it('ignores events once the turn is over', () => {
    const d = fold([{ type: 'done', full_text: 'ok' }])
    expect(applyEvent(d, { type: 'delta', text: 'late' })).toBe(d)
  })
})

describe('endTurn', () => {
  it('closes a stream that ended without done', () => {
    const s = endTurn(fold([{ type: 'delta', text: 'half an answ' }]))
    expect(last(s)).toMatchObject({ text: 'half an answ', pending: false })
    expect(last(endTurn(turn()))).toMatchObject({ pending: false, error: true })
    expect(last(endTurn(turn(), 'Stopped.')).text).toBe('Stopped.')
  })
})

describe('optionLabel', () => {
  it('reads strings, numbers and labelled objects', () => {
    expect(optionLabel('Kitchen')).toBe('Kitchen')
    expect(optionLabel(3)).toBe('3')
    expect(optionLabel({ label: 'Office', value: 'office' })).toBe('Office')
    expect(optionLabel({ value: 'v' })).toBe('v')
  })
})

describe('per-tab storage (§3.2)', () => {
  beforeEach(() => sessionStorage.clear())
  it('round-trips per user and household, and closes a turn cut off by a reload', () => {
    const key = chatKey(7, 'h1')
    expect(key).toBe('jarvis-admin:chat:7:h1')
    saveChat(key, turn())
    const back = loadChat(key)
    expect(back?.messages).toHaveLength(2)
    expect(last(back as ChatState).pending).toBe(false)
    expect(loadChat(chatKey(8, 'h1'))).toBeNull()
    sessionStorage.setItem(key, '{broken')
    expect(loadChat(key)).toBeNull()
    expect(emptyChat('n')).toEqual({ nodeId: 'n', conversationId: '', messages: [] })
  })
})
