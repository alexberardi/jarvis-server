/// <reference types="node" />
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import { colors, contrast, type ColorToken } from './colors'

// The tokens live in three places: tokens.py (the decision's source), index.css (what the app
// draws) and colors.ts (JS). These keep them equal and AA, so a later edit can't drift or fail
// contrast quietly. vitest runs in web/admin.
const root = path.resolve(process.cwd(), '../..')
const css = readFileSync(path.join(process.cwd(), 'src/index.css'), 'utf8')
const py = readFileSync(path.join(root, 'docs/design/directions/tokens.py'), 'utf8')

const themes = ['light', 'dark'] as const
const pyName: Record<string, ColorToken> = { bg: 'background', muted: 'text-muted' }

/** pyTheme reads DIRECTIONS["B"][theme] from tokens.py. */
function pyTheme(theme: string): Record<string, string> {
  const b = py.slice(py.indexOf('"B": {'), py.indexOf('"C": {'))
  const block = b.slice(b.indexOf(`"${theme}": {`)).split('}')[0]
  const out: Record<string, string> = {}
  for (const [, k, v] of block.matchAll(/"([a-z-]+)": "(#[0-9a-fA-F]{6})"/g)) out[pyName[k] ?? k] = v.toLowerCase()
  return out
}

/** cssTheme reads the --color-* values of index.css's @theme (light) or .dark block. */
function cssTheme(theme: string): Record<string, string> {
  const start = theme === 'light' ? css.indexOf('@theme static {') : css.indexOf('\n.dark {')
  const block = css.slice(start, css.indexOf('\n}', start))
  const out: Record<string, string> = {}
  for (const [, k, v] of block.matchAll(/--color-([a-z-]+): (#[0-9a-fA-F]{6});/g)) out[k] = v.toLowerCase()
  return out
}

const lower = (o: Record<string, string>) => Object.fromEntries(Object.entries(o).map(([k, v]) => [k, v.toLowerCase()]))

// tokens.py's PAIRS (label, fg, bg, minimum), plus the pairs the components add.
const pairs: [string, ColorToken, ColorToken, number][] = [
  ['text on background', 'text', 'background', 4.5],
  ['text on surface', 'text', 'surface', 4.5],
  ['text on surface-alt', 'text', 'surface-alt', 4.5],
  ['muted on background', 'text-muted', 'background', 4.5],
  ['muted on surface', 'text-muted', 'surface', 4.5],
  ['muted on surface-alt', 'text-muted', 'surface-alt', 4.5],
  ['primary (link) on surface', 'primary', 'surface', 4.5],
  ['primary (link) on background', 'primary', 'background', 4.5],
  ['on-primary on primary (button)', 'on-primary', 'primary', 4.5],
  ['text on primary-soft (selection)', 'text', 'primary-soft', 4.5],
  ['success on surface', 'success', 'surface', 4.5],
  ['success on success-soft', 'success', 'success-soft', 4.5],
  ['warning on surface', 'warning', 'surface', 4.5],
  ['warning on warning-soft', 'warning', 'warning-soft', 4.5],
  ['danger on surface', 'danger', 'surface', 4.5],
  ['danger on danger-soft', 'danger', 'danger-soft', 4.5],
  ['border-strong on surface (input edge)', 'border-strong', 'surface', 3],
  ['border-strong on surface-alt', 'border-strong', 'surface-alt', 3],
  ['focus ring on background', 'focus', 'background', 3],
  ['focus ring on surface', 'focus', 'surface', 3],
  // Added by the components: the active nav item and primary badge, a danger button's label.
  ['primary on primary-soft (active nav item, badge)', 'primary', 'primary-soft', 4.5],
  ['on-primary on danger (danger button)', 'on-primary', 'danger', 4.5],
  ['muted on primary-soft', 'text-muted', 'primary-soft', 4.5],
]

describe.each(themes)('%s theme tokens', (theme) => {
  it('match tokens.py (direction B)', () => {
    expect(lower(colors[theme])).toEqual(pyTheme(theme))
  })

  it('match index.css', () => {
    const fromCss = cssTheme(theme)
    expect(Object.fromEntries(Object.keys(colors[theme]).map((k) => [k, fromCss[k]]))).toEqual(lower(colors[theme]))
  })

  it.each(pairs)('%s passes AA', (_label, fg, bg, min) => {
    expect(contrast(colors[theme][fg], colors[theme][bg])).toBeGreaterThanOrEqual(min)
  })
})

describe('contrast', () => {
  it('is 21 for black on white and 1 for a colour on itself', () => {
    expect(contrast('#000000', '#ffffff')).toBeCloseTo(21, 5)
    expect(contrast('#8A00C4', '#8A00C4')).toBe(1)
  })
})

describe('motion tokens', () => {
  it('keep every UI duration under 300ms', () => {
    const ms = [...css.matchAll(/--duration-[a-z-]+: (\d+)ms;/g)].map((m) => Number(m[1]))
    expect(ms.length).toBeGreaterThan(4)
    for (const d of ms) expect(d).toBeLessThan(300)
  })

  it('turn transforms off and slow spinners for prefers-reduced-motion', () => {
    const reduce = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce) {'))
    expect(reduce).toMatch(/--motion-pop-y: 0px/)
    expect(reduce).toMatch(/--motion-sheet-y: 0px/)
    expect(reduce).toMatch(/\.animate-spin \{\s*animation-duration: 1\.6s/)
  })
})
