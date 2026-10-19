import { describe, expect, it } from 'vitest'
import type { WStep } from '@/api/workflows'
import { candidatesOf, draftSteps, eventSentence, stepSentence, traceEvents } from './workflows'

const step = (action: WStep['action'], element?: WStep['element']): WStep => ({ intent: action.intent, target: 'browser', action, element })

describe('stepSentence', () => {
  it('reads each action kind', () => {
    const text = (s: WStep) => stepSentence(s).map((p) => p.text).join('')
    expect(text(step({ kind: 'navigate', intent: 'x', url: 'https://bank.example.com/a?b' }))).toBe('Open bank.example.com')
    expect(text(step({ kind: 'keypress', intent: 'x', keys: ['ctrl', 's'] }))).toBe('Press ctrl+s')
    expect(text(step({ kind: 'wait', intent: 'x', ms: 1500 }))).toBe('Wait 1.5 s')
    expect(text(step({ kind: 'scroll', intent: 'x', dy: -3 }))).toBe('Scroll up')
    // Without an element name a click is its intent.
    expect(text(step({ kind: 'click', intent: 'Click the blue button', x: 1, y: 2 }))).toBe('Click the blue button')
    expect(text(step({ kind: 'screenshot', intent: 'Open the statements page' }))).toBe('Open the statements page')
  })
})

describe('eventSentence', () => {
  it('never shows a masked value', () => {
    expect(eventSentence({ kind: 'change', masked: true, value: 'hunter2' }).map((p) => p.text).join('')).toBe('Typed (hidden)')
    expect(eventSentence({ kind: 'download', filename: 'statement.csv' })[0].text).toBe('Downloaded statement.csv')
  })
  it('reads a trace that is not a list as empty', () => {
    expect(traceEvents({ events: [] })).toEqual([])
    expect(traceEvents([{ kind: 'click' }, null, 3])).toEqual([{ kind: 'click' }])
  })
})

describe('candidatesOf', () => {
  it('offers typed literals (off, kind guessed, unique names) after the params', () => {
    const steps = draftSteps([
      step({ kind: 'type', intent: 'a', text: '2026-09-01' }, { name: 'From date' }),
      step({ kind: 'type', intent: 'b', text: '42' }, { name: 'From date' }),
      step({ kind: 'type', intent: 'c', text: '{month}' }),
      step({ kind: 'set_value', intent: 'd', text: 'hello', ref: 'e1' }),
    ])
    const c = candidatesOf([{ name: 'month', kind: 'month', example: '2026-09' }], steps)
    expect(c.map((x) => [x.name, x.kind, x.on])).toEqual([
      ['month', 'month', true],
      ['from_date', 'date', false],
      ['from_date_2', 'number', false],
      ['value', 'text', false],
    ])
  })
})
