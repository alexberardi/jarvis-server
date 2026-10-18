/// <reference types="node" />
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import { createRoutesFromChildren, Route, type RouteObject } from 'react-router-dom'
import { appRoutes } from '@/routes'

// The coverage gate (docs/decisions/2026-10-18-e2e-coverage-gate.md): every path of the real
// route tree has an entry in e2e/coverage.json, naming the E2E specs that cover it or why it
// has none, and every entry is a real path. A new page can't land without one.

// Outside the SPA's root, so read from disk (Vite won't serve it); vitest runs in web/admin.
const root = path.resolve(process.cwd(), '../..')
const coverage = JSON.parse(readFileSync(path.join(root, 'e2e/coverage.json'), 'utf8')) as {
  web: Record<string, { tests?: string[]; manual?: string; pending?: string }>
}
const isSpec = (t: string) => t.startsWith('e2e/web/tests/') && t.endsWith('.spec.ts') && existsSync(path.join(root, t))

/** routePaths lists the full path of every route that renders something: leaves and index routes. */
function routePaths(routes: RouteObject[], parent = ''): string[] {
  const out: string[] = []
  for (const r of routes) {
    let full = parent
    if (r.path) full = r.path.startsWith('/') ? r.path : `${parent.replace(/\/$/, '')}/${r.path}`
    if (r.children?.length) out.push(...routePaths(r.children, full))
    else out.push(full || '/')
  }
  return out
}

const paths = routePaths(createRoutesFromChildren(appRoutes()))

describe('routePaths', () => {
  it('joins nested paths, takes absolute ones as they are and gives an index its parent', () => {
    const tree = createRoutesFromChildren(
      <>
        <Route path="/a" />
        <Route>
          <Route path="/b" element={null}>
            <Route index />
            <Route path="c/:id" />
            <Route path="*" />
          </Route>
        </Route>
      </>,
    )
    expect(routePaths(tree)).toEqual(['/a', '/b', '/b/c/:id', '/b/*'])
  })
})

describe('e2e/coverage.json', () => {
  it('reads the real route tree', () => {
    expect(paths).toContain('/admin/traces/:id')
    expect(paths).toContain('/setup')
    expect(new Set(paths).size).toBe(paths.length)
  })

  it('has an entry for every route', () => {
    const missing = paths.filter((p) => !(p in coverage.web))
    expect(missing, 'routes with no entry in e2e/coverage.json (add the specs covering them, or a "manual" reason)').toEqual([])
  })

  it('has no entry for a route that does not exist', () => {
    const stale = Object.keys(coverage.web).filter((p) => !paths.includes(p))
    expect(stale, 'entries in e2e/coverage.json for routes that are gone').toEqual([])
  })

  it('gives each entry exactly one of tests, manual or pending', () => {
    const bad = Object.entries(coverage.web)
      .filter(([, e]) => {
        const kinds = [e.tests?.length ? 1 : 0, e.manual ? 1 : 0, e.pending ? 1 : 0]
        return kinds.reduce((a, b) => a + b, 0) !== 1 || Object.keys(e).length !== 1
      })
      .map(([p]) => p)
    expect(bad).toEqual([])
  })

  it('names spec files that exist', () => {
    const gone = Object.entries(coverage.web).flatMap(([p, e]) =>
      (e.tests ?? []).filter((t) => !isSpec(t)).map((t) => `${p}: ${t}`),
    )
    expect(gone).toEqual([])
  })
})
