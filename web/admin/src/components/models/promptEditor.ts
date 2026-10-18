import { autocompletion, type Completion, type CompletionContext, type CompletionResult } from '@codemirror/autocomplete'
import { RangeSetBuilder, type Extension } from '@codemirror/state'
import { Decoration, EditorView, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view'
import type { PromptVariable } from '@/api/prompts'

/**
 * CodeMirror extensions for the voice prompt editor (docs/cc/prompt-contract.md): `{{`
 * completes the variables, an unknown `{{name}}` is underlined, and the theme follows the
 * admin's CSS variables (light and dark).
 */

/** The class an unknown variable gets. */
export const unknownVariableClass = 'underline decoration-wavy decoration-red-500'

// A {{name}} on one line; the name is trimmed, as the server does.
const tokenRe = /\{\{([^{}\n]*)\}\}/g

function variableCompletions(variables: PromptVariable[]) {
  const options: Completion[] = variables.map((v) => ({
    label: v.name,
    detail: v.description,
    type: 'variable',
    apply: (view, _completion, from, to) => {
      // Close the variable unless the text already does.
      const close = view.state.sliceDoc(to, to + 2) === '}}' ? '' : '}}'
      const insert = v.name + close
      view.dispatch({ changes: { from, to, insert }, selection: { anchor: from + insert.length } })
    },
  }))
  return (ctx: CompletionContext): CompletionResult | null => {
    const m = ctx.matchBefore(/\{\{\s*[a-z0-9_]*$/)
    if (!m) return null
    const name = /[a-z0-9_]*$/.exec(m.text)?.[0] ?? ''
    return { from: m.to - name.length, options, validFor: /^[a-z0-9_]*$/ }
  }
}

function unknownVariables(known: Set<string>) {
  const mark = Decoration.mark({ class: unknownVariableClass })
  const build = (view: EditorView): DecorationSet => {
    const b = new RangeSetBuilder<Decoration>()
    for (const { from, to } of view.visibleRanges) {
      const text = view.state.sliceDoc(from, to)
      for (const m of text.matchAll(tokenRe)) {
        if (!known.has(m[1].trim())) b.add(from + m.index, from + m.index + m[0].length, mark)
      }
    }
    return b.finish()
  }
  return ViewPlugin.fromClass(
    class {
      decorations: DecorationSet
      constructor(view: EditorView) {
        this.decorations = build(view)
      }
      update(u: ViewUpdate) {
        if (u.docChanged || u.viewportChanged) this.decorations = build(u.view)
      }
    },
    { decorations: (p) => p.decorations },
  )
}

/** promptVariables is the completion and the underline for one variable list. */
export function promptVariables(variables: PromptVariable[]): Extension {
  return [
    autocompletion({ override: [variableCompletions(variables)], icons: false }),
    unknownVariables(new Set(variables.map((v) => v.name))),
  ]
}

export const promptTheme = EditorView.theme({
  '&': {
    backgroundColor: 'var(--color-surface-alt)',
    color: 'var(--color-text)',
    border: '1px solid var(--color-border)',
    borderRadius: '0.5rem',
    fontSize: '0.8125rem',
  },
  '&.cm-focused': { outline: '2px solid var(--color-primary)' },
  '.cm-scroller': {
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
    maxHeight: '28rem',
    minHeight: '10rem',
    overflow: 'auto',
  },
  '.cm-content': { caretColor: 'var(--color-text)', padding: '0.5rem 0' },
  '.cm-line': { padding: '0 0.75rem' },
  '&.cm-focused .cm-cursor': { borderLeftColor: 'var(--color-text)' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': {
    backgroundColor: 'color-mix(in srgb, var(--color-primary) 25%, transparent)',
  },
  '.cm-tooltip': {
    backgroundColor: 'var(--color-surface)',
    color: 'var(--color-text)',
    border: '1px solid var(--color-border)',
    borderRadius: '0.5rem',
    overflow: 'hidden',
  },
  '.cm-tooltip.cm-tooltip-autocomplete > ul': { fontFamily: 'inherit', maxHeight: '16rem' },
  '.cm-tooltip.cm-tooltip-autocomplete > ul > li': { padding: '0.25rem 0.5rem' },
  '.cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]': {
    backgroundColor: 'var(--color-primary)',
    color: '#fff',
  },
  '.cm-completionLabel': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' },
  '.cm-completionDetail': { marginLeft: '0.75rem', fontStyle: 'normal', opacity: '0.75' },
})
