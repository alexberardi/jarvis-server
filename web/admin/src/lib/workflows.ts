import { parseServerTime } from '@/lib/time'
import type { ParamKind, SuccessCheck, TaughtWorkflow, TraceEvent, WStep, WorkflowPut } from '@/api/workflows'

/** A piece of a sentence; bold is what the step acts on ("Click **Download CSV**"). */
export interface Part {
  text: string
  bold?: boolean
}

function host(url: string | undefined): string {
  if (!url) return ''
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

/** stepSentence is a workflow step as a short sentence: "Open bank.example.com", "Type {month}". */
export function stepSentence(s: WStep): Part[] {
  const a = s.action ?? { kind: '', intent: '' }
  const name = s.element?.name?.trim()
  switch (a.kind) {
    case 'navigate':
      return [{ text: `Open ${host(a.url) || s.site || a.url || ''}` }]
    case 'click':
    case 'double_click':
    case 'triple_click':
    case 'press':
      if (name) return [{ text: a.kind === 'double_click' ? 'Double-click ' : 'Click ' }, { text: name, bold: true }]
      break
    case 'type':
    case 'set_value':
      if (a.text) return [{ text: `Type ${a.text}` }]
      break
    case 'select':
      if (a.text) return [{ text: 'Choose ' }, { text: a.text, bold: true }]
      break
    case 'keypress':
      if (a.keys?.length) return [{ text: `Press ${a.keys.join('+')}` }]
      break
    case 'scroll':
      return [{ text: (a.dy ?? 0) < 0 ? 'Scroll up' : 'Scroll down' }]
    case 'wait':
      return [{ text: `Wait ${Math.round((a.ms ?? 0) / 100) / 10} s` }]
  }
  return [{ text: s.intent || a.intent }]
}

/** eventSentence is one recorded event as a sentence ("Clicked **Download CSV**"). */
export function eventSentence(e: TraceEvent): Part[] {
  const name = e.element?.name?.trim()
  switch (e.kind) {
    case 'navigate':
      return [{ text: `Opened ${host(e.url)}` }]
    case 'click':
      return name ? [{ text: 'Clicked ' }, { text: name, bold: true }] : [{ text: 'Clicked' }]
    case 'input':
    case 'change': {
      const what = e.masked ? '(hidden)' : `"${e.value ?? ''}"`
      return name ? [{ text: `Typed ${what} in ` }, { text: name, bold: true }] : [{ text: `Typed ${what}` }]
    }
    case 'submit':
      return [{ text: 'Submitted the form' }]
    case 'key':
      return name ? [{ text: `Pressed ${e.key ?? 'a key'} in ` }, { text: name, bold: true }] : [{ text: `Pressed ${e.key ?? 'a key'}` }]
    case 'cannot_watch':
      return [{ text: `Couldn't watch ${host(e.url)}` }]
    case 'download':
      return [{ text: `Downloaded ${e.filename ?? 'a file'}` }]
  }
  return [{ text: e.kind ?? 'Something happened' }]
}

/** traceEvents is a recording's trace as a list of events (empty when it isn't one). */
export function traceEvents(trace: unknown): TraceEvent[] {
  return Array.isArray(trace) ? trace.filter((e): e is TraceEvent => !!e && typeof e === 'object') : []
}

/** How soon after a click, submit or key a navigation in its tab is that action's doing (ms). */
const NAV_FOLD_MS = 1500

/**
 * foldTrace keeps a navigation only when the user opened the page themselves: one an action in
 * its tab caused becomes the action's result_url, and one to the URL its tab is already on is
 * dropped. Events go in the order of their times (a navigation can arrive before the click that
 * caused it); a trace without times folds a navigation right after the action. The same rule as
 * the learner's (foldNavigations in workflows_learn.go).
 */
export function foldTrace(evs: TraceEvent[]): TraceEvent[] {
  if (evs.every((e) => e.ts)) evs = [...evs].sort((a, b) => (a.ts ?? 0) - (b.ts ?? 0))
  const out: TraceEvent[] = []
  const at = new Map<number, string | undefined>() // the URL each tab is on
  const action = new Map<number, number>() // each tab's last click, submit or key: its index in out
  for (const e of evs) {
    const tab = e.tab ?? 0
    if (e.kind !== 'navigate') {
      if (e.kind === 'click' || e.kind === 'submit' || e.kind === 'key') action.set(tab, out.length)
      if (e.kind !== 'download' && e.url) at.set(tab, e.url)
      out.push(e)
      continue
    }
    if (at.get(tab) === e.url) continue
    at.set(tab, e.url)
    const i = action.get(tab)
    if (i !== undefined) {
      const a = out[i]
      if (a.ts && e.ts ? e.ts - a.ts <= NAV_FOLD_MS : i === out.length - 1) {
        out[i] = { ...a, result_url: e.url }
        continue
      }
    }
    out.push(e)
  }
  return out
}

/** RISKS are computeruse's risk categories, in plain words: a step with one asks first. */
export const RISKS: { value: string; label: string }[] = [
  { value: 'submit_third_party', label: 'Sends a form to someone' },
  { value: 'pay', label: 'Pays' },
  { value: 'send', label: 'Sends a message' },
  { value: 'delete', label: 'Deletes something' },
  { value: 'sharing', label: 'Shares something' },
  { value: 'install', label: 'Installs something' },
  { value: 'subscribe', label: 'Subscribes' },
  { value: 'unsubscribe', label: 'Unsubscribes' },
  { value: 'security_settings', label: 'Changes security settings' },
  { value: 'captcha', label: 'Solves a captcha' },
  { value: 'sensitive_input', label: 'Enters sensitive information' },
]

export function riskLabel(risk: string | undefined): string {
  return RISKS.find((r) => r.value === risk)?.label ?? risk ?? ''
}

/** A step while it is edited: uid keeps its identity through reordering. */
export interface DraftStep {
  uid: string
  step: WStep
  /** The step was risky when loaded: it always asks first. */
  lockedRisk: boolean
}

/**
 * A value that may change each run ("Which of these change each time?"): one of the workflow's
 * params (from.param, its saved name), or a literal a step types (from.step, that step's uid).
 * on means it is a param when saved, under name.
 */
export interface Candidate {
  key: string
  name: string
  kind: ParamKind
  example: string
  description?: string
  on: boolean
  from: { param: string } | { step: string }
}

const TYPED = new Set(['type', 'set_value', 'select'])

function guessKind(v: string): ParamKind {
  if (/^\d{4}-\d{2}$/.test(v)) return 'month'
  if (/^\d{4}-\d{2}-\d{2}$/.test(v)) return 'date'
  if (/^-?\d+(\.\d+)?$/.test(v)) return 'number'
  return 'text'
}

function slug(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
}

export function draftSteps(steps: WStep[]): DraftStep[] {
  return steps.map((step, i) => ({ uid: `s${i}`, step, lockedRisk: !!step.risk }))
}

/** candidatesOf is the workflow's params (on) and the literal values its steps type (off). */
export function candidatesOf(params: TaughtWorkflow['params'], steps: DraftStep[]): Candidate[] {
  const out: Candidate[] = (params ?? []).map((p) => ({
    key: `p:${p.name}`,
    name: p.name,
    kind: p.kind,
    example: p.example ?? '',
    description: p.description,
    on: true,
    from: { param: p.name },
  }))
  const taken = new Set(out.map((c) => c.name))
  for (const d of steps) {
    const text = d.step.action?.text
    if (!TYPED.has(d.step.action?.kind) || !text || text.includes('{')) continue
    let name = slug(d.step.element?.name ?? '') || 'value'
    for (let n = 2; taken.has(name); n++) name = `${slug(d.step.element?.name ?? '') || 'value'}_${n}`
    taken.add(name)
    out.push({ key: `s:${d.uid}`, name, kind: guessKind(text), example: text, on: false, from: { step: d.uid } })
  }
  return out
}

/** A param name steps can say as {name}. */
export const PARAM_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/

/** paramError is why the candidates that are on can't be saved, or ''. */
export function paramError(cands: Candidate[]): string {
  const seen = new Set<string>()
  for (const c of cands) {
    if (!c.on) continue
    if (!PARAM_NAME.test(c.name)) return `"${c.name}" can't be a name: use letters, digits and _.`
    if (seen.has(c.name)) return `Two values are named "${c.name}".`
    seen.add(c.name)
  }
  return ''
}

const swap = (s: string | undefined, from: string, to: string) => (s === undefined ? s : s.split(from).join(to))

/**
 * applyParams is the steps as they will be saved: a param that is on and renamed reads {new} in
 * place of {old}; one that is off goes back to its example; a typed value that is on reads {name}.
 */
export function applyParams(steps: DraftStep[], cands: Candidate[]): DraftStep[] {
  return steps.map((d) => {
    let intent = d.step.intent
    const action = { ...d.step.action }
    for (const c of cands) {
      if ('param' in c.from) {
        const to = c.on ? `{${c.name}}` : c.example
        const from = `{${c.from.param}}`
        if (to === from) continue
        intent = swap(intent, from, to)!
        action.intent = swap(action.intent, from, to)!
        action.text = swap(action.text, from, to)
      } else if (c.on && c.from.step === d.uid && c.example) {
        intent = swap(intent, c.example, `{${c.name}}`)!
        action.intent = swap(action.intent, c.example, `{${c.name}}`)!
        action.text = `{${c.name}}`
      }
    }
    return { ...d, step: { ...d.step, intent, action } }
  })
}

/** successParts is the success check as clauses of "I'll know it worked when …". */
export function successParts(check: SuccessCheck): string[] {
  const c = cleanSuccess(check)
  const out: string[] = []
  if (c.download?.name_like) out.push(`a file named like ${c.download.name_like} downloads`)
  if (c.url_contains) out.push(`the address contains ${c.url_contains}`)
  for (const t of c.text_present ?? []) out.push(`the page shows "${t}"`)
  if (c.file?.path) out.push(`${c.file.path} exists${c.file.contains?.length ? ` and contains ${c.file.contains.join(', ')}` : ''}`)
  if (c.screen) out.push(`the screen answers yes to: ${c.screen}`)
  return out
}

/** cleanSuccess drops empty fields, so cc sees only the checks that are set. */
export function cleanSuccess(c: SuccessCheck): SuccessCheck {
  const out: SuccessCheck = {}
  if (c.url_contains?.trim()) out.url_contains = c.url_contains.trim()
  const texts = (c.text_present ?? []).map((t) => t.trim()).filter(Boolean)
  if (texts.length) out.text_present = texts
  if (c.download?.name_like?.trim()) out.download = { ...c.download, name_like: c.download.name_like.trim() }
  if (c.file?.path) out.file = c.file
  if (c.screen?.trim()) out.screen = c.screen.trim()
  return out
}

/** toPut is the save payload: exactly the fields PUT takes. */
export function toPut(d: {
  name: string
  triggers: string[] | null
  steps: DraftStep[]
  cands: Candidate[]
  success: SuccessCheck
  shared: boolean
}): WorkflowPut {
  const live = d.cands.filter((c) => !('step' in c.from) || d.steps.some((s) => s.uid === (c.from as { step: string }).step))
  return {
    name: d.name.trim(),
    triggers: d.triggers ?? [],
    params: live
      .filter((c) => c.on)
      .map((c) => ({
        name: c.name,
        kind: c.kind,
        ...(c.example ? { example: c.example } : {}),
        ...(c.description ? { description: c.description } : {}),
      })),
    steps: applyParams(d.steps, live).map(({ step }) => {
      const { risk, ...rest } = step
      return risk ? { ...rest, risk } : rest
    }),
    success: cleanSuccess(d.success),
    shared: d.shared,
  }
}

const DAY = 86_400_000

/** expiresIn is when a recording is deleted, in words: "in 12 days", "today". */
export function expiresIn(iso: string, now = Date.now()): string {
  const t = parseServerTime(iso)
  if (!t) return iso
  const days = Math.floor((t.getTime() - now) / DAY)
  return days <= 0 ? 'today' : days === 1 ? 'tomorrow' : `in ${days} days`
}

