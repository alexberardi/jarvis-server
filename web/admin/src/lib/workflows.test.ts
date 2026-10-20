import { describe, expect, it } from 'vitest'
import type { WStep } from '@/api/workflows'
import { candidatesOf, draftSteps, eventSentence, foldTrace, stepSentence, traceEvents } from './workflows'

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
  it('names the key and the field it was pressed in', () => {
    const text = (e: Parameters<typeof eventSentence>[0]) => eventSentence(e).map((p) => p.text).join('')
    expect(text({ kind: 'key', key: 'Enter', element: { role: 'textbox', name: 'Message' } })).toBe('Pressed Enter in Message')
    expect(eventSentence({ kind: 'key', key: 'Enter', element: { name: 'Message' } })[1]).toEqual({ text: 'Message', bold: true })
    expect(text({ kind: 'key', key: 'Escape' })).toBe('Pressed Escape')
  })
  it('says which page it could not watch', () => {
    const text = eventSentence({ kind: 'cannot_watch', url: 'https://bank.example.com/login' }).map((p) => p.text).join('')
    expect(text).toBe("Couldn't watch bank.example.com")
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

describe('foldTrace', () => {
  const u = 'http://localhost:7710'
  const sentences = (evs: ReturnType<typeof traceEvents>) => foldTrace(evs).map((e) => eventSentence(e).map((p) => p.text).join(''))
  it('folds the navigation a click caused into the click', () => {
    const evs = [
      { seq: 1, ts: 1000, tab: 4, kind: 'navigate', url: `${u}/` },
      { seq: 2, ts: 5000, tab: 4, kind: 'click', url: `${u}/`, element: { name: 'Add-ons' } },
      { seq: 3, ts: 5300, tab: 4, kind: 'navigate', url: `${u}/addons` },
      // reported before the click it came from
      { seq: 4, ts: 9100, tab: 4, kind: 'navigate', url: `${u}/chat` },
      { seq: 5, ts: 9000, tab: 4, kind: 'click', url: `${u}/addons`, element: { name: 'Chat' } },
      { seq: 6, ts: 9200, tab: 4, kind: 'navigate', url: `${u}/chat` },
      // too late: the user opened it
      { seq: 7, ts: 20000, tab: 4, kind: 'navigate', url: `${u}/settings` },
    ]
    expect(sentences(evs)).toEqual(['Opened localhost:7710', 'Clicked Add-ons', 'Clicked Chat', 'Opened localhost:7710'])
    expect(foldTrace(evs)[1].result_url).toBe(`${u}/addons`)
    expect(foldTrace(evs)[2].result_url).toBe(`${u}/chat`)
  })
  it('folds a navigation that came in before the Enter that caused it', () => {
    // Headless Firefox (lane-events.jsonl)
    const evs = [
      { seq: 6, ts: 67775, tab: 1, kind: 'click', url: `${u}/chat`, element: { name: 'Message' } },
      { seq: 7, ts: 68141, tab: 1, kind: 'navigate', url: `${u}/chat?sent=1` },
      { seq: 8, ts: 68136, tab: 1, kind: 'input', url: `${u}/chat`, element: { name: 'Message' }, value: 'what time is it?' },
      { seq: 9, ts: 68136, tab: 1, kind: 'key', key: 'Enter', url: `${u}/chat`, element: { name: 'Message' } },
      { seq: 10, ts: 68138, tab: 1, kind: 'submit', url: `${u}/chat`, element: { name: 'f' } },
    ]
    expect(sentences(evs)).toEqual(['Clicked Message', 'Typed "what time is it?" in Message', 'Pressed Enter in Message', 'Submitted the form'])
    expect(foldTrace(evs)[3].result_url).toBe(`${u}/chat?sent=1`)
  })
  it('drops a navigation to the page the tab is on', () => {
    const evs = Array.from({ length: 12 }, (_, i) => ({ seq: i + 1, ts: i * 3000, tab: 4, kind: 'navigate', url: `${u}/` }))
    expect(sentences([{ seq: 0, ts: 0, tab: 3, kind: 'navigate', url: 'http://127.0.0.1:7790/' }, ...evs])).toEqual([
      'Opened 127.0.0.1:7790',
      'Opened localhost:7710',
    ])
  })
  it('folds a navigation right after an action in an old trace without times', () => {
    const evs = [
      { kind: 'click', url: `${u}/`, element: { name: 'Chat' } },
      { kind: 'navigate', url: `${u}/chat` },
      { kind: 'change', url: `${u}/chat`, element: { name: 'Message' }, value: 'hi' },
      { kind: 'navigate', url: `${u}/` },
    ]
    expect(sentences(evs)).toEqual(['Clicked Chat', 'Typed "hi" in Message', 'Opened localhost:7710'])
  })
})
